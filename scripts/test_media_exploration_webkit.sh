#!/usr/bin/env bash
# Uses only generated media and an owned loopback server, never the user's library.
set -euo pipefail
[[ "$(uname -s)" == Darwin ]] || { echo 'WKWebView verification requires macOS.' >&2; exit 2; }
[[ $# -le 1 ]] || { echo 'Usage: test_media_exploration_webkit.sh [screenshot.png]' >&2; exit 2; }
if [[ $# == 0 && -n "${CINEINSIGHT_EXPLORATION_SCREENSHOT:-}" ]]; then set -- "$CINEINSIGHT_EXPLORATION_SCREENSHOT"; fi
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d "${TMPDIR:-/tmp}/cineinsight-exploration-webkit.XXXXXX")"
fixture_pid=''
cleanup(){ if [[ -n "$fixture_pid" ]]; then kill "$fixture_pid" 2>/dev/null || true; wait "$fixture_pid" 2>/dev/null || true; fi; rm -rf "$work_dir"; }
trap cleanup EXIT
nice -n 10 node "$repo_root/frontend/scripts/playback-queue-webkit/build.mjs" "$work_dir/dist" exploration-fixture.js >&2
ffmpeg -hide_banner -loglevel error -f lavfi -i 'testsrc2=s=640x360:r=10' -t 6 -threads 2 -c:v libx264 -pix_fmt yuv420p -an -movflags +faststart "$work_dir/dist/clip.mp4"
python3 "$repo_root/frontend/scripts/playback-queue-webkit/exploration-fixture-server.py" "$work_dir/dist" "$work_dir/port" > "$work_dir/server.log" 2>&1 &
fixture_pid=$!
for attempt in {1..100}; do [[ -s "$work_dir/port" ]] && break; sleep 0.05; done
[[ -s "$work_dir/port" ]] || { cat "$work_dir/server.log" >&2; exit 1; }
python3 - "$work_dir/dist/index.html" "$work_dir/port" <<'PY'
import pathlib,sys
page=pathlib.Path(sys.argv[1]);port=pathlib.Path(sys.argv[2]).read_text();page.write_text(page.read_text().replace('<meta charset="utf-8">','<meta charset="utf-8"><base href="http://127.0.0.1:'+port+'/">',1))
PY
nice -n 10 clang -fobjc-arc -framework Cocoa -framework WebKit "$repo_root/frontend/scripts/playback-queue-webkit/runner.m" -o "$work_dir/measure"
"$work_dir/measure" "$work_dir/dist/index.html" "$@"
