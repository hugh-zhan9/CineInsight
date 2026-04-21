#!/usr/bin/env bash

set -euo pipefail

MODEL="${1:-Qwen/Qwen3-ASR-1.7B}"
ALIGNER="${2:-Qwen/Qwen3-ForcedAligner-0.6B}"
PYTHON_BIN="${PYTHON_BIN:-python3}"

BASE_DIR="${HOME}/.video-master"
SIDECAR_DIR="${BASE_DIR}/qwen_asr_sidecar"
VENV_DIR="${SIDECAR_DIR}/venv"
VENV_PYTHON="${VENV_DIR}/bin/python3"
HF_DIR="${SIDECAR_DIR}/hf"
HF_HUB_DIR="${HF_DIR}/hub"
TORCH_DIR="${SIDECAR_DIR}/torch"

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "Qwen v1 当前只为 macOS 本地 sidecar 设计。" >&2
  exit 1
fi

if [[ "$(uname -m)" != "arm64" ]]; then
  echo "警告: 当前不是 macOS arm64，脚本仍会继续，但应用默认不会启用该运行时。" >&2
fi

if ! command -v "${PYTHON_BIN}" >/dev/null 2>&1; then
  echo "未找到 Python: ${PYTHON_BIN}" >&2
  echo "可通过 PYTHON_BIN=/path/to/python3 指定解释器。" >&2
  exit 1
fi

echo "==> 停掉应用和残留 Qwen ASR 进程"
pkill -x 析微影策 2>/dev/null || true
pkill -f 'qwen_asr_worker.py' 2>/dev/null || true

echo "==> 检查 Python 版本"
"${PYTHON_BIN}" - <<'PY'
import sys
major, minor = sys.version_info[:2]
if (major, minor) < (3, 10):
    raise SystemExit("Qwen 运行时需要 Python 3.10+")
print(f"Using Python {major}.{minor}", flush=True)
PY

echo "==> 准备缓存目录"
mkdir -p "${HF_HUB_DIR}" "${TORCH_DIR}"

if [[ ! -x "${VENV_PYTHON}" ]]; then
  echo "==> 创建 Qwen 虚拟环境"
  rm -rf "${VENV_DIR}"
  "${PYTHON_BIN}" -m venv "${VENV_DIR}"
fi

echo "==> 升级 pip/setuptools/wheel"
"${VENV_PYTHON}" -m pip install --upgrade pip setuptools wheel

echo "==> 安装 Qwen ASR 运行时依赖"
"${VENV_PYTHON}" -m pip install -U qwen-asr numpy soundfile "huggingface_hub[cli]"

export HF_HOME="${HF_DIR}"
export TORCH_HOME="${TORCH_DIR}"

echo "==> 预热 Hugging Face 缓存"
"${VENV_PYTHON}" - <<'PY' "${MODEL}" "${ALIGNER}" "${HF_HUB_DIR}"
import sys
from huggingface_hub import snapshot_download

model = sys.argv[1]
aligner = sys.argv[2]
cache_dir = sys.argv[3]

print(f"Caching ASR model: {model}", flush=True)
snapshot_download(repo_id=model, cache_dir=cache_dir)

print(f"Caching aligner model: {aligner}", flush=True)
snapshot_download(repo_id=aligner, cache_dir=cache_dir)

print("Hugging Face cache warmed.", flush=True)
PY

echo "==> 校验 qwen-asr 可导入"
"${VENV_PYTHON}" - <<'PY'
import qwen_asr
print("qwen-asr import ok", flush=True)
PY

echo "==> 完成"
echo "虚拟环境: ${VENV_DIR}"
echo "HF 缓存目录: ${HF_HUB_DIR}"
