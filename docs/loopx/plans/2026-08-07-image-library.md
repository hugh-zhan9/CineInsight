---
source: docs/loopx/design/2026-08-07-image-library/需求设计文档.md（AC-1..AC-11 / D-001..D-017 / TC-1..TC-9；intake：.loopx/intake/2026-08-07-image-library/）
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
    depends: [P-001]
  - id: P-005
    status: done
    depends: [P-002]
  - id: P-006
    status: done
    depends: [P-002, P-003, P-004, P-005]
  - id: P-007
    status: done
    depends: [P-003]
  - id: P-008
    status: done
    depends: [P-007]
  - id: P-009
    status: done
    depends: [P-006, P-007, P-008]
  - id: P-010
    status: done
    depends: [P-003, P-005, P-006]
  - id: P-011
    status: done
    depends: [P-001]
  - id: P-012
    status: done
    depends: [P-001, P-003, P-004]
  - id: P-013
    status: done
    depends: [P-006, P-012]
---

# CineInsight 图片管理

## Goal And Boundaries

目标：把图片（含 macOS 上的 HEIC/RAW）做成与视频并行的一等实体——独立扫描入库、独立照片页浏览与查看、共享标签体系、收藏/评分、AI 中文描述、照片页内语义检索、精确/近似重复清理审阅、可恢复回收站、洞察图片维度。

设计已定稿（`docs/loopx/design/2026-08-07-image-library/需求设计文档.md`，提案已获批准），以下结论执行时不得重开：

- **并行复制不泛化**：八张新表（`images` 族），不给 `videos` 加字段、不做多态外键；只复用纯函数级资产（`TrashService`、`differenceHash`、`getPartialHash`、脱敏/截断助手）。
- **解码矩阵**（D-006）：常规格式走 ffmpeg；HEIC/RAW 仅 darwin 走 `sips`（build tag 隔离，超时 30s）；其他平台占位降级。缩略图独立缓存目录 `~/.video-master/image-thumbnails/`。
- **AI 描述**（D-009）：chat 多模态单图请求（缩略图产物、temperature 0.1），单表 `image_ai_descriptions` 承载结果+状态，无 run 历史表；复用 `ai_tagging_*` 配置，不新增外发端点。
- **语义**（D-010/D-011）：共享全局 `SemanticIndexProfile`，视频/图片索引任务全局互斥；索引文本=标题+标签+AI 描述，无描述跳过；检索仅照片页内，offset/limit。
- **兼容承诺**（D-016）：纯增量。现有代码唯一允许的行为改动是 `tag_service` 删除/合并补 `image_tags` 分支（D-002）。

非目标（不得顺手实现）：EXIF、图片目录 watcher、网格虚拟化、人物/作品集覆盖图片、搜索混排、非 darwin 解码、图片进 AI 质量评估/同源检测。

全局约束：新增 UI 使用既有令牌体系与 `BaseModal`/`btn-*` 原语，不改现有页面结构；Go 侧遵循仓库既有模式（context 首参、CAS 状态机、临时文件+rename、`log.Printf` API 审计）；每个切片完成时 `go test ./...` 基线不降。

## P-001 数据层与迁移

交付全部持久化契约：8 个新模型（`Image`、`ImageDirectory`、`ImageTrashEntry`、`ImageAIDescription`、`ImageSemanticIndex`、`ImageSemanticIndexAttempt`、`ImageNearDuplicateDismissal`，及 `image_tags` many2many 定义）注册进 `models.AllModels()`；`Settings` 新增 `ImageExtensions`（新库默认清单写入 `database.Init`，老库零值由使用方回退）；`ensureImagePathUniqueIndex`（返回 error）与 `ensureImageQueryIndexes`（best-effort）按设计 5.1 的索引清单落地；`PrepareImageSemanticVectorStorage` / `EnsureImageSemanticVectorANNIndex` 幂等建 `image_semantic_vectors`（PK/CHECK/HNSW 镜像视频侧）。

完成标志：新库初始化与老库升级（对既有 schema 重复执行 AutoMigrate）均成功且可重复执行；`images.path` 部分唯一索引生效（软删除不占路径）；现有全部测试通过。

> writes: `models/*.go`, `database/database.go`, `database/semantic_vector.go`（或新增 `database/image_semantic_vector.go`）, 相应 `*_test.go`
> anchors: D-001, D-004, D-016, AC-8（结构不变面）
> verify: `go test ./...`；新增迁移/索引幂等测试（重复执行 Init 语义不报错）
> review: schema 与设计 5.1 逐列一致性；`videos` 及现有表零改动（diff 审查）

## P-002 图片扫描与目录管理

交付 `ImageService` 的扫描对账闭环与目录管理 API：`image_directories` 四件套（`GetAllImageDirectories/AddImageDirectory/UpdateImageDirectory/DeleteImageDirectory`）、`SyncImageDirectories()` 对账扫描（扩展名过滤含空值回退默认清单、复用 `scan_exclude_paths`、`trash` 目录过滤、name+size 双向唯一迁移检测、失踪仅删记录）、`addImage` 路径预查与最小字段创建、`imagePathMutationMu` 路径锁。启动自动扫描挂现有 `auto_scan_on_startup` 前端触发链（本切片只交付后端 API，前端触发在 P-006 接线）。

完成标志：TC-1 场景有自动化覆盖——批量入库、幂等重扫零重复、迁移保留元数据、视频扫描不受影响。

> writes: `services/image_service.go`, `services/image_service_test.go`, `app.go`, `main_bindings.go`
> anchors: AC-1, D-003, D-005, D-017（目录与扫描 API 面）, TC-1
> verify: `go test ./services -run 'TestImage.*(Scan|Directory|Relocate)' -count=1` 通过后 `go test ./...`
> review: 对账语义（失踪仅删记录、迁移双向唯一不猜测）与视频扫描零回归

## P-003 缩略图、查看管线与预览路由

交付 `ImageThumbnailService`：解码矩阵（ffmpeg 常规 / darwin `sips` HEIC+RAW / 其他平台哨兵错误 stub，`//go:build` 隔离）、尺寸探测回填 `width/height`、独立缓存目录与 `<id>.jpg`/`<id>.view.jpg` 命名、`validThumbnailCache` 同款失效判定、per-ID 锁 + 原子 rename、256 MiB LRU；`newAssetHandler` 注册 `/preview/image/<id>` 与 `/preview/image-thumbnail/<id>`（GET/HEAD、404/405 语义、缩略图 `max-age=300`）；缩略图生成后计算 dHash（`image.Decode` → `differenceHash` 全图）写 `images.perceptual_hash` + source 指纹。

完成标志：darwin 上 heic/nef 夹具可出缩略图与转码大图（TC-2 自动化部分）；非 darwin 构建下同请求走 stub 返回 404；路由 handler 测试覆盖错误码。

> writes: `services/image_thumbnail_service.go`, `services/image_decode_darwin.go`, `services/image_decode_other.go`, `services/image_thumbnail_service_test.go`, `preview_asset_handler.go`, `preview_asset_handler_test.go`（如无则新增）
> anchors: AC-2, AC-3, D-006, D-007, TC-2（自动化部分）
> verify: `go test ./services -run 'TestImage.*(Thumbnail|Decode|Hash)' -count=1`；darwin 本机跑含 sips 的测试；`go test ./...`
> review: build tag 平台隔离正确性（`go vet`/双平台构建 `GOOS=linux go build ./...`）；缓存目录与视频 `thumbnails/` 零交叉

## P-004 查询、标签与用户态

交付照片页数据面：`SearchImagePage(ImagePageRequest{Filter,Cursor,Limit})` DTO 游标分页（筛选=关键词/标签/仅收藏/评分区间/体积区间；排序=最近添加(默认)/体积/评分，NULL 评分排后；`ORDER BY` 含 `id` 决胜列）、`GetImageDetail`、`SetImageFavorite`/`SetImageRating`（0–10 半分制校验、可清空）、打/去标四件套与批量（返回 `BatchImageOperationResult`）。**改动现有代码**：`tag_service` 的 `DeleteTag` 补 `image_tags` 清理、`MergeTags` 补 `image_tags` 改写去重；视频侧行为与统计口径不变。

完成标志：TC-3/TC-8 自动化覆盖，含"删除/合并标签时视频与图片双表正确"用例与视频侧标签回归。

> writes: `services/image_service.go`（查询与用户态方法）, `services/image_query_test.go`, `services/tag_service.go`, `services/tag_service_test.go`, `app.go`, `main_bindings.go`
> anchors: AC-4, AC-10, D-002, D-013, D-017（查询 API 面）, TC-3, TC-8
> verify: `go test ./services -run 'TestImage.*(Page|Tag|Favorite|Rating)|TestTag' -count=1`；`go test ./...`
> review: 高危——触碰现有 `tag_service`；独立审查删除/合并的双表语义与视频侧零回归

## P-005 图片回收站

交付 `image_trash_entries` 四态状态机（`pending_move/deleted/restoring/rollback`）与全链路：`DeleteImage`/`BatchDeleteImages`（`deleteFile=false` 仅软删）、`ListImageTrashEntries`、`RestoreImageTrashEntry`（写锁、目标存在拒绝、指纹校验、条目物理删除）、启动对账（镜像 `cancelInterruptedDeletion`/`reconcilePendingDelete`）。文件操作复用 `TrashService`。

完成标志：TC-5 自动化覆盖——删除入回收站、恢复成功、原路径冲突拒绝不覆盖、重复删除被唯一条目拒绝、崩溃状态对账。

> writes: `services/image_service.go`（回收站方法，或独立 `services/image_trash.go`）, `services/image_trash_test.go`, `app.go`, `main_bindings.go`
> anchors: AC-7, D-008, TC-5
> verify: `go test ./services -run 'TestImageTrash' -count=1`；`go test ./...`
> review: 高危——状态机与"绝不覆盖"语义；对照视频侧既有 trash 测试逐场景核对

## P-006 照片页前端与设置页扩展

交付用户可见的浏览闭环：`App.vue` 导航"图片"入口与 `v-if` 挂载；`PhotoLibraryPage.vue`（CSS Grid `auto-fill minmax(180px,1fr)`、IntersectionObserver 哨兵 + 手动加载兜底、每页 60、token 竞态防护、缩略图 `@error` 占位、筛选条、排序）；lightbox 查看器（方向键/Esc/F、侧栏元数据+标签+评分编辑、非 darwin HEIC/RAW 占位）；回收站与删除入口（复刻 TrashRestoreDialog 模式）；设置页"图片扫描目录"块与 `image_extensions` textarea（含 `saveSettings` 全量提交对象与 App.vue settings 默认值同步、启动自动扫描接线 `SyncImageDirectories`）。

完成标志：TC-1/TC-2 的人工验收路径可走通（添加目录→网格→查看器）；前端测试套件通过并为照片页核心逻辑（分页追加、筛选重置、占位降级）新增 vitest 用例。

> writes: `frontend/src/App.vue`, `frontend/src/components/PhotoLibraryPage.vue`（及配套子组件/测试）, `frontend/src/components/SettingsPage.vue`, `frontend/src/styles/components.css`
> anchors: AC-1/AC-2/AC-3/AC-4/AC-7/AC-10（前端面）, D-004（前端回退）, D-015, D-017（前端消费契约）
> verify: `cd frontend && npm test`；darwin 本机 `wails dev` 人工验收网格与查看器
> review: 现有五页面导航与 `.main-view` 滚动宿主零回归

## P-007 AI 描述服务

交付 `ImageAIDescriptionService`：单表状态机（pending/processing/completed/failed + `attempt_count`/`error_code`）、批量三件套 `StartImageAIDescription/GetImageAIDescriptionStatus/CancelImageAIDescription`（单 worker、进度事件、启动时 processing 复位 interrupted）、单张 `RegenerateImageAIDescription` 覆盖式重跑；请求复用 `SettingsAITaggingConfigProvider` + chat 多模态单图（缩略图产物 base64、提示词契约=80–200 字中文禁标签、temperature 0.1、5min 超时、无客户端重试）；落库前脱敏；缩略图不可得记 `decode_unsupported` 跳过。

完成标志：mock client 单测覆盖状态机全分支（成功/失败/取消/中断复位/未配置拒绝/解码不支持跳过/超长截断）。

> writes: `services/image_ai_description_service.go`, `services/image_ai_description_service_test.go`, `app.go`, `main_bindings.go`
> anchors: AC-5, D-009, TC-4（描述部分）
> verify: `go test ./services -run 'TestImageAIDescription' -count=1`；`go test ./...`
> review: 数据外发边界（仅降采样 JPEG+提示词）与脱敏落库；不触碰视频 AI 打标链路

## P-008 图片语义索引与检索

交付 `ImageSemanticIndexService`：索引三件套（共享 `SemanticIndexProfile`、profile 缺失时同款 CAS 创建、**与视频索引任务全局互斥**、索引文本=标题+标签+描述、无描述 Skipped、指纹续跑、失败码集合镜像视频侧、双写 `image_semantic_indexes`+`image_semantic_vectors`）；`SearchImagesSemantic`（`<=>` 余弦距离、`ORDER BY distance,id OFFSET/LIMIT+1`、filter 组合、查询向量缓存、score=1-distance）。视频页任何搜索路径不查图片表。

完成标志：TC-4 检索部分自动化（mock embed 单测 + 跟随现有语义集成测试形态的 pgvector 用例）；互斥用例（视频任务运行中图片 Start 被拒，反之亦然）；"视频语义搜索结果不含图片"断言。

> writes: `services/image_semantic_index_service.go`, `services/image_semantic_search.go`（或并入前者）, 相应 `*_test.go`, `app.go`, `main_bindings.go`
> anchors: AC-6, D-010, D-011, TC-4（检索部分）
> verify: `go test ./services -run 'TestImageSemantic' -count=1`；`go test ./...`
> review: 高危——共享 profile 的互斥与 generation 语义；视频语义索引/搜索零回归（现有语义测试全绿）

## P-009 描述与语义检索前端

交付照片页的 AI 面：查看器侧栏描述展示与"重新生成"；照片页搜索框"文件名/语义"模式切换（语义模式 offset 追加分页、能力不可用显示原因并禁用）；设置页或照片页内的描述批量任务与图片语义索引任务面板（Start/进度/取消，镜像现有语义索引面板交互）。

完成标志：TC-4 人工路径可走通（生成描述→照片页语义搜索命中→视频页同词不返回图片）；前端测试覆盖模式切换与不可用降级。

> writes: `frontend/src/components/PhotoLibraryPage.vue`（及子组件/测试）, `frontend/src/components/SettingsPage.vue`（任务面板如放设置页）
> anchors: AC-5/AC-6（前端面）, TC-4（人工部分）
> verify: `cd frontend && npm test`；darwin 本机人工验收 TC-4 全路径
> review: 无（消费已审查的后端契约）

## P-010 图片清理审阅

交付 `StartImageCleanupAnalysis/GetImageCleanupStatus`（异步+进度事件）：精确重复=size 分桶+`getPartialHash` 实时配对；近似重复=库内 dHash（stale 指纹跳过计数）+16 位前缀分桶+邻居上限+汉明≤8 连通分量；排除精确对与 `image_near_duplicate_dismissals`；`DismissImageNearDuplicateGroup`；审阅 UI（按目录分组、精确重复默认勾选多余副本而近似重复不勾〔2026-08-08 修订〕、删除走 `BatchDeleteImages(ids,true)`、忽略、StaleHashCount 提示）；删除/恢复后分析失效。

完成标志：TC-7 自动化覆盖（同字节夹具成精确组、缩放差异夹具成近似组、忽略后不再出现、删除可恢复）。

> writes: `services/image_cleanup.go`, `services/image_cleanup_test.go`, `app.go`, `main_bindings.go`, `frontend/src/components/PhotoLibraryPage.vue`（或独立审阅子组件）
> anchors: AC-9, D-012, TC-7
> verify: `go test ./services -run 'TestImageCleanup' -count=1`；`go test ./...`；`cd frontend && npm test`
> review: 视频清理审阅五类候选零回归；默认勾选规则契约（见设计文档 D-012 的 2026-08-08 修订）

## P-011 洞察图片维度

交付 `GetImageInsights()`（`summary{image_count,total_size,favorite_count}` + `storage_by_directory` + `storage_by_format`，只查活跃行）与 InsightsPage"图片"分区（摘要卡 + 两个 `BucketChart` 复用，图片区加载失败不影响视频区）。`GetLibraryInsights()` 一字不改。

完成标志：TC-9 自动化——聚合数值正确 + `GetLibraryInsights` 返回结构与数值不变断言。

> writes: `services/image_stats_service.go`, `services/image_stats_service_test.go`, `app.go`, `main_bindings.go`, `frontend/src/components/InsightsPage.vue`（及测试）
> anchors: AC-11, D-014, TC-9
> verify: `go test ./services -run 'TestImageStats' -count=1`；`cd frontend && npm test`
> review: 无（纯增量只读聚合）

## Integration And Final Verification

- 全量基线：`go test ./...` 与 `cd frontend && npm test` 全绿（TC-6 / AC-8 / D-016）；`gofmt` 干净；`GOOS=linux go build ./...` 与 `GOOS=windows go build ./...` 验证平台隔离编译。
- 不变面 diff 审查：`models/video.go` 仅 `Settings` 加列；现有 Wails API 签名零变更（`frontend/wailsjs/go/main/App.d.ts` diff 只有新增行）；`/preview/` 现有五条路由行为不变。
- 混排终验：视频页文件/字幕/语义搜索均不出现图片；照片页不出现视频（AC-6 集成级断言）。
- darwin 人工终验一轮：TC-1→TC-2→TC-3→TC-4→TC-5→TC-7→TC-8→TC-9 按 requirements.md 场景走通。
- deferred-with-rationale：TC-2 的非 darwin 真机人工验证（无 Windows/Linux 环境时以 stub 单测 + 交叉编译代替）。

## P-012 EXIF 元数据（含 GPS）

用户于 2026-08-07 首版交付后裁决把原非目标「EXIF 拍摄时间」纳入范围，粒度为**全量含 GPS 位置**。本切片交付 EXIF 提取、存储、展示与筛选排序。

`images` 表新增 EXIF 列：`taken_at *time.Time`（index）、`camera_make`、`camera_model`、`lens_model`、`iso int`、`f_number float64`、`exposure_time string`（原始分数文本如 `1/250`）、`focal_length float64`、`exif_orientation int`、`gps_latitude/gps_longitude *float64`、`exif_parsed_at *time.Time`（区分"未解析"与"已解析但无 EXIF"）。解析用 `github.com/rwcarlsen/goexif`（零依赖纯 Go，已固定进 go.mod 并预置 go.sum）：JPEG 与 TIFF 系 RAW（DNG/CR2/NEF/ARW/RW2/ORF）直接 `exif.Decode`；HEIC/HEIF/CR3 因是 ISO-BMFF 容器，扫描文件头部定位 `Exif\x00\x00` 魔数后把其后的 TIFF blob 交给同一解析器。解析失败或无 EXIF 一律不是错误——写 `exif_parsed_at` 后留空值，不阻塞任何流程。

回填沿用 dHash 的双轨模式：扫描入库时解析一次；另提供显式后台任务补全历史图片（Start/Status/Cancel 三件套，镜像既有任务形态）。

排序与筛选：排序下拉新增「拍摄时间」（**默认仍为「最近添加」，D-013 的既有默认不变**），无 `taken_at` 的图片回退到文件 mtime 参与同一排序；筛选条新增拍摄日期区间。查看器侧栏新增「拍摄信息」区展示相机/镜头/参数/拍摄时间，有 GPS 时显示经纬度。

**安全要求（本切片必须落实）**：AI 描述发往外部端点的 JPEG 必须剥除元数据——`sips` 转码会保留 EXIF，GPS 会随图外发。需验证并确保送出的字节不含 EXIF/GPS。同时 GPS 与相机参数**不得进入语义索引文本**（索引文本维持标题+标签+AI 描述三段不变）。

完成标志：TC-10/TC-11 自动化覆盖；`GetLibraryInsights` 与视频侧行为不变。

> writes: `models/image.go`, `database/database.go`（新列索引）, `services/image_exif.go`, `services/image_exif_test.go`, `services/image_service.go`（扫描时解析接入）, `services/image_library_service.go`（排序/筛选）, `services/image_ai_description_service.go`（元数据剥除）, `app.go`, `frontend/src/components/PhotoLibraryPage.vue`, `frontend/src/components/PhotoLibraryPage.test.js`, `frontend/wailsjs/go/main/App.*`
> anchors: 用户 2026-08-07 裁决（EXIF 全量含 GPS；默认排序不变）；新增 AC-12（EXIF 提取与展示）、AC-13（拍摄时间排序与日期筛选）、AC-14（外发 JPEG 不含 EXIF/GPS）
> verify: `go test ./services -run 'TestImageExif' -count=1`；`go test ./...`；`cd frontend && npm test`；外发剥除以字节断言验证（解析送出的 JPEG 应无 EXIF 段）
> review: 高危——GPS 是敏感信息且触碰既有 AI 外发路径；独立审查外发剥除的有效性与语义索引文本未被污染

## P-013 照片网格虚拟化与时间线分组

用户裁决把原非目标「网格虚拟化」纳入范围，并**一并做时间线分组**。两者必须协同设计：分组头要在虚拟化窗口内正确定位。

现有 `frontend/src/utils/virtualList.js` 的 `calculateVirtualWindow` 是一维行高累加，不支持多列，也不支持分组头——本切片新增二维分组窗口计算（建议 `frontend/src/utils/photoGrid.js`，**不修改现有 virtualList.js**，避免回归视频列表）。模型：按当前列数把条目切成行，分组头占独立整行；前缀和支持 O(log n) 定位；列数随容器宽度变化时重算。

时间线分组作为独立浏览模式（开关或视图切换），开启时强制按 `taken_at` 排序并插入年/月分组头；关闭时是普通虚拟化网格，沿用排序下拉。分组模式需要后端配合返回分组计数（避免前端为算分组头拉全量），新增轻量 API 返回按年月的计数摘要。

完成标志：万级图片滚动流畅（DOM 节点数恒定）；分组头吸顶或正确穿插；调整窗口宽度后布局与滚动位置不错乱；视频列表虚拟化行为零回归。

> writes: `frontend/src/utils/photoGrid.js`, `frontend/src/utils/photoGrid.test.js`, `frontend/src/components/PhotoLibraryPage.vue`, `frontend/src/components/PhotoLibraryPage.test.js`, `services/image_library_service.go`（分组计数 API）, `services/image_library_service_test.go`, `app.go`, `frontend/wailsjs/go/main/App.*`
> anchors: 用户 2026-08-07 裁决（网格虚拟化 + 时间线分组）；新增 AC-15（虚拟化网格 DOM 恒定）、AC-16（时间线年月分组浏览）
> verify: `cd frontend && npm test`（含 photoGrid 纯函数窗口计算用例：多列切行、分组头占位、列数变化重算、边界空组）；`go test ./services -run 'TestImageTimeline' -count=1`；万级数据的人工滚动验收
> review: 现有 `virtualList.js` 与视频列表虚拟化零改动（diff 审查）

## 集成终验结果（2026-08-07，全部为自动化新鲜输出）

- `go test ./...` 全绿：root 5.1s / database 1.2s / services 22.3s / cmd / subtitleparser。
- `cd frontend && npm test` 全绿：13 files / **108 tests**（起始基线 76）。
- `npx vite build` 通过；`go vet ./...` 通过；`gofmt` 本次新增/修改文件全部干净。
- `GOOS=linux go build ./...` 通过（验证 sips 平台隔离）。`GOOS=windows` 失败于 `services/enhancement_service.go`（超分服务的 Unix-only syscall），**stash 全部本次改动后同样失败，确认为基线既有问题，与本计划无关**。
- 不变面：`frontend/wailsjs/go/main/App.d.ts` +58/-0、`App.js` +116/-0（纯增量）；`app.go` 零删除行（既有 API 签名未动）；`models/video.go` 仅新增 `Settings.ImageExtensions` 一列。
- 混排断言：视频侧服务（library/semantic_search/semantic_index/library_stats/cleanup）零引用 `images`/`image_tags`/`image_semantic_vectors`；图片侧 `services/image_*.go` 零引用 `videos`/`video_tags`/`video_semantic_vectors`。
- 执行期发现并修复的契约偏差：`ImageAIDescription` 缺 `TableName()` 会使 GORM 生成 `image_a_idescriptions`，已按设计固定为 `image_ai_descriptions`。
- 待人工验收（需 macOS + 真实 AI/pgvector 环境）：TC-2 的 HEIC/RAW 查看器观感、TC-4 全链路、TC-5 文件级恢复、TC-7 真实图库重复分组质量。各切片报告已给出逐步操作清单。

## Handoff And Residual Risks

- Blockers: none。
- Residual risks: sips 对小众 RAW 变体（如老型号专有格式）的覆盖未逐一验证，失败降级为占位属预期行为；万张级近似重复分析的实际耗时未实测，若过慢按 D-012 的分桶常量调优（不改契约）；共享 profile 下视频重建导致图片索引过期依赖用户重跑任务，状态面板需把提示做醒目。
- Resume note: 各切片验证命令即恢复点；P-004 触碰 `services/tag_service.go` 是全计划唯一的现有行为改动面，中断后从该文件 diff 判断进度。

## Execution rules for the consuming agent

- Execute slices in frontmatter dependency order; verify each slice with its
  `verify` line before starting dependents, and update its frontmatter
  `status` as work proceeds.
- Two slices may run in parallel only when neither depends on the other and
  their `writes` paths are disjoint; integrate results sequentially.
- Keep the frontmatter and body consistent: every frontmatter slice id has
  exactly one body section, every slice declares `depends` explicitly (an
  empty list asserts independence), and dependencies appear only in the
  frontmatter.
- Follow the installed working agreement for verification, review, stop, and
  Git discipline throughout.
