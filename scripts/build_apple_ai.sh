#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
OUTPUT="${PROJECT_ROOT}/build/apple-ai/CineInsightAppleAI.app/Contents"
[[ "$(uname -s)" == Darwin ]] || { echo 'Apple 系统字幕组件仅能在 macOS 构建' >&2; exit 1; }
# 组件用的语音识别、翻译接口只在 macOS 26 SDK 里有，运行也要求 macOS 26（应用在更低系统上先判版本、
# 直接报「需要 macOS 26」，不会去找组件）。SDK 低于 26 时不构建，打包脚本据此跳过，应用其余部分照常打包。
sdk_major="$(xcrun --show-sdk-version 2>/dev/null | cut -d. -f1)"
if [[ -z "${sdk_major}" || "${sdk_major}" -lt 26 ]]; then
  echo "当前 macOS SDK 为 ${sdk_major:-未知}，苹果系统字幕与视觉组件需要 macOS 26 SDK，本次不构建；应用里相关功能会显示「需要 macOS 26」。" >&2
  exit 0
fi
mkdir -p "${OUTPUT}/MacOS"
xcrun clang -fobjc-arc -O2 -Wno-deprecated-declarations -mmacosx-version-min=26.0 -c "${PROJECT_ROOT}/native/apple-ai/vision.m" -o "${OUTPUT}/vision.o"
xcrun swiftc -parse-as-library -O -target "$(uname -m)-apple-macos26.0" "${PROJECT_ROOT}/native/apple-ai/main.swift" "${OUTPUT}/vision.o" -import-objc-header "${PROJECT_ROOT}/native/apple-ai/vision.h" -framework Vision -framework ImageIO -framework CoreImage -o "${OUTPUT}/MacOS/cineinsight-apple-ai"
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
<key>LSMinimumSystemVersion</key><string>26.0</string>
</dict></plist>
PLIST
codesign --force --sign - "${OUTPUT%/Contents}"
