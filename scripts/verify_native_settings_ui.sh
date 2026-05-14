#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
content_view="$repo_root/macos/CineInsightNative/Sources/CineInsightNative/ContentView.swift"

test -s "$content_view"

settings_panel="$(
  awk '
    /private var settingsPanel: some View/ { in_panel = 1 }
    in_panel { print }
    /private var settingsUnavailableHint: some View/ { exit }
  ' "$content_view"
)"

if printf '%s\n' "$settings_panel" | rg -n 'if let settings = library\.settings|guard let settings = library\.settings' >/tmp/cineinsight-settings-ui.txt; then
  cat /tmp/cineinsight-settings-ui.txt >&2
  echo "settings UI must remain visible when daemon settings are unavailable" >&2
  exit 1
fi

rg -n 'settingsUnavailableHint' "$content_view" >/dev/null
rg -n 'disabled\(library\.settings == nil\)' "$content_view" >/dev/null

echo "native settings ui passed"
