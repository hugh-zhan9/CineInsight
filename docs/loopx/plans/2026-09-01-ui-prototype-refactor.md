---
source: Claude Design 原型两份 —— `析微影策 · 桌面端界面重构.dc.html`（11 块画板）与 `手机端短视频播放.dc.html`；用户 2026-09-01 两项裁决：手机端保留点赞（右侧栏六按钮）、原型新增统计全部补齐
status: ready
slices:
  - id: P-001
    status: done
    depends: []
  - id: P-002
    status: done
    depends: [P-001]
  - id: P-003
    status: done
    depends: [P-001]
  - id: P-004
    status: done
    depends: [P-002, P-003]
  - id: P-005
    status: done
    depends: [P-004]
  - id: P-006
    status: pending
    depends: [P-005]
  - id: P-007
    status: pending
    depends: [P-005]
  - id: P-008
    status: pending
    depends: [P-002]
  - id: P-009
    status: pending
    depends: [P-002]
  - id: P-010
    status: pending
    depends: [P-001]
  - id: P-011
    status: pending
    depends: [P-002]
  - id: P-012
    status: pending
    depends: [P-001]
  - id: P-013
    status: pending
    depends: [P-001]
  - id: P-014
    status: pending
    depends: [P-001]
---

# 按 Claude Design 原型重构桌面端与手机端界面

## Goal And Boundaries

把两份已定稿的 Claude Design 原型落到实现：桌面端六个页面加两个大弹窗按 A1–A11
画板重排，手机端短视频 Feed 按整屏吸附 + 右侧操作栏 + 底部动作面板重做。原型已经
替我们settle 了三条主设计结论，实现不再重新讨论：

1. **工具栏按频率分三层。**常驻第一行只放搜索与高频切换；体积、分辨率、评分三类
   区间条件收进「筛选」弹出层并用计数徽标回显；12 个库维护动作收进「管理」菜单分
   扫描 / 整理 / 补全 / 维护四组；保存视图的三个控件合并成一个「视图」菜单；随机
   的模式下拉与两个按钮合并成一个拆分按钮。
2. **行内 11 个动作降到常驻 4 个**（预览 · 播放 · 收藏 · 已看）加一个 `⋯` 菜单，
   菜单内容与既有右键菜单共用同一份定义。不做「悬停才出现」的隐藏式动作。
3. **玻璃拟态整体下线。**改为不透明面板 + 1px 发丝线分层，投影只留给真正浮起的弹
   出层。暗色不是浅色反相：面板比画布更亮，主色提亮到 `#23a99b` 以在深底保持对比度。

信息密度不得下降——原型的 88px 行承载了原来两排按钮才装下的内容，这是硬约束而不是
目标。中文界面、浅色/暗色双主题、既有键盘快捷键（F 收藏 / W 已看 / T 加标签 / J K
移动）全部保持可用。

**非目标。** 不引入登录、协作、云同步、通知中心；不做桌面端的移动响应式布局；不接
在线影视资料源；不改动任何后端业务语义（随机加权、清理判定、AI 打标闭集、软删除与
回收站语义、文件迁移安全边界原样保留）。本计划只重排界面并补足原型明确要求的读侧
统计与手机端交互接口。

**用户裁决。** 手机端右侧操作栏保留「点赞」，共六个按钮（收藏 · 点赞 · 评分 · 标签 ·
已看 · 删除），标签偏好学习链路因此不断；现有「收藏页」用户未否决，保留其顶栏入口，
同时新增原型的「播放范围」筛选（其中含收藏范围），两条路径并存。原型出现的新统计全部
补齐。

**统计数据的实际缺口比原型看上去小。**平均单片时长、观看总次数、最长连续天数、已评分
数、评分中位数、评分 21 档补空，全部可由现有 `GetLibraryInsights` 载荷在前端推导，不
新增后端查询。真正需要后端的只有三项：筛选结果计数、最近 30 天新增数、扫描卷可用性。

## P-001 设计令牌换血与玻璃拟态下线

把 `tokens.css` 的两套主题改成原型 A11 的色板：浅色画布 `#f4f6f6`、面板 `#ffffff`、
次级面板 `#eef1f1`、发丝线 `#dde3e3`；暗色画布 `#101415`、面板 `#1a2122`、次级面板
`#171d1e`、发丝线 `#2a3234`，主色 `#23a99b`、主色文字 `#5fd0c2`、主色底 `#12312e`。
`.glass-surface` 从「半透明 + backdrop-filter + 大投影」改为「不透明面板 + 1px 发丝
线」，投影令牌只保留给弹出层与弹窗。

这一片是所有后续片的地基：所有页面都通过令牌取色，改完之后各页面即使还没重排，配色
也应当整体切换且不出现硬编码色残留。完成的标志是全应用在浅色与暗色下都没有半透明面
板、没有 `backdrop-filter`，并且此前遗留的约 20 处浅色字面色（清理与字幕弹窗内）已
收敛进令牌、暗色下可读。

> writes: `frontend/src/styles/tokens.css`, `frontend/src/styles/components.css`, `frontend/src/components/**/*.vue`（仅字面色收敛）
> anchors: 原型 A11 双主题色板；「为什么去掉玻璃拟态」说明卡；`frontend-ui-direction` 记忆中遗留的暗色适配项
> verify: `cd frontend && npm test`；`npx vite build`；人工核对浅/暗两态下 `grep -rn "backdrop-filter\|rgba(255, *255, *255, *0\.[0-9]" frontend/src/styles` 无面板级残留

## P-002 弹出层与菜单原语

新增两个共用原语：锚定式弹出层（用于「筛选」条件面板）与下拉菜单（用于「管理」「视图」
「⋯」以及随机拆分按钮的 `▾` 半边）。原型要求它们行为一致：Esc 关闭、方向键在项间移动、
Enter 确认、点击外部关闭、打开时把列表的键盘快捷键挂起、关闭后焦点回到触发按钮。菜单
支持分组标题与右侧快捷键提示（管理菜单要显示 `⇧⌘N` `⌘R` `⌘T` `⌘K`）。

原语要能被后续所有页面直接复用，因此接口必须与具体业务无关：只接受菜单项定义与锚点
元素。完成的标志是两个原语各有单元测试覆盖键盘交互与外部点击关闭，并且 `⋯` 行菜单与
既有右键菜单能共用同一份菜单项定义。

> writes: `frontend/src/components/ui/BasePopover.vue`, `frontend/src/components/ui/BaseMenu.vue`, `frontend/src/components/ui/*.test.js`
> anchors: 原型「键盘流」说明卡；A1 筛选弹出层；A3 管理菜单；A4「行内 11 个动作：新的归属」
> verify: `cd frontend && npx vitest run src/components/ui`
> review: 焦点管理与快捷键挂起容易和既有全局键盘处理冲突，需独立核对不会吞掉列表的 J/K/F/W/T

## P-003 应用外壳与库计数

header 改为原型样式：左侧应用名，右侧导航改成 32px 胶囊（选中态填主色），最右显示
「库 N 视频 · M 图片」。计数复用既有 `GetLibraryInsights` / `GetImageInsights` 的
summary，不新增查询；两者任一失败时该段整体不显示，不显示占位数字。

> writes: `frontend/src/App.vue`, `frontend/src/styles/components.css`
> anchors: A1 header「库 3,482 视频 · 41,206 图片」
> verify: `cd frontend && npm test`；`npx vite build`

## P-004 视频库工具栏三层重排与结果条

把当前约 25 个平铺控件重排成原型 A1 的两行：第一行是搜索模式三段器、搜索框（带 ⌘F
提示）、智能视图下拉、筛选按钮（带生效条件计数徽标）、排序下拉、随机拆分按钮、视图
菜单、列表/网格三段器、管理菜单；第二行是标签 chip 横滚行加「标签管理」「选择本页」。
筛选弹出层内含体积区间、分辨率区间、评分区间与「存为视图」「应用（N）」。

新增结果条：显示「筛选出 N / 全库 M」、当前生效条件的中文回显、一键清除，右侧是行高
紧凑/舒适切换（新交互，偏好持久化）。计数需要后端支持——新增按 `LibraryFilter` 计数的
服务方法与绑定，复用既有 `applyLibraryFilter` 保证与列表查询同口径；全库总数复用同一
方法传空筛选。计数在筛选变化后请求一次，不随分页滚动重复请求。

完成的标志是原有全部筛选能力在新结构下无一丢失（含语义搜索模式下工具栏的相应变体），
筛选计数与实际翻完页的条数一致，清除按钮能把所有条件复位。

> writes: `services/library_service.go`, `services/library_service_test.go`, `app.go`, `frontend/wailsjs/**`, `frontend/src/components/VideoListPage.vue`, `frontend/src/components/VideoListPage.test.js`
> anchors: A1 工具栏与筛选弹出层；A1「工具栏：每个控件去了哪里」六格说明；结果条「筛选出 218 / 3,482」
> verify: `go test ./services/...`；`cd frontend && npm test`
> review: 25 个控件重排极易漏掉某个筛选入口，需独立核对新旧控件一一对应无遗漏

## P-005 视频行与网格卡重构

列表行改为原型 A1 的 88px 结构：复选框、132×74 缩略图（右下时长角标、底部观看进度
条）、标题行（含已看 / 路径失效徽标）、等宽字体的文件名与目录、元信息行、标签 chip 与
字幕命中句，右侧常驻预览 / 播放 / ♥ / 已看四个动作加 `⋯` 菜单。`⋯` 内含文件组（目录 ·
重命名 · 迁移）、字幕组（字幕 · 编辑字幕 · 预览字幕）与危险区（删除），与右键菜单共用
定义。同时提供窄行变体（68px、96px 缩略图、只留播放 / ♥ / ⋯）供抽屉展开时使用。

网格卡按 A3 重做：`minmax(200px,1fr)`、元信息压在缩略图上、标题两行、底部四个动作。

行高变化会影响虚拟列表的高度预估，`estimateVideoRowHeight` 需要同步更新并覆盖紧凑/
舒适两档与窄行变体，否则长列表滚动会跳动。

> writes: `frontend/src/components/VideoListRow.vue`, `frontend/src/components/VideoListPage.vue`, `frontend/src/utils/virtualList.js`, `frontend/src/utils/*.test.js`, `frontend/scripts/virtual-list.test.mjs`
> anchors: A1 行结构；A3 网格卡；A4 窄行变体与「行内 11 个动作：新的归属」
> verify: `cd frontend && npm test`（含 virtual-list 与 visual-library 两个源文本断言脚本）
> review: 11 个行内动作收进菜单后必须全部仍可达，且删除仍走既有确认与回收站路径

## P-006 多选批量操作栏

选中态下用批量栏顶替结果条（不额外增加高度），显示已选数量与合计体积，四个批量动作
（批量标签编辑 / 批量迁移 / 导入本地资料 / 批量删除）加右侧「选择本页」「清除选择 Esc」。
选中行整排隐去行内动作并换用选中底色，避免误点。Esc 清除选择。

> writes: `frontend/src/components/VideoListPage.vue`, `frontend/src/components/VideoListRow.vue`, `frontend/src/components/VideoListPage.test.js`
> anchors: A2 批量操作栏与选中行变体
> verify: `cd frontend && npm test`

## P-007 详情抽屉重构

抽屉宽度固定 520px 并排，列表自动切到 P-005 的窄行变体；窗口窄于 1100 时改为覆盖式，
列表不再收窄。抽屉内容按 A4 顺序：顶栏（在系统播放器打开 / 目录 / 关闭）、256px 播放
区、标题与原始标题、个人评分（0–10 半分制、可清空）、关联人物与关联作品集两栏 chip、
技术信息键值表、本地资料差异审阅表（字段 / 库内 / NFO 文件 / 采用·忽略）。

> writes: `frontend/src/components/PreviewDrawer.vue`, `frontend/src/components/VideoListPage.vue`
> anchors: A4 抽屉全部分区与断点说明
> verify: `cd frontend && npm test`；`npx vite build`

## P-008 清理候选审阅弹窗重构

改为 A5 的三段结构：顶部标题栏（候选总数、可释放空间、结果过期提示与重新分析）、类别
筛选条（全部 / 精确重复 / 近似重复 / 同源视频 / 低清 / 短视频，各带计数）与后台分析进
度、左侧 268px 目录分组列表、右侧候选流，底栏常显「将移入回收站 N 项 · 释放 X」。同源
候选保持并排 A/B 卡片与「确认同源 / 不是同源」。新增「按建议勾选本组」。

默认零选中这条既有安全边界必须原样保留，删除仍走可恢复回收站。

> writes: `frontend/src/components/VideoListPage.vue`, `frontend/src/components/VideoListPage.test.js`
> anchors: A5 全部分区；「默认不勾选任何一项」安全边界
> verify: `cd frontend && npm test`
> review: 清理会删数据，需独立核对默认零选中、同源否认与回收站可撤销三条边界未被重排破坏

## P-009 AI 标签工作台重构

改为 A6 的左右结构：左侧待审列表（每条含三帧证据缩略图、标签 chip、置信度徽标、视频名、
模型与提示词版本），顶部是全选与批准/拒绝批量动作加置信度、模型筛选；右侧 352px 常驻质量
评估面板（时间窗三段器、标签/模型/提示词三个筛选、指标行「分子 / 分母 · 百分比」）。
分母为 0 显示「—」这条既有语义必须保留。视频同源子列表内联在待审流末尾。

> writes: `frontend/src/components/AITagReviewDialog.vue`, `frontend/src/components/AIQualityPanel.vue`
> anchors: A6 两个子页签合并为主从布局；「分母为 0 显示「—」」
> verify: `cd frontend && npm test`

## P-010 洞察页重构

按 A7 重排：四张摘要卡（视频总数含最近 30 天新增、总时长含平均单片、存储占用含卷可用性、
已看比例含分子分母与进度条）、整宽热力图卡（含图例、观看总次数、最长连续天数）、三栏分布
卡、评分分布柱状图（21 档、含已评分数与中位数），下半部图片库区块。

其中最近 30 天新增需要后端新增计数；卷可用性复用既有扫描目录与监听状态；平均单片时长、
观看总次数、最长连续天数、已评分数、中位数、21 档补空全部在前端从现有载荷推导。

> writes: `services/library_stats_service.go`, `services/library_stats_service_test.go`, `frontend/src/components/InsightsPage.vue`
> anchors: A7 全部卡片与副行指标
> verify: `go test ./services/...`；`cd frontend && npm test`

## P-011 图片库重构

按 A8 改成与视频库同构的两行工具栏（文件名/语义三段器、搜索框、图片流/按文件夹三段器、
排序、管理菜单 + 标签 chip 行），选中态复用 P-006 的批量栏样式，瀑布流改为
`column-count` 布局、列宽 `minmax(190px,1fr)`，勾选框只在悬停或已选时显形，收藏角标常显。

> writes: `frontend/src/components/PhotoLibraryPage.vue`, `frontend/src/components/PhotoCleanupPage.vue`
> anchors: A8 工具栏、批量栏与瀑布流
> verify: `cd frontend && npm test`；`npx vite build`

## P-012 人物与作品集重构

按 A9 改成一套布局两种态。列表态：搜索、排序、新建按钮加 8 列卡片网格（封面、名称、副
标题、活跃作品数）。详情态：顶栏返回 + 头像 + 名称 + 副信息 + 编辑与批量关联 + 重命名，
第二行是按文件夹筛选、多选加入、右侧合计，内容区是 6 列视频卡（预览 / 播放 / 目录）。
作品集在第二行位置换成「拖拽排序」开关，其余完全一致。

> writes: `frontend/src/components/EntityLibraryPage.vue`
> anchors: A9 列表态与详情态
> verify: `cd frontend && npm test`；`npx vite build`

## P-013 设置页锚点导航

16 个分区改为左侧 230px 锚点导航加右侧连续长表单，滚动时高亮当前分区。扫描目录改为表格
行样式，每行显示路径、条数与最近扫描时间、原因码、四态状态徽标与重试。AI 标签库改为每行
一个分类、行内挂标签 chip，颜色方块与启用开关都在 chip 内部编辑，不再跳二级弹窗。

> writes: `frontend/src/components/SettingsPage.vue`, `frontend/src/components/SettingsPage.test.js`
> anchors: A10 锚点导航、扫描目录四态、AI 标签库行内编辑
> verify: `cd frontend && npm test`
> review: AI 标签库有「防误清空」双层保护，行内编辑改造不得绕过成功加载才允许保存的前置条件

## P-014 手机端短视频 Feed 重构

按手机端原型重做：整屏 `scroll-snap` 吸附的连续 Feed（沿用既有一次取一条的加权选择，滚到
底自动续取并追加，右侧圆点指示已加载位置）、右侧六个圆形动作（收藏 · 点赞 · 评分 · 标签 ·
已看 · 删除）、底部标题与元信息与标签行、底部播放进度条、顶部「短视频 N / M」与播放范围
按钮。四个底部动作面板：评分（21 档半分制网格 + 清空）、标签（搜索框 + 可切换 chip）、
删除确认、播放范围（全部 / 未看 / 收藏 / 最近添加 / 未打标签，各带计数）。删除后出现带
「撤销」的浮层提示。

手机端服务器需要新增五个接口：设置评分、列出标签与切换条目标签、设置已看、按范围取下一条
（含各范围计数）、撤销删除。全部复用既有服务层方法（评分走详情更新、标签走既有增删、已看
走 `SetVideoWatched`、撤销走 `RestoreTrashEntry`），不新增业务语义。既有收藏页入口保留。

> writes: `services/short_feed_service.go`, `services/short_feed_service_test.go`, `frontend/src/short-feed/**`, `frontend/scripts/short-feed.test.mjs`
> anchors: 手机端原型全部分区；用户裁决「保留点赞，六按钮」「收藏页保留」
> verify: `go test ./services/...`；`cd frontend && npm test`
> review: 新增五个 HTTP 接口是对外表面，需独立核对参数校验、软删除语义与既有接口一致

## Integration And Final Verification

- 全量套件：`go test ./...` 与 `cd frontend && npm test` 均通过；`npx vite build` 无错误。
- 双主题回归：六个顶级页面与两个大弹窗在浅色与暗色下逐一目视核对，无半透明面板残留、无
  不可读的浅色字面色。
- 键盘回归：J/K 移动、F 收藏、W 已看、T 加标签在列表获焦时仍生效；任一弹出层打开时挂起、
  关闭后恢复；Esc 能逐层关闭弹出层与选中态。
- 控件对账：逐条核对重构前的 25 个工具栏控件与 11 个行内动作在新结构中的落点，确认零丢失。
- `wails generate module` 在后端绑定变更后重新生成，且 `frontend/wailsjs` 差异只含新增项。

## Handoff And Residual Risks

- Blockers: 无。
- Residual risks: 原型的「行高 紧凑/舒适」「按建议勾选本组」「同源组同步标签」三处是原型
  新引入的交互，现有后端无对应能力假设，若实现中发现与既有语义冲突需回到 `spec` 而不是在
  实现里自行裁决。虚拟列表高度预估随行高档位变化，长列表滚动跳动只能靠真机滚动观察，单元
  测试无法完全覆盖。
- Resume note: 从 frontmatter 中第一个非 `done` 的切片继续；P-001 未完成前不要开始任何页面
  级重排，否则会与令牌换血产生大面积冲突。
