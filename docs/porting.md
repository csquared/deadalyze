# What moved here, from where, and the cutover

deadalyze gathers the analysis bundle, the algorithms, the engine that runs
them and the research tools that were spread across two repositories. This
is the map, and the record of the cutover to the engine protocol.

## The map

| here                              | from                                                       | notes |
|-----------------------------------|------------------------------------------------------------|-------|
| `bundle/build.sh`                 | deadca7-old `ml/build.sh`                                  | paths repointed; stages `algos/`, `settings/` and `bin/engine`; manifest layout 3 |
| `bundle/python/analysis/*`        | deadca7-old `app/analysis/{pyproject.toml,uv.lock,beatthis,embed}` | the uv workspace, unchanged |
| `bundle/python/stems/*`           | deadca7-old `app/stems/{pyproject.toml,uv.lock,separate.py}` | unchanged |
| `bundle/python/beatthis/grid.py`  | deadca7-old `app/analysis/beatthis/grid.py`                | the server's Beat This runner; kept for layout 1 |
| `bundle/python/embed/embed.py`    | deadca7-old `app/analysis/embed/embed.py`                  | CLAP embeddings for tag suggestions |
| `.github/workflows/bundle-release.yml` | deadca7-old `.github/workflows/ml-release.yml`        | same jobs; change detection over `bundle/ algos/ settings/ engine/ protocol/` |
| `algos/*/runner.py`               | deadcatalog `analysis/{internal/beatnet,internal/beat_this,key,features,cues}/runner.py` | the legs: now with identity keys, `--device auto`, batch modes for every leg, cues from a DC7F file |
| `engine/`                         | new (Rust)                                                 | the engine: `docs/engine-protocol.md`; native ports of deadcatalog's `analysis/consensus.go` + `engine.go: arbitrate`, `analysis/waveform` + `wavecolor` (byte for byte), `analysis/timeline`, `analysis/key.Camelot`, `analysis/features.Decode` |
| `protocol/`                       | new                                                        | the contract as JSON Schema |
| `client/`                         | new (Go)                                                   | the Go side of the protocol, for the harnesses here and for deadcatalog |
| `anlz/`                           | deadcatalog `anlz` (reader) + `anlz/export` (PWV2)         | the sections waveeval scores, field for field; deadcatalog owns the format |
| `corpus/schema.sql`               | deadcatalog `catalog/schema/00001_schema.sql`              | a copy, so a corpus catalog opens in `dc`; deadcatalog owns the spec |
| `cmd/grideval`, `cmd/waveeval`    | deadcatalog `cmd/grideval`, `cmd/waveeval`                 | now clients of the engine |
| `cmd/engineconf`                  | new                                                        | the conformance runner |
| `fixtures/grideval`, `fixtures/waveeval`, `fixtures/engineconf` | deadcatalog `fixtures/`; new     | references, floors, goldens |
| `corpus/`, `cmd/corpus`, `cmd/bundle` | new                                                    |       |

## What stays where

- deadcatalog keeps `analysis/runtime` (resolving and installing a bundle),
  the catalog schema and the CLI. Its `analysis/engine` becomes a client of
  the bundle's engine through `client/` here (the `engine-client` branch).
- DEADCA7 keeps `Engine/Runtime/RuntimeInstaller.swift`, its catalog write
  and its native cue picker; its pipeline becomes `EngineClient.swift`.
- deadca7.com (deadca7-old `www/internal/handler/install.go`) keeps serving
  `downloads/ml/<tag>/manifest.json` from GitHub releases.

## The cutover

1. `ml-v0.2.1` (2026-10-08): the first bundle from here, layout 2; the hosts
   still ran their embedded runners. Done.
2. The engine protocol (2026-10-08): `docs/engine-protocol.md`; the recipe
   moved into `engine/` as `deadca7-v2` with the vote relabel at 0.7 and the
   device chosen by the engine; the legs' identities re-based on named key
   sets with device out (`beatnet-dbn-v15`, `beat-this-v2`). deadalyze no
   longer depends on deadcatalog. Done.
3. `ml-v0.3.0`: layout 3, `bin/engine` in the archive. The layout-1 keys are
   unchanged, so a host that has not cut over installs it and keeps working.
4. DEADCA7 cuts over: one `engine analyze` per chunk, `analyses.version`
   "2", the sha256 pins replaced by a `describe` check. deadca7.com sets
   `DEADCA7_RELEASE_REPO=csquared/deadalyze` so the app installs from here.
5. deadcatalog cuts over by PR (`engine-client`): `analysis/engine` a client,
   the Go recipe and runner copies deleted, `dc analyze` one batch per run.
6. deadca7-old's `ml/` and `.github/workflows/ml-release.yml` are deleted.

`algos/algos_test.go` pins the legs' hashes; the sibling checks skip until
the hosts have cut over, after which they become a protocol check.
