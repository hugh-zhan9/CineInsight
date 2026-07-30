# 视频详情与结构化媒体信息

## Source And Goal

- Source: `docs/loopx/design/2026-07-30-video-details-metadata/需求设计文档.md`
- Goal: 在现有 Go/Wails/Vue 片库中交付单页视频详情、人物与作品集实体、只读本地技术快照、评分查询和旧片库显式补全，并保持现有文件、播放、字幕、标签、迁移和回收站行为兼容。

## Boundaries And Global Constraints

- 数据只来自本地文件、现有数据库和用户输入；不加入在线影视资料、第三方 API 或新凭证。
- 不加入导演、角色、生日、人物简介、季集、人物自动合并/显式删除或技术字段手工编辑。
- 详情继续使用右侧单页抽屉，不引入独立全页或标签页。
- 新 schema 和接口纯增量；旧 Wails 方法、默认片库排序和既有数据库记录保持兼容。
- 技术探测失败不得阻止视频导入或清空最后成功快照；启动不得隐式全库回填。
- 视频软删除保留人物和作品集关系；托管头像/封面不得暴露或依赖原始外部路径。
- 所有代码切片按仓库工作协议先写会失败的行为测试，再实现并运行新鲜验证；不提交、推送或丢弃用户工作。

## Execution Slices

### P-001: 建立兼容的数据模型与关键约束

- Outcome: 新增视频详情字段、人物、作品集、关系、技术快照和媒体流模型；旧数据库无 DML 回填即可启动，关键 CHECK/FK/唯一索引可靠创建。
- Depends on: none
- Write scope: `models/**`, `database/**`, related Go tests
- Source anchors: AC-002, AC-004, AC-005, AC-008, AC-011, AC-014, AC-015; D-002, D-003, D-005, D-006, D-009, D-010, D-013, D-020; TC-002, TC-004, TC-008, TC-014, TC-015
- Acceptance: nullable 评分区分 NULL/0 并受半分 CHECK；人物同名可写；作品集活跃规范化名称唯一；关系在视频软删除后保留；旧记录使用安全空值。
- Verification: `go test ./database ./models/...`（无独立 models 包测试时运行 `go test ./database ./...` 的相关定向用例）；`go test ./...`; `git diff --check`
- Review focus: 约束必须由数据库最终裁决；不得通过迁移删除、重写或隐式回填旧数据。

### P-002: 交付本地技术探测与最后成功快照

- Outcome: `ffprobe` 解析文件/视频流/音轨/内封字幕并事务替换快照；探测失败、取消或源文件变化时保留旧快照；详情读取不触发探测。
- Depends on: P-001
- Write scope: `services/media_probe*`, `services/video_service.go`, `models/**`, focused tests and local fixtures
- Source anchors: AC-011, AC-012, AC-016; D-013, D-014, D-015, D-016, D-021; TC-011, TC-012, TC-016
- Acceptance: 多流和缺失字段按契约解析；HDR 不猜测；前后 stat 变化丢弃；成功同步现有基础元数据；失败只更新尝试状态；无网络依赖。
- Verification: focused media probe tests; `go test ./services`; `go test -race ./services`; `go vet ./...`; `git diff --check`
- Review focus: 子进程 context/输出上限、事务整组替换、旧快照保留和路径安全。

### P-003: 交付人物实体、同名处理和头像托管

- Outcome: 人物可搜索、创建、编辑、关联和浏览；同名人物允许明确新建；头像由应用托管并通过实体 ID 路由读取；显式移除最后关系自动清理人物。
- Depends on: P-001
- Write scope: `services/person*`, `services/managed_image*`, `app.go`, `preview_asset_handler.go`, `models/**`, focused tests
- Source anchors: AC-005, AC-006, AC-007, AC-014, AC-016; D-006, D-007, D-008, D-019, D-020, D-021; TC-005, TC-006, TC-007, TC-016
- Acceptance: 姓名不唯一；候选搜索可区分同名；关系无角色/顺序；软删除不清理人物；头像导入/替换/移除及失败补偿符合契约；资源路由不可越界。
- Verification: focused person/managed-image/asset-handler tests; `go test ./services ./...`; `go test -race ./services`; `git diff --check`
- Review focus: DB/文件系统补偿、最后关系清理竞态、绝对路径泄漏和任意文件读取。

### P-004: 交付作品集实体、成员排序和封面托管

- Outcome: 作品集支持唯一名称、简介、封面、多重视频归属、成员增删和稳定重排；删除作品集不删除视频；软删除视频恢复原成员槽位。
- Depends on: P-001, P-003
- Write scope: `services/collection*`, shared managed image code, `app.go`, `preview_asset_handler.go`, `models/**`, focused tests
- Source anchors: AC-008, AC-009, AC-014, AC-016; D-009, D-010, D-011, D-012, D-019, D-020, D-021; TC-008, TC-009, TC-014, TC-016
- Acceptance: 规范化活跃名称冲突可靠；视频可属于多个作品集；完整活跃成员重排行锁校验；隐藏成员保槽；删除仅影响集合/关系/封面。
- Verification: focused collection concurrency/order/asset tests; `go test ./services ./...`; `go test -race ./services`; `git diff --check`
- Review focus: 重排并发、隐藏成员顺序、删除边界和封面补偿。

### P-005: 交付聚合详情、评分查询与三类稳定分页接口

- Outcome: Wails 服务可读取/更新聚合视频详情；主片库使用显示标题 fallback、扩展搜索、评分筛选/排序/保存视图；人物和作品集列表及详情稳定分页；旧查询接口保留。
- Depends on: P-002, P-003, P-004
- Write scope: `services/video_detail*`, `services/library_service.go`, `models/**`, `app.go`, `app_test.go`, focused tests
- Source anchors: AC-001, AC-002, AC-003, AC-004, AC-010, AC-015; D-001, D-002, D-003, D-004, D-005; TC-001, TC-002, TC-003, TC-004, TC-010, TC-015
- Acceptance: 更新详情事务原子；标题不改文件；搜索覆盖四字段；评分 NULL 段键集分页无重复/漏项；保存视图恢复评分；人物/作品集分页确定；旧默认排序不变。
- Verification: focused detail/library pagination tests; `go test ./...`; `go vet ./...`; `git diff --check`
- Review focus: NULL 游标、搜索转义、关系集合覆盖语义和旧 API 兼容。

### P-006: 交付显式、可取消、可续跑的技术补全

- Outcome: 单 worker 后台任务只处理缺失/过期技术快照，提供状态事件、轮询和取消；成功逐项持久化，重启后再次运行跳过完成项；应用启动不自动触发。
- Depends on: P-002
- Write scope: `services/technical_backfill*`, `app.go`, app/service tests
- Source anchors: AC-013, AC-016; D-017, D-018, D-021; TC-013, TC-016
- Acceptance: 空任务、重复 Start、单项失败、取消、运行中删除视频和进程重启后的数据续跑均有确定结果；失败摘要有上限。
- Verification: focused backfill state/race/cancel tests; `go test ./services`; `go test -race ./services`; `go test ./...`; `git diff --check`
- Review focus: goroutine 所有权、取消传播、状态竞态、隐式启动禁令。

### P-007: 交付单页详情抽屉和人物/作品集片库视图

- Outcome: Vue 主片库具有视频/人物/作品集视图；视频详情抽屉连续展示预览、描述、评分、人物、作品集和技术信息；人物/作品集详情支持返回导航和规定编辑动作；技术补全可操作。
- Depends on: P-005, P-006
- Write scope: `frontend/src/**`, `frontend/scripts/**`, generated `frontend/wailsjs/**`, frontend package metadata if test tooling requires
- Source anchors: AC-001 through AC-013, AC-016; D-001, D-004, D-007, D-008, D-011, D-012, D-016, D-018, D-019, D-021; TC-001 through TC-013, TC-016
- Acceptance: 无标签页；列表筛选/滚动和播放器时序保持；人物候选有复用/明确新建分支；作品集可拖拽；技术字段只读；错误/空态/取消均可见；关键测试不是源码正则断言。
- Verification: `npm test`; component behavior tests for drawer/entity/rating/reorder/backfill; `npm run build`; regenerated Wails bindings compile; manual Wails visual smoke; `git diff --check`
- Review focus: Vue 状态同步、抽屉资源释放、可访问性、拖拽失败回滚和避免巨型单组件继续膨胀。

### P-008: 集成回收站、扫描、文档与最终兼容验证

- Outcome: 新视频导入和文件变化更新技术快照但失败不阻止原流程；软删除/恢复完整保留详情关系；README/GUIDE/AI-CONTEXT 反映已实现行为；所有生成物和完整验证闭环。
- Depends on: P-005, P-006, P-007
- Write scope: `services/video_service.go`, related integration tests, `README.md`, `GUIDE.md`, `AI-CONTEXT.md`, Wails generated bindings, package/build metadata
- Source anchors: AC-012, AC-014, AC-015, AC-016; D-015, D-018, D-020, D-021; TC-012, TC-014, TC-015, TC-016
- Acceptance: 导入/刷新/扫描行为符合探测失败隔离；回收恢复人物与多作品集顺序；旧数据库迁移和旧接口回归；无在线依赖；文档无超前声明。
- Verification: `go test ./...`; `go test -race ./services`; `go vet ./...`; `npm test`; `npm run build`; Wails production build/package smoke; `git diff --check`
- Review focus: 精确 diff 的兼容、安全和数据生命周期；Critical/Important 发现修复后重新全量验证。

## Integration And Final Verification

- 先运行并记录代码变更前基线：`go test ./...`、`npm test`、`npm run build`；环境依赖失败必须与代码失败区分。
- 验证 AC-001..AC-016、D-001..D-021、TC-001..TC-016 全部由对应切片或最终集成覆盖，无 deferred 项。
- 对 PostgreSQL 评分排序、人物搜索、作品集列表和详情聚合查询执行代表性 `EXPLAIN ANALYZE`；只报告实际结果，不虚构 SLO。
- 运行 `go test ./...`、`go test -race ./services`、`go vet ./...`、`npm test`、`npm run build`、Wails 构建/打包 smoke、`git diff --check`。
- 对最终精确 diff 做独立兼容与安全审查；修复 Critical/Important 发现后重复受影响测试和完整套件。
- 手工 smoke：有/无标题详情、评分 0/NULL、同名人物、头像/封面、作品集拖拽、软删除恢复、多流技术信息、补全取消，以及无网络运行。

## Handoff And Residual Risks

- Status: ready
- Blockers: none
- Residual risks: 实际容器的 ffprobe 字段差异、PostgreSQL 代表性数据量下的评分分页计划、Wails 原生文件选择器自动化边界、托管文件清理失败后的无引用文件，需要在执行和最终审查中用新鲜证据收口。
- Resume note: 从 P-001 开始；每个切片验证通过后再进入依赖切片。源设计中的 D-* 和 intake 中的 AC/TC 不得在执行计划内重新解释。

## Execution rules for the consuming agent

- Execute slices in dependency order; verify each slice with its exact commands before starting dependents.
- Two slices may run in parallel only when neither depends on the other and their write scopes are disjoint; integrate results sequentially.
- Follow the installed working agreement for verification, review, stop, and Git discipline throughout.
