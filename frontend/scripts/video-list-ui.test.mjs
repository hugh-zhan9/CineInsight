import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const appSource = readFileSync(new URL('../src/App.vue', import.meta.url), 'utf8');
const videoListSource = readFileSync(new URL('../src/components/VideoListPage.vue', import.meta.url), 'utf8');
const mainSource = readFileSync(new URL('../src/main.js', import.meta.url), 'utf8');
const videoRowSource = readFileSync(new URL('../src/components/VideoListRow.vue', import.meta.url), 'utf8');
const shortFeedCss = readFileSync(new URL('../src/short-feed/short-feed.css', import.meta.url), 'utf8');
const shortFeedSource = readFileSync(new URL('../src/short-feed/ShortFeedApp.vue', import.meta.url), 'utf8');
const shortFeedActionRail = readFileSync(new URL('../src/short-feed/components/FeedActionRail.vue', import.meta.url), 'utf8');
const shortFeedMain = readFileSync(new URL('../src/short-feed/main.js', import.meta.url), 'utf8');
const settingsSource = readFileSync(new URL('../src/components/SettingsPage.vue', import.meta.url), 'utf8');
const scanDirectoriesSource = readFileSync(new URL('../src/components/settings/ScanDirectoriesSection.vue', import.meta.url), 'utf8');
// P-002 把清理审阅面板抽成了独立组件，下面属于面板的断言跟着搬到新文件上。
const cleanupPanelSource = readFileSync(new URL('../src/components/video-list/CleanupReviewPanel.vue', import.meta.url), 'utf8');
const taskBarsSource = readFileSync(new URL('../src/components/video-list/BackgroundTaskStatusBars.vue', import.meta.url), 'utf8');
// 吸顶工具栏（含结果条）与批量操作栏也各自成组件了。
const toolbarSource = readFileSync(new URL('../src/components/video-list/LibraryToolbar.vue', import.meta.url), 'utf8');
const batchBarSource = readFileSync(new URL('../src/components/video-list/BatchActionBar.vue', import.meta.url), 'utf8');
const incrementalScanSource = readFileSync(new URL('../src/components/video-list/IncrementalScanBar.vue', import.meta.url), 'utf8');
const componentCss = readFileSync(new URL('../src/styles/components.css', import.meta.url), 'utf8');

assert.match(mainSource, /styles\/tokens\.css/, 'desktop entry should load shared design tokens');
assert.match(appSource, /class="app-shell glass-app-shell"/, 'app shell should use the shared glass shell treatment');
// 2026-09-01 桌面端重构原型反转了此前"管理动作不得藏进更多菜单"的结论：
// 25 个平铺控件里真正跟着查询走的只有前 9 个，12 个库维护动作改为收进「管理」
// 菜单分四组。下面钉的是新结构，以及"收进去之后一个都不能少"。
assert.match(toolbarSource, /class="toolbar-row"/, 'toolbar should have a query row of always-on controls');
assert.match(toolbarSource, /class="toolbar-row toolbar-row--tags"/, 'tag filtering and selection should live on their own row');
assert.match(toolbarSource, /class="segmented"/, 'search mode and layout should use segmented controls');
assert.match(toolbarSource, /class="split-btn"/, 'random play should be one split button, not a select plus two buttons');
assert.match(toolbarSource, /toggleToolbarMenu\('filter', 'filterTrigger'\)/, 'range conditions should collapse into a filter popover');
assert.match(toolbarSource, /toggleToolbarMenu\('manage', 'manageTrigger'\)/, 'library maintenance should collapse into a manage menu');

// 管理菜单的四个分组和 12 个动作一个都不能丢。
for (const heading of ['扫描', '整理', '补全', '维护']) {
  assert.match(toolbarSource, new RegExp(`heading: '${heading}'`), `manage menu should keep the ${heading} group`);
}
for (const id of [
  'scan-new', 'scan-incremental', 'move-folder', 'rename-folder', 'export-nfo',
  'backfill-technical', 'backfill-phash', 'backfill-local-metadata',
  'ai-tags', 'tag-manager', 'cleanup', 'trash'
]) {
  assert.match(toolbarSource, new RegExp(`id: '${id}'`), `manage menu should keep the ${id} action`);
}
assert.match(videoListSource, /case 'ai-tags': this\.openAITagReviewDialog\(\)/, 'manage menu should still open AI tag review');
assert.match(videoListSource, /case 'cleanup': this\.openCleanupDialog\(\)/, 'manage menu should still open cleanup review');
assert.match(videoListSource, /case 'trash': this\.openTrashDialog\(\)/, 'manage menu should still open the trash');
assert.match(toolbarSource, /aiTagSummary\.same_source_unread/, 'AI tag management should expose unread same-source relations');
assert.match(videoListSource, /GetAITaggingStatusSummary/, 'same-source unread badge should refresh from the backend summary');
// 徽标从按钮搬到了「管理」按钮上，但仍要能一眼看到有待办。
assert.match(toolbarSource, /manageAttentionCount\(\)/, 'the manage button should carry a combined attention badge');
assert.match(toolbarSource, /待审阅 \$\{this\.cleanupBadgeCount\} 项/, 'a completed background analysis should still surface its count');

// 运行中的补全任务把进度和取消让给了常驻状态条——按钮进了菜单，
// 进度不能跟着一起藏起来，否则关掉菜单就看不到还在跑什么。
assert.match(taskBarsSource, /技术信息 \{\{ technicalBackfill\.processed \}\}\/\{\{ technicalBackfill\.total \}\}/, 'running backfill progress should stay visible in the status banner');
assert.match(taskBarsSource, /@click="cancelTechnicalBackfill"/, 'the status banner should carry the cancel action');
assert.match(taskBarsSource, /@click="cancelPerceptualHashBackfill"/, 'the status banner should carry the phash cancel action');
assert.match(taskBarsSource, /@click="cancelLocalMetadataExport"/, 'the status banner should carry the NFO export cancel action');

// 结果条是新工具栏的回显机制：条件折叠进浮层后，靠它让用户知道还开着什么。
assert.match(toolbarSource, /class="result-bar"/, 'a result bar should echo the active conditions');
assert.match(toolbarSource, /activeConditionLabels/, 'the result bar should spell out active conditions in Chinese');
assert.match(videoListSource, /CountLibraryVideos/, 'the result bar count should come from the backend filter count');
assert.match(toolbarSource, /selection-toolbar/, 'batch actions should live in a contextual selection toolbar');
assert.match(videoListSource, /@click="runIncrementalScan"|case 'scan-incremental': this\.runIncrementalScan\(\)/, 'video list should expose a manual incremental scan action');
assert.match(incrementalScanSource, /SyncScanDirectories/, 'manual incremental scan should reuse the backend sync API');
assert.match(incrementalScanSource, /增量扫描完成：\$\{summary\.join\('，'\)\}/, 'manual incremental scan should report its result counts');
assert.match(incrementalScanSource, /scan-sync-status--\$\{incrementalScan\.state\}/, 'manual incremental scan should expose success, warning, and error states');
// 行内 11 个动作 2026-09-01 起降到常驻 4 个（预览·播放·收藏·已看）加一个 ⋯，
// 七个次级动作与右键菜单共用同一份定义。四个常驻动作不做"悬停才出现"。
assert.match(videoRowSource, /class="video-actions"/, 'video rows keep an always-visible action rail');
assert.match(videoRowSource, /@click="\$emit\('open-row-menu', video, \$event\.currentTarget\)"/, 'secondary row actions collapse into a shared menu');
assert.doesNotMatch(videoRowSource, /row-secondary-actions/, 'the second row of action buttons is gone');
for (const label of ['预览', '播放', '已看']) {
  assert.match(videoRowSource, new RegExp(`>${label}<`), `${label} must stay an always-visible row action`);
}
// 七个次级动作在页面级菜单里一个都不能少，删除仍走既有确认与回收站路径。
for (const id of ['directory', 'rename', 'move', 'export-nfo', 'subtitle', 'subtitle-edit', 'subtitle-preview', 'enhance', 'delete']) {
  assert.match(videoListSource, new RegExp(`id: '${id}'`), `row menu should keep the ${id} action`);
}
assert.match(videoListSource, /case 'delete': this\.confirmDelete\(video\)/, 'row menu delete must still go through the confirm + trash path');
// ⋯ 与右键是同一份定义、两个入口：右键只是换了定位方式。
assert.match(videoListSource, /this\.rowMenu = \{ video, anchor: null, position: \{ x: event\.clientX, y: event\.clientY \} \}/, 'right-click reuses the same menu definition');
assert.doesNotMatch(videoListSource, /class="context-menu"/, 'the bespoke context menu markup is gone');
assert.match(shortFeedCss, /--short-glass-bg:\s*var\(--glass-strong-bg\)/, 'short feed should consume shared glass tokens');
assert.match(shortFeedMain, /styles\/tokens\.css/, 'short feed entry should load shared design tokens');
assert.doesNotMatch(shortFeedSource, /🔇|🔊|🗑/, 'short feed controls should avoid emoji action labels');
assert.doesNotMatch(shortFeedSource, />\s*(Fav|Mute|Sound|Like|Save|Del)\s*</, 'short feed controls should use compact icons instead of text action labels');
// 动作按钮已拆到 FeedActionRail.vue，断言跟着搬过去。
assert.match(shortFeedActionRail, /class="action-icon action-icon--heart"/, 'short feed like action should render as a heart icon');
assert.match(shortFeedActionRail, /class="action-icon action-icon--bookmark"/, 'short feed favorite action should render as a bookmark icon');
assert.match(shortFeedCss, /\.feed-stage::after/, 'short feed should use a subtle readable overlay instead of boxed panels');
assert.match(shortFeedCss, /\.progress-dock\s*{[^}]*height:\s*3px;/s, 'short feed progress should be a minimal bottom bar');
assert.match(settingsSource, /class="settings-grid-shell"/, 'settings page should use a compact grouped layout shell');
assert.match(scanDirectoriesSource, /class="directories-list"/, 'settings directories should use class-based layout instead of inline layout');

assert.match(
  componentCss,
  /\.tag-chip\s*{[^}]*height:\s*24px;[^}]*padding:\s*0 8px;[^}]*font-size:\s*11px;/s,
  'tag filter chips should stay compact when tag count grows'
);
assert.match(
  componentCss,
  /\.tag-chip-wrap\s*{[^}]*max-width:\s*160px;/s,
  'tag filter chips should have a tighter max width'
);

assert.match(cleanupPanelSource, /cleanup-modal-header/, 'cleanup modal should have a fixed header area');
assert.match(cleanupPanelSource, /cleanup-modal-body/, 'cleanup modal should have a dedicated scroll body');
assert.match(cleanupPanelSource, /cleanup-modal-footer/, 'cleanup modal should keep actions visible at the bottom');
// 顶层改成按目录分组：目录标题可折叠，组内类别用 cleanup-card-kind 标注。
// 2026-09-01 起目录分组从内联折叠改成左侧栏 + 右侧候选流（原型 A5），
// 折叠开关随之消失，但"按目录分组"这件事本身保留。
assert.match(cleanupPanelSource, /data-test="cleanup-dir-section"/, 'cleanup candidates should be grouped by directory');
assert.match(cleanupPanelSource, /class="cleanup-split"/, 'cleanup should use a directory sidebar plus a candidate stream');
assert.match(cleanupPanelSource, /activeCleanupDirectory/, 'the sidebar should drive which directory the stream shows');
assert.match(cleanupPanelSource, /data-test="cleanup-category"/, 'cleanup should offer a category filter with counts');
// 三条安全边界不能被重排破坏。
assert.match(cleanupPanelSource, /默认不勾选任何一项/, 'the zero-selected-by-default boundary must stay stated in the UI');
assert.match(cleanupPanelSource, /移入回收站可撤销，不会立即删除磁盘文件/, 'the footer must keep saying the delete is undoable');
assert.match(cleanupPanelSource, /:disabled="cleanupSelection\.length === 0/, 'the trash button stays disabled while nothing is selected');
assert.match(cleanupPanelSource, /RejectSameSourceRelation/, 'same-source rejection must still be reachable');
assert.match(cleanupPanelSource, /cleanup-card-kind/, 'cleanup cards should label their candidate category');
assert.match(cleanupPanelSource, /toggleCleanupSelection\(entry\.keeper\?\.id\)/, 'cleanup duplicate original row should be selectable');
assert.match(cleanupPanelSource, /@click="previewCleanupVideo\(/, 'cleanup candidates should expose preview actions');
assert.match(cleanupPanelSource, /cleanup-item-actions/, 'cleanup candidate rows should reserve an actions area');
// 判定阈值从顶部一整段说明挪到了各个类别自己的 tooltip 上——
// 「低清到底指多低」这个疑问产生在类别上，说明就该在那里。
assert.match(cleanupPanelSource, /短视频：时长 < 5 秒/, 'cleanup should still explain the short-video threshold');
assert.match(cleanupPanelSource, /低清视频：分辨率低于 480x320/, 'cleanup should still explain the low-resolution threshold');
assert.match(cleanupPanelSource, /:title="option\.hint"/, 'the thresholds should ride on the category chips');
assert.match(cleanupPanelSource, /低清视频：分辨率低于 480x320/, 'cleanup dialog should explain the low-resolution threshold');
assert.match(cleanupPanelSource, /近似重复（不同转码，不会默认选中）/, 'near-duplicate groups should state that they are not selected by default');
assert.match(cleanupPanelSource, /near_duplicate_groups/, 'cleanup dialog should render perceptual-hash near-duplicate groups');
assert.match(cleanupPanelSource, /低清视频[\s\S]*短视频/, 'low-resolution section should appear before short-video section');
assert.match(videoListSource, /GetPreviewSession/, 'cleanup preview should validate file availability before opening');
assert.match(cleanupPanelSource, /StartCleanupAnalysis/, 'cleanup analysis should start as a background task');
assert.match(cleanupPanelSource, /GetCleanupStatus/, 'cleanup dialog should reopen from background status');
assert.match(cleanupPanelSource, /@click="reanalyzeCleanupCandidates"/, 'cleanup reanalysis should bypass completed background status');
assert.match(cleanupPanelSource, /后台继续分析/, 'cleanup dialog should allow closing while analysis continues');

// 批量操作栏必须随吸顶工具栏一起常驻：选中项后向下滚动时若按钮被滚走，
// 用户就得滚回顶部才能操作。用「selection-toolbar 出现在 .toolbar 闭合之前」
// 来钉住它的嵌套位置，避免被挪回流内。
// 非贪婪正则会停在工具栏自己的闭合上，改用两个稳定锚点之间的切片。
const chromeStart = toolbarSource.indexOf('<div class="library-chrome">');
const chromeEnd = toolbarSource.indexOf('<!-- 筛选浮层');
assert.ok(chromeStart > 0 && chromeEnd > chromeStart, 'sticky library chrome block should be locatable');
const chromeBlock = toolbarSource.slice(chromeStart, chromeEnd);
assert.match(
  chromeBlock,
  /<BatchActionBar/,
  'batch action bar must live inside the sticky chrome so it stays visible while scrolling'
);
assert.match(
  batchBarSource,
  /class="selection-toolbar"/,
  'the batch action bar is the selection toolbar'
);
assert.match(
  chromeBlock,
  /class="result-bar"/,
  'the result bar shares the sticky slot with the batch bar'
);
assert.match(
  toolbarSource,
  /\.library-chrome\s*{[^}]*position:\s*sticky;/s,
  'the chrome that hosts the batch action bar must stay sticky'
);

// 网格卡：信息区下内边距为 0，动作行必须自己撑开间距，否则标签行和按钮行贴在一起。
const componentsCss = readFileSync(new URL('../src/styles/components.css', import.meta.url), 'utf8');
const gridActions = componentsCss.match(/\.video-item--grid \.video-actions \{([^}]*)\}/);
assert.ok(gridActions, 'grid cards should style their action row');
const gridActionsPadding = gridActions[1].match(/padding:\s*([^;]+);/);
assert.ok(gridActionsPadding, 'grid action row should declare padding');
assert.ok(
  !/^0(px)?\s/.test(gridActionsPadding[1].trim()),
  'grid action row needs top padding so tags do not touch the buttons'
);

// 行内元信息：窄卡片里被压扁会让"评分 8/10"竖着断成两行、"看到 06:29 / 36:28"断成四行。
// 放不下要整项换行，不在项内部拆字，换行之间还得有行距。
const metaRule = componentsCss.match(/\.video-meta \{([^}]*)\}/);
assert.ok(metaRule, 'video meta row should be styled');
assert.match(metaRule[1], /flex-wrap:\s*wrap/, 'meta items should wrap instead of being squeezed');
const metaGap = metaRule[1].match(/gap:\s*([^;]+);/);
assert.ok(metaGap && metaGap[1].trim().split(/\s+/)[0] !== '0', 'wrapped meta rows need a row gap');
assert.match(componentsCss, /\.video-meta > \* \{[^}]*white-space:\s*nowrap/s, 'each meta item must not break inside');

// 详情抽屉那排动作按钮曾经完全没有样式：靠行内空白撑间隙，换行后两行贴死。
const drawerSource = readFileSync(new URL('../src/components/PreviewDrawer.vue', import.meta.url), 'utf8');
const inlineActions = drawerSource.match(/\.detail-inline-actions \{([^}]*)\}/);
assert.ok(inlineActions, 'the drawer action row must actually be styled');
assert.match(inlineActions[1], /flex-wrap:\s*wrap/, 'drawer actions should wrap');
assert.match(inlineActions[1], /gap:\s*(?!0)/, 'wrapped drawer actions need a gap');

// <details> 的内容在 WebKit 里走 shadow slot：在 details 上写 display:grid 只作用到
// summary 与整块内容之间，内容里的控件不是网格项，间距落不到它们身上。
// 展开区必须自己是布局容器。
const detailsBlocks = [...drawerSource.matchAll(/<details class="([^"]+)">([\s\S]*?)<\/details>/g)];
for (const [, className, inner] of detailsBlocks) {
  const controls = (inner.match(/<(input|button|select|textarea)\b/g) || []).length;
  if (controls < 2) continue;
  const wrapped = /<div class="[^"]*__fields[^"]*"/.test(inner);
  assert.ok(
    wrapped,
    `<details class="${className}"> 里有多个控件，必须包一层自己的布局容器，否则间距无效`
  );
}

// IINA 断点同步完要当场刷新列表，否则"继续观看"得等下次重启才更新。
assert.match(videoListSource, /registerRuntimeEvent\('iina-progress-synced'/, 'video list should react to IINA progress sync');
// 但必须是就地更新：整表重载会按当前排序重新打分，刚看完的视频会跳位置。
const iinaHandler = videoListSource.match(/registerRuntimeEvent\('iina-progress-synced'[\s\S]{0,240}?\}\);/);
assert.ok(iinaHandler, 'IINA sync handler should exist');
assert.doesNotMatch(iinaHandler[0], /reloadCurrentView/, 'IINA sync must not reload the whole list');
assert.match(iinaHandler[0], /applyWatchProgressUpdates/, 'IINA sync should patch affected rows in place');

console.log('video-list-ui tests passed');
