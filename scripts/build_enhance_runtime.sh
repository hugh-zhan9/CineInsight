#!/bin/bash
# 构建视频超分 sidecar 运行时包（P-012 定稿 §1）。
#
# 产出目录结构（复制到 .app/Contents/Resources/enhance-runtime/，
# 开发模式放在可执行文件旁的 enhance-runtime/）：
#   bin/realesrgan-ncnn-vulkan        darwin/arm64 原生二进制（源码构建）
#   models/realesrgan-x4plus.{param,bin}
#   models/realesr-animevideov3.{param,bin}
#   licenses/…                        第三方许可证
#   manifest.json                     runtime_version + 全部文件 SHA-256
#
# 前置条件：Xcode 命令行工具、cmake、Vulkan SDK（MoltenVK）。
# 本脚本只做源码拉取、构建、模型收集与清单生成；应用签名/公证在
# 应用打包流程中对整个 Resources 目录统一进行。
set -euo pipefail

RUNTIME_VERSION="realesrgan-ncnn-vulkan-v0.2.0-cineinsight1"
UPSTREAM_TAG="v0.2.0"
OUT_DIR="${1:-build/enhance-runtime}"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT

if [[ "$(uname -sm)" != "Darwin arm64" ]]; then
  echo "必须在 Apple Silicon macOS 上构建" >&2
  exit 1
fi

echo "==> 拉取 realesrgan-ncnn-vulkan ${UPSTREAM_TAG}（含 ncnn 子模块）"
git clone --depth 1 --branch "$UPSTREAM_TAG" --recurse-submodules \
  https://github.com/xinntao/Real-ESRGAN-ncnn-vulkan.git "$WORK_DIR/src"

echo "==> 构建 darwin/arm64 二进制"
cmake -S "$WORK_DIR/src/src" -B "$WORK_DIR/build" \
  -DCMAKE_BUILD_TYPE=Release -DCMAKE_OSX_ARCHITECTURES=arm64
cmake --build "$WORK_DIR/build" -j "$(sysctl -n hw.ncpu)"

echo "==> 组装运行时目录"
rm -rf "$OUT_DIR"
mkdir -p "$OUT_DIR/bin" "$OUT_DIR/models" "$OUT_DIR/licenses"
cp "$WORK_DIR/build/realesrgan-ncnn-vulkan" "$OUT_DIR/bin/"
for model in realesrgan-x4plus realesr-animevideov3; do
  cp "$WORK_DIR/src/models/${model}.param" "$OUT_DIR/models/"
  cp "$WORK_DIR/src/models/${model}.bin" "$OUT_DIR/models/"
done
cp "$WORK_DIR/src/LICENSE" "$OUT_DIR/licenses/REAL-ESRGAN-NCNN-VULKAN-LICENSE.txt"

echo "==> 生成 manifest.json"
python3 - "$OUT_DIR" "$RUNTIME_VERSION" <<'PY'
import hashlib, json, os, sys
out_dir, version = sys.argv[1], sys.argv[2]
files = []
for root, _, names in os.walk(out_dir):
    for name in sorted(names):
        full = os.path.join(root, name)
        rel = os.path.relpath(full, out_dir)
        if rel == "manifest.json":
            continue
        digest = hashlib.sha256(open(full, "rb").read()).hexdigest()
        files.append({"path": rel, "sha256": digest})
manifest = {
    "runtime_version": version,
    "binary": "bin/realesrgan-ncnn-vulkan",
    "model_dir": "models",
    "files": sorted(files, key=lambda item: item["path"]),
}
with open(os.path.join(out_dir, "manifest.json"), "w") as handle:
    json.dump(manifest, handle, ensure_ascii=False, indent=2)
print(f"manifest: {len(files)} files")
PY

echo "==> 完成：$OUT_DIR"
echo "    将该目录随应用打包到 Contents/Resources/enhance-runtime/ 并纳入签名。"
