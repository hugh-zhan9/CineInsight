#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
OUTPUT="${PROJECT_ROOT}/build/apple-ai/CineInsightAppleAI.app/Contents"
SOURCES="${PROJECT_ROOT}/native/apple-ai"
[[ "$(uname -s)" == Darwin ]] || { echo 'Apple 系统字幕组件仅能在 macOS 构建' >&2; exit 1; }
# 组件最低运行系统为 macOS 15：macOS 26+ 用新的语音识别/翻译接口，macOS 15 用 SFSpeechRecognizer 本机识别
# 与隐藏窗口 translationTask（D-004）。新接口只在 macOS 26 SDK 里有：SDK ≥ 26 时以 APPLE_AI_SDK26 编入，
# 低于 26 的 SDK 构建出的组件只有 macOS 15 路径。SDK 低于 15 无法构建，跳过并提示，打包脚本据此跳过。
sdk_version="$(xcrun --show-sdk-version 2>/dev/null || true)"
sdk_major="${sdk_version%%.*}"
if [[ ! "${sdk_major}" =~ ^[0-9]+$ ]] || (( sdk_major < 15 )); then
  echo "当前 macOS SDK 为 ${sdk_version:-未知}，苹果系统字幕与视觉组件至少需要 macOS 15 SDK，本次不构建；应用里相关功能会显示不可用。" >&2
  exit 0
fi
swift_defines=()
if (( sdk_major >= 26 )); then
  swift_defines+=(-D APPLE_AI_SDK26)
fi
mkdir -p "${OUTPUT}/MacOS"
xcrun clang -fobjc-arc -O2 -Wno-deprecated-declarations -mmacosx-version-min=15.0 -c "${SOURCES}/vision.m" -o "${OUTPUT}/vision.o"
xcrun swiftc -parse-as-library -O -target "$(uname -m)-apple-macos15.0" ${swift_defines[@]+"${swift_defines[@]}"} \
  "${SOURCES}"/*.swift "${OUTPUT}/vision.o" -import-objc-header "${SOURCES}/vision.h" \
  -framework Vision -framework ImageIO -framework CoreImage -o "${OUTPUT}/MacOS/cineinsight-apple-ai"
rm "${OUTPUT}/vision.o"
cat > "${OUTPUT}/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>com.cineinsight.apple-ai</string>
<key>CFBundleName</key><string>CineInsightAppleAI</string>
<key>CFBundleExecutable</key><string>cineinsight-apple-ai</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleVersion</key><string>1</string>
<key>LSMinimumSystemVersion</key><string>15.0</string>
<key>NSSpeechRecognitionUsageDescription</key><string>析微影策使用系统语音识别在本机为视频生成字幕，音频不会上传。</string>
</dict></plist>
PLIST
codesign --force --sign - "${OUTPUT%/Contents}"
