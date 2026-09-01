---
source: docs/loopx/design/2026-09-01-sqlite-backend-and-switching/需求设计文档.md
status: done
slices:
  - id: P-001
    status: done
    depends: []
  - id: P-002
    status: done
    depends: [P-001]
  - id: P-003
    status: done
    depends: [P-002]
  - id: P-004
    status: done
    depends: [P-002]
  - id: P-005
    status: done
    depends: [P-002]
  - id: P-006
    status: done
    depends: [P-002]
  - id: P-007
    status: done
    depends: [P-006]
  - id: P-008
    status: done
    depends: [P-002]
---

# SQLite 后端与双后端切换

## Goal And Boundaries

按 `docs/loopx/design/2026-09-01-sqlite-backend-and-switching/需求设计文档.md` 实现：
应用可以在 SQLite 或 PostgreSQL 上运行，两者之间可以带数据切换。设计已经 settle 的
结论不再重新讨论，其中三条在实施期必须一直守住：

1. **现有安装升级后行为完全不变**（D-001）。`DB_BACKEND` 未设且 `PG_HOST` 还在 →
   仍走 Postgres。这是整个改动里最硬的兼容性约束。
2. **迁移是复制不是移动**（D-006）。源库全程只读、完整保留，这是「改回配置重启即可
   回滚」能成立的唯一理由。
3. **方言差异在 Go 侧消解，不写两套 SQL 分支**（D-002）。仓库既有惯例，理由是测试
   只跑得到 SQLite 那套，PG 分支会无覆盖地腐化。

**非目标。** 不做同时连两个库或读写分离；不在 SQLite 上实现语义检索；不扩展
`cmd/migrate_sqlite_to_pg`；不做运行时热换句柄；不改动任何业务语义（随机加权、清理
判定、AI 打标闭集、软删除与回收站、文件迁移安全边界原样保留）。

**执行顺序上的一条硬约束。** P-002（双后端测试基座）必须在任何会影响两个后端共享
代码的切片之前完成。当前 Postgres 分支零测试覆盖，没有这层安全网，后续每一处改动
都是在赌「没碰坏 PG」。P-001 排在它前面只是因为基座要复用后端判定逻辑。

## P-001 双后端连接层与后端判定

`database.Init()` 从写死 `postgres.Open` 改成按配置选驱动。新增 `DB_BACKEND`
（`sqlite` / `postgres`）与 `SQLITE_PATH` 两个配置项，未设置时按 D-001 自动判定：
显式配置优先，其次看 `PG_HOST` 是否存在，都没有则用 SQLite。

后端判定必须抽成一个独立的纯函数，不依赖真实连接，这样四种配置组合可以直接断言。
非法 `DB_BACKEND` 取值直接启动失败并报出取值，不静默回退——回退会让用户以为连上了
实际是另一个库。SQLite 默认路径用新文件名 `~/.video-master/library.db`，不复用历史的
`video-master.db`，避免静默采纳一份很久以前的陈旧库。

`AutoMigrate`、各 `ensure*Indexes`、默认设置初始化这些后续步骤两后端共用，不分叉。

完成的标志是：设了 `PG_HOST` 不设 `DB_BACKEND` 判定为 postgres；都不设判定为 sqlite；
`DB_BACKEND=sqlite` 时即使有 `PG_HOST` 也走 sqlite；非法取值报错。

> writes: `database/database.go`, `database/database_test.go`, `.env.example`
> anchors: D-001；设计 4.1
> verify: `go test ./database/...`；`go build ./...`
> review: 后端判定是兼容性契约，需独立核对「现有安装升级后仍连 Postgres」这条在四种配置组合下都成立

## P-002 双后端测试基座

把 8 个测试文件里 24 处 `sqlite.Open(...)` 收敛成一个共用 helper，并让它能按环境变量
在 SQLite 与 PostgreSQL 之间切换。这是本计划最重要的一片：设计文档明确指出当前
Postgres 分支零测试覆盖，加了第二个后端之后「改 A 坏 B」才成为真实风险。

helper 放在一个可以被 `services` 与 `database` 两个包的测试同时导入的位置。默认
SQLite（保持现有行为与速度），设置了 Postgres 测试连接串时切到 PG。PG 下每个测试需要
自己的隔离空间——每次 `Open` 建一个独立 schema 并在 `t.Cleanup` 里删掉，避免测试之间
相互看见数据，也避免为每个测试建库的开销。

完成的标志是：不设环境变量时整套测试行为与现在完全一致（同样的用例数、同样的耗时量级）；
设了 PG 连接串时同一套测试能在 Postgres 上跑完；未配置 PG 时 PG 相关断言被跳过而不是失败。

> writes: `internal/dbtest/**`（新增）, `services/*_test.go`, `database/*_test.go`, `app_test.go`
> anchors: 设计 11.3 第 1 条（最重要的验证投入）
> verify: `go test ./...`（默认 SQLite，用例数不减）；配置 PG 连接串后再跑一次 `go test ./...`
> review: 这一片改动面覆盖全部测试文件，需独立核对没有测试在切换后变成静默跳过

## P-003 热力图方言消解

`LibraryStatsService.GetStats()` 里的观看热力图分组当前用
`CAST(last_played_at AS DATE)`，在 SQLite 上返回 `2026` 而不是 `2026-09-01`——一整年的
播放会塞进一个格子，且不报错。改成只投影 `last_played_at` 一列、在 Go 侧按 `time.Local`
归并成 `YYYY-MM-DD`，与 `ListImageTimelineBuckets` 同一套做法。

这不只是可移植性：它顺带消除了 Postgres 侧 `timestamptz` 按会话时区取值与前端按本地
日期渲染坐标轴之间的潜在错位。

完成的标志是：同一批数据在两个后端产出完全相同的按日分组。这条一致性测试必须在两个
后端上都跑——只在 SQLite 上跑恰好会掩盖本次要修的问题。

> writes: `services/library_stats_service.go`, `services/library_stats_service_test.go`
> anchors: D-002；设计 4.2
> verify: `go test ./services/ -run LibraryStats`（双后端各跑一次）
> review: 归并口径必须与前端 `heatmapDays` 的本地日期坐标轴一致，否则热力图会整体错位一天

## P-004 行锁语义验证

21 处 `clause.Locking{Strength: "UPDATE"}` 在 SQLite 上被静默忽略。设计的结论是这安全
——SQLite 写事务在库级互斥，保证强于行级锁。本片不改任何生产代码，只把这个结论用测试
钉住：并发跑同一条 upsert 路径（短视频互动计数是最合适的靶子，它是典型的读-改-写），
断言最终计数正确、无丢更新。

只写文档不写测试是不够的：后人看到 SQLite 上锁不生效，会以为是遗漏而去"修"它。

> writes: `services/short_feed_service_test.go`
> anchors: D-003；设计 4.3
> verify: `go test ./services/ -run Concurren -race`（双后端各跑一次）

## P-005 SQLite 备份分支

备份服务当前整个建在 `pg_dump` / `pg_restore` 上。加一条 SQLite 分支：备份用
`VACUUM INTO` 写单文件一致性快照，不需要停写；恢复是替换库文件后提示重启。

复用现有的备份目录、保留份数、定时间隔与恢复确认 UI，不新建一套配置。跨后端误恢复
必须直接拒绝并说明（PG 快照是 pg_dump 自定义格式，SQLite 快照是库文件），不能尝试。

完成的标志是：SQLite 下备份产出可用快照并按保留份数轮转；用 PG 快照在 SQLite 模式下
恢复被拒绝且库文件未被改动；现有 `backup_service_test.go` 全绿（Postgres 路径无回归）。

> writes: `services/backup_service.go`, `services/backup_service_test.go`
> anchors: D-005；设计 4.6
> verify: `go test ./services/ -run Backup`
> review: 备份/恢复直接操作用户数据，需独立核对跨后端拒绝与恢复失败时原库完好

## P-006 通用双向迁移器

新增按 `models.AllModels()`（36 张）驱动的全量迁移器，另加 `video_tags`、`image_tags`
两张隐式多对多关联表（`VideoPerson` / `CollectionVideo` 是显式模型，已在 `AllModels()` 中）。
不扩展 `cmd/migrate_sqlite_to_pg`——它只覆盖 7 类实体，是 schema 还很小时的历史工具。

四条实现要点：**保留主键**（否则所有外键与关联行失效）；**Postgres 作为目标时逐表重置
序列**（显式带 ID 插入不推进 BIGSERIAL，迁移后第一次新建就主键冲突）；**软删除行一并
迁移**（回收站与"否认后不得重复确认"都依赖它存在）；**pgvector 向量不跨后端携带**
（PG→SQLite 丢弃向量并清空语义索引元数据，让 UI 报"需要重建"而不是声称有索引却搜不出）。

迁移是复制，源库全程只读。每张表一个事务，全部完成后写入完成标记；目标库存在但无完成
标记即视为半迁移状态，下次必须显式清空才能重试。

完成的标志是：SQLite → PG → SQLite 往返后逐表行数与主键集合一致、软删除行保留、源库
前后不变；迁移到 PG 后立即新建记录不冲突；目标非空时拒绝迁移。

> writes: `database/migrator/**`（新增）, `database/migrator/*_test.go`
> anchors: D-006、D-008；设计 4.5
> verify: `go test ./database/...`（含往返测试与序列重置测试）
> review: 迁移搬的是用户全部数据，需独立核对源库只读、ID 保持、序列重置与半迁移状态的拒绝逻辑

## P-007 切换入口与迁移进度

设置页新增「数据库」分区：显示当前后端与库位置、语义检索是否可用；提供切换目标后端的
入口，走「预检 → 迁移 → 写配置 → 提示重启」四步。预检要报出目标是否可连接、是否为空。
迁移进度复用既有的 Wails 事件通道模式（与 `technical-backfill-state` 一致），并提供轮询
兜底。

切换需要重启这件事必须说清楚：配置已写但句柄未换，未重启前仍在用旧后端。确认切换时还要
告知回滚代价——切换后在新后端产生的改动不会回到旧库。

> writes: `app.go`, `frontend/wailsjs/**`, `frontend/src/components/SettingsPage.vue`, `frontend/src/components/SettingsPage.test.js`
> anchors: D-007；设计 3.3、七
> verify: `go build ./...`；`cd frontend && npm test`；`npx vite build`

## P-008 语义检索降级的前端确认

`PrepareSemanticVectorStorage` 已经在按方言降级并返回 `pgvector_requires_postgres`，
本片不新增降级逻辑，只确认两处前端提示在 SQLite 模式下确实被触发：视频库的
`semanticSearchErrorText`、图片库的 `semanticNotice` 与 `semanticAvailable`。

验收标准是「入口置灰并说明原因」，不是「搜索返回空结果」——后者会让用户以为库里没有
匹配内容，而不是这个能力当前不可用。

> writes: `frontend/src/components/VideoListPage.vue`, `frontend/src/components/PhotoLibraryPage.vue`, 对应测试
> anchors: D-004；设计 4.4
> verify: `cd frontend && npm test`

## Integration And Final Verification

- 全量套件：`go test ./...` 在 SQLite 与 PostgreSQL 两个后端各跑一次全绿；
  `cd frontend && npm test`；`npx vite build`。
- 兼容性回归：设了 `PG_HOST` 不设 `DB_BACKEND` 的配置下启动，确认连的是原 Postgres 库、
  数据完整、语义检索仍可用。
- 往返回归：SQLite → PG → SQLite 全量迁移后，抽查视频/图片/标签/回收站/同源关系的行数与
  内容一致。
- 后端绑定变更后重新 `wails generate module`，确认 `frontend/wailsjs` 差异只含新增项。

## Handoff And Residual Risks

- Blockers: 无。
- Residual risks:
  - 设计假定除热力图外不存在其他会产出错误数据的方言差异。这是基于 60 处裸 SQL 的扫描与
    五项实测得出的，但不是穷尽证明。P-002 的双后端测试是发现漏网之鱼的主要手段；若发现
    新的一处，属于设计假设被证伪，须回 `spec` 修订而不是就地打补丁。
  - PG 下的 schema 隔离测试基座会显著拉长 PG 侧测试耗时，可能需要在本地按需开启而不是
    每次都跑。
  - 迁移大库（数千视频 + 数万图片）的实际耗时只能在真机上测，单元测试给不出量级。
- Resume note: 从 frontmatter 第一个非 `done` 的切片继续。P-002 未完成前不要开始 P-003
  及之后任何一片——那些改动都会同时影响两个后端，没有双后端测试就没有安全网。
