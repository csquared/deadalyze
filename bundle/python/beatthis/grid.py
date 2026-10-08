#!/usr/bin/env python
# grid.py -- beatgrid from the Beat This! tracker (ISMIR 2024, CPJKU/beat_this).
#
# Runs the final0 checkpoint (beats + downbeats, no DBN), then fits deadca7's
# constant-tempo lattice the same way the beatnet pipeline does: least-squares
# period + origin over the tracked beats, BPM snapped to a clean value within
# tolerance, lattice extended back to ~0, downbeat = the opening beat (a DJ
# track's bar 1 is the top of the track). Beat This is frame-quantized at 50fps
# like BeatNet, so the same near-zero origin snap applies.
#
# Beyond the house rule, the raw model downbeats also vote on bar phase
# (bt_phase_vote: which lattice beat mod 4 the model calls "1", with agreement
# 0..1) -- reported for evaluation, not applied.
#
# Usage: grid.py --audio PATH [--device cpu|mps] [--origin-frame-snap 0.025]
# Prints one JSON line shaped like analysis.Grid plus bt_* diagnostics.
import argparse
import json
import os
import subprocess
import sys

import numpy as np
import soundfile as sf

SR = 44100

ALGO_VERSION = "beat-this-v1"
BPM_SNAP_TOLERANCE = 0.15
DYNAMIC_RESID_S = 0.05
DYNAMIC_FRACTION = 0.10
INLIER_RESID_S = 0.05


WINDOW = 64
WINDOW_STEP = 32


def lstsq_line(idx, times):
    a = np.vstack([idx, np.ones_like(idx)]).T
    sol, _, _, _ = np.linalg.lstsq(a, times, rcond=None)
    return float(sol[0]), float(sol[1])


def fit_window(win, period0):
    # Seeded from the track-wide consensus so a 50fps-quantized local IBI median
    # (raw IBIs cluster on 20ms multiples, never the true period) can't
    # mis-assign indices; insertions/deletions elsewhere land in other windows.
    period, origin = period0, float(win[0])
    for _ in range(3):
        idx = np.round((win - origin) / period)
        period, origin = lstsq_line(idx, win)
    resid = win - (origin + np.round((win - origin) / period) * period)
    return period, origin, float(np.std(resid))


def consensus_period(beats):
    # Medians over 8-beat spans: frame quantization averages down to ~2.5ms and
    # a missed beat corrupts only the spans crossing it.
    span = 8
    if len(beats) <= span:
        return float(np.median(np.diff(beats)))
    sp = (beats[span:] - beats[:-span]) / span
    sp = sp[(sp > 0.2) & (sp < 1.2)]
    return float(np.median(sp))


def fit_lattice(beats):
    consensus = consensus_period(beats)
    best = None
    fallback = None
    n = len(beats)
    w = min(WINDOW, n)
    for start in range(0, max(1, n - w + 1), WINDOW_STEP):
        period, origin, spread = fit_window(beats[start:start + w], consensus)
        drift = abs(period - consensus) / consensus
        if fallback is None or drift < fallback[3]:
            fallback = (period, origin, spread, drift)
        if drift > 0.02:
            continue
        if best is None or spread < best[2]:
            best = (period, origin, spread, drift)
    period, origin = (best or fallback)[:2]
    resid = beats - (origin + np.round((beats - origin) / period) * period)
    return period, origin, resid


def refine_origin(path, lattice, duration):
    # Same audio-anchored origin polish as the beatnet pipeline (see
    # pipeline.go refine_origin): stacked multi-band onset strength, rising
    # edge at 20% of the pulse peak. Keeping the two engines on one estimator
    # makes their origins agree except where the audio itself is ambiguous.
    from scipy.signal import butter, sosfiltfilt

    start = max(10.0, duration * 0.25)
    dur = min(90.0, duration - start - 5)
    if dur < 30 or len(lattice) < 48:
        return lattice, 0.0
    out = subprocess.run(
        ["ffmpeg", "-v", "error", "-ss", str(start), "-t", str(dur), "-i", path,
         "-ac", "1", "-ar", str(SR), "-f", "f32le", "-"], capture_output=True)
    if out.returncode:
        return lattice, 0.0
    x = np.frombuffer(out.stdout, dtype=np.float32).astype(np.float64)
    if len(x) < SR * 20:
        return lattice, 0.0
    ons = np.zeros(len(x))
    k = max(1, int(0.002 * SR))
    box = np.ones(k) / k
    for lo, hi in ((35, 110), (300, 1500), (2000, 8000)):
        sos = butter(4, [lo, hi], btype="band", fs=SR, output="sos")
        env = np.convolve(np.abs(sosfiltfilt(sos, x)), box, mode="same")
        d = np.diff(env, prepend=env[0])
        d[d < 0] = 0
        scale = np.percentile(d, 95)
        if scale > 0:
            ons += d / scale
    w = int(0.06 * SR)
    idx = [int((b - start) * SR) for b in lattice if start + 0.06 < b < start + dur - 0.06]
    if len(idx) < 24:
        return lattice, 0.0
    wins = np.array([ons[i - w:i + w] for i in idx])
    peaks = wins.max(axis=1)
    keep = wins[peaks >= 0.25 * np.percentile(peaks, 75)]
    if len(keep) < 24:
        return lattice, 0.0
    stacked = (keep / keep.max(axis=1, keepdims=True)).mean(axis=0)
    pk = int(np.argmax(stacked))
    j = pk
    while j > 0 and stacked[j] >= 0.2 * stacked[pk]:
        j -= 1
    shift = (j - w) / SR
    if not (0.0005 < abs(shift) <= 0.045):
        return lattice, 0.0
    return np.maximum(lattice + shift, 0.0), shift * 1000


def refit_origin(beats, period, origin):
    resid = beats - (origin + np.round((beats - origin) / period) * period)
    keep = np.abs(resid) < INLIER_RESID_S
    if keep.sum() >= 16:
        return origin + float(np.mean(resid[keep]))
    return origin + float(np.mean(resid))


def analyze_one(f2b, audio, no_refine, origin_frame_snap):
    beats, downbeats = f2b(audio)
    beats = np.asarray(beats, dtype=np.float64)
    downbeats = np.asarray(downbeats, dtype=np.float64)
    if len(beats) < 16:
        return {"error": f"only {len(beats)} beats tracked"}

    info = sf.info(audio)
    duration = info.frames / info.samplerate

    period, origin, resid = fit_lattice(beats)
    resid_stdev = float(np.std(resid[np.abs(resid) < INLIER_RESID_S]))
    dynamic = float((np.abs(resid) > DYNAMIC_RESID_S).mean()) > DYNAMIC_FRACTION

    bpm = 60.0 / period
    raw_bpm = bpm
    if abs(bpm - round(bpm)) <= BPM_SNAP_TOLERANCE:
        bpm = float(round(bpm))
        period = 60.0 / bpm
        origin = refit_origin(beats, period, origin)

    first_tracked_idx = round((beats[0] - origin) / period)
    origin = origin - np.floor(origin / period) * period
    if origin >= period - 0.005:
        origin = 0.0
    if 0 < origin <= origin_frame_snap:
        origin = 0.0

    lattice = np.arange(origin, duration, period)
    refine_ms = 0.0
    if not no_refine:
        lattice, refine_ms = refine_origin(audio, lattice, duration)
        origin = float(lattice[0]) if len(lattice) else origin
    grid_downbeats = lattice[::4]

    votes = np.round((downbeats - origin) / period).astype(int) % 4
    counts = np.bincount(votes, minlength=4)
    phase = int(np.argmax(counts))
    agreement = float(counts[phase] / max(1, len(votes)))

    return {
        "algoVersion": ALGO_VERSION,
        "analysisSource": "beatthis",
        "beats": [round(float(b), 4) for b in lattice],
        "bpm": round(bpm, 2),
        "bt_first_downbeat": round(float(downbeats[0]), 4) if len(downbeats) else -1,
        "bt_first_tracked_idx": int(first_tracked_idx),
        "bt_phase_agreement": round(agreement, 3),
        "bt_phase_vote": phase,
        "bt_refine_ms": round(refine_ms, 1),
        "bt_resid_stdev": round(resid_stdev, 4),
        "downbeats": [round(float(b), 4) for b in grid_downbeats],
        "duration": round(duration, 4),
        "dynamic": bool(dynamic),
        "raw_bpm": round(raw_bpm, 3),
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--audio")
    ap.add_argument("--items")  # JSON [{"id": N, "audio": PATH}, ...]: one model load, many tracks
    ap.add_argument("--device", default="cpu")
    ap.add_argument("--no-refine", action="store_true")
    ap.add_argument("--origin-frame-snap", type=float, default=0)
    args = ap.parse_args()

    from beat_this.inference import File2Beats

    # The checkpoint load is the expensive part (~seconds); batch mode pays it
    # once for the whole list instead of once per track. The app bundle ships
    # final0.ckpt beside this script so an installed DEADCA7.app never
    # downloads a model; without it (dev), the name resolves via the
    # beat_this cache as before.
    checkpoint = os.path.join(os.path.dirname(os.path.abspath(__file__)), "final0.ckpt")
    if not os.path.exists(checkpoint):
        checkpoint = "final0"
    f2b = File2Beats(checkpoint_path=checkpoint, device=args.device, dbn=False)

    if args.items:
        out = []
        for item in json.loads(args.items):
            try:
                r = analyze_one(f2b, item["audio"], args.no_refine, args.origin_frame_snap)
            except Exception as e:  # one bad track must not sink the batch
                r = {"error": str(e)}
            r["id"] = int(item["id"])
            out.append(r)
        print(json.dumps(out))
        return

    if not args.audio:
        ap.error("--audio or --items is required")
    print(json.dumps(analyze_one(f2b, args.audio, args.no_refine, args.origin_frame_snap)))


if __name__ == "__main__":
    sys.exit(main())
