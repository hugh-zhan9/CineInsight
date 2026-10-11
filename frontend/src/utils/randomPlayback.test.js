import { afterEach, describe, expect, it } from 'vitest';
import {
  LEGACY_RANDOM_MODE_STORAGE_KEY, loadRandomConditions, loadRecentRandomIDs, normalizeRecentRandomIDs, RANDOM_CONDITIONS_STORAGE_KEY,
  RANDOM_RECENT_STORAGE_KEY, randomConditionFields, randomConditionTags, rerollWindowOpen, saveRandomConditions, saveRecentRandomIDs
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
  it('PLAY-08 随机条件与最近 12 次写进 localStorage，下次启动读回', () => {
    const store = installStorage();
    saveRandomConditions({ watchState: 'unwatched', favoritesOnly: true, maxMinutes: 90 });
    saveRecentRandomIDs([1, 2, 3, 2, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14]);
    expect(JSON.parse(store[RANDOM_CONDITIONS_STORAGE_KEY])).toEqual({ watchState: 'unwatched', favoritesOnly: true, maxMinutes: 90 });
    // 只存 ID 数组，去重后保留最近的 12 个。
    expect(JSON.parse(store[RANDOM_RECENT_STORAGE_KEY])).toEqual([3, 2, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14].slice(-12));
    expect(loadRandomConditions()).toEqual({ watchState: 'unwatched', favoritesOnly: true, maxMinutes: 90 });
    expect(loadRecentRandomIDs()).toHaveLength(12);
  });

  it('PLAY-08 旧版的随机模式在新键缺失时迁移；新键存在时以新键为准', () => {
    installStorage({ [LEGACY_RANDOM_MODE_STORAGE_KEY]: 'unwatched' });
    expect(loadRandomConditions()).toEqual({ watchState: 'unwatched', favoritesOnly: false, maxMinutes: 0 });
    installStorage({ [LEGACY_RANDOM_MODE_STORAGE_KEY]: 'favorites' });
    expect(loadRandomConditions()).toEqual({ watchState: 'any', favoritesOnly: true, maxMinutes: 0 });
    installStorage({ [LEGACY_RANDOM_MODE_STORAGE_KEY]: 'balanced' });
    expect(loadRandomConditions()).toEqual({ watchState: 'any', favoritesOnly: false, maxMinutes: 0 });
    installStorage({ [LEGACY_RANDOM_MODE_STORAGE_KEY]: 'favorites', [RANDOM_CONDITIONS_STORAGE_KEY]: '{"watchState":"watched"}' });
    expect(loadRandomConditions()).toEqual({ watchState: 'watched', favoritesOnly: false, maxMinutes: 0 });
  });

  it('PLAY-08 存储里的坏值逐项回落到默认：不认识的状态、越界分钟、非 JSON、非法 ID', () => {
    installStorage({ [RANDOM_CONDITIONS_STORAGE_KEY]: '{"watchState":"weird","favoritesOnly":"yes","maxMinutes":1441}', [RANDOM_RECENT_STORAGE_KEY]: '{oops' });
    expect(loadRandomConditions()).toEqual({ watchState: 'any', favoritesOnly: false, maxMinutes: 0 });
    expect(loadRecentRandomIDs()).toEqual([]);
    expect(normalizeRecentRandomIDs([0, -1, 'x', 3.5, 7])).toEqual([7]);
    installStorage({ [RANDOM_CONDITIONS_STORAGE_KEY]: '{oops' });
    expect(loadRandomConditions()).toEqual({ watchState: 'any', favoritesOnly: false, maxMinutes: 0 });
    installStorage({ [RANDOM_CONDITIONS_STORAGE_KEY]: '{"watchState":"watched","maxMinutes":2.5}' });
    expect(loadRandomConditions()).toEqual({ watchState: 'watched', favoritesOnly: false, maxMinutes: 0 });
  });

  it('PLAY-08 localStorage 不可用（访问即抛错）时照常工作，只是不记忆', () => {
    installStorage({}, { throwOnAccess: true });
    expect(loadRandomConditions()).toEqual({ watchState: 'any', favoritesOnly: false, maxMinutes: 0 });
    expect(loadRecentRandomIDs()).toEqual([]);
    expect(() => saveRandomConditions({ favoritesOnly: true })).not.toThrow();
    expect(() => saveRecentRandomIDs([1])).not.toThrow();
  });

  it('条件转成后端字段与按钮短标签：只列生效的条件，分钟换成秒', () => {
    expect(randomConditionFields({ watchState: 'watched', favoritesOnly: true, maxMinutes: 45 }))
      .toEqual({ watch_state: 'watched', favorites_only: true, max_duration_seconds: 2700 });
    expect(randomConditionFields({})).toEqual({ watch_state: 'any', favorites_only: false, max_duration_seconds: 0 });
    expect(randomConditionTags({})).toEqual([]);
    expect(randomConditionTags({ watchState: 'unwatched', favoritesOnly: true, maxMinutes: 30 })).toEqual(['探索', '收藏', '≤30分钟']);
  });

  it('PLAY-08 「换一个」窗口是启动后 30 秒', () => {
    const startedAt = 1_000_000;
    expect(rerollWindowOpen(startedAt, startedAt + 29_999)).toBe(true);
    expect(rerollWindowOpen(startedAt, startedAt + 30_000)).toBe(false);
    expect(rerollWindowOpen(0, startedAt)).toBe(false);
  });
});
