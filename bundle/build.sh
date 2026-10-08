#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

GOOS="$(go env GOOS 2>/dev/null || uname | tr '[:upper:]' '[:lower:]')"
GOARCH="$(go env GOARCH 2>/dev/null || uname -m)"
case "$GOARCH" in
  x86_64) GOARCH="amd64" ;;
  aarch64) GOARCH="arm64" ;;
esac

OUT_DIR="${ML_OUT_DIR:-$ROOT/bundle/dist}"
WORK_DIR="${ML_WORK_DIR:-$ROOT/bundle/build.noindex}"
BUILD_CACHE="$WORK_DIR/cache"
VERSION="${ML_VERSION:-}"
UV="${UV:-uv}"
SIGN="${ML_SIGN:-1}"
FFMPEG_BUILD="${ML_FFMPEG_BUILD:-1783164229_N-125450-gfad2e0bc50}"
FFMPEG_LINUX_BUILD="${ML_FFMPEG_LINUX_BUILD:-master-latest}"
EMBED_MODEL="${ML_EMBED_MODEL:-music_audioset_epoch_15_esc_90.14.pt}"
STEM_MODEL="${ML_STEM_MODEL:-model_bs_roformer_ep_317_sdr_12.9755.ckpt}"

usage() {
  cat <<'EOF'
usage: bundle/build.sh [--platform GOOS/GOARCH] [--out DIR] [--version VERSION] [--no-sign]

Builds a platform-local analysis bundle (the DEADCA7 ML runtime):
  bundle/dist/deadca7-ml-<goos>-<goarch>.tar.gz

The Python runtime must be built on the target OS/arch. Cross-compiling is not
supported because Python wheels and native libraries are platform-specific.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --platform)
      IFS=/ read -r GOOS GOARCH <<<"$2"
      shift 2
      ;;
    --out)
      OUT_DIR="$2"
      shift 2
      ;;
    --version)
      VERSION="$2"
      shift 2
      ;;
    --no-sign)
      SIGN=0
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

HOST_GOOS="$(go env GOOS 2>/dev/null || uname | tr '[:upper:]' '[:lower:]')"
HOST_GOARCH="$(go env GOARCH 2>/dev/null || uname -m)"
case "$HOST_GOARCH" in
  x86_64) HOST_GOARCH="amd64" ;;
  aarch64) HOST_GOARCH="arm64" ;;
esac
if [[ "$GOOS/$GOARCH" != "$HOST_GOOS/$HOST_GOARCH" ]]; then
  echo "ml: cross-building Python runtimes is unsupported: requested $GOOS/$GOARCH on $HOST_GOOS/$HOST_GOARCH" >&2
  exit 2
fi

if [[ -z "$VERSION" ]]; then
  VERSION="$(git describe --tags --dirty --always 2>/dev/null || date -u +%Y%m%d%H%M%S)"
fi

export UV_CACHE_DIR="${UV_CACHE_DIR:-$BUILD_CACHE/uv}"
export XDG_CACHE_HOME="${XDG_CACHE_HOME:-$BUILD_CACHE/xdg}"
export TORCH_HOME="${TORCH_HOME:-$BUILD_CACHE/torch}"
export PIP_CACHE_DIR="${PIP_CACHE_DIR:-$BUILD_CACHE/pip}"
export MPLCONFIGDIR="${MPLCONFIGDIR:-$BUILD_CACHE/matplotlib}"

ASSET="deadca7-ml-${GOOS}-${GOARCH}.tar.gz"
PKG="deadca7-ml-${GOOS}-${GOARCH}"
STAGE="$WORK_DIR/$PKG"

copy_tree() {
  local src="$1"
  local dst="$2"
  rm -rf "$dst"
  mkdir -p "$(dirname "$dst")"
  if command -v rsync >/dev/null 2>&1; then
    mkdir -p "$dst"
    rsync -a --delete "$src"/ "$dst"/
  elif [[ "$GOOS" == "darwin" ]] && command -v ditto >/dev/null 2>&1; then
    ditto "$src" "$dst"
  else
    cp -a "$src" "$dst"
  fi
}

require_tool() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "ml: missing required tool: $1" >&2
    exit 2
  fi
}

managed_python_install() {
  "$UV" python install 3.11 >&2
  UV_PYTHON_PREFERENCE=only-managed "$UV" python find 3.11
}

runtime_python() {
  printf '%s\n' "$STAGE/python/bin/python3"
}

install_python() {
  local python_install="$1"
  copy_tree "$python_install" "$STAGE/python"
}

filter_requirements() {
  local src="$1"
  local dst="$2"

  awk '
    /^[[:space:]]*#/ { print; next }
    /^[[:space:]]*$/ { print; next }
    tolower($0) ~ /^torch==/ { next }
    tolower($0) ~ /^(cuda-|nvidia-|triton==)/ { next }
    { print }
  ' "$src" > "$dst"
}

torch_requirement() {
  awk 'tolower($0) ~ /^torch==/ { print $1; exit }' "$@"
}

install_runtime_deps() {
  echo "ml: staging shared PyTorch runtime"
  local analysis_raw="$WORK_DIR/analysis-requirements.raw.txt"
  local stems_raw="$WORK_DIR/stems-requirements.raw.txt"
  local analysis_reqs="$WORK_DIR/analysis-requirements.txt"
  local stems_reqs="$WORK_DIR/stems-requirements.txt"

  "$UV" export --project bundle/python/analysis --all-packages --no-emit-workspace --no-hashes -o "$analysis_raw"
  "$UV" export --project bundle/python/stems --no-emit-project --no-hashes -o "$stems_raw"

  local torch_req
  torch_req="$(torch_requirement "$analysis_raw" "$stems_raw")"
  if [[ -z "$torch_req" ]]; then
    echo "ml: torch requirement not found in exported runtime requirements" >&2
    exit 2
  fi

  rm -rf "$STAGE/lib"
  mkdir -p "$STAGE/lib"
  local torch_args=(pip install --python "$(runtime_python)" --target "$STAGE/lib")
  if [[ "$GOOS" == "linux" ]]; then
    torch_args+=(--torch-backend cpu)
  fi
  torch_args+=("$torch_req")
  "$UV" "${torch_args[@]}"

  echo "ml: staging analysis Python dependencies"
  filter_requirements "$analysis_raw" "$analysis_reqs"
  rm -rf "$STAGE/analysis/lib"
  mkdir -p "$STAGE/analysis/lib"
  local analysis_args=(pip install --python "$(runtime_python)" --target "$STAGE/analysis/lib" --no-deps)
  if [[ "$GOOS" == "linux" ]]; then
    analysis_args+=(--torch-backend cpu)
  fi
  analysis_args+=(-r "$analysis_reqs")
  "$UV" "${analysis_args[@]}"

  echo "ml: staging stems Python dependencies"
  filter_requirements "$stems_raw" "$stems_reqs"
  rm -rf "$STAGE/stems/lib"
  mkdir -p "$STAGE/stems/lib"
  local stems_args=(pip install --python "$(runtime_python)" --target "$STAGE/stems/lib" --no-deps)
  if [[ "$GOOS" == "linux" ]]; then
    stems_args+=(--torch-backend cpu)
  fi
  stems_args+=(-r "$stems_reqs")
  "$UV" "${stems_args[@]}"
}

build_analysis() {
  echo "ml: staging analysis runtime"
  local python
  python="$(runtime_python)"
  copy_tree "$ROOT/bundle/python/beatthis" "$STAGE/beatthis"

  PYTHONPATH="$STAGE/analysis/lib:$STAGE/lib" PYTHONNOUSERSITE=1 "$python" -c 'import sys, types; sys.modules.setdefault("pyaudio", types.SimpleNamespace(paFloat32=0, PyAudio=lambda: None)); from BeatNet.BeatNet import BeatNet; import librosa, madmom, numpy, soundfile, torch; import beat_this; print("analysis runtime ok")'

  local checkpoint="$STAGE/beatthis/final0.ckpt"
  if [[ ! -f "$checkpoint" ]]; then
    echo "ml: fetching Beat This final0 checkpoint"
    PYTHONPATH="$STAGE/analysis/lib:$STAGE/lib" PYTHONNOUSERSITE=1 "$python" - "$checkpoint" <<'PY'
import os
import shutil
import sys
import torch
from beat_this.inference import load_checkpoint

dst = sys.argv[1]
load_checkpoint("final0")
src = os.path.join(torch.hub.get_dir(), "checkpoints", "beat_this-final0.ckpt")
shutil.copy(src, dst)
print("bundled", src)
PY
  fi
}

build_stems() {
  local python
  python="$(runtime_python)"
  echo "ml: staging stems runtime"
  mkdir -p "$STAGE/stems"
  cp "$ROOT/bundle/python/stems/separate.py" "$STAGE/stems/separate.py"

  PATH="$STAGE/tools:$PATH" PYTHONPATH="$STAGE/stems/lib:$STAGE/lib" PYTHONNOUSERSITE=1 "$python" -c 'from audio_separator.separator import Separator; print("stems runtime ok")'

  local models="$STAGE/stems/models"
  mkdir -p "$models"
  if [[ ! -f "$models/$STEM_MODEL" ]]; then
    echo "ml: fetching default stems model"
    PATH="$STAGE/tools:$PATH" PYTHONPATH="$STAGE/stems/lib:$STAGE/lib" PYTHONNOUSERSITE=1 "$python" - "$models" "$STEM_MODEL" <<'PY'
import sys
from audio_separator.separator import Separator

models, model = sys.argv[1], sys.argv[2]
s = Separator(model_file_dir=models)
s.load_model(model_filename=model)
print("stems model downloaded")
PY
  fi
}

build_embed() {
  local python
  python="$(runtime_python)"
  echo "ml: staging embed runtime"
  mkdir -p "$STAGE/embed"
  cp "$ROOT/bundle/python/embed/embed.py" "$STAGE/embed/embed.py"

  PYTHONPATH="$STAGE/analysis/lib:$STAGE/lib" PYTHONNOUSERSITE=1 "$python" -c 'import laion_clap, torchvision; print("embed runtime ok")'

  local models="$STAGE/embed/models"
  local ckpt="$models/$EMBED_MODEL"
  mkdir -p "$models"
  if [[ ! -f "$ckpt" ]]; then
    echo "ml: fetching CLAP music checkpoint (slimmed to model weights)"
    PYTHONPATH="$STAGE/analysis/lib:$STAGE/lib" PYTHONNOUSERSITE=1 "$python" - "$ckpt" "$EMBED_MODEL" <<'PY'
import os
import sys
import urllib.request
import torch

dst, name = sys.argv[1], sys.argv[2]
tmp = dst + ".full"
urllib.request.urlretrieve(f"https://huggingface.co/lukewys/laion_clap/resolve/main/{name}", tmp)
# The published checkpoint carries optimizer state the runtime never uses;
# keeping only state_dict shrinks the bundle by roughly two thirds.
full = torch.load(tmp, map_location="cpu", weights_only=False)
torch.save({"epoch": full.get("epoch", 0), "state_dict": full["state_dict"]}, dst)
os.remove(tmp)
print("bundled", dst, os.path.getsize(dst), "bytes")
PY
  fi

  # Seed the HF cache with roberta-base's config and tokenizer, then prove
  # the whole thing answers OFFLINE -- an installed app never touches the
  # network. The weights themselves are not kept: embed.py builds the text
  # tower from the config and the CLAP checkpoint fills it, so the 476 MB
  # model file only ever served the download step.
  local hfcache="$STAGE/embed/hfcache"
  mkdir -p "$hfcache"
  HF_HOME="$hfcache" PYTHONPATH="$STAGE/analysis/lib:$STAGE/lib" PYTHONNOUSERSITE=1 \
    EMBED_CHECKPOINT="$ckpt" "$python" "$STAGE/embed/embed.py" --texts '["smoke"]' > /dev/null
  find "$hfcache" \( -name 'model.safetensors' -o -name 'pytorch_model.bin' -o -name '*.h5' -o -name '*.msgpack' \) -print0 \
    | while IFS= read -r -d '' f; do
        local blob
        blob="$(readlink "$f" || true)"
        if [[ -n "$blob" ]]; then
          rm -f "$(dirname "$f")/$blob"
        fi
        rm -f "$f"
      done
  HF_HOME="$hfcache" HF_HUB_OFFLINE=1 TRANSFORMERS_OFFLINE=1 \
    PYTHONPATH="$STAGE/analysis/lib:$STAGE/lib" PYTHONNOUSERSITE=1 \
    EMBED_CHECKPOINT="$ckpt" "$python" "$STAGE/embed/embed.py" --texts '["offline smoke"]' > /dev/null
  echo "ml: embed offline smoke ok"
}

bundle_ffmpeg() {
  mkdir -p "$STAGE/tools"
  case "$GOOS" in
    darwin)
      echo "ml: bundling macOS ffmpeg runtime"
      for tool in ffmpeg ffprobe; do
        local url="https://ffmpeg.martin-riedl.de/download/macos/arm64/${FFMPEG_BUILD}/${tool}.zip"
        local zip="$WORK_DIR/${tool}.zip"
        curl -fsSL -o "$zip" "$url"
        unzip -oq "$zip" -d "$STAGE/tools"
        rm -f "$zip"
        chmod 0755 "$STAGE/tools/$tool"
      done
      ;;
    linux)
      echo "ml: bundling Linux ffmpeg runtime"
      local platform
      case "$GOARCH" in
        amd64) platform="linux64" ;;
        arm64) platform="linuxarm64" ;;
        *) echo "ml: unsupported Linux ffmpeg architecture: $GOARCH" >&2; exit 2 ;;
      esac
      local asset="ffmpeg-${FFMPEG_LINUX_BUILD}-${platform}-gpl.tar.xz"
      local archive="$WORK_DIR/$asset"
      local extract="$WORK_DIR/ffmpeg-linux"
      curl -fsSL -o "$archive" "https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/$asset"
      rm -rf "$extract"
      mkdir -p "$extract"
      tar -xJf "$archive" -C "$extract"
      for tool in ffmpeg ffprobe; do
        local src
        src="$(find "$extract" -path "*/bin/$tool" -type f -print -quit)"
        if [[ -z "$src" ]]; then
          echo "ml: $tool not found in $asset" >&2
          exit 2
        fi
        cp "$src" "$STAGE/tools/$tool"
        chmod 0755 "$STAGE/tools/$tool"
      done
      rm -f "$archive"
      rm -rf "$extract"
      ;;
  esac
}

macos_identity() {
  if [[ -n "${APPLE_CODESIGN_IDENTITY:-}" ]]; then
    printf '%s\n' "$APPLE_CODESIGN_IDENTITY"
    return 0
  fi
  security find-identity -v -p codesigning 2>/dev/null \
    | sed -n 's/.*"\(Developer ID Application:[^"]*\)".*/\1/p' \
    | head -1
}

write_macos_entitlements() {
  cat > "$1" <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>com.apple.security.cs.allow-jit</key>
  <true/>
  <key>com.apple.security.cs.allow-unsigned-executable-memory</key>
  <true/>
  <key>com.apple.security.cs.disable-library-validation</key>
  <true/>
</dict>
</plist>
EOF
}

sign_macos_runtime() {
  [[ "$GOOS" == "darwin" && "$SIGN" == "1" ]] || return 0
  require_tool codesign
  require_tool file

  local identity
  identity="$(macos_identity || true)"
  local args=(--force --sign)
  if [[ -n "$identity" ]]; then
    echo "ml: signing Mach-O files with $identity"
    local entitlements="$WORK_DIR/entitlements.plist"
    write_macos_entitlements "$entitlements"
    args+=("$identity" --options runtime --timestamp --entitlements "$entitlements")
  else
    echo "ml: no Developer ID identity found; ad-hoc signing Mach-O files"
    args+=("-")
  fi

  while IFS= read -r -d '' f; do
    if file -b "$f" | grep -q 'Mach-O'; then
      codesign "${args[@]}" "$f"
    fi
  done < <(find "$STAGE" -type f -print0)
}

build_algos() {
  echo "ml: staging algorithms and settings"
  local python
  python="$(runtime_python)"
  rm -rf "$STAGE/algos" "$STAGE/settings"
  mkdir -p "$STAGE/algos" "$STAGE/settings"
  local dir name
  for dir in "$ROOT"/algos/*/; do
    name="$(basename "$dir")"
    [[ -f "$dir/runner.py" && -f "$dir/algo.json" ]] || continue
    mkdir -p "$STAGE/algos/$name"
    cp "$dir/runner.py" "$STAGE/algos/$name/runner.py"
    cp "$dir/algo.json" "$STAGE/algos/$name/algo.json"
    # Every runner answers --help on the bundle's own interpreter and
    # libraries: a syntax error or a missing import fails the build here.
    PYTHONPATH="$STAGE/analysis/lib:$STAGE/lib" PYTHONNOUSERSITE=1 "$python" "$STAGE/algos/$name/runner.py" --help > /dev/null
  done
  cp "$ROOT"/settings/*.json "$STAGE/settings/"
  echo "ml: algos ok ($(ls "$STAGE/algos" | tr '\n' ' '))"
}

build_engine() {
  echo "ml: building the engine"
  require_tool cargo
  local target_dir="$ROOT/engine/target"
  (cd "$ROOT/engine" && cargo build --release --locked -p engine)
  mkdir -p "$STAGE/bin"
  cp "$target_dir/release/engine" "$STAGE/bin/engine"
  chmod 755 "$STAGE/bin/engine"
  # The engine answers describe from the stage: the legs, the checkpoint
  # and the interpreter are all where it expects them.
  (cd "$STAGE" && DEADCA7_BUNDLE="$STAGE" ./bin/engine describe > /dev/null)
  echo "ml: engine ok ($(cd "$STAGE" && ./bin/engine version))"
}

write_manifest() {
  local created
  created="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  mkdir -p "$STAGE"
  # Layout 3 = layout 2 (algos/ and settings/) plus the engine. Every
  # layout-1 path keeps its name, so a layout-1 installer (deadcatalog, and
  # DEADCA7 before its cutover) reads this manifest unchanged.
  # docs/bundle-format.md is the contract.
  local algos
  algos="$(cd "$STAGE/algos" && ls -d */ | sed 's#/##' | awk 'BEGIN{ORS=""} NR>1{print ", "} {print "\"" $0 "\""}')"
  cat > "$STAGE/manifest.json" <<EOF
{
  "name": "deadca7-ml",
  "version": "$VERSION",
  "platform": "$GOOS/$GOARCH",
  "layout": 3,
  "created_at": "$created",
  "python": "python/bin/python3",
  "lib": "lib",
  "analysis_lib": "analysis/lib",
  "algos": "algos",
  "algo_names": [$algos],
  "settings": "settings",
  "engine": "bin/engine",
  "protocols": [1],
  "recipe": "$(cd "$STAGE" && ./bin/engine describe | python3 -c 'import json,sys; print(json.load(sys.stdin)["recipe"])')",
  "beatthis_script": "beatthis/grid.py",
  "beatthis_checkpoint": "beatthis/final0.ckpt",
  "embed_hfcache": "embed/hfcache",
  "embed_models": "embed/models",
  "embed_script": "embed/embed.py",
  "stems_script": "stems/separate.py",
  "stems_lib": "stems/lib",
  "stems_models": "stems/models",
  "tools": "tools"
}
EOF
}

archive_runtime() {
  mkdir -p "$OUT_DIR"
  rm -f "$OUT_DIR/$ASSET" "$OUT_DIR/checksums.txt"
  tar -C "$WORK_DIR" -czf "$OUT_DIR/$ASSET" "$PKG"
  (
    cd "$OUT_DIR"
    if command -v shasum >/dev/null 2>&1; then
      shasum -a 256 "$ASSET" > checksums.txt
    else
      sha256sum "$ASSET" > checksums.txt
    fi
  )
  echo "ml: wrote $OUT_DIR/$ASSET"
  echo "ml: wrote $OUT_DIR/checksums.txt"
}

require_tool "$UV"
require_tool tar
require_tool curl
if ! command -v shasum >/dev/null 2>&1 && ! command -v sha256sum >/dev/null 2>&1; then
  echo "ml: missing required tool: shasum or sha256sum" >&2
  exit 2
fi
if [[ "$GOOS" == "darwin" ]]; then
  require_tool unzip
fi

rm -rf "$STAGE"
mkdir -p "$WORK_DIR" "$STAGE"

PYTHON_BIN="$(managed_python_install)"
PYTHON_INSTALL="$(cd "$(dirname "$PYTHON_BIN")/.." && pwd -P)"

install_python "$PYTHON_INSTALL"
install_runtime_deps
build_analysis
build_embed
bundle_ffmpeg
build_stems
build_algos
build_engine
write_manifest
sign_macos_runtime
archive_runtime
