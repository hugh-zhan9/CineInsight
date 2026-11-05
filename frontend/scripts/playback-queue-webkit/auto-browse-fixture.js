import { createApp, h, nextTick } from 'vue';
import ShortFeedApp from '../../src/short-feed/ShortFeedApp.vue';
import '../../src/styles/tokens.css';
import '../../src/styles/components.css';
import '../../src/short-feed/short-feed.css';
document.body.classList.add('surface-cinema');
const canvas = document.createElement('canvas'); canvas.width = 320; canvas.height = 240;
canvas.getContext('2d').fillRect(0, 0, 320, 240);
const photo = canvas.toDataURL('image/png');
const video = new URL('original.mp4', location.href).href;
const kinds = ['image', 'image', 'video', 'video', 'image'];
const items = kinds.map((kind, index) => ({ id: index + 1, media_kind: kind, name: `Synthetic ${index + 1}`, media_url: kind === 'image' ? photo + `#${index}` : video + `#${index}`, width: 320, height: 240, duration: 1, tags: [] }));
let fetchIndex = 0;
window.fetch = async url => ({ ok: true, status: 200, headers: { get: () => 'application/json' }, json: async () => String(url).includes('/feed/next') ? (() => { const seq = fetchIndex++; const base = items[seq % items.length]; return { ...base, id: seq + 1, media_url: base.media_url.split('#')[0] + `#${seq}` }; })() : {} });
const root = createApp({ render: () => h(ShortFeedApp, { ref: 'feed' }) }).mount('#app');
const wait = ms => new Promise(resolve => setTimeout(resolve, ms));
function assert(value, label) { if (!value) throw new Error(label); }
async function until(test, label) { window.progress = label; const end = Date.now() + 12000; while (!test() && Date.now() < end) { await wait(20); await nextTick(); } assert(test(), label); }
window.fixture = { async run() {
  const app = root.$refs.feed;
  await until(() => app.autoReady && app.stagePrefetched, 'initial image decode');
  document.querySelector('[data-test=feed-auto-toggle]').click(); await nextTick();
  assert(innerWidth === 390, 'phone width');
  for (const button of document.querySelectorAll('.feed-auto-controls button')) {
    const rect = button.getBoundingClientRect();
    assert(rect.left >= 0 && rect.right <= innerWidth && rect.top >= 0 && rect.bottom < innerHeight, 'automatic controls fit phone viewport');
  }
  for (let id = 1; id <= 4; id++) {
    await until(() => app.currentVideo?.id === id, `current ${id}`);
    const stage = app.$refs.stage;
    await until(() => stage.state(app.stagePrefetched) === 'ready', `prepare ${id + 1}`);
    const expectedKey = stage.mediaKey(app.stagePrefetched);
    const node = stage._media[expectedKey];
    const start = performance.now();
    await until(() => app.currentVideo?.id === id + 1, `automatic transition ${id} to ${id + 1}`);
    const active = stage._media[stage.mediaKey(app.currentVideo)];
    assert(active === node, `reused decoded/buffered element ${id + 1}: ${JSON.stringify({ expectedKey, currentKey: stage.mediaKey(app.currentVideo), expected: node?.outerHTML, actual: active?.outerHTML, keys: Object.keys(stage._media) })}`);
    assert(document.querySelectorAll('.feed-media-slot').length <= 2, 'bounded media elements');
    if (id === 1) assert(performance.now() - start >= 4500, 'five second image hold');
  }
  const current = app.currentVideo.id; app.toggleAutoPause(); await wait(5200);
  assert(app.currentVideo.id === current, 'explicit pause prevents advance');
  app.toggleAutoBrowse();
  for (let n = 0; n < 45; n++) {
    await app.nextVideo(); await nextTick();
    await until(() => app.$refs.stage.state(app.currentVideo) === 'ready', `long browsing ${n}`);
    assert(Object.keys(app.$refs.stage._media).length <= 2, 'bounded media references after repeated browsing');
    assert(Object.keys(app.$refs.stage.readiness).length <= 2, 'bounded readiness records');
    assert(app.items.length <= 40, 'bounded browsing history');
  }
  root.$.appContext.app.unmount();
  assert(document.querySelectorAll('video, .feed-photo').length === 0, 'media released on exit');
  window.results = { passed: true, cases: ['image-image', 'image-video', 'video-video', 'video-image', 'five-second-hold', 'real-ended-event', 'element-reuse', 'bounded-media', 'pause', 'unmount-release', '45-switch-resource-bound', '390px-controls'] };
  window.resultJSON = JSON.stringify(window.results);
} };

window.ready = true;
