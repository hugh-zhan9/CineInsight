---
schema: loopx-plan/v1
source: ../design/2026-10-21-functional-polish/概要设计.md
status: complete
slices:
  - id: P-001
    status: done
    depends: []
  - id: P-002
    status: done
    depends: []
  - id: P-003
    status: done
    depends: [P-001, P-002]
  - id: P-004
    status: done
    depends: [P-001, P-002]
  - id: P-005
    status: done
    depends: [P-003, P-004]
---

# 功能打磨与扩展实施记录

## Goal And Boundaries

用户于 2026-10-22 明确要求「按照这个来，分为 2 个 subagent 进行开发」，授权实施此前方案及其推荐规则。完整范围为 [概要设计](../design/2026-10-21-functional-polish/概要设计.md) 与 [需求设计](../design/2026-10-21-functional-polish/需求设计文档.md) 的 D-F01–10；不含手机遥控、照片地图、安装、提交或部署。此前文档中的“仅出方案、尚未授权”属于历史状态，本次授权替代该状态。

采用作品级探索及最大成员播放分数、暂避 7 天、标签权重 0.5 且不连乘、显式 IINA 精确同步、可纠正季集与手填总集数、仅往年同日及闰日单独展示。文件级事实、人工关联、系统默认播放器及既有维护围栏继续保留。

基线 HEAD：`9e81a86b5a0a6507c4b9ec883e29f0f88755027c`。已有修改为 `frontend/package.json.md5`（内容 `f05a682410322334c1c908289bd7c2b4`）；两份设计为已有未跟踪文件，均须保留。基线副本与两个独立副本位于 `/tmp/cineinsight-functional-polish-20261022/`。实现者不得在主工作区编辑，不提交；候选按相对于基线的完整补丁检查后顺序集成。

公共注册与生命周期、生成绑定、schema golden、双库往返夹具以及总文档仅由集成切片修改。实现切片在各自数据库夹具中显式迁移新增模型，前端定向测试使用已有绑定 mock 方式；不得为了局部测试修改公共注册或生成产物。发现范围外依赖时先报协调者分配路径。

## P-001 播放、字幕与推荐

落实 D-F01/03/05/06/07：两个播放器共用安全字幕投影，源身份与会话令牌有效；文字轨提取经过预览、目标冲突与原子备份写入；随机与推荐按作品计算；反馈可查看撤销；受控 IINA 复用无队列业务的适配且保持进度、重播、维护退出语义。所有正常与异常验收以原设计为准。公共生命周期接线形成明确集成需求，不以尚未接线冒充端到端完成。

> writes: `services/{video_service,video_discovery,video_playback,settings_service,video_random*,video_recommend*,recommendation_*,playback_*,controlled_*,iina_*,preview_*,subtitle_*,tag_service,video_watch*,viewing_*}.go` 及对应测试；`models/{recommendation_feedback,play_event,settings,viewing_diary}.go` 及对应测试；`models/video.go` 仅新增 DesktopPlayer 设置字段；`models/viewing_notes.go` 仅将 ViewingDiaryEntry.Source 拓宽到32（PG新来源长度需要，play_event.go 同步）；`app_{subtitle*,playback*,recommendation*,video*,settings*}.go`；播放器、字幕、推荐、设置相关前端组件/工具及测试（不含 App.vue、Photo*、TaskCenter*、EntityLibraryPage*）；`scripts/test_playback_queue_webkit.sh`、`frontend/scripts/playback-queue-webkit/**`；旧随机/版本/队列/选片合同对应修订。不含公共注册文件或生成产物。额外路径须协调登记。
> anchors: D-F01, D-F03, D-F05, D-F06, D-F07
> architecture: 扩展 SubtitleWorkbenchService/SubtitleFileWriter、VideoService 和原队列 IINA 进程 IPC；受控适配不拥有业务表；推荐不写观看事实；字幕失败不影响视频；按路径验证 SRT 写入互斥。
> verify: 定向 Go 与 Vitest 回归，先失败的关键断言或变异证据；合成字幕轨文件验证；集成阶段补全绑定、全套测试与真机矩阵。
> review: 独立审阅受控进程边界、字幕源/目标冲突与随机行为兼容改动。

## P-002 关联、失败恢复、照片与剧集

落实 D-F02/04/08/09：自动关联读写校验保留 manual/dismissed；失败诊断完整持久化、分页可达、冻结集合且按失败版本条件认领；照片仅按真实 EXIF 日期查询并复用可见性；剧集按已确认系列/季及人工覆写报告缺口与离线，成员解除与覆写清理原子提交。所有正常与异常验收以原设计为准。公共生命周期接线形成明确集成需求。

> writes: `services/{watchlist_*,video_title_sort,ai_tag*,technical_*,playback_proxy_service*,task_failure*,image_library*,image_annivers*,collection_*}.go` 及对应测试；`models/{task_failure*,collection_episode*}.go` 及对应测试；`models/ai_tagging.go` 仅新增 AITaggingState.FailureRetrySourceVersion（冻结授权随原 owner 排队并在执行前复核，普通重试清理）；`models/media_details.go`、`models/playback_proxy.go` 仅新增 FailureRetryClaim（原服务持久化条件认领，成功/失败/取消/启动清理，不自动重新排队）；`app_{task*,watchlist*,image*,collection*}.go`；Photo*、TaskCenter*、EntityLibraryPage*、新增照片回忆/失败明细/季集组件与工具及测试。`services/media_probe_service.go` 的失败认领清理与冻结源参数传递/获gate后执行前复核（审阅Important修复）及 `services/playback_proxy_store.go` 的 upsertProxyRow 仅追加 failure_retry_claim 清理。不含公共注册文件或生成产物。`playback_proxy_service*` 和上述 store 字段清理专属于本切片，从 P-001 的 playback 通配范围排除。
> anchors: D-F02, D-F04, D-F08, D-F09
> architecture: WatchlistService 拥有关联，原任务服务拥有执行和条件认领；诊断记录不成为第二调度器；图片查询和作品集复用原可见性及成员；人工事实用 revision/条件 DML，无应用控制数据库锁。
> verify: 定向 Go 与 Vitest 回归，覆盖 73 条失败、重复确认、记录不完整、源变化、关联人工竞争、缺集/离线/未知季、闰日及分页筛选；新增表在专用测试 DB 中验证。
> review: 独立审阅失败认领并发、季集复合外键与原子清理、自动关联人工竞争。

## P-003 应用生命周期、调用方与绑定

在两份已审候选的同一基线副本中，完成 App 启动/维护/退出、所有新增入口与状态事件、版本变更通知及观看来源消费者。任务诊断 store 必须在 owner 启动前恢复并注入；停止先封提交再等 owner。IINA 关闭失败保留引用与占用、中止维护推进，恢复不得复用已关闭实例或绕过待清理进程；提取确认取消先于路径围栏。绑定只用规定工具生成，删除临时 harness binding fallback。数据库注册由 P-004 拥有，本切片只依赖已经确定的模型类型和既有 ApplySchema 合同；局部测试可用工作区外模型注册 overlay 预演，最终运行时交叉验收属于 P-005。

> writes: `main.go`（schema完成后/Wails前功能初始化，诊断失败局部禁入并告警，不扩大为全App失败）、`app.go`、`app_functional_polish*.go`、`app_playback*.go`、`app_settings.go`、`app_task*.go`、`app_collection_episodes.go`、`app_image_anniversaries.go`、`app_subtitle_extract.go`、`app_subtitle_playback.go`、`app_recommendation.go` 及必要 App 生命周期/调用方测试；`services/version_group_service.go` 及测试；`services/task_failure_service.go`、`services/image_anniversaries.go`、`services/collection_episodes.go` 仅新增RPC取消上下文变体/薄包装与对应验证（保留既有包装及owner语义）；`services/recap_service.go`、`services/library_stats_service.go` 及新来源的其他实际枚举消费者/测试；`frontend/src/App.vue`、`frontend/src/utils/recap.js`、`frontend/src/components/InsightsPage.vue`、`frontend/src/components/ViewingNotesPage.vue`（实际来源展示/导出映射）及对应测试、`frontend/src/components/PlaybackQueuePanel.vue`（cleanup_pending清理重试入口与移除缓冲播放器）及对应测试；`frontend/scripts/playback-queue-webkit/build.mjs`；`frontend/wailsjs/**`（仅生成）。不得编辑 P-004 的注册、迁移与schema文件。
> anchors: D-F01–09 的公共入口与生命周期
> architecture: App 仅接线，任务执行归原 owner，受控进程引用在关闭成功前不可丢弃；所有新入口受维护/待重启围栏约束。模型形状来自已接受 P-001/002，不新增数据库模型；与 P-004 的 runtime 注册依赖在 P-005 一并验证。
> verify: App 启动/维护/恢复/退出及来源枚举定向回归与 race；`GOTOOLCHAIN=go1.24.9 wails generate module`；前端对应组件与绑定守卫；最终全部注册后的全套测试见 P-005。
> review: 独立审阅关闭失败引用保留、提交/回调等待、恢复准入、真实绑定调用方。

## P-004 数据库注册、备份与双库迁移

注册五张新增模型，父表先于子表；保存原字段和历史记录，旧来源列拓宽，新增 claim/source 字段正确默认值。更新模型 golden，验证实际五表数据、关系、revision、时间与false值经 SQLite→PostgreSQL→SQLite 往返及备份兼容。只使用专用测试库，不改真实片库。P-003 不修改模型形状，两个切片在独立副本中没有共同写入的文件或生成产物。

> writes: `models/schema*.go`、`services/testdata/model_schema_golden.json`、`services/backup_schema.go` 及对应备份/schema验证测试、新增 `services/functional_polish_backup_test.go`；`database/**`（限新增功能schema升级、实际数据迁移夹具和对应必要支持）。不改模型定义、App、前端或生成绑定；发现真实schema形状问题先报协调者归属。
> anchors: D-F04, D-F05, D-F07, D-F08, D-F10
> architecture: 复用 AllModels、ApplySchema、BackupService 与既有迁移器；不另建迁移引擎。真实 CollectionVideo 复合成员先于覆写，TaskFailureRun先于Item；保留逻辑媒体引用与诊断历史。
> verify: schema/golden/外键顺序测试；旧结构升级保留原行；专用PG+SQLite新表实际数据往返、source列长度及备份表/列清单与恢复兼容回归。根包完整测试由 P-005 跑。
> review: 独立审阅加性迁移、复合FK/拓扑、实际数据非空往返和备份兼容。

## P-005 最终集成、文档与现场验收

协调者顺序核对两份公共候选并集成，作者处理发现，原审阅者复核必要修复。更新 AI-CONTEXT 与原设计中的接受/实施状态，记录新行为及真实验证范围。原设备缺失项按 D-F10 明确未验证，不以单测替代。此处包含最终全部验收，不能用局部 overlay 或作者报告代替。

> writes: P-001–004 已声明路径中的必要集成修正；`AI-CONTEXT.md`、两份原设计的接受/实施状态与验证记录。共享文件修复串行分配给一位作者，协调者不写功能代码。
> anchors: D-F00–10
> architecture: 最终核对 owner、维护隔离与唯一真值源；AllModels 注册和 App lifecycle 结合后实际验收，不新增默认降级/重试或安装行为。
> verify: `go test ./...`、`go vet ./...`、frontend `npm test`、`bash scripts/build_and_install_app.sh --build-only`；专用 PostgreSQL 新行为与实际双向数据迁移；关键生命周期 `-race`；真实 WKWebView 与 IINA 合成媒体；专用文件恢复/权限回归，外置盘/SMB/exFAT无真实设备如实标未验证。
> review: 两位原 leaf worker 交叉精确差异审阅，所有 Important/Critical 修复并复核；协调者最后核对原设计场景与主工作区新鲜证据。

## Integration And Final Verification

逐条对照原设计场景表而非只核对 D 编号。确认所有绑定均有真实前端调用、字幕变更不重置视频、随机计数只落实际文件、失败重试 accepted 不冒充成功、未知剧集范围不宣称完整、照片读取不算查看。核对各新表真实数据能够备份及两库往返，且维护/退出时没有悬空 DB 回调。

## Handoff And Residual Risks

- Review：2026-10-22 两位 leaf worker /root/playback 与 /root/library 已交叉检查完整计划及原设计，未发现需新增用户裁决的阻塞。实施必须覆盖：任务逻辑媒体引用与真实 run FK、季集复合成员 FK、人工关联 CAS、IINA 普通/队列互斥与维护等待、实际 SRT 路径锁及服务端源/token 校验。公共版本通知、观看来源枚举、注册迁移留 P-003 顺序完成；实际差异仍须交叉审阅。
- Blockers：无未解决独立审阅Important/Critical。P-001/002已通过最终复审及协调者合并回归，进入公共接线。
- Residual risks：专用PG、IINA与WKWebView已可用并有局部通过证据；真实外置盘/SMB/exFAT测试卷仍未提供，代码测试不能替代现场验收。
- Resume note：全部代码与文档已写回主工作区，按已验证候选SHA逐文件一致；P-001–005完成。无待实施/待修的重要问题。仅真实外置盘/SMB/exFAT/系统权限现场矩阵未验证，已在原设计明确记录，不作为已通过证据。

### 验收环境

专用 PostgreSQL 容器 `cineinsight-polish-test-20261022` 已启动（仅回环端口 55483，数据库/用户 cine_test，临时测试数据），两位 worker 已获连接信息；dbtest 为每个测试隔离 schema。现有 IINA.app、FFmpeg、Wails 可用。WKWebView 可复用现有 queue 合成样片 harness；暂未挂载外置测试卷。

### 基线证据

- 主工作区 `go test ./...` 退出 0；`npm test` 退出 0（187 文件、2986 测试，绑定 465/465）。
- 现有 `bash scripts/test_playback_queue_webkit.sh` 在未修改状态退出 1，失败 `bounded queue panel`；已交给播放 worker 定位原生夹具与现有 UI 的差异，不能归因新增字幕。日志均位于上述临时目录。
- PostgreSQL 全量与真实 IINA 现有集成基线已启动，结果待记录。

- 真实 IINA 旧集成基线退出 0（19.176s）。PostgreSQL 首轮基线中断：专用容器日志确认 `No space left on device` / WAL PANIC（Docker 数据盘仅剩 255MB），非业务回归。仅删除本任务创建的容器及匿名卷并重建为 2GiB tmpfs、WAL 256MB 的临时库，连接地址不变；未清理其他项目资源。PG 基线待重跑。

### 实施抽查与接线备忘

- P-001：writer 通知 `SetSubtitleChangedListener` → Wails `subtitle-changed`，退出置 nil；维护/退出先 `QuiesceSubtitleExtraction()` 取消并等待，恢复 `ResumeSubtitleExtraction()`。普通受控播放 `QuiesceControlledPlayback()` 返回恢复函数，维护取消时调用、退出不调用。
- P-002：任务认领需使用已经校验的同一个失败快照 CAS，不能二次读取后提升授权到新失败。技术/代理持久化 claim 只表达原 owner 的入队认领，不创造调度器；加字段已授权，原失败身份不因入队而改写，释放带本次 claim/身份。反馈进度须逐项发布；重复失败上报使用实际尝试稳定身份。

### 继续执行记录

用户中断后明确要求“继续”，原范围、两位 worker 与授权不变。P-001 报告新真实 IINA 普通播放/暂停/自然结束/原生重播通过（10.867s），两播放器 WKWebView Blob 字幕及同源重建通过；协调者仍需集成复验。旧 queue WebKit 基线失败系宽选择器把子项算作行，夹具修正为 li 行选择器。P-002 请求的原 owner 失败 claim 清理文件已分配，App 新 taskFailures 字段及 startup/维护/退出接线仍由 P-003 完成。

- 临时 PG 重跑全量基线退出 0（services 1122.812s）；两库基础环境已确认。P-001 新 PG 回归发现原 play_events.source / viewing_diary_entries.source varchar(16) 不够 desktop_controlled_view，已授权拓宽32，P-003 必须验证老数据保留与两库迁移。

### 候选冻结与独立审阅

P-001已冻结53文件（playback-manifest.json / playback-handoff.md），P-002已冻结41文件（library-manifest.json / library-candidate.patch / library-handoff.md），均在临时根目录。协调者按保存基线逐字节核对两份候选路径无交集，原md5与未跟踪设计未纳入候选。/root/library 正在只读审 P-001，/root/playback 正在审 P-002（先review-r1不可变快照，再复核最终差异）。尚未将业务候选写入主工作区；只有审阅、集成与相应验证通过才标切片done。

- P-002 独立审阅 /root/playback 确认两项 Important：技术重试在外层复核后等待每视频 probe gate，随后会处理被替换的新源；40,000条同年份片单因未去重年份SQL占位符而失败，启动对账亦缺设计要求分批。可重复失败证据在 `library-review-repros/results.log`。作者已收到，修复后同一审阅者复核；P-002尚不准入集成。

- P-001 独立审阅 /root/library 确认3项 Important：提取确认等待SRT锁时取消未覆盖Confirm；普通受控Close成功前过早释放占用；软删暂避目标加入版本组后仍错误排除其他成员。复现 `playback-review-library/results.log`。作者已转回修复角色；同一审阅者随后复核。P-002规模回归另证实单条片单+40,000同年份无关视频也因代表查询无界IN失败（`library-review-repros/one-entry.log`）。

- 审阅修复进展：P-002新增RefreshForSource在真实probe gate后核对冻结源，technical/proxy复用；想看年份去重分段、500视频流式标题筛选、代表查询分500、启动500条目一批。作者已将40k两形态、startup1001及两类gate竞争复现永久化，等待独立复审。P-001使用原contextMutex统一路径锁的可取消等待，Confirm登记取消；普通owner/transport关闭成功才清占用与句柄，反馈活跃性一致，并补队列关闭失败的对称释放。尚未以作者通过报告代替独立复审。

- 公共分工独立检查 /root/library：P-003/004/005 同一集成基线、五表拓扑、真实绑定与schema写范围无冲突；已将P2新增照片/季集绑定及其他新增绑定的维护围栏修正明确归P-003。复审P2规模及正常gate已通过，仅取消未执行分支需保护原失败状态；P1后端复审全过，仅停止失败状态栏恢复入口待局部修复。

### 公共阶段准入证据

P-001最终审阅由/root/library关闭（后端SQLite/race/PG及状态组件2测试新鲜通过）；P-002 r3最终审阅由/root/playback关闭，含原取消overlay复现，41文件manifest SHA256 `6b92a1c77b36688efa2299d874099f87bb83010fc3d5df743bd59666107d4ed3`。协调者按baseline哈希先检查目标未漂移，再顺序复制55+41文件；无路径交集，`integrated-slices-check.log` 的合并服务定向回归退出0，diff检查通过。公共切片以该接受基线建独立副本，不写主工作区。

- P-003补充准入：失败诊断初始化需在前端可调用前形成ready/unavailable状态；允许main.go仅schema后/Wails前接线。RecoverInterrupted错误不被忽略，但按D-F04故障隔离只禁bulk retry并任务中心告警，保留原owner执行；schema本身失败仍遵循既有startupError。

- P-004 AllModels五表注册形状已固定，P-003可使用schema副本该文件作外部overlay预演。宿主无pg_dump/pg_restore，允许P-004用本任务专用容器工具与独立fixture数据库验证真实dump/restore，不引入生产docker依赖或宣称宿主工具可用。P-003额外获分配原PlaybackQueuePanel的cleanup_pending恢复入口，清理成功前移除缓冲播放器、保留明确重试停止，迟到操作不得影响新会话。

- P-003新RPC准入采用短mutex关闭/取消generation后WaitGroup排空，避免长Confirm持RW锁使维护无法取消。任务Preview需Context变体传DB/循环，已分配原服务最小补线；异步Confirm返回后批次仍由store拥有，不能被短RPC的done立即取消，维护先StopAndWait提交再停原owner。

### 公共候选验证进展

P-004冻结8文件（schema-manifest.json / schema-handoff.md）：两库models/database/migrator全模块、五表非空往返、旧结构升级、SQLite和独立PG fixture真实BackupService恢复均通过。PG工具仅容器测试runner，fixture库已删除。P-003前端6文件261测试和485/485绑定守卫通过；独立审阅初查无生产Important。组合APP02/Functional/PlaybackMaintenance暴露旧待重启fixture清理用生产release被新终态保护拒绝，作者仅调整测试专用lease释放，保留生产保护，组合race待确认。

- P-003已冻结38文件（lifecycle-manifest.json/lifecycle-handoff.md），组合race退出0（5.680s）、前端6文件261测试、真实绑定485/485；P4冻结8文件。协调者核对公共路径无交集。P3生产无已证实Critical/Important，reviewer仅核最终delta；P4由playback独立只读审。Wails生成models.ts tab-only空行保留原工具输出，手写diff检查0，不手改生成文件或扩大生成器修改。

### 最终集成开始

P-003最终独立审阅通过，38文件逐项SHA匹配，独立组合race5.254s及版本通知0.680s通过；P-004独立审阅通过，8文件manifest SHA `acf12cce512e76052c29fadf1ca0e2d40bdd4fdb386fd1cb19c705a8244557d6`，独立双库/真实备份恢复通过。协调者将38+8文件按基线SHA顺序集成到final-staging，无公共路径冲突。正在以真实注册/绑定运行全Go、npm、vet，无overlay。library在该副本仅更新AI-CONTEXT和两原设计，不改代码；root负责最终验证与主工作区写回。

### 最终代码验证及写回

真实注册/绑定最终副本：`go test ./... -timeout 600s`退出0（root45.916s、services170.089s）；`npm test`退出0（195文件3008测试及全守卫，485/485绑定）；`go vet ./...`退出0；关键App/services race退出0（5.947/15.023s）；最终真实IINA退出0（27.065s），WKWebView16项合成场景通过；PG新行为/旧结构升级/五表实际往返/真实BackupService恢复专项退出0；`build_and_install_app.sh --build-only`最终退出0。中途全量发现并修正：两处旧终态fixture自有lease清理、旧随机合同测试改为作品语义但保留其他文件消费者断言、三个控件共享class与刷新字幕文案。没有放宽生产保护/守卫。

协调者按原baseline哈希验证目标无漂移后，将final-code-manifest.json中的131文件写回主工作区，再逐文件验证全部candidate哈希一致；原用户package.json.md5字节不变，未混入构建产物。主工作区与已验证源逐字节一致，未为目录移动重复全量。文档三文件待最后同步，D-F10真实外置盘/SMB/exFAT无设备仍明确未验证。

### 完成交付

三份文档（AI-CONTEXT与原概要/详情）已同步接受状态、实际接口/模型/生命周期及最终验证证据；检查78个本地引用、显式D锚点/代码围栏无新增错误。文档也按baseline/candidate SHA写回。主工作区共有134个候选文件（131代码/测试/生成/旧合同+3文档）及本执行记录；用户原md5保持。临时PG容器及恢复fixture数据库均已清理，构建包仅在临时最终副本build/bin，未安装；未commit/push/部署或改真实片库。
