#!/usr/bin/env bash
# Synthetic chapter/media fixtures only; optional output is a verification screenshot.
set -euo pipefail
[[ "$(uname -s)" == Darwin ]] || { echo 'WKWebView verification requires macOS.' >&2; exit 2; }
[[ $# -le 1 ]] || { echo 'Usage: test_chapters_webkit.sh [screenshot.png]' >&2; exit 2; }
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d "${TMPDIR:-/tmp}/cineinsight-chapters-webkit.XXXXXX")"
trap 'rm -rf "$work_dir"' EXIT
nice -n 10 node "$repo_root/frontend/scripts/playback-queue-webkit/build.mjs" "$work_dir/dist" chapter-fixture.js >&2
ffmpeg -hide_banner -loglevel error -f lavfi -i 'color=c=darkslategray:s=640x360:r=10' -t 3 -threads 2 -c:v libx264 -pix_fmt yuv420p -an "$work_dir/dist/clip.mp4"
clang -fobjc-arc -framework Cocoa -framework WebKit "$repo_root/frontend/scripts/playback-queue-webkit/runner.m" -o "$work_dir/measure"
"$work_dir/measure" "$work_dir/dist/index.html" "$@"
