package corpus

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/csquared/deadcatalog/catalog"
	_ "modernc.org/sqlite"
)

// LegacyOptions point ImportLegacy at a DEADCA7 (Go server) library: its
// deadca7.db and the analysis directory the grid and cue JSON live in.
type LegacyOptions struct {
	DB          string
	AnalysisDir string
	Out         string
	Contributor string
	Limit       int
	Log         func(string)
}

// legacyGrid is the part of a DEADCA7 grid JSON the corpus keeps.
type legacyGrid struct {
	AlgoVersion    string         `json:"algoVersion"`
	CfgHash        string         `json:"cfg_hash"`
	Cfg            map[string]any `json:"cfg"`
	BPM            float64        `json:"bpm"`
	RawBPM         float64        `json:"raw_bpm"`
	Beats          []float64      `json:"beats"`
	Downbeats      []float64      `json:"downbeats"`
	Duration       float64        `json:"duration"`
	Dynamic        bool           `json:"dynamic"`
	FileHash       string         `json:"file_hash"`
	SamplesHash    string         `json:"samples_hash"`
	OriginRefineMs float64        `json:"origin_refine_ms"`
	Anchor         map[string]any `json:"anchor"`
	Phase          map[string]any `json:"phase"`
}

type legacyCues struct {
	Cues []struct {
		Comment string  `json:"comment"`
		HotCue  int     `json:"hotCue"`
		Kind    string  `json:"kind"`
		Time    float64 `json:"time"`
	} `json:"cues"`
}

// ImportLegacy brings a DEADCA7 library's own grids into the corpus as
// provider "deadca7", version "legacy". A track whose file name matches one
// already in the corpus (rekordbox's grid of the same file, typically) is
// merged into it; the rest become new entries named by the audio file's
// sha256 the grid recorded.
//
// The match is the one DEADCA7 used between its library and rekordbox: the
// file's base name with everything but letters and digits removed.
func ImportLegacy(ctx context.Context, opts LegacyOptions) (Report, error) {
	var rep Report
	if opts.DB == "" || opts.AnalysisDir == "" || opts.Out == "" {
		return rep, errors.New("corpus legacy: -db, -analysis and -out are required")
	}
	db, err := sql.Open("sqlite", "file:"+opts.DB+"?mode=ro")
	if err != nil {
		return rep, err
	}
	defer db.Close()
	if err := os.MkdirAll(filepath.Join(opts.Out, "tracks"), 0o755); err != nil {
		return rep, err
	}
	byName := map[string]Entry{}
	if idx, err := Load(opts.Out); err == nil {
		for _, e := range idx.Tracks {
			if e.FileName != "" {
				byName[NormalizeName(e.FileName)] = e
			}
		}
	}
	tracks, err := rows(ctx, db, `SELECT t.id, t.title, t.length_ms, t.bpm_x100, t.audio_hash, t.created_at, t.date_added, t.comment,
		a.name AS artist, ta.beats_path, ta.beats_src, ta.cues_path, ta.cues_src,
		(SELECT key FROM track_alias x WHERE x.track_id = t.id AND x.kind = 'basename' LIMIT 1) AS basename,
		(SELECT key FROM track_alias x WHERE x.track_id = t.id AND x.kind = 'rekordbox' LIMIT 1) AS rekordbox_id
		FROM track_analysis ta JOIN track t ON t.id = ta.track_id LEFT JOIN artist a ON a.id = t.artist_id
		WHERE ta.beats_path != '' ORDER BY t.id`)
	if err != nil {
		return rep, err
	}
	for _, t := range tracks {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		if opts.Limit > 0 && rep.Tracks >= opts.Limit {
			break
		}
		rep.Tracks++
		gridPath := filepath.Join(opts.AnalysisDir, str(t["beats_path"]))
		raw, err := os.ReadFile(gridPath)
		if err != nil {
			rep.NoAnalysis++
			continue
		}
		var grid legacyGrid
		if err := json.Unmarshal(raw, &grid); err != nil {
			return rep, errors.Wrapf(err, "%s", gridPath)
		}
		if len(grid.Beats) == 0 || grid.BPM <= 0 {
			rep.NoAnalysis++
			continue
		}
		var cues legacyCues
		if p := str(t["cues_path"]); p != "" {
			if b, err := os.ReadFile(filepath.Join(opts.AnalysisDir, p)); err == nil {
				_ = json.Unmarshal(b, &cues)
			}
		}
		match, matched := byName[str(t["basename"])]
		cat := legacyCatalog(t, grid, cues, match)
		id := grid.FileHash
		if matched {
			id = match.ID
		}
		if id == "" {
			id = catalog.NewUUID()
		}
		out := filepath.Join(opts.Out, "tracks", id+".cdb")
		verb := "wrote"
		if _, err := os.Stat(out); err == nil {
			merged, err := merge(ctx, out, cat)
			if err != nil {
				return rep, errors.Wrapf(err, "merge into %s", out)
			}
			if !merged {
				rep.Skipped++
				continue
			}
			rep.Merged++
			verb = "merged"
		} else {
			file, err := catalog.CreateFile(out, cat)
			if err != nil {
				return rep, errors.Wrapf(err, "write %s", out)
			}
			_ = file.Close()
			rep.Written++
		}
		if opts.Log != nil {
			opts.Log(fmt.Sprintf("%s  %s  %s  %.2f bpm", verb, id[:min(12, len(id))], str(t["title"]), grid.BPM))
		}
	}
	if _, err := Reindex(ctx, opts.Out, opts.Contributor); err != nil {
		return rep, err
	}
	return rep, nil
}

// legacyCatalog builds the track-local catalog for one legacy grid.
func legacyCatalog(t map[string]any, grid legacyGrid, cues legacyCues, match Entry) catalog.Catalog {
	now := time.Now().UTC().Format(time.RFC3339)
	trackUUID := catalog.NewUUID()
	catalogUUID := catalog.NewUUID()
	var tables []catalog.Table
	artist := str(t["artist"])
	if artist != "" {
		tables = append(tables, catalog.Table{Name: "artists", Rows: []map[string]any{{"id": 1, "name": artist}}})
	}
	lengthMs, _ := t["length_ms"].(int64)
	track := map[string]any{
		"uuid":       trackUUID,
		"title":      str(t["title"]),
		"duration":   lengthMs / 1000,
		"tempo":      int64(math.Round(grid.BPM * 100)),
		"date_added": firstNonEmpty(str(t["date_added"]), str(t["created_at"]), now),
	}
	if match.FileName != "" {
		track["file_name"] = match.FileName
	}
	if artist != "" {
		track["artist_id"] = 1
	}
	if c := str(t["comment"]); c != "" {
		track["comment"] = c
	}
	tables = append(tables, catalog.Table{Name: "tracks", Rows: []map[string]any{track}})
	tables = append(tables, catalog.Table{Name: "catalogs", Rows: []map[string]any{{
		"created_at": now, "kind": "corpus", "name": "deadca7-legacy", "updated_at": now, "uuid": catalogUUID,
	}}})
	external := map[string]any{"deadca7_track_id": t["id"]}
	if h := str(t["audio_hash"]); h != "" {
		external["deadca7_audio_hash"] = h
	}
	if r := str(t["rekordbox_id"]); r != "" {
		external["deadca7_rekordbox_alias"] = r
	}
	ext, _ := json.Marshal(external)
	obs := map[string]any{
		"uuid": catalog.NewUUID(), "catalog_uuid": catalogUUID, "track_uuid": trackUUID, "provider": "deadca7",
		"title": str(t["title"]), "duration": lengthMs / 1000, "observed_at": now, "external_ids": string(ext),
	}
	if grid.FileHash != "" {
		obs["file_sha256"] = grid.FileHash
	}
	if match.FileName != "" {
		obs["file_name"] = match.FileName
	}
	tables = append(tables, catalog.Table{Name: "catalog_tracks", Rows: []map[string]any{obs}})

	analysis := map[string]any{
		"id": 1, "uuid": catalog.NewUUID(), "track_uuid": trackUUID, "provider": "deadca7", "version": "legacy",
		"date": firstNonEmpty(str(t["created_at"]), now), "selected": 1,
	}
	if match.FileName != "" {
		analysis["audio_path"] = match.FileName
	}
	tables = append(tables, catalog.Table{Name: "analyses", Rows: []map[string]any{analysis}})
	beats, _ := catalog.EncodeBeatTuples(legacyTuples(grid))
	tables = append(tables, catalog.Table{Name: "beats", Rows: []map[string]any{{"analysis_id": 1, "beats_json": beats}}})
	var cueRows []map[string]any
	for i, c := range cues.Cues {
		cueRows = append(cueRows, map[string]any{
			"analysis_id": 1, "cue_index": i, "kind": "cue", "hot_cue": c.HotCue, "time_ms": int64(math.Round(c.Time * 1000)),
			"comment": c.Comment, "source": "deadca7:legacy",
		})
	}
	if len(cueRows) > 0 {
		tables = append(tables, catalog.Table{Name: "cues", Rows: cueRows})
	}
	meta, _ := json.Marshal(map[string]any{
		"algo_version": grid.AlgoVersion, "cfg_hash": grid.CfgHash, "cfg": grid.Cfg, "bpm": grid.BPM, "raw_bpm": grid.RawBPM,
		"duration": grid.Duration, "dynamic": grid.Dynamic, "file_hash": grid.FileHash, "samples_hash": grid.SamplesHash,
		"origin_refine_ms": grid.OriginRefineMs, "anchor": grid.Anchor, "phase": grid.Phase, "source": str(t["beats_src"]),
	})
	tables = append(tables, catalog.Table{Name: "analysis_files", Rows: []map[string]any{{"analysis_id": 1, "kind": "grid", "data": meta}}})
	return catalog.Catalog{Tables: tables}
}

// legacyTuples turns beat and downbeat seconds into the catalog's
// [beat_number, tempo_x100, time_ms] tuples. A beat is numbered 1 when a
// downbeat falls on it, else one past the beat before.
func legacyTuples(grid legacyGrid) []catalog.BeatTuple {
	tempo := int64(math.Round(grid.BPM * 100))
	down := map[int64]bool{}
	for _, d := range grid.Downbeats {
		down[int64(math.Round(d*1000))] = true
	}
	tuples := make([]catalog.BeatTuple, 0, len(grid.Beats))
	number := int64(0)
	for _, b := range grid.Beats {
		ms := int64(math.Round(b * 1000))
		if down[ms] || number == 0 {
			number = 1
		} else {
			number = number%4 + 1
		}
		tuples = append(tuples, catalog.BeatTuple{number, tempo, ms})
	}
	return tuples
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]`)

// NormalizeName is DEADCA7's file-name match key: the base name without its
// extension, lower-cased, letters and digits only.
func NormalizeName(name string) string {
	base := filepath.Base(name)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	return nonAlnum.ReplaceAllString(strings.ToLower(base), "")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
