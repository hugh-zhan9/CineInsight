---
source: docs/loopx/design/2026-08-07-image-library/需求设计文档.md §4.6.6（2026-08-08 裁决修订，推翻 D-009、修订 D-010、连带推翻 intake 的 AC-5）；2026-08-18 补充裁决见 Goal And Boundaries
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
    depends: []
  - id: P-005
    status: done
    depends: [P-003]
  - id: P-006
    status: done
    depends: [P-004, P-005]
  - id: P-007
    status: done
    depends: [P-006]
---

# 图片 AI 产出由「描述」改为「标签候选」

## Goal And Boundaries

目标：把图片的 AI 产出从「一段中文自然语言描述」换成「标签候选」，走与视频完全一致的
「候选 → 人工审阅 → 接受/拒绝」流程，接受后写入 `image_tags`，标签词表与视频共享 `tags` 表。
改造完成后，`image_ai_descriptions` 这条链路在代码与数据两个层面都不再存在。

### 已经定稿、执行时不得重开的结论

设计文档 §4.6.6（2026-08-08）：

- 图片 AI 产出**标签候选**，不再产描述；标签词表与视频共享 `tags` 表。
- 语义索引文本 = `标题 + 标签`，不再含描述；**无描述不再是跳过索引的理由**。
- 明确接受的代价：描述原本是图片索引文本里唯一有实质信息量的部分，改为「标题 + 标签」后，
  图片语义检索会明显弱于改造前，接近关键词匹配。用户已知悉并接受，不要在实现中试图补偿。
- 保持不变：视频 AI 打标全链路；AI 数据外发边界（仅降采样图像 + 提示词）；
  `ai_tagging_*` 配置只读复用，不新增独立配置或 Token 项。

2026-08-18 补充裁决：

- **表策略：并行复制不泛化。** 新建 `image_ai_tag_candidates` / `image_ai_tag_approval_records` /
  `image_ai_tagging_states` 三张图片侧表，外键绑 `images`。视频侧 `ai_tag_candidates` /
  `ai_tagging_runs` / `ai_tag_approval_records` / `ai_tagging_states` / `ai_tag_agent_steps`
  **一字不动**，不改多态。这与当年 `images` 表族的「方案B」一致。
- **旧数据一次性删除，且在启动时检查删除。** 残留的 `image_ai_descriptions` 行与对应的
  图片语义向量在应用启动时被检查并删除，程序幂等（无表/无行即空转）。不可逆，独立切片承载。
- **展示位：卡片与手机端 Feed 换成展示标签；清理审阅页直接移除该区块。**
  近似重复的两张图标签几乎一定相同，标签在重复判断上信息量为零甚至误导，
  该处靠已有的目录/尺寸/时间/缩略图对比判断，不硬塞标签。

### 与视频侧的对齐范围

对齐的是**契约与交互**，不是实现复用：

| 对齐 | 不对齐 |
|---|---|
| 候选状态机 `pending/approved/rejected/superseded` | 视频的多轮 agent（`request_more_frames` / `request_transcript` / `find_same_source` 对静图无意义），图片是单轮请求 |
| 审批语义：接受写官方关联 + 落审批记录；已有手工官方标签则把待审候选置 `superseded` | `ai_tagging_runs` 历史表（D-009 原则保留：AI 质量评估不覆盖图片） |
| 打标状态机 `pending/processing/completed/skipped/failed` + 证据指纹幂等 | `ai_tag_agent_steps`（无 agent 轮次） |
| 闭合标签词表约束（`GetAITagLibrary` 只读复用） | 视频的提示词构造（含字幕与抽帧描述） |

可复用的纯函数级资产：`formatClosedTagLibraryForPrompt`、`mergeAITagSuggestions`、
`aiConfidenceRank`、`truncateRunes`、`AITagSuggestion` 结构、脱敏与截断助手。

### 保护行为

- 视频 AI 打标全链路（候选/审批/同源/质量评估）行为与数据零改动。
- `tag_service` 的 `DeleteTag` / `MergeTags` 双表分支（D-002）不得回退。
- 图片外发安全承诺：送往 AI 端点的图像必须是缩略图管线产物且经
  `StripJPEGMetadataForUpload`（`services/image_exif.go:755`）剥离元数据。
  这条不是性能优化，是 EXIF/GPS 不外泄的唯一保证。
- 照片页的文件夹视图、时间线分组、网格虚拟化、语义/关键词搜索切换行为不受本次改造影响。

### 非目标

不做：图片进 AI 质量评估或同源检测；图片打标的独立配置项或开关；视频页文件夹视图；
为语义检索变弱做任何补偿性设计（例如保留描述做双路索引）。

## P-001 图片 AI 标签候选表族落地

新建三张图片侧表并接入 `AutoMigrate`，为后续切片提供落库结构。

`image_ai_tag_candidates` 镜像 `AITagCandidate` 的字段与索引形态，`video_id` 换 `image_id`
（外键 `images` + CASCADE），保留 `suggested_name` / `normalized_name` / `matched_tag_id` /
`confidence` / `reasoning` / `source_summary` / `status` / `approved_at` / `rejected_at`，
不带 `run_id`（图片侧无 run 历史表）。`image_ai_tag_approval_records` 以 `(image_id, tag_id)`
唯一、`candidate_id` 唯一，用途与视频侧一致：它是「哪些 `image_tags` 关联是 AI 接受产生的」
的唯一依据，审批逻辑靠它反推手工标签，不能省。`image_ai_tagging_states` 以 `image_id` 唯一，
承载 `status` / `skip_reason` / `evidence_fingerprint` / `attempt_count` / `last_error` /
`last_processed_at`。

完成的判据：三张表在迁移后存在且索引与视频侧同形；图片硬删会级联清掉候选、审批记录与打标状态；
视频侧五张 `ai_tag*` 表的结构与索引在迁移前后逐项一致。

显式索引与视频侧同路：`ensureAITaggingIndexes`（`database/database.go:441`）为视频候选表补的
那批索引，图片侧新增一个同形的 `ensureImageAITaggingIndexes` 承担，不要塞进
`ensureImageQueryIndexes`——那个函数的语义是「照片页查询索引」。

> writes: `models/image_ai_tagging.go`, `models/schema.go`, `database/database.go`, `database/image_schema_test.go`
> anchors: `D-009（撤销后重立的 data contract）`, `AC-5（4.6.6 重写版）`
> verify: `go test ./database/... ./models/...`；新增用例断言三表存在、索引列顺序、CASCADE 生效，并断言视频侧 ai_tag* 表结构未变
> review: 外键与唯一索引是否与视频侧同形；是否误改了 `ai_tag_candidates` 等视频侧结构

## P-002 图片 AI 打标服务产出候选

新建图片打标服务，输入单张图片、输出落库的标签候选，替代 `ImageAIDescriptionService` 的产出职责。

取图沿用描述服务已经验证过的路径：`ImageThumbnailService.ResolveImageThumbnail` 拿 JPEG，
经 `StripJPEGMetadataForUpload` 剥元数据后再 base64 送出。请求是单轮 chat 多模态，
`temperature 0.1`、5 分钟超时、无客户端重试（重试语义由 `attempt_count` 承担）。
提示词要求模型只从闭合标签词表里选标签并输出 JSON（标签名 + 置信度 + 理由），
沿用 `formatClosedTagLibraryForPrompt` 的词表呈现与 `AITagSuggestion` 的解析结构。
标签词表为空时该图记 `skipped`，不发请求。

任务形态镜像现有描述任务：批量三件套（启动、查状态、取消，单 worker，进度事件推送）+
单张重跑；启动时把残留 `processing` 复位为 `failed/interrupted`。

**目标集在实现时收窄为「只排除 processing」**（初稿写的是「无 completed 且非 processing」）。
原写法会让证据指纹判定变成永远走不到的死代码，而且复刻了视频侧 `findUntaggedVideos` 的一个
真实缺陷：completed 与 skipped 都被排除，导致标签库扩充后已打标的媒体永远不会按新词表重评，
「词表为空时跑过一次」的媒体更是永久停在 skipped。改为只排除 processing 后，
「这张图要不要再发一次 AI」由证据指纹独任，不发请求的跳过路径代价只有一次状态查询。

证据指纹 = 图片身份（id/path/name/size）+ 标签库快照哈希。**刻意不含 `PerceptualHash` 与
`HashSourceModTimeNS`**：解析缩略图会顺带回填这两列，把它们算进指纹等于把打标自己的副作用
算了进去，第一次跑完指纹必然失配，幂等判定会永远失效（实现时踩过一次）。

自动触发保持与描述链路同样的时机——应用启动与图片扫描完成后增量跑，
AI 配置缺失或任务运行中静默跳过。缩略图不可得（非 darwin 的 HEIC/RAW、损坏文件）记
`failed/decode_unsupported`，批量继续。

完成的判据：mock client 下，一张图能产出待审候选并落 `pending`；重复跑同一张图在证据指纹未变时
不重复产候选；取消能停在当前图并让它回到可重跑状态；配置缺失时启动被明确拒绝而不是静默成功；
外发请求体里只有缩略图 JPEG 与提示词，且该 JPEG 已剥元数据。

> writes: `services/image_ai_tagging_service.go`, `services/image_ai_tagging_client.go`, `services/image_ai_tagging_service_test.go`, `app.go`, `frontend/wailsjs/go/**`
> anchors: `AC-5（4.6.6 重写版）`, `D-009（重立）`, `TC-4（重写：描述状态机单测 → 打标状态机单测）`, `4.6.5 外发边界不变`
> verify: `go test ./services/ -run 'ImageAITag'`；请求体断言用例覆盖「只含缩略图 + 提示词」与「JPEG 已过 StripJPEGMetadataForUpload」；`gofmt -l .` 无输出
> review: 外发 payload 是否可能包含原图、路径、EXIF 或 API key；标签词表约束是否真的闭合（模型自造标签必须被丢弃或标记为不可接受）

## P-003 候选审阅与接受写入官方标签

把候选变成用户可处置的东西：照片页内可以看待审候选、接受或拒绝，接受后标签出现在图片上。

后端提供列出候选、接受、拒绝、按图片批量拒绝、单图重跑五个能力。接受的事务语义与视频侧
`ApproveCandidate` 逐条对齐：只接受 `pending` 且置信度为 high/medium 的候选；若该图已有
手工官方标签（即存在不在审批记录里的 `image_tags` 关联），则把该图所有待审候选置 `superseded`
而不是写入；正常路径下解析出官方 tag、`INSERT ... ON CONFLICT DO NOTHING` 写 `image_tags`、
落审批记录、把同图同名的其他待审候选置 `superseded`。图片已被软删则拒绝接受。

审阅能力落在新文件 `services/image_ai_tagging_review.go`（而不是塞进已有 700 余行的
`image_ai_tagging_service.go`），另加一个 `GetImageAITaggingSummary` 供照片页显示待审徽标。

前端在照片页工具栏「清理审阅」旁提供入口（带待审条数徽标）。不复用 `AITagReviewDialog.vue`
——那个组件 1000 余行，内嵌 AI 质量面板、同源审阅与视频手工打标对话框，全是视频专属；
图片侧新建 `ImageAITagReviewPanel.vue`，只做「按图片分组的候选列表 + 接受/拒绝/整图全拒」。

界面上有一处必须交代清楚：接受候选时若该图已有手工标签，后端返回的是 `superseded`
而不是报错，标签不会挂上。用户点的是「接受」，界面得说明为什么没反应——这条说明走独立的
notice 位而不是 error 位，否则会被紧随其后的列表刷新清掉（实现时踩过一次）。

完成的判据：接受一个候选后该标签出现在图片上且照片页按该标签能筛到它；拒绝后候选不再出现在
待审列表且不产生 `image_tags` 关联；对已手工打标的图片接受候选不会写入标签而是全部置
`superseded`；视频侧的候选审阅行为与既有测试结果不变。

> writes: `services/image_ai_tagging_review.go`, `services/image_ai_tagging_review_test.go`, `app.go`, `frontend/wailsjs/go/**`, `frontend/src/components/ImageAITagReviewPanel.vue`, `frontend/src/components/ImageAITagReviewPanel.test.js`, `frontend/src/components/PhotoLibraryPage.vue`
> anchors: `AC-5（4.6.6 重写版）`, `AC-4（复用不得破坏）`, `D-002（tag_service 双表分支不回退）`, `TC-3`, `TC-4（重写）`
> verify: `go test ./services/ -run 'ImageAITag|TagService'`；`cd frontend && npm run test:components`
> review: 接受路径的事务边界与并发（两次接受同一候选、接受与删除图片竞争）；手工标签判定是否会把 AI 接受过的标签误判为手工

## P-004 语义索引文本去掉描述段

按 §4.6.6 修订 D-010：图片语义索引文本从「标题 + 标签 + 描述」改为「标题 + 标签」，
并删掉「无 completed 描述则 Skipped」这条跳过规则（`services/image_semantic_index_service.go:285-296`）。
`loadCompletedDescription` 及其调用点一并移除。

索引文本变化会让所有已有图片向量的内容指纹失配，这是预期的：下次索引任务会按新文本重建。
本切片不主动触发重建，也不递增 `SemanticIndexProfile.Generation`（generation 是全局的，
递增会连带作废视频向量，不在本次范围）。

完成的判据：没有描述的图片会进入索引而不是被计入 Skipped；索引文本与指纹只由文件名和标签决定，
同图在标签不变时指纹稳定、标签变化时指纹变化；视频侧索引文本构造与统计口径一字未动。

> writes: `services/image_semantic_index_service.go`, `services/image_semantic_index_service_test.go`
> anchors: `D-010（4.6.6 修订版）`, `AC-6（修订版）`, `TC-4（重写：索引/检索单测）`
> verify: `go test ./services/ -run 'ImageSemantic'`；用例覆盖「无描述不再 Skipped」「指纹随标签变化」

## P-005 展示位由描述改为标签

把三处仍在读描述的界面改掉，同时替换照片页的描述维度筛选。

照片卡片改为展示该图已接受的标签；清理审阅页（`services/image_cleanup.go:448`）**移除描述区块**，
不补标签——近似重复的两张图标签几乎一定相同，放在那里帮不上判断。

手机端 Feed 这一处比计划初稿预计的小得多（2026-08-18 核实）：`imageDTO`
（`services/short_feed_service.go:1106`）**已经在下发 `Tags` 了**，描述只是它额外多查的一次
（1116-1124 行）。所以这里只是删掉那段描述查询与 DTO 的 `Description` 字段，标签本来就在，
不新增任何查询。`b5bc922` 已经把「整库预载标签」改成「只为选中项 `Preload("Tags").First`」，
参数量与库大小无关——初稿里担心的重演 Postgres 参数上限在这条路径上不成立。
`ImageFilter.AIDescriptionState`（described/undescribed）替换为打标维度的筛选，
取值来自 `image_ai_tagging_states` 与待审候选：未打标 / 有待审候选 / 已完成。
`PhotoAITaskPanel.vue` 的任务面板改接 P-002 的打标任务三件套。

这是本次唯一的对外接口不兼容改动：`ImageFilter` 少一个字段、多一个字段，`GetImage*` 系列
筛选语义随之变化。消费方只有本仓库前端，不存在外部调用方。

完成的判据：三处界面不再出现任何描述文本；照片页按打标状态筛选能正确分出三类；
手机端 Feed 的图片项带标签且视频项行为不变；文件夹视图、时间线分组、虚拟化滚动无回归。

> writes: `services/image_library_service.go`, `services/image_library_service_test.go`, `services/short_feed_service.go`, `services/short_feed_service_test.go`, `services/image_cleanup.go`, `services/image_cleanup_test.go`, `frontend/src/components/PhotoLibraryPage.vue`, `frontend/src/components/PhotoLibraryPage.test.js`, `frontend/src/components/PhotoAITaskPanel.vue`, `app.go`, `frontend/wailsjs/go/**`
> anchors: `AC-5（4.6.6 重写版）`, `D-017（Wails 接口清单变更）`, `2026-08-18 展示位裁决`
> verify: `go test ./services/ -run 'ImageLibrary|ShortFeed|ImageCleanup'`；`cd frontend && npm run test:components && npm run test:short-feed`
> review: 打标状态筛选的 SQL 是否随图片数量线性膨胀参数；照片卡片展示标签是否引入 N+1 查询

## P-002/P-003 独立评审结论与修复（2026-08-18）

P-002 触及 AI 外发边界，按工作约定做了独立评审。外发边界本身守住了（不外发原图、
剥元数据 fail-closed、请求体与日志无路径/EXIF/key、闭合词表真闭合），但评审发现一批实现缺陷，
已全部修复并各自补了回归测试。记在这里是因为其中几条是**容易再犯的口径问题**：

| 缺陷 | 后果 | 修法 |
|---|---|---|
| 用 `hasNonAutomaticTags` 判「已手工打标」 | AI 标签库里的标签 `AutomaticKind` 全为空串，用户接受一个候选后这张图就被误判成手工打标，**永久停在 skipped**，正好废掉本计划的核心改进 | 改用减去审批记录的 `hasManualOfficialImageTags`，与审阅路径同口径 |
| `excluded.attempt_count + 1` | excluded 是待插入行，其值恒为 1，所以 attempt_count 恒等于 2 而非自增 | 改用表名限定 `image_ai_tagging_states.attempt_count + 1`（Postgres 的歧义问题靠限定解决，不是靠 excluded） |
| 指纹用 `tagLibraryHash`（id+name+updated_at） | 改个标签颜色就改写 updated_at → 整库图片指纹失配 → 启动自动触发把**整个图库无人值守重发一遍** | 指纹只哈希提示词里真正出现的 namespace+name |
| 重打时不处理旧候选 | 被拒绝的标签会复活让用户反复拒；模型不再建议的标签永远挂在待审列表 | 重打前整体 supersede 旧待审候选；已拒绝的标签不再重复推送 |
| 批量与单张重跑的认领是单向的 | 重跑可在批量 check 与执行之间插入，同一张图发两次 AI、状态互相覆盖 | 两条路径共用同一张认领表；并加 `(image_id, normalized_name) WHERE status='pending'` 部分唯一索引兜底 |
| `rollbackTaggingPending` 无状态守卫 | 取消可能把另一条路径刚写的 completed 踩回 pending | 只回退仍停在 processing 的行 |
| `source_summary` 用解析缩略图之前的宽高 | 首轮打标记的是 0x0 | 取图后重读一次宽高 |
| `is_stale` 图片留在目标集 | 每轮都失败一次，刷纯噪音留痕 | 目标集排除 is_stale |
| 标签库变更不同步图片候选 | 改名/停用后待审候选仍显示旧名、指向已停用标签 | 在 `tag_service.SaveAITagLibrary` 里作废受影响的图片候选（**不是**像视频侧那样就地改名——图片侧有部分唯一索引，改名会撞键让整个保存事务失败；作废是自愈的，因为指纹含标签名，改名本就触发重打） |

测试侧同时修掉：httptest handler goroutine 里的 `t.Fatalf`（会 Goexit 且不写响应）、一条永真断言、
以及「失败可重跑」「停用标签不进提示词」「改颜色不触发重打」三个没被钉住的行为。
F1 的回归测试做过变异验证：把修复改回旧写法，测试确实失败。

一条**未修**的既有隐患，记录待办：`boundedError`（`services/perceptual_hash_service.go:358`）
按字节截断可能切断中文产生非法 UTF-8，写进 Postgres 的 text 列会被 22021 拒绝，失败留痕反而丢失。
这是既有 helper，不在本计划范围。

## P-006 描述链路代码下线

删除描述产出链路本身：`services/image_ai_description_service.go` 及其测试、
`app.go` 的四个 Wails 绑定（`StartImageAIDescription` / `GetImageAIDescriptionStatus` /
`CancelImageAIDescription` / `RegenerateImageAIDescription`）、`resetImageAIDescriptionService`
与 `triggerImageAIDescriptionAuto` 及其启动与扫描后的调用点、`models.ImageAIDescription`
与 `Image.AIDescriptions` 字段、`models/schema.go` 的 `AutoMigrate` 条目、
前端残留的描述相关调用与生成物。

本切片执行时 `image_ai_descriptions` 表已经没有任何读取方（P-004 与 P-005 已断开），
删除代码后它成为孤儿表，数据仍在——数据清理由 P-007 承担。

完成的判据：全仓搜索 `AIDescription` / `ai_description` / `aiDescription` 在
非归档文档外零命中；`go build ./...` 与全量测试通过；Wails 绑定生成物与 Go 侧一致
（不能只删 Go 不重生成 `.d.ts` / `.js`）。

> writes: `services/image_ai_description_service.go`（删除）, `services/image_ai_description_service_test.go`（删除）, `services/image_exif_test.go`, `services/image_library_service.go`, `services/image_library_service_test.go`, `services/image_semantic_index_service_test.go`, `services/image_semantic_search_test.go`, `models/image.go`, `models/schema.go`, `database/image_schema_test.go`, `app.go`, `frontend/wailsjs/go/**`, `frontend/src/components/PhotoLibraryPage.test.js`
> anchors: `D-009（撤销条款的代码清除）`, `AC-8 / D-016（视频侧零回归）`
> verify: `go build ./...`；`go test ./...`；`gofmt -l .` 无输出；`GOPRIVATE=none GOSUMDB=off GOFLAGS= GOPROXY=https://goproxy.cn,direct wails generate module` 后 `git diff --exit-code frontend/wailsjs`
> review: 是否有绑定被删掉但前端仍在调用；是否误删了图片缩略图或 EXIF 侧共用的代码

## P-007 旧描述与图片语义向量的启动清理

不可逆的数据清理，单独一个切片，单独审阅。

应用启动时运行一段幂等清理程序：检查 `image_ai_descriptions` 是否存在，存在则删除全部图片
语义向量与其索引/尝试记录（`image_semantic_vectors` / `image_semantic_indexes` /
`image_semantic_index_attempts` 的图片侧行）、删除全部描述行、`DROP TABLE image_ai_descriptions`。
表已不存在时整段空转。清理用原始 SQL 与 migrator 完成，不依赖 P-006 已删除的模型。
清理前后各写一条带行数的日志，让「删了多少」可查。失败时记录错误并原样退出，
留给下次启动重跑，不做降级路径。

挂载位置：`database.Init()` 里现有的启动清理序列（`cleanupDuplicateVideos` /
`cleanupReimportedSoftDeletedVideos`，`database/database.go:258,306`）就是这类程序的既定位置，
新程序单独成文件挂进去，不要往 `database/maintenance.go` 里加——那个文件是维护模式闸门，
职责不同。

删除向量的直接后果：图片语义检索在用户手动重跑一次图片语义索引任务之前**返回空结果**。
这是裁决接受的代价，不要用「向量缺失时回退关键词搜索」之类的兜底把它盖住。

完成的判据：在一个装有描述行与图片向量的库上启动一次，描述表消失、图片语义向量行清零、
日志有删除计数；再启动一次是纯空转且不报错；视频语义向量与视频索引记录一行未动。

> writes: `database/image_description_cleanup.go`, `database/image_description_cleanup_test.go`, `database/database.go`
> anchors: `4.6.6 第三条（已生成描述与其语义向量按用户要求删除）`, `2026-08-18 补充裁决（启动时检查删除、幂等）`
> verify: `go test ./database/... -run 'ImageDescriptionCleanup'`，用例覆盖有数据、无数据、表已删除三种入口，并断言视频侧向量未受影响；人工跑一次应用启动确认日志计数
> review: 删除范围是否精确限于图片侧（任何触及 `video_semantic_*` 的语句都是缺陷）；DROP 与 DELETE 是否在同一事务内保证不会留下半清理状态

### P-006/P-007 执行记录（2026-08-18）

P-006 顺带删掉了 `ImageDetail.AIDescription`：详情接口不再回填任何 AI 产出。
候选走独立的审阅接口，因为接受/拒绝之后要能单独刷新，塞进详情会逼前端为了一条候选重拉整个详情。

P-007 踩到一个**表名漂移**，值得记下来：清理程序最初按设计文档写死了 `image_semantic_indexes`，
但 GORM 的复数化实际把 `ImageSemanticIndex` 映射成 **`image_semantic_indices`**，
`HasTable` 永远为假、索引记录会被静默漏删。设计文档（4.7.2、5.x 的 ER 图）与实现从一开始就对不上。
修法是不再手拼表名：模型还在的表直接把模型交给 GORM 解析，只有
`image_ai_descriptions`（模型已删）与 `image_semantic_vectors`（无模型，原始 DDL 建）写字面量。
这条已用变异验证钉住：把表名换回字面量，测试立刻失败。

顺带澄清一个容易踩的前提：`image_semantic_indexes/attempts` 不在 `models.AllModels()` 里，
生产由 `PrepareImageSemanticVectorStorage` 单独 AutoMigrate，测试夹具必须自己补建。

## Integration And Final Verification

- 全量 `go test ./...` 与 `cd frontend && npm test`，与改造前基线逐项对比，通过率不降（AC-8 / D-016 / TC-6）。
- 视频侧不变行为专项回归：AI 打标候选/审批/同源/质量评估四条链路的既有用例全绿；
  `ai_tagging_*` 配置字段语义未变；`tag_service` 的 `DeleteTag` / `MergeTags` 双表用例全绿（D-002 / TC-3）。
- 端到端人工验证（替代原 TC-4）：对一张海边照片触发打标 → 候选出现在照片页审阅入口 →
  接受「海边」「日落」→ 标签出现在图片上、照片页按标签能筛到 → 跑一次图片语义索引任务 →
  照片页语义搜索「海边日落」命中该图 → 视频页搜同词不返回任何图片。
- AI 外发边界 diff 审查：确认送往端点的只有缩略图 JPEG 与提示词，且 JPEG 已过
  `StripJPEGMetadataForUpload`（4.6.5 安全承诺）。
- 文档回写：`docs/loopx/design/2026-08-07-image-library/需求设计文档.md` 的 4.6 全节按落地结果
  更新（4.6.2 的描述契约替换为标签候选契约，4.6.6 标为已实施），并在 §10 排期表补一行。
  intake 的 AC-5 已被 4.6.6 推翻，在设计文档里注明其失效而不是改写 intake 原文。

## Handoff And Residual Risks

- Blockers: 无。三项待决（表策略、旧数据处理、展示位）已于 2026-08-18 裁决，见 Goal And Boundaries。
- **未做的验证（需要真实环境，交给使用者）**：Integration 里的端到端人工验证还没跑过——
  自动化覆盖到的是 mock client 与 SQLite 测试库，没有真跑过一次「配置真实 AI 端点 →
  启动应用 → 观察清理日志计数 → 打标 → 审阅接受 → 重跑语义索引 → 语义搜索命中」的完整链路。
  尤其是 P-007 的启动清理只在 SQLite 上验证过，Postgres 上的 DROP TABLE 行为未实测。
- Residual risks:
  - **图片语义检索显著变弱**，且在 P-007 之后、用户手动重跑索引之前**完全无结果**。已裁决接受。
  - 标签词表为空或过窄时，图片打标会大面积 `skipped` 且用户看不到候选。这是闭合词表的固有结果，
    不在本次范围内做自动扩表；若用户反馈「跑了没反应」，先查 AI 标签库是否为空。
  - **本次范围外、但值得记下的既有隐患**：`ImageSemanticIndexService.run` 用
    `Preload("Tags").Find(&images)` 一次性把整库图片连标签读出来
    （`services/image_semantic_index_service.go:269`），形状与 `b5bc922` 修掉的那个
    Postgres 65535 参数上限问题一致。这是 P-004 之前就存在的写法，本次未引入也未加重；
    但 P-004 之后进入索引的图片从「有描述的少数」变成「全部」，一旦图库规模上来，
    这条路径会先撞墙。不在本计划范围内修，需要时单开。
  - 视频页仍无文件夹视图，照片页有——两边展示模式不对齐，本次不处理。
- Resume note: 全部切片已完成，无待续内容。改动尚未提交，工作区即当前状态。
