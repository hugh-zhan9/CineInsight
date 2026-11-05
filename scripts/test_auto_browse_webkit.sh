#!/usr/bin/env bash
# Synthetic data URLs only; this fixture never opens the user's library.
set -euo pipefail
[[ "$(uname -s)" == Darwin ]] || { echo 'WKWebView verification requires macOS.' >&2; exit 2; }
[[ $# -le 1 ]] || { echo 'Usage: test_auto_browse_webkit.sh [screenshot.png]' >&2; exit 2; }
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d "${TMPDIR:-/tmp}/cineinsight-auto-browse-webkit.XXXXXX")"
trap 'rm -rf "$work_dir"' EXIT
nice -n 10 node "$repo_root/frontend/scripts/playback-queue-webkit/build.mjs" "$work_dir/dist" auto-browse-fixture.js >&2
nice -n 10 ffmpeg -hide_banner -loglevel error -f lavfi -i 'testsrc2=size=64x64:rate=10' -t 2 -threads 2 -c:v libx264 -pix_fmt yuv420p -an -movflags +faststart "$work_dir/dist/original.mp4"
# Reuse the native runner at a phone-sized viewport without changing other suites.
python3 - "$repo_root/frontend/scripts/playback-queue-webkit/runner.m" "$work_dir/mobile-runner.m" <<'PYCODE'
from pathlib import Path
import sys
source = Path(sys.argv[1]).read_text()
assert 'NSMakeRect(30,30,1160,760)' in source
Path(sys.argv[2]).write_text(source.replace('NSMakeRect(30,30,1160,760)', 'NSMakeRect(30,30,390,844)'))
PYCODE
nice -n 10 clang -fobjc-arc -framework Cocoa -framework WebKit "$work_dir/mobile-runner.m" -o "$work_dir/measure"
"$work_dir/measure" "$work_dir/dist/index.html" "$@"
