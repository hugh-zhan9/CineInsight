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
const componentCss = readFileSync(new URL('../src/styles/components.css', import.meta.url), 'utf8');

assert.match(mainSource, /styles\/tokens\.css/, 'desktop entry should load shared design tokens');
assert.match(appSource, /class="app-shell glass-app-shell"/, 'app shell should use the shared glass shell treatment');
// 2026-09-01 桌面端重构原型反转了此前"管理动作不得藏进更多菜单"的结论：
// 25 个平铺控件里真正跟着查询走的只有前 9 个，12 个库维护动作改为收进「管理」
// 菜单分四组。下面钉的是新结构，以及"收进去之后一个都不能少"。
assert.match(videoListSource, /class="toolbar-row"/, 'toolbar should have a query row of always-on controls');
assert.match(videoListSource, /class="toolbar-row toolbar-row--tags"/, 'tag filtering and selection should live on their own row');
assert.match(videoListSource, /class="segmented"/, 'search mode and layout should use segmented controls');
assert.match(videoListSource, /class="split-btn"/, 'random play should be one split button, not a select plus two buttons');
assert.match(videoListSource, /toggleToolbarMenu\('filter', 'filterTrigger'\)/, 'range conditions should collapse into a filter popover');
assert.match(videoListSource, /toggleToolbarMenu\('manage', 'manageTrigger'\)/, 'library maintenance should collapse into a manage menu');

// 管理菜单的四个分组和 12 个动作一个都不能丢。
for (const heading of ['扫描', '整理', '补全', '维护']) {
  assert.match(videoListSource, new RegExp(`heading: '${heading}'`), `manage menu should keep the ${heading} group`);
}
for (const id of [
  'scan-new', 'scan-incremental', 'move-folder', 'rename-folder', 'export-nfo',
  'backfill-technical', 'backfill-phash', 'backfill-local-metadata',
  'ai-tags', 'tag-manager', 'cleanup', 'trash'
]) {
  assert.match(videoListSource, new RegExp(`id: '${id}'`), `manage menu should keep the ${id} action`);
}
assert.match(videoListSource, /case 'ai-tags': this\.openAITagReviewDialog\(\)/, 'manage menu should still open AI tag review');
assert.match(videoListSource, /case 'cleanup': this\.openCleanupDialog\(\)/, 'manage menu should still open cleanup review');
assert.match(videoListSource, /case 'trash': this\.openTrashDialog\(\)/, 'manage menu should still open the trash');
assert.match(videoListSource, /aiTagSummary\.same_source_unread/, 'AI tag management should expose unread same-source relations');
assert.match(videoListSource, /GetAITaggingStatusSummary/, 'same-source unread badge should refresh from the backend summary');
// 徽标从按钮搬到了「管理」按钮上，但仍要能一眼看到有待办。
assert.match(videoListSource, /manageAttentionCount\(\)/, 'the manage button should carry a combined attention badge');
assert.match(videoListSource, /待审阅 \$\{this\.cleanupBadgeCount\} 项/, 'a completed background analysis should still surface its count');

// 运行中的补全任务把进度和取消让给了常驻状态条——按钮进了菜单，
// 进度不能跟着一起藏起来，否则关掉菜单就看不到还在跑什么。
assert.match(videoListSource, /技术信息 \{\{ technicalBackfill\.processed \}\}\/\{\{ technicalBackfill\.total \}\}/, 'running backfill progress should stay visible in the status banner');
assert.match(videoListSource, /@click="cancelTechnicalBackfill"/, 'the status banner should carry the cancel action');
assert.match(videoListSource, /@click="cancelPerceptualHashBackfill"/, 'the status banner should carry the phash cancel action');
assert.match(videoListSource, /@click="cancelLocalMetadataExport"/, 'the status banner should carry the NFO export cancel action');

// 结果条是新工具栏的回显机制：条件折叠进浮层后，靠它让用户知道还开着什么。
assert.match(videoListSource, /class="result-bar"/, 'a result bar should echo the active conditions');
assert.match(videoListSource, /activeConditionLabels/, 'the result bar should spell out active conditions in Chinese');
assert.match(videoListSource, /CountLibraryVideos/, 'the result bar count should come from the backend filter count');
assert.match(videoListSource, /selection-toolbar/, 'batch actions should live in a contextual selection toolbar');
assert.match(videoListSource, /@click="runIncrementalScan"|case 'scan-incremental': this\.runIncrementalScan\(\)/, 'video list should expose a manual incremental scan action');
assert.match(videoListSource, /SyncScanDirectories/, 'manual incremental scan should reuse the backend sync API');
assert.match(videoListSource, /增量扫描完成：\$\{summary\.join\('，'\)\}/, 'manual incremental scan should report its result counts');
assert.match(videoListSource, /scan-sync-status--\$\{incrementalScan\.state\}/, 'manual incremental scan should expose success, warning, and error states');
assert.match(videoRowSource, /row-primary-actions/, 'video rows should keep only primary actions in the always-visible rail');
assert.match(videoRowSource, /row-secondary-actions/, 'video rows should group secondary actions separately');
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
assert.match(settingsSource, /class="directories-list"/, 'settings directories should use class-based layout instead of inline layout');

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

assert.match(videoListSource, /cleanup-modal-header/, 'cleanup modal should have a fixed header area');
assert.match(videoListSource, /cleanup-modal-body/, 'cleanup modal should have a dedicated scroll body');
assert.match(videoListSource, /cleanup-modal-footer/, 'cleanup modal should keep actions visible at the bottom');
// 顶层改成按目录分组：目录标题可折叠，组内类别用 cleanup-card-kind 标注。
assert.match(videoListSource, /data-test="cleanup-dir-section"/, 'cleanup candidates should be grouped by directory');
assert.match(videoListSource, /@click="toggleCleanupDir\(section\.directory\)"/, 'cleanup directory headers should collapse');
assert.match(videoListSource, /cleanup-card-kind/, 'cleanup cards should label their candidate category');
assert.match(videoListSource, /toggleCleanupSelection\(entry\.keeper\?\.id\)/, 'cleanup duplicate original row should be selectable');
assert.match(videoListSource, /@click="previewCleanupVideo\(/, 'cleanup candidates should expose preview actions');
assert.match(videoListSource, /cleanup-item-actions/, 'cleanup candidate rows should reserve an actions area');
assert.match(videoListSource, /短视频：时长 < 5 秒/, 'cleanup dialog should explain the short-video threshold');
assert.match(videoListSource, /低清视频：分辨率低于 480x320/, 'cleanup dialog should explain the low-resolution threshold');
assert.match(videoListSource, /近似重复（不同转码，不会默认选中）/, 'near-duplicate groups should state that they are not selected by default');
assert.match(videoListSource, /near_duplicate_groups/, 'cleanup dialog should render perceptual-hash near-duplicate groups');
assert.match(videoListSource, /低清视频[\s\S]*短视频/, 'low-resolution section should appear before short-video section');
assert.match(videoListSource, /GetPreviewSession/, 'cleanup preview should validate file availability before opening');
assert.match(videoListSource, /StartCleanupAnalysis/, 'cleanup analysis should start as a background task');
assert.match(videoListSource, /GetCleanupStatus/, 'cleanup dialog should reopen from background status');
assert.match(videoListSource, /@click="reanalyzeCleanupCandidates"/, 'cleanup reanalysis should bypass completed background status');
assert.match(videoListSource, /后台继续分析/, 'cleanup dialog should allow closing while analysis continues');

// 批量操作栏必须随吸顶工具栏一起常驻：选中项后向下滚动时若按钮被滚走，
// 用户就得滚回顶部才能操作。用「selection-toolbar 出现在 .toolbar 闭合之前」
// 来钉住它的嵌套位置，避免被挪回流内。
// 非贪婪正则会停在工具栏自己的闭合上，改用两个稳定锚点之间的切片。
const chromeStart = videoListSource.indexOf('<div class="library-chrome">');
const chromeEnd = videoListSource.indexOf('v-if="incrementalScan.message"');
assert.ok(chromeStart > 0 && chromeEnd > chromeStart, 'sticky library chrome block should be locatable');
const chromeBlock = videoListSource.slice(chromeStart, chromeEnd);
assert.match(
  chromeBlock,
  /class="selection-toolbar"/,
  'batch action bar must live inside the sticky chrome so it stays visible while scrolling'
);
assert.match(
  chromeBlock,
  /class="result-bar"/,
  'the result bar shares the sticky slot with the batch bar'
);
assert.match(
  videoListSource,
  /\.library-chrome\s*{[^}]*position:\s*sticky;/s,
  'the chrome that hosts the batch action bar must stay sticky'
);

console.log('video-list-ui tests passed');
