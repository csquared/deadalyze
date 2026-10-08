package corpus

// The corpus writes and reads deadcatalog .cdb files without importing the
// catalog package: schema.sql is a copy of deadcatalog's schema, and the
// functions here reproduce the slice of its loader the corpus relies on
// (the goose version rows dc checks on open, the row preparation its
// importers do, the uuid and revision tokens it stamps). deadcatalog owns
// the format; this file follows it.

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cockroachdb/errors"
	"golang.org/x/text/unicode/norm"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// schemaVersion is deadcatalog's catalog schema version: PRAGMA user_version
// and the last applied goose migration, which dc checks on open.
const schemaVersion int64 = 1

// BeatTuple is one beat of a stored grid: [beat_number, bpm*100, time_ms],
// the element of the beats table's beats_json.
type BeatTuple [3]int64

// EncodeBeatTuples renders beat tuples as beats_json.
func EncodeBeatTuples(beats []BeatTuple) (string, error) {
	if beats == nil {
		beats = []BeatTuple{}
	}
	data, err := json.Marshal(beats)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// DecodeBeatTuples reads beats_json; an empty value is no beats.
func DecodeBeatTuples(value string) ([]BeatTuple, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	var beats []BeatTuple
	if err := json.Unmarshal([]byte(value), &beats); err != nil {
		return nil, err
	}
	return beats, nil
}

// table is the rows of one catalog table, as the loader takes them.
type table struct {
	Name string
	Rows []map[string]any
}

// contents is what goes into one track catalog, in insert order.
type contents struct {
	Tables []table
}

// newUUID returns a time-sortable UUIDv7, the identity deadcatalog gives
// portable records.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	ms := uint64(time.Now().UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	b[6] = 0x70 | (b[6] & 0x0f) // version 7
	b[8] = 0x80 | (b[8] & 0x3f) // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// revisionToken is the token deadcatalog's mutation boundary stamps on the
// rows a commit touched.
func revisionToken() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}

// dsn is deadcatalog's connection string: a file URL with the pragmas it
// applies to every connection.
func dsn(absolute, mode string) string {
	u := &url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Set("mode", mode)
	u.RawQuery = q.Encode()
	return u.String()
}

func openFile(p, mode string) (*sql.DB, error) {
	absolute, err := filepath.Abs(p)
	if err != nil {
		return nil, err
	}
	return sql.Open("sqlite", dsn(absolute, mode))
}

// openReadOnly opens an existing catalog read-only and rejects one of
// another schema version, as deadcatalog does.
func openReadOnly(p string) (*sql.DB, error) {
	db, err := openFile(p, "ro")
	if err != nil {
		return nil, err
	}
	if err := checkVersion(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, errors.Wrapf(err, "%s", p)
	}
	return db, nil
}

func checkVersion(ctx context.Context, db *sql.DB) error {
	var version int64
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version != schemaVersion {
		return errors.Errorf("unsupported catalog schema %d (supported %d)", version, schemaVersion)
	}
	var migration int64
	if err := db.QueryRowContext(ctx, "SELECT coalesce(max(version_id),0) FROM goose_db_version WHERE is_applied").Scan(&migration); err != nil {
		return err
	}
	if migration != schemaVersion {
		return errors.Errorf("catalog schema/migration mismatch: %d/%d", version, migration)
	}
	return nil
}

// createFile writes a new catalog at p holding c. The file must not exist.
// It fails, and leaves nothing behind, when a row does not fit the schema.
func createFile(p string, c contents) (*sql.DB, error) {
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(p)
		return nil, err
	}
	db, err := openFile(p, "rwc")
	if err != nil {
		_ = os.Remove(p)
		return nil, err
	}
	if err := load(context.Background(), db, c); err != nil {
		_ = db.Close()
		_ = os.Remove(p)
		return nil, err
	}
	return db, nil
}

// gooseSQL is the version table goose keeps beside the schema; dc reads
// its last applied version on open. The text is goose's own.
const gooseSQL = `CREATE TABLE goose_db_version (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		version_id INTEGER NOT NULL,
		is_applied INTEGER NOT NULL,
		tstamp TIMESTAMP DEFAULT (datetime('now'))
	)`

func load(ctx context.Context, db *sql.DB, c contents) error {
	if _, err := db.ExecContext(ctx, gooseSQL); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, 1)`); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		return errors.Wrap(err, "catalog schema")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)`, schemaVersion); err != nil {
		return err
	}
	prepareObservations(c)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	columns := map[string][]string{}
	for _, t := range c.Tables {
		cols, ok := columns[t.Name]
		if !ok {
			cols, err = tableColumns(ctx, tx, t.Name)
			if err != nil {
				return err
			}
			columns[t.Name] = cols
		}
		for _, row := range t.Rows {
			if t.Name == "catalogs" && rowString(row["kind"]) == "main" {
				continue
			}
			if err := insert(ctx, tx, t.Name, row, cols); err != nil {
				return errors.Wrapf(err, "%s", t.Name)
			}
		}
	}
	if err := stampRevisions(ctx, tx); err != nil {
		return err
	}
	if err := checkKeys(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func tableColumns(ctx context.Context, tx *sql.Tx, name string) ([]string, error) {
	rs, err := tx.QueryContext(ctx, "SELECT * FROM "+sqlIdent(name)+" LIMIT 0")
	if err != nil {
		return nil, errors.Wrapf(err, "unsupported catalog table %q", name)
	}
	defer rs.Close()
	return rs.Columns()
}

// insert writes one row the way deadcatalog's loader does: every column the
// row names (an unknown one is an error), revision tokens left to the
// boundary, a missing identity allocated.
func insert(ctx context.Context, tx *sql.Tx, name string, row map[string]any, cols []string) error {
	known := make(map[string]bool, len(cols))
	for _, col := range cols {
		known[col] = true
	}
	values := make(map[string]any, len(row))
	for col, v := range row {
		if !known[col] {
			return errors.Errorf("unknown catalog column %q", col)
		}
		if isRevisionColumn(name, col) {
			continue
		}
		values[col] = v
	}
	if name == "catalogs" || name == "analyses" || name == "catalog_tracks" {
		if rowString(values["uuid"]) == "" {
			values["uuid"] = newUUID()
		}
	}
	if len(values) == 0 {
		return errors.New("empty catalog insert")
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	args := make([]any, len(keys))
	marks := make([]string, len(keys))
	idents := make([]string, len(keys))
	for i, k := range keys {
		args[i], marks[i], idents[i] = values[k], "?", sqlIdent(k)
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO "+sqlIdent(name)+" ("+strings.Join(idents, ", ")+") VALUES ("+strings.Join(marks, ",")+")", args...)
	return err
}

func isRevisionColumn(table, column string) bool {
	switch {
	case table == "tracks":
		return column == "content_revision" || column == "audio_revision" || column == "artwork_revision"
	case table == "catalogs":
		return column == "revision"
	}
	return false
}

// stampRevisions gives every track and the main row the one token a
// deadcatalog commit would: the state is new, so everything changed.
func stampRevisions(ctx context.Context, tx *sql.Tx) error {
	token, err := revisionToken()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tracks SET content_revision = ?, audio_revision = ?, artwork_revision = ?`, token, token, token); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE catalogs SET revision = ? WHERE id = 0`, token)
	return err
}

// checkKeys is deadcatalog's invariant that identities are never blank.
func checkKeys(ctx context.Context, tx *sql.Tx) error {
	keys := map[string][]string{
		"tracks": {"uuid"}, "catalogs": {"uuid"}, "catalog_tracks": {"uuid"}, "analyses": {"uuid"},
		"analysis_files": {"analysis_id", "kind"}, "beats": {"analysis_id"}, "cues": {"analysis_id", "cue_index"},
		"phrases": {"analysis_id", "phrase_index"}, "vbr_info": {"analysis_id"}, "waveforms": {"analysis_id", "waveform_id"},
	}
	names := make([]string, 0, len(keys))
	for name := range keys {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		conditions := make([]string, len(keys[name]))
		for i, col := range keys[name] {
			conditions[i] = sqlIdent(col) + " IS NULL OR trim(" + sqlIdent(col) + ")=''"
		}
		var invalid int
		err := tx.QueryRowContext(ctx, "SELECT 1 FROM "+sqlIdent(name)+" WHERE "+strings.Join(conditions, " OR ")+" LIMIT 1").Scan(&invalid)
		if err == nil {
			return errors.Errorf("%s requires nonempty keys: %s", name, strings.Join(keys[name], ", "))
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return nil
}

// prepareObservations does what deadcatalog's importers do to catalog_tracks
// rows before they are stored: a track's isrc moves onto its observations;
// an observation takes the track's metadata for any field it did not record
// itself; and the file match key dc matches imports by is derived.
func prepareObservations(c contents) {
	artists := map[int64]string{}
	albums := map[int64]struct {
		artistID int64
		name     string
	}{}
	tracks := map[string]map[string]any{}
	for _, t := range c.Tables {
		switch t.Name {
		case "artists":
			for _, row := range t.Rows {
				if id, ok := rowInt64(row["id"]); ok {
					artists[id] = rowString(row["name"])
				}
			}
		case "albums":
			for _, row := range t.Rows {
				if id, ok := rowInt64(row["id"]); ok {
					artistID, _ := rowInt64(row["artist_id"])
					albums[id] = struct {
						artistID int64
						name     string
					}{artistID, rowString(row["name"])}
				}
			}
		case "tracks":
			for _, row := range t.Rows {
				if uuid := rowString(row["uuid"]); uuid != "" {
					tracks[uuid] = row
				}
			}
		}
	}
	observed := map[string]bool{}
	for _, t := range c.Tables {
		if t.Name != "catalog_tracks" {
			continue
		}
		for _, row := range t.Rows {
			track := tracks[rowString(row["track_uuid"])]
			if track == nil {
				prepareFileMatchKey(row)
				continue
			}
			observed[rowString(row["track_uuid"])] = true
			if rowString(row["isrc"]) == "" && rowString(track["isrc"]) != "" {
				row["isrc"] = track["isrc"]
			}
			artistName := func(field string) string {
				id, ok := rowInt64(track[field])
				if !ok {
					return ""
				}
				return artists[id]
			}
			values := map[string]any{
				"added_at":        track["date_added"],
				"artist":          artistName("artist_id"),
				"composer":        artistName("composer_id"),
				"original_artist": artistName("original_artist_id"),
				"remixer":         artistName("remixer_id"),
			}
			if id, ok := rowInt64(track["album_id"]); ok {
				values["album"] = albums[id].name
				values["album_artist"] = artists[albums[id].artistID]
			} else {
				values["album"], values["album_artist"] = "", ""
			}
			for _, name := range []string{
				"bitrate", "color", "comment", "disc_number", "duration", "file_name", "file_path", "file_size",
				"genre", "key", "mix_name", "play_count", "rating", "record_label", "release_date", "sample_depth",
				"sample_rate", "tempo", "title", "track_number", "year",
			} {
				values[name] = track[name]
			}
			for name, value := range values {
				if _, ok := row[name]; ok {
					continue
				}
				row[name] = nil
				if !valueMissing(value) {
					row[name] = value
				}
			}
			prepareFileMatchKey(row)
		}
	}
	for _, t := range c.Tables {
		if t.Name != "tracks" {
			continue
		}
		for _, row := range t.Rows {
			if observed[rowString(row["uuid"])] {
				row["isrc"] = nil
			}
		}
	}
}

// prepareFileMatchKey is deadcatalog's file-match-v1 key: compact JSON
// [1, artist, album, title, extension], NULL when any part is missing.
func prepareFileMatchKey(row map[string]any) {
	artist := norm.NFC.String(rowString(row["artist"]))
	album := norm.NFC.String(rowString(row["album"]))
	title := norm.NFC.String(rowString(row["title"]))
	name := rowString(row["file_name"])
	if name == "" {
		name = rowString(row["file_path"])
	}
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
	row["file_match_key"] = nil
	if artist == "" || album == "" || title == "" || ext == "" {
		return
	}
	for _, v := range []string{artist, album, title, ext} {
		if !utf8.ValidString(v) {
			return
		}
	}
	key, _ := json.Marshal([5]any{1, artist, album, title, ext})
	row["file_match_key"] = string(key)
}

func valueMissing(v any) bool {
	if v == nil {
		return true
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s) == ""
	}
	return false
}

func rowString(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	case fmt.Stringer:
		return strings.TrimSpace(value.String())
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func rowInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int8:
		return int64(n), true
	case int16:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case uint:
		return int64(n), true
	case uint8:
		return int64(n), true
	case uint16:
		return int64(n), true
	case uint32:
		return int64(n), true
	case uint64:
		if n > uint64(^uint64(0)>>1) {
			return 0, false
		}
		return int64(n), true
	}
	return 0, false
}

func sqlIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
