# Algorithms

An algorithm is one directory here: `runner.py` and `algo.json`. The runner
is a Python script the engine runs on the bundle's interpreter with the
bundle's libraries (`PYTHONPATH=analysis/lib:lib`, `PYTHONNOUSERSITE=1`), and
nothing else: no venv, no network, no files outside the paths it is handed.

These five are the legs of the deadca7 engine. The engine (`../engine`, the
`bin/engine` of a layout-3 bundle) is the only thing that runs them: it
speaks the protocol in `../docs/engine-protocol.md` to the hosts, and this
contract to the legs. A host never launches a runner. As a leg is rewritten
natively inside the engine, its runner here stays as the reference the native
one is checked against (`../cmd/engineconf`) and is then retired.

Until the hosts have cut over (`../docs/porting.md`) deadcatalog still embeds
these with `go:embed` and DEADCA7 as Swift string literals, byte for byte;
`algos_test.go` pins the hashes here and checks the sibling checkouts.

## The leg contract

- **Input** is flags. One track is `--audio PATH`. A batch is `--items JSON`,
  a list of `{"id": ..., "audio": PATH}`; the model loads once and one JSON
  line comes back per item, carrying its `id` (`{"error": ..., "id": ...}` when
  that item failed, so the batch goes on). Every leg batches.
- **Output** is one JSON object on stdout (`features` writes its binary DC7F
  artifact instead, or one JSON line per item with the artifact base64 in
  `data` under `--items`). Nothing else goes to stdout; diagnostics go to
  stderr.
- **Audio** is decoded with the `--ffmpeg` the engine passes (the bundle's own
  `tools/ffmpeg`), mono, 44.1 kHz, f32le, so every format ffmpeg reads works.
  The grid legs read the file themselves for the model (librosa, soundfile)
  and use ffmpeg for the refiner; the engine, not the leg, puts the result on
  the rekordbox timeline.
- **Identity.** A leg reports `algo_version` and a `cfg_hash`. The hash is
  the sha256 of the canonical JSON (sorted keys, no whitespace, shortest
  round-trip floats) of the keys listed under `identity.keys` in the leg's
  `algo.json`, so a native implementation of the same leg hashes the same
  bytes. `device` and paths are never identity keys. Change the algorithm or
  the key set, bump `algo_version`; change a default, the hash moves on its
  own. The same (algo_version, cfg_hash) on the same audio must give the
  same answer, within the tolerances `engineconf` states.
- **Grids** are constant-tempo lattices: `bpm`, `first_beat_ms`,
  `first_downbeat_ms`, `beats_per_bar`, and the `beats` list (index, time_ms,
  beat_number). The opening beat is bar 1. `origin_refine_ms` says how far the
  audio-anchored refiner moved the lattice. The cross-checker's bar-phase vote
  (`bt_phase_vote`, `bt_phase_agreement`) is reported, never applied: the
  engine's recipe applies it.
- **Settings** (see `../settings`) arrive as flags; `algo.json` says which
  flag each setting feeds and whether it is part of the identity.

## Contributing one

1. Copy the closest directory. Keep the runner self-contained and offline.
2. Write `algo.json`: `name`, `kind` (`grid`, `key`, `features`, `cues`),
   `algo_version`, `identity.keys`, the flags with defaults and the settings
   they take, `requires` (Python packages, tools, checkpoints). A package the
   bundle does not ship goes into `bundle/python/analysis/pyproject.toml` and
   its lock, and a checkpoint into `bundle/build.sh`, so the bundle carries it.
3. Run it on the corpus: `go run ./cmd/grideval -corpus-dir DIR -fresh`
   scores the engine's grids against the rekordbox references. Put the scores
   in the pull request.
4. A new leg ships in the bundle first; the engine adopts it when its scores
   earn it.
