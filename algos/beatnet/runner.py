#!/usr/bin/env python3
# BeatNet (CRNN + madmom DBN) fitted to a constant-tempo lattice: the primary
# leg of the deadca7 engine, ported from DEADCA7's analysis pipeline
# (beatnet-dbn-v14; v15 is the same algorithm with the engine's identity
# contract, algos/README.md). Raw beats are never the grid. The cleanest 64-beat
# window (scored by span over p95 error) anchors the tempo; the strongest
# downbeat candidate's local fit gives the BPM, snapped to an integer within
# tolerance; a constant lattice is laid from BeatNet's first beat back to the
# top of the track, and that opening beat is bar 1 ("a DJ track's bar 1 is the
# start of the track"). The audio-anchored origin refiner then polishes the
# lattice by up to 45ms. The grid identity is (algo version, cfg hash).
import argparse
import hashlib
import json
import subprocess
import sys
import types

import numpy as np

SR = 44100
ALGO_VERSION = "beatnet-dbn-v15"

ANCHOR_WINDOW_BEATS = 64
ANCHOR_STEP_BEATS = 8
POST_CANDIDATES = 4
POST_CANDIDATE_MIN_SEPARATION = 0.05
# A tempo is written as the simplest one (whole, then one decimal, then two)
# whose grid stays within this of the track's own beats from start to end;
# otherwise as measured. See choose_bpm.
BPM_DRIFT_TOLERANCE = 0.005


def ms(seconds):
    return int(round(float(seconds) * 1000))


def load_beatnet(model, device, beats_per_bar):
    try:
        import pyaudio  # noqa: F401
    except ImportError:
        sys.modules["pyaudio"] = types.SimpleNamespace(paFloat32=0, PyAudio=lambda: None)
    from BeatNet.BeatNet import BeatNet
    from madmom.features.downbeats import DBNDownBeatTrackingProcessor

    net = BeatNet(model, mode="offline", inference_model="DBN", plot=[], thread=False, device=device)
    net.estimator = DBNDownBeatTrackingProcessor(beats_per_bar=[beats_per_bar], fps=50)
    return net


def resample(mono, rate, sr):
    # The fallback decoder's resampler: librosa when it is importable, else
    # scipy's polyphase, else linear interpolation. ffmpeg's own resampler is
    # the common path; this only runs when ffmpeg refused the file.
    try:
        import librosa
        return librosa.resample(mono, orig_sr=rate, target_sr=sr)
    except ImportError:
        pass
    try:
        from math import gcd
        from scipy.signal import resample_poly
        g = gcd(int(sr), int(rate))
        return resample_poly(mono, int(sr) // g, int(rate) // g)
    except ImportError:
        pass
    n = int(round(mono.size * sr / rate))
    return np.interp(np.arange(n) * (rate / sr), np.arange(mono.size), mono)


def decode_audio(path, ffmpeg, sr=SR, flags=(), start=0.0, duration=None):
    # Mono float32 at sr. ffmpeg first, as the leg contract says, so every
    # format it reads works; when ffmpeg refuses the file (a WAV with a
    # malformed trailing LIST chunk: "too short LIST tag") libsndfile reads
    # it, mixed to mono and resampled to sr. How the samples were decoded is
    # not identity (docs/engine-protocol.md): the answer is the same either
    # way. flags are ffmpeg input options; start and duration (seconds)
    # select a stretch of the track for both decoders. The error when both
    # refuse names both attempts.
    cmd = [ffmpeg, "-nostdin", "-v", "error", *flags]
    if start:
        cmd += ["-ss", str(start)]
    if duration is not None:
        cmd += ["-t", str(duration)]
    cmd += ["-i", path, "-ac", "1", "-ar", str(sr), "-f", "f32le", "-"]
    out = subprocess.run(cmd, capture_output=True)
    if out.returncode == 0 and out.stdout:
        return np.frombuffer(out.stdout, dtype="<f4")
    why = out.stderr.decode(errors="replace").strip()[:400] or f"exit {out.returncode}, no samples"
    try:
        import soundfile as sf
        with sf.SoundFile(path) as f:
            rate = int(f.samplerate)
            if start:
                f.seek(int(round(start * rate)))
            frames = -1 if duration is None else int(round(duration * rate))
            data = f.read(frames, dtype="float32", always_2d=True)
    except Exception as e:
        raise RuntimeError(f"ffmpeg could not decode {path}: {why}; soundfile could not either: {e}") from None
    # ffmpeg's downmix: equal gains, L2-normalised (stereo is (L+R)/sqrt 2),
    # so the fallback is at ffmpeg's level and the key CNN hears the same.
    mono = data[:, 0] if data.shape[1] == 1 else data.sum(axis=1) / np.sqrt(data.shape[1])
    if rate != sr:
        mono = resample(mono, rate, sr)
    return np.ascontiguousarray(mono, dtype=np.float32)


def decode(ffmpeg, path):
    return decode_audio(path, ffmpeg)


def beatnet_bpm(beats):
    b = np.asarray(beats, dtype=np.float64)
    b = b[np.isfinite(b)]
    if b.size < 4:
        return 0.0
    intervals = np.diff(b)
    intervals = intervals[(intervals > 0.2) & (intervals < 2.0)]
    if intervals.size == 0:
        return 0.0
    med = float(np.median(intervals))
    return 60.0 / med if med > 0 else 0.0


def linear_window(beats):
    y = np.asarray(beats, dtype=np.float64)
    x = np.arange(len(y), dtype=np.float64)
    xm = float(x.mean())
    ym = float(y.mean())
    den = float(np.sum((x - xm) ** 2))
    if den <= 0:
        return None
    period = float(np.sum((x - xm) * (y - ym)) / den)
    if period <= 0:
        return None
    phase = ym - period * xm
    fit = phase + period * x
    err = np.abs(y - fit)
    return {
        "bpm": 60.0 / period,
        "duration": float(y[-1] - y[0]),
        "end": float(y[-1]),
        "median_abs_error": float(np.median(err)),
        "p95_abs_error": float(np.percentile(err, 95)),
        "period": period,
        "phase": phase,
        "start": float(y[0]),
    }


def anchor(beats, min_bpm, max_bpm):
    beats = np.asarray(beats, dtype=np.float64)
    beats = beats[np.isfinite(beats)]
    beats = beats[beats >= 0]
    if beats.size < max(8, ANCHOR_WINDOW_BEATS // 2):
        return None
    sizes = [ANCHOR_WINDOW_BEATS]
    if beats.size < ANCHOR_WINDOW_BEATS:
        sizes = [int(beats.size)]
    elif beats.size >= ANCHOR_WINDOW_BEATS * 2:
        sizes.append(ANCHOR_WINDOW_BEATS * 2)
    candidates = []
    for size in sizes:
        if size < 8:
            continue
        for start in range(0, int(beats.size) - size + 1, ANCHOR_STEP_BEATS):
            fit = linear_window(beats[start:start + size])
            if not fit:
                continue
            if fit["bpm"] < min_bpm or fit["bpm"] > max_bpm:
                continue
            fit["beat_count"] = int(size)
            fit["score"] = fit["duration"] / ((fit["p95_abs_error"] + 0.005) ** 1.5)
            fit["start_index"] = int(start)
            candidates.append(fit)
    if not candidates:
        return None
    return max(candidates, key=lambda c: c["score"])


def circular_residual(times, phase, period):
    return np.abs(np.mod(times - phase + period / 2, period) - period / 2)


def phase_distance(a, b, period):
    return float(abs(np.mod(a - b + period / 2, period) - period / 2))


def candidate_phases(times, period, limit=None):
    times = np.asarray(times, dtype=np.float64)
    times = times[np.isfinite(times)]
    if times.size == 0:
        return []
    ranked = []
    for phase in np.unique(np.round(np.mod(times, period), 4)):
        err = np.sort(circular_residual(times, phase, period))
        keep = max(1, int(round(len(err) * 0.95)))
        ranked.append({"phase_mod": float(phase), "score": float(np.mean(err[:keep])), "p95": float(np.percentile(err, 95))})
    ranked.sort(key=lambda c: (c["score"], c["p95"], c["phase_mod"]))
    if limit is not None:
        selected = []
        min_sep = min(POST_CANDIDATE_MIN_SEPARATION, period / 4)
        for candidate in ranked:
            if all(phase_distance(candidate["phase_mod"], prev["phase_mod"], period) >= min_sep for prev in selected):
                selected.append(candidate)
                if len(selected) >= limit:
                    break
        ranked = selected or ranked[:limit]
    return ranked


def fallback_anchor(beats, min_bpm, max_bpm):
    beats = np.asarray(beats, dtype=np.float64)
    beats = beats[np.isfinite(beats)]
    beats = beats[beats >= 0]
    if beats.size < 4:
        return None
    diffs = np.diff(beats)
    diffs = diffs[np.isfinite(diffs) & (diffs > 0.2) & (diffs < 2.0)]
    if diffs.size == 0:
        return None
    period = float(np.median(diffs))
    bpm = 60.0 / period if period > 0 else 0.0
    while bpm > 0 and bpm < min_bpm:
        bpm *= 2.0
        period /= 2.0
    while bpm > max_bpm:
        bpm /= 2.0
        period *= 2.0
    if bpm < min_bpm or bpm > max_bpm or period <= 0:
        return None
    phases = candidate_phases(beats, period, limit=1)
    phase_mod = float(phases[0]["phase_mod"]) if phases else float(np.mod(beats[0], period))
    residual = circular_residual(beats, phase_mod, period)
    p95 = float(np.percentile(residual, 95)) if residual.size else 0.0
    return {
        "beat_count": int(beats.size),
        "bpm": bpm,
        "duration": float(beats[-1] - beats[0]),
        "end": float(beats[-1]),
        "median_abs_error": float(np.median(residual)) if residual.size else 0.0,
        "p95_abs_error": p95,
        "period": period,
        "phase": phase_mod,
        "score": float(beats.size) / ((p95 + 0.005) ** 1.5) if residual.size else float(beats.size),
        "source": "global_median_fallback",
        "start": float(beats[0]),
        "start_index": 0,
    }


def activation_strength(activations, time):
    a = np.asarray(activations, dtype=np.float64)
    if a.ndim != 2 or a.shape[0] == 0:
        return 0.0
    i = int(round(float(time) * 50.0))
    i = max(0, min(a.shape[0] - 1, i))
    channel = 1 if a.shape[1] > 1 else 0
    return float(a[i, channel])


def audio_strength(samples, time, norm):
    s = np.asarray(samples, dtype=np.float64)
    if s.size == 0 or norm <= 0:
        return 0.0
    start = max(0, int(round((float(time) - 0.05) * SR)))
    end = min(s.size, int(round((float(time) + 0.30) * SR)))
    if end <= start:
        return 0.0
    seg = s[start:end]
    return min(1.0, float(np.sqrt(np.mean(seg * seg))) / norm)


def local_fit(raw_beats, center_index, min_bpm, max_bpm):
    raw_beats = np.asarray(raw_beats, dtype=np.float64)
    size = min(int(raw_beats.size), ANCHOR_WINDOW_BEATS)
    if size < 8:
        return None
    start = max(0, min(int(center_index) - size // 2, int(raw_beats.size) - size))
    fit = linear_window(raw_beats[start:start + size])
    if not fit:
        return None
    if fit["bpm"] < min_bpm or fit["bpm"] > max_bpm:
        return None
    fit["beat_count"] = int(size)
    fit["start_index"] = int(start)
    fit["score"] = fit["duration"] / ((fit["p95_abs_error"] + 0.005) ** 1.5)
    return fit


def post_candidates(raw_beats, labels, activations, samples, min_bpm, max_bpm):
    # The strongest downbeat candidates, each with the local tempo fit around
    # it; the best one's tempo becomes the grid's BPM.
    raw_beats = np.asarray(raw_beats, dtype=np.float64)
    labels = np.asarray(labels)
    samples = np.asarray(samples, dtype=np.float64)
    energy_norm = float(np.percentile(np.abs(samples), 99.5)) if samples.size else 0.0
    indexes = np.flatnonzero(labels == 1) if labels.size == raw_beats.size else np.arange(raw_beats.size)
    candidates = []
    for idx in indexes:
        t = float(raw_beats[int(idx)])
        fit = local_fit(raw_beats, int(idx), min_bpm, max_bpm)
        if not fit:
            continue
        activation = activation_strength(activations, t)
        energy = audio_strength(samples, t, energy_norm)
        strength = (0.65 * activation) + (0.35 * energy)
        candidates.append({
            "time": t,
            "beat_index": int(idx),
            "activation": activation,
            "energy": energy,
            "strength": strength,
            "bpm": fit["bpm"],
            "period": fit["period"],
            "fit_score": fit["score"],
            "score": strength * fit["score"],
            "p95": fit["p95_abs_error"],
        })
    candidates.sort(key=lambda c: (-c["score"], c["p95"], c["time"]))
    return candidates[:POST_CANDIDATES]


def free_period(raw_beats, period):
    # The track's own tempo: BeatNet's beats, each put on the lattice the
    # local fit implies (a missed or doubled beat still lands on a lattice
    # point), and a straight line through them by least squares. Its error
    # shrinks with the track: about a millisecond of drift over seven minutes
    # for BeatNet's 20 ms frames.
    beats = np.asarray(raw_beats, dtype=np.float64)
    if period <= 0 or beats.size < 8:
        return period
    for _ in range(3):
        idx = np.round((beats - beats[0]) / period)
        residual = beats - (beats[0] + idx * period)
        keep = np.abs(residual) < period / 4
        if keep.sum() < 8:
            break
        slope, _ = np.linalg.lstsq(np.vstack([idx[keep], np.ones(int(keep.sum()))]).T, beats[keep], rcond=None)[0]
        if not np.isfinite(slope) or slope <= 0:
            break
        period = float(slope)
    return period


def choose_bpm(raw_beats, local_bpm, duration, tolerance=BPM_DRIFT_TOLERANCE):
    # DJ tempos are mostly whole numbers and BeatNet's local fit lands a hair
    # off (122.99, 124.01), so a whole number is written when it costs
    # nothing. What it costs is drift: a grid 0.02 BPM off is 63 ms out by
    # the end of a seven-minute track, which no rounding is worth. So the
    # simplest tempo (whole, one decimal, two) whose grid stays within
    # tolerance of the track's own beats over its whole length, given the
    # best origin, is the one; a short track rounds where a long one cannot.
    # Returns the bpm, its period, and the decimals it kept (None: measured).
    if local_bpm <= 0:
        return local_bpm, 0.0, None
    free = free_period(raw_beats, 60.0 / local_bpm)
    span = duration / free if free > 0 else 0.0
    for digits in (0, 1, 2):
        bpm = round(60.0 / free, digits)
        if bpm > 0 and abs(60.0 / bpm - free) * span / 2 <= tolerance:
            return float(bpm), 60.0 / bpm, digits
    return 60.0 / free, free, None


def grid_from_anchor(anchor_time, period, duration, origin_frame_snap):
    # Lay a constant-period grid phased to BeatNet's beats, with its first
    # beat at the top of the track: step back from the anchor by whole
    # periods to ~0. That first beat is the downbeat.
    first = max(0.0, float(anchor_time))
    while first - period >= -0.25 * period:
        first = first - period
    first = max(0.0, first)
    # BeatNet emits its first beat one 50fps frame in (~20ms) rather than at
    # 0. Snapping that to 0 traded cross-library consistency for per-track
    # truth, so it is off by default.
    if origin_frame_snap > 0 and 0 < first <= origin_frame_snap:
        first = 0.0
    beats = []
    t = first
    while t <= duration:
        beats.append(float(t))
        t += period
    return np.asarray(beats, dtype=np.float64)


def refine_origin(samples, beats, downbeats, duration):
    # Beat-synchronous onset stack: for every fitted line, take +-60ms of
    # multi-band onset strength (kick body / punch / attack click), average
    # across all strong beats (one beat lies, two hundred averaged do not),
    # and shift the lattice so the stacked pulse's rising edge lands on the
    # lines.
    from scipy.signal import butter, sosfiltfilt

    start = max(10.0, duration * 0.25)
    dur = min(90.0, duration - start - 5)
    if dur < 30 or len(beats) < 48:
        return beats, downbeats, 0.0
    lo_i, hi_i = int(start * SR), int((start + dur) * SR)
    x = np.asarray(samples[lo_i:hi_i], dtype=np.float64)
    if len(x) < SR * 20:
        return beats, downbeats, 0.0
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
    idx = [int((b - start) * SR) for b in beats if start + 0.06 < b < start + dur - 0.06]
    if len(idx) < 24:
        return beats, downbeats, 0.0
    wins = np.array([ons[i - w:i + w] for i in idx])
    peaks = wins.max(axis=1)
    keep = wins[peaks >= 0.25 * np.percentile(peaks, 75)]
    if len(keep) < 24:
        return beats, downbeats, 0.0
    stacked = (keep / keep.max(axis=1, keepdims=True)).mean(axis=0)
    pk = int(np.argmax(stacked))
    j = pk
    while j > 0 and stacked[j] >= 0.2 * stacked[pk]:
        j -= 1
    shift = (j - w) / SR
    if not (0.0005 < abs(shift) <= 0.045):
        return beats, downbeats, 0.0
    refined = [max(0.0, float(b) + shift) for b in beats]
    refined_down = [max(0.0, float(b) + shift) for b in downbeats]
    return refined, refined_down, shift * 1000


def identity_cfg(args):
    # The identity keys of algos/beatnet/algo.json, and nothing else: device
    # is provenance, not identity (the same cfg on cpu and mps is the same
    # grid), and paths never enter.
    return {
        "anchor_max_bpm": float(args.max_bpm),
        "anchor_min_bpm": float(args.min_bpm),
        "anchor_step_beats": int(ANCHOR_STEP_BEATS),
        "anchor_window_beats": int(ANCHOR_WINDOW_BEATS),
        "beats_per_bar": int(args.beats_per_bar),
        "bpm_drift_tolerance": float(BPM_DRIFT_TOLERANCE),
        "inference_model": "DBN",
        "mode": "offline",
        "model": int(args.model),
        "origin_frame_snap": float(args.origin_frame_snap),
        "origin_refine": not args.no_refine,
        "post_candidates": int(POST_CANDIDATES),
        "strategy": "strong_downbeat_candidate_local_bpm",
    }


def canonical_json(cfg):
    # The canonical form every implementation of this leg must reproduce
    # byte for byte (docs/engine-protocol.md, Identity rules): keys sorted,
    # no whitespace, ints as ints (1), floats as their shortest round-trip
    # repr (0.0, 70.0, 0.005), bools as true/false, strings JSON-escaped.
    # Python's json gives exactly this; a native port must match these
    # bytes, not re-derive them.
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


def analyze(args, estimator=None):
    cfg = identity_cfg(args)
    cfg_hash = canonical_hash(cfg)

    if estimator is None:
        estimator = load_beatnet(args.model, args.device, args.beats_per_bar)
    if hasattr(estimator, "activation_extractor_online") and hasattr(estimator, "estimator"):
        activations = estimator.activation_extractor_online(args.audio)
        output = np.asarray(estimator.estimator(activations), dtype=np.float64)
    else:
        activations = np.empty((0, 2), dtype=np.float64)
        output = np.asarray(estimator.process(args.audio), dtype=np.float64)
    if output.ndim != 2 or output.shape[1] < 1:
        raise RuntimeError("BeatNet produced no beat times")
    raw_times = output[:, 0]
    valid = np.isfinite(raw_times) & (raw_times >= 0)
    raw_beats = np.round(raw_times[valid], 4)
    labels = output[:, 1].astype(int)[valid] if output.shape[1] >= 2 else np.zeros(raw_beats.size, dtype=int)
    if len(raw_beats) < 4:
        raise RuntimeError("BeatNet produced fewer than four beats")

    samples = decode(args.ffmpeg, args.audio)
    duration = float(samples.size) / SR
    raw_bpm = beatnet_bpm(raw_beats)
    anch = anchor(raw_beats, args.min_bpm, args.max_bpm) or fallback_anchor(raw_beats, args.min_bpm, args.max_bpm)
    if not anch:
        raise RuntimeError(f"BeatNet produced no usable anchor window (beats={len(raw_beats)}, raw_bpm={round(float(raw_bpm), 3)})")

    candidates = post_candidates(raw_beats, labels, activations, samples, args.min_bpm, args.max_bpm)
    if candidates:
        local_bpm = candidates[0]["bpm"]
        source = "strong_downbeat_candidate"
    else:
        local_bpm = anch["bpm"]
        source = "anchor_linear_fallback"
    bpm, period, bpm_digits = choose_bpm(raw_beats, local_bpm, duration)
    beats = grid_from_anchor(float(raw_beats[0]), period, duration, args.origin_frame_snap)
    if not len(beats):
        raise RuntimeError("BeatNet post strategy produced no grid")
    beats = beats.tolist()
    downbeats = [float(t) for i, t in enumerate(beats) if i % args.beats_per_bar == 0]
    refine_ms = 0.0
    if not args.no_refine:
        beats, downbeats, refine_ms = refine_origin(samples, beats, downbeats, duration)

    bar = args.beats_per_bar
    return {
        "provider": "beatnet",
        "audio_path": args.audio,
        "algo_version": ALGO_VERSION,
        "cfg_hash": cfg_hash,
        "identity": {"algo_version": ALGO_VERSION, "cfg_hash": cfg_hash, "cfg": cfg},
        "config": dict(cfg, device=args.device),
        "grid": {
            "bpm": round(float(bpm), 3),
            "first_beat_ms": ms(beats[0]),
            "first_downbeat_ms": ms(downbeats[0]) if downbeats else None,
            "beats_per_bar": bar,
            "source": source,
        },
        "beats": [{"index": i, "time_ms": ms(t), "beat_number": i % bar + 1} for i, t in enumerate(beats)],
        "downbeats": [{"index": i, "time_ms": ms(t), "beat_number": 1} for i, t in enumerate(downbeats)],
        "duration": round(duration, 4),
        "dynamic": False,
        "origin_refine_ms": round(float(refine_ms), 1),
        "raw_bpm": round(float(raw_bpm), 3),
        "bpm_digits": bpm_digits,
        "raw_beats": int(len(raw_beats)),
        "anchor": {
            "beat_count": anch["beat_count"],
            "bpm": round(float(anch["bpm"]), 4),
            "p95_abs_error": round(float(anch["p95_abs_error"]), 4),
            "score": round(float(anch["score"]), 4),
            "source": anch.get("source", "linear_window"),
            "start": round(float(anch["start"]), 4),
        },
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--audio")
    # JSON [{"id": ..., "audio": PATH}, ...]: the model load is the expensive
    # part, so a batch pays it once and streams one result line per track as
    # it finishes; a failed track reports its error and the batch goes on.
    ap.add_argument("--items")
    ap.add_argument("--min-bpm", type=float, default=70.0)
    ap.add_argument("--max-bpm", type=float, default=150.0)
    ap.add_argument("--beats-per-bar", type=int, default=4)
    ap.add_argument("--model", type=int, default=1)
    ap.add_argument("--device", default="cpu", help="cpu, mps, cuda:N, or auto (mps when available, else cpu)")
    ap.add_argument("--no-refine", action="store_true")
    ap.add_argument("--origin-frame-snap", type=float, default=0)
    ap.add_argument("--ffmpeg", default="ffmpeg")
    args = ap.parse_args()
    args.device = resolve_device(args.device)
    if args.items:
        estimator = load_beatnet(args.model, args.device, args.beats_per_bar)
        for item in json.loads(args.items):
            args.audio = item["audio"]
            try:
                out = analyze(args, estimator)
            except Exception as e:  # one bad track must not sink the batch
                out = {"error": str(e)}
            out["id"] = item["id"]
            json.dump(out, sys.stdout, separators=(",", ":"))
            sys.stdout.write("\n")
            sys.stdout.flush()
        return
    if not args.audio:
        ap.error("--audio or --items is required")
    json.dump(analyze(args), sys.stdout, separators=(",", ":"))
    sys.stdout.write("\n")


if __name__ == "__main__":
    sys.exit(main())
