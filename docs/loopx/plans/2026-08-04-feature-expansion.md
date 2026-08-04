---
source: 用户口头批准（2026-08-04 会话）：实施顾问评估提出的全部 10 项功能；三项架构裁决已由用户拍板（见 Goal And Boundaries）
status: blocked
slices:
  - id: P-001
    status: done
    depends: []
  - id: P-002
    status: done
    depends: []
  - id: P-003
    status: done
    depends: []
  - id: P-004
    status: done
    depends: []
  - id: P-005
    status: done
    depends: []
  - id: P-006
    status: done
    depends: []
  - id: P-007
    status: done
    depends: []
  - id: P-008
    status: done
    depends: []
  - id: P-009
    status: done
    depends: [P-001]
  - id: P-010
    status: done
    depends: [P-009]
  - id: P-011
    status: done
    depends: []
  - id: P-012
    status: done
    depends: []
  - id: P-013
    status: in_progress
    depends: [P-012]
---

# CineInsight 功能扩展总计划

## Goal And Boundaries

目标：交付 2026-08-04 顾问评估中提出并获用户全部采纳的 10 项能力——6 项"把现有做好"（数据备份、NFO 回写、随机时间衰减、pHash 近重复、sprite 悬停预览、前端收尾×2）与 4 项"新增能力"（片库洞察页、语义搜索+找相似、短视频反馈回流、视频超分）。

用户已拍板的三项架构裁决（本计划的既定结论，执行时不得重开）：

1. **数据层**：保留 Postgres，不迁移 SQLite；本计划交付内置自动备份与一键恢复。
2. **语义搜索向量来源**：复用现有 AI 标签的 OpenAI 兼容接口配置（`ai_tagging_base_url` 等，本地 LM Studio 或云端均可），不内置新的模型运行时。
3. **视频超分**：纳入本计划；先把 `docs/video-super-resolution-model-selection.md`（当前为"阶段性建议"）收敛成定稿设计并经用户批准，之后才实施。

全局约束（延续仓库既有原则，来源：`docs/loopx/plans/2026-07-31-media-workflows-roadmap.md` 与前端统一重构计划）：

- **不改动现有 UI 布局与信息层级**（用户 2026-08-04 裁决，见记忆 frontend-ui-direction）；新增页面/页签允许，现有页面结构不动。新增 UI 一律使用既有令牌体系与 `BaseModal`/`btn-*` 原语。
- 数据库只做加法迁移；功能关闭不删除数据；保持软删除、`libraryPathMutationMu`/`scanSyncMu`、字幕索引、托管图片与现有 Wails API 语义。
- 除用户已配置的 AI 接口外不发起任何网络请求；持久化与日志不得包含 API Key、帧、字幕正文或绝对路径泄漏。
- 长任务一律复用"单 worker、可取消、可续跑、进度上报"的既有模式（参照 `services/technical_backfill_service.go`）。
- 写入用户媒体目录的功能（NFO 回写、超分输出）默认显式触发、绝不覆盖已有文件除非用户逐次确认。

非目标：SQLite 迁移（用户裁决为不做）；macOS 三栏布局改造；任何联网元数据抓取。

（各切片 verify 中的 `npm` 命令均在 `frontend/` 目录下执行。）

## P-001 数据库自动备份与一键恢复

交付内置的 `pg_dump` 定时备份：可配置备份目录（默认用户数据目录）与保留份数，应用启动时按间隔策略触发，设置页展示最近备份时间/结果并提供"立即备份"与"从备份恢复"。恢复是破坏性操作：必须列出可用备份、显示时间与大小、二次确认，恢复前自动先对当前库做一次备份。`pg_dump`/`pg_restore` 不可用时功能降级为明确的不可用状态提示，不静默失败。

完成条件：备份产物可被 `pg_restore` 独立验证；保留策略正确轮转；恢复后应用数据与备份点一致；备份失败在设置页可见且不影响应用其他功能。

> writes: `services/backup_service.go`, `services/backup_service_test.go`, `services/settings_service.go`, `models/video.go`, `database/`, `app.go`, `main_bindings.go`, `frontend/src/components/SettingsPage.vue`, `frontend/src/components/**/*.test.js`, `frontend/wailsjs/`
> anchors: 顾问评估第 1 项（数据安全）；用户裁决"保留 Postgres + 内置自动备份"
> verify: `go test -count=1 ./services ./database`; `go vet ./...`; `npm test`; 手工演练一次备份→恢复闭环并核对行数
> review: 恢复流程是否可能在确认前触碰现有数据；pg_dump 命令注入与凭据泄漏（连接串含密码时的进程参数与日志）；备份期间写入的并发一致性

## P-002 NFO 元数据回写

在既有本地元数据（只读）能力上补齐写出：将评分、标签、人物、作品集、显示标题回写为视频同名 NFO（Kodi 兼容字段映射，与 `services/local_metadata_parser.go` 的读取映射对称）。入口为显式触发：单视频（详情抽屉/右键菜单）与批量（对当前筛选结果）。目标 NFO 已存在时默认合并更新本应用管理的字段、保留陌生字段；无法解析的现有 NFO 绝不覆盖，逐条报告跳过。批量走单 worker 可取消任务模式。

完成条件：写出的 NFO 能被本应用自身的解析器无损读回（往返测试）；不产生除 NFO 外的任何文件；只读文件系统/权限失败逐视频报告；现有"本地元数据导入"行为不变。

> writes: `services/local_metadata_export.go`, `services/local_metadata_export_test.go`, `services/local_metadata_service.go`, `app.go`, `frontend/src/components/PreviewDrawer.vue`, `frontend/src/components/VideoListPage.vue`, `frontend/src/components/**/*.test.js`, `frontend/wailsjs/`
> anchors: 顾问评估第 2 项（元数据可带走、与 Jellyfin/Kodi 互通）
> verify: `go test -count=1 ./services`（含写出→读回往返 fixture）; `npm test`; `go vet ./...`
> review: 对用户媒体目录的写入边界（是否可能写到库外路径）；合并语义是否可能丢失第三方工具写入的字段；XML 转义与超长字段

## P-003 智能随机算法时间衰减

给播放分数引入半衰期：`play_count`/`random_play_count` 的贡献按播放时间距今以可配置半衰期（默认 90 天）指数衰减，使久未观看的视频自然回到随机池。实现基于既有 `last_played_at` 类时间戳，不新增每次播放的明细表；设置页暴露半衰期参数（0 = 关闭衰减，行为退回现状）。同步更新 `ALGORITHM.md`。

完成条件：给定构造的播放时间 fixture，衰减后的选中概率排序符合公式；半衰期设为 0 时与现有算法输出一致（回归保护）；`播放可靠性`语义（失败不污染统计）不变。

> writes: `services/library_service.go`, `services/library_service_test.go`, `services/settings_service.go`, `models/video.go`, `database/`, `frontend/src/components/SettingsPage.vue`, `frontend/wailsjs/`, `ALGORITHM.md`
> anchors: 顾问评估第 4 项（随机算法时间衰减）
> verify: `go test -count=1 ./services`（含 0 半衰期回归用例与衰减排序用例）; `npm test`
> review: 无

## P-004 pHash 近重复检测入清理审阅

对视频关键帧计算感知哈希（多帧取样聚合，纯本地 ffmpeg，不调用 AI），持久化后按汉明距离聚类，作为新候选类别"近似重复（不同转码）"并入现有清理审阅入口，与精确重复、AI 同源并列展示且同样不自动勾选。哈希补全走单 worker 可取消任务；源文件变化后哈希随既有缩略图刷新机制失效重算。

完成条件：对同片不同分辨率/码率的转码对能稳定聚类，不相关视频不误聚（用 fixture 对照阈值）；清理审阅中该类候选可预览、可移入回收站、走既有恢复语义；未补全哈希的视频不产生误报。

> writes: `services/perceptual_hash_service.go`, `services/perceptual_hash_service_test.go`, `services/cleanup_service.go`, `services/cleanup_service_test.go`, `models/video.go`, `models/schema.go`, `database/`, `app.go`, `frontend/src/components/VideoListPage.vue`, `frontend/wailsjs/`
> anchors: 顾问评估第 3 项（近重复检测补齐精确重复与 AI 同源之间的空档）
> verify: `go test -count=1 ./services ./database`; `npm test`; 用真实转码样本人工核对一组聚类结果
> review: 汉明阈值导致的误删风险（候选默认不勾选是否被 UI 绕过）；大库聚类的内存与查询代价

## P-005 进度条 sprite 悬停预览

复用缩略图管线为每个视频按需生成 seek sprite（等间隔帧拼图 + 索引），通过既有 `preview_asset_handler.go` 路由提供；预览抽屉内嵌播放器进度条 hover 时显示对应帧缩略图。sprite 生成失败不影响播放，行为与现有缩略图失败占位一致；源文件变化后随缩略图机制刷新。

完成条件：hover 定位帧与实际画面时间误差在取样间隔内；未生成 sprite 时进度条行为与现状完全一致；sprite 缓存大小有上限策略并纳入现有资产目录管理。

> writes: `services/thumbnail_service.go`, `services/preview_service.go`, `services/*_test.go`, `preview_asset_handler.go`, `frontend/src/components/PreviewDrawer.vue`, `frontend/src/components/**/*.test.js`, `frontend/wailsjs/`
> anchors: 顾问评估第 5 项（进度条悬停缩略图）
> verify: `go test -count=1 ./services`; `npm test`; `npm run build`; 手工验证长视频 hover 定位准确性
> review: 无

## P-006 暗色主题适配收尾

完成 2026-08-04 样式统一重构记录的后续项：`VideoListPage.vue` 清理弹窗与字幕弹窗内约 20 处浅色系字面色（#666、#111827、#e5e7eb、#f0f0f0 等）改为语义令牌并补齐暗色值；顺带处理该重构残留清单中 AI 审核状态椅片三色的令牌化。浅色主题渲染保持不变，暗色主题从"发灰不可读"修复为正常对比度。

完成条件：两主题下清理/字幕弹窗全部文字可读、层级正确；浅色主题截图对比无回归；样式统一重构计划中的残留清单相应条目关闭。

> writes: `frontend/src/components/VideoListPage.vue`, `frontend/src/components/AITagReviewDialog.vue`, `frontend/src/styles/tokens.css`
> anchors: 顾问评估第 6 项前半（暗色适配）；样式统一重构计划"执行结果"节残留清单
> verify: `npm test`; `npm run build`; 两主题手工过一遍清理与字幕弹窗
> review: 无

## P-007 高频操作键盘快捷键

为"批量审阅"场景加快捷键：列表 J/K（或方向键）移动选中焦点、空格开合预览抽屉、F 收藏、W 已看、T 打开加标签弹窗、回车播放；预览抽屉内同键位一致。焦点在输入框/弹窗时快捷键不劫持；提供一个可从设置页进入的快捷键说明（用 `BaseModal` 呈现）。不引入全局配置系统，键位首版固定。

完成条件：上述键位在列表与抽屉生效且与现有鼠标操作等价（走同一方法，统计语义一致）；输入态不误触发；说明弹窗可达。

> writes: `frontend/src/components/VideoListPage.vue`, `frontend/src/components/PreviewDrawer.vue`, `frontend/src/utils/`, `frontend/src/components/**/*.test.js`, `frontend/src/components/SettingsPage.vue`
> anchors: 顾问评估第 6 项后半（键盘快捷键）
> verify: `npm test`（含键盘事件的挂载组件用例）; `npm run build`
> review: 无

## P-008 片库洞察页

新增"洞察"页签（App.vue 导航追加一项，不改既有页面）：总量/总时长/已看比例、存储按标签/目录/分辨率分布、近一年观看热力图、评分分布、AI 标签 Top 分布。后端提供聚合查询服务（只读，索引支撑，游标或物化视图按数据量取舍）；前端图表自绘或轻量内联实现，遵守"无外部 CDN 依赖"的 Wails 约束与既有令牌配色（图表设计遵循 dataviz skill 的配色与形式规范）。

完成条件：万级视频库下洞察页首屏聚合查询可接受（记录 `EXPLAIN` 关键路径）；空库/小库有合理空状态；所有数字可与直接 SQL 抽查对上。

> writes: `services/library_stats_service.go`, `services/library_stats_service_test.go`, `app.go`, `main_bindings.go`, `frontend/src/components/InsightsPage.vue`, `frontend/src/components/InsightsPage.test.js`, `frontend/src/App.vue`, `frontend/wailsjs/`
> anchors: 顾问评估第 8 项（片库洞察页）
> verify: `go test -count=1 ./services`（聚合 fixture 对数）; `npm test`; `npm run build`; PostgreSQL `EXPLAIN` 记录
> review: 无

## P-009 语义索引基建（pgvector + embedding 管线）

启用 pgvector 扩展并新增向量索引表：对每个视频将"AI 标签 + 字幕摘要 + 显示标题/元数据简介"组装为受长度控制的索引文本，经用户已配置的 OpenAI 兼容接口取 embedding 持久化。维度在首次成功调用时探测并固化到设置，模型/维度变更视为需要重建索引（显式触发，不自动）。补全走单 worker 可取消任务；扩展不可用或接口未配置时功能整体降级为明确的不可用提示，既有功能零影响。索引文本与 embedding 的持久化不包含绝对路径与 API Key。

完成条件：`CREATE EXTENSION vector` 缺失时启动与全部既有功能不受影响；索引任务可中断续跑；重建语义清晰（旧向量按模型标记隔离，不混用检索）；接口失败逐视频记录可重试。

> writes: `go.mod`, `go.sum`, `database/`, `models/schema.go`, `models/semantic_index.go`, `services/semantic_index_service.go`, `services/semantic_index_service_test.go`, `services/ai_tagging_client.go`（仅复用配置读取，若需抽公共函数）, `app.go`, `frontend/src/components/SettingsPage.vue`, `frontend/wailsjs/`
> anchors: 顾问评估第 7 项前半（语义索引基建）；用户裁决"embedding 复用现有 AI 接口配置"
> verify: `go test -count=1 ./database ./services`（含无扩展降级用例、维度变更重建用例、mock 接口管线用例）; `go test -race ./services/...`; `go vet ./...`
> review: 加法迁移是否可能在无 pgvector 的库上失败并阻塞启动；embedding 请求的批量与速率控制；混维度检索的防护

## P-010 语义搜索与"找相似"

在搜索模式下拉（现有 文件搜索/字幕搜索）追加"语义搜索"：自然语言查询经同一接口取查询向量，pgvector 近邻检索并与现有筛选（标签/体积/分辨率/智能视图）组合；结果按相似度排序并标注分数。预览抽屉新增"找相似"动作，以当前视频向量检索近邻。未建索引的视频不出现在语义结果中且 UI 明示覆盖率（已索引 n/总数 m）。

完成条件：语义模式不影响既有两种搜索模式的行为与性能；查询接口失败有明确错误态不静默回退；相似结果排除自身；覆盖率提示准确。

> writes: `services/semantic_index_service.go`, `services/library_service.go`, `services/*_test.go`, `app.go`, `frontend/src/components/VideoListPage.vue`, `frontend/src/components/PreviewDrawer.vue`, `frontend/src/components/**/*.test.js`, `frontend/wailsjs/`
> anchors: 顾问评估第 7 项后半（自然语言检索 + 找相似）
> verify: `go test -count=1 ./services`（构造向量 fixture 验证近邻排序与过滤组合）; `npm test`; `npm run build`; 真实接口手工冒烟一次
> review: 语义检索与游标分页/现有筛选组合的 SQL 正确性；大库近邻查询是否需要 ivfflat/hnsw 索引及其参数

## P-011 短视频反馈回流

把手机短视频端已持久化的 liked/favorited（`models/short_feed.go`）桥接回主库：favorited 同步为主库收藏；liked 维护一个自动标签（复用既有"短视频"自动标签的 automatic_kind 机制）。同步为单向（短视频端 → 主库）、幂等、可在设置页开关（默认开启，关闭不清除已同步结果）；主库侧取消收藏不反向改写短视频端状态。

完成条件：短视频端点赞/收藏后主库状态在下次同步点一致；重复同步无副作用；开关行为符合"关闭不删数据"约束；既有短视频 feed 行为不变。

> writes: `services/short_feed_service.go`, `services/short_feed_service_test.go`, `services/video_service.go`, `services/tag_service.go`, `app.go`, `frontend/src/components/SettingsPage.vue`, `frontend/wailsjs/`
> anchors: 顾问评估第 10 项（短视频流反馈回流，使其成为打标入口）
> verify: `go test -count=1 ./services`（幂等与开关语义用例）; `npm test`
> review: 同步幂等性与自动标签的重复创建防护；主库收藏被用户手动改动后再次同步是否会意外回写覆盖

## P-012 超分选型定稿（设计文档，不写代码）

把 `docs/video-super-resolution-model-selection.md` 从"阶段性建议"收敛为定稿设计：确定模型与运行方式（M4/32GB 本地约束）、输出文件命名与存放、任务表结构、与同源关系的关联方式、失败/取消/磁盘不足的处置、以及首版明确不做的范围。定稿需经用户批准——这是本计划内的显式审批边界，P-013 在批准前不得开工。

完成条件：定稿文档落在 `docs/loopx/design/` 下且用户明确批复；文档覆盖上述全部决策点，无"待定"项。

> writes: `docs/loopx/design/`（新设计文档）, `docs/video-super-resolution-model-selection.md`（标注被取代）
> anchors: 顾问评估第 9 项前半；用户裁决"纳入计划，首片先定稿选型"
> verify: 用户对定稿文档的明确批复（会话记录）；文档内无未决项
> review: 无（文档本身即送审对象）

## P-013 视频超分实施

按 P-012 定稿实施：超分任务走单 worker 可取消队列（复用技术信息补全的任务模式与进度上报），输出写新文件、原视频只读不动，完成后通过既有同源关系把增强版与原版关联并在详情中可发现。入口放在视频右键菜单与详情抽屉，磁盘空间预检，失败清理残留文件。

完成条件：按定稿验收；任务可取消且取消后无残留临时文件；原文件字节不变（校验）；增强版入库并与原版建立同源关联；队列一次一个任务。

> writes: 以 P-012 定稿的写入范围为准（预期：`services/enhance_*.go` 与测试, `models/`, `database/`, `app.go`, `frontend/src/components/VideoListPage.vue`, `frontend/src/components/PreviewDrawer.vue`, `frontend/wailsjs/`, 外置运行时脚本目录）
> anchors: 顾问评估第 9 项后半（超分实施）
> verify: `go test -count=1 ./services`; `npm test`; `npm run build`; 对一个真实样本执行完整超分闭环并核对原文件校验和不变
> review: 文件生命周期（临时文件、失败清理、磁盘不足中断）；长任务与扫描/watcher 的锁交互；外置运行时的进程管理与取消

## Integration And Final Verification

- 全量 `go test -count=1 ./...`、`go test -race ./services/...`、`go vet ./...`、`cd frontend && npm test && npm run build` 在全部切片完成后再跑一遍。
- 隐私横切检查：新增持久化与日志中无 API Key、帧数据、字幕正文、库外绝对路径（复用 roadmap P-301 的反射/fixture 手法覆盖新表）。
- 数据库横切检查：从一个真实旧库快照启动，验证全部加法迁移一次通过且既有数据完好；随后用 P-001 做一次备份→恢复演练。
- UI 横切检查：两主题手工过一遍新增页面（洞察页、快捷键说明、语义搜索模式、设置页新增区块），确认均使用既有令牌与原语、现有布局未被改动。
- 更新 `README.md` 功能清单与 `docs/required.md` 勾选状态。

## Handoff And Residual Risks

- 实现复审记录（2026-08-04）：P-001–P-011 实现经 5 个独立评审代理审查，发现 2 Critical + 10 Important，已全部修复并经二次独立复审确认，全量验证绿色（go vet/test/race + npm test/build）。修复涉及：短视频收藏投影加 `favorite_synced_to_library` 归属、BaseModal 补 `role="dialog"`、快捷键页面激活/滚动跟随、清理全选排除近重复组、sprite 后台异步生成、语义筛选 DTO 消毒、覆盖率排除软删除、向量列定维 + HNSW、语义检索回车触发、NFO 空值保留。各切片评审的 Minor 发现清单见会话记录，可作为后续小任务排期。
- P-008 的 EXPLAIN 验收证据待补：工具 `cmd/stats_explain` 已交付，用户裁决后补（2026-08-04），不阻塞其余工作；命令：`go run ./cmd/stats_explain > docs/loopx/design/2026-08-04-insights-explain.md`。
- P-012 已获用户批复（2026-08-04，"设计稿没问题的话可以开始干了"，依据独立评审确认定稿无待定项），P-013 开工。
- 用户同时要求修复实现复审记录的全部 Minor 发现（约 41 项），与 P-013 同步进行。
- Minor 修复轮完成（2026-08-04）：约 41 项中除 3 项带理由保留（备份列表指纹为恢复校验必需、NFO 解析器字段映射为已定设计、watcher 对账交互为已知有界行为）外全部修复；两个并行子代理（备份簇、NFO 簇）+ 主线（语义/媒体/前端/app.go 簇）完成，终审确认无回归。
- P-013 代码实施完成（2026-08-04）：模型/迁移/运行时清单校验/任务队列/分块流水线/原子发布/启动对账/绑定/前端入口全部落地并经两轮独立评审（首轮 2 Critical + 5 Important 全部修复并获逐项 CONFIRMED）；`scripts/build_enhance_runtime.sh` 提供 sidecar 构建。**切片验收未闭合的两步（按定稿必须真机完成，不可用单元测试替代）**：① 运行 `scripts/build_enhance_runtime.sh` 构建并随应用打包签名 sidecar；② M4 真机对一般/动漫真实样片执行完整闭环验收（含取消、磁盘不足、崩溃续跑）。P-013 状态保持 in_progress 直至真机验收。
- 记录的残留小项：sidecar Mach-O/Vulkan 主动探测（真机验收阶段补）、任务表 source_name/CHECK/输出 FK SET NULL（后续加法迁移）、分段文件本身的 fsync（损坏可被哈希校验点检测，不影响正确性）。
- Blockers: none
- Residual risks: pgvector 扩展需要用户的 Postgres 安装具备（未安装时功能降级，已在 P-009 约定）；embedding 质量取决于用户所配模型，语义搜索效果需真实库调优阈值；pHash 阈值需真实样本校准，误报风险由"默认不勾选 + 回收站可恢复"兜底；`pg_dump` 依赖 Postgres 客户端工具在用户机器上的可用性。
- Resume note: 切片间除标注依赖外无语义顺序要求，但多个无依赖切片共享写入文件（SettingsPage.vue、VideoListPage.vue、app.go 等），并行与否一律以"写入范围不相交"规则为准，串行按 frontmatter 顺序推进最稳妥；P-001 建议最先（其后所有大改动都有备份兜底）；中断后先核对 frontmatter status 与本文件各切片完成条件，再继续未完成切片。
