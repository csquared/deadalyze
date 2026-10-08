# The engine protocol (v1)

The engine is one executable in the bundle. A host asks it what it can do,
hands it a batch of tracks and a settings document, and reads the results
back as they are made. The host never names a runner, a flag, a checkpoint
or a threshold: everything that decides what a grid is lives behind this
line, so the recipe exists once (not once per host) and a compiled engine
replaces the Python one without a host release.

This document is the contract. `../protocol/*.schema.json` is the same
contract as JSON Schema; `../cmd/engineconf` checks an engine against both
and against golden outputs. deadca7 (Swift) and deadcatalog (Go) are the
hosts; the first engine is `../engine` (Rust), which delegates the model
legs to the Python runners in `../algos` until each is native.

## Finding the engine

The bundle manifest (`bundle-format.md`, layout 3) names it:

```json
{"engine": "bin/engine", "protocols": [1]}
```

A host runs it with the bundle root as the working directory and no
environment of its own beyond `PATH`: the engine knows where its interpreter,
libraries, checkpoints and tools are. `DEADCA7_ENGINE_LOG=debug` turns on
diagnostics on stderr.

## Commands

| command    | stdin             | stdout          | exit |
|------------|-------------------|-----------------|------|
| `describe` | –                 | one JSON object | 0 |
| `analyze`  | one request JSON  | JSON Lines      | 0 after `batch_done`; 2 after `batch_error` |
| `stems`    | one request JSON  | JSON Lines      | as `analyze` |
| `embed`    | one request JSON  | JSON Lines      | as `analyze` |

stdout carries nothing but the protocol. stderr is diagnostics and is never
parsed, except that a host may keep its tail for an error message.

## `describe`

```json
{"protocols": [1],
 "engine": {"impl": "deadca7-engine-rs", "version": "0.1.0"},
 "recipe": "deadca7-v2",
 "runtime_version": "ml-v0.3.0",
 "tasks": ["grid", "key", "features", "waveform", "cues"],
 "commands": ["describe", "analyze"],
 "legs": {
   "beatnet":   {"algo_version": "beatnet-dbn-v15",     "impl": "python", "ready": true},
   "beat_this": {"algo_version": "beat-this-v2",        "impl": "python", "ready": true},
   "key":       {"algo_version": "madmom-key-cnn-2018", "impl": "python", "ready": true},
   "features":  {"algo_version": "dc7f-1",              "impl": "python", "ready": true},
   "waveform":  {"algo_version": "wave-v1",             "impl": "native", "ready": true}
 },
 "decoder": {"tool": "ffmpeg", "version": "7.1"},
 "devices": ["cpu", "mps"],
 "limits": {"max_batch_items": 64, "max_line_bytes": 16777216},
 "settings_schema": {
   "beats_per_bar": {"type": "integer", "default": 4, "minimum": 1, "maximum": 16,
                     "title": "Beats per bar", "group": "grid", "affects_identity": true},
   "arbitrate_min_vote_agreement": {"type": "number", "default": 0.7, "minimum": 0.5, "maximum": 1,
                     "step": 0.05, "title": "Relabel bar 1 on the cross-checker's vote at",
                     "group": "recipe", "affects_identity": true},
   "device": {"type": "string", "enum": ["auto", "cpu", "mps"], "default": "auto",
              "title": "Device", "group": "runtime", "affects_identity": false}
 }}
```

- `protocols` are the versions this engine speaks. A request names one.
- `recipe` is the identity of how the legs are combined into one answer
  (`deadca7-v2`). It is what a host stores as `analyses.version`; hosts do
  not carry it as a constant.
- `legs[].impl` is `python` or `native`; `ready` is false when a checkpoint
  or model is missing, and the task that needs it fails with
  `model_load_failed`.
- `settings_schema` is one entry per settings key, enough for a UI to
  render it: `type`, `default`, `title`, optional `minimum`/`maximum`/
  `step`/`enum`/`unit`, `group`, and `affects_identity` (whether the key is
  part of a leg's `cfg_hash`).
- `limits.max_line_bytes` bounds any one stdout line; `max_batch_items`
  bounds a request.

## `analyze`

### Request

```json
{"protocol": 1,
 "settings": {"name": "dance4x4", "beats_per_bar": 4, "min_bpm": 110, "max_bpm": 130,
              "arbitrate_min_vote_agreement": 0.7, "device": "auto"},
 "items": [
   {"id": "6d3c…", "audio": "/Users/me/Music/track.mp3",
    "tasks": ["grid", "key", "features", "waveform"]},
   {"id": "a91f…", "audio": "/Users/me/Music/other.aiff",
    "tasks": ["cues"], "cue_algo": "mix16",
    "inputs": {"grid": {"bpm": 126, "beats_per_bar": 4, "beats": [[1, 213], [2, 689]]}}}
 ]}
```

- `settings` is a bundle settings document (`../settings/README.md`): the
  runners' tunables and the recipe's in one object. Keys the engine does
  not know are kept and ignored. Missing keys take the defaults from
  `settings_schema`.
- `items[].id` is the host's handle, echoed on every event; `audio` is an
  absolute path the engine can read.
- `tasks` is any subset of `describe.tasks`. A host that already has a key
  leaves `key` out. `features` and `waveform` need nothing.
- `cues` needs a grid and the track's features: each from the same request
  (the item's own `grid` and `features` tasks) or from the host's store
  (`inputs.grid` in the grid event's shape, `inputs.features` as a DC7F
  blob). With both inputs given the engine reads no audio and loads no
  model, so a host re-places hot cues (a new `cue_algo`, a hand-moved
  grid) without analysing again. That is what the DC7F artifact is for;
  `cue_algo` is one of `mix8`, `mix16`, `novelty8`, `novelty16` (default
  `mix16`).

### Events

One JSON object per line. Every event has `event` and `seq`, a counter
that starts at 1 and increases by one per line in the batch. Item events
have `id`. Ordering is guaranteed per id; events for different ids
interleave. `item_done` or `item_error` is the last event for an id.
`batch_done` is the last line. A host ignores events and fields it does not
know.

| event | fields |
|-------|--------|
| `batch_started` | `engine`, `recipe`, `runtime_version`, `settings` (as resolved: defaults filled, `device` chosen), `items` (ids accepted) |
| `item_started` | `duration_ms` (from the container, for a first paint) |
| `waveform_frame` | `offset` (columns from the start), `columns`, `columns_per_second` (150), `data` (3 bytes a column: low, mid, high on 0..127). Provisional, in order, about a second at a time, while the audio decodes |
| `waveform` | one per kind: `kind`, `entry_bytes`, `entry_count`, `rate`, `source`, `data`; the first for an id also carries `duration_ms` and `band_scales` |
| `grid` | below |
| `key` | `camelot` ("7A"; "" when none), `label` ("D minor"), `confidence`, `top` ([{label, p}]), `algo_version`, `note` (why there is no key, else absent) |
| `features` | `format` ("dc7f"), `algo_version`, `data` |
| `cues` | `algo`, `cues` ([{comment, hot_cue, time_ms}]) |
| `item_warning` | `task`, `code`, `message`: a non-fatal failure; the task's result says what was done instead |
| `item_error` | `task`, `code`, `message`, `retryable`: the task failed; the item's other tasks continue |
| `item_done` | `tasks`: `{task: "ok" \| "failed" \| "skipped"}` for every task requested |
| `progress` | `stage` (`decode`, `beatnet`, `beat_this`, `key`, `features`, `waveform`, `cues`), `done`, `total` |
| `batch_done` | `items`, `ok`, `failed`, `elapsed_ms` |
| `batch_error` | `code`, `message`: the request itself was refused; no item events follow |

Results stream as they are made, not when the batch ends. The engine runs
BeatNet over the batch, Beat This over the batch, arbitrates, then the key
pass, with waveforms and features drawn beside the model passes; an item's
`grid` goes out the moment its second leg lands, and its `item_done` when
its last task does. A host writes a track when it sees `item_done`.

Waveform streaming is part of the contract, not an optimisation: for an
item with the `waveform` task the engine emits `waveform_frame` events
while the audio decodes, in order from offset 0, each about a second of
columns, and the last frame, then the seven `waveform` events, arrive
before that item's `grid`. A host draws the track filling in within
seconds of `item_started`, long before any model answers. `engineconf`
fails an engine whose frames arrive late, out of order, or not at all.

### Blobs

`data` is an object with one of `b64` (standard base64 of the bytes) or
`path` (an absolute path to a file the engine wrote, which the host owns
from then on). `analyze` uses `b64`: a track's waveforms and features are
about 1.4 MB encoded. `stems` uses `path`.

### Codes

`invalid_request`, `unsupported_protocol`, `unsupported_task`,
`audio_missing`, `decode_failed`, `model_load_failed`, `grid_failed`,
`leg_unavailable`, `cancelled`, `engine_crashed` (host-side, see below),
`internal`.

Which failures are fatal is the engine's rule: BeatNet failing fails the
`grid` task (`item_error grid_failed`); Beat This failing is
`item_warning leg_unavailable` and the grid's `consensus.verdict` is
`unavailable`; the key leg failing is `item_warning` and the `key` event
carries a `note`.

### The grid event

```json
{"event": "grid", "seq": 41, "id": "6d3c…",
 "bpm": 126.0, "beats_per_bar": 4,
 "beats": [[1, 213], [2, 689], [3, 1165], [4, 1641], [1, 2117]],
 "first_beat_ms": 213, "first_downbeat_ms": 213,
 "timeline": {"name": "rekordbox", "offset_ms": 26.122, "applied_shift_ms": 26,
              "decode": {"flags": ["-flags2", "+skip_manual"], "start_time_s": 0.026122}},
 "identity": {"recipe": "deadca7-v2",
              "legs": {"beatnet":   {"algo_version": "beatnet-dbn-v15", "cfg_hash": "3f…"},
                       "beat_this": {"algo_version": "beat-this-v2",    "cfg_hash": "9a…"}},
              "decoder": "ffmpeg-7.1",
              "identity_hash": "c0…"},
 "ran_on": {"device": "mps", "runtime_version": "ml-v0.3.0",
            "engine": {"impl": "deadca7-engine-rs", "version": "0.1.0"}},
 "consensus": {"verdict": "agreed",
               "shift_ms": 0, "relabel_beats": 1,
               "beat_this": {"bpm": 126.0, "first_beat_ms": 213, "phase_vote": 1,
                             "phase_agreement": 0.8, "resid_stdev": 0.004}},
 "provenance": {"beatnet": {"raw_bpm": 125.98, "raw_beats": 812, "anchor": {"…": "…"}, "config": {"…": "…"}},
                "origin_refine_ms": 3.1}}
```

- `beats` are `[beat_number, time_ms]`, every beat of the track, on the
  rekordbox timeline; `bpm` is constant across them. A host stores them as
  `[[beat_number, round(bpm*100), time_ms]]`.
- `timeline` says what was added to the model's times and why:
  `offset_ms` is the container's `start_time` the decoders trim and
  rekordbox keeps (MP3 only; 0 otherwise), `applied_shift_ms` the integer
  the beats were moved by. `decode.flags` are the ffmpeg flags the
  waveform and features were decoded with (they are already on this
  timeline and were not shifted).
- `identity` is what makes two grids the same grid. `identity_hash` is the
  sha256 over the canonical JSON of `{recipe, legs, decoder}`; it excludes
  the runtime version, the device and the engine, which are `ran_on`.
- `consensus.verdict` is `agreed`, `disputed`, `unavailable` (no second
  leg) or `skipped` (consensus off in settings). `dispute` is present when
  `disputed`, as text for a person. `shift_ms` and `relabel_beats` say what
  arbitration did.
- `provenance` carries what the primary leg reported about itself, kept
  for review and never read back by a host.

A host stores `identity`, `ran_on`, `timeline`, `consensus` and
`provenance` together as the grid's provenance file
(`analysis_files/grid`), and `consensus.dispute` as `analysis_files/dispute`.

### Identity rules

- Each leg's `cfg_hash` is the sha256 (hex) of the canonical JSON of the
  leg's identity keys: the keys listed under `identity.keys` in the leg's
  `algo.json`, in sorted order, with no whitespace, integers as integers,
  floats in shortest round-trip form, booleans as `true`/`false`. Any
  implementation of the leg hashes the same bytes.
- `device`, paths, and anything that does not change the answer are not
  identity keys. A checkpoint is identified by its name (`final0`), not
  its path.
- Changing a leg's identity keys or its behaviour bumps its
  `algo_version`. Changing how the legs are combined bumps `recipe`.

## Cancellation and crashes

The engine puts itself and every child in its own process group and kills
the group when it exits or when its parent dies. A host cancels by sending
SIGTERM to the engine, then SIGKILL after a grace period (five seconds);
closing stdin is a secondary signal the engine also honours. A cancelled
batch ends with `item_error cancelled` for each unfinished id and
`batch_done`, when the engine is given the time to write them.

The model legs run as children of the engine, so a crash in one of them is
reported for the affected items (`leg_unavailable`, `grid_failed`) and the
batch goes on. If the engine itself exits without `batch_done`, the host
treats every id without `item_done` as `item_error engine_crashed` with the
exit status and the stderr tail as the message, and retries those ids by
halving the batch, then singly.

## `stems` and `embed`

Same request and event grammar. `stems` takes `items[{id, audio, model,
out_dir}]` and answers `stems` events `{model, stems: {name: data{path}}}`
plus `progress` (`stage: "separate"`, `done`/`total` in percent of the
item). `embed` takes `items[{id, audio}]` and answers `embedding`
`{model, dims, data{b64}}` (float32 little-endian). Both are specified here
so one client handles all three; the first engine implements `analyze`
first.

## Versions

Three things version independently and every grid names all three:

| what | where | example |
|------|-------|---------|
| the protocol | `describe.protocols`, request `protocol` | `1` |
| the recipe | `describe.recipe`, `grid.identity.recipe` | `deadca7-v2` |
| the legs | `describe.legs`, `grid.identity.legs` | `beatnet-dbn-v15` + `cfg_hash` |

and the bundle (`ml-vX.Y.Z`) rides along as `runtime_version` in `ran_on`.
A protocol change that a v1 host could not ignore (a renamed field, a
changed meaning) is protocol 2; adding an event or a field is not.
