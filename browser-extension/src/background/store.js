// 命中存储：按标签页聚合、按规范化 URL 去重、限量。
//
// 不碰 chrome API，sniffer.js 负责把事件喂进来，因此这里可以直接单测。
// 生命周期与进程一致：service worker 被回收后重建即清空，命中本来就是"当前页面正在放什么"，
// 隔一次重启还留着反而是脏数据。

import { MEDIA_KIND } from '../common/constants.js';
import { dedupeKey } from '../common/url.js';
import { segmentPlaylistHint } from '../common/media-kind.js';

// 单个标签页最多留这么多条。命中数上千的页面（广告轮播、分片直连）不该把内存吃光，
// 也不该让用户在几百条里翻。超出后丢最早的非命中焦点项。
const MAX_DETECTIONS_PER_TAB = 200;
const MAX_SEGMENT_GROUPS_PER_TAB = 50;

// 会在界面上单独列出来的类型。分片不在其中：它只喂 segmentGroups。
const LISTABLE_KINDS = new Set([MEDIA_KIND.HLS, MEDIA_KIND.FILE, MEDIA_KIND.DASH, MEDIA_KIND.MSE]);

export class DetectionStore {
  constructor({ now = () => Date.now() } = {}) {
    this.now = now;
    this.tabs = new Map(); // tabId -> { detections: Map<key, detection>, segmentGroups: Map<hint, group>, page }
    this.nextId = 1;
    // Cookie 单独存、只存内存、从不落 chrome.storage：它是登录凭据。
    // 只有用户在推送时显式勾选"附带 Cookie"才会被读走一次。
    this.cookies = new Map(); // detectionId -> cookie header
  }

  tabState(tabId) {
    let state = this.tabs.get(tabId);
    if (!state) {
      state = { detections: new Map(), segmentGroups: new Map(), page: { url: '', title: '' } };
      this.tabs.set(tabId, state);
    }
    return state;
  }

  setPageInfo(tabId, { url, title }) {
    const state = this.tabState(tabId);
    if (typeof url === 'string' && url) state.page.url = url;
    if (typeof title === 'string' && title) state.page.title = title;
    // 已登记的命中沿用最新的页面标题：命中往往先于 title 就位。
    for (const detection of state.detections.values()) {
      if (!detection.pageTitle && state.page.title) detection.pageTitle = state.page.title;
      if (!detection.pageUrl && state.page.url) detection.pageUrl = state.page.url;
    }
    return state.page;
  }

  // 返回 { detection, isNew }；重复命中只累加计数与时间戳。
  add(tabId, { kind, url, extension, mime, contentLength, acceptsRanges, headers, cookie }) {
    const state = this.tabState(tabId);
    const key = dedupeKey(url);

    if (kind === MEDIA_KIND.SEGMENT) {
      return { detection: this.addSegment(state, url, headers), isNew: false };
    }

    const existing = state.detections.get(key);
    if (existing) {
      existing.hits += 1;
      existing.lastSeenAt = this.now();
      // 体积与断点续传能力以最后一次观察为准：首次可能是 206 之类的局部响应。
      if (contentLength) existing.contentLength = contentLength;
      if (acceptsRanges) existing.acceptsRanges = true;
      return { detection: existing, isNew: false };
    }

    const detection = {
      id: `d${this.nextId++}`,
      tabId,
      kind,
      url,
      key,
      extension: extension || '',
      mime: mime || '',
      contentLength: contentLength || 0,
      acceptsRanges: Boolean(acceptsRanges),
      pageUrl: state.page.url,
      pageTitle: state.page.title,
      firstSeenAt: this.now(),
      lastSeenAt: this.now(),
      hits: 1,
      headers: sanitizeHeaders(headers),
      hasCookie: Boolean(cookie)
    };
    state.detections.set(key, detection);
    if (cookie) this.cookies.set(detection.id, cookie);
    this.trim(state);
    return { detection, isNew: true };
  }

  // 分片只累加到它所属目录的分组上。上千条 .ts 逐条列出对用户没有任何用处，
  // 有用的是"这个目录下有分片在跑，但没看见 m3u8"。
  addSegment(state, url, headers) {
    const hint = segmentPlaylistHint(url);
    if (!hint) return null;
    let group = state.segmentGroups.get(hint);
    if (!group) {
      if (state.segmentGroups.size >= MAX_SEGMENT_GROUPS_PER_TAB) return null;
      group = {
        id: `s${this.nextId++}`,
        kind: MEDIA_KIND.SEGMENT,
        hint,
        sampleUrl: url,
        count: 0,
        firstSeenAt: this.now(),
        headers: sanitizeHeaders(headers)
      };
      state.segmentGroups.set(hint, group);
    }
    group.count += 1;
    group.lastSeenAt = this.now();
    return group;
  }

  // 界面要的列表：可列类型的命中，加上"只见分片没见播放列表"的分组。
  list(tabId) {
    const state = this.tabs.get(tabId);
    if (!state) return { page: { url: '', title: '' }, items: [], orphanSegments: [] };

    const items = [...state.detections.values()]
      .filter((detection) => LISTABLE_KINDS.has(detection.kind))
      .sort((a, b) => b.lastSeenAt - a.lastSeenAt);

    const hlsPrefixes = items
      .filter((item) => item.kind === MEDIA_KIND.HLS)
      .map((item) => directoryPrefix(item.url));

    const orphanSegments = [...state.segmentGroups.values()]
      .filter((group) => !hlsPrefixes.some((prefix) => prefix && group.hint.startsWith(prefix)))
      .sort((a, b) => b.count - a.count);

    return { page: { ...state.page }, items, orphanSegments };
  }

  get(tabId, detectionId) {
    const state = this.tabs.get(tabId);
    if (!state) return null;
    for (const detection of state.detections.values()) {
      if (detection.id === detectionId) return detection;
    }
    for (const group of state.segmentGroups.values()) {
      if (group.id === detectionId) return group;
    }
    return null;
  }

  // 取 Cookie 是一次显式动作：只有推送时用户勾了"附带 Cookie"才会走到这里。
  cookieFor(detectionId) {
    return this.cookies.get(detectionId) || '';
  }

  // 界面角标只数可列项加孤儿分片分组。
  count(tabId) {
    const { items, orphanSegments } = this.list(tabId);
    return items.length + orphanSegments.length;
  }

  clearTab(tabId) {
    const state = this.tabs.get(tabId);
    if (!state) return;
    for (const detection of state.detections.values()) this.cookies.delete(detection.id);
    this.tabs.delete(tabId);
  }

  // 导航到新页面：命中清空，但页面信息换成新的。
  resetForNavigation(tabId, { url, title }) {
    this.clearTab(tabId);
    this.setPageInfo(tabId, { url, title });
  }

  trim(state) {
    if (state.detections.size <= MAX_DETECTIONS_PER_TAB) return;
    const ordered = [...state.detections.entries()].sort((a, b) => a[1].lastSeenAt - b[1].lastSeenAt);
    const excess = state.detections.size - MAX_DETECTIONS_PER_TAB;
    for (let index = 0; index < excess; index += 1) {
      const [key, detection] = ordered[index];
      state.detections.delete(key);
      this.cookies.delete(detection.id);
    }
  }
}

// 只留下取流真正需要的三个头。Cookie 不进这里，Authorization 之类也不留：
// 它们会随命中一起进入界面与推送载荷，凭据不该走这条路。
function sanitizeHeaders(headers) {
  const source = headers || {};
  return {
    referer: source.referer || '',
    origin: source.origin || '',
    userAgent: source.userAgent || ''
  };
}

function directoryPrefix(url) {
  try {
    const parsed = new URL(url);
    const lastSlash = parsed.pathname.lastIndexOf('/');
    parsed.pathname = lastSlash >= 0 ? parsed.pathname.slice(0, lastSlash + 1) : '/';
    parsed.search = '';
    parsed.hash = '';
    return parsed.toString();
  } catch {
    return '';
  }
}
