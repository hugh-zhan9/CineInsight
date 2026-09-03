// 给抓到的流生成一张缩略图。
//
// 网页上的流没有现成的缩略图，只能自己解一帧出来。路子是：取第一个分片 → 需要的话
// 转成 mp4 → 交给一个 <video> 元素解码 → 画到 canvas 上导出。
//
// 为什么不直接把原始地址塞给 <video>：跨源的视频画到 canvas 上会污染画布，
// toDataURL 会抛安全错误，一张图也导不出来。自己 fetch 成 blob 之后源就是扩展自己，
// 画布不再被污染。
//
// 只在用户点「预览」时生成：每张图都要真的下一个分片，自动给每条命中都生成
// 等于一开弹窗就偷偷下一堆数据。

import { parsePlaylist } from '../common/hls/parser.js';
import { remuxTsToMp4 } from '../download/remux.js';
import { MEDIA_KIND } from '../common/constants.js';

const THUMB_WIDTH = 240;
const THUMB_HEIGHT = 135;
// 直链只取开头这么多：够解出第一帧就行，不为了一张缩略图把整部片子拉下来。
const DIRECT_PROBE_BYTES = 3 * 1024 * 1024;

let muxjsPromise = null;
async function loadMuxjs() {
  if (!muxjsPromise) {
    muxjsPromise = (async () => {
      // 弹窗是正经文档，window 本来就有，不需要 worker 里那个垫片。
      await import('../../vendor/mux-mp4.min.js');
      return globalThis.muxjs;
    })();
  }
  return muxjsPromise;
}

async function fetchBytes(url, byteRange) {
  const headers = {};
  if (byteRange) headers.Range = `bytes=${byteRange.offset}-${byteRange.offset + byteRange.length - 1}`;
  const response = await fetch(url, { credentials: 'include', headers });
  if (!response.ok) throw new Error(`取样失败：HTTP ${response.status}`);
  return new Uint8Array(await response.arrayBuffer());
}

// 取一段能被 <video> 解码的 mp4 字节。
async function buildPreviewBytes(detection) {
  if (detection.kind === MEDIA_KIND.FILE) {
    // 直链：只取开头一段试试。moov 在文件末尾的 mp4 解不出来，那种情况下就没有缩略图，
    // 不为了一张图把整部片子拉下来。
    return fetchBytes(detection.url, { offset: 0, length: DIRECT_PROBE_BYTES });
  }

  const text = await (await fetch(detection.url, { credentials: 'include' })).text();
  let playlist = parsePlaylist(text, detection.url);
  if (playlist.type === 'invalid') throw new Error(playlist.reason);

  // master 清单先挑一条码率——挑最低的那条，缩略图不需要 4K。
  if (playlist.type === 'master') {
    const variant = playlist.variants[playlist.variants.length - 1];
    const mediaText = await (await fetch(variant.uri, { credentials: 'include' })).text();
    playlist = parsePlaylist(mediaText, variant.uri);
    if (playlist.type !== 'media') throw new Error('这条清单展不开，取不到样');
  }
  if (!playlist.encryption.supported) throw new Error(playlist.encryption.reason);
  if (playlist.segments.length === 0) throw new Error('清单里没有分片');
  if (playlist.segments[0].key) throw new Error('加密流暂不生成预览');

  const first = playlist.segments[0];
  const segment = await fetchBytes(first.uri, first.byteRange);

  if (playlist.containerHint === 'fmp4') {
    if (!playlist.initSegment) return segment;
    const init = await fetchBytes(playlist.initSegment.uri, playlist.initSegment.byteRange);
    const merged = new Uint8Array(init.length + segment.length);
    merged.set(init, 0);
    merged.set(segment, init.length);
    return merged;
  }

  // TS 要转封装之后 <video> 才认。
  const muxjs = await loadMuxjs();
  const chunks = [];
  let offset = 0;
  await remuxTsToMp4({
    muxjs,
    totalBytes: segment.length,
    readChunk: async (at, length) => {
      if (at >= segment.length) return new Uint8Array(0);
      offset = Math.min(at + length, segment.length);
      return segment.subarray(at, offset);
    },
    write: (bytes) => chunks.push(bytes)
  });
  const total = chunks.reduce((sum, chunk) => sum + chunk.length, 0);
  const merged = new Uint8Array(total);
  let cursor = 0;
  for (const chunk of chunks) {
    merged.set(chunk, cursor);
    cursor += chunk.length;
  }
  return merged;
}

// 把一段 mp4 字节解出一帧，返回 data URL。
function captureFrame(bytes) {
  return new Promise((resolve, reject) => {
    const url = URL.createObjectURL(new Blob([bytes], { type: 'video/mp4' }));
    const video = document.createElement('video');
    video.muted = true;
    video.playsInline = true;
    video.preload = 'auto';

    // 解码卡住时不能让界面一直转：给一个上限，超时就当没有缩略图。
    const timer = setTimeout(() => finish(new Error('解码超时')), 8000);
    function finish(error, dataUrl) {
      clearTimeout(timer);
      URL.revokeObjectURL(url);
      video.remove();
      error ? reject(error) : resolve(dataUrl);
    }

    video.addEventListener('error', () => finish(new Error('这段数据解不出画面')));
    video.addEventListener('loadeddata', () => {
      // 第一帧常常是黑场，往后挪一点再截。
      const target = Number.isFinite(video.duration) && video.duration > 1.2 ? 1 : 0;
      if (Math.abs(video.currentTime - target) < 0.01) return draw();
      video.addEventListener('seeked', draw, { once: true });
      video.currentTime = target;
    });

    function draw() {
      try {
        const canvas = document.createElement('canvas');
        canvas.width = THUMB_WIDTH;
        canvas.height = THUMB_HEIGHT;
        canvas.getContext('2d').drawImage(video, 0, 0, THUMB_WIDTH, THUMB_HEIGHT);
        finish(null, canvas.toDataURL('image/jpeg', 0.7));
      } catch (error) {
        finish(error);
      }
    }

    video.src = url;
  });
}

// 会话内缓存：弹窗一关就销毁，缓存放 session storage 里，下次开弹窗不用重下一遍。
async function cachedThumbnail(key) {
  try {
    const stored = await chrome.storage.session.get(key);
    return stored[key] || '';
  } catch {
    return '';
  }
}

async function cacheThumbnail(key, dataUrl) {
  try {
    await chrome.storage.session.set({ [key]: dataUrl });
  } catch {
    // 会话存储满了或不可用时不影响功能，只是下次要重新生成。
  }
}

export async function generateThumbnail(detection) {
  const key = `thumb:${detection.url}`;
  const cached = await cachedThumbnail(key);
  if (cached) return cached;

  const bytes = await buildPreviewBytes(detection);
  const dataUrl = await captureFrame(bytes);
  await cacheThumbnail(key, dataUrl);
  return dataUrl;
}
