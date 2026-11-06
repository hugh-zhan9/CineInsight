#!/usr/bin/env bash
# Isolated Wails window and synthetic AVI; never opens a user's library or installs an app.
set -euo pipefail
[[ "$(uname -sm)" == 'Darwin arm64' ]] || { echo 'Native probe requires Apple Silicon macOS' >&2; exit 2; }
[[ $# == 0 ]] || { echo 'Usage: CINE_NATIVE_PROBE_LIBRARY=/absolute/libmpv.dylib test_native_playback_probe.sh' >&2; exit 2; }
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
probe_lib="${CINE_NATIVE_PROBE_LIBRARY:-${repo_root}/build/native-playback/runtime/lib/libmpv.2.dylib}"
[[ -f "$probe_lib" ]] || { echo 'Explicit libmpv probe library is missing' >&2; exit 1; }
work_dir="$(mktemp -d "${TMPDIR:-/tmp}/cine-native-probe.XXXXXX")"
trap 'rm -rf "$work_dir"' EXIT
nice -n 10 node "$repo_root/frontend/scripts/playback-queue-webkit/build.mjs" "$work_dir/assets" ../native-playback/fixture.js
nice -n 10 ffmpeg -hide_banner -loglevel error -f lavfi -i 'testsrc2=size=640x360:rate=24' -f lavfi -i 'sine=frequency=440:sample_rate=44100' -t 4 -c:v mpeg4 -q:v 5 -c:a libmp3lame -threads 2 "$work_dir/fixture.avi"
cd "$repo_root"
GOTOOLCHAIN=go1.24.9 CGO_LDFLAGS='-framework UniformTypeIdentifiers' /Users/zhangyukun/.local/bin/agent-go build -tags desktop,production,nativeplaybackprobe -o "$work_dir/probe" ./native/playback/probe
CINE_NATIVE_PROBE_LIBRARY="$probe_lib" python3 - "$work_dir" <<'PY'
from pathlib import Path
import os,subprocess,sys,json,shutil,plistlib
root=Path(sys.argv[1]); lib=os.environ['CINE_NATIVE_PROBE_LIBRARY']
# A real signed Wails app bundle, kept entirely inside this disposable fixture.
app=root/'CineInsightNativeProbe.app';contents=app/'Contents';resources=contents/'Resources';binary=contents/'MacOS/probe'
binary.parent.mkdir(parents=True);resources.mkdir(parents=True)
shutil.copy2(root/'probe',binary);shutil.copytree(root/'assets',resources/'assets')
runtime=Path(lib).parent.parent
if not (runtime/'manifest.json').is_file():raise SystemExit('Probe requires the pinned assembled runtime')
manifest=json.loads((runtime/'manifest.json').read_text())
if not manifest.get('source_materials_complete'):raise SystemExit('Native source/license evidence is incomplete')
shutil.copytree(runtime,resources/'native-playback')
lib=str(resources/'native-playback'/manifest['entry'])
(contents/'Info.plist').write_bytes(plistlib.dumps({'CFBundleExecutable':'probe','CFBundleIdentifier':'com.cineinsight.nativeprobe','CFBundleName':'CineInsightNativeProbe','CFBundlePackageType':'APPL','CFBundleVersion':'1','LSMinimumSystemVersion':'15.0','NSHighResolutionCapable':True}))
subprocess.run(['codesign','--force','--sign','-',str(app)],check=True)
subprocess.run(['codesign','--verify','--deep','--strict',str(app)],check=True)
env={key:value for key,value in os.environ.items() if not key.startswith('DYLD_')}
source=os.environ.get('CINE_NATIVE_PROBE_MEDIA',str(root/'fixture.avi'))
repeat=int(os.environ.get('CINE_NATIVE_PROBE_REPEAT','1'))
reports=[]
for index in range(repeat):
    result=subprocess.run([str(binary),str(resources/'assets'),lib,source,str(root/'result.json')],env={**env,'CINE_NATIVE_PROBE_INDEX':str(index)},timeout=90)
    if result.returncode: raise SystemExit(result.returncode)
    report=json.loads((root/'result.json').read_text());reports.append(report)
    print(json.dumps(report,ensure_ascii=False),flush=True)
    if not report.get('passed'):raise SystemExit(1)
output=os.environ.get('CINE_NATIVE_PROBE_REPORT')
if output:Path(output).write_text(json.dumps(reports,indent=2))
PY
