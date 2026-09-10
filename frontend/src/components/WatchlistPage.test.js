import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'CreateWatchlistEntry', 'UpdateWatchlistEntry', 'DeleteWatchlistEntry', 'ListWatchlist'
].map(name => [name, vi.fn()])));
const feedback = vi.hoisted(() => ({ confirmAction: vi.fn(), notifySuccess: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);
vi.mock('../utils/feedback.js', () => feedback);

import WatchlistPage from './WatchlistPage.vue';

const entry = (id, title) => ({ id, title, created_at: '2026-09-10T10:00:00+08:00' });
const wrappers = [];
const deferred = () => {
  let resolve;
  const promise = new Promise(done => { resolve = done; });
  return { promise, resolve };
};

beforeEach(() => {
  vi.resetAllMocks();
  api.ListWatchlist.mockResolvedValue({ entries: [], next_id: 0 });
  api.CreateWatchlistEntry.mockResolvedValue(entry(1, '沙丘'));
  feedback.confirmAction.mockResolvedValue(true);
});
afterEach(() => { while (wrappers.length) wrappers.pop().unmount(); });

async function page() {
  const wrapper = mount(WatchlistPage);
  wrappers.push(wrapper);
  await flushPromises();
  return wrapper;
}
const find = (wrapper, name) => wrapper.get(`[data-test="watchlist-${name}"]`);

describe('想看片单', () => {
  it('空片单提示添加，空白不提交，填写片名即可添加并重读', async () => {
    const wrapper = await page();
    expect(find(wrapper, 'empty').text()).toContain('还没有');
    await find(wrapper, 'title').setValue('   ');
    await find(wrapper, 'form').trigger('submit');
    expect(api.CreateWatchlistEntry).not.toHaveBeenCalled();
    api.ListWatchlist.mockResolvedValue({ entries: [entry(1, '沙丘')], next_id: 0 });
    await find(wrapper, 'title').setValue(' 沙丘 ');
    await find(wrapper, 'form').trigger('submit');
    await flushPromises();
    expect(api.CreateWatchlistEntry).toHaveBeenCalledWith('沙丘');
    expect(find(wrapper, 'title').element.value).toBe('');
    expect(find(wrapper, 'entry').text()).toContain('沙丘');
  });

  it('保存期间不重复提交，失败保留草稿与已有记录', async () => {
    api.ListWatchlist.mockResolvedValue({ entries: [entry(1, '原片名')], next_id: 0 });
    const wrapper = await page();
    const pending = deferred();
    api.CreateWatchlistEntry.mockReturnValue(pending.promise);
    await find(wrapper, 'title').setValue('新片名');
    await find(wrapper, 'form').trigger('submit');
    await find(wrapper, 'form').trigger('submit');
    expect(api.CreateWatchlistEntry).toHaveBeenCalledTimes(1);
    pending.resolve(entry(2, '新片名'));
    await flushPromises();
    api.CreateWatchlistEntry.mockRejectedValue(new Error('数据库断开'));
    await find(wrapper, 'title').setValue('保留草稿');
    await find(wrapper, 'form').trigger('submit');
    await flushPromises();
    expect(find(wrapper, 'title').element.value).toBe('保留草稿');
    expect(find(wrapper, 'entry').text()).toContain('原片名');
    expect(find(wrapper, 'error').text()).toContain('数据库断开');
  });

  it('按 ID 改名，取消编辑不写入，修改失败保留输入', async () => {
    api.ListWatchlist.mockResolvedValue({ entries: [entry(3, '原片名')], next_id: 0 });
    const wrapper = await page();
    await find(wrapper, 'edit-3').trigger('click');
    await find(wrapper, 'cancel').trigger('click');
    expect(api.UpdateWatchlistEntry).not.toHaveBeenCalled();
    await find(wrapper, 'edit-3').trigger('click');
    await find(wrapper, 'title').setValue('改后的片名');
    api.UpdateWatchlistEntry.mockRejectedValueOnce(new Error('写入失败'));
    await find(wrapper, 'form').trigger('submit');
    await flushPromises();
    expect(find(wrapper, 'title').element.value).toBe('改后的片名');
    api.ListWatchlist.mockResolvedValue({ entries: [entry(3, '改后的片名')], next_id: 0 });
    await find(wrapper, 'form').trigger('submit');
    await flushPromises();
    expect(api.UpdateWatchlistEntry).toHaveBeenLastCalledWith(3, '改后的片名');
    expect(api.CreateWatchlistEntry).not.toHaveBeenCalled();
    expect(find(wrapper, 'entry').text()).toContain('改后的片名');
  });

  it('移除需确认，失败保留条目，成功只移除指定 ID', async () => {
    api.ListWatchlist.mockResolvedValue({ entries: [entry(2, '沙丘'), entry(1, '沙丘')], next_id: 0 });
    const wrapper = await page();
    feedback.confirmAction.mockResolvedValueOnce(false);
    await find(wrapper, 'remove-2').trigger('click');
    await flushPromises();
    expect(api.DeleteWatchlistEntry).not.toHaveBeenCalled();
    api.DeleteWatchlistEntry.mockRejectedValueOnce(new Error('移除失败'));
    await find(wrapper, 'remove-2').trigger('click');
    await flushPromises();
    expect(wrapper.findAll('[data-test="watchlist-entry"]')).toHaveLength(2);
    await find(wrapper, 'remove-2').trigger('click');
    await flushPromises();
    expect(api.DeleteWatchlistEntry).toHaveBeenLastCalledWith(2);
    expect(wrapper.findAll('[data-test="watchlist-entry"]')).toHaveLength(1);
    expect(find(wrapper, 'remove-1').exists()).toBe(true);
  });

  it('搜索丢弃旧响应，翻页按当前词追加，失败保留已加载页', async () => {
    const wrapper = await page();
    const stale = deferred();
    api.ListWatchlist.mockReturnValueOnce(stale.promise);
    await find(wrapper, 'search').setValue('旧词');
    api.ListWatchlist.mockResolvedValueOnce({ entries: [entry(9, '沙丘')], next_id: 9 });
    await find(wrapper, 'search').setValue('沙丘');
    await flushPromises();
    stale.resolve({ entries: [entry(10, '旧词结果')], next_id: 10 });
    await flushPromises();
    expect(find(wrapper, 'entry').text()).toContain('沙丘');
    api.ListWatchlist.mockRejectedValueOnce(new Error('翻页失败'));
    await find(wrapper, 'more').trigger('click');
    await flushPromises();
    expect(find(wrapper, 'entry').text()).toContain('沙丘');
    expect(find(wrapper, 'error').text()).toContain('翻页失败');
    api.ListWatchlist.mockResolvedValueOnce({ entries: [entry(3, '沙丘（1984）')], next_id: 0 });
    await find(wrapper, 'more').trigger('click');
    await flushPromises();
    expect(api.ListWatchlist).toHaveBeenLastCalledWith('沙丘', 9, 50);
    expect(wrapper.findAll('[data-test="watchlist-entry"]')).toHaveLength(2);
    expect(wrapper.find('[data-test="watchlist-more"]').exists()).toBe(false);
  });

  it('移除当前页最后一条后加载剩余页，不误报片单为空', async () => {
    api.ListWatchlist.mockResolvedValueOnce({ entries: [entry(5, '第一页')], next_id: 5 });
    const wrapper = await page();
    api.ListWatchlist.mockResolvedValueOnce({ entries: [entry(2, '下一页')], next_id: 0 });
    await find(wrapper, 'remove-5').trigger('click');
    await flushPromises();
    expect(api.ListWatchlist).toHaveBeenLastCalledWith('', 5, 50);
    expect(find(wrapper, 'entry').text()).toContain('下一页');
    expect(wrapper.find('[data-test="watchlist-empty"]').exists()).toBe(false);
  });

  it('读取失败不显示空片单，200 字符边界按 Unicode 计数', async () => {
    api.ListWatchlist.mockRejectedValueOnce(new Error('读取失败'));
    const wrapper = await page();
    expect(wrapper.find('[data-test="watchlist-empty"]').exists()).toBe(false);
    expect(find(wrapper, 'error').text()).toContain('读取失败');
    await find(wrapper, 'title').setValue('影'.repeat(201));
    await find(wrapper, 'form').trigger('submit');
    expect(api.CreateWatchlistEntry).not.toHaveBeenCalled();
    await find(wrapper, 'title').setValue('🎬'.repeat(200));
    await find(wrapper, 'form').trigger('submit');
    await flushPromises();
    expect(api.CreateWatchlistEntry).toHaveBeenCalledWith('🎬'.repeat(200));
  });
});
