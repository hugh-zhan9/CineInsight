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
mkdir -p "$macos_root" "$resources_root/bin" "$resources_root/contracts" "$resources_root/sidecars" "$resources_root/runtime" "$staging_root"

cd "$repo_root/rust"
cargo build --release --bin cine-daemon

cd "$repo_root/macos/CineInsightNative"
swift build -c release --product CineInsightNative

cp "$repo_root/macos/CineInsightNative/.build/release/CineInsightNative" "$macos_root/CineInsightNative"
cp "$repo_root/rust/target/release/cine-daemon" "$resources_root/bin/cine-daemon"
cp "$repo_root/contracts/native-api.yaml" "$resources_root/contracts/native-api.yaml"
cp "$repo_root/build/bin/析微影策.app/Contents/Resources/iconfile.icns" "$resources_root/iconfile.icns"
if [[ ! -s "$repo_root/frontend/dist/short.html" ]]; then
  (cd "$repo_root/frontend" && npm run build)
fi
mkdir -p "$resources_root/short-feed"
cp "$repo_root/frontend/dist/short.html" "$resources_root/short-feed/short.html"
cp -R "$repo_root/frontend/dist/assets" "$resources_root/short-feed/assets"
cp "$repo_root/services/whisperx_worker.py" "$resources_root/sidecars/whisperx_worker.py"
cp "$repo_root/services/qwen_asr_worker.py" "$resources_root/sidecars/qwen_asr_worker.py"
mkdir -p "$resources_root/runtime/whisperx_sidecar/venv/bin" "$resources_root/runtime/whisperx_sidecar/hf" "$resources_root/runtime/whisperx_sidecar/torch"
mkdir -p "$resources_root/runtime/qwen_asr_sidecar/venv/bin" "$resources_root/runtime/qwen_asr_sidecar/hf" "$resources_root/runtime/qwen_asr_sidecar/torch"
cp "$repo_root/services/whisperx_worker.py" "$resources_root/runtime/whisperx_sidecar/whisperx_worker.py"
cp "$repo_root/services/qwen_asr_worker.py" "$resources_root/runtime/qwen_asr_sidecar/qwen_asr_worker.py"
python_bin="$(command -v python3 || true)"
if [[ -n "$python_bin" ]]; then
  cat > "$resources_root/runtime/whisperx_sidecar/venv/bin/python3" <<PYTHON
#!/usr/bin/env sh
exec "$python_bin" "\$@"
PYTHON
  cat > "$resources_root/runtime/qwen_asr_sidecar/venv/bin/python3" <<PYTHON
#!/usr/bin/env sh
exec "$python_bin" "\$@"
PYTHON
fi
cat > "$resources_root/runtime/manifest.json" <<'MANIFEST'
{
  "schema": 1,
  "sidecars": {
    "whisperx": {
      "worker": "whisperx_sidecar/whisperx_worker.py",
      "venv_python": "whisperx_sidecar/venv/bin/python3",
      "package": "whisperx==3.8.2",
      "model": "medium"
    },
    "qwen": {
      "worker": "qwen_asr_sidecar/qwen_asr_worker.py",
      "venv_python": "qwen_asr_sidecar/venv/bin/python3",
      "package": "qwen-asr",
      "model": "Qwen/Qwen3-ASR-1.7B",
      "aligner": "Qwen/Qwen3-ForcedAligner-0.6B"
    }
  }
}
MANIFEST
cat > "$resources_root/runtime/README.txt" <<'RUNTIME'
CineInsight ASR runtime cache

WhisperX and Qwen ASR Python environments, Hugging Face models, and Torch cache data are prepared here by the native daemon.
RUNTIME
chmod +x "$macos_root/CineInsightNative" "$resources_root/bin/cine-daemon"
chmod +x "$resources_root/sidecars/whisperx_worker.py" "$resources_root/sidecars/qwen_asr_worker.py"
chmod +x "$resources_root/runtime/whisperx_sidecar/whisperx_worker.py" "$resources_root/runtime/qwen_asr_sidecar/qwen_asr_worker.py"
chmod +x "$resources_root/runtime/whisperx_sidecar/venv/bin/python3" "$resources_root/runtime/qwen_asr_sidecar/venv/bin/python3"

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
  <string>析微影策</string>
  <key>CFBundleDisplayName</key>
  <string>析微影策</string>
  <key>CFBundleIconFile</key>
  <string>iconfile</string>
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
- WhisperX/Qwen Python worker scripts and runtime cache directories are bundled under Contents/Resources.
- Python packages and model caches are prepared/downloaded into the bundled runtime directory during development use.
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
