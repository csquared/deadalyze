# First round: the release, the corpus, speed and accuracy (2026-10-08)

## The release

`ml-v0.2.1` is the first bundle published from this repository:
https://github.com/csquared/deadalyze/releases/tag/ml-v0.2.1, both platforms,
checksums and manifest. `ml-v0.2.0` built both archives and could not
publish them: GitHub caps a release asset at 2 GiB and the archives were
2.23 GB (darwin/arm64) and 2.80 GB (linux/amd64). What came off:

| what                          | why it was there                                   | darwin  | linux   |
|-------------------------------|----------------------------------------------------|---------|---------|
| TensorFlow and its tree       | a declared dependency nothing imports              | 227 MB  | 591 MB  |
| roberta-base weights          | CLAP builds its text tower with `from_pretrained`, then overwrites every weight from its own checkpoint | 476 MB | 476 MB |

(wheel and file sizes, before the archive's own compression). The archives
are 1.68 GB and 1.90 GB now. The published darwin bundle was downloaded,
checksummed, extracted and run: the engine gives the same grids as the
installed `ml-v0.1.2` runtime and the embedder answers offline with a 6 MB
HF cache.

While removing the roberta weights: the audio embedding depended on the
RNG state at inference. librosa's resampled 10 s window is 480001 samples
against CLAP's clip length of 480000, and laion_clap meets a long input
with a random crop, so the vector moved by about 6e-4 with the seed and was
only stable because nothing consumed the RNG before it. The window is cut
to the clip length now; vectors are deterministic and differ from the
shipped ones by about 3e-4 (norm 0.94).

## The corpus

Built from what was on this machine, into `~/grids` (`-hash -keep-paths`):

| source                                   | tracks | grids                |
|------------------------------------------|--------|----------------------|
| stick DEADCA7-2 (`261d8dea…`)            | 795    | rekordbox 795        |
| stick SAYLESS1 (`fd3b1b65…`)             | 1283 new (273 shared) | rekordbox 1283 |
| DEADCA7 legacy library (`~/Music/DEADCA7`) | 798  | deadca7 784, manual 11 |

2181 tracks, 692 with two providers. Two things the data said that the
labels did not:

- **DEADCA7-2's "rekordbox" grids are the engine's own.** 660 of the 664
  tracks it shares with the legacy library match that library's grid to the
  millisecond. The stick was written by DEADCA7; scoring against it scores
  the engine against itself. SAYLESS1 is the stick to use as rekordbox's
  reference (`grideval -source fd3b1b65`).
- **The legacy library's `arbitrated` grids are machine grids.** The word
  meant DEADCA7's own consensus fix applied in place. Only the 11 marked
  `manual` are a person's; they are provider `manual` in the corpus now, so
  `-truth manual` scores an engine against them.

## Speed

**The harness.** `grideval -fresh` went through `engine.Analyze` a track at
a time: both models loaded once a track and the key detector ran on every
one, none of which the score reads. It is one `engine.AnalyzeBatch` with
the key off now. Three tracks: 30 s against about 20 s a track before, and
the model loads amortise further over a longer batch.

**Beat This on the GPU.** The hosts run both grid legs with `--device cpu`.
On this machine (Apple Silicon), Beat This on `mps` gives the same grid on
39 of 39 tracks that decoded (bpm, first beat within 1 ms, phase vote) at
a fraction of the CPU:

| leg, device    | 40 tracks wall | CPU time | per track |
|----------------|----------------|----------|-----------|
| beat_this, cpu | 163 s          | 559 s    | ~3.7 s    |
| beat_this, mps | 99 s           | 34 s     | ~2.3 s    |
| beatnet, cpu   |                |          | ~2.1 s    |
| beatnet, mps   |                |          | ~2.0 s    |

BeatNet does not move (madmom's DBN is the cost, and it is CPU). The
change is one default in the host: deadcatalog's
`analysis/internal/beat_this/beat_this.go` sets `Device: "cpu"`; "mps"
when `torch.backends.mps.is_available()` would halve the Beat This pass
and free the cores for everything else (the pass ran at 500 % CPU here).
The runner's `--device` already accepts it; no runner change, no hash
change.

**Where a batch's time goes.** The engine's batch is BeatNet over every
track, then Beat This over every track, then arbitration, features and
waveforms a track at a time; nothing lands until the Beat This pass ends.
On 1283 tracks the BeatNet pass took about 65 minutes with other work on
the machine and the Beat This pass is of the same order. A run that
streams results would need the engine to arbitrate as each track's second
leg lands, which is a deadcatalog change.

## Accuracy

**Against the person's grids** (`-truth manual`, 10 of 11 with audio
reachable): 10 of 10 pass, 3 disputed. Small, but it is the only human
reference there is, and the engine is on it.

**Against SAYLESS1's rekordbox grids** (`-fresh -source fd3b1b65`): 1276
tracks scored, 1115 pass (87 %), 152 disputed by the engine's own
cross-check. The run took 3 h 11 min with other work on the machine, 9.0 s a
track end to end. The failures:

| class                         | tracks | note                                                        |
|-------------------------------|--------|-------------------------------------------------------------|
| phase: one beat               | 71     | bar 1 a beat off; ours at the top of the track, theirs a beat either side (26 later, 17 earlier among the clean cases) |
| phase: half a bar (two beats) | 21     |                                                              |
| phase: a fraction             | 26     | 120–420 ms and ~800 ms offsets, 25 of 26 disputed            |
| phase: half a beat            | 19     | the offbeat lock arbitration exists for, 7 of 19 disputed    |
| tempo                         | 23     | acapellas, DJ sets, a 3:2 and a double; not the dance floor  |
| error                         | 1      | the malformed WAV below                                      |

By file type: AIFF 671 of 742 (90 %), MP3 363 of 436 (83 %), WAV 64 of 77.

A scoring fault came out of this run and is fixed: 223 of the 1276
references have their bar 1 before zero (rekordbox lists the first beat as
beat 2, 3 or 4 and the corpus lays bar 1 back from it). Nothing can start
there, and a grid whose bar 1 is the first audible downbeat agrees with
such a reference, but the whole-bar check counted the bar between them as
a fault: 30 grids in phase failed on bars alone. Whole bars are counted
only against an audible bar 1 now.

Bar phase at the top of the track is the round-two target: 90 of the 161
failures are whole-beat slips of the "1", which the lattice cannot see.
The failures were rerun with the vote and the arbitration record on each
score (`-only build/sayless1-failures.json`):

- **Arbitration is not the cause.** The phase relabel (the cross-checker's
  vote applied at ≥ 0.9 agreement) fired on 1 of the 90. The raw lattice
  is what rekordbox disagrees with.
- **Ours is early.** The raw error is −1 beat on 47, +1 on 20, ±2 on 22:
  ours calls the beat at the top of the track "1" (first downbeat at 0 ms
  on half of them) where rekordbox's "1" is the beat after it. A track
  that opens on a pickup, and a rule that cannot see one.
- **The vote knows some of it.** The cross-checker voted 0 (agreeing with
  our origin) on 40 of 90; on the other 50 its vote, applied regardless of
  agreement, would fix 42. At its current 0.9 threshold it fixes 1; at 0.8,
  20; at 0.6, 38. What a lower threshold breaks among the 1115 passes is
  the number that decides it, and it is being measured on a 150-track
  sample of them.
- **BeatNet's own downbeat labels are thrown away.** The runner keeps the
  DBN's beat/downbeat labels only to rank candidates for the tempo fit;
  the lattice's "1" is the top of the track by rule. On a pickup the DBN's
  label of the first beats is exactly the missing signal. A research copy
  of the runner that keeps the raw labels is being run over the 90 and the
  150 to see how often the DBN's "1" is rekordbox's.

Two runs of the same engine on the same 191 tracks did not agree
everywhere: 9 tracks moved by a fraction of a beat (a 0.3–0.7 beat origin
error on one run, within tolerance on the other), and the half-beat
shift arbitration fired differently on some. The origin estimate is not
deterministic run to run, which the harness should pin down before any
threshold is tuned on it.

**One track to listen to.** "Charlotte Moss - Beat Goes Round" (DEADCA7-2):
the stick's grid has bar 1 at 472 ms, the engine now says 3 ms, one beat
earlier. BeatNet's raw first beat is at 227 ms (half a beat off), Beat
This's lattice starts at 3 ms with its raw downbeats voting for 3 ms at
0.90 agreement, and arbitration moved ours onto Beat This's lattice. Which
beat is "1" at the top of a track is a call the house rule ("bar 1 is the
start of the track") cannot make; the only signal is the cross-checker's
downbeat vote, and here it disagrees with the stored grid.

**A decoder gap.** One SAYLESS1 WAV (`Notre Dame - Not Your Business`)
fails both legs: ffmpeg rejects its malformed LIST chunk (`too short LIST
tag`) while libsndfile reads it fine. The runners decode with the bundle's
ffmpeg only; a `soundfile` fallback in `decode()` would cover it, at the
cost of a runner hash change in every host.
