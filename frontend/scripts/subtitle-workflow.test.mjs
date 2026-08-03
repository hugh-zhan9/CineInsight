import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const videoListSource = readFileSync(new URL('../src/components/VideoListPage.vue', import.meta.url), 'utf8');
const settingsSource = readFileSync(new URL('../src/components/SettingsPage.vue', import.meta.url), 'utf8');
const bindingsSource = readFileSync(new URL('../wailsjs/go/main/App.js', import.meta.url), 'utf8');

assert.match(videoListSource, /GetSubtitleQueueState/);
assert.match(videoListSource, /CancelSubtitleTask/);
assert.match(videoListSource, /subtitle-queue-panel/);
assert.match(videoListSource, /SearchLibraryVideoPage/);
assert.match(videoListSource, /GetLibrarySubtitleHits/);
assert.match(videoListSource, /minimizeSubtitleProgress/);
assert.match(videoListSource, /subtitleProgressTaskID/);
assert.match(videoListSource, /minimizedSubtitleTaskIds/);
assert.match(videoListSource, /CancelSubtitleTask\(this\.subtitleProgressTaskID\)/);
assert.match(settingsSource, /subtitle_translation_provider/);
assert.match(settingsSource, /OpenAI 兼容接口（本地 \/ 远程）/);
assert.match(settingsSource, /此配置不会复用 AI 标签接口/);
assert.match(settingsSource, /subtitle_translation_base_url/);
assert.match(settingsSource, /subtitle_translation_api_key/);
assert.match(settingsSource, /subtitle_translation_model/);
assert.match(settingsSource, /subtitle_whisperx_model/);
assert.match(settingsSource, /subtitle_whisperx_batch_size/);
assert.match(bindingsSource, /export function GetSubtitleQueueState/);
assert.match(bindingsSource, /export function CancelSubtitleTask/);
assert.match(bindingsSource, /export function SearchSubtitleMatchesWithFilters/);

console.log('subtitle-workflow tests passed');
