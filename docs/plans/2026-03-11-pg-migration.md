# SQLite → Postgres Migration Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 完全切换到 Postgres，并提供 Go CLI 迁移脚本（含软删除记录）。

**Architecture:** 运行时 DB 仅使用 Postgres；迁移 CLI 从 SQLite 读取全量数据写入 PG，完成后重置序列。

**Tech Stack:** Go 1.23+, GORM, Postgres

---

### Task 1: 修复 `go test ./...` 基线失败（缺少 frontend/dist）

**Files:**
- Create: `frontend/dist/.keep`
- Modify: `.gitignore`
- Test: `database` 相关测试保持不变

**Step 1: Write the failing test**

说明：当前 `go test ./...` 直接失败（`go:embed` 找不到 `frontend/dist`），将其作为 RED。

Run: `go test ./...`
Expected: FAIL with `pattern all:frontend/dist: no matching files found`

**Step 2: Write minimal implementation**

- 新增 `frontend/dist/.keep`，保证 embed 有匹配文件。
- 更新 `.gitignore` 允许跟踪 `.keep`（若已忽略 `frontend/dist`）。

**Step 3: Run test to verify it passes**

Run: `go test ./...`
Expected: PASS

**Step 4: Commit**

```bash
git add frontend/dist/.keep .gitignore
git commit -m "fix: 补齐 frontend dist 以通过 go test"
```

### Task 2: 运行时数据库切换到 Postgres

**Files:**
- Modify: `database/database.go`
- Modify: `go.mod` / `go.sum`

**Step 1: Write the failing test**

```go
func TestInitUsesPostgresEnv(t *testing.T) {
  t.Setenv("PG_HOST", "127.0.0.1")
  t.Setenv("PG_PORT", "5432")
  t.Setenv("PG_USER", "user")
  t.Setenv("PG_PASSWORD", "pass")
  t.Setenv("PG_DB", "db")
  t.Setenv("PG_SSLMODE", "disable")
  if err := Init(); err == nil {
    t.Fatalf("expected error when PG is unreachable")
  }
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./database -run TestInitUsesPostgresEnv`
Expected: FAIL (Init 仍使用 SQLite)

**Step 3: Write minimal implementation**

- 使用 `gorm.io/driver/postgres`。
- 从 `.env` 读取 PG 连接配置（优先环境变量）。
- 替换 SQLite 特有 SQL（`INSERT OR IGNORE` → `ON CONFLICT DO NOTHING`）。

**Step 4: Run test to verify it passes**

Run: `go test ./database -run TestInitUsesPostgresEnv`
Expected: PASS

**Step 5: Commit**

```bash
git add database/database.go go.mod go.sum
git commit -m "feat: 运行时数据库切换到 postgres"
```

### Task 3: 迁移 CLI（SQLite → Postgres）

**Files:**
- Create: `cmd/migrate_sqlite_to_pg/main.go`
- Modify: `go.mod` / `go.sum`
- Test: `cmd/migrate_sqlite_to_pg/main_test.go`

**Step 1: Write the failing test**

```go
func TestMigrateSqliteToPg(t *testing.T) {
  // 使用临时 SQLite 填充数据
  // 使用本地 PG 连接（或跳过当未配置）
  // 验证 tags/videos/settings/scan_directories/video_tags 数量一致
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./cmd/migrate_sqlite_to_pg -run TestMigrateSqliteToPg`
Expected: FAIL (未实现迁移)

**Step 3: Write minimal implementation**

- 读取 SQLite 路径 `~/.video-master/video-master.db`。
- 读取 `.env` 配置并连接 PG。
- AutoMigrate + 全量迁移（含软删除）。
- 重置序列：`SELECT setval(pg_get_serial_sequence('videos','id'), max(id))` 等。

**Step 4: Run test to verify it passes**

Run: `go test ./cmd/migrate_sqlite_to_pg -run TestMigrateSqliteToPg`
Expected: PASS

**Step 5: Commit**

```bash
git add cmd/migrate_sqlite_to_pg/main.go cmd/migrate_sqlite_to_pg/main_test.go go.mod go.sum
git commit -m "feat: 添加 sqlite 到 postgres 迁移 CLI"
```

### Task 4: 文档与验证

**Files:**
- Modify: `README.md` or `GUIDE.md`

**Step 1: Update docs**
- 添加 `.env` 配置示例与迁移 CLI 使用说明

**Step 2: Verify**

Run: `go test ./...`
Expected: PASS

Run: `wails build`
Expected: PASS

**Step 3: Commit**

```bash
git add README.md GUIDE.md
git commit -m "docs: 更新 postgres 配置与迁移说明"
```

