import test from 'node:test';
import assert from 'node:assert/strict';

import { parsePlaylist, parseAttributes, variantLabel } from '../../src/common/hls/parser.js';

const BASE = 'https://cdn.example.com/v/2024/index.m3u8';

test('master 播放列表按码率从高到低展开，带分辨率与音轨渲染', () => {
  const text = [
    '#EXTM3U',
    '#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aac",NAME="国语",LANGUAGE="zh",DEFAULT=YES,URI="audio/zh.m3u8"',
    '#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360,CODECS="avc1.42c01e,mp4a.40.2"',
    '360/index.m3u8',
    '#EXT-X-STREAM-INF:BANDWIDTH=4200000,AVERAGE-BANDWIDTH=3900000,RESOLUTION=1920x1080,FRAME-RATE=29.97,AUDIO="aac"',
    'https://other.example.com/1080/index.m3u8'
  ].join('\n');

  const result = parsePlaylist(text, BASE);
  assert.equal(result.type, 'master');
  assert.equal(result.variants.length, 2);
  assert.equal(result.variants[0].label, '1080p');
  assert.equal(result.variants[0].uri, 'https://other.example.com/1080/index.m3u8');
  assert.equal(result.variants[0].frameRate, 29.97);
  assert.equal(result.variants[0].averageBandwidth, 3900000);
  assert.equal(result.variants[1].label, '360p');
  // 相对 URI 按基址解析
  assert.equal(result.variants[1].uri, 'https://cdn.example.com/v/2024/360/index.m3u8');
  assert.equal(result.audioRenditions.length, 1);
  assert.equal(result.audioRenditions[0].uri, 'https://cdn.example.com/v/2024/audio/zh.m3u8');
  assert.equal(result.audioRenditions[0].isDefault, true);
});

test('media 播放列表算出总时长、序号与容器', () => {
  const text = [
    '#EXTM3U',
    '#EXT-X-VERSION:3',
    '#EXT-X-TARGETDURATION:10',
    '#EXT-X-MEDIA-SEQUENCE:7',
    '#EXTINF:9.009,片头',
    'seg0.ts',
    '#EXTINF:8.5,',
    'seg1.ts',
    '#EXT-X-ENDLIST'
  ].join('\n');

  const result = parsePlaylist(text, BASE);
  assert.equal(result.type, 'media');
  assert.equal(result.segments.length, 2);
  assert.equal(result.isLive, false);
  assert.equal(result.containerHint, 'ts');
  assert.equal(result.outputExtension, 'ts');
  assert.ok(Math.abs(result.totalDuration - 17.509) < 1e-9);
  assert.equal(result.segments[0].mediaSequence, 7);
  assert.equal(result.segments[1].mediaSequence, 8);
  assert.equal(result.segments[0].title, '片头');
  assert.equal(result.segments[0].uri, 'https://cdn.example.com/v/2024/seg0.ts');
  assert.equal(result.encryption.supported, true);
  assert.equal(result.encryption.method, 'NONE');
});

test('AES-128 认作可下载，IV 缺省时留空交给下载侧按序号推导', () => {
  const text = [
    '#EXTM3U',
    '#EXT-X-TARGETDURATION:10',
    '#EXT-X-KEY:METHOD=AES-128,URI="key.bin"',
    '#EXTINF:10,',
    'seg0.ts',
    '#EXT-X-KEY:METHOD=AES-128,URI="key2.bin",IV=0x0123456789ABCDEF0123456789ABCDEF',
    '#EXTINF:10,',
    'seg1.ts',
    '#EXT-X-ENDLIST'
  ].join('\n');

  const result = parsePlaylist(text, BASE);
  assert.equal(result.encryption.supported, true);
  assert.equal(result.encryption.method, 'AES-128');
  assert.equal(result.segments[0].key.uri, 'https://cdn.example.com/v/2024/key.bin');
  assert.equal(result.segments[0].key.iv, null);
  assert.equal(result.segments[1].key.iv, '0123456789abcdef0123456789abcdef');
});

test('半截 IV 当作没写，不拿它去解出乱码', () => {
  const text = [
    '#EXTM3U',
    '#EXT-X-KEY:METHOD=AES-128,URI="key.bin",IV=0x00FF',
    '#EXTINF:10,',
    'seg0.ts',
    '#EXT-X-ENDLIST'
  ].join('\n');
  const result = parsePlaylist(text, BASE);
  assert.equal(result.segments[0].key.iv, null);
});

test('SAMPLE-AES 与 DRM KEYFORMAT 明确不可下载并带出原因（D-B02）', () => {
  const sampleAes = parsePlaylist([
    '#EXTM3U',
    '#EXT-X-KEY:METHOD=SAMPLE-AES,URI="skd://x"',
    '#EXTINF:10,',
    'seg0.ts',
    '#EXT-X-ENDLIST'
  ].join('\n'), BASE);
  assert.equal(sampleAes.encryption.supported, false);
  assert.match(sampleAes.encryption.reason, /SAMPLE-AES/);

  const widevine = parsePlaylist([
    '#EXTM3U',
    '#EXT-X-KEY:METHOD=AES-128,URI="https://lic/x",KEYFORMAT="urn:uuid:edef8ba9-79d6-4ace-a3c8-27dcd51d21ed"',
    '#EXTINF:10,',
    'seg0.ts',
    '#EXT-X-ENDLIST'
  ].join('\n'), BASE);
  assert.equal(widevine.encryption.supported, false);
  assert.match(widevine.encryption.reason, /DRM/);
});

test('BYTERANGE 省略 offset 时接在同一 URI 的上一段之后', () => {
  const text = [
    '#EXTM3U',
    '#EXTINF:10,',
    '#EXT-X-BYTERANGE:1000@0',
    'all.ts',
    '#EXTINF:10,',
    '#EXT-X-BYTERANGE:2000',
    'all.ts',
    '#EXTINF:10,',
    '#EXT-X-BYTERANGE:500@9000',
    'all.ts',
    '#EXT-X-ENDLIST'
  ].join('\n');

  const result = parsePlaylist(text, BASE);
  assert.deepEqual(result.segments[0].byteRange, { length: 1000, offset: 0 });
  assert.deepEqual(result.segments[1].byteRange, { length: 2000, offset: 1000 });
  assert.deepEqual(result.segments[2].byteRange, { length: 500, offset: 9000 });
});

test('EXT-X-MAP 初始化分片让容器判为 fmp4，输出扩展名为 mp4', () => {
  const text = [
    '#EXTM3U',
    '#EXT-X-MAP:URI="init.mp4"',
    '#EXTINF:6,',
    'seg0.m4s',
    '#EXT-X-ENDLIST'
  ].join('\n');

  const result = parsePlaylist(text, BASE);
  assert.equal(result.initSegment.uri, 'https://cdn.example.com/v/2024/init.mp4');
  assert.equal(result.containerHint, 'fmp4');
  assert.equal(result.outputExtension, 'mp4');
});

test('没有 ENDLIST 判为直播；PLAYLIST-TYPE:VOD 不算直播', () => {
  const live = parsePlaylist(['#EXTM3U', '#EXTINF:4,', 'a.ts'].join('\n'), BASE);
  assert.equal(live.isLive, true);

  const vod = parsePlaylist(['#EXTM3U', '#EXT-X-PLAYLIST-TYPE:VOD', '#EXTINF:4,', 'a.ts'].join('\n'), BASE);
  assert.equal(vod.isLive, false);
});

test('DISCONTINUITY 标在它后面那一条分片上', () => {
  const text = [
    '#EXTM3U',
    '#EXTINF:4,',
    'a.ts',
    '#EXT-X-DISCONTINUITY',
    '#EXTINF:4,',
    'b.ts',
    '#EXT-X-ENDLIST'
  ].join('\n');
  const result = parsePlaylist(text, BASE);
  assert.equal(result.segments[0].discontinuity, false);
  assert.equal(result.segments[1].discontinuity, true);
});

test('畸形输入返回可读原因而不是抛异常', () => {
  for (const [input, pattern] of [
    ['', /#EXTM3U/],
    ['随便一段网页 HTML', /#EXTM3U/],
    ['#EXTM3U\n#EXT-X-ENDLIST', /没有任何分片/],
    ['#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\n', /没有可用的码率条目/]
  ]) {
    const result = parsePlaylist(input, BASE);
    assert.equal(result.type, 'invalid', `输入 ${JSON.stringify(input)} 应判为 invalid`);
    assert.match(result.reason, pattern);
  }
  assert.equal(parsePlaylist(null, BASE).type, 'invalid');
  assert.equal(parsePlaylist(undefined, BASE).type, 'invalid');
});

test('没有 EXTINF 的裸行不会被当成分片', () => {
  const text = ['#EXTM3U', '# 注释', 'stray-line-without-extinf.ts', '#EXTINF:4,', 'a.ts', '#EXT-X-ENDLIST'].join('\n');
  const result = parsePlaylist(text, BASE);
  assert.equal(result.segments.length, 1);
  assert.equal(result.segments[0].uri, 'https://cdn.example.com/v/2024/a.ts');
});

test('属性列表能处理引号内的逗号', () => {
  const attrs = parseAttributes('BANDWIDTH=1,CODECS="avc1.4d401f,mp4a.40.2",RESOLUTION=1280x720');
  assert.equal(attrs.CODECS, 'avc1.4d401f,mp4a.40.2');
  assert.equal(attrs.RESOLUTION, '1280x720');
  assert.equal(attrs.BANDWIDTH, '1');
});

test('码率标签优先用分辨率，没有分辨率退到 kbps', () => {
  assert.equal(variantLabel({ width: 1920, height: 1080 }, 5000000), '1080p');
  assert.equal(variantLabel(null, 1500000), '1500kbps');
  assert.equal(variantLabel(null, 0), '默认');
});
