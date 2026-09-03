// service worker：嗅探接线、消息路由、任务调度的入口。
//
// MV3 会随时回收它，所以这里不存任何"丢了就回不来"的状态：命中是页面级的、
// 丢了重新嗅探即可；任务状态在 TaskController 里落盘。监听器必须在顶层同步注册，
// 否则 worker 被唤醒时会漏事件。

import { MSG, MEDIA_KIND, TASK_STATE } from '../common/constants.js';
import { DetectionStore } from './store.js';
import { Sniffer, attachSniffer } from './sniffer.js';
import { TaskController } from './task-controller.js';
import { HeaderRuleManager, hostsForTask } from './dnr-headers.js';
import { parsePlaylist } from '../common/hls/parser.js';
import { hostOf, extensionOf } from '../common/url.js';
import { loadSettings, saveSettings } from '../common/settings.js';
import { probeBridge, pushToBridge, playViaBridge } from '../common/bridge.js';
import { pruneOrphanFiles } from '../download/opfs-store.js';

const store = new DetectionStore();
const tasks = new TaskController(chrome);
const probeRules = new HeaderRuleManager(chrome);
const sniffer = new Sniffer({ store, onChange: (tabId) => updateBadge(tabId) });
// 页面报上来的捕获进展，只在内存里留最近一次：它是给弹窗看的即时状态，
// 丢了重新开一次捕获即可，不值得落盘。
const captureStates = new Map();

attachSniffer(sniffer, chrome);

chrome.tabs.onRemoved.addListener((tabId) => store.clearTab(tabId));

chrome.tabs.onUpdated.addListener((tabId, changeInfo, tab) => {
  // 地址变了算换了一个页面：旧命中留着只会误导人。
  if (changeInfo.url) {
    store.resetForNavigation(tabId, { url: changeInfo.url, title: tab.title || '' });
    updateBadge(tabId);
    return;
  }
  if (changeInfo.title) store.setPageInfo(tabId, { title: changeInfo.title, url: tab.url || '' });
});

chrome.tabs.onActivated.addListener(({ tabId }) => updateBadge(tabId));

chrome.downloads.onChanged.addListener((delta) => {
  tasks.handleDownloadChanged(delta).catch((error) => console.warn('下载状态处理失败', error));
});

chrome.runtime.onStartup.addListener(() => bootstrap());
chrome.runtime.onInstalled.addListener(() => bootstrap());

tasks.onChange(() => {
  chrome.runtime.sendMessage({ type: MSG.TASKS_CHANGED, target: 'ui' }).catch(() => {
    // 没有界面开着，没人听是正常的。
  });
});

chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
  if (!message || typeof message.type !== 'string') return false;
  // 发给 offscreen 的消息不归这里管，但 chrome 会广播给所有接收方。
  if (message.target === 'offscreen') return false;

  handleMessage(message, sender)
    .then((result) => sendResponse({ ok: true, data: result }))
    .catch((error) => sendResponse({ ok: false, error: error && error.message ? error.message : String(error) }));
  return true; // 异步回包
});

async function handleMessage(message, sender) {
  switch (message.type) {
    case MSG.LIST_DETECTIONS: {
      const tabId = message.tabId ?? sender.tab?.id;
      const listed = store.list(tabId);
      return { ...listed, tabId };
    }
    case MSG.CLEAR_TAB: {
      store.clearTab(message.tabId);
      updateBadge(message.tabId);
      return true;
    }
    case MSG.EXPAND_VARIANTS:
      return expandVariants(message.tabId, message.detectionId);
    case MSG.START_DOWNLOAD:
      return startDownload(message);
    case MSG.LIST_TASKS:
      await tasks.load();
      return tasks.list();
    case MSG.PAUSE_TASK:
      await tasks.load();
      await tasks.pause(message.taskId);
      return true;
    case MSG.RESUME_TASK:
      await tasks.load();
      await tasks.resume(message.taskId);
      return true;
    case MSG.CANCEL_TASK:
      await tasks.load();
      await tasks.cancel(message.taskId);
      return true;
    case MSG.REMOVE_TASK:
      await tasks.load();
      await tasks.remove(message.taskId);
      return true;
    case MSG.PROBE_BRIDGE:
      return probeBridgeCached(message.force);
    case MSG.PUSH_TO_BRIDGE:
      return pushDetectionToBridge(message);
    case MSG.PLAY_VIA_BRIDGE:
      return playDetectionViaBridge(message);
    case MSG.OFFSCREEN_PROGRESS:
      await tasks.handleReport(message);
      return true;
    case MSG.OFFSCREEN_HOSTS:
      await tasks.load();
      await tasks.applyHeaderHosts(message.taskId, message.hosts || []);
      return true;
    case MSG.OFFSCREEN_READY:
      return true;
    case MSG.MSE_DETECTED:
      return recordMse(sender, message);
    case MSG.SET_CAPTURE:
      return setCapture(message.tabId, message.enabled);
    case MSG.CAPTURE_STATE:
      // 页面报上来的捕获进展。留在最近状态里给弹窗看，不落盘。
      captureStates.set(sender.tab?.id, { ...message, at: Date.now() });
      return true;
    case MSG.BLOB_SOURCE:
      // blob: 地址与真实来源的对应关系。目前只用于判断"这条 blob 背后是 MSE"，
      // 真正的兜底走 MSE 捕获，所以这里只记不做别的。
      return true;
    case 'get-capture-state':
      return captureStates.get(message.tabId) || null;
    case 'get-settings':
      return loadSettings();
    case 'save-settings':
      return saveSettings(message.patch || {});
    default:
      throw new Error(`不认识的消息类型 ${message.type}`);
  }
}

// 展开一条 HLS 命中：取播放列表、解析。master 返回码率清单，media 返回时长与加密情况。
// 这也是"响应体特征"这条判定路线真正落地的地方——解析不出来就说明这条命中是假的。
async function expandVariants(tabId, detectionId) {
  const detection = store.get(tabId, detectionId);
  if (!detection) throw new Error('这条命中已经不在了，刷新页面重试');

  const probeId = `probe-${detectionId}`;
  await probeRules.apply(probeId, [hostOf(detection.url)], detection.headers);
  try {
    const response = await fetch(detection.url, { credentials: 'include' });
    if (!response.ok) throw new Error(`播放列表取不到：HTTP ${response.status}`);
    const text = await response.text();
    const playlist = parsePlaylist(text, detection.url);
    if (playlist.type === 'invalid') throw new Error(playlist.reason);

    if (playlist.type === 'master') {
      return {
        type: 'master',
        variants: playlist.variants.map((variant) => ({
          url: variant.uri,
          label: variant.label,
          bandwidth: variant.bandwidth || variant.averageBandwidth,
          resolution: variant.resolution,
          codecs: variant.codecs
        }))
      };
    }
    return {
      type: 'media',
      url: detection.url,
      durationSeconds: playlist.totalDuration,
      segmentCount: playlist.segments.length,
      isLive: playlist.isLive,
      outputExtension: playlist.outputExtension,
      encryption: playlist.encryption,
      hosts: hostsForTask({
        playlistUrl: detection.url,
        segmentUrls: playlist.segments.slice(0, 50).map((segment) => segment.uri),
        keyUrls: playlist.segments.filter((segment) => segment.key).slice(0, 5).map((segment) => segment.key.uri)
      })
    };
  } finally {
    await probeRules.clear(probeId);
  }
}

async function startDownload(message) {
  const detection = store.get(message.tabId, message.detectionId);
  const headers = detection ? detection.headers : message.headers || {};
  const url = message.url || (detection ? detection.url : '');
  if (!url) throw new Error('没有可下载的地址');

  await tasks.load();
  return tasks.create({
    kind: message.kind || (detection && detection.kind === MEDIA_KIND.FILE ? 'file' : 'hls'),
    url,
    title: message.title || (detection ? detection.pageTitle : ''),
    variantLabel: message.variantLabel || '',
    extension: message.extension || (message.kind === 'file' ? extensionOf(url) || 'mp4' : 'ts'),
    headers,
    totalBytes: message.totalBytes || (detection ? detection.contentLength : 0),
    pageUrl: detection ? detection.pageUrl : ''
  });
}

// 桥接端口探测有代价（最多 21 个端口），命中的端口缓存起来，失效时再重扫。
let cachedBridge = null;
async function probeBridgeCached(force) {
  if (cachedBridge && !force && Date.now() - cachedBridge.at < 30_000) return cachedBridge.result;
  const settings = await loadSettings();
  const result = await probeBridge({ preferredPort: settings.bridgePort, hasToken: Boolean(settings.bridgeToken) });
  cachedBridge = { at: Date.now(), result };
  if (result.available && result.port !== settings.bridgePort) {
    await saveSettings({ bridgePort: result.port });
  }
  return result;
}

async function pushDetectionToBridge(message) {
  const settings = await loadSettings();
  const bridge = await probeBridgeCached(false);
  if (!bridge.available) throw new Error('没有检测到正在运行的 CineInsight');
  if (!settings.bridgeToken) throw new Error('还没填配对令牌，去选项页填一下');

  const detection = store.get(message.tabId, message.detectionId);
  const cookie = settings.attachCookieOnPush && detection ? store.cookieFor(detection.id) : '';

  return pushToBridge({
    port: bridge.port,
    token: settings.bridgeToken,
    payload: {
      url: message.url,
      kind: message.kind || 'hls',
      title: message.title || (detection ? detection.pageTitle : ''),
      variant_label: message.variantLabel || '',
      page_url: detection ? detection.pageUrl : '',
      referer: detection ? detection.headers.referer : '',
      user_agent: detection ? detection.headers.userAgent : '',
      origin: detection ? detection.headers.origin : '',
      cookie
    }
  });
}

// 让桌面端直接播这条流。与推送下载共用同一套探测与令牌校验。
async function playDetectionViaBridge(message) {
  const settings = await loadSettings();
  const bridge = await probeBridgeCached(false);
  if (!bridge.available) throw new Error('没有检测到正在运行的 CineInsight');
  if (!settings.bridgeToken) throw new Error('还没填配对令牌，去选项页填一下');

  // 优先用调用方带来的请求头：service worker 被回收之后 store 里的命中就没了，
  // 只依赖 store 的话取到的是一组空头，源站认 Referer 时就会 403。
  const detection = store.get(message.tabId, message.detectionId);
  const headers = message.headers || (detection ? detection.headers : {}) || {};
  return playViaBridge({
    port: bridge.port,
    token: settings.bridgeToken,
    payload: {
      url: message.url,
      referer: headers.referer || '',
      origin: headers.origin || '',
      user_agent: headers.userAgent || ''
    }
  });
}

async function recordMse(sender, message) {
  const tabId = sender.tab?.id;
  if (tabId === undefined) return false;
  store.add(tabId, {
    kind: MEDIA_KIND.MSE,
    url: message.url || `mse://${tabId}/${message.mimeType || 'unknown'}`,
    extension: guessMseExtension(message.mimeType),
    mime: message.mimeType || ''
  });
  updateBadge(tabId);
  return true;
}

function guessMseExtension(mimeType) {
  const text = String(mimeType || '').toLowerCase();
  if (text.includes('webm')) return 'webm';
  if (text.includes('mp2t')) return 'ts';
  return 'mp4';
}

async function setCapture(tabId, enabled) {
  await chrome.tabs.sendMessage(tabId, { type: MSG.SET_CAPTURE, enabled: Boolean(enabled) });
  return true;
}

function updateBadge(tabId) {
  if (tabId === undefined || tabId < 0) return;
  const count = store.count(tabId);
  chrome.action.setBadgeText({ tabId, text: count > 0 ? String(count) : '' }).catch(() => {});
  chrome.action.setBadgeBackgroundColor({ tabId, color: '#2f6feb' }).catch(() => {});
}

async function bootstrap() {
  await tasks.load();
  const liveIds = tasks
    .list()
    .filter((task) => task.state !== TASK_STATE.DONE && task.state !== TASK_STATE.CANCELED)
    .map((task) => task.id);
  // 崩溃或强制退出会留下没人认领的 .part 文件，启动时清一遍。
  pruneOrphanFiles(liveIds).catch(() => {});
}

bootstrap().catch((error) => console.warn('启动初始化失败', error));
