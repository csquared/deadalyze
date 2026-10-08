# embed.py computes LAION-CLAP embeddings: audio tracks into 512-d vectors
# (--items, one model load for the whole batch) and tag names through the
# text tower (--texts). One JSON array on stdout; per-item errors stay per
# item so one unreadable file never sinks a batch.
#
# Determinism matters (embeddings are content-addressed by audio hash): the
# model runs in eval mode and audio windows are FIXED offsets, never random
# crops. A track longer than one 10s window is embedded at three fixed
# positions and averaged.
import argparse
import json
import os
import sys

import numpy as np

WINDOW_SEC = 10.0
WINDOW_FRACTIONS = (0.2, 0.5, 0.8)
SAMPLE_RATE = 48000


def find_checkpoint(models_dir):
    name = "music_audioset_epoch_15_esc_90.14.pt"
    override = os.environ.get("EMBED_CHECKPOINT")
    candidates = [override] if override else []
    if models_dir:
        candidates.append(os.path.join(models_dir, name))
    candidates.append(os.path.join(os.path.dirname(os.path.abspath(__file__)), "models", name))
    for c in candidates:
        if c and os.path.exists(c):
            return c
    raise FileNotFoundError(
        f"CLAP checkpoint {name} not found -- set EMBED_CHECKPOINT or place it under {os.path.dirname(candidates[-1])}"
    )


def load_model(models_dir):
    import laion_clap

    model = laion_clap.CLAP_Module(enable_fusion=False, amodel="HTSAT-base")
    model.load_ckpt(find_checkpoint(models_dir))
    model.eval()
    return model


def quantize(audio):
    # int16 round-trip for parity with CLAP's training data pipeline.
    clipped = np.clip(audio, -1.0, 1.0)
    return (clipped * 32767.0).astype(np.int16).astype(np.float32) / 32767.0


def embed_audio(model, path):
    import librosa

    duration = librosa.get_duration(path=path)
    if duration <= 0:
        raise ValueError("zero-length audio")
    slack = max(0.0, duration - WINDOW_SEC)
    offsets = sorted({round(f * slack, 3) for f in WINDOW_FRACTIONS}) if slack > 0 else [0.0]

    vecs = []
    for off in offsets:
        audio, _ = librosa.load(path, sr=SAMPLE_RATE, mono=True, offset=off, duration=WINDOW_SEC)
        if audio.size == 0:
            continue
        window = quantize(audio).reshape(1, -1)
        vec = model.get_audio_embedding_from_data(x=window, use_tensor=False)[0]
        vecs.append(np.asarray(vec, dtype=np.float64))
    if not vecs:
        raise ValueError("no audio decoded")
    return np.mean(vecs, axis=0)


def rounded(vec):
    return [round(float(x), 6) for x in vec]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--items")  # JSON [{"id": N, "audio": PATH}, ...]
    ap.add_argument("--texts")  # JSON ["desert", "organic", ...]
    ap.add_argument("--models-dir")
    args = ap.parse_args()
    if not args.items and not args.texts:
        ap.error("--items or --texts is required")

    model = load_model(args.models_dir)
    out = []

    if args.texts:
        texts = json.loads(args.texts)
        vecs = model.get_text_embedding(texts, use_tensor=False)
        out = [{"embedding": rounded(v), "text": t} for t, v in zip(texts, vecs)]
        print(json.dumps(out))
        return

    for item in json.loads(args.items):
        try:
            r = {"embedding": rounded(embed_audio(model, item["audio"]))}
        except Exception as e:  # one bad track must not sink the batch
            r = {"error": str(e)}
        r["id"] = int(item["id"])
        out.append(r)
    print(json.dumps(out))


if __name__ == "__main__":
    sys.exit(main())
