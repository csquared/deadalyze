#!/usr/bin/env python
# separate.py -- stem separation via audio-separator (the headless UVR engine).
#
# Splits one audio file into stems with a UVR model zoo checkpoint (default:
# BS-Roformer vocals/instrumental, the community-standard acapella model).
# Model files download on first use into --models-dir and are cached there.
#
# Usage: separate.py --audio PATH --out DIR [--model CKPT] [--models-dir DIR]
# Prints one JSON line: {"model": ..., "sec": ..., "stems": {"vocals": PATH, ...}}
# or {"error": ...} on failure. Progress/log noise goes to stderr.
import argparse
import json
import logging
import re
import sys
import time
from pathlib import Path

DEFAULT_MODEL = "model_bs_roformer_ep_317_sdr_12.9755.ckpt"
DEFAULT_MODELS_DIR = Path.home() / ".cache" / "deadca7" / "stem-models"

STEM_NAME_RE = re.compile(r"_\(([^)]+)\)_")


def stem_key(filename):
    m = STEM_NAME_RE.search(Path(filename).name)
    if m:
        return m.group(1).lower()
    return Path(filename).stem.lower()


def separate(args):
    from audio_separator.separator import Separator

    print(f"loading model {args.model}", file=sys.stderr)
    separator = Separator(
        log_level=logging.WARNING,
        model_file_dir=args.models_dir,
        output_dir=args.out,
        output_format="WAV",
    )
    separator.load_model(model_filename=args.model)
    print(f"separating {args.audio}", file=sys.stderr)
    start = time.time()
    files = separator.separate(args.audio)
    out = Path(args.out)
    stems = {}
    for f in files:
        p = Path(f)
        if not p.is_absolute():
            p = out / p
        stems[stem_key(f)] = str(p)
    return {
        "model": args.model,
        "sec": round(time.time() - start, 1),
        "stems": stems,
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--audio", required=True)
    ap.add_argument("--model", default=DEFAULT_MODEL)
    ap.add_argument("--models-dir", default=str(DEFAULT_MODELS_DIR))
    ap.add_argument("--out", required=True)
    args = ap.parse_args()
    Path(args.models_dir).mkdir(parents=True, exist_ok=True)
    Path(args.out).mkdir(parents=True, exist_ok=True)
    try:
        result = separate(args)
    except Exception as e:
        result = {"error": str(e)}
    print(json.dumps(result))


if __name__ == "__main__":
    main()
