# Settings

A settings file is a tweak to the analysis without a code change: a JSON
object whose keys are the tunables of the runners and of the recipe that
combines them. `dance4x4.json` is the default every host ships. A host hands
the whole document to the engine as the request's `settings`
(`../docs/engine-protocol.md`); the engine's `describe` lists every key it
knows with its type, default, range and whether it is part of a grid's
identity, which is what a settings UI renders.

## The runners' keys

| key             | type   | feeds                     | algorithms         | identity |
|-----------------|--------|---------------------------|--------------------|----------|
| `min_bpm`       | number | `--min-bpm`               | beatnet            | yes      |
| `max_bpm`       | number | `--max-bpm`               | beatnet            | yes      |
| `beats_per_bar` | int    | `--beats-per-bar`         | beatnet, beat_this | yes      |
| `origin_refine` | bool   | `--no-refine` when false  | beatnet, beat_this | yes      |
| `device`        | string | `--device`; `auto` picks `mps` where torch has it, else `cpu` | beatnet, beat_this, key | no |

## The recipe's keys

How the engine turns two grid legs into one answer (`deadca7-v2`). The
thresholds were calibrated in DEADCA7 on a 100-track double run (2026-07-02)
and the relabel threshold re-set on 1283 rekordbox grids
(`../docs/research/2026-10-08-first-round.md`): votes between 0.7 and 0.9
agreement were right on every one of the 30 failures they fix and moved no
right answer in a 150-track pass sample.

| key                              | default | meaning |
|----------------------------------|---------|---------|
| `consensus`                      | true    | run the Beat This cross-check at all |
| `arbitrate`                      | true    | apply the two deterministic fixes (false: only flag disputes) |
| `arbitrate_min_vote_agreement`   | 0.7     | relabel bar 1 on the cross-checker's downbeat vote at this agreement or more |
| `arbitrate_half_beat_slop_ms`    | 60      | how far from half a beat apart the lattices may be and still count as the offbeat lock |
| `consensus_max_bpm_delta`        | 0.05    | tempo disagreement that makes a dispute |
| `consensus_max_origin_ms`        | 60      | origin disagreement (folded by whole beats) that makes a dispute |
| `consensus_max_phase_beats`      | 0.5     | downbeat disagreement (folded into the bar) that makes a dispute |
| `consensus_min_vote_agreement`   | 0.6     | the vote is reported as a dispute at this agreement or more |

`name` and `summary` are for people. Any other key is kept and ignored, so
an algorithm can read its own tunables from the same file.

Every key marked identity goes into a leg's `cfg_hash` (the key set is in
the leg's `algo.json`), so two grids made with different settings never
share an identity; `device` does not, because it does not change the grid.
Contribute a settings file by adding it here with a name that says what it
is for (`dnb.json`, `house_wide.json`) and a `summary`; a settings file
never changes the defaults in `dance4x4.json`.
