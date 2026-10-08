#!/usr/bin/env python3
# Beat This! (ISMIR 2024, CPJKU/beat_this) fitted to a constant-tempo
# lattice, the cross-check leg of the deadca7 engine. Ported from DEADCA7's
# analysis/beatthis/grid.py: the final0 checkpoint (beats + downbeats, no
# DBN), tempo consensus over 8-beat spans, the cleanest 64-beat window,
# integer BPM snap within tolerance, the lattice extended back to ~0 with the
# opening beat as bar 1, and the audio-anchored origin refiner. The raw model
# downbeats also vote on bar phase (bt_phase_vote, agreement 0..1): reported
# for arbitration, never applied here. v2 is the same algorithm with the
# engine's identity contract (algos/README.md).
import argparse
import hashlib
import json
import os
import subprocess
import sys

import numpy as np
import soundfile as sf

SR = 44100

ALGO_VERSION = "beat-this-v2"
BPM_SNAP_TOLERANCE = 0.15
DYNAMIC_RESID_S = 0.05
DYNAMIC_FRACTION = 0.10
INLIER_RESID_S = 0.05

WINDOW = 64
WINDOW_STEP = 32


def ms(seconds):
    return int(round(float(seconds) * 1000))


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


def refine_origin(ffmpeg, path, lattice, duration):
    # Audio-anchored origin polish, the same estimator as the BeatNet leg:
    # stacked multi-band onset strength around every strong beat, rising edge
    # at 20% of the pulse peak, polish capped at 45ms. Keeping the two legs on
    # one estimator makes their origins agree except where the audio itself
    # is ambiguous.
    from scipy.signal import butter, sosfiltfilt

    start = max(10.0, duration * 0.25)
    dur = min(90.0, duration - start - 5)
    if dur < 30 or len(lattice) < 48:
        return lattice, 0.0
    out = subprocess.run(
        [ffmpeg, "-v", "error", "-ss", str(start), "-t", str(dur), "-i", path,
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


def model_name(model):
    # The identity names the checkpoint, never its path: a bundled
    # beatthis/final0.ckpt and the library's own "final0" are one model.
    base = os.path.basename(model)
    return base[:-5] if base.endswith(".ckpt") else base


def identity_cfg(args):
    # The identity keys of algos/beat_this/algo.json, and nothing else:
    # device is provenance, not identity, and paths never enter.
    return {
        "beats_per_bar": int(args.beats_per_bar),
        "model": model_name(args.model),
        "origin_frame_snap": float(args.origin_frame_snap),
        "origin_refine": not args.no_refine,
    }


def canonical_json(cfg):
    # The canonical form every implementation of this leg must reproduce
    # byte for byte (docs/engine-protocol.md, Identity rules): keys sorted,
    # no whitespace, ints as ints (4), floats as their shortest round-trip
    # repr (0.0), bools as true/false, strings JSON-escaped. Python's json
    # gives exactly this; a native port must match these bytes.
    return json.dumps(cfg, sort_keys=True, separators=(",", ":"), ensure_ascii=False, allow_nan=False)


def canonical_hash(cfg):
    return hashlib.sha256(canonical_json(cfg).encode("utf-8")).hexdigest()


def resolve_device(device):
    # "auto" is the Apple GPU when torch has it, else the CPU; the grid is
    # the same either way and the choice is reported, not hashed.
    if device != "auto":
        return device
    try:
        import torch
        return "mps" if torch.backends.mps.is_available() else "cpu"
    except Exception:
        return "cpu"


def analyze(f2b, args):
    cfg = identity_cfg(args)
    cfg_hash = canonical_hash(cfg)
    beats, downbeats = f2b(args.audio)
    beats = np.asarray(beats, dtype=np.float64)
    downbeats = np.asarray(downbeats, dtype=np.float64)
    if len(beats) < 16:
        raise RuntimeError(f"beat_this tracked only {len(beats)} beats")

    info = sf.info(args.audio)
    duration = info.frames / info.samplerate
    bar = args.beats_per_bar

    period, origin, resid = fit_lattice(beats)
    resid_stdev = float(np.std(resid[np.abs(resid) < INLIER_RESID_S]))
    dynamic = float((np.abs(resid) > DYNAMIC_RESID_S).mean()) > DYNAMIC_FRACTION

    bpm = 60.0 / period
    raw_bpm = bpm
    if abs(bpm - round(bpm)) <= BPM_SNAP_TOLERANCE:
        bpm = float(round(bpm))
        period = 60.0 / bpm
        origin = refit_origin(beats, period, origin)

    origin = origin - np.floor(origin / period) * period
    if origin >= period - 0.005:
        origin = 0.0
    if 0 < origin <= args.origin_frame_snap:
        origin = 0.0

    lattice = np.arange(origin, duration, period)
    refine_ms = 0.0
    if not args.no_refine:
        lattice, refine_ms = refine_origin(args.ffmpeg, args.audio, lattice, duration)
        origin = float(lattice[0]) if len(lattice) else origin
    grid_downbeats = lattice[::bar]

    votes = np.round((downbeats - origin) / period).astype(int) % bar
    counts = np.bincount(votes, minlength=bar)
    phase = int(np.argmax(counts))
    agreement = float(counts[phase] / max(1, len(votes)))

    return {
        "provider": "beat_this",
        "audio_path": args.audio,
        "algo_version": ALGO_VERSION,
        "cfg_hash": cfg_hash,
        "identity": {"algo_version": ALGO_VERSION, "cfg_hash": cfg_hash, "cfg": cfg},
        "config": dict(cfg, device=args.device, model_path=args.model),
        "grid": {
            "bpm": round(bpm, 3),
            "first_beat_ms": ms(lattice[0]) if len(lattice) else None,
            "first_downbeat_ms": ms(grid_downbeats[0]) if len(grid_downbeats) else None,
            "beats_per_bar": bar,
            "source": "lattice",
        },
        "beats": [{"index": i, "time_ms": ms(t), "beat_number": i % bar + 1} for i, t in enumerate(lattice)],
        "downbeats": [{"index": i, "time_ms": ms(t), "beat_number": 1} for i, t in enumerate(grid_downbeats)],
        "duration": round(duration, 4),
        "dynamic": bool(dynamic),
        "origin_refine_ms": round(refine_ms, 1),
        "raw_bpm": round(raw_bpm, 3),
        "raw_beats": int(len(beats)),
        "bt_first_downbeat_ms": ms(downbeats[0]) if len(downbeats) else None,
        "bt_phase_vote": phase,
        "bt_phase_agreement": round(agreement, 3),
        "bt_resid_stdev": round(resid_stdev, 4),
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--audio")
    # JSON [{"id": ..., "audio": PATH}, ...]: the checkpoint load is the
    # expensive part, so a batch pays it once and streams one result line
    # per track as it finishes; a failed track reports its error and the
    # batch goes on.
    ap.add_argument("--items")
    ap.add_argument("--model", default="final0")
    ap.add_argument("--device", default="cpu", help="cpu, mps, cuda:N, or auto (mps when available, else cpu)")
    ap.add_argument("--beats-per-bar", type=int, default=4)
    ap.add_argument("--no-refine", action="store_true")
    ap.add_argument("--origin-frame-snap", type=float, default=0)
    ap.add_argument("--ffmpeg", default="ffmpeg")
    args = ap.parse_args()
    args.device = resolve_device(args.device)

    from beat_this.inference import File2Beats

    f2b = File2Beats(checkpoint_path=args.model, device=args.device, dbn=False)
    if args.items:
        for item in json.loads(args.items):
            args.audio = item["audio"]
            try:
                out = analyze(f2b, args)
            except Exception as e:  # one bad track must not sink the batch
                out = {"error": str(e)}
            out["id"] = item["id"]
            json.dump(out, sys.stdout, separators=(",", ":"))
            sys.stdout.write("\n")
            sys.stdout.flush()
        return
    if not args.audio:
        ap.error("--audio or --items is required")
    json.dump(analyze(f2b, args), sys.stdout, separators=(",", ":"))
    sys.stdout.write("\n")


if __name__ == "__main__":
    sys.exit(main())
