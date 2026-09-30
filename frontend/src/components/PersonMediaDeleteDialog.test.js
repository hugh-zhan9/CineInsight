import { DOMWrapper, mount, flushPromises } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';
const api = vi.hoisted(() => ({
  DeleteVideo: vi.fn(), DeleteImage: vi.fn(),
  DeleteVideosWithResult: vi.fn(), DeleteImagesWithResult: vi.fn(),
  PermanentlyDeleteVideos: vi.fn(), PermanentlyDeleteImages: vi.fn(),
  RestoreTrashBatch: vi.fn(), CancelBatchDelete: vi.fn()
}));
vi.mock('../../wailsjs/go/main/App', () => api);
vi.mock('../utils/feedback.js', async (importOriginal) => ({ ...(await importOriginal()), notify: vi.fn(), notifyError: vi.fn() }));
vi.mock('./TrashCenterDialog.vue', async (importOriginal) => ({ ...(await importOriginal()), default: { name: 'TrashCenterDialog', template: '<div />' } }));
import PersonMediaDeleteDialog from './PersonMediaDeleteDialog.vue';
const target = { kind: 'image', personID: 7, media: { id: 11, name: 'photo.jpg', path: '/photos/photo.jpg', size: 1024 } };
const create = (props = {}) => mount(PersonMediaDeleteDialog, { props: { target, ...props }, global: { stubs: { teleport: true } } });
const okResult = (ids, batchID = 'b1') => ({ batch_id: batchID, items: ids.map(id => ({ id, code: 'ok' })) });
// 删除之后的撤销条阶段要看真实的 Teleport 渲染（teleport 桩不跟着重渲染），所以这几条直接查 body。
const mountLive = (props = {}) => mount(PersonMediaDeleteDialog, { props: { target, ...props } });
const body = () => new DOMWrapper(document.body);
beforeEach(() => vi.resetAllMocks());
describe('PersonMediaDeleteDialog', () => {
  it('cancels without calling a deletion API; G-3 确认框只写文件名，不带绝对路径', async () => {
    const w = create();
    expect(w.text()).toContain('photo.jpg');
    expect(w.text()).not.toContain('/photos/photo.jpg');
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' })); await flushPromises();
    expect(w.emitted('close')).toHaveLength(1);
    expect(api.DeleteImagesWithResult).not.toHaveBeenCalled(); expect(api.DeleteVideosWithResult).not.toHaveBeenCalled(); w.unmount();
  });
  it('keeps the dialog on failure, prevents duplicate submissions and allows retry', async () => {
    let reject;
    api.DeleteImagesWithResult.mockImplementationOnce(() => new Promise((_, fail) => { reject = fail; })).mockResolvedValueOnce(okResult([11]));
    const w = create();
    await w.get('[data-test="person-media-delete-confirm"]').trigger('click');
    await w.vm.confirm();
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    expect(api.DeleteImagesWithResult).toHaveBeenCalledTimes(1); expect(w.emitted('close')).toBeUndefined();
    reject('磁盘不可用'); await flushPromises();
    expect(w.get('[role="alert"]').text()).toContain('磁盘不可用'); expect(w.emitted('deleted')).toBeUndefined();
    await w.get('[data-test="person-media-delete-file"]').setValue(true);
    await w.get('[data-test="person-media-delete-confirm"]').trigger('click'); await flushPromises();
    expect(api.DeleteImagesWithResult).toHaveBeenLastCalledWith([11], true, expect.stringMatching(/^[0-9a-f]{32}$/));
    expect(w.emitted('deleted')).toEqual([[target]]); w.unmount();
  });

  it('LIB-12 删除改走带结果码的接口，删完留下整批撤销条，撤销后通知宿主恢复', async () => {
    api.DeleteImagesWithResult.mockResolvedValueOnce(okResult([11], 'batch-1'));
    api.RestoreTrashBatch.mockResolvedValueOnce({ items: [{ id: 11, code: 'ok' }] });
    const w = mountLive();
    expect(body().get('[data-test="person-media-delete-consequence"]').text()).toContain('以后扫描到同一个文件不会再收录');
    await body().get('[data-test="person-media-delete-confirm"]').trigger('click'); await flushPromises();
    expect(api.DeleteImage).not.toHaveBeenCalled();
    expect(api.DeleteImagesWithResult).toHaveBeenCalledWith([11], false, expect.any(String));
    expect(w.emitted('deleted')).toEqual([[target]]);
    // 撤销条还在，组件保持挂载；确认框已收起，遮罩层不再挡住页面。
    expect(w.emitted('close')).toBeUndefined();
    expect(body().find('[data-test="person-media-delete-dialog"]').exists()).toBe(false);
    expect(body().get('.person-media-delete-layer').classes()).toContain('person-media-delete-layer--notice');
    expect(body().get('[data-test="delete-undo-banner"]').text()).toContain('已从图片库移除 1 张图片（文件保留）');
    await body().get('[data-test="delete-undo"]').trigger('click'); await flushPromises();
    expect(api.RestoreTrashBatch).toHaveBeenCalledWith('image', 'batch-1');
    expect(w.emitted('restored')).toEqual([[{ kind: 'image', ids: [11] }]]);
    // 撤销条消失后才通知宿主收起。
    expect(w.emitted('close')).toHaveLength(1);
    w.unmount();
  });

  it('LIB-05 磁盘不支持废纸篓时先让用户二选一；暂不处理则记录原样保留、可以重试', async () => {
    const video = { kind: 'video', personID: 7, media: { id: 21, name: 'clip.mp4', path: '/Volumes/SMB/clip.mp4', size: 10 } };
    api.DeleteVideosWithResult.mockResolvedValueOnce({ batch_id: 'b2', items: [{ id: 21, code: 'trash_unsupported' }] });
    const w = mountLive({ target: video });
    await body().get('[data-test="person-media-delete-file"]').setValue(true);
    await body().get('[data-test="person-media-delete-confirm"]').trigger('click'); await flushPromises();
    expect(body().get('[data-test="trash-unsupported-names"]').text()).toContain('clip.mp4');
    await body().get('[data-test="trash-unsupported-cancel"]').trigger('click'); await flushPromises();
    expect(api.PermanentlyDeleteVideos).not.toHaveBeenCalled();
    expect(w.emitted('deleted')).toBeUndefined();
    expect(body().get('[data-test="person-media-delete-dialog"] [role="alert"]').text()).toContain('不支持废纸篓');
    w.unmount();
  });

  it('宿主换了一条要删的媒体时回到确认步骤', async () => {
    api.DeleteImagesWithResult.mockResolvedValueOnce(okResult([11]));
    const w = mountLive();
    await body().get('[data-test="person-media-delete-confirm"]').trigger('click'); await flushPromises();
    expect(body().find('[data-test="person-media-delete-dialog"]').exists()).toBe(false);
    await w.setProps({ target: { kind: 'image', personID: 7, media: { id: 12, name: 'b.jpg', path: '/photos/b.jpg', size: 1 } } });
    expect(body().get('[data-test="person-media-delete-dialog"]').text()).toContain('b.jpg');
    w.unmount();
  });
});
