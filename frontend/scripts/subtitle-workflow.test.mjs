import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const videoListSource = readFileSync(new URL('../src/components/VideoListPage.vue', import.meta.url), 'utf8');
const settingsSource = readFileSync(new URL('../src/components/SettingsPage.vue', import.meta.url), 'utf8');
const bindingsSource = readFileSync(new URL('../wailsjs/go/main/App.js', import.meta.url), 'utf8');
// P-002 把字幕任务队列与生成弹窗抽成了独立组件，下面属于它的断言跟着搬。
const subtitleDialogSource = readFileSync(new URL('../src/components/video-list/SubtitleGenerateDialog.vue', import.meta.url), 'utf8');
// 设置页的字幕分区也抽成了独立组件。
const subtitleSettingsSource = readFileSync(new URL('../src/components/settings/SubtitleSection.vue', import.meta.url), 'utf8');

assert.match(subtitleDialogSource, /GetSubtitleQueueState/);
assert.match(subtitleDialogSource, /CancelSubtitleTask/);
assert.match(subtitleDialogSource, /subtitle-queue-panel/);
assert.match(videoListSource, /SearchLibraryVideoPage/);
assert.match(videoListSource, /GetLibrarySubtitleHits/);
assert.match(subtitleDialogSource, /minimizeSubtitleProgress/);
assert.match(subtitleDialogSource, /subtitleProgressTaskID/);
assert.match(subtitleDialogSource, /minimizedSubtitleTaskIds/);
assert.match(subtitleDialogSource, /CancelSubtitleTask\(this\.subtitleProgressTaskID\)/);
assert.match(subtitleSettingsSource, /subtitle_translation_provider/);
assert.match(subtitleSettingsSource, /OpenAI 兼容接口（本地 \/ 远程）/);
assert.match(subtitleSettingsSource, /此配置不会复用 AI 标签接口/);
assert.match(subtitleSettingsSource, /subtitle_translation_base_url/);
assert.match(subtitleSettingsSource, /subtitle_translation_api_key/);
assert.match(subtitleSettingsSource, /subtitle_translation_model/);
assert.match(subtitleSettingsSource, /subtitle_whisperx_model/);
assert.match(subtitleSettingsSource, /subtitle_whisperx_batch_size/);
assert.match(bindingsSource, /export function GetSubtitleQueueState/);
assert.match(bindingsSource, /export function CancelSubtitleTask/);
// 旧的字幕搜索入口 SearchSubtitleMatchesWithFilters 已无前端调用，P-040 删除；片库页的字幕命中走 GetLibrarySubtitleHits。
assert.doesNotMatch(bindingsSource, /export function SearchSubtitleMatchesWithFilters/);
assert.match(bindingsSource, /export function GetLibrarySubtitleHits/);

console.log('subtitle-workflow tests passed');
