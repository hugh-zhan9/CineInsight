#!/usr/bin/env bash

set -euo pipefail

MODEL="${1:-medium}"
ALIGN_LANG="${2:-zh}"

BASE_DIR="${HOME}/.video-master"
SIDECAR_DIR="${BASE_DIR}/whisperx_sidecar"
VENV_PYTHON="${SIDECAR_DIR}/venv/bin/python3"
ASR_DIR="${SIDECAR_DIR}/model_cache/asr"
ALIGN_DIR="${SIDECAR_DIR}/model_cache/align"
HF_DIR="${SIDECAR_DIR}/hf"
HF_HUB_DIR="${HF_DIR}/hub"
TORCH_DIR="${SIDECAR_DIR}/torch"
XDG_DIR="${SIDECAR_DIR}/xdg_cache"

if [[ ! -x "${VENV_PYTHON}" ]]; then
  echo "WhisperX 虚拟环境不存在: ${VENV_PYTHON}" >&2
  echo "请先在应用里完成一次 WhisperX 运行时安装。" >&2
  exit 1
fi

echo "==> 停掉应用和残留 WhisperX 进程"
pkill -x 析微影策 2>/dev/null || true
pkill -f 'whisperx_worker.py' 2>/dev/null || true

echo "==> 准备缓存目录"
mkdir -p "${ASR_DIR}" "${ALIGN_DIR}" "${HF_HUB_DIR}" "${TORCH_DIR}" "${XDG_DIR}"

echo "==> 清理残留的下载锁和未完成文件"
find "${ASR_DIR}" -type f \( -name '*.incomplete' -o -name '*.lock' \) -print -delete 2>/dev/null || true
find "${ALIGN_DIR}" -type f \( -name '*.incomplete' -o -name '*.lock' \) -print -delete 2>/dev/null || true

export HF_HUB_DISABLE_XET=1
export WHISPERX_ASR_MODEL_DIR="${ASR_DIR}"
export WHISPERX_ALIGN_MODEL_DIR="${ALIGN_DIR}"
export HF_HOME="${HF_DIR}"
export HF_HUB_CACHE="${HF_HUB_DIR}"
export TORCH_HOME="${TORCH_DIR}"
export XDG_CACHE_HOME="${XDG_DIR}"

echo "==> 预拉 WhisperX 模型"
echo "    ASR model: ${MODEL}"
echo "    Align language: ${ALIGN_LANG}"

"${VENV_PYTHON}" - <<'PY' "${MODEL}" "${ALIGN_LANG}" "${ASR_DIR}" "${ALIGN_DIR}"
import sys

model_name = sys.argv[1]
align_lang = sys.argv[2]
asr_dir = sys.argv[3]
align_dir = sys.argv[4]

print(f"Downloading ASR model: {model_name}", flush=True)
from faster_whisper import WhisperModel
WhisperModel(model_name, device="cpu", compute_type="int8", download_root=asr_dir)

print(f"Downloading align model: {align_lang}", flush=True)
import whisperx
align_model, align_meta = whisperx.load_align_model(
    language_code=align_lang,
    device="cpu",
    model_dir=align_dir,
)

del align_model
del align_meta
print("WhisperX prefetch completed.", flush=True)
PY

echo "==> 检查残留状态"
if find "${SIDECAR_DIR}/model_cache" -type f \( -name '*.incomplete' -o -name '*.lock' \) | grep -q .; then
  echo "仍有未完成文件残留，请检查网络后重试：" >&2
  find "${SIDECAR_DIR}/model_cache" -type f \( -name '*.incomplete' -o -name '*.lock' \)
  exit 2
fi

echo "==> 完成"
echo "模型缓存目录: ${SIDECAR_DIR}/model_cache"
