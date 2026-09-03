// 隔离世界的中转：主世界挂钩与扩展后台之间唯一的通道。
//
// 主世界拿不到 chrome.* ，后台也够不着页面里的对象，两边只能靠 postMessage 传话。
// 这个文件不做任何判断，只转发——判断放在后台，页面里的代码越少越不容易踩坏站点。

const CHANNEL = 'cineinsight-grabber';

// 收集页面上所有可能是"视频叫什么"的来源，交给调用方去挑。
// 这里只取原始值，不做清洗——清洗规则在 common/title.js 里，那部分有单测。
function collectTitleCandidates() {
  const meta = (selector) => document.querySelector(selector)?.content?.trim() || '';
  const heading = document.querySelector('h1')?.textContent?.trim() || '';
  return {
    ogTitle: meta('meta[property="og:title"]'),
    twitterTitle: meta('meta[name="twitter:title"]') || meta('meta[property="twitter:title"]'),
    siteName: meta('meta[property="og:site_name"]'),
    heading: heading.slice(0, 200),
    documentTitle: (document.title || '').trim()
  };
}

// 从本 frame 里正在播的 <video> 上截一帧。
//
// 截图不需要绕主世界：内容脚本和页面共用 DOM，直接画就行。画布会不会被污染
// 取决于视频源的来源，与脚本在哪个世界无关——MSE 的 blob 源属于页面自己，
// 所以导得出图。
//
// 返回 null 表示"本 frame 没有可截的东西"，调用方据此不应答。
function captureFrame() {
  try {
    const videos = [...document.querySelectorAll('video')]
      .filter((video) => video.videoWidth > 0 && video.videoHeight > 0)
      // 页面上可能有好几个（广告、预览小窗），取画面最大的那个
      .sort((a, b) => b.videoWidth * b.videoHeight - a.videoWidth * a.videoHeight);
    if (videos.length === 0) return null;

    const video = videos[0];
    const canvas = document.createElement('canvas');
    canvas.width = 240;
    canvas.height = Math.max(1, Math.round((video.videoHeight / video.videoWidth) * 240));
    canvas.getContext('2d').drawImage(video, 0, 0, canvas.width, canvas.height);
    return { ok: true, dataUrl: canvas.toDataURL('image/jpeg', 0.7) };
  } catch {
    // 画布被污染（视频源是跨源直链）时会走到这里。这一条要回话：
    // 本 frame 确实有播放器，只是导不出图，让用户知道原因而不是干等。
    return { ok: false, error: '这个播放器的画面受同源限制，截不出来' };
  }
}

window.addEventListener('message', (event) => {
  // 只收本页面自己发的消息。要说清楚这一条挡住的是什么：它排除的是**别的窗口
  // 与 iframe**，页面内的任何脚本（包括第三方广告与统计脚本）都能伪造这类消息。
  // 所以下面转发出去的消息类型必须是无害的——只有 MSE 检测与捕获状态，后台据此
  // 顶多多出几条命中，命中数本来也有上限。真正的动作（下载、推送）一律不从这里进。
  if (event.source !== window) return;
  const data = event.data;
  if (!data || data.channel !== CHANNEL || data.direction !== 'to-extension') return;

  // 截图结果是回给发起方的，不进后台。
  if (data.type === 'frame-captured') {
    const pending = pendingFrames.get(data.requestId);
    if (pending) {
      pendingFrames.delete(data.requestId);
      pending(data.error ? { ok: false, error: data.error } : { ok: true, dataUrl: data.dataUrl });
    }
    return;
  }

  chrome.runtime.sendMessage(toBackgroundMessage(data)).catch(() => {
    // 后台正在重启，这类通知丢了不影响下一次。
  });
});

function toBackgroundMessage(data) {
  switch (data.type) {
    case 'mse-detected':
      return { type: 'mse-detected', mimeType: data.mimeType };
    case 'blob-source':
      return { type: 'blob-source', url: data.url, sourceKind: data.sourceKind, size: data.size };
    case 'mse-capture-status':
      return { type: 'capture-state', bytes: data.bytes, truncated: data.truncated, capturing: data.capturing };
    case 'mse-export-done':
      return { type: 'capture-state', exported: true, bytes: data.bytes, truncated: data.truncated };
    case 'mse-export-failed':
      return { type: 'capture-state', exportError: data.message };
    default:
      return { type: 'capture-state', raw: data.type };
  }
}

chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
  if (!message || typeof message.type !== 'string') return false;
  if (message.type === 'set-capture') {
    postToPage({ type: 'set-capture', enabled: Boolean(message.enabled) });
    sendResponse({ ok: true });
    return false;
  }
  if (message.type === 'export-capture') {
    postToPage({ type: 'export-capture', filename: message.filename });
    sendResponse({ ok: true });
    return false;
  }
  if (message.type === 'page-title') {
    // 只有顶层页面应答：视频标题这类元数据在外层文档上，播放器所在的 iframe
    // 通常什么都没有。不加这一条的话，先回话的 iframe 会把真答案挤掉——
    // 截图那边踩过同一个坑。
    if (window.top !== window) return false;
    sendResponse(collectTitleCandidates());
    return false;
  }
  if (message.type === 'capture-frame') {
    // 这类站点的播放器几乎都在 iframe 里。sendMessage 会发给页面的每一个 frame，
    // **最先回话的那个赢**——顶层页面没有 <video>，它抢先回一句"没有视频"，
    // 真正有播放器的那个 iframe 的回答就被丢掉了。
    //
    // 所以这里的规矩是：本 frame 里没有可截的画面就**不回话**，把应答机会留给
    // 有播放器的那个 frame。一个 frame 都没有的话，调用方那边的 promise 会失败，
    // 由它给出"页面上没有正在播放的视频"。
    const frame = captureFrame();
    if (!frame) return false;
    sendResponse(frame);
    return false;
  }
  return false;
});

function postToPage(payload) {
  window.postMessage({ channel: CHANNEL, direction: 'to-page', ...payload }, window.location.origin || '*');
}
