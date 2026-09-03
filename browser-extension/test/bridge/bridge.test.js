import test from 'node:test';
import assert from 'node:assert/strict';

import { probeBridge, pushToBridge, BRIDGE_STATUS } from '../../src/common/bridge.js';
import { BRIDGE_PORT_START, BRIDGE_PORT_END, BRIDGE_TOKEN_HEADER } from '../../src/common/constants.js';

// 假 fetch：只有指定端口"在监听"，其余一律连接失败。
function fakeFetch({ listeningPort, body = { app: 'cineinsight', protocol: 1 }, status = 200, onCall } = {}) {
  const calls = [];
  const impl = async (url, options) => {
    calls.push({ url, options });
    if (onCall) onCall({ url, options });
    const port = Number(/:(\d+)\//.exec(url)?.[1]);
    if (port !== listeningPort) throw new Error('ECONNREFUSED');
    return {
      ok: status >= 200 && status < 300,
      status,
      json: async () => body
    };
  };
  impl.calls = calls;
  return impl;
}

test('探测到 CineInsight 且已有令牌时判为可用', async () => {
  const fetchImpl = fakeFetch({ listeningPort: 18115 });
  const result = await probeBridge({ hasToken: true, fetchImpl });

  assert.equal(result.available, true);
  assert.equal(result.status, BRIDGE_STATUS.AVAILABLE);
  assert.equal(result.port, 18115);
  assert.match(result.message, /18115/);
});

test('探测到了但没填令牌：入口置灰，理由说清楚是"要配对"而不是"没检测到"', async () => {
  const fetchImpl = fakeFetch({ listeningPort: 18110 });
  const result = await probeBridge({ hasToken: false, fetchImpl });

  assert.equal(result.available, false);
  assert.equal(result.status, BRIDGE_STATUS.NEEDS_TOKEN);
  assert.equal(result.port, 18110);
  assert.match(result.message, /令牌/);
});

test('桌面端没在跑时判为未检测到', async () => {
  const fetchImpl = fakeFetch({ listeningPort: 0 });
  const result = await probeBridge({ hasToken: true, fetchImpl });

  assert.equal(result.available, false);
  assert.equal(result.status, BRIDGE_STATUS.NOT_RUNNING);
  assert.equal(result.port, 0);
  // 整个区间都试过
  const probed = new Set(fetchImpl.calls.map((call) => Number(/:(\d+)\//.exec(call.url)[1])));
  assert.equal(probed.size, BRIDGE_PORT_END - BRIDGE_PORT_START + 1);
});

test('记住的端口先试，命中就不再扫整个区间', async () => {
  const fetchImpl = fakeFetch({ listeningPort: 18120 });
  const result = await probeBridge({ preferredPort: 18120, hasToken: true, fetchImpl });

  assert.equal(result.port, 18120);
  assert.equal(fetchImpl.calls.length, 1, `命中缓存端口后不该再扫，实际发了 ${fetchImpl.calls.length} 次`);
});

test('端口上蹲着别的程序时不认作 CineInsight', async () => {
  const fetchImpl = fakeFetch({ listeningPort: 18110, body: { app: 'something-else' } });
  const result = await probeBridge({ hasToken: true, fetchImpl });
  assert.equal(result.status, BRIDGE_STATUS.NOT_RUNNING);
});

test('协议号对不上也不认', async () => {
  const fetchImpl = fakeFetch({ listeningPort: 18110, body: { app: 'cineinsight', protocol: 99 } });
  const result = await probeBridge({ hasToken: true, fetchImpl });
  assert.equal(result.status, BRIDGE_STATUS.NOT_RUNNING);
});

test('推送把令牌放在专用请求头里，载荷原样送出', async () => {
  let seen = null;
  const fetchImpl = async (url, options) => {
    seen = { url, options };
    return { ok: true, status: 200, json: async () => ({ id: 'bd1', filename: 'a.mp4' }) };
  };

  const payload = { url: 'https://cdn/a.m3u8', kind: 'hls', title: '某剧', referer: 'https://page/x' };
  const result = await pushToBridge({ port: 18110, token: 'tok', payload, fetchImpl });

  assert.equal(result.filename, 'a.mp4');
  assert.equal(seen.url, 'http://127.0.0.1:18110/bridge/v1/downloads');
  assert.equal(seen.options.method, 'POST');
  assert.equal(seen.options.headers[BRIDGE_TOKEN_HEADER], 'tok');
  assert.equal(seen.options.headers['Content-Type'], 'application/json');
  assert.deepEqual(JSON.parse(seen.options.body), payload);
});

test('令牌不对时给出可区分的提示，而不是笼统的推送失败', async () => {
  const fetchImpl = async () => ({ ok: false, status: 401, json: async () => ({ error: 'invalid_token' }) });
  await assert.rejects(
    pushToBridge({ port: 18110, token: 'bad', payload: {}, fetchImpl }),
    /令牌不对/
  );
});

test('桌面端返回的错误原文透到用户面前', async () => {
  const fetchImpl = async () => ({
    ok: false,
    status: 400,
    json: async () => ({ error: 'download_directory_unset', message: '还没有设置下载目录，请先在设置页选一个' })
  });
  await assert.rejects(
    pushToBridge({ port: 18110, token: 'tok', payload: {}, fetchImpl }),
    /还没有设置下载目录/
  );
});
