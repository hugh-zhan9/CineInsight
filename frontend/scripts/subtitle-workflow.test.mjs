import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const videoListSource = readFileSync(new URL('../src/components/VideoListPage.vue', import.meta.url), 'utf8');
const settingsSource = readFileSync(new URL('../src/components/SettingsPage.vue', import.meta.url), 'utf8');
const bindingsSource = readFileSync(new URL('../wailsjs/go/main/App.js', import.meta.url), 'utf8');

assert.match(videoListSource, /GetSubtitleQueueState/);
assert.match(videoListSource, /CancelSubtitleTask/);
assert.match(videoListSource, /subtitle-queue-panel/);
assert.match(videoListSource, /SearchLibraryVideos/);
assert.match(videoListSource, /GetLibrarySubtitleHits/);
assert.match(videoListSource, /minimizeSubtitleProgress/);
assert.match(videoListSource, /subtitleProgressTaskID/);
assert.match(videoListSource, /minimizedSubtitleTaskIds/);
assert.match(videoListSource, /CancelSubtitleTask\(this\.subtitleProgressTaskID\)/);
assert.match(settingsSource, /subtitle_translation_provider/);
assert.match(settingsSource, /OpenAI 兼容 API/);
assert.match(settingsSource, /subtitle_whisperx_model/);
assert.match(settingsSource, /subtitle_whisperx_batch_size/);
assert.match(bindingsSource, /export function GetSubtitleQueueState/);
assert.match(bindingsSource, /export function CancelSubtitleTask/);
assert.match(bindingsSource, /export function SearchSubtitleMatchesWithFilters/);

console.log('subtitle-workflow tests passed');
