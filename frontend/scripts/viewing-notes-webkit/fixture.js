import { createApp, h, nextTick } from 'vue';
import PreviewDrawer from '../../src/components/PreviewDrawer.vue';
import ViewingNotesPage from '../../src/components/ViewingNotesPage.vue';
import '../../src/styles/tokens.css';
import '../../src/styles/components.css';
const video = { id: 1, name: '合成样片.mp4', display_title: '原生播放验证', duration: 3, tags: [], watch_position_seconds: 0 };
const session = version => ({ video_id: 1, mode: 'inline', source_version: version, inline_source: { locator_value: './clip.mp4', mime: 'video/mp4' } });
const events = [];
window.go = { main: { App: new Proxy({}, { get: (_, method) => async (...args) => {
  if (method === 'GetVideoDetails') return { video, effective_title: video.display_title, people: [], collections: [], streams: [], technical_status: { state: 'current' }, technical_metadata: { format_name: 'mp4' } };
  if (method === 'GetPreviewSession') return session('v1');
  if (method === 'RecordViewEvent') { events.push(args); return true; }
  if (method === 'GetViewingYearReview') return { year: 2026, total: 50, automatic: 30, historical: 10, manual: 10, days: 2, months: [0,0,0,0,0,0,0,0,0,50,0,0], rated_count: 0, average_rating: null, most_watched: [] };
  if (method === 'ListViewingDiary') return { items: Array.from({ length: 50 }, (_, i) => ({ id: 50-i, revision: 1, title: `观看日记 ${i}`, watched_on: '2026-10-10', origin: 'manual', note: '重复观看独立保留。', rating: null })), has_more: true };
  if (method === 'ListVideoBookmarks') return { items: [], has_more: false };
  return [];
} }) } };
const root = createApp({ data: () => ({ mode: 'preview', session: session('v1'), bookmark: { videoID: 1, startMS: 0, endMS: 500, sourceToken: 'v1', requestID: 1 } }), render() {
  return this.mode === 'preview' ? h(PreviewDrawer, { ref: 'drawer', video, session: this.session, bookmarkPosition: this.bookmark }) : h(ViewingNotesPage);
} }).mount('#app');
const assert = (condition, message) => { if (!condition) throw new Error(message); };
const until = async (test, label) => { window.progress = label; const end = Date.now() + 7000; while (!test() && Date.now() < end) { await new Promise(resolve => setTimeout(resolve, 30)); await nextTick(); } assert(test(), label); };
window.fixture = { async run() {
  await until(() => document.querySelector('video')?.readyState >= 1, 'initial decoded metadata');
  let media = document.querySelector('video'); media.currentTime = 1;
  root.bookmark = { ...root.bookmark, requestID: 2 }; await until(() => media.currentTime < .05, 'repeat seek to zero');
  await media.play(); await until(() => root.$refs.drawer.bookmarkRangeFinished && media.paused, 'range pause');
  assert(!root.$refs.drawer._viewSession.ended && events.length === 0, 'range pause manufactured a natural end');
  root.$refs.drawer.continueBookmarkPlayback(); await until(() => media.ended && events.length === 1, 'first effective viewing');
  await media.play(); await until(() => media.ended && events.length === 2, 'same element explicit rewatch');
  assert(events[0][2] !== events[1][2], 'rewatch reused session ID');
  root.bookmark = null;
  for (const version of ['v2', 'v2']) {
    const old = media; root.session = session(version);
    await until(() => document.querySelector('video') !== old && document.querySelector('video')?.readyState >= 1, 'same locator reload ' + version);
    media = document.querySelector('video'); assert(media.querySelector('source').getAttribute('src') === './clip.mp4', 'source locator lost');
    assert(!media.error, 'reloaded media decode error');
  }
  root.mode = 'notes'; await until(() => document.querySelectorAll('[data-test="diary-row"]').length === 50, 'notes page bounded rows');
  document.querySelector('[data-test="diary-add"]').click(); await until(() => document.querySelector('[data-test="diary-editor"]'), 'manual diary editor');
  assert(document.querySelector('[data-test="diary-date"]').value === '', 'manual date was invented');
  const editor = document.querySelector('[data-test="diary-editor"]').getBoundingClientRect();
  assert(editor.width > 300 && editor.width < innerWidth, 'editor width outside viewport');
  window.results = { passed: true, engine: navigator.userAgent, realDecodedMedia: true, effectiveViewings: events.length, sameLocatorReloads: 2, diaryRows: 50, editorWidth: editor.width, cases: ['repeat-zero', 'range-pause', 'continue-full', 'natural-rewatch', 'changed-source-reload', 'same-version-reload', 'diary-page', 'manual-date'] };
} };
window.ready = true;
