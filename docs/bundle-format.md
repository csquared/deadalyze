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
| release manifest | `manifest.json`, an asset of the release: `https://github.com/csquared/deadalyze/releases/download/<tag>/manifest.json`, or `releases/latest/download/manifest.json` |

The names are the ones DEADCA7 (`Engine/Runtime/RuntimeInstaller.swift`) and
deadcatalog (`analysis/runtime`) resolve. A host fetches the manifest from
the GitHub release itself (DEADCA7 does, from its cutover on); deadca7.com
may mirror it at `downloads/ml/<tag>/manifest.json` for a host that is
pointed there (`DEADCATALOG_RUNTIME_URL`), and is not required.

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

`go run ./cmd/bundle manifest` writes it from the build's `checksums.txt`,
and the release workflow publishes it beside the archives.

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
| `bin/engine`            | the engine: what a layout-3 host runs, see `engine-protocol.md` | 3     |

A layout-3 host runs the engine, with the bundle root as its working
directory, and nothing else:

    bin/engine describe
    bin/engine analyze < request.json

The engine runs a runner as

    python/bin/python3 <runner> <flags>
    PYTHONPATH=analysis/lib:lib  PYTHONNOUSERSITE=1

(stems with `PYTHONPATH=stems/lib:lib`), which is also how a layout-1 host
still runs its embedded copy. Nothing in the bundle is imported from
anywhere else, and nothing reaches the network: `build.sh` proves the
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
  "layout": 3,
  "created_at": "2026-10-08T00:00:00Z",
  "python": "python/bin/python3",
  "lib": "lib",
  "analysis_lib": "analysis/lib",
  "algos": "algos",
  "algo_names": ["beat_this", "beatnet", "cues", "features", "key"],
  "settings": "settings",
  "engine": "bin/engine",
  "protocols": [1],
  "recipe": "deadca7-v2",
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

Layout 2 is layout 1 plus `algos`, `algo_names` and `settings`. Layout 3
is layout 2 plus `engine`, `protocols` and `recipe`. Every layout-1 key
keeps its name and meaning, so a layout-1 host (DEADCA7 and deadcatalog
before their cutover) installs a layout-3 bundle and reads it unchanged. A
host that knows layout 3 runs `engine` and nothing else; `protocols` says
which protocol versions it speaks and `recipe` which recipe it carries, so
a host can refuse a bundle before launching it.

## Versions

Three things version independently and a grid names all three:

- the bundle: `ml-vX.Y.Z`, the runtime tag (`RuntimeVersion` on a result);
- the algorithm: `algo_version` inside the runner (`beatnet-dbn-v14`), plus
  the `cfg_hash` of the tunables it ran with;
- the recipe: the engine's (`deadca7-v2`), which says how the engine
  arbitrates the legs into one answer; a host stores it, never defines it.

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
