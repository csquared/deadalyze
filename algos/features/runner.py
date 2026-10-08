#!/usr/bin/env python3
"""DC7F recipe 1: fixed-clock band RMS and pooled MFCCs, independent of beats.

One track (--audio) writes the raw DC7F bytes to stdout. A batch (--items)
writes one JSON line per item with the bytes base64 in data.b64, under the
leg identity (algos/README.md): algo_version dc7f-1, no identity keys, so
cfg_hash is the sha256 of "{}".
"""
import argparse
import base64
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
FRAME_HZ = 20
HOP = 512
N_MFCC = 20
DB_FLOOR = -120.0
ALGO_VERSION = "dc7f-1"
# sha256 of the canonical JSON of no identity keys: "{}".
CFG_HASH = hashlib.sha256(b"{}").hexdigest()


def rekordbox_timeline(path):
    # ffmpeg input flags that decode on rekordbox's timeline, where the grid's
    # beats are: an MP3 keeps LAME's encoder delay (Go package timeline).
    return ["-flags2", "+skip_manual"] if path.lower().endswith(".mp3") else []


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
    # Hash precisely the bytes decoded, even if the source changes mid-run.
    # A seekable snapshot also preserves decoder padding/delay handling and
    # supports containers whose metadata is at the end of the file.
    sha = hashlib.sha256()
    with tempfile.TemporaryDirectory(prefix="dc-features-") as temp:
        snapshot = os.path.join(temp, "audio" + os.path.splitext(path)[1])
        with open(path, "rb") as source, open(snapshot, "wb") as target:
            while block := source.read(1024 * 1024):
                sha.update(block)
                target.write(block)
        samples = decode_audio(snapshot, ffmpeg, flags=rekordbox_timeline(snapshot)).astype(np.float64)
    if not samples.size:
        raise ValueError("audio decoded no samples")
    if not np.isfinite(samples).all():
        raise ValueError("nonfinite decoded audio")
    return samples, sha.digest()


def filtered(samples, cutoff, high=False):
    # Same second-order Butterworth biquads and band edges as waveform.go.
    from scipy.signal import lfilter

    w = 2 * math.pi * cutoff / SR
    alpha = math.sin(w) / (2 * math.sqrt(0.5))
    c = math.cos(w)
    if high:
        b = [(1 + c) / 2, -(1 + c), (1 + c) / 2]
    else:
        b = [(1 - c) / 2, 1 - c, (1 - c) / 2]
    return lfilter(b, [1 + alpha, -2 * c, 1 - alpha], samples)


def band_rms(samples):
    step = SR // FRAME_HZ
    starts = np.arange(0, samples.size, step)
    counts = np.minimum(step, samples.size - starts)
    columns = []
    for cuts in [((300, False),), ((250, True), (1200, False)),
                 ((3000, True), (9000, False))]:
        values = samples
        for cutoff, high in cuts:
            values = filtered(values, cutoff, high)
        columns.append(np.sqrt(np.add.reduceat(values * values, starts) / counts))
    rms = np.asarray(columns)
    # One reference across all bands retains relative bass/mid/high strength.
    peak = rms.max()
    if peak <= 0:
        return np.full_like(rms, DB_FLOOR)
    return 20 * np.log10(np.maximum(rms / peak, 10 ** (DB_FLOOR / 20)))


def pooled_mfcc(samples, frames):
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


def extract(samples, digest):
    bands = band_rms(samples)
    frames = bands.shape[1]
    mfcc = pooled_mfcc(samples, frames)
    values = np.concatenate((bands, mfcc), axis=0).astype("<f2")
    if not np.isfinite(values).all():
        raise ValueError("features exceed finite float16 range")
    duration_ms = max(1, int(math.floor(samples.size * 1000 / SR + 0.5)))
    header = struct.pack("<4sHHIBBHI32s12x", b"DC7F", 1, FRAME_HZ, frames,
                         3, N_MFCC, 0, duration_ms, digest)
    # Column-major n_frames x channels: all low frames, then mid, high,
    # then all MFCC 0 frames, MFCC 1 frames, and so on.
    return header + values.tobytes(order="C")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--audio")
    # JSON [{"id": ..., "audio": PATH}, ...]: one JSON line per item, the
    # DC7F bytes base64 under data.b64; a failed track reports its error
    # and the batch goes on.
    ap.add_argument("--items")
    ap.add_argument("--ffmpeg", default="ffmpeg")
    args = ap.parse_args()
    warnings.filterwarnings("ignore")
    if args.items:
        for item in json.loads(args.items):
            try:
                samples, digest = decode(args.ffmpeg, item["audio"])
                out = {
                    "format": "dc7f",
                    "algo_version": ALGO_VERSION,
                    "cfg_hash": CFG_HASH,
                    "data": {"b64": base64.b64encode(extract(samples, digest)).decode("ascii")},
                }
            except Exception as e:  # one bad track must not sink the batch
                out = {"error": str(e)}
            out["id"] = item["id"]
            json.dump(out, sys.stdout, separators=(",", ":"))
            sys.stdout.write("\n")
            sys.stdout.flush()
        return
    if not args.audio:
        ap.error("--audio or --items is required")
    samples, digest = decode(args.ffmpeg, args.audio)
    sys.stdout.buffer.write(extract(samples, digest))


if __name__ == "__main__":
    main()
