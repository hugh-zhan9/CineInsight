#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

echo "== no legacy desktop stack =="
(cd "$repo_root" && bash scripts/verify_no_legacy_desktop_stack.sh)

echo "== Rust format =="
(cd "$repo_root/rust" && cargo fmt --all --check)

echo "== Rust clippy =="
(cd "$repo_root/rust" && cargo clippy --workspace --all-targets -- -D warnings)

echo "== Rust tests =="
(cd "$repo_root/rust" && cargo test --workspace)

echo "== Swift smoke tests =="
(cd "$repo_root/macos/CineInsightNative" && swift run CineInsightNativeSmokeTests)

echo "== Swift app build =="
(cd "$repo_root/macos/CineInsightNative" && swift build --product CineInsightNative)

echo "native foundation verification passed"
