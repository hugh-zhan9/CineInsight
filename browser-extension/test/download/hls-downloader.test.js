import test from 'node:test';
import assert from 'node:assert/strict';

import { downloadHls, downloadFile, DownloadStopped } from '../../src/download/hls-downloader.js';
import { SequentialWriteLedger } from '../../src/download/ledger.js';
import { parsePlaylist } from '../../src/common/hls/parser.js';

const BASE = 'https://cdn.example.com/v/index.m3u8';
const RAW_KEY = new Uint8Array([1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16]);

function fakeWriter() {
  const chunks = [];
  return {
    chunks,
    write(bytes) { chunks.push(Uint8Array.from(bytes)); },
    flush() {},
    get bytes() {
      const total = chunks.reduce((sum, chunk) => sum + chunk.length, 0);
      const merged = new Uint8Array(total);
      let offset = 0;
      for (const chunk of chunks) { merged.set(chunk, offset); offset += chunk.length; }
      return merged;
    }
  };
}

// 分片内容就是它的序号，重复填满 16 字节的整数倍，方便断言顺序。
function segmentBody(index, blocks = 2) {
  return new Uint8Array(16 * blocks).fill(index);
}

// 加密路径要过容器特征检查，所以密文对应的明文必须真的像一段 TS：
// 首字节是 TS 的同步字节 0x47，后面才是用来断言顺序的标记。
function tsSegmentBody(marker, blocks = 2) {
  const body = new Uint8Array(16 * blocks).fill(marker);
  body[0] = 0x47;
  return body;
}

function plainPlaylist(count) {
  const lines = ['#EXTM3U', '#EXT-X-TARGETDURATION:10'];
  for (let index = 0; index < count; index += 1) {
    lines.push('#EXTINF:10,', `seg${index}.ts`);
  }
  lines.push('#EXT-X-ENDLIST');
  return parsePlaylist(lines.join('\n'), BASE);
}

test('分片乱序回来也按序写盘', async () => {
  const playlist = plainPlaylist(8);
  const writer = fakeWriter();
  // 让后面的分片先回来：序号越大越快
  const fetchBytes = async (url) => {
    const index = Number(/seg(\d+)\.ts/.exec(url)[1]);
    await new Promise((resolve) => setTimeout(resolve, (8 - index) * 2));
    return segmentBody(index);
  };

  await downloadHls({ playlist, fetchBytes, fetchKey: async () => RAW_KEY, writer, concurrency: 8 });

  const bytes = writer.bytes;
  assert.equal(bytes.length, 8 * 32);
  for (let index = 0; index < 8; index += 1) {
    assert.equal(bytes[index * 32], index, `第 ${index} 片位置不对`);
  }
});

test('进度按已完成分片数上报，最终等于总数', async () => {
  const playlist = plainPlaylist(5);
  const writer = fakeWriter();
  const progress = [];

  await downloadHls({
    playlist,
    fetchBytes: async (url) => segmentBody(Number(/seg(\d+)/.exec(url)[1])),
    fetchKey: async () => RAW_KEY,
    writer,
    concurrency: 2,
    onProgress: (update) => progress.push(update)
  });

  const last = progress[progress.length - 1];
  assert.equal(last.completedItems, 5);
  assert.equal(last.totalItems, 5);
  assert.equal(last.bytesWritten, 5 * 32);
  // 进度只增不减
  const completed = progress.map((item) => item.completedItems);
  assert.deepEqual(completed, [...completed].sort((a, b) => a - b));
});

test('AES-128 分片先解密再落盘，密钥只取一次', async () => {
  const text = [
    '#EXTM3U',
    '#EXT-X-KEY:METHOD=AES-128,URI="key.bin"',
    '#EXTINF:10,', 'seg0.ts',
    '#EXTINF:10,', 'seg1.ts',
    '#EXT-X-ENDLIST'
  ].join('\n');
  const playlist = parsePlaylist(text, BASE);
  const writer = fakeWriter();

  const key = await crypto.subtle.importKey('raw', RAW_KEY, { name: 'AES-CBC' }, false, ['encrypt']);
  const plain = [tsSegmentBody(7), tsSegmentBody(9)];
  const ivFor = (sequence) => {
    const iv = new Uint8Array(16);
    iv[15] = sequence;
    return iv;
  };
  const ciphers = await Promise.all(
    plain.map(async (body, index) =>
      new Uint8Array(await crypto.subtle.encrypt({ name: 'AES-CBC', iv: ivFor(index) }, key, body)))
  );

  let keyFetches = 0;
  await downloadHls({
    playlist,
    fetchBytes: async (url) => ciphers[Number(/seg(\d+)/.exec(url)[1])],
    fetchKey: async () => { keyFetches += 1; return RAW_KEY; },
    writer,
    concurrency: 2
  });

  assert.equal(keyFetches, 1, '密钥应当只取一次并复用');
  const bytes = writer.bytes;
  assert.equal(bytes.length, 64);
  assert.equal(bytes[0], 0x47);
  assert.equal(bytes[1], 7);
  assert.equal(bytes[33], 9);
});

test('密钥不对时明确失败，不交出一个"下载完成却打不开"的文件', async () => {
  // AES-CBC 用错密钥照样"解得出来"，只是全是噪声；而不校验补齐的那条解密路径
  // 按构造必然通过。所以只能靠容器特征把它拦下来。
  const text = [
    '#EXTM3U',
    '#EXT-X-KEY:METHOD=AES-128,URI="key.bin"',
    '#EXTINF:10,', 'seg0.ts',
    '#EXT-X-ENDLIST'
  ].join('\n');
  const playlist = parsePlaylist(text, BASE);
  const writer = fakeWriter();

  const realKey = await crypto.subtle.importKey('raw', RAW_KEY, { name: 'AES-CBC' }, false, ['encrypt']);
  const iv = new Uint8Array(16);
  const cipher = new Uint8Array(await crypto.subtle.encrypt({ name: 'AES-CBC', iv }, realKey, tsSegmentBody(7)));
  const wrongKey = new Uint8Array(16).fill(0xab);

  await assert.rejects(
    downloadHls({
      playlist,
      fetchBytes: async () => cipher,
      fetchKey: async () => wrongKey,
      writer,
      retries: 0
    }),
    /密钥不对/
  );
  assert.equal(writer.chunks.length, 0, '不该写出任何字节');
});

test('DRM 流在下载前就停住，不产出半成品（D-B02）', async () => {
  const playlist = parsePlaylist([
    '#EXTM3U',
    '#EXT-X-KEY:METHOD=SAMPLE-AES,URI="skd://x"',
    '#EXTINF:10,', 'seg0.ts',
    '#EXT-X-ENDLIST'
  ].join('\n'), BASE);
  const writer = fakeWriter();

  await assert.rejects(
    downloadHls({ playlist, fetchBytes: async () => segmentBody(0), fetchKey: async () => RAW_KEY, writer }),
    /SAMPLE-AES/
  );
  assert.equal(writer.chunks.length, 0, '不该写出任何字节');
});

test('续跑只取剩下的分片，已写部分不重下', async () => {
  const playlist = plainPlaylist(10);
  const writer = fakeWriter();
  const fetched = [];
  const ledger = new SequentialWriteLedger({ totalItems: 10, nextIndex: 6, bytesWritten: 6 * 32 });

  await downloadHls({
    playlist,
    fetchBytes: async (url) => {
      const index = Number(/seg(\d+)/.exec(url)[1]);
      fetched.push(index);
      return segmentBody(index);
    },
    fetchKey: async () => RAW_KEY,
    writer,
    ledger,
    concurrency: 4
  });

  assert.deepEqual([...fetched].sort((a, b) => a - b), [6, 7, 8, 9]);
  assert.equal(ledger.bytesWritten, 10 * 32);
  assert.equal(writer.bytes[0], 6, '续跑写出的第一段应当是第 6 片');
});

test('初始化分片只在从头开始时写一次', async () => {
  const text = [
    '#EXTM3U',
    '#EXT-X-MAP:URI="init.mp4"',
    '#EXTINF:6,', 'seg0.m4s',
    '#EXTINF:6,', 'seg1.m4s',
    '#EXT-X-ENDLIST'
  ].join('\n');
  const playlist = parsePlaylist(text, BASE);

  const fresh = fakeWriter();
  const fetchedFresh = [];
  await downloadHls({
    playlist,
    fetchBytes: async (url) => { fetchedFresh.push(url); return segmentBody(url.includes('init') ? 255 : 1); },
    fetchKey: async () => RAW_KEY,
    writer: fresh
  });
  assert.equal(fetchedFresh.filter((url) => url.includes('init.mp4')).length, 1);
  assert.equal(fresh.bytes[0], 255, '初始化分片应当排在最前面');

  const resumed = fakeWriter();
  const fetchedResume = [];
  await downloadHls({
    playlist,
    fetchBytes: async (url) => { fetchedResume.push(url); return segmentBody(1); },
    fetchKey: async () => RAW_KEY,
    writer: resumed,
    ledger: new SequentialWriteLedger({ totalItems: 2, nextIndex: 1, bytesWritten: 64 })
  });
  assert.equal(fetchedResume.filter((url) => url.includes('init.mp4')).length, 0, '续跑不该重写初始化分片');
});

test('取消时抛 DownloadStopped，已写的部分留在盘上等续传', async () => {
  const playlist = plainPlaylist(50);
  const writer = fakeWriter();
  let stopped = false;
  let fetches = 0;

  await assert.rejects(
    downloadHls({
      playlist,
      fetchBytes: async (url) => {
        fetches += 1;
        if (fetches > 6) stopped = true;
        return segmentBody(Number(/seg(\d+)/.exec(url)[1]));
      },
      fetchKey: async () => RAW_KEY,
      writer,
      concurrency: 2,
      shouldStop: () => (stopped ? 'canceled' : false)
    }),
    (error) => error instanceof DownloadStopped
  );
  assert.ok(writer.chunks.length > 0, '停下前写出的部分应当留着');
  assert.ok(writer.chunks.length < 50);
});

test('直链支持 Range 时分块并发下载，字节数与声明一致', async () => {
  const writer = fakeWriter();
  const total = 10 * 1000;
  const requested = [];

  const snapshot = await downloadFile({
    url: 'https://cdn/movie.mp4',
    totalBytes: total,
    supportsRange: true,
    chunkSize: 1000,
    concurrency: 3,
    writer,
    fetchBytes: async (url, { byteRange }) => {
      requested.push(byteRange);
      return new Uint8Array(byteRange.length).fill(byteRange.offset / 1000);
    }
  });

  assert.equal(snapshot.bytesWritten, total);
  assert.equal(writer.bytes.length, total);
  assert.equal(requested.length, 10);
  // 落盘顺序按偏移，不按回包先后
  assert.equal(writer.bytes[0], 0);
  assert.equal(writer.bytes[9000], 9);
});

test('末块长度不足一整块也能算对', async () => {
  const writer = fakeWriter();
  const snapshot = await downloadFile({
    url: 'https://cdn/movie.mp4',
    totalBytes: 2500,
    supportsRange: true,
    chunkSize: 1000,
    writer,
    fetchBytes: async (url, { byteRange }) => new Uint8Array(byteRange.length).fill(1)
  });
  assert.equal(snapshot.bytesWritten, 2500);
  assert.equal(writer.chunks.length, 3);
  assert.equal(writer.chunks[2].length, 500);
});

test('服务端少给字节时明确失败，不交出短文件', async () => {
  const writer = fakeWriter();
  await assert.rejects(
    downloadFile({
      url: 'https://cdn/movie.mp4',
      totalBytes: 3000,
      supportsRange: true,
      chunkSize: 1000,
      retries: 0,
      writer,
      fetchBytes: async (url, { byteRange }) =>
        new Uint8Array(byteRange.offset === 2000 ? 10 : byteRange.length)
    }),
    /长度不对/
  );
});

test('不支持 Range 时整取，且不假装能续传', async () => {
  const writer = fakeWriter();
  const snapshot = await downloadFile({
    url: 'https://cdn/movie.mp4',
    totalBytes: 0,
    supportsRange: false,
    writer,
    fetchWhole: async () => new Uint8Array(1234).fill(3)
  });
  assert.equal(snapshot.bytesWritten, 1234);

  await assert.rejects(
    downloadFile({
      url: 'https://cdn/movie.mp4',
      totalBytes: 0,
      supportsRange: false,
      writer: fakeWriter(),
      ledger: new SequentialWriteLedger({ totalItems: 1, nextIndex: 0, bytesWritten: 500 }),
      fetchWhole: async () => new Uint8Array(10)
    }),
    /不支持断点续传/
  );
});
