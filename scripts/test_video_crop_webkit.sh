#!/usr/bin/env bash
# Uses only synthetic frames and in-memory recipes in a temporary directory.
set -euo pipefail
if [[ "$(uname -s)" != Darwin ]]; then
  echo 'WKWebView verification requires macOS and Xcode command line tools.' >&2
  exit 2
fi
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d "${TMPDIR:-/tmp}/cineinsight-crop-webkit.XXXXXX")"
trap 'rm -rf "$work_dir"' EXIT
node "$repo_root/frontend/scripts/crop-webkit/build.mjs" "$work_dir/dist" >&2
ffmpeg -hide_banner -loglevel error -threads 2 -filter_threads 1 -f lavfi -i 'color=c=green:s=96x160:r=1,pad=320:180:112:10:black' -frames:v 1 -threads 2 "$work_dir/dist/preview.png"
clang -fobjc-arc -framework Cocoa -framework WebKit "$repo_root/frontend/scripts/playback-queue-webkit/runner.m" -o "$work_dir/measure"
"$work_dir/measure" "$work_dir/dist/index.html" "$@"
