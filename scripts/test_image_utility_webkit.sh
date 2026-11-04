#!/usr/bin/env bash
# Synthetic data URLs only; this fixture never opens the user's library.
set -euo pipefail
[[ "$(uname -s)" == Darwin ]] || { echo 'WKWebView verification requires macOS.' >&2; exit 2; }
[[ $# -le 1 ]] || { echo 'Usage: test_image_utility_webkit.sh [screenshot.png]' >&2; exit 2; }
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d "${TMPDIR:-/tmp}/cineinsight-image-utility-webkit.XXXXXX")"
trap 'rm -rf "$work_dir"' EXIT
nice -n 10 node "$repo_root/frontend/scripts/playback-queue-webkit/build.mjs" "$work_dir/dist" image-utility-fixture.js >&2
nice -n 10 ffmpeg -hide_banner -loglevel error -f lavfi -i 'testsrc2=size=64x64:rate=10' -t 2 -threads 2 -c:v libx264 -pix_fmt yuv420p -an -movflags +faststart "$work_dir/dist/original.mp4"
nice -n 10 ffmpeg -hide_banner -loglevel error -i "$work_dir/dist/original.mp4" -vf scale=128:128 -threads 2 -c:v libx264 -pix_fmt yuv420p -an -movflags +faststart "$work_dir/dist/enhanced.mp4"
nice -n 10 clang -fobjc-arc -framework Cocoa -framework WebKit "$repo_root/frontend/scripts/playback-queue-webkit/runner.m" -o "$work_dir/measure"
"$work_dir/measure" "$work_dir/dist/index.html" "$@"
