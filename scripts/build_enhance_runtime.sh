#!/bin/bash
# 构建视频超分 sidecar 运行时包（P-012 定稿 §1）。
#
# 产出目录结构（复制到 .app/Contents/Resources/enhance-runtime/，
# 开发模式放在可执行文件旁的 enhance-runtime/）：
#   bin/realesrgan-ncnn-vulkan        darwin/arm64 原生二进制（源码构建）
#   models/realesrgan-x4plus.{param,bin}
#   models/realesr-animevideov3-x2.{param,bin}（sidecar 对该模型按 <name>-x<scale> 拼路径）
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
# 上游 CMakeLists 声明的最低版本早于 3.5，cmake 4 已移除该兼容；
# CMAKE_POLICY_VERSION_MINIMUM 让它按 3.5 的策略配置（cmake 自身给出的对策）。
cmake -S "$WORK_DIR/src/src" -B "$WORK_DIR/build" \
  -DCMAKE_BUILD_TYPE=Release -DCMAKE_OSX_ARCHITECTURES=arm64 \
  -DCMAKE_POLICY_VERSION_MINIMUM=3.5
cmake --build "$WORK_DIR/build" -j "$(sysctl -n hw.ncpu)"

echo "==> 组装运行时目录"
rm -rf "$OUT_DIR"
mkdir -p "$OUT_DIR/bin" "$OUT_DIR/models" "$OUT_DIR/licenses"
cp "$WORK_DIR/build/realesrgan-ncnn-vulkan" "$OUT_DIR/bin/"
# 注意：模型文件不在源码仓库中，需从上游 release 包获取：
#   https://github.com/xinntao/Real-ESRGAN/releases/download/v0.2.5.0/realesrgan-ncnn-vulkan-20220424-macos.zip
# 其中 realesr-animevideov3-x2.{param,bin} 须重命名为 <model>-x<scale> 形式
#（sidecar 对该模型按 scale 拼路径，见上游 main.cpp）。
MODELS_SRC="${MODELS_SRC:-}"
if [[ -z "$MODELS_SRC" ]]; then
  echo "==> 拉取模型文件（上游 release 包）"
  curl -sfL -o "$WORK_DIR/models.zip" \
    "https://github.com/xinntao/Real-ESRGAN/releases/download/v0.2.5.0/realesrgan-ncnn-vulkan-20220424-macos.zip"
  unzip -o -q "$WORK_DIR/models.zip" -d "$WORK_DIR/models"
  MODELS_SRC="$WORK_DIR/models/models"
fi
# 用户裁决：模型不随应用打包，改成用户在设置里按需下载。这里只把模型
# 收集到一个暂存目录用于计算 SHA-256，清单记下身份，产物目录里不留模型。
STAGED_MODELS="$WORK_DIR/staged-models"
mkdir -p "$STAGED_MODELS"
cp "$MODELS_SRC/realesrgan-x4plus.param" "$STAGED_MODELS/"
cp "$MODELS_SRC/realesrgan-x4plus.bin" "$STAGED_MODELS/"
cp "$MODELS_SRC/realesr-animevideov3-x2.param" "$STAGED_MODELS/realesr-animevideov3-x2.param"
cp "$MODELS_SRC/realesr-animevideov3-x2.bin" "$STAGED_MODELS/realesr-animevideov3-x2.bin"
cp "$WORK_DIR/src/LICENSE" "$OUT_DIR/licenses/REAL-ESRGAN-NCNN-VULKAN-LICENSE.txt"
rmdir "$OUT_DIR/models" 2>/dev/null || true

echo "==> 生成 manifest.json"
python3 - "$OUT_DIR" "$RUNTIME_VERSION" "$STAGED_MODELS" <<'PY'
import hashlib, json, os, sys
out_dir, version, models_dir = sys.argv[1], sys.argv[2], sys.argv[3]
files = []
for root, _, names in os.walk(out_dir):
    for name in sorted(names):
        full = os.path.join(root, name)
        rel = os.path.relpath(full, out_dir)
        if rel == "manifest.json":
            continue
        digest = hashlib.sha256(open(full, "rb").read()).hexdigest()
        files.append({"path": rel, "sha256": digest})
# 模型的身份记进清单但文件不入包：应用按需下载后逐个比对这些哈希。
models = []
for name in sorted(os.listdir(models_dir)):
    full = os.path.join(models_dir, name)
    if not os.path.isfile(full):
        continue
    digest = hashlib.sha256(open(full, "rb").read()).hexdigest()
    models.append({"path": name, "sha256": digest})
manifest = {
    "runtime_version": version,
    "binary": "bin/realesrgan-ncnn-vulkan",
    "model_dir": "models",
    "files": sorted(files, key=lambda item: item["path"]),
    "models": models,
}
with open(os.path.join(out_dir, "manifest.json"), "w") as handle:
    json.dump(manifest, handle, ensure_ascii=False, indent=2)
print(f"manifest: {len(files)} bundled files, {len(models)} models (按需下载)")
PY

echo "==> 完成：$OUT_DIR"
echo "    将该目录随应用打包到 Contents/Resources/enhance-runtime/ 并纳入签名。"
