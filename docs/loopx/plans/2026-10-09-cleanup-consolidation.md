---
schema: loopx-plan/v1
source: docs/loopx/design/2026-09-29-product-completeness/概要设计.md#D-PC48-CONSOLIDATE
status: complete
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
    depends: [P-003]
---

# 视频清理集中整理

## Goal And Boundaries

用户已接受“先确定保留版本，再集中到目标目录”，并授权实施。交付完整的相关组选择、只读路径与附件预览、后台迁移、取消/重启对账、任务中心回看，以及移动完成后的独立清理确认。原始需求和边界以 [D-PC48-CONSOLIDATE](../design/2026-09-29-product-completeness/概要设计.md#D-PC48-CONSOLIDATE) 及[实施技术合同](../design/2026-09-29-product-completeness/需求设计文档.md#cleanup-consolidation-contract) 为准，不从计划省略或改写验收。

维护原有精确重复目录推荐、各类别保留限制、元数据合并、跨组锁定、回收站和人工确认。文件执行仍由 VideoService 的迁移模块拥有；CleanupService 只拥有预览及持久任务编排。用户真实图库不参与开发测试；开发阶段不提交、推送或安装应用。完成后用户另行明确授权本轮提交并push；安装仍不在范围内。持久计划用于跨模型/文件执行/应用接线/UI 阶段的恢复及独立审阅协调。

## P-001 可核对的整理预览与任务数据合同

从当前有效分析验证选定的组、版本和手动保留项，返回建议目标、确切视频及附件路径、冲突新名、共享附件复制策略、容量/扫描根信息。仅对未手动锁定的精确重复选择目标目录的等价项，其他类别保持已选版本，截取保留完整片。请求携带全部分析组的保留/本组不删保护，核对后持久化；同一保留项跨组去重并拒绝冲突；空/单组/大批输入有明确结果。预览只读，启动令牌由服务拥有，不能由客户端修改清单。

任务模型和私有 JSON DTO 固化技术合同，进入 SQLite/PG 通用 AllModels；任务不与媒体级联删除。附属文件及目标预检是迁移模块的可复用能力，不在前端复制规则。以真实临时文件覆盖同名/语言后缀字幕、同名 NFO、共享附件、忽略/失效快照、目标被替换及目录建议边界。

> writes: `models/cleanup_consolidation.go`, `models/schema.go`, `models/cleanup_consolidation_test.go`, `database/cleanup_consolidation_schema_test.go`, `database/migrator/migrator_test.go`（仅新模型造数确有需要时）, `services/cleanup_consolidation_types.go`, `services/cleanup_consolidation_preview.go`, `services/cleanup_consolidation_preview_test.go`, `services/file_migration_plan.go`, `services/file_migration_plan_test.go`, `services/cleanup_service.go`（仅新增编排字段及初始化，不改既有分析）
> anchors: D-PC48-CONSOLIDATE；技术合同请求/预览/持久任务/附件；D-PC48-EXACT-DIR 与 D-PC48/49 保护行为
> architecture: CleanupService 复用当前分析、忽略决定和整理分；VideoService 迁移模块持有附件/路径/身份预检；模型经 AllModels 接入既有双后端；没有新迁移服务或客户端状态源；检查预览零文件变更和 schema 往返。
> verify: `go test ./models ./database ./database/migrator -count=1 -timeout 300s`; `go test ./services -run 'Test(CleanupConsolidationPreview|ConsolidationPreview|FileMigrationPlan|CleanupExact|CleanupIMG03)' -count=1 -timeout 300s`; edited Go gofmt; diff check
> review: 独立核对确认清单完整性、保留项约束、附件归属及预览不会授权任意路径移动。

## P-002 可取消、可对账的单项迁移和持久编排

实现 PreviewID 幂等启动、唯一活动任务、每项持久日志与状态 CAS；执行及恢复由按PreviewID定位的同一内核文件锁互斥，启动先拿锁再插任务、恢复拿锁后重读、所有文件收尾结束才释放ActiveSlot，OwnerScope 不符时禁止恢复接管；复制/摘要在路径锁外，短锁内复查快照、排他发布及库内路径切换。视频 ID 与关系保持，附属文件随同名规则处理，共享附件留源；跨盘源按既有迁移残留管理。迁移与既有字幕锁协调，真实库字幕写入器校验当前关联路径以阻止迟到写回；进度节流，取消不继续下一项，部分完成如实返回；启动不会执行清理。

重启仅对账，使用文件身份、摘要和数据库确定已完成项，清理/恢复只触碰被证明属于本任务的临时产物。不能确认的位置全部保留并报告。准备、复制、发布、数据库切换等阶段故障注入可重复验证；完成任务按原确认保留关系回读本次清理内容，外部改动/忽略变化/跨组保护冲突时拒绝继续旧计划。旧 MoveVideo/MoveDirectory 行为保持，公共迁移代码改动须验证原调用方。

> writes: `models/cleanup_consolidation.go`, `models/schema.go`, `models/cleanup_consolidation_test.go`, `database/cleanup_consolidation_schema_test.go`, `database/migrator/migrator_test.go`（仅新计划/明细模型造数确有需要时）, `services/cleanup_consolidation_preview_test.go`（仅移除尚未发布的task内大JSON字段夹具）, `services/cleanup_consolidation_types.go`, `services/cleanup_service.go`, `services/cleanup_consolidation_run.go`, `services/cleanup_consolidation_persistence.go`, `services/cleanup_consolidation_persistence_test.go`（三表读写私有函数，仍属CleanupService，非独立store）, `services/cleanup_consolidation_recovery.go`, `services/cleanup_consolidation_review.go`, `services/cleanup_consolidation_run_test.go`, `services/cleanup_consolidation_recovery_test.go`, `services/cleanup_consolidation_review_test.go`, `services/file_migration_consolidation*.go`, `services/file_migration_plan.go` / `services/file_migration_plan_test.go`（仅执行前复验确有需要时复用扩展） , `services/file_migration.go`, `services/file_migration_io.go`, `services/file_migration_test.go`, `services/background_task_registry.go`, `services/background_task_registry_test.go`, `services/cleanup_consolidation_lock*.go`, `services/subtitle_file_lock.go`, `services/subtitle_file_writer.go`, `services/subtitle_file_writer_test.go`, `services/subtitle_service.go`（仅写入器构造）, `services/subtitle_workbench.go`（仅写入器构造）, `services/enhancement_pipeline.go`（仅写入器构造）, `services/cleanup_consolidation_subtitle_test.go`, `services/subtitle_translation_test.go`（仅两个已有finalize夹具使target与真实DB视频同名SRT一致，保留原翻译断言）
> anchors: D-PC48-CONSOLIDATE；技术合同启动/状态/日志/文件执行/恢复/清理衔接；D-PC05 迁移残留、D-PC13 字幕、D-PC49 锁定保护
> architecture: 编排在 CleanupService，文件及附件变更仅进入 VideoService 迁移模块；复用 stableFileIdentity、维护围栏/路径锁、字幕索引、MigrationStagedSource、BackgroundTaskRegistry；三表任务记录为同一状态源（标量头/不可变计划/逐项日志），版本条件更新，无事务内大 I/O；源码检查和慢复制读并发测试验证边界。
> verify: `go test ./models ./database ./database/migrator -count=1 -timeout 300s`（默认及一次性PG）；`go test ./services -count=1 -timeout 1500s`; `go test -race ./services -run 'Test(CleanupConsolidation|Consolidation|FileMigrationConsolidation)' -count=1 -timeout 600s`; 有效定向变异覆盖新计算/失败分支；gofmt 与 diff check
> review: 文件覆盖/取消误删、长锁、重复执行、发布中断、源版本变化、任务日志与库路径一致性均须独立审阅；Critical/Important 修复后复审。

## P-003 应用桥接、生命周期与任务中心后端

App 注入已有 VideoService，启动时恢复对账，关闭/维护生命周期取消并等待本任务受控结束。五个已约定绑定只暴露服务能力；后台事件与任务中心能显示进度、结果和中断状态。移动完成标记分析过期，完成任务可请求本次选定组的清理审阅，不重新选择保留项。生成 Wails 绑定供后续界面使用。

> writes: `app.go`, `app_cleanup_consolidation.go`, `app_cleanup_consolidation_test.go`, `app_cleanup.go`（仅元数据合并字幕写入器构造） , `app_task_center.go`, `app_task_center_test.go`, `app_tasks.go`, `app_tasks_test.go`, `app_quit.go`, `app_quit_test.go`, `app_settings.go`（仅维护生命周期接入，如确有需要）, `frontend/wailsjs/go/main/App.js`, `frontend/wailsjs/go/main/App.d.ts`, `frontend/wailsjs/go/models.ts`
> anchors: D-PC48-CONSOLIDATE；技术合同 App/API、任务中心、恢复、独立清理确认；既有数据库维护围栏
> architecture: App 仅注入/桥接/聚合，持久任务仍在服务；所有状态取已有服务，TaskCenter 不建立状态源；生成绑定禁止手改；检查五个新 API 最终均有 UI 调用。
> verify: `go test . -count=1 -timeout 300s`; `GOTOOLCHAIN=go1.24.9 wails generate module`; `go vet ./...`; 新绑定使用守卫在 P-004 集成验证
> review: 独立核对启动恢复不重跑/不删除、维护与退出期间任务终止顺序及桥接不绕过服务快照验证。

## P-004 集中整理审阅、任务回看与清理衔接

清理页对允许类别提供独立整理范围选择，保留项仍遵守各类别规则；用户能选择推荐或指定目标，分页查看视频/附件落点、同名处理、复制字节量、未处理附件及阻塞原因。扫描根外沿用显式加入选择。确认后冻结计划，可显示后台进度并取消；重开页面/任务中心仍可查看当前和历史结果。

完成后可“只集中”退出，或打开明确标为“本次集中整理结果”的既有清理面板，保留确认项、完整保护快照回读的 locked_ids 和当前路径，裁剪原删除意向再单独确认合并/删除；失败、取消或中断绝不自动打开执行删除。组件测试覆盖用户 A/D 集中例子、同名/附件、目标改变使预览失效、手工保留、部分完成、恢复回看、关闭后台继续和各类锁定；所有新绑定均接通，文案说明实际发生的操作。

> writes: `frontend/src/components/video-list/CleanupReviewPanel.vue`, `frontend/src/components/video-list/CleanupReviewPanel.test.js`, `frontend/src/components/video-list/CleanupConsolidationDialog.vue`, `frontend/src/components/video-list/CleanupConsolidationDialog.test.js`, `frontend/src/components/TaskCenterDrawer.vue`, `frontend/src/components/TaskCenterDrawer.test.js`, `frontend/src/utils/cleanupConsolidation.js`, `frontend/src/utils/cleanupConsolidation.test.js`, `frontend/src/utils/taskCommands.js`, `frontend/src/utils/taskCommands.test.js`, `frontend/src/utils/idleScheduling.js`, `frontend/src/utils/idleScheduling.test.js`, `frontend/src/components/VideoListPage.vue`, `frontend/src/components/VideoListPage.test.js`, `frontend/src/App.vue`, `frontend/src/App.test.js`（仅任务回看路由如确有需要）
> anchors: D-PC48-CONSOLIDATE 全部用户可见流程；D-PC48/49 与既有精确重复集中推荐兼容
> architecture: 清理审阅组件拥有选择/预览 UI，服务令牌和任务记录才是执行依据；任务中心复用既有聚合与事件路由；不增加前端文件写入或第二套删除/元数据合并算法；集成测试验证真调用参数与执行顺序。
> verify: `npm test`（frontend）; `npm run build`（frontend）; `git diff --check`; 修改条件/状态测试须观察失败或有效变异
> review: 独立核对移动授权与删除授权分离、陈旧预览/迟到响应、保留覆盖及任务恢复时不会默认清理。

## Integration And Final Verification

- 重读用户“其他类型也尽量集中目录”及接受的方案，对照 D-PC48-CONSOLIDATE 全部正常/失败场景和技术合同验收，不用单项完成替代跨层验证。
- `go test ./... -count=1 -timeout 1500s`、相关 race、`go vet ./...`、前端 `npm test`、`GOTOOLCHAIN=go1.24.9 wails build`；schema 用 SQLite 与可用一次性 PostgreSQL 验证，跨盘用隔离卷/强制复制夹具核对完整摘要与恢复，区分模拟与真机范围。
- 用隔离夹具走完整预览→移动→回读→单独合并/删除确认；覆盖复制取消/不足、同名低清版本占位、共享字幕、阶段故障恢复；测试必须能发现关键规则破坏。
- 全量 exact diff 独立审阅，确认迁移单一所有者、任务状态单一事实源、维护/锁/事务边界及无未经确认的移动/删除。文档、主上下文、绑定与实际结果保持一致。
- 更新现有设计与 AI-CONTEXT 的实施状态和验证记录（控制者拥有这三份文档）；不新建平行规格。真实用户文件不作为验证素材，未实测的外置盘/平台明确列出。

## Handoff And Residual Risks

- Review: 文件系统与数据库双写、跨盘取消/恢复、既有删除流程衔接构成具体高风险，P-001~004 及最终集成需独立只读叶子审阅。计划与技术合同初稿已自查，首次独立审阅由 `/root/review_image_cleanup` 执行；提出完整跨组保护快照、双实例恢复独占、字幕迟到写回三个 Important，已在源技术合同与相关 writes 修订，已局部复核解决。实际构造点修正为 enhancement_pipeline.go / app_cleanup.go；锁按现有 64 桶去重加锁，关联校验置于 Replace/Restore 共用入口。P-001 可实施。最终只读报告无其他 Critical/Important；三项 Minor 已写入合同/范围（实际构造点、按锁桶去重、外部 locked_ids 与本次动态保留分离）。
- Baseline evidence: 现有服务 Move/Cleanup 聚焦测试通过（`/tmp/cineinsight-consolidation-baseline.log`）；一次性 Docker PostgreSQL 18 的 models/database/migrator 基线通过（`/tmp/cineinsight-consolidation-pg-baseline.log`，models 0.321s、database 89.775s、migrator 26.425s）。容器 `cineinsight-consolidation-pg-20261009` 仅本次测试使用，最终删除。
- Blockers: 已解除。用户通知壁纸任务完成；已核对其24路径变更归属并保留，基于checkpoint015继续，暂无未决产品选择。
- Residual risks: macOS 真机外置盘与外部进程并发写入需要区分自动化模拟证据；Mermaid 无本地渲染器，已核对文本/引用，未作渲染验收。
- Resume note: baseline HEAD `4e710932a12461e97dc1a831df025476664ab906`；工作区 `/Users/zhangyukun/project/CineInsight`。已有精确重复目录集中实现及本轮之前设计文档为已归属基线，完整内容存 `/tmp/cineinsight-consolidation-run-624cqfby/baseline` 与 `baseline.json`，staged/unstaged 补丁同目录。执行器只使用隔离副本和完整未应用补丁，不在共享主工作区并行修改。每次集成保存 checkpoint；最新已归属完整内容见 `/tmp/cineinsight-consolidation-run-624cqfby/checkpoint-021-complete` 与同名 JSON；P-001 完整补丁已按基线 SHA/mode 校验后集成，10 个允许路径；集成 SQLite models/database/migrator、services 指定范围均退出 0；PG 同三包退出 0（database 79.723s、migrator 21.474s）；10 项变异日志均为实际断言失败，0 编译无效。独立 exact diff 审阅发现：源路径被其他未选库记录经别名引用、目标缺失文件记录通过未知别名占位、NFC/NFD同批冲突漏检，以及推荐目录10k并列组约13.96s（D×G）。已回派原叶子工人，在 `P001R-work` 基于 digest `f7fc18a095f62e27f4746fb384c4319b3d5245fc5412cd5922b4a8026278fe5f` 修复并补回归；P001R 四文件补丁已集成，主库当前为checkpoint-004；聚焦回归通过（3.028s），原4项复现通过、10k目标238ms；复审新增悬空文件symlink目标占位缺口，P001R2两文件已集成并通过3.370s聚焦测试，原悬空链接复现通过；复审又定位原始DB路径含link/..被Abs提前清除，P001R3两文件已集成：引用按链接顺序解析、被选源/目标的原始..明确拒绝，集成回归4.550s通过，独立局部复审0.742s通过，无未解决Critical/Important/Minor。其隔离digest为c4e036d2ba18dd445677666aa15a2bc5ec057cca13fa5a590c598a1776fee8eb。P-001已完成，P-002现在派发隔离实施。技术合同按审阅意见将OS锁定位简化为PreviewID（无DSN归一问题），并明确收尾结束后释放活动槽。隔离副本、身份及完整补丁均存同一 run 目录。

### P-001 accepted integration evidence

模型/database/migrator 在SQLite及一次性PG全包通过；最终服务聚焦回归4.550s通过。预览与三轮路径修订共21个有效定向变异触发断言失败，相关缺陷测试先红后绿。独立叶子 `/root/review_image_cleanup` 最终复审无未解决项。推荐目录10k双成员组约0.2s（原15s），250随机oracle一致，80项占位只查一次videos。仅预览/任务数据合同完成，不代表迁移执行已验证。

### Current execution

四个切片均done，整体complete。HEAD65dff31为其他任务的壁纸提交；开发阶段未提交/推送/安装，完成后用户已另行授权提交并push。最后代码checkpoint020，最终文档同步后checkpoint021-complete；全Go、相关race、双后端schema、前端99/1457、362绑定和Wails打包均通过。全轮独立审阅无剩余findings；真实外置盘/exFAT与Wails图形会话未实测。后续无需重跑已完成实施计划。

### 最终验证与交付

- `go test ./... -count=1 -timeout 1500s`：退出0，App9.813s、services89.880s，其余包通过；日志 `/tmp/cineinsight-consolidation-final-go.log`。最终 `go vet ./...`退出0。P2服务race78.959s、P3App race2.705s；其后Go实现未变化。
- schema/往返已在SQLite及一次性PG通过（P2集成记录）；后续没有模型/schema修改。该临时PG容器在本机环境中已不存在，最终查询无同名运行容器，没有触碰其他容器。
- 最终主库 `npm test --prefix frontend`：99文件/1457测试和全部脚本守卫通过，362导出/362真实使用，0allowlist；日志 `/tmp/cineinsight-consolidation-final-frontend.log`。
- `GOTOOLCHAIN=go1.24.9 wails build -m -nosyncgomod`：40.34s退出0，完整生成绑定、前端编译、Go编译及darwin/arm64打包，CGO沿用既有构建脚本的UniformTypeIdentifiers参数。产物 `build/bin/析微影策.app`；未安装。日志 `/tmp/cineinsight-consolidation-final-wails.log`。既有大包/静动态导入及重复objc链接警告保留。
- P4两项Minor及目录页收缩同类边界共3项先红后绿；P004R仅2允许文件，SHA fdab659162badee9b80f42c86b380c77ad0530aec71ae17282d16469b84f425d。独立复核3测试0.721s通过，日志 `/tmp/cine-review-p4r-pagination.log`，全轮无遗留findings。
- 重读原始“相关视频尽可能保留同一目录”与D-PC48-CONSOLIDATE：精确等价目标优先、其他类型保留审阅版本、完整片保护、独立范围、附件/冲突预览、取消/部分结果、重启只对账、历史回看、单独清理确认均有跨层实现和测试；没有以自动移动/自动删除替代用户确认。
- 49个改动Go文件gofmt clean；手写diff whitespace通过；生成models.ts的32个原生tab空行按前述生成合同保留，重复生成字节一致。壁纸24路径外部提交保留；受审业务代码与checkpoint020逐字节一致，最后只更新控制者拥有的3份文档。原精确重复目录推荐基线未丢弃。
- 有效变异：P1共21、P2初版15与修订9、P3共10、P4共10，均有真实断言失败；初次raw-pairs夹具未触发逻辑的survivor已修正并重测，编译无效结果未算入通过。实际80/160/320任务WAL11.634/23.186/46.309MiB，锁内全库FS扫描为0；不声称全任务耗时与库规模无关。
- 自动化只使用隔离临时文件与强制复制/故障注入；没有操作真实图库。未覆盖实际exFAT/NFS、真实外置卷断开与Wails图形会话退出；单个OS stat不可取消，外部进程文件变化不能被应用锁冻结，保持设计内的保留证据/冲突停止规则。

### 历史执行记录（旧状态按发生时间保留）

P-002 基线：隔离services全包93.280s只有DeepL基线快照缺失（宿主忽略的既有只读夹具未复制），不是代码失败。控制者补复制DeepL与data-test-set两个原有输入，并在candidate工具中明确排除它们的交付diff；只复跑该缺失夹具用例。App当前基线 `go test . -count=1 -timeout 300s` 已通过8.877s，日志 `/tmp/cineinsight-consolidation-app-baseline.log`。

P-002 实现期范围校正：允许修正 `subtitle_translation_test.go` 两个真实DB finalize夹具的字幕目标，使其对应已建视频路径；新library writer应拒绝旧夹具的另一临时目录，不削弱校验或原翻译断言。registry新key置于image_cleanup之后保持既有相邻顺序合同。DeepL缺失夹具补齐后定向通过0.595s。

P-002 控制者草稿核对：要求进度事件/任务列表只读聚合，避免每250ms反解全清单并逐文件stat；恢复纳入cancel/done及任务登记，维护可停止长hash；常规失败只对当前项收尾，重启才全量对账；已提交项恢复补字幕索引；常规Review按确认的文件身份/版本及保护复验，不每次重读全部视频内容。上述均在既有执行/性能合同与P-002路径内，待候选完成后核对验证。

前端实施前主库基线：`npm test` 已退出0（含全部组件/工具及bindings守卫，353 exports/used）；日志 `/tmp/cineinsight-consolidation-frontend-baseline.log`。P-002中途宿主磁盘短暂ENOSPC，暂停写入后自行恢复19Gi，可写探针成功；未删除用户或其他任务文件，环境失败不计为代码回归。

P-002 PG验证首次遇到连接拒绝（旧一次性容器已不存在，建库前失败），控制者重新创建同名无宿主挂载PG18容器并通过pg_isready；当前映射端口32768（旧32770已失效），要求重跑新逻辑矩阵。该环境中断不作为逻辑测试证据。

P-002 外置卷兼容性自查：不能以源mtime/mode直接验证目标，因目标卷可能量化时间或不同权限表示。已要求在同一私有journal保存全文校验后的实际目标快照，用于发布/恢复/Review；源snapshot保持严格，属性设置报错不吞掉，补目标元数据差异的模拟回归。属于既有跨盘合同实现修正，技术合同已明确，不声称真实exFAT验收。

P-002 完整候选28个允许代码路径已按base SHA/mode校验后集成，patch SHA256 `6a2786a4c95acca5619eb4611540e7ccf3b545856471f11358b840beec06c2f1`，当前主内容checkpoint-009-p002-integrated。工人最终全services74.166s、race15.396s、一次性PG新逻辑55.353s、vet通过；12类逻辑共15次有效变异断言失败（1次编译无效已剔除替代），0 survivor。控制者正在主库复跑services/race；独立审阅尚未进行，P-002保持in_progress。P3新App基线同步检查登记表增加后的接线缺口。

P-002 主仓新鲜证据：全services76.853s、相关race20.620s、一次性PG执行/恢复/Review/字幕矩阵46.842s，均退出0；diffcheck和checkpoint009全部899文件身份核对通过。日志 `/tmp/cineinsight-consolidation-p002-integrated-{services,race,pg}.log`。P3 App基线只在TestAPP03TaskCenterAdaptersCoverEveryRegistryKey失败（21→22），6.606s，无其他失败。独立叶子 `/root/review_image_cleanup` 正在审阅P002 exact diff，完成前不解锁P3。

P-002 独立审阅已复现4项Important（完整报告待收尾）：目标mtime变化覆盖旧源忽略指纹；逐项write锁内全active引用重建；整批journal平方写放大；共享字幕多个owner同批迁移使后项CopyOnly自失效。复现日志 `/tmp/cine-review-p2-reproductions.log`、`/tmp/cine-review-p2-shared.log`。主代码未修，P2仍in_progress。控制者已按spec在原概要/技术合同记录逐项日志表Proposed修订，待局部风险复核；不改产品范围、不部署，不提前解锁P3。

P-002 完整首轮独立报告：4 Important如上，另Minor copied故障hook错误固定第一项VideoID。P002R修复副本已派原叶子（work P002R-work，base digest900573ac059466254b3be3b48f0307003d18e491a31954074f44a770c989fafe），当前只授权R1忽略、R4共享字幕及M1注入身份修复；R2/R3设计复核前禁止改。独立叶子consolidation_p001正在只读核对数据布局/锁外库存方案，关注父大PlanJSON在SQLite仍可能放大写入，以及父版本和明细的一致读取。主代码仍checkpoint009，文档checkpoint010；尚未接受Proposed数据修订。

R2/R3技术修订已接受（原产品范围不变）：独立真实GORM探针确认大Plan留热行仍有平方WAL，故定稿三表（头标量/1:1 immutablePlan/逐项journal）；初始两表Proposed不采纳。已更新原概要和技术合同、扩展P2模型/schema/迁移测试writes及双后端verify。一致读父版本前后比较，无重试；单项及父CAS同事务、内存版本在commit后推进；终态前保存最终单项错误。全库路径/附件/目录FS扫描移到所有路径锁外，每项短write锁内保留轻量完整id/path精确比较与本家庭/目录版本复验，不能跨项用旧alias cache。此处无法冻结外部FS或中断卡住的stat，未承诺零外部竞态。P002R同一叶子继续顺序实现全部修复，最后完整验证/独立复审。

P002R/R2集成验证完成：主库全services123.057s、SQLite models/database/migrator 0.385/8.135/1.717s、PG 0.115/134.550/34.622s、相关race78.959s均通过。P002R2仅修models测试打印持锁结构值的vet诊断，断言不变，主库models0.701s及vet services/models/database/migrator通过。最新完整内容checkpoint-014-p002r2-vet，902文件身份核对一致。2026-10-09已派原独立审阅者局部复审P002R+R2 exact diff；P2仍in_progress，复审前不解锁P3。

P-002 最终独立局部复审通过：原叶子review_image_cleanup核对P002R/R2 SHA、19路径和checkpoint902文件一致；聚焦2.434s、原复现/8与16项成本0.753s、阶段恢复/损坏记录/取消2.726s均通过。报告无未解决Critical/Important/Minor；保留逐项全库SQL与锁外FS、不可冻结外部FS的限制。P2 done，P3已解锁。

### 等待共享工作区释放（2026-10-09 用户已裁定）

P2完成之后、P3派发之前，checkpoint验证发现外部桌面壁纸任务持续新增/修改：`app_wallpaper.go`、`services/wallpaper_*`、`docs/loopx/design/2026-10-09-desktop-wallpaper/`，随后`app.go`、`frontend/src/App.vue`、图片/预览组件及`frontend/wailsjs/**`也变化。这些不是本run改动，不得覆盖或纳入本功能交付。控制者没有集成P3、没有修改上述共享文件；先前创建的P003候选只是快照，尚无工人改动。

用户答复：“仍在开发，我让它先完成”。因此暂停共享文件的接线，等待壁纸完成通知；不将文件短时静止当作完成。之后先比较checkpoint014与现状，分开确认外部基线和集中整理已归属改动，基于最终工作区建立新P003隔离候选（不可复用当前混合时刻P003）。确认app.go/生成绑定的现状后按P003-task-draft.md继续；P3完成/复审后P4，最后全量验证和文档闭合。不得把目前仅后端完成报告为整个功能完成。

P2交付补丁、所有checkpoint、候选身份、红绿/变异/PG/race/独立复现证据保留在 `/tmp/cineinsight-consolidation-run-624cqfby`。一次性PG容器cineinsight-consolidation-pg-20261009仍在，端口32768；恢复时先检查健康再使用，最终只删除此容器。没有Git提交、push、安装或真实图库操作。

2026-10-09恢复：用户明确“壁纸任务完成了”，共享资源等待解除。核对24路径外部变化，App构造/退出与App.vue全局壁纸条保留；4个壁纸绑定随生成保留。HEAD未变，P2所有代码与checkpoint014一致。建立checkpoint015外部基线，重新跑App/前端基线后派P003B；前次中途P003副本不再使用。

恢复基线检查：App全包62.861s仅TaskCenterAdapters21→22失败；前端97files/1428tests仅idleScheduling21→22失败，都是P3/P4已知接线缺口，无壁纸相关失败。日志 `/tmp/cineinsight-consolidation-wallpaper-{app,frontend}-baseline.log`。P003B已派叶子consolidation_p003，base digest28953e409a851bbdc6ec8a5b9c8b8e01908972ad44b6257bef99ed70bb2472fc；保留24路径外部基线，不在主仓写实现。

P003B 13路径候选已按SHA/mode集成，patch SHA f9a0d16eeb216a74764fb3d067b75bf149d69dc293c8e297cd817d2f2f87ba51；最新checkpoint017，共919文件。工人冻结全App10.506s、race5.832s、vet全仓通过；10项有效变异无存活，源码字节恢复。控制者正复跑App/race/vet/生成，独立审阅尚未开始。

生成物检查口径：首次严格apply被Wails models.ts原生32行纯tab空行拦住，没有应用；误命名checkpoint016已改为checkpoint-016-p003-not-applied（内容仍015）。核实Wails v2.11.0 binding.go:190会给每行加tab，基线已有744行，禁止手改生成绑定；随后只对这32行生成器格式保留原生字节，手写diff仍严格whitespace检查、候选所有after SHA逐个核对。生成物必须重复生成字节一致和绑定使用守卫通过，不能把原生空白诊断报告成手写格式问题已修复。

P3主库复验完成：全App10.636s、TestAppConsolidation race2.705s、全仓vet退出0；GOTOOLCHAIN=go1.24.9 wails generate module退出0，所有生成文件与候选字节一致，仅runtime三文件mode恢复基线；checkpoint017全部919文件一致。独立review_consolidation_p003已派，尚未解锁P4。

P3独立review_consolidation_p003通过：exact13路径/SHA核对，相关race2.628s通过，checkpoint919文件一致，无Critical/Important/Minor。P3 done，P4 in_progress；真实Wails图形会话的退出/恢复未实测。

P4派发前HEAD变为65dff319aec23d7e4fbf240dec2e4c36d73f00dc（feat(wallpaper)）。git show仅已明确归属的24路径壁纸任务提交；当前919文件仍与checkpoint017逐字节/mode相同，没有新内容污染、没有集中整理实现被提交。用户已明确壁纸完成及继续，故将该外部提交记为新HEAD基线，checkpoint018保存相同内容并新HEAD。P004 base digestd1de428746008dc39512d5f6bf19137327d2d1c01b1a18795edbe481ab0f4a7e，基于此新HEAD；控制者未执行Git提交。

P4实施中：独立对话框/工具、清理面板选择与task scope、任务路由已在隔离副本接通。第一轮完整npm test99files/1453tests通过，362exports全部真实消费，0allowlist；尚未交付候选，不代表集成完成。控制者草稿检查要求并已回派：Start响应前terminal事件不丢、任意任务terminal刷新库、阶段/附件中文文案、同轮普通重开保留skip、预览移动/待清理计数、copy_bytes总复制语义。最终需受影响回归/build/变异与独立审阅。最终完整审阅的归属基线已准备final-review-base（原run baseline加已明确归属壁纸commit，不含本run实施）。

P004集成：16允许路径，patch SHA0945e3c2613ffeab07dd98e983622992b0b3ffdd315dd865082707a5c93d1985。主库npm test11.87s（99/1454+所有scriptguards、362/362bindings），build0.52s通过，既有bundle>500KB和静/动态导入混用提示保留。10有效mutants均有AssertionError。全run审阅基线final-review-base与最终71路径patch/manifest在run目录，已派独立跨层复审；控制者final go test./...进行中，未宣称整体完成。

最终Go全套退出0，日志/tmp/cineinsight-consolidation-final-go.log（含App、services、models、database/migrator等）。独立P4/全run审阅正在进行，已复现两项Minor分页问题：同源定位后category watcher复位目录页、组/目录数量缩减后原页状态未clamp。原工人P004R两文件隔离修复（base digest082f9662c1cee06ca6f0d8b5b7016e7b2de09ccbcb1fdbbccda2a646f2cb46fe），不得修改原冻结补丁；集成后复跑前端并局部复审。

### Git交付授权

用户在研发验证完成后明确要求“提交并push代码”。本次提交包含已验证的集中整理、原精确重复目录偏好以及相关测试/文档；壁纸已单独提交65dff31，保留其历史。提交前当前923文件与最终checkpoint021一致，验证输入未变化，无需重复业务测试。目标为当前master分支及origin/master，正常push，不强推、不安装。
