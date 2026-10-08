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
#
# The curve is computed from DC7F features (the features leg's artifact:
# 20 Hz pooled MFCCs), the same lookup-and-sort the deadca7 host does from
# its stored features (apple/Sources/Engine/Analysis/CuePicker.swift), so
# `--features PATH` (or `-` for stdin) needs no audio. `--audio` is the
# fallback: it computes the same DC7F MFCCs in memory (the pooling copied
# from features/runner.py, rounded through float16 as the file would be)
# and picks from those, so both paths give the same cues.
#
# Identity (algos/README.md): algo_version is the algorithm id and the one
# identity key is "algo".
import argparse
import hashlib
import json
import math
import os
import struct
import subprocess
import sys
import tempfile
import warnings

import numpy as np

SR = 44100
BAR_BEATS = 4
N_CUES = 8
FRONT = 4
BACK = 4

# DC7F recipe 1 constants, as features/runner.py has them.
FRAME_HZ = 20
HOP = 512
N_MFCC = 20
N_BANDS = 3
DC7F_HEADER = struct.Struct("<4sHHIBBHI32s12x")


# --- DC7F in: the file, or the same MFCCs computed in memory ---------------


def read_dc7f(data):
    # The DC7F layout (features/runner.py, Features.swift): a 64-byte header,
    # then float16 values channel-major (all low-band frames, mid, high, then
    # MFCC 0's frames, MFCC 1's, ...). Returns (frame_hz, mfcc[n_mfcc, frames]).
    if len(data) < DC7F_HEADER.size:
        raise ValueError("features too short for a DC7F header")
    magic, version, frame_hz, frames, bands, mfccs, _reserved, duration_ms, _digest = DC7F_HEADER.unpack_from(data)
    if magic != b"DC7F" or version != 1:
        raise ValueError("not a DC7F version 1 file")
    if frame_hz != FRAME_HZ or bands != N_BANDS or mfccs != N_MFCC or frames == 0 or duration_ms == 0:
        raise ValueError("invalid features dimensions")
    expected = DC7F_HEADER.size + frames * (bands + mfccs) * 2
    if len(data) != expected:
        raise ValueError(f"features size {len(data)} != {expected}")
    values = np.frombuffer(data, dtype="<f2", offset=DC7F_HEADER.size).reshape(bands + mfccs, frames)
    return int(frame_hz), values[bands:].astype(np.float64)


def rekordbox_timeline(path):
    # ffmpeg input flags that decode on rekordbox's timeline, where the grid's
    # beats are: an MP3 keeps LAME's encoder delay (Go package timeline).
    return ["-flags2", "+skip_manual"] if path.lower().endswith(".mp3") else []


def decode(ffmpeg, path):
    # features/runner.py's decode: a seekable snapshot so decoder delay and
    # padding are handled as they are there, and trailing metadata is read.
    with tempfile.TemporaryDirectory(prefix="dc-cues-") as temp:
        snapshot = os.path.join(temp, "audio" + os.path.splitext(path)[1])
        with open(path, "rb") as source, open(snapshot, "wb") as target:
            while block := source.read(1024 * 1024):
                target.write(block)
        result = subprocess.run(
            [ffmpeg, "-nostdin", "-v", "error", *rekordbox_timeline(snapshot), "-i", snapshot, "-ac", "1",
             "-ar", str(SR), "-f", "f32le", "-"],
            capture_output=True, check=True,
        )
    samples = np.frombuffer(result.stdout, dtype="<f4").astype(np.float64)
    if not samples.size:
        raise ValueError("audio decoded no samples")
    if not np.isfinite(samples).all():
        raise ValueError("nonfinite decoded audio")
    return samples


def pooled_mfcc(samples, frames):
    # features/runner.py's pooled_mfcc, verbatim.
    import librosa

    mel = librosa.feature.melspectrogram(
        y=samples, sr=SR, hop_length=HOP,
        n_fft=2048, win_length=2048, window="hann", center=True,
        pad_mode="constant", power=2.0, n_mels=128, fmin=0.0,
        fmax=SR / 2, htk=False, norm="slaney",
    )
    mfcc = librosa.feature.mfcc(
        S=librosa.power_to_db(mel, ref=1.0, amin=1e-10, top_db=80.0),
        n_mfcc=N_MFCC, dct_type=2, norm="ortho", lifter=0,
    )
    # Assign each centered MFCC frame by its timestamp to a half-open 50ms bin.
    boundaries = (np.arange(frames + 1, dtype=np.int64) * SR + FRAME_HZ * HOP - 1) // (FRAME_HZ * HOP)
    pooled = np.empty((N_MFCC, frames), dtype=np.float64)
    for i in range(frames):
        a = min(int(boundaries[i]), mfcc.shape[1] - 1)
        b = min(max(a + 1, int(boundaries[i + 1])), mfcc.shape[1])
        pooled[:, i] = mfcc[:, a:b].mean(axis=1)
    return pooled


def mfcc_from_audio(ffmpeg, path):
    # The DC7F MFCC channels for this audio, as the features leg would write
    # them: the frame count is the band-RMS clock (one frame per 50 ms,
    # the last partial), and the values go through float16 as the file's do.
    samples = decode(ffmpeg, path)
    frames = int(math.ceil(samples.size / (SR // FRAME_HZ)))
    mfcc = pooled_mfcc(samples, frames).astype("<f2")
    if not np.isfinite(mfcc).all():
        raise ValueError("features exceed finite float16 range")
    return FRAME_HZ, mfcc.astype(np.float64)


# --- The picker, as CuePicker.swift computes it from the stored features --


def novelty_curve(mfcc, frame_hz, beats_ms):
    # CuePicker.noveltyCurve: the frames of each beat averaged (a beat's
    # frame is floor(ms / 1000 * hz), the last beat runs to the end), each
    # beat scored by the distance between the mean of the four beats before
    # it and the four after, the biggest 1.
    frames = mfcc.shape[1]
    n = len(beats_ms)

    def frame_of(ms):
        return min(max(int(math.floor(ms / 1000 * frame_hz)), 0), frames - 1)

    feats = np.empty((n, mfcc.shape[0]), dtype=np.float64)
    for i in range(n):
        a = frame_of(beats_ms[i])
        b = frame_of(beats_ms[i + 1]) if i + 1 < n else frames
        if b < a + 1:
            b = a + 1
        feats[i] = mfcc[:, a:b].mean(axis=1)
    win = BAR_BEATS
    nov = np.zeros(n, dtype=np.float64)
    for i in range(win, n - win):
        before = feats[i - win:i].mean(axis=0)
        after = feats[i:i + win].mean(axis=0)
        nov[i] = float(np.sqrt(np.sum((after - before) ** 2)))
    top = float(nov.max()) if n else 0.0
    if top > 0:
        nov = nov / top
    return nov


def downbeat_indices(downbeats_ms, beats_ms):
    # Each downbeat as the index of the nearest beat (the first of equals),
    # so cues land on actual beat times, nothing reconstructed.
    beats = np.asarray(beats_ms, dtype=np.int64)
    return [int(np.argmin(np.abs(beats - d))) for d in downbeats_ms]


def phrase_positions(db_idx, phrase_bars):
    # Every phrase_bars-th downbeat is a phrase boundary; the very first
    # downbeat (t~0) is skipped.
    return [db_idx[i] for i in range(phrase_bars, len(db_idx), phrase_bars)]


def top_by_novelty(cands, nov, n):
    # The n most novel; ties keep time order (a stable sort).
    return sorted(cands, key=lambda c: -nov[c])[:n]


def pick_mix(cands, nov):
    # The mixing layout: the four most novel boundaries of the first third,
    # the four of the last, in time order.
    if not cands:
        return []
    third = max(1, len(cands) // 3)
    picked = top_by_novelty(cands[:third], nov, FRONT) + top_by_novelty(cands[-third:], nov, BACK)
    return sorted(set(picked))


def pick_novelty(cands, nov):
    # The eight most novel boundaries wherever they fall, in time order.
    return sorted(top_by_novelty(cands, nov, N_CUES))


def parse_algo(algo):
    bars = 16 if algo.endswith("16") else 8
    kind = "novelty" if algo.startswith("novelty") else "mix"
    return kind, bars


def pick(mfcc, frame_hz, beats_ms, downbeats_ms, algo):
    if len(beats_ms) < BAR_BEATS * 16 or len(downbeats_ms) < 2:
        return []
    nov = novelty_curve(mfcc, frame_hz, beats_ms)
    db_idx = downbeat_indices(downbeats_ms, beats_ms)
    anchor_idx = db_idx[0]
    kind, phrase_bars = parse_algo(algo)
    cands = phrase_positions(db_idx, phrase_bars)
    chosen = pick_novelty(cands, nov) if kind == "novelty" else pick_mix(cands, nov)
    # The bar number comes from the chosen beat's index relative to the
    # first downbeat, so the label can never drift from the position.
    return [{"comment": "bar %d" % ((bi - anchor_idx) // BAR_BEATS), "hot_cue": slot + 1, "time_ms": int(beats_ms[bi])}
            for slot, bi in enumerate(chosen)]


# --- Identity and the command line -----------------------------------------


def canonical_hash(cfg):
    # sha256 of the canonical JSON (sorted keys, no whitespace) of the
    # identity keys of algos/cues/algo.json: {"algo": "mix16"}.
    return hashlib.sha256(json.dumps(cfg, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode("utf-8")).hexdigest()


def to_ms(seconds):
    # The host passes the grid in ms; the leg takes seconds and rounds back
    # to the same integers.
    return [int(round(float(t) * 1000)) for t in seconds]


ALGOS = ["mix8", "mix16", "novelty8", "novelty16"]


def run_one(ffmpeg, algo, beats, downbeats, features="", audio=""):
    if algo not in ALGOS:
        raise ValueError(f'unknown cue algorithm "{algo}"')
    beats_ms = to_ms(beats)
    downbeats_ms = to_ms(downbeats)
    out = {"algo": algo, "algo_version": algo, "cfg_hash": canonical_hash({"algo": algo})}
    if len(beats_ms) < BAR_BEATS * 16 or len(downbeats_ms) < 2:
        out["cues"] = []
        return out
    if features:
        data = sys.stdin.buffer.read() if features == "-" else open(features, "rb").read()
        frame_hz, mfcc = read_dc7f(data)
        out["features"] = "dc7f"
    elif audio:
        frame_hz, mfcc = mfcc_from_audio(ffmpeg, audio)
        out["features"] = "audio"
    else:
        raise ValueError("features or audio is required")
    out["cues"] = pick(mfcc, frame_hz, beats_ms, downbeats_ms, algo)
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--audio", help="the track; read only when --features is not given")
    ap.add_argument("--features", default="", help="a DC7F file from the features leg, or - for stdin; with it no audio is read")
    ap.add_argument("--beats", help="JSON list of beat times in seconds")
    ap.add_argument("--downbeats", help="JSON list of downbeat times in seconds")
    ap.add_argument("--algo", default="mix16", choices=ALGOS)
    # JSON [{"id": ..., "features": PATH or "audio": PATH, "beats": [...],
    # "downbeats": [...], "algo": ...}, ...]: one JSON line per item; a
    # failed item reports its error and the batch goes on.
    ap.add_argument("--items")
    ap.add_argument("--ffmpeg", default="ffmpeg")
    args = ap.parse_args()
    warnings.filterwarnings("ignore")

    if args.items:
        for item in json.loads(args.items):
            try:
                out = run_one(args.ffmpeg, item.get("algo", args.algo), item["beats"], item["downbeats"],
                              features=item.get("features", ""), audio=item.get("audio", ""))
            except Exception as e:  # one bad item must not sink the batch
                out = {"error": str(e)}
            out["id"] = item["id"]
            json.dump(out, sys.stdout, separators=(",", ":"))
            sys.stdout.write("\n")
            sys.stdout.flush()
        return
    if not args.features and not args.audio:
        ap.error("--features or --audio is required")
    if args.beats is None or args.downbeats is None:
        ap.error("--beats and --downbeats are required")
    out = run_one(args.ffmpeg, args.algo, json.loads(args.beats), json.loads(args.downbeats),
                  features=args.features, audio=args.audio or "")
    print(json.dumps(out, separators=(",", ":")))


if __name__ == "__main__":
    sys.exit(main())
