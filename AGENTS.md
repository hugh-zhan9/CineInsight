# Repository Guidelines

## Project Structure & Module Organization
- `main.go` and `app.go`: Wails app bootstrap and Go methods exposed to the frontend.
- `services/`: business logic (`video_service.go`, `tag_service.go`, `settings_service.go`, `directory_service.go`).
- `database/`: SQLite initialization and migrations.
- `models/`: GORM models and relations.
- `frontend/src/`: Vue UI (`App.vue`, `main.js`, styles/assets).
- `frontend/wailsjs/`: auto-generated Wails bindings. Do not edit manually.
- `build/`: platform packaging resources (Darwin/Windows metadata, icons, installer files).

## Build, Test, and Development Commands
- `go mod download`: install backend dependencies.
- `cd frontend && npm install`: install frontend dependencies.
- `wails dev`: run desktop app in development mode with hot reload.
- `wails build`: produce release binaries under `build/bin/`.
- `cd frontend && npm run build`: build frontend bundle only (mainly for troubleshooting).
- `go test ./...`: run Go tests (currently minimal/no test files; add tests with new logic).

## Coding Style & Naming Conventions
- Go: run `gofmt` before commit; keep package names lowercase and exported methods in `PascalCase`.
- Vue/JS: use 2-space indentation, semicolons, and `camelCase` for methods/data fields.
- Files: Go files use `snake_case` by domain (`video_service.go`), Vue components use `PascalCase`.
- Keep Wails bridge method names stable once consumed by frontend.

## Testing Guidelines
- Prefer table-driven tests for `services/` and `database/` behavior.
- Name Go tests as `Test<FunctionName>` in `*_test.go` files colocated with source.
- For UI-impacting changes, verify manually with `wails dev`: scan directory, tag CRUD, search/filter, delete flow.
- Add regression tests for bug fixes where possible.

## Commit & Pull Request Guidelines
- No existing commit history is available; adopt Conventional Commits (e.g., `feat: add directory alias validation`).
- Keep commits focused and atomic; avoid mixing frontend/backend refactors in one commit.
- PRs should include: summary, affected paths, manual test steps, and screenshots/GIFs for UI changes.
- Link related issues and note any database/schema or config impact explicitly.

## Security & Configuration Tips
- Do not commit local database files, logs, or OS artifacts (`.video-master`, `.DS_Store`).
- Review destructive operations (`DeleteVideo(..., deleteFile=true)`) carefully in code review.
