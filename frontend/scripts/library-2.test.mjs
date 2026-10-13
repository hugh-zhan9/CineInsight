import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const page = readFileSync(new URL('../src/components/VideoListPage.vue', import.meta.url), 'utf8');
const row = readFileSync(new URL('../src/components/VideoListRow.vue', import.meta.url), 'utf8');
const preview = readFileSync(new URL('../src/components/PreviewDrawer.vue', import.meta.url), 'utf8');
// D-UI-11 把抽屉里的内嵌播放器抽成了 InlineVideoPlayer，起播与进度上报的断言跟着搬到新文件上。
const inlinePlayer = readFileSync(new URL('../src/components/player/InlineVideoPlayer.vue', import.meta.url), 'utf8');
// P-002 把清理审阅面板抽成了独立组件，下面属于面板的断言跟着搬到新文件上。
const cleanupPanel = readFileSync(new URL('../src/components/video-list/CleanupReviewPanel.vue', import.meta.url), 'utf8');
const saveViewDialog = readFileSync(new URL('../src/components/video-list/SaveViewDialog.vue', import.meta.url), 'utf8');
// UI 二期（D-P2-01）把「随机播放一部」抽到 utils/randomPlayback.js 的 randomPlaybackMixin，随机播放请求的断言跟着搬到新文件上。
// 三期（D-P3-03）片库管理拿掉了随机播放、今晚看什么与随机 N 部，只有视频海报墙用它（筛选来自海报墙自己的 currentFilter）。
const randomPlayback = readFileSync(new URL('../src/utils/randomPlayback.js', import.meta.url), 'utf8');
const videoBrowse = readFileSync(new URL('../src/components/viewer/VideoBrowsePage.vue', import.meta.url), 'utf8');

assert.match(page, /SearchLibraryVideoPage/, 'main library should use the stable shared smart-view query');
// D-P3-03：最近播放、继续观看这些观看向的智能视图从片库管理拿掉了（去处在观看侧「视频」），专用的键集接口随之不再用。
assert.doesNotMatch(page, /ListRecentlyPlayedWithFilter|ListContinueWatchingWithFilter|cursorLastPlayedAt|cursorProgressUpdatedAt/, 'library manage no longer pages recently played / continue watching');
assert.match(page, /GetLibrarySubtitleHits\(keyword, videos\.map\(video => video\.id\)\)/, 'subtitle snippets should enrich only the videos handed in, never the whole library');
assert.doesNotMatch(page, /PickRandomVideos|randomPick|TonightPickerDialog|RandomPickBanner/, 'library manage no longer offers random pick, random play or 今晚看什么');
assert.match(page, /ListSavedLibraryViews/, 'saved views should be loaded from the backend');
assert.match(saveViewDialog, /SaveLibraryView\(\{ name, \.\.\.this\.currentLibraryFilter\(\) \}\)/, 'saved views should capture the current filter');
// P-034（D-PC35、LIB-15）：剔除已删除的标签与人物改由后端 FilterActiveTagIDs / FilterActivePersonIDs 判定，
// 与 Jellyfin 同一口径；被剔除的数量提示为「N 个条件已失效」。
assert.match(page, /FilterActiveTagIDs\(storedTagIDs\)/, 'saved views should ignore deleted tag IDs when restored');
assert.match(page, /FilterActivePersonIDs\(storedPersonIDs\)/, 'saved views should ignore deleted people when restored');
assert.match(page, /个条件已失效/, 'saved views should tell how many conditions were dropped');
assert.doesNotMatch(page, /randomPlaybackMixin/, 'the library page no longer plays random');
assert.match(videoBrowse, /mixins: \[[^\]]*randomPlaybackMixin/, 'the video poster wall plays random through the shared random playback mixin');
assert.match(randomPlayback, /PlayRandomVideoWithFilter\(randomPlayRequest\(this\.randomPlayFilter\(\), this\.randomConditions, this\.recentRandomVideoIDs\)\)/, 'random play should send the host filter, conditions and recent picks');
assert.match(randomPlayback, /exclude_ids: normalizeRecentRandomIDs\(recentIDs\)/, 'random play should avoid recent repeats');
assert.match(page, /@watch-progress="handlePreviewWatchProgress"/, 'preview progress should be persisted by the page');
// P-034（D-PC41/42）：看完与续播的判定只在 utils/watchState.js 一处，样例与 Go 测试对齐（watchState.test.js）。
assert.match(page, /from '\.\.\/utils\/watchState\.js'/, 'resume and completion checks should come from the shared watchState helper');
assert.match(page, /return resumePosition\(video\);/, 'near-end positions should restart instead of immediately ending');
assert.doesNotMatch(page, /WATCHED_COMPLETION_TOLERANCE_SECONDS|watchedCompletionTolerance/, 'the old 1-second tolerance copy is gone');
assert.match(row, /resumePosition\(this\.video\)/, 'row progress should use the same resume formula as the drawer');
assert.match(row, /toggle-favorite/, 'library rows should expose favorite state');
assert.match(row, /toggle-watched/, 'library rows should expose watched state');
assert.match(row, /watch_position_seconds/, 'library rows should show resume progress');
assert.match(inlinePlayer, /detailPlaybackStartMs\(/, 'preview should resolve subtitle and resume start times through the tested behavior helper');
assert.doesNotMatch(preview, /^\s+resumePositionSeconds\(\)/m, 'progress persistence must not seek the active player backwards');
assert.doesNotMatch(inlinePlayer, /^\s+resumePositionSeconds\(\)/m, 'progress persistence must not seek the active player backwards');
assert.match(inlinePlayer, /@ended="handleEnded"/, 'natural end has a dedicated playback handler');
assert.match(inlinePlayer, /handleEnded\(\)\s*\{\s*this\.emitWatchProgress\(true, true\)/, 'natural end still reports watch completion');
assert.match(cleanupPanel, /same_source_groups/, 'cleanup review should include same-source candidates');
assert.match(cleanupPanel, /RejectSameSourceRelation/, 'cleanup review should reuse the existing rejection path');
assert.match(cleanupPanel, /byID\.set\(group\.alternative\.id, group\.alternative\)/, 'only the alternative same-source version should be selectable for cleanup');
assert.match(cleanupPanel, /await this\.reanalyzeCleanupCandidates\(\)/, 'cleanup should refresh stale candidates after deletion');

console.log('library 2 tests passed');
