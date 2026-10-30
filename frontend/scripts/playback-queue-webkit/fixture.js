import { createApp, h, nextTick } from 'vue';
import InlineVideoPlayer from '../../src/components/player/InlineVideoPlayer.vue';
import QueueInlinePlayer from '../../src/components/QueueInlinePlayer.vue';
import PlaybackQueuePanel from '../../src/components/PlaybackQueuePanel.vue';
import '../../src/styles/tokens.css';
import '../../src/styles/components.css';
const reports = [];
const subtitles = [{start_ms:100,end_ms:2800,text:'Hello <world>\n你好'}];
const subtitleRequests=[];
let controls = 0;
let maintenance = false;
const session = (token = 'a', paused = true) => ({ token, video_id: 1, source_version: token, locator: './clip.mp4', mime: 'video/mp4', start_seconds: 0, duration: 3, view_cycle: 1, control_sequence: ++controls, desired_paused: paused });
window.go = { main: { App: new Proxy({}, { get: (_, method) => async (...args) => {
  if (method === 'GetPlaybackSubtitle') { subtitleRequests.push(args[0]); return {status:'ready',cues:subtitles}; }
  if (method === 'GetPlaybackProxy') return null;
  if (method === 'GetPlaybackProxyStatus') return {running:false,queued:0,results:[]};
  if (method === 'ReportInlineQueuePlayback') { reports.push(args[0]); return; }
  if (method === 'GetPlaybackQueue' && maintenance) throw new Error('queue_unavailable: 数据库正在恢复或切换，暂时无法读取');
  if (method === 'GetPlaybackQueue') return { state: { revision: 1, player: 'inline', autoplay: false, status: 'paused', active_token: 'panel' }, items: Array.from({ length: 50 }, (_, i) => ({ id: i+1, title: `待播影片 ${i+1}`, video_id: i+1, source_available: true })), current: { id: 1, video_id: 1, title: '原生队列播放' }, total: 10000, has_more: true, cursor_id: 50, cursor_position: 50, sequence: 1, capabilities: [{ player: 'inline', available: true, autoplay: true }], session: session('panel') };
  return [];
} }) } };
const root = createApp({ data: () => ({ mode: 'player', session: session(), open: true, detail: {video_id:1,mode:'inline',source_version:'detail-source',inline_source:{locator_value:'./clip.mp4',mime:'video/mp4'}} }), render() { if (this.mode === 'detail') return h(InlineVideoPlayer,{ref:'detail',videoId:1,session:this.detail}); return this.mode === 'player' ? h('section', {style:'width:380px;max-width:100%'}, [h(QueueInlinePlayer, { session: this.session, key: this.session.token })]) : h(PlaybackQueuePanel, { ref: 'panel', open: this.open, onClose: () => { this.open = false; } }); } }).mount('#app');
const assert = (value, message) => { if (!value) throw new Error(message); };
const until = async (test, label) => { window.progress = label; const end = Date.now()+8000; while (!test() && Date.now()<end) { await new Promise(resolve => setTimeout(resolve, 20)); await nextTick(); } assert(test(), label); };
const click = selector => { const element = document.querySelector(selector); assert(element, `Missing ${selector}`); element.click(); };
document.addEventListener('pointerdown', event => { window.lastPointer = { x:event.clientX, y:event.clientY, trusted:event.isTrusted, target:event.composedPath()[0]?.tagName }; }, true);
const trustedClick = (element, ratio = .5) => { const rect = element.getBoundingClientRect(); window.lastClickRect = {x:rect.x,y:rect.y,width:rect.width,height:rect.height}; window.webkit.messageHandlers.nativeClick.postMessage({ x: rect.left + rect.width * ratio, y: rect.top + rect.height / 2 }); };
async function verifyControls(video, label) {
  const surface = video.parentElement;
  const bar = document.querySelector('[data-test="playback-controls"]');
  const seek = document.querySelector('media-time-range');
  await until(() => seek.mediaDuration === 3, `${label} library media association`);
  assert(document.querySelectorAll('media-time-range').length === 1 && !video.controls, 'one timeline without native controls');
  assert(!document.querySelector('media-volume-range') && !document.querySelector('input[type=number]'), 'settings initially collapsed');
  assert(bar.scrollWidth <= bar.clientWidth + 1, 'controls fit player');
  const shell = surface.parentElement;
  assert(shell.getBoundingClientRect().width <= shell.parentElement.getBoundingClientRect().width + 1, 'player fits allocated parent width');
  assert(Math.abs(video.getBoundingClientRect().bottom - surface.getBoundingClientRect().bottom) < 1, 'video fills player');
  await until(() => video.textTracks[0]?.cues?.length === 1, 'caption loaded');
  assert(video.textTracks[0].cues[0].line < 90, 'caption avoids control overlay');
  for (const ratio of [.25, .65]) {
    trustedClick(seek.range, ratio);
    try {
      await until(() => !video.seeking && Math.abs(video.currentTime - 3 * ratio) < .12, `${label} click timeline at ${ratio}`);
    } catch (error) {
      throw new Error(`${error.message}: ${JSON.stringify({time:video.currentTime,duration:video.duration,seeking:video.seeking,rect:window.lastClickRect,pointer:window.lastPointer})}`);
    }
  }
  seek.dispatchEvent(new KeyboardEvent('keydown', {key:'ArrowLeft', bubbles:true, cancelable:true}));
  await until(() => !video.seeking && video.currentTime < .05, 'timeline keyboard uses five-second step');
  video.currentTime = 1.2; await until(() => !video.seeking, 'prepare focused keyboard fixture');
  const playButton = document.querySelector('media-play-button'); playButton.focus();
  playButton.dispatchEvent(new KeyboardEvent('keydown', {key:'ArrowLeft',bubbles:true,cancelable:true}));
  await until(() => !video.seeking && video.currentTime < .05, 'arrow keys after play button focus');
  video.volume = .5; seek.dispatchEvent(new KeyboardEvent('keydown', {key:'ArrowUp', bubbles:true, cancelable:true}));
  assert(Math.abs(video.volume - .55) < .001 && !video.muted, 'keyboard volume');
  click('[data-test="playback-toggle"]'); await until(() => !video.paused && !document.querySelector('media-play-button').mediaPaused, 'library play button');
  click('[data-test="playback-toggle"]'); await until(() => video.paused && document.querySelector('media-play-button').mediaPaused, 'library pause button');
  click('[data-test="playback-volume-toggle"]'); await nextTick();
  const volume = document.querySelector('media-volume-range');
  volume.range.value = '.4'; volume.range.dispatchEvent(new Event('input', {bubbles:true, composed:true}));
  await until(() => Math.abs(video.volume - .4) < .01 && !video.muted, 'library volume slider');
  await new Promise(r => setTimeout(r, 60));
  assert(document.querySelector('[data-test="playback-volume-panel"]').getBoundingClientRect().bottom < bar.getBoundingClientRect().bottom, 'inline volume menu opens above controls');
  click('[data-test="playback-volume-toggle"]'); await nextTick();
  // Loop only this synthetic clip so idle controls can finish fading before EOF.
  video.loop = true; video.currentTime = 0; await video.play();
  surface.dispatchEvent(new PointerEvent('pointermove', {pointerType:'mouse',bubbles:true}));
  await until(() => surface.hasAttribute('userinactive') && Number(getComputedStyle(bar).opacity) < .05, 'idle controls fade within frame');
  assert(video.textTracks[0].cues[0].line === 95, 'idle caption returns to bottom');
  video.pause(); video.loop = false;
  await until(() => Number(getComputedStyle(bar).opacity) > .99, 'paused controls visible');
  video.currentTime = 1.7; await until(() => !video.seeking, 'stable fullscreen position');
  const original = video;
  click('[data-test="inline-player-fullscreen"]'); await nextTick(); click('[data-test="player-app-fullscreen"]');
  await until(() => document.querySelector('dialog.playback-shell').matches(':modal'), 'application fullscreen');
  assert(Math.abs(surface.getBoundingClientRect().width - innerWidth) < 1, 'application fullscreen width');
  click('[data-test="playback-subtitle-toggle"]'); await nextTick(); await new Promise(r => setTimeout(r, 50));
  const panel = document.querySelector('.playback-subtitles__panel'); const rect = panel.getBoundingClientRect();
  assert(document.elementFromPoint(rect.left + 20, rect.top + 20)?.closest('.playback-subtitles__panel'), 'subtitle settings visible in application fullscreen');
  panel.dispatchEvent(new KeyboardEvent('keydown', {key:'Escape',bubbles:true,cancelable:true})); await nextTick();
  assert(document.querySelector('dialog.playback-shell').matches(':modal'), 'settings Escape keeps fullscreen');
  window.dispatchEvent(new KeyboardEvent('keydown', {key:'Escape',bubbles:true,cancelable:true})); await nextTick();
  assert(!document.querySelector('dialog.playback-shell').matches(':modal'), 'application fullscreen exit');
  click('[data-test="inline-player-fullscreen"]'); await nextTick();
  await new Promise(r => setTimeout(r, 60));
  trustedClick(document.querySelector('[data-test="player-system-fullscreen"]'));
  try { await until(() => document.fullscreenElement === surface, `${label} system fullscreen`); }
  catch(error) { throw new Error(`${error.message}: ${JSON.stringify({alert:document.querySelector('[role=alert]')?.textContent,rect:window.lastClickRect,pointer:window.lastPointer,fullscreen:document.fullscreenElement?.tagName,focus:document.hasFocus()})}`); }
  click('[data-test="playback-volume-toggle"]'); await nextTick(); await new Promise(r => setTimeout(r, 50));
  const vr = document.querySelector('[data-test="playback-volume-panel"]').getBoundingClientRect();
  assert(document.elementFromPoint(vr.left + 20, vr.top + 20)?.closest('[data-test="playback-volume-panel"]'), 'volume settings visible in system fullscreen');
  click('[data-test="playback-volume-toggle"]'); await nextTick();
  await document.exitFullscreen(); await until(() => !document.fullscreenElement, 'system fullscreen exit');
  assert(document.querySelector('video') === original && Math.abs(video.currentTime - 1.7) < .1, 'both fullscreen modes preserve video and time');
  video.currentTime = 0; await until(() => !video.seeking, 'reset fixture position');
}
window.fixture = { async run() {
  await until(() => document.querySelector('video')?.readyState >= 1, 'queue metadata');
  let video = document.querySelector('video');
  await until(() => video.textTracks[0]?.cues?.length===1,'queue Blob track loaded');
  assert(video.textTracks[0].cues[0].getCueAsHTML().textContent==='Hello <world>\n你好','queue subtitle literal bilingual text');
  assert(subtitleRequests[0].queue_token==='a','queue token passed');
  assert(video.paused && video.currentTime < .05, 'initial zero/paused');
  await verifyControls(video, 'queue');
  root.session = session('a', false); await until(() => video.currentTime > .4, 'actual playback');
  video.pause(); await until(() => video.paused && reports.some(e => e.kind === 'paused'), 'native pause');
  root.session = session('a', false); await until(() => !video.paused && video.currentTime > .6, 'resume after native pause with same desired flag');
  root.session = session('a', true); await until(() => video.paused && reports.some(e => e.kind === 'paused'), 'explicit pause');
  assert(!reports.some(e => e.kind === 'ended'), 'pause fabricated EOF');
  video.currentTime = 1; await until(() => !video.seeking, 'seek settled');
  assert(!reports.some(e => e.kind === 'ended'), 'seek fabricated EOF');
  root.session = session('a', false); await until(() => video.ended && reports.filter(e => e.kind === 'ended').length === 1, 'first natural end');
  await video.play(); await until(() => video.ended && reports.filter(e => e.kind === 'ended').length === 2, 'native rewatch');
  assert(reports.filter(e => e.kind === 'ended').map(e => e.view_cycle).join(',') === '1,2', 'rewatch cycle');
  assert(reports.every((e,i) => e.seq === i+1 && e.token === 'a' && e.source_version === 'a'), 'ordered stable identity');
  const old = video; const count = reports.length; root.session = session('b');
  await until(() => document.querySelector('video') !== old && document.querySelector('video')?.readyState >= 1, 'new token new element');
  old.dispatchEvent(new Event('ended')); await nextTick(); await new Promise(resolve => setTimeout(resolve, 50));
  assert(reports.slice(count).every(e => e.token === 'b'), 'retired source emitted into new session');
  root.mode = 'panel'; await until(() => document.querySelectorAll('li[data-test^="queue-item-"]').length === 50 && document.querySelector('video')?.readyState >= 1, 'bounded queue panel');
  const width = document.querySelector('[data-test="queue-panel"]').getBoundingClientRect().width; assert(width > 700 && width < innerWidth, 'queue panel width');
  video = document.querySelector('video'); root.open = false; await nextTick();
  assert(document.querySelector('video') === video && !document.querySelector('[data-test="queue-panel"]'), 'closing list destroyed playback');
  await video.play(); await until(() => video.currentTime > .25, 'player survives list close');
  maintenance = true; await root.$refs.panel.load(); await until(() => !document.querySelector('video'), 'maintenance unmounts buffered player'); assert(video.paused, 'maintenance did not pause detached video');
  root.mode='detail'; await until(()=>document.querySelector('video')?.textTracks[0]?.cues?.length===1,'detail Blob track loaded');
  video=document.querySelector('video'); await verifyControls(video, 'detail'); await video.play(); await until(()=>video.currentTime>.3,'detail actual playback');video.pause();
  const position=video.currentTime; const originalVideo=video;
  click('[data-test="playback-subtitle-toggle"]'); await nextTick();
  const offset=document.querySelector('[aria-label="字幕偏移（秒）"]');offset.value='0.5';offset.dispatchEvent(new Event('input',{bubbles:true}));
  await until(()=>video.textTracks[0]?.cues?.[0]?.startTime===.6,'subtitle offset changes cue');
  assert(document.querySelector('video')===originalVideo && Math.abs(video.currentTime-position)<.05,'subtitle update reset media');
  root.detail={...root.detail,inline_source:{...root.detail.inline_source,locator_value:'./clip.mp4?reload=1'}};
  await until(()=>document.querySelector('video')!==originalVideo && document.querySelector('video')?.textTracks[0]?.cues?.length===1,'same source version new element receives subtitle track');
  assert(subtitleRequests.some(request=>request.source_version==='detail-source' && !request.queue_token),'detail subtitle source identity');
  window.results = { passed: true, engine: navigator.userAgent, realDecodedMedia: true, naturalEnds: 2, queueRows: 50, queueTotal: 10000, panelWidth: width, cases: ['zero-start', 'pause-not-end', 'native-external-control-interleave', 'seek-not-end', 'natural-end', 'native-rewatch', 'fixed-token-identity', 'retired-element', 'bounded-page', 'close-list-keeps-player', 'maintenance-stops-buffered-player','queue-blob-subtitle','detail-blob-subtitle','bilingual-literal-text','subtitle-offset-preserves-video','same-source-new-video-track','one-timeline','trusted-pointer-seek','library-play-pause-volume','keyboard','both-fullscreen-modes','fullscreen-popups','caption-overlay-clearance','idle-hide-pause-show'] };
} };
window.ready = true;
