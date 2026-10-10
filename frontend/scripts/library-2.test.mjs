import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const page = readFileSync(new URL('../src/components/VideoListPage.vue', import.meta.url), 'utf8');
const row = readFileSync(new URL('../src/components/VideoListRow.vue', import.meta.url), 'utf8');
const preview = readFileSync(new URL('../src/components/PreviewDrawer.vue', import.meta.url), 'utf8');
// P-002 把清理审阅面板抽成了独立组件，下面属于面板的断言跟着搬到新文件上。
const cleanupPanel = readFileSync(new URL('../src/components/video-list/CleanupReviewPanel.vue', import.meta.url), 'utf8');
const saveViewDialog = readFileSync(new URL('../src/components/video-list/SaveViewDialog.vue', import.meta.url), 'utf8');

assert.match(page, /SearchLibraryVideoPage/, 'main library should use the stable shared smart-view query');
assert.match(page, /ListRecentlyPlayedWithFilter/, 'recently played should be filtered and paginated by the backend');
// 条数由调用方给：触底加载用 pageSize，同条件原地刷新按已加载深度分批取。
assert.match(page, /this\.cursorLastPlayedAt,\s+this\.cursorRecentPlayedID,\s+limit/, 'recently played should use a stable time-and-ID cursor');
assert.match(page, /GetLibrarySubtitleHits\(keyword, videos\.map\(video => video\.id\)\)/, 'subtitle snippets should enrich only the videos handed in, never the whole library');
assert.match(page, /PickRandomVideos\(\{[\s\S]*?filter: this\.currentLibraryFilter\(\)/, 'random pick should reuse the same filter contract as random play');
assert.match(page, /if \(this\.randomPick\.active\) return this\.refreshRandomPick\(\)/, 'reloads inside a random batch should refresh the fixed batch, not re-draw one');
assert.match(page, /ListSavedLibraryViews/, 'saved views should be loaded from the backend');
assert.match(saveViewDialog, /SaveLibraryView\(\{ name, \.\.\.this\.currentLibraryFilter\(\) \}\)/, 'saved views should capture the current filter');
// P-034（D-PC35、LIB-15）：剔除已删除的标签与人物改由后端 FilterActiveTagIDs / FilterActivePersonIDs 判定，
// 与 Jellyfin 同一口径；被剔除的数量提示为「N 个条件已失效」。
assert.match(page, /FilterActiveTagIDs\(storedTagIDs\)/, 'saved views should ignore deleted tag IDs when restored');
assert.match(page, /FilterActivePersonIDs\(storedPersonIDs\)/, 'saved views should ignore deleted people when restored');
assert.match(page, /个条件已失效/, 'saved views should tell how many conditions were dropped');
assert.match(page, /PlayRandomVideoWithFilter/, 'random play should use the current filter contract');
assert.match(page, /exclude_ids: this\.recentRandomVideoIDs\.slice\(-12\)/, 'random play should avoid recent repeats');
assert.match(page, /@watch-progress="handlePreviewWatchProgress"/, 'preview progress should be persisted by the page');
// P-034（D-PC41/42）：看完与续播的判定只在 utils/watchState.js 一处，样例与 Go 测试对齐（watchState.test.js）。
assert.match(page, /from '\.\.\/utils\/watchState\.js'/, 'resume and completion checks should come from the shared watchState helper');
assert.match(page, /return resumePosition\(video\);/, 'near-end positions should restart instead of immediately ending');
assert.doesNotMatch(page, /WATCHED_COMPLETION_TOLERANCE_SECONDS|watchedCompletionTolerance/, 'the old 1-second tolerance copy is gone');
assert.match(row, /resumePosition\(this\.video\)/, 'row progress should use the same resume formula as the drawer');
assert.match(row, /toggle-favorite/, 'library rows should expose favorite state');
assert.match(row, /toggle-watched/, 'library rows should expose watched state');
assert.match(row, /watch_position_seconds/, 'library rows should show resume progress');
assert.match(preview, /detailPlaybackStartMs\(/, 'preview should resolve subtitle and resume start times through the tested behavior helper');
assert.doesNotMatch(preview, /^\s+resumePositionSeconds\(\)/m, 'progress persistence must not seek the active player backwards');
assert.match(preview, /@ended="handleEnded"/, 'natural end has a dedicated playback handler');
assert.match(preview, /handleEnded\(\)\s*\{\s*this\.emitWatchProgress\(true, true\)/, 'natural end still reports watch completion');
assert.match(cleanupPanel, /same_source_groups/, 'cleanup review should include same-source candidates');
assert.match(cleanupPanel, /RejectSameSourceRelation/, 'cleanup review should reuse the existing rejection path');
assert.match(cleanupPanel, /byID\.set\(group\.alternative\.id, group\.alternative\)/, 'only the alternative same-source version should be selectable for cleanup');
assert.match(cleanupPanel, /await this\.reanalyzeCleanupCandidates\(\)/, 'cleanup should refresh stale candidates after deletion');

console.log('library 2 tests passed');
