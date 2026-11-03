#!/usr/bin/env bash
# All photo/tile bytes are synthetic and served by an owned loopback fixture.
set -euo pipefail
[[ "$(uname -s)" == Darwin ]] || { echo 'WKWebView verification requires macOS.' >&2; exit 2; }
[[ $# -le 1 ]] || { echo 'Usage: test_photo_map_webkit.sh [screenshot.png]' >&2; exit 2; }
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d "${TMPDIR:-/tmp}/cineinsight-photo-map-webkit.XXXXXX")"
map_fixture_pid=''
cleanup(){ if [[ -n "$map_fixture_pid" ]]; then kill "$map_fixture_pid" 2>/dev/null || true; wait "$map_fixture_pid" 2>/dev/null || true; fi; rm -rf "$work_dir"; }
trap cleanup EXIT
nice -n 10 node "$repo_root/frontend/scripts/playback-queue-webkit/build.mjs" "$work_dir/dist" photo-map-fixture.js >&2
ffmpeg -hide_banner -loglevel error -f lavfi -i 'color=c=darkslategray:s=256x256' -frames:v 1 -threads 2 "$work_dir/dist/tile.png"
python3 "$repo_root/frontend/scripts/playback-queue-webkit/map-fixture-server.py" "$work_dir/dist" "$work_dir/port" > "$work_dir/server.log" 2>&1 &
map_fixture_pid=$!
for attempt in {1..100}; do [[ -s "$work_dir/port" ]] && break; sleep 0.05; done
[[ -s "$work_dir/port" ]] || { cat "$work_dir/server.log" >&2; exit 1; }
python3 - "$work_dir/dist/index.html" "$work_dir/port" <<'PY'
import pathlib,sys
page=pathlib.Path(sys.argv[1]);port=pathlib.Path(sys.argv[2]).read_text();page.write_text(page.read_text().replace('<meta charset="utf-8">','<meta charset="utf-8"><base href="http://127.0.0.1:'+port+'/">',1))
PY
clang -fobjc-arc -framework Cocoa -framework WebKit "$repo_root/frontend/scripts/playback-queue-webkit/runner.m" -o "$work_dir/measure"
"$work_dir/measure" "$work_dir/dist/index.html" "$@"
