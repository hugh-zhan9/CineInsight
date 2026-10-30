# AI-CONTEXT.md: 析微影策核心上下文 (Single Source of Truth)

> 本文件是“析微影策 (Xī Wēi Yǐng Cè)”项目所有 AI 助手（Gemini, Claude, GPT 等）的权威上下文来源。

## 1. 项目架构与技术栈 (Architecture & Stack)

本项目是一个基于 **Wails v2** 的跨平台桌面视频管理系统，命名为“析微影策”，旨在通过 AI 分析与智能策略提供极致的本地影视管理体验。

- **后端 (Go 1.23+):**
  - **框架:** Wails v2 (负责桥接 Go 方法到前端、窗口管理、事件分发)。
  - **数据库:** 通过 **GORM** 支持 SQLite 与 PostgreSQL 两个后端，**默认 SQLite**（库文件 `~/.CineInsight/library.db`，`SQLITE_PATH` 可覆盖）。判定在 `database.ResolveBackend`：`DB_BACKEND` 显式给出时以它为准；未给出而配置了 `PG_HOST` 的老安装沿用 PostgreSQL；两者都没有时用 SQLite。设置页「数据库」分区可「迁移并切换」或「切回之前的后端（只改配置）」，切换只写数据目录下的 `.env`（`DB_BACKEND`、`PREVIOUS_BACKEND`），重启后生效；该文件加载顺序排在其他 `.env` 之前，进程环境变量仍最优先，进程环境里有 `DB_BACKEND` 时应用内切换一律返回 `backend_env_locked`。迁移并切换与只改配置成功后都不撤维护围栏，进入「待重启」终态（全局遮罩，唯一出口「立即重启」`RelaunchApp`），期间恢复、再次切换、只改配置、清空目标库一律返回 `relaunch_pending`，避免迁移后到重启前的写入落进旧库（D-PC55，APP-02）；目标库非空时预检区分半迁移与完整非空库，「清空目标库…」要输入「清空目标库」确认（2026-10-13 统一设计起，输入的就是按钮上的文字；发给后端的协议常量仍是「清空」）。语义检索（pgvector）只在 PostgreSQL 后端可用。（此处旧写法「PostgreSQL 作为当前主持久化存储」与代码不符，2026-09-30 产品完善度批次按代码更正，APP-13。）
  - **后端回落保护（2026-10-14，D-DBG-01..05，设计 `docs/loopx/design/2026-10-14-db-backend-guard/`）:** 每次库打开、`ApplySchema` 成功后把这次的后端原子写进数据目录下的 `backend-state.json`（0600，临时文件 fsync 后 rename 再 fsync 目录；`backend`、`location`、`updated_at`，PostgreSQL 另记 `postgres: {host, port, user, dbname, sslmode?, timezone?}`；永远不含密码；SQLite 记库文件路径；写失败只记日志；`ApplySchema` 失败不写）。启动时（`resolveStartupBackend` 首次调用，判定是纯函数 `backendFallbackBlockReason`）本次解析为 SQLite、加载完所有配置文件后 `DB_BACKEND` 仍为空，并且记录里上次是 postgres（`previous_postgres`），或者没有记录但本次的 SQLite 库文件不存在、数据目录里 `thumbnails` / `image-thumbnails` / `media-details` / `proxies` / `backups` / `faces` 任一目录非空（`inferred_postgres`，升级后第一次启动的缺口；`NewApp` 不预建这些目录，全新安装不会被拦）时，`Init` 在建目录与 `openBackend` 之前返回 `backend_fallback_blocked:` 错误，不打开、不创建 SQLite 库文件；显式 `DB_BACKEND=sqlite`（含应用内切换写下的）、上次 sqlite、本次 PostgreSQL 都放行，读不出的记录按没有记录处理；恢复备份后的重连沿用启动结果、不再判定。启动出错页按 `GetStartupBackendGuard().blocked` 换成专门说明：两种依据各自的说法、按优先级读过的配置文件及是否存在、启动环境里空 `PG_HOST` 挡住配置文件时的说明（`pg_host_empty_in_launch_env`），以及可复制的配置块 `config_snippet`（应用内切换写下的同一格式 `DB_BACKEND=postgres` + `PREVIOUS_BACKEND=sqlite` + 按记录预填的 `PG_*`，密码是占位；`navigator.clipboard` 不可用时选中整段），存成数据目录下的 `.env`（已有这个文件时替换全部内容，按 `data_dir_config_status` 说明；只贴进旧文件会被旧的 `DB_BACKEND=sqlite` 压过），按页上的引号规则（godotenv v1.5.1）填好密码后「重新打开」（`RelaunchApp`）；另一个出口「这次改用本机 SQLite…」（`confirmAction` 二次确认 → `AcceptSQLiteAfterFallbackBlock`：只在被拦下时可用，只把记录改成 sqlite 再走 `RelaunchApp`，不写任何 `.env`）；其他启动错误页面不变。缺 `PREVIOUS_BACKEND` 的数据目录 `.env` 照旧整份不采用（M-1；曾试过只忽略其中的后端键、其余照常加载，评审指出会改变现有安装打开哪个库，已撤回）。配置块存好、在 PostgreSQL 上跑起来之后设置页不误报「待重启」，「切回之前的后端」在本机没有 SQLite 片库时按 `target_empty` 拒绝。
  - **业务逻辑:** 封装在 `services/` 目录下（如 `VideoService`, `SubtitleService`, `TagService`, `DirectoryService`, `PreviewService`）。
- **前端 (Vue 3 + Vite):**
  - **UI 框架:** 原生 CSS + Vue 3 组合式 API (Composition API)。
  - **通信:** 通过 `wailsjs/go` 自动生成的绑定调用后端方法，使用 `wailsjs/runtime` 进行事件监听。
  - **绑定守卫（2026-09-30，P-040）:** `frontend/scripts/bindings-usage.test.mjs`（`npm test` 最后一环）双向检查：前端从 `wailsjs/go/main/App` 导入的每个名字都在 `App.d.ts` 里，`App.d.ts` 的每个导出都至少被前端非测试代码引用一次。新增 App 方法必须同时接上前端调用方；只给 Go 测试用的能力放在服务层，不要做成 App 方法。绑定用 `GOTOOLCHAIN=go1.24.9 wails generate module` 生成，不手改 `frontend/wailsjs/**`。
- **外部依赖 (Sidecars):**
  - **FFmpeg:** 用于提取视频音频流（16kHz, mono, WAV）。
  - **Apple 系统字幕（2026-10-12；2026-10-13 支持 macOS 15）:** Swift 辅助程序调用 Speech/Translation/NaturalLanguage，macOS 15+ 可用（macOS 26+ 用新接口，macOS 15 用 SFSpeechRecognizer 本机识别与隐藏窗口 translationTask）；资源由苹果系统管理，不安装字幕 Python/第三方模型。
  - **字幕翻译:** 仅用 Apple Translation；DeepL/OpenAI 兼容字幕翻译已退役（AI 打标自身的外部配置不变）。

## 2. 按领域读取当前合同

本文件仍是权威入口；以下领域文件是其组成部分。修改某领域前读取对应文件及其引用的设计。原节号保留以便查阅，文内明确标为历史/被取代的描述不能覆盖当前规则。

- [播放、队列与观看记录](docs/context/playback.md)
- [片库、扫描、清理与维护](docs/context/library.md)
- [标签、人物、作品集与片单](docs/context/metadata.md)
- [AI、字幕与场景检索](docs/context/ai-subtitles.md)
- [外部入口与桌面集成](docs/context/integrations.md)
- [视频工作台与帧分析](docs/context/workbench.md)
- [界面、导航与任务生命周期](docs/context/ui.md)

## 3. 关键目录说明 (Directory Structure)

- `/services`: **核心业务层**（Video, VideoDetail, MediaProbe, TechnicalBackfill, Person, Collection, Subtitle, SubtitleWorkbench, LibraryWatcher, LocalMetadata, AIQuality, Tag, Settings, Directory 服务）。
- 根目录 `app_*.go` 与 `/services/video_*.go`: **按领域拆分的同包多文件**（行为保持，签名与 Wails 绑定不变）。`app.go` 只留 `App` 结构体、构造、`startup`/`shutdown` 与日志，绑定方法分到 `app_video.go`、`app_library.go`（片库查询、保存视图、扫描同步与洞察）、`app_media_details.go`（视频详情、本地元数据、标签、人物、作品集）、`app_ai.go`（AI 打标、语义索引、同源关系）、`app_subtitle.go`、`app_cleanup.go`、`app_image.go`、`app_settings.go`（设置、备份恢复、后端切换、扫描目录与监听）、`app_tasks.go`（后台长任务的启动/状态/取消，含超分）；`VideoService` 拆为 `video_service.go`（核心 CRUD 与回收站）、`video_pagination.go`、`video_scan.go`、`video_rename_move.go`、`video_random.go`、`video_playback.go`。
- `/models`: **数据模型层**（GORM 结构体定义）。`schema.go` 的 `AllModels()` 顺序有约束：**被外键引用的表一律排在引用方之前**（双向迁移器按这份清单逐表复制，顺序错会撞外键），由 `models/schema_test.go::TestAllModelsIsTopologicallyOrdered` 用 `schema.Parse` 解析关联守住（曾抓出 `Image` 排在 `ShortFeedImageInteraction` 之后、`FaceCluster` 排在 `FaceObservation` 之后两处缺陷）；`database/migrator` 的往返测试夹具每表至少一行，任一表为空即失败。
- `/database`: **持久化层**（数据库连接、迁移与初始化）。
- `/frontend/src/components`: **UI 组件**（Vue 组件）。
  - `video-list/`：`VideoListPage.vue` 拆出的 15 个子组件（工具栏、批量操作栏、清理审阅面板、字幕生成/预览/翻译弹窗、超分弹窗、重命名与保存视图弹窗、后台任务状态条、任务失败明细、增量扫描条、语义提示条、随机批次条、删除撤销条）与 3 个共用模块（`runtimeEvents.js` 事件注册 mixin、`format.js`、`wheelForwarding.js`）。父子只经 props / emit / 父→直接子 `$refs`；需要保持 `await` 顺序的片库侧动作用函数 prop 回调。
  - `settings/`：`SettingsPage.vue` 按分区拆出的 16 个子组件（`SETTINGS_SECTIONS` 共 20 个锚点，每项带 `saveMode`：即时生效 / 需保存 / 部分需保存，扫描目录排第 2 位，D-PC57 / D-PC58）；父组件只留锚点导航、`settingsForm`、`saveSettings` 与未保存状态（`dirty`，切页前由 `App.vue` 确认「放弃未保存的设置修改？」），分区通过 `form` prop 改字段。（旧写法「10 个 / 13 个」已过时，2026-09-30 按代码更正，APP-13。）
  - `frontend/src/utils/idleScheduling.js` 收口后台任务名与等待原因的中文口径，设置页与状态条共用。
  - `frontend/src/utils/{commandRegistry,taskCommands,appCommands}.js` 与 `frontend/src/components/CommandPalette.vue`：命令面板（见 2.21）。
  - `services/desktop_notify*.go`：桌面通知与 Dock 角标，`_darwin.go` 使用 cgo（见 2.20）；另一个 cgo 入口为 FSEvents 的 `library_watcher_backend_darwin.go`（见 2.14）。
  - `services/playback_proxy_*.go` + `models/playback_proxy.go`：播放代理（见 2.23）；`services/frame_hash_service.go`、`clip_match.go`、`clip_cleanup.go` + `models/frame_hash.go`：帧哈希与截取片段（见 2.24）；`services/face_*.go` + `native/apple-ai/vision.m` + `models/face.go`：人脸运行时与分析（见 2.25）；`services/collection_suggestion_*.go`：建议作品集（见 2.22）。
  - `frontend/src/components/settings/`：分区组件见上一条（含 `IdleSchedulingSection`、`ProxySection`、`FaceSection`、`JellyfinSection`、`IINASyncSection`）；`frontend/src/utils/playbackProxy.js` 收口代理结果码与文案。
  - 2026-09-29/30 产品完善度批次新增的前端落点：`TaskCenterDrawer.vue`（任务中心）、`PendingWorkHub.vue`（待处理；2026-10-13 起换成 `manage/PendingWorkPage.vue`）、`QuitConfirmDialog.vue`（退出确认）、`TrashCenterDialog.vue` / `TrashUnsupportedDialog.vue`（回收站与不支持废纸篓时的选择）；`utils/watchState.js`（看完与断点判定）、`utils/viewThreshold.js`（有效观看阈值）、`utils/cleanupSelection.js`（清理勾选规则）、`utils/randomPlayback.js`（随机模式与排除表）、`utils/enhancement.js`（超分状态文案）。后端新增 `app_trash.go`、`app_task_center.go`、`app_pending.go`、`app_quit.go`、`app_scan.go`、`app_short_feed.go`，以及 `services/system_trash_*.go`、`services/qrcode_*.go` 两个 cgo 入口、`services/subtitle_file_writer.go`、`services/trash_service.go`。
  - `frontend/scripts/data-test-set.test.mjs` 是 `data-test` 钩子集合守卫（基线 `.loopx/workspace/2026-09-02-capability-batch/baseline/data-test-set-2026-09-02.txt`，允许新增、不允许缺失），已挂进 `npm test`。
- `/browser-extension`: **Chrome/Edge MV3 扩展**（见 2.27）。纯逻辑在 `src/common`、`src/download` 且有单测；`services/browser_bridge_server.go` 与 `services/browser_download_service.go` 是桌面端这一侧，安全边界写在 `docs/browser-extension.md`。
- `/frontend/src/utils`: **前端纯函数工具层**（如虚拟列表窗口计算与缓存工具）。

## 4. 开发与构建指南 (Development & Build)

- **开发模式:** `GOTOOLCHAIN=go1.24.9 wails dev`（Wails 2.11.0 的 `go/packages` 与本机 Go 1.27.1 不兼容；开发模式尚未实测） 使用系统字幕前先执行 `bash scripts/build_apple_ai.sh`，开发二进制读取该原生产物。该脚本部署目标 macOS 15.0；SDK ≥ 26 时以 `-D APPLE_AI_SDK26` 编入新接口，SDK 低于 15 跳过构建（打包同样跳过）。
- **构建应用:** `GOTOOLCHAIN=go1.24.9 wails build`（2026-09-23 darwin/arm64 已完整出包；默认 Go 1.27.1 在 Wails 扫描嵌入资源阶段报 `package "fmt" without types`，`go test ./...` 不受影响）
- **构建并安装脚本:** `scripts/build_and_install_app.sh` 在 `GOTOOLCHAIN` 未设置或为 `auto` 时只为 Wails 构建使用 Go 1.24.9；`--build-only` 可安全验证构建和 sidecar 打包而不替换 `/Applications` 中的应用。 脚本会先编译并打包苹果系统字幕辅助程序；只执行原始 `wails build` 不会自动打包它。
- **数据库:** 默认 SQLite（`~/.CineInsight/library.db`），不需要任何配置；要用 PostgreSQL 时在 `.env` 写 `DB_BACKEND=postgres` 与 `PG_*` 连接项（有 `PG_HOST` 而没有 `DB_BACKEND` 的老安装自动沿用 PostgreSQL），或在设置页「数据库」分区迁移并切换，见 §1。备份在设置页「数据库备份」：SQLite 用 `VACUUM INTO` 写 `.sqlite` 快照，PostgreSQL 用 `pg_dump` 写 `.dump`，默认目录 `~/.CineInsight/backups`（可改、可在访达中显示），默认每 24 小时一份、保留 7 份，运行期间每小时检查一次是否到期（D-PC54 / D-PC56）；恢复前先给当前库做安全备份（SQLite 在进入维护模式之前做），恢复完成后应用自动退出，重新打开即用恢复的数据。
- **测试:** 默认 `go test ./...` 走 SQLite；PG 腿用 `CINEINSIGHT_TEST_PG_DSN` 指向一次性库，`./services` 在 PG 上耗时较长，必须加 `-timeout`（如 `-timeout 1500s`）。

## 5. 开发规范与后续演进

- **规范:** Go 方法导出 PascalCase，JSON 映射 snake_case。
- **验证入口:** `npm test` 覆盖组件与静态合同；`npm run test:macos` 串行执行虚拟窗口、观看笔记、队列播放与裁剪真实 WKWebView 检查。fixture 使用合成素材，不读取用户片库。完整应用启动、外部播放器及 TCC 权限仍需对应真实环境验收。
- **待立项:** 照片地图及可能的人物地点足迹；EXIF GPS已入库，但尚无地图视图。底图联网与位置隐私边界须单独确定。「那年今天」已于2026-10-22授权实施，不再列为待立项。



## 2026-10-30 优化与本机签名

- 片库管理保持常驻宿主与命令注册，首次显示才加载列表、计数、语义状态、保存视图及超分能力；隐藏期间列表刷新合并到再次显示。`libraryQuery.js` 管查询生命周期，`librarySelection.js` 管选择，页面仍拥有筛选与 UI 动作。
- 作品集进度复用有序查询结果的切片，避免重复复制成员；单成员不排序，版本聚合口径不变。基线及命令见 [性能记录](docs/context/performance.md)。
- 本机打包用固定钥匙串证书；首次 `python3 scripts/macos_signing.py setup`，后续打包完成资源装配后最终签名，失败阻止安装。不清除 TCC；详见 [本机签名](docs/context/local-signing.md)。
