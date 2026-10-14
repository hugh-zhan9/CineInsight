import { flushPromises, shallowMount, enableAutoUnmount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
const api = vi.hoisted(() => ({ ListViewingDiary: vi.fn(), DeleteViewingDiary: vi.fn(), GetViewingYearReview: vi.fn(), ListUnconfirmedPlaybackHistory: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);
vi.mock('../utils/feedback.js', () => ({ confirmAction: vi.fn().mockResolvedValue(true) }));
import ViewingNotesPage from './ViewingNotesPage.vue';
import DiaryEditor from './DiaryEditor.vue';
enableAutoUnmount(afterEach);
const item = (id, date) => ({ id, watched_on: date, title: `影片${id}`, origin: 'manual', revision: 1, rating: null, note: '' });
beforeEach(() => { Object.values(api).forEach(mock => mock.mockReset()); api.ListViewingDiary.mockResolvedValue({ items: [], has_more: false }); api.ListUnconfirmedPlaybackHistory.mockResolvedValue({ items: [] }); api.GetViewingYearReview.mockResolvedValue({ year: 2024, total: 0, days: 0, months: Array(12).fill(0), average_rating: null, most_watched: [] }); });
describe('ViewingNotesPage', () => {
  it('groups each viewing by date, keeps repeat entries, and uses a composite page cursor', async () => {
    api.ListViewingDiary.mockResolvedValueOnce({ items: [item(3, '2024-03-01'), item(2, '2024-03-01'), item(1, '2024-02-29')], has_more: true });
    const wrapper = shallowMount(ViewingNotesPage); await flushPromises();
    expect(wrapper.findAll('[data-test="diary-row"]')).toHaveLength(3); expect(wrapper.findAll('h3').map(h => h.text())).toEqual(['2024-03-01', '2024-02-29']);
    await wrapper.vm.next(); expect(api.ListViewingDiary).toHaveBeenLastCalledWith(expect.objectContaining({ cursor_id: 1, cursor_date: '2024-02-29' }));
    expect(wrapper.text()).toContain('平均分暂无');
  });
  it('never lets old-year data overwrite a new query, and exposes failures rather than empty success', async () => {
    let old; api.ListViewingDiary.mockImplementationOnce(() => new Promise(resolve => old = resolve));
    const wrapper = shallowMount(ViewingNotesPage); api.ListViewingDiary.mockResolvedValueOnce({ items: [item(9, '2023-01-01')] }); await wrapper.setData({ year: 2023 }); await flushPromises();
    old({ items: [item(1, '2024-01-01')] }); await flushPromises(); expect(wrapper.text()).toContain('影片9'); expect(wrapper.text()).not.toContain('影片1');
    api.ListViewingDiary.mockRejectedValueOnce(new Error('unavailable')); await wrapper.vm.reset(); await flushPromises(); expect(wrapper.text()).toContain('unavailable'); expect(wrapper.text()).not.toContain('当前条件没有记录');
    // 空态是 EmptyState（统一设计 D-UD-06，浅渲染下是桩、不出文字）：读失败时它也不能出现。
    expect(wrapper.find('[data-test="notes-empty"]').exists()).toBe(false);
  });
  it('history prefills only a title and saving a manual entry returns to its own year', async () => {
    api.ListUnconfirmedPlaybackHistory.mockResolvedValue({ items: [{ id: 7, video_id: 5, title: '过去的播放', played_at: '2020-01-01T00:00:00Z', source: 'desktop_play' }] });
    // 分区页签是 PageHeader below 插槽里的 AppTabs（D-UD-06）：两者都不桩掉才点得到。
    const wrapper = shallowMount(ViewingNotesPage, { global: { stubs: { PageHeader: false, AppTabs: false } } });
    await wrapper.get('[data-test="notes-tab-history"]').trigger('click'); await flushPromises();
    expect(wrapper.get('[data-test="notes-tab-history"]').attributes('aria-selected')).toBe('true');
    const button = wrapper.findAll('[data-test="playback-history-row"] button').find(b => b.text() === '手动补记'); await button.trigger('click');
    expect(wrapper.getComponent(DiaryEditor).props('entry')).toEqual({ title: '过去的播放' });
    wrapper.getComponent(DiaryEditor).vm.$emit('saved', { id: 88, watched_on: '2022-10-10' }); await flushPromises();
    expect(wrapper.vm.tab).toBe('diary'); expect(wrapper.vm.year).toBe(2022);
  });
});

it('displays missing and Go-zero historical timestamps as unknown without inventing a calendar date', async () => {
  const wrapper = shallowMount(ViewingNotesPage); await flushPromises();
  expect(wrapper.vm.formatDate(null)).toBe('日期未知'); expect(wrapper.vm.formatDate('0001-01-01T00:00:00Z')).toBe('日期未知');
});

it('does not commit a failed page transition to its previous-page history', async () => {
  api.ListViewingDiary.mockResolvedValueOnce({ items: [item(51, '2024-01-01')], has_more: true }).mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce({ items: [item(25, '2024-01-01')], has_more: true }).mockResolvedValueOnce({ items: [item(51, '2024-01-01')], has_more: true });
  const wrapper = shallowMount(ViewingNotesPage); await flushPromises(); await wrapper.vm.next(); await flushPromises(); await wrapper.vm.next(); await flushPromises(); await wrapper.vm.previous(); await flushPromises();
  expect(api.ListViewingDiary.mock.calls.map(([q]) => q.cursor_id)).toEqual([0, 51, 51, 0]);
});

it('reactivation during pending pagination reads only its last committed cursor', async () => {
  let finish; api.ListViewingDiary.mockResolvedValueOnce({ items: [item(51, '2024-01-01')], has_more: true }).mockImplementationOnce(() => new Promise(resolve => finish = resolve)).mockImplementationOnce(q => Promise.resolve({ items: [item(q.cursor_id === 51 ? 25 : 51, '2024-01-01')], has_more: true }));
  const wrapper = shallowMount(ViewingNotesPage); await flushPromises(); const pending = wrapper.vm.next();
  await wrapper.setProps({ pageActive: false }); await wrapper.setProps({ pageActive: true }); await flushPromises(); finish({ items: [item(25, '2024-01-01')], has_more: true }); await pending;
  expect(wrapper.vm.items[0].id).toBe(51); expect(wrapper.vm.cursor).toBeNull(); expect(wrapper.vm.cursors).toEqual([]);
});

// 两种页签行（独立页的页头里、嵌在片单页时的一行里）都和面板互相指向：role=tabpanel、id、aria-labelledby 对上
// 页签的 id 与 aria-controls；← → 切分区时面板跟着换。
it('分区页签与面板互相指向，独立与嵌入两种写法都一样，← → 切换后面板跟着换', async () => {
  for (const embedded of [false, true]) {
    const wrapper = shallowMount(ViewingNotesPage, { props: { embedded }, global: { stubs: { PageHeader: false, AppTabs: false } } });
    await flushPromises();
    for (const [key, next] of [['diary', 'ArrowRight'], ['bookmarks', 'ArrowRight'], ['history', null]]) {
      const panel = wrapper.get('[role="tabpanel"]');
      expect(panel.attributes('id'), `${embedded} ${key}`).toBe(`viewing-notes-panel-${key}`);
      expect(panel.attributes('aria-labelledby')).toBe(`viewing-notes-tab-${key}`);
      expect(wrapper.get(`#viewing-notes-tab-${key}`).attributes('aria-controls')).toBe(`viewing-notes-panel-${key}`);
      expect(wrapper.get(`#viewing-notes-tab-${key}`).attributes('aria-selected')).toBe('true');
      if (key === 'bookmarks') expect(panel.find('bookmark-panel-stub').exists()).toBe(true);
      else expect(panel.find('.notes-pager').exists()).toBe(true);
      if (next) { await wrapper.get('[role="tablist"]').trigger('keydown', { key: next }); await flushPromises(); }
    }
    wrapper.unmount();
  }
});

it('embedded（嵌在片单页里）不显示自己的页头，分区页签与「手动补记」留在本页一行里', async () => {
  const wrapper = shallowMount(ViewingNotesPage, { props: { embedded: true }, global: { stubs: { AppTabs: false } } });
  await flushPromises();
  expect(wrapper.findComponent({ name: 'PageHeader' }).exists()).toBe(false);
  expect(wrapper.classes()).not.toContain('page-content');
  await wrapper.get('[data-test="notes-tab-bookmarks"]').trigger('click');
  expect(wrapper.vm.tab).toBe('bookmarks');
  await wrapper.get('[data-test="diary-add"]').trigger('click');
  expect(wrapper.getComponent(DiaryEditor).props('entry')).toEqual({});
  const standalone = shallowMount(ViewingNotesPage);
  await flushPromises();
  expect(standalone.findComponent({ name: 'PageHeader' }).exists()).toBe(true);
  expect(standalone.find('.notes-embedded-bar').exists()).toBe(false);
});
