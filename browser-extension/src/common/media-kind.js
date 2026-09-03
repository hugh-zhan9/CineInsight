// 命中判定：一个网络请求是不是我们要抓的流式媒体，是哪一类。
// 纯函数，输入是 webRequest 能给到的东西（URL + 响应头 + 资源类型），可直接单测。

import { MEDIA_KIND } from './constants.js';
import { extensionOf } from './url.js';

// 路由一：路径扩展名。
const HLS_EXTENSIONS = new Set(['m3u8', 'm3u']);
const SEGMENT_EXTENSIONS = new Set(['ts', 'm4s', 'cmfv', 'cmfa', 'fmp4']);
const DASH_EXTENSIONS = new Set(['mpd']);
const FILE_EXTENSIONS = new Set([
  'mp4', 'm4v', 'mkv', 'webm', 'mov', 'avi', 'flv', 'wmv', 'ogv',
  'mpg', 'mpeg', '3gp', 'ts_', 'rmvb', 'f4v'
]);

// 路由二：Content-Type。同一种流在不同 CDN 上的 MIME 五花八门，这里按前缀比。
const HLS_MIMES = [
  'application/vnd.apple.mpegurl',
  'application/x-mpegurl',
  'audio/mpegurl',
  'audio/x-mpegurl',
  'application/mpegurl',
  'vnd.apple.mpegurl'
];
const SEGMENT_MIMES = ['video/mp2t', 'video/mpeg2ts', 'application/octet-stream+ts'];
const DASH_MIMES = ['application/dash+xml'];
const FILE_MIMES = [
  'video/mp4', 'video/webm', 'video/x-matroska', 'video/quicktime',
  'video/x-msvideo', 'video/x-flv', 'video/ogg', 'video/3gpp', 'video/x-ms-wmv'
];

// 只在这些资源类型上判定。图片、脚本、样式表不进这条链路。
const CANDIDATE_RESOURCE_TYPES = new Set(['media', 'xmlhttprequest', 'other', 'object', 'main_frame', 'sub_frame']);

export function contentTypeOf(responseHeaders) {
  const header = findHeader(responseHeaders, 'content-type');
  if (!header) return '';
  return String(header).split(';')[0].trim().toLowerCase();
}

export function contentLengthOf(responseHeaders) {
  const header = findHeader(responseHeaders, 'content-length');
  const value = Number.parseInt(header, 10);
  return Number.isFinite(value) && value >= 0 ? value : 0;
}

export function acceptsRanges(responseHeaders) {
  const header = String(findHeader(responseHeaders, 'accept-ranges') || '').toLowerCase();
  return header.includes('bytes');
}

export function findHeader(headers, name) {
  if (!Array.isArray(headers)) return '';
  const wanted = name.toLowerCase();
  for (const header of headers) {
    if (header && typeof header.name === 'string' && header.name.toLowerCase() === wanted) {
      return header.value ?? '';
    }
  }
  return '';
}

// 判定结果为 null 表示"不是我们要的东西"，调用方直接丢弃。
//
// 这里只用得到 URL 与响应头。播放列表的正文特征（首行 #EXTM3U）留到展开时验证：
// webRequest 拿不到响应体，为了看正文额外发一次请求代价太大，而展开时本来就要取一次，
// 那时解析不出来就把这条命中标成无效，不会让用户对着一条假命中点下载。
export function classifyRequest({ url, responseHeaders, resourceType }) {
  if (!url || !/^https?:/i.test(url)) return null;
  if (resourceType && !CANDIDATE_RESOURCE_TYPES.has(resourceType)) return null;

  const extension = extensionOf(url);
  const mime = contentTypeOf(responseHeaders);

  if (HLS_EXTENSIONS.has(extension) || matchesMime(mime, HLS_MIMES) || looksLikeHLSQuery(url)) {
    return {
      kind: MEDIA_KIND.HLS,
      extension: 'm3u8',
      mime,
      contentLength: contentLengthOf(responseHeaders),
      acceptsRanges: false
    };
  }
  if (DASH_EXTENSIONS.has(extension) || matchesMime(mime, DASH_MIMES)) {
    return {
      kind: MEDIA_KIND.DASH,
      extension: 'mpd',
      mime,
      contentLength: contentLengthOf(responseHeaders),
      acceptsRanges: false
    };
  }
  if (SEGMENT_EXTENSIONS.has(extension) || matchesMime(mime, SEGMENT_MIMES)) {
    return {
      kind: MEDIA_KIND.SEGMENT,
      extension: extension || 'ts',
      mime,
      contentLength: contentLengthOf(responseHeaders),
      acceptsRanges: acceptsRanges(responseHeaders)
    };
  }
  if (FILE_EXTENSIONS.has(extension) || matchesMime(mime, FILE_MIMES)) {
    return {
      kind: MEDIA_KIND.FILE,
      extension: extension || extensionFromMime(mime) || 'mp4',
      mime,
      contentLength: contentLengthOf(responseHeaders),
      acceptsRanges: acceptsRanges(responseHeaders)
    };
  }
  return null;
}

function matchesMime(mime, candidates) {
  if (!mime) return false;
  return candidates.some((candidate) => mime === candidate || mime.endsWith(candidate));
}

// 转发型地址把真实播放列表塞在查询串里：/play?src=https%3A//cdn/x.m3u8。
// 只认 m3u8 这一个词，别的后缀不做这种猜测。
function looksLikeHLSQuery(url) {
  const lower = url.toLowerCase();
  const queryStart = lower.indexOf('?');
  if (queryStart < 0) return false;
  return lower.slice(queryStart).includes('m3u8');
}

function extensionFromMime(mime) {
  switch (mime) {
    case 'video/mp4': return 'mp4';
    case 'video/webm': return 'webm';
    case 'video/x-matroska': return 'mkv';
    case 'video/quicktime': return 'mov';
    case 'video/ogg': return 'ogv';
    case 'video/3gpp': return '3gp';
    case 'video/x-flv': return 'flv';
    case 'video/x-msvideo': return 'avi';
    case 'video/x-ms-wmv': return 'wmv';
    default: return '';
  }
}

// 分片不单独列给用户，但它能反推出播放列表所在目录，用于"只看到分片没看到 m3u8"的场合。
export function segmentPlaylistHint(segmentUrl) {
  try {
    const parsed = new URL(segmentUrl);
    const lastSlash = parsed.pathname.lastIndexOf('/');
    parsed.pathname = lastSlash >= 0 ? `${parsed.pathname.slice(0, lastSlash + 1)}` : '/';
    parsed.search = '';
    parsed.hash = '';
    return parsed.toString();
  } catch {
    return '';
  }
}
