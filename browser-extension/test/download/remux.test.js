import test from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, writeFileSync, openSync, readSync, closeSync, statSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { remuxTsToMp4, REMUX_CHUNK_SIZE } from '../../src/download/remux.js';

const here = path.dirname(fileURLToPath(import.meta.url));
const vendorPath = path.join(here, '../../vendor/mux-mp4.min.js');

function ffmpegPath() {
  for (const candidate of ['ffmpeg', '/opt/homebrew/bin/ffmpeg', '/usr/local/bin/ffmpeg']) {
    const probe = spawnSync(candidate, ['-version'], { encoding: 'utf8' });
    if (!probe.error && probe.status === 0) return candidate;
  }
  return null;
}

function ffprobePath() {
  for (const candidate of ['ffprobe', '/opt/homebrew/bin/ffprobe', '/usr/local/bin/ffprobe']) {
    const probe = spawnSync(candidate, ['-version'], { encoding: 'utf8' });
    if (!probe.error && probe.status === 0) return candidate;
  }
  return null;
}

// vendor 里的 mux.js 是 UMD，且依赖一个 window 全局。Worker 与 Node 里都没有 window，
// 因此加载前要把它指到全局对象上——扩展的下载 worker 里用的是同一招。
async function loadMuxjs() {
  globalThis.window = globalThis.window || globalThis;
  globalThis.self = globalThis.self || globalThis;
  const source = readFileSync(vendorPath, 'utf8');
  // 把 module/exports/define 遮蔽掉，逼 UMD 走"挂到全局"那条分支——那正是浏览器
  // Worker 里会走的路。不遮蔽的话 Node 的模块作用域会让它走 CommonJS 分支，
  // 测的就不是线上那条路径了。
  const load = new Function(
    'globalThis', 'window', 'self', 'exports', 'module', 'define', 'require',
    `${source}\nreturn globalThis.muxjs;`
  );
  return load(globalThis, globalThis, globalThis, undefined, undefined, undefined, undefined);
}

test('mux.js 能在没有 window 的环境里加载起来', async () => {
  const muxjs = await loadMuxjs();
  assert.ok(muxjs, 'mux.js 没有挂到全局上');
  // mp4 专用构建把 Transmuxer 放在顶层，全量构建放在 .mp4 下面
  const Transmuxer = (muxjs.mp4 && muxjs.mp4.Transmuxer) || muxjs.Transmuxer;
  assert.ok(Transmuxer, '拿不到 Transmuxer');
});

test('真的把 MPEG-TS 转成能被 ffprobe 认出来的 mp4', async () => {
  const ffmpeg = ffmpegPath();
  const ffprobe = ffprobePath();
  if (!ffmpeg || !ffprobe) {
    console.log('没装 ffmpeg/ffprobe，跳过转封装的真机验证');
    return;
  }

  const dir = mkdtempSync(path.join(tmpdir(), 'cine-remux-'));
  const tsPath = path.join(dir, 'source.ts');
  // 造一段真的 TS：h264 视频 + aac 音频，正是 HLS 分片的典型构成
  try {
    execFileSync(ffmpeg, [
      '-nostdin', '-v', 'error',
      '-f', 'lavfi', '-i', 'testsrc=size=128x128:rate=15:duration=3',
      '-f', 'lavfi', '-i', 'sine=frequency=440:duration=3',
      '-c:v', 'libx264', '-pix_fmt', 'yuv420p', '-c:a', 'aac',
      '-f', 'mpegts', '-y', tsPath
    ]);
  } catch (error) {
    console.log('造 TS 素材失败（ffmpeg 可能缺 libx264/aac），跳过：', error.message);
    return;
  }

  const muxjs = await loadMuxjs();
  const fd = openSync(tsPath, 'r');
  const size = statSync(tsPath).size;
  const output = [];

  try {
    const result = await remuxTsToMp4({
      muxjs,
      totalBytes: size,
      readChunk: async (offset, length) => {
        const buffer = Buffer.alloc(Math.min(length, Math.max(0, size - offset)));
        if (buffer.length === 0) return new Uint8Array(0);
        readSync(fd, buffer, 0, buffer.length, offset);
        return new Uint8Array(buffer);
      },
      write: (bytes) => output.push(Buffer.from(bytes))
    });
    assert.ok(result.outputBytes > 0, '没有产出任何数据');
  } finally {
    closeSync(fd);
  }

  const mp4Path = path.join(dir, 'out.mp4');
  writeFileSync(mp4Path, Buffer.concat(output));

  // 最终判据：ffprobe 认不认这个文件。自己断言几个字节没有意义，
  // 用户拿到手是要用播放器打开的。
  const probe = spawnSync(ffprobe, [
    '-v', 'error', '-show_entries', 'format=format_name', '-show_entries', 'stream=codec_type',
    '-of', 'default=noprint_wrappers=1', mp4Path
  ], { encoding: 'utf8' });

  assert.equal(probe.status, 0, `ffprobe 不认这个文件：${probe.stderr}`);
  assert.match(probe.stdout, /format_name=mov,mp4|format_name=.*mp4/, `产物不是 mp4：${probe.stdout}`);
  assert.match(probe.stdout, /codec_type=video/, `产物里没有视频轨：${probe.stdout}`);
});

test('分块大小必须是 TS 包长的整数倍，否则会把包从中间切断', () => {
  assert.equal(REMUX_CHUNK_SIZE % 188, 0);
});

test('数据不是 TS 时明确失败，不交出一个空文件', async () => {
  const muxjs = await loadMuxjs();
  const junk = new Uint8Array(4096).fill(0x41);
  let consumed = false;
  await assert.rejects(
    remuxTsToMp4({
      muxjs,
      readChunk: async () => {
        if (consumed) return new Uint8Array(0);
        consumed = true;
        return junk;
      },
      write: () => {}
    }),
    /没有产出任何数据/
  );
});
