// 下载 worker：一个任务一个。真正取数据、解密、写 OPFS 的地方。
//
// 逻辑主体在 src/download/ 下的纯模块里（那部分有单测），这里只负责把它们接到
// 真实的 fetch 与 OPFS 上，并处理暂停/取消。

import { parsePlaylist } from '../common/hls/parser.js';
import { downloadHls, downloadFile, DownloadStopped } from '../download/hls-downloader.js';
import { SequentialWriteLedger } from '../download/ledger.js';
import { openTaskWriter, readTaskFile } from '../download/opfs-store.js';
import { remuxTsToMp4 } from '../download/remux.js';
import { hostsForTask } from '../background/dnr-headers.js';

let stopSignal = null; // null | 'paused' | 'canceled'
let controller = new AbortController();
let currentLedger = null;

self.onmessage = async (event) => {
  const data = event.data || {};
  if (data.type === 'control') {
    stopSignal = data.action === 'pause' ? 'paused' : 'canceled';
    controller.abort();
    return;
  }
  if (data.type === 'hosts-ready') {
    resolveHostsAck();
    return;
  }
  if (data.type !== 'start') return;

  stopSignal = null;
  controller = new AbortController();
  try {
    await runTask(data.task);
  } catch (error) {
    if (error instanceof DownloadStopped || stopSignal) {
      post({ type: 'stopped', reason: stopSignal || 'canceled', progress: progressSnapshot() });
    } else {
      post({ type: 'error', message: describeError(error), progress: progressSnapshot() });
    }
  }
};

async function runTask(task) {
  const settings = task.settings || {};
  const resume = task.resume && task.resume.bytesWritten > 0 ? task.resume : null;

  const writer = await openTaskWriter(task.id, { truncateTo: resume ? resume.bytesWritten : 0 });
  let outcome;
  try {
    outcome = task.kind === 'hls'
      ? await runHls(task, settings, resume, writer)
      : await runDirect(task, settings, resume, writer);
    writer.close();
  } catch (error) {
    // 先关句柄再往外抛：句柄不关，下次续跑打不开同一个文件。
    try { writer.close(); } catch { /* 句柄已经坏了，没有别的补救 */ }
    throw error;
  }

  // MPEG-TS 要转成 mp4 再交付。转封装单独一趟、不掺在下载里：下载阶段落的是
  // 原始 TS，断点续传因此完好；边下边转的话续传要从头重放整个转封装状态机。
  let variant = '';
  if (outcome.needsRemux) {
    post({ type: 'remuxing' });
    await remuxTaskFile(task, outcome.snapshot);
    variant = 'mp4';
  }

  const snapshot = outcome.snapshot;
  post({
    type: 'done',
    variant,
    progress: { ...snapshot, completedItems: snapshot.nextIndex, totalItems: snapshot.totalItems }
  });
}

// mux.js 是 UMD，且要一个 window 全局；worker 里没有 window，所以先把它指到 self 上
// 再动态载入。用动态 import 而不是顶层 import：顶层 import 会被提升到赋值之前执行。
let muxjsPromise = null;
function loadMuxjs() {
  if (!muxjsPromise) {
    muxjsPromise = (async () => {
      if (!self.window) self.window = self;
      await import('../../vendor/mux-mp4.min.js');
      if (!self.muxjs) throw new Error('转封装库加载失败');
      return self.muxjs;
    })();
  }
  return muxjsPromise;
}

// 把已经下好的原始 TS 转成 mp4，写进同一个任务的第二份产物里。
// 流式读写，不把整份文件读进内存。
async function remuxTaskFile(task, snapshot) {
  const muxjs = await loadMuxjs();
  const source = await readTaskFile(task.id, '');
  const out = await openTaskWriter(task.id, { truncateTo: 0, variant: 'mp4' });
  try {
    await remuxTsToMp4({
      muxjs,
      totalBytes: source.size,
      readChunk: async (offset, length) => {
        if (offset >= source.size) return new Uint8Array(0);
        const slice = source.slice(offset, Math.min(offset + length, source.size));
        return new Uint8Array(await slice.arrayBuffer());
      },
      write: (bytes) => out.write(bytes),
      onProgress: (progress) => post({ type: 'remux-progress', progress, snapshot }),
      shouldStop: () => stopSignal
    });
    out.close();
  } catch (error) {
    try { out.close(); } catch { /* 已经坏了 */ }
    throw error;
  }
}

async function runHls(task, settings, resume, writer) {
  const playlistText = await fetchText(task.url);
  const playlist = parsePlaylist(playlistText, task.url);
  if (playlist.type === 'invalid') throw new Error(playlist.reason);
  if (playlist.type === 'master') {
    // 走到这里说明界面把 master 地址当成可下载的了。展开是界面的职责，
    // 这里不替它挑一条码率——那等于替用户做主。
    throw new Error('这是一份多码率清单，请先选择具体码率');
  }
  if (playlist.isLive) {
    throw new Error('这是直播流，没有终点，暂不支持下载');
  }

  // 续跑之前先确认还是同一份播放列表。断点是"第几片"，而这个序号只有在分片清单
  // 没变的前提下才有意义：地址重新取回来可能换了一档码率、或者重新编过号，那时按
  // 旧序号接着下，出来的文件是两条流拼在一起的，而且长度检查照样过。
  if (resume && resume.totalItems && resume.totalItems !== playlist.segments.length) {
    throw new Error(
      `播放列表已经变了（原 ${resume.totalItems} 片，现在 ${playlist.segments.length} 片），断点作废，请取消后重新下载`
    );
  }
  // 分片与密钥常常不在播放列表那个域名下。请求头规则必须按真实的主机集合装，
  // 只按播放列表主机装的话，跨域的分片一个头都带不上，直接 403。
  await requestHeaderRules(hostsForTask({
    playlistUrl: task.url,
    segmentUrls: playlist.segments.map((segment) => segment.uri),
    keyUrls: playlist.segments.filter((segment) => segment.key).map((segment) => segment.key.uri)
  }));

  currentLedger = resume
    ? SequentialWriteLedger.restore({ ...resume, totalItems: playlist.segments.length })
    : new SequentialWriteLedger({ totalItems: playlist.segments.length });

  const snapshot = await downloadHls({
    playlist,
    ledger: currentLedger,
    writer,
    concurrency: settings.concurrency,
    retries: settings.retries,
    backoffMs: settings.retryBackoffMs,
    fetchBytes: (url, options) => fetchBytes(url, options),
    fetchKey: (url) => fetchBytes(url, {}),
    onProgress: (progress) => post({ type: 'progress', progress }),
    shouldStop: () => stopSignal
  });
  // 只有 MPEG-TS 需要转：fMP4 拼出来本来就是 mp4。
  return { snapshot, needsRemux: playlist.containerHint === 'ts' };
}

async function runDirect(task, settings, resume, writer) {
  const probe = await probeRange(task.url);
  const totalBytes = task.totalBytes || probe.totalBytes;
  const chunkSize = settings.chunkSize || 4 * 1024 * 1024;
  const totalChunks = probe.supportsRange && totalBytes ? Math.ceil(totalBytes / chunkSize) : 1;

  currentLedger = resume
    ? SequentialWriteLedger.restore({ ...resume, totalItems: totalChunks })
    : new SequentialWriteLedger({ totalItems: totalChunks });

  const snapshot = await downloadFile({
    url: task.url,
    totalBytes,
    supportsRange: probe.supportsRange,
    chunkSize,
    ledger: currentLedger,
    writer,
    concurrency: Math.min(settings.concurrency || 4, 8),
    retries: settings.retries,
    backoffMs: settings.retryBackoffMs,
    fetchBytes: (url, options) => fetchBytes(url, options),
    fetchWhole: (url) => fetchBytes(url, {}),
    onProgress: (progress) => post({ type: 'progress', progress }),
    shouldStop: () => stopSignal
  });
  // 直链是什么容器就存什么，不转。
  return { snapshot, needsRemux: false };
}

// 探测服务端支不支持范围请求。用 Range: bytes=0-0 而不是 HEAD：
// 不少 CDN 的 HEAD 要么不实现要么不回 Accept-Ranges，但对 Range 会老实回 206。
async function probeRange(url) {
  const response = await fetch(url, {
    method: 'GET',
    headers: { Range: 'bytes=0-0' },
    credentials: 'include',
    signal: controller.signal
  });
  if (!response.ok) throw new Error(`探测失败：HTTP ${response.status}`);
  // 只看状态与响应头，正文一律丢弃。忽略 Range 的服务端会回 200 加整个文件，
  // 用 arrayBuffer() 读它等于把几个 GB 塞进内存——正是用 OPFS 要避开的事。
  try {
    await response.body?.cancel();
  } catch {
    // 已经关掉的流再取消不是错误。
  }

  const contentRange = response.headers.get('content-range') || '';
  const match = /\/(\d+)\s*$/.exec(contentRange);
  if (response.status === 206 && match) {
    return { supportsRange: true, totalBytes: Number(match[1]) };
  }
  const length = Number(response.headers.get('content-length') || 0);
  return { supportsRange: false, totalBytes: Number.isFinite(length) ? length : 0 };
}

async function fetchBytes(url, { byteRange } = {}) {
  const headers = {};
  if (byteRange) {
    const end = byteRange.offset + byteRange.length - 1;
    headers.Range = `bytes=${byteRange.offset}-${end}`;
  }
  const response = await fetch(url, { credentials: 'include', headers, signal: controller.signal });
  if (!response.ok) throw new Error(`HTTP ${response.status} ${response.statusText || ''}`.trim());
  return new Uint8Array(await response.arrayBuffer());
}

async function fetchText(url) {
  const response = await fetch(url, { credentials: 'include', signal: controller.signal });
  if (!response.ok) throw new Error(`播放列表取不到：HTTP ${response.status}`);
  return response.text();
}

let hostsAck = null;

function resolveHostsAck() {
  if (!hostsAck) return;
  const resolve = hostsAck;
  hostsAck = null;
  resolve();
}

// 回报主机集合并等 service worker 把规则装好。
// 有意带超时：确认消息丢了就继续下载，取不到分片会以 403 明确失败，
// 比让任务永远停在这里强。
function requestHeaderRules(hosts) {
  return new Promise((resolve) => {
    hostsAck = resolve;
    post({ type: 'hosts', hosts });
    setTimeout(resolveHostsAck, 5000);
  });
}

function progressSnapshot() {
  if (!currentLedger) return null;
  const snapshot = currentLedger.snapshot();
  return { ...snapshot, completedItems: snapshot.nextIndex };
}

function describeError(error) {
  if (!error) return '未知错误';
  if (error.name === 'AbortError') return '请求被中断';
  return error.message || String(error);
}

function post(message) {
  self.postMessage(message);
}
