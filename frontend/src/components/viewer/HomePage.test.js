import { flushPromises, mount, enableAutoUnmount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
  GetRecap: vi.fn(),
  GetVideoDetails: vi.fn(),
  ListCollectionProgress: vi.fn(),
  ListContinueWatchingWithFilter: vi.fn(),
  ListWatchlistInLibrary: vi.fn(),
  SearchLibraryVideoPage: vi.fn(),
  SuggestTonightVideos: vi.fn()
}));
vi.mock('../../../wailsjs/go/main/App', () => api);
import HomePage, { HOME_PEOPLE_LIMIT, HOME_PROGRESS_LIMIT, HOME_WATCHLIST_LIMIT, homeProgressText, pickHomeHero } from './HomePage.vue';
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
    expect(api.ListContinueWatchingWithFilter).toHaveBeenCalledWith({}, '', 0, 10);
    expect(api.SuggestTonightVideos).toHaveBeenCalledWith({ filter: {}, watch_state: 'any', favorites_only: false, max_duration_seconds: 0, exclude_ids: [] }, 3);
    expect(api.SearchLibraryVideoPage).toHaveBeenCalledWith({ filter: { smart_view: 'recently_added' }, limit: 14 });
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
    expect(api.ListCollectionProgress).toHaveBeenCalledWith({ collection_ids: [], continue_only: true, limit: HOME_PROGRESS_LIMIT });
    expect(HOME_PROGRESS_LIMIT).toBe(12);
    expect(api.ListWatchlistInLibrary).toHaveBeenCalledWith(HOME_WATCHLIST_LIMIT);
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
