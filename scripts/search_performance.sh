#!/usr/bin/env bash
# Synthetic only. Set CINEINSIGHT_TEST_PG_DSN explicitly for an isolated PG schema.
set -euo pipefail
[[ $# == 1 ]] || { echo 'Usage: search_performance.sh output.json' >&2; exit 2; }
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
export CINEINSIGHT_SEARCH_PERF=1
export CINEINSIGHT_SEARCH_PERF_OUT="$1"
export GOTOOLCHAIN=go1.24.9
exec /Users/zhangyukun/.local/bin/agent-go test -tags desktop,production ./services -run '^TestLibrarySearchPerformanceEvaluation$' -count=1 -timeout 1500s
