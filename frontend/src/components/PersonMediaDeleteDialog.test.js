import { DOMWrapper, mount, flushPromises } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';
const api = vi.hoisted(() => ({
  DeleteVideo: vi.fn(), DeleteImage: vi.fn(),
  DeleteVideosWithResult: vi.fn(), DeleteImagesWithResult: vi.fn(),
  PermanentlyDeleteVideos: vi.fn(), PermanentlyDeleteImages: vi.fn(),
  RestoreTrashBatch: vi.fn(), CancelBatchDelete: vi.fn(), GetSettings: vi.fn()
}));
vi.mock('../../wailsjs/go/main/App', () => api);
vi.mock('../utils/feedback.js', async (importOriginal) => ({ ...(await importOriginal()), notify: vi.fn(), notifyError: vi.fn() }));
vi.mock('./TrashCenterDialog.vue', async (importOriginal) => ({ ...(await importOriginal()), default: { name: 'TrashCenterDialog', template: '<div />' } }));
import PersonMediaDeleteDialog from './PersonMediaDeleteDialog.vue';
import { clearToasts, feedbackState, notify, runToastAction } from '../utils/feedback.js';
import * as recentOperations from '../utils/recentOperations.js';
const target = { kind: 'image', personID: 7, media: { id: 11, name: 'photo.jpg', path: '/photos/photo.jpg', size: 1024 } };
const okResult = (ids, batchID = 'b1') => ({ batch_id: batchID, items: ids.map(id => ({ id, code: 'ok' })) });
// 挂载后先读设置（「删除前确认」），读完才决定亮确认框还是直接删：确认框是之后才渲染的，要看真实的 Teleport 渲染
// （teleport 桩不跟着重渲染），所以都直接查 body，挂完先等一轮。
const mountLive = async (props = {}) => { const w = mount(PersonMediaDeleteDialog, { props: { target, ...props } }); await flushPromises(); return w; };
const body = () => new DOMWrapper(document.body);
const undoToast = () => feedbackState.toasts.find(toast => toast.action?.label === '撤销');
beforeEach(() => {
  vi.resetAllMocks(); clearToasts(); recentOperations.clear();
  api.GetSettings.mockResolvedValue({ confirm_before_delete: true, delete_original_file: false });
});
describe('PersonMediaDeleteDialog', () => {
  it('cancels without calling a deletion API; G-3 确认框只写文件名，不带绝对路径', async () => {
    const w = await mountLive();
    expect(body().get('[data-test="person-media-delete-dialog"]').text()).toContain('photo.jpg');
    expect(body().text()).not.toContain('/photos/photo.jpg');
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' })); await flushPromises();
    expect(w.emitted('close')).toHaveLength(1);
    expect(api.DeleteImagesWithResult).not.toHaveBeenCalled(); expect(api.DeleteVideosWithResult).not.toHaveBeenCalled(); w.unmount();
  });
  it('keeps the dialog on failure, prevents duplicate submissions and allows retry', async () => {
    let reject;
    api.DeleteImagesWithResult.mockImplementationOnce(() => new Promise((_, fail) => { reject = fail; })).mockResolvedValueOnce(okResult([11]));
    const w = await mountLive();
    await body().get('[data-test="person-media-delete-confirm"]').trigger('click');
    await w.vm.confirm();
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    expect(api.DeleteImagesWithResult).toHaveBeenCalledTimes(1); expect(w.emitted('close')).toBeUndefined();
    reject('磁盘不可用'); await flushPromises();
    expect(body().get('[data-test="person-media-delete-dialog"] [role="alert"]').text()).toContain('磁盘不可用'); expect(w.emitted('deleted')).toBeUndefined();
    await body().get('[data-test="person-media-delete-file"]').setValue(true);
    await body().get('[data-test="person-media-delete-confirm"]').trigger('click'); await flushPromises();
    expect(api.DeleteImagesWithResult).toHaveBeenLastCalledWith([11], true, expect.stringMatching(/^[0-9a-f]{32}$/));
    expect(w.emitted('deleted')).toEqual([[target]]); w.unmount();
  });

  it('LIB-12 删除改走带结果码的接口，删完给带「撤销」的全局提示（D-UD-07/09），撤销后通知宿主恢复', async () => {
    api.DeleteImagesWithResult.mockResolvedValueOnce(okResult([11], 'batch-1'));
    api.RestoreTrashBatch.mockResolvedValueOnce({ items: [{ id: 11, code: 'ok' }] });
    const w = await mountLive();
    expect(body().get('[data-test="person-media-delete-consequence"]').text()).toContain('以后扫描到同一个文件不会再收录');
    await body().get('[data-test="person-media-delete-confirm"]').trigger('click'); await flushPromises();
    expect(api.DeleteImage).not.toHaveBeenCalled();
    expect(api.DeleteImagesWithResult).toHaveBeenCalledWith([11], false, expect.any(String));
    expect(w.emitted('deleted')).toEqual([[target]]);
    // 撤销提示还在，组件保持挂载；确认框已收起，遮罩层不再挡住页面；页内不再出撤销条。
    expect(w.emitted('close')).toBeUndefined();
    expect(body().find('[data-test="person-media-delete-dialog"]').exists()).toBe(false);
    expect(body().get('.person-media-delete-layer').classes()).toContain('person-media-delete-layer--notice');
    expect(body().find('[data-test="delete-undo-banner"]').exists()).toBe(false);
    expect(undoToast().message).toContain('已从图片库移除 1 张图片（文件保留）');
    // 同一条操作也记进任务中心「最近操作」（notifyUndoable）。
    expect(recentOperations.list().map(item => item.label)).toEqual([undoToast().message]);
    await runToastAction(undoToast().id); await flushPromises();
    expect(api.RestoreTrashBatch).toHaveBeenCalledWith('image', 'batch-1');
    expect(w.emitted('restored')).toEqual([[{ kind: 'image', ids: [11] }]]);
    expect(recentOperations.list()).toEqual([]);
    // 撤销完、提示收起后才通知宿主收起。
    expect(w.emitted('close')).toHaveLength(1);
    w.unmount();
  });

  it('提示收起、对话框被宿主卸下之后从「最近操作」撤销：照样按批次恢复，并经 @restored 通知宿主重读', async () => {
    api.DeleteImagesWithResult.mockResolvedValueOnce(okResult([11], 'batch-7'));
    api.RestoreTrashBatch.mockResolvedValueOnce({ items: [{ id: 11, code: 'ok' }] });
    const onRestored = vi.fn();
    const w = await mountLive({ onRestored });
    await body().get('[data-test="person-media-delete-confirm"]').trigger('click'); await flushPromises();
    // 提示到时收起：对话框发 close，宿主（v-if）把它卸下。
    clearToasts(); await flushPromises();
    expect(w.emitted('close')).toHaveLength(1);
    w.unmount();

    const [entry] = recentOperations.list();
    expect(await recentOperations.undo(entry.id)).toBe(true);
    expect(api.RestoreTrashBatch).toHaveBeenCalledWith('image', 'batch-7');
    expect(onRestored.mock.calls).toEqual([[{ kind: 'image', ids: [11] }]]);
    expect(recentOperations.list()).toEqual([]);
  });

  it('卸下之后撤销有项没恢复：仍让宿主以回收站为准重读，「最近操作」留着可再试', async () => {
    api.DeleteImagesWithResult.mockResolvedValueOnce(okResult([11], 'batch-8'));
    api.RestoreTrashBatch.mockResolvedValueOnce({ items: [{ id: 11, code: 'restore_conflict', message: '原位置已有同名文件' }] });
    const onRestored = vi.fn();
    const w = await mountLive({ onRestored });
    await body().get('[data-test="person-media-delete-confirm"]').trigger('click'); await flushPromises();
    clearToasts(); await flushPromises();
    w.unmount();

    const [entry] = recentOperations.list();
    await expect(recentOperations.undo(entry.id)).rejects.toThrow('恢复 0 项，1 项未恢复');
    expect(onRestored.mock.calls).toEqual([[{ kind: 'image', ids: [] }]]);
    expect(recentOperations.list().map(item => item.id)).toEqual([entry.id]);
  });

  // M-4：这批已经在回收站里恢复过了（恢复 0 项、也没有失败）：记录移除并说一声，宿主以回收站为准重读。
  it('这批已经从回收站恢复过：撤销时提示「这批已经恢复过了」，「最近操作」移除这一条', async () => {
    api.DeleteImagesWithResult.mockResolvedValueOnce(okResult([11], 'batch-done'));
    api.RestoreTrashBatch.mockResolvedValueOnce({ items: [] });
    const onRestored = vi.fn();
    const w = await mountLive({ onRestored });
    await body().get('[data-test="person-media-delete-confirm"]').trigger('click'); await flushPromises();
    clearToasts(); await flushPromises();
    w.unmount();

    const [entry] = recentOperations.list();
    expect(await recentOperations.undo(entry.id)).toBe(true);
    expect(api.RestoreTrashBatch).toHaveBeenCalledWith('image', 'batch-done');
    expect(notify).toHaveBeenCalledWith('这批已经恢复过了');
    expect(onRestored.mock.calls).toEqual([[{ kind: 'image', ids: [] }]]);
    expect(recentOperations.list()).toEqual([]);
  });

  it('撤销有项没恢复时报「撤销失败」，宿主以回收站为准重读，「最近操作」留着可再试', async () => {
    api.DeleteImagesWithResult.mockResolvedValueOnce(okResult([11], 'batch-1'));
    api.RestoreTrashBatch.mockResolvedValueOnce({ items: [{ id: 11, code: 'restore_conflict', message: '原位置已有同名文件' }] });
    const w = await mountLive();
    await body().get('[data-test="person-media-delete-confirm"]').trigger('click'); await flushPromises();
    const label = undoToast().message;
    expect(await runToastAction(undoToast().id)).toBe(false); await flushPromises();
    expect(feedbackState.toasts.some(toast => toast.level === 'error' && toast.message.startsWith('撤销失败：恢复 0 项，1 项未恢复'))).toBe(true);
    expect(w.emitted('restored')).toEqual([[{ kind: 'image', ids: [] }]]);
    expect(recentOperations.list().map(item => item.label)).toEqual([label]);
    expect(w.emitted('close')).toHaveLength(1);
    w.unmount();
  });

  // I-2：人物页 / 抽屉里删媒体一律先确认，不看「删除前确认」开关（按钮容易被当成「从人物名下拿掉」）；
  // 框里「同时把原文件移到废纸篓」默认取「删除原文件」的设置。
  it('「删除前确认」关掉时仍弹确认框；用户确认之前不调删除，勾选默认取「删除原文件」', async () => {
    api.GetSettings.mockResolvedValue({ confirm_before_delete: false, delete_original_file: true });
    api.DeleteImagesWithResult.mockResolvedValueOnce({ batch_id: 'b9', items: [{ id: 11, code: 'ok' }] });
    const w = await mountLive();
    expect(body().get('[data-test="person-media-delete-dialog"]').text()).toContain('photo.jpg');
    expect(body().get('[data-test="person-media-delete-file"]').element.checked).toBe(true);
    expect(body().get('[data-test="person-media-delete-confirm"]').text()).toBe('移到废纸篓');
    expect(api.DeleteImagesWithResult).not.toHaveBeenCalled();
    expect(api.DeleteVideosWithResult).not.toHaveBeenCalled();
    await body().get('[data-test="person-media-delete-confirm"]').trigger('click'); await flushPromises();
    expect(api.DeleteImagesWithResult).toHaveBeenCalledWith([11], true, expect.any(String));
    expect(w.emitted('deleted')).toEqual([[target]]);
    expect(undoToast().message).toContain('已移到废纸篓 1 张图片');
    w.unmount();
  });

  it('「删除前确认」关掉、取消确认框时什么都不删', async () => {
    api.GetSettings.mockResolvedValue({ confirm_before_delete: false, delete_original_file: false });
    const video = { kind: 'video', personID: 7, media: { id: 21, name: 'clip.mp4', path: '/v/clip.mp4', size: 10 } };
    const w = await mountLive({ target: video });
    expect(body().get('[data-test="person-media-delete-file"]').element.checked).toBe(false);
    expect(body().get('[data-test="person-media-delete-confirm"]').text()).toBe('从片库移除');
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' })); await flushPromises();
    expect(w.emitted('close')).toHaveLength(1);
    expect(api.DeleteVideosWithResult).not.toHaveBeenCalled();
    expect(api.DeleteImagesWithResult).not.toHaveBeenCalled();
    w.unmount();
  });

  it('设置读不出来时按默认（先确认）处理', async () => {
    api.GetSettings.mockRejectedValue(new Error('数据库正在维护'));
    const w = await mountLive();
    expect(body().get('[data-test="person-media-delete-dialog"]').text()).toContain('photo.jpg');
    expect(api.DeleteImagesWithResult).not.toHaveBeenCalled();
    w.unmount();
  });

  it('LIB-05 磁盘不支持废纸篓时先让用户二选一；暂不处理则记录原样保留、可以重试', async () => {
    const video = { kind: 'video', personID: 7, media: { id: 21, name: 'clip.mp4', path: '/Volumes/SMB/clip.mp4', size: 10 } };
    api.DeleteVideosWithResult.mockResolvedValueOnce({ batch_id: 'b2', items: [{ id: 21, code: 'trash_unsupported' }] });
    const w = await mountLive({ target: video });
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
    const w = await mountLive();
    await body().get('[data-test="person-media-delete-confirm"]').trigger('click'); await flushPromises();
    expect(body().find('[data-test="person-media-delete-dialog"]').exists()).toBe(false);
    await w.setProps({ target: { kind: 'image', personID: 7, media: { id: 12, name: 'b.jpg', path: '/photos/b.jpg', size: 1 } } });
    await flushPromises();
    expect(body().get('[data-test="person-media-delete-dialog"]').text()).toContain('b.jpg');
    w.unmount();
  });
});
