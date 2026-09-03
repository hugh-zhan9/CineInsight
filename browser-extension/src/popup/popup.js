// 弹窗：当前标签页抓到了什么，以及对每一条能做什么。
//
// 弹窗一失焦就关，所以这里只负责发起动作，不承担任何长任务的状态——
// 进度去下载管理页看。

import { MSG, MEDIA_KIND } from '../common/constants.js';
import { formatBytes, formatDuration, formatBitrate, estimateSize } from '../common/format.js';
import { buildFfmpegCommand } from '../common/ffmpeg-command.js';
import { buildFilename, sanitizeFilename, basenameOf } from '../common/url.js';
import { generateThumbnail } from './thumbnail.js';
import { pickMediaTitle } from '../common/title.js';

// 从页面里正在播的播放器上截一帧。MSE 这类没有可取地址的流只有这一条路：
// blob 源属于页面自己，画布不会被污染，所以截得出来。
async function captureFromPage(tabId) {
  const reply = await chrome.tabs.sendMessage(tabId, { type: 'capture-frame' });
  if (!reply || !reply.ok) throw new Error((reply && reply.error) || '页面没有响应');
  return reply.dataUrl;
}

// 跑一次预览。auto=true 是自动那一轮：失败就安静地把按钮留着让用户自己点，
// 不弹红字——自动失败没必要打断人，手动点了才需要知道原因。
async function runPreview(button, box, produce, auto = false) {
  if (!button || button.dataset.done === '1') return;
  button.disabled = true;
  button.textContent = '生成中…';
  try {
    const dataUrl = await produce();
    button.dataset.done = '1';
    showThumb(box, dataUrl, button);
  } catch (error) {
    button.disabled = false;
    button.textContent = '预览';
    if (!auto) showMessage(`生成预览失败：${error.message || error}`, true);
  }
}

// 自动生成：顺序跑，且有上限。每张图都要真的下一段数据，
// 一开弹窗就并发给所有命中都下一遍等于替用户白烧流量。
async function runPreviewJobs() {
  for (const job of previewJobs.slice(0, AUTO_PREVIEW_LIMIT)) {
    await job();
  }
}

// 把一张缩略图放进卡片，并撤掉那个按钮。
function showThumb(box, dataUrl, button) {
  const image = document.createElement('img');
  image.src = dataUrl;
  image.className = 'thumb';
  box.replaceChildren(image);
  if (button) button.remove();
}

const listEl = document.getElementById('list');
const emptyEl = document.getElementById('empty');
const messageEl = document.getElementById('message');
const bridgeEl = document.getElementById('bridge-status');

let currentTab = null;
let bridgeState = { available: false, message: '正在检测 CineInsight…' };
let settings = null;
// 页面自己声明的视频名。比 document.title 准得多——后者几乎总带着站点后缀，
// 没有 <title> 的页面浏览器还会拿地址充数。
let pageTitle = '';
// 本轮渲染要自动生成的预览。顺序执行——每张图都要真的下一段数据，
// 并发会把带宽和站点一起打爆。
let previewJobs = [];
// 自动生成的上限：命中很多时不为每一条都去下一段数据。
const AUTO_PREVIEW_LIMIT = 6;

document.getElementById('open-downloads').addEventListener('click', () => {
  chrome.tabs.create({ url: chrome.runtime.getURL('src/downloads-page/downloads.html') });
});
document.getElementById('open-options').addEventListener('click', () => chrome.runtime.openOptionsPage());

init().catch((error) => showMessage(String(error.message || error), true));

async function init() {
  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  currentTab = tab;
  settings = await send({ type: 'get-settings' });
  pageTitle = await resolvePageTitle(tab.id);
  await render();
  probeBridge();
  runPreviewJobs();
}

// 问页面要一个像样的视频名。拿不到就返回空串，由调用方退到别的来源。
async function resolvePageTitle(tabId) {
  try {
    const candidates = await chrome.tabs.sendMessage(tabId, { type: 'page-title' });
    return candidates ? pickMediaTitle(candidates) : '';
  } catch {
    // 内容脚本没注入（比如 chrome:// 页面）不是错误。
    return '';
  }
}

async function send(message) {
  const response = await chrome.runtime.sendMessage(message);
  if (!response) throw new Error('后台没有响应，试着重载扩展');
  if (!response.ok) throw new Error(response.error);
  return response.data;
}

async function probeBridge() {
  bridgeEl.hidden = false;
  bridgeEl.textContent = '正在检测 CineInsight…';
  try {
    bridgeState = await send({ type: MSG.PROBE_BRIDGE });
  } catch (error) {
    bridgeState = { available: false, message: `检测失败：${error.message}` };
  }
  bridgeEl.textContent = bridgeState.message;
  bridgeEl.classList.toggle('ok', Boolean(bridgeState.available));
  // 入口的可用状态跟着检测结果走，理由直接写在按钮的 title 上。
  for (const button of listEl.querySelectorAll('[data-push]')) {
    button.disabled = !bridgeState.available;
    button.title = bridgeState.available ? '交给 CineInsight 下载并入库' : bridgeState.message;
  }
}

async function render() {
  const data = await send({ type: MSG.LIST_DETECTIONS, tabId: currentTab.id });
  listEl.replaceChildren();

  previewJobs = [];
  // 下不了的条目默认不占地方：页面内嵌流（没有可下载的地址）与缺清单的分片
  // （不知道总共多少片、什么顺序，拼不出完整视频）。选项页里可以打开。
  const showAll = settings ? settings.showUndownloadable : false;
  const items = (data.items || []).filter((item) => showAll || item.kind !== MEDIA_KIND.MSE);
  const orphans = showAll ? (data.orphanSegments || []) : [];
  emptyEl.hidden = items.length > 0 || orphans.length > 0;

  for (const item of items) listEl.appendChild(renderDetection(item, data.page));
  for (const group of orphans) listEl.appendChild(renderOrphan(group));
}

function renderDetection(detection, page) {
  const card = document.createElement('div');
  card.className = 'card';

  const head = document.createElement('div');
  head.className = 'card-head';
  head.appendChild(tag(kindLabel(detection.kind), detection.kind === MEDIA_KIND.DASH || detection.kind === MEDIA_KIND.MSE ? 'tag tag-warn' : 'tag'));

  const title = document.createElement('strong');
  title.className = 'truncate';
  title.textContent = detection.pageTitle || page.title || detection.url;
  head.appendChild(title);
  card.appendChild(head);

  const url = document.createElement('div');
  url.className = 'muted small mono truncate url';
  url.textContent = detection.url;
  url.title = detection.url;
  card.appendChild(url);

  const meta = [];
  if (detection.contentLength) meta.push(formatBytes(detection.contentLength));
  if (detection.acceptsRanges) meta.push('支持断点续传');
  if (detection.hits > 1) meta.push(`命中 ${detection.hits} 次`);
  if (detection.hasCookie) meta.push('带登录状态');
  if (meta.length > 0) {
    const metaEl = document.createElement('div');
    metaEl.className = 'muted small';
    metaEl.textContent = meta.join(' · ');
    card.appendChild(metaEl);
  }

  // 文件名可改：自动推出来的名字常常是站点标题或一串 URL 片段，没法看。
  // 一张卡片一个输入框，卡片里所有下载与推送都用它，码率后缀与扩展名仍由下游拼。
  // 取名优先级：页面自己声明的视频名 > 抓到命中时的页面标题 > 地址末段。
  // 最后那一档是兜底，出来的东西通常不好看，所以旁边给了可编辑的输入框。
  const defaultStem = sanitizeFilename(pageTitle || detection.pageTitle || page.title || basenameOf(detection.url));
  const nameRow = document.createElement('div');
  nameRow.className = 'row';
  nameRow.style.marginTop = '8px';
  const nameLabel = document.createElement('span');
  nameLabel.className = 'muted small';
  nameLabel.textContent = '文件名';
  const nameInput = document.createElement('input');
  nameInput.type = 'text';
  nameInput.value = defaultStem;
  nameInput.spellcheck = false;
  nameInput.title = '下载时使用的文件名（不含扩展名）';
  nameInput.style.flex = '1';
  nameRow.appendChild(nameLabel);
  nameRow.appendChild(nameInput);
  card.appendChild(nameRow);
  // 清干净之后为空就退回自动推出来的名字，不让下游拼出一个空名字。
  const chosenName = () => sanitizeFilename(nameInput.value) || defaultStem;

  // 缩略图：一个页面上抓到好几条流时，光看地址分不清谁是谁。
  // 只在点了「预览」之后才生成——每张图都要真的下一个分片。
  const thumbBox = document.createElement('div');
  thumbBox.className = 'thumb-box';
  card.appendChild(thumbBox);

  const actions = document.createElement('div');
  actions.className = 'row';
  actions.style.marginTop = '8px';

  if (detection.kind !== MEDIA_KIND.MSE && detection.kind !== MEDIA_KIND.DASH) {
    const previewButton = button('预览', '', () => runPreview(previewButton, thumbBox, () => generateThumbnail(detection)));
    actions.appendChild(previewButton);
    previewJobs.push(() => runPreview(previewButton, thumbBox, () => generateThumbnail(detection), true));
  }

  if (detection.kind === MEDIA_KIND.HLS) {
    actions.appendChild(button('查看码率并下载', 'primary', async (btn) => {
      btn.disabled = true;
      btn.textContent = '解析中…';
      try {
        await expand(card, detection, chosenName);
        btn.remove();
      } catch (error) {
        btn.disabled = false;
        btn.textContent = '查看码率并下载';
        showMessage(error.message, true);
      }
    }));
  } else if (detection.kind === MEDIA_KIND.FILE) {
    actions.appendChild(button('下载', 'primary', () => startDownload({
      detection,
      url: detection.url,
      kind: 'file',
      extension: detection.extension,
      totalBytes: detection.contentLength,
      title: chosenName()
    })));
  } else if (detection.kind === MEDIA_KIND.MSE) {
    const mseButton = button('预览', '', () => runPreview(mseButton, thumbBox, () => captureFromPage(currentTab.id)));
    actions.appendChild(mseButton);
    previewJobs.push(() => runPreview(mseButton, thumbBox, () => captureFromPage(currentTab.id), true));
    actions.appendChild(button('开启捕获', '', async (btn) => {
      await send({ type: MSG.SET_CAPTURE, tabId: currentTab.id, enabled: true });
      btn.textContent = '已开启，请从头重播';
      btn.disabled = true;
      showMessage('捕获已开启。需要把视频从头重新播放一遍才能录到完整内容，播完在这里点「导出」。');
    }));
    actions.appendChild(button('导出', '', async () => {
      await chrome.tabs.sendMessage(currentTab.id, {
        type: 'export-capture',
        filename: buildFilename({ title: chosenName(), extension: detection.extension || 'mp4' })
      });
      showMessage('已让页面保存录到的内容，去浏览器下载列表看。');
    }));
  } else if (detection.kind === MEDIA_KIND.DASH) {
    const note = document.createElement('div');
    note.className = 'muted small';
    note.textContent = 'DASH 流内置下载器不支持，可以复制命令用 ffmpeg 下，或推送给 CineInsight。';
    card.appendChild(note);
  }

  if (detection.kind === MEDIA_KIND.MSE) {
    const note = document.createElement('div');
    note.className = 'muted small';
    note.style.marginTop = '4px';
    note.textContent = '页面把视频数据直接喂给播放器，没有可以下载的地址。要拿到它，'
      + '得开启捕获再从头重播一遍；「预览」是直接从播放器上截的图。';
    card.appendChild(note);
  }

  actions.appendChild(button('复制链接', '', () => copy(detection.url, '链接已复制')));
  actions.appendChild(button('复制 ffmpeg', '', () => copy(
    buildFfmpegCommand({
      url: detection.url,
      headers: detection.headers,
      filename: buildFilename({ title: chosenName(), url: detection.url, extension: 'mp4' })
    }),
    'ffmpeg 命令已复制'
  )));

  if (detection.kind !== MEDIA_KIND.MSE) {
    const play = button('用 IINA 播放', '', () => playInIINA({ detection, url: detection.url }));
    play.dataset.push = '1';
    play.disabled = !bridgeState.available;
    play.title = bridgeState.available ? '交给本机 IINA 直接播放，不下载' : bridgeState.message;
    actions.appendChild(play);

    const push = button('发送到 CineInsight', '', () => pushToBridge({
      detection, url: detection.url, kind: detection.kind === MEDIA_KIND.FILE ? 'file' : 'hls', title: chosenName()
    }));
    push.dataset.push = '1';
    push.disabled = !bridgeState.available;
    push.title = bridgeState.message || '';
    actions.appendChild(push);
  }

  card.appendChild(actions);
  return card;
}

function renderOrphan(group) {
  const card = document.createElement('div');
  card.className = 'card';
  const head = document.createElement('div');
  head.className = 'card-head';
  head.appendChild(tag('TS 分片', 'tag tag-muted'));
  const title = document.createElement('strong');
  title.textContent = `抓到 ${group.count} 个视频分片，但缺少清单文件`;
  head.appendChild(title);
  card.appendChild(head);

  const hint = document.createElement('div');
  hint.className = 'muted small mono truncate';
  hint.textContent = group.hint;
  card.appendChild(hint);

  const note = document.createElement('div');
  note.className = 'muted small';
  note.style.marginTop = '4px';
  note.textContent = '视频被切成很多小片，而列出这些片的清单（.m3u8）没被抓到——'
    + '它多半在扩展开始监听之前就取过了。没有清单就不知道总共多少片、什么顺序，拼不出完整视频。'
    + '刷新一下这个页面通常就能抓到。';
  card.appendChild(note);

  const thumbBox = document.createElement('div');
  thumbBox.className = 'thumb-box';
  card.appendChild(thumbBox);

  const actions = document.createElement('div');
  actions.className = 'row';
  actions.style.marginTop = '8px';
  const orphanButton = button('预览', '', () => runPreview(orphanButton, thumbBox, () => captureFromPage(currentTab.id)));
  actions.appendChild(orphanButton);
  previewJobs.push(() => runPreview(orphanButton, thumbBox, () => captureFromPage(currentTab.id), true));
  actions.appendChild(button('刷新页面重抓', '', async () => {
    await chrome.tabs.reload(currentTab.id);
    window.close();
  }));
  actions.appendChild(button('复制分片地址', '', () => copy(group.sampleUrl, '分片地址已复制')));
  card.appendChild(actions);
  return card;
}

async function expand(card, detection, chosenName) {
  const result = await send({ type: MSG.EXPAND_VARIANTS, tabId: currentTab.id, detectionId: detection.id });
  const container = document.createElement('div');
  container.className = 'variant-list';

  if (result.type === 'master') {
    for (const variant of result.variants) {
      container.appendChild(renderVariant(detection, variant, chosenName));
    }
  } else {
    container.appendChild(renderMediaSummary(detection, result, chosenName));
  }
  card.appendChild(container);
}

function renderVariant(detection, variant, chosenName) {
  const row = document.createElement('div');
  row.className = 'variant';

  const label = document.createElement('strong');
  label.textContent = variant.label;
  row.appendChild(label);

  const meta = document.createElement('span');
  meta.className = 'muted small';
  const parts = [];
  if (variant.resolution) parts.push(`${variant.resolution.width}×${variant.resolution.height}`);
  if (variant.bandwidth) parts.push(formatBitrate(variant.bandwidth));
  meta.textContent = parts.join(' · ');
  row.appendChild(meta);

  const actions = document.createElement('div');
  actions.className = 'row row-end';
  actions.appendChild(button('下载', 'primary', () => startDownload({
    detection,
    url: variant.url,
    kind: 'hls',
    variantLabel: variant.label,
    // HLS 一律出 mp4：TS 分片会在下完之后转封装（无损，不重新编码）。
    extension: 'mp4',
    title: chosenName()
  })));
  const play = button('用 IINA 播放', '', () => playInIINA({ detection, url: variant.url }));
  play.dataset.push = '1';
  play.disabled = !bridgeState.available;
  play.title = bridgeState.available ? '交给本机 IINA 直接播放，不下载' : bridgeState.message;
  actions.appendChild(play);

  const push = button('发送到 CineInsight', '', () => pushToBridge({
    detection, url: variant.url, kind: 'hls', variantLabel: variant.label, title: chosenName()
  }));
  push.dataset.push = '1';
  push.disabled = !bridgeState.available;
  push.title = bridgeState.message || '';
  actions.appendChild(push);
  row.appendChild(actions);
  return row;
}

function renderMediaSummary(detection, media, chosenName) {
  const row = document.createElement('div');
  row.className = 'variant';

  const info = document.createElement('div');
  const parts = [`${media.segmentCount} 个分片`];
  if (media.durationSeconds) parts.push(formatDuration(media.durationSeconds));
  if (media.isLive) parts.push('直播流');
  if (media.encryption && media.encryption.method === 'AES-128') parts.push('AES-128 加密');
  info.innerHTML = `<strong>${parts.join(' · ')}</strong>`;

  if (!media.encryption.supported) {
    const bad = document.createElement('div');
    bad.className = 'small';
    bad.style.color = 'var(--danger)';
    bad.textContent = media.encryption.reason;
    info.appendChild(bad);
  } else if (media.isLive) {
    const note = document.createElement('div');
    note.className = 'muted small';
    note.textContent = '直播流没有终点，内置下载器不接；可以复制 ffmpeg 命令自己录。';
    info.appendChild(note);
  }
  row.appendChild(info);

  const actions = document.createElement('div');
  actions.className = 'row row-end';
  const canDownload = media.encryption.supported && !media.isLive;
  const downloadButton = button('下载', 'primary', () => startDownload({
    detection,
    url: media.url,
    kind: 'hls',
    extension: 'mp4',
    title: chosenName()
  }));
  downloadButton.disabled = !canDownload;
  if (!canDownload) downloadButton.title = media.encryption.supported ? '直播流不支持' : media.encryption.reason;
  actions.appendChild(downloadButton);
  row.appendChild(actions);
  return row;
}

async function startDownload({ detection, url, kind, variantLabel, extension, totalBytes, title }) {
  try {
    await send({
      type: MSG.START_DOWNLOAD,
      tabId: currentTab.id,
      detectionId: detection.id,
      url,
      kind,
      variantLabel,
      extension,
      totalBytes,
      title: title || detection.pageTitle || currentTab.title
    });
    showMessage('已加入下载队列，进度在「下载管理」里看。');
  } catch (error) {
    showMessage(error.message, true);
  }
}

// 用本机 IINA 直接播，不下载。走桥接是因为只有桌面端能把 Referer 一起带给播放器；
// 浏览器直接开 iina:// 带不了请求头，这类站点会 403。
async function playInIINA({ detection, url }) {
  try {
    // 把命中里的请求头一起发过去：service worker 随时可能被回收，回收后它内存里的
    // 命中就没了，后台再去查会查到空——日志里那条 referer="" 就是这么来的。
    await send({
      type: MSG.PLAY_VIA_BRIDGE,
      tabId: currentTab.id,
      detectionId: detection.id,
      url,
      headers: detection.headers
    });
    showMessage('已交给 IINA 播放。');
  } catch (error) {
    showMessage(error.message, true);
  }
}

async function pushToBridge({ detection, url, kind, variantLabel, title }) {
  try {
    const result = await send({
      type: MSG.PUSH_TO_BRIDGE,
      tabId: currentTab.id,
      detectionId: detection.id,
      url,
      kind,
      variantLabel,
      title: title || detection.pageTitle || currentTab.title
    });
    showMessage(`已交给 CineInsight：${(result && result.filename) || '任务已建立'}`);
  } catch (error) {
    showMessage(error.message, true);
  }
}

async function copy(text, okMessage) {
  await navigator.clipboard.writeText(text);
  showMessage(okMessage);
}

function showMessage(text, isError = false) {
  messageEl.hidden = false;
  messageEl.textContent = text;
  messageEl.classList.toggle('error', Boolean(isError));
}

function button(label, extraClass, onClick) {
  const el = document.createElement('button');
  el.textContent = label;
  if (extraClass) el.className = extraClass;
  el.addEventListener('click', () => onClick(el));
  return el;
}

function tag(text, className) {
  const el = document.createElement('span');
  el.className = className;
  el.textContent = text;
  return el;
}

function kindLabel(kind) {
  switch (kind) {
    case MEDIA_KIND.HLS: return 'HLS';
    case MEDIA_KIND.FILE: return '直链';
    case MEDIA_KIND.DASH: return 'DASH';
    // 不能只写 "MSE"：那三个字母对用户没有任何意义。
    case MEDIA_KIND.MSE: return '页面内嵌流';
    default: return kind;
  }
}
