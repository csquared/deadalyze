#!/usr/bin/env python3
"""DC7F recipe 1: fixed-clock band RMS and pooled MFCCs, independent of beats."""
import argparse
import hashlib
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


def rekordbox_timeline(path):
    # ffmpeg input flags that decode on rekordbox's timeline, where the grid's
    # beats are: an MP3 keeps LAME's encoder delay (Go package timeline).
    return ["-flags2", "+skip_manual"] if path.lower().endswith(".mp3") else []


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
    ap.add_argument("--audio", required=True)
    ap.add_argument("--ffmpeg", default="ffmpeg")
    args = ap.parse_args()
    warnings.filterwarnings("ignore")
    samples, digest = decode(args.ffmpeg, args.audio)
    sys.stdout.buffer.write(extract(samples, digest))


if __name__ == "__main__":
    main()
