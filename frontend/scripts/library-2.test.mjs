import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const page = readFileSync(new URL('../src/components/VideoListPage.vue', import.meta.url), 'utf8');
const row = readFileSync(new URL('../src/components/VideoListRow.vue', import.meta.url), 'utf8');
const preview = readFileSync(new URL('../src/components/PreviewDrawer.vue', import.meta.url), 'utf8');

assert.match(page, /SearchLibraryVideoPage/, 'main library should use the stable shared smart-view query');
assert.match(page, /ListRecentlyPlayedWithFilter/, 'recently played should be filtered and paginated by the backend');
assert.match(page, /this\.cursorLastPlayedAt,\s+this\.cursorRecentPlayedID,\s+this\.pageSize/, 'recently played should use a stable time-and-ID cursor');
assert.match(page, /GetLibrarySubtitleHits\(keyword, newVideos\.map\(video => video\.id\)\)/, 'subtitle snippets should enrich only the current filtered page');
assert.match(page, /ListSavedLibraryViews/, 'saved views should be loaded from the backend');
assert.match(page, /SaveLibraryView\(\{ name, \.\.\.this\.currentLibraryFilter\(\) \}\)/, 'saved views should capture the current filter');
assert.match(page, /activeTagIDs\.has\(id\)/, 'saved views should ignore deleted tag IDs when restored');
assert.match(page, /PlayRandomVideoWithFilter/, 'random play should use the current filter contract');
assert.match(page, /exclude_ids: this\.recentRandomVideoIDs\.slice\(-12\)/, 'random play should avoid recent repeats');
assert.match(page, /@watch-progress="handlePreviewWatchProgress"/, 'preview progress should be persisted by the page');
assert.match(page, /position >= Math\.max\(duration - 5, duration \* 0\.98\)/, 'completed or near-end positions should restart instead of immediately ending');
assert.match(row, /toggle-favorite/, 'library rows should expose favorite state');
assert.match(row, /toggle-watched/, 'library rows should expose watched state');
assert.match(row, /watch_position_seconds/, 'library rows should show resume progress');
assert.match(preview, /detailPlaybackStartMs\(/, 'preview should resolve subtitle and resume start times through the tested behavior helper');
assert.doesNotMatch(preview, /^\s+resumePositionSeconds\(\)/m, 'progress persistence must not seek the active player backwards');
assert.match(preview, /@ended="emitWatchProgress\(true, true\)"/, 'finishing inline playback should mark completion');
assert.match(page, /same_source_groups/, 'cleanup review should include same-source candidates');
assert.match(page, /RejectSameSourceRelation/, 'cleanup review should reuse the existing rejection path');
assert.match(page, /byID\.set\(group\.alternative\.id, group\.alternative\)/, 'only the alternative same-source version should be selectable for cleanup');
assert.match(page, /await this\.reanalyzeCleanupCandidates\(\)/, 'cleanup should refresh stale candidates after deletion');

console.log('library 2 tests passed');
