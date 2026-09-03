// HLS 播放列表解析。纯函数，不依赖任何浏览器 API，供下载引擎与界面共用。
//
// 返回值是判别联合：type 为 'master' | 'media' | 'invalid'。畸形输入返回
// { type: 'invalid', reason } 而不是抛异常——调用方拿到的永远是可以直接显示给用户的话。

import { resolveUrl, extensionOf } from '../url.js';

// 按 D-B02：只有明文与 AES-128（KEYFORMAT 为 identity）能下载，其余一律明确失败。
const DRM_KEYFORMATS = [
  'urn:uuid:edef8ba9-79d6-4ace-a3c8-27dcd51d21ed', // Widevine
  'urn:uuid:9a04f079-9840-4286-ab92-e65be0885f95', // PlayReady
  'com.apple.streamingkeydelivery', // FairPlay
  'com.microsoft.playready'
];

export function parsePlaylist(text, baseUrl) {
  const raw = typeof text === 'string' ? text : '';
  const lines = raw.split(/\r?\n/).map((line) => line.trim());
  const firstMeaningful = lines.find((line) => line.length > 0);
  if (firstMeaningful !== '#EXTM3U') {
    return { type: 'invalid', reason: '不是 HLS 播放列表：开头缺少 #EXTM3U' };
  }
  const isMaster = lines.some((line) => line.startsWith('#EXT-X-STREAM-INF'));
  try {
    return isMaster ? parseMaster(lines, baseUrl) : parseMedia(lines, baseUrl);
  } catch (error) {
    return { type: 'invalid', reason: `播放列表解析失败：${error && error.message ? error.message : error}` };
  }
}

function parseMaster(lines, baseUrl) {
  const variants = [];
  const audioRenditions = [];
  const subtitleRenditions = [];
  let pendingStreamInf = null;

  for (const line of lines) {
    if (!line) continue;
    if (line.startsWith('#EXT-X-STREAM-INF:')) {
      pendingStreamInf = parseAttributes(line.slice('#EXT-X-STREAM-INF:'.length));
      continue;
    }
    if (line.startsWith('#EXT-X-MEDIA:')) {
      const attrs = parseAttributes(line.slice('#EXT-X-MEDIA:'.length));
      const rendition = {
        groupId: attrs['GROUP-ID'] || '',
        name: attrs.NAME || '',
        language: attrs.LANGUAGE || '',
        uri: attrs.URI ? safeResolve(baseUrl, attrs.URI) : '',
        isDefault: (attrs.DEFAULT || '').toUpperCase() === 'YES',
        autoselect: (attrs.AUTOSELECT || '').toUpperCase() === 'YES'
      };
      if ((attrs.TYPE || '').toUpperCase() === 'AUDIO') audioRenditions.push(rendition);
      if ((attrs.TYPE || '').toUpperCase() === 'SUBTITLES') subtitleRenditions.push(rendition);
      continue;
    }
    if (line.startsWith('#')) continue;
    if (!pendingStreamInf) continue;

    const resolution = parseResolution(pendingStreamInf.RESOLUTION);
    variants.push({
      uri: safeResolve(baseUrl, line),
      bandwidth: toInt(pendingStreamInf.BANDWIDTH),
      averageBandwidth: toInt(pendingStreamInf['AVERAGE-BANDWIDTH']),
      resolution,
      codecs: pendingStreamInf.CODECS || '',
      frameRate: toFloat(pendingStreamInf['FRAME-RATE']),
      audioGroup: pendingStreamInf.AUDIO || '',
      label: variantLabel(resolution, toInt(pendingStreamInf.BANDWIDTH))
    });
    pendingStreamInf = null;
  }

  if (variants.length === 0) {
    return { type: 'invalid', reason: 'master 播放列表里没有可用的码率条目' };
  }
  // 高码率在前：用户十有八九要最好的那条，让它落在第一个。
  variants.sort((a, b) => (b.bandwidth || 0) - (a.bandwidth || 0));
  return { type: 'master', variants, audioRenditions, subtitleRenditions };
}

function parseMedia(lines, baseUrl) {
  const segments = [];
  let targetDuration = 0;
  let mediaSequence = 0;
  let discontinuitySequence = 0;
  let playlistType = '';
  let hasEndList = false;
  let initSegment = null;

  let pendingDuration = null;
  let pendingTitle = '';
  let pendingByteRange = null;
  let pendingDiscontinuity = false;
  let currentKey = null;
  let sawKeyTag = false;
  let unsupportedKey = null;
  // BYTERANGE 省略 offset 时，起点是同一个 URI 上一段的末尾。
  const byteRangeCursor = new Map();
  let sequence = 0;
  let sequenceInitialized = false;

  for (const line of lines) {
    if (!line) continue;

    if (line.startsWith('#EXT-X-TARGETDURATION:')) {
      targetDuration = toFloat(line.slice('#EXT-X-TARGETDURATION:'.length));
      continue;
    }
    if (line.startsWith('#EXT-X-MEDIA-SEQUENCE:')) {
      mediaSequence = toInt(line.slice('#EXT-X-MEDIA-SEQUENCE:'.length));
      sequence = mediaSequence;
      sequenceInitialized = true;
      continue;
    }
    if (line.startsWith('#EXT-X-DISCONTINUITY-SEQUENCE:')) {
      discontinuitySequence = toInt(line.slice('#EXT-X-DISCONTINUITY-SEQUENCE:'.length));
      continue;
    }
    if (line.startsWith('#EXT-X-PLAYLIST-TYPE:')) {
      playlistType = line.slice('#EXT-X-PLAYLIST-TYPE:'.length).trim().toUpperCase();
      continue;
    }
    if (line === '#EXT-X-ENDLIST') {
      hasEndList = true;
      continue;
    }
    if (line === '#EXT-X-DISCONTINUITY') {
      pendingDiscontinuity = true;
      continue;
    }
    if (line.startsWith('#EXT-X-KEY:')) {
      sawKeyTag = true;
      const parsed = parseKey(line.slice('#EXT-X-KEY:'.length), baseUrl);
      if (parsed.unsupported) {
        unsupportedKey = parsed;
        currentKey = null;
      } else {
        currentKey = parsed.key;
      }
      continue;
    }
    if (line.startsWith('#EXT-X-MAP:')) {
      const attrs = parseAttributes(line.slice('#EXT-X-MAP:'.length));
      if (attrs.URI) {
        initSegment = {
          uri: safeResolve(baseUrl, attrs.URI),
          byteRange: parseByteRange(attrs.BYTERANGE, byteRangeCursor, safeResolve(baseUrl, attrs.URI))
        };
      }
      continue;
    }
    if (line.startsWith('#EXTINF:')) {
      const payload = line.slice('#EXTINF:'.length);
      const comma = payload.indexOf(',');
      pendingDuration = toFloat(comma >= 0 ? payload.slice(0, comma) : payload);
      pendingTitle = comma >= 0 ? payload.slice(comma + 1).trim() : '';
      continue;
    }
    if (line.startsWith('#EXT-X-BYTERANGE:')) {
      pendingByteRange = line.slice('#EXT-X-BYTERANGE:'.length).trim();
      continue;
    }
    if (line.startsWith('#')) continue;

    // 到这里是一条分片 URI。没有 EXTINF 的裸行不算分片，跳过而不是记一条时长为 0 的。
    if (pendingDuration === null) continue;
    const uri = safeResolve(baseUrl, line);
    segments.push({
      index: segments.length,
      uri,
      duration: pendingDuration,
      title: pendingTitle,
      byteRange: parseByteRange(pendingByteRange, byteRangeCursor, uri),
      mediaSequence: sequenceInitialized || segments.length > 0 ? sequence : mediaSequence,
      discontinuity: pendingDiscontinuity,
      key: currentKey
    });
    sequence += 1;
    pendingDuration = null;
    pendingTitle = '';
    pendingByteRange = null;
    pendingDiscontinuity = false;
  }

  if (segments.length === 0) {
    return { type: 'invalid', reason: '播放列表里没有任何分片' };
  }

  const encryption = classifyEncryption({ sawKeyTag, unsupportedKey, segments });
  const totalDuration = segments.reduce((sum, segment) => sum + (segment.duration || 0), 0);
  const containerHint = detectContainer(initSegment, segments);

  return {
    type: 'media',
    targetDuration,
    mediaSequence,
    discontinuitySequence,
    playlistType,
    isLive: !hasEndList && playlistType !== 'VOD',
    totalDuration,
    initSegment,
    segments,
    encryption,
    containerHint,
    outputExtension: containerHint === 'fmp4' ? 'mp4' : containerHint
  };
}

// 加密分级。unsupported 的原因要能直接显示给用户。
function classifyEncryption({ sawKeyTag, unsupportedKey, segments }) {
  if (unsupportedKey) {
    return { supported: false, method: unsupportedKey.method, reason: unsupportedKey.reason };
  }
  const encryptedSegments = segments.filter((segment) => segment.key);
  if (!sawKeyTag || encryptedSegments.length === 0) {
    return { supported: true, method: 'NONE', reason: '' };
  }
  return { supported: true, method: 'AES-128', reason: '' };
}

function parseKey(attributeText, baseUrl) {
  const attrs = parseAttributes(attributeText);
  const method = (attrs.METHOD || '').toUpperCase();
  const keyFormat = (attrs.KEYFORMAT || 'identity').toLowerCase();

  if (method === 'NONE') {
    return { key: null };
  }
  if (DRM_KEYFORMATS.some((format) => keyFormat.includes(format))) {
    return {
      unsupported: true,
      method,
      reason: `受 DRM 保护（KEYFORMAT=${attrs.KEYFORMAT}），无法下载`
    };
  }
  // identity 之外的 KEYFORMAT 一律不认，而不是只挡掉列举出来的那几种 DRM。
  // 白名单之外的格式意味着密钥的取法或用法与标准 AES-128 不同，按 identity 去解
  // 只会解出噪声——而错误的密钥同样"解得出来"，最后交给用户一个打不开的文件。
  if (keyFormat !== 'identity') {
    return {
      unsupported: true,
      method,
      reason: `不支持的密钥格式（KEYFORMAT=${attrs.KEYFORMAT}），无法下载`
    };
  }
  if (method === 'SAMPLE-AES' || method === 'SAMPLE-AES-CTR' || method === 'SAMPLE-AES-CENC') {
    return {
      unsupported: true,
      method,
      reason: `加密方式 ${method} 需要 DRM 密钥，无法下载`
    };
  }
  if (method !== 'AES-128') {
    return {
      unsupported: true,
      method: method || '未知',
      reason: `不认识的加密方式 ${method || '(空)'}，无法下载`
    };
  }
  if (!attrs.URI) {
    return { unsupported: true, method, reason: 'AES-128 缺少密钥地址（URI），无法下载' };
  }
  return {
    key: {
      method: 'AES-128',
      uri: safeResolve(baseUrl, attrs.URI),
      // IV 缺省时按媒体序号推导，推导放在下载侧（那里才知道分片的序号）。
      iv: attrs.IV ? normalizeIV(attrs.IV) : null,
      keyFormat
    }
  };
}

// IV 是 0x 前缀的 32 位十六进制。长度不对就当没写，交由序号推导，
// 而不是拿一个半截 IV 去解出乱码。
function normalizeIV(raw) {
  const hex = String(raw).trim().replace(/^0x/i, '').toLowerCase();
  if (!/^[0-9a-f]{32}$/.test(hex)) return null;
  return hex;
}

// BYTERANGE 形如 "1024@512" 或 "1024"（省略 offset 时接在同一 URI 上一段之后）。
function parseByteRange(raw, cursor, uri) {
  if (!raw) return null;
  const text = String(raw).trim();
  const [lengthText, offsetText] = text.split('@');
  const length = toInt(lengthText);
  if (!length) return null;
  const offset = offsetText !== undefined ? toInt(offsetText) : (cursor.get(uri) || 0);
  cursor.set(uri, offset + length);
  return { length, offset };
}

function detectContainer(initSegment, segments) {
  if (initSegment) return 'fmp4';
  const first = segments[0];
  const extension = first ? extensionOf(first.uri) : '';
  if (extension === 'ts') return 'ts';
  if (extension === 'aac') return 'aac';
  if (extension === 'mp3') return 'mp3';
  if (extension === 'm4s' || extension === 'mp4' || extension === 'm4a') return 'fmp4';
  // 认不出容器时按 TS 处理：HLS 的默认容器就是 MPEG-TS。
  return 'ts';
}

// HLS 的属性列表：KEY=VALUE 用逗号分隔，值可以带引号且引号内允许逗号。
export function parseAttributes(text) {
  const attributes = {};
  let index = 0;
  const source = String(text || '');
  while (index < source.length) {
    const equals = source.indexOf('=', index);
    if (equals < 0) break;
    const name = source.slice(index, equals).trim().toUpperCase();
    index = equals + 1;
    let value = '';
    if (source[index] === '"') {
      const closing = source.indexOf('"', index + 1);
      if (closing < 0) {
        value = source.slice(index + 1);
        index = source.length;
      } else {
        value = source.slice(index + 1, closing);
        index = closing + 1;
        if (source[index] === ',') index += 1;
      }
    } else {
      const comma = source.indexOf(',', index);
      if (comma < 0) {
        value = source.slice(index);
        index = source.length;
      } else {
        value = source.slice(index, comma);
        index = comma + 1;
      }
      value = value.trim();
    }
    if (name) attributes[name] = value;
  }
  return attributes;
}

function parseResolution(raw) {
  if (!raw) return null;
  const match = /^(\d+)x(\d+)$/i.exec(String(raw).trim());
  if (!match) return null;
  return { width: Number(match[1]), height: Number(match[2]) };
}

// 码率标签：优先分辨率（用户认这个），没有分辨率时退到 kbps。
export function variantLabel(resolution, bandwidth) {
  if (resolution && resolution.height) return `${resolution.height}p`;
  if (bandwidth) return `${Math.round(bandwidth / 1000)}kbps`;
  return '默认';
}

function safeResolve(baseUrl, uri) {
  try {
    return resolveUrl(baseUrl, uri);
  } catch {
    return uri;
  }
}

function toInt(value) {
  const parsed = Number.parseInt(String(value ?? '').trim(), 10);
  return Number.isFinite(parsed) ? parsed : 0;
}

function toFloat(value) {
  const parsed = Number.parseFloat(String(value ?? '').trim());
  return Number.isFinite(parsed) ? parsed : 0;
}
