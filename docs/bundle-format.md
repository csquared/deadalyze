# The analysis bundle

The bundle is the Python runtime the deadca7 engine runs on: an interpreter,
PyTorch, the analysis libraries, the model checkpoints, ffmpeg, and the stem
separator. App and CLI releases stay small; a host installs one bundle on
demand and points every runner at it. deadalyze builds and publishes it.

## Names

| thing            | value                                            |
|------------------|--------------------------------------------------|
| archive          | `deadca7-ml-<goos>-<goarch>.tar.gz`              |
| platforms        | `darwin/arm64`, `linux/amd64`                    |
| release tag      | `ml-vX.Y.Z` (a GitHub release on this repo)      |
| release manifest | `downloads/ml/<tag>/manifest.json` on deadca7.com, `latest` allowed |

The names are the ones DEADCA7 (`Engine/Runtime/RuntimeInstaller.swift`) and
deadcatalog (`analysis/runtime`) already resolve. They do not change with the
move; what changes is which repository the release lives in, and deadca7.com
follows that with its `DEADCA7_RELEASE_REPO` setting.

## The release manifest

What a host fetches first. One version, one asset per platform:

```json
{
  "version": "ml-v0.2.0",
  "assets": {
    "darwin/arm64": {
      "name": "deadca7-ml-darwin-arm64.tar.gz",
      "sha256": "…",
      "url": "https://github.com/csquared/deadalyze/releases/download/ml-v0.2.0/deadca7-ml-darwin-arm64.tar.gz"
    }
  }
}
```

`go run ./cmd/bundle manifest` writes it from the build's `checksums.txt`.
deadca7.com writes the same shape on the fly from the GitHub release.

## The archive

One top-level directory, `deadca7-ml-<goos>-<goarch>/`, holding `manifest.json`
and these paths. A host validates a root by three of them: `python/bin/python3`,
`lib`, `analysis/lib`.

| path                    | what                                                           | layout |
|-------------------------|----------------------------------------------------------------|--------|
| `manifest.json`         | the bundle manifest below                                      | 1      |
| `python/`               | managed CPython 3.11                                           | 1      |
| `lib/`                  | PyTorch, shared by analysis and stems                          | 1      |
| `analysis/lib/`         | the analysis dependencies from `bundle/python/analysis/uv.lock` | 1     |
| `beatthis/final0.ckpt`  | the Beat This checkpoint (`beatthis/grid.py` beside it is the DEADCA7 server's runner) | 1 |
| `embed/`                | CLAP: `embed.py`, `models/`, an offline `hfcache/`             | 1      |
| `stems/`                | `separate.py`, `lib/`, `models/` (BS-Roformer)                 | 1      |
| `tools/ffmpeg`, `tools/ffprobe` | static builds; the hosts prefer these to PATH          | 1      |
| `algos/<name>/runner.py`, `algos/<name>/algo.json` | the algorithms, see `../algos`      | 2      |
| `settings/*.json`       | the settings files, see `../settings`                          | 2      |

The hosts run a runner as

    python/bin/python3 <runner> <flags>
    PYTHONPATH=analysis/lib:lib  PYTHONNOUSERSITE=1

(stems with `PYTHONPATH=stems/lib:lib`). Nothing in the bundle is imported
from anywhere else, and nothing reaches the network: `build.sh` proves the
embed tower answers with `HF_HUB_OFFLINE=1` before it archives.

On macOS every Mach-O file is signed with the Developer ID when one is in the
keychain (hardened runtime, JIT and unsigned-memory entitlements, which
PyTorch needs), ad hoc otherwise.

## The bundle manifest

`manifest.json` inside the archive names every path above so a host never
hard-codes a layout. `layout` says which generation it is.

```json
{
  "name": "deadca7-ml",
  "version": "ml-v0.2.0",
  "platform": "darwin/arm64",
  "layout": 2,
  "created_at": "2026-10-08T00:00:00Z",
  "python": "python/bin/python3",
  "lib": "lib",
  "analysis_lib": "analysis/lib",
  "algos": "algos",
  "algo_names": ["beat_this", "beatnet", "cues", "features", "key"],
  "settings": "settings",
  "beatthis_script": "beatthis/grid.py",
  "beatthis_checkpoint": "beatthis/final0.ckpt",
  "embed_hfcache": "embed/hfcache",
  "embed_models": "embed/models",
  "embed_script": "embed/embed.py",
  "stems_script": "stems/separate.py",
  "stems_lib": "stems/lib",
  "stems_models": "stems/models",
  "tools": "tools"
}
```

Layout 2 is layout 1 plus `algos`, `algo_names` and `settings`. Every
layout-1 key keeps its name and meaning, so a layout-1 host (DEADCA7 and
deadcatalog today) installs a layout-2 bundle and reads it unchanged. A host
that knows layout 2 may run the bundle's `algos/<name>/runner.py` instead of
its embedded copy, and may offer `settings/` in its UI.

## Versions

Three things version independently and a grid names all three:

- the bundle: `ml-vX.Y.Z`, the runtime tag (`RuntimeVersion` on a result);
- the algorithm: `algo_version` inside the runner (`beatnet-dbn-v14`), plus
  the `cfg_hash` of the tunables it ran with;
- the recipe: deadcatalog's `AnalysisVersion` (`deadca7-v1`), which says how
  the engine arbitrates the runners into one answer.

A bundle release that changes no algorithm bumps the patch; one that adds an
algorithm or a dependency bumps the minor; one that changes a layout-1 path
would bump `layout`, and does not happen without a host release that reads it.

## Building and publishing

    ./bundle/build.sh                    # this platform, into bundle/dist/
    ./bundle/build.sh --no-sign          # local, ad hoc
    go run ./cmd/bundle inspect bundle/dist/deadca7-ml-darwin-arm64.tar.gz

Python runtimes are built on the target platform; there is no cross build.
`.github/workflows/bundle-release.yml` builds both platforms on an `ml-v*` tag
and publishes the archives and `checksums.txt` as the release's assets. It is
skipped when nothing under `bundle/`, `algos/` or `settings/` changed since
the previous `ml-v*` tag.

The first release from here is `ml-v0.2.0`, the second bundle: the same
runtime as `ml-v0.1.2` (built from nzoschke/deadca7) in layout 2. To cut
over, deadca7.com's `DEADCA7_RELEASE_REPO` becomes `csquared/deadalyze`; no
host changes.
