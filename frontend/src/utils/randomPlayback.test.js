import { afterEach, describe, expect, it } from 'vitest';
import {
  loadRandomMode, loadRecentRandomIDs, normalizeRecentRandomIDs, RANDOM_MODE_STORAGE_KEY, RANDOM_RECENT_STORAGE_KEY,
  rerollWindowOpen, saveRandomMode, saveRecentRandomIDs
} from './randomPlayback.js';

function installStorage(initial = {}, { throwOnAccess = false } = {}) {
  const store = { ...initial };
  const storage = {
    getItem: key => (key in store ? store[key] : null),
    setItem: (key, value) => { store[key] = String(value); },
    removeItem: key => { delete store[key]; }
  };
  Object.defineProperty(window, 'localStorage', {
    configurable: true,
    get() {
      if (throwOnAccess) throw new Error('SecurityError');
      return storage;
    }
  });
  return store;
}

afterEach(() => {
  installStorage();
});

describe('PLAY-08 随机模式与排除表持久化', () => {
  it('PLAY-08 模式与最近 12 次写进 localStorage，下次启动读回', () => {
    const store = installStorage();
    saveRandomMode('unwatched');
    saveRecentRandomIDs([1, 2, 3, 2, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14]);
    expect(store[RANDOM_MODE_STORAGE_KEY]).toBe('unwatched');
    // 只存 ID 数组，去重后保留最近的 12 个。
    expect(JSON.parse(store[RANDOM_RECENT_STORAGE_KEY])).toEqual([3, 2, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14].slice(-12));
    expect(loadRandomMode()).toBe('unwatched');
    expect(loadRecentRandomIDs()).toHaveLength(12);
  });

  it('PLAY-08 存储里的坏值回落到默认：不认识的模式、非 JSON、非法 ID', () => {
    installStorage({ [RANDOM_MODE_STORAGE_KEY]: 'weird', [RANDOM_RECENT_STORAGE_KEY]: '{oops' });
    expect(loadRandomMode()).toBe('balanced');
    expect(loadRecentRandomIDs()).toEqual([]);
    expect(normalizeRecentRandomIDs([0, -1, 'x', 3.5, 7])).toEqual([7]);
    saveRandomMode('everything');
    expect(loadRandomMode()).toBe('balanced');
  });

  it('PLAY-08 localStorage 不可用（访问即抛错）时照常工作，只是不记忆', () => {
    installStorage({}, { throwOnAccess: true });
    expect(loadRandomMode()).toBe('balanced');
    expect(loadRecentRandomIDs()).toEqual([]);
    expect(() => saveRandomMode('favorites')).not.toThrow();
    expect(() => saveRecentRandomIDs([1])).not.toThrow();
  });

  it('PLAY-08 「换一个」窗口是启动后 30 秒', () => {
    const startedAt = 1_000_000;
    expect(rerollWindowOpen(startedAt, startedAt + 29_999)).toBe(true);
    expect(rerollWindowOpen(startedAt, startedAt + 30_000)).toBe(false);
    expect(rerollWindowOpen(0, startedAt)).toBe(false);
  });
});
