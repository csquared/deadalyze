# The grid corpus

A corpus is a folder of tracks without their audio: for each track, every
beat grid anyone has made of it. rekordbox's grid is the reference the engine
is scored against; deadca7's grids are what is being scored; a contributor's
algorithm adds its own. Because the catalogs carry no audio, a corpus can be
shared, and a grid can be compared without the runtime.

```
grids/
  index.json            one entry per track
  tracks/<id>.cdb       a track-local catalog in the deadcatalog schema
```

`<id>` is the audio file's sha256 when the source knew it (a library import
records `file_sha256`; `-hash` computes it when the file is reachable), else
the track's uuid. Two sources that know the same hash land in the same
catalog, which is how rekordbox's grid and deadca7's meet.

## A track catalog

A `.cdb` is SQLite in deadcatalog's schema (`deadcatalog/catalog/schema`), so
`dc`, the DEADCA7 app and any SQLite tool open it. The corpus uses:

| table            | what is kept                                                      |
|------------------|-------------------------------------------------------------------|
| `tracks`         | the one track: title, duration, tempo, key; `file_name`, never `file_path` |
| `artists`, `albums` | the names the track points at                                  |
| `catalogs`       | one row of kind `corpus` per source the grids came from (`Library`, `STICK`, `deadca7-legacy`) |
| `catalog_tracks` | the source's observation: file name, size, `file_sha256`, rekordbox ids in `external_ids` |
| `analyses`       | one row per grid: `provider` (`rekordbox`, `deadca7`, a contributor's name), `version` |
| `beats`          | `beats_json`: `[[beat_number, tempo_x100, time_ms], …]`           |
| `cues`, `phrases`, `vbr_info` | as the source had them                               |
| `analysis_files` | the ones that carry data: `grid` (the runner's JSON: `algo_version`, `cfg_hash`, config, anchor), `key`, `features` |
| `waveforms`      | only with `-waveforms`                                            |

Nothing names a path on anyone's machine unless the corpus was built with
`-keep-paths`, which is for your own `-fresh` runs.

## index.json

```json
{
  "format": 1,
  "updated": "2026-10-08T06:27:00Z",
  "tracks": [
    {
      "id": "0080494b…",
      "file": "tracks/0080494b….cdb",
      "title": "Phase Lock", "artist": "Mono Culture",
      "file_name": "12 - Mono Culture - Phase Lock.mp3",
      "duration_s": 150,
      "source": "Library", "contributor": "csquared", "added": "…",
      "grids": [
        {"provider": "rekordbox", "algo": "rekordbox", "bpm": 129, "first_downbeat_ms": 0, "beats_per_bar": 4, "beats": 323, "cues": 6},
        {"provider": "deadca7", "version": "1", "algo": "beatnet-dbn-v14", "cfg_hash": "6c56…", "bpm": 129, "first_downbeat_ms": 0, "beats_per_bar": 4, "beats": 323, "cues": 8, "selected": true}
      ]
    }
  ]
}
```

The index is derived: `corpus index DIR` rebuilds it from the catalogs,
keeping each entry's `added` and `contributor`.

## Building one

```sh
go run ./cmd/corpus build -from Library.cdb -out ~/grids -contributor you
go run ./cmd/corpus build -from stick.cdb  -out ~/grids -providers rekordbox -hash   # the stick mounted
go run ./cmd/corpus legacy -db ~/Music/DEADCA7/db/deadca7.db -analysis ~/Music/DEADCA7/analysis -out ~/grids
go run ./cmd/corpus stats ~/grids
```

`build` reads any deadcatalog catalog: the app's library, a stick the app
imported (`~/Library/Application Support/deadca7/devices/*.cdb`), a rekordbox
desktop import, a sidecar. `legacy` reads a DEADCA7 server library (the Go
app's `deadca7.db` and its `analysis/grid/*.json`), matching tracks already in
the corpus by file name the way that app matched rekordbox. A second source
for a track merges its new `(provider, version)` grids into the catalog it is
already in and leaves the rest alone, so building again changes nothing.

## Using one

```sh
go run ./cmd/grideval -corpus-dir ~/grids                       # stored deadca7 grids vs rekordbox, no runtime
go run ./cmd/grideval -corpus-dir ~/grids -provider yours       # a contributor's stored grids
go run ./cmd/grideval -corpus-dir ~/grids -fresh -audio ~/Music # the current engine on the audio, by file name
go run ./cmd/corpus refs ~/grids > refs.json                    # the references, for the folder-mode harness
```

A grid passes when its BPM is within 0.5 of the reference, its first downbeat
within a quarter beat (folded into the bar), and it does not start a whole bar
away. `bars` is the whole-bar part the fold would hide: a grid one bar early
is wrong on stage even with phase 0.

## Contributing data

A corpus is a repository of its own (this one holds the tools). To add yours:

1. Build it from your library and sticks, without `-keep-paths` and without
   `-waveforms`. Look at `index.json`: it is what you are publishing. Titles
   and artists are in it; paths and audio are not.
2. Open a pull request against the corpus repository that adds your
   `tracks/*.cdb` and runs `corpus index .`; a track already there merges.
3. Say in the PR which rekordbox version made the reference grids, and whether
   you reviewed them by ear. A reference you corrected by hand is worth a
   note in the entry.

Contributing grids from a new algorithm is the same: `provider` is your
algorithm's name, `version` its `algo_version`, and the `grid` analysis file
carries its config, so a score is reproducible.
