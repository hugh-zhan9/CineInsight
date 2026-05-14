#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
install_script="$repo_root/scripts/build_and_install_app.sh"

test -s "$install_script"

if rg -n 'wails|build/bin|WAILS_ARGS|resolve_wails' "$install_script" >/tmp/cineinsight-native-install-entrypoint.txt; then
  cat /tmp/cineinsight-native-install-entrypoint.txt >&2
  echo "build_and_install_app.sh must install the Rust/SwiftUI native package, not the legacy Wails app." >&2
  exit 1
fi

rg -n 'package_native_dev\.sh|dist/native-dev/CineInsightNative\.app|dist/native-dev/CineInsightNative-dev\.dmg' "$install_script" >/dev/null

echo "native install entrypoint passed"
