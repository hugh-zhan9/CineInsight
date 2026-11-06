#!/usr/bin/env bash
set -euo pipefail
[[ "$(uname -s)" == Darwin ]] || { echo 'WKWebView verification requires macOS.' >&2; exit 2; }
[[ $# -le 1 ]] || { echo 'Usage: test_discovery_webkit.sh [screenshot.png]' >&2; exit 2; }
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d "${TMPDIR:-/tmp}/cine-discovery-webkit.XXXXXX")"
trap 'rm -rf "$work_dir"' EXIT
nice -n 10 node "$repo_root/frontend/scripts/playback-queue-webkit/build.mjs" "$work_dir/dist" ../discovery-webkit/fixture.js >&2
nice -n 10 ffmpeg -hide_banner -loglevel error -f lavfi -i 'color=c=darkslategray:s=640x360:r=10' -t 3 -threads 2 -c:v libx264 -pix_fmt yuv420p -an "$work_dir/dist/clip.mp4"
nice -n 10 clang -fobjc-arc -framework Cocoa -framework WebKit "$repo_root/frontend/scripts/playback-queue-webkit/runner.m" -o "$work_dir/measure"
"$work_dir/measure" "$work_dir/dist/index.html" "$@"
