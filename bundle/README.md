# bundle

Builds the analysis bundle, the separately distributed runtime the deadca7
engine runs on. `docs/bundle-format.md` is the contract; this is the how.

```sh
./bundle/build.sh                 # this platform, signed with the Developer ID in the login keychain
./bundle/build.sh --no-sign       # ad hoc, for a local test
APPLE_CODESIGN_IDENTITY="Developer ID Application: …" ./bundle/build.sh
DEADCATALOG_RUNTIME=$PWD/bundle/build.noindex/deadca7-ml-darwin-arm64 go run ./cmd/grideval -corpus-dir ~/grids -fresh -audio ~/Music
```

Outputs land in `bundle/dist/`: the archive and `checksums.txt`. The stage
under `bundle/build.noindex/` is a valid runtime root; point a host at it
with `DEADCATALOG_RUNTIME` to test before a release. `uv` must be on PATH;
the first build downloads the interpreter, the wheels and the checkpoints
into `bundle/build.noindex/cache` and takes a while.

GitHub caps a release asset at 2 GiB, so the archive has to stay under it:
ml-v0.2.0 did not (2.2 GB and 2.8 GB) and its release failed. Nothing the
runners import is optional, so the weight comes off elsewhere: TensorFlow is
gone (nothing imported it), and roberta-base's weights are not shipped
(`embed.py` builds the text tower from its config and the CLAP checkpoint
fills it).

`python/analysis` is a uv workspace (`beatthis`, `embed` members) whose lock
pins every analysis dependency; `python/stems` is the stem separator's. Add a
dependency there and re-lock (`uv lock --project bundle/python/analysis`); the
build exports the locks and installs them without resolution.
