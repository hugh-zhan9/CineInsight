# Jellyfin 兼容层可行性探讨（探索笔记，未立项）

> **状态：探索，未决策，未立项。** 本文不是设计文档也不是裁决记录，不构成实现依据。
> 若将来决定推进，需重新走 clarify → spec → plan2exec，届时以新产出的文档为准。
>
> 讨论日期：2026-08-08 · 参与：用户与 Claude · 触发问题：能否让手机上的 Jellyfin 客户端连上本应用看视频

## 一、结论摘要

技术上可行，且在**本应用的具体条件下比通常情况便宜得多**。真正值得做的理由不是"多一种播放方式"，而是把应用内的策展结构（标签、智能视图、作品集、评分）搬到手机客户端上——这是现成 Jellyfin 给不了的。

用户已明确的两个前提：

1. 想在手机上看到的是**应用里整理出来的那套结构**，不是普通片库。
2. 客户端是 **Fileball 或其他支持 Jellyfin 的 iOS 软件**。

## 二、关键认知：Jellyfin 没有"协议"

Jellyfin 提供的是一套 HTTP REST API（自 Emby 继承），不是私有 wire protocol。所谓"支持 Jellyfin"，实质是**把本应用伪装成一台 Jellyfin 服务器**，让客户端认得。因此工作量取决于"要让哪些客户端的哪些功能跑通"，而非实现某个完整规范。

## 三、为什么这个组合特别有利：可以完全跳过转码

Jellyfin 服务端成本的大头是转码。客户端通过 `POST /Items/{id}/PlaybackInfo` 提交 DeviceProfile，服务端回答"能否直连播放"，不能则需提供 HLS 切片地址——那是一个独立的大子系统。

**Fileball 是直连播放型客户端**，自带较强解码能力（HEVC、MKV 容器、多数音轨）。因此可以只声明 direct play、不实现转码，工作量从"实现一台媒体服务器"降为"实现一套 JSON 接口 + 带 Range 的文件流"。

代价：Fileball 也啃不动的编码就是播不了。以其能力这种情况少见，但不为零。

## 四、核心价值：策展即媒体库

本应用已有的查询层可以直接映射成 Jellyfin 的"媒体库"（Views）。这是自研的唯一理由——现成 Jellyfin 不认识这些概念。

现有智能视图（`services/library_service.go:27-36`）：

| 常量 | 语义 |
|---|---|
| `LibraryViewFavorites` | 收藏 |
| `LibraryViewContinueWatching` | 继续观看 |
| `LibraryViewUnwatched` / `LibraryViewWatched` | 未看 / 已看 |
| `LibraryViewRecentlyAdded` / `LibraryViewRecentlyPlayed` | 最近添加 / 最近播放 |
| `LibraryViewUntagged` / `LibraryViewNoSubtitle` / `LibraryViewStale` | 未打标签 / 无字幕 / 路径失效 |

设想的映射：

```
GET /Users/{id}/Views  →  [收藏] [未看] [继续观看] [最近添加]
                          [用户保存的每个命名视图（SavedLibraryView）...]
                          [作品集]
```

实体映射：

| 本应用 | Jellyfin |
|---|---|
| 视频 | Movie item |
| 作品集（MediaCollection） | BoxSet |
| 人物（Person） | People |
| 标签（Tag） | Tags / Genres |
| `WatchPositionSeconds` / `IsWatched` | UserData.PlaybackPositionTicks / Played |
| `Duration`（秒） | RunTimeTicks（**100 纳秒 ticks，= 秒 × 10⁷**） |

`POST /Sessions/Playing/Progress` 回写 `WatchPositionSeconds`，即可实现手机与桌面的观看进度闭环——这是"装一台真 Jellyfin 在旁边"做不到的。

## 五、可复用的既有地基

| 需要什么 | 现状 |
|---|---|
| 常驻 HTTP 服务 | `services/short_feed_server.go` 的 `ShortFeedHTTPServer`，含启动/停止/状态三件套 |
| 网络访问控制 | 同文件 `shortFeedRemoteAllowed`（:163-170）：绑 `0.0.0.0` 但只接受回环与内网地址 |
| 带 Range 的视频流 | `http.ServeContent`（同文件 :347），seek 必需的断点续传已具备 |
| 编解码信息 | `models/media_details.go` 的 `MediaStream`：codec_name、bit_rate、language、channels、width/height（:114-134），可直接拼 `MediaSources` |
| 封面图 | 缩略图管线 + `preview_asset_handler.go` 的 `/preview/` 路由 |
| 筛选与分页 | `services/library_service.go` 的 `LibraryFilter` 与游标分页 |

## 六、需要实现的接口面（最小集，待抓包校正）

```
GET  /System/Info/Public          客户端验证服务器地址，不认这个连不上
POST /Users/AuthenticateByName    返回 AccessToken
GET  /Users/{id}/Views            顶层媒体库（= 策展映射的落点）
GET  /Items?ParentId=&SortBy=&…   浏览主力，查询参数繁多
GET  /Users/{id}/Items/{itemId}   详情
GET  /Items/{id}/Images/Primary   封面
POST /Items/{id}/PlaybackInfo     播放能力协商（只声明 direct play）
GET  /Videos/{id}/stream          直连文件流
POST /Sessions/Playing[/Progress|/Stopped]   进度回传
```

`/socket`（WebSocket）部分客户端会连，多数容忍缺失，待抓包确认。

## 七、推荐的第一步：先抓包，别猜

**在写任何代码之前**，用 Docker 跑一个真 Jellyfin，塞两个视频，让 Fileball 连上去完整浏览并播放一遍，然后读 Jellyfin 的访问日志。

产出是一份精确的接口清单与参数组合，包括 Fileball 到底发不发 `PlaybackInfo`、`/Items` 带哪些查询参数、要不要 WebSocket。这把整个项目最大的不确定性——第三方闭源客户端的实际行为——在动工前就消掉。成本约半小时，可省掉后续多轮试错。

## 八、风险与代价（如实记录）

1. **应用必须运行**。这是 Wails 桌面应用而非守护进程，主机休眠则手机无法播放。短视频服务已是同样性质。
2. **Fileball 接口预期无公开文档**，靠抓包与试错收敛。"80% 很快跑通、最后 20% 反复磨"是这类工作的常态。
3. **不做转码**，编码兼容性完全依赖客户端能力。
4. **安全边界**：整个片库暴露于内网。沿用短视频服务的内网白名单是底线；是否需要更强认证（如按设备的令牌、过期策略）需单独裁决。
5. **Jellyfin API 是移动目标**，客户端可能按 `/System/Info` 返回的版本号做特性判断，长期存在维护成本。

## 九、若推进，待裁决的问题

- 暴露哪些视图？全部智能视图 + 全部保存视图，还是用户逐个勾选？
- 认证做到什么程度？固定单用户 + 内网白名单，还是按设备发令牌？
- 是否支持多设备并发播放与会话列表？
- 图片库（P-001..P-013 交付的照片功能）是否也暴露？Jellyfin 有 Photo item 类型。
- 服务开关放在设置页何处，默认开还是关？

## 十、备选方案（若不自研）

| 方案 | 成本 | 得到什么 | 失去什么 |
|---|---|---|---|
| 装一台真 Jellyfin 指向同一批目录 | 零开发 | 全客户端兼容 + 转码 | 策展结构、单一观看状态 |
| WebDAV（`golang.org/x/net/webdav`） | 约两百行 | Fileball/Infuse/nPlayer 可浏览播放 | 元数据、策展、进度同步 |
| 只伺候一个客户端的 Jellyfin 子集 | 本文方案 | 策展 + 进度闭环 | 转码、广泛客户端兼容 |
