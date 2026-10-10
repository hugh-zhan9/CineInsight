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
  });
  it('history prefills only a title and saving a manual entry returns to its own year', async () => {
    api.ListUnconfirmedPlaybackHistory.mockResolvedValue({ items: [{ id: 7, video_id: 5, title: '过去的播放', played_at: '2020-01-01T00:00:00Z', source: 'desktop_play' }] });
    const wrapper = shallowMount(ViewingNotesPage); await wrapper.get('[data-test="notes-tab-history"]').trigger('click'); await flushPromises();
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
