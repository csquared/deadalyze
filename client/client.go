// Package client runs the bundle's engine and reads its events: the Go
// side of docs/engine-protocol.md, for the harnesses here and for any Go
// host (deadcatalog) once it cuts over. It knows how to find an installed
// bundle, how to start the engine in it, how to hand it a request, and how
// to deliver each event as it lands; it knows nothing about runners,
// flags or thresholds.
//
//	eng, err := client.Resolve()               // DEADCA7_BUNDLE, the installed runtime, beside the exe
//	info, err := eng.Describe(ctx)
//	err = eng.Analyze(ctx, req, func(ev client.Event) { ... })
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"syscall"
	"time"

	"github.com/cockroachdb/errors"
)

// Protocol is the version this client speaks.
const Protocol = 1

// Engine is a bundle with an engine in it.
type Engine struct {
	// Root is the bundle directory; Cmd the engine command, relative to it.
	Root string
	Cmd  []string
	// Version is the bundle's version from its manifest, "" without one.
	Version string
	// Env is added to the engine's environment (DEADCA7_ALGOS for a
	// checkout of the legs, DEADCA7_ENGINE_LOG=debug for diagnostics).
	Env []string
	// Stderr receives the engine's diagnostics; nil keeps the last of them
	// for error messages only.
	Stderr io.Writer
}

// ErrNotInstalled is what a machine with no bundle answers.
var ErrNotInstalled = errors.New("analysis bundle not installed: set DEADCA7_BUNDLE, or install the runtime (dc runtime install, or the app's Settings > Analysis)")

// Resolve finds the bundle to use: DEADCA7_BUNDLE, then DEADCATALOG_RUNTIME
// (what the hosts read today), then the installed runtime under the hosts'
// data directory, then a runtime beside the executable. A bundle without
// an engine (layout 1 or 2) is answered with ErrNoEngine unless
// DEADCA7_ENGINE names one to run in it.
func Resolve() (*Engine, error) {
	candidates := []string{os.Getenv("DEADCA7_BUNDLE"), os.Getenv("DEADCATALOG_RUNTIME")}
	if data := dataDir(); data != "" {
		candidates = append(candidates, filepath.Join(data, "runtime", "current"))
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "..", "runtime"))
	}
	for _, root := range candidates {
		if root == "" || !validRoot(root) {
			continue
		}
		return At(root)
	}
	return nil, ErrNotInstalled
}

// ErrNoEngine is a bundle the hosts can use but that carries no engine.
var ErrNoEngine = errors.New("this bundle has no engine (layout 1 or 2): set DEADCA7_ENGINE to an engine binary to run in it")

// At is the engine of the bundle at root.
func At(root string) (*Engine, error) {
	e := &Engine{Root: root}
	var manifest struct {
		Version   string `json:"version"`
		Engine    string `json:"engine"`
		Protocols []int  `json:"protocols"`
	}
	if b, err := os.ReadFile(filepath.Join(root, "manifest.json")); err == nil {
		_ = json.Unmarshal(b, &manifest)
	}
	e.Version = manifest.Version
	switch {
	case os.Getenv("DEADCA7_ENGINE") != "":
		e.Cmd = []string{os.Getenv("DEADCA7_ENGINE")}
	case manifest.Engine != "":
		e.Cmd = []string{filepath.Join(root, manifest.Engine)}
	default:
		if p := filepath.Join(root, "bin", "engine"); isExecutable(p) {
			e.Cmd = []string{p}
		} else {
			return nil, ErrNoEngine
		}
	}
	if len(manifest.Protocols) > 0 && !contains(manifest.Protocols, Protocol) {
		return nil, errors.Errorf("bundle %s speaks protocols %v, this client %d", root, manifest.Protocols, Protocol)
	}
	return e, nil
}

func contains(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func validRoot(root string) bool {
	for _, rel := range []string{filepath.Join("python", "bin", "python3"), "lib", filepath.Join("analysis", "lib")} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			return false
		}
	}
	return true
}

func isExecutable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
}

// dataDir is where the hosts install runtimes.
func dataDir() string {
	if p := os.Getenv("DEADCATALOG_DATA_DIR"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	switch goruntime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "deadcatalog")
	case "windows":
		if appData := os.Getenv("LOCALAPPDATA"); appData != "" {
			return filepath.Join(appData, "deadcatalog")
		}
		return filepath.Join(home, "AppData", "Local", "deadcatalog")
	default:
		return filepath.Join(home, ".local", "share", "deadcatalog")
	}
}

func (e *Engine) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, e.Cmd[0], append(append([]string{}, e.Cmd[1:]...), args...)...)
	cmd.Dir = e.Root
	cmd.Env = append(os.Environ(), e.Env...)
	// The engine is a process group of its own; a cancel is SIGTERM to it,
	// then SIGKILL, and the engine ends its legs.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

// Describe asks the engine what it is and can do.
func (e *Engine) Describe(ctx context.Context) (*Describe, error) {
	cmd := e.command(ctx, "describe")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, errors.Errorf("engine describe: %w: %s", err, tail(stderr.String()))
	}
	var d Describe
	if err := json.Unmarshal(out, &d); err != nil {
		return nil, errors.Errorf("engine describe: %w", err)
	}
	return &d, nil
}

// Analyze runs one batch and calls on for every event, in order, as it
// lands. It returns when the engine exits: nil after batch_done; an error
// naming the exit status and the stderr tail when the engine died first,
// after on has been given an item_error (engine_crashed) for every item
// without an item_done, as the protocol asks a host to do.
func (e *Engine) Analyze(ctx context.Context, req Request, on func(Event)) error {
	if req.Protocol == 0 {
		req.Protocol = Protocol
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	cmd := e.command(ctx, "analyze")
	cmd.Stdin = bytes.NewReader(body)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	if e.Stderr != nil {
		cmd.Stderr = io.MultiWriter(&stderr, e.Stderr)
	} else {
		cmd.Stderr = &stderr
	}
	if err := cmd.Start(); err != nil {
		return errors.Errorf("engine analyze: %w", err)
	}
	done := map[string]bool{}
	var batchDone, batchError bool
	var batchErr Event
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		ev.Raw = append([]byte(nil), line...)
		switch ev.Event {
		case "item_done":
			done[ev.ID] = true
		case "batch_done":
			batchDone = true
		case "batch_error":
			batchError = true
			batchErr = ev
		}
		on(ev)
	}
	waitErr := cmd.Wait()
	if batchError {
		return errors.Errorf("engine refused the batch: %s: %s", batchErr.Code, batchErr.Message)
	}
	if !batchDone {
		status := "exited"
		if waitErr != nil {
			status = waitErr.Error()
		}
		msg := fmt.Sprintf("engine %s before batch_done: %s", status, tail(stderr.String()))
		for _, item := range req.Items {
			if !done[item.ID] {
				for _, t := range item.Tasks {
					on(Event{Event: "item_error", ID: item.ID, Task: t, Code: "engine_crashed", Message: msg, Retryable: true})
				}
			}
		}
		return errors.New(msg)
	}
	return nil
}

func tail(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > 6 {
		lines = lines[len(lines)-6:]
	}
	return strings.Join(lines, "\n")
}

// AnalyzeAll runs a request of any size as batches the engine accepts,
// one after another, delivering every event to on. size is the batch
// size; 0 asks the engine (describe.limits.max_batch_items). Events keep
// their per-batch seq; a caller that needs one sequence counts for
// itself. The first batch that fails ends the run with its error after
// its events have been delivered.
func (e *Engine) AnalyzeAll(ctx context.Context, req Request, size int, on func(Event)) error {
	if size <= 0 {
		d, err := e.Describe(ctx)
		if err != nil {
			return err
		}
		size = d.Limits.MaxBatchItems
		if size <= 0 {
			size = 16
		}
	}
	for start := 0; start < len(req.Items); start += size {
		end := min(start+size, len(req.Items))
		batch := req
		batch.Items = req.Items[start:end]
		if err := e.Analyze(ctx, batch, on); err != nil {
			return err
		}
	}
	return nil
}
