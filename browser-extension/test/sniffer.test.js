import test from 'node:test';
import assert from 'node:assert/strict';

import { DetectionStore } from '../src/background/store.js';
import { Sniffer } from '../src/background/sniffer.js';
import { classifyRequest } from '../src/common/media-kind.js';
import { MEDIA_KIND } from '../src/common/constants.js';

function header(name, value) {
  return { name, value };
}

test('按 URL 后缀判定 HLS、分片与直链', () => {
  assert.equal(classifyRequest({ url: 'https://a/b/index.m3u8', resourceType: 'xmlhttprequest' }).kind, MEDIA_KIND.HLS);
  assert.equal(classifyRequest({ url: 'https://a/b/seg1.ts', resourceType: 'media' }).kind, MEDIA_KIND.SEGMENT);
  assert.equal(classifyRequest({ url: 'https://a/b/movie.mp4', resourceType: 'media' }).kind, MEDIA_KIND.FILE);
  assert.equal(classifyRequest({ url: 'https://a/b/manifest.mpd', resourceType: 'xmlhttprequest' }).kind, MEDIA_KIND.DASH);
});

test('后缀认不出来时按 Content-Type 判定', () => {
  const hls = classifyRequest({
    url: 'https://a/play/12345',
    resourceType: 'xmlhttprequest',
    responseHeaders: [header('Content-Type', 'application/vnd.apple.mpegurl; charset=utf-8')]
  });
  assert.equal(hls.kind, MEDIA_KIND.HLS);

  const file = classifyRequest({
    url: 'https://a/stream/9',
    resourceType: 'media',
    responseHeaders: [header('content-type', 'video/mp4'), header('Content-Length', '1048576'), header('Accept-Ranges', 'bytes')]
  });
  assert.equal(file.kind, MEDIA_KIND.FILE);
  assert.equal(file.extension, 'mp4');
  assert.equal(file.contentLength, 1048576);
  assert.equal(file.acceptsRanges, true);
});

test('播放列表藏在查询串里也能认出来', () => {
  const hit = classifyRequest({ url: 'https://a/proxy?src=https%3A%2F%2Fcdn%2Fx.m3u8&t=1', resourceType: 'xmlhttprequest' });
  assert.equal(hit.kind, MEDIA_KIND.HLS);
});

test('非媒体请求与非 http 协议不判为命中', () => {
  assert.equal(classifyRequest({ url: 'https://a/app.js', resourceType: 'script' }), null);
  assert.equal(classifyRequest({ url: 'https://a/logo.png', resourceType: 'image' }), null);
  assert.equal(classifyRequest({ url: 'blob:https://a/xyz', resourceType: 'media' }), null);
  assert.equal(classifyRequest({ url: 'https://a/page.html', resourceType: 'main_frame' }), null);
});

test('同一条流反复请求只留一条并累加计数', () => {
  const store = new DetectionStore();
  store.setPageInfo(1, { url: 'https://page/x', title: '某剧 第一集' });
  const first = store.add(1, { kind: MEDIA_KIND.HLS, url: 'https://a/x.m3u8#one', extension: 'm3u8' });
  const second = store.add(1, { kind: MEDIA_KIND.HLS, url: 'https://a/x.m3u8#two', extension: 'm3u8' });

  assert.equal(first.isNew, true);
  assert.equal(second.isNew, false);
  assert.equal(second.detection.id, first.detection.id);
  assert.equal(second.detection.hits, 2);
  assert.equal(store.list(1).items.length, 1);
  assert.equal(store.list(1).items[0].pageTitle, '某剧 第一集');
});

test('查询串不同视为不同的流', () => {
  const store = new DetectionStore();
  store.add(1, { kind: MEDIA_KIND.HLS, url: 'https://a/x.m3u8?token=1' });
  store.add(1, { kind: MEDIA_KIND.HLS, url: 'https://a/x.m3u8?token=2' });
  assert.equal(store.list(1).items.length, 2);
});

test('分片按所属目录归组，不逐条列出', () => {
  const store = new DetectionStore();
  for (let index = 0; index < 500; index += 1) {
    store.add(1, { kind: MEDIA_KIND.SEGMENT, url: `https://a/hls/seg${index}.ts` });
  }
  const listed = store.list(1);
  assert.equal(listed.items.length, 0);
  assert.equal(listed.orphanSegments.length, 1);
  assert.equal(listed.orphanSegments[0].count, 500);
  assert.equal(listed.orphanSegments[0].hint, 'https://a/hls/');
});

test('同目录下已经看到 m3u8 时，分片分组不再单列', () => {
  const store = new DetectionStore();
  store.add(1, { kind: MEDIA_KIND.SEGMENT, url: 'https://a/hls/seg0.ts' });
  store.add(1, { kind: MEDIA_KIND.HLS, url: 'https://a/hls/index.m3u8' });
  const listed = store.list(1);
  assert.equal(listed.items.length, 1);
  assert.equal(listed.orphanSegments.length, 0);
});

test('命中只留取流需要的三个请求头，Cookie 不进命中记录', () => {
  const store = new DetectionStore();
  store.add(1, {
    kind: MEDIA_KIND.HLS,
    url: 'https://a/x.m3u8',
    headers: { referer: 'https://page/x', origin: 'https://page', userAgent: 'UA/1', authorization: 'Bearer secret' },
    cookie: 'session=secret'
  });
  const detection = store.list(1).items[0];
  assert.deepEqual(Object.keys(detection.headers).sort(), ['origin', 'referer', 'userAgent']);
  assert.equal(detection.headers.authorization, undefined);
  assert.equal(JSON.stringify(detection).includes('secret'), false);
  assert.equal(detection.hasCookie, true);
  // Cookie 只在显式索取时才拿得到
  assert.equal(store.cookieFor(detection.id), 'session=secret');
});

test('标签页关闭与导航都会清掉命中，Cookie 一并丢弃', () => {
  const store = new DetectionStore();
  store.add(1, { kind: MEDIA_KIND.HLS, url: 'https://a/x.m3u8', cookie: 'k=v' });
  const id = store.list(1).items[0].id;

  store.resetForNavigation(1, { url: 'https://page/y', title: '第二集' });
  assert.equal(store.list(1).items.length, 0);
  assert.equal(store.cookieFor(id), '');
  assert.equal(store.list(1).page.title, '第二集');

  store.add(1, { kind: MEDIA_KIND.HLS, url: 'https://a/y.m3u8' });
  store.clearTab(1);
  assert.equal(store.list(1).items.length, 0);
});

test('单个标签页的命中数量有上限，超出后丢最早的', () => {
  const store = new DetectionStore({ now: (() => { let t = 0; return () => (t += 1); })() });
  for (let index = 0; index < 260; index += 1) {
    store.add(1, { kind: MEDIA_KIND.FILE, url: `https://a/v${index}.mp4` });
  }
  const items = store.list(1).items;
  assert.equal(items.length, 200);
  assert.equal(items.some((item) => item.url.endsWith('v0.mp4')), false);
  assert.equal(items.some((item) => item.url.endsWith('v259.mp4')), true);
});

test('Sniffer 把请求头接到随后的响应上', () => {
  const store = new DetectionStore();
  const changed = [];
  const sniffer = new Sniffer({ store, onChange: (tabId) => changed.push(tabId) });

  sniffer.handleSendHeaders({
    requestId: 'r1',
    tabId: 3,
    requestHeaders: [header('Referer', 'https://page/x'), header('User-Agent', 'UA/2'), header('Cookie', 'a=b')]
  });
  sniffer.handleHeadersReceived({
    requestId: 'r1',
    tabId: 3,
    url: 'https://cdn/x.m3u8',
    type: 'xmlhttprequest',
    responseHeaders: [header('Content-Type', 'application/x-mpegurl')]
  });

  const detection = store.list(3).items[0];
  assert.equal(detection.headers.referer, 'https://page/x');
  assert.equal(detection.headers.userAgent, 'UA/2');
  assert.equal(detection.hasCookie, true);
  assert.deepEqual(changed, [3]);
  // 响应到达后暂存即清空，不堆积
  assert.equal(sniffer.pending.size, 0);
});

test('Sniffer 忽略不属于任何标签页的请求', () => {
  const store = new DetectionStore();
  const sniffer = new Sniffer({ store });
  sniffer.handleSendHeaders({ requestId: 'r1', tabId: -1, requestHeaders: [] });
  assert.equal(sniffer.pending.size, 0);
  assert.equal(sniffer.handleHeadersReceived({ requestId: 'r1', tabId: -1, url: 'https://cdn/x.m3u8', type: 'media' }), null);
});

test('没有响应的请求头暂存会被扫掉，不无限堆积', () => {
  let clock = 0;
  const store = new DetectionStore();
  const sniffer = new Sniffer({ store, now: () => clock });
  sniffer.handleSendHeaders({ requestId: 'stale', tabId: 1, requestHeaders: [] });
  assert.equal(sniffer.pending.size, 1);
  clock += 61_000;
  sniffer.handleSendHeaders({ requestId: 'fresh', tabId: 1, requestHeaders: [] });
  assert.equal(sniffer.pending.size, 1);
  assert.equal(sniffer.pending.has('fresh'), true);
});
