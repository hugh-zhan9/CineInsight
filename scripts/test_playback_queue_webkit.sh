#!/usr/bin/env bash
# Uses only synthetic media/API fixtures in a temporary directory.
set -euo pipefail
if [[ "$(uname -s)" != Darwin ]]; then
  echo 'WKWebView verification requires macOS and Xcode command line tools.' >&2
  exit 2
fi
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d "${TMPDIR:-/tmp}/cineinsight-queue-webkit.XXXXXX")"
trap 'rm -rf "$work_dir"' EXIT
node "$repo_root/frontend/scripts/playback-queue-webkit/build.mjs" "$work_dir/dist" >&2
ffmpeg -hide_banner -loglevel error -f lavfi -i 'color=c=green:s=160x90:r=10' -t 3 -c:v libx264 -pix_fmt yuv420p -an "$work_dir/dist/clip.mp4"
clang -fobjc-arc -framework Cocoa -framework WebKit "$repo_root/frontend/scripts/virtual-window-webkit/runner.m" -o "$work_dir/measure"
"$work_dir/measure" "$work_dir/dist/index.html"
