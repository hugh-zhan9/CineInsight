// 扩展设置：默认值、归一化与读写。
//
// 归一化是纯函数，可单测；读写走 chrome.storage.local，在没有 chrome 的环境里
// （单测）退化成内存，测试因此不需要造一整套 chrome mock。

import { STORAGE_KEYS } from './constants.js';

export const DEFAULT_SETTINGS = {
  // 分片并发。太高会被 CDN 限速甚至封，6 是经验上的稳妥值。
  concurrency: 6,
  // 单个分片的重试次数，用尽即整个任务失败并保留断点。
  retries: 3,
  retryBackoffMs: 800,
  // 直链分块下载的块大小（字节）。
  chunkSize: 4 * 1024 * 1024,
  // 推送到 CineInsight 时是否附带 Cookie。默认关：Cookie 是登录凭据，
  // 交给另一个进程是用户该显式点头的事。
  attachCookieOnPush: false,
  // MSE 捕获默认关：它要在页面里缓存整段媒体，内存代价由用户决定是否承担。
  captureByDefault: false,
  // 页面内嵌流（MSE）与缺清单的分片都下载不了，默认不在列表里占地方。
  // 需要用 MSE 捕获、或想看到"这里有流但缺清单"的提示时再打开。
  showUndownloadable: false,
  bridgeToken: '',
  // 0 表示自动探测端口。
  bridgePort: 0
};

const LIMITS = {
  concurrency: { min: 1, max: 16 },
  retries: { min: 0, max: 10 },
  retryBackoffMs: { min: 100, max: 30_000 },
  chunkSize: { min: 256 * 1024, max: 64 * 1024 * 1024 }
};

export function normalizeSettings(raw) {
  const source = raw && typeof raw === 'object' ? raw : {};
  const settings = { ...DEFAULT_SETTINGS };

  for (const key of Object.keys(LIMITS)) {
    settings[key] = clampInt(source[key], DEFAULT_SETTINGS[key], LIMITS[key].min, LIMITS[key].max);
  }
  settings.attachCookieOnPush = Boolean(source.attachCookieOnPush);
  settings.captureByDefault = Boolean(source.captureByDefault);
  settings.showUndownloadable = Boolean(source.showUndownloadable);
  settings.bridgeToken = typeof source.bridgeToken === 'string' ? source.bridgeToken.trim() : '';
  settings.bridgePort = clampInt(source.bridgePort, 0, 0, 65535);
  return settings;
}

function clampInt(value, fallback, min, max) {
  const parsed = Math.trunc(Number(value));
  if (!Number.isFinite(parsed)) return fallback;
  if (parsed < min) return min;
  if (parsed > max) return max;
  return parsed;
}

const memoryStore = new Map();

function storageArea() {
  return typeof chrome !== 'undefined' && chrome.storage ? chrome.storage.local : null;
}

export async function loadSettings() {
  const area = storageArea();
  if (!area) return normalizeSettings(memoryStore.get(STORAGE_KEYS.SETTINGS));
  const stored = await area.get(STORAGE_KEYS.SETTINGS);
  return normalizeSettings(stored ? stored[STORAGE_KEYS.SETTINGS] : null);
}

export async function saveSettings(patch) {
  const current = await loadSettings();
  const next = normalizeSettings({ ...current, ...patch });
  const area = storageArea();
  if (!area) {
    memoryStore.set(STORAGE_KEYS.SETTINGS, next);
    return next;
  }
  await area.set({ [STORAGE_KEYS.SETTINGS]: next });
  return next;
}
