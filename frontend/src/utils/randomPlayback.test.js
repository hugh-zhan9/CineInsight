import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ PlayRandomVideoWithFilter: vi.fn(), RerollRandom: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);
vi.mock('./feedback.js', () => ({ notify: vi.fn(), notifyError: vi.fn() }));

import * as feedback from './feedback.js';
import {
  applyRandomConditionItem, LEGACY_RANDOM_MODE_STORAGE_KEY, loadRandomConditions, loadRecentRandomIDs, normalizeRecentRandomIDs,
  RANDOM_CONDITIONS_STORAGE_KEY, RANDOM_RECENT_STORAGE_KEY, RANDOM_REROLL_WINDOW_MS, randomConditionFields, randomConditionMenuItems,
  randomConditionTags, randomPlaybackMixin, randomPlayRequest, readStoredRandomState, rerollWindowOpen, saveRandomConditions, saveRecentRandomIDs
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

describe('随机播放的共用部分（D-P2-01：片库管理与视频海报墙共用）', () => {
  it('请求带上宿主的筛选、随机条件与去重后的最近 12 次', () => {
    const filter = { keyword: '甲', collapse_versions: true };
    const recent = [1, 2, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13];
    expect(randomPlayRequest(filter, { watchState: 'watched', favoritesOnly: true, maxMinutes: 30 }, recent)).toEqual({
      filter, watch_state: 'watched', favorites_only: true, max_duration_seconds: 1800, exclude_ids: [2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13]
    });
    expect(randomPlayRequest({}, {}, undefined).exclude_ids).toEqual([]);
  });

  it('▾ 菜单的条件开关：勾选态跟着条件走，有时长上限才出现「取消时长上限」', () => {
    const ids = items => items.map(item => item.id || item.heading || (item.divider ? '—' : ''));
    expect(ids(randomConditionMenuItems({}))).toEqual(['观看状态', 'watch:unwatched', 'watch:any', 'watch:watched', '—', 'favorites']);
    const items = randomConditionMenuItems({ watchState: 'unwatched', favoritesOnly: true, maxMinutes: 45 });
    expect(items.filter(item => item.checked).map(item => item.id)).toEqual(['watch:unwatched', 'favorites']);
    expect(items.at(-1)).toEqual({ id: 'duration:clear', label: '取消时长上限（≤45 分钟）' });
  });

  it('条件开关换成新条件；不认识的项返回 null', () => {
    const base = { watchState: 'any', favoritesOnly: false, maxMinutes: 45 };
    expect(applyRandomConditionItem(base, 'watch:unwatched')).toEqual({ ...base, watchState: 'unwatched' });
    expect(applyRandomConditionItem(base, 'favorites')).toEqual({ ...base, favoritesOnly: true });
    expect(applyRandomConditionItem({ ...base, favoritesOnly: true }, 'favorites')).toEqual(base);
    expect(applyRandomConditionItem(base, 'duration:clear')).toEqual({ ...base, maxMinutes: 0 });
    expect(applyRandomConditionItem(base, 'watch:weird')).toBeNull();
    expect(applyRandomConditionItem(base, 'tonight')).toBeNull();
    expect(applyRandomConditionItem(base, 'pick-ten')).toBeNull();
  });

  it('从存储读回两页共用的状态；存储不可用时返回 null（保留页面内存里那份）', () => {
    installStorage({ [RANDOM_CONDITIONS_STORAGE_KEY]: '{"watchState":"watched"}', [RANDOM_RECENT_STORAGE_KEY]: '[4,5]' });
    expect(readStoredRandomState()).toEqual({ conditions: { watchState: 'watched', favoritesOnly: false, maxMinutes: 0 }, recentIDs: [4, 5] });
    installStorage({}, { throwOnAccess: true });
    expect(readStoredRandomState()).toBeNull();
  });
});

describe('randomPlaybackMixin 的宿主契约', () => {
  const Host = {
    mixins: [randomPlaybackMixin],
    props: { pageActive: { type: Boolean, default: true }, blocked: { type: String, default: '' } },
    data() { return { applied: [] }; },
    methods: {
      randomPlayFilter() { return { keyword: '宿主' }; },
      randomPlayBlockedReason() { return this.blocked; },
      applyPlaybackAttemptResult(result) { this.applied.push(result); }
    },
    template: '<div />'
  };
  const played = (id, token = `tok-${id}`) => ({ dispatch_succeeded: true, video: { id, name: `v${id}.mp4` }, reroll_token: token });

  beforeEach(() => {
    vi.clearAllMocks();
    installStorage();
  });

  it('宿主说不能随机时只提示、不请求', async () => {
    const wrapper = mount(Host, { props: { blocked: '语义搜索结果不支持随机' } });
    await wrapper.vm.playRandom();
    expect(feedback.notify).toHaveBeenCalledWith('语义搜索结果不支持随机');
    expect(api.PlayRandomVideoWithFilter).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it('抽中后记进排除表、出结果条，播放结果交给宿主；30 秒到点结果条改成「已计入」', async () => {
    vi.useFakeTimers();
    try {
      const wrapper = mount(Host);
      api.PlayRandomVideoWithFilter.mockResolvedValueOnce(played(5));
      await wrapper.vm.playRandom();
      expect(api.PlayRandomVideoWithFilter).toHaveBeenCalledWith({ filter: { keyword: '宿主' }, watch_state: 'any', favorites_only: false, max_duration_seconds: 0, exclude_ids: [] });
      expect(wrapper.vm.randomPlayBanner).toEqual({ active: true, videoName: 'v5.mp4', reason: '按当前筛选条件选择', rerollable: true, loading: false });
      expect(wrapper.vm.recentRandomVideoIDs).toEqual([5]);
      expect(wrapper.vm.applied).toHaveLength(1);
      vi.advanceTimersByTime(RANDOM_REROLL_WINDOW_MS);
      expect(wrapper.vm.randomPlayBanner.rerollable).toBe(false);
      wrapper.unmount();
    } finally {
      vi.useRealTimers();
    }
  });

  it('没抽到时收起结果条，失败结果仍交给宿主；请求抛错给出提示', async () => {
    const wrapper = mount(Host);
    api.PlayRandomVideoWithFilter.mockResolvedValueOnce(played(5));
    await wrapper.vm.playRandom();
    api.PlayRandomVideoWithFilter.mockResolvedValueOnce({ dispatch_succeeded: false, reason_code: 'no_filtered_videos' });
    await wrapper.vm.playRandom();
    expect(wrapper.vm.randomPlayBanner).toBeNull();
    expect(wrapper.vm.applied.at(-1)).toEqual({ dispatch_succeeded: false, reason_code: 'no_filtered_videos' });
    api.PlayRandomVideoWithFilter.mockRejectedValueOnce('超时');
    await wrapper.vm.playRandom();
    expect(feedback.notifyError).toHaveBeenCalledWith('随机播放失败: 超时');
    expect(wrapper.vm.randomPlay.loading).toBe(false);
    wrapper.unmount();
  });

  // 原在 VideoListPage.test.js 的「P-036 随机播放：换一个」三条（片库管理三期拿掉了随机，D-P3-03）：「换一个」是混入的
  // 共用流程，搬到这里对着宿主契约测，海报墙与任何宿主都一样。
  it('PLAY-08 30 秒内「换一个」走 RerollRandom，新结果接管结果条，不再另起一次普通随机', async () => {
    const wrapper = mount(Host);
    api.PlayRandomVideoWithFilter.mockResolvedValueOnce(played(5, 'tok-a'));
    await wrapper.vm.playRandom();
    api.RerollRandom.mockResolvedValueOnce(played(6, 'tok-b'));
    await wrapper.vm.rerollRandomPlay();
    await flushPromises();
    expect(api.RerollRandom).toHaveBeenCalledWith('tok-a');
    expect(api.PlayRandomVideoWithFilter).toHaveBeenCalledTimes(1);
    expect(wrapper.vm.randomPlay).toEqual(expect.objectContaining({ token: 'tok-b', rerollable: true }));
    expect(wrapper.vm.randomPlayBanner.videoName).toBe('v6.mp4');
    expect(wrapper.vm.recentRandomVideoIDs).toEqual([5, 6]);
    wrapper.unmount();
  });

  it('PLAY-08 后端答 reroll_expired（已提交）时改为普通的再随机一次，排除刚才那一部', async () => {
    const wrapper = mount(Host);
    api.PlayRandomVideoWithFilter.mockResolvedValueOnce(played(5, 'tok-a'));
    await wrapper.vm.playRandom();
    api.RerollRandom.mockResolvedValueOnce({ dispatch_succeeded: false, reason_code: 'reroll_expired', user_message: '「换一个」已失效' });
    api.PlayRandomVideoWithFilter.mockResolvedValueOnce(played(8, 'tok-c'));
    await wrapper.vm.rerollRandomPlay();
    await flushPromises();
    expect(api.RerollRandom).toHaveBeenCalledWith('tok-a');
    expect(api.PlayRandomVideoWithFilter).toHaveBeenCalledTimes(2);
    expect(api.PlayRandomVideoWithFilter).toHaveBeenLastCalledWith(expect.objectContaining({ exclude_ids: [5] }));
    expect(feedback.notifyError).not.toHaveBeenCalled();
    expect(wrapper.vm.randomPlay.token).toBe('tok-c');
    wrapper.unmount();
  });

  it('PLAY-08 过了 30 秒「换一个」直接普通随机，不再拿旧令牌', async () => {
    const wrapper = mount(Host);
    api.PlayRandomVideoWithFilter.mockResolvedValueOnce(played(5, 'tok-a'));
    await wrapper.vm.playRandom();
    wrapper.vm.randomPlay = { ...wrapper.vm.randomPlay, startedAt: Date.now() - 31_000 };
    api.PlayRandomVideoWithFilter.mockResolvedValueOnce(played(9));
    await wrapper.vm.rerollRandomPlay();
    expect(api.RerollRandom).not.toHaveBeenCalled();
    expect(api.PlayRandomVideoWithFilter).toHaveBeenCalledTimes(2);
    wrapper.unmount();
  });

  it('条件开关回写存储；页面切回来时读回另一页存下的条件与排除表', async () => {
    const wrapper = mount(Host);
    expect(wrapper.vm.applyRandomConditionItem('favorites')).toBe(true);
    expect(wrapper.vm.applyRandomConditionItem('tonight')).toBe(false);
    await flushPromises();
    expect(loadRandomConditions()).toEqual({ watchState: 'any', favoritesOnly: true, maxMinutes: 0 });

    saveRandomConditions({ watchState: 'unwatched', favoritesOnly: false, maxMinutes: 0 });
    saveRecentRandomIDs([8, 9]);
    await wrapper.setProps({ pageActive: false });
    expect(wrapper.vm.randomConditions.watchState).toBe('any');
    await wrapper.setProps({ pageActive: true });
    expect(wrapper.vm.randomConditions).toEqual({ watchState: 'unwatched', favoritesOnly: false, maxMinutes: 0 });
    expect(wrapper.vm.recentRandomVideoIDs).toEqual([8, 9]);
    wrapper.unmount();
  });
});
