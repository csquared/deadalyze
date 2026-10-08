// Command corpus builds and indexes a grid corpus from deadcatalog catalogs.
//
//	corpus build -from Library.cdb -out ~/grids            # every analysed track, grids only
//	corpus build -from stick.cdb -out ~/grids -providers rekordbox -hash   # the stick's grids; same file, same id, when it is mounted
//	corpus build -from Library.cdb -out ~/grids -waveforms -keep-paths   # for your own fresh runs
//	corpus legacy -db ~/Music/DEADCA7/db/deadca7.db -analysis ~/Music/DEADCA7/analysis -out ~/grids
//	                                                       # a DEADCA7 server library's own grids, merged by file name
//	corpus index ~/grids                                   # rebuild index.json
//	corpus refs ~/grids [-truth rekordbox] > refs.json     # grideval references from the corpus
//	corpus stats ~/grids                                   # what is in it
//
// The source is opened read-only; the corpus is written beside nobody's data.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/csquared/deadalyze/corpus"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "corpus:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: corpus build|legacy|index|refs|stats ...")
	}
	ctx := context.Background()
	switch args[0] {
	case "build":
		fs := flag.NewFlagSet("build", flag.ContinueOnError)
		var opts corpus.Options
		var providers string
		fs.StringVar(&opts.From, "from", "", "source catalog (.cdb)")
		fs.StringVar(&opts.Out, "out", "", "corpus directory")
		fs.StringVar(&providers, "providers", "", "comma-separated analysis providers to keep (default: all)")
		fs.BoolVar(&opts.Waveforms, "waveforms", false, "keep waveforms (most of the bytes)")
		fs.BoolVar(&opts.KeepPaths, "keep-paths", false, "keep absolute audio paths (for your own machine)")
		fs.BoolVar(&opts.HashAudio, "hash", false, "name unhashed tracks by their audio file's sha256 when the file is reachable (a mounted stick)")
		fs.BoolVar(&opts.Force, "force", false, "rewrite tracks already in the corpus instead of merging into them")
		fs.StringVar(&opts.Contributor, "contributor", "", "recorded on each new entry")
		fs.IntVar(&opts.Limit, "limit", 0, "stop after this many tracks (for trials)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if providers != "" {
			opts.Providers = strings.Split(providers, ",")
		}
		opts.Log = func(s string) { fmt.Println(s) }
		rep, err := corpus.Build(ctx, opts)
		if err != nil {
			return err
		}
		fmt.Printf("corpus: %d tracks, %d written, %d merged, %d already there, %d without analysis\n", rep.Tracks, rep.Written, rep.Merged, rep.Skipped, rep.NoAnalysis)
		return nil
	case "legacy":
		fs := flag.NewFlagSet("legacy", flag.ContinueOnError)
		var opts corpus.LegacyOptions
		fs.StringVar(&opts.DB, "db", "", "the DEADCA7 server library's deadca7.db")
		fs.StringVar(&opts.AnalysisDir, "analysis", "", "its analysis directory (grid/ and cues/ JSON)")
		fs.StringVar(&opts.Out, "out", "", "corpus directory")
		fs.StringVar(&opts.Contributor, "contributor", "", "recorded on each new entry")
		fs.IntVar(&opts.Limit, "limit", 0, "stop after this many tracks (for trials)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		opts.Log = func(s string) { fmt.Println(s) }
		rep, err := corpus.ImportLegacy(ctx, opts)
		if err != nil {
			return err
		}
		fmt.Printf("corpus: %d legacy grids, %d written, %d merged into matching tracks, %d already there, %d unreadable\n", rep.Tracks, rep.Written, rep.Merged, rep.Skipped, rep.NoAnalysis)
		return nil
	case "index":
		fs := flag.NewFlagSet("index", flag.ContinueOnError)
		contributor := fs.String("contributor", "", "recorded on entries that have none")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		dir := fs.Arg(0)
		if dir == "" {
			return fmt.Errorf("usage: corpus index DIR")
		}
		idx, err := corpus.Reindex(ctx, dir, *contributor)
		if err != nil {
			return err
		}
		fmt.Printf("corpus: indexed %d tracks\n", len(idx.Tracks))
		return nil
	case "refs":
		fs := flag.NewFlagSet("refs", flag.ContinueOnError)
		truth := fs.String("truth", "rekordbox", "the provider whose grids are the references")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		dir := fs.Arg(0)
		if dir == "" {
			return fmt.Errorf("usage: corpus refs DIR")
		}
		idx, err := corpus.Load(dir)
		if err != nil {
			return err
		}
		type ref struct {
			Path            string  `json:"path"`
			BPM             float64 `json:"bpm"`
			FirstDownbeatMs int     `json:"first_downbeat_ms"`
			BeatsPerBar     int     `json:"beats_per_bar"`
			Note            string  `json:"note,omitempty"`
		}
		var refs []ref
		for _, e := range idx.Tracks {
			for _, g := range e.Grids {
				if g.Provider == *truth && g.Beats > 0 {
					refs = append(refs, ref{Path: e.FileName, BPM: g.BPM, FirstDownbeatMs: g.FirstDownbeatMs, BeatsPerBar: g.BeatsPerBar, Note: e.ID})
					break
				}
			}
		}
		b, _ := json.MarshalIndent(refs, "", "  ")
		fmt.Println(string(b))
		return nil
	case "stats":
		dir := ""
		if len(args) > 1 {
			dir = args[1]
		}
		if dir == "" {
			return fmt.Errorf("usage: corpus stats DIR")
		}
		idx, err := corpus.Load(dir)
		if err != nil {
			return err
		}
		providers := map[string]int{}
		both := 0
		for _, e := range idx.Tracks {
			seen := map[string]bool{}
			for _, g := range e.Grids {
				providers[g.Provider]++
				seen[g.Provider] = true
			}
			if seen["rekordbox"] && len(seen) > 1 {
				both++
			}
		}
		names := make([]string, 0, len(providers))
		for p := range providers {
			names = append(names, p)
		}
		sort.Strings(names)
		fmt.Printf("%d tracks, updated %s\n", len(idx.Tracks), idx.Updated)
		for _, p := range names {
			fmt.Printf("  %-12s %d grids\n", p, providers[p])
		}
		fmt.Printf("  %d tracks carry rekordbox and another provider (comparable)\n", both)
		return nil
	}
	return fmt.Errorf("unknown command %q", args[0])
}
