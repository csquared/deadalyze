#!/usr/bin/env python3
# Key detection: madmom's key CNN (Korzeniowski & Widmer, trained on
# electronic and pop music), the model the runtime already ships. One label of
# 24 (12 major, 12 minor) with a softmax confidence; the Go side maps the
# label to Camelot regardless of confidence. Audio is decoded by
# ffmpeg, as the cue picker does, so every format ffmpeg reads works.
# Identity (algos/README.md): algo_version names the model, and the key set
# is empty, so cfg_hash is the sha256 of "{}" and never moves.
import argparse
import hashlib
import json
import subprocess
import sys
import warnings

import numpy as np

SR = 44100
ALGO_VERSION = "madmom-key-cnn-2018"
# sha256 of the canonical JSON of no identity keys: "{}".
CFG_HASH = hashlib.sha256(b"{}").hexdigest()


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


def detect(proc, ffmpeg, path):
    from madmom.audio.signal import Signal
    from madmom.features.key import KEY_LABELS

    samples = decode(ffmpeg, path)
    out = {"algo_version": ALGO_VERSION, "cfg_hash": CFG_HASH}
    if samples.size < SR:  # under a second: nothing tonal to hear
        out.update(label="", confidence=0.0, top=[])
        return out
    probs = proc(Signal(samples, sample_rate=SR, num_channels=1))[0]
    order = np.argsort(probs)[::-1]
    top = [{"label": KEY_LABELS[i], "p": round(float(probs[i]), 4)} for i in order[:3]]
    out.update(label=top[0]["label"], confidence=top[0]["p"], top=top)
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--audio")
    # JSON [{"id": ..., "audio": PATH}, ...]: the model loads once and one
    # result line streams per track; a failed track reports its error and
    # the batch goes on.
    ap.add_argument("--items")
    ap.add_argument("--ffmpeg", default="ffmpeg")
    # The engine passes every leg its device setting. madmom's key CNN is
    # numpy only, so it is accepted and ignored.
    ap.add_argument("--device", default="cpu", help="accepted for the leg contract; the key CNN runs on the CPU")
    args = ap.parse_args()
    warnings.filterwarnings("ignore")

    from madmom.features.key import CNNKeyRecognitionProcessor

    proc = CNNKeyRecognitionProcessor()
    if args.items:
        for item in json.loads(args.items):
            try:
                out = detect(proc, args.ffmpeg, item["audio"])
            except Exception as e:  # one bad track must not sink the batch
                out = {"error": str(e)}
            out["id"] = item["id"]
            json.dump(out, sys.stdout, separators=(",", ":"))
            sys.stdout.write("\n")
            sys.stdout.flush()
        return
    if not args.audio:
        ap.error("--audio or --items is required")
    json.dump(detect(proc, args.ffmpeg, args.audio), sys.stdout, separators=(",", ":"))
    sys.stdout.write("\n")


if __name__ == "__main__":
    sys.exit(main())
