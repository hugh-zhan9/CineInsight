import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const appSource = readFileSync(new URL('../src/App.vue', import.meta.url), 'utf8');
const videoListSource = readFileSync(new URL('../src/components/VideoListPage.vue', import.meta.url), 'utf8');

assert.match(
  appSource,
  /\.tag-chip\s*{[^}]*height:\s*24px;[^}]*padding:\s*0 8px;[^}]*font-size:\s*11px;/s,
  'tag filter chips should stay compact when tag count grows'
);
assert.match(
  appSource,
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
