import { flushPromises, mount, enableAutoUnmount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ ListRecapItems: vi.fn() }));
vi.mock('../../../wailsjs/go/main/App', () => api);
import RecapListDialog, { RECAP_LIST_PAGE } from './RecapListDialog.vue';

enableAutoUnmount(afterEach);

const month = { period: 'month', year: 2026, month: 10 };
const video = (id, extra = {}) => ({ id, title: `影片${id}`, name: `v${id}.mp4`, duration: 600, personal_rating: null, available: true, ...extra });
const emptyPage = { videos: [], video_ranks: [], image_ranks: [], favorites: [], diary: [], feed: [], tags: [], entities: [] };
const page = (kind, overrides) => ({ ...emptyPage, kind, offset: 0, total: 0, has_more: false, ...overrides });

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((ok, fail) => { resolve = ok; reject = fail; });
  return { promise, resolve, reject };
}

function mountDialog(props = {}) {
  return mount(RecapListDialog, { props: { kind: 'new', request: month, total: 137, ...props } });
}

beforeEach(() => {
  api.ListRecapItems.mockReset();
});

describe('RecapListDialog', () => {
  it('pages through the full list with 加载更多 and titles it with the total', async () => {
    const first = Array.from({ length: RECAP_LIST_PAGE }, (_, index) => video(index + 1));
    const second = deferred();
    api.ListRecapItems.mockResolvedValueOnce(page('new', { total: 62, has_more: true, videos: first })).mockReturnValueOnce(second.promise);
    const wrapper = mountDialog();
    await wrapper.vm.$nextTick();
    expect(wrapper.find('[data-test="recap-list-loading"]').exists()).toBe(true);
    expect(wrapper.get('.ui-dialog__title').text()).toBe('本期新发现（137 部）');
    await flushPromises();
    expect(api.ListRecapItems).toHaveBeenCalledWith({ ...month, kind: 'new', offset: 0, limit: RECAP_LIST_PAGE });
    expect(wrapper.get('.ui-dialog__title').text()).toBe('本期新发现（62 部）');
    expect(wrapper.findAll('[data-test="recap-list-item"]')).toHaveLength(60);
    const more = wrapper.get('[data-test="recap-list-more"]');
    expect(more.classes()).toEqual(expect.arrayContaining(['btn', 'btn--secondary', 'btn--sm']));
    expect(more.text()).toBe('加载更多');
    await more.trigger('click');
    expect(wrapper.get('[data-test="recap-list-more"]').text()).toBe('正在加载…');
    expect(wrapper.get('[data-test="recap-list-more"]').attributes('disabled')).toBeDefined();
    second.resolve(page('new', { offset: 60, total: 62, has_more: false, videos: [video(61), video(62, { available: false })] }));
    await flushPromises();
    expect(api.ListRecapItems).toHaveBeenLastCalledWith({ ...month, kind: 'new', offset: 60, limit: RECAP_LIST_PAGE });
    const items = wrapper.findAll('[data-test="recap-list-item"]');
    expect(items).toHaveLength(62);
    expect(items[61].text()).toContain('已不在库中');
    expect(wrapper.find('[data-test="recap-list-more"]').exists()).toBe(false);
  });

  it('drops a late response from an earlier request', async () => {
    const stale = deferred();
    const fresh = deferred();
    api.ListRecapItems.mockReturnValueOnce(stale.promise).mockReturnValueOnce(fresh.promise);
    const wrapper = mountDialog();
    await wrapper.setProps({ kind: 'rewatched' });
    fresh.resolve(page('rewatched', { total: 1, videos: [video(9)] }));
    await flushPromises();
    stale.resolve(page('new', { total: 2, videos: [video(1), video(2)] }));
    await flushPromises();
    expect(api.ListRecapItems).toHaveBeenLastCalledWith({ ...month, kind: 'rewatched', offset: 0, limit: RECAP_LIST_PAGE });
    expect(wrapper.findAll('[data-test="recap-list-item"]').map(item => item.text())).toEqual(['影片9']);
    expect(wrapper.get('.ui-dialog__title').text()).toBe('本期重温（1 部）');
  });

  it('keeps items no longer in the library greyed and unclickable; a click hands the item to the page', async () => {
    api.ListRecapItems.mockResolvedValueOnce(page('videos', { total: 2, video_ranks: [
      { video: video(1), count: 3, last_at: '2026-10-05T21:00:00+08:00' },
      { video: video(2, { title: '', available: false }), count: 1, last_at: '2026-10-02T21:00:00+08:00' }
    ] }));
    const wrapper = mountDialog({ kind: 'videos', total: 2 });
    await flushPromises();
    const [first, gone] = wrapper.findAll('[data-test="recap-list-item"]');
    expect(first.text()).toContain('1');
    expect(first.text()).toContain('3 次');
    expect(gone.attributes('disabled')).toBeDefined();
    expect(gone.classes()).toContain('recap-card--unavailable');
    expect(gone.text()).toContain('已删除的视频');
    expect(gone.text()).toContain('已不在库中');
    await gone.trigger('click');
    wrapper.vm.pick({ action: 'video', available: false, payload: video(2, { available: false }) });
    expect(wrapper.emitted('pick')).toBeUndefined();
    await first.trigger('click');
    expect(wrapper.emitted('pick')).toEqual([[{ action: 'video', payload: video(1) }]]);
  });

  it('renders rankings, diary and the feed as compact rows', async () => {
    const tag = { id: 5, name: '科幻', color: '#336699' };
    api.ListRecapItems
      .mockResolvedValueOnce(page('tags', { total: 1, tags: [{ tag, weight: 4, videos: 2 }] }))
      .mockResolvedValueOnce(page('diary', { total: 1, diary: [{ id: 3, title: '旧片', rating: 8, watched_on: '2026-10-03', video_id: 7, video_available: false }] }))
      .mockResolvedValueOnce(page('feed', { total: 2, feed: [
        { kind: 'video', id: 9, title: '', source: 'desktop_play', at: '2026-10-11T09:00:00+08:00', available: false },
        { kind: 'image', id: 4, title: 'p4.jpg', source: 'desktop_viewer', at: '2026-10-11T08:00:00+08:00', available: true }
      ] }));
    const wrapper = mountDialog({ kind: 'tags', total: 1 });
    await flushPromises();
    const row = wrapper.get('[data-test="recap-list-item"]');
    expect(row.text()).toContain('科幻');
    expect(row.text()).toContain('4 次播放 · 2 部');
    await row.trigger('click');
    expect(wrapper.emitted('pick')[0]).toEqual([{ action: 'tag', payload: tag }]);

    await wrapper.setProps({ kind: 'diary' });
    await flushPromises();
    expect(wrapper.get('[data-test="recap-list-item"]').text()).toContain('8.0 分');
    expect(wrapper.get('[data-test="recap-list-item"]').text()).toContain('关联的视频已不在库中');

    await wrapper.setProps({ kind: 'feed', request: { period: 'live', year: 2026, month: 10 } });
    await flushPromises();
    expect(wrapper.get('.ui-dialog__title').text()).toBe('今日流水（2 条）');
    const [gone, viewed] = wrapper.findAll('.recap-feed__open');
    expect(gone.attributes('disabled')).toBeDefined();
    expect(gone.text()).toContain('已不在库中');
    await viewed.trigger('click');
    expect(wrapper.emitted('pick')[1][0]).toMatchObject({ action: 'feed', payload: { kind: 'image', id: 4 } });
  });

  it('shows an error banner with 重试, and an empty state', async () => {
    api.ListRecapItems.mockRejectedValueOnce(new Error('数据库忙')).mockResolvedValueOnce(page('people', { total: 0 }));
    const wrapper = mountDialog({ kind: 'people', total: 7 });
    await flushPromises();
    expect(wrapper.get('[data-test="recap-list-error"]').text()).toContain('读取列表失败：数据库忙');
    await wrapper.get('[data-test="recap-list-retry"]').trigger('click');
    await flushPromises();
    expect(api.ListRecapItems).toHaveBeenCalledTimes(2);
    expect(wrapper.find('[data-test="recap-list-error"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="recap-list-empty"]').text()).toBe('这一期没有可列出的人物排行。');
  });
});

it('loads a selected day, renders exact times, switches days and ignores stale responses', async () => {
  const stale = deferred();
  api.ListRecapItems.mockReturnValueOnce(stale.promise).mockResolvedValueOnce(page('feed', { total: 0, feed: [] }));
  const wrapper = mountDialog({ kind: 'feed', request: { ...month, date: '2026-10-08' }, total: 2 });
  await flushPromises();
  expect(api.ListRecapItems).toHaveBeenCalledWith({ ...month, date: '2026-10-08', kind: 'feed', offset: 0, limit: RECAP_LIST_PAGE });
  expect(wrapper.text()).toContain('2026-10-08 的观看记录');
  await wrapper.setProps({ request: { ...month, date: '2026-10-09' } });
  await flushPromises();
  expect(wrapper.text()).toContain('这一天没有观看记录');
  stale.resolve(page('feed', { total: 1, feed: [{ kind: 'video', id: 1, title: '旧日期视频', available: true, at: '2026-10-08T12:00:00+08:00' }] }));
  await flushPromises();
  expect(wrapper.text()).not.toContain('旧日期视频');
  expect(wrapper.text()).toContain('2026-10-09 的观看记录（0 条）');
});
