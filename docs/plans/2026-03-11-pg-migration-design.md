# SQLite → Postgres 迁移设计说明

> **目标**：完全切换到 Postgres 运行，并提供 Go CLI 迁移脚本（含软删除记录）。

## 目标与范围
- 运行时数据库从 SQLite 完全切换到 Postgres（不保留切换开关）。
- 迁移脚本以 Go CLI 形式提供，读取 SQLite 全量数据并写入 Postgres。
- 迁移包含软删除记录（`deleted_at` 非空数据）。

## 配置与连接
- Postgres 连接来自 `.env`，字段：
  - `PG_HOST`, `PG_PORT`, `PG_USER`, `PG_PASSWORD`, `PG_DB`, `PG_SSLMODE`, `PG_TIMEZONE`（可选）。

## 运行时数据库初始化
- `database.Init()` 使用 `gorm.io/driver/postgres`。
- 保持 `AutoMigrate`，修正 SQLite 特有 SQL。
- 主要差异：
  - `INSERT OR IGNORE` → `ON CONFLICT DO NOTHING`。

## 迁移 CLI（Go）
- 新增 `cmd/migrate_sqlite_to_pg`：
  - 读取 SQLite：`~/.video-master/video-master.db`。
  - 读取 `.env` Postgres 配置。
  - 先在 PG 侧 `AutoMigrate` 建表。
  - 顺序迁移：`settings` → `tags` → `videos` → `scan_directories` → `video_tags`。
  - 迁移完成后重置序列（`setval`），避免 ID 冲突。
  - 全量迁移，幂等只保证“空库首次迁移”，二次迁移需人工处理。

## 兼容性与风险
- 部分索引在 PG 仍可用（部分索引 `WHERE deleted_at IS NULL`）。
- 迁移后应验证记录数与关键字段一致。

## 预验证
- 当前 `go test ./...` 会因缺少 `frontend/dist` 失败，需要在计划中修复基线。

