---
schema: loopx-plan/v1
source: docs/loopx/design/2026-09-13-av-multi-source-aggregation/需求设计文档.md
status: superseded
slices:
  - id: P-001
    status: done
    depends: []
  - id: P-002
    status: done
    depends: [P-001]
  - id: P-003
    status: blocked
    depends: [P-002]
  - id: P-004
    status: done
    depends: [P-002]
---

# AV 在线补全多源聚合

## Goal And Boundaries

想看片单 **av** 类型的自动补全，从「FANZA 首选 + JavBus 兜底、整条记录归属单一源」
改为「JavBus + jav321 + airav 三源并问、逐字段择优」。做完之后 av 补全**不需要任何凭证**，
今天恒为空的简介会有值，中文片名也能看到。

### 源文档已裁定的结论（不在本计划内重开）

- **D-AVM01** 聚合与走链**并存**。`services/watchlist_metadata_chain.go` 是
  movie/tv/show/anime 的唯一策略，本次零改动；av 是唯一走聚合的类型。
- **D-AVM02** 源清单 = JavBus（复用；其年龄门缺陷已于 2026-09-13 单独修复）+ jav321 + airav，全零认证。
- **D-AVM03** 择优 = 静态按字段优先级表 + 非空优先，表是代码常量、不可配置，
  且**只列真正能落库的字段**。
- **D-AVM04** `SourceName` 写固定字面量 `aggregate`；新增列 `source_fields` 存字段级归属。
- **D-AVM05** 番号归一化只做去空白 / 转大写 / 分隔符统一，**不做**补零等源专有编码换算。
- **D-AVM06** 存量 av 条目不动、不回填。
- **D-AVM07** FANZA 的 `settings` 两列**保留在库不删**，只从结构体与载荷移除。
- **D-AVM08** 手动重选候选保持**单源**语义，且**应用候选后 `source_fields` 必须清空**。
- **D-AVM09** 三源并发共用 registry 注入的 `*http.Client`，不得自建。
- **D-AVM10** 有一源成功即成功；全失败按「最值得处理」排序上报，全 `not_found` 才报 `not_found`。
- **D-AVM11** 源站片名另存新列 `source_title`，**不覆盖**用户手输的 `Title`（番号）；
  界面并列展示；日文原题本期无落点、直接丢弃。
- **D7（requirements.md）** 两个新列**只在 `kind==av` 时写**，其余类型恒为空串。
  两条写入路径（`settleEnrichmentSuccess`、`ApplyCandidate`）**全类型共用、无 kind 分支**，
  不判类型会让一条 movie 条目从「沙丘」变成「沙丘 · Dune」——红线禁止的可观察变化，
  而且既有 movie 测试抓不到（新列不在任何断言里）。

### 实施顺序是硬约束

```mermaid
flowchart LR
    P1[P-001 聚合器落地<br/>av 只挂 JavBus] --> P2[P-002 接 jav321] --> P3[P-003 接 airav] --> P4[P-004 FANZA 代码清除]
```

先让 av 有源再摘 FANZA。反序会留下一段 **av 完全没有源**的窗口
（概要设计 §6 R3；设计提案「Implementation And Transition」）。

四个切片**全链串行，无并行空间**：相邻切片至少共享以下可变文件——
P-001↔P-002 共享 `registry.go` / `registry_test.go` / `probe.go` / `probe_test.go` /
`aggregate_test.go` / `OnlineSourceSection.vue`；
P-002↔P-003 共享同一组；P-003↔P-004 共享 `probe.go` / `probe_test.go` /
`registry_test.go` / `OnlineSourceSection.vue` / `models.ts`。

### 非目标

不改 TMDB / Bangumi 适配器与它们的类型路由；不接 javdb（需 cookies）与 r18dev（需离线 dump）；
不做本地 NFO 优先、文件监控、重命名归档、演员资料库；
**不存日文原题**；**不给 `WatchlistMetadataDetail` 新增字段**
（jav321/airav 的 runtime、studio 本期直接丢弃）；
字段级归属（`source_fields`）本期只入库、不在界面展示；不回填历史条目。

### 受保护行为（技术红线）

movie / tv / show / anime 四个类型的补全行为**零变化**。下列**三个源文件**
不应出现在本次改动中：

```
services/watchlist_metadata_chain.go
services/watchlist_metadata_tmdb.go
services/watchlist_metadata_bangumi.go
```

**`watchlist_metadata_javbus.go` 已移出红线**：2026-09-13 用真实请求查出它缺年龄门
cookie（源站 302 到 `/doc/driver-verify`，Go 跟过去拿回解析不了的 200 → 每次查询都
落 `source_error`），该缺陷已在本计划开工前单独修复并端到端验证。它不再是「零改动复用」。

**注意区分源文件与测试文件**：`chain_test.go`、`bangumi_test.go`、`probe_test.go`、
`registry_test.go` 里存放着 FANZA 与 av 链的测试，本计划**必须**改它们。
改测试不构成越红线；红线只管上面四个 `.go` 源文件。

**红线的已知代价**：P-004 之后，这四个源文件里提到 FANZA 的注释
（`chain.go:12,43`、`bangumi.go:18,96`、`tmdb.go:17`）会带着已删源的
名字留存。（`watchlist_service.go:110` 的同类注释**不在红线内**，该文件本就在 P-001 的
writes 里，顺手改掉即可；`javbus.go:16`（「只有 FANZA 查不到时才问它」）同理——该文件已移出红线；`registry.go:20-21,54-56`、`watchlist_metadata_source.go:97,110`、
`settings_service.go:100`、`models/video.go:239` 同理，各自所属切片顺手改。）它们全是注释、无符号引用，
不影响编译，但会误导后续会话以为 av 仍走 FANZA。这是红线换来的代价，明记在此。

补全的认领（`claimEnrichment`）与写回（`writeBackEnrichment` 的 `status + claim` 双守卫 CAS）
**一字不改**。本计划不引入任何新的并发持久化状态：聚合全部发生在写回之前，
新列与 `source_name` 在同一条既有 UPDATE 里写。

### 开工前基线（已实测，2026-09-13）

基线提交 **`3d9b3e0`**。`go build ./...` 通过。

- `./services`、`./database`、`./models`、`./database/migrator` 等包**全绿**
- 前端 `npm run test:components`：60 文件 / 652 用例**全绿**
- **根包 `video-master` 有一个既有失败**：`TestDeleteImageDirectoryHidesItsImages`
  （`app_test.go:994`，「删除图片目录后……实际还有 1 张」），连跑 3 次 3 次失败，
  **不是偶发**，与本次范围（想看片单 / av）无关。用户裁决：记录在案、照常开工。

**开工前工作区已有用户的并行改动**（用户确认是本人在改，本计划不得触碰）。
2026-09-13 实测 **12 条**已跟踪 + 1 条未跟踪：

```
 M AI-CONTEXT.md                                    ← 见下，本计划也要写它
 M frontend/scripts/library-2.test.mjs
 M frontend/src/components/VideoListPage.vue / .test.js
 M frontend/src/components/VideoListRow.vue
 M services/iina_progress_service.go / _test.go
 M services/jellyfin_library.go / _test.go
 M services/jellyfin_playback.go
 M services/library_service.go / _test.go
?? frontend/src/components/VideoListRow.test.js
```

**该清单会继续变动**——用户还在并行工作。开工时必须重取一次
`git status --porcelain` 作为当时的快照，不要照抄本文写死的清单。

**`AI-CONTEXT.md` 是共享文件，需要特殊处理**：它既在 P-004 的 `writes` 里，
又已承载用户的未提交改动（2026-09-13 新增的「算看完」与「历史遗留记录不回填」两条裁决）。
按工作约定「两个改动触及同一共享文件时顺序整合并重读合并结果，绝不让一次编辑覆盖另一次」：

- P-004 编辑它之前**必须重读当前工作区版本**，不得基于 HEAD 版本或本文引用的行号
- 只改本计划要改的三处（av 路由、「策略只有一份」、Settings 列数），**按内容定位，不按行号**
  ——用户的改动已经让这三处从 `:324/:327/:328` 漂到 `:326/:329/:330`，还会继续漂
- 红线与最终 diff 审阅：其余 11 条在途路径整条减除；`AI-CONTEXT.md` **逐 hunk 分辨归属**

`services/watchlist_metadata_javbus.go` 与 `services/watchlist_metadata_chain_test.go`
的改动**属于本计划**（开工前的年龄门修复），不在减除之列。

执行期间若测试只剩这一条红，即非本次改动所致。**注意**：用管道跑测试时
（如 `go test ./... | tail`）拿到的退出码是管道末端命令的，不是 `go test` 的——
必须看输出里的 `FAIL`，不能只看退出码。

## P-001 av 改走聚合器，先只挂 JavBus；新增两列与片名展示

本切片把 av 的补全路径从走链切到聚合器，但**源清单里只有 JavBus 一个**，
用来单独验证「链路切换本身没有回退」。

交付：番号归一化、聚合器（并发调度 + 逐字段择优表 + 失败分类聚合）、
`lookupWatchlistDetail` 的按类型分流、`WatchlistEntry` 的 `source_fields` 与
`source_title` 两个新列、以及前端的片名并列展示。

**等价性的一处例外**：产出与今天 JavBus 单源补全基本一致（差别在 `source_name` 变成
`aggregate`、多两列），但 **D-AVM05 的归一化会改变实际发给 JavBus 的查询串**——
今天 `javbus.go:125` 只做 `TrimSpace`，本切片起还要转大写与统一分隔符。
由此产生的结果差异是预期的，不要当成回退。

四处连带改动，漏掉任何一处本切片自己的验证就过不去：

1. **编译连带**：av 链不再含 FANZA 后，`registry.go:49` 的 `fanza := NewFANZA...`
   成为未使用变量，必须摘掉这行装配。
2. **链断言连带**：`bangumi_test.go:711-715` 与 `registry_test.go` 断言 `Chain(av)`
   长度为 2 且首位是 FANZA，都要改到新期望值。
3. **探测连带**：连接探测**通过路由表反查适配器**
   （`probe.go:199-209` 遍历 `registry.Chain(target.kind)` 按名字找源）。
   fanza 一离开 `Chain(av)`，`probe.go:75` 的 fanza 探测目标就再也解析不到，
   `probe_test.go:195-241` 的两条测试会红。因此**探测表的 fanza 项与设置页的
   FANZA 分区在本切片一并移除**——否则设置页会留下一个返回「装配资料源失败」的死按钮。
   FANZA 的**代码**（适配器文件、配置结构体、Settings 列、环境变量）仍留到 P-004。
4. **手动重选连带**（D-AVM08）：`watchlist_service.go:345-358` 的 `ApplyCandidate`
   写 `source_name` 但不写新列，必须补上 `source_fields` 置空与 `source_title` 写入。
5. **kind 边界连带**（D7）：上一条与 `settleEnrichmentSuccess` 这两条路径都是
   **全类型共用**的，必须显式判 `kind==av` 才写新列。不判会动到四个受保护类型。

前端另需：`SOURCE_LABELS` 加 `aggregate: '多源合并'`（**`fanza` 键必须保留**，
存量条目的源名仍是它）；修正 `OnlineSourceSection.vue:6` 与 `:133-134` 关于
「AV 先问 FANZA」的文案；Wails 绑定重新生成以带上两个新字段。

**前提已清除**：2026-09-13 查明 JavBus 因缺 `dv=1` 年龄门 cookie 而对每次查询都返回
`source_error`（从未真正工作过），该缺陷已在开工前单独修复，并用 `SSIS-001` 做真实
请求端到端验证通过（番号/标题/年份/导演/类型/演员/封面全部落位，`Overview` 与
`Rating` 确认为空）。本切片「与今天的 JavBus 等价」现在有了真实基准。

完成的判据：movie 条目补全后两个新列仍为空串、界面无 `·` 后缀（D7）；
av 条目补全后 `source_name` 为 `aggregate`、`source_fields` 记录字段来源、
`source_title` 存源站片名而 `title` 仍是用户输入的番号，界面两者并列且片名为空时不留分隔符；
三个桩源给互补字段时合并结果齐备且归属可复现；桩源之一 500 时仍成功；
桩源全 `not_found` 时报 `not_found`，两个 `not_found` 加一个 `proxy_unreachable` 时
报 `proxy_unreachable`；手动重选候选后 `source_fields` 为空串；
含 `source_name='fanza'` 已补全行的老库升级后内容仍可读（SQLite 与 PostgreSQL 各验一次）；
movie/tv/show/anime 的既有测试全绿。

> writes: `services/watchlist_metadata_javbus.go`（仅注释与已落地的年龄门修复）, `services/watchlist_metadata_chain_test.go`（同上）, `services/watchlist_metadata_aggregate.go`, `services/watchlist_metadata_aggregate_test.go`, `services/watchlist_metadata_normalize.go`, `services/watchlist_metadata_normalize_test.go`, `services/watchlist_enrichment.go`, `services/watchlist_enrichment_test.go`, `services/watchlist_service.go`, `services/watchlist_service_test.go`, `services/watchlist_metadata_registry.go`, `services/watchlist_metadata_registry_test.go`, `services/watchlist_metadata_bangumi_test.go`, `services/watchlist_metadata_probe.go`, `services/watchlist_metadata_probe_test.go`, `models/watchlist.go`, `database/watchlist_schema_test.go`, `database/watchlist_kind_schema_test.go`, `frontend/src/components/WatchlistPage.vue`, `frontend/src/components/WatchlistPage.test.js`, `frontend/src/components/settings/OnlineSourceSection.vue`, `frontend/src/components/settings/OnlineSourceSection.test.js`, `frontend/wailsjs/go/models.ts`
> anchors: `AC-01, AC-02, AC-03, AC-04, AC-05, AC-07（部分：移除探测表 fanza 项与设置页 FANZA 分区）, AC-08, AC-09; D-AVM01, D-AVM03, D-AVM04, D-AVM05, D-AVM06, D-AVM08, D-AVM09, D-AVM10, D-AVM11; TC-01, TC-02, TC-03, TC-06, TC-07, TC-08`
> architecture: `复用 services/watchlist_enrichment.go:565 watchlistSourceDetail 作为单源一跳，复用 registry 建好的共享 *http.Client（watchlist_metadata_registry.go:30 的既有约定），复用 watchlist_metadata_source.go:102-112 的六个失败分类不扩张；聚合器归 services 所有，依赖方向维持单向 WatchlistService → 聚合器 → 适配器 → net/http，聚合器为纯函数不碰数据库；字段归属表经 lookupWatchlistDetail 的内部签名回传给 enrichClaimedEntry（不给 WatchlistMetadataDetail 加字段，非目标不被绕过）；共享状态边界＝无新增，认领与写回的 CAS 守卫不动，两个新列随既有 UPDATE 写入，ApplyCandidate 在既有单条 UPDATE 的 map 里增键而非新增语句；维护检查＝聚合器单测可注入假适配器，且三个受保护源文件不得出现在本切片改动中`
> verify: `go build ./... && go test ./services/... ./database/... ./models/...`；`CINEINSIGHT_TEST_PG_DSN=<dsn> go test ./services ./database -timeout 40m`（**必须含 `./database`**：TC-06 的落点在 `database/watchlist_*_schema_test.go`，它们是 `internal/dbtest` 的消费者，只跑 `./services` 覆盖不到 PG 腿）；`cd frontend && npm run test:components`；重新生成 Wails 绑定并确认 `frontend/wailsjs/go/models.ts` 的 diff 只含两个新字段的新增
> review: `择优表与失败分类聚合是核心契约（D-AVM03/D-AVM10）；两个新列须为字符串列 default:''，不得触犯 2026-09-02 的布尔/非零数值 gorm default 禁令；须确认聚合未改动认领/写回 CAS 与 ABA 防护；须确认 ApplyCandidate 清空 source_fields（D-AVM08）；须确认补全仍不写 title（D-AVM11/D-WM06）`

## P-002 接入 jav321，av 变成两源并问

jav321 是三个源里最省事的一个：零认证、`GET /video/{番号}` 单跳直达，
搜索与详情打同一个页面。它补的主要是 **JavBus 拿不到的简介**，外加演员与年份。
（它也给原题，但本仓库没有存日文原题的列，本期丢弃。）

接入后 av 源清单变成 `{javbus, jav321}`，择优表开始真正起作用——
这是第一次能观察到「某字段来自 A、某字段来自 B」。设置页在线资料源分区增加
jav321 的连接探测项（无凭证输入框，只有测试按钮，形态照抄现有 JavBus 分区），
探测目标表同步加一条。

两条硬约定必须落实：出网只用注入的 client，不自建；**解析选择器失效必须归
`source_error`，不得归 `not_found`**——报 not_found 会把用户支去核对番号，
而真正该做的是修适配器。只有 HTTP 404 才是「没有这个番号」。

完成的判据：jav321 产出的简介被择优表采纳（`source_fields.overview` 为 `jav321`）；
构造 404 桩返回 `not_found`、构造结构损坏的 200 桩返回 `source_error`；
连接探测里 jav321 项可解析、可测且经代理；registry 的链装配测试反映两源。

> writes: `services/watchlist_metadata_jav321.go`, `services/watchlist_metadata_jav321_test.go`, `services/watchlist_metadata_registry.go`, `services/watchlist_metadata_registry_test.go`, `services/watchlist_metadata_aggregate_test.go`, `services/watchlist_metadata_probe.go`, `services/watchlist_metadata_probe_test.go`, `frontend/src/components/settings/OnlineSourceSection.vue`, `frontend/src/components/settings/OnlineSourceSection.test.js`
> anchors: `AC-01, AC-03, AC-06; D-AVM02, D-AVM09; TC-01（部分，两源）；TC-04 的完整验证在 P-003`
> architecture: `新增适配器实现既有 WatchlistMetadataSource 接口（watchlist_metadata_source.go:51），不新建平行抽象；出网客户端复用 registry 注入的实例，不自建；归 services 所有，依赖方向不变，适配器不碰数据库不下海报；故障边界＝单源失败被聚合器吞掉不外溢；维护检查＝_test.go 用桩替身覆盖 404 与结构损坏两条路径，解析失效归类有测试钉住`
> verify: `go build ./... && go test ./services/...`；`cd frontend && npm run test:components`
> review: `选择器失效必须归 source_error 而非 not_found（概要 §3.1/§3.2）；确认未自建 http.Client`

## P-003 接入 airav，三源齐备，中文字段到位

airav 是引入多源的**主要理由**：它给中文片名、中文简介和中文标签，
而 CineInsight 界面是中文的。形态上比前两个多一跳——先搜索再取详情页。

接入后择优表完全生效：片名/简介/标签取 airav，封面取 JavBus，年份与演员取 jav321。
搜索命中多条时**取第一条不重排**（源给的就是匹配度顺序）。
同样地，选择器失效归 `source_error`。

本切片完成后，requirements 的三源场景才第一次可以完整验证。

完成的判据：`source_title` 为中文片名且 `source_fields.source_title` 记为 `airav`，
而条目 `title` 仍是用户输入的番号，界面并列展示（TC-08）；
三源互补时合并结果齐备（TC-01）；一源 500 仍成功且缺字段由存活源补（TC-02）；
三源全 not_found 报 not_found（TC-03）；把代理指向一个确定关闭的本地端口时，
三源全部报 `proxy_unreachable` 且无任何一源产出结果（TC-04；口径按
需求设计文档 §11.3 的 `automation`，自动化用例即可，不需要人工抓包）；
同字段两源取值不同时采纳结果可复现（TC-07）。

> writes: `services/watchlist_metadata_airav.go`, `services/watchlist_metadata_airav_test.go`, `services/watchlist_metadata_registry.go`, `services/watchlist_metadata_registry_test.go`, `services/watchlist_metadata_probe.go`, `services/watchlist_metadata_probe_test.go`, `services/watchlist_metadata_aggregate_test.go`, `frontend/src/components/settings/OnlineSourceSection.vue`, `frontend/src/components/settings/OnlineSourceSection.test.js`
> anchors: `AC-01, AC-02, AC-03, AC-05, AC-06, AC-09; D-AVM02, D-AVM03, D-AVM09, D-AVM11; TC-01, TC-02, TC-03, TC-04, TC-07, TC-08`
> architecture: `同 P-002：实现既有 WatchlistMetadataSource 接口，复用注入的 client，不新建平行抽象；搜索→详情两跳仍在适配器内部完成，不把多跳语义泄漏给聚合器；故障边界与维护检查同 P-002，另需覆盖「搜索空结果 → not_found」与「详情页 404 → not_found」两条路径`
> verify: `go build ./... && go test ./services/...`；`cd frontend && npm run test:components`
> review: `三源齐备后择优表的实际生效顺序须与需求设计文档 §4.1.2 表格逐行一致；确认 TC-04 下没有任何源绕过代理直连；确认 source_title 落库而 title 未被改写`

## P-004 FANZA 代码彻底清除

P-001 已经把 FANZA 从路由表、探测表与设置页移走，本切片清除剩下的**代码与配置面**：
适配器文件、出网配置字段、环境变量、`models.Settings` 字段、`UpdateSettings` 赋值、
设置保存载荷，以及散落在既有测试里的 FANZA 测试块。

**FANZA 没有独立的 `_test.go` 文件**——它的测试符号散在**六个**既有测试文件里，
删适配器源文件与结构体字段会直接打断 `services` 包的测试编译，所以这六个必须同时改
（数字为量级参考，以实际编译结果为准）：

| 文件 | FANZA 引用量级 | 内容 |
|---|---|---|
| `watchlist_metadata_chain_test.go` | ~80 处 | 桩、构造器、`TestWatchlistMetadataFANZA*` 系列、AV 链两跳端到端测试 |
| `watchlist_metadata_registry_test.go` | ~15 处 | 链装配断言（P-001 已先改过一轮） |
| `watchlist_metadata_probe_test.go` | ~12 处 | 探测表与凭证载荷（P-001 已先改过一轮） |
| `watchlist_metadata_bangumi_test.go` | 3 处 | `Chain(av)` 断言（P-001 已先改过一轮） |
| `watchlist_metadata_config_test.go` | ~10 处 | 凭证加载与环境变量，删 `WatchlistMetadataConfig` 字段后断编译 |
| `settings_service_test.go` | ~12 处 | 设置保存，删 `models.Settings` 字段后断编译 |

改这些测试文件**不触红线**——红线只管 `chain.go` 等四个源文件。

一处容易漏且会静默出错：`services/settings_service.go:109-110` 那两行**逐字段赋值**
必须同步删掉。`UpdateSettings` 不是整体 `Save(input)` 而是逐字段显式赋值
（同文件 `:39-40` 的注释记着上一次踩坑：漏赋值导致用户拨了开关、提示保存成功、
重开设置页又变回去）。结构体字段删了这两行不删会直接编译失败——这反而是好事。

**数据库两列保留不删**（D-AVM07）。迁移器本就不删列；专门写删列迁移反而会让
回退到旧版本时读不到列。前端 `SOURCE_LABELS` 的 `fanza` 键同样**保留**。

最后必须回写 `AI-CONTEXT.md`：`AGENTS.md:3-5` 声明它是项目上下文的唯一权威、
任何行动前必须先读。其 `:324`（av→FANZA 优先 / 「策略只有一份」）、
`:327`（Settings 新增 5 列的口径，删两列后失效）、`:328`（FANZA 番号换算）
在本计划完成后全部失效，不改会持续误导后续所有会话。

完成的判据：设置保存与连接探测的请求体中不含 `fanza_api_id` / `fanza_affiliate_id`；
探测接口传 `fanza` 返回「未知的资料源」而非静默忽略；两个 FANZA 环境变量不再被读取
且不报错；升级后 `settings` 表仍能查到两个 FANZA 列；存量 `source_name='fanza'`
条目在界面上仍显示为 FANZA；`AI-CONTEXT.md` 上述三处已与实现一致。

> writes: `services/watchlist_metadata_fanza.go`（删除）, `services/watchlist_metadata_chain_test.go`, `services/watchlist_metadata_registry_test.go`, `services/watchlist_metadata_probe_test.go`, `services/watchlist_metadata_bangumi_test.go`, `services/watchlist_metadata_config.go`, `services/watchlist_metadata_config_test.go`, `services/watchlist_metadata_probe.go`, `services/watchlist_metadata_source.go`, `models/video.go`, `services/settings_service.go`, `services/settings_service_test.go`, `frontend/src/components/settings/OnlineSourceSection.vue`, `frontend/src/components/settings/OnlineSourceSection.test.js`, `frontend/src/components/SettingsPage.vue`, `frontend/wailsjs/go/models.ts`, `AI-CONTEXT.md`
> anchors: `AC-07, AC-08; D-AVM06, D-AVM07; TC-05, TC-06`
> architecture: `删除能力而非新增；models.Settings 的数据所有权不变，两列从结构体移除但保留在库——AutoMigrate（database/database.go:336）本就不删列，不写删列迁移即满足降级兼容；依赖方向不变；watchlist_metadata_source.go 仅改接口注释里的源名枚举，不动接口形状；故障边界＝旧前端仍发 fanza 字段时结构体已无该字段，JSON 解码忽略，不报错；维护检查＝升级后表结构仍含两列，且旧版本二进制可正常读写`
> verify: `go build ./... && go test ./services/... ./models/...`；`CINEINSIGHT_TEST_PG_DSN=<dsn> go test ./services ./database -timeout 40m`；`cd frontend && npm run test:components`；重新生成 Wails 绑定并确认 `models.ts` 的 diff 只含 `models.Settings`（`:472-473,550-551`）与 `services.WatchlistMetadataProbeInput`（`:6318-6319,6331-6332`）两处 fanza 字段的删除
> review: `删字段属公开载荷变更：须确认 settings_service.go 的两行赋值已删、models.Settings 字段已删、而数据库两列未删；确认 SOURCE_LABELS 的 fanza 键保留；确认 AI-CONTEXT.md 的 :324/:327/:328 三处已同步`

## Integration And Final Verification

- **全量回归**：`go build ./... && go test ./...`；
  `CINEINSIGHT_TEST_PG_DSN=<dsn> go test ./services ./database -timeout 40m`
  （PG 必须同时含 `./database`，且必须给足超时——Docker 卷上 fsync 极慢，
  10 分钟默认超时会被拖过）；`cd frontend && npm test`。
  与上方「开工前基线」对照：只剩 `TestDeleteImageDirectoryHidesItsImages` 一条红才算通过。
- **技术红线核验（整体 diff）**：用**比较工作区**的形式，不能用 `<基线>..HEAD`——
  本仓库工作约定是「未经用户明确要求不得 commit」，实施期不产生新提交，
  `3d9b3e0..HEAD` 恒为空集，会给出一条假绿证据。正确形式：

  ```
  git diff --name-only 3d9b3e0        # 已跟踪文件的改动
  git status --porcelain              # 覆盖新增未跟踪文件
  ```

  两者合起来都不得包含 `services/watchlist_metadata_chain.go`、
  `watchlist_metadata_tmdb.go`、`watchlist_metadata_bangumi.go`。
  同名 `_test.go` 不在红线内，本计划有意改动它们。`javbus.go` 已移出红线（见上）。
- **整体架构检查**：确认没有出现第二份出网客户端、第二套失败分类、第二处源路由表；
  聚合器与走链的边界仍只由 `lookupWatchlistDetail` 一处按类型分流承担；
  认领与写回的 CAS 守卫与 ABA 防护未被改动；补全仍不写 `title`。
- **AC-06 跨切片验证**：三源全部经 `NewWatchlistMetadataHTTPClient`，
  代理指向确定关闭的端口时无任何源直连（TC-04 在 P-003 首次可验，整合期复验一次）。
- **文档一致性**：`AI-CONTEXT.md` 的 av 路由、「策略只有一份」、Settings 列数与
  FANZA 相关描述已与实现一致（AGENTS.md 声明它是唯一权威）。
- **仅在整合层覆盖的锚点**：无。AC-01～AC-09、D-AVM01～D-AVM11、TC-01～TC-08
  均已落到具体切片。

## Handoff And Residual Risks

- Review evidence: `第一轮（plan blob 4a10790）blocked，四项阻塞已修；第二轮
  （plan blob 3f83509）blocked，四项阻塞 B-1（探测经路由表反查会被 P-001 打红）、
  B-2（择优表含从不落库的 Title/OriginalTitle）、B-3（红线命令由假阳性变假阴性）、
  B-4（PG 命令漏 ./database）与九项改进已全部处理。B-2 经用户裁决新增
  source_title 列（requirements.md D6 / AC-09 / TC-08，设计三份已同步至
  D-AVM11 / 概要 v1.2 / 详设 V1.0.4）。第三轮（plan blob 55e7e07）blocked，两项阻塞
  B-1（新列写入路径全类型共用、缺 kind 边界）与 B-2（工作区已有他人在途改动、基线漂移）
  及十二项改进已处理：B-1 按 requirements.md D4 推出 D7 并写死边界，B-2 记录脏路径快照。
  另据 2026-09-13 真实页面侦察修正两处设计事实（jav321 有评分、javbus 有年龄门）。
  第四轮（plan blob 6b4bcab）blocked，两项：B-1（脏快照过时且漏了 P-004 要写的
  AI-CONTEXT.md）、B-2（详设 §11.4 的 D-AVM02 仍钉「JavBus 零改动」，与已落地的年龄门
  修复冲突，执行者照它办会把修复回退）。两项及八项改进已处理：快照刷新为实测 12 条并
  加共享文件处理指令；设计三份统一改为「reuse + 一处年龄门修复」（详设 V1.0.6 / 概要 v1.3）。
  本轮修订后的内容待第五轮评审`
- Blockers: `无。用户 2026-09-13 裁决绕过计划就绪门，改为 prompt-first 执行；
  本计划转为记录用途，切片划分与约束仍然有效，但不再走 $exec 的准入。`
- Residual risks:
  - **jav321 与 airav 的字段映射仍零真实请求验证**（概要 §6 R1 的剩余部分）。
    **JavBus 这一家已于 2026-09-13 用 `SSIS-001` 真实验证通过**，不再属于本风险。
    剩下两家的自动化验证仍全部基于桩替身，**不阻塞 plan/exec，但阻塞「完成」声明**。
  - jav321 与 airav 是 HTML 抓取，站点改版即失效。唯一缓解是「解析失效必须归
    `source_error`」这条硬约定，已写进 P-002/P-003 的 review 行。
  - 回退到旧版本后，新补全条目的 `source_name='aggregate'` 在旧前端显示为裸字符串，
    `source_title` 被忽略。不崩，不为此加兼容代码。
  - P-004 之后三个受保护源文件里的 FANZA 注释成为错误描述（见「红线的已知代价」）。
  - 基线自带一条既有红：`TestDeleteImageDirectoryHidesItsImages`，与本范围无关，
    用户裁决记录在案、不在本计划内修。
- Resume note: `全部完成（prompt-first，2026-09-13），P-003 除外。基线 3ea8fd7。

  P-001 ✅ 聚合器 / 番号归一化 / 两个新列 / 按类型分流 / 片名并列展示。
  P-002 ✅ jav321 接入，真实番号 SSIS-001 端到端验证通过（简介、演员、年份、评分、封面）。
         关键发现：它的 /video/ 只认 DMM 补零编码，标准番号要过 POST /search 让源站自己换算——
         因此 D-AVM05「不做源专有编码换算」得以保持。
  P-003 ❌ **blocked，不再推进**：airav 全站含 API 返回 Cloudflare JS 挑战，普通 HTTP 客户端
         过不去；候选替代 freejavbt 抽样 8 个番号仅 3 个有中文标题且 1 个是错片；javlibrary
         同为 CF 挑战。用户裁决缩为两源（requirements.md D8）。要重启需先解决 CF 或找到
         新的中文源，届时只需加一个适配器文件 + 路由表一行，择优表已为它留好位置。
  P-004 ✅ FANZA 代码清除（适配器、配置、环境变量、Settings 字段、settings_service 两行赋值、
         六个测试文件里的 FANZA 块、设置页分区、Wails 绑定），数据库两列按 D-AVM07 保留未删。
         AI-CONTEXT.md 已回写（av 路由、验证状态、Settings 列数三处）。

  验证：go test ./... 只余基线既有红 TestDeleteImageDirectoryHidesItsImages；
  前端 61 文件 662 用例全绿；PostgreSQL 双后端 services 264s / database 27s 全绿；
  红线核验三个受保护源文件未触碰。

  历史：P-001 的原始 Resume note——基线 3ea8fd7。交付：番号归一化、
  聚合器（并发+择优表+失败分类聚合）、两个新列、按类型分流、av 链去 FANZA、探测表去 FANZA、
  设置页去 FANZA 分区、想看页片名并列展示、Wails 绑定重生成。验证：go test ./... 只余基线既有红
  TestDeleteImageDirectoryHidesItsImages；前端 61 文件 662 用例全绿；PostgreSQL 双后端
  services 260s / database 35s 全绿。下一步 P-002（接入 jav321）。`
