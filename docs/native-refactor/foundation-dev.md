---
scope: Native foundation build slice
created_at: 2026-05-12
---

# Native Foundation Development

This repository now contains the first native replacement foundation:

- `rust/`: Rust workspace for the future daemon/core.
- `macos/CineInsightNative/`: SwiftPM macOS SwiftUI scaffold.
- `contracts/native-api.yaml`: private daemon API seed contract.
- `docs/native-refactor/api-parity-matrix.md`: legacy Wails API parity inventory.
- `docs/native-refactor/schema-inventory.md`: PostgreSQL compatibility inventory.

## Verification

Use:

```bash
scripts/verify_native_foundation.sh
```

The script runs:

- legacy Go tests
- Rust format check
- Rust clippy
- Rust workspace tests
- Swift smoke executable tests
- Swift app build

## Environment Notes

The current machine has Apple Command Line Tools but not full Xcode selected. Because `xcodebuild` is unavailable in this environment, the foundation uses SwiftPM for compile/smoke verification. A future packaging phase should add an Xcode project or generated project once full Xcode is available.

## Daemon Foundation

The Rust daemon foundation currently exposes the tested core pieces needed by later phases:

- bearer-token protected `/health`
- health payload contract
- PostgreSQL `PG_*` config loading
- static legacy schema validation against required tables, indexes, and extensions

The daemon is not yet a long-running bound server in this slice; that belongs to the next implementation step after the foundation contract is stable.
