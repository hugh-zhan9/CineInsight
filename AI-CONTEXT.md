# AI-CONTEXT.md: 析微影策核心上下文 (Single Source of Truth)

> 本文件是“析微影策 (Xī Wēi Yǐng Cè)”项目所有 AI 助手（Gemini, Claude, GPT 等）的权威上下文来源。

## 1. 项目架构与技术栈 (Architecture & Stack)

本项目是一个基于 **Wails v2** 的跨平台桌面视频管理系统，命名为“析微影策”，旨在通过 AI 分析与智能策略提供极致的本地影视管理体验。

- **后端 (Go 1.23+):**
  - **框架:** Wails v2 (负责桥接 Go 方法到前端、窗口管理、事件分发)。
  - **数据库:** PostgreSQL（通过 **GORM** 驱动），作为当前主持久化存储。
  - **业务逻辑:** 封装在 `services/` 目录下（如 `VideoService`, `SubtitleService`, `TagService`, `DirectoryService`, `PreviewService`）。
- **前端 (Vue 3 + Vite):**
  - **UI 框架:** 原生 CSS + Vue 3 组合式 API (Composition API)。
  - **通信:** 通过 `wailsjs/go` 自动生成的绑定调用后端方法，使用 `wailsjs/runtime` 进行事件监听。
- **外部依赖 (Sidecars):**
  - **FFmpeg:** 用于提取视频音频流（16kHz, mono, WAV）。
  - **WhisperX Runtime:** 用于本地离线语音识别生成字幕，并与当前管理的 Python 运行时集成。
  - **DeepL API (可选):** 用于双语字幕翻译（用户在设置页配置 API Key）。

## 2. 核心功能与实现原理 (Core Features)

### 2.1 智能随机播放 (Smart Random Play)
采用自研加权随机算法 (`ALGORITHM.md`)，旨在平衡视频库的播放频率：
- **公式:** `播放分数 = 普通播放次数 * PlayWeight + 随机播放次数`。
- **逻辑:** 分数越低的视频被选中的概率越高。
- **权重:** `PlayWeight` 可配置（默认 2.0）。
- **筛选内随机:** 主片库可在当前关键词、标签、体积、分辨率和智能视图范围内按“均衡 / 未看 / 收藏”随机，并排除最近 12 次随机结果；当前范围无候选时明确失败，不回退全库。

### 2.2 离线字幕生成 (Offline Subtitle Generation)
集成 AI 能力实现全本地化字幕制作：
- **运行时:** `services/whisperx_runtime.go` 管理 WhisperX sidecar、Python 环境、模型缓存与执行流程。
- **流程:** 视频 -> FFmpeg (提取音频) -> WhisperX Runtime (推理识别) -> 后处理校验 -> .srt 文件。
- **抗幻觉:** 当前仍保留基于后处理的质量校验与强制生成分支。
- **幻觉确认:** 检测到幻觉时弹窗询问用户，可选择强制生成保留结果 (`ForceGenerateSubtitle`)。
- **任务取消:** 字幕生成过程中可随时取消 (`CancelSubtitle`)，通过 `exec.CommandContext` 终止子进程。
- **双语字幕 (可选):** 开启后可调用 DeepL 或用户配置的 OpenAI 兼容外部 AI API 翻译原文，再合并为双语 SRT（原文上行、翻译下行）；翻译失败会保留原文并返回警告。
- **识别质量设置:** 设置页可维护 WhisperX 模型（tiny/base/small/medium/large-v2/large-v3）和 CPU 批量大小（1-16），计算类型保持内部固定为 CPU 可靠的 `int8`。
- **任务队列:** 字幕生成通过 FIFO 队列串行执行，前端显示当前任务和排队任务，支持逐任务取消、后台继续和队列状态事件。
- **强制生成复用:** 质量校验失败的 SRT 会暂存；用户选择强制生成时直接复用该文件，不重复执行 WhisperX。
- **字幕搜索筛选:** 字幕关键词搜索会把标签、文件大小和视频高度筛选下推到数据库查询，避免先取回全量结果再由前端过滤。
- **字幕命中跳转:** 字幕搜索结果保留首个命中片段的起止时间并复用主列表虚拟壳；点击命中或预览时，支持内嵌预览的格式会在媒体元数据就绪后跳到命中时间，普通预览不会复用旧跳转位置。
- **重命名联动:** 重命名视频会同步重命名同目录 `.srt` 文件，并刷新字幕索引。
- **依赖管理:** `SubtitleService` 负责自动检测系统路径及 Homebrew 路径下的依赖。自动安装/下载依赖只在 macOS 实现，其他平台 `PrepareEngine` 与 `downloadFFmpeg` 统一返回 `ErrSubtitleDependencyUnsupportedPlatform`（明确「不支持自动下载，请手工安装」）。
- **翻译术语表与滑动窗口上下文（D-033/D-034/D-035）:** `translation_glossary_entries` 存「源词→目标词」绑定，`collection_id` 为空表示全局条目；唯一键用物化列 `(scope_key, source_term_lower)`（`scope_key = COALESCE(collection_id, 0)`，源词写入时转小写），避免两后端表达式索引的方言差异；源词/目标词 ≤200 字符、备注 ≤500 字符。`TranslationGlossaryService.ResolveForVideo` 取视频所属全部**活跃**作品集的条目 + 全局条目，同源词作品集级覆盖全局级，多作品集冲突取 `updated_at` 最新并只记冲突条数、不记词本身；作品集是软删除，其条目不级联删除而是留在库里不生效（有意选择）。术语只注入 OpenAI 兼容翻译器：`SubtitleTranslator` 接口不变，新增可选 `ContextualTranslator.TranslateWithContext(ctx, TranslationRequest)`，调用方类型断言，且只有翻译器可注入时才去读术语表；DeepL 只实现旧接口，请求体与失败路径零改动（基线快照 `.loopx/workspace/2026-09-02-capability-batch/baseline/deepl-request-before-P-010.txt` 逐字节钉住）。提示词新增「术语表（必须遵循）」（只列大小写不敏感命中本批原文的条目，筛选在提示词构造处统一完成）与「上文（只读，勿翻译，勿计入返回）」（前一批尾部 5 条原文/译文）两段 JSON Lines；返回条数校验仍只按本批，批大小 50 与「翻译失败保留原文」语义不变；术语表读取失败在生成路径按翻译失败处理（保留原文 + 警告），在工作台重译直接报错。字幕生成的双语翻译与工作台选区重译共用同一套解析与滑动窗口。
- **术语表入口:** 设置页「字幕翻译」分区维护全局表（provider 为 DeepL 时提示「术语表不适用于 DeepL」，且不受「启用双语字幕翻译」开关控制，因为工作台重译也用它），预览抽屉内的作品集详情维护该作品集的表；两处复用 `frontend/src/components/GlossaryEditor.vue`。`UpsertGlossaryEntry` 带 ID 时按 ID 改写（允许改源词，撞唯一键返回 `ErrGlossaryTermConflict`），不带 ID 时按唯一键 upsert。

### 2.3 标签管理 (Tag Management)
- **自动配色:** 创建标签时自动从 12 色预设调色板中轮换分配颜色，用户无需手动选色。
- **透明度显示:** 标签背景色渲染时自动加 35% 透明度（hex→rgba），保证深色文字清晰可读。
- **搜索过滤:** 添加标签弹窗中输入框同时支持创建新标签和实时过滤已有标签。
- **软删除恢复:** 创建同名已删除标签时自动恢复（清除 `deleted_at`），避免唯一约束冲突。
- **改名防冲突:** 改名时检查活跃标签和软删除标签，自动清理废弃记录。
- **标签合并:** 标签管理页先按普通/AI 类型筛选保留目标，再按名称筛选、复选任意普通或 AI 同义来源标签；来源关键词不会反向隐藏目标。合并后保留目标标签的 ID 和普通/AI 身份，视频关联、AI 审批历史引用和短视频偏好权重会一并归并，来源标签随后软删除；AI 来源并入普通目标时，关联的待审候选会失效，避免批准已不在 AI 标签库中的标签。自动标签不参与合并。
- **手机端点赞投影为列而非标签（2026-09-01 裁决）:** 手机端「喜欢」的存储一直是
`short_feed_interactions.liked`；主片库这一侧早先投影到一个 `automatic_kind=short_feed_liked`
的自动标签，因为 `videos` 表没有对应列。该做法有两个问题：应用启动就无条件往用户的标签
列表里塞一个标签（哪怕一次喜欢都没点过），而这个标签除创建与改名外没有任何代码读它，对
Feed 的推荐加权也毫无贡献（加权加的是视频自己的内容标签）。现改为 `videos.is_liked` /
`images.is_liked` 真实列 + 智能视图「点赞」，与收藏完全同构。两者的投影语义仍有意不同：
收藏每次手机端动作只投影一次（不覆盖主片库里的手工取消），点赞完全由投影拥有、双向对账。
升级时 `migrateShortFeedLikedTagToColumn` 一次性把已有标签关联转成列值并硬删该自动标签、
其关联与偏好分；用户自己建的同名标签不在范围内、不受影响。
- **列表行标签换行铺开（2026-09-07，替代 09-04 的 +N 溢出入口）:** 用户裁决"只展示一行不行"。`.video-item` 的高度改为下限（`min-height` 88/104/68 + 上下内边距），`.video-tags` / `.video-tags__strip` 直接 `flex-wrap: wrap`，标签一个不藏、行随内容长高；`VirtualVideoList` 本就按 `heightCache` 里的实测行高定位并在高度变化后重新锚定滚动，所以不需要额外机制。`utils/virtualList.estimateVideoRowHeight` 只是首屏前的预估，现在按标签数与列宽估行数（`estimateTagLines`：徽标约 69px、可用宽 = 列宽 − 缩略图 − 行内动作 − 间距），实测出来后覆盖。原 `+N` 入口、绝对定位展开浮层、`utils/rowTags.js` 与 `ResizeObserver` 测量一并删除。窄变体（详情抽屉打开）仍不显示标签。
- **短视频自动标签:** 复用“短视频 Feed 最大时长”设置维护带持久 `automatic_kind=short_video` 身份且固定显示名为“短视频”的自动标签；新增、迁移、元数据刷新、增量扫描、启动和阈值变更都会同步。若已有同名人工/AI 标签，旧标签保留 ID 和全部关联并安全改名为“短视频（原标签）”（必要时追加序号），不会被自动规则接管；AI 打标判断会忽略自动标签。
- **用户配置闭集标签库:** 不内置或 seed 任何标签和分类；设置页按“每分类一行”维护分类及其多个标签、颜色和启用状态。已有普通标签可原地加入 AI 标签库并保留全部视频关联；用现有名称替换一个 AI 标签库条目时复用该普通标签，旧条目退出标签库但不删除。AI 只从 `is_system=true AND is_active=true` 标签中选择，标签库为空时暂停分析，出集结果服务端丢弃。标签库或 AI 配置保存后会立即唤醒后台任务；标签库变更时仅让已移除、停用或无法匹配的待审候选失效，仍匹配有效标签的候选会保留，标签改名时同步候选名称；没有有效待审候选且无正式标签的视频会按新库重跑。
- **AI 标签库防误清空:** 设置页只有在标签库成功加载后才允许保存全部设置，加载失败时保留错误并提供重新加载；普通 `SaveAITagLibrary([])` 不能清空一个已有标签库，必须由前端二次确认后调用独立 `ClearAITagLibrary`。该双层保护避免数据库短暂断连把前端空状态误写为用户主动清空，并连带使待审候选失效。
- **AI 抽帧与分批:** 固定按每分钟 1 帧、至少 10 帧取样，均匀覆盖 5%–95% 时段；图片按 `AITaggingImagesPerRequest`（默认 10）分批发送，同一视频的批次结果按标签去重并保留最高置信度。
- **受限 AI 打标 Agent:** 初始证据收集后，外部 OpenAI 兼容模型最多进行 4 轮单动作决策，可选择结束、按模型指定数量补帧、请求本地临时字幕或查找同源视频；额外帧由服务端按最大空档取样并受 `AITaggingMaxExtraFrames`（默认 20，范围 1–100）约束，最后仍复用闭集分批标签分析与人工审批。
- **临时字幕证据:** Agent 只调用已准备好的本地 WhisperX（优先）或 Qwen（回退），自动检测语言并与正式字幕共享串行转写槽；结果只驻留当前分析内存，不写 `.srt`、不翻译、不进入候选摘要/数据库，也不会自动安装运行时。原始音频仅存在于本地临时文件且会清理。
- **同源内容识别:** 针对清晰度变化、重编码和空间裁剪，先按时长有界召回，使用五点多裁剪 dHash 视觉指纹筛出本地候选，再把最多 5 组候选 JPEG 交给当前外部多模态模型确认；只有 `high` 结论持久化为同源关系。同源视频的已有非自动标签仅作为 Agent 证据，绝不直接复制为正式标签。
- **同源审阅与否认:** `video_same_source_relations` 保存规范化视频对、双方内容指纹、置信度、理由、未读状态和处理时间；待审工作台用独立子页签区分 AI 标签与视频同源，内容区独立滚动。同源页并排展示 A/B 缩略图和原始路径，缩略图失败时显示本地占位；单个“预览”操作会同时打开两个视频，并提供删除 A/B 片库记录（保留原文件）、“确认同源 / 不是同源”处理。确认后关系仍保留给清理中心使用，但退出待处理列表；涉及软删除视频的关系不会展示。用户否认后，只要双方内容指纹未变就禁止再次确认；任一文件内容变化后允许重判。
- **AI 数据外发边界:** 设置页明确披露抽帧图片、临时字幕文本和同源候选帧可能发送到配置的外部 API；原始音频不外发。Agent step 只保存动作、计数、状态和耗时，日志不记录请求 payload、图片、字幕正文或模型响应正文。
- **已删除视频候选:** AI 标签审阅保留软删除视频的历史候选，展示原视频名称、路径和“已删除”标识；已删除视频不可预览、重命名、重新分析或批准候选，但可拒绝候选。
- **审阅弹窗封高与缩略图（2026-09-04）:** 两个 AI 标签审阅弹窗都自己封高、只让内容区滚动，并在候选分组上展示缩略图。视频侧 `AITagReviewDialog` 与图片侧 `ImageAITagReviewPanel` 分别用 `/preview/thumbnail/{videoID}` 与 `/preview/image-thumbnail/{imageID}`，`loading="lazy"`，失败就地占位且失败状态在一次打开内是黏的（重开面板重试）。图片侧此前把面板级规则写成普通 scoped 选择器（`.image-ai-tag-review[data-v-x]`），而 `BaseModal` 的 scope id 只挂在遮罩层、类名落在内层 `.modal` 上，整条规则从未生效：宽度停在全局 `.modal` 的 500px，且完全不封高，候选一多就把弹窗撑出应用窗口。面板级规则必须走 `:deep(.类名)`。
- **样式审计与失败明细折叠（2026-09-06）:** `AITagReviewDialog` 在质量面板开启时给 `.modal` 追加 `ai-tag-review-modal--wide`（宽 1120px；不开时仍 760px），右侧 352px 常驻栏才不会把待审流压到三百多像素——之前五个动作按钮与缩略图挤在标题同一行，标题被压成一字一行。组头动作独占一行，同源卡片也独占整宽、动作放卡片下方；`AIQualityPanel` 的筛选与统计卡改为 `repeat(auto-fit, minmax(136px, 1fr))` 按容器自适应，表头 `white-space: nowrap`。并排/堆叠断点降到 960px（应用窗口最小 1024px，实际常年并排）。`utils/mediaDetails.formatMediaMeta(video)` 产出「大小 · 时长 · 分辨率」数组、缺项省略，是审阅/同源/删除确认/超分/相关视频等处显示文件大小的统一入口；`video-list/TaskFailureList.vue` 承接后台任务失败明细（后端每轮最多 50 条 × 500 字符）：默认露 3 条、单行截断全文进 `title`、「展开全部 N 条」后在 168px 内滚动，`failures` 变化即收起；状态条、人脸分析、播放代理三处都换成它，`PhotoAITaskPanel` 仍用自己的 5 条上限。`sharedClassScope.test.js` 门禁下跨组件共用的类只能放 `styles/components.css`（本轮新增 `.form-group`/`.checkbox-label`/`.empty-hint`/`.settings-error`）。两个附加 JSON 字段：`CollectionSuggestionMemberView.Size`（同集多版本挑哪个删）与 `ImageFolderGroup.TotalSize`（文件夹卡带「删除文件夹」）。
- **清理候选口径与否决对称（2026-09-07）:** 清理分析四类候选（精确/近似重复、疑似同源、截取片段）统一走 `cleanupPathScope`（`library_service.go`）：扫描根之内**且**不在 `scan_exclude_paths` 黑名单里；此前只裁扫描根，黑名单目录里的旧记录仍会被列成候选。没有 settings 行按无黑名单处理。用户对一对视频说的「不是同片」（`NearDuplicateDismissal`）与「不是同源」（关系 `rejected`）是同一个判断，合成一个排除集给近似重复与同源两类同时过滤；`DismissNearDuplicateGroup` 顺带把这些对上仍 `detected` 的同源关系（及当前评估样本）判为 `rejected`（`rejectDetectedSameSourceRelationsForPairs`），同源检测器 `FindSameSource` 在问 AI 之前也先跳过已忽略的对。以前忽略掉的一对下一轮会换成「疑似同源」再回来。
- **状态切换返回带标签（2026-09-07）:** `SetVideoFavorite` / `SetVideoWatched` / `UpdateVideoWatchProgress` 改为 `getVideoWithTags` 回传（`Preload("Tags")`）。前端 `mergeVideoState` 拿返回值整行覆盖列表项，旧返回值 `tags: null` 盖上去就是"收藏会清空标签"（库里其实还在）；前端同时兜底：返回值没有 tags 数组时沿用行上已有的。
- **AI 接口连通性测试（2026-09-07）:** `App.TestAITaggingConnection(input)` → `services.ProbeAITaggingConnection`：以已保存/环境配置为底、表单非空字段覆盖，发一条 `max_tokens=8` 的纯文本 chat/completions，30s 超时；只证明接口与模型可达，不证明支持图像输入。返回 `ok/base_url/model/latency_ms/reply/message`，不回传也不记日志 API Key；HTTP 401/403、404、429、5xx、超时各有一句可动手排查的中文。设置页「AI 标签」模型输入框下方的「测试连接」按钮用表单当前值发起，不必先保存。目的：打标失败率高时先排除接口问题，剩下的才是抽帧/扫描一侧。
- **手机端标签面板（2026-09-07）:** `POST /short-api/tags {name}`（同源校验 + 严格 JSON 体，与其他写操作同一套防线）→ `ShortFeedService.CreateFeedTag`：去首尾空白、同名直接复用（含软删除恢复，走 `TagService.CreateTag`）、空名 400 `tag_name_required`、超过 64 个字符 400 `tag_name_too_long`、含控制字符 400 `tag_name_invalid`、撞自动标签名 400 `automatic_tag`；数据库错误只回「创建标签失败」，原文进日志。这是第一个由局域网客户端往全局表写任意字符串的入口，长度与字符限制不能省。面板每次打开都重拉标签列表（之前只在首次为空时拉，桌面端新建的标签要刷新页面才出现）；搜索无精确匹配时给「＋ 新建「xx」并添加」，建完直接挂到当前条目。`.sheet-input` 改成在面板底色上可见的框、字号 16px（避免 iOS 聚焦自动放大）。
- **手机端资源类型筛选与手势修复（2026-09-07）:** `GET /short-api/feed/next?media=all|video|image`（`NextItemFiltered(exclude, scope, media)`；`NextItemInScope` 保留为 media=all 的委托），与播放范围正交，非法值 400 `invalid_media`（哨兵错误 `ErrShortFeedInvalidMediaFilter`）；仅图片时不再回落"格式不支持"的视频提示；`/short-api/feed/scopes?media=` 的范围计数同样按类型收窄（`ScopeCountsFiltered`）。前端在「播放范围」面板顶部放三段切换（全部资源 / 仅视频 / 仅图片），选择记在 `localStorage` 的 `short-feed-media-kind`，切换后与换范围一样重开时间线（`restartTimeline`），顶栏范围标签带上「· 仅图片」。两处手势根因：① 舞台根节点对非交互目标一律 `preventDefault` 三个 touch 事件，底部面板的遮罩点击（合成 click）与输入框聚焦都被吃掉——`isInteractiveControl` 现在把 `.sheet-layer`、`input/textarea/select` 都算交互目标，`FeedSheet` 头部另加显式关闭按钮；② 图片放大后整个舞台让给原生平移，touch 路径不再识别双击，pointer 路径又只认非 touch，放大就回不来、上下滑也切不了——`finishPointerPress` 在放大态对 touch 也走 `handleStageTap`，`FeedStage` 放大时给「恢复适屏」按钮（`zoom-reset`）。注意 pointerup 先于 touchend：放大态开始的那段触摸要用 `touchBeganZoomed` 记下来，touchend 到时整段忽略，否则退出放大的同一下会被拿陈旧起点再算一次手势（再放大或误翻页）；双击一旦消费就清掉 `lastStageTapAt`。`DismissNearDuplicateGroup` 的写忽略表与判同源关系在同一事务里。
- **候选审阅游标分页（2026-09-04）:** 待审候选没有上限，两个审阅界面原来都是一次全量下发（压 IPC + 一次渲染上千行）。现在走 `ListAITagCandidatePage` / `ListImageAITagCandidatePage`（App 层同名方法）：单键游标 `id < cursor` + `ORDER BY id DESC` + `LIMIT`，`NextID` 只在这一页满员时给出（短页即末页），页大小复用 `normalizeEntityPageLimit`（默认 50、上限 200），前端传 `limit=0` 用服务端默认。排序从 `(created_at desc, id desc)` 收成单键 `id desc`——键集分页需要单一稳定键，而 id 自增与 created_at 同向增长，用户看到的顺序不变。全量方法两个都保留，但用途不同：图片侧 `ListImageAITagCandidates` 仍服务单图视图（`PhotoLibraryPage`，一张图的候选是个位数）；视频侧 `ListCandidates` 与 `App.ListAITagCandidates` 已经没有前端调用方，只剩测试在用，属待清理的死绑定。
- **分页后的局部更新规则:** 批准、拒绝、整媒体拒绝、重新分析都按后端实际改动的行局部移除（同媒体同名候选一并 `superseded`；该媒体已有手工标签时整媒体作废；`RetryVideo` 把该视频待审候选全部 `superseded`），因为整表重拉会把已翻出来的页丢掉、把用户拉回列表顶部。仍需真正重取的动作（手动刷新、手动加标签、同源删除、候选过期报错）显式回到第一页。当页被移空但后端还有下一页时自动补一页，避免"没有待审候选"的假空态。
- **视频侧搜索口径不变:** 搜索仍覆盖全部待审候选——输入关键词时把剩余页一次翻完（`loadAllCandidatesForSearch`），不把关键词下推到 SQL（它要同时匹配视频名、路径、已有标签名、候选名、匹配标签名和理由，下推等于把这套语义复制进多表 LIKE）。这条只在用户明确检索时才触发全量加载。已知限制：两侧都没有虚拟滚动，连续翻很多页后 DOM 仍会变大；图片侧没有搜索框。
- **AI 审阅局部更新:** 批准、拒绝、重试、手动加标签和同源确认/否认优先局部移除或静默同步候选，操作时不切换为整页加载态；主片库仅在关闭审阅弹窗后统一刷新一次。

### 2.4 稳定分页机制 (Cursor-based Pagination)
针对大规模视频列表设计了基于游标的稳定分页：
- **排序规则:** `score ASC, size DESC, id DESC`。
- **最近播放:** 使用 `(last_played_at DESC, id DESC)` 键集游标，播放时间变化时不依赖 `OFFSET`。

### 2.5 预览优先浏览 (Preview-First Browsing)
- **抽屉预览:** 视频列表项支持通过右侧抽屉进行内嵌预览。
- **观看闭环:** 主片库视频记录收藏、已看、观看位置和更新时间；内嵌预览每 10 秒及暂停/关闭时保存进度，播放结束标记已看。字幕命中跳转优先于断点续播位置。
- **降级策略:** 对不适合内嵌预览的文件，会退化为统计中立的系统播放器预览，不污染正式播放统计。
- **资源路由:** 预览媒体通过 `preview_asset_handler.go` 暴露受控资源路径，由前端 `<video>` 使用；列表缩略图通过 `/preview/thumbnail/{videoID}` 按需生成并缓存，源文件更新后自动失效，生成失败只显示占位图。

### 2.6 播放可靠性与失效纠偏
- **统计保护:** 正式播放仅在 `dispatch success` 后更新统计，失败不会污染 `play_count` / `random_play_count` / `last_played_at`。
- **明确错误:** 播放失败会返回文件级错误信息，包含文件名与路径。
- **失效标记:** 记录支持 `is_stale` 状态，用于表示当前路径失效/待纠偏。
- **局部纠偏:** 播放失败后会返回窄 `reconcile result`，当前页面可据此 patch 当前行或回退 `reloadCurrentView()`。
- **统计事务:** 正式播放成功后，计数递增（`play_count` / `random_play_count` / `last_played_at`）与 `play_events` 插入在同一个数据库事务内完成；事务失败只记一行日志，`PlaybackAttemptResult` 仍为成功，计数与事件都不写，两者永远一致。
- **事件产生点:** 只有桌面正式播放（`desktop_play`）、桌面随机播放（`desktop_random`）与手机端信息流播放（`mobile_feed`）产生事件；内嵌预览、观看进度更新与 IINA 断点回读都不产生。

### 2.7 视频扫描与路径管理
- **扫描机制:** 递归遍历目录，基于 `Settings` 中的 `VideoExtensions` 过滤。
- **附带大小:** `ScanDirectoryWithInfo` 返回 `[]ScannedFile`（含 path+size），用于迁移检测。
- **唯一性:** 在数据库层面通过 `idx_videos_path_active` 唯一索引（结合 `deleted_at IS NULL`）保证路径唯一。
- **手动增量扫描:** 视频列表工具栏可手动扫描全部已配置目录，复用启动时同步逻辑并展示新增、迁移、移除记录、元数据补全、跳过和失败数量；发现新视频后立即唤醒 AI 打标任务。
- **扫描目录黑名单:** 设置页按目录维护扫描排除列表；黑名单目录及其全部子目录会在启动扫描、手动目录扫描、增量扫描和迁移候选扫描中跳过。加入黑名单只阻止后续收录，不自动删除已有视频记录。
- **删除扫描目录 = 标失效，不删记录（D-S01..D-S03）:** `App.DeleteDirectory` 在删配置行**之前**调 `VideoService.MarkVideosStaleUnderRemovedRoot`，把只属于该根的活跃视频置 `is_stale=true`；标记失败就不删配置行（先删配置又没标上，那批记录会掉进"扫描看不见、列表看得见"的夹缝——这正是本次修的毛病，因为两条对账都只在**当前**扫描根之下取候选）。有意不走软删：`deleteVideoRecord` 每条都会建 `VideoTrashEntry`，删一个目录就往回收站灌上千条。嵌套根按"属于被移除根且不属于任何剩余根"判定，`/media` 与 `/media/movies` 同时配着时删前者不牵连后者。
- **失效记录不进默认列表（D-S02）:** `applyLibraryFilter` 在智能视图不是 `stale` 时一律追加 `is_stale = false`。这条边界是主片库与随机播放共用的，因此随机也不会再抽中指不到文件的记录。连带变化：因播放失败或文件丢失而失效的记录此前显示在主列表里，现在也只在「路径失效」视图里出现。
- **对账收拾孤儿记录（D-S01/D-S04 的历史遗留补充）:** 只在删目录那一刻处理是不够的——旧版本删目录只删配置行，那批记录已经掉进夹缝。`SyncScanDirectories` / `SyncImageDirectories` 末尾各跑一次 `reconcileOrphanedVideos` / `reconcileOrphanedImages`：不属于任何**已配置**目录的记录，视频标失效、图片按失踪对账软删。判据用配置目录而非本轮扫到的 `roots`——盘没插时根扫不了但仍配置着，底下的记录不能被隐藏；目录清单读失败时直接返回错误、绝不走到这一步，否则会把整库藏起来。
- **加回目录即恢复（D-S03）:** `App.AddDirectory` 之后后台跑一次 `SyncAffectedDirectories`（窄对账，文件重新出现时清 `is_stale`），完成后复用 `library-watcher-reconciled` 事件通知前端。`ScanSyncResult.Restored` / `LibraryReconcileSummary.Restored` 是为此新增的计数——只做恢复的那一轮在其余计数上全是 0，漏掉它前端不会刷新列表。只恢复真的扫得到的文件，盘没插或路径写错时不会复活任何记录。
- **删除语义:** 视频记录使用 GORM 软删除。新发生的删除会创建 `video_trash_entries` 可恢复快照；用户选择删除原文件时先持久化 `pending_move` 状态、预定路径和强文件身份，再以不覆盖方式移动文件并以数据库事务清理字幕索引、设置 `deleted_at` 和完成状态。复制回退会校验源文件未变化及内容摘要。恢复使用 `restoring` 状态，字幕索引重建也在恢复事务内；事务返回错误时先确认数据库终态再决定是否补偿，应用启动会对账中断状态。异常状态和最近错误在回收站可见，并可恢复原状态。仅删除记录时磁盘文件保留。首页回收站可恢复到原路径，目标被占用或源文件缺失时失败且保留条目，不覆盖现有文件；历史软删除不猜测回填。增量扫描发现文件缺失且无法判定为迁移时，也只软删除数据库记录。

### 2.8 文件迁移检测
- **应用场景:** 自动扫描时区分“文件移走”和“文件删除”，移走的文件更新路径而非删除重建。
- **匹配算法:** 用 name + size 指纹对 stale 记录和新文件配对，配对成功调用 `RelocateVideo` 保留标签等元数据。
- **匹配范围:** 全库匹配，不限于当前目录。
- **主动文件迁移:** 单个或批量视频可迁移到已存在的目标目录，同名 SRT 会随视频移动；采用排他目标创建避免覆盖，同文件系统使用硬链接切换，跨文件系统完整复制并落盘；为避免外部进程持有旧文件句柄继续写入造成数据丢失，跨盘源文件会保留在隐藏暂存路径并向前端返回警告，数据库更新失败会回滚磁盘路径。
- **主动文件夹迁移:** 可把整个文件夹迁移到目标父目录，保留内部相对结构，并同步更新包含的视频记录、字幕索引和扫描目录路径；采用内容哈希校验、数据库切换、最后清理源目录的顺序，支持跨磁盘迁移，跨盘源文件夹同样保留并警告；禁止迁移到源文件夹自身或其真实路径子目录，拒绝源根符号链接。
- **文件夹重命名:** 可选择包含已收录视频或已配置扫描根的实际文件夹，在同一父目录内原子改名；目标已存在、根目录、符号链接和带路径分隔符的名称会被拒绝。改名同步更新活跃/软删除视频路径、扫描目录、回收站恢复路径、位于该树内的扫描黑名单路径和字幕索引，数据库更新失败时回滚磁盘名称。

### 2.9 视频重命名
- **功能:** `RenameVideo` 同时重命名磁盘文件和数据库记录（name/path）。
- **安全:** 自动保留原扩展名，目标文件已存在时拒绝操作，数据库更新失败时回滚文件名。

### 2.10 首页主列表虚拟化
- **目标:** 当前首页主列表已经引入可回收 DOM 的虚拟列表机制，优先解决长列表滚动性能。
- **滚动宿主:** 首轮实现以 `.main-view` 作为真实滚动宿主。
- **高度策略:** 采用预估高度、渲染后测量和高度缓存的最小闭环。
- **范围:** 首页主列表与字幕搜索结果均复用虚拟列表壳；字幕模式使用独立高度缓存维度。
- **视觉布局:** 首页支持列表/响应式网格切换并持久化布局偏好；列表继续使用虚拟化，网格关闭行虚拟化并使用按页加载的多列缩略图卡片，卡片固定在紧凑宽度范围且不拉伸未满行。

### 2.11 片库智能视图与统一清理
- **内置视图:** 全部、继续观看、收藏、最近播放、未看、已看、最近添加、未打标签、无字幕和路径失效。
- **字幕索引一致性:** 首次进入无字幕视图或执行字幕过滤前同步磁盘 `.srt` 索引，避免未索引字幕被误判为缺失。
- **保存视图:** 可将搜索模式、关键词、智能视图、标签、体积和分辨率组合保存为命名视图；名称在活跃记录中唯一，删除采用软删除。
- **共享筛选契约:** 主片库查询与筛选内随机播放复用 `LibraryFilter`，旧分页和筛选接口保持兼容。
- **统一清理:** 清理中心同时展示大小+采样哈希确认的精确重复、短/低清视频及已有 `detected` 同源关系；精确重复配对不会再次进入同源区。
- **安全边界:** 同源候选按分辨率、大小、标签数量和 ID 稳定给出保留建议与预计释放空间，但默认不选中、不自动删除；否认沿用 `RejectSameSourceRelation`，删除沿用可恢复回收站。

### 2.12 本地媒体详情、人物与作品集
- **单页详情抽屉:** `PreviewDrawer.vue` 在同一连续页面展示预览、显示标题、原始标题、nullable 半分制个人评分、人物、作品集及技术信息；人物/作品集详情使用抽屉内部返回栈，不引入标签页。
- **显示标题边界:** `videos.display_title` 为空时前端回退 `name`；编辑显示/原始标题不调用文件重命名。文件搜索覆盖显示标题、原始标题、文件名和路径。
- **评分查询:** `LibraryFilter` 支持 nullable `min_rating/max_rating` 和 `balanced/rating_desc/rating_asc`。评分排序使用显式 NULL 段游标；0 与 NULL 不等价；保存视图持久化评分条件。
- **人物:** `people` 只保存显示姓名、原始姓名和托管头像，允许同名；`video_people` 与 `image_people` 是完全对称的复合主键关系表，都不保存角色或顺序。人物同时覆盖视频与图片（2026-09-02 裁决 CD-02，替换 2026-08-07「人物首期不覆盖图片」）。人物详情支持按标题、文件名或路径搜索视频并原子地关联/解除关联，图片关系由照片页单图详情维护。**最后关系判定跨两种媒体**：只有 `video_people` 与 `image_people` 都不再有该人物时，显式移除才确认并清理人物；全部清理路径（`RemovePersonVideo`、`RemovePersonImage`、`SetVideoPeople` / `SetImagePeople`、`UpdateVideoDetails`）共用 `personHasRemainingRelations` 这一个判定，它数的是关系行而非活跃媒体，因此视频或图片软删除都保留关系且不触发清理。作品集仍不覆盖图片。
- **人物详情双区块:** `PersonDetail` 的「视频」与「图片」各自游标分页、不混排。`GetPersonDetail(personID, cursorVideoID, limit)` 签名不变，只推进视频游标；**图片区块只在 `cursorVideoID == 0` 的首屏下发**，翻视频页时 `images` 为空、`next_image_id` 为 0。图片继续翻页走 `GetPersonImages(personID, cursorImageID, limit)`，该方法在人物不存在时返回 `gorm.ErrRecordNotFound`，与 `GetPersonDetail` 对称。图片区块使用 `/preview/image-thumbnail/{imageID}` 缩略图。`PersonListItem` 的 `active_video_count` / `active_image_count` 只数活跃媒体，与关系行口径刻意不同；前端「最后一条关系」确认框按活跃计数判断，后端清理按关系行判断，前者只会多提示不会少提示。
- **照片页人物筛选:** `ImageFilter.person_ids` 与 `tag_ids` 同为 AND 语义（分组计数子查询），空数组等同不筛，对 `SearchImagePage`、文件夹分组与时间线三条路径统一生效；语义检索的 `ImageSemanticFilter` 不含该维度，照片页在语义模式下把人物组合框置灰（与 AI 状态、拍摄日期筛选的既有处理一致）。人物组合框与既有标签组合框同一交互（输入即搜、方向键、回车），候选来自 `ListPeople`；单图详情返回 `ImageDetail.people` 供维护。
- **人物筛选与关系维护的联动:** 照片页单图详情里关联/解除人物时，若被改动的人物正在 `filters.person_ids` 里，网格重查（`reload`，会连带关闭查看器）；人物被连带清理时改为撤掉该筛选条件并重查。
- **作品集:** `media_collections` 是手工编排的通用容器，活跃规范化名称唯一；`collection_videos.position` 保存完整顺序。作品集详情支持搜索、加入和移出视频并继续支持拖拽排序；隐藏的软删除视频保留顺序槽位，恢复后回到原位；删除作品集不删除视频。
- **关联视频辨识:** 人物与作品集详情的已关联视频及搜索候选统一显示 `/preview/thumbnail/{videoID}` 缩略图、显示标题和文件名；缩略图失败时只显示本地占位，不影响关系维护。
- **关联视频目录筛选:** 人物与作品集的批量关联编辑器既可选择已配置扫描根目录，也可通过原生目录选择器指定任意子文件夹；目录范围查询递归包含所选目录下全部后代目录中的已收录视频。
- **托管图片:** 头像和封面仅接受不超过 20 MiB 的 JPEG/PNG/WebP，复制到 `~/.CineInsight/media-details/{people|collections}/{id}/`；WebView 只通过实体 ID 资源路由访问，不暴露数据库路径。
- **技术快照:** `MediaProbeService` 以参数数组执行本地 `ffprobe -v error -show_format -show_streams -print_format json <path>`，限制输出并在探测前后核对 size+mtime。成功事务替换 `video_technical_metadata/media_streams` 并同步基础元数据；失败只更新尝试状态，保留最后成功快照。
- **显式补全:** `TechnicalBackfillService` 只有一个串行 worker，只由 Wails 操作显式启动，可取消并通过 `technical-backfill-state` 事件/轮询展示状态。应用启动和详情读取都不会隐式全库探测；再次运行依靠成功指纹跳过有效项。
- **本地边界:** 本期详情信息只来自用户输入、现有数据库和本地文件/ffprobe，不调用在线影视资料源。

### 2.13 字幕编辑工作台
- **范围:** 只编辑视频同名外置 `.srt`；ASS/SSA/VTT 和内嵌字幕不提供编辑入口。
- **严格解析:** `subtitleparser` 的编辑用 codec 与检索用宽松解析分离，坏块不静默跳过而是报错，避免编辑时丢内容。
- **编辑能力:** 视频同步预览与点击定位、逐条改文本与起止时间、插入/删除/拆分/合并、全局或选区时间平移、查找替换、选区重译、100 步会话级撤销重做。
- **冲突检测:** 打开文档时记录 `size + mtime + sha256` 指纹，保存前二次校验；外部已修改时返回 `subtitle_conflict` 并拒绝写入一个字节。
- **原子保存:** 覆盖当前文件且不保留历史或 `.bak`；Unix 用 `rename`，Windows 用 `MoveFileEx(REPLACE_EXISTING|WRITE_THROUGH)`。替换失败时原文件保持完整可读。
- **写锁:** 保存与字幕生成共享同一把视频级串行写锁，避免应用内生成与编辑互相覆盖。
- **保存状态:** `saved` / `saved_index_pending`（文件已写入但字幕索引刷新失败）/ `rejected`，不把部分成功伪装成成功。

### 2.14 片库实时监听
- **开关:** 全局 `library_watch_enabled`。新安装默认开启；已有安装升级后默认关闭，避免大型片库未经确认就持续监听。
- **事件源:** `fsnotify` 递归注册全部扫描根目录；添加、修改、删除扫描目录时动态重配 watcher，无需重启。
- **合并与稳定探测:** 同一根目录 750 ms 窗口内合并事件，最多启动一个稳定探测；任何稳定探测最长 30 秒，可取消。
- **窄范围对账:** 只遍历受影响子树，不做全根目录枚举；复用现有导入、迁移检测、元数据探测和字幕索引逻辑。对账只按根目录范围查询视频记录，不加载全库。
- **迁移匹配:** 第一趟沿用全量扫描的 (文件名, 大小) 唯一身份规则，候选可覆盖全部可恢复记录；第二趟为支持改名而只按大小匹配，因规则过宽，候选被限制在本批次实际对账过的目录内，避免把无关记录的标签、评分和 AI 数据串到同大小的新文件上。
- **删除语义:** watcher 事件绝不直接永久删除文件或数据库记录；暂时消失只标记 `is_stale`。手工全量扫描的既有软删除语义保持不变。
- **状态:** 每个根目录展示 `watching / unavailable / error / disabled` 与原因码，并提供显式重试；网络盘等不支持的卷明确报错而不是假装在监听。
- **兜底:** watcher 是加速层，启动扫描和手工扫描始终是最终恢复路径。

### 2.15 本地元数据导入 (NFO / 图片)
- **来源边界:** 只读取同名 `.nfo` 与同目录图片，全程无网络请求；NFO 上限 4 MiB、XML 深度 64、文本字段 64 KiB、演员 1000 个，图片沿用 20 MiB 托管图片限制。XML 禁止 DTD 与处理指令，拒绝非普通文件和越界符号链接。
- **触发规则:** 新视频首次入库时自动填充空白字段；已有视频的 NFO/图片变化只标记"本地资料有更新"，不自动覆盖数据库。
- **差异审阅:** 详情页展示字段级 diff，空字段默认选中，覆盖任何已有内容必须显式确认。
- **人物与作品集映射:** 同名人物唯一匹配时预选复用，多个同名必须由用户选择或明确新建，无匹配预选新建；作品集名称唯一因此同名预选复用。确认前不创建实体、不写关联，绝不自动合并已有同名人物。同一批次内同一来源名只决策一次。
- **只改关系不删实体:** NFO 导入只重写 `video_people` 关联。即使 NFO 的演员列表不再包含某人物，且该人物随后不再关联任何视频，也不会删除 Person 记录或其托管头像——这与 2.12 中「详情页显式移除最后关系会清理人物」的语义刻意不同，导入外部资料不得销毁手工维护的实体。
- **变更探测:** 扫描时对已有视频先比对全部来源文件的 (大小, mtime) 廉价指纹，一致则跳过读取与哈希；只有指纹变化才计算内容 manifest。显式打开详情差异始终走完整内容比对。
- **批量与补全:** 列表可对显式选中的视频批量导入，单次上限 500 个；另提供可取消的"补全缺失元数据"后台任务，同一时间只运行一个。以单视频为事务边界，部分失败可重跑，托管图片写入失败有补偿。
- **开关:** `local_metadata_enabled` 只控制自动发现、新视频填空和补全任务；手工详情维护不受影响。

### 2.16 AI 质量评估
- **数据来源:** 纯本地统计，不额外调用模型、不训练、不自动调整提示词或阈值。
- **运行归因:** `ai_tagging_runs` 每次实际 AI 打标任务一条，记录状态、模型标识、提示词版本、耗时、请求数和工具调用数；`ai_tag_candidates.run_id` 关联当前 run，历史候选保持 NULL。进程退出留下的 `processing` 在下次启动改判 `failed/interrupted`，不自动重跑。
- **同源评估历史:** `ai_same_source_evaluations` 保存实际展示给用户的关系样本，相同指纹的重复任务只刷新关系不增加样本；用户点"不是同源"时在同一事务内更新 relation 与当前 evaluation。
- **指标语义:** 标签窗口用 `COALESCE(approved_at, rejected_at)`，同源用 `COALESCE(rejected_at, detected_at)`，运行用 `completed_at`。`pending` 和 `superseded` 永不进入分母；所有比例同时返回分子、分母和浮点率，分母为零时率为 `null` 而不是 0%。
- **历史未知版本:** 缺少模型或提示词版本的旧记录统一映射为 `historical_unknown`，显示"历史/未知版本"，不推测为当前模型。
- **入口:** AI 标签管理拆成"待审工作台"和"质量评估"两个 tab，默认 30 天窗口，支持 all/30d/7d 与标签、置信度、模型、版本过滤。质量视图只读，不提供批准、拒绝、重跑或阈值修改。
- **隐私:** 只持久化计数、耗时、稳定版本号和模型名称；不保存帧、字幕正文、请求/响应 body、API Key、base URL 或本地路径。
- **软删除:** 软删除视频的历史样本仍进入聚合，但不返回可操作记录。

### 2.17 图片库批量管理与扫描恢复
- **图片多选:** 图片流卡片支持复选、全选当前已加载结果、批量添加标签和批量删除；批量接口逐项返回失败，失败项保留选中以便重试。
- **图片标签搜索:** 单图详情和批量标签工具均使用可搜索组合输入框；输入时直接展示匹配标签，支持方向键选择并按回车添加，不使用独立下拉框或确认按钮。
- **自动删除恢复:** 图片扫描对账因路径失踪而软删除记录时写入 `is_stale` 恢复标记；同一路径文件重新出现后复活原记录，保留原 ID、标签、收藏、评分等数据。用户主动“仅删除记录”不会写该标记，因此不会被后续扫描自动恢复。
- **删除图片目录复用这条路（D-S04）:** `App.DeleteImageDirectory` 在删配置行之前调 `ImageService.MarkImagesStaleUnderRemovedRoot`，逐条走既有的 `deleteMissingImageRecord`（软删 + `is_stale`，不动磁盘文件、不建回收站条目）；加回同一路径后由 `restoreStaleImage` 复活同一行。用户可见结果与视频侧一致（从列表消失、记录留着、加回即恢复），但机制沿用图片自己这套，不套视频的标失效——图片的软删本来就不建回收站条目。图片侧只有全量 `SyncImageDirectories()`，没有按目录的窄扫描，因此加回目录触发的是全量对账。

### 2.18 播放历史账本
- **表:** `play_events(id, video_id → videos ON DELETE CASCADE, played_at, source, created_at)`，索引 `(video_id, played_at)` 与 `(played_at)`。只追加，应用层没有任何 UPDATE / DELETE 路径；视频软删除时事件保留并继续进聚合，永久删除时级联清除。
- **迁移:** `ApplySchema` 内的 `backfillLegacyPlayEvents` 对 `last_played_at` 非空的视频各回填一条 `legacy` 事件，`NOT EXISTS` 保证幂等，两后端共用同一条 SQL；不按 `play_count` 合成多条（库里没有那些时间点）。实际插入行数大于 0 时打一行启动日志。
- **洞察改读:** `libraryWatchHeatmap` 改查 `play_events.played_at >= now-1y`，仍在 Go 侧按 `time.Local` 归并（SQLite 无 DATE 类型的老教训不变）。热力图口径由「每日有多少部片的最后播放落在这天」变成「每日播放次数」，同一部片一天播两次现在计两次。`services.LibraryStats` 新增 `total_play_events` 与 `plays_by_source`（不限一年窗口），洞察页在「已看比例」卡片副行展示。
- **与算法的关系:** 账本不参与随机算法，打分仍只读 `videos` 的三列（`ALGORITHM.md` 已注明）；`services/random_score_order_test.go` 用固定夹具把分数序钉死。

### 2.19 后台任务登记表与空闲调度
- **登记表:** `services.BackgroundTaskRegistry`（进程内计数 map，不持久化，重启清零）。taskKey 是 D-014 的固定 16 项，`Snapshot()` 按面板顺序返回；未知 key 与未配对 `End` 直接 panic。12 个长任务在进入 running 时 `Begin`、终态 `defer End`；本地资料补全与 NFO 写出共用 `local_metadata`。App 暴露 `GetBackgroundTasks()`，变化时发 `background-tasks` 事件（Dock 角标与命令面板都读它）。
- **空闲门:** `services.IdleGate` + `idle_probe_darwin.go` / `idle_probe_other.go`。darwin 读 `ioreg -c IOHIDSystem` 的 `HIDIdleTime` 与 `pmset -g batt`，30 秒缓存，5 秒超时；探测失败一律视为不空闲并记 `probe_failed`；非 darwin 恒为空闲。判定 = 空闲 ≥ 阈值 ∧（不要求电源 ∨ 接着电源）∧（时间窗为空 ∨ now ∈ 窗，支持跨午夜）。开关关闭或设置读取失败时 `Run` 与钩子都直通。
- **只挡自动路径:** 经门的是扫描后自动化三件事（技术信息、pHash、清理分析）、扫描后与片库监听触发的 AI 打标唤醒（`SyncScanDirectories` 的日志字段 `ai_wake_queued` 表示唤醒已提交、可能在门后等待）、图片 AI 打标自动触发、图片自动 EXIF；显式 `Start*` / `Trigger*` / `Retry*` 一律直通，并在启动前摘掉可能残留的项间检查点。AI 打标 worker 自身的启动批次与 5 分钟 ticker 不受门（它与显式触发共用一个循环，D-030 于 V1.0.6 收窄）。
- **项间检查点:** 技术信息、pHash、图片 EXIF、图片 AI 打标四个单 worker 服务支持 `StartWithPauseHook(ctx, hook)`：装钩子与翻 Running 在服务同一把锁里完成，服务已在跑的那一轮不补装；显式 `Start` 在锁内摘钩子并 `Release` 放行已停在检查点上的 worker。`TaskPauseHook` 是接口（`Wait` + `Release`），worker 处理下一项前在锁外调用 `Wait`，阻塞时把 `Status().Gate = {waiting_idle, reason}` 回报给前端，`Gate` 由钩子返回时清零；可随 worker ctx 取消。`Release` 先在钩子实例上置 sticky `detached` 再唤醒等待者，`Wait` 在入口、注册后与每轮等待前都检查它，因此显式启动摘钩子与 worker 读钩子之间不存在丢唤醒窗口。清理分析无逐项循环，只在 `Run` 层过门。
- **立即运行:** `RunGatedTaskNow(taskKey)` 只对当前确实被门挡住的任务生效（否则返回 `ErrIdleGateTaskNotWaiting`，不留状态）；bypass 豁免的是这一轮，不是一项，也不是关开关——在登记表看到该任务 running→absent、或 `Run` 返回而任务从未进登记表时清除，另有 10 分钟兜底。门按 key 去重：同一任务同时只留一个自动唤醒等待者，后来者合并（`ErrIdleGateTaskAlreadyWaiting`，自动路径视为正常）。探测一律用独立 `context.Background()`，调用方取消不会污染共享缓存；`GetIdleSchedulerStatus` 只读缓存并在后台补探测，状态含 `probed` 与 `settings_error`；保存设置会 `InvalidateSettings` 让开关即时生效。`GetIdleSchedulerStatus()` 与 `idle-scheduler-state` 事件供设置页与状态条读；任务尚未开始（卡在 `Run`）时状态条从等待清单取原因，跑到一半被拦住则读任务自己的 `gate`。
- **设置:** `settings` 新增 `idle_scheduling_enabled`（默认 true，`migrateIdleSchedulingSetting` 对老库显式刷 true）、`idle_threshold_minutes`（1–120，默认 5）、`idle_require_ac_power`、`idle_window_start/end`（`HH:MM`，非法归空）。`idle_scheduling_enabled` **有意不带 gorm default 标签**：默认 true 的布尔列会被 GORM `Create` 当零值跳过，双向迁移器逐行复制时会把用户关掉的开关翻回 true；默认开只由 `ApplySchema` 显式插入 + 迁移函数保证（后续所有默认 true 的布尔列同此规则；既有 `short_feed_feedback_sync_enabled` 带该标签属残留）。设置页新增「后台任务调度」分区；图片侧 `PhotoAITaskPanel` 与片库页状态条共用 `utils/idleScheduling.js` 的文案与归一化。同批修掉既有缺陷：`UpdateSettings` 此前从不保存四个 `auto_*` 开关，用户拨了也不生效。
- **重媒体槽:** `services.MediaWorkSlot`（容量 1）已就位，由转封装、帧哈希、人脸抽帧三类 ffmpeg 重任务在处理每一项前获取（D-007）；字幕转写槽与超分队列不并入。

### 2.22 建议作品集
- **解析（纯函数）:** `services/collection_suggestion_parser.go` 只看文件名（去扩展名）、不看目录名。去噪顺序固定：全角数字→半角 → 去方括号组名（`^\[\d{1,3}\]$` 的纯数字括号保留，它是集号）→ 去质量/编码/来源标签固定清单 → 把 `.` `_` 换成空格（`-` 不换，第五条模式靠它认集号）。五种模式按优先级：`SxxExx`（唯一能给出季）> `第 N 集/话/話` > `\bEP?\s*N\b` > `[N]` > ` - N(vX)?$`。系列名 = 命中位置之前去分隔符、折叠空白；`series_name` 保留大小写供展示，`normalized_series` 小写作分组键；集号由 `[N]` 命中且系列名为空时，取紧邻集号括号之前、非标签且非纯数字的方括号组作系列名（覆盖 `[字幕组][片名][05][1080p]`）。
- **成组:** 分组键 `(scan_root, normalized_series)`，扫描根取 `models.ScanDirectory` 最长匹配前缀；不在任何扫描根下的视频跳过。成员 ≥2 才成候选；已在任何作品集里的视频先剔除。成员按 `(season, episode, video_id)` 排序，`position` 用稠密排名——同一 `(season, episode)` 共用一个 position，`List()` 据此标 `multiple_versions`（`03` 与 `03v2`）。
- **记忆:** `collection_suggestions.fingerprint` = 成员 ID 排序后 sha256，唯一索引。`confirmed`/`dismissed` 的指纹一律跳过；上一轮遗留的 `pending` 不在本轮结果里就删除。忽略只翻状态，成员一行不动。
- **确认:** `ConfirmCollectionSuggestion(id, name, orderedVideoIDs)` 复用 `CollectionService`：同名活跃作品集存在则加入（确认成员排到末尾）、否则 `CreateCollection`，然后 `AddCollectionVideos` + `ReorderCollectionVideos`，最后候选置 `confirmed`。名称留空回退系列名；`orderedVideoIDs` 必须是成员子集、不重复、至少两个（`member_mismatch`）；候选非 `pending` 报 `suggestion_not_pending`。**绝不改 `display_title` / `name` / `path`**。**不是单一事务**：`CollectionService` 各方法自带事务，SQLite `_txlock=immediate` 下外层再套必自锁，因此是「每步自身原子 + 整体可重入」，任何一步失败候选留 `pending`、重试等价于一次成功；`Confirm`/`Dismiss` 由 `confirmMu` 串行。
- **任务与开关:** `Analyze(ctx)` 单 worker、可取消、分页读活跃视频、登记表 `collection_suggest` 键，状态经 `collection-suggestion-state` 事件推送。轻任务：不经 `MediaWorkSlot`、无项间检查点（只在 `Run` 层过门），`Status` 无 `Gate` 字段。`settings.auto_collection_suggestions` 默认 false，开启时 `runPostScanAutomation` 起第四个 goroutine 经 `runGatedAutoTask` 过空闲门；显式启动不过门。
- **入口:** `CollectionSuggestionPanel.vue`（`BaseModal`）由 `video-list/LibraryToolbar.vue` 自持渲染：管理菜单的 `collection-suggestions` 在 `onManageSelect` 本地拦截，其余菜单项照旧 emit；工具栏订阅 `collection-suggestion-state` 只为菜单文案显示「（分析中）」。面板含候选列表、成员缩略图与集号、去成员/放回、改名、确认、忽略（二次确认）、分析进度与「分析剧集」按钮。

### 2.23 兼容性转封装代理（播放代理）
- **表:** `video_playback_proxies(id, video_id → videos ON DELETE CASCADE 唯一, source_size, source_mod_time_ns, strategy, status, output_size, last_used_at, last_error, …)`，索引 `uniq(video_id)` 与 `(last_used_at)`。**不存路径**：文件名由 `<videoID>-<sha256("size:mtimeNS") 前 16 位>.mp4` 推导。只有终态落表（ready / failed）；`failed` 行只为详情抽屉显示上次原因，不算有效代理、不计占用、不进 LRU。
- **文件位置:** 唯一位置 `~/.CineInsight/proxies/`，临时目录 `proxies/.tmp/`；产物先写临时目录，校验通过后同卷 `os.Rename` 发布。代理**不进 `videos` 表**、不写用户媒体目录、淘汰只删自己的文件。
- **策略（`playback_proxy_encode.go`，参数数组集中一处）:** 读技术快照首个非封面视频流与首个音频流：`h264|hevc` × (`aac|mp3` 或无音频) → remux `-v error -y -i <src> -map 0:v:0 -map 0:a:0? -c copy [-tag:v hvc1] -movflags +faststart <tmp>`；否则 transcode `… -c:v h264_videotoolbox -b:v min(源码率,8000000) -vf scale='if(gt(iw,ih),min(1920,iw),-2)':'if(gt(iw,ih),-2,min(1920,ih))' -c:a aac -b:a 160k -movflags +faststart <tmp>`（按**长边** ≤1920 约束，竖屏同样不高于 1080p，永不放大）。HEVC 一律打 `hvc1`；两条路都用快照解析出的**绝对流序号** `-map 0:<v> -map 0:<a>` 只保留一路正片视频一路音频（避开封面图流与字幕流，无音频时不留空映射）。快照缺失或不新鲜先探测一次，仍不可用报 `probe_failed`。
- **八步流程与错误码:** 入队（自动路径先过 `IdleGate.Run`）→ 每项 `MediaWorkSlot.Acquire` → stat 源得指纹 A、已有有效代理报 `already_exists` → 取快照选策略 → ffmpeg 写临时 → 再 stat 得 B，A≠B 报 `source_changed` 并删临时 → ffprobe 读产物且时长与源相差 ≤2% → rename 落位、upsert `ready` 行、`enforceLimit()`。错误码：`already_exists` / `in_progress` / `source_changed` / `encode_failed`（带 stderr 尾部 2 KB，**绝对路径经 `scrubPlaybackProxyPaths` 擦成 `<path>`** 后再落表与回前端）/ `disk_full`（不触发淘汰）/ `file_missing`（顺带标 `is_stale`）/ `probe_failed` / `cancelled`（取消时正在跑与未轮到的项都记结果，计入 Skipped）。任何失败路径都清临时文件。取消后 worker 退出前再触发报 `ErrPlaybackProxyStopping`。手机端抽中视频后校验代理指纹，失效即清理并重抽；`ResolveMedia` 对白名单不命中且无可用代理的视频返回 `ErrShortFeedNoEligibleVideos`（路由 404），绝不回落源文件 MIME。
- **消费（D-004）:** `GetPreviewSession` 在 `inlinePreviewMIME` **不命中之后**才查代理；命中则 `mode=inline`、`LocatorValue` 仍是 `previewMediaPath(id)`、MIME `video/mp4`，并新增可选字段 `proxy: {strategy, size}`。`ResolvePreviewMedia` 解析到代理路径。手机端三个 MIME 门（`collectCandidates`、`videoDTO`、`ResolveMedia`）全部接上。**`inlinePreviewMIMEs` 一个字都没改**（`TestInlinePreviewMIMEsUnchanged`）。正式播放永远打开源文件（`TestPlayVideoNeverOpensProxy`）；内嵌预览不计播放。
- **失效与级联（D-002）:** 预览与手机端命中代理前先 `os.Stat` 源比对，不一致或产物被外部删掉就同步删文件与表行，本次按无代理处理——不做后台巡检。`VideoService.deleteVideo` 成功后调 `DeleteForVideo(id)`：软删除（回收站）与扫描对账删除都走这条；回收站恢复后要用得再次触发。
- **LRU（D-005）:** `settings.proxy_cache_limit_bytes` 默认 50 GiB（`database.DefaultProxyCacheLimitBytes`），**0 = 不限**，因此**不带 gorm default 标签**（默认非零数值列同此规则），默认值由 `ApplySchema` 新库显式行 + `migrateProxyCacheLimitSetting` 保证，负数归一化为默认。每次成功写入后与「立即整理」都跑 `EnforcePlaybackProxyLimit()`：只看 ready 行，按 `last_used_at ASC` 删到不超限，跳过保护窗口（120 s = 2 × 节流窗口）内用过的行。`last_used_at` 由预览会话与手机端媒体请求刷新，内存 map 每视频节流 60 秒，但表里的值已老过保护窗口时绕过节流强制刷新（否则「一直在播但每次被节流」的代理会被淘汰）。`GetPlaybackProxyUsage` 另扫目录统计孤儿文件（匹配命名规则但无表行）与非本应用文件数，「清空全部代理」只删匹配 `^\d+-[0-9a-f]{16}\.mp4$` 的文件并连带孤儿。`appdata` 解析失败（数据目录不可用）时代理服务的删除入口一律返回 `ErrPlaybackProxyDirUnavailable`，不在相对路径上动手。
- **触发与并发:** `CreatePlaybackProxy` / `BatchCreatePlaybackProxies` / `BatchCreatePlaybackProxiesForFilter`（服务端解析筛选成 ID）；单 worker + FIFO，批量内去重、已在处理的报 `in_progress`；「队列取空」与「翻 Running」同锁判定。登记表 key `proxy`；一轮跑完经 `DesktopNotifier` 发一条（只有成功/失败条数）。`settings.auto_compatibility_proxy` 默认 false，开启时 `runPostScanAutomation` 经 `runGatedAutoTask("proxy", …)` 对 `ScanSyncResult.AddedVideoIDs` 里不在白名单的视频入队；代理任务没有项间检查点（只在 `Run` 层过门）。
- **手机端直连上限（用户裁决 2026-09-07，`short_feed_mobile_fit.go`）:** 内嵌白名单只看扩展名，4K / 高码率 mp4 原样发给手机就会卡。现在 `ResolveMedia` 对白名单命中的源文件再过一道"手机能不能吃"的门：长边 >1920 **或** 总码率 >8 Mbps（快照总码率优先，缺快照按 大小×8/时长 估）即"重"，与代理产物规格刻意一致。重文件有有效代理就发代理字节；没有则照发源文件（总比放不了强），并在 `auto_compatibility_proxy` 打开时经 `EnqueueMobileFitCandidates`（跳过白名单筛选、只确认活跃）非阻塞排队生成，每视频十分钟最多提一次；判定结果按源指纹缓存十分钟（Safari 一次播放发很多 Range 请求）。扫描后的自动候选 `filterPlaybackProxyCandidates` 同步纳入"白名单命中但超标"的新视频，设置页开关文案随之改写。桌面端预览不受影响。同批前端修复：Feed 预取的下一条 `<video>` 从 `preload="auto"` 改为宿主控制的 `prefetchPreload`——换到新一条先 `metadata`，当前条 `canplaythrough` 后才升到 `auto`（图片为当前项时直接 `auto`），不再与正在播的视频抢带宽。
- **入口:** 详情抽屉技术信息区块（生成/删除此代理/状态/「经代理播放」）、行 ⋯ 菜单「增强」组、批量操作栏「为选中生成代理」、管理菜单「补全」组「为当前筛选生成代理」、设置页「播放代理」分区（占用/数量/上限 GiB/清空全部/立即整理/任务状态与取消）、「自动化与扫描」分区的自动开关。文案集中在 `frontend/src/utils/playbackProxy.js`。VideoToolbox 转码仅 macOS，其他平台只有 remux（有意不做软件编码回退）。

### 2.24 帧哈希序列与截取片段识别
- **序列表:** `models.VideoFrameHashSequence`（`video_frame_hash_sequences`）：`video_id` 主键兼外键（CASCADE）、`interval_ms`（代码写入 2000，无 gorm default）、`hashes` BLOB（连续 uint64 大端，两小时片 ≈28.8 KB）、`frame_count`、源指纹、`computed_at`（索引）、`last_error`。三点感知哈希与近重复逻辑一行未动。
- **回填:** `services/frame_hash_service.go` 单 worker、可取消、可续跑、`StartWithPauseHook` 项间检查点，登记表键 `frame_hash`，事件 `frame-hash-backfill-state`，每项前取共享 `MediaWorkSlot`（`app.mediaWorkSlot`，转封装与人脸抽帧共用同一实例）。候选 = 没有序列、带 `last_error`、帧数 0、间隔不等于当前常量、或源指纹变过的活跃视频。抽帧是单趟 `ffmpeg -v warning -fflags +discardcorrupt -i <src> -vf "fps=1000/2000,scale=9:8:flags=area,format=gray" -f rawvideo pipe:1`，Go 侧每 72 字节一帧算 dHash；ffmpeg 以函数变量注入便于单测。失败写 `last_error` 行，下一轮重试。
- **匹配:** 阈值集中在 `services/clip_match.go`：`clipMaxDurationRatio=0.9`、`clipMinDurationSeconds=10`、`clipHammingThreshold=10`、`clipMatchRateThreshold=0.70`、粗筛前 16 帧命中 ≥12 才全量验证。粗筛是剪枝不是等价变换（门槛 0.75 > 0.70），fixture 用暴力解钉住。不识别倍速、镜像、裁剪。
- **清理类别:** `services/clip_cleanup.go` 在 `analyzeCleanupCandidates` 末尾追加一步（旧五类计算路径零改动，有快照用例）：按帧数降序 + 二分定位 `len(B) ≤ 0.9·len(A)` 的起点逐对 `MatchClip`。`CleanupAnalysis` 新增 `clip_groups`（`{full, clip, offset_seconds, match_rate, estimated_savings}`，建议保留 `full`）与 `stale_frame_hash_count`（**未回填 + 失效合成一个数**，与 pHash 的 `stale_hash_count` 只数失效不同）。排除精确重复配对；**一个片段只报一条候选**。
- **忽略:** `models.ClipDismissal`（`clip_dismissals`，`uniq(video_full_id, video_clip_id)`，方向有意义）连同双方源指纹存；任一侧重编码后忽略自动失效。
- **入口与开关:** Wails `StartFrameHashBackfill` / `GetFrameHashBackfillStatus` / `CancelFrameHashBackfill` / `DismissClipCandidate`（`app_cleanup.go`）。`settings.auto_frame_hash_sequence` 默认 false。清理审阅面板新增「截取片段」类别（并排 A/B、偏移 mm:ss 与命中率、A 不给勾选框、默认不勾选、「全选候选」也不选它、删除走既有回收站、「忽略」调后端）与「补全帧哈希」提示；`BackgroundTaskStatusBars.vue` 第三条状态条含等待空闲与立即运行。
- **基准:** 10 分钟 1080p30 H.264 端到端 6.7 s → 两小时约 80 s（达标 ≤3 min）；4K HEVC 软解约 6.2 s/分钟 → 两小时约 12 min（已知瓶颈，`-hwaccel` 留后续）。
- **评审回写:** 回填失败原因落库前经 `redactFrameHashErrorPaths` 抹掉词首绝对路径（`<path>`），内存态失败清单仍是原文；`MatchClip(full, clip, intervalMS)` 的间隔由调用方传行上的 `interval_ms`，间隔 ≤0 直接不给结论；命令面板任务组含 `frame_hash`（零参启动 + 取消）；旧五类快照用例带扫描根（根内外各一整套素材），同时钉住「清理只看扫描根内视频」这一开工前的既有行为；`clip_dismissals` 两个 video id 无外键，永久删除后忽略行惰性滞留（无害）。

### 2.25 人脸运行时与分析
- **运行时:** `services/face_runtime.go` + `face_runtime_manifest.go`。目录 `~/.CineInsight/face-runtime/{python,venv,models/buffalo_l}`；身份 `insightface-1.0.1-buffalo_l-onnxruntime-1.23.1`；托管 Python 区间 3.10–3.13（onnxruntime wheel 只到 cp313，Homebrew 3.14 会被拒绝并回落托管 3.10）；insightface 用 `--no-deps` 单独装（否则拉 GUI 版 opencv 覆盖 headless 的 cv2），运行时依赖逐个 pin；状态机 `available/missing_python/missing_venv/missing_model/download_failed/incompatible`（非 darwin+arm64）；就绪判定用标记文件 + 文件大小，哈希只在安装时校验。`PrepareFaceRuntime` 显式触发、可取消、事件 `face-runtime-state`；这是本批**唯一新增的网络出口**，含三项：PyPI 依赖安装、必要时下载托管 Python、模型包下载（`ResponseHeaderTimeout` 30 s，整体中止靠 ctx）；准备完成后分析全程离线，设置页披露文案如实列出。解释器版本探测按路径缓存 30 秒（准备流程前后显式失效）。
- **模型:** `buffalo_l.zip`（GitHub insightface v0.7，288,621,354 字节，sha256 `80ffe37d…ca2f`）只解 `det_10g.onnx` 与 `w600k_r50.onnx` 并逐个校验；`settings.face_model_mirror_url` 以「前缀 + 官方 URL 整体」拼接（ghproxy 形状）。本机直连 GitHub 不稳，真机准备需配镜像。
- **worker:** `services/face_worker.py`（embed，写入运行时目录后常驻执行）。stdin `{"id","path"}` 一行一条，stdout `{"id","faces":[{bbox 相对坐标, quality, embedding=base64 float32×512 已 L2 归一化}]}`；加载完成先打 `{"ready":true}`；加载日志一律到 stderr（stdout 是协议通道）；图片用 `imdecode` 读字节（`cv2.imread` 在中文路径静默失败）。会话由 `face_worker_process.go` 管理（一问一答、超时/kill、读协程不泄漏）。
- **分析:** `services/face_analysis_service.go`（+ `face_cluster.go` 向量与阈值、`face_crop.go` 纯标准库裁剪、`face_worker_process.go` 抽帧）。单 worker、可取消、可续跑、项间检查点、登记表 key `face`、每媒体前取共享 `MediaWorkSlot`。候选 = 非 stale 的活跃视频/图片 + 有头像的人物，指纹变化或无观测者；视频复用 `planAITaggingFramePositions`（宽 1280），图片走 `ResolveImageView`。
- **数据:** `models/face.go` 三张表（`face_observations` / `face_clusters` / `face_person_candidates`）。观测唯一键 `(media_kind, media_id, source_fingerprint, frame_ms, bbox_hash)`；图片/头像/无脸标记的 `frame_ms` 存哨兵 `-1`（`models.FaceFrameMSNone`）而非 NULL，否则两个后端都把 NULL 视为互不相等、唯一键形同虚设；`bbox`/`bbox_hash` 必须写 `column:` 标签（GORM 会把 `BBox` 拆成 `b_box`）。无脸写一条 `bbox_hash='no_face'`（每媒体一条）；全部帧检测失败按失败记账、绝不写 no_face。`media_id` 是多态引用做不了数据库级联：媒体永久删除后的孤儿观测由分析开跑前 `pruneOrphanFaceData` 清理，软删除不清。人物删除 → 簇 `person_id` SET NULL 回 unnamed。
- **聚类:** 点积 ≥0.55 归入最相似簇（named 与 ignored 簇同样参与比对，忽略过的脸不再冒成新簇）；否则新建 unnamed；簇代表取质量最高观测；重新分析删旧观测时在同一事务内 `recomputeFaceClustersTx` 重算受影响簇的 `observation_count`，归零的簇连同候选删除、代表观测换成剩余质量最高者（否则候选门、质心加权与幽灵簇全错）；归入 named 簇的新观测置 `append_status=pending`，**分析路径绝不写 `video_people`/`image_people`**（那是人脸审阅的事）；人物头像种子相似度 ≥0.6 且簇观测 ≥3 生成 `face_person_candidates`。
- **隐私:** 裁剪图 ≤160px 存 `~/.CineInsight/faces/<observationID>.jpg`，只经 `/preview/face-crop/{id}` 读且服务侧校验必须落在 faces 目录内；`ClearFaceData` 只删三张人脸表 + 裁剪目录、不动 `people`/`video_people`/`image_people`；日志与逐项失败文案经 `scrubFacePaths` 抹掉媒体路径；通知只带计数。
- **设置:** `auto_face_analysis`（默认关）、`face_model_mirror_url`（默认空）；设置页新增「人脸识别」分区（`settings/FaceSection.vue`），**分析的启动/取消入口只在这里**（含等待空闲与立即运行、用量、清除二次确认、隐私披露）；扫描后自动化在开关开且运行时可用时经 `runGatedAutoTask("face", …)`，不可用静默跳过。命令面板可取消运行中的人脸分析，启动带 scope 参数不进面板。

### 2.26 人脸审阅与人物候选
- **服务:** `services/face_review_service.go`（无状态 `FaceReviewService`）。`ListFaceClusters(filter{status, media_kind})` 返回簇卡片：代表观测 id（前端拼 `/preview/face-crop/{id}`）、观测数与涉及视频/图片数（按观测行现算）、状态、已关联人物、候选人物、pending 追加数与涉及媒体名；按观测数降序。
- **写关系的唯一入口:** `NameFaceCluster`（事务：校验 unnamed → 建人物 → 按 `(media_kind, media_id)` 去重写两表（`ON CONFLICT DO NOTHING`）→ 簇 named + person_id → 删该簇候选 → 簇内观测 `append_status=confirmed`，首次命名即视为确认全部）、`LinkFaceCluster`（同上不建人物）、`ConfirmFaceClusterAppend`（只写 pending 观测涉及的媒体）。`IgnoreFaceCluster` / `DismissFaceClusterAppend` 不写关系；无「全部接受」。人脸链路里只有这个文件**写**这两张表（`face_analysis_service.go` 里的三处提及都是「绝不写」的注释）。
- **错误码:** `cluster_not_found` / `cluster_not_unnamed` / `cluster_not_named`（确认追加要求簇已命名）/ `person_name_invalid` / `person_not_found`。并发命名同簇靠事务内行锁 + `WHERE status='unnamed'` 的 RowsAffected 双保险，第二次必报 `cluster_not_unnamed`。
- **人物删除后:** 外键只把 `person_id` 置空，`reconcileFaceClusterPeople` 在列举与命名/关联/忽略前把 `status='named' AND person_id IS NULL` 的簇拉回 `unnamed`。
- **候选漂移:** `face_person_candidates` 只增不删（分析侧刻意如此），`ListFaceClusters` 用簇当前代表向量与人物头像种子重算相似度，<0.6 的不展示；人物头像换掉/没了的候选同样不展示。
- **软删除媒体:** 写关系时用 `Unscoped` 校验媒体行存在——软删除媒体照样建关系（与 `personHasRemainingRelations` 口径一致），只跳过已永久删除的。
- **绑定与事件:** `app_ai.go` 六个方法；成功后发 `face-review-changed`（无载荷）。
- **前端:** `FaceClusterReviewPanel.vue` 一个组件两处复用——视频侧 `AITagReviewDialog.vue` 第三个页签「人物候选」（`face-cluster-review-tab`），图片侧 `ImageAITagReviewPanel.vue` 顶部页签（`image-ai-tag-review-tab` / `image-face-cluster-review-tab`）。未命名簇三动作（命名新人物 / 关联现有人物（候选人物置顶）/ 忽略）；已命名簇只在有 pending 追加时出现（确认追加 / 忽略追加）。订阅 `face-review-changed` 与 `face-analysis-state` 局部刷新，刷新失败保留已有卡片。**卡片分批渲染（2026-09-07 修卡死）：** 簇数没有上限（每张没匹配上的脸都是一簇），面板只渲染前 20 张卡片（`CARD_PAGE_SIZE`），「显示更多（还有 N 组）」每次再放 20 张；窗口在事件刷新与动作后重拉时不缩回；回报宿主的 `loaded` 计数始终按全部簇算，与窗口无关。已知限制：后端 `ListFaceClusters` 仍一次返回全部簇（无服务端分页）、每次动作后整体重拉、没有「预览这一簇里的媒体」入口。**第三个入口（用户裁决 2026-09-07）**：人物页（`EntityLibraryPage`，`entityType=person`）列表视图顶部常驻「待命名人脸」分区，复用同一面板；面板加载后 `emit('loaded', { unnamed, appendPending })`，宿主据此写摘要、在没有待处理项时默认收起（用户手动切换过就不再自动收），面板 `changed` 触发人物列表重拉——命名/关联出来的人物立刻出现在下面的列表。
- **既有对照:** 扫描到新视频时 NFO 导入会按 2.15 语义自动写 `video_people`——那是既有的本地资料填空路径，与人脸链路无关。

### 2.20 桌面通知与 Dock 角标
- `services/desktop_notify.go` 定义 `DesktopNotifier{Notify, SetBadge}` 与 `DesktopNotificationCenter`；`desktop_notify_darwin.go` 是**仓库唯一的 cgo 文件**（`#cgo CFLAGS: -x objective-c -Wno-deprecated-declarations`、`LDFLAGS: -framework Cocoa`，`NSUserNotificationCenter` + `NSApp.dockTile`，全部 `dispatch_async` 到主队列，NSApp/center 判空不崩），`desktop_notify_other.go` 是 `!darwin` 空实现。`UNUserNotificationCenter` 需授权与 entitlement，有意推迟。
- 抑制规则集中在 `DesktopNotificationCenter`：窗口在前台不发、`desktop_notifications_enabled` 关掉不发、读不到设置也不发（fail-closed）；**角标不受这两条影响**。前后台由前端 `App.vue` 在 focus/blur/visibilitychange 与挂载时上报 `SetWindowForeground(bool)`，未上报默认按后台。
- 角标接在 `app.go` `startup` 的 `backgroundTasks.SetOnChange` 里：`BadgeLabelForCount(len(running))`，0 → 空串，与 `background-tasks` 事件并存。
- 已接入的触发点：字幕（完成/失败/需确认幻觉，`subtitle_queue.go`，取消不发）、超分（completed/failed，`enhancement_service.go`）、备份创建失败（`backup_service.go`，恢复失败与「转储成功但轮转失败」不发）、视频与图片语义索引完成/失败（两个服务的 `finish`）。转封装、帧哈希、人脸由各自切片接入自己的终态。
- 通知文案一律中文、含任务名与视频显示名，**不含失败原因**——识别/ffmpeg/pg_dump 的报错里常带绝对路径，通知会进系统通知中心留存。
- Settings 新列 `desktop_notifications_enabled`（默认 true，**无 gorm default 标签**，由 `migrateDesktopNotificationsSetting` + `ApplySchema` 新库显式行保证），入口在设置页「基本设置」分区，标注「仅 macOS」。真机投递效果与 cgo 对 `wails build` 签名的影响属真机验收项。

### 2.21 命令面板
- 快捷键 **⌘⇧P / Ctrl+Shift+P**（⌘K 早已归片库页「清理审阅」且菜单印有 ⌘K，命令面板不抢）；顶栏无按钮；焦点在输入框内仍生效；`event.defaultPrevented` 时让位；面板关闭时不拦截任何既有键（J/K、空格、F、W、T、回车、⌘F、⇧⌘N、⌘R、⌘T）。
- `frontend/src/utils/commandRegistry.js`：进程内注册表，`registerCommands(scopeKey, commands)` / `unregisterCommands(scopeKey)` / `commandList()` / `filterCommands(query)` / `isCommandEnabled(command)`；命令项 `{id, group ∈ navigate|action|task|video, label, keywords[], run(), enabled?()}`；分组顺序固定，同 scope 重复注册就地覆盖、同 id 只留先注册者；过滤权重 label 前缀 > label 包含 > keywords 包含，同权重按注册顺序；内部 `shallowRef` 版本计数器让 computed 跟随变化。
- `frontend/src/utils/taskCommands.js`：任务组三态（运行中·取消 / 等待空闲·立即运行 / 启动），输入是 `GetBackgroundTasks`/`background-tasks` 与 `GetIdleSchedulerStatus`/`idle-scheduler-state`；只收零参 `Start*`/`Cancel*` 绑定，需要选参数的任务（字幕、超分、语义索引、清理分析）闲置时不列出；`idle_gate_task_not_waiting` 不算失败。
- `frontend/src/utils/appCommands.js`：`appCommandsMixin` 被 `App.vue` 混入（App.vue 只留挂载点），负责全局快捷键、面板开关、导航组（6 个页面、11 个智能视图、保存视图、作品集与人物各懒加载前 50 条）、应用级动作（立即备份、设置各分区锚点——直接读 `SettingsPage` 的命名导出 `SETTINGS_SECTIONS`）、任务组随事件重建；智能视图清单是 `LibraryToolbar.vue` `smartViewOptions` 的副本，有防漂移断言。
- `frontend/src/components/CommandPalette.vue`：基于 `BaseModal`，只做过滤 + 视频搜索 + 执行；视频组不入注册表，输入 ≥2 字调 `SearchLibraryVideoPage`（limit 8），回车经 `openVideo` 函数 prop 回到片库页既有 `openPreview`。
- 页面注册：`VideoListPage`（`video-list`，另提供 `applySmartViewCommand` / `applySavedViewCommand`）、`PhotoLibraryPage`（`photo-library`）、`EntityLibraryPage`（`entity-person` / `entity-collection`，新增可选 prop `focusEntity` 承接跨页落点）、`InsightsPage`（`insights-page`）、`SettingsPage`（`settings-page`）。已知限制：作品集/人物只取前 50 条，不随输入词二次查询。

### 2.27 浏览器插件与桥接下载（D-B01..D-B06）
- 扩展在 `browser-extension/`（Chrome/Edge MV3，无构建步骤，原生 ESM 直接「加载已解压的扩展程序」）。分两层：`src/common/`、`src/download/` 是不碰 chrome API 的纯逻辑（HLS 解析、AES-128 解密、断点账本、下载主循环、文件名与 ffmpeg 命令拼装），有 Node 内置 test runner 的单测；`src/background/`、`src/offscreen/`、`src/content/` 是与 chrome API 的接线，靠人工验收。
- 嗅探走 `webRequest` **只读**监听（不阻塞不改写）：`onSendHeaders` 抓 Referer/Origin/User-Agent（事后没有第二次机会），`onHeadersReceived` 按「URL 后缀 + Content-Type」判定，播放列表正文特征（`#EXTM3U`）留到展开时验证。命中按标签页聚合、按去掉 fragment 的 URL 去重、单页上限 200 条；分片不逐条列出，只按所属目录归组，且同目录已看到 m3u8 时不再单列。
- **Cookie 只留内存**：不进命中记录、不写 chrome.storage、不出现在下载产物里；只有用户在选项页显式打开「推送时附带 Cookie」，它才会随那一次推送发给本机桌面端。
- 下载器挂在 offscreen 文档下的专用 Worker 里（MV3 的 service worker 随时被回收，长下载放不进去），分片经 OPFS 同步访问句柄增量落盘（几个 GB 不能堆内存），断点账本 `SequentialWriteLedger` 保证并发乱序取回的分片按序写盘——`nextIndex` 与 `bytesWritten` 永远自洽，续跑时按 `bytesWritten` 截断文件再从下一片开始。受限请求头经 `declarativeNetRequest` 会话规则附加，作用域三重限定（`tabIds:[-1]` + 主机 urlFilter + `xmlhttprequest`），任务结束即撤销。
- **D-B01**：浏览器内不做转封装。TS 流输出 `.ts`，fMP4 输出 `.mp4`；要规范 mp4 走桥接（ffmpeg `-c copy`）。**D-B02**：SAMPLE-AES 与 DRM `KEYFORMAT` 在解析阶段就判为不可下载并给出原因，绝不产出半成品。直播流（无 `EXT-X-ENDLIST`）同样不接。
- 桥接服务 `services/browser_bridge_server.go`（**D-B03**）：只绑 `127.0.0.1`、端口 18110..18130、除 `ping` 外每请求带 `X-CineInsight-Token` 定长比较、带 Origin 的只放行 `chrome-extension://`；请求体纪律与手机端 feed 同口径（JSON / 1 MiB / 拒绝未知字段）。与 `short_feed_server.go` 是两条边界完全不同的通道，不要照着其中一条理解另一条，对照表见 `docs/browser-extension.md`。
- 下载队列 `services/browser_download_service.go`（**D-B04**、**D-B05**）：并发 1–4 默认 2，**不进空闲门也不占 `MediaWorkSlot`**（用户显式发起；`-c copy` 是网络/IO 密集，占重媒体槽会被超分饿死），后台任务登记表新 key `browser_download`。入口校验把外部输入一律当不可信：协议只认 http/https（ffmpeg 认得 `file:`/`concat:`/`pipe:`，放任等于交出读本机文件的能力，命令行另带 `-protocol_whitelist`）、请求头值禁 CR/LF、参数直接给 exec 不经 shell、文件名清洗后先用 `O_EXCL` 占住再下载（重名另起名字，绝不覆盖已有文件）。落盘后调既有 `SyncAffectedDirectories` 入库；**下载目录不自动加进扫描目录**，不在扫描范围时如实报「文件已保存但没有入库」——下载失败与入库失败是任务上两个不同字段。
- 设置四列：`browser_bridge_enabled`（默认 false，零值即默认）、`browser_bridge_token`（**有意不走 `UpdateSettings`**，只由 `RegenerateBrowserBridgeToken` 写——走通用保存的话前端漏带一次就会把令牌抹空、已配对的插件静默断开）、`browser_download_directory`（为空拒绝建任务）、`browser_download_concurrency`（0 在这里是非法值而非"不限"，因此可以带 gorm default）。**D-B06**：令牌由桌面端生成、用户手工复制，不做自动配对与发现广播。

## 3. 关键目录说明 (Directory Structure)

- `/services`: **核心业务层**（Video, VideoDetail, MediaProbe, TechnicalBackfill, Person, Collection, Subtitle, SubtitleWorkbench, LibraryWatcher, LocalMetadata, AIQuality, Tag, Settings, Directory 服务）。
- 根目录 `app_*.go` 与 `/services/video_*.go`: **按领域拆分的同包多文件**（行为保持，签名与 Wails 绑定不变）。`app.go` 只留 `App` 结构体、构造、`startup`/`shutdown` 与日志，绑定方法分到 `app_video.go`、`app_library.go`（片库查询、保存视图、扫描同步与洞察）、`app_media_details.go`（视频详情、本地元数据、标签、人物、作品集）、`app_ai.go`（AI 打标、语义索引、同源关系）、`app_subtitle.go`、`app_cleanup.go`、`app_image.go`、`app_settings.go`（设置、备份恢复、后端切换、扫描目录与监听）、`app_tasks.go`（后台长任务的启动/状态/取消，含超分）；`VideoService` 拆为 `video_service.go`（核心 CRUD 与回收站）、`video_pagination.go`、`video_scan.go`、`video_rename_move.go`、`video_random.go`、`video_playback.go`。
- `/models`: **数据模型层**（GORM 结构体定义）。`schema.go` 的 `AllModels()` 顺序有约束：**被外键引用的表一律排在引用方之前**（双向迁移器按这份清单逐表复制，顺序错会撞外键），由 `models/schema_test.go::TestAllModelsIsTopologicallyOrdered` 用 `schema.Parse` 解析关联守住（曾抓出 `Image` 排在 `ShortFeedImageInteraction` 之后、`FaceCluster` 排在 `FaceObservation` 之后两处缺陷）；`database/migrator` 的往返测试夹具每表至少一行，任一表为空即失败。
- `/database`: **持久化层**（数据库连接、迁移与初始化）。
- `/frontend/src/components`: **UI 组件**（Vue 组件）。
  - `video-list/`：`VideoListPage.vue` 拆出的 13 个子组件（工具栏、批量操作栏、清理审阅面板、字幕生成/预览弹窗、超分弹窗、重命名与保存视图弹窗、后台任务状态条、增量扫描条、语义提示条、随机批次条、删除撤销条）与 3 个共用模块（`runtimeEvents.js` 事件注册 mixin、`format.js`、`wheelForwarding.js`）。父子只经 props / emit / 父→直接子 `$refs`；需要保持 `await` 顺序的片库侧动作用函数 prop 回调。
  - `settings/`：`SettingsPage.vue` 按分区拆出的 10 个子组件；父组件只留锚点导航、`settingsForm` 与 `saveSettings`，分区通过 `form` prop 改字段。
  - `frontend/src/utils/idleScheduling.js` 收口后台任务名与等待原因的中文口径，设置页与状态条共用。
  - `frontend/src/utils/{commandRegistry,taskCommands,appCommands}.js` 与 `frontend/src/components/CommandPalette.vue`：命令面板（见 2.21）。
  - `services/desktop_notify*.go`：桌面通知与 Dock 角标，`_darwin.go` 是仓库唯一 cgo 文件（见 2.20）。
  - `services/playback_proxy_*.go` + `models/playback_proxy.go`：播放代理（见 2.23）；`services/frame_hash_service.go`、`clip_match.go`、`clip_cleanup.go` + `models/frame_hash.go`：帧哈希与截取片段（见 2.24）；`services/face_*.go` + `face_worker.py` + `models/face.go`：人脸运行时与分析（见 2.25）；`services/collection_suggestion_*.go`：建议作品集（见 2.22）。
  - `frontend/src/components/settings/`：分区组件现为 13 个（含 `IdleSchedulingSection`、`ProxySection`、`FaceSection`）；`frontend/src/utils/playbackProxy.js` 收口代理结果码与文案。
  - `frontend/scripts/data-test-set.test.mjs` 是 `data-test` 钩子集合守卫（基线 `.loopx/workspace/2026-09-02-capability-batch/baseline/data-test-set-2026-09-02.txt`，允许新增、不允许缺失），已挂进 `npm test`。
- `/browser-extension`: **Chrome/Edge MV3 扩展**（见 2.27）。纯逻辑在 `src/common`、`src/download` 且有单测；`services/browser_bridge_server.go` 与 `services/browser_download_service.go` 是桌面端这一侧，安全边界写在 `docs/browser-extension.md`。
- `/frontend/src/utils`: **前端纯函数工具层**（如虚拟列表窗口计算与缓存工具）。

## 4. 开发与构建指南 (Development & Build)

- **开发模式:** `wails dev`
- **构建应用:** `wails build`
- **数据库:** 当前通过 `.env` 中的 PostgreSQL 配置连接。

## 5. 开发规范与后续演进

- **规范:** Go 方法导出 PascalCase，JSON 映射 snake_case。
- **代办:** 补齐首页虚拟列表和媒体 seek 的更强组件级自动化验证。
