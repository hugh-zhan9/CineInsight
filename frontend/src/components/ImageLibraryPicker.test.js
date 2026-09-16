import { mount, flushPromises } from '@vue/test-utils';
import { beforeEach, expect, it, vi } from 'vitest';
const api = vi.hoisted(() => ({ SearchImagePage: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);
import ImageLibraryPicker from './ImageLibraryPicker.vue';
beforeEach(() => vi.clearAllMocks());
const mountPicker = () => mount(ImageLibraryPicker, { global: { stubs: { teleport: true } } });
it('searches, pages with the applied keyword, and selects an image', async () => {
  api.SearchImagePage.mockResolvedValueOnce({ images: [] }).mockResolvedValueOnce({ images: [{ id: 1, name: 'one', path: '/one.png' }], next_cursor: { id: 1 } }).mockResolvedValueOnce({ images: [{ id: 2, name: 'two', path: '/two.png' }] });
  const w = mountPicker(); await flushPromises(); expect(w.text()).toContain('没有符合条件');
  await w.find('input').setValue(' portrait '); await w.find('form').trigger('submit'); await flushPromises();
  await w.find('input').setValue('unsubmitted'); await w.vm.search(false);
  expect(api.SearchImagePage).toHaveBeenLastCalledWith({ filter: { keyword: 'portrait' }, cursor: { id: 1 }, limit: 40 });
  await w.find('[aria-label="使用 two 作为头像"]').trigger('click');
  expect(w.emitted('select')[0][0]).toEqual({ id: 2, name: 'two', path: '/two.png' }); w.unmount();
});
it('ignores a stale search response', async () => {
  let resolveFirst; api.SearchImagePage.mockImplementationOnce(() => new Promise(resolve => { resolveFirst = resolve; })).mockResolvedValueOnce({ images: [{ id: 2, name: 'current' }] });
  const w = mountPicker(); await w.find('input').setValue('new'); await w.find('form').trigger('submit'); await flushPromises();
  resolveFirst({ images: [{ id: 1, name: 'old' }] }); await flushPromises();
  expect(w.vm.images.map(x => x.id)).toEqual([2]); w.unmount();
});
it('allows retry and blocks selection while saving', async () => {
  api.SearchImagePage.mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce({ images: [{ id: 1, name: 'one' }] });
  const w = mountPicker(); await flushPromises(); expect(w.find('[role="alert"]').text()).toContain('offline');
  await w.find('form').trigger('submit'); await flushPromises(); await w.setProps({ busy: true, actionError: 'unsupported' });
  expect(w.find('[aria-label="使用 one 作为头像"]').attributes('disabled')).toBeDefined();
  w.vm.close(); expect(w.emitted('close')).toBeUndefined(); expect(w.text()).toContain('unsupported'); w.unmount();
});
