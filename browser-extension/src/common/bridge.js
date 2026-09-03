// CineInsight 桌面端桥接客户端。
//
// 桌面端在 127.0.0.1 的一个端口上开着一个只认令牌的小服务（D-B03）。这里负责
// 找到那个端口、把选中的流推过去。找不到、没令牌、令牌不对是三种不同的情况，
// 三种都要能分清楚地告诉用户——统统显示成"推送失败"没法排查。

import { BRIDGE_PORT_START, BRIDGE_PORT_END, BRIDGE_TOKEN_HEADER, BRIDGE_PROTOCOL } from './constants.js';

const PROBE_TIMEOUT_MS = 700;
const PUSH_TIMEOUT_MS = 8000;

export const BRIDGE_STATUS = {
  AVAILABLE: 'available',
  NOT_RUNNING: 'not_running',
  NEEDS_TOKEN: 'needs_token'
};

// 探测：优先试上次记住的端口，没命中再扫整个区间。
// 扫描是并发的，没在监听的端口会立刻连接失败，代价很小。
export async function probeBridge({ preferredPort = 0, hasToken = false, fetchImpl = fetch } = {}) {
  const ports = [];
  if (preferredPort >= BRIDGE_PORT_START && preferredPort <= BRIDGE_PORT_END) ports.push(preferredPort);

  const hit = await firstResponding(ports, fetchImpl);
  if (hit) return describe(hit, hasToken);

  const rest = [];
  for (let port = BRIDGE_PORT_START; port <= BRIDGE_PORT_END; port += 1) {
    if (port !== preferredPort) rest.push(port);
  }
  const scanned = await firstResponding(rest, fetchImpl);
  if (scanned) return describe(scanned, hasToken);

  return {
    available: false,
    status: BRIDGE_STATUS.NOT_RUNNING,
    port: 0,
    message: '没检测到正在运行的 CineInsight（桌面端要开着，且设置里打开了浏览器插件桥接）'
  };
}

function describe(port, hasToken) {
  if (!hasToken) {
    return {
      available: false,
      status: BRIDGE_STATUS.NEEDS_TOKEN,
      port,
      message: '检测到 CineInsight，但还没配对：把桌面端设置页里的令牌填到选项页'
    };
  }
  return { available: true, status: BRIDGE_STATUS.AVAILABLE, port, message: `已连接 CineInsight（端口 ${port}）` };
}

async function firstResponding(ports, fetchImpl) {
  if (ports.length === 0) return 0;
  const results = await Promise.all(ports.map((port) => pingPort(port, fetchImpl)));
  const index = results.findIndex(Boolean);
  return index >= 0 ? ports[index] : 0;
}

async function pingPort(port, fetchImpl) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), PROBE_TIMEOUT_MS);
  try {
    const response = await fetchImpl(`http://127.0.0.1:${port}/bridge/v1/ping`, {
      method: 'GET',
      signal: controller.signal
    });
    if (!response.ok) return false;
    const body = await response.json();
    // 端口区间里可能蹲着别的程序，认准应答内容再说是 CineInsight。
    return body && body.app === 'cineinsight' && Number(body.protocol) === BRIDGE_PROTOCOL;
  } catch {
    return false;
  } finally {
    clearTimeout(timer);
  }
}

export async function pushToBridge({ port, token, payload, fetchImpl = fetch }) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), PUSH_TIMEOUT_MS);
  try {
    const response = await fetchImpl(`http://127.0.0.1:${port}/bridge/v1/downloads`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        [BRIDGE_TOKEN_HEADER]: token
      },
      body: JSON.stringify(payload),
      signal: controller.signal
    });
    const body = await response.json().catch(() => null);
    if (response.status === 401 || response.status === 403) {
      throw new Error('令牌不对或桥接已关闭，去桌面端设置页核对一下');
    }
    if (!response.ok) {
      throw new Error((body && (body.message || body.error)) || `推送失败：HTTP ${response.status}`);
    }
    return body;
  } catch (error) {
    if (error.name === 'AbortError') throw new Error('推送超时，桌面端没有响应');
    throw error;
  } finally {
    clearTimeout(timer);
  }
}

export async function listBridgeTasks({ port, token, fetchImpl = fetch }) {
  const response = await fetchImpl(`http://127.0.0.1:${port}/bridge/v1/downloads`, {
    headers: { [BRIDGE_TOKEN_HEADER]: token }
  });
  if (!response.ok) throw new Error(`取任务列表失败：HTTP ${response.status}`);
  return response.json();
}

// 让桌面端用本机播放器直接播这条流，不下载。
// 请求头一起带过去——这类站点的 CDN 认 Referer，缺了直接 403。
export async function playViaBridge({ port, token, payload, fetchImpl = fetch }) {
  const response = await fetchImpl(`http://127.0.0.1:${port}/bridge/v1/play`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', [BRIDGE_TOKEN_HEADER]: token },
    body: JSON.stringify(payload)
  });
  const body = await response.json().catch(() => null);
  if (response.status === 401 || response.status === 403) {
    throw new Error('令牌不对或桥接已关闭，去桌面端设置页核对一下');
  }
  if (!response.ok) {
    throw new Error((body && (body.message || body.error)) || `播放失败：HTTP ${response.status}`);
  }
  return body;
}
