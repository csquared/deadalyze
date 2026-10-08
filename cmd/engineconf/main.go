// Command engineconf checks an engine against the protocol
// (docs/engine-protocol.md): the grammar and ordering of its events, the
// shape of what it answers, that its identities are stable across two
// runs, that its grids land on the hand-reviewed references, and that its
// waveforms, features and keys match the goldens. A Rust leg proves itself
// here before a host sees it.
//
//	go run ./cmd/engineconf -audio DIR                       # the fixtures' audio is under DIR
//	go run ./cmd/engineconf -audio DIR -update               # write the goldens from this engine
//	go run ./cmd/engineconf -audio DIR -engine /path/engine  # an engine other than the bundle's
//
// Tiers: exact (grammar, ordering, identity fields, waveform bytes, key
// label), near-exact (bpm to 0.001, first beat to 1 ms, DC7F to the byte
// today), and the references (bpm to 0.05, phase to a quarter beat, as grideval).
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/csquared/deadalyze/client"
)

type Reference struct {
	Path            string  `json:"path"`
	BPM             float64 `json:"bpm"`
	FirstDownbeatMs int     `json:"first_downbeat_ms"`
	BeatsPerBar     int     `json:"beats_per_bar"`
}

// Golden is what one engine made of one track, compared across engines.
type Golden struct {
	BPM             float64           `json:"bpm"`
	FirstBeatMs     int64             `json:"first_beat_ms"`
	FirstDownbeatMs int64             `json:"first_downbeat_ms"`
	Beats           int               `json:"beats"`
	IdentityHash    string            `json:"identity_hash"`
	Waveforms       map[string]string `json:"waveforms"`
	Features        string            `json:"features"`
	FeaturesBytes   int               `json:"features_bytes"`
	Key             string            `json:"key"`
	Cues            []client.Cue      `json:"cues"`
}

type report struct {
	failures []string
	warnings []string
}

func (r *report) failf(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}
func (r *report) warnf(format string, args ...any) {
	r.warnings = append(r.warnings, fmt.Sprintf(format, args...))
}

func main() {
	var (
		audio   = flag.String("audio", "", "directory the references' paths are under")
		refs    = flag.String("refs", "fixtures/grideval/refs.json", "the references")
		golden  = flag.String("golden", "fixtures/engineconf/golden.json", "the goldens")
		update  = flag.Bool("update", false, "write the goldens from this engine instead of checking them")
		engine  = flag.String("engine", "", "engine command to run in the bundle (default: the bundle's)")
		bundle  = flag.String("bundle", "", "bundle root (default: resolved as a host would)")
		timeout = flag.Duration("timeout", 20*time.Minute, "for the whole run")
	)
	flag.Parse()
	if *audio == "" {
		flag.Usage()
		os.Exit(2)
	}
	if *engine != "" {
		os.Setenv("DEADCA7_ENGINE", *engine)
	}
	if *bundle != "" {
		os.Setenv("DEADCA7_BUNDLE", *bundle)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	r := &report{}
	if err := run(ctx, r, *audio, *refs, *golden, *update); err != nil {
		fmt.Fprintln(os.Stderr, "engineconf:", err)
		os.Exit(1)
	}
	for _, w := range r.warnings {
		fmt.Println("warn ", w)
	}
	for _, f := range r.failures {
		fmt.Println("FAIL ", f)
	}
	if len(r.failures) > 0 {
		fmt.Printf("engineconf: %d failure(s), %d warning(s)\n", len(r.failures), len(r.warnings))
		os.Exit(1)
	}
	fmt.Printf("engineconf: ok, %d warning(s)\n", len(r.warnings))
}

func run(ctx context.Context, r *report, audioDir, refsPath, goldenPath string, update bool) error {
	eng, err := client.Resolve()
	if err != nil {
		return err
	}
	d, err := eng.Describe(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("engine %s %s, recipe %s, bundle %q, legs:", d.Engine.Impl, d.Engine.Version, d.Recipe, d.RuntimeVersion)
	for _, name := range sortedKeys(d.Legs) {
		fmt.Printf(" %s=%s/%s", name, d.Legs[name].AlgoVersion, d.Legs[name].Impl)
	}
	fmt.Println()
	checkDescribe(r, d)

	var references []Reference
	b, err := os.ReadFile(refsPath)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &references); err != nil {
		return err
	}
	var items []client.Item
	byID := map[string]Reference{}
	for _, ref := range references {
		path := filepath.Join(audioDir, ref.Path)
		if _, err := os.Stat(path); err != nil {
			r.warnf("%s: no audio at %s, skipped", ref.Path, path)
			continue
		}
		items = append(items, client.Item{ID: ref.Path, Audio: path, Tasks: []string{client.TaskGrid, client.TaskKey, client.TaskFeatures, client.TaskWaveform, client.TaskCues}})
		byID[ref.Path] = ref
	}
	if len(items) == 0 {
		return fmt.Errorf("no reference audio under %s", audioDir)
	}

	first, err := runBatch(ctx, r, eng, items, "run 1")
	if err != nil {
		return err
	}
	second, err := runBatch(ctx, r, eng, items, "run 2")
	if err != nil {
		return err
	}
	for id, a := range first {
		b, ok := second[id]
		if !ok {
			continue
		}
		if a.IdentityHash != b.IdentityHash {
			r.failf("%s: identity_hash differs between runs (%s vs %s)", id, a.IdentityHash, b.IdentityHash)
		}
		if math.Abs(a.BPM-b.BPM) > 0.001 || absInt(a.FirstBeatMs-b.FirstBeatMs) > 1 {
			r.warnf("%s: the engine does not repeat itself: bpm %.3f vs %.3f, first beat %d vs %d ms", id, a.BPM, b.BPM, a.FirstBeatMs, b.FirstBeatMs)
		}
		for kind, sum := range a.Waveforms {
			if b.Waveforms[kind] != sum {
				r.failf("%s: waveform %s differs between runs", id, kind)
			}
		}
		if a.Features != b.Features {
			r.warnf("%s: features differ between runs", id)
		}
	}

	for id, g := range first {
		ref := byID[id]
		bar := ref.BeatsPerBar
		if bar <= 0 {
			bar = 4
		}
		if math.Abs(g.BPM-ref.BPM) > 0.05 {
			r.failf("%s: bpm %.2f, reference %.2f", id, g.BPM, ref.BPM)
			continue
		}
		period := 60000.0 / ref.BPM
		phase := float64(g.FirstDownbeatMs-int64(ref.FirstDownbeatMs)) / period
		phase -= math.Round(phase/float64(bar)) * float64(bar)
		if math.Abs(phase) > 0.25 {
			r.failf("%s: first downbeat %d ms is %+.2f beats off the reference's %d ms", id, g.FirstDownbeatMs, phase, ref.FirstDownbeatMs)
		}
	}

	if update {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			return err
		}
		b, _ := json.MarshalIndent(first, "", "  ")
		if err := os.WriteFile(goldenPath, append(b, '\n'), 0o644); err != nil {
			return err
		}
		fmt.Printf("wrote %d golden(s) to %s\n", len(first), goldenPath)
		return nil
	}
	goldens := map[string]Golden{}
	if b, err := os.ReadFile(goldenPath); err == nil {
		if err := json.Unmarshal(b, &goldens); err != nil {
			return fmt.Errorf("%s: %w", goldenPath, err)
		}
	} else {
		r.warnf("no goldens at %s (run with -update to write them)", goldenPath)
	}
	for id, g := range first {
		want, ok := goldens[id]
		if !ok {
			continue
		}
		if want.IdentityHash != g.IdentityHash {
			r.warnf("%s: identity %s, golden %s (a new recipe or leg; -update once the change is meant)", id, g.IdentityHash, want.IdentityHash)
		}
		if math.Abs(want.BPM-g.BPM) > 0.001 {
			r.failf("%s: bpm %.3f, golden %.3f", id, g.BPM, want.BPM)
		}
		if absInt(want.FirstBeatMs-g.FirstBeatMs) > 1 {
			r.failf("%s: first beat %d ms, golden %d", id, g.FirstBeatMs, want.FirstBeatMs)
		}
		if want.Beats != g.Beats {
			r.failf("%s: %d beats, golden %d", id, g.Beats, want.Beats)
		}
		for kind, sum := range want.Waveforms {
			if g.Waveforms[kind] != sum {
				r.failf("%s: waveform %s is not the golden's bytes", id, kind)
			}
		}
		if want.Features != g.Features {
			r.failf("%s: features are not the golden's bytes (%d vs %d bytes)", id, g.FeaturesBytes, want.FeaturesBytes)
		}
		if want.Key != g.Key {
			r.failf("%s: key %q, golden %q", id, g.Key, want.Key)
		}
		if fmt.Sprint(want.Cues) != fmt.Sprint(g.Cues) {
			r.failf("%s: cues differ from the golden's", id)
		}
	}
	return nil
}

func checkDescribe(r *report, d *client.Describe) {
	has := false
	for _, p := range d.Protocols {
		if p == client.Protocol {
			has = true
		}
	}
	if !has {
		r.failf("describe: protocols %v lack %d", d.Protocols, client.Protocol)
	}
	if !regexp.MustCompile(`^deadca7-v\d+$`).MatchString(d.Recipe) {
		r.failf("describe: recipe %q", d.Recipe)
	}
	for _, t := range []string{client.TaskGrid, client.TaskKey, client.TaskFeatures, client.TaskWaveform, client.TaskCues} {
		if !containsStr(d.Tasks, t) {
			r.failf("describe: no %s task", t)
		}
	}
	for _, leg := range []string{"beatnet", "beat_this", "key", "features", "waveform"} {
		l, ok := d.Legs[leg]
		if !ok {
			r.failf("describe: no %s leg", leg)
		} else if !l.Ready {
			r.failf("describe: leg %s not ready: %s", leg, l.Note)
		}
	}
	if d.Limits.MaxBatchItems < 1 || d.Limits.MaxLineBytes < 65536 {
		r.failf("describe: limits %+v", d.Limits)
	}
	for _, k := range []string{"beats_per_bar", "arbitrate_min_vote_agreement", "device"} {
		if _, ok := d.SettingsSchema[k]; !ok {
			r.failf("describe: settings_schema lacks %s", k)
		}
	}
}

// track is what one id's events add up to, as they come.
type track struct {
	started, done     bool
	frames            int
	nextOffset        uint32
	lastFrameSeq      uint64
	waveKinds         map[string]bool
	lastWaveSeq       uint64
	gridSeq           uint64
	golden            Golden
	tasks             map[string]string
	outOfOrderReports int
}

var camelotRe = regexp.MustCompile(`^(1[0-2]|[1-9])[AB]$`)

func runBatch(ctx context.Context, r *report, eng *client.Engine, items []client.Item, label string) (map[string]Golden, error) {
	tracks := map[string]*track{}
	for _, it := range items {
		tracks[it.ID] = &track{waveKinds: map[string]bool{}, golden: Golden{Waveforms: map[string]string{}}}
	}
	var seq uint64
	var first, last string
	started := time.Now()
	err := eng.Analyze(ctx, client.Request{Settings: map[string]any{"name": "dance4x4"}, Items: items}, func(ev client.Event) {
		seq++
		if ev.Seq != seq {
			r.failf("%s: seq %d at line %d", label, ev.Seq, seq)
		}
		if first == "" {
			first = ev.Event
		}
		last = ev.Event
		if ev.ID == "" {
			return
		}
		t, ok := tracks[ev.ID]
		if !ok {
			r.failf("%s: event for unknown id %q", label, ev.ID)
			return
		}
		if t.done {
			r.failf("%s: %s: %s after item_done", label, ev.ID, ev.Event)
		}
		switch ev.Event {
		case client.ItemStarted:
			t.started = true
		case client.WaveformFrame:
			if !t.started {
				r.failf("%s: %s: frame before item_started", label, ev.ID)
			}
			if ev.Offset != t.nextOffset && t.outOfOrderReports == 0 {
				r.failf("%s: %s: frame at offset %d, expected %d", label, ev.ID, ev.Offset, t.nextOffset)
				t.outOfOrderReports++
			}
			if ev.ColumnsPerSecond != 150 {
				r.failf("%s: %s: columns_per_second %d", label, ev.ID, ev.ColumnsPerSecond)
			}
			data, err := ev.Data.Bytes()
			if err != nil || len(data) != int(ev.Columns)*3 {
				r.failf("%s: %s: frame data %d bytes for %d columns", label, ev.ID, len(data), ev.Columns)
			}
			t.nextOffset = ev.Offset + ev.Columns
			t.frames++
			t.lastFrameSeq = ev.Seq
		case client.WaveformEvent:
			data, err := ev.Data.Bytes()
			if err != nil || len(data) != ev.EntryBytes*ev.EntryCount {
				r.failf("%s: %s: waveform %s: %d bytes for %d×%d", label, ev.ID, ev.Kind, len(data), ev.EntryBytes, ev.EntryCount)
			}
			t.waveKinds[ev.Kind] = true
			t.golden.Waveforms[ev.Kind] = sha(data)
			t.lastWaveSeq = ev.Seq
		case client.GridEvent:
			t.gridSeq = ev.Seq
			checkGrid(r, label, ev, &t.golden)
		case client.FeaturesEvent:
			data, err := ev.Data.Bytes()
			if err != nil || len(data) < 64 || string(data[:4]) != "DC7F" || ev.Format != "dc7f" {
				r.failf("%s: %s: features are not DC7F", label, ev.ID)
			}
			t.golden.Features = sha(data)
			t.golden.FeaturesBytes = len(data)
		case client.KeyEvent:
			if ev.Camelot != "" && !camelotRe.MatchString(ev.Camelot) {
				r.failf("%s: %s: camelot %q", label, ev.ID, ev.Camelot)
			}
			if ev.AlgoVersion == "" {
				r.failf("%s: %s: key without algo_version", label, ev.ID)
			}
			t.golden.Key = ev.Label
		case client.CuesEvent:
			t.golden.Cues = ev.Cues
		case client.ItemError:
			r.failf("%s: %s: %s failed: %s: %s", label, ev.ID, ev.Task, ev.Code, ev.Message)
		case client.ItemWarning:
			r.warnf("%s: %s: %s: %s: %s", label, ev.ID, ev.Task, ev.Code, ev.Message)
		case client.ItemDone:
			t.done = true
			t.tasks = ev.Tasks
		}
	})
	if err != nil {
		return nil, err
	}
	fmt.Printf("%s: %d track(s) in %.0fs\n", label, len(items), time.Since(started).Seconds())
	if first != client.BatchStarted {
		r.failf("%s: first event %s", label, first)
	}
	if last != client.BatchDone {
		r.failf("%s: last event %s", label, last)
	}
	out := map[string]Golden{}
	kinds := []string{"mono_preview", "mono_detail", "color_preview", "color_detail", "three_band_preview", "three_band_detail", "three_band_scales"}
	for _, it := range items {
		t := tracks[it.ID]
		if !t.started || !t.done {
			r.failf("%s: %s: started %v, done %v", label, it.ID, t.started, t.done)
			continue
		}
		if t.frames == 0 {
			r.failf("%s: %s: no waveform frames were streamed", label, it.ID)
		}
		for _, k := range kinds {
			if !t.waveKinds[k] {
				r.failf("%s: %s: no %s waveform", label, it.ID, k)
			}
		}
		if t.gridSeq == 0 {
			r.failf("%s: %s: no grid", label, it.ID)
		} else if t.lastFrameSeq > t.gridSeq || t.lastWaveSeq > t.gridSeq {
			r.failf("%s: %s: waveform arrived after the grid (frame %d, wave %d, grid %d)", label, it.ID, t.lastFrameSeq, t.lastWaveSeq, t.gridSeq)
		}
		for _, task := range it.Tasks {
			if t.tasks[task] != "ok" {
				r.failf("%s: %s: task %s is %q", label, it.ID, task, t.tasks[task])
			}
		}
		out[it.ID] = t.golden
	}
	return out, nil
}

func checkGrid(r *report, label string, ev client.Event, g *Golden) {
	id := ev.ID
	if ev.BPM <= 0 || len(ev.Beats) == 0 {
		r.failf("%s: %s: empty grid", label, id)
		return
	}
	bar := ev.BeatsPerBar
	if bar <= 0 {
		r.failf("%s: %s: beats_per_bar %d", label, id, bar)
		bar = 4
	}
	prev := int64(-1)
	for i, b := range ev.Beats {
		if b[0] < 1 || int(b[0]) > bar {
			r.failf("%s: %s: beat %d has number %d", label, id, i, b[0])
			break
		}
		if i > 0 {
			want := ev.Beats[i-1][0]%int64(bar) + 1
			if b[0] != want {
				r.failf("%s: %s: beat %d numbered %d after %d", label, id, i, b[0], ev.Beats[i-1][0])
				break
			}
		}
		if b[1] < prev {
			r.failf("%s: %s: beat %d at %d ms before %d", label, id, i, b[1], prev)
			break
		}
		prev = b[1]
	}
	if ev.FirstBeatMs == nil || *ev.FirstBeatMs != ev.Beats[0][1] {
		r.failf("%s: %s: first_beat_ms is not the first beat", label, id)
	}
	var firstDown int64 = -1
	for _, b := range ev.Beats {
		if b[0] == 1 {
			firstDown = b[1]
			break
		}
	}
	if ev.FirstDownbeatMs == nil || *ev.FirstDownbeatMs != firstDown {
		r.failf("%s: %s: first_downbeat_ms is not the first beat numbered 1", label, id)
	}
	if ev.Timeline == nil || ev.Timeline.Name != "rekordbox" {
		r.failf("%s: %s: timeline %+v", label, id, ev.Timeline)
	}
	if ev.Identity == nil || !regexp.MustCompile(`^deadca7-v\d+$`).MatchString(ev.Identity.Recipe) || len(ev.Identity.IdentityHash) != 64 || ev.Identity.Legs["beatnet"].CfgHash == "" {
		r.failf("%s: %s: identity %+v", label, id, ev.Identity)
	} else {
		g.IdentityHash = ev.Identity.IdentityHash
	}
	if ev.RanOn == nil || ev.RanOn.Device == "" {
		r.failf("%s: %s: ran_on %+v", label, id, ev.RanOn)
	}
	if ev.Consensus == nil || !containsStr([]string{"agreed", "disputed", "unavailable", "skipped"}, ev.Consensus.Verdict) {
		r.failf("%s: %s: consensus %+v", label, id, ev.Consensus)
	} else if ev.Consensus.Verdict == "disputed" && ev.Consensus.Dispute == "" {
		r.failf("%s: %s: disputed without a dispute", label, id)
	}
	g.BPM = ev.BPM
	g.FirstBeatMs = ev.Beats[0][1]
	g.FirstDownbeatMs = firstDown
	g.Beats = len(ev.Beats)
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func absInt(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

func containsStr(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
