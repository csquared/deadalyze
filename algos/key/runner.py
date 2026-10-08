#!/usr/bin/env python3
# Key detection: madmom's key CNN (Korzeniowski & Widmer, trained on
# electronic and pop music), the model the runtime already ships. One label of
# 24 (12 major, 12 minor) with a softmax confidence; the Go side maps the
# label to Camelot regardless of confidence. Audio is decoded by
# ffmpeg, as the cue picker does, so every format ffmpeg reads works.
import argparse
import json
import subprocess
import sys
import warnings

import numpy as np

SR = 44100


def decode(ffmpeg, path):
    raw = subprocess.run(
        [ffmpeg, "-v", "error", "-i", path, "-ac", "1", "-ar", str(SR), "-f", "f32le", "-"],
        capture_output=True, check=True,
    ).stdout
    return np.frombuffer(raw, dtype=np.float32)


def detect(proc, ffmpeg, path):
    from madmom.audio.signal import Signal
    from madmom.features.key import KEY_LABELS

    samples = decode(ffmpeg, path)
    if samples.size < SR:  # under a second: nothing tonal to hear
        return {"label": "", "confidence": 0.0, "top": []}
    probs = proc(Signal(samples, sample_rate=SR, num_channels=1))[0]
    order = np.argsort(probs)[::-1]
    top = [{"label": KEY_LABELS[i], "p": round(float(probs[i]), 4)} for i in order[:3]]
    return {"label": top[0]["label"], "confidence": top[0]["p"], "top": top}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--audio")
    # JSON [{"id": ..., "audio": PATH}, ...]: the model loads once and one
    # result line streams per track; a failed track reports its error and
    # the batch goes on.
    ap.add_argument("--items")
    ap.add_argument("--ffmpeg", default="ffmpeg")
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
