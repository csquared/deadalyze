# deadalyze

The analysis side of deadca7, on its own: the bundle the engine runs on, the
algorithms it runs, the corpus they are judged against, and the harnesses
that do the judging. DEADCA7 and deadcatalog are its consumers.

Three jobs:

1. **Build the bundle.** `bundle/build.sh` makes `deadca7-ml-<goos>-<goarch>.tar.gz`:
   CPython, PyTorch, the analysis libraries, Beat This and CLAP checkpoints,
   ffmpeg, the stem separator, and (new in layout 2) the algorithms and
   settings. `docs/bundle-format.md` is the contract the hosts read.
2. **Do the research.** `cmd/grideval` scores grids against references,
   from a corpus without the runtime or fresh from the audio with it;
   `cmd/waveeval` scores waveforms against rekordbox's. `corpus/` is the
   corpus: one track-local catalog per track, every grid anyone made of it,
   no audio.
3. **Publish.** An `ml-v*` tag builds both platforms and publishes a GitHub
   release with the archives, checksums and manifest; deadca7.com serves the
   manifest the hosts resolve.

```
algos/        the five runners the engine ships (beatnet, beat_this, key, features, cues) and their algo.json
settings/     settings files: tunables as JSON, dance4x4.json the default
bundle/       build.sh, the Python projects and locks, the layout-1 scripts
corpus/       the corpus format and the Go package that builds and reads one
cmd/corpus    build | legacy | index | refs | stats
cmd/grideval  the beat-grid harness
cmd/waveeval  the waveform harness
cmd/bundle    manifest | inspect | verify
docs/         bundle-format.md, porting.md
```

## Contributing

- **An algorithm or a setting tweak:** `algos/README.md` has the runner
  contract (flags in, JSON out, `algo_version` + `cfg_hash` as identity) and
  `settings/README.md` the settings keys. Score it on the corpus and put the
  numbers in the PR.
- **Data:** `corpus/README.md` says how to build a corpus from a library or a
  stick and what is in it (titles and grids; never paths or audio).

## Status

Not cut over. The hosts still install `ml-v0.1.2` from nzoschke/deadca7 and
embed their own runner copies; `algos/algos_test.go` holds all three copies
to one hash. `docs/porting.md` lists what came from where and the cutover
steps. Building needs the sibling `../deadcatalog` checkout (see `go.mod`).
