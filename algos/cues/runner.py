#!/usr/bin/env python3
# DEADCA7's hot-cue picker. Candidate positions are the phrase grid only (8-
# or 16-bar), so every cue lands on a downbeat at a musically sane phrase
# length; t=0 is skipped (the start of the track is not a mix point). Eight
# cues. The algorithm id is "{kind}{bars}":
#   mix     -- ends-weighted + novelty: cues only in the start region (mix-in)
#              and end region (mix-out), the middle left clear; within each
#              region the four highest-novelty phrase boundaries win.
#   novelty -- pure novelty across the whole track: the eight highest-novelty
#              phrase boundaries wherever they fall (cues the middle too).
# Novelty is a windowed MFCC timbre-change curve; sections spike.
import argparse
import json
import subprocess
import sys

import numpy as np

SR = 44100
BAR_BEATS = 4
N_CUES = 8
FRONT = 4
BACK = 4


def rekordbox_timeline(path):
    # ffmpeg input flags that decode on rekordbox's timeline, where the grid's
    # beats are: an MP3 keeps LAME's encoder delay (Go package timeline).
    return ["-flags2", "+skip_manual"] if path.lower().endswith(".mp3") else []


def decode(ffmpeg, path):
    raw = subprocess.run(
        [ffmpeg, "-v", "error", *rekordbox_timeline(path), "-i", path, "-ac", "1", "-ar", str(SR), "-f", "f32le", "-"],
        capture_output=True, check=True,
    ).stdout
    return np.frombuffer(raw, dtype=np.float32).astype(np.float64)


def novelty_curve(samples, beats):
    import librosa
    hop = 512
    mfcc = librosa.feature.mfcc(y=samples, sr=SR, n_mfcc=20, hop_length=hop)
    frames = np.clip(librosa.time_to_frames(beats, sr=SR, hop_length=hop), 0, mfcc.shape[1] - 1)
    n = len(frames)
    feats = np.empty((n, mfcc.shape[0]), dtype=np.float64)
    for i in range(n):
        a = int(frames[i])
        b = int(frames[i + 1]) if i + 1 < n else mfcc.shape[1]
        feats[i] = mfcc[:, a:max(a + 1, b)].mean(axis=1)
    win = BAR_BEATS
    nov = np.zeros(n, dtype=np.float64)
    for i in range(win, n - win):
        before = feats[i - win:i].mean(axis=0)
        after = feats[i:i + win].mean(axis=0)
        nov[i] = np.linalg.norm(after - before)
    if nov.max() > 0:
        nov = nov / nov.max()
    return nov


def downbeat_indices(downbeats, beats):
    # Each real downbeat time to its index in the real beats array (nearest
    # beat), so cues land on actual downbeat times, nothing reconstructed.
    return [int(np.argmin(np.abs(beats - d))) for d in downbeats]


def phrase_positions(db_idx, phrase_bars):
    # Every phrase_bars-th downbeat is a phrase boundary; the very first
    # downbeat (t~0) is skipped.
    return [(db_idx[i], i) for i in range(phrase_bars, len(db_idx), phrase_bars)]


def pick_mix(cands, nov):
    if not cands:
        return []
    third = max(1, len(cands) // 3)
    start, end = cands[:third], cands[-third:]
    front = sorted(start, key=lambda c: -nov[c[0]])[:FRONT]
    back = sorted(end, key=lambda c: -nov[c[0]])[:BACK]
    return sorted(set(front) | set(back), key=lambda c: c[0])


def pick_novelty(cands, nov):
    return sorted(sorted(cands, key=lambda c: -nov[c[0]])[:N_CUES], key=lambda c: c[0])


def parse_algo(algo):
    bars = 16 if algo.endswith("16") else 8
    kind = "novelty" if algo.startswith("novelty") else "mix"
    return kind, bars


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--audio", required=True)
    ap.add_argument("--beats", required=True, help="JSON list of beat times in seconds")
    ap.add_argument("--downbeats", required=True, help="JSON list of downbeat times in seconds")
    ap.add_argument("--algo", default="mix16")
    ap.add_argument("--ffmpeg", default="ffmpeg")
    args = ap.parse_args()

    beats = np.asarray(json.loads(args.beats), dtype=np.float64)
    downbeats = np.asarray(json.loads(args.downbeats), dtype=np.float64)
    if beats.size < BAR_BEATS * 16 or downbeats.size < 2:
        print(json.dumps({"cues": []}))
        return

    samples = decode(args.ffmpeg, args.audio)
    nov = novelty_curve(samples, beats)

    db_idx = downbeat_indices(downbeats, beats)
    anchor_idx = db_idx[0] if db_idx else 0
    kind, phrase_bars = parse_algo(args.algo)
    cands = phrase_positions(db_idx, phrase_bars)
    chosen = pick_novelty(cands, nov) if kind == "novelty" else pick_mix(cands, nov)

    cues = []
    for slot, (bi, _bar) in enumerate(chosen):
        # The bar number comes from the chosen beat's index relative to the
        # first downbeat, so the label can never drift from the position.
        bar_num = (bi - anchor_idx) // BAR_BEATS
        cues.append({"comment": "bar %d" % bar_num, "hot_cue": slot + 1, "time_ms": int(round(float(beats[bi]) * 1000))})
    print(json.dumps({"algo": args.algo, "cues": cues}))


if __name__ == "__main__":
    sys.exit(main())
