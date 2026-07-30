import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const videoListSource = readFileSync(new URL('../src/components/VideoListPage.vue', import.meta.url), 'utf8');
const videoRowSource = readFileSync(new URL('../src/components/VideoListRow.vue', import.meta.url), 'utf8');
const previewSource = readFileSync(new URL('../src/components/PreviewDrawer.vue', import.meta.url), 'utf8');

assert.match(videoListSource, /_subtitleMatchStartMs: segment\.start_time_ms/, 'subtitle results should retain the first hit start time');
assert.match(videoListSource, /:start-time-ms="previewStartTimeMs"/, 'preview drawer should receive the requested subtitle time');
assert.match(videoListSource, /this\.previewStartTimeMs = null/, 'ordinary or closed previews should clear stale seek time');
assert.match(videoListSource, /:subtitle-mode="isSubtitleSearchActive\(\)"/, 'subtitle results should use the virtual list in subtitle mode');
assert.match(videoRowSource, /_subtitleMatchStartMs/, 'subtitle result rows should expose the hit time');
assert.match(previewSource, /startTimeMs/, 'preview drawer should accept a subtitle start time');
assert.match(previewSource, /startTimeMs < 0 \|\| video\.readyState < 1/, 'seek should preserve 0ms and wait for metadata');
assert.match(previewSource, /this\.appliedSeekKey = ''/, 'switching preview sessions should clear the previous seek');
assert.match(previewSource, /video\.currentTime = seekSeconds/, 'preview drawer should seek only after metadata is available');

console.log('subtitle-seek tests passed');
