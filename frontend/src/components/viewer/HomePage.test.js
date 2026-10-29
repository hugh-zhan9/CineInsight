import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { flushPromises, mount, enableAutoUnmount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { nextTick } from 'vue';

const api = vi.hoisted(() => ({
  GetRecap: vi.fn(),
  GetVideoDetails: vi.fn(),
  ListCollectionProgress: vi.fn(),
  ListCollectionProgressPage: vi.fn(),
  ListContinueWatchingWithFilter: vi.fn(),
  ListWatchlistInLibrary: vi.fn(),
  ListWatchlistInLibraryPage: vi.fn(),
  SearchLibraryVideoPage: vi.fn(),
  SuggestTonightVideos: vi.fn()
}));
vi.mock('../../../wailsjs/go/main/App', () => api);
import HomePage, {
  HOME_CONTINUE_LIMIT, HOME_PEOPLE_LIMIT, HOME_PROGRESS_LIMIT, HOME_RECENT_LIMIT, HOME_WATCHLIST_LIMIT, homeProgressText, pickHomeHero
} from './HomePage.vue';
import RecapBackdrop from '../recap/RecapBackdrop.vue';

enableAutoUnmount(afterEach);

const video = (id, extra = {}) => ({ id, name: `v${id}.mp4`, display_title: `影片${id}`, duration: 600, watch_position_seconds: 0, ...extra });
const suggestion = (id, reasons = ['没看过']) => ({ video: video(id), reasons });
const person = id => ({ entity: { id, name: `人物${id}`, image_url: '' }, weight: 10 - id, videos: 1 });
const recap = (overrides = {}) => ({
  period: 'month', label: '2026年10月', in_progress: true, empty: false,
  overview: { plays: 5, videos: 3, active_days: 2 },
  favorite: { video: { id: 41, title: '最爱', duration: 600, available: true }, count: 3 },
  top_people: Array.from({ length: 10 }, (_, index) => person(index + 1)),
  ...overrides
});
const today = new Date();
const thisMonth = { period: 'month', year: today.getFullYear(), month: today.getMonth() + 1 };

beforeEach(() => {
  api.ListContinueWatchingWithFilter.mockReset().mockResolvedValue({ videos: [video(1, { watch_position_seconds: 75 }), video(2)] });
  api.SuggestTonightVideos.mockReset().mockResolvedValue([suggestion(11, ['没看过', '收藏']), suggestion(12), suggestion(13)]);
  api.SearchLibraryVideoPage.mockReset().mockResolvedValue({ videos: [video(21), video(22)] });
  api.GetRecap.mockReset().mockResolvedValue(recap());
  api.GetVideoDetails.mockReset().mockResolvedValue({ artwork: { has_poster: false, has_fanart: false } });
  api.ListCollectionProgress.mockReset().mockResolvedValue([]);
  api.ListWatchlistInLibrary.mockReset().mockResolvedValue([]);
  api.ListCollectionProgressPage.mockReset().mockResolvedValue({ items: [], total: 0, has_more: false });
  api.ListWatchlistInLibraryPage.mockReset().mockResolvedValue({ items: [], total: 0, has_more: false });
});

async function mountPage(props = {}) {
  const wrapper = mount(HomePage, { props });
  await flushPromises();
  return wrapper;
}

const heroBackdrop = wrapper => wrapper.findAllComponents(RecapBackdrop).find(item => item.attributes('data-test') === 'home-hero');

describe('HomePage blocks', () => {
  it('loads every block from its existing API with the documented arguments', async () => {
    const wrapper = await mountPage();
    expect(api.ListContinueWatchingWithFilter).toHaveBeenCalledWith({}, '', 0, 11); // 多取一条判断有没有放全
    expect(api.SuggestTonightVideos).toHaveBeenCalledWith({ filter: {}, watch_state: 'any', favorites_only: false, max_duration_seconds: 0, exclude_ids: [] }, 3);
    expect(api.SearchLibraryVideoPage).toHaveBeenCalledWith({ filter: { smart_view: 'recently_added' }, limit: 15 });
    expect(api.GetRecap).toHaveBeenCalledTimes(1);
    expect(api.GetRecap).toHaveBeenCalledWith(thisMonth);

    expect(wrapper.findAll('[data-test="home-continue"] [data-test="recap-card-video"]')).toHaveLength(2);
    expect(wrapper.get('[data-test="home-continue"]').text()).toContain('看到 01:15 / 10:00');
    const tonight = wrapper.findAll('[data-test="home-tonight-item"]');
    expect(tonight).toHaveLength(3);
    expect(tonight[0].text()).toContain('没看过 · 收藏');
    const posters = wrapper.findAll('[data-test="home-recent-item"]');
    expect(posters).toHaveLength(2);
    expect(posters[0].get('img').attributes('src')).toBe('/preview/video-artwork/21?kind=poster');
    expect(wrapper.findAll('[data-test="home-person"]')).toHaveLength(HOME_PEOPLE_LIMIT);
    expect(wrapper.get('[data-test="home-highlight-plays"]').text()).toContain('5次');
    expect(wrapper.get('[data-test="home-highlight-favorite"]').text()).toContain('看了 3 次');
  });

  it('shows a loading line per block while its request is pending', async () => {
    api.SearchLibraryVideoPage.mockReturnValue(new Promise(() => {}));
    api.GetRecap.mockReturnValue(new Promise(() => {}));
    const wrapper = await mountPage();
    expect(wrapper.find('[data-test="home-recent-loading"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="home-people-loading"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="home-highlight-loading"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="home-continue-loading"]').exists()).toBe(false);
    expect(wrapper.findAll('[data-test="home-tonight-item"]')).toHaveLength(3);
  });

  it('fails each block on its own', async () => {
    api.ListContinueWatchingWithFilter.mockRejectedValue('数据库正在维护');
    api.GetRecap.mockRejectedValue(new Error('database is in maintenance mode'));
    const wrapper = await mountPage();
    expect(wrapper.get('[data-test="home-continue-error"]').text()).toBe('读取继续观看失败：数据库正在维护');
    expect(wrapper.get('[data-test="home-people-error"]').text()).toContain('数据库正在恢复备份或切换后端');
    expect(wrapper.get('[data-test="home-highlight-error"]').text()).toContain('读取本月回顾失败');
    expect(wrapper.findAll('[data-test="home-tonight-item"]')).toHaveLength(3);
    expect(wrapper.findAll('[data-test="home-recent-item"]')).toHaveLength(2);
    expect(wrapper.get('[data-test="home-hero-title"]').text()).toBe('影片11');
  });

  it('hides blocks that came back empty instead of leaving shells', async () => {
    api.ListContinueWatchingWithFilter.mockResolvedValue({ videos: [] });
    api.SearchLibraryVideoPage.mockResolvedValue({ videos: null });
    api.GetRecap.mockResolvedValue(recap({ empty: true, top_people: [], favorite: null }));
    const wrapper = await mountPage();
    for (const block of ['home-continue', 'home-progress', 'home-recent', 'home-watchlist', 'home-people', 'home-highlight']) {
      expect(wrapper.find(`[data-test="${block}"]`).exists()).toBe(false);
    }
    expect(wrapper.find('[data-test="home-tonight"]').exists()).toBe(true);
  });

  it('keeps the people block while the month has records but hides it without people', async () => {
    api.GetRecap.mockResolvedValue(recap({ top_people: [] }));
    const wrapper = await mountPage();
    expect(wrapper.find('[data-test="home-people"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="home-highlight"]').exists()).toBe(true);
  });
});

describe('HomePage hero', () => {
  it('prefers the first continue-watching video', async () => {
    const wrapper = await mountPage();
    expect(wrapper.get('[data-test="home-hero-title"]').text()).toBe('影片1');
    expect(wrapper.get('[data-test="home-hero-meta"]').text()).toBe('看到 01:15 / 10:00');
    // 大图区的文字层是本页的页头 HeroHeader（D-UD-06），标题沿用 h2。
    expect(wrapper.get('[data-test="home-hero-title"]').element.tagName).toBe('H2');
    expect(wrapper.get('[data-test="home-hero"] .ui-hero-header [data-test="home-hero-open"]').exists()).toBe(true);
    expect(api.GetVideoDetails).toHaveBeenCalledWith(1);
  });

  it('falls back to the first tonight suggestion, then to a guide when both are empty', async () => {
    api.ListContinueWatchingWithFilter.mockResolvedValue({ videos: [] });
    let wrapper = await mountPage();
    expect(wrapper.get('[data-test="home-hero-title"]').text()).toBe('影片11');
    expect(wrapper.get('[data-test="home-hero-meta"]').text()).toBe('没看过 · 收藏');
    wrapper.unmount();
    api.SuggestTonightVideos.mockResolvedValue([]);
    wrapper = await mountPage();
    expect(wrapper.find('[data-test="home-hero"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="home-guide"]').exists()).toBe(true);
    await wrapper.get('[data-test="home-guide-scan"]').trigger('click');
    await wrapper.get('[data-test="home-guide-library"]').trigger('click');
    expect(wrapper.emitted('navigate')).toEqual([['settings'], ['videos']]);
  });

  it('waits for continue-watching before using a tonight suggestion', async () => {
    let finishContinue;
    api.ListContinueWatchingWithFilter.mockReturnValue(new Promise(resolve => { finishContinue = resolve; }));
    const wrapper = await mountPage();
    expect(wrapper.find('[data-test="home-hero-loading"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="home-hero"]').exists()).toBe(false);
    finishContinue({ videos: [video(5)] });
    await flushPromises();
    expect(wrapper.get('[data-test="home-hero-title"]').text()).toBe('影片5');
  });

  it('shows no hero and no guide when a source failed and nothing could be picked', async () => {
    api.ListContinueWatchingWithFilter.mockResolvedValue({ videos: [] });
    api.SuggestTonightVideos.mockRejectedValue('挑选失败');
    const wrapper = await mountPage();
    expect(wrapper.find('[data-test="home-hero"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="home-guide"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="home-tonight-error"]').text()).toBe('读取今晚看什么失败：挑选失败');
  });

  it('uses the fanart route when the video has fanart, otherwise real frames', async () => {
    api.GetVideoDetails.mockResolvedValue({ artwork: { has_fanart: true } });
    let wrapper = await mountPage();
    expect(heroBackdrop(wrapper).props('frames')).toEqual(['/preview/video-artwork/1?kind=fanart']);
    expect(heroBackdrop(wrapper).props('fallback')).toBe('/preview/thumbnail/1');
    wrapper.unmount();
    api.GetVideoDetails.mockResolvedValue({ artwork: { has_fanart: false } });
    wrapper = await mountPage();
    const frames = heroBackdrop(wrapper).props('frames');
    expect(frames).toHaveLength(5);
    expect(frames[0]).toBe('/preview/frame/1?ms=60000&w=960');
    wrapper.unmount();
    api.GetVideoDetails.mockRejectedValue('详情读取失败');
    wrapper = await mountPage();
    expect(heroBackdrop(wrapper).props('frames')[0]).toBe('/preview/frame/1?ms=60000&w=960');
  });

  it('picks heroes in a fixed order (pure helper)', () => {
    const resumed = { loading: false, error: '', items: [video(1)] };
    const empty = { loading: false, error: '', items: [], first: null };
    const tonight = { loading: false, error: '', items: [suggestion(2)], first: suggestion(2) };
    expect(pickHomeHero(resumed, tonight)).toMatchObject({ state: 'ready', from: 'continue' });
    expect(pickHomeHero({ ...empty, loading: true }, tonight)).toEqual({ state: 'pending' });
    expect(pickHomeHero(empty, tonight)).toMatchObject({ state: 'ready', from: 'tonight' });
    expect(pickHomeHero(empty, { ...empty, loading: true })).toEqual({ state: 'pending' });
    expect(pickHomeHero({ ...empty, error: 'x' }, empty)).toEqual({ state: 'none' });
    expect(pickHomeHero(empty, empty)).toEqual({ state: 'guide' });
    // 回到首页重取时（上一次已取到、结果是空的）不再回到「正在挑一部…」：大图区保持原样，结果出来再换。
    expect(pickHomeHero({ ...empty, loading: true, loaded: true }, empty)).toEqual({ state: 'guide' });
    expect(pickHomeHero(empty, { ...empty, loading: true, loaded: true })).toEqual({ state: 'guide' });
    expect(homeProgressText({ duration: 0, watch_position_seconds: 0 })).toBe('');
    expect(homeProgressText({ duration: 3600 })).toBe('共 01:00:00');
  });
});

describe('HomePage actions', () => {
  it('rerolls tonight excluding the current batch without changing the hero', async () => {
    api.ListContinueWatchingWithFilter.mockResolvedValue({ videos: [] });
    const wrapper = await mountPage();
    api.SuggestTonightVideos.mockResolvedValue([suggestion(14)]);
    await wrapper.get('[data-test="home-tonight-reroll"]').trigger('click');
    await flushPromises();
    expect(api.SuggestTonightVideos).toHaveBeenLastCalledWith(expect.objectContaining({ exclude_ids: [11, 12, 13] }), 3);
    expect(wrapper.findAll('[data-test="home-tonight-item"]').map(item => item.text())).toEqual([expect.stringContaining('影片14')]);
    expect(wrapper.get('[data-test="home-hero-title"]').text()).toBe('影片11');
    api.SuggestTonightVideos.mockResolvedValue([]);
    await wrapper.get('[data-test="home-tonight-reroll"]').trigger('click');
    await flushPromises();
    expect(wrapper.findAll('[data-test="home-tonight-item"]')).toHaveLength(1);
    expect(wrapper.find('[data-test="home-tonight-notice"]').exists()).toBe(true);
  });

  it('emits open-video for cards and the hero, open-person for people and navigate for the recap entry', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-test="home-hero-open"]').trigger('click');
    await wrapper.findAll('[data-test="home-continue"] [data-test="recap-card-video"]')[1].trigger('click');
    await wrapper.findAll('[data-test="home-tonight-item"]')[2].trigger('click');
    await wrapper.findAll('[data-test="home-recent-item"]')[0].trigger('click');
    await wrapper.get('[data-test="home-highlight-favorite"]').trigger('click');
    expect(wrapper.emitted('open-video')).toEqual([[1], [2], [13], [21], [41]]);
    await wrapper.findAll('[data-test="home-person"]')[0].trigger('click');
    expect(wrapper.emitted('open-person')).toEqual([[{ id: 1, name: '人物1' }]]);
    await wrapper.get('[data-test="home-open-recap"]').trigger('click');
    expect(wrapper.emitted('navigate')).toEqual([['recap']]);
  });

  it('refreshes continue watching, recent and recap when the page becomes active again, keeping tonight and the hero', async () => {
    const wrapper = await mountPage();
    api.ListContinueWatchingWithFilter.mockResolvedValue({ videos: [video(3), video(1, { watch_position_seconds: 300 })] });
    api.SearchLibraryVideoPage.mockResolvedValue({ videos: [video(23)] });
    api.SuggestTonightVideos.mockResolvedValue([suggestion(14), suggestion(15), suggestion(16)]);
    await wrapper.setProps({ pageActive: false });
    await wrapper.setProps({ pageActive: true });
    expect(wrapper.get('[data-test="home-hero-title"]').text()).toBe('影片1');
    await flushPromises();
    expect(api.ListContinueWatchingWithFilter).toHaveBeenCalledTimes(2);
    expect(api.SearchLibraryVideoPage).toHaveBeenCalledTimes(2);
    expect(api.GetRecap).toHaveBeenCalledTimes(2);
    expect(api.SuggestTonightVideos).toHaveBeenCalledTimes(1);
    expect(wrapper.findAll('[data-test="home-tonight-item"]').map(item => item.text())).toEqual([
      expect.stringContaining('影片11'), expect.stringContaining('影片12'), expect.stringContaining('影片13')
    ]);
    expect(wrapper.findAll('[data-test="home-recent-item"]')).toHaveLength(1);
    expect(wrapper.findAll('[data-test="home-continue"] [data-test="recap-card-video"]').map(item => item.text())).toEqual([
      expect.stringContaining('影片3'), expect.stringContaining('影片1')
    ]);
    // 大图还是那一部，进度用新读到的。
    expect(wrapper.get('[data-test="home-hero-title"]').text()).toBe('影片1');
    expect(wrapper.get('[data-test="home-hero-meta"]').text()).toBe('看到 05:00 / 10:00');
  });

  it('keeps a hero picked from tonight when continue watching appears later', async () => {
    api.ListContinueWatchingWithFilter.mockResolvedValue({ videos: [] });
    const wrapper = await mountPage();
    expect(wrapper.get('[data-test="home-hero-title"]').text()).toBe('影片11');
    api.ListContinueWatchingWithFilter.mockResolvedValue({ videos: [video(3)] });
    await wrapper.setProps({ pageActive: false });
    await wrapper.setProps({ pageActive: true });
    await flushPromises();
    expect(wrapper.get('[data-test="home-hero-title"]').text()).toBe('影片11');
    expect(wrapper.get('[data-test="home-hero-meta"]').text()).toBe('没看过 · 收藏');
    expect(api.GetVideoDetails).toHaveBeenCalledTimes(1);
  });
});

// 二期两栏（D-P2-07）：追到哪了（D-P2-02）放在继续观看之后，想看的片片库里已有（D-P2-03）放在最近添加之后，
// 各自独立加载、独立失败，没有数据不显示。
describe('HomePage phase-2 sections', () => {
  const progress = (id, extra = {}) => ({
    collection_id: id, name: `作品集${id}`, has_cover: false, total_works: 4, watched_works: 1, complete: false,
    next: { video_id: 200 + id, title: `第二部${id}`, work_index: 2, resume: false }, ...extra
  });
  const wish = (id, extra = {}) => ({
    entry_id: id, entry_title: `想看${id}`, kind: 'movie', year: 1990 + id, has_poster: false, video_id: 300 + id, video_title: `片库${id}`, origin: 'manual', ...extra
  });
  const sectionOrder = wrapper => wrapper.findAll('section.recap-section').map(section => section.attributes('data-test'));

  it('loads both with the documented arguments and places them after 继续观看 / 最近添加', async () => {
    api.ListCollectionProgress.mockResolvedValue([progress(1, { has_cover: true }), progress(2, { next: null, complete: true }), progress(3, { next: { video_id: 203, title: '续', work_index: 3, resume: true } })]);
    api.ListWatchlistInLibrary.mockResolvedValue([wish(1, { has_poster: true }), wish(2, { origin: 'auto' }), wish(3, { video_id: 0 })]);
    const wrapper = await mountPage();
    expect(api.ListCollectionProgress).toHaveBeenCalledWith({ collection_ids: [], continue_only: true, limit: HOME_PROGRESS_LIMIT + 1 });
    expect(HOME_PROGRESS_LIMIT).toBe(12);
    expect(api.ListWatchlistInLibrary).toHaveBeenCalledWith(HOME_WATCHLIST_LIMIT + 1);
    expect(HOME_WATCHLIST_LIMIT).toBe(12);
    expect(sectionOrder(wrapper)).toEqual(['home-continue', 'home-progress', 'home-tonight', 'home-recent', 'home-watchlist', 'home-people', 'home-highlight']);

    const progressCards = wrapper.findAll('[data-test="home-progress-item"]');
    expect(progressCards).toHaveLength(2);
    expect(progressCards[0].text()).toContain('作品集1');
    expect(progressCards[0].text()).toContain('第 2 / 4 部 · 第二部1');
    expect(progressCards[0].get('img').attributes('src')).toBe('/preview/collection-cover/1');
    expect(progressCards[1].get('img').attributes('src')).toBe('/preview/video-artwork/203?kind=poster');
    expect(progressCards[1].text()).toContain('续播');

    const wishes = wrapper.findAll('[data-test="home-watchlist-item"]');
    expect(wishes).toHaveLength(2);
    expect(wishes[0].text()).toContain('想看1');
    expect(wishes[0].get('img').attributes('src')).toBe('/preview/watchlist-poster/1');
    expect(wishes[1].get('img').attributes('src')).toBe('/preview/video-artwork/302?kind=poster');
    expect(wishes[1].text()).toContain('1992 · 自动匹配');

    await progressCards[1].trigger('click');
    await wishes[0].get('[data-test="home-watchlist-play"]').trigger('click');
    await wishes[1].get('[data-test="recap-card-video"]').trigger('click');
    expect(wrapper.emitted('open-video')).toEqual([[203], [301], [302]]);
  });

  it('shows skeletons while loading and a retryable error per section, without touching the others', async () => {
    api.ListCollectionProgress.mockReturnValueOnce(new Promise(() => {}));
    api.ListWatchlistInLibrary.mockReturnValueOnce(new Promise(() => {}));
    const loading = await mountPage();
    expect(loading.find('[data-test="home-progress-loading"]').exists()).toBe(true);
    expect(loading.find('[data-test="home-watchlist-loading"]').exists()).toBe(true);
    loading.unmount();

    api.ListCollectionProgress.mockRejectedValueOnce('数据库不可用');
    api.ListWatchlistInLibrary.mockRejectedValueOnce(new Error('超时'));
    const wrapper = await mountPage();
    expect(wrapper.get('[data-test="home-progress-error"]').text()).toContain('读取追到哪了失败：数据库不可用');
    expect(wrapper.get('[data-test="home-watchlist-error"]').text()).toContain('读取想看片单失败：超时');
    expect(wrapper.findAll('[data-test="home-recent-item"]')).toHaveLength(2);
    expect(wrapper.findAll('[data-test="home-continue"] [data-test="recap-card-video"]')).toHaveLength(2);

    api.ListCollectionProgress.mockResolvedValueOnce([progress(1)]);
    api.ListWatchlistInLibrary.mockResolvedValueOnce([wish(1)]);
    await wrapper.get('[data-test="home-progress-retry"]').trigger('click');
    await wrapper.get('[data-test="home-watchlist-retry"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-test="home-progress-error"]').exists()).toBe(false);
    expect(wrapper.findAll('[data-test="home-progress-item"]')).toHaveLength(1);
    expect(wrapper.findAll('[data-test="home-watchlist-item"]')).toHaveLength(1);
  });

  it('refreshes both when the page becomes active again (watching changes them)', async () => {
    api.ListCollectionProgress.mockResolvedValueOnce([progress(1)]);
    api.ListWatchlistInLibrary.mockResolvedValueOnce([wish(1)]);
    const wrapper = await mountPage();
    expect(wrapper.findAll('[data-test="home-progress-item"]')).toHaveLength(1);
    await wrapper.setProps({ pageActive: false });
    await wrapper.setProps({ pageActive: true });
    await flushPromises();
    expect(api.ListCollectionProgress).toHaveBeenCalledTimes(2);
    expect(api.ListWatchlistInLibrary).toHaveBeenCalledTimes(2);
    expect(wrapper.find('[data-test="home-progress"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="home-watchlist"]').exists()).toBe(false);
  });
});

describe('HomePage highlight layout', () => {
  it('lets the stat tiles take the whole row when the month has no favorite', async () => {
    api.GetRecap.mockResolvedValue(recap({ favorite: null }));
    const wrapper = await mountPage();
    expect(wrapper.get('[data-test="home-highlight"] .viewer-home__highlight').classes()).toContain('viewer-home__highlight--solo');
    api.GetRecap.mockResolvedValue(recap());
    const withFavorite = await mountPage();
    expect(withFavorite.get('[data-test="home-highlight"] .viewer-home__highlight').classes()).not.toContain('viewer-home__highlight--solo');
  });
});

// 2026-10-14 G3：从整页详情「返回」首页时，App 按离开时的 scrollTop 原样放回（goBackFromDetail：切回 → 等一拍 → 放回），
// 首页随即重取继续观看、追到哪了、最近添加、想看已在库、本月回顾。重取期间空的几块先闪出骨架、结果出来又多了少了一块，
// 视口里的内容被整体推上推下。这里用固定的块高模拟排版，看「最近添加」在视口里的位置回来前后是否一致。
describe('HomePage keeps its place when coming back from the full-page detail', () => {
  const progress = id => ({
    collection_id: id, name: `作品集${id}`, has_cover: false, total_works: 4, watched_works: 1, complete: false,
    next: { video_id: 200 + id, title: `第二部${id}`, work_index: 2, resume: false }
  });
  // 本页每个直接子节点（按 data-test）的高度，块与块之间不留缝；视口 600 高。
  const HEIGHTS = {
    'home-hero': 400, 'home-hero-loading': 160, 'home-guide': 160, 'home-continue': 260, 'home-progress': 320, 'home-tonight': 220,
    'home-recent': 600, 'home-watchlist': 340, 'home-people': 160, 'home-highlight': 320
  };
  let geometry;
  let host;

  beforeEach(() => {
    geometry = { scrollTop: 0 };
    host = document.createElement('div');
    host.className = 'main-view';
    Object.defineProperty(host, 'scrollTop', { configurable: true, get: () => geometry.scrollTop, set: value => { geometry.scrollTop = Math.max(0, value); } });
    Object.defineProperty(host, 'clientHeight', { configurable: true, get: () => 600 });
    Object.defineProperty(host, 'scrollHeight', { configurable: true, get: () => 1e6 });
    const rect = (top, height) => ({ top, bottom: top + height, left: 0, right: 1000, width: 1000, height, x: 0, y: top });
    const blockHeight = node => HEIGHTS[node.dataset?.test] || 0;
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function () {
      if (this === host) return rect(0, 600);
      if (this.classList.contains('viewer-home')) return rect(-geometry.scrollTop, [...this.children].reduce((sum, child) => sum + blockHeight(child), 0));
      const root = this.parentElement;
      if (root?.classList.contains('viewer-home')) {
        let top = 0;
        for (const child of root.children) {
          if (child === this) break;
          top += blockHeight(child);
        }
        return rect(top - geometry.scrollTop, blockHeight(this));
      }
      return rect(0, 0);
    });
    document.body.appendChild(host);
  });
  afterEach(() => {
    vi.restoreAllMocks();
    host.remove();
  });

  async function mountHome() {
    const mountPoint = document.createElement('div');
    host.appendChild(mountPoint);
    const wrapper = mount(HomePage, { props: { pageActive: true }, attachTo: mountPoint });
    await flushPromises();
    return wrapper;
  }
  const offsetOf = key => document.querySelector(`[data-test="${key}"]`)?.getBoundingClientRect().top;
  const blocks = wrapper => wrapper.findAll('.viewer-home > [data-test]').map(node => node.attributes('data-test'));
  // 滚到「最近添加」顶边往下 100px 处（像用户那样滚，发 scroll 事件）。
  function scrollIntoRecent() {
    geometry.scrollTop += offsetOf('home-recent') + 100;
    host.dispatchEvent(new Event('scroll'));
    return geometry.scrollTop;
  }
  // App.goBackFromDetail 的顺序：离开（整页详情从顶部看起）→ 切回 → 等一拍 → 按离开时的 scrollTop 放回。
  async function leaveAndComeBack(wrapper, { restore = true } = {}) {
    const saved = geometry.scrollTop;
    await wrapper.setProps({ pageActive: false });
    geometry.scrollTop = 0;
    await wrapper.setProps({ pageActive: true });
    await nextTick();
    if (restore) geometry.scrollTop = saved;
  }
  const pending = mockFn => {
    let resolve;
    mockFn.mockReturnValueOnce(new Promise(done => { resolve = done; }));
    return value => resolve(value);
  };

  it('keeps 最近添加 in place when 追到哪了 fills in after coming back, with no skeleton flash meanwhile', async () => {
    const wrapper = await mountHome();
    expect(blocks(wrapper)).toEqual(['home-hero', 'home-continue', 'home-tonight', 'home-recent', 'home-people', 'home-highlight']);
    scrollIntoRecent();
    expect(offsetOf('home-recent')).toBe(-100);

    const settleProgress = pending(api.ListCollectionProgress);
    const settleWatchlist = pending(api.ListWatchlistInLibrary);
    await leaveAndComeBack(wrapper);
    await flushPromises();
    // 重取期间上一次是空的几块保持不显示（不先闪出骨架），最近添加还在原处。
    expect(wrapper.findAll('[aria-busy="true"]')).toHaveLength(0);
    expect(offsetOf('home-recent')).toBe(-100);

    settleProgress([progress(1)]);
    settleWatchlist([]);
    await flushPromises();
    expect(blocks(wrapper)).toContain('home-progress');
    expect(offsetOf('home-recent')).toBe(-100);
  });

  it('keeps 最近添加 in place when 继续观看 appears or 追到哪了 empties after coming back', async () => {
    api.ListContinueWatchingWithFilter.mockResolvedValueOnce({ videos: [] });
    api.ListCollectionProgress.mockResolvedValueOnce([progress(1)]);
    const wrapper = await mountHome();
    expect(blocks(wrapper)).toEqual(['home-hero', 'home-progress', 'home-tonight', 'home-recent', 'home-people', 'home-highlight']);
    scrollIntoRecent();

    // 在整页详情里开始看了一部、看完了那个作品集：继续观看出现在最上面，追到哪了收起。
    const settleContinue = pending(api.ListContinueWatchingWithFilter);
    const settleProgress = pending(api.ListCollectionProgress);
    await leaveAndComeBack(wrapper);
    await flushPromises();
    expect(offsetOf('home-recent')).toBe(-100);
    settleContinue({ videos: [video(5)] });
    await flushPromises();
    expect(blocks(wrapper)).toContain('home-continue');
    expect(offsetOf('home-recent')).toBe(-100);
    settleProgress([]);
    await flushPromises();
    expect(blocks(wrapper)).not.toContain('home-progress');
    expect(offsetOf('home-recent')).toBe(-100);
  });

  it('follows the user scrolling between two late blocks, keeping the new place when the second one lands', async () => {
    const wrapper = await mountHome();
    scrollIntoRecent();
    const settleContinue = pending(api.ListContinueWatchingWithFilter);
    const settleProgress = pending(api.ListCollectionProgress);
    await leaveAndComeBack(wrapper);
    settleProgress([progress(1)]);
    await flushPromises();
    expect(offsetOf('home-recent')).toBe(-100);
    // 用户接着往下滚了 50px，然后继续观看那一块才回来（少了一部，块高不变；再让它整块收起）。
    geometry.scrollTop += 50;
    host.dispatchEvent(new Event('scroll'));
    settleContinue({ videos: [] });
    await flushPromises();
    expect(blocks(wrapper)).not.toContain('home-continue');
    expect(offsetOf('home-recent')).toBe(-150);
  });

  it('records the block at the viewport top when leaving, even if the last scroll never fired a scroll event', async () => {
    const wrapper = await mountHome();
    geometry.scrollTop += offsetOf('home-recent') + 100;
    const settle = pending(api.ListCollectionProgress);
    await leaveAndComeBack(wrapper);
    settle([progress(1)]);
    await flushPromises();
    expect(offsetOf('home-recent')).toBe(-100);
  });

  it('does not move the view when home was entered some other way or the user scrolled since', async () => {
    const wrapper = await mountHome();
    scrollIntoRecent();
    // 从导航切回首页：App 不放回滚动位置，视口在顶部；结果出来不把人拉回离开时的位置。
    const settle = pending(api.ListCollectionProgress);
    await leaveAndComeBack(wrapper, { restore: false });
    settle([progress(1)]);
    await flushPromises();
    expect(geometry.scrollTop).toBe(0);

    // 用户刚滚过（滚动事件还没派发）时，墙上面多出一块也不把视口拉回去。
    scrollIntoRecent();
    const saved = geometry.scrollTop;
    const again = pending(api.ListCollectionProgress);
    await leaveAndComeBack(wrapper);
    geometry.scrollTop = saved + 250;
    again([]);
    await flushPromises();
    expect(geometry.scrollTop).toBe(saved + 250);
  });

  it('opts out of the browser scroll anchoring so only this page moves the view', () => {
    const source = readFileSync(resolve(process.cwd(), 'src/components/viewer/HomePage.vue'), 'utf8');
    expect(source).toMatch(/\.viewer-home \{[^}]*overflow-anchor: none/);
  });

  it('shows skeletons on the first load and on a retry after an error, not on a refresh of a block that was empty', async () => {
    const settleFirst = pending(api.ListWatchlistInLibrary);
    const wrapper = await mountHome();
    expect(wrapper.find('[data-test="home-watchlist-loading"]').exists()).toBe(true);
    settleFirst([]);
    await flushPromises();
    expect(wrapper.find('[data-test="home-watchlist"]').exists()).toBe(false);

    const refresh = pending(api.ListWatchlistInLibrary);
    await wrapper.setProps({ pageActive: false });
    await wrapper.setProps({ pageActive: true });
    await flushPromises();
    expect(wrapper.find('[data-test="home-watchlist"]').exists()).toBe(false);
    refresh(Promise.reject('数据库不可用'));
    await flushPromises();
    expect(wrapper.find('[data-test="home-watchlist-error"]').exists()).toBe(true);

    const retry = pending(api.ListWatchlistInLibrary);
    await wrapper.get('[data-test="home-watchlist-retry"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-test="home-watchlist-loading"]').exists()).toBe(true);
    retry([]);
    await flushPromises();
    expect(wrapper.find('[data-test="home-watchlist"]').exists()).toBe(false);
  });
});

// 「查看全部」（2026-10-14）：只有没放全（多取的那一条取到了）的块才有；继续观看、最近添加去「视频」页的同一视图，
// 追到哪了、想看已在库开弹窗按页取全（与区块同一口径）。
describe('HomePage view-all', () => {
  const many = (count, make) => Array.from({ length: count }, (_, index) => make(index + 1));
  const progress = id => ({ collection_id: id, name: `作品集${id}`, has_cover: false, total_works: 3, watched_works: 1, next: { video_id: 500 + id, title: `下一部${id}`, work_index: 2 } });
  const wish = id => ({ entry_id: id, entry_title: `想看${id}`, year: 2000, has_poster: false, video_id: 600 + id, origin: 'manual' });

  it('shows no 查看全部 when every block fits', async () => {
    const wrapper = await mountPage();
    for (const key of ['continue', 'progress', 'recent', 'watchlist']) expect(wrapper.find(`[data-test="home-${key}-all"]`).exists()).toBe(false);
  });

  it('continue / recent: trims the extra item and navigates to the same view on 视频', async () => {
    api.ListContinueWatchingWithFilter.mockResolvedValue({ videos: many(HOME_CONTINUE_LIMIT + 1, id => video(id, { watch_position_seconds: 30 })) });
    api.SearchLibraryVideoPage.mockResolvedValue({ videos: many(HOME_RECENT_LIMIT + 1, id => video(100 + id)) });
    const wrapper = await mountPage();
    expect(wrapper.findAll('[data-test="home-continue"] .recap-strip__item')).toHaveLength(6);
    await wrapper.get('[data-test="home-continue"] [aria-label="下一组"]').trigger('click');
    expect(wrapper.findAll('[data-test="home-continue"] .recap-strip__item')).toHaveLength(HOME_CONTINUE_LIMIT - 6);
    expect(wrapper.findAll('[data-test="home-recent-item"]')).toHaveLength(HOME_RECENT_LIMIT);
    await wrapper.get('[data-test="home-continue-all"]').trigger('click');
    await wrapper.get('[data-test="home-recent-all"]').trigger('click');
    expect(wrapper.emitted('navigate')).toEqual([['videos', { watch: 'continue_watching' }], ['videos', { watch: 'recently_added' }]]);
  });

  it('追到哪了: opens a paged dialog with the same set and opens the picked video', async () => {
    api.ListCollectionProgress.mockResolvedValue(many(HOME_PROGRESS_LIMIT + 1, progress));
    api.ListCollectionProgressPage
      .mockResolvedValueOnce({ items: many(50, progress), total: 51, has_more: true })
      .mockResolvedValueOnce({ items: [progress(51)], total: 51, has_more: false });
    const wrapper = await mountPage();
    expect(wrapper.findAll('[data-test="home-progress-item"]')).toHaveLength(HOME_PROGRESS_LIMIT);
    await wrapper.get('[data-test="home-progress-all"]').trigger('click');
    await flushPromises();
    expect(api.ListCollectionProgressPage).toHaveBeenCalledWith({ collection_ids: [], continue_only: true, offset: 0, limit: 50 });
    const dialog = wrapper.get('[data-test="home-progress-dialog"]');
    expect(dialog.get('[data-test="view-all-count"]').text()).toBe('共 51 个');
    expect(dialog.findAll('[data-test="view-all-item"]')).toHaveLength(50);
    await dialog.get('[data-test="view-all-more"]').trigger('click');
    await flushPromises();
    expect(api.ListCollectionProgressPage).toHaveBeenLastCalledWith({ collection_ids: [], continue_only: true, offset: 50, limit: 50 });
    expect(wrapper.findAll('[data-test="view-all-item"]')).toHaveLength(51);
    expect(wrapper.find('[data-test="view-all-more"]').exists()).toBe(false);
    await wrapper.findAll('[data-test="view-all-item"]')[50].trigger('click');
    expect(wrapper.emitted('open-video')).toEqual([[551]]);
    expect(wrapper.find('[data-test="home-progress-dialog"]').exists()).toBe(false);
  });

  it('想看已在库: opens a paged dialog from ListWatchlistInLibraryPage', async () => {
    api.ListWatchlistInLibrary.mockResolvedValue(many(HOME_WATCHLIST_LIMIT + 1, wish));
    api.ListWatchlistInLibraryPage.mockResolvedValue({ items: many(13, wish), total: 13, has_more: false });
    const wrapper = await mountPage();
    expect(wrapper.findAll('[data-test="home-watchlist-item"]')).toHaveLength(HOME_WATCHLIST_LIMIT);
    await wrapper.get('[data-test="home-watchlist-all"]').trigger('click');
    await flushPromises();
    expect(api.ListWatchlistInLibraryPage).toHaveBeenCalledWith(0, 50);
    const dialog = wrapper.get('[data-test="home-watchlist-dialog"]');
    expect(dialog.get('[data-test="view-all-count"]').text()).toBe('共 13 部');
    expect(dialog.findAll('[data-test="view-all-item"]')).toHaveLength(13);
    expect(dialog.find('[data-test="view-all-more"]').exists()).toBe(false);
  });
});


describe('HomePage 播放后的推荐补位', () => {
  const ids = wrapper => wrapper.vm.tonightBlock.items.map(item => item.video.id);
  it('只打开不换；开始播放后只替换原槽位，重复通知不再抽取', async () => {
    const wrapper = await mountPage();
    await wrapper.findAll('[data-test="home-tonight-item"]')[1].trigger('click');
    expect(api.SuggestTonightVideos).toHaveBeenCalledTimes(1);
    api.SuggestTonightVideos.mockResolvedValue([suggestion(14)]);
    await wrapper.vm.replacePlayedTonight(12);
    expect(api.SuggestTonightVideos).toHaveBeenLastCalledWith(expect.objectContaining({ exclude_ids: [11, 12, 13] }), 1);
    expect(ids(wrapper)).toEqual([11, 14, 13]);
    await wrapper.vm.replacePlayedTonight(12);
    await wrapper.vm.replacePlayedTonight(13); // 没有从今晚看什么打开的片不换。
    expect(api.SuggestTonightVideos).toHaveBeenCalledTimes(2);
    expect(wrapper.get('[data-test="home-hero-title"]').text()).toBe('影片1');
  });

  it('大图来自今晚看什么且播放了它时，大图与对应推荐一起替换', async () => {
    api.ListContinueWatchingWithFilter.mockResolvedValue({ videos: [] });
    const wrapper = await mountPage();
    await wrapper.get('[data-test="home-hero-open"]').trigger('click');
    api.SuggestTonightVideos.mockResolvedValue([suggestion(14)]);
    await wrapper.vm.replacePlayedTonight(11);
    await flushPromises();
    expect(ids(wrapper)).toEqual([14, 12, 13]);
    expect(wrapper.get('[data-test="home-hero-title"]').text()).toBe('影片14');
  });

  it.each(['empty', 'failed'])('候选 %s 保留旧卡片并说明', async mode => {
    const wrapper = await mountPage();
    await wrapper.findAll('[data-test="home-tonight-item"]')[0].trigger('click');
    if (mode === 'empty') api.SuggestTonightVideos.mockResolvedValue([]);
    else api.SuggestTonightVideos.mockRejectedValue(new Error('读取失败'));
    await wrapper.vm.replacePlayedTonight(11);
    expect(ids(wrapper)).toEqual([11, 12, 13]);
    expect(wrapper.get(mode === 'empty' ? '[data-test="home-tonight-notice"]' : '[data-test="home-tonight-error"]').text()).not.toBe('');
  });

  it('两项先后开始播放时串行补位，第二次排除第一项的新候选', async () => {
    const wrapper = await mountPage();
    await wrapper.findAll('[data-test="home-tonight-item"]')[0].trigger('click');
    let finish;
    api.SuggestTonightVideos.mockReturnValueOnce(new Promise(resolve => { finish = resolve; })).mockResolvedValueOnce([suggestion(15)]);
    const first = wrapper.vm.replacePlayedTonight(11);
    await wrapper.findAll('[data-test="home-tonight-item"]')[1].trigger('click');
    const second = wrapper.vm.replacePlayedTonight(12);
    await flushPromises();
    expect(api.SuggestTonightVideos).toHaveBeenCalledTimes(2);
    finish([suggestion(14)]);
    await Promise.all([first, second]);
    expect(api.SuggestTonightVideos).toHaveBeenLastCalledWith(expect.objectContaining({ exclude_ids: [14, 12, 13] }), 1);
    expect(ids(wrapper)).toEqual([14, 15, 13]);
  });

  it('换批后不采用迟到的单项替换', async () => {
    const wrapper = await mountPage();
    await wrapper.findAll('[data-test="home-tonight-item"]')[0].trigger('click');
    let finish;
    api.SuggestTonightVideos.mockReturnValueOnce(new Promise(resolve => { finish = resolve; }));
    const replacing = wrapper.vm.replacePlayedTonight(11);
    await flushPromises();
    api.SuggestTonightVideos.mockResolvedValueOnce([suggestion(21), suggestion(22), suggestion(23)]);
    await wrapper.vm.loadTonight({ reroll: true });
    finish([suggestion(14)]);
    await replacing;
    expect(ids(wrapper)).toEqual([21, 22, 23]);
  });
});


it('今晚打开但没播放，返回后从其他入口播放同一部不补位', async () => {
  const wrapper = await mountPage();
  await wrapper.findAll('[data-test="home-tonight-item"]')[0].trigger('click');
  await wrapper.setProps({ pageActive: false });
  await wrapper.setProps({ pageActive: true });
  await wrapper.vm.replacePlayedTonight(11);
  expect(api.SuggestTonightVideos).toHaveBeenCalledTimes(1);
  await wrapper.findAll('[data-test="home-tonight-item"]')[0].trigger('click');
  wrapper.vm.openVideo(video(11));
  await wrapper.vm.replacePlayedTonight(11);
  expect(api.SuggestTonightVideos).toHaveBeenCalledTimes(1);
});
