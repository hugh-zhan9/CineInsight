import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const appSource = readFileSync(new URL('../src/App.vue', import.meta.url), 'utf8');
const videoListSource = readFileSync(new URL('../src/components/VideoListPage.vue', import.meta.url), 'utf8');
const mainSource = readFileSync(new URL('../src/main.js', import.meta.url), 'utf8');
const videoRowSource = readFileSync(new URL('../src/components/VideoListRow.vue', import.meta.url), 'utf8');
const shortFeedCss = readFileSync(new URL('../src/short-feed/short-feed.css', import.meta.url), 'utf8');
const shortFeedSource = readFileSync(new URL('../src/short-feed/ShortFeedApp.vue', import.meta.url), 'utf8');
const shortFeedMain = readFileSync(new URL('../src/short-feed/main.js', import.meta.url), 'utf8');
const settingsSource = readFileSync(new URL('../src/components/SettingsPage.vue', import.meta.url), 'utf8');
const componentCss = readFileSync(new URL('../src/styles/components.css', import.meta.url), 'utf8');

assert.match(mainSource, /styles\/tokens\.css/, 'desktop entry should load shared design tokens');
assert.match(appSource, /class="app-shell glass-app-shell"/, 'app shell should use the shared glass shell treatment');
assert.doesNotMatch(videoListSource, /<ActionMenu\b/, 'video list toolbar should not hide primary management actions in a more menu');
assert.doesNotMatch(videoListSource, /ui\/ActionMenu\.vue/, 'video list should not keep the unused more-menu primitive for primary actions');
assert.match(videoListSource, /toolbar-primary/, 'video list should split primary toolbar controls from secondary actions');
assert.match(videoListSource, /selection-toolbar/, 'batch actions should live in a contextual selection toolbar');
assert.doesNotMatch(videoListSource, /<ActionMenu label="更多">[\s\S]*AI 标签管理[\s\S]*<\/ActionMenu>/, 'AI tag management should not be hidden in the more menu');
assert.match(videoListSource, /<button[^>]+@click="openAITagReviewDialog\(\)"[^>]*>AI 标签管理<\/button>/, 'AI tag management should be a direct toolbar action');
assert.match(videoListSource, /<button[^>]+@click="openCleanupDialog\(\)"[^>]*>清理候选<\/button>/, 'cleanup candidates should be a direct toolbar action');
assert.match(videoListSource, /<button[^>]+@click="showTagManagerDialog = true"[^>]*>标签管理<\/button>/, 'tag manager should be a direct toolbar action');
assert.match(videoRowSource, /row-primary-actions/, 'video rows should keep only primary actions in the always-visible rail');
assert.match(videoRowSource, /row-secondary-actions/, 'video rows should group secondary actions separately');
assert.match(shortFeedCss, /--short-glass-bg:\s*var\(--glass-strong-bg\)/, 'short feed should consume shared glass tokens');
assert.match(shortFeedMain, /styles\/tokens\.css/, 'short feed entry should load shared design tokens');
assert.doesNotMatch(shortFeedSource, /🔇|🔊|🗑/, 'short feed controls should avoid emoji action labels');
assert.doesNotMatch(shortFeedSource, />\s*(Fav|Mute|Sound|Like|Save|Del)\s*</, 'short feed controls should use compact icons instead of text action labels');
assert.match(shortFeedSource, /class="action-icon action-icon--heart"/, 'short feed like action should render as a heart icon');
assert.match(shortFeedSource, /class="action-icon action-icon--bookmark"/, 'short feed favorite action should render as a bookmark icon');
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
assert.match(videoListSource, /cleanup-section-title/, 'cleanup sections should use visible category headers');
assert.match(videoListSource, /toggleCleanupSelection\(group\.original\?\.id\)/, 'cleanup duplicate original row should be selectable');
assert.match(videoListSource, /@click="previewCleanupVideo\(/, 'cleanup candidates should expose preview actions');
assert.match(videoListSource, /cleanup-item-actions/, 'cleanup candidate rows should reserve an actions area');
assert.match(videoListSource, /短视频：时长 < 5 秒/, 'cleanup dialog should explain the short-video threshold');
assert.match(videoListSource, /低清视频：分辨率低于 480x320/, 'cleanup dialog should explain the low-resolution threshold');
assert.match(videoListSource, /低清视频[\s\S]*短视频/, 'low-resolution section should appear before short-video section');
assert.match(videoListSource, /GetPreviewSession/, 'cleanup preview should validate file availability before opening');
assert.match(videoListSource, /StartCleanupAnalysis/, 'cleanup analysis should start as a background task');
assert.match(videoListSource, /GetCleanupStatus/, 'cleanup dialog should reopen from background status');
assert.match(videoListSource, /@click="reanalyzeCleanupCandidates"/, 'cleanup reanalysis should bypass completed background status');
assert.match(videoListSource, /后台继续分析/, 'cleanup dialog should allow closing while analysis continues');

console.log('video-list-ui tests passed');
