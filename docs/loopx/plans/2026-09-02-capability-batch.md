---
source: docs/loopx/design/2026-09-02-capability-batch/需求设计文档.md
status: ready
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
    depends: [P-003]
  - id: P-005
    status: done
    depends: [P-001]
  - id: P-006
    status: done
    depends: [P-002, P-003, P-004]
  - id: P-007
    status: done
    depends: [P-002, P-003]
  - id: P-008
    status: done
    depends: [P-002, P-003]
  - id: P-009
    status: done
    depends: [P-002, P-003]
  - id: P-010
    status: done
    depends: [P-001, P-002]
  - id: P-011
    status: done
    depends: [P-001]
  - id: P-012
    status: done
    depends: [P-003]
  - id: P-013
    status: done
    depends: [P-004, P-011, P-012]
---

# CineInsight 九项能力扩展与代码健康治理

## Goal And Boundaries

交付 `.loopx/intake/2026-09-02-capability-batch/requirements.md` 的十项范围：兼容性转封装代理、播放历史账本、桌面通知与 Dock 角标、人脸识别驱动的人物关联（人物同时覆盖视频与图片）、建议作品集、截取片段识别、命令面板、后台任务空闲调度、字幕翻译术语表与滑动窗口，以及四个巨型文件的行为保持拆分。设计结论以 `docs/loopx/design/2026-09-02-capability-batch/需求设计文档.md` 的 D-001..D-039 为准，方向取舍见同目录《设计提案.md》，本计划不重开。

已定且不得重开的裁决：代理是隐藏派生文件、不入库、有 LRU 上限（CD-01、CD-05）；人物覆盖视频与图片且 `image_people` 与 `video_people` 对称（CD-02，替换 2026-08-07 旧裁决）；播放事件账本与计数列并存、随机算法不改；人脸运行时是托管 Python sidecar、向量 BLOB + Go 侧相似度；通知与角标经 darwin cgo 且 build tag 隔离；空闲门只挡自动任务；术语表只注入 OpenAI 兼容提示词。

全局约束：不改任何现有 Wails 导出方法签名与事件名；数据库只加不删且 SQLite / Postgres 双后端都通过既有测试基座；除既有 AI 接口与显式人脸模型下载外无网络出口；人脸数据、帧、字幕正文不出本机；长任务沿用单 worker 可取消可续跑；AI 与算法产出只生成候选、用户确认才写关系；不改布局与信息层级，新 UI 用既有令牌与 `BaseModal` / `BaseMenu` / `BasePopover` / `btn-*`；Claude 不提交、不推送。

顺序约束（来自设计 Implementation And Transition）：P-001 / P-002 的拆分先于一切会改 `app.go`、`video_service.go`、`VideoListPage.vue`、`SettingsPage.vue` 的功能切片；人脸切片串行排在最后（其写入与其他切片在 `models/`、`app_*.go`、绑定与设置页等共享文件上重叠，不可并行）。绝大多数切片都要碰 `models/`、`app*.go`、`frontend/wailsjs/**`、`SettingsPage` 子组件与 `AI-CONTEXT.md`，因此除 P-001 ∥ P-002 外基本按依赖串行执行。

非目标：手机端 Jellyfin / 浏览模式 / PIN；联网元数据；改随机算法；DeepL 术语表 API；实时转码；替换或删除原文件；人脸不经审阅自动关联；作品集覆盖图片；Rust/SwiftUI 重写；超分 P-013 真机验收与当前未提交文件的提交（用户侧动作）。

环境要点：Go 依赖与 `wails generate module` 需 `GOPRIVATE=none GOSUMDB=off GOFLAGS= GOPROXY=https://goproxy.cn,direct`（本机直连 GitHub 不通）；人脸模型下载同理需镜像可配。全部 `npm` 命令在 `frontend/` 下执行。双后端测试机制：`internal/dbtest` 按环境变量 `CINEINSIGHT_TEST_PG_DSN` 是否设置选 Postgres 或 SQLite，「双后端各一次」= 不设与设该变量各跑一次。本机没有 `.env` 且 5432 未监听，Postgres 一遍的可行做法（P-005 验证过）：`docker run --rm -d -p 55433:5432 -e POSTGRES_PASSWORD=p005 -e POSTGRES_DB=cineinsight_test postgres:16`，DSN **必须用 key=value 形式** `host=127.0.0.1 port=55433 user=postgres password=p005 dbname=cineinsight_test sslmode=disable`（dbtest 会追加 ` search_path=…`，URL 形式会报 sslmode invalid）；不要用 `pgvector/pgvector` 镜像，两条「pgvector 不可用需明确降级」的测试会因扩展可用而失败；跑完 `docker rm -f`。Postgres 上 `./services` 单次约 6–10 分钟。`gofmt` 检查一律用 `gofmt -l $(git ls-files -co --exclude-standard '*.go' | grep -v '^search/')`：既覆盖本批新建的未跟踪文件（Claude 不提交，新文件一直是未跟踪状态），又排除 `search/` 这个未跟踪的第三方研究副本（独立 go.mod，`gofmt -l .` 会误报其中 30 个文件）。基线记录存放在 `.loopx/workspace/2026-09-02-capability-batch/baseline/`：2026-09-02 已写入全量测试结果、四个文件行数与前端 `data-test` 集合；绑定快照由 P-001 在拆分前生成，随机分数序快照由 P-005、DeepL 请求体快照由 P-010 在各自改动前生成。

## P-001 Go 侧行为保持拆分与平台说明收口

把 `app.go` 按领域拆成同包多文件，文件名固定为设计 D-036 所列：`app.go`（结构体、构造、startup/shutdown、日志、前后台标记）、`app_video.go`、`app_library.go`（片库查询、保存视图与洞察，含 `GetLibraryInsights`）、`app_media_details.go`、`app_ai.go`、`app_subtitle.go`、`app_cleanup.go`、`app_image.go`、`app_settings.go`、`app_tasks.go`，`services/video_service.go` 拆为 `video_service.go`（核心 CRUD）、`video_scan.go`、`video_playback.go`、`video_random.go`、`video_rename_move.go`、`video_pagination.go`。所有方法与函数只搬运不改，包内可见性不变。同一切片内把 `services/subtitle_service.go:1190` 的 Windows 二进制下载 TODO 收口为明确的「当前平台不支持自动下载」错误，并在 README / GUIDE 增加「平台支持」小节，如实标注 sips、IINA、超分 sidecar 为 macOS 专属（后续切片各自补充自己的条目）。

完成条件：`go build`、`go vet`、限定本仓库 Go 文件（含未跟踪新文件、排除 `search/`）的 `gofmt -l` 干净且全部 Go 测试通过；绑定以「拆分前在当前工作树重新生成一次并快照到基线目录 → 拆分后再生成 → 与快照 diff 为空」为证据（工作树里的 `frontend/wailsjs` 本身已有未提交改动，不能直接 `git diff`）；`app.go` 与 `video_service.go` 各不超过原行数 40%；Windows 分支有单测覆盖新错误。

> writes: `app.go`, `app_video.go`, `app_library.go`, `app_media_details.go`, `app_ai.go`, `app_subtitle.go`, `app_cleanup.go`, `app_image.go`, `app_settings.go`, `app_tasks.go`（后九个新增）, `services/video_service.go`, `services/video_scan.go`, `services/video_playback.go`, `services/video_random.go`, `services/video_rename_move.go`, `services/video_pagination.go`（后五个新增）, `services/subtitle_service.go`, `services/subtitle_service_platform_test.go`（新增）, `README.md`, `GUIDE.md`, `AI-CONTEXT.md`, `.loopx/workspace/2026-09-02-capability-batch/baseline/**`
> anchors: D-036、D-037（Windows 与文档部分）；AC-24、AC-25（文档与 Windows 部分）；TC-12
> verify: `gofmt -l $(git ls-files -co --exclude-standard '*.go' | grep -v '^search/')` 为空；`go vet ./...`；`go test -count=1 ./...`；拆分前后各执行 `GOPRIVATE=none GOSUMDB=off GOFLAGS= GOPROXY=https://goproxy.cn,direct wails generate module`，`diff -r` 基线快照与 `frontend/wailsjs` 为空；`wc -l app.go services/video_service.go` 对比基线
> review: 纯搬运是否夹带了逻辑改动；导出方法集合前后是否完全一致（以绑定 diff 为证）

## P-002 前端行为保持拆分与暗色令牌

`VideoListPage.vue`（Options API，脚本 3130 行）抽出清理审阅面板、字幕预览弹窗、字幕生成弹窗、超分弹窗、重命名与文件夹重命名与保存视图弹窗、批量操作栏、后台任务状态条为子组件，父子只经 props / emit 通信，不用 `$parent`；`SettingsPage.vue` 按现有分区抽子组件，父组件保留锚点导航与保存流程。每抽一个组件跑一次前端测试。`frontend/package.json` 的 `test:components` glob 改为覆盖 `src/components/**/*.test.js`，并把新增的 `data-test` 集合对比脚本挂进 `npm test`。同一切片内把清理与字幕弹窗约 20 处浅色字面色改为令牌并补齐暗色值；字面色检查为对抽出后的 `frontend/src/components/video-list/**` 与 `SubtitleWorkbench.vue` 以外的字幕弹窗组件 grep `#[0-9a-fA-F]{3,8}\b|rgba?\(`（沿用 2026-08-04 样式统一计划的口径）。

完成条件：前端测试全绿、`vite build` 通过；两个文件各不超过原行数 40%；拆分前后 `data-test` 钩子集合完全一致（脚本对比，基线存基线目录）；上述 grep 结果为零；暗色主题下人工核对可读；`AI-CONTEXT.md` 第 3 节目录说明更新。

> writes: `frontend/src/components/VideoListPage.vue`, `frontend/src/components/SettingsPage.vue`, `frontend/src/components/video-list/**`（新增子组件）, `frontend/src/components/settings/**`（新增子组件）, `frontend/src/components/*.test.js`, `frontend/src/styles/tokens.css`, `frontend/src/styles/components.css`, `frontend/scripts/*.mjs`（data-test 集合对比脚本）, `frontend/package.json`, `AI-CONTEXT.md`, `.loopx/workspace/2026-09-02-capability-batch/baseline/**`
> anchors: D-036、D-037（令牌部分）；AC-24、AC-25（令牌部分）；TC-12
> verify: `cd frontend && npm test`；`cd frontend && npx vite build`；`data-test` 集合前后 diff 为空；`wc -l` 对比基线；暗色截图人工核对
> review: Options API 抽组件后 `this.$refs` / 事件链是否断裂；既有 `data-test` 与 emit 事件名是否全部保留

## P-003 共享基础件与后台任务空闲调度

新增三个进程内基础件：`MediaWorkSlot`（容量 1，重 ffmpeg 任务共享）、`BackgroundTaskRegistry`（运行中任务登记，taskKey 固定集合，`background-tasks` 事件）、`IdleGate`（`ioreg` / `pmset` exec 探测，30 秒缓存，`Run` 门控与 `SetPauseHook` 项间检查点，`bypass` 立即运行）。全部既有长任务服务接入 `Begin/End` 登记（技术信息、pHash、清理分析、图片 EXIF、本地元数据补全与导出、视频与图片语义索引、AI 打标、图片 AI 打标、字幕队列、超分、备份），其中单 worker 类再接入可选暂停钩子；服务的 Start/Cancel/Status 签名不变；`runPostScanAutomation`（拆分后位于 `app_library.go`）、`SyncImageDirectories` 内的图片 EXIF / AI 自动触发（`app_image.go`）、`startup` 内的 AI 打标自动唤醒（`app.go`）等自动路径经门，用户显式启动不经门。README / GUIDE 平台支持小节补「空闲判定仅 macOS」。Settings 新增空闲调度五列（`idle_scheduling_enabled` 默认 true 需显式迁移），设置页新增「后台任务调度」分区，各任务面板增加「忽略空闲立即运行」。

顺带修一处既有测试脆弱点：`cleanup_service_test.go` 的 `TestCleanupBackgroundAnalysisKeepsStatusAfterCompletion` 与 `TestCleanupDoneProgressAppearsOnlyAfterAnalysisIsReadable` 用固定等待窗口等后台 worker 走到 done，在 Postgres 全量并发下会超时（P-011 验证时观察到，单跑通过）；本切片接入登记表与暂停钩子时把等待改为可注入的完成信号或按后端放宽窗口，不得删弱断言。

完成条件：门控单测覆盖等待、放行、bypass、取消、探测失败视为不空闲、跨午夜时间窗；集成测试证明自动 pHash 在活跃时等待而显式补全不等待；registry 用 `defer End` 无漂移；两后端迁移测试证明老库升级后 `idle_scheduling_enabled` 为 true。

> writes: `services/media_work_slot.go`, `services/background_task_registry.go`, `services/idle_gate.go`, `services/idle_probe_darwin.go`, `services/idle_probe_other.go`, `services/*_test.go`, `services/technical_backfill_service.go`, `services/perceptual_hash_service.go`, `services/cleanup_service.go`, `services/image_exif.go`, `services/image_ai_tagging_service.go`, `services/image_semantic_index_service.go`, `services/semantic_index_service.go`, `services/local_metadata_batch.go`, `services/local_metadata_export_worker.go`, `services/ai_tagging_service.go`, `services/subtitle_service.go`, `services/subtitle_queue.go`, `services/enhancement_service.go`, `services/backup_service.go`, `services/settings_service.go`, `models/video.go`, `database/database.go`, `database/*_test.go`, `app.go`, `app_library.go`, `app_image.go`, `app_tasks.go`, `app_settings.go`, `app_test.go`, `frontend/wailsjs/**`, `frontend/src/components/settings/**`, `frontend/src/components/*.test.js`, `README.md`, `GUIDE.md`, `AI-CONTEXT.md`
> anchors: D-007、D-014、D-030、D-031、D-032；AC-20、AC-21；TC-10
> verify: `go test -count=1 ./services ./database/...` 与 `CINEINSIGHT_TEST_PG_DSN=<dsn> go test -count=1 ./services ./database/...`（双后端各一次）；`go vet ./...`；`GOOS=windows go vet ./...`；`cd frontend && npm test`；`GOPRIVATE=none GOSUMDB=off GOFLAGS= GOPROXY=https://goproxy.cn,direct wails generate module` 后绑定 diff 只含本切片新增成员
> review: 显式启动路径是否被误门控；暂停钩子是否可能让 worker 在持锁状态下阻塞；默认 true 布尔列迁移是否覆盖「表已存在、列新建」的老库

## P-004 桌面通知与 Dock 角标

`services/desktop_notify_darwin.go`（cgo，`NSUserNotificationCenter` + `NSApp.dockTile`）与 `desktop_notify_other.go` 空实现；角标由 `BackgroundTaskRegistry` 变化驱动。前端在 `focus` / `blur` / `visibilitychange` 时调用新方法 `SetWindowForeground(bool)`；前台不发系统通知。角标由 P-003 的登记表驱动，本切片只做 `setDockBadge` 接线。触发清单固定为设计 D-013 所列的长任务终态；本切片接入其中已存在的任务（字幕、超分、备份失败、视频与图片语义索引），后续切片各自接入自己的终态。README / GUIDE 平台支持小节补「通知与角标仅 macOS」。Settings 新增 `desktop_notifications_enabled`（默认 true，显式迁移），设置页新增开关并标注「仅 macOS」。

完成条件：notifier 以小接口注入、单测用 stub 断言触发与前台抑制；`GOOS=windows` / `GOOS=linux` vet 通过；真机后台切出时字幕完成弹通知、两个任务运行时角标为 2、结束后清空。

> writes: `services/desktop_notify.go`, `services/desktop_notify_darwin.go`, `services/desktop_notify_darwin.m`（若需要）, `services/desktop_notify_other.go`, `services/desktop_notify_test.go`, `services/background_task_registry.go`, `services/subtitle_service.go`, `services/enhancement_service.go`, `services/backup_service.go`, `services/semantic_index_service.go`, `services/image_semantic_index_service.go`, `models/video.go`, `database/database.go`, `database/*_test.go`, `app.go`, `app_settings.go`, `frontend/wailsjs/**`, `frontend/src/App.vue`, `frontend/src/App.test.js`, `frontend/src/components/settings/**`, `README.md`, `GUIDE.md`, `AI-CONTEXT.md`
> anchors: D-012、D-013、D-014（角标接线）；AC-08、AC-09；TC-04
> verify: `go test -count=1 ./services ./database/...` 与 `CINEINSIGHT_TEST_PG_DSN=<dsn> go test -count=1 ./services ./database/...`（双后端各一次）；`GOOS=windows go vet ./...`；`GOOS=linux go vet ./...`；`cd frontend && npm test`；`GOPRIVATE=none GOSUMDB=off GOFLAGS= GOPROXY=https://goproxy.cn,direct wails generate module` 后绑定 diff 只含本切片新增成员；macOS 真机：后台切出触发一次字幕完成通知并观察角标
> review: 首个 cgo 文件是否只在 darwin 参与编译；主线程调度是否正确（`dispatch_async` 到 main queue）；`wails build` 签名流程是否受影响

## P-005 播放历史流水表与洞察改读

新增 `play_events` 模型与迁移，`ApplySchema` 内幂等回填 `legacy` 事件；在 `dispatchFormalPlayback`（桌面播放/随机，播放器启动成功后的统计更新）与 `ShortFeedService.RecordPlayback` 视频分支写事件：计数更新与事件插入放进同一个数据库事务，事务失败沿用今天的语义——只记日志、播放结果仍为成功、计数与事件都不写，两者永远一致；`libraryWatchHeatmap` 改读事件并在 Go 侧按本地日期归并；`services.LibraryStats`（`GetLibraryInsights` 的返回类型）增加 `total_play_events`、`plays_by_source`，洞察页展示；`ALGORITHM.md` 注明账本不参与算法。计数列与随机算法零改动。

完成条件：双后端测试证明三处各写一条对应 source 的事件、内嵌预览不写、播放分发失败不写、统计事务失败时计数与事件同时缺失且播放结果仍为成功；legacy 回填幂等；同一视频两天各播一次热力图两日各计数；随机分数序快照与改动前一致。

> writes: `models/play_event.go`（新增）, `models/schema.go`, `database/database.go`, `database/*_test.go`, `services/video_playback.go`, `services/short_feed_service.go`, `services/library_stats_service.go`, `services/*_test.go`, `app_library.go`, `frontend/wailsjs/**`, `frontend/src/components/InsightsPage.vue`, `frontend/src/components/insights/**`, `frontend/src/components/*.test.js`, `ALGORITHM.md`, `AI-CONTEXT.md`
> anchors: D-008、D-009、D-010、D-011；AC-05、AC-06、AC-07；TC-03
> verify: `go test -count=1 ./services ./database/...` 与 `CINEINSIGHT_TEST_PG_DSN=<dsn> go test -count=1 ./services ./database/...`（双后端各一次），含随机分数序快照用例（基线快照存基线目录）；`cd frontend && npm test`；`GOPRIVATE=none GOSUMDB=off GOFLAGS= GOPROXY=https://goproxy.cn,direct wails generate module` 后绑定 diff 只含本切片新增成员
> review: 事件写入是否与计数递增在同一事务且失败同回滚、播放结果语义未变；热力图日期归并口径是否与前端本地日期轴一致（沿用 SQLite 设计 D-002 的教训）

## P-006 兼容性转封装代理

新增 `PlaybackProxyService` 与 `video_playback_proxies`：显式单视频与批量触发、`AutoCompatibilityProxy` 自动路径经 IdleGate、每项经 MediaWorkSlot；策略按技术快照选 remux 或 VideoToolbox 转码（≤1080p、不放大）；产物写临时目录校验后 rename 到 `~/.CineInsight/proxies/`；源指纹变化即失效删除；视频记录软删除（`DeleteVideo` 进回收站）或永久删除时都连带删除代理文件与表行；`ProxyCacheLimitBytes` 默认 50 GiB、LRU by `last_used_at`（60 秒保护、写入节流）。`GetPreviewSession` 与手机端 `ResolveMedia` 在有效代理存在时换源，路由路径形态与统计语义不变。前端：详情抽屉与行菜单入口、批量与结果条入口、设置页「播放代理」分区（占用、数量、上限、清空全部、立即整理）以及详情抽屉内的「删除此代理」。批量完成触发桌面通知。README / GUIDE 平台支持小节补「VideoToolbox 转码仅 macOS」。

完成条件：ffmpeg 以 stub 注入，单测断言 remux / transcode 参数数组与临时→最终落位；指纹变化用例证明失效删除并退回无代理行为；LRU 用例证明调低上限后下一次写入淘汰最早使用者且跳过 60 秒内使用者；「清空全部代理」后目录为空且表清空；按视频删除只删该视频代理；`DeleteVideo` 软删除后代理被删；`PlayVideo` 路径断言不引用代理；真机对一个 H.264/AAC 的 mkv 与一个 avi 各演练一次并在预览与手机端确认。

> writes: `services/playback_proxy_service.go`, `services/playback_proxy_*.go`, `services/playback_proxy_*_test.go`, `services/preview_service.go`, `services/short_feed_service.go`, `services/video_*.go`（删除级联）, `models/video.go`, `models/schema.go`, `database/database.go`, `app_video.go`, `app_settings.go`, `frontend/wailsjs/**`, `frontend/src/components/PreviewDrawer.vue`, `frontend/src/components/VideoListRow.vue`, `frontend/src/components/video-list/**`, `frontend/src/components/settings/**`, `frontend/src/components/*.test.js`, `docs/short-feed-lan.md`, `README.md`, `GUIDE.md`, `AI-CONTEXT.md`
> anchors: D-001、D-002、D-003、D-004、D-005、D-006；AC-01、AC-02、AC-03、AC-04；TC-01、TC-02
> verify: `go test -count=1 ./services ./database/...` 与 `CINEINSIGHT_TEST_PG_DSN=<dsn> go test -count=1 ./services ./database/...`（双后端各一次）；`cd frontend && npm test`；`GOPRIVATE=none GOSUMDB=off GOFLAGS= GOPROXY=https://goproxy.cn,direct wails generate module` 后绑定 diff 只含本切片新增成员；macOS 真机 mkv / avi 各一次并核对 `videos` 行数不变
> review: 代理是否可能被任何路径当作片库记录或写入用户目录；淘汰是否可能触碰源文件；正式播放是否仍打开源文件；`inlinePreviewMIMEs` 白名单是否被改动

## P-007 建议作品集

新增 `CollectionSuggestionService`、`collection_suggestions` 与成员表：文件名去噪后按五种剧集模式解析，`(scan_root, normalized_series)` 分组、成员 ≥ 2、排除已入集视频、以成员 fingerprint 记忆 confirmed / dismissed；确认走事务复用 `CreateCollection`（同名活跃集则加入）、`AddCollectionVideos`、`ReorderCollectionVideos`，绝不改标题与文件名。`AutoCollectionSuggestions` 默认关并经 IdleGate。前端在清理中心同级新增「建议作品集」面板（成员预览、可去掉成员、改名、确认、忽略），工具栏任务菜单增加「分析剧集」显式入口。

完成条件：表驱动测试覆盖五种模式、去噪、同集多版本、跨季、全角数字、单文件不成组；确认事务断言作品集顺序与集号一致且标题文件名不变；忽略后成员不变不再出现、成员变化重新出现；面板组件测试覆盖确认、忽略、去成员与空态。

> writes: `services/collection_suggestion_service.go`, `services/collection_suggestion_parser.go`, `services/collection_suggestion_*_test.go`, `models/media_details.go`, `models/schema.go`, `database/database.go`, `app_media_details.go`, `app_settings.go`, `frontend/wailsjs/**`, `frontend/src/components/CollectionSuggestionPanel.vue`（新增）, `frontend/src/components/video-list/**`（入口）, `frontend/src/components/settings/**`, `frontend/src/components/*.test.js`, `AI-CONTEXT.md`
> anchors: D-023、D-024、D-025；AC-15、AC-16；TC-07
> verify: `go test -count=1 ./services ./database/...` 与 `CINEINSIGHT_TEST_PG_DSN=<dsn> go test -count=1 ./services ./database/...`（双后端各一次）；`cd frontend && npm test`；`GOPRIVATE=none GOSUMDB=off GOFLAGS= GOPROXY=https://goproxy.cn,direct wails generate module` 后绑定 diff 只含本切片新增成员
> review: 无

## P-008 帧哈希序列与截取片段识别

新增 `FrameHashService` 与 `video_frame_hash_sequences`：单趟 ffmpeg 抽 2 秒间隔灰度小图、Go 侧 dHash 成 uint64 序列，单 worker、经 MediaWorkSlot、指纹跳过、可取消可续，`AutoFrameHashSequence` 默认关。`CleanupService` 新增 `ClipGroups`：时长预筛、16 帧粗筛偏移、全量验证（汉明 ≤10、命中 ≥0.70）、排除精确重复对与 `clip_dismissals`。前端清理中心新增「截取片段」类别：并排 A/B、偏移与命中率、默认建议保留较长者、默认不勾选、删除走回收站、忽略。旧三类计算路径与结果不变。

完成条件：合成序列 fixture（A、A 的中段截取 B、无关 C）钉住阈值且 C 不出现；旧三类结果快照不变；清理审阅面板组件测试覆盖新类别的默认不勾选、保留建议与忽略；回填基准两小时片 ≤ 3 分钟（真机一次）。

> writes: `services/frame_hash_service.go`, `services/frame_hash_*_test.go`, `services/clip_match.go`, `services/clip_match_test.go`, `services/cleanup_service.go`, `services/cleanup_service_test.go`, `models/media_details.go`, `models/schema.go`, `database/database.go`, `app_cleanup.go`, `app_settings.go`, `frontend/wailsjs/**`, `frontend/src/components/video-list/**`（清理审阅面板）, `frontend/src/components/settings/**`, `frontend/src/components/*.test.js`, `AI-CONTEXT.md`
> anchors: D-026、D-027、D-028；AC-17、AC-18；TC-08
> verify: `go test -count=1 ./services ./database/...` 与 `CINEINSIGHT_TEST_PG_DSN=<dsn> go test -count=1 ./services ./database/...`（双后端各一次）；`cd frontend && npm test`；`GOPRIVATE=none GOSUMDB=off GOFLAGS= GOPROXY=https://goproxy.cn,direct wails generate module` 后绑定 diff 只含本切片新增成员；真机一次回填基准
> review: 新类别是否默认不勾选且删除只走回收站；旧三类路径是否被触碰

## P-009 命令面板

新增 `frontend/src/utils/commandRegistry.js` 与 `CommandPalette.vue`（基于 `BaseModal`）：⌘K / Ctrl+K 打开、Esc 关闭、方向键与回车；导航组（页面、智能视图、保存视图、作品集、人物）、动作组（扫描、随机、立即备份、打开设置分区）、任务组（读取 `background-tasks` 事件与空闲门状态，启动/取消/立即运行）、视频组（输入 ≥ 2 字调用既有 `SearchLibraryVideoPage` 限 8 条，回车打开详情抽屉）。各页面在挂载/卸载时注册与注销。不新增顶栏按钮、不改现有快捷键与 DOM。

完成条件：Vitest 覆盖注册/注销、过滤与权重、回车执行、Esc、输入框内 ⌘K 仍生效、面板关闭时不拦截既有 J/K 等键；现有页面 `data-test` 钩子集合与基线一致（复用 P-002 的对比脚本），既有页面测试全部通过。

> writes: `frontend/src/utils/commandRegistry.js`, `frontend/src/utils/commandRegistry.test.js`, `frontend/src/components/CommandPalette.vue`, `frontend/src/components/CommandPalette.test.js`, `frontend/src/App.vue`, `frontend/src/App.test.js`, `frontend/src/components/VideoListPage.vue`, `frontend/src/components/PhotoLibraryPage.vue`, `frontend/src/components/EntityLibraryPage.vue`, `frontend/src/components/InsightsPage.vue`, `frontend/src/components/SettingsPage.vue`（仅注册调用）, `AI-CONTEXT.md`, `GUIDE.md`
> anchors: D-029；AC-19；TC-09
> verify: `cd frontend && npm test`；`cd frontend && npx vite build`
> review: 无

## P-010 字幕翻译术语表与滑动窗口上下文

新增 `translation_glossary_entries`（`scope_key` 物化唯一）与 `TranslationGlossaryService`（CRUD、按视频所属作品集解析生效集、作品集级覆盖全局级、多集冲突取最新并计数）；`OpenAICompatibleSubtitleTranslator` 实现可选 `ContextualTranslator`，提示词新增「术语表（只列命中本批原文的条目）」与「上文（只读勿翻译）」两段；字幕生成与工作台选区重译两处循环维护前一批尾部 5 条；DeepL 实现与请求体不变，设置页在 DeepL 下提示「术语表不适用」。前端：设置页「字幕翻译」分区维护全局表；作品集详情维护作品集表。

完成条件：翻译器 stub 捕获请求体，断言术语区块、覆盖规则、前批 5 条与条数校验以本批为准；DeepL stub 请求体与改动前逐字节一致（改动前先新增 DeepL 请求体测试并把结果存基线目录，今天仓库没有这条测试）；两个调用点都覆盖；术语表编辑组件测试覆盖增删改与 DeepL 提示。

> writes: `services/translation_glossary_service.go`, `services/translation_glossary_service_test.go`, `services/subtitle_translation.go`, `services/subtitle_translation_test.go`, `services/subtitle_service.go`（翻译循环）, `services/subtitle_workbench.go`, `services/*_test.go`, `models/media_details.go`, `models/schema.go`, `database/database.go`, `app_subtitle.go`, `frontend/wailsjs/**`, `frontend/src/components/settings/**`, `frontend/src/components/PreviewDrawer.vue`（作品集详情区块）, `frontend/src/components/EntityLibraryPage.vue`, `frontend/src/components/*.test.js`, `AI-CONTEXT.md`
> anchors: D-033、D-034、D-035；AC-22、AC-23；TC-11
> verify: `go test -count=1 ./services ./database/...` 与 `CINEINSIGHT_TEST_PG_DSN=<dsn> go test -count=1 ./services ./database/...`（双后端各一次）；`cd frontend && npm test`；`GOPRIVATE=none GOSUMDB=off GOFLAGS= GOPROXY=https://goproxy.cn,direct wails generate module` 后绑定 diff 只含本切片新增成员
> review: DeepL 路径是否零改动；上下文条目是否可能被计入返回条数校验

## P-011 人物覆盖图片（image_people）

新增 `image_people` 对称表；`PersonService` 增加 `AddPersonImages` / `RemovePersonImage` / `SetImagePeople`，`PersonDetail` 增加 `images` 与 `next_image_id` 独立分页；最后关系清理判定改为视频与图片关系皆空；`ImageFilter` 增加 `person_ids`（AND）。前端：照片页人物组合框筛选（与标签组合框同交互）、单图详情人物维护、人物详情「视频」「图片」两个区块。NFO 导入不删人物的例外不变；作品集不覆盖图片。

完成条件：双后端测试覆盖筛选、分页、清理判定（仅图片关系剩余时不清理；两者皆空才确认清理）；前端组件测试覆盖筛选与双区块。

> writes: `models/media_details.go`, `models/schema.go`, `database/database.go`, `database/*_test.go`, `services/person_service.go`, `services/person_service_test.go`, `services/video_detail_service.go`（第三条孤儿人物清理路径，实施中发现必须改）, `services/image_library_service.go`, `services/image_library_service_test.go`, `database/image_people_schema_test.go`（新增）, `app_media_details.go`, `app_image.go`, `frontend/wailsjs/**`, `frontend/src/components/PhotoLibraryPage.vue`, `frontend/src/components/PhotoLibraryPage.test.js`, `frontend/src/components/EntityLibraryPage.vue`, `frontend/src/components/PreviewDrawer.vue`, `frontend/src/components/*.test.js`, `AI-CONTEXT.md`
> anchors: D-015、D-021；AC-13；TC-06
> verify: `go test -count=1 ./services ./database/...` 与 `CINEINSIGHT_TEST_PG_DSN=<dsn> go test -count=1 ./services ./database/...`（双后端各一次）；`cd frontend && npm test`；`GOPRIVATE=none GOSUMDB=off GOFLAGS= GOPROXY=https://goproxy.cn,direct wails generate module` 后绑定 diff 只含本切片新增成员
> review: 最后关系清理语义是否对两种媒体一致且不误删仍有图片关系的人物；`video_people` 既有行为是否零改动

## P-012 人脸运行时与分析

新增 `FaceRuntime`（对齐 WhisperX：托管 Python、venv、pinned `onnxruntime` + `insightface`、`buffalo_l` 模型显式下载、`FaceModelMirrorURL`、manifest sha256、状态机 `available / missing_* / download_failed / incompatible`）、embed 的 `face_worker.py`（stdin/stdout JSON 行协议）、`FaceAnalysisService`（候选媒体按源指纹、视频复用 `planAITaggingFramePositions` 抽帧、图片用 `ResolveImageView` JPEG、经 MediaWorkSlot、写 `face_observations` 与裁剪图、增量聚类 0.55、簇代表、`no_face` 标记；吸收进已命名簇的新观测置 `append_status=pending`，本切片只建列与置值，不写关系）、三张表（`face_observations` 含 `append_status` 列）、`/preview/face-crop/{id}` 路由、`ClearFaceData` 与用量、`AutoFaceAnalysis`（默认关、经 IdleGate、运行时不可用静默跳过并报因）。设置页「人脸识别」分区含隐私披露，并承载分析的启动/取消入口（运行时不可用时置灰并说明原因）；命令面板任务组经 P-003 登记表自然获得该任务。视频列表与照片页不加独立入口（设计 V1.0.1 收窄）。README / GUIDE 平台支持小节补「人脸 sidecar 仅 macOS」。

完成条件：sidecar 以假 worker（固定向量）注入，单测覆盖聚类归并/新建、`no_face`、指纹跳过、interrupted 续跑、`ClearFaceData` 不动 `people` 三表；运行时状态机与下载失败路径单测；真机执行 `PrepareFaceRuntime` 成功并对含人脸的样片与照片各分析一次。

> writes: `services/face_runtime.go`, `services/face_runtime_manifest.go`, `services/face_worker.py`, `services/face_analysis_service.go`, `services/face_cluster.go`, `services/face_*_test.go`, `models/face.go`（新增）, `models/schema.go`, `database/database.go`, `preview_asset_handler.go`, `app_ai.go`, `app_settings.go`, `models/video.go`（Settings 两列）, `frontend/wailsjs/**`, `frontend/src/components/settings/**`, `frontend/src/components/*.test.js`, `scripts/*`（若需运行时构建脚本）, `AI-CONTEXT.md`, `README.md`, `GUIDE.md`
> anchors: D-016、D-017、D-018、D-020、D-022；AC-10、AC-14；TC-05（运行时与分析部分）
> verify: `go test -count=1 ./services ./database/...` 与 `CINEINSIGHT_TEST_PG_DSN=<dsn> go test -count=1 ./services ./database/...`（双后端各一次）；`cd frontend && npm test`；`GOPRIVATE=none GOSUMDB=off GOFLAGS= GOPROXY=https://goproxy.cn,direct wails generate module` 后绑定 diff 只含本切片新增成员；macOS 真机 `PrepareFaceRuntime` + 一次分析
> review: 是否存在显式下载之外的网络出口；向量与裁剪图是否可能进入日志或外发；模型来源 URL 与 sha256 是否固定；Apple Silicon 上 onnxruntime 实际可用性需真机证据

## P-013 人脸审阅与人物候选

新增 `FaceReviewService`：`ListFaceClusters`、`NameFaceCluster`（事务建人物 + 按媒体去重写 `video_people` / `image_people` + 簇 `named`）、`LinkFaceCluster`、`IgnoreFaceCluster`；人物头像种子向量与 `face_person_candidates`（≥0.6）；已 `named` 簇在后续分析吸收到新观测时**不自动写关系**，而是生成「追加候选」（该人物 + 新增媒体）进同一 section，确认后才写——任何路径都不自动写 `video_people` / `image_people`（requirements D-4f，设计 V1.0.1）。追加候选由 `face_observations.append_status` 承载（P-012 建列），本切片提供 `ConfirmFaceClusterAppend` / `DismissFaceClusterAppend`。前端：视频侧 `AITagReviewDialog` 新增「人物候选」section，图片侧对应面板；簇卡片显示代表裁剪图、观测数、涉及媒体数、候选人物；未命名簇三个动作（命名、关联、忽略），已命名簇的追加候选两个动作（确认追加、忽略）。分析完成/失败触发桌面通知。

完成条件：事务测试覆盖命名（新人物 + 两表关系）、关联、忽略、追加候选确认、并发命名冲突、人物删除后簇回 `unnamed`；任何未经用户动作的路径都不写两表（含 named 簇吸收新观测）；前端组件测试覆盖五个动作（命名、关联、忽略、确认追加、忽略追加）与空态。

> writes: `services/face_review_service.go`, `services/face_review_service_test.go`, `services/face_analysis_service.go`（named 簇吸收标记 pending）, `models/face.go`, `app_ai.go`, `frontend/wailsjs/**`, `frontend/src/components/AITagReviewDialog.vue`, `frontend/src/components/AITagReviewDialog.test.js`, `frontend/src/components/FaceClusterReviewPanel.vue`（新增）, `frontend/src/components/ImageAITagReviewPanel.vue`, `frontend/src/components/*.test.js`, `AI-CONTEXT.md`
> anchors: D-019、D-015（写入部分）；AC-11、AC-12；TC-05（审阅部分）
> verify: `go test -count=1 ./services ./database/...` 与 `CINEINSIGHT_TEST_PG_DSN=<dsn> go test -count=1 ./services ./database/...`（双后端各一次）；`cd frontend && npm test`；`GOPRIVATE=none GOSUMDB=off GOFLAGS= GOPROXY=https://goproxy.cn,direct wails generate module` 后绑定 diff 只含本切片新增成员；真机对真实簇命名一次并在人物详情核对两区块
> review: 是否存在任何未经用户动作写 `video_people` / `image_people` 的路径（包括 named 簇吸收新观测）；是否提供了「全部接受」之类的批量自动化

## Integration And Final Verification

> **2026-09-03 执行结果**：`gofmt -l $(git ls-files -co --exclude-standard '*.go' | grep -v '^search/')` 为空；`go vet ./...`、`GOOS=windows`、`GOOS=linux` 通过；`go build ./...` 通过（含 cgo 通知文件）；`go test -count=1 ./...` 8 包 ok（SQLite）；`cd frontend && npm test` 43 文件 521 测试 + `data-test` 守卫（188 基线全在，现 326）；`npx vite build` 通过；`wails generate module` 后与 P-001 基线 diff：0 个既有导出方法被删，新增 45 个方法。**Postgres 全量**：2026-09-03 用户重启 OrbStack 后，单容器（`postgres:16`，`fsync=off`）`CINEINSIGHT_TEST_PG_DSN=… go test -count=1 -timeout 40m ./...` **8 包全部 ok、EXIT=0**（`services` 91 s、`database` 19 s、`migrator` 14 s），容器已删除。至此本批全部自动化门在最终合并树上双后端全绿。真机验收项见 Handoff。

- 全量：`gofmt -l $(git ls-files -co --exclude-standard '*.go' | grep -v '^search/')` 为空；`go vet ./...`；`GOOS=windows go vet ./...`；`GOOS=linux go vet ./...`；`go test -count=1 ./...` 与 `CINEINSIGHT_TEST_PG_DSN=<dsn> go test -count=1 ./...`；`cd frontend && npm test && npx vite build`；`wails generate module` 后绑定 diff 只含本批新增方法与字段。
- D-038：新表全部出现在 `models.AllModels()`；双后端测试基座覆盖每张新表；双向迁移器往返测试包含 BLOB 列。
- D-039：独立评审 grep 本批新增的 `http.` 客户端与 `exec.Command` 调用，逐一对照「仅人脸模型显式下载」与「ioreg / pmset / ffmpeg / ffprobe」。
- 真机验收（无法单测替代）：TC-01 / TC-02（mkv、avi 代理与手机端）、TC-04（通知与角标）、TC-05（模型下载、真实人脸、命名后两区块）、TC-08（回填基准）、TC-10（空闲等待与立即运行）、TC-12（暗色截图）。
- 文档：`AI-CONTEXT.md` 各章节由对应切片补齐；`README.md` / `GUIDE.md` 平台支持小节覆盖通知、角标、空闲判定、VideoToolbox、人脸 sidecar；设计文档「一、修订历史」记录实施期任何被证据推翻的结论；`docs/loopx/plans/2026-08-04-frontend-style-unification.md` 残留清单对应条目关闭。
- 记忆：完成后更新项目记忆（人物覆盖图片的新裁决、代理与 LRU 裁决、cgo 首例、人脸运行时位置）。

## Handoff And Residual Risks

- Blockers（2026-09-03 收尾时）：① ~~Postgres 全量~~ 已补跑全绿；② 真机验收待用户：TC-04 通知与角标、转封装 GUI 与手机实看、设置页「准备人脸运行时」（约 700 MB，需镜像）与真实人脸分析/审阅、TC-12 暗色截图。以下为开工时的记录——2026-09-02 用户裁决：推断默认全部接受、不先提交未提交文件、不走 design-review，直接开工；执行模式为 Opus 子代理并行实现、主会话调度与审查（clarification Round 4 / CD-06）。
- 执行记录（2026-09-02）：P-001 完成并经主会话独立复验（gofmt 门、双平台 vet、全量测试、绑定快照 diff 为空、与 HEAD 的逐行多重集比对）。P-001 顺带裁决：`downloadWhisperWindows` 死代码删除，`PrepareEngine` 与 `downloadFFmpeg` 的非 darwin 分支统一返回 `ErrSubtitleDependencyUnsupportedPlatform`（Windows 专属文案变化，属 D-037 意图）。并行执行协议：当两个切片共享 `models/schema.go`、`database/database.go`、`frontend/wailsjs/**` 时，各自只做追加式最小插入、编辑前重读、不重排既有行，`wails generate module` 只在切片末尾跑一次；`AI-CONTEXT.md` / `README.md` / `GUIDE.md` 由主会话统一集成，子代理只回报增量。
- 执行记录（2026-09-03，收尾）：独立评审最终确认 N-1（三重证据：改序、真实迁移器复现转绿、守卫测试对 45 条有序约束边全部通过且能单独拦住回退）、#7、#4 全部修复，无新 Critical/Important；P-007 测试侧竞争修复后 `-race` 门覆盖全部新任务分组四包全绿。评审提醒：本轮触及的 FK 强制、`frame_ms not null default -1`、BLOB 往返都是方言敏感项，Docker 恢复后的那一遍 Postgres 全量是这批真正的最后一道门。
- 执行记录（2026-09-03）：P-006 最后一轮完成：`Image` 提前到 `Video` 之后；迁移器夹具 `seedEveryTable` 每表至少一行并以「任一表为空即失败」自守；新增 `models/schema_test.go::TestAllModelsIsTopologicallyOrdered`（`schema.Parse` 解析关联，能单独抓出两处历史缺陷）；手机端 `ResolveMedia` 对非白名单且无可用代理的视频返回 `ErrShortFeedNoEligibleVideos`（404）不再回落源 MIME。P-006/P-008/P-012 标 done。最终合并树：gofmt/三平台 vet/`go build`、全量 Go 8 包（含新 `models` 测试）、前端 43 文件 521 测试、`vite build`、绑定重生成（0 个既有方法被删）全部通过；`-race` 抓出 P-007 一条**测试侧**数据竞争（登记表回调写、测试读无同步），已交回修复。
- 执行记录（2026-09-02，续十六）：P-013 完成并经主会话复验（`-race` 人脸测试全过；人脸代码里只有 `face_review_service.go` 出现关系表；面板三文件测试通过）。**运维事故**：并行三个 Postgres 容器 + 多代理反复编译把宿主磁盘写满到 1.1 GiB，Postgres 容器 PANIC、OrbStack Docker 守护进程掉线（用户的 `sub2api*` 容器随之停止）；主会话已 `go clean -cache` 与清理中断测试留下的 `go-build*` 临时目录，释放约 18 GiB（现约 23 GiB 空闲），未动任何用户数据。**后果**：「Postgres 全量 `./services` 在最终合并树上一次全绿」这条证据缺失（各切片在各自时点的 Postgres 全量都过了，最终树只补到 `-run Face` 与 `./database/...`），需用户重启 OrbStack 后补跑；P-006 的最后一轮（N-1/#7）只能用 SQLite 验证。
- 执行记录（2026-09-02，续十五）：评审复核 6/6 Important 确认修复；机械核对 48 条模型关联又抓出 **N-1（Important，既有缺陷）**：`AllModels()` 里 `ShortFeedImageInteraction` 排在 `Image` 之前，双向迁移器复制撞外键——用户只要在手机端看过一张图片，已上线（P-007，commit 5b18d52）的数据库后端切换就会中途失败并留下进行中标记。修法：改序 + 迁移器夹具每表至少一行 + 新增拓扑有序守卫测试，交 P-006 代理；#4 人脸侧目录守卫交 P-012；#7 手机端失效代理回落交 P-006。
- 执行记录（2026-09-02，续十四）：三切片评审项修复回报：P-008 补带扫描根的旧五类快照、路径擦除、`MatchClip` 传间隔、面板 `frame_hash` 绑定；P-012 事务内重算簇计数、`frame_ms` 哨兵 `-1`、披露文案与响应头超时、`Lstat` 拒软链、解释器探测 30 s 缓存；P-006 保护窗口 120 s + 陈旧强刷、长边 ≤1920、绝对流序号 map、路径擦除、`cancelled` 结果码、孤儿统计、Range/206 测试、迁移器往返 BLOB 断言。**#30 顺带抓出真缺陷**：`AllModels()` 里 `FaceObservation` 排在 `FaceCluster` 之前，双向迁移器复制会撞外键，已改序并写入 AI-CONTEXT 目录说明作为规则。主会话合并树复验：全量 Go 7 包、`-race` 五组任务测试通过。待评审复核 6 个 Important。
- 执行记录（2026-09-02，续十三）：P-006/P-008/P-012 独立评审 0 Critical / 6 Important / 24 Minor。Important：LRU 保护窗口与节流等长可淘汰正在播放的代理；转码只约束宽度（竖屏 4K 超 1080p）；`face_clusters.observation_count` 重分析后虚高；准备运行时的 PyPI/Python 出口未披露；迁移器往返测试对六张新表空转；#12 扫描根裁剪核实为开工前未提交改动（非 P-008），只补快照测试。全部分派回三位实现代理修复，待复验。
- 执行记录（2026-09-02，续十二）：P-006 / P-008 / P-012 三切片回报完成，各自 SQLite + Postgres 双后端全绿（Postgres `./services` 在三个并发测试进程下 627–759 s）；主会话合并树复验：Go 7 包、三平台 vet、前端 42 文件 501 测试全绿；三个服务确认共用 `app.mediaWorkSlot`；`runPostScanAutomation` 七个自动块齐全。三切片交独立评审（隐私/网络出口/文件删除范围/默认不勾选），通过后标 done。P-013 开工。裁决：P-006 接受合成 ffmpeg 样片 + 路由级集成作为 TC-01/02 证据（本机片库无 mkv/avi、外接卷未挂载），GUI 与手机实看留真机；接受 `BatchCreatePlaybackProxiesForFilter`、`ScanSyncResult.AddedVideoIDs`、`ErrPlaybackProxyStopping`；P-008 `stale_frame_hash_count` = 未回填 + 失效，一个片段只报一条候选，D-026 基准按分辨率分档（1080p ≤3 min 达标，4K HEVC 约 12 min 为软解瓶颈、`-hwaccel` 留后续）；P-012 走 insightface 1.0.1 纯 Python wheel（运行时约 700 MB 接受），托管 Python 3.10–3.13，被忽略的簇继续吸收新观测不再冒成新簇，孤儿观测由分析前对账清理（多态列无外键），命令面板给人脸分析补取消绑定。**真机待办**：用户在真实 `~/.CineInsight` 点一次「准备运行时」（直连 GitHub 不稳需镜像）；转封装 GUI 点一遍与手机浏览器实看；TC-04 通知与角标。
- 执行记录（2026-09-02，续十一）：P-007 方括号片名规则落地并复验（24 条表驱动用例），边界取「片名括号不是第一个方括号组」（`[2019][片名][05]` 也能取到片名，`[组名][05]` / `[05]` / `[2019][1080p][05]` 不成候选）。P-007 标 done。
- 执行记录（2026-09-02，续十）：P-007 完成（含 Postgres 全量 `./services` 1191 s 通过），主会话复验合并树全绿；裁决：`Confirm` 采用「每步自身原子 + 整体可重入」（SQLite `_txlock=immediate` 下嵌套事务必自锁，设计 7.3.2 字面「事务内」改写），`[字幕组][片名][05][1080p]` 类命名不产候选属真实覆盖缺口，D-023 规则补「集号由 `[NN]` 命中且系列名为空时取紧邻集号前的非标签方括号组」，P-007 代理只改解析器补齐。Postgres 一遍在 Docker 卷上 fsync 极慢，与前端套件并发会把 `./services` 拖过 10 分钟默认超时：后续统一 `-timeout 40m` 并可给一次性容器加 `-c fsync=off -c synchronous_commit=off`。P-006 / P-008 / P-012 并行开工，共享 `runPostScanAutomation`（`app_library.go`）、`models/video.go` Settings、`settings_service.go`、`AutomationSection.vue`、`SettingsPage.vue`、`schema.go`、`database.go`、绑定，按追加式协议；app 层方法分别落 `app_video.go` / `app_cleanup.go` / `app_ai.go`。**默认非零的数值设置列（如 `proxy_cache_limit_bytes`）同样禁止 gorm default 标签**（用户设 0=不限会被迁移器翻回默认）。
- 执行记录（2026-09-02，续九）：P-004 完成并经主会话复验（三平台 vet、`go build` 含 cgo、全量 Go、Postgres `./database/...`）；cgo 文件审读：AppKit 调用全部派发主队列、NSApp/通知中心判空、C 字符串复制后释放；通知中心在设置不可读时不发（fail-closed，与空闲门的 fail-open 有意相反）。裁决：通知文案不带失败原因（报错常含绝对路径）、恢复失败不扩入触发清单、开关放「基本设置」分区、挂载时同步一次前后台。**TC-04 真机验收（通知投递、角标 2→0、cgo 对 `wails build` 签名无影响）待用户在真机完成。** P-009 完成：⌘K 已归片库页清理审阅，命令面板改用 ⌘⇧P / Ctrl+Shift+P（设计 V1.0.9、requirements AC-19 已改，用户可否决）；任务组只列零参绑定；作品集/人物懒加载前 50 条为已知限制。前端全量套件在负载 >10 时出现 vitest 5 秒超时误报（单文件重跑通过），P-007 收尾后低负载复跑确认。
- 执行记录（2026-09-02，续八）：P-003 NEW-6 以钩子实例 sticky `detached` 标记闭合并经独立评审给出完整交错论证确认（无新 Important），epoch 代码移除；主会话 `-race -count=20` 复验通过。P-003 彻底关闭。
- 执行记录（2026-09-02，续七）：P-003 复验确认 NEW-1..5 修复，但 epoch 方案留下几条指令宽的残余窗口（NEW-6：`Release` 落在 worker 读完钩子指针与读 epoch 快照之间）；改为钩子实例上的 sticky `detached` 标记精确闭合，P-003 代理仅改 `idle_gate.go` 与测试，待复验。P-003 的 done 状态维持（缺陷只影响显式启动时的一项停顿，不影响依赖切片的 API）。
- 执行记录（2026-09-02，续六）：P-003 两轮修复后经独立评审确认 #1–#7 全部修复；修复引入的丢唤醒（`Release` 先于 `Wait` 注册）以 per-key release epoch 修掉并有可复现测试；主会话复验全量 Go、`-race -count=3`、前端 363 测试全绿。P-003 标 done。P-004 / P-007 / P-009 并行开工，共享文件（`models/video.go` Settings 列、`services/settings_service.go`、`database/database.go`、`frontend/src/App.vue`、`SettingsPage.vue`、`frontend/wailsjs/**`）按追加式协议。
- 执行记录（2026-09-02，续五）：P-003 独立评审 0 Critical / 7 Important / 13 Minor：装钩子与显式启动不原子（两处）、bypass 永久泄漏、探测缓存被单任务 ctx 取消污染、`IdleSchedulingEnabled` 的 `gorm:"default:true"` 会让双向迁移器把用户关掉的开关翻回 true（GORM Create 跳过零值）、`SetPauseHook(nil)` 唤不醒已阻塞的 worker、等待 goroutine 无上限堆积；已全部交回实现代理修复，待复验。**后续所有默认 true 的布尔设置列（P-004 的 `desktop_notifications_enabled` 等）禁止用 `default:true` 标签，只靠显式迁移 + 新库显式行。**
- 执行记录（2026-09-02，续四）：P-010 完成并经主会话复验（DeepL 请求体基线快照逐字节不变、术语解析失败在生成路径按翻译失败保留原文、工作台直接报错）；三项裁决：软删除作品集的术语条目留库不生效、`note` 上限 500 字符、术语表编辑区不受双语开关控制。P-003 完成待独立评审与两项收口：① `SyncScanDirectories` 后的 AI 打标唤醒也过门；② 修既有缺陷——`UpdateSettings` 从不保存四个 `auto_*` 开关（范围外缺陷修复，否则 AC-20 真机不可演练）。D-030 口径收窄：AI 打标 worker 自身的启动批次与 5 分钟 ticker 不装门，只有唤醒路径受门（设计 V1.0.6）。
- 执行记录（2026-09-02，续三）：P-002 完成：`VideoListPage.vue` 4874→1931、`SettingsPage.vue` 2041→492，13 个 `video-list/` 子组件 + 10 个 `settings/` 分区组件，字面色门零命中，`data-test` 守卫（188 基线全在，现 200）挂进 `npm test`，vitest glob 用 `src/components/*/*.test.js`（vitest 裸参数不是 glob、sh 无 globstar，`**` 不可用）。P-002 代理违反「不派生子代理」自行起了评审子代理（发现并修了 5 处真实回归：`isTagSelected`/`allVisibleSelected` 丢失、删除后收窄顺序、搜索框 `v-model` 丢输入法组词保护、设置页孤儿 `@media`）——结果有益但流程违规，后续切片提示词必须再次显式禁止。合并树整体复验：`npm test` 32/325、`go test ./...` 7 包 ok。P-002/P-005/P-011 标 done；P-003 与 P-010 并行开工（共享 `models/schema.go`、`database/database.go`、`frontend/wailsjs/**`，按追加式协议；P-003 的字幕任务登记只放 `subtitle_queue.go`，不碰 P-010 的 `subtitle_service.go`）。
- 执行记录（2026-09-02，续二）：P-011 独立评审 0 Critical / 2 Important / 10 Minor；Important 两项（AI-CONTEXT 更新、图片侧存活测试）与五项 Minor 已修并复验，三项有意选择记入设计 V1.0.4。P-011 在 Postgres 全量下暴露 `cleanup_service` 两条等待型用例超时（与 P-011 无关、单跑通过），已转为 P-003 待办。
- 执行记录（2026-09-02，续）：P-005 完成并经主会话复验（gofmt/vet/双后端测试、事务与回填 diff 审读）；设计 4.2.2「无额外日志」与 4.2.4「启动日志一行」的矛盾按「仅实际回填行数 > 0 时打一行」收口；`PlaysBySource` 用 `map[string]int64`（绑定只多两个字段）；热力图 tooltip「部」改「次播放」是口径更正。P-011 完成，Go 侧经主会话复验，破坏性路径另交独立评审。两者的前端全量套件因 P-002 并行改动暂时红在 P-002 名下的三个脚本，待 P-002 收尾后统一复跑。
- Residual risks：`clip_dismissals` 两个 video id 无外键，视频永久删除后忽略行惰性滞留（无害）；截取匹配粗筛是有意的非等价剪枝（12/16 > 0.70）；绝对路径擦除目前是 P-006 与 P-008 各自的本地小助手，后续可合并为一个共用函数。帧哈希回填对 4K HEVC 走软解约 6 s/分钟视频，两小时约 12 分钟，超出 D-026 的 ≤3 分钟（1080p 达标）；`-hwaccel videotoolbox` 为 macOS 专属优化，未做；截取匹配一次把全库有效序列读入内存（万级片库约 288 MB）。人脸运行时约 700 MB（insightface venv 517 MB + 模型 191 MB）；`FaceRuntime.Status()` 每次调用起 python 子进程问版本，未缓存；托管 Python 下载无超时无哈希（WhisperX 既有行为，人脸侧已接 ctx 可取消）。`cleanupDuplicateVideos`（`ApplySchema` 期硬删重复 path 行）会留下无主代理文件，出路是设置页「清空全部代理」。前端全量套件在机器高负载（load > 10）时会有 vitest 默认 5 秒超时的误报，单独重跑通过；未改超时配置，后续若持续困扰可在 `vitest.config.js` 提高 `testTimeout`。既有列 `short_feed_feedback_sync_enabled` 带 `gorm:"default:true"`，双向迁移器复制时会把用户关掉的值翻回 true（P-003 评审顺带发现，本批不动）；AI 打标 worker 的启动批次与周期 ticker 未受空闲门（D-030 收窄，见设计 V1.0.6）；清理分析的等待状态只在设置页可见，片库清理面板没有「等待空闲」提示；`go test ./...` 在高负载冷编译时出现过一次 `services` 包 FAIL 且未能复现（P-001 开工前观察到，与本批无关，需留意）；`wails generate module` 会把 `frontend/wailsjs/runtime/` 三个文件权限位改成 755（内容不变，纯噪音）；`services/video_service.go` 拆后 998 行已占 40% 预算的 95%，后续切片往里加代码要克制；`installWhisperMac` 是既有死代码，本批不清理；InsightFace + onnxruntime 在 Apple Silicon 的 wheel 与 CoreML EP 兼容性未经真机验证（P-012 首步即验证，失败则回 `spec` 讨论替代模型）；首个 cgo 文件对 `wails build` 签名流程的影响需 P-004 真机构建一次确认；Options API 巨型组件拆分依赖既有测试覆盖，覆盖薄弱处以 `data-test` 集合与手工核对兜底；帧哈希与聚类阈值初值需 fixture 校准，允许在 ±20% 内调整，超出回 `spec`；`NSUserNotificationCenter` 自 macOS 11 起标记废弃（设计有意推迟 `UNUserNotificationCenter`），若真机上不再投递则回 `spec` 改用后者。
- Resume note：基线目录 `.loopx/workspace/2026-09-02-capability-batch/baseline/` 已有 2026-09-02 的全量测试结果（Go 7 包 ok、前端全绿）、四文件行数与 `data-test` 集合；若工作树自那以后有变化，开工前重跑并覆盖；绑定快照、随机分数序、DeepL 请求体三项由对应切片在改动前自行补入；从 frontmatter 第一个非 `done` 的切片继续；并行与顺序只看 frontmatter；任何切片发现设计假设被推翻时先在设计文档追加修订历史再改代码。

## Execution rules for the consuming agent

- Execute slices in frontmatter dependency order; verify each slice with its `verify` line before starting dependents, and update its frontmatter `status` as work proceeds.
- Two slices may run in parallel only when neither depends on the other and their `writes` paths are disjoint; integrate results sequentially.
- Keep the frontmatter and body consistent: every frontmatter slice id has exactly one body section, every slice declares `depends` explicitly (an empty list asserts independence), and dependencies appear only in the frontmatter.
- Follow the installed working agreement for verification, review, stop, and Git discipline throughout.
