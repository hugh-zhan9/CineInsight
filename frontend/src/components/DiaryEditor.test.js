import { flushPromises, mount, enableAutoUnmount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
const api = vi.hoisted(() => ({ SaveViewingDiary: vi.fn(), SearchLibraryVideoPage: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);
import DiaryEditor from './DiaryEditor.vue';
enableAutoUnmount(afterEach);
beforeEach(() => { Object.values(api).forEach(mock => mock.mockReset()); api.SaveViewingDiary.mockResolvedValue({ id: 1, watched_on: '2024-02-29' }); api.SearchLibraryVideoPage.mockResolvedValue({ videos: [{ id: 9, name: '片库视频' }] }); });
describe('DiaryEditor', () => {
  it('requires the user to supply a viewing date even when prefilling a historical title', async () => {
    const wrapper = mount(DiaryEditor, { props: { entry: { title: '片库外观看' } } });
    expect(wrapper.get('[data-test="diary-date"]').element.value).toBe('');
    await wrapper.get('form').trigger('submit'); expect(api.SaveViewingDiary).not.toHaveBeenCalled();
    await wrapper.get('[data-test="diary-date"]').setValue('2024-02-29'); await wrapper.get('[data-test="diary-rating"]').setValue('8.5'); await wrapper.get('form').trigger('submit'); await flushPromises();
    expect(api.SaveViewingDiary).toHaveBeenCalledWith(expect.objectContaining({ title: '片库外观看', watched_on: '2024-02-29', video_id: null, rating: 8.5 }));
    expect(wrapper.emitted('saved')).toHaveLength(1); expect(api.SearchLibraryVideoPage).not.toHaveBeenCalled();
  });
  it('selects a library video through the existing paged library contract', async () => {
    const wrapper = mount(DiaryEditor); await wrapper.get('[data-test="diary-source"]').setValue('library');
    await wrapper.vm.search(false); await wrapper.get('.diary-video-choice').trigger('click');
    await wrapper.get('[data-test="diary-date"]').setValue('2024-02-29'); await wrapper.get('form').trigger('submit'); await flushPromises();
    expect(api.SearchLibraryVideoPage).toHaveBeenCalledWith(expect.objectContaining({ filter: expect.objectContaining({ sort_mode: 'balanced' }), limit: 20 }));
    expect(api.SaveViewingDiary).toHaveBeenCalledWith(expect.objectContaining({ video_id: 9, rating: null }));
  });
  it('keeps immutable linkage and optimistic revision, with a recoverable save error', async () => {
    api.SaveViewingDiary.mockRejectedValue(new Error('viewing_note_conflict'));
    const wrapper = mount(DiaryEditor, { props: { entry: { id: 12, revision: 4, video_id: 2, origin: 'automatic', title: '原片', watched_on: '2024-02-29', rating: null } } });
    expect(wrapper.find('[data-test="diary-source"]').exists()).toBe(false);
    await wrapper.get('[data-test="diary-note"]').setValue('重看笔记'); await wrapper.get('form').trigger('submit'); await flushPromises();
    expect(api.SaveViewingDiary).toHaveBeenCalledWith(expect.objectContaining({ id: 12, revision: 4, video_id: 2, note: '重看笔记' }));
    expect(wrapper.text()).toContain('viewing_note_conflict'); expect(wrapper.emitted('saved')).toBeUndefined();
  });
});

it('locks the full edit form during save, and hidden-page Escape cannot discard its draft', async () => {
  let complete; api.SaveViewingDiary.mockImplementationOnce(() => new Promise(resolve => complete = resolve));
  const wrapper = mount(DiaryEditor, { props: { entry: { title: '草稿', watched_on: '2024-02-29' } } });
  await wrapper.setProps({ active: false }); await wrapper.vm.close(); expect(wrapper.emitted('close')).toBeUndefined();
  await wrapper.setProps({ active: true }); await wrapper.get('form').trigger('submit');
  for (const name of ['title', 'date', 'rating', 'note']) expect(wrapper.get(`[data-test="diary-${name}"]`).element.matches(':disabled')).toBe(true);
  complete({ id: 1, watched_on: '2024-02-29' }); await flushPromises(); expect(wrapper.emitted('saved')).toHaveLength(1);
});
