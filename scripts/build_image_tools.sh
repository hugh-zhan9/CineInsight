#!/usr/bin/env bash
# Build the small lossless JPEG sidecar for the app's actual minimum OS.
set -euo pipefail
[[ "$(uname -sm)" == 'Darwin arm64' ]] || { echo 'JPEG sidecar build requires Apple Silicon macOS' >&2; exit 1; }
[[ $# == 0 ]] || { echo 'Usage: build_image_tools.sh' >&2; exit 2; }
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT
git clone --depth 1 --branch 3.2.0 https://github.com/libjpeg-turbo/libjpeg-turbo.git "$work_dir/source"
cmake -S "$work_dir/source" -B "$work_dir/build" \
  -DCMAKE_BUILD_TYPE=Release -DCMAKE_OSX_ARCHITECTURES=arm64 -DCMAKE_OSX_DEPLOYMENT_TARGET=15.0 \
  -DCMAKE_INSTALL_PREFIX="$work_dir/install" -DWITH_JPEG8=1 -DWITH_TURBOJPEG=0 \
  -DENABLE_SHARED=1 -DENABLE_STATIC=0 -DWITH_TESTS=0 -DWITH_TOOLS=1
cmake --build "$work_dir/build" --parallel "${CINEINSIGHT_BUILD_JOBS:-2}"
cmake --install "$work_dir/build"
python3 - "$work_dir/install" "$repo_root/build/image-tools" <<'PY'
from pathlib import Path
import shutil,sys,tempfile
source,target=map(Path,sys.argv[1:]); target.parent.mkdir(parents=True,exist_ok=True)
with tempfile.TemporaryDirectory(prefix='.image-tools-build-',dir=target.parent) as work:
    staged=Path(work)/'image-tools';shutil.copytree(source,staged,symlinks=True)
    if not (staged/'bin/jpegtran').is_file() or not (staged/'lib/libjpeg.8.dylib').is_file():
        raise SystemExit('Incomplete JPEG8 build')
    if target.is_symlink(): raise SystemExit('Build output must not be a symlink')
    if target.exists(): shutil.rmtree(target)
    staged.rename(target)
print('Built macOS15 JPEG tools:',target)
PY
