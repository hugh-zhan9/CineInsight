import { flushPromises, mount, enableAutoUnmount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
const api = vi.hoisted(() => ({ ListVideoBookmarks: vi.fn(), SaveVideoBookmark: vi.fn(), DeleteVideoBookmark: vi.fn(), ResolveVideoBookmark: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);
vi.mock('../utils/feedback.js', () => ({ confirmAction: vi.fn().mockResolvedValue(true) }));
import BookmarkPanel from './BookmarkPanel.vue';
enableAutoUnmount(afterEach);
const row = (id = 1) => ({ id, revision: 3, video_id: 7, video_title: '原片', title: `笔记${id}`, note: '海边', tags: ['风景'], start_ms: 0, end_ms: 3000, source_available: true });
beforeEach(() => { Object.values(api).forEach(mock => mock.mockReset()); api.ListVideoBookmarks.mockResolvedValue({ items: [], has_more: false }); api.SaveVideoBookmark.mockResolvedValue(row()); });
describe('BookmarkPanel', () => {
  it('captures the preview source once and saves zero/range without using a refreshed token', async () => {
    const wrapper = mount(BookmarkPanel, { props: { video: { id: 7 }, session: { source_version: 'old' }, currentPosition: () => 0 } }); await flushPromises();
    await wrapper.get('[data-test="bookmark-add"]').trigger('click');
    await wrapper.setProps({ session: { source_version: 'new' } });
    await wrapper.get('[data-test="bookmark-title"]').setValue('起点');
    await wrapper.get('[data-test="bookmark-end"]').setValue('3.001');
    await wrapper.get('form').trigger('submit'); await flushPromises();
    expect(api.SaveVideoBookmark).toHaveBeenCalledWith(expect.objectContaining({ video_id: 7, start_ms: 0, end_ms: 3001, source_token: 'old', accept_source_token: '' }));
  });
  it('requires explicit source acceptance and retains stale-revision errors in the draft', async () => {
    api.ResolveVideoBookmark.mockResolvedValue({ status: 'source_changed', bookmark: row(), video: { id: 7 }, source_token: 'checked' });
    api.SaveVideoBookmark.mockRejectedValue(new Error('viewing_note_conflict'));
    const wrapper = mount(BookmarkPanel); await flushPromises(); await wrapper.vm.resolve(row());
    expect(wrapper.text()).toContain('原片内容已改变');
    await wrapper.get('form').trigger('submit'); await flushPromises();
    expect(api.SaveVideoBookmark).toHaveBeenLastCalledWith(expect.objectContaining({ revision: 3, accept_source_token: '' }));
    expect(wrapper.text()).toContain('viewing_note_conflict');
    await wrapper.get('[data-test="bookmark-accept-source"]').setValue(true); await wrapper.get('form').trigger('submit'); await flushPromises();
    expect(api.SaveVideoBookmark).toHaveBeenLastCalledWith(expect.objectContaining({ accept_source_token: 'checked' }));
    expect(wrapper.find('form').exists()).toBe(true);
  });
  it('drops stale query and resolution replies; renders one bounded page with error distinct from empty', async () => {
    let old, resolveOld; api.ListVideoBookmarks.mockImplementationOnce(() => new Promise(resolve => old = resolve));
    const wrapper = mount(BookmarkPanel);
    api.ListVideoBookmarks.mockResolvedValueOnce({ items: [row(9)], has_more: true });
    await wrapper.get('[data-test="bookmark-search"]').setValue('new'); await flushPromises(); old({ items: [row(1)] }); await flushPromises();
    expect(wrapper.findAll('[data-test="bookmark-row"]')).toHaveLength(1); expect(wrapper.text()).toContain('笔记9'); expect(wrapper.text()).not.toContain('笔记1');
    api.ResolveVideoBookmark.mockImplementationOnce(() => new Promise(resolve => resolveOld = resolve)); const pending = wrapper.vm.resolve(row(9));
    wrapper.vm.edit(row(9)); resolveOld({ status: 'ready', video: { id: 7 }, bookmark: row(1) }); await pending;
    expect(wrapper.emitted('open-bookmark')).toBeUndefined();
    api.ListVideoBookmarks.mockRejectedValueOnce(new Error('database unavailable')); await wrapper.vm.next(); await flushPromises();
    expect(wrapper.text()).toContain('database unavailable'); expect(wrapper.text()).not.toContain('还没有片段书签');
  });
  it('rejects fractional milliseconds and deletes only the selected record revision', async () => {
    const wrapper = mount(BookmarkPanel); await flushPromises(); wrapper.vm.edit(row()); await flushPromises();
    await wrapper.get('[data-test="bookmark-start"]').setValue('0.0001'); await wrapper.get('form').trigger('submit');
    expect(api.SaveVideoBookmark).not.toHaveBeenCalled(); expect(wrapper.text()).toContain('毫秒精度');
    await wrapper.vm.remove(row()); expect(api.DeleteVideoBookmark).toHaveBeenCalledWith(1, 3);
  });
});

it('locks all draft inputs while saving and preserves the draft while inspecting changed source', async () => {
  let complete; api.SaveVideoBookmark.mockImplementationOnce(() => new Promise(resolve => complete = resolve));
  const wrapper = mount(BookmarkPanel); await flushPromises();
  wrapper.vm.edit(row(), { status: 'source_changed', video: { id: 7 }, source_token: 'checked' }); await flushPromises();
  const preview = wrapper.findAll('form button').find(button => button.text() === '打开当前原片核对'); await preview.trigger('click');
  expect(wrapper.find('form').exists()).toBe(false); expect(wrapper.emitted('open-video')[0]).toEqual([{ id: 7 }]);
  await wrapper.get('[data-test="bookmark-checking"] button').trigger('click'); expect(wrapper.get('[data-test="bookmark-title"]').element.value).toBe('笔记1');
  await wrapper.get('form').trigger('submit'); expect(wrapper.get('fieldset').element.disabled).toBe(true);
  expect(wrapper.get('[data-test="bookmark-title"]').element.matches(':disabled')).toBe(true);
  expect(wrapper.get('[data-test="bookmark-start"]').element.matches(':disabled')).toBe(true);
  complete(row()); await flushPromises(); expect(wrapper.find('form').exists()).toBe(false);
});

it('refreshes an existing list on return without replacing its suspended source-check draft', async () => {
  api.ListVideoBookmarks.mockResolvedValueOnce({ items: [row(1)] }).mockResolvedValueOnce({ items: [row(2)] });
  const wrapper = mount(BookmarkPanel); await flushPromises(); wrapper.vm.edit(row(1)); wrapper.vm.editorSuspended = true;
  await wrapper.setProps({ pageActive: false }); wrapper.vm.closeEditor(); expect(wrapper.vm.draft.id).toBe(1); await wrapper.setProps({ pageActive: true }); await flushPromises();
  expect(wrapper.text()).toContain('笔记2'); expect(wrapper.vm.draft.id).toBe(1); expect(wrapper.vm.editorSuspended).toBe(true);
});

it('keeps pagination history intact after a failed next page and its retry', async () => {
  api.ListVideoBookmarks.mockResolvedValueOnce({ items: [row(51)], has_more: true }).mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce({ items: [row(25)], has_more: true }).mockResolvedValueOnce({ items: [row(51)], has_more: true });
  const wrapper = mount(BookmarkPanel); await flushPromises(); await wrapper.vm.next(); await flushPromises();
  expect(wrapper.vm.items[0].id).toBe(51); await wrapper.vm.next(); await flushPromises(); await wrapper.vm.previous(); await flushPromises();
  expect(api.ListVideoBookmarks.mock.calls.map(([q]) => q.cursor_id)).toEqual([0, 51, 51, 0]);
});

it('reactivation during an unfinished next page reads the last committed page with consistent history', async () => {
  let finish; api.ListVideoBookmarks.mockResolvedValueOnce({ items: [row(51)], has_more: true }).mockImplementationOnce(() => new Promise(resolve => finish = resolve)).mockImplementationOnce(q => Promise.resolve({ items: [row(q.cursor_id === 51 ? 25 : 51)], has_more: true }));
  const wrapper = mount(BookmarkPanel); await flushPromises(); const pending = wrapper.vm.next();
  await wrapper.setProps({ pageActive: false }); await wrapper.setProps({ pageActive: true }); await flushPromises(); finish({ items: [row(25)], has_more: true }); await pending;
  expect(wrapper.vm.items[0].id).toBe(51); expect(wrapper.vm.cursor).toBe(0); expect(wrapper.vm.cursors).toEqual([]);
});
