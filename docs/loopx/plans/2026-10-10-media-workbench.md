---
schema: loopx-plan/v1
source: .loopx/intake/2026-10-10-media-workbench/requirements.md
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
    depends: [P-011]
  - id: P-005
    status: done
    depends: [P-004]
  - id: P-006
    status: done
    depends: [P-011]
  - id: P-007
    status: done
    depends: [P-003]
  - id: P-008
    status: done
    depends: []
  - id: P-009
    status: done
    depends: []
  - id: P-010
    status: done
    depends: [P-003]
  - id: P-011
    status: done
    depends: [P-010]
  - id: P-012
    status: done
    depends: [P-011]
---

# 媒体工作台持续交付记录

## Goal And Boundaries

按[原始需求与 Q1–Q16](../../../.loopx/intake/2026-10-10-media-workbench/requirements.md)实现；优先性能与诊断，保留六项新功能和视频编辑的完整需求。新的数据/状态合同由[概要](../design/2026-10-10-media-workbench/概要设计.md)与[详细设计](../design/2026-10-10-media-workbench/需求设计文档.md)拥有。计划用于多阶段恢复与两个独立文档责任的协调，不替代方案，不重新请求已经给出的授权。

当前被阻止的切片是详细合同尚不完整，不是用户拒绝或授权缺失；主代理应继续设计并解除对应阻碍。完整图片编辑本轮只出独立项目方案，用户明确不要求实施或与 CineInsight 互通。用户最新授权：全部开发和必要检查完成后提交并推送；没有部署或外部消息授权。

## P-001 图片选择与发布名称

图片已有选择时点主体切换选中，清空后恢复预览；各展示模式与原动作继续可用。三平台打包输入采用当前中文输出名，下载归档名不变。

> writes: `frontend/src/components/PhotoLibraryPage.vue`, `PhotoLibraryPage.test.js`, `.github/workflows/release.yml`, `AI-CONTEXT.md`, 对应既有设计
> anchors: AC-03, AC-12, D-RELEASE-NAME, D-IMAGE-SELECTION, TC-01–TC-07
> architecture: 选择属于既有照片页，复用 toggleImageSelection/openViewer；打包属于既有平台矩阵，不新增状态或脚本
> verify: `npm test`; `npm run build`; 中文产物临时 zip/tar 与缺失输入检查；Windows 仅静态检查，远端未执行

证据：原83项图片页基线通过；新增回归观察到3项因原主体点击行为失败；修复后全量前端99文件/1466项通过，绑定362项使用检查与构建通过。macOS/Linux 正常归档及缺失输入检查通过。无需为已完成修复重走方案审批。

## P-002 运行状态与安全诊断

任务中心统一入口显示数据库、磁盘、依赖、索引、任务与缓存，分项错误不遮住其他项；显式本地导出结构化白名单报告，取消不写，同名不覆盖。

> writes: `app_health.go`, `app_health_test.go`, `services/system_health_support*`, `services/face_runtime.go`, `services/settings_service.go`, `services/backup_service.go`, `services/directory_service.go`, `services/image_service.go`, `frontend/src/App.vue`, `App.test.js`, `frontend/src/components/SystemHealthPanel*`, `TaskCenterDrawer*`, `frontend/wailsjs/**`（生成）, 设计与上下文
> anchors: AC-02, D-MW-HEALTH, TC-11–TC-12
> architecture: App 只读组合现有服务；字段白名单与本地文件输出在同一明确边界；状态事实仍归原服务；不新增任务库或 HTTP 出口
> verify: Go App/受影响服务测试与 race；前端全量与 build；新逻辑变异验证；Wails 绑定生成及使用守卫；本机应用构建
> review: 导出白名单、原生保存路径、同名/符号链接、维护围栏与隐藏页负载，精确 diff 独立只读评审

完成证据：默认Go全量通过（services178.099s）；最后新增等待清单纯读的App/服务定向与race通过；go vet全仓通过；最终前端100文件/1474项、绑定364项和全部脚本通过；最终Go1.24.9 Wails macOS构建退出0。独立审阅首轮3I+1M、复审补1I均修复并经相应范围复审无新实质问题；启动失败只读入口也已独立复审。共10个Go、4个UI有效手动变异被杀死，0存活，生产源码未被变异重写。原生保存对话框/故障磁盘未图形实测；生成models.ts仍有Wails默认空白行tab，未手改生成物。

## P-003 大库审阅的完整范围分页搜索

完整范围的服务端分页检索及对应游标索引，保留字面关键词与标签/置信度语义，避免搜索取回全部详情；性能按同一临时夹具比较。审批和虚拟窗口分出为下面两个独立结果，不以只完成查询宣称 AC-01 全部完成。

> writes: `services/ai_review_search*`, 候选 models 索引标签、对应 App 查询绑定与两侧审阅组件/测试、生成绑定、性能基准
> anchors: AC-01 的查询部分, D-MW-PERF 查询合同, TC-08
> architecture: 审阅与查询由原服务拥有；不新增并行搜索事实库；批准沿用事务所有权；真实窗口和筛选集合独立
> verify: 双后端查询/索引回归；同夹具查询/IPC基准；完整前端/受影响 Go 与绑定使用守卫
> review: 查询语义、游标消费、生成绑定兼容，关键词不放宽范围

查询已有明确实施合同，首轮临时夹具证据见[性能基线](../design/2026-10-10-media-workbench/性能基线.md)。无需等待其他功能的数据模型设计。

实施完成：新增两侧完整范围分页搜索与(status,id)索引，前端200ms防抖、旧响应隔离、读取失败保留、完整标签选项。SQLite与一次性PostgreSQL18定向回归通过；当前App/services/models/database及迁移器包全量退出0（services118.954s）。前端全量100文件/1481项及全部脚本通过；随后同源读取空态修正已另跑两个组件52项通过；最终macOS Wails构建退出0。5个Go查询变异、2个UI生命周期/错误变异和2个源码守卫负例全部被检测，0存活；均在overlay/隔离副本，源SHA未变。

独立评审报1 Important（首次关键词请求丢掉慢到的汇总/同源列表），先红回归复现后拆分请求代次，复审确认解决；复审Minor（读取未结束或失败报“暂无”）已补先红回归并修复，最终52项通过。无Critical。

同一夹具10万候选：旧方案501次/约189.1MiB JSON/50.7–69.3秒；新常见词首屏1次/约96.9KiB/2–9ms；稀有/无命中全范围扫描420–728ms。仅后端取数与序列化，不含真实IPC/布局，不是SLA，累计分配不是RSS。完整原始记录见性能基线；后续批量与渲染结果见P-010/P-011。

## P-010 明确范围的批量审阅

已加载与全部筛选两种入口；全量操作预览数量并冻结集合，确认后新增候选不混入。批次执行、取消、候选版本及预览容量已在[批量审阅合同](../design/2026-10-10-media-workbench/批量审阅合同.md)明确，当前进入实现。

> writes: 两侧候选服务的批次辅助、App 审批绑定、审阅组件与任务中心接缝；按详细合同固化
> anchors: AC-01 的审批部分, D-MW-PERF, TC-09
> architecture: 预览/执行属于原候选服务，审批复用逐项事务；不在App直接写标签关系
> verify: 两后端状态与版本冲突、部分成功、取消、前端确认范围、独立差异审阅
> review: 冻结集合不扩大、过期不自动重新批准、并发与部分成功

已实现两侧预览/冻结/后台批准/取消/分页结果，以及任务中心、退出、数据库维护接线。候选匹配SELECT同时取版本摘要，资格加载再次比对；事务Begin前ctx；旧结果只触发当前pending状态的有界重查，面板按事件顺序核对并保留跨页面保护。旧3个批量Wails入口已迁移，当前368导出均有调用。

独立合同评审3 Important/1 Minor修正后通过；实现首轮3 Important/1 Minor（匹配后版本替换、Begin前缺ctx、旧结果误删、缺进度通知）均先补证据后修正；复审再报1 Important（核对并发丢后到权威结果），先红回归后改为顺序队列，最后定点复审无实质发现。

最终Go全仓退出0（services89.044s）；修复后的PG三包定向退出0（services29.661s），race三包退出0；go vet全仓退出0。前端最终全量101文件/1495项及全部脚本退出0；最终macOS Wails构建退出0（13.542s）。7个有效Go变异与4个UI变异均被杀死，0存活；一次expiry变异因未用变量编译失败已纠正再测，不计入有效数。变异仅overlay/隔离副本，源SHA未变。原生确认/后台实际Wails会话未图形验证。

100,000合成候选的批次夹具已实际执行，预览100,000项/10,000关联，最终批准10,000、跳过重复90,000、失败0；这轮无并发改写，仅证明批次规模与结果，不替代并发回归。初次性能采集在匹配版本修复前，后续增加版本列会影响预览耗时，不将旧耗时作为最终实现性能承诺。

## P-011 有界渲染与 WebKit 验证

视频列表/网格、两侧候选审阅窗口的DOM规模保持有界，单媒体大量候选同样覆盖；标签不隐藏，操作和宽度变化保持锚点。WebKit 旧禁用分支需先复现并取得真实验证。

> writes: `VirtualVideoList*`, `VideoListPage*`, 审阅窗口/纯计算工具与测试；按窗口设计固化
> anchors: AC-01 的渲染部分, D-MW-PERF, TC-10
> architecture: 滚动与几何仍属于窗口组件，数据/选择属于页面；复用照片二维窗口的经验，不把可见DOM当作选择范围
> verify: 窗口纯函数/组件、真实WebKit与大库DOM/滚动/内存基准
> review: 变量高度和列数变化、选择/审批范围、迟到测量与滚动锚点

已完成：Fenwick高度索引、内层网格、active单一滚动恢复、逐条候选窗口；push/splice、离屏标签名称变化均触发重建，选择和批准仍来自父页完整数据。布局提交后测量与恢复按代次隔离，连续重建保留原恢复位置；加载更多只在落定后判断。原生WKWebView验证后移除Mac禁用和grid关闭分支。

原生可重跑脚本`bash scripts/test_virtual_window_webkit.sh`退出0，覆盖2万视频/2万媒体/单媒体10万候选、宽度/密度/标签/追加/删除/切页/尾部；失败fixture和缺参返回非零。数据、纯计算对比与验证边界见[性能基线](../design/2026-10-10-media-workbench/性能基线.md#p-011-最终窗口与原生布局验证)。全量前端103文件1509项+脚本退出0，macOS Wails构建退出0；6有效手动变异均被杀死且源SHA未变。独立复审2 Important/1 Minor全部修正，无剩余实质发现；最终守卫启用与测试脚本增量已由同一独立代理核对，无重要集成问题。完整Wails人工操作及RSS未测。

## P-004 片段书签与观影日记

书签能保存并回跳，日记按实际观看日期回顾并保留用户笔记；缺失的历史不编造。

> writes: 新书签/日记领域模型与服务、App 绑定、对应界面、schema/迁移夹具；按设计补齐后固化
> anchors: AC-04, AC-09, D-MW-BOOKMARK, D-MW-DIARY, TC-13, TC-19
> architecture: 复用媒体 ID、预览和播放账本；用户日记与不可变播放事实分开，删除/来源变化由领域服务明确定义
> verify: 双后端数据与删除/源变化/日期回归，前端跳转与编辑场景，迁移往返

已完成Q17–Q19及书签/日记合同：片段标签与回跳、源token与Revision、逐次观看日记及手工补记、独立删除生命周期、历史回填和删除占位、年度回顾、移动可选观看会话、双向迁库高水位。新增9个App绑定已接界面，合计378导出均使用。

SQLite全仓退出0（services87.792s），PG四包定向、race四包、全仓vet退出0；最终前端107文件1543项+全部脚本退出0，Wails构建11.031s退出0。原生WKWebView合成视频实际验证0定位/区间停止/继续/自然重播/同地址重载，桥接是夹具，不是用户片库人工验收。8个承重Go与12个有效UI变异被阻止，源文件未变。多轮独立审阅问题已修正，最终无遗留；完整证据和边界见[书签与日记合同](../design/2026-10-10-media-workbench/书签与日记合同.md#实现与验证记录)。

## P-005 下一集与队列（选片独立为P-012）

选片从本切片抽为P-012，避免等待日记裁定时停下独立工作。此切片保存待播顺序，支持手动推进与可开关自动连播，自然结束只推进一次。

> writes: 片库筛选与保存视图调用方、队列领域模型/服务、播放适配和播放器组件；按设计补齐后固化
> anchors: AC-05, AC-06, D-MW-DISCOVERY, D-MW-QUEUE, TC-14–TC-15
> architecture: 复用 LibraryFilter 与播放服务；不改随机计分；队列服务拥有版本和会话，播放器只提供事实事件
> verify: 条件/空集、真实 IINA IPC、并发改队列、重复 EOF/停止/失败、双后端与直接调用方

已取得真实IINA单进程连续装载、播放末尾/stop/error及暂停握手证据；keep-open=no的窗口收尾竞态已复现，采用keep-open=yes的eof-reached事实。[播放队列合同](../design/2026-10-10-media-workbench/播放队列合同.md)已Ready。实施前审阅2 Important/2 Minor以及持续准入增量均闭合：先实现context贯穿/持续维护准入与事实收尾，再实现队列模型/控制器、严格源交付、IINA/inline适配及UI。共享行为基线`go test ./services -run 'Test(LaunchPlayback|IINA|Collection|PLAY07|UpdateVideoWatchProgress)' -count=1 -timeout 300s`退出0（2.192s）。普通播放和随机计分/换一个规则须保持，公共入口及维护/备份直接调用方扩大回归。

进行中：context贯穿、持续播放准入和退出终态前置已实现。完整Go在队列模型加入前通过（services90.303s），PG前置定向通过；扩展race揭示原HEAD既有测试变量竞争，已在隔离HEAD重复复现后只修测试夹具加锁。退出取消迁移的重开窗口先红后修，独立复核及重复race通过。

队列模型、CAS编辑、有界分页、作品集冻结顺序及FD媒体租用已加入，SQLite/PG与迁移往返通过。存储独立复核1 Important/1 Minor（作品集起点跨语句快照、当前项并发删除错误分类）均先红后修，定点复审闭合。7个承重变异最终均被杀死；首轮2个测试缺口通过重排后的ID顺序、代理保持而原片变化的用例补齐。代理旧预览回归通过，实际SQL取消包含代理读/touch/失效删除。

控制器、IPC与专用IINA适配已实现，真实IINA同进程两片/暂停恢复/自然完成/日记和启动计数/停止清理已执行；stop-between-items触发窗口收尾的错误先真实复现，再改pause+loadfile replace。后续5秒样片、库内时长0和99，首片3秒暂停已形成日记，证明实际duration接入（11.806s通过）。

控制器独立审阅5 Important及sequence归零风险：Stop数据库读写失败、拒绝替换副作用、旧start-file、实际时长、错误路径清洗及单调通知均已修，正在定点复审。Stop/拒绝替换/写失败补写/路径/序号回归先红后绿；prepared计划冻结且不在事务内控制播放器。SQLite/race/PG定向通过；新增超限作品集及准备后成员新增的范围验证也已加入。

App生命周期、专用Range路由、生成的7个队列绑定、全局面板/播放器、行菜单/批量/详情/作品集入口已接，仍在集成验证。前端调用方7文件302项、新组件2文件9项通过，原生WebKit新播放器、完整前后端、最终构建和App/HTTP/UI独立复核尚未完成；P-005仍在进行中。

2026-10-11收尾（Claude接手）：最终独立差异复核报2 Important（拖动到末尾使会话失步、IINA复用进程丢失属性观察者）与3 Minor（初次play被暂停打断判失败、IPC事件通道在慢写库时溢出、重挂载旧序号被静默丢弃），均先补失败回归再修正，并经同一复核者定点复审确认。复审提出“拖到末尾算不算自然结束”，主代理依TC-15/AC-09/本合同“非seek状态”裁定不算：seek后须观察到越过落点的实际播放才推进并记有效观看，两种播放器一致。最终全量与构建见Integration。真实IINA集成测试（`CINEINSIGHT_TEST_IINA=1`）与WebKit新播放器图形验证交用户。


## P-006 多版本聚合

人工确认的版本组可聚合展示和选择播放，保留文件级状态及删除独立性。

> writes: 新版本分组领域模型/服务、片库展示/筛选与详情；按设计补齐后固化
> anchors: AC-07, D-MW-VERSIONS, TC-16
> architecture: 分组不替代作品集/同源；建议需要用户确认，现有视频仍是文件生命周期所有者
> verify: 双后端游标与筛选、合并/拆分/删除、元数据不串写

2026-10-10 合同已收敛为[版本分组合同](../design/2026-10-10-media-workbench/版本分组合同.md)，解除阻碍。实现由独立worktree叶子代理完成后由主代理逐个整合；共享文件（AllModels、app.go、VideoListPage、迁移器夹具、生成绑定）由主代理顺序合并。

> verify: SQLite与一次性PG的聚合查询/CAS/级联；迁移往返；前端徽标/展开/播放所选版本；绑定消费守卫

完成（2026-10-11）：实现与评审修正见[版本分组合同](../design/2026-10-10-media-workbench/版本分组合同.md#实现与验证记录2026-10-11)。独立评审1 Important（默认开启聚合时过滤重复执行）/4 Minor均已修正并补回归；12个承重变异均被检测。

## P-007 字幕与视觉场景检索

两类场景都覆盖整片并返回具体时间；双数据库、本地优先，外部 API 只在显式启用后使用。

> writes: 场景索引领域模型/服务、本地运行时、查询与预览入口、模型配置/任务中心/迁移；按选型后固化
> anchors: AC-08, D-MW-SCENES, TC-17–TC-18
> architecture: 派生索引按源/模型/代际隔离；复用探测、抽帧和媒体槽；不得把本地错误变成外发
> verify: 中文固定检索语料、全片/后半段、失效和模型切换、双后端规模基准、取消恢复与零外发证明
> review: 模型许可/下载完整性、外部数据边界、代际发布与旧索引回收

2026-10-10 合同已收敛为[场景检索合同](../design/2026-10-10-media-workbench/场景检索合同.md)，含本机Chinese-CLIP量化ONNX实测；解除阻碍。共享的`/preview/frame/{id}?ms=`单帧预览由主代理先行实现（`services/thumbnail_frame_at.go`，服务与路由测试通过），场景检索与视频工作台共用。

> verify: 对白后半段/画面替身向量/源变化与模型更换/零外发；int8扫描基准；运行时准备在本机实跑一次；前端页面与设置分区

完成（2026-10-11）：临时目录实跑运行时准备与中文检索冒烟通过；独立评审2 Important（改回本地后外发未停止、源不可读删除有效索引）与Minor均已修正并补回归。详见[场景检索合同](../design/2026-10-10-media-workbench/场景检索合同.md#实现与验证记录2026-10-11)。

## P-008 视频编辑

顺序合并、两种批量去片头、多段高清替换；精确和快速模式、多音轨/各类字幕；新产物入库，原片确认后清理。

> writes: 新视频编辑领域、配方/任务模型、工作台与绑定、媒体发布/入库接缝、任务中心/退出保护/迁移夹具；按详细设计后固化
> anchors: AC-10, D-MW-EDIT, TC-20–TC-23
> architecture: 新编辑服务拥有时间线及执行日志；复用媒体槽、探测、受控抽帧；删除必须由既有回收站服务执行
> verify: 多段对齐、真实多轨字幕素材、输出精度/同步、源字节不变、取消/崩溃/磁盘满/重名与清理确认
> review: 不覆盖、来源版本、发布/入库的部分成功与恢复、原片删除边界

2026-10-10 合同已收敛为[视频编辑合同](../design/2026-10-10-media-workbench/视频编辑合同.md)；解除阻碍。“与原片建立版本组”按钮依赖P-006接口，由主代理在两者整合后接线。

> verify: ffmpeg合成夹具（多音轨/文本字幕/共享片头/删减插入的HD版）端到端；取消/重启对账/磁盘/重名/来源变化；原片字节不变；双后端CAS；前端工作台

完成（2026-10-11）：服务、纯计算对齐包`services/editalign`与工作台前端分三路实现后整合；真实ffmpeg端到端（合并/去片头/快速与精确/高清替换/片头识别/分段对齐）通过；“与原片建立版本组”已接P-006接口；补“复制为新草稿”解决排队后配方只读的失败项目无法修正。独立评审2 Important（停止竞态、mpegts起始时间导致快速切点错位）与Minor均已修正并补回归。详见[视频编辑合同](../design/2026-10-10-media-workbench/视频编辑合同.md#实现与验证记录2026-10-11)。

## P-009 独立图片编辑项目方案

按用户 Q13，由单一叶子子代理出完整新项目方案；只读主项目背景、独占文档目录，不实施、不建立集成协议。

> writes: `docs/loopx/design/2026-10-10-image-editor-project/**`
> anchors: AC-11, TC-24
> architecture: 完全独立项目的 proposed 方案；CineInsight 不新增运行时或数据依赖；平台/AI/格式待决由该项目单独拥有
> verify: 主代理核对需求覆盖、只改指定目录、相对链接和图示/未决事项的实际状态

子代理 `image_editor_design` 已交付两份文档，报告25相对链接/11锚点检查通过；3张图未渲染，未运行应用或模型。主代理已核对完整能力范围、独立项目/不集成/只设计边界及 proposed 与已裁定的区分；未把 Mac 栈和 AI 外发建议写成批准决定。7个独立项目待决问题留在其概要中，不打断主流程。

## P-012 今晚看什么

按[选片助手合同](../design/2026-10-10-media-workbench/选片助手合同.md)复用当前片库条件，增加本次可用时间和观看偏好，输出少量候选与事实理由。与P-004的日记/书签数据裁定独立，不新增持久表，不改随机计分。

> writes: `services/video_discovery.go`及测试、`app_discovery.go`、`TonightPickerDialog.vue`及测试、`VideoListPage`与`LibraryToolbar`入口、生成绑定；必要时给既有扫描范围读取透传查询context
> anchors: AC-05, D-MW-DISCOVERY, TC-14
> architecture: 现有VideoService拥有查询，现有LibraryFilter拥有范围；时长为本次选片请求参数，不改变保存视图/随机筛选模型
> verify: 严格筛选/未知时长/边界/排序理由/只读/双后端、UI请求代次/空态/跳转、所有绑定有消费者

P-012已完成。完整范围/预算/偏好查询、弹窗入口与播放/预览接线已落地；共享scope读取透传调用方连接和context，未新增表或改变随机计分。独立评审发现并闭合播放ID接线问题。默认Go全仓、一次性PG定向、go vet全仓、前端1514项/全部脚本、369绑定消费与macOS构建通过；5个Go/3个UI有效变异均被杀死。实际验证记录见合同；补充race服务/App定向也退出0（1.981s/1.750s）。

## Integration And Final Verification

重读源 AC/TC 与当前设计。用户2026-10-10最新裁定：大部分测试由用户真机执行。后续以编译、绑定生成/消费、受影响关键路径和必要的数据/文件保护检查为主；大范围回归、真机体验、复杂故障场景交用户，交付简短测试清单。既有证据不作废，也不因原计划机械重跑全量。交用户的待验项目不计为已验证；功能实现范围不缩减。必要的独立差异审阅仍按实际数据/文件边界执行。

全部功能开发及必要检查完成后，按用户最新明确授权提交并推送远端；不在尚未完成时提前提交收工。

最终整合（2026-10-11）：P-006/P-007/P-008在独立worktree实现、以三方合并回主树；共享文件（AllModels、迁移器夹具、任务登记表26个key与各计数守卫、app.go、VideoListPage等）由主代理顺序合并并重新生成绑定。最终状态：`go vet ./...`通过；默认SQLite `go test ./... -count=1 -timeout 1500s`全部9包通过（services 99.3s）；一次性PostgreSQL18 `go test ./... -count=1 -timeout 3600s`除`TestSceneIndexStartRightAfterCancelIsNotSilentlyDropped`外全部通过（services 1509s），该用例是测试时序竞争（取消可能在worker进入取向量前就收尾），改为等worker真正阻塞后再取消，SQLite与PG各重复20次通过；`npm test` 125文件/1692项及全部脚本通过，绑定417导出全部有调用；`npm run build`与`GOTOOLCHAIN=go1.24.9 wails build`（macOS）通过；Windows仅`GOOS=windows go build ./services/`交叉编译核对。

### 交给用户的真机测试清单

1. 场景检索：设置页“场景检索”准备模型（约200MB，需能访问huggingface.co或填镜像），为若干视频建立画面索引，用中文描述检索画面、用台词检索对白，点结果跳转；切到“外部”时确认披露弹窗，再切回本地确认不再外发。
2. 视频工作台：真实多音轨/内嵌字幕/HDR素材各做一次合并、去片头（统一时间与自动识别各一次）、高清替换（含删减或插入的版本），检查切点画面、音轨与字幕同步；快速模式对比显示切点；导出中取消、退出应用后重开继续、磁盘不足与重名；确认成品后清理原片走废纸篓。
3. 待播队列：应用内与专用IINA两种播放器开启自动连播，自然播完推进一次；暂停、停止、拖到末尾、关闭窗口都不推进。
4. 多版本：大库下开关“合并版本”的列表速度与网格展开行布局；在展开列表里播放非代表版本后汇总更新。

## Handoff And Residual Risks

- 本计划覆盖全部源 AC-01–AC-12 与 TC-01–TC-24；独立图片项目不实施是用户明确授权的范围，其他 blocked 项未被延期或删除。
- 基线：默认后端 `go test ./... -count=1 -timeout 1500s` 退出0，services 101.983s；前端基线含 P-001，99文件/1466项及 build 通过。本轮新增搜索已有一次性PostgreSQL18回归；WebKit组件验证见P-011；真实编辑样片仍待验证。
- P-002 首轮独立 `review_health_diff` 报3 Important（无界 Python 探测、真实构造的人脸服务在 nil DB 时 panic、Status 的无期限前置 SQL）和1 Minor（备份设置失败报零）。已修正为缓存纯读、全程有 context 的已发布索引查询、失败指标缺席；目录与缓存同步采用有期限只读查询。缓存原检查时间保留为 observed_at。定向 Go 回归通过，已送同一代理复审。
- 首轮5个Go和2个UI有效手动变异均被杀死，0存活；UI export_failure 以缺失必需错误元素失败，首个统计脚本把它误当非AssertionError，已核对日志确认为有效。源码只在overlay/隔离副本变异。修复新增的4个变异与最终全量/race/构建正在验证。
- 用户 Q1–Q14 已经通过 Loam memory_add 保存为材料 `drawer_inbox_8911e2_note_3104953b4bd2`，来源是本轮 intake；只存需求裁定，未把实现、提交或部署写成完成。
- 小修复验证日志位于 `/tmp/cineinsight-photo-selection-*.log`、`/tmp/cineinsight-media-workbench-{frontend,build,go-baseline}.log`。健康验证日志 `/tmp/cineinsight-health-*.log`。
- 无提交/推送/部署。Wails 生成器修改 runtime 三文件执行位已恢复到原0644，没有回退用户文件。

- Q15–Q16 追加裁定已保存 Loam `drawer_inbox_8911e2_note_903a0606697c`，来源同 intake；需求批准与实现状态分开。

- Q17–Q19持久裁定已写Loam材料`drawer_inbox_8911e2_note_54eb3af9e1b2`，来源为本轮intake，明确需求已批准/书签日记尚未实施。
