// webRequest 接线：把浏览器的网络事件翻译成命中，喂给 DetectionStore。
//
// 全程只读监听，不阻塞、不改写任何用户请求。请求头必须在请求发出时抓，
// 事后没有第二次机会——很多 CDN 只认那一次的 Referer。

import { classifyRequest } from '../common/media-kind.js';
import { MEDIA_KIND } from '../common/constants.js';

// 请求头暂存的上限与保活时长：正常情况下 onSendHeaders 与 onHeadersReceived 紧挨着，
// 留不住的都是没有响应的请求，扫掉即可。
const MAX_PENDING = 2000;
const PENDING_TTL_MS = 60_000;

export class Sniffer {
  constructor({ store, onChange, now = () => Date.now() }) {
    this.store = store;
    this.onChange = onChange || (() => {});
    this.now = now;
    this.pending = new Map(); // requestId -> { headers, cookie, at }
  }

  // 请求发出时抓头。Cookie 单独拿出来，不进 headers——它不该跟着命中到处走。
  handleSendHeaders(details) {
    if (!details || details.tabId === undefined || details.tabId < 0) return;
    const headers = {};
    let cookie = '';
    for (const header of details.requestHeaders || []) {
      const name = String(header.name || '').toLowerCase();
      if (name === 'referer') headers.referer = header.value || '';
      else if (name === 'origin') headers.origin = header.value || '';
      else if (name === 'user-agent') headers.userAgent = header.value || '';
      else if (name === 'cookie') cookie = header.value || '';
    }
    this.sweepPending();
    if (this.pending.size >= MAX_PENDING) return;
    this.pending.set(details.requestId, { headers, cookie, at: this.now() });
  }

  // 响应头到达时判定类型。这是唯一会往 store 写命中的入口。
  handleHeadersReceived(details) {
    if (!details || details.tabId === undefined || details.tabId < 0) return null;
    const classification = classifyRequest({
      url: details.url,
      responseHeaders: details.responseHeaders,
      resourceType: details.type
    });
    const pending = this.pending.get(details.requestId);
    this.pending.delete(details.requestId);
    if (!classification) return null;

    const result = this.store.add(details.tabId, {
      ...classification,
      url: details.url,
      headers: pending ? pending.headers : {},
      cookie: pending ? pending.cookie : ''
    });
    if (result && (result.isNew || classification.kind === MEDIA_KIND.SEGMENT)) {
      this.onChange(details.tabId);
    }
    return result;
  }

  finishRequest(details) {
    if (details && details.requestId) this.pending.delete(details.requestId);
  }

  sweepPending() {
    const deadline = this.now() - PENDING_TTL_MS;
    for (const [requestId, entry] of this.pending) {
      if (entry.at < deadline) this.pending.delete(requestId);
    }
  }
}

// 把 Sniffer 挂到真实的 chrome.webRequest 上。测试里不会走到这里。
export function attachSniffer(sniffer, chromeApi) {
  const filter = { urls: ['http://*/*', 'https://*/*'] };

  chromeApi.webRequest.onSendHeaders.addListener(
    (details) => sniffer.handleSendHeaders(details),
    filter,
    ['requestHeaders', 'extraHeaders']
  );
  chromeApi.webRequest.onHeadersReceived.addListener(
    (details) => sniffer.handleHeadersReceived(details),
    filter,
    ['responseHeaders', 'extraHeaders']
  );
  chromeApi.webRequest.onCompleted.addListener((details) => sniffer.finishRequest(details), filter);
  chromeApi.webRequest.onErrorOccurred.addListener((details) => sniffer.finishRequest(details), filter);
}
