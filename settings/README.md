# Settings

A settings file is a tweak to the algorithms without a code change: a JSON
object whose keys are the tunables the hosts already pass to the runners.
`dance4x4.json` is the default every host ships (it is `config.Dance4x4()` in
deadcatalog and the same literal in DEADCA7).

| key             | type   | flag it feeds        | algorithms          |
|-----------------|--------|----------------------|---------------------|
| `min_bpm`       | number | `--min-bpm`          | beatnet             |
| `max_bpm`       | number | `--max-bpm`          | beatnet             |
| `beats_per_bar` | int    | `--beats-per-bar`    | beatnet, beat_this  |

`name` and `summary` are for people. Any other key is kept and ignored by the
hosts, so an algorithm can read its own tunables from the same file.

The engine folds the settings it was given into the runner flags, and the
runner folds the flags into its `cfg_hash`, so two grids made with different
settings never share an identity. Contribute a settings file by adding it
here with a name that says what it is for (`dnb.json`, `house_wide.json`) and
a `summary`; a settings file never changes the defaults in `dance4x4.json`.
