#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

fail_if_tracked() {
  local pattern="$1"
  local message="$2"
  local matches

  matches="$(git ls-files | rg "$pattern" || true)"
  if [[ -n "$matches" ]]; then
    printf '%s\n' "$matches" >&2
    printf '%s\n' "$message" >&2
    exit 1
  fi
}

fail_if_tracked '(^|/)([^/]+\.go|go\.mod|go\.sum|wails\.json)$' \
  "tracked Go/Wails files remain"
fail_if_tracked '^(cmd|database|models)/' \
  "tracked legacy Go directories remain"
fail_if_tracked '^services/.*\.go$|^services/subtitleparser/' \
  "tracked legacy Go service files remain"
fail_if_tracked '^frontend/(index\.html|wailsjs/|src/(App\.vue|main\.js|components/|styles/|utils/))' \
  "tracked legacy Vue desktop files remain"
fail_if_tracked '^build/(darwin|windows|README\.md)' \
  "tracked legacy Wails build files remain"

test -s build/appicon.png
test -s services/whisperx_worker.py
test -s services/qwen_asr_worker.py
test -s frontend/short.html
test -s frontend/src/short-feed/main.js

echo "no legacy desktop stack passed"
