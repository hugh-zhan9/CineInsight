import { flushPromises, mount, enableAutoUnmount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ GetRecap: vi.fn(), ListRecapItems: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);
import RecapPage, { RECAP_ACTIVITY_DEBOUNCE_MS, RECAP_POLL_MS } from './RecapPage.vue';

enableAutoUnmount(afterEach);

const video = (id, extra = {}) => ({ id, title: `影片${id}`, name: `v${id}.mp4`, duration: 600, personal_rating: null, available: true, ...extra });
const image = (id, extra = {}) => ({ id, name: `p${id}.jpg`, width: 400, height: 300, directory: '/p', available: true, ...extra });
const comparison = { label: '上月同期', plays: 2, videos: 1, image_views: 0, plays_change: 50, videos_change: 0, image_views_change: null };
const hours = Array.from({ length: 24 }, (_, hour) => ({ hour, plays: hour === 21 ? 3 : 0, views: 0 }));

function recap(overrides = {}) {
  return {
    period: 'month', year: 2026, month: 10, label: '2026年10月', in_progress: true, generated_at: '2026-10-11T10:00:00+08:00', empty: false,
    overview: { plays: 3, legacy_plays: 0, videos: 2, image_views: 1, images: 1, active_days: 2, longest_streak: 1, previous: comparison },
    favorite: {
      video: video(1), count: 2, last_at: '2026-10-05T21:00:00+08:00', tags: [{ id: 5, name: '科幻', color: '#336699' }],
      people: [{ id: 8, name: '演员甲', image_url: '' }], collections: [], related: [{ video: video(3), shared: 2 }, { video: video(4, { available: true }), shared: 1 }]
    },
    favorite_image: { image: image(11), count: 1, last_at: '2026-10-06T22:00:00+08:00', tags: [], people: [], related: [] },
    top_videos: [{ video: video(1), count: 2, last_at: '2026-10-05T21:00:00+08:00' }, { video: video(2, { available: false }), count: 1, last_at: '2026-10-02T21:00:00+08:00' }],
    top_tags: [{ tag: { id: 5, name: '科幻', color: '#336699' }, weight: 2, videos: 1 }],
    top_people: [{ entity: { id: 8, name: '演员甲', image_url: '' }, weight: 2, videos: 1 }], top_collections: [],
    top_images: [{ image: image(11), count: 1, last_at: '2026-10-06T22:00:00+08:00' }],
    habits: { total: 4, hours, days: [{ date: '2026-10-01', plays: 1, views: 0 }, { date: '2026-10-02', plays: 2, views: 1 }], busiest_day: { date: '2026-10-02', plays: 2, views: 1 },
      peak_hour: 21, peak_slot: 'evening', weekend_count: 1, weekend_share: 0.25, late_night_count: 0, late_night_share: 0, sources: [{ kind: 'video', source: 'desktop_play', count: 3 }, { kind: 'image', source: 'desktop_viewer', count: 1 }] },
    freshness: { new_videos: 1, rewatched_videos: 1, new_samples: [video(2, { available: false })], new_favorite_videos: 0, new_favorite_images: 0, diary_rated: 0 },
    titles: [{ key: 'night_owl', name: '夜猫子', detail: '40% 的记录发生在 0–5 点（2/5）' }],
    months: [], month_best: [], quarter_tags: [], feed: [], week: [], now_playing: null,
    ...overrides
  };
}

const today = new Date();
const thisMonth = { period: 'month', year: today.getFullYear(), month: today.getMonth() + 1 };
let handlers;

beforeEach(() => {
  api.GetRecap.mockReset();
  api.GetRecap.mockImplementation(request => Promise.resolve(recap({ period: request.period, year: request.year, month: request.month })));
  handlers = {};
  window.runtime = { EventsOn: vi.fn((name, handler) => { handlers[name] = handler; return () => delete handlers[name]; }) };
});

afterEach(() => {
  delete window.runtime;
  vi.useRealTimers();
});

async function mountPage(props = {}) {
  const wrapper = mount(RecapPage, { props });
  await flushPromises();
  return wrapper;
}

describe('RecapPage', () => {
  it('opens on this month and renders the favorite hero, numbers, titles and related strip from facts', async () => {
    const wrapper = await mountPage();
    expect(api.GetRecap).toHaveBeenCalledWith(thisMonth);
    expect(wrapper.get('[data-test="recap-hero-title"]').text()).toBe('《影片1》');
    // 大图文字层是 HeroHeader（页面标题在 PageHeader，这里是 h3）；标签芯片是带颜色圆点的 TagPill。
    expect(wrapper.get('[data-test="recap-hero-title"]').element.tagName).toBe('H3');
    const tagChip = wrapper.get('[data-test="recap-hero-tag"]');
    expect(tagChip.classes()).toEqual(expect.arrayContaining(['ui-tag-pill', 'recap-chip']));
    expect(tagChip.find('.ui-tag-pill__leading .recap-chip__dot').exists()).toBe(true);
    expect(tagChip.text()).toBe('科幻');
    // 回顾范围是 SegmentedControl：当前月度 aria-checked。
    expect(wrapper.get('[data-test="recap-tab-month"]').attributes('aria-checked')).toBe('true');
    expect(wrapper.get('[data-test="recap-stats"]').text()).toContain('播放次数');
    expect(wrapper.get('[data-test="recap-stats"]').text()).toContain('↑ 50%');
    expect(wrapper.findAll('[data-test="recap-title"]')).toHaveLength(1);
    expect(wrapper.get('[data-test="recap-title"]').text()).toContain('2/5');
    expect(wrapper.findAll('[data-test="recap-related"] [data-test="recap-card-video"]')).toHaveLength(2);
    expect(wrapper.find('[data-test="recap-in-progress"]').exists()).toBe(true);
    expect(wrapper.get('[data-test="recap-next"]').attributes('disabled')).toBeDefined();
  });

  it('switches tabs and periods with the matching request', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-test="recap-prev"]').trigger('click');
    await flushPromises();
    const previous = thisMonth.month === 1 ? { period: 'month', year: thisMonth.year - 1, month: 12 } : { ...thisMonth, month: thisMonth.month - 1 };
    expect(api.GetRecap).toHaveBeenLastCalledWith(previous);
    expect(wrapper.get('[data-test="recap-next"]').attributes('disabled')).toBeUndefined();
    await wrapper.get('[data-test="recap-tab-year"]').trigger('click');
    await flushPromises();
    expect(api.GetRecap).toHaveBeenLastCalledWith({ period: 'year', year: previous.year, month: 0 });
    await wrapper.get('[data-test="recap-tab-live"]').trigger('click');
    await flushPromises();
    expect(api.GetRecap).toHaveBeenLastCalledWith({ period: 'live', year: 0, month: 0 });
    expect(wrapper.find('[data-test="recap-prev"]').exists()).toBe(false);
  });

  it('drops a late response from an earlier request', async () => {
    let releaseFirst;
    api.GetRecap.mockImplementationOnce(() => new Promise(resolve => { releaseFirst = resolve; }));
    const wrapper = await mountPage();
    api.GetRecap.mockResolvedValueOnce(recap({ period: 'year', label: '新年度', favorite: { ...recap().favorite, video: video(9, { title: '新结果' }) } }));
    await wrapper.get('[data-test="recap-tab-year"]').trigger('click');
    await flushPromises();
    releaseFirst(recap({ favorite: { ...recap().favorite, video: video(7, { title: '旧结果' }) } }));
    await flushPromises();
    expect(wrapper.get('[data-test="recap-hero-title"]').text()).toBe('《新结果》');
  });

  it('shows the empty state without hero or titles, and errors are shown instead of an empty success', async () => {
    api.GetRecap.mockResolvedValueOnce(recap({ empty: true, favorite: null, favorite_image: null, titles: [], top_videos: [] }));
    const wrapper = await mountPage();
    expect(wrapper.find('[data-test="recap-empty"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="recap-hero"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="recap-title"]').exists()).toBe(false);
    api.GetRecap.mockRejectedValueOnce(new Error('db down'));
    await wrapper.get('[data-test="recap-tab-year"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="recap-error"]').text()).toContain('db down');
    expect(wrapper.find('[data-test="recap-empty"]').exists()).toBe(false);
  });

  it('clicks through to the video drawer, library tag filter, photo viewer, people and the diary', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-test="recap-hero-open"]').trigger('click');
    expect(wrapper.emitted('open-video-id')[0]).toEqual([1]);
    await wrapper.get('[data-test="recap-hero-tag"]').trigger('click');
    expect(wrapper.emitted('open-tag')[0]).toEqual([{ id: 5, name: '科幻' }]);
    await wrapper.get('[data-test="recap-top-images"] [data-test="recap-card-image"]').trigger('click');
    expect(wrapper.emitted('open-image')[0]).toEqual([{ id: 11, name: 'p11.jpg' }]);
    const posters = wrapper.findAll('[data-test="recap-top-videos"] [data-test="recap-card-video"]');
    await posters[1].trigger('click');
    expect(wrapper.emitted('open-video-id')).toHaveLength(1);
    await wrapper.get('.recap-person').trigger('click');
    expect(wrapper.emitted('open-entity')[0]).toEqual([{ type: 'person', id: 8, name: '演员甲' }]);
    await wrapper.get('[data-test="recap-tab-year"]').trigger('click');
    await flushPromises();
    await wrapper.get('[data-test="recap-open-diary"]').trigger('click');
    expect(wrapper.emitted('navigate')[0]).toEqual(['viewing-notes']);
  });
});

describe('RecapPage freshness switch', () => {
  const freshness = (overrides = {}) => ({
    new_videos: 2, new_samples: [video(21), video(22)],
    rewatched_videos: 3, rewatched_samples: [video(31), video(32, { title: '' })],
    new_favorite_videos: 1, new_favorite_images: 1,
    new_favorite_samples: [
      { kind: 'image', id: 41, title: 'p41.jpg', favorited_at: '2026-10-09T10:00:00+08:00', available: true },
      { kind: 'video', id: 42, title: '收藏片', favorited_at: '2026-10-08T10:00:00+08:00', available: true }
    ],
    diary_rated: 3,
    diary_rated_samples: [
      { id: 51, title: '有关联', rating: 9.5, watched_on: '2026-10-20', video_id: 61, video_available: true },
      { id: 52, title: '无关联', rating: 7, watched_on: '2026-10-05', video_id: null, video_available: false },
      { id: 53, title: '视频已删', rating: 8, watched_on: '2026-10-03', video_id: 62, video_available: false }
    ],
    ...overrides
  });
  const options = ['new', 'rewatched', 'favorites', 'diary'];
  const option = (wrapper, key) => wrapper.get(`[data-test="recap-fresh-${key}"]`);
  const checked = wrapper => options.filter(key => option(wrapper, key).attributes('aria-checked') === 'true');
  const accented = wrapper => options.filter(key => option(wrapper, key).classes().includes('recap-fact--accent'));
  const cards = wrapper => wrapper.findAll('[data-test="recap-fresh-samples"] .recap-card');

  async function mountFresh(overrides = {}, mountOptions = {}) {
    api.GetRecap.mockImplementation(() => Promise.resolve(recap({ freshness: freshness(overrides) })));
    const wrapper = mount(RecapPage, mountOptions);
    await flushPromises();
    return wrapper;
  }

  it('is a radio group that starts on 新发现, with only the selected card accented', async () => {
    const wrapper = await mountFresh();
    const group = wrapper.get('[data-test="recap-freshness"]');
    expect(group.attributes('role')).toBe('radiogroup');
    for (const key of options) {
      const element = option(wrapper, key);
      expect(element.element.tagName).toBe('BUTTON');
      expect(element.attributes('role')).toBe('radio');
      expect(element.classes()).toContain('btn-plain');
    }
    expect(checked(wrapper)).toEqual(['new']);
    expect(accented(wrapper)).toEqual(['new']);
    expect(option(wrapper, 'new').attributes('tabindex')).toBe('0');
    expect(option(wrapper, 'diary').attributes('tabindex')).toBe('-1');
    expect(option(wrapper, 'favorites').text()).toContain('视频 1 · 图片 1');
    expect(cards(wrapper).map(card => card.text())).toEqual(['影片21本期新发现', '影片22本期新发现']);
    await cards(wrapper)[1].trigger('click');
    expect(wrapper.emitted('open-video-id')).toEqual([[22]]);
  });

  it('switches the strip and its subtitle with the selected category', async () => {
    const wrapper = await mountFresh();
    await option(wrapper, 'rewatched').trigger('click');
    expect(checked(wrapper)).toEqual(['rewatched']);
    expect(accented(wrapper)).toEqual(['rewatched']);
    expect(cards(wrapper).map(card => card.text())).toEqual(['影片31本期重温', '已删除的视频本期重温']);
    await cards(wrapper)[0].trigger('click');
    expect(wrapper.emitted('open-video-id')).toEqual([[31]]);

    await option(wrapper, 'favorites').trigger('click');
    expect(accented(wrapper)).toEqual(['favorites']);
    const favorites = cards(wrapper);
    expect(favorites.map(card => card.attributes('data-test'))).toEqual(['recap-card-image', 'recap-card-video']);
    expect(favorites.map(card => card.text())).toEqual(['p41.jpg本期新收藏', '收藏片本期新收藏']);
    expect(favorites[0].get('img').attributes('src')).toBe('/preview/image-thumbnail/41');
    await favorites[0].trigger('click');
    expect(wrapper.emitted('open-image')).toEqual([[{ id: 41, name: 'p41.jpg' }]]);
    await favorites[1].trigger('click');
    expect(wrapper.emitted('open-video-id')).toEqual([[31], [42]]);

    await option(wrapper, 'diary').trigger('click');
    expect(accented(wrapper)).toEqual(['diary']);
    const diary = cards(wrapper);
    expect(diary.map(card => card.text())).toEqual(['9.5 分有关联本期日记评分', '7.0 分无关联本期日记评分', '8.0 分视频已删本期日记评分']);
    // 只有还在库的关联视频才拿它的缩略图；没有关联、视频已删的用占位图标，但都能点。
    expect(diary[0].get('img').attributes('src')).toBe('/preview/thumbnail/61');
    expect(diary[1].find('img').exists()).toBe(false);
    expect(diary[2].find('img').exists()).toBe(false);
    expect(diary.map(card => card.attributes('disabled'))).toEqual([undefined, undefined, undefined]);
    await diary[0].trigger('click');
    expect(wrapper.emitted('open-video-id')).toEqual([[31], [42], [61]]);
    await diary[1].trigger('click');
    await diary[2].trigger('click');
    expect(wrapper.emitted('navigate')).toEqual([['viewing-notes'], ['viewing-notes']]);
  });

  it('moves the selection and focus with the arrow keys, wrapping at both ends', async () => {
    const wrapper = await mountFresh({}, { attachTo: document.body });
    const group = wrapper.get('[data-test="recap-freshness"]');
    option(wrapper, 'new').element.focus();
    await group.trigger('keydown', { key: 'ArrowRight' });
    expect(checked(wrapper)).toEqual(['rewatched']);
    expect(document.activeElement).toBe(option(wrapper, 'rewatched').element);
    expect(cards(wrapper)[0].text()).toContain('本期重温');
    await group.trigger('keydown', { key: 'ArrowRight' });
    await group.trigger('keydown', { key: 'ArrowRight' });
    expect(checked(wrapper)).toEqual(['diary']);
    await group.trigger('keydown', { key: 'ArrowRight' });
    expect(checked(wrapper)).toEqual(['new']);
    await group.trigger('keydown', { key: 'ArrowLeft' });
    expect(checked(wrapper)).toEqual(['diary']);
    expect(document.activeElement).toBe(option(wrapper, 'diary').element);
    expect(option(wrapper, 'diary').attributes('tabindex')).toBe('0');
    expect(option(wrapper, 'new').attributes('tabindex')).toBe('-1');
    await group.trigger('keydown', { key: 'Home' });
    expect(checked(wrapper)).toEqual(['new']);
    await group.trigger('keydown', { key: 'End' });
    expect(checked(wrapper)).toEqual(['diary']);
    await group.trigger('keydown', { key: 'Enter' });
    expect(checked(wrapper)).toEqual(['diary']);
  });

  it('shows one muted line when a category is zero or all of its media are gone', async () => {
    const wrapper = await mountFresh({ rewatched_videos: 0, rewatched_samples: [], new_videos: 3, new_samples: [], diary_rated: 0, diary_rated_samples: [] });
    expect(wrapper.find('[data-test="recap-fresh-samples"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="recap-fresh-empty"]').text()).toBe('这 3 部都已不在库中，记录仍然保留。');
    await option(wrapper, 'rewatched').trigger('click');
    expect(checked(wrapper)).toEqual(['rewatched']);
    expect(accented(wrapper)).toEqual(['rewatched']);
    expect(wrapper.find('[data-test="recap-fresh-samples"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="recap-fresh-empty"]').text()).toBe('这一期没有重温。');
    await option(wrapper, 'diary').trigger('click');
    expect(wrapper.get('[data-test="recap-fresh-empty"]').text()).toBe('这一期没有观影日记评分。');
    await option(wrapper, 'favorites').trigger('click');
    expect(wrapper.find('[data-test="recap-fresh-empty"]').exists()).toBe(false);
    expect(cards(wrapper)).toHaveLength(2);
  });
});

describe('RecapPage live refresh', () => {
  const liveResult = () => recap({
    period: 'live', label: '今天', favorite: null, favorite_image: null, titles: [],
    feed: [{ kind: 'image', id: 11, title: 'p11.jpg', source: 'desktop_viewer', at: '2026-10-11T09:30:00+08:00', available: true },
      { kind: 'video', id: 1, title: '影片1', source: 'inline_view', at: '2026-10-11T09:00:00+08:00', available: true }],
    week: Array.from({ length: 7 }, (_, index) => ({ date: `2026-10-${String(5 + index).padStart(2, '0')}`, plays: index, views: 0 })),
    now_playing: { video: video(1), basis: 'recent_play', source: 'inline_view', at: '2026-10-11T09:00:00+08:00' }
  });

  it('refreshes on recap-activity only while the live tab is visible, debounced', async () => {
    vi.useFakeTimers();
    api.GetRecap.mockImplementation(() => Promise.resolve(liveResult()));
    const wrapper = await mountPage();
    await wrapper.get('[data-test="recap-tab-live"]').trigger('click');
    await flushPromises();
    expect(wrapper.findAll('[data-test="recap-feed"] li')).toHaveLength(2);
    expect(wrapper.find('[data-test="recap-live-hero"]').exists()).toBe(true);
    const calls = api.GetRecap.mock.calls.length;
    handlers['recap-activity']({ kind: 'video' });
    handlers['recap-activity']({ kind: 'video' });
    vi.advanceTimersByTime(RECAP_ACTIVITY_DEBOUNCE_MS);
    await flushPromises();
    expect(api.GetRecap).toHaveBeenCalledTimes(calls + 1);
    expect(api.GetRecap).toHaveBeenLastCalledWith({ period: 'live', year: 0, month: 0 });
    await wrapper.setProps({ pageActive: false });
    const hiddenCalls = api.GetRecap.mock.calls.length;
    handlers['recap-activity']({ kind: 'image' });
    vi.advanceTimersByTime(RECAP_POLL_MS * 2);
    await flushPromises();
    expect(api.GetRecap).toHaveBeenCalledTimes(hiddenCalls);
    await wrapper.get('[data-test="recap-feed-image"]').trigger('click');
    expect(wrapper.emitted('open-image')[0]).toEqual([{ id: 11, name: 'p11.jpg' }]);
  });

  it('polls every 60 s on the live tab while visible, and not on month or when hidden', async () => {
    vi.useFakeTimers();
    api.GetRecap.mockImplementation(request => Promise.resolve(request.period === 'live' ? liveResult() : recap()));
    const wrapper = await mountPage();
    const monthCalls = api.GetRecap.mock.calls.length;
    vi.advanceTimersByTime(RECAP_POLL_MS * 3);
    await flushPromises();
    expect(api.GetRecap).toHaveBeenCalledTimes(monthCalls);
    handlers['recap-activity']({ kind: 'video' });
    vi.advanceTimersByTime(RECAP_ACTIVITY_DEBOUNCE_MS);
    await flushPromises();
    expect(api.GetRecap).toHaveBeenCalledTimes(monthCalls);
    await wrapper.get('[data-test="recap-tab-live"]').trigger('click');
    await flushPromises();
    const liveCalls = api.GetRecap.mock.calls.length;
    vi.advanceTimersByTime(RECAP_POLL_MS);
    await flushPromises();
    expect(api.GetRecap).toHaveBeenCalledTimes(liveCalls + 1);
    await wrapper.setProps({ pageActive: false });
    vi.advanceTimersByTime(RECAP_POLL_MS * 2);
    await flushPromises();
    expect(api.GetRecap).toHaveBeenCalledTimes(liveCalls + 1);
    await wrapper.setProps({ pageActive: true });
    await flushPromises();
    expect(api.GetRecap).toHaveBeenCalledTimes(liveCalls + 2);
    vi.advanceTimersByTime(RECAP_POLL_MS);
    await flushPromises();
    expect(api.GetRecap).toHaveBeenCalledTimes(liveCalls + 3);
  });

  it('offers 查看全部 only where the number on the page is larger than what it lists', async () => {
    let wrapper = await mountPage();
    expect(wrapper.findAll('[data-test^="recap-all-"]')).toHaveLength(0);
    wrapper.unmount();
    const fresh = recap().freshness;
    api.GetRecap.mockResolvedValue(recap({
      overview: { ...recap().overview, videos: 15, images: 1 },
      top_tags_total: 12, top_people_total: 1, top_collections_total: 0,
      freshness: { ...fresh, new_videos: 137, rewatched_videos: 4, rewatched_samples: [] }
    }));
    wrapper = await mountPage();
    expect(wrapper.get('[data-test="recap-all-videos"]').text()).toBe('查看全部 15 部');
    expect(wrapper.get('[data-test="recap-all-videos"]').classes()).toEqual(expect.arrayContaining(['btn', 'btn--secondary', 'btn--sm']));
    expect(wrapper.get('[data-test="recap-all-tags"]').text()).toBe('查看全部 12 个');
    expect(wrapper.get('[data-test="recap-all-new"]').text()).toBe('查看全部 137 部');
    for (const kind of ['images', 'people', 'collections', 'feed']) expect(wrapper.find(`[data-test="recap-all-${kind}"]`).exists()).toBe(false);
    // 有计数但样本全都不在库中：空行与按钮同时出现。
    await wrapper.get('[data-test="recap-fresh-rewatched"]').trigger('click');
    expect(wrapper.get('[data-test="recap-fresh-empty"]').text()).toBe('这 4 部都已不在库中，记录仍然保留。');
    expect(wrapper.get('[data-test="recap-all-rewatched"]').text()).toBe('查看全部 4 部');
    // 计数与样本一样多（重温换成新收藏：0 个）就没有按钮。
    await wrapper.get('[data-test="recap-fresh-favorites"]').trigger('click');
    expect(wrapper.find('[data-test^="recap-all-favorites"]').exists()).toBe(false);
  });

  it('keeps the Top and image sections (with 查看全部) when everything watched this period is gone', async () => {
    api.GetRecap.mockResolvedValue(recap({ overview: { ...recap().overview, videos: 3, images: 2 }, top_videos: [], top_images: [] }));
    const wrapper = await mountPage();
    expect(wrapper.get('[data-test="recap-top-videos-gone"]').text()).toBe('这 3 部都已不在库中，记录仍然保留。');
    expect(wrapper.get('[data-test="recap-all-videos"]').text()).toBe('查看全部 3 部');
    expect(wrapper.find('[data-test="recap-top-videos"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="recap-top-images-gone"]').text()).toBe('这 2 张都已不在库中，记录仍然保留。');
    expect(wrapper.get('[data-test="recap-all-images"]').text()).toBe('查看全部 2 张');
  });

  it('opens the full list for the displayed period and closes it when an item opens', async () => {
    api.GetRecap.mockResolvedValue(recap({ overview: { ...recap().overview, videos: 15 } }));
    api.ListRecapItems.mockResolvedValue({ kind: 'videos', offset: 0, total: 15, has_more: false, videos: [], image_ranks: [], favorites: [], diary: [], feed: [],
      tags: [], entities: [], video_ranks: [{ video: video(7), count: 1, last_at: '2026-10-03T21:00:00+08:00' }] });
    const wrapper = await mountPage();
    await wrapper.get('[data-test="recap-all-videos"]').trigger('click');
    await flushPromises();
    expect(api.ListRecapItems).toHaveBeenCalledWith({ period: 'month', year: thisMonth.year, month: thisMonth.month, kind: 'videos', offset: 0, limit: 60 });
    expect(wrapper.get('[data-test="recap-list-dialog"]').text()).toContain('本期看过的视频（15 部）');
    await wrapper.get('[data-test="recap-list-item"]').trigger('click');
    expect(wrapper.emitted('open-video-id')).toEqual([[7]]);
    expect(wrapper.find('[data-test="recap-list-dialog"]').exists()).toBe(false);
  });
});
