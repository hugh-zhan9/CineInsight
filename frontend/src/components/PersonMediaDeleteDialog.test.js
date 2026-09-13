import { mount, flushPromises } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';
const api = vi.hoisted(() => ({ DeleteVideo: vi.fn(), DeleteImage: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);
import PersonMediaDeleteDialog from './PersonMediaDeleteDialog.vue';
const target = { kind: 'image', personID: 7, media: { id: 11, name: 'photo.jpg', path: '/photos/photo.jpg', size: 1024 } };
const create = () => mount(PersonMediaDeleteDialog, { props: { target }, global: { stubs: { teleport: true } } });
beforeEach(() => vi.resetAllMocks());
describe('PersonMediaDeleteDialog', () => {
  it('cancels without calling a deletion API', async () => {
    const w = create();
    expect(w.text()).toContain('/photos/photo.jpg');
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' })); await flushPromises();
    expect(w.emitted('close')).toHaveLength(1);
    expect(api.DeleteImage).not.toHaveBeenCalled(); expect(api.DeleteVideo).not.toHaveBeenCalled(); w.unmount();
  });
  it('keeps the dialog on failure, prevents duplicate submissions and allows retry', async () => {
    let reject;
    api.DeleteImage.mockImplementationOnce(() => new Promise((_, fail) => { reject = fail; })).mockResolvedValueOnce();
    const w = create();
    await w.get('[data-test="person-media-delete-confirm"]').trigger('click');
    await w.vm.confirm();
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    expect(api.DeleteImage).toHaveBeenCalledTimes(1); expect(w.emitted('close')).toBeUndefined();
    reject('磁盘不可用'); await flushPromises();
    expect(w.get('[role="alert"]').text()).toContain('磁盘不可用'); expect(w.emitted('deleted')).toBeUndefined();
    await w.get('[data-test="person-media-delete-file"]').setValue(true);
    await w.get('[data-test="person-media-delete-confirm"]').trigger('click'); await flushPromises();
    expect(api.DeleteImage).toHaveBeenLastCalledWith(11, true);
    expect(w.emitted('deleted')).toEqual([[target]]); expect(w.emitted('close')).toHaveLength(1); w.unmount();
  });
});
