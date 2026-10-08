// Package corpus is the grid corpus: one track-local catalog per track, in
// the deadcatalog schema, holding every analysis anyone made of that audio
// (rekordbox's grid from a stick or the desktop, deadca7's grid, a
// contributor's) and none of the audio.
//
// A corpus is a directory:
//
//	index.json          one Entry per track: who it is, what grids it carries
//	tracks/<id>.cdb     the track-local catalog; <id> is the audio's sha256
//	                    when the source knew it, else the track's uuid
//
// A track catalog is a deadcatalog .cdb (schema in deadcatalog/catalog/schema)
// with: the tracks row and its artist and album; one catalogs row of kind
// "corpus" naming the source it came from, with the catalog_tracks
// observation under it (file name, size, sha256, rekordbox ids); and every
// analyses row for the track with its beats, cues, phrases, vbr_info and the
// analysis_files that carry data (grid, key, features). Waveforms are kept on
// request: they are most of the bytes and no grid question needs them.
// Absolute paths are stripped unless asked for, so a corpus can be shared.
package corpus

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
)

// Entry is one track in the index.
type Entry struct {
	ID          string `json:"id"`
	File        string `json:"file"`
	Title       string `json:"title,omitempty"`
	Artist      string `json:"artist,omitempty"`
	FileName    string `json:"file_name,omitempty"`
	DurationS   int64  `json:"duration_s,omitempty"`
	Source      string `json:"source,omitempty"`
	Contributor string `json:"contributor,omitempty"`
	Added       string `json:"added"`
	Grids       []Grid `json:"grids"`
}

// Grid is one analysis of the track, reduced to what a grid comparison needs.
type Grid struct {
	Provider        string  `json:"provider"`
	Version         string  `json:"version,omitempty"`
	Algo            string  `json:"algo,omitempty"`
	CfgHash         string  `json:"cfg_hash,omitempty"`
	BPM             float64 `json:"bpm"`
	FirstDownbeatMs int     `json:"first_downbeat_ms"`
	BeatsPerBar     int     `json:"beats_per_bar"`
	Beats           int     `json:"beats"`
	Cues            int     `json:"cues"`
	Dynamic         bool    `json:"dynamic,omitempty"`
	Selected        bool    `json:"selected,omitempty"`
}

// Index is index.json.
type Index struct {
	Format  int     `json:"format"`
	Tracks  []Entry `json:"tracks"`
	Updated string  `json:"updated"`
}

const Format = 1

// Options steer Build.
type Options struct {
	// From is the source catalog (.cdb): a library, a stick, a sidecar.
	From string
	// Out is the corpus directory.
	Out string
	// Providers keeps only these analyses (empty: all).
	Providers []string
	// Waveforms keeps the waveforms table.
	Waveforms bool
	// KeepPaths keeps absolute audio paths in tracks and analyses.
	KeepPaths bool
	// HashAudio names a track by its audio file's sha256 when the source
	// has no hash but the file is reachable (a mounted stick); the hash is
	// the container file's, the one a library import records as
	// file_sha256, so the same file gets the same id from either side.
	HashAudio bool
	// Force rewrites a track catalog that is already in the corpus instead
	// of merging the source's analyses into it.
	Force bool
	// Contributor is recorded on each entry.
	Contributor string
	// Limit stops after this many tracks (0: all); for trials.
	Limit int
	// Log receives one line per track when set.
	Log func(string)
}

// Report says what Build did.
type Report struct {
	Tracks, Written, Merged, Skipped, NoAnalysis int
}

// Build adds every analysed track of a source catalog to the corpus.
func Build(ctx context.Context, opts Options) (Report, error) {
	var rep Report
	if opts.From == "" || opts.Out == "" {
		return rep, errors.New("corpus: -from and -out are required")
	}
	src, err := openReadOnly(opts.From)
	if err != nil {
		return rep, err
	}
	defer src.Close()
	if err := os.MkdirAll(filepath.Join(opts.Out, "tracks"), 0o755); err != nil {
		return rep, err
	}
	tracks, err := rows(ctx, src, `SELECT * FROM tracks ORDER BY rowid`)
	if err != nil {
		return rep, err
	}
	sourceName := strings.TrimSuffix(filepath.Base(opts.From), filepath.Ext(opts.From))
	for _, track := range tracks {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		if opts.Limit > 0 && rep.Tracks >= opts.Limit {
			break
		}
		rep.Tracks++
		uuid := str(track["uuid"])
		analyses, err := rows(ctx, src, `SELECT * FROM analyses WHERE track_uuid = ? ORDER BY id`, uuid)
		if err != nil {
			return rep, err
		}
		analyses = filterProviders(analyses, opts.Providers)
		if len(analyses) == 0 {
			rep.NoAnalysis++
			continue
		}
		observations, err := rows(ctx, src, `SELECT * FROM catalog_tracks WHERE track_uuid = ? ORDER BY observed_at`, uuid)
		if err != nil {
			return rep, err
		}
		id := trackID(track, append(observations, analyses...), opts.HashAudio)
		out := filepath.Join(opts.Out, "tracks", id+".cdb")
		cat, err := trackCatalog(ctx, src, track, analyses, observations, sourceName, opts)
		if err != nil {
			return rep, errors.Wrapf(err, "track %s", uuid)
		}
		verb := "wrote"
		if _, err := os.Stat(out); err == nil && !opts.Force {
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
			if err == nil {
				if err := os.Remove(out); err != nil {
					return rep, err
				}
			}
			db, err := createFile(out, cat)
			if err != nil {
				return rep, errors.Wrapf(err, "write %s", out)
			}
			_ = db.Close()
			rep.Written++
		}
		if opts.Log != nil {
			opts.Log(fmt.Sprintf("%s  %s  %s  %d analyses", verb, id[:min(12, len(id))], str(track["title"]), len(analyses)))
		}
	}
	if _, err := Reindex(ctx, opts.Out, opts.Contributor); err != nil {
		return rep, err
	}
	return rep, nil
}

// trackCatalog assembles the tables of one track-local catalog.
func trackCatalog(ctx context.Context, src *sql.DB, track map[string]any, analyses, observations []map[string]any, sourceName string, opts Options) (contents, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	var tables []table
	for _, ref := range []struct{ table, col string }{{"artists", "artist_id"}, {"albums", "album_id"}} {
		if id, ok := track[ref.col].(int64); ok {
			r, err := rows(ctx, src, `SELECT * FROM `+ref.table+` WHERE id = ?`, id)
			if err != nil {
				return contents{}, err
			}
			if len(r) > 0 {
				tables = append(tables, table{Name: ref.table, Rows: r})
			}
		}
	}
	// Albums carry an artist of their own; keep the referenced one too.
	if album := findTable(tables, "albums"); album != nil {
		if id, ok := album.Rows[0]["artist_id"].(int64); ok && !hasRow(findTable(tables, "artists"), id) {
			r, err := rows(ctx, src, `SELECT * FROM artists WHERE id = ?`, id)
			if err != nil {
				return contents{}, err
			}
			if a := findTable(tables, "artists"); a != nil {
				a.Rows = append(a.Rows, r...)
			} else if len(r) > 0 {
				tables = append(tables, table{Name: "artists", Rows: r})
			}
		}
	}
	track = clone(track)
	// composer_id and original_artist_id point at artists this catalog does
	// not carry; the corpus is about the grid, not the credits.
	delete(track, "composer_id")
	delete(track, "original_artist_id")
	delete(track, "remixer_id")
	if !opts.KeepPaths {
		if p := str(track["file_path"]); p != "" {
			track["file_path"] = nil
			if str(track["file_name"]) == "" {
				track["file_name"] = filepath.Base(p)
			}
		}
	}
	tables = append(tables, table{Name: "tracks", Rows: []map[string]any{track}})

	// One corpus catalog stands for the source; the observations hang off it.
	catalogUUID := newUUID()
	tables = append(tables, table{Name: "catalogs", Rows: []map[string]any{{
		"created_at": now, "kind": "corpus", "name": sourceName, "updated_at": now, "uuid": catalogUUID,
	}}})
	var obs []map[string]any
	for _, o := range observations {
		o = clone(o)
		o["catalog_uuid"] = catalogUUID
		if !opts.KeepPaths {
			if p := str(o["file_path"]); p != "" {
				o["file_path"] = nil
				if str(o["file_name"]) == "" {
					o["file_name"] = filepath.Base(p)
				}
			}
			o["file_uri"] = nil
		}
		obs = append(obs, o)
	}
	if len(obs) > 0 {
		tables = append(tables, table{Name: "catalog_tracks", Rows: obs})
	}

	var an []map[string]any
	for _, a := range analyses {
		a = clone(a)
		if !opts.KeepPaths {
			if p := str(a["audio_path"]); p != "" {
				a["audio_path"] = filepath.Base(p)
			}
		}
		an = append(an, a)
	}
	tables = append(tables, table{Name: "analyses", Rows: an})
	ids := make([]any, 0, len(an))
	marks := make([]string, 0, len(an))
	for _, a := range an {
		ids = append(ids, a["id"])
		marks = append(marks, "?")
	}
	in := "(" + strings.Join(marks, ",") + ")"
	children := []string{"beats", "cues", "phrases", "vbr_info"}
	if opts.Waveforms {
		children = append(children, "waveforms")
	}
	for _, name := range children {
		r, err := rows(ctx, src, `SELECT * FROM `+name+` WHERE analysis_id IN `+in, ids...)
		if err != nil {
			return contents{}, err
		}
		if len(r) > 0 {
			tables = append(tables, table{Name: name, Rows: r})
		}
	}
	// analysis_files without data name files on the source device (the
	// ANLZ set a stick carries); nothing portable is lost dropping them.
	files, err := rows(ctx, src, `SELECT * FROM analysis_files WHERE analysis_id IN `+in+` AND data IS NOT NULL`, ids...)
	if err != nil {
		return contents{}, err
	}
	if len(files) > 0 {
		for _, f := range files {
			if !opts.KeepPaths {
				if p := str(f["path"]); p != "" {
					f["path"] = filepath.Base(p)
				}
			}
		}
		tables = append(tables, table{Name: "analysis_files", Rows: files})
	}
	return contents{Tables: tables}, nil
}

// trackID is the audio file's sha256 when any observation knows it, else
// the file's own hash when asked and the file is reachable, else the track
// uuid.
func trackID(track map[string]any, observations []map[string]any, hashAudio bool) string {
	for _, o := range observations {
		for _, col := range []string{"file_sha256", "audio_sha256"} {
			if s := str(o[col]); s != "" {
				return s
			}
		}
	}
	if hashAudio {
		for _, o := range append(observations, track) {
			for _, candidate := range []string{str(o["file_path"]), str(o["audio_path"]), pathOfURI(str(o["file_uri"]))} {
				if candidate == "" {
					continue
				}
				if sum, err := fileSHA256(candidate); err == nil {
					return sum
				}
			}
		}
	}
	return str(track["uuid"])
}

func pathOfURI(uri string) string {
	if !strings.HasPrefix(uri, "file://") {
		return ""
	}
	u, err := url.Parse(uri)
	if err != nil {
		return ""
	}
	return u.Path
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// merge adds a source's analyses to a track catalog already in the corpus:
// every (provider, version) the catalog does not have yet, with its children,
// and the source's catalogs row with its observations. Analyses the catalog
// already carries are left alone, so a rebuild is idempotent. It reports
// false when there was nothing new.
func merge(ctx context.Context, path string, incoming contents) (bool, error) {
	db, err := openReadOnly(path)
	if err != nil {
		return false, err
	}
	existing := contents{}
	for _, name := range []string{"artists", "albums", "tracks", "catalogs", "catalog_tracks", "analyses", "beats", "cues", "phrases", "vbr_info", "waveforms", "analysis_files"} {
		query := `SELECT * FROM ` + name
		if name == "catalogs" {
			query += ` WHERE kind IS NOT 'main'`
		}
		r, err := rows(ctx, db, query)
		if err != nil {
			db.Close()
			return false, err
		}
		if len(r) > 0 {
			existing.Tables = append(existing.Tables, table{Name: name, Rows: r})
		}
	}
	db.Close()

	// The corpus track keeps its uuid; everything incoming hangs off it.
	trackUUID := ""
	if t := findTable(existing.Tables, "tracks"); t != nil && len(t.Rows) > 0 {
		trackUUID = str(t.Rows[0]["uuid"])
	}
	if trackUUID == "" {
		return false, errors.Newf("%s has no track row", path)
	}
	have := map[string]bool{}
	maxID := int64(0)
	if t := findTable(existing.Tables, "analyses"); t != nil {
		for _, a := range t.Rows {
			have[str(a["provider"])+"\x00"+str(a["version"])] = true
			if id := asInt64(a["id"]); id > maxID {
				maxID = id
			}
		}
	}
	var fresh []map[string]any
	renumber := map[int64]int64{}
	if t := findTable(incoming.Tables, "analyses"); t != nil {
		for _, a := range t.Rows {
			if have[str(a["provider"])+"\x00"+str(a["version"])] {
				continue
			}
			a = clone(a)
			old := asInt64(a["id"])
			maxID++
			renumber[old] = maxID
			a["id"] = maxID
			a["track_uuid"] = trackUUID
			// The merged catalog keeps one selected analysis: the one it had.
			a["selected"] = 0
			fresh = append(fresh, a)
		}
	}
	if len(fresh) == 0 {
		return false, nil
	}
	appendRows := func(name string, rows []map[string]any) {
		if t := findTable(existing.Tables, name); t != nil {
			t.Rows = append(t.Rows, rows...)
		} else {
			existing.Tables = append(existing.Tables, table{Name: name, Rows: rows})
		}
	}
	appendRows("analyses", fresh)
	for _, name := range []string{"beats", "cues", "phrases", "vbr_info", "waveforms", "analysis_files"} {
		t := findTable(incoming.Tables, name)
		if t == nil {
			continue
		}
		var kept []map[string]any
		for _, r := range t.Rows {
			if id, ok := renumber[asInt64(r["analysis_id"])]; ok {
				r = clone(r)
				r["analysis_id"] = id
				kept = append(kept, r)
			}
		}
		if len(kept) > 0 {
			appendRows(name, kept)
		}
	}
	// The source's own row and observations; the track row stays the one
	// the corpus had.
	if t := findTable(incoming.Tables, "catalogs"); t != nil {
		appendRows("catalogs", t.Rows)
	}
	if t := findTable(incoming.Tables, "catalog_tracks"); t != nil {
		var obs []map[string]any
		for _, o := range t.Rows {
			o = clone(o)
			o["track_uuid"] = trackUUID
			obs = append(obs, o)
		}
		appendRows("catalog_tracks", obs)
	}
	// tracks before catalogs before analyses: the loader inserts in table
	// order and the foreign keys point up the list.
	order := map[string]int{"artists": 0, "albums": 1, "tracks": 2, "catalogs": 3, "catalog_tracks": 4, "analyses": 5}
	sort.SliceStable(existing.Tables, func(i, j int) bool {
		oi, oki := order[existing.Tables[i].Name]
		oj, okj := order[existing.Tables[j].Name]
		if !oki {
			oi = 9
		}
		if !okj {
			oj = 9
		}
		return oi < oj
	})
	tmp := path + ".merge"
	_ = os.Remove(tmp)
	out, err := createFile(tmp, existing)
	if err != nil {
		return false, err
	}
	if err := out.Close(); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, path)
}

// Reindex rebuilds index.json from the track catalogs.
func Reindex(ctx context.Context, dir, contributor string) (Index, error) {
	existing, _ := Load(dir)
	byID := map[string]Entry{}
	for _, e := range existing.Tracks {
		byID[e.ID] = e
	}
	paths, err := filepath.Glob(filepath.Join(dir, "tracks", "*.cdb"))
	if err != nil {
		return Index{}, err
	}
	sort.Strings(paths)
	idx := Index{Format: Format, Updated: time.Now().UTC().Format(time.RFC3339)}
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return Index{}, err
		}
		e, err := Describe(ctx, p)
		if err != nil {
			return Index{}, errors.Wrapf(err, "%s", p)
		}
		if old, ok := byID[e.ID]; ok {
			e.Added, e.Contributor = old.Added, old.Contributor
		} else {
			e.Added = idx.Updated
			e.Contributor = contributor
		}
		if e.Contributor == "" {
			e.Contributor = contributor
		}
		idx.Tracks = append(idx.Tracks, e)
	}
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return Index{}, err
	}
	return idx, os.WriteFile(filepath.Join(dir, "index.json"), append(b, '\n'), 0o644)
}

// Load reads index.json.
func Load(dir string) (Index, error) {
	b, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		return Index{}, err
	}
	var idx Index
	if err := json.Unmarshal(b, &idx); err != nil {
		return Index{}, errors.Wrapf(err, "%s", filepath.Join(dir, "index.json"))
	}
	return idx, nil
}

// Describe reads one track catalog into an Entry.
func Describe(ctx context.Context, path string) (Entry, error) {
	db, err := openReadOnly(path)
	if err != nil {
		return Entry{}, err
	}
	defer db.Close()
	e := Entry{ID: strings.TrimSuffix(filepath.Base(path), ".cdb"), File: filepath.Join("tracks", filepath.Base(path))}
	tracks, err := rows(ctx, db, `SELECT t.title, t.file_name, t.duration, a.name AS artist FROM tracks t LEFT JOIN artists a ON a.id = t.artist_id LIMIT 1`)
	if err != nil {
		return Entry{}, err
	}
	if len(tracks) == 1 {
		t := tracks[0]
		e.Title, e.Artist, e.FileName = str(t["title"]), str(t["artist"]), str(t["file_name"])
		e.DurationS, _ = t["duration"].(int64)
	}
	cats, err := rows(ctx, db, `SELECT name FROM catalogs WHERE kind = 'corpus' LIMIT 1`)
	if err != nil {
		return Entry{}, err
	}
	if len(cats) == 1 {
		e.Source = str(cats[0]["name"])
	}
	grids, err := Grids(ctx, db)
	if err != nil {
		return Entry{}, err
	}
	e.Grids = grids
	return e, nil
}

// Grids reads every analysis of an open track catalog.
func Grids(ctx context.Context, db *sql.DB) ([]Grid, error) {
	an, err := rows(ctx, db, `SELECT a.id, a.provider, a.version, a.selected, b.beats_json,
		(SELECT count(*) FROM cues c WHERE c.analysis_id = a.id) AS cues,
		(SELECT data FROM analysis_files f WHERE f.analysis_id = a.id AND f.kind = 'grid') AS grid
		FROM analyses a LEFT JOIN beats b ON b.analysis_id = a.id ORDER BY a.id`)
	if err != nil {
		return nil, err
	}
	var grids []Grid
	for _, a := range an {
		g := Grid{Provider: str(a["provider"]), Version: str(a["version"])}
		if sel, ok := a["selected"].(int64); ok {
			g.Selected = sel == 1
		}
		g.Cues = int(a["cues"].(int64))
		if g.Provider == "rekordbox" {
			g.Algo = "rekordbox"
		}
		if raw, ok := a["grid"].([]byte); ok && len(raw) > 0 {
			var meta struct {
				Algo    string `json:"algo_version"`
				CfgHash string `json:"cfg_hash"`
			}
			if json.Unmarshal(raw, &meta) == nil {
				g.Algo, g.CfgHash = meta.Algo, meta.CfgHash
			}
		}
		if beats := str(a["beats_json"]); beats != "" {
			tuples, err := DecodeBeatTuples(beats)
			if err != nil {
				return nil, err
			}
			fromBeats(&g, tuples)
		}
		grids = append(grids, g)
	}
	return grids, nil
}

// fromBeats reads bpm, first downbeat and meter off the beat tuples
// [beat_number, tempo_x100, time_ms]. A grid need not list a beat at its
// bar 1: rekordbox's often opens on beat 2 or 3 of a bar whose downbeat is
// before the first listed beat. The first downbeat is that bar's, laid back
// from the first listed beat by its number, so a lattice that agrees beat
// for beat scores as agreeing; it can be negative.
func fromBeats(g *Grid, tuples []BeatTuple) {
	g.Beats = len(tuples)
	if len(tuples) == 0 {
		return
	}
	g.BPM = float64(tuples[0][1]) / 100
	for _, t := range tuples {
		if t[1] != tuples[0][1] {
			g.Dynamic = true
		}
		if int(t[0]) > g.BeatsPerBar {
			g.BeatsPerBar = int(t[0])
		}
	}
	first := tuples[0]
	if first[0] <= 1 || g.BPM <= 0 {
		g.FirstDownbeatMs = int(first[2])
		return
	}
	period := 60000 / g.BPM
	g.FirstDownbeatMs = int(math.Round(float64(first[2]) - float64(first[0]-1)*period))
}

// Beats reads one analysis's beat tuples from an open track catalog.
func Beats(ctx context.Context, db *sql.DB, provider string) ([]BeatTuple, Grid, error) {
	an, err := rows(ctx, db, `SELECT a.id, a.provider, a.version, b.beats_json FROM analyses a JOIN beats b ON b.analysis_id = a.id WHERE a.provider = ? ORDER BY a.selected DESC, a.id DESC LIMIT 1`, provider)
	if err != nil {
		return nil, Grid{}, err
	}
	if len(an) == 0 {
		return nil, Grid{}, nil
	}
	tuples, err := DecodeBeatTuples(str(an[0]["beats_json"]))
	if err != nil {
		return nil, Grid{}, err
	}
	g := Grid{Provider: provider, Version: str(an[0]["version"])}
	fromBeats(&g, tuples)
	return tuples, g, nil
}

// Open opens a track catalog read-only.
func Open(path string) (*sql.DB, error) { return openReadOnly(path) }

func filterProviders(analyses []map[string]any, providers []string) []map[string]any {
	if len(providers) == 0 {
		return analyses
	}
	var kept []map[string]any
	for _, a := range analyses {
		for _, p := range providers {
			if str(a["provider"]) == p {
				kept = append(kept, a)
				break
			}
		}
	}
	return kept
}

// rows runs a query into generic rows, the shape table takes.
func rows(ctx context.Context, db *sql.DB, query string, args ...any) ([]map[string]any, error) {
	rs, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, errors.Wrapf(err, "%s", query)
	}
	defer rs.Close()
	cols, err := rs.Columns()
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for rs.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(cols))
		for i, c := range cols {
			if vals[i] != nil {
				row[c] = vals[i]
			}
		}
		out = append(out, row)
	}
	return out, rs.Err()
}

// asInt64 reads an id whether it came from SQLite (int64) or Go (int).
func asInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

func str(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case []byte:
		return string(s)
	}
	return ""
}

func clone(m map[string]any) map[string]any {
	c := make(map[string]any, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

func findTable(tables []table, name string) *table {
	for i := range tables {
		if tables[i].Name == name {
			return &tables[i]
		}
	}
	return nil
}

func hasRow(t *table, id int64) bool {
	if t == nil {
		return false
	}
	for _, r := range t.Rows {
		if v, ok := r["id"].(int64); ok && v == id {
			return true
		}
	}
	return false
}
