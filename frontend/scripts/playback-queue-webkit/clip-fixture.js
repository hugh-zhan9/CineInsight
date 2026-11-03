import { createApp, h, nextTick } from 'vue';
import QueueInlinePlayer from '../../src/components/QueueInlinePlayer.vue';
import '../../src/styles/tokens.css';
import '../../src/styles/components.css';
const reports = [], decoded = new Set(), formal = [];
let root;
const session = (token, videoID, start, end, paused) => ({ token, video_id: videoID, source_version: token, locator: './clip.mp4', mime: 'video/mp4', entry_kind: 'clip', clip_start_ms: start, clip_end_ms: end, start_seconds: start / 1000, duration: 3, view_cycle: 1, control_sequence: 1, desired_paused: paused });
const completed = new Set();
window.go = { main: { App: new Proxy({}, { get: (_, method) => async (...args) => {
  if (method === 'GetPlaybackSubtitle') return { status: 'ready', cues: [{ start_ms: 0, end_ms: 150, text: '区间外台词' }, { start_ms: 2750, end_ms: 2950, text: '区间内台词' }] };
  if (method === 'ReportInlineQueuePlayback') {
    const event = args[0]; reports.push(event);
    if (event.kind === 'loaded') decoded.add(event.token);
    if (event.kind === 'ended' && !completed.has(event.token) && root.session?.token === event.token) {
      completed.add(event.token);
      root.session = event.token === 'clip-a' ? session('clip-b', 2, 200, 650, false) : null;
    }
    return;
  }
  if (['RecordViewEvent', 'UpdateVideoWatchProgress', 'PlayVideo'].includes(method)) formal.push(method);
  if (method === 'GetPlaybackQueue') return { session: root.session };
  return [];
} }) } };
root = createApp({ data: () => ({ session: session('clip-a', 1, 2750, 2950, true) }), render() { return h('section', { style: 'width:900px;max-width:100%;padding:16px;box-sizing:border-box' }, [h('h2', '合成素材：受控区间预览'), this.session ? h(QueueInlinePlayer, { ref: 'player', key: this.session.token, session: this.session }) : h('p', { 'data-test': 'clip-finished' }, '预览批次已结束')]); } }).mount('#app');
const assert = (condition, label) => { if (!condition) throw new Error(label); };
const until = async (test, label) => { window.progress = label; const deadline = Date.now() + 8000; while (!test() && Date.now() < deadline) { await new Promise(resolve => setTimeout(resolve, 20)); await nextTick(); } assert(test(), label); };
const click = async selector => { const element = document.querySelector(selector); assert(element, `missing ${selector}`); element.click(); await nextTick(); };
document.addEventListener('pointerdown', event => { window.lastPointer = { trusted: event.isTrusted, target: event.composedPath()[0]?.tagName, hook: event.composedPath()[0]?.getAttribute?.('data-test') }; }, true);
const nativeClick = (element, ratio) => { const rect = element.getBoundingClientRect(); assert(rect.width > 20, 'visible clip slider'); window.webkit.messageHandlers.nativeClick.postMessage({ x: rect.left + rect.width * ratio, y: rect.top + rect.height / 2 }); };
window.fixture = { async run() {
  await until(() => document.querySelector('video')?.readyState >= 2 && root.$refs.player?.initialized, 'decoded clip metadata');
  const first = document.querySelector('video');
  assert(first.videoWidth > 0 && Math.abs(first.currentTime - 2.75) < .04, 'near-end start must not restart whole video');
  const slider = document.querySelector('[data-test=playback-clip-seek]');
  assert(Number(slider.min) === 2.75 && Number(slider.max) === 2.95, 'slider bounds');
  nativeClick(slider, .7);
  await until(() => window.lastPointer?.trusted && !first.seeking && first.currentTime > 2.8, 'trusted bounded seek');
  first.parentElement.focus(); first.parentElement.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowLeft', bubbles: true, cancelable: true }));
  await until(() => !first.seeking && Math.abs(first.currentTime - 2.75) < .02, 'bounded keyboard seek');
  await click('[data-test=playback-subtitle-toggle]'); await click('[data-test=playback-transcript-toggle]');
  assert(document.querySelector('[data-cue-id="0"]').disabled, 'outside dialogue cannot seek out of clip');
  await click('[aria-label="关闭台词"]');
  await click('[data-test=inline-player-fullscreen]'); await click('[data-test=player-app-fullscreen]');
  assert(document.querySelector('dialog').matches(':modal'), 'application fullscreen');
  await click('[data-test=inline-player-fullscreen]');
  await until(() => !document.querySelector('dialog').matches(':modal'), 'leave application fullscreen');
  await click('[data-test=inline-player-fullscreen]');
  await new Promise(resolve => setTimeout(resolve, 60));
  assert(root.session?.token === 'clip-a' && first.paused, 'fullscreen preparation must stay paused');
  nativeClick(document.querySelector('[data-test=player-system-fullscreen]'), .5);
  try { await until(() => document.fullscreenElement || document.webkitFullscreenElement, 'system fullscreen'); } catch (error) { throw new Error(`${error.message}: ${JSON.stringify({pointer:window.lastPointer,session:root.session?.token,alert:document.querySelector('[role=alert]')?.textContent})}`); }
  nativeClick(document.querySelector('[data-test=playback-clip-seek]'), .4);
  await until(() => !first.seeking && first.currentTime >= 2.75 && first.currentTime < 2.95, 'fullscreen bounded seek');
  await click('[data-test=inline-player-fullscreen]');
  await until(() => !document.fullscreenElement && !document.webkitFullscreenElement, 'leave system fullscreen');
  root.$refs.player.seekClip(2.75); await until(() => !first.seeking, 'reset clip start');
  root.session = { ...root.session, desired_paused: false, control_sequence: 2 };
  await until(() => root.session?.token === 'clip-b' && document.querySelector('video')?.videoWidth > 0, 'next real clip decoded');
  await until(() => !root.session && document.querySelector('[data-test=clip-finished]'), 'batch endpoint stopped');
  assert(first.paused, 'retired element paused');
  assert(decoded.has('clip-a') && decoded.has('clip-b'), 'both clips loaded');
  for (const [token,end] of [['clip-a',2.95],['clip-b',.65]]) {
    const ends = reports.filter(event => event.token === token && event.kind === 'ended');
    assert(ends.length === 1 && Math.abs(ends[0].position - end) < .001, 'one exact endpoint per clip ' + token);
    assert(reports.filter(event => event.token === token).every(event => event.view_cycle === 1), 'clip never starts a formal replay cycle');
  }
  assert(formal.length === 0, 'no formal client writes');
  root.session = session('clip-display', 3, 500, 1500, true); await until(() => document.querySelector('video')?.readyState >= 2, 'paused verification display');
  await click('[data-test=playback-volume-toggle]'); await new Promise(resolve => setTimeout(resolve, 100));
  window.results = { passed: true, realDecodedMedia: true, clips: completed.size, cases: ['near-end-start', 'native-bounded-slider', 'bounded-keyboard', 'bounded-transcript', 'application-fullscreen', 'system-fullscreen', 'two-clip-protocol', 'single-end-per-token', 'no-formal-client-writes'] };
} };
window.ready = true;
