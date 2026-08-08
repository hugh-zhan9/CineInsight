---
source: 用户请求（2026-08-08 会话）：重构短视频局域网访问模块并接入图片。已裁决：混编流；图片支持喜欢/收藏回写、删除进回收站、看原图缩放、展示 AI 描述与标签；交互表用并行新表 short_feed_image_interactions；图片不自动翻页；重构范围含后端媒体类型无关层与前端巨组件拆分。
status: ready
slices:
  - id: P-001
    status: pending
    depends: []
  - id: P-002
    status: pending
    depends: [P-001]
  - id: P-003
    status: pending
    depends: [P-001]
  - id: P-004
    status: pending
    depends: [P-002, P-003]
  - id: P-005
    status: pending
    depends: []
  - id: P-006
    status: pending
    depends: [P-004, P-005]
  - id: P-007
    status: pending
    depends: [P-006]
---

# 短视频局域网访问接入图片

## Goal And Boundaries

手机在同一个局域网页面里刷到的是一条**混编流**：向上划，下一条可能是视频，也可能是图片。图片可以喜欢、收藏、删除（进回收站）、放大看原图，并能看到已生成的 AI 描述与标签。喜欢与收藏要回写到图片库本体，和视频侧现有的投影语义一致。

今天这个模块把"媒体 = 视频"写死在六处资格校验、一处 `video_tags` 原生 SQL 投影、一条 `/short-media/{id}` 裸 ID 路由和 `?exclude=1,2,3` 的纯数字列表里（现状勘察见本文件末尾"现状锚点"）。所以本计划先把**媒体标识**这一维度打通，再接图片，而不是在视频分支旁边并排复制一份图片分支。

### 已裁决（不要在执行期重新决定）

- **交互表用并行新表** `short_feed_image_interactions`，不动 `short_feed_interactions` 的现有行、唯一索引与外键。零迁移风险是这条裁决的目的；服务层用一个接口收敛两套读写。
- **图片不自动翻页**。图片停在那里直到用户划走，没有停留计时器、没有进度条。
- **混编是随机的**，不是"先视频后图片"或按比例配额。沿用现有的 tag 偏好加权（上限 1.5×），图片复用同一套 `short_feed_tag_preferences`，因为 tags 表本身是图片与视频共享的。
- 局域网访问仍然**不加登录/PIN/二维码**，维持"仅环回 + 私网 + 链路本地"的既有拦截。本计划不改这条。

### 非目标

- 不做转码。图片和视频都按原文件下发，靠 `http.ServeContent` 处理 Range。
- 不做手机端的标签编辑、评分、目录浏览。
- 不引入分页游标。feed 仍是"一次一条 + 客户端最近列表排除"的无状态抽取。
- 不改桌面端图片库与清理审阅的任何行为。

### 兼容性边界

`/short-media/{id}` 与 `?exclude=` 的线上格式会变成带媒体类型的形式。手机页是同一个 Go 二进制里 embed 的 `frontend/dist/short.html`，与后端同版本发布，不存在独立升级的外部消费者，因此按不兼容变更一次性改掉，不做双读兼容。桌面端唯一绑定方法 `GetShortFeedServerStatus` 的返回结构不变。

### 保护行为

- 视频侧现有语义零回归：时长门槛、`inline_not_supported` 提示、播放计数与 `last_played_at`、喜欢投影独占"短视频喜欢"自动标签、收藏一次性投影 `favorite_synced_to_library`、删除走视频回收站。
- 服务端仍拒绝公网源 IP，仍对变更请求做同源校验与 1 MiB/JSON/未知字段的请求体纪律。

## P-001 媒体标识贯通：类型化的 item、媒体路由与排除集

把"这一条是什么媒体"提升为一等概念，贯穿 DTO、媒体路由与排除集，而视频的可观察行为保持不变。

服务层引入统一的媒体引用（类型 + ID）与统一的 feed item 结构，item 上带 `media_kind`。`/short-media/{id}` 改为带类型的形式，`?exclude=` 改为携带类型键的列表，客户端的 `recentIDs` 相应改为类型化的键。选取、资格判定、媒体解析这三处的入口签名改为接受媒体引用，内部暂时只有视频实现——本切片不引入任何图片行为。

完成的标志：手机端刷视频、喜欢、收藏、删除、Range 拖动全部与改动前一致；`short_feed_service_test.go` 现有 8 个用例在按新签名调整调用后仍然通过；新增用例覆盖类型化排除集的解析与非法类型的拒绝。

> writes: `services/short_feed_types.go`, `services/short_feed_service.go`, `services/short_feed_server.go`, `services/short_feed_service_test.go`, `frontend/src/short-feed/api.js`, `frontend/src/short-feed/ShortFeedApp.vue`
> anchors: 后端抽成不绑定媒体类型的层；`/short-media/{id}` 与 `?exclude=` 的裸 ID 命名空间冲突
> verify: `go test ./services/ -run ShortFeed -count=1`；`cd frontend && npm run test:short-feed`
> review: 视频侧六处资格校验与四处投影是否在签名迁移中被漏改或改变语义；新路由是否仍拒绝越权媒体 ID

## P-002 图片资格与媒体下发

让图片成为可下发的媒体：入选规则、原图与缩略图的下发、以及浏览器可内联显示的格式判定。

图片没有时长，所以现有那条"时长 > 0 且 < 上限"的通用门槛必须按媒体类型分叉：视频保持原判据，图片改为"非 stale + 文件存在 + 格式可内联显示"。可内联的图片格式判定不要就地扩宽 `inlinePreviewMIMEs`——那张表与桌面预览共用，就地加条目会外溢到预览链路；图片走自己的判定，必要时复用 `preview_asset_handler.go` 里已有的图片解析逻辑（RAW/HEIC 在 macOS 走 sips 转码后才可显示，不可显示的图片直接不入选，而不是入选后再报 `inline_not_supported`）。

原图与缩略图都要能被手机取到：原图用于放大查看，缩略图用于列表与预取。两者都必须带 `Cache-Control` 并支持条件请求，否则手机端反复拉原图会很痛。

完成的标志：给一张可显示的图片建记录后，媒体路由能返回它的字节并对 `Range` 返回 206；不可显示的图片不出现在 feed 里；缺失文件的图片被标记 stale 并跳过，与视频侧行为一致。

> writes: `services/short_feed_service.go`, `services/short_feed_types.go`, `services/short_feed_server.go`, `services/short_feed_service_test.go`
> anchors: 图片入选规则（无 duration）；可内联格式判定不污染桌面预览白名单；原图 + 缩略图下发
> verify: `go test ./services/ -run ShortFeed -count=1`（含新增的图片 Range 与不可显示格式用例）
> review: 是否有路径能让手机取到图片库之外的任意文件；sips 转码结果是否可能把 EXIF/GPS 带出局域网

## P-003 图片互动表与回写投影

图片的喜欢、收藏、删除，以及回写到图片库本体。

新建 `short_feed_image_interactions`，形状镜像视频表（喜欢/收藏/浏览计数/最近浏览/一次性收藏投影标记），外键指向 images。服务层用一个接口把两套读写收敛起来，调用方按媒体类型取到对应实现，而不是在每个方法里写 if/else。

回写投影按图片侧的等价物重做：喜欢投影到图片的自动标签（走 `image_tags`，与视频的 `video_tags` 投影对称，同样是"投影完全拥有这个自动标签"的语义，含反向清理），收藏回写 `images.is_favorite` 并用一次性标记避免重复覆盖用户在桌面端的手动改动。删除复用图片回收站链路（`imageService` 的删除语义），不是硬删。投影总开关沿用现有的 `ShortFeedFeedbackSyncEnabled`。

完成的标志：手机上喜欢一张图，桌面图片库里该图出现对应自动标签；取消喜欢，标签被回收；收藏一次后在桌面端手动取消，再次同步不会把它改回去；删除后图片进回收站且可恢复。投影关闭时不新增也不删除既有投影。

> writes: `models/short_feed.go`, `models/schema.go`, `database/database.go`, `services/short_feed_service.go`, `services/short_feed_service_test.go`
> anchors: 并行新表裁决；喜欢/收藏回写图片库；删除进回收站；投影开关沿用现有设置
> verify: `go test ./services/ -run ShortFeed -count=1`（含投影幂等、关闭开关不清理既有投影、收藏一次性标记三类用例）
> review: 自动标签的反向清理是否可能误删用户手动打的同名标签；一次性收藏标记在并发下是否仍不会覆盖桌面端的手动取消

## P-004 混编选取

把图片与视频合成一条流。

候选集合并两种媒体后统一加权抽取，权重仍是"1.0 + 该媒体标签的偏好分（上限 0.5）"。排除集按类型化的键工作，客户端传来的最近列表能同时排除两种媒体。

顺带修掉一个既有缺陷：当排除集覆盖了全部候选时，现在会静默回退到未过滤集合，于是小库上会不停重复，而不是给出干净的"没有更多了"。混编之后这个问题更容易被撞到（两种媒体各自数量都不大），所以在这一层一并处理：候选耗尽就如实返回耗尽。

完成的标志：库里同时有可播视频与可显示图片时，连续抽取能抽到两种媒体；排除集把两种媒体都排除干净后返回耗尽而不是重复；标签偏好对两种媒体都生效且不超过 1.5× 上限。

> writes: `services/short_feed_service.go`, `services/short_feed_service_test.go`
> anchors: 混编随机；tag 偏好复用；候选耗尽语义修复
> verify: `go test ./services/ -run ShortFeed -count=1`（含混编抽取、耗尽语义、权重上限用例）
> review: 耗尽语义变化是否会让手机端在正常小库上过早停流

## P-005 前端拆分 ShortFeedApp.vue

把 685 行单文件组件拆成职责清晰的若干块，行为零回归。

现在播放控制、手势、进度条拖动、收藏页、删除弹窗、唤醒锁全挤在一个组件里，再往里加图片分支只会更难读。按"媒体舞台 / 覆盖层 / 收藏页 / 弹窗"拆成子组件，把手势、播放状态、唤醒锁这些与 DOM 无关的逻辑提成组合式函数。顺带删掉已确认无人调用的倍速切换死代码。

这一切片不改任何可观察行为，因此它与后端各切片无依赖，可以并行推进。注意 `frontend/scripts/short-feed.test.mjs` 里有若干条针对 `.vue`/`.css` **源码文本**的正则断言，拆分会撞到它们；这些断言要跟着搬到新的文件位置，而不是删掉了事。

完成的标志：拆分后手机端的播放、划动、双击暂停、长按喜欢+收藏、进度拖动、收藏页、删除确认全部与拆分前一致；`short-feed.test.mjs` 全绿。

> writes: `frontend/src/short-feed/`, `frontend/scripts/short-feed.test.mjs`
> anchors: 前端拆分单文件巨组件；源码正则断言随之迁移
> verify: `cd frontend && npm run test:short-feed && npx vite build`
> review: 拆分是否悄悄改变了控件自动隐藏、双击阈值、长按阈值等交互常量

## P-006 图片在手机端的呈现与操作

图片分支的界面与交互。

媒体舞台按 `media_kind` 分支：视频保持现有的 `<video>` 与进度条；图片渲染 `<img>`，默认适屏，双击或双指可放大看原图细节，**不显示进度条、不自动翻页**。图片下方展示已生成的 AI 描述与标签，当作图说。喜欢、收藏、删除三个动作沿用同一套按钮与乐观更新（失败回滚），只是打到图片的接口上。

要注意现在的控件自动隐藏逻辑是以"正在播放"为条件的，图片没有播放态，直接沿用会让控件永远挂在画面上；图片需要自己的显隐节奏。文案里"视频"字样在图片条目下要换成中性说法。

完成的标志：刷到图片时画面适屏、没有进度条、不会自己翻页；双击能放大再还原；喜欢/收藏/删除生效并与桌面端一致；刷回视频条目时播放与进度条一切如常。

> writes: `frontend/src/short-feed/`, `frontend/scripts/short-feed.test.mjs`
> anchors: 图片渲染分支；不自动翻页；看原图缩放；AI 描述与标签；喜欢/收藏/删除；文案去"视频"化
> verify: `cd frontend && npm run test:short-feed && npx vite build`
> review: 图片分支是否会让视频的播放记录、唤醒锁、自动前进被误触发

## P-007 桌面侧文案、状态面板与文档

桌面端设置页现在把这个能力称作"手机短视频"、把上限称作"短视频时长上限"。接入图片之后这些说法不再准确：时长上限只对视频生效，需要在标签上说清楚；状态面板要说明手机端现在能看到图片与视频两类内容。

同时更新模块文档，把混编、并行表、类型化路由这三条写进去，避免下一次会话又从代码里反推设计。

完成的标志：设置页文案与实际行为一致；`SettingsPage.test.js` 通过；文档能让一个没参与本次改动的人看懂 feed 的选取与投影规则。

> writes: `frontend/src/components/SettingsPage.vue`, `frontend/src/components/SettingsPage.test.js`, `docs/`
> anchors: 文案与实际能力一致；模块设计留档
> verify: `cd frontend && npm test`

## Integration And Final Verification

- 全量回归：`go test ./... -count=1`、`cd frontend && npm test`、`npx vite build`、`gofmt -l services/` 干净。
- 端到端手工确认一次：桌面启动后从设置页拿到局域网地址，手机打开能刷到视频与图片两类内容，喜欢/收藏/删除在桌面端可见对应结果。
- 视频侧零回归的证据来自 `short_feed_service_test.go` 现有 8 个用例全绿（含公网 IP 403、表单编码 415、缺失请求体 400、Origin 不匹配 403、`Range: bytes=0-3` 返回 206、端口回退与关闭）。
- 局域网安全面未在任何切片中放宽：仍无登录/PIN，仍只放行环回、私网与链路本地源地址。

## Handoff And Residual Risks

- Blockers: 无。
- Residual risks: 手机端一次性拿到的图片是原图，大 RAW/HEIC 在弱 WiFi 下可能很慢，缩略图预取能缓解但放大看原图时仍会等待；图片不自动翻页与视频自动前进混在一条流里，节奏是否舒服需要实机体感确认，必要时再回来加"图片停留秒数"设置（本轮已明确不做）；`short-feed.test.mjs` 大量断言绑在源码文本上，P-005/P-006 的结构调整会反复撞到它们。
- Resume note: 执行前先确认工作区没有上一批未提交的改动混入；本计划的所有产品裁决已在 `Goal And Boundaries` 的"已裁决"小节固化，执行期不要重新讨论。

## 现状锚点（勘察结论，供执行期查证）

| 关注点 | 位置 |
| --- | --- |
| 服务器生命周期、路由表、源 IP 拦截、同源校验 | `services/short_feed_server.go` |
| 选取与加权、互动、投影、媒体解析 | `services/short_feed_service.go` |
| DTO 与常量 | `services/short_feed_types.go` |
| 互动与标签偏好模型 | `models/short_feed.go` |
| 设置字段 `ShortFeedMaxDurationMinutes` / `ShortFeedFeedbackSyncEnabled` | `models/video.go` |
| 手机端页面（第二个 Vite 入口，经 embed 由 Go 服务） | `frontend/short.html`, `frontend/src/short-feed/` |
| 可内联播放的 MIME 白名单（与桌面预览共用） | `services/preview_service.go` |
| 图片缩略图/原图的已有解析逻辑 | `preview_asset_handler.go` |
| 桌面状态面板与设置项 | `frontend/src/components/SettingsPage.vue` |
