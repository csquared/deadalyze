-- A copy of deadcatalog's catalog schema (deadcatalog/catalog/schema/00001_schema.sql,
-- its "+goose Up" half, taken 2026-10-08). deadcatalog owns the spec; this
-- copy lets the corpus write a .cdb that dc opens without importing the
-- catalog package. When deadcatalog adds a migration, copy its Up half here
-- too and bump the version cdb.go stamps.
PRAGMA user_version = 1;

-- UUIDs are generated as UUIDv7 values by the Go importers. Integer IDs are
-- local implementation IDs for tables that mirror external relational concepts.
CREATE TABLE
  catalogs (created_at text, id integer PRIMARY KEY, kind text, name text, revision text NOT NULL DEFAULT '' CHECK (kind
  IS 'main' OR revision = ''), source_audio_path text, source_audio_sha256 text, source_uri text, updated_at text, uuid
  text, CHECK ((kind IS 'main') = (id = 0)), CHECK ((kind IS 'main') = (uuid IS 'main')), CHECK (kind IS NOT 'main' OR
  (source_uri IS NULL AND source_audio_path IS NULL AND source_audio_sha256 IS NULL)));
-- The reserved main row represents this database, not an imported source.
INSERT INTO
  catalogs (id, kind, name, uuid)
VALUES
  (0, 'main', 'main', 'main');
CREATE UNIQUE INDEX
  catalogs_uuid_unique ON catalogs(uuid);
CREATE UNIQUE INDEX
  catalogs_source_uri_unique ON catalogs(source_uri)
WHERE
  source_uri IS NOT NULL;

CREATE TABLE
  catalog_files (catalog_uuid text NOT NULL REFERENCES catalogs(uuid) ON DELETE CASCADE, data blob NOT NULL, id integer
  PRIMARY KEY, imported_at text, kind text NOT NULL, path text NOT NULL, sha256 text NOT NULL, size_bytes integer NOT
  NULL);
CREATE UNIQUE INDEX
  catalog_files_unique_version ON catalog_files(catalog_uuid, kind, path, sha256);
CREATE INDEX
  catalog_files_catalog ON catalog_files(catalog_uuid);

CREATE TABLE
  artists (id integer PRIMARY KEY, name text);

CREATE TABLE
  albums (artist_id integer REFERENCES artists(id), id integer PRIMARY KEY, name text);

CREATE TABLE
  tracks (album_id integer REFERENCES albums(id), artist_id integer REFERENCES artists(id), artwork_revision text NOT
  NULL DEFAULT '', audio_revision text NOT NULL DEFAULT '', bitrate integer, color text, comment text, composer_id
  integer REFERENCES artists(id), content_revision text NOT NULL DEFAULT '', date_added text, disc_number integer,
  duration integer, file_name text, file_path text, file_size integer, genre text, isrc text, "key" text CHECK ("key" IS
  NULL OR "key" IN ('1A', '2A', '3A', '4A', '5A', '6A', '7A', '8A', '9A', '10A', '11A', '12A', '1B', '2B', '3B', '4B',
  '5B', '6B', '7B', '8B', '9B', '10B', '11B', '12B')), mix_name text, original_artist_id integer REFERENCES artists(id),
  play_count integer, rating integer, record_label text, release_date text, remixer_id integer REFERENCES artists(id),
  sample_depth integer, sample_rate integer, tempo integer, title text, track_number integer, uuid text NOT NULL PRIMARY
  KEY, year integer);

CREATE INDEX
  tracks_album_id ON tracks(album_id);
CREATE INDEX
  tracks_artist_id ON tracks(artist_id);
CREATE INDEX
  tracks_composer_id ON tracks(composer_id);
CREATE INDEX
  tracks_original_artist_id ON tracks(original_artist_id);
CREATE INDEX
  tracks_remixer_id ON tracks(remixer_id);
CREATE INDEX
  albums_artist_id ON albums(artist_id);

CREATE TABLE
  artwork_files (data blob, file_index integer, kind text, mime text, path text, provider text, sha256 text, track_uuid
  text NOT NULL REFERENCES tracks(uuid) ON DELETE CASCADE, PRIMARY KEY (track_uuid, file_index));

CREATE TABLE
  analyses (audio_path text, date text, id integer PRIMARY KEY, provider text, selected integer NOT NULL DEFAULT 0 CHECK
  (selected IN (0, 1)), track_uuid text NOT NULL REFERENCES tracks(uuid), uuid text, version text);
CREATE UNIQUE INDEX
  analyses_uuid_unique ON analyses(uuid);
CREATE UNIQUE INDEX
  analyses_track_uuid_unique ON analyses(track_uuid, uuid);
CREATE UNIQUE INDEX
  analyses_track_selected_unique ON analyses(track_uuid)
WHERE
  selected = 1;
CREATE INDEX
  analyses_track_provider_id ON analyses(track_uuid, provider, id);

CREATE TABLE
  analysis_files (analysis_id integer REFERENCES analyses(id), data blob, kind text, path text, sha256 text, PRIMARY KEY
  (analysis_id, kind));

CREATE TABLE
  beats (analysis_id integer PRIMARY KEY REFERENCES analyses(id), beats_json text NOT NULL);

CREATE TABLE
  cues (analysis_id integer REFERENCES analyses(id), color_blue integer, color_code integer, color_green integer,
  color_id integer, color_red integer, comment text, cue_index integer, hot_cue integer, kind text, list_type integer,
  loop_denominator integer, loop_numerator integer, loop_time_ms integer, source text, time_ms integer, PRIMARY KEY
  (analysis_id, cue_index));

CREATE TABLE
  phrases (analysis_id integer REFERENCES analyses(id), beat integer, "index" integer, kind text, kind_code integer,
  mood text, mood_code integer, phrase_index integer, variant_1 integer, variant_2 integer, variant_3 integer, PRIMARY
  KEY (analysis_id, phrase_index));

CREATE TABLE
  vbr_info (analysis_id integer PRIMARY KEY REFERENCES analyses(id), entries blob, entry_count integer);

CREATE TABLE
  waveforms (analysis_id integer REFERENCES analyses(id), data blob, entry_bytes integer, entry_count integer, kind
  text, rate integer, source text, waveform_id integer, PRIMARY KEY (analysis_id, waveform_id));

-- A catalog track is one observed source-catalog track/file that resolves to a
-- canonical track. It is intentionally not unique by (catalog_uuid, track_uuid)
-- so duplicate source tracks can share one canonical track.
CREATE TABLE
  catalog_tracks (added_at text, album text, album_artist text, artist text, audio_fingerprint text, audio_sha256 text,
  bitrate integer, catalog_uuid text NOT NULL REFERENCES catalogs(uuid) ON DELETE CASCADE, color text, comment text,
  composer text, disc_number integer, duration integer, external_ids text CHECK (external_ids IS NULL OR
  (json_valid(external_ids) AND json_type(external_ids) = 'object')), file_match_key text CHECK (file_match_key IS NULL
  OR (json_valid(file_match_key) AND json_type(file_match_key) = 'array' AND json_array_length(file_match_key) = 5)),
  file_name text, file_path text, file_sha256 text, file_size integer, file_uri text, genre text, isrc text, "key" text
  CHECK ("key" IS NULL OR "key" IN ('1A', '2A', '3A', '4A', '5A', '6A', '7A', '8A', '9A', '10A', '11A', '12A', '1B',
  '2B', '3B', '4B', '5B', '6B', '7B', '8B', '9B', '10B', '11B', '12B')), mix_name text, observed_at text,
  original_artist text, play_count integer, provider text NOT NULL, rating integer, record_label text, release_date
  text, remixer text, sample_depth integer, sample_rate integer, tempo integer, title text, track_number integer,
  track_uuid text NOT NULL REFERENCES tracks(uuid) ON DELETE CASCADE, uuid text PRIMARY KEY, year integer);

CREATE INDEX
  catalog_tracks_catalog ON catalog_tracks(catalog_uuid);
CREATE INDEX
  catalog_tracks_file_match_key ON catalog_tracks(file_match_key)
WHERE
  file_match_key IS NOT NULL;
CREATE INDEX
  catalog_tracks_track ON catalog_tracks(track_uuid);
CREATE UNIQUE INDEX
  catalog_tracks_catalog_uuid_unique ON catalog_tracks(catalog_uuid, uuid);
CREATE UNIQUE INDEX
  catalog_tracks_catalog_uuid_track_unique ON catalog_tracks(catalog_uuid, uuid, track_uuid);
CREATE INDEX
  catalog_tracks_file_uri ON catalog_tracks(catalog_uuid, provider, file_uri)
WHERE
  file_uri IS NOT NULL;
CREATE INDEX
  catalog_tracks_audio_fingerprint ON catalog_tracks(audio_fingerprint)
WHERE
  audio_fingerprint IS NOT NULL AND audio_fingerprint <> '';
CREATE INDEX
  catalog_tracks_audio_sha256 ON catalog_tracks(audio_sha256)
WHERE
  audio_sha256 IS NOT NULL AND audio_sha256 <> '';
CREATE INDEX
  catalog_tracks_file_sha256 ON catalog_tracks(file_sha256)
WHERE
  file_sha256 IS NOT NULL AND file_sha256 <> '';
CREATE INDEX
  catalog_tracks_isrc ON catalog_tracks(isrc)
WHERE
  isrc IS NOT NULL AND isrc <> '';

CREATE TABLE
  catalog_playlists (catalog_uuid text NOT NULL REFERENCES catalogs(uuid) ON DELETE CASCADE, kind text NOT NULL CHECK
  (kind IN ('folder', 'playlist')), name text NOT NULL, parent_uuid text, position integer NOT NULL DEFAULT 0, uuid text
  PRIMARY KEY, FOREIGN KEY (catalog_uuid, parent_uuid) REFERENCES catalog_playlists(catalog_uuid, uuid) ON DELETE
  CASCADE);

CREATE UNIQUE INDEX
  catalog_playlists_catalog_uuid_unique ON catalog_playlists(catalog_uuid, uuid);
CREATE INDEX
  catalog_playlists_tree ON catalog_playlists(catalog_uuid, parent_uuid, position);

CREATE TABLE
  catalog_playlist_tracks (catalog_track_uuid text NOT NULL, catalog_uuid text NOT NULL, playlist_uuid text NOT NULL,
  position integer NOT NULL, PRIMARY KEY (playlist_uuid, position), FOREIGN KEY (catalog_uuid, playlist_uuid) REFERENCES
  catalog_playlists(catalog_uuid, uuid) ON DELETE CASCADE, FOREIGN KEY (catalog_uuid, catalog_track_uuid) REFERENCES
  catalog_tracks(catalog_uuid, uuid) ON DELETE restrict);

CREATE INDEX
  catalog_playlist_tracks_catalog_track ON catalog_playlist_tracks(catalog_track_uuid);

CREATE TABLE
  activities (applied_at text, artifact_uri text, created_at text NOT NULL, dest_json text NOT NULL, error text,
  operation text NOT NULL, plan_json text, src_json text NOT NULL, status text NOT NULL CHECK (status IN ('planned',
  'running', 'applied', 'undone', 'failed')), summary_json text NOT NULL DEFAULT '{}', undo_json text, undone_at text,
  uuid text PRIMARY KEY);

CREATE TABLE
  activity_changes (action text NOT NULL, activity_uuid text NOT NULL REFERENCES activities(uuid) ON DELETE CASCADE,
  after_json text, before_json text, change_index integer NOT NULL, key_json text, table_name text, PRIMARY KEY
  (activity_uuid, change_index));

CREATE INDEX
  activity_changes_table_key ON activity_changes(table_name, key_json);

