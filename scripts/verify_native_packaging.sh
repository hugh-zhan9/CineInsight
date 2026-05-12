#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
artifact_root="$repo_root/target/native-package-smoke"

rm -rf "$artifact_root"
mkdir -p "$artifact_root/bin" "$artifact_root/contracts"

cd "$repo_root/rust"
cargo build --bin cine-daemon

cd "$repo_root/macos/CineInsightNative"
swift build --product CineInsightNative

cp "$repo_root/rust/target/debug/cine-daemon" "$artifact_root/bin/cine-daemon"
cp "$repo_root/contracts/native-api.yaml" "$artifact_root/contracts/native-api.yaml"

test -x "$artifact_root/bin/cine-daemon"
test -s "$artifact_root/contracts/native-api.yaml"

echo "native packaging smoke passed"
