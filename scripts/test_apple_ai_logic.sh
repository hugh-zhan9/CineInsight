#!/usr/bin/env bash
# Logic tests for the Apple AI helper's pure Swift code (snapshot accumulation, word timing, error
# classification, language detection decisions). Needs no system permission, language resource or built helper.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SOURCES="$(cd "${SCRIPT_DIR}/../native/apple-ai" && pwd)"
[[ "$(uname -s)" == Darwin ]] || { echo 'Apple 系统组件的逻辑测试仅能在 macOS 运行' >&2; exit 1; }
WORK="$(mktemp -d "${TMPDIR:-/tmp}/cineinsight-apple-ai-logic.XXXXXX")"
trap 'rm -rf "${WORK}"' EXIT
xcrun swiftc -parse-as-library -target "$(uname -m)-apple-macos15.0" \
  "${SOURCES}/protocol.swift" "${SOURCES}/speech_words.swift" "${SOURCES}/tests/speech_words_tests.swift" \
  -o "${WORK}/speech_words_tests"
"${WORK}/speech_words_tests"
