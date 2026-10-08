# deadalyze

The analysis side of deadca7, on its own: the engine, the bundle it ships
in, the algorithms it runs, the corpus they are judged against, and the
harnesses that do the judging. DEADCA7 and deadcatalog are its consumers:
each runs the bundle's engine behind one protocol (`docs/engine-protocol.md`)
and never a runner, a flag or a threshold of its own.

Four jobs:

1. **Be the engine.** `engine/` is the Rust executable a host runs
   (`bin/engine describe | analyze`): the recipe that combines the legs
   (`deadca7-v2`), the waveforms, the rekordbox timeline and the key mapping
   are native; the model legs are the Python runners in `algos/` until each
   is rewritten. `client/` is the Go side of the protocol; `protocol/` the
   contract as JSON Schema; `cmd/engineconf` proves an engine against it.
2. **Build the bundle.** `bundle/build.sh` makes `deadca7-ml-<goos>-<goarch>.tar.gz`:
   CPython, PyTorch, the analysis libraries, Beat This and CLAP checkpoints,
   ffmpeg, the stem separator, the algorithms and settings, and (layout 3)
   the engine. `docs/bundle-format.md` is the contract the hosts read.
3. **Do the research.** `cmd/grideval` scores grids against references,
   from a corpus without the runtime or fresh through the engine with it;
   `cmd/waveeval` scores waveforms against rekordbox's. `corpus/` is the
   corpus: one track-local catalog per track, every grid anyone made of it,
   no audio.
4. **Publish.** An `ml-v*` tag builds both platforms and publishes a GitHub
   release with the archives, checksums and manifest; deadca7.com serves the
   manifest the hosts resolve.

```
engine/       the engine (Rust): crates/protocol, crates/wave, engine (the binary)
protocol/     the protocol as JSON Schema: describe, request, event
client/       the Go client: resolve a bundle, run its engine, read events
algos/        the five legs (beatnet, beat_this, key, features, cues) and their algo.json
settings/     settings files: the runners' and the recipe's tunables, dance4x4.json the default
bundle/       build.sh, the Python projects and locks, the layout-1 scripts
corpus/       the corpus format and the Go package that builds and reads one
anlz/         the ANLZ sections waveeval reads (the format is deadcatalog's)
cmd/corpus    build | legacy | index | refs | stats
cmd/grideval  the beat-grid harness
cmd/waveeval  the waveform harness
cmd/engineconf  the conformance runner
cmd/bundle    manifest | inspect | verify
docs/         engine-protocol.md, bundle-format.md, porting.md, research/
```

Running the engine against an installed runtime that predates layout 3:

    export DEADCA7_BUNDLE="$HOME/Library/Application Support/deadcatalog/runtime/current"
    export DEADCA7_ENGINE=$PWD/engine/target/release/engine   # cargo build --release -p engine, in engine/
    export DEADCA7_ALGOS=$PWD/algos
    go run ./cmd/grideval -corpus-dir ~/grids -fresh -audio ~/Music -n 3

## Contributing

- **An algorithm or a setting tweak:** `algos/README.md` has the runner
  contract (flags in, JSON out, `algo_version` + `cfg_hash` as identity) and
  `settings/README.md` the settings keys. Score it on the corpus and put the
  numbers in the PR.
- **Data:** `corpus/README.md` says how to build a corpus from a library or a
  stick and what is in it (titles and grids; never paths or audio).

## Status

The engine and the protocol are here and the harnesses run on them; this
repository depends on nothing in deadcatalog. The hosts cut over next
(`docs/porting.md`): DEADCA7 first, deadcatalog by PR; until then they run
their embedded copies of the previous legs against the layout-1 keys of
the same bundle.
