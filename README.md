# deadalyze

deadalyze is the DJ track analysis bundle behind DEADCA7 and deadcatalog. It
takes an audio file and answers what a DJ app needs to play it: the beat grid
(BPM, every beat, bar 1), the musical key, waveforms, hot cues, a feature
blob, stems and an embedding. One Rust engine runs the model legs and speaks
a JSON Lines protocol to any host, so an app in Swift, Go or anything else
with a process API gets the same grid from the same recipe.

This repository holds:

| path            | what |
|-----------------|------|
| `engine/`       | the engine (Rust): the protocol, consensus between two grid models, the rekordbox timeline, Camelot keys, waveforms |
| `algos/`        | the five model legs as Python runners: `beatnet`, `beat_this`, `key`, `features`, `cues` |
| `bundle/`       | `build.sh` builds the runtime archive: CPython 3.11, PyTorch, the libraries, checkpoints, ffmpeg, the stem separator and `bin/engine` |
| `protocol/`     | the protocol as JSON Schema (`describe`, `request`, `event`) |
| `client/`       | the Go client of the engine |
| `settings/`     | the tunables of the legs and the recipe; `dance4x4.json` is the default |
| `corpus/`, `cmd/corpus` | a grid corpus: one track-local catalog per track, holding every grid anyone made of it |
| `cmd/grideval`  | scores grids against references (BPM, bar 1, phase) |
| `cmd/waveeval`  | scores waveforms against the ones rekordbox wrote for the same audio |
| `cmd/engineconf`| checks an engine against the protocol and the goldens |
| `cmd/bundle`    | the release manifest, inspect and verify an archive |
| `docs/`         | `engine-protocol.md` (the contract), `bundle-format.md`, `porting.md`, `research/` |

The docs are the contract. This file is the tour.

## What the engine answers

A `grid` is a constant BPM and every beat of the track as `[beat_number,
time_ms]`, on the rekordbox timeline (the MP3 container's `start_time`,
which rekordbox keeps and ffmpeg trims, is added back). Two models make it:
BeatNet is the primary leg, Beat This the cross-checker. The recipe
(`deadca7-v2`) compares the two lattices and applies two deterministic
fixes: a half-beat shift when the lattices lock on the offbeat, and a bar 1
relabel when the cross-checker's downbeat vote reaches 0.7 agreement. The
grid event carries the verdict (`agreed`, `disputed`, `unavailable`,
`skipped`), what arbitration did, and an `identity_hash` over the recipe,
the legs' versions and config hashes and the decoder, so two hosts know
when they hold the same grid.

Other tasks: `key` (madmom CNN, Camelot label and a top list), `features`
(the DC7F blob the cue picker reads), `waveform` (seven kinds, byte for
byte what deadcatalog wrote, streamed as frames while the audio decodes),
`cues` (`mix8`, `mix16`, `novelty8`, `novelty16`, from a grid and features
the host can pass in, so re-placing cues reads no audio). Results stream as
they are made. On an M-series Mac a track takes about 5 seconds in a batch.

## Install the runtime

The runtime is a GitHub release of this repository, tag `ml-vX.Y.Z`, one
archive per platform (`darwin/arm64`, `linux/amd64`, about 1.7 GB and
1.9 GB) plus `checksums.txt` and `manifest.json`. A host fetches
`https://github.com/csquared/deadalyze/releases/latest/download/manifest.json`,
downloads its platform's asset, checks the sha256, extracts it and runs the
engine the bundle manifest names (`bin/engine`). The hosts already do this:

```sh
dc runtime install                 # deadcatalog, into ~/Library/Application Support/deadcatalog/runtime/current
# DEADCA7: Settings > Analysis > Install
```

By hand:

```sh
curl -LO https://github.com/csquared/deadalyze/releases/latest/download/deadca7-ml-darwin-arm64.tar.gz
tar xzf deadca7-ml-darwin-arm64.tar.gz
export DEADCA7_BUNDLE=$PWD/deadca7-ml-darwin-arm64
```

`docs/bundle-format.md` has the archive layout and the manifests.

## Command line

The engine has two commands. Run it from the bundle root; it needs no
environment of its own.

```sh
cd "$DEADCA7_BUNDLE"
bin/engine describe | jq .tasks
# ["grid","key","features","waveform","cues"]

cat > req.json <<'EOF'
{"protocol": 1,
 "settings": {"name": "dance4x4"},
 "items": [{"id": "t1", "audio": "/Users/me/Music/track.mp3", "tasks": ["grid", "key"]}]}
EOF
bin/engine analyze < req.json
```

stdout is one JSON object per line: `batch_started`, `item_started`,
`progress`, then `grid`, `key`, `item_done`, `batch_done`. Exit 0 after
`batch_done`, 2 after `batch_error`. stderr is diagnostics
(`DEADCA7_ENGINE_LOG=debug` for more). Pull one grid out:

```sh
bin/engine analyze < req.json | jq -c 'select(.event=="grid") | {bpm, first_downbeat_ms, verdict: .consensus.verdict}'
# {"bpm":126,"first_downbeat_ms":213,"verdict":"agreed"}
```

A request carries up to 64 items; the models load once per batch, so batch
when you can. `settings` is a settings document (`settings/README.md`);
missing keys take the defaults `describe` lists under `settings_schema`,
and `device` is `auto` (`mps` on Apple Silicon where torch has it, else
`cpu`).

The harnesses here are `go run` commands and need the bundle
(`DEADCA7_BUNDLE`, or the installed runtime, see `client.Resolve`):

```sh
make test                                   # go vet, go test, the build script parses
go run ./cmd/engineconf -audio DIR          # the engine against the protocol and the goldens
go run ./cmd/waveeval -export ~/rb-usb      # waveforms against a rekordbox USB export
./bundle/build.sh                           # build the archive for this machine
```

## Call it from your app

The host starts the engine as a child process with the bundle root as the
working directory, writes one request to stdin, closes stdin, and reads
events from stdout until `batch_done`. It writes a track when it sees that
track's `item_done`. To cancel, send SIGTERM, then SIGKILL after five
seconds. If the engine exits without `batch_done`, every item without
`item_done` is `engine_crashed`; retry those by halving the batch, then
singly. `docs/engine-protocol.md` has every event and field;
`protocol/*.schema.json` is the same contract for a validator.

### Go

```go
import "github.com/csquared/deadalyze/client"

eng, err := client.Resolve() // DEADCA7_BUNDLE, DEADCATALOG_RUNTIME, then the installed runtime
if err != nil { return err }

req := client.Request{
    Settings: map[string]any{"name": "dance4x4"},
    Items: []client.Item{
        {ID: "t1", Audio: "/Users/me/Music/track.mp3",
         Tasks: []string{client.TaskGrid, client.TaskKey, client.TaskWaveform}},
    },
}
err = eng.AnalyzeAll(ctx, req, 0, func(ev client.Event) {
    switch ev.Event {
    case "grid":
        fmt.Println(ev.ID, ev.BPM, ev.FirstDownbeatMs, ev.Consensus.Verdict)
    case "key":
        fmt.Println(ev.ID, ev.Camelot, ev.Label)
    case "item_error":
        fmt.Println(ev.ID, ev.Task, ev.Code, ev.Message)
    }
})
```

`AnalyzeAll` splits a request into batches of the engine's
`limits.max_batch_items` (0 takes the engine's limit) and applies the
crash retry. `Analyze` sends one batch as is. `Describe` returns the
`describe` document, including `settings_schema` for a settings UI.
`cmd/grideval/main.go` is a complete host.

### Swift, or any other language

Spawn `bin/engine analyze`, pipe the request JSON in, decode stdout line by
line. DEADCA7 does this in `Sources/Cdb/EngineRuntime.swift` and its engine
client. What a host stores from a grid event:

| store as                      | from the event |
|-------------------------------|----------------|
| beats `[[n, round(bpm*100), ms]]` | `beats`, `bpm` |
| `analyses.version`            | `batch_started.recipe` (`deadca7-v2`) |
| `analysis_files/grid`         | `identity`, `ran_on`, `timeline`, `consensus`, `provenance` together |
| `analysis_files/dispute`      | `consensus.dispute` when the verdict is `disputed` |

A host never carries a model name, a threshold or a checkpoint path. A new
runtime release changes the answer; the host release does not.

## Evaluate your rekordbox collection

grideval scores every grid in a corpus against a reference provider. For a
rekordbox library the references are rekordbox's own grids, and the
question is how many of the engine's grids land on them: BPM within 0.05,
bar 1 within a quarter beat. Three steps.

1. Turn the rekordbox library into a catalog. deadcatalog reads rekordbox
   6/7's `master.db`; the source is named, so no path is typed. `Rekordbox.app`
   resolves to `~/Library/Pioneer/rekordbox` on this machine, is read in one
   transaction while rekordbox runs, and lands in `~/Music/DEADCA7/dbs/main.cdb`.
   A USB export is a source too.

   ```sh
   dc import Rekordbox.app --apply        # the desktop library
   dc import /Volumes/SAYLESS1 --apply    # a stick's export.pdb and ANLZ grids
   ```

2. Build the corpus from it. `-providers rekordbox` keeps only rekordbox's
   grids, `-hash` names each track by its audio's sha256 (so the same file
   from a stick and the desktop is one entry) and `-keep-paths` keeps the
   audio paths for a fresh run on this machine.

   ```sh
   go run ./cmd/corpus build -from ~/Music/DEADCA7/dbs/main.cdb -out ~/grids -providers rekordbox -hash -keep-paths
   go run ./cmd/corpus stats ~/grids
   ```

3. Analyse the audio with the engine and score it. `-dry-run` says how many
   tracks have audio and how long the run will take. The run is one batch
   per 64 tracks, about 5 seconds a track on an M-series Mac. `-json` keeps
   the per-track scores.

   ```sh
   go run ./cmd/grideval -corpus-dir ~/grids -fresh -dry-run
   go run ./cmd/grideval -corpus-dir ~/grids -fresh -json build/grideval-fresh.json
   go run ./cmd/grideval -corpus-dir ~/grids -fresh -audio ~/Music,/Volumes/SAYLESS1   # audio found by file name
   ```

One line per track, then the total:

```
pass  01 - Alive.mp3  bpm 126.00 vs 126.00 (Δ+0.00)  meter 4 vs 4  phase +0.00 beats  bars +0  downbeat 213 ms  5.1s
FAIL  02 - Other.aiff  bpm 124.00 vs 124.00 (Δ+0.00)  meter 4 vs 4  phase +1.00 beats  bars +0  downbeat 689 ms  4.9s  disputed: downbeat vote 0.65
grideval: 1115 of 1276 pass, 48 disputed
```

Phase is the number that matters on stage: `+1.00 beats` is a grid one beat
off rekordbox's, `+0.50` the offbeat lock. `-no-arbitrate` flags the
cross-checker's disagreements without applying its fixes, to see what the
recipe changes. `-source PREFIX` scores one source catalog only; a stick
whose "rekordbox" grids were exported by DEADCA7 scores against itself.

### One track: a catalog's grid against rekordbox's

`-track NAME -detail` is the grid diff. With no `-fresh` it reads the grid a
catalog already holds (a DEADCA7 `.cdb`, imported into the corpus) and
prints it beat by beat against rekordbox's, in seconds and with no runtime.
With `-fresh` it analyses the track again and adds the engine's timeline,
consensus and identity lines.

```sh
go run ./cmd/grideval -corpus-dir ~/grids -track "02 - Other.aiff" -detail
go run ./cmd/grideval -corpus-dir ~/grids -track "02 - Other.aiff" -detail -fresh -audio ~/Music
```

```
      02 - Other.aiff              deadca7        fd3b1b65… ref
      bpm                          124.000        124.000
      first downbeat (ms)          689            205             Δ +484 ms
      beat                         deadca7 n@ms   ref n@ms
      1                            4@205          1@205           Δ +0
      2                            1@689          2@689           Δ +0
      3                            2@1173         3@1173          Δ +0
      …
FAIL  02 - Other.aiff  bpm 124.00 vs 124.00 (Δ+0.00)  meter 4 vs 4  phase +1.00 beats  bars +0  downbeat 689 ms  0.0s
```

The beats land on the same milliseconds; the numbering is one beat off.
That is the whole class of failure the relabel fix targets.

Already-stored grids score in seconds with no runtime: `make grideval`
compares the `deadca7` grids in the corpus with rekordbox's. The round that
set the current thresholds is `docs/research/2026-10-08-first-round.md`:
1276 SAYLESS1 tracks, 1115 pass.

## Development

```sh
make test                      # Go: vet, test, build script
cd engine && cargo test        # the engine; CI runs fmt, clippy and test on Rust 1.88
make pins                      # the runners here hash the same as the hosts' embedded copies
```

Against a built engine and a checkout of the legs while an older bundle is
installed: `DEADCA7_ENGINE=engine/target/release/engine DEADCA7_ALGOS=$PWD/algos`.
A tag `ml-vX.Y.Z` builds both platforms and publishes the release
(`.github/workflows/bundle-release.yml`); the Test workflow runs on every
push. `docs/porting.md` records what came from deadcatalog and DEADCA7 and
what stays there.
