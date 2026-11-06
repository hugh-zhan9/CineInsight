#!/usr/bin/env bash
# One serial acceptance entry point; fixtures never use the user's library.
set -euo pipefail
[[ "$(uname -s)" == Darwin ]] || { echo 'macOS WebKit acceptance requires macOS.' >&2; exit 2; }
[[ $# == 0 ]] || { echo 'Usage: test_macos_webkit.sh' >&2; exit 2; }
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
for suite in virtual_window viewing_notes playback_queue playback_subtitles playback_clips chapters photo_map media_exploration video_crop image_utility discovery; do
  printf 'WebKit: %s\n' "$suite"
  bash "$script_dir/test_${suite}_webkit.sh"
done
