package corpus

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/csquared/deadcatalog/catalog"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// A library with one track analysed twice: rekordbox's grid (bar 1 at 49ms,
// 117 BPM) and deadca7's (one beat late), plus a track nobody analysed.
func library(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Library.cdb")
	beats := func(firstMs, tempoX100 int64, n int) string {
		var tuples []catalog.BeatTuple
		period := 6000000 / tempoX100
		for i := 0; i < n; i++ {
			tuples = append(tuples, catalog.BeatTuple{int64(i%4 + 1), tempoX100, firstMs + int64(i)*period})
		}
		s, err := catalog.EncodeBeatTuples(tuples)
		require.NoError(t, err)
		return s
	}
	catalogUUID := catalog.NewUUID()
	cat := catalog.Catalog{Tables: []catalog.Table{
		{Name: "artists", Rows: []map[string]any{{"id": 1, "name": "Night Shift"}}},
		{Name: "tracks", Rows: []map[string]any{
			{"uuid": "t1", "title": "Alive", "artist_id": 1, "file_name": "01 - Alive.mp3", "file_path": "/Users/x/Music/01 - Alive.mp3", "duration": 180, "tempo": 11700},
			{"uuid": "t2", "title": "Silent", "file_name": "02 - Silent.mp3"},
		}},
		{Name: "catalogs", Rows: []map[string]any{{"uuid": catalogUUID, "kind": "devicelib", "name": "STICK", "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"}}},
		{Name: "catalog_tracks", Rows: []map[string]any{{"uuid": "ct1", "catalog_uuid": catalogUUID, "track_uuid": "t1", "provider": "rekordbox", "audio_sha256": "abc123", "file_path": "/Volumes/STICK/Contents/01 - Alive.mp3", "file_uri": "file:///Volumes/STICK/Contents/01%20-%20Alive.mp3"}}},
		{Name: "analyses", Rows: []map[string]any{
			{"id": 1, "uuid": "a1", "track_uuid": "t1", "provider": "rekordbox", "version": "6", "audio_path": "/Volumes/STICK/Contents/01 - Alive.mp3", "selected": 0},
			{"id": 2, "uuid": "a2", "track_uuid": "t1", "provider": "deadca7", "version": "1", "audio_path": "/Users/x/Music/01 - Alive.mp3", "selected": 1},
		}},
		{Name: "beats", Rows: []map[string]any{
			{"analysis_id": 1, "beats_json": beats(49, 11700, 64)},
			{"analysis_id": 2, "beats_json": beats(49+512, 11700, 64)},
		}},
		{Name: "cues", Rows: []map[string]any{{"analysis_id": 2, "cue_index": 0, "kind": "cue", "time_ms": 49, "hot_cue": 1}}},
		{Name: "analysis_files", Rows: []map[string]any{
			{"analysis_id": 1, "kind": "dat", "path": "/Volumes/STICK/PIONEER/USBANLZ/P01/ANLZ0000.DAT"},
			{"analysis_id": 2, "kind": "grid", "data": []byte(`{"algo_version":"beatnet-dbn-v14","cfg_hash":"deadbeef"}`)},
		}},
		{Name: "waveforms", Rows: []map[string]any{{"analysis_id": 1, "waveform_id": 1, "kind": "mono_preview", "data": make([]byte, 400), "entry_bytes": 1, "entry_count": 400, "rate": 150}}},
	}}
	db, err := catalog.CreateFile(path, cat)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	return path
}

func TestBuildAndIndex(t *testing.T) {
	ctx := context.Background()
	out := filepath.Join(t.TempDir(), "grids")
	rep, err := Build(ctx, Options{From: library(t), Out: out, Contributor: "test"})
	require.NoError(t, err)
	require.Equal(t, Report{Tracks: 2, Written: 1, NoAnalysis: 1}, rep)

	// The track is named by its audio sha256, and the index describes it.
	require.FileExists(t, filepath.Join(out, "tracks", "abc123.cdb"))
	idx, err := Load(out)
	require.NoError(t, err)
	require.Len(t, idx.Tracks, 1)
	e := idx.Tracks[0]
	require.Equal(t, "abc123", e.ID)
	require.Equal(t, "Alive", e.Title)
	require.Equal(t, "Night Shift", e.Artist)
	require.Equal(t, "01 - Alive.mp3", e.FileName)
	require.Equal(t, "Library", e.Source)
	require.Equal(t, "test", e.Contributor)
	require.Len(t, e.Grids, 2)
	rb, dc := e.Grids[0], e.Grids[1]
	require.Equal(t, Grid{Provider: "rekordbox", Version: "6", Algo: "rekordbox", BPM: 117, FirstDownbeatMs: 49, BeatsPerBar: 4, Beats: 64}, rb)
	require.Equal(t, Grid{Provider: "deadca7", Version: "1", Algo: "beatnet-dbn-v14", CfgHash: "deadbeef", BPM: 117, FirstDownbeatMs: 561, BeatsPerBar: 4, Beats: 64, Cues: 1, Selected: true}, dc)

	// No absolute paths, no device file references, no waveforms by default.
	db, err := Open(filepath.Join(out, "tracks", "abc123.cdb"))
	require.NoError(t, err)
	defer db.Close()
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM tracks WHERE file_path IS NOT NULL`).Scan(&n))
	require.Equal(t, 0, n)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM catalog_tracks WHERE file_path IS NOT NULL OR file_uri IS NOT NULL`).Scan(&n))
	require.Equal(t, 0, n)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM analyses WHERE audio_path LIKE '/%'`).Scan(&n))
	require.Equal(t, 0, n)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM analysis_files`).Scan(&n))
	require.Equal(t, 1, n, "only the file that carries data")
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM waveforms`).Scan(&n))
	require.Equal(t, 0, n)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM catalogs WHERE kind = 'corpus'`).Scan(&n))
	require.Equal(t, 1, n)

	// Beats reads the stored grid back for scoring.
	tuples, g, err := Beats(ctx, db, "rekordbox")
	require.NoError(t, err)
	require.Len(t, tuples, 64)
	require.Equal(t, 49, g.FirstDownbeatMs)

	// A second build skips what is there; -force rewrites and keeps the entry's provenance.
	rep, err = Build(ctx, Options{From: library(t), Out: out})
	require.NoError(t, err)
	require.Equal(t, 1, rep.Skipped)
	rep, err = Build(ctx, Options{From: library(t), Out: out, Force: true, Contributor: "other"})
	require.NoError(t, err)
	require.Equal(t, 1, rep.Written)
	idx, err = Load(out)
	require.NoError(t, err)
	require.Equal(t, "test", idx.Tracks[0].Contributor)
	require.Equal(t, e.Added, idx.Tracks[0].Added)
}

// A stick catalog of the same file: rekordbox's grid only, no hash, but the
// file itself is where the stick says.
func stick(t *testing.T, audio string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "STICK.cdb")
	catalogUUID := catalog.NewUUID()
	cat := catalog.Catalog{Tables: []catalog.Table{
		{Name: "tracks", Rows: []map[string]any{{"uuid": "s1", "title": "Alive", "file_name": filepath.Base(audio), "duration": 180}}},
		{Name: "catalogs", Rows: []map[string]any{{"uuid": catalogUUID, "kind": "devicelib", "name": "STICK", "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"}}},
		{Name: "catalog_tracks", Rows: []map[string]any{{"uuid": "cs1", "catalog_uuid": catalogUUID, "track_uuid": "s1", "provider": "rekordbox", "file_path": audio}}},
		{Name: "analyses", Rows: []map[string]any{{"id": 7, "uuid": "sa1", "track_uuid": "s1", "provider": "rekordbox", "version": "7", "selected": 1}}},
		{Name: "beats", Rows: []map[string]any{{"analysis_id": 7, "beats_json": `[[1,11700,49],[2,11700,561],[3,11700,1074],[4,11700,1587]]`}}},
		{Name: "cues", Rows: []map[string]any{{"analysis_id": 7, "cue_index": 0, "kind": "cue", "time_ms": 49, "hot_cue": 0}}},
	}}
	db, err := catalog.CreateFile(path, cat)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	return path
}

func TestMergeAndHash(t *testing.T) {
	ctx := context.Background()
	out := filepath.Join(t.TempDir(), "grids")
	// The library knows the file's hash; the stick does not, but the file is here.
	audio := filepath.Join(t.TempDir(), "01 - Alive.mp3")
	require.NoError(t, os.WriteFile(audio, []byte("not really audio"), 0o644))
	sum := sha256.Sum256([]byte("not really audio"))
	id := hex.EncodeToString(sum[:])

	_, err := Build(ctx, Options{From: stick(t, audio), Out: out, HashAudio: true})
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(out, "tracks", id+".cdb"), "named by the file's hash")

	// The same stick again: nothing new, nothing rewritten.
	rep, err := Build(ctx, Options{From: stick(t, audio), Out: out, HashAudio: true})
	require.NoError(t, err)
	require.Equal(t, Report{Tracks: 1, Skipped: 1}, rep)

	// A library whose observation carries that hash merges its deadca7 grid in.
	lib := library(t)
	db, err := catalog.OpenReadOnly(lib)
	require.NoError(t, err)
	db.Close()
	rewrite(t, lib, `UPDATE catalog_tracks SET audio_sha256 = NULL, file_sha256 = ?`, id)
	rep, err = Build(ctx, Options{From: lib, Out: out})
	require.NoError(t, err)
	require.Equal(t, Report{Tracks: 2, Merged: 1, NoAnalysis: 1}, rep)

	idx, err := Load(out)
	require.NoError(t, err)
	require.Len(t, idx.Tracks, 1)
	providers := map[string]int{}
	for _, g := range idx.Tracks[0].Grids {
		providers[g.Provider+"-"+g.Version]++
	}
	require.Equal(t, map[string]int{"rekordbox-7": 1, "rekordbox-6": 1, "deadca7-1": 1}, providers)

	tdb, err := Open(filepath.Join(out, "tracks", id+".cdb"))
	require.NoError(t, err)
	defer tdb.Close()
	var n int
	require.NoError(t, tdb.QueryRow(`SELECT count(*) FROM analyses WHERE selected = 1`).Scan(&n))
	require.Equal(t, 1, n, "one selected analysis after a merge")
	require.NoError(t, tdb.QueryRow(`SELECT count(*) FROM catalogs WHERE kind = 'corpus'`).Scan(&n))
	require.Equal(t, 2, n, "both sources are named")
	require.NoError(t, tdb.QueryRow(`SELECT count(*) FROM cues`).Scan(&n))
	require.Equal(t, 2, n, "each grid's cues followed it")
	require.NoError(t, tdb.QueryRow(`SELECT count(*) FROM tracks`).Scan(&n))
	require.Equal(t, 1, n)
}

func rewrite(t *testing.T, path, query string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(query, args...)
	require.NoError(t, err)
}

func TestBuildKeepsPathsAndWaveformsOnRequest(t *testing.T) {
	ctx := context.Background()
	out := filepath.Join(t.TempDir(), "grids")
	_, err := Build(ctx, Options{From: library(t), Out: out, KeepPaths: true, Waveforms: true, Providers: []string{"rekordbox"}})
	require.NoError(t, err)
	db, err := Open(filepath.Join(out, "tracks", "abc123.cdb"))
	require.NoError(t, err)
	defer db.Close()
	var path string
	require.NoError(t, db.QueryRow(`SELECT file_path FROM tracks`).Scan(&path))
	require.Equal(t, "/Users/x/Music/01 - Alive.mp3", path)
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM waveforms`).Scan(&n))
	require.Equal(t, 1, n)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM analyses`).Scan(&n))
	require.Equal(t, 1, n, "only the provider asked for")
	_ = os.Remove
}

func TestFirstDownbeatLaidBack(t *testing.T) {
	// rekordbox lists beats 2, 3, 4, 1 from 483 ms at 124 BPM: bar 1 is at 0.
	var g Grid
	fromBeats(&g, []catalog.BeatTuple{{2, 12400, 483}, {3, 12400, 967}, {4, 12400, 1451}, {1, 12400, 1935}})
	require.Equal(t, 124.0, g.BPM)
	require.Equal(t, 4, g.BeatsPerBar)
	require.InDelta(t, 0, g.FirstDownbeatMs, 1)
	// A grid that opens on beat 3 after a lead-in: bar 1 lands before zero.
	fromBeats(&g, []catalog.BeatTuple{{3, 12000, 200}, {4, 12000, 700}, {1, 12000, 1200}})
	require.Equal(t, -800, g.FirstDownbeatMs)
}

func TestLegacyTuples(t *testing.T) {
	g := legacyGrid{BPM: 125, Beats: []float64{0.0457, 0.5257, 1.0057, 1.4857, 1.9657, 2.4457}, Downbeats: []float64{0.0457, 1.9657}}
	got := legacyTuples(g)
	require.Equal(t, []catalog.BeatTuple{{1, 12500, 46}, {2, 12500, 526}, {3, 12500, 1006}, {4, 12500, 1486}, {1, 12500, 1966}, {2, 12500, 2446}}, got)
	require.Equal(t, "9a125saylessbreakextendedmixv05", NormalizeName("/Volumes/X/Contents/9A - 125 - SAYLESS - BREAK (extended mix - v05).aiff"))
}
