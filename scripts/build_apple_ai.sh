#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
OUTPUT="${PROJECT_ROOT}/build/apple-ai/CineInsightAppleAI.app/Contents"
[[ "$(uname -s)" == Darwin ]] || { echo 'Apple 系统字幕组件仅能在 macOS 构建' >&2; exit 1; }
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
