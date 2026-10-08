// Command grideval is the beat-grid harness: it scores grids against
// references (BPM, first downbeat, bar), failing the run when any grid
// drifts. Phase is the signal that matters on stage: a one-beat offset reads
// as 1.0 here.
//
// Against a corpus (see ../../corpus), with no runtime: the grids already in
// the track catalogs are scored against the reference provider's.
//
//	go run ./cmd/grideval -corpus-dir ~/grids                       # deadca7's stored grids vs rekordbox's
//	go run ./cmd/grideval -corpus-dir ~/grids -provider contributor # another provider's stored grids
//	go run ./cmd/grideval -corpus-dir ~/grids -json out.json        # keep the per-track scores
//
// Fresh, with the runtime (dc runtime install, or DEADCATALOG_RUNTIME at a
// build stage): every track whose audio is found is analysed again with the
// current engine, about twenty seconds a file.
//
//	go run ./cmd/grideval -corpus-dir ~/grids -fresh -audio ~/Music     # audio found by file name under -audio
//	go run ./cmd/grideval -corpus-dir ~/grids -fresh                    # audio at the paths the catalogs keep (-keep-paths)
//
// Against a folder of audio and hand-reviewed references, as deadcatalog's
// harness ran:
//
//	go run ./cmd/grideval -corpus ~/tracks -refs ~/tracks/refs.json
//	go run ./cmd/grideval -corpus ~/tracks -refs refs.json -update   # write the current grids as the references (review the diff!)
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/csquared/deadalyze/corpus"
	"github.com/csquared/deadcatalog/analysis"
	"github.com/csquared/deadcatalog/analysis/engine"
	"github.com/csquared/deadcatalog/catalog"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "grideval:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		corpusDir = flag.String("corpus-dir", "", "a corpus directory (index.json + tracks/*.cdb)")
		truth     = flag.String("truth", "rekordbox", "corpus: the provider whose grids are the references")
		provider  = flag.String("provider", "deadca7", "corpus: the provider whose stored grids are scored")
		fresh     = flag.Bool("fresh", false, "corpus: analyse the audio again with the current engine instead of scoring stored grids")
		audioDir  = flag.String("audio", "", "corpus -fresh: find each track's audio by file name under this directory")
		audio     = flag.String("corpus", "", "folder mode: directory the references' paths are relative to")
		refsPath  = flag.String("refs", "", "folder mode: references JSON")
		update    = flag.Bool("update", false, "folder mode: write the current grids as the references instead of scoring them")
		jsonOut   = flag.String("json", "", "also write the scores as JSON here")
		limit     = flag.Int("n", 0, "score only the first n tracks (0: all)")
	)
	flag.Parse()
	ctx := context.Background()
	var scores []Score
	var err error
	switch {
	case *corpusDir != "":
		scores, err = scoreCorpus(ctx, *corpusDir, *truth, *provider, *fresh, *audioDir, *limit)
	case *audio != "" || *refsPath != "":
		return folderMode(ctx, *audio, *refsPath, *update, *jsonOut)
	default:
		flag.Usage()
		return errors.New("one of -corpus-dir or -corpus/-refs is required")
	}
	if err != nil {
		return err
	}
	return report(scores, *jsonOut)
}

// scoreCorpus scores one provider's grids against another's, track by track.
func scoreCorpus(ctx context.Context, dir, truth, provider string, fresh bool, audioDir string, limit int) ([]Score, error) {
	idx, err := corpus.Load(dir)
	if err != nil {
		return nil, err
	}
	var byName map[string]string
	if fresh && audioDir != "" {
		if byName, err = audioByName(audioDir); err != nil {
			return nil, err
		}
	}
	var scores []Score
	skipped := 0
	for _, e := range idx.Tracks {
		if limit > 0 && len(scores) >= limit {
			break
		}
		db, err := corpus.Open(filepath.Join(dir, e.File))
		if err != nil {
			return nil, err
		}
		ref, ok, err := reference(ctx, db, e, truth)
		if err != nil {
			db.Close()
			return nil, err
		}
		if !ok {
			db.Close()
			skipped++
			continue
		}
		started := time.Now()
		var result *analysis.Result
		if fresh {
			path := audioPath(ctx, db, e, byName)
			if path == "" {
				db.Close()
				skipped++
				continue
			}
			result, err = engine.Analyze(ctx, path)
			if err != nil {
				return nil, errors.Errorf("%s: %w", e.FileName, err)
			}
		} else {
			tuples, g, err := corpus.Beats(ctx, db, provider)
			if err != nil {
				db.Close()
				return nil, err
			}
			if len(tuples) == 0 {
				db.Close()
				skipped++
				continue
			}
			result = resultFromBeats(tuples, g)
		}
		db.Close()
		s := score(ref, result)
		s.Duration = time.Since(started).Seconds()
		scores = append(scores, s)
		fmt.Println(s.line())
	}
	if skipped > 0 {
		fmt.Printf("grideval: %d track(s) skipped (no %s reference, no %s grid, or no audio)\n", skipped, truth, provider)
	}
	return scores, nil
}

// reference is the truth provider's grid for a track, as a Reference. The
// path is the file name: what a fresh run finds the audio by.
func reference(ctx context.Context, db *sql.DB, e corpus.Entry, truth string) (Reference, bool, error) {
	tuples, g, err := corpus.Beats(ctx, db, truth)
	if err != nil || len(tuples) == 0 {
		return Reference{}, false, err
	}
	bar := g.BeatsPerBar
	if bar <= 0 {
		bar = 4
	}
	return Reference{Path: e.FileName, BPM: g.BPM, FirstDownbeatMs: g.FirstDownbeatMs, BeatsPerBar: bar, Note: e.ID}, true, nil
}

// audioPath finds a track's audio: by file name under -audio, else at the
// path the catalog kept (a corpus built with -keep-paths).
func audioPath(ctx context.Context, db *sql.DB, e corpus.Entry, byName map[string]string) string {
	if p, ok := byName[e.FileName]; ok {
		return p
	}
	var path sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT file_path FROM tracks LIMIT 1`).Scan(&path); err == nil && path.Valid {
		if _, err := os.Stat(path.String); err == nil {
			return path.String
		}
	}
	return ""
}

func resultFromBeats(tuples []catalog.BeatTuple, g corpus.Grid) *analysis.Result {
	r := &analysis.Result{Provider: g.Provider, Version: g.Version, BPM: g.BPM}
	r.Grid = analysis.Grid{BPM: g.BPM, BeatsPerBar: g.BeatsPerBar}
	for i, t := range tuples {
		r.Beats = append(r.Beats, analysis.Beat{Index: i, TimeMs: int(t[2]), BeatNumber: int(t[0])})
		if t[0] == 1 {
			r.Downbeats = append(r.Downbeats, analysis.Downbeat{Index: i, TimeMs: int(t[2]), BeatNumber: 1})
		}
	}
	if len(r.Beats) > 0 {
		first := r.Beats[0].TimeMs
		r.Grid.FirstBeatMs = &first
	}
	if len(r.Downbeats) > 0 {
		first := r.Downbeats[0].TimeMs
		r.Grid.FirstDownbeatMs = &first
	}
	return r
}

// audioByName indexes the audio files under a directory by base name.
func audioByName(dir string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if isAudio(path) {
			out[filepath.Base(path)] = path
		}
		return nil
	})
	return out, err
}

func isAudio(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3", ".aiff", ".aif", ".wav", ".flac", ".m4a":
		return true
	}
	return false
}

func report(scores []Score, jsonOut string) error {
	failed, disputed := 0, 0
	for _, s := range scores {
		if !s.Pass {
			failed++
		}
		if s.Dispute != "" {
			disputed++
		}
	}
	if jsonOut != "" {
		b, _ := json.MarshalIndent(scores, "", "  ")
		if err := os.WriteFile(jsonOut, b, 0o644); err != nil {
			return err
		}
	}
	if len(scores) == 0 {
		return errors.New("nothing scored")
	}
	fmt.Printf("grideval: %d of %d pass, %d disputed\n", len(scores)-failed, len(scores), disputed)
	if failed > 0 {
		return errors.Errorf("%d grid(s) off the reference", failed)
	}
	return nil
}

// folderMode is deadcatalog's harness: audio files and a hand-reviewed refs.json.
func folderMode(ctx context.Context, dir, refsPath string, update bool, jsonOut string) error {
	if dir == "" || refsPath == "" {
		return errors.New("folder mode needs both -corpus and -refs")
	}
	refs, err := loadRefs(refsPath)
	if err != nil && !(update && os.IsNotExist(err)) {
		return err
	}
	if update && len(refs) == 0 {
		if refs, err = scanCorpus(dir); err != nil {
			return err
		}
	}
	if len(refs) == 0 {
		return errors.Errorf("no references in %s", refsPath)
	}
	var scores []Score
	for i, ref := range refs {
		started := time.Now()
		result, err := engine.Analyze(ctx, filepath.Join(dir, ref.Path))
		if err != nil {
			return errors.Errorf("%s: %w", ref.Path, err)
		}
		if update {
			refs[i] = referenceFrom(ref, result)
			fmt.Printf("ref   %s  bpm %.2f  downbeat %d ms\n", ref.Path, refs[i].BPM, refs[i].FirstDownbeatMs)
			continue
		}
		s := score(ref, result)
		s.Duration = time.Since(started).Seconds()
		scores = append(scores, s)
		fmt.Println(s.line())
	}
	if update {
		if err := writeRefs(refsPath, refs); err != nil {
			return err
		}
		fmt.Printf("wrote %d references to %s; review them before committing\n", len(refs), refsPath)
		return nil
	}
	return report(scores, jsonOut)
}

func (s Score) line() string {
	if s.Error != "" {
		return fmt.Sprintf("ERROR %s  %s", s.Path, s.Error)
	}
	mark := "FAIL"
	if s.Pass {
		mark = "pass"
	}
	dispute := ""
	if s.Dispute != "" {
		dispute = "  disputed: " + s.Dispute
	}
	return fmt.Sprintf("%s  %s  bpm %.2f vs %.2f (Δ%+.2f)  meter %d vs %d  phase %+.2f beats  bars %+d  downbeat %d ms  %.1fs%s",
		mark, s.Path, s.BPM, s.BPMRef, s.BPMDelta, s.BeatsPerBar, s.BeatsPerBarRef, s.PhaseBeats, s.Bars, s.FirstDownbeatMs, s.Duration, dispute)
}

func referenceFrom(ref Reference, r *analysis.Result) Reference {
	ref.BPM = r.BPM
	if first, ok := firstDownbeat(r); ok {
		ref.FirstDownbeatMs = first
	}
	ref.BeatsPerBar = r.Grid.BeatsPerBar
	if ref.BeatsPerBar <= 0 {
		ref.BeatsPerBar = 4
	}
	return ref
}

func loadRefs(path string) ([]Reference, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var refs []Reference
	if err := json.Unmarshal(b, &refs); err != nil {
		return nil, errors.Errorf("%s: %w", path, err)
	}
	return refs, nil
}

func writeRefs(path string, refs []Reference) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(refs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// scanCorpus lists the audio files under a directory as bare references.
func scanCorpus(dir string) ([]Reference, error) {
	var refs []Reference
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if isAudio(path) {
			rel, _ := filepath.Rel(dir, path)
			refs = append(refs, Reference{Path: rel})
		}
		return nil
	})
	sort.Slice(refs, func(i, j int) bool { return refs[i].Path < refs[j].Path })
	return refs, err
}
