#!/usr/bin/env bash
# Native macOS layout regression with synthetic data only; no Wails app or library DB.
set -euo pipefail
if [[ "$(uname -s)" != Darwin ]]; then
  echo 'WKWebView verification requires macOS and Xcode command line tools.' >&2
  exit 2
fi
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fixture_dir="$repo_root/frontend/scripts/virtual-window-webkit"
work_dir="$(mktemp -d "${TMPDIR:-/tmp}/cineinsight-webkit.XXXXXX")"
trap 'rm -rf "$work_dir"' EXIT
node "$fixture_dir/build.mjs" "$work_dir/dist" >&2
clang -fobjc-arc -framework Cocoa -framework WebKit "$fixture_dir/runner.m" -o "$work_dir/measure"
"$work_dir/measure" "$work_dir/dist/index.html"
