# What moved here, and from where

deadalyze gathers the analysis bundle, the algorithms and the research tools
that were spread across two repositories. Nothing was cut over yet: the hosts
still install `ml-v0.1.2` from nzoschke/deadca7 through deadca7.com, and
still embed their own copies of the runners. This is the map.

| here                              | from                                                       | notes |
|-----------------------------------|------------------------------------------------------------|-------|
| `bundle/build.sh`                 | deadca7-old `ml/build.sh`                                  | paths repointed; stages `algos/` and `settings/`; manifest layout 2 |
| `bundle/python/analysis/*`        | deadca7-old `app/analysis/{pyproject.toml,uv.lock,beatthis,embed}` | the uv workspace, unchanged |
| `bundle/python/stems/*`           | deadca7-old `app/stems/{pyproject.toml,uv.lock,separate.py}` | unchanged |
| `bundle/python/beatthis/grid.py`  | deadca7-old `app/analysis/beatthis/grid.py`                | the server's Beat This runner; kept for layout 1 |
| `bundle/python/embed/embed.py`    | deadca7-old `app/analysis/embed/embed.py`                  | CLAP embeddings for tag suggestions |
| `.github/workflows/bundle-release.yml` | deadca7-old `.github/workflows/ml-release.yml`        | same jobs; change detection over `bundle/ algos/ settings/` |
| `algos/*/runner.py`               | deadcatalog `analysis/{internal/beatnet,internal/beat_this,key,features,cues}/runner.py` | byte for byte; also the Swift literals in deadca7 `Engine/Analysis/RunnerScripts.swift` |
| `cmd/grideval`                    | deadcatalog `cmd/grideval`                                 | plus the corpus mode; the rekordbox-truth idea is deadca7-old `app/cmd/grideval` |
| `cmd/waveeval`                    | deadcatalog `cmd/waveeval`                                 | `-export` is required (no fixture here) |
| `fixtures/grideval`, `fixtures/waveeval` | deadcatalog `fixtures/`                             | the reference and floor files only |
| `corpus/`, `cmd/corpus`, `cmd/bundle` | new                                                    |       |

## What stays where

- deadcatalog keeps `analysis/runtime` (resolving and installing a bundle),
  `analysis/engine` (arbitrating the runners into a grid) and its embedded
  runner copies. The engine is a consumer of the bundle and the algorithms.
- DEADCA7 keeps `Engine/Runtime/RuntimeInstaller.swift` and its runner
  literals, pinned by hash in `AnalysisCoreTests`.
- deadca7.com (deadca7-old `www/internal/handler/install.go`) keeps serving
  `downloads/ml/<tag>/manifest.json` from GitHub releases.

`algos/algos_test.go` holds the three copies together: it pins each runner's
sha256, reads deadcatalog's files and DEADCA7's pins when the checkouts are
beside this one, and fails when any drifts.

## Cutting over

1. Tag `ml-v*` here; the workflow publishes the bundle. Done: `ml-v0.2.1`
   (2026-10-08) is the first; `ml-v0.2.0` built but its archives were over
   GitHub's 2 GiB asset cap, see `bundle/README.md`.
2. On deadca7.com set `DEADCA7_RELEASE_REPO=csquared/deadalyze`. Hosts see
   `ml-v0.2.1` as the latest and install it; layout 1 keys are unchanged.
3. deadcatalog and DEADCA7 point their runner copies at `algos/` (a `go:embed`
   of a vendored copy, a generated Swift file), with the pin test as the gate.
4. deadca7-old's `ml/` and `.github/workflows/ml-release.yml` are deleted.
