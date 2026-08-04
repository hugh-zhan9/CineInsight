---
source: 用户口头批准（2026-08-04 会话）：做前端评估中问题 1–4 的行为不变重构；明确不做 macOS 三栏改造、不改 UI 展示效果
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
    depends: [P-002]
  - id: P-004
    status: done
    depends: [P-002]
  - id: P-005
    status: done
    depends: [P-002]
---

# 前端样式统一重构（行为不变）

## Goal And Boundaries

目标：消除前端"元素不统一"的四个代码层根因——样式归属散落、按钮类名体系混乱、硬编码颜色绕过设计令牌、弹窗结构手抄——同时保持渲染出来的 UI 视觉与交互行为完全不变。

已由用户settle的结论：

- **不做** macOS 三栏范式改造，不调整任何布局、信息层级或控件形态（用户认为当前整体感觉可以，硬改可能适得其反）。
- 保留现有 `tokens.css` 令牌系统与明暗双主题作为统一的落点。
- 保留 `btn-random`（琥珀色随机按钮是有意的视觉强调，且已定义在共享样式中）。

受保护行为：所有页面在明/暗两个主题下的渲染结果不变；全部 14 组测试（vitest 49 用例 + 13 个脚本测试）保持通过。

已知的三处"隐性失效"样式是例外，修复它们会产生轻微可见变化，属于恢复原始意图的缺陷修复，已向用户在报告中标注：

1. `frontend/src/components/AddTagDialog.vue:298` 使用未定义令牌 `var(--card-bg)`（背景静默失效）；
2. `frontend/src/components/PreviewDrawer.vue:684` 使用未定义令牌 `var(--input-bg)`；
3. `frontend/src/components/SettingsPage.vue:936` 使用未定义令牌 `var(--primary-color)`；
4. `frontend/src/components/VideoListPage.vue` scoped 样式中的 `.btn-processing` 作用不到子组件 `VideoListRow.vue` 中的使用点（字幕生成中的按钮态样式当前不生效）。

非目标：拆分 `VideoListPage.vue` 巨石组件（弹窗抽取除外）、迁移 Composition API、引入状态管理或路由、任何视觉再设计。

关键证据：

- `frontend/src/style.css` 未被任何入口引入（`main.js` 只引 `tokens.css` 与 `components.css`），是死文件；`tokens.css:75` 的 `!important` 为其历史残留。
- `App.vue` 尾部约 160 行**非 scoped** 全局样式定义了 `video-item`、`settings-section`、`setting-item`、`tag-edit-row`、`divider`、`virtual-video-list--grid` 等类，实际使用方是 `VideoListRow`、`SettingsPage`、`TagManagerDialog`、`AddTagDialog`、`VirtualVideoList`、`AITagReviewDialog` 等子组件。
- `.btn-secondary` 与 `.btn-action` 在 `components.css` 中是同一组规则的两个名字（用量 95 / 34）；一次性类名 `btn-small`（AddTagDialog scoped，≈`btn-compact`）、`btn-action-danger`（SettingsPage scoped）、`btn-link`（SettingsPage scoped）、`btn-processing`（VideoListPage scoped、跨组件失效）。
- `.vue` 组件内硬编码颜色约 90 处（`VideoListPage` 34、`AITagReviewDialog` 17、`App` 11 为大头）。
- 9 个文件手写 `modal-overlay`/`modal`/`modal-actions` 弹窗骨架，其中 `VideoListPage.vue` 内联 6 个弹窗（重命名、文件夹重命名、保存视图、清理、字幕预览、字幕生成）。
- 现有共享 UI 原语仅 `components/ui/ActionMenu.vue`，新原语按此惯例放 `components/ui/`。

## P-001 清除死样式文件与残留

删除未被引用的 `frontend/src/style.css`，并移除 `tokens.css` 中因它而生的两处 `!important`（body 背景）。完成条件：仓库内不再有对 `style.css` 的引用；明暗主题下应用背景渲染与改动前一致；测试套件通过。

> writes: `frontend/src/style.css`, `frontend/src/styles/tokens.css`
> anchors: 问题1（样式归属散落）之死文件部分
> verify: `grep -rn "style.css" frontend/src frontend/index.html frontend/short.html` 无命中（short-feed.css 除外）；`cd frontend && npm test` 通过
> review: 无

## P-002 App.vue 全局样式归位

把 `App.vue` 非 scoped 样式块中不属于 App 自身的规则迁出：跨组件共享的布局类（`video-item*`、`video-actions`、`setting-item`、`divider`、`virtual-video-list--grid` 等）迁入 `styles/components.css`；仅单一组件使用的（`settings-section`、`setting-grid`、`tag-edit-row`、`tags-filter` 等）迁入对应组件。App.vue 只保留 header、导航、启动错误页等自身样式。选择器文本原样搬运，不借机改值。

同时把 `VideoListPage.vue` scoped 中的 `.btn-processing` 迁至 `components.css`，使其重新作用于 `VideoListRow` 中的使用点（上文标注的缺陷修复 4）。

完成条件：`App.vue` 样式块中不再有子组件专属类；各页面（列表/网格、设置、标签管理、AI 标签审核）渲染与改动前一致；测试通过。

> writes: `frontend/src/App.vue`, `frontend/src/styles/components.css`, `frontend/src/components/SettingsPage.vue`, `frontend/src/components/TagManagerDialog.vue`, `frontend/src/components/VideoListPage.vue`, `frontend/src/components/VideoListRow.vue`, `frontend/src/components/AddTagDialog.vue`
> anchors: 问题1（样式归属散落）主体；缺陷修复4
> verify: `cd frontend && npm test` 通过；对照迁移前后 `grep` 类名清单逐一确认无遗漏；`wails dev` 或构建后人工核对列表页/设置页明暗主题观感
> review: 迁移是纯搬运还是夹带值变更；scoped→全局迁移是否意外扩大选择器命中范围

## P-003 按钮类名体系统一

统一为一套按钮词汇：`btn-primary` / `btn-secondary` / `btn-danger` / `btn-random` / `btn-compact`（尺寸修饰）。全仓把 `btn-action` 用法替换为 `btn-secondary`（二者 CSS 本就相同），同步更新依赖 `.btn-action` 的上下文选择器（`App.vue` 迁移后的 `video-actions` 规则、`.btn-action.active` 等）；`btn-small` 用法替换为 `btn-compact` 并删除其 scoped 定义（两者渲染差异需先核对，若不一致则保留视觉逐像素一致的处理方式）；`btn-action-danger` 与 `btn-link` 作为真实存在的变体提升进 `components.css`（命名 `btn-ghost-danger`、`btn-link` 或保持原名，以最小 diff 为准），规则值不变。

完成条件：`grep` 全仓无 `btn-action`（类名与选择器）；按钮在所有出现位置（工具栏、行内操作、弹窗、设置页）视觉不变；测试通过。执行时决议：`btn-small`（AddTagDialog）与 `btn-compact` 并非逐像素一致（后者强制 `height: var(--h-compact)`），按"保留视觉逐像素一致"条款保留为真实尺寸变体，不再视为待清理项。

> writes: `frontend/src/components/*.vue`, `frontend/src/styles/components.css`, `frontend/src/App.vue`
> anchors: 问题2（按钮体系名存实亡）
> verify: `grep -rn 'btn-action\b\|btn-small' frontend/src` 无命中；`cd frontend && npm test` 通过（含 video-list-ui、visual-library 等 UI 断言脚本）
> review: 上下文选择器（如 `.video-actions .btn-secondary`）替换后命中集合是否与原 `.video-actions .btn-action` 完全一致，特别是 `.active` 态与 hover 态

## P-004 硬编码颜色收敛进令牌

把 `.vue` 组件与 `components.css` 中约 90 处硬编码颜色替换为既有令牌；值与既有令牌不完全相等时，在 `tokens.css` 新增值完全相同的令牌（含暗色主题对应值，若原代码明暗共用一个硬编码值则新令牌两主题同值），不做任何色值"顺手修正"。重点文件：`VideoListPage.vue`（34 处）、`AITagReviewDialog.vue`（17 处）、`App.vue`（11 处）、`PreviewDrawer.vue`、`RelatedVideoItem.vue`、`SettingsPage.vue`、`SubtitleWorkbench.vue`。

同步修复三个未定义令牌的使用点（缺陷修复 1–3）：`--card-bg`、`--input-bg`、`--primary-color` 分别改为语义正确的既有令牌（如 `--panel-solid-bg`、`--control-bg`、`--accent-color`），这是本切片中唯一允许的可见变化。

完成条件：组件内残留的字面色值仅剩确有语义的特例（如缩略图占位深底、数据 URI 图标）且逐条列入报告；未定义令牌使用为零；明暗主题渲染与改动前一致（三处缺陷修复点除外）；测试通过。

> writes: `frontend/src/components/*.vue`, `frontend/src/App.vue`, `frontend/src/styles/tokens.css`, `frontend/src/styles/components.css`
> anchors: 问题3（硬编码颜色绕过令牌）；缺陷修复1–3
> verify: 复跑"未定义 var(--*) 检查"脚本为零命中；`grep -oE '#[0-9a-fA-F]{3,8}|rgba?\(' frontend/src/components/*.vue` 数量对照改动前收敛并逐条说明残留；`cd frontend && npm test` 通过
> review: 新增令牌值是否与被替换字面值逐字节相等；暗色主题下被替换处是否引入了原本不存在的主题差异

## P-005 弹窗原语 BaseModal

在 `components/ui/` 新增 `BaseModal.vue`，渲染与现状完全一致的 `modal-overlay` / `modal` / `modal-actions` DOM 结构与类名，通过 slot 承载内容与按钮区，通过 prop 透传附加类（如 `ai-tag-review-modal`、`cleanup-modal` 等宽度定制类）。迁移全部 9 个使用方：`AddTagDialog`、`AITagReviewDialog`、`DeleteConfirmDialog`、`ScanDialog`、`SettingsPage`、`TagDeleteDialog`、`TagManagerDialog`、`TrashRestoreDialog`，以及 `VideoListPage` 内联的 6 个弹窗。slot 内容编译在父作用域，父组件 scoped 样式对 slot 内元素继续生效，迁移不改变现有样式命中。

完成条件：仓库内 `modal-overlay` 字面量仅存在于 `components.css` 与 `BaseModal.vue`；每个弹窗的打开/关闭、遮罩、按钮排布与迁移前一致；为 `BaseModal` 补一个渲染结构与 slot 透传的单元测试；测试通过。

> writes: `frontend/src/components/ui/BaseModal.vue`, `frontend/src/components/ui/BaseModal.test.js`（或并入现有组件测试约定）, `frontend/src/components/*.vue`
> anchors: 问题4（弹窗无统一原语）
> verify: `grep -rln 'class="modal-overlay"' frontend/src/components/*.vue` 无命中；`cd frontend && npm test` 通过（vitest 组件用例覆盖新原语）
> review: v-if 挂载时序与事件（如点击遮罩关闭、Esc）迁移前后是否一致；`VideoListPage` 内联弹窗迁移是否遗漏其 scoped 宽度定制类

## Integration And Final Verification

- 全量 `cd frontend && npm test`（vitest 49 用例 + 13 个脚本测试）通过。
- 明暗两主题下人工过一遍：视频列表（列表/网格）、设置页、标签管理、AI 标签审核、回收站、字幕工作台、各弹窗，确认与重构前观感一致。
- 复跑评估用的三个度量并写入报告：组件内硬编码色值计数、`btn-*` 类名清单、`modal-overlay` 使用点清单。
- 四处缺陷修复（未定义令牌 ×3、`.btn-processing` 失效）的前后差异截图或描述，供用户确认。

## Handoff And Residual Risks

- Blockers: none
- Residual risks: UI 断言类脚本测试（video-list-ui、visual-library 等）可能对类名有字面断言，P-003/P-005 改名时需同步测试期望，属预期内修改而非行为回归；短视频流（short-feed）独立入口只引 `tokens.css`，改动 `components.css` 不影响它，但 P-004 动 `tokens.css` 时需确认 short-feed 观感不变。
- Resume note: 按 frontmatter 顺序执行；P-003/P-004/P-005 相互独立但都依赖 P-002 完成后的样式归属现状，若中断，先核对 frontmatter status 再继续。

## 执行结果（2026-08-04）

全部 5 个切片完成。最终验证：`npm test` 全绿（vitest 10 文件 53 用例，含新增 BaseModal 4 用例；13 个脚本测试全部通过）；`npm run build` 成功；未定义 `var(--*)` 引用清零。独立评审（general-purpose 子代理）审查完整 diff：无 Critical/Important 发现，3 个 Minor 已处置（btn-small 决议入档、`.btn-processing:disabled` 特异度补齐、计划状态同步）。

测试期望的两处迁移性修改：`scripts/visual-library.test.mjs` 中 grid 规则断言从 App.vue 改读 `styles/components.css`；`scripts/ai-tag-review.test.mjs` 中 `.ai-tag-review-modal` 断言改为 `:deep(...)` 写法、`btn-action` 期望改为 `btn-secondary`。断言强度不变。

4 处轻微可见变化（均为恢复原始意图的缺陷修复，已获计划授权）：AddTagDialog 标签卡片背景（原 `--card-bg` 未定义，静默无背景 → `--panel-solid-bg`）；PreviewDrawer 详情输入框背景（原 `--input-bg` 未定义 → `--control-bg`）；SettingsPage 目录监控"重试"链接颜色（原 `--primary-color` 未定义 → `--accent-color`）；VideoListRow 字幕生成中按钮恢复灰底/降透明度/等待光标（原规则因 scoped 隔离完全失效）。

残留字面色 52 处（原约 90），均已归类：VideoListPage 清理/字幕弹窗的浅色系（暗色主题适配为独立后续项）、单次出现的状态个例色（红色徽章 #dc2626、监控点 #22a06b、AI 审核状态三色椅片、AddTagDialog 红/蓝软底等）、品牌常量（btn-primary 文字色、btn-random 琥珀）、阴影、JS 数据值（AI 标签组默认色 '#0D9488'）。
