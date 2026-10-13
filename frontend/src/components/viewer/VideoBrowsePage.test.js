import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
  CountLibraryVideos: vi.fn(),
  ListCollectionProgress: vi.fn(),
  ListPeople: vi.fn(),
  ListRecentlyPlayedWithFilter: vi.fn(),
  PlayVideo: vi.fn(),
  PlayRandomVideoWithFilter: vi.fn(),
  RerollRandom: vi.fn(),
  SearchLibraryVideoPage: vi.fn(),
  SuggestTonightVideos: vi.fn()
}));
vi.mock('../../../wailsjs/go/main/App', async importOriginal => ({ ...(await importOriginal()), ...api }));
vi.mock('../../utils/feedback.js', async importOriginal => ({ ...(await importOriginal()), notify: vi.fn(), notifyError: vi.fn() }));

import VideoBrowsePage, {
  collectionProgressCard, recentlyPlayedNextCursor, VIDEO_BROWSE_PAGE_SIZE, VIDEO_BROWSE_WATCH_OPTIONS, videoBrowseFilter, videoBrowseMark
} from './VideoBrowsePage.vue';
import BaseMenu from '../ui/BaseMenu.vue';
import RandomPickBanner from '../video-list/RandomPickBanner.vue';
import * as feedback from '../../utils/feedback.js';
import { VIDEO_BROWSE_SMART_VIEWS } from '../../utils/appCommands.js';
import { RANDOM_CONDITIONS_STORAGE_KEY, RANDOM_RECENT_STORAGE_KEY } from '../../utils/randomPlayback.js';

enableAutoUnmount(afterEach);

const video = (id, extra = {}) => ({ id, name: `v${id}.mp4`, display_title: `影片${id}`, year: 2000 + id, duration: 6000, watch_position_seconds: 0, is_watched: false, ...extra });
const page = (videos, nextCursor = null, extra = {}) => ({ videos, next_cursor: nextCursor, version_groups: {}, ...extra });
const progress = (id, extra = {}) => ({
  collection_id: id, name: `作品集${id}`, has_cover: false, total_works: 5, watched_works: 2, in_progress_works: 0, complete: false,
  next: { video_id: 100 + id, title: `下一部${id}`, work_index: 3, resume: false, watch_position_seconds: 0, duration: 0 },
  last_activity_at: '2026-10-12T10:00:00Z',
  ...extra
});
const DEFAULT_FILTER = { search_mode: 'file', keyword: '', smart_view: '', tag_ids: [], person_ids: [], sort_mode: 'added_desc', collapse_versions: true };
const tags = [
  { id: 1, name: '科幻', namespace: '类型' },
  { id: 2, name: '喜剧', namespace: '类型' },
  { id: 3, name: '自动分类', namespace: '自动' },
  { id: 4, name: '收藏夹', namespace: '' }
];

let store;
function installStorage(initial = {}) {
  store = { ...initial };
  Object.defineProperty(window, 'localStorage', {
    configurable: true,
    get: () => ({
      getItem: key => (key in store ? store[key] : null),
      setItem: (key, value) => { store[key] = String(value); },
      removeItem: key => { delete store[key]; }
    })
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  installStorage();
  api.SearchLibraryVideoPage.mockReset().mockResolvedValue(page([video(1), video(2)]));
  api.CountLibraryVideos.mockReset().mockResolvedValue(1234);
  api.ListCollectionProgress.mockReset().mockResolvedValue([]);
  api.ListPeople.mockReset().mockResolvedValue([]);
  api.ListRecentlyPlayedWithFilter.mockReset().mockResolvedValue([]);
  api.PlayVideo.mockReset().mockResolvedValue({ dispatch_succeeded: true });
  api.PlayRandomVideoWithFilter.mockReset();
  api.RerollRandom.mockReset();
  api.SuggestTonightVideos.mockReset().mockResolvedValue([]);
});

async function mountPage(props = {}, options = {}) {
  const wrapper = mount(VideoBrowsePage, {
    props: { tags, ...props },
    global: { stubs: { teleport: true, TonightPickerDialog: true } },
    ...options
  });
  await flushPromises();
  return wrapper;
}

const lastRequest = () => api.SearchLibraryVideoPage.mock.calls.at(-1)[0];

describe('VideoBrowsePage pure helpers', () => {
  it('builds the poster-wall LibraryFilter: file search, collapsed versions, the chosen sort', () => {
    expect(videoBrowseFilter()).toEqual(DEFAULT_FILTER);
    expect(videoBrowseFilter({ keyword: '  星际 ', watch: 'favorites', tagIDs: [3, '5'], people: [{ id: 9, name: '甲' }], sort: 'title_asc' })).toEqual({
      ...DEFAULT_FILTER, keyword: '星际', smart_view: 'favorites', tag_ids: [3, 5], person_ids: [9], sort_mode: 'title_asc'
    });
  });

  it('marks a card 在看 when resumable (same rule as the 在看 view), else 看过 when watched', () => {
    expect(videoBrowseMark(video(1))).toBeNull();
    expect(videoBrowseMark(video(1, { watch_position_seconds: 120 }))).toEqual({ kind: 'progress', label: '在看' });
    expect(videoBrowseMark(video(1, { is_watched: true }))).toEqual({ kind: 'watched', label: '看过' });
    // 看完后留下的旧断点不算在看（resumable 的重看规则）。
    expect(videoBrowseMark(video(1, { is_watched: true, watch_position_seconds: 50, watched_at: '2026-10-12T10:00:00Z', watch_progress_updated_at: '2026-10-11T10:00:00Z' })))
      .toEqual({ kind: 'watched', label: '看过' });
  });

  it('maps collection progress to a card: cover or next poster, 第 N / M 部, next title; finished ones drop out', () => {
    expect(collectionProgressCard(progress(4))).toEqual({
      key: 4, collectionID: 4, videoID: 104, title: '作品集4', cover: '/preview/video-artwork/104?kind=poster',
      position: '第 3 / 5 部', nextTitle: '下一部4', subtitle: '第 3 / 5 部 · 下一部4', badge: ''
    });
    const covered = collectionProgressCard(progress(5, { has_cover: true, next: { video_id: 7, title: '', work_index: 2, resume: true } }));
    expect(covered).toMatchObject({ cover: '/preview/collection-cover/5', subtitle: '第 2 / 5 部', badge: '续播', videoID: 7 });
    expect(collectionProgressCard(progress(6, { next: undefined, complete: true }))).toBeNull();
    expect(collectionProgressCard(null)).toBeNull();
  });

  it('offers exactly the watch states the command palette routes here', () => {
    expect(VIDEO_BROWSE_WATCH_OPTIONS.map(option => option.value)).toEqual([...VIDEO_BROWSE_SMART_VIEWS]);
    expect(VIDEO_BROWSE_WATCH_OPTIONS.map(option => option.label)).toEqual(['全部', '没看过', '在看', '看过', '收藏', '点赞', '最近播放']);
  });

  it('does not collapse versions for 最近播放 (that listing is per file), so the count matches the list', () => {
    expect(videoBrowseFilter({ watch: 'recently_played' })).toEqual({ ...DEFAULT_FILTER, smart_view: 'recently_played', collapse_versions: false });
    expect(videoBrowseFilter({ watch: 'liked' })).toEqual({ ...DEFAULT_FILTER, smart_view: 'liked' });
  });

  it('builds the 最近播放 cursor from the last row only when a full page came back', () => {
    const rows = n => Array.from({ length: n }, (_, index) => ({ id: 100 - index, last_played_at: `2026-10-13T10:00:0${index % 10}.123456789+08:00` }));
    expect(recentlyPlayedNextCursor(rows(3), 3)).toEqual({ lastPlayedAt: '2026-10-13T10:00:02.123456789+08:00', id: 98 });
    expect(recentlyPlayedNextCursor(rows(2), 3)).toBeNull();
    expect(recentlyPlayedNextCursor([], 3)).toBeNull();
    expect(recentlyPlayedNextCursor([{ id: 5, last_played_at: null }], 1)).toBeNull();
    expect(recentlyPlayedNextCursor(null)).toBeNull();
  });
});

describe('VideoBrowsePage list', () => {
  it('loads the first batch of 60 with the default filter, the card count and the continue strip', async () => {
    api.SearchLibraryVideoPage.mockResolvedValue(page(
      [video(1, { watch_position_seconds: 300 }), video(2, { is_watched: true }), video(3, { year: 0, display_title: '' })],
      null,
      { version_groups: { 2: { member_count: 3 } } }
    ));
    const wrapper = await mountPage();
    expect(api.SearchLibraryVideoPage).toHaveBeenCalledWith({ filter: DEFAULT_FILTER, limit: VIDEO_BROWSE_PAGE_SIZE });
    expect(VIDEO_BROWSE_PAGE_SIZE).toBe(60);
    expect(api.CountLibraryVideos).toHaveBeenCalledWith(DEFAULT_FILTER);
    expect(api.ListCollectionProgress).toHaveBeenCalledWith({ collection_ids: [], continue_only: true, limit: 12 });
    expect(wrapper.find('.ui-page-header__subtitle').text()).toBe('共 1,234 部');

    const cards = wrapper.findAll('[data-test="browse-card"]');
    expect(cards).toHaveLength(3);
    expect(cards[0].get('img').attributes('src')).toBe('/preview/video-artwork/1?kind=poster');
    expect(cards[0].get('img').attributes('loading')).toBe('lazy');
    expect(cards[0].text()).toContain('影片1');
    expect(cards[0].text()).toContain('2001');
    expect(cards[0].get('[data-test="browse-card-mark"]').text()).toBe('在看');
    expect(cards[1].get('[data-test="browse-card-mark"]').text()).toBe('看过');
    expect(cards[1].text()).toContain('3 个版本');
    expect(cards[2].text()).toContain('v3.mp4');
    expect(cards[2].text()).toContain('年份未知');
    expect(cards[2].find('[data-test="browse-card-mark"]').exists()).toBe(false);
    // 「追到哪了」没有数据不显示。
    expect(wrapper.find('[data-test="browse-progress"]').exists()).toBe(false);

    await cards[0].trigger('click');
    expect(wrapper.emitted('open-video')).toEqual([[1]]);
  });

  it('falls back to an icon when the poster (and its thumbnail fallback) fails to load', async () => {
    const wrapper = await mountPage();
    await wrapper.findAll('[data-test="browse-card"] img')[0].trigger('error');
    expect(wrapper.findAll('[data-test="browse-card"]')[0].find('img').exists()).toBe(false);
    expect(wrapper.findAll('[data-test="browse-card"]')[1].find('img').exists()).toBe(true);
  });

  it('maps watch states to smart views and sorts to sort modes, reloading from the first batch', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-test="browse-watch-continue"]').trigger('click');
    await flushPromises();
    expect(lastRequest()).toEqual({ filter: { ...DEFAULT_FILTER, smart_view: 'continue_watching' }, limit: 60 });
    expect(wrapper.find('.ui-page-header__subtitle').text()).toBe('筛选出 1,234 部');

    const sorts = wrapper.findAll('[data-test="browse-sort"] option').map(option => [option.element.value, option.text()]);
    expect(sorts).toEqual([['added_desc', '最近添加'], ['year_desc', '年份'], ['title_asc', '片名'], ['rating_desc', '评分'], ['balanced', '均衡']]);
    await wrapper.get('[data-test="browse-sort"]').setValue('year_desc');
    await flushPromises();
    expect(lastRequest()).toEqual({ filter: { ...DEFAULT_FILTER, smart_view: 'continue_watching', sort_mode: 'year_desc' }, limit: 60 });
    expect(lastRequest().cursor).toBeUndefined();
  });

  it('pages with the returned cursor, appends without duplicates and stops when there is no cursor', async () => {
    const cursor = { sort_mode: 'added_desc', created_at: '2026-10-01T00:00:00Z', id: 2 };
    api.SearchLibraryVideoPage.mockResolvedValueOnce(page([video(1), video(2)], cursor));
    const wrapper = await mountPage();
    expect(wrapper.find('[data-test="browse-load-more"]').exists()).toBe(true);
    api.SearchLibraryVideoPage.mockResolvedValueOnce(page([video(2), video(3)], null));
    await wrapper.get('[data-test="browse-load-more"]').trigger('click');
    await flushPromises();
    expect(lastRequest()).toEqual({ filter: DEFAULT_FILTER, limit: 60, cursor });
    expect(wrapper.findAll('[data-test="browse-card"]').map(card => card.text())).toEqual([
      expect.stringContaining('影片1'), expect.stringContaining('影片2'), expect.stringContaining('影片3')
    ]);
    expect(wrapper.find('[data-test="browse-load-more"]').exists()).toBe(false);
    expect(wrapper.vm.loadMore()).toBeNull();
  });

  it('drops a late page from an older filter', async () => {
    let resolveOld;
    api.SearchLibraryVideoPage.mockReturnValueOnce(new Promise(resolve => { resolveOld = resolve; }));
    const wrapper = await mountPage();
    api.SearchLibraryVideoPage.mockResolvedValueOnce(page([video(9)]));
    await wrapper.get('[data-test="browse-watch-watched"]').trigger('click');
    await flushPromises();
    resolveOld(page([video(1), video(2)]));
    await flushPromises();
    expect(wrapper.findAll('[data-test="browse-card"]')).toHaveLength(1);
    expect(wrapper.get('[data-test="browse-card"]').text()).toContain('影片9');
  });

  it('debounces typing, skips IME composition, and searches immediately on Enter', async () => {
    vi.useFakeTimers();
    try {
      const wrapper = mount(VideoBrowsePage, { props: { tags }, global: { stubs: { teleport: true, TonightPickerDialog: true } } });
      await vi.runAllTimersAsync();
      const calls = api.SearchLibraryVideoPage.mock.calls.length;
      const input = wrapper.get('[data-test="browse-search"]');
      input.element.value = '星';
      await input.trigger('input', { isComposing: true });
      await vi.advanceTimersByTimeAsync(400);
      expect(api.SearchLibraryVideoPage.mock.calls.length).toBe(calls);
      await input.setValue('星际');
      await vi.advanceTimersByTimeAsync(299);
      expect(api.SearchLibraryVideoPage.mock.calls.length).toBe(calls);
      await vi.advanceTimersByTimeAsync(1);
      expect(api.SearchLibraryVideoPage.mock.calls.length).toBe(calls + 1);
      expect(lastRequest().filter.keyword).toBe('星际');
      await input.setValue('星际穿越');
      await input.trigger('keyup', { key: 'Enter' });
      expect(lastRequest().filter.keyword).toBe('星际穿越');
      await vi.advanceTimersByTimeAsync(400);
      expect(api.SearchLibraryVideoPage.mock.calls.length).toBe(calls + 2);
    } finally {
      vi.useRealTimers();
    }
  });

  it('shows skeletons while the first batch loads, then a retryable error banner', async () => {
    api.SearchLibraryVideoPage.mockReturnValueOnce(new Promise(() => {}));
    const loading = await mountPage();
    expect(loading.findAll('[data-test="browse-loading"] .ui-skeleton')).toHaveLength(12);
    expect(loading.find('[data-test="browse-grid"]').exists()).toBe(false);

    api.SearchLibraryVideoPage.mockRejectedValueOnce(new Error('数据库正在维护'));
    const wrapper = await mountPage();
    expect(wrapper.get('[data-test="browse-error"]').text()).toContain('数据库正在维护');
    await wrapper.get('[data-test="browse-retry"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-test="browse-error"]').exists()).toBe(false);
    expect(wrapper.findAll('[data-test="browse-card"]')).toHaveLength(2);
  });

  it('keeps loaded cards when a later batch fails and retries that batch', async () => {
    const cursor = { sort_mode: 'added_desc', id: 2 };
    api.SearchLibraryVideoPage.mockResolvedValueOnce(page([video(1), video(2)], cursor));
    const wrapper = await mountPage();
    api.SearchLibraryVideoPage.mockRejectedValueOnce('超时');
    await wrapper.vm.loadMore();
    await flushPromises();
    expect(wrapper.findAll('[data-test="browse-card"]')).toHaveLength(2);
    expect(wrapper.get('[data-test="browse-more-error"]').text()).toContain('超时');
    api.SearchLibraryVideoPage.mockResolvedValueOnce(page([video(3)], null));
    await wrapper.get('[data-test="browse-more-retry"]').trigger('click');
    await flushPromises();
    expect(lastRequest().cursor).toEqual(cursor);
    expect(wrapper.findAll('[data-test="browse-card"]')).toHaveLength(3);
  });

  it('empty results: with filters offers 清空筛选, without filters points to adding a scan directory', async () => {
    api.SearchLibraryVideoPage.mockResolvedValue(page([]));
    const wrapper = await mountPage();
    expect(wrapper.get('[data-test="browse-empty"]').text()).toContain('片库里还没有视频');
    await wrapper.get('[data-test="browse-empty-scan"]').trigger('click');
    expect(wrapper.emitted('navigate')).toEqual([['settings']]);

    await wrapper.get('[data-test="browse-watch-favorites"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="browse-empty"]').text()).toContain('没有符合条件的视频');
    await wrapper.get('[data-test="browse-empty-clear"]').trigger('click');
    await flushPromises();
    expect(lastRequest()).toEqual({ filter: DEFAULT_FILTER, limit: 60 });
  });

  it('hides the count when counting fails, without touching the list', async () => {
    api.CountLibraryVideos.mockRejectedValue('失败');
    const wrapper = await mountPage();
    expect(wrapper.find('.ui-page-header__subtitle').exists()).toBe(false);
    expect(wrapper.findAll('[data-test="browse-card"]')).toHaveLength(2);
  });
});

describe('VideoBrowsePage filters', () => {
  it('filters by tags from the popover (no 自动 namespace) and removes them from the chips row', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-test="browse-tag-trigger"]').trigger('click');
    const popover = wrapper.get('[data-test="browse-tag-popover"]');
    expect(popover.text()).toContain('类型');
    expect(popover.text()).toContain('未分类');
    expect(popover.text()).not.toContain('自动分类');
    await wrapper.get('[data-test="browse-tag-option-1"]').trigger('click');
    await flushPromises();
    expect(lastRequest().filter.tag_ids).toEqual([1]);
    await wrapper.get('[data-test="browse-tag-option-2"]').trigger('click');
    await flushPromises();
    expect(lastRequest().filter.tag_ids).toEqual([1, 2]);
    expect(wrapper.get('[data-test="browse-tag-chip-1"]').text()).toContain('科幻');

    await wrapper.get('[data-test="browse-tag-chip-1"] .ui-filter-chip__remove').trigger('click');
    await flushPromises();
    expect(lastRequest().filter.tag_ids).toEqual([2]);
    await wrapper.get('[data-test="browse-clear-chips"]').trigger('click');
    await flushPromises();
    expect(lastRequest().filter.tag_ids).toEqual([]);
    expect(wrapper.find('[data-test="browse-active-filters"]').exists()).toBe(false);
  });

  it('filters by people picked from a searchable list', async () => {
    api.ListPeople.mockResolvedValue([{ person: { id: 7, display_name: '张三' }, active_video_count: 4 }]);
    const wrapper = await mountPage();
    await wrapper.get('[data-test="browse-person-trigger"]').trigger('click');
    await flushPromises();
    expect(api.ListPeople).toHaveBeenLastCalledWith('', '', 0, 20);
    expect(wrapper.get('[data-test="browse-person-option-7"]').text()).toContain('4 部');
    await wrapper.get('[data-test="browse-person-option-7"]').trigger('click');
    await flushPromises();
    expect(lastRequest().filter.person_ids).toEqual([7]);
    expect(wrapper.get('[data-test="browse-person-chip-7"]').text()).toContain('张三');
    expect(wrapper.get('[data-test="browse-person-option-7"]').attributes('aria-pressed')).toBe('true');
    await wrapper.get('[data-test="browse-person-option-7"]').trigger('click');
    await flushPromises();
    expect(lastRequest().filter.person_ids).toEqual([]);

    api.ListPeople.mockRejectedValueOnce('失败');
    await wrapper.get('[data-test="browse-person-search"]').setValue('李');
    await new Promise(resolve => setTimeout(resolve, 250));
    await flushPromises();
    expect(api.ListPeople).toHaveBeenLastCalledWith('李', '', 0, 20);
    expect(wrapper.get('[data-test="browse-person-popover"]').text()).toContain('人物搜索失败');
  });

  it('applies a navigation request as a fresh browse (filters reset, sort kept) on mount and later', async () => {
    const wrapper = await mountPage({ navigationRequest: { keyword: '星际', token: 1 } });
    expect(api.SearchLibraryVideoPage).toHaveBeenCalledTimes(1);
    expect(lastRequest().filter.keyword).toBe('星际');

    await wrapper.get('[data-test="browse-sort"]').setValue('title_asc');
    await wrapper.get('[data-test="browse-watch-watched"]').trigger('click');
    await flushPromises();
    await wrapper.setProps({ navigationRequest: { tagId: 4, token: 2 } });
    await flushPromises();
    expect(lastRequest().filter).toEqual({ ...DEFAULT_FILTER, tag_ids: [4], sort_mode: 'title_asc' });

    await wrapper.setProps({ navigationRequest: { watch: 'favorites', token: 3 } });
    await flushPromises();
    expect(lastRequest().filter).toEqual({ ...DEFAULT_FILTER, smart_view: 'favorites', sort_mode: 'title_asc' });
    // 海报墙不认的观看状态（最近添加没有单列）按「全部」。
    await wrapper.setProps({ navigationRequest: { watch: 'recently_added', token: 4 } });
    await flushPromises();
    expect(lastRequest().filter).toEqual({ ...DEFAULT_FILTER, sort_mode: 'title_asc' });
  });

  it('merges state changes from the full-page detail into a loaded card; stale ones leave the wall', async () => {
    const wrapper = await mountPage();
    wrapper.vm.applyExternalVideoStateChange({ id: 1, changes: { is_watched: true } });
    await flushPromises();
    expect(wrapper.findAll('[data-test="browse-card"]')[0].get('[data-test="browse-card-mark"]').text()).toBe('看过');
    wrapper.vm.applyExternalVideoStateChange({ id: 99, changes: { is_watched: true } });
    wrapper.vm.applyExternalVideoStateChange({ id: 2, changes: { is_stale: true, stale_reason: 'missing_file' } });
    await flushPromises();
    expect(wrapper.findAll('[data-test="browse-card"]')).toHaveLength(1);
  });
});

describe('VideoBrowsePage 追到哪了', () => {
  it('shows collection cards that open the next video, with skeletons while loading', async () => {
    let resolveProgress;
    api.ListCollectionProgress.mockReturnValueOnce(new Promise(resolve => { resolveProgress = resolve; }));
    const wrapper = await mountPage();
    expect(wrapper.find('[data-test="browse-progress-loading"]').exists()).toBe(true);
    resolveProgress([progress(1, { has_cover: true }), progress(2, { next: null, complete: true })]);
    await flushPromises();
    const cards = wrapper.findAll('[data-test="browse-progress-item"]');
    expect(cards).toHaveLength(1);
    expect(cards[0].text()).toContain('作品集1');
    expect(cards[0].text()).toContain('第 3 / 5 部 · 下一部1');
    expect(cards[0].get('img').attributes('src')).toBe('/preview/collection-cover/1');
    await cards[0].trigger('click');
    expect(wrapper.emitted('open-video')).toEqual([[101]]);
  });

  it('fails on its own with a retry and refreshes when the page becomes active again', async () => {
    api.ListCollectionProgress.mockRejectedValueOnce('数据库不可用');
    const wrapper = await mountPage();
    expect(wrapper.get('[data-test="browse-progress-error"]').text()).toContain('数据库不可用');
    expect(wrapper.findAll('[data-test="browse-card"]')).toHaveLength(2);
    api.ListCollectionProgress.mockResolvedValueOnce([progress(1)]);
    await wrapper.get('[data-test="browse-progress-retry"]').trigger('click');
    await flushPromises();
    expect(wrapper.findAll('[data-test="browse-progress-item"]')).toHaveLength(1);

    api.ListCollectionProgress.mockResolvedValueOnce([]);
    await wrapper.setProps({ pageActive: false });
    await wrapper.setProps({ pageActive: true });
    await flushPromises();
    expect(api.ListCollectionProgress).toHaveBeenCalledTimes(3);
    expect(wrapper.find('[data-test="browse-progress"]').exists()).toBe(false);
    // 列表不跟着重取：筛选与已加载的卡片留着。
    expect(api.SearchLibraryVideoPage).toHaveBeenCalledTimes(1);
  });
});

describe('VideoBrowsePage random playback', () => {
  const played = (id, token = `tok-${id}`) => ({ dispatch_succeeded: true, video: video(id), selection_reason: '在当前筛选范围内仅选择未看视频', reroll_token: token });

  it('plays one at random inside the current filter with the shared conditions and recent picks', async () => {
    installStorage({ [RANDOM_CONDITIONS_STORAGE_KEY]: '{"watchState":"unwatched","favoritesOnly":false,"maxMinutes":0}', [RANDOM_RECENT_STORAGE_KEY]: '[3]' });
    const wrapper = await mountPage();
    expect(wrapper.get('[data-test="browse-random-play"]').text()).toContain('随机播放 · 探索');
    await wrapper.get('[data-test="browse-watch-favorites"]').trigger('click');
    api.PlayRandomVideoWithFilter.mockResolvedValueOnce(played(5));
    await wrapper.get('[data-test="browse-random-play"]').trigger('click');
    await flushPromises();
    expect(api.PlayRandomVideoWithFilter).toHaveBeenCalledWith({
      filter: { ...DEFAULT_FILTER, smart_view: 'favorites' }, watch_state: 'unwatched', favorites_only: false, max_duration_seconds: 0, exclude_ids: [3]
    });
    expect(wrapper.getComponent(RandomPickBanner).props('randomPlay')).toEqual(expect.objectContaining({ active: true, videoName: '影片5', rerollable: true }));
    expect(store[RANDOM_RECENT_STORAGE_KEY]).toBe('[3,5]');

    api.RerollRandom.mockResolvedValueOnce(played(6));
    wrapper.getComponent(RandomPickBanner).vm.$emit('reroll');
    await flushPromises();
    expect(api.RerollRandom).toHaveBeenCalledWith('tok-5');
    expect(wrapper.getComponent(RandomPickBanner).props('randomPlay').videoName).toBe('影片6');
  });

  it('reports a failed draw and drops a card the backend marked stale', async () => {
    const wrapper = await mountPage();
    api.PlayRandomVideoWithFilter.mockResolvedValueOnce({
      dispatch_succeeded: false, user_message: '文件不存在 (/Volumes/A/影片1.mp4)', video: video(1),
      reconcile_result: { video_id: 1, did_mark_stale: true }
    });
    await wrapper.vm.playRandom();
    await flushPromises();
    expect(feedback.notifyError).toHaveBeenCalledTimes(1);
    expect(feedback.notifyError.mock.calls[0][0]).not.toContain('/Volumes/A');
    expect(wrapper.findAll('[data-test="browse-card"]')).toHaveLength(1);
    expect(wrapper.getComponent(RandomPickBanner).props('randomPlay')).toBeNull();
  });

  it('has the library menu minus 随机 N 部; condition switches persist and relabel the button', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-test="browse-random-menu"]').trigger('click');
    const menu = wrapper.getComponent(BaseMenu);
    expect(menu.props('items').map(item => item.id || item.heading || (item.divider ? '—' : ''))).toEqual([
      'tonight', '—', '观看状态', 'watch:unwatched', 'watch:any', 'watch:watched', '—', 'favorites'
    ]);
    menu.vm.$emit('select', { id: 'watch:watched' });
    menu.vm.$emit('close');
    await flushPromises();
    expect(JSON.parse(store[RANDOM_CONDITIONS_STORAGE_KEY])).toEqual({ watchState: 'watched', favoritesOnly: false, maxMinutes: 0 });
    expect(wrapper.get('[data-test="browse-random-play"]').text()).toContain('随机播放 · 重温');
  });

  it('opens 今晚看什么 with the current filter; candidates open the detail, play directly, or play one at random', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-test="browse-watch-unwatched"]').trigger('click');
    wrapper.vm.onRandomSelect({ id: 'tonight' });
    await flushPromises();
    const picker = wrapper.getComponent({ name: 'TonightPickerDialog' });
    expect(picker.props('filter')).toEqual({ ...DEFAULT_FILTER, smart_view: 'unwatched' });
    picker.vm.$emit('update:conditions', { watchState: 'any', favoritesOnly: true, maxMinutes: 45 });
    await flushPromises();
    expect(wrapper.get('[data-test="browse-random-play"]').text()).toContain('收藏 · ≤45分钟');

    picker.vm.$emit('preview', video(8));
    await flushPromises();
    expect(wrapper.emitted('open-video')).toEqual([[8]]);
    expect(wrapper.findComponent({ name: 'TonightPickerDialog' }).exists()).toBe(false);

    wrapper.vm.openTonightPicker();
    await flushPromises();
    wrapper.getComponent({ name: 'TonightPickerDialog' }).vm.$emit('play', video(9));
    await flushPromises();
    expect(api.PlayVideo).toHaveBeenCalledWith(9);

    wrapper.vm.openTonightPicker();
    await flushPromises();
    api.PlayRandomVideoWithFilter.mockResolvedValueOnce(played(4));
    wrapper.getComponent({ name: 'TonightPickerDialog' }).vm.$emit('play-random');
    await flushPromises();
    expect(api.PlayRandomVideoWithFilter).toHaveBeenCalledWith(expect.objectContaining({ favorites_only: true, max_duration_seconds: 2700 }));
    expect(wrapper.findComponent({ name: 'TonightPickerDialog' }).exists()).toBe(false);
  });

  it('picks up conditions changed on the library page when it becomes active again', async () => {
    const wrapper = await mountPage();
    store[RANDOM_CONDITIONS_STORAGE_KEY] = '{"watchState":"watched","favoritesOnly":true,"maxMinutes":0}';
    store[RANDOM_RECENT_STORAGE_KEY] = '[11,12]';
    await wrapper.setProps({ pageActive: false });
    await wrapper.setProps({ pageActive: true });
    await flushPromises();
    expect(wrapper.vm.randomConditions).toEqual({ watchState: 'watched', favoritesOnly: true, maxMinutes: 0 });
    expect(wrapper.vm.recentRandomVideoIDs).toEqual([11, 12]);
  });
});

describe('VideoBrowsePage scrolling and shortcuts', () => {
  function scrollHost() {
    const host = document.createElement('div');
    host.className = 'main-view';
    let top = 0;
    Object.defineProperty(host, 'scrollTop', { configurable: true, get: () => top, set: value => { top = value; } });
    Object.defineProperty(host, 'scrollHeight', { configurable: true, get: () => 5000 });
    Object.defineProperty(host, 'clientHeight', { configurable: true, get: () => 800 });
    const mountPoint = document.createElement('div');
    host.appendChild(mountPoint);
    document.body.appendChild(host);
    return { host, mountPoint };
  }

  it('remembers the shared scroll position when leaving and restores it when coming back', async () => {
    const { host, mountPoint } = scrollHost();
    const wrapper = await mountPage({}, { attachTo: mountPoint });
    host.scrollTop = 1200;
    await wrapper.setProps({ pageActive: false });
    host.scrollTop = 40;
    await wrapper.setProps({ pageActive: true });
    await flushPromises();
    expect(host.scrollTop).toBe(1200);

    // 带参数打开是新的一次浏览：回到顶部，不放回旧位置。
    host.scrollTop = 900;
    await wrapper.setProps({ pageActive: false, navigationRequest: { keyword: '新', token: 1 } });
    await wrapper.setProps({ pageActive: true });
    await flushPromises();
    expect(host.scrollTop).toBe(0);
    wrapper.unmount();
    host.remove();
  });

  it('restores the scroll position in 最近播放 too, without refetching the list', async () => {
    const { host, mountPoint } = scrollHost();
    api.ListRecentlyPlayedWithFilter.mockResolvedValue([video(1, { last_played_at: '2026-10-13T10:00:00Z' })]);
    const wrapper = await mountPage({ navigationRequest: { watch: 'recently_played', token: 1 } }, { attachTo: mountPoint });
    const calls = api.ListRecentlyPlayedWithFilter.mock.calls.length;
    host.scrollTop = 700;
    await wrapper.setProps({ pageActive: false });
    host.scrollTop = 0;
    await wrapper.setProps({ pageActive: true });
    await flushPromises();
    expect(host.scrollTop).toBe(700);
    expect(api.ListRecentlyPlayedWithFilter.mock.calls.length).toBe(calls);
    wrapper.unmount();
    host.remove();
  });

  it('loads the next batch when the sentinel nears the viewport, only while the page is active', async () => {
    const observers = [];
    const original = window.IntersectionObserver;
    window.IntersectionObserver = class {
      constructor(callback, options) { this.callback = callback; this.options = options; observers.push(this); }
      observe(target) { this.target = target; }
      disconnect() {}
    };
    try {
      const cursor = { sort_mode: 'added_desc', id: 2 };
      api.SearchLibraryVideoPage.mockResolvedValueOnce(page([video(1), video(2)], cursor));
      const wrapper = await mountPage({ pageActive: false });
      expect(observers).toHaveLength(1);
      expect(observers[0].options.rootMargin).toBe('800px 0px');
      observers[0].callback([{ isIntersecting: true }]);
      await flushPromises();
      expect(api.SearchLibraryVideoPage).toHaveBeenCalledTimes(1);

      await wrapper.setProps({ pageActive: true });
      api.SearchLibraryVideoPage.mockResolvedValueOnce(page([video(3)], { ...cursor, id: 3 }));
      api.SearchLibraryVideoPage.mockResolvedValueOnce(page([video(4)], null));
      observers[0].callback([{ isIntersecting: true }]);
      await flushPromises();
      // 一屏放得下这一批时哨兵仍可见：接着取，直到没有下一批。
      expect(api.SearchLibraryVideoPage).toHaveBeenCalledTimes(3);
      expect(wrapper.findAll('[data-test="browse-card"]')).toHaveLength(4);
    } finally {
      window.IntersectionObserver = original;
    }
  });

  it('⌘F focuses the search box while the page is visible', async () => {
    const wrapper = await mountPage({}, { attachTo: document.body });
    const event = new KeyboardEvent('keydown', { key: 'f', metaKey: true, cancelable: true });
    window.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(wrapper.get('[data-test="browse-search"]').element);

    wrapper.get('[data-test="browse-search"]').element.blur();
    await wrapper.setProps({ pageActive: false });
    const hidden = new KeyboardEvent('keydown', { key: 'f', metaKey: true, cancelable: true });
    window.dispatchEvent(hidden);
    expect(hidden.defaultPrevented).toBe(false);
    wrapper.unmount();
  });
});

// 三期集成：点赞走原来的分页；最近播放按最近一次播放倒序，用 ListRecentlyPlayedWithFilter 的键集分页（last_played_at, id）。
describe('VideoBrowsePage 点赞 and 最近播放', () => {
  const played = (id, at) => video(id, { last_played_at: at });
  const fullPage = (start, at) => Array.from({ length: VIDEO_BROWSE_PAGE_SIZE }, (_, index) => played(start - index, at(index)));

  it('点赞 stays on the regular paged search with smart_view liked', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-test="browse-watch-liked"]').trigger('click');
    await flushPromises();
    expect(lastRequest()).toEqual({ filter: { ...DEFAULT_FILTER, smart_view: 'liked' }, limit: 60 });
    expect(api.ListRecentlyPlayedWithFilter).not.toHaveBeenCalled();
    expect(wrapper.get('[data-test="browse-sort"]').attributes('disabled')).toBeUndefined();
  });

  it('最近播放 pages by last play with the (last_played_at, id) cursor, ties included, and stops on a short page', async () => {
    // 第一页最后几行同一时刻播放：游标取最后一行的时间与 id，同时间靠 id 排开。
    const tie = '2026-10-13T09:00:00.5+08:00';
    const page1 = fullPage(500, index => (index >= VIDEO_BROWSE_PAGE_SIZE - 3 ? tie : `2026-10-13T10:${String(59 - index).padStart(2, '0')}:00+08:00`));
    const page2 = [played(441, tie), played(440, tie), played(100, '2026-10-12T08:00:00Z')];
    api.ListRecentlyPlayedWithFilter.mockResolvedValueOnce(page1).mockResolvedValueOnce(page2);
    const wrapper = await mountPage();
    const searchCalls = api.SearchLibraryVideoPage.mock.calls.length;
    await wrapper.get('[data-test="browse-watch-recent"]').trigger('click');
    await flushPromises();

    const filter = { ...DEFAULT_FILTER, smart_view: 'recently_played', collapse_versions: false };
    expect(api.ListRecentlyPlayedWithFilter).toHaveBeenNthCalledWith(1, filter, '', 0, 60);
    expect(api.SearchLibraryVideoPage.mock.calls.length).toBe(searchCalls);
    expect(api.CountLibraryVideos).toHaveBeenLastCalledWith(filter);
    expect(wrapper.findAll('[data-test="browse-card"]')).toHaveLength(60);
    expect(wrapper.find('[data-test="browse-load-more"]').exists()).toBe(true);

    await wrapper.get('[data-test="browse-load-more"]').trigger('click');
    await flushPromises();
    expect(api.ListRecentlyPlayedWithFilter).toHaveBeenNthCalledWith(2, filter, tie, 441, 60);
    // 第二页把第一页最后那一行又带回来（同一时刻）也不重复出卡。
    const ids = wrapper.findAll('[data-test="browse-card"]').map(card => card.text());
    expect(ids).toHaveLength(62);
    expect(wrapper.findAll('[data-test="browse-card"]').at(-1).text()).toContain('影片100');
    expect(wrapper.find('[data-test="browse-load-more"]').exists()).toBe(false);
    expect(wrapper.vm.loadMore()).toBeNull();
    expect(api.ListRecentlyPlayedWithFilter).toHaveBeenCalledTimes(2);
  });

  it('locks the sort select to 「按最近播放时间」 while selected and restores the chosen sort afterwards', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-test="browse-sort"]').setValue('title_asc');
    await flushPromises();
    await wrapper.get('[data-test="browse-watch-recent"]').trigger('click');
    await flushPromises();
    const select = wrapper.get('[data-test="browse-sort"]');
    expect(select.attributes('disabled')).toBeDefined();
    expect(select.findAll('option').map(option => option.text())).toEqual(['按最近播放时间']);
    expect(select.attributes('title')).toContain('按播放时间从近到远');
    // 锁住期间不论从哪写进来（下拉本身已禁用），都不改掉用户原来选的排序。
    wrapper.vm.sortModel = 'recently_played';
    expect(wrapper.vm.sort).toBe('title_asc');

    await wrapper.get('[data-test="browse-watch-watched"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="browse-sort"]').attributes('disabled')).toBeUndefined();
    expect(wrapper.get('[data-test="browse-sort"]').element.value).toBe('title_asc');
    expect(lastRequest().filter).toEqual({ ...DEFAULT_FILTER, smart_view: 'watched', sort_mode: 'title_asc' });
  });

  it('drops a late 最近播放 page after switching away, and a late search page after switching to it', async () => {
    let resolveRecent;
    const wrapper = await mountPage();
    api.ListRecentlyPlayedWithFilter.mockReturnValueOnce(new Promise(resolve => { resolveRecent = resolve; }));
    await wrapper.get('[data-test="browse-watch-recent"]').trigger('click');
    api.SearchLibraryVideoPage.mockResolvedValueOnce(page([video(7)]));
    await wrapper.get('[data-test="browse-watch-favorites"]').trigger('click');
    await flushPromises();
    resolveRecent([played(1, '2026-10-13T10:00:00Z'), played(2, '2026-10-13T09:00:00Z')]);
    await flushPromises();
    expect(wrapper.findAll('[data-test="browse-card"]').map(card => card.text())).toEqual([expect.stringContaining('影片7')]);

    let resolveSearch;
    api.SearchLibraryVideoPage.mockReturnValueOnce(new Promise(resolve => { resolveSearch = resolve; }));
    await wrapper.get('[data-test="browse-watch-all"]').trigger('click');
    api.ListRecentlyPlayedWithFilter.mockResolvedValueOnce([played(9, '2026-10-13T10:00:00Z')]);
    await wrapper.get('[data-test="browse-watch-recent"]').trigger('click');
    await flushPromises();
    resolveSearch(page([video(1), video(2)]));
    await flushPromises();
    expect(wrapper.findAll('[data-test="browse-card"]').map(card => card.text())).toEqual([expect.stringContaining('影片9')]);
  });

  it('opens straight into 最近播放 from a navigation request (palette 「视频 · 最近播放」)', async () => {
    api.ListRecentlyPlayedWithFilter.mockResolvedValueOnce([played(3, '2026-10-13T10:00:00Z')]);
    const wrapper = await mountPage({ navigationRequest: { watch: 'recently_played', token: 1 } });
    expect(api.ListRecentlyPlayedWithFilter).toHaveBeenCalledWith({ ...DEFAULT_FILTER, smart_view: 'recently_played', collapse_versions: false }, '', 0, 60);
    expect(api.SearchLibraryVideoPage).not.toHaveBeenCalled();
    expect(wrapper.get('[data-test="browse-watch-recent"]').attributes('aria-checked')).toBe('true');
    expect(wrapper.findAll('[data-test="browse-card"]')).toHaveLength(1);
  });
});
