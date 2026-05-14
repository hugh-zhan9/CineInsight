#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

: "${NATIVE_TEST_DATABASE_URL:?NATIVE_TEST_DATABASE_URL must point to a scratch PostgreSQL database}"

cd "$repo_root"
bash scripts/verify_no_legacy_desktop_stack.sh

cd "$repo_root/rust"
cargo fmt --all --check
cargo clippy --workspace --all-targets -- -D warnings
cargo test --workspace

cd "$repo_root/macos/CineInsightNative"
swift run CineInsightNativeSmokeTests
swift build --product CineInsightNative

echo "native video core verification passed"
