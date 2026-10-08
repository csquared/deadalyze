// Command waveeval is the waveform regression harness: it analyses each track
// of a rekordbox USB export fresh with the bundle's engine, lays its waveforms
// out as the sections a stick export would carry, and scores every waveform
// section (PWAV, PWV2-7, PWVC) against the one rekordbox wrote for the same
// audio, field by field: exact %, mean absolute error and correlation. A
// section whose mean error rises above its floor fails the run.
//
//	go run ./cmd/waveeval -export ~/rb-usb          # a rekordbox export: PIONEER/USBANLZ plus the audio it names; floors in fixtures/waveeval/floors.json
//	go run ./cmd/waveeval -update                   # write the current errors (plus a margin) as the floors
//	go run ./cmd/waveeval -fields                   # every field, not just each section's mean
//
// It needs the bundle and its engine (DEADCA7_BUNDLE, DEADCA7_ENGINE, see
// client.Resolve), so it is not part of go test.
package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cockroachdb/errors"
	"github.com/csquared/deadalyze/anlz"
	"github.com/csquared/deadalyze/client"
)

// floorMargin is the slack a floor written by -update leaves over what was
// measured, so a change that moves nothing does not fail on rounding.
const floorMargin = 0.05

// Floors are the highest mean error each section may have, per track (the
// content path rekordbox recorded).
type Floors map[string]map[string]float64

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "waveeval:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		exportRoot = flag.String("export", "", "a rekordbox USB export (PIONEER/USBANLZ plus the audio it names)")
		floorsPath = flag.String("floors", "", "floors JSON (default: fixtures/waveeval/floors.json)")
		update     = flag.Bool("update", false, "write the measured errors as the floors instead of checking them")
		showFields = flag.Bool("fields", false, "print every field")
	)
	flag.Parse()

	root, err := repoRoot()
	if err != nil {
		return err
	}
	if *floorsPath == "" {
		*floorsPath = filepath.Join(root, "fixtures", "waveeval", "floors.json")
	}
	if *exportRoot == "" {
		return errors.New("-export is required: a rekordbox USB export to score against")
	}
	floors := Floors{}
	if b, err := os.ReadFile(*floorsPath); err == nil {
		if err := json.Unmarshal(b, &floors); err != nil {
			return errors.Errorf("%s: %w", *floorsPath, err)
		}
	} else if !*update {
		return err
	}

	tracks, err := rekordboxTracks(*exportRoot)
	if err != nil {
		return err
	}
	if len(tracks) == 0 {
		return errors.Errorf("no analysed tracks with audio under %s", *exportRoot)
	}
	eng, err := client.Resolve()
	if err != nil {
		return err
	}
	ctx := context.Background()
	var failures []string
	for _, tr := range tracks {
		got, err := engineSections(ctx, eng, tr.audio)
		if err != nil {
			return errors.Errorf("%s: %w", tr.content, err)
		}
		fmt.Printf("%s\n", tr.content)
		for _, section := range Sections {
			want, ok := tr.sections[section]
			if !ok {
				continue
			}
			s := scoreSection(section, want, got[section])
			floor, held := floors[tr.content][section]
			mark := "    "
			switch {
			case *update:
				if floors[tr.content] == nil {
					floors[tr.content] = map[string]float64{}
				}
				floors[tr.content][section] = roundUp(s.MAE + floorMargin)
			case !held:
				mark = "NEW "
			case s.MAE > floor:
				mark = "FAIL"
				failures = append(failures, fmt.Sprintf("%s %s: mean error %.3f above its floor %.3f", tr.content, section, s.MAE, floor))
			}
			fmt.Printf("  %s %-5s mean error %6.3f  floor %6.3f\n", mark, section, s.MAE, floor)
			if *showFields {
				for _, f := range s.Fields {
					fmt.Printf("         %-7s exact %5.1f%%  mae %6.3f  corr %5.3f  (%d/%d)\n", f.Name, f.Exact, f.MAE, f.Corr, f.RefLen, f.GotLen)
				}
			}
		}
	}
	if *update {
		if err := os.MkdirAll(filepath.Dir(*floorsPath), 0o755); err != nil {
			return err
		}
		b, _ := json.MarshalIndent(floors, "", "  ")
		if err := os.WriteFile(*floorsPath, append(b, '\n'), 0o644); err != nil {
			return err
		}
		fmt.Printf("wrote floors for %d track(s) to %s; review the diff before committing\n", len(tracks), *floorsPath)
		return nil
	}
	if len(failures) > 0 {
		return errors.Errorf("%d section(s) worse than their floor:\n  %s", len(failures), strings.Join(failures, "\n  "))
	}
	fmt.Printf("waveeval: %d track(s) within their floors\n", len(tracks))
	return nil
}

type rekordboxTrack struct {
	content  string
	audio    string
	sections map[string][]byte
}

// rekordboxTracks reads every analysis rekordbox wrote under an export and
// pairs it with the audio its PPTH names, when that audio is there.
func rekordboxTracks(exportRoot string) ([]rekordboxTrack, error) {
	dats, err := filepath.Glob(filepath.Join(exportRoot, "PIONEER", "USBANLZ", "*", "*", "ANLZ0000.DAT"))
	if err != nil {
		return nil, err
	}
	var out []rekordboxTrack
	for _, dat := range dats {
		dir := filepath.Dir(dat)
		sections, content, err := readSections(dir)
		if err != nil {
			return nil, errors.Errorf("%s: %w", dir, err)
		}
		audio := filepath.Join(exportRoot, filepath.FromSlash(strings.TrimPrefix(content, "/")))
		if _, err := os.Stat(audio); err != nil {
			continue
		}
		out = append(out, rekordboxTrack{content: content, audio: audio, sections: sections})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].content < out[j].content })
	return out, nil
}

// readSections reads an analysis folder's waveform entries by FourCC (PWVC as
// its three scales, big-endian) and the content path it was made for.
func readSections(dir string) (map[string][]byte, string, error) {
	out := map[string][]byte{}
	var content string
	for _, ext := range []string{".DAT", ".EXT", ".2EX"} {
		path := filepath.Join(dir, "ANLZ0000"+ext)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		f, err := anlz.ReadFile(path)
		if err != nil {
			return nil, "", err
		}
		if ext == ".DAT" {
			content = f.AudioPath()
		}
		for _, w := range f.Waveforms() {
			out[w.FourCC] = w.Data
		}
		if scales, ok := f.BandScales(); ok {
			b := make([]byte, 6)
			for i, v := range scales {
				binary.BigEndian.PutUint16(b[i*2:], v)
			}
			out["PWVC"] = b
		}
	}
	return out, content, nil
}

func roundUp(v float64) float64 {
	return float64(int(v*1000+0.999)) / 1000
}

// repoRoot is this checkout: the directory holding go.mod above
// the working directory.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.Errorf("not inside the deadalyze checkout (no go.mod above %s)", dir)
		}
		dir = parent
	}
}
