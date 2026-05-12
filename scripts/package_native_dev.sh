#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dist_root="$repo_root/dist/native-dev"
bundle_root="$dist_root/CineInsightNative.app"
contents_root="$bundle_root/Contents"
macos_root="$contents_root/MacOS"
resources_root="$contents_root/Resources"
staging_root="$dist_root/dmg-root"
dmg_path="$dist_root/CineInsightNative-dev.dmg"

rm -rf "$dist_root"
mkdir -p "$macos_root" "$resources_root/bin" "$resources_root/contracts" "$staging_root"

cd "$repo_root/rust"
cargo build --release --bin cine-daemon

cd "$repo_root/macos/CineInsightNative"
swift build -c release --product CineInsightNative

cp "$repo_root/macos/CineInsightNative/.build/release/CineInsightNative" "$macos_root/CineInsightNative"
cp "$repo_root/rust/target/release/cine-daemon" "$resources_root/bin/cine-daemon"
cp "$repo_root/contracts/native-api.yaml" "$resources_root/contracts/native-api.yaml"
chmod +x "$macos_root/CineInsightNative" "$resources_root/bin/cine-daemon"

cat > "$contents_root/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleDevelopmentRegion</key>
  <string>zh_CN</string>
  <key>CFBundleExecutable</key>
  <string>CineInsightNative</string>
  <key>CFBundleIdentifier</key>
  <string>local.cineinsight.native.dev</string>
  <key>CFBundleInfoDictionaryVersion</key>
  <string>6.0</string>
  <key>CFBundleName</key>
  <string>CineInsightNative</string>
  <key>CFBundlePackageType</key>
  <string>APPL</string>
  <key>CFBundleShortVersionString</key>
  <string>0.1.0-dev</string>
  <key>CFBundleVersion</key>
  <string>0.1.0-dev</string>
  <key>LSMinimumSystemVersion</key>
  <string>14.0</string>
  <key>NSHighResolutionCapable</key>
  <true/>
</dict>
</plist>
PLIST

cat > "$dist_root/README.txt" <<'README'
CineInsightNative dev package

This is an unsigned local development build.

Run:
1. Copy CineInsightNative.app to /Applications or run it directly from the DMG.
2. If macOS blocks launch because the app is unsigned, right-click the app and choose Open.

Limitations:
- This is not notarized or signed.
- Real PostgreSQL configuration and daemon lifecycle wiring are still development-stage.
- This package is for local smoke testing, not production distribution.
README

cp -R "$bundle_root" "$staging_root/CineInsightNative.app"
cp "$dist_root/README.txt" "$staging_root/README.txt"

if command -v codesign >/dev/null 2>&1; then
  codesign --force --deep --sign - "$staging_root/CineInsightNative.app"
  codesign --verify --deep --strict "$staging_root/CineInsightNative.app"
fi

hdiutil create \
  -volname "CineInsightNative Dev" \
  -srcfolder "$staging_root" \
  -ov \
  -format UDZO \
  "$dmg_path"

test -s "$dmg_path"
echo "$dmg_path"
