import { createApp, h, nextTick } from 'vue';
import InlineVideoPlayer from '../../src/components/player/InlineVideoPlayer.vue';
import AppFeedback from '../../src/components/AppFeedback.vue';
import SubtitleWorkbench from '../../src/components/SubtitleWorkbench.vue';
import { SUBTITLE_TOOL_REQUEST } from '../../src/utils/subtitleNavigation.js';
import '../../src/styles/tokens.css';
import '../../src/styles/components.css';
const cues = [{ start_ms: 100, end_ms: 2900, text: 'A quiet moment\n安静的片刻' }];
let badSubtitle = false;
window.go = { main: { App: new Proxy({}, { get: (_, method) => async () => {
  if (method === 'GetPlaybackSubtitle') { if (badSubtitle) throw new Error('字幕需要修复'); return { status: 'ready', cues }; }
  if (method === 'GetVideoDetails') return { video: { id: 1, name: '合成字幕测试', duration: 3 } };
  if (method === 'GetSubtitleEditDocument') return { video_id: 1, fingerprint: { size: 80, mod_time_ns: 1, sha256: 'fixture' }, entries: [{ client_id: 'one', start_time_ms: 100, end_time_ms: 2900, text: cues[0].text }] };
  if (method === 'GetPreviewSession') return { video_id: 1, mode: 'inline', source_version: 'fixture', inline_source: { locator_value: './clip.mp4', mime: 'video/mp4' } };
  if (method === 'GetPlaybackProxy') return null;
  if (method === 'GetPlaybackProxyStatus') return { running: false, queued: 0, results: [] };
  return [];
} }) } };
const root = createApp({ data: () => ({ source: 'fixture', tool: false }), render() { return [h(InlineVideoPlayer, { videoId: 1, session: { video_id: 1, mode: 'inline', source_version: this.source, inline_source: { locator_value: './clip.mp4', mime: 'video/mp4' } } }), h(AppFeedback), this.tool ? h(SubtitleWorkbench, { video: { id: 1, name: '合成字幕测试', duration: 3 }, onClose: () => { this.tool = false; } }) : null]; } }).mount('#app');
window.addEventListener(SUBTITLE_TOOL_REQUEST, event => { event.detail.handled = true; if (event.detail.isCurrent()) root.tool = true; event.detail.resolve(true); });
const assert = (condition, label) => { if (!condition) throw new Error(label); };
const until = async (test, label) => { window.progress = label; const end = Date.now() + 7000; while (!test() && Date.now() < end) { await new Promise(r => setTimeout(r, 20)); await nextTick(); } assert(test(), label); };
const click = async selector => { const element = document.querySelector(selector); assert(element, `missing ${selector}`); element.click(); await nextTick(); };
window.fixture = { async run() {
  await until(() => document.querySelector('video')?.textTracks[0]?.cues?.length === 1, 'real caption track');
  const original = document.querySelector('video'); original.currentTime = 1;
  await until(() => !original.seeking, 'seek fixture');
  await click('[data-test=playback-subtitle-toggle]');
  for (const [label, value] of [['字幕字号','xlarge'],['字幕背景','solid'],['字幕位置','top']]) {
    const input = document.querySelector(`[aria-label="${label}"]`); input.value = value; input.dispatchEvent(new Event('change', { bubbles: true })); await nextTick();
  }
  assert(original.textTracks[0].cues[0].line === 5, 'top position');
  // WebKit does not expose the native cue box through getComputedStyle(::cue).
  // Check its supplied style here; the runner screenshot verifies the rendered text.
  assert(original.style.getPropertyValue('--fs-subtitle-active') === '40px', 'cue font projection');
  await click('[data-test=playback-transcript-toggle]');
  assert(document.querySelector('[data-cue-id="0"] span').textContent === cues[0].text, 'bilingual transcript');
  await click('[data-cue-id="0"]'); await until(() => !original.seeking && Math.abs(original.currentTime - .1) < .05, 'dialogue seek');
  await click('[aria-label="关闭台词"]');
  await click('[data-test=inline-player-fullscreen]'); await click('[data-test=player-app-fullscreen]');
  assert(document.querySelector('dialog').matches(':modal'), 'application fullscreen');
  badSubtitle = true; root.source = 'fixture-changed';
  await until(() => document.querySelector('[data-test=playback-subtitle-toggle]'), 'new source controls');
  await click('[data-test=playback-subtitle-toggle]');
  await until(() => document.querySelector('[data-test=playback-subtitle-edit]'), 'repair action');
  await click('[data-test=playback-subtitle-edit]');
  await until(() => document.querySelector('[data-test=entry-text-one]'), 'real workbench');
  assert(!document.querySelector('dialog').matches(':modal'), 'tool exits fullscreen');
  assert(!document.querySelector('dialog').contains(document.querySelector('.subtitle-workbench')), 'tool keyboard domain isolated');
  const textarea = document.querySelector('[data-test=entry-text-one]'); textarea.value = 'edited'; textarea.dispatchEvent(new Event('input', { bubbles: true })); await nextTick();
  root.source = 'changed-while-editing'; await nextTick();
  assert(document.querySelector('[data-test=entry-text-one]') === textarea && textarea.value === 'edited', 'source change preserves editing session');
  await click('[data-test=close-workbench]'); await until(() => document.querySelector('[data-test=app-confirm-ok]'), 'unsaved confirmation');
  const confirm = document.querySelector('[data-test=app-confirm]'); const rect = confirm.getBoundingClientRect();
  assert(document.elementFromPoint(rect.left + rect.width / 2, rect.top + 25)?.closest('[data-test=app-confirm]'), 'confirmation above workbench');
  await click('[data-test=app-confirm-cancel]'); assert(document.querySelector('.subtitle-workbench'), 'cancel preserves editor');
  await click('[data-test=close-workbench]'); await click('[data-test=app-confirm-ok]');
  await until(() => !document.querySelector('.subtitle-workbench'), 'confirmed close');
  badSubtitle = false; root.source = 'fixture-final'; await until(() => document.querySelector('video')?.textTracks[0]?.cues?.length === 1, 'final caption');
  document.querySelector('video').currentTime = 1; await until(() => !document.querySelector('video').seeking, 'final position');
  await click('[data-test=playback-subtitle-toggle]'); await click('[data-test=playback-transcript-toggle]');
  window.results = { passed: true, realDecodedMedia: true, cases: ['cue-font-projection', 'native-cue-position', 'bilingual-transcript', 'transcript-seek', 'tool-exits-fullscreen', 'tool-keyboard-isolation', 'source-change-keeps-draft', 'unsaved-confirm-visible', 'cancel-keeps-editor', 'confirm-closes-editor'] };
} };
window.ready = true;
