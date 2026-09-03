// HLS 下载主循环：并发取分片、按需解密、按序落盘、随时可停可续。
//
// 依赖全部从外面注入（取数、落盘、计时），因此这套逻辑可以在 Node 里用假实现单测，
// 不需要浏览器。真实接线在 offscreen/download-worker.js。

import { SequentialWriteLedger, runWithConcurrency } from './ledger.js';
import { importAesKey, decryptSegment, deriveIV, ivFromHex } from './crypto-aes.js';

export class DownloadStopped extends Error {
  constructor(reason = 'stopped') {
    super(reason === 'paused' ? '任务已暂停' : '任务已取消');
    this.name = 'DownloadStopped';
    this.reason = reason;
  }
}

export async function downloadHls({
  playlist,
  fetchBytes,
  fetchKey,
  writer,
  ledger,
  concurrency = 6,
  retries = 3,
  backoffMs = 800,
  onProgress = () => {},
  shouldStop = () => false,
  sleep
}) {
  if (!playlist || playlist.type !== 'media') {
    throw new Error('需要一份已经解析好的 media 播放列表');
  }
  if (!playlist.encryption.supported) {
    // D-B02：解不开的流在这里就停，绝不产出一个"下载完了但打不开"的文件。
    throw new Error(playlist.encryption.reason || '这条流的加密方式不支持下载');
  }

  const segments = playlist.segments;
  const activeLedger = ledger || new SequentialWriteLedger({ totalItems: segments.length });
  const keyCache = new Map();

  // 初始化分片（fMP4 的 init.mp4）必须排在所有媒体分片之前，且只在从头开始时写。
  // 续跑时它已经在文件里了，再写一次就把文件写坏了。
  if (playlist.initSegment && activeLedger.nextIndex === 0 && activeLedger.bytesWritten === 0) {
    const initBytes = await fetchBytes(playlist.initSegment.uri, { byteRange: playlist.initSegment.byteRange });
    writer.write(initBytes);
    activeLedger.bytesWritten += initBytes.length;
  }

  const emitProgress = () => {
    onProgress({
      completedItems: activeLedger.nextIndex,
      totalItems: segments.length,
      bytesWritten: activeLedger.bytesWritten
    });
  };
  emitProgress();

  await runWithConcurrency({
    total: segments.length,
    startIndex: activeLedger.nextIndex,
    concurrency,
    retries,
    backoffMs,
    maxHeld: Math.max(4, concurrency * 2),
    shouldStop,
    heldCount: () => activeLedger.heldCount,
    sleep,
    run: async (index) => {
      const segment = segments[index];
      const raw = await fetchBytes(segment.uri, { byteRange: segment.byteRange });
      const bytes = segment.key ? await decryptOne(segment, raw, keyCache, fetchKey) : raw;
      // 第一片解出来先验一眼容器特征：密钥不对时 AES-CBC 一样"解得出来"，
      // 只是全是噪声，不看一眼就会产出一个下载完成却根本打不开的文件。
      if (index === activeLedger.nextIndex && segment.key) {
        assertPlaintextLooksRight(bytes, playlist.containerHint);
      }
      activeLedger.record(index, bytes);
      activeLedger.flush((chunk) => writer.write(chunk), shouldStop);
      emitProgress();
    }
  });

  if (shouldStop()) throw new DownloadStopped(stopReason(shouldStop));

  // 走到这里 nextIndex 必须等于分片总数。不等就是账本和落盘不同步，
  // 与其交出一个缺片的文件，不如明确失败。
  if (activeLedger.nextIndex !== segments.length) {
    throw new Error(`分片没取全：写入 ${activeLedger.nextIndex} / ${segments.length}`);
  }
  writer.flush();
  emitProgress();
  return activeLedger.snapshot();
}

async function decryptOne(segment, raw, keyCache, fetchKey) {
  const keyUri = segment.key.uri;
  // 缓存的是"取密钥这件事"而不是取回来的结果：并发的头几片会同时未命中，
  // 缓存结果的话它们会各自去取一次同一把密钥，对一些按次计费/限流的密钥服务
  // 这不只是浪费。
  let pending = keyCache.get(keyUri);
  if (!pending) {
    pending = (async () => importAesKey(await fetchKey(keyUri)))();
    keyCache.set(keyUri, pending);
    // 取失败就把这次尝试从缓存里摘掉，否则整条流会一直复用一个失败的 promise。
    pending.catch(() => keyCache.delete(keyUri));
  }
  const cryptoKey = await pending;
  const iv = segment.key.iv ? ivFromHex(segment.key.iv) : deriveIV(segment.mediaSequence);
  return decryptSegment(cryptoKey, iv, raw);
}

function stopReason(shouldStop) {
  const value = shouldStop();
  return typeof value === 'string' ? value : 'canceled';
}

// 解密结果的容器特征检查。
//
// 为什么需要：AES-CBC 用错误的密钥同样"解得出来"，只是结果是噪声。而我们那条
// 不校验补齐的解密路径按构造必然通过（见 crypto-aes.js），所以密钥不对时整条链路
// 一声不吭地跑完，交给用户一个下载完成却打不开的文件。密钥服务返回轮换过的、
// 或者干脆是诱饵的密钥，都会落到这里。
//
// 只查第一片、只查开头几个字节：代价可以忽略，而错密钥几乎不可能凑巧命中。
function assertPlaintextLooksRight(bytes, containerHint) {
  if (!bytes || bytes.length < 8) return;
  if (containerHint === 'ts') {
    // MPEG-TS 每 188 字节一个包，包头固定是 0x47。
    if (bytes[0] !== 0x47) {
      throw new Error('解密结果不是有效的 TS 数据：密钥不对，或这条流用了不支持的加密方式');
    }
    return;
  }
  if (containerHint === 'fmp4') {
    // fMP4 分片以一个 box 开头，第 5..8 字节是 box 类型。
    const boxType = String.fromCharCode(bytes[4], bytes[5], bytes[6], bytes[7]);
    if (!['ftyp', 'styp', 'moof', 'sidx', 'free', 'skip', 'moov'].includes(boxType)) {
      throw new Error('解密结果不是有效的 fMP4 数据：密钥不对，或这条流用了不支持的加密方式');
    }
  }
  // 其他容器（aac、mp3）不做判断：它们没有一个足够可靠的固定开头，
  // 与其用一个会误判的规则挡住正常下载，不如不判。
}

// 直链下载：支持 Range 就分块并发，不支持就整取。
//
// 不支持 Range 的情况不假装能续传——UI 会照实说这条流断了要重来。
export async function downloadFile({
  url,
  totalBytes,
  supportsRange,
  chunkSize = 4 * 1024 * 1024,
  fetchBytes,
  fetchWhole,
  writer,
  ledger,
  concurrency = 4,
  retries = 3,
  backoffMs = 800,
  onProgress = () => {},
  shouldStop = () => false,
  sleep
}) {
  if (!supportsRange || !totalBytes) {
    if (ledger && ledger.bytesWritten > 0) {
      throw new Error('这条流不支持断点续传，需要从头重新下载');
    }
    const bytes = await fetchWhole(url, { shouldStop });
    if (shouldStop()) throw new DownloadStopped(stopReason(shouldStop));
    writer.write(bytes);
    writer.flush();
    onProgress({ completedItems: 1, totalItems: 1, bytesWritten: bytes.length });
    return { totalItems: 1, nextIndex: 1, bytesWritten: bytes.length };
  }

  const totalChunks = Math.ceil(totalBytes / chunkSize);
  const activeLedger = ledger || new SequentialWriteLedger({ totalItems: totalChunks });
  const emitProgress = () => {
    onProgress({
      completedItems: activeLedger.nextIndex,
      totalItems: totalChunks,
      bytesWritten: activeLedger.bytesWritten
    });
  };
  emitProgress();

  await runWithConcurrency({
    total: totalChunks,
    startIndex: activeLedger.nextIndex,
    concurrency,
    retries,
    backoffMs,
    maxHeld: Math.max(4, concurrency * 2),
    shouldStop,
    heldCount: () => activeLedger.heldCount,
    sleep,
    run: async (index) => {
      const offset = index * chunkSize;
      const length = Math.min(chunkSize, totalBytes - offset);
      const bytes = await fetchBytes(url, { byteRange: { offset, length } });
      if (bytes.length !== length) {
        throw new Error(`第 ${index + 1} 块长度不对：期望 ${length}，实际 ${bytes.length}`);
      }
      activeLedger.record(index, bytes);
      activeLedger.flush((chunk) => writer.write(chunk), shouldStop);
      emitProgress();
    }
  });

  if (shouldStop()) throw new DownloadStopped(stopReason(shouldStop));
  if (activeLedger.bytesWritten !== totalBytes) {
    throw new Error(`下载字节数对不上：写入 ${activeLedger.bytesWritten}，服务端声明 ${totalBytes}`);
  }
  writer.flush();
  emitProgress();
  return activeLedger.snapshot();
}
