# Algorithms

An algorithm is one directory here: `runner.py` and `algo.json`. The runner
is a Python script the host runs on the bundle's interpreter with the bundle's
libraries (`PYTHONPATH=analysis/lib:lib`, `PYTHONNOUSERSITE=1`), and nothing
else: no venv, no network, no files outside the paths it is handed.

These five are the deadca7 engine as it ships today. deadcatalog embeds them
with `go:embed` and DEADCA7 embeds them as Swift string literals, byte for
byte: each host writes the script to `runtime/wrappers/<name>/runner-<sha256>.py`
and the three copies agree on the hash. `algos_test.go` pins the hashes here
and, when the sibling checkouts are present, checks that their copies have not
drifted from these.

## The contract

- **Input** is flags. One track is `--audio PATH`. A batch is `--items JSON`,
  a list of `{"id": ..., "audio": PATH}`; the model loads once and one JSON
  line comes back per item, carrying its `id` (`{"error": ..., "id": ...}` when
  that item failed, so the batch goes on).
- **Output** is one JSON object on stdout (`features` writes its binary DC7F
  artifact instead). Nothing else goes to stdout; diagnostics go to stderr.
- **Audio** is decoded with the `--ffmpeg` the host passes (the bundle's own
  `tools/ffmpeg`), mono, 44.1 kHz, f32le, so every format ffmpeg reads works.
- **Identity.** A grid runner reports `algo_version` and a `cfg_hash` over the
  config it actually used. Change the algorithm, bump `algo_version`; change a
  default, the hash moves on its own. The same (algo_version, cfg_hash) on the
  same audio must give the same grid.
- **Grids** are constant-tempo lattices: `bpm`, `first_beat_ms`,
  `first_downbeat_ms`, `beats_per_bar`, and the `beats` list (index, time_ms,
  beat_number). The opening beat is bar 1. `origin_refine_ms` says how far the
  audio-anchored refiner moved the lattice. The cross-checker's bar-phase vote
  (`bt_phase_vote`, `bt_phase_agreement`) is reported, never applied.
- **Settings** (see `../settings`) arrive as flags; `algo.json` says which
  flag each setting feeds.

## Contributing one

1. Copy the closest directory. Keep the runner self-contained and offline.
2. Write `algo.json`: `name`, `kind` (`grid`, `key`, `features`, `cues`),
   `algo_version`, the flags with defaults and the settings they take,
   `requires` (Python packages, tools, checkpoints). A package the bundle does
   not ship goes into `bundle/python/analysis/pyproject.toml` and its lock, and
   a checkpoint into `bundle/build.sh`, so the bundle carries it.
3. Run it on the corpus: `go run ./cmd/grideval -corpus-dir DIR -fresh -algo NAME`
   scores the new grids against the rekordbox references and against the
   shipped engine. Put the scores in the pull request.
4. The hosts choose what to run; a new algorithm ships in the bundle first and
   the engines adopt it when its scores earn it.
