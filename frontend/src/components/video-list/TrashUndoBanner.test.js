// 删除流程宿主（P-030）：带结果码的删除、进度与取消、卷不支持废纸篓时的二选一、整批撤销。
// 片库页与图片页共用这个组件，这里直接驱动它，页面侧的接线另见各页面的用例。
import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const feedback = vi.hoisted(() => ({
  confirmAction: vi.fn(() => Promise.resolve(true)),
  notify: vi.fn(),
  notifyError: vi.fn(),
  notifySuccess: vi.fn()
}));
vi.mock('../../utils/feedback.js', async (importOriginal) => ({
  ...(await importOriginal()),
  ...feedback
}));

const api = vi.hoisted(() => new Proxy({}, {
  get(target, prop) {
    if (typeof prop === 'symbol' || prop === 'then') return undefined;
    if (!target[prop]) target[prop] = vi.fn(() => Promise.resolve(null));
    return target[prop];
  }
}));
vi.mock('../../../wailsjs/go/main/App', () => api);

import TrashUndoBanner from './TrashUndoBanner.vue';
import { feedbackState, resetFeedback, runToastAction } from '../../utils/feedback.js';
import * as recentOperations from '../../utils/recentOperations.js';

// 删除结果的全局提示（D-UD-07/09）：可撤销的经 notifyUndoable 进提示列表并带「撤销」；没有可撤销项的只走 notifySuccess。
const undoToast = () => feedbackState.toasts.find(toast => toast.action?.label === '撤销');
const noticeText = () => undoToast()?.message ?? String(feedback.notifySuccess.mock.calls.at(-1)?.[0] ?? '');
const undoNow = () => runToastAction(undoToast().id);

function result(items, batchID = 'batch-1') {
  const ok = items.filter(item => item.code === 'ok' || item.code === 'file_missing').length;
  const cancelled = items.filter(item => item.code === 'cancelled').length;
  return { batch_id: batchID, requested: items.length, succeeded: ok, cancelled, failed: items.length - ok - cancelled, items };
}

// 「不支持废纸篓」二选一出现时才 teleport 到 body（层级按打开先后，见 tokens.css「层级」）：用 teleport 桩留在子树里查。
function mountBanner(props = {}) {
  return mount(TrashUndoBanner, {
    props: { afterRestore: vi.fn().mockResolvedValue(), ...props },
    global: { stubs: { teleport: true, TrashCenterDialog: { props: ['visible', 'initialTab'], template: '<div class="trash-center-stub" />' } } }
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.useRealTimers();
  delete window.runtime;
  resetFeedback();
  recentOperations.clear();
});

// 统一设计 D-UD-07/09：undo-toast 的宿主（片库页）删除成功后不出页内条，改出全局提示「撤销」，
// 并记进任务中心「最近操作」；两处是同一条记录，撤销走既有的 RestoreTrashBatch。
describe('D-UD-07/09 撤销改为全局提示（undo-toast）', () => {
  it('可撤销的删除出带「撤销」的提示并进「最近操作」；点撤销按批次恢复，记录随之移除', async () => {
    api.DeleteVideosWithResult.mockResolvedValueOnce(result([{ id: 1, code: 'ok' }, { id: 2, code: 'ok' }], 'b-t1'));
    const afterRestore = vi.fn().mockResolvedValue();
    const wrapper = mountBanner({ afterRestore });
    wrapper.vm.showDeleteNotice(await wrapper.vm.runDelete({ ids: [1, 2], deleteFile: true }));
    await flushPromises();

    expect(wrapper.find('[data-test="delete-undo-banner"]').exists()).toBe(false);
    expect(feedbackState.toasts).toHaveLength(1);
    const toast = feedbackState.toasts[0];
    expect(toast.message).toContain('已移到废纸篓 2 个视频');
    expect(toast.action.label).toBe('撤销');
    expect(recentOperations.list().map(item => item.label)).toEqual([toast.message]);

    api.RestoreTrashBatch.mockResolvedValueOnce(result([{ id: 11, code: 'ok' }, { id: 12, code: 'ok' }], 'b-t1'));
    expect(await runToastAction(toast.id)).toBe(true);
    expect(api.RestoreTrashBatch).toHaveBeenCalledWith('video', 'b-t1');
    expect(afterRestore).toHaveBeenCalledWith([1, 2], false);
    expect(recentOperations.list()).toEqual([]);
    wrapper.unmount();
  });

  it('没有可撤销的项（文件本就不在）时只报结果，不给「撤销」也不进「最近操作」', async () => {
    api.DeleteVideosWithResult.mockResolvedValueOnce(result([{ id: 3, code: 'file_missing' }], 'b-t2'));
    const wrapper = mountBanner();
    wrapper.vm.showDeleteNotice(await wrapper.vm.runDelete({ ids: [3], deleteFile: true }));
    await flushPromises();

    expect(feedback.notifySuccess).toHaveBeenCalledWith(expect.stringContaining('文件已不在原处'));
    expect(feedbackState.toasts).toEqual([]);
    expect(recentOperations.list()).toEqual([]);
    expect(wrapper.find('[data-test="delete-undo-banner"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('宿主接口不变：撤销进行中 undoing 为真（洞察页据此锁住切页），dismissNotice 仍可调用', async () => {
    api.DeleteVideosWithResult.mockResolvedValueOnce(result([{ id: 5, code: 'ok' }], 'b-t4'));
    const wrapper = mountBanner();
    wrapper.vm.showDeleteNotice(await wrapper.vm.runDelete({ ids: [5], deleteFile: true }));
    await flushPromises();
    expect(() => wrapper.vm.dismissNotice()).not.toThrow();
    expect(undoToast()).toBeDefined();

    let finishRestore;
    api.RestoreTrashBatch.mockImplementationOnce(() => new Promise(resolve => { finishRestore = resolve; }));
    const undoing = undoNow();
    await flushPromises();
    expect(wrapper.vm.undoing).toBe(true);
    finishRestore(result([{ id: 50, code: 'ok' }], 'b-t4'));
    expect(await undoing).toBe(true);
    expect(wrapper.vm.undoing).toBe(false);
    wrapper.unmount();
  });

  // 破坏性流程复审的探针（undo-older-batch）：先后删了两批，宿主卸下之后从「最近操作」撤销较早的那批——
  // 只按那一批恢复、只执行一次（提示里再点也不重复），宿主照样收到 afterRestore；较晚那批的「撤销」原样留着。
  it('撤销较早的一批：只恢复那一批、只执行一次，宿主卸下后照样回调；较晚那批的撤销不受影响', async () => {
    const afterRestore = vi.fn().mockResolvedValue();
    const wrapper = mountBanner({ afterRestore });
    api.DeleteVideosWithResult
      .mockResolvedValueOnce(result([{ id: 1, code: 'ok' }, { id: 2, code: 'ok' }], 'A'))
      .mockResolvedValueOnce(result([{ id: 3, code: 'ok' }], 'B'));
    wrapper.vm.showDeleteNotice(await wrapper.vm.runDelete({ ids: [1, 2], deleteFile: true }));
    wrapper.vm.showDeleteNotice(await wrapper.vm.runDelete({ ids: [3], deleteFile: true }));
    const [toastA, toastB] = feedbackState.toasts.filter(toast => toast.action?.label === '撤销');
    const [opB, opA] = recentOperations.list();
    wrapper.unmount();

    api.RestoreTrashBatch.mockResolvedValue(result([{ id: 11, code: 'ok' }, { id: 12, code: 'ok' }], 'A'));
    expect(await recentOperations.undo(opA.id)).toBe(true);
    expect(await runToastAction(toastA.id)).toBe(true);
    await flushPromises();
    expect(api.RestoreTrashBatch.mock.calls).toEqual([['video', 'A']]);
    expect(afterRestore).toHaveBeenCalledWith([1, 2], false);
    expect(recentOperations.list().map(op => op.id)).toEqual([opB.id]);
    expect(toastB.action.label).toBe('撤销');
  });

  it('恢复调用失败时提示「撤销失败」，记录留在「最近操作」里可再试', async () => {
    api.DeleteVideosWithResult.mockResolvedValueOnce(result([{ id: 4, code: 'ok' }], 'b-t3'));
    const afterRestore = vi.fn().mockResolvedValue();
    const wrapper = mountBanner({ afterRestore });
    wrapper.vm.showDeleteNotice(await wrapper.vm.runDelete({ ids: [4], deleteFile: true }));
    await flushPromises();
    const toast = feedbackState.toasts[0];

    api.RestoreTrashBatch.mockRejectedValueOnce(new Error('database is in maintenance mode'));
    expect(await runToastAction(toast.id)).toBe(false);
    // runToastAction 在 feedback.js 内部出错误提示（不经过被替换的 notifyError），直接看提示列表。
    const errors = feedbackState.toasts.filter(item => item.level === 'error');
    expect(errors.map(item => item.message)).toEqual([expect.stringMatching(/^撤销失败：数据库正在维护/)]);
    expect(afterRestore).not.toHaveBeenCalled();
    expect(recentOperations.list()).toHaveLength(1);
    wrapper.unmount();
  });
});

describe('LIB-12 删除与撤销', () => {
  it('LIB-12 批量删除成功后给「撤销」，撤销按批次调用 RestoreTrashBatch', async () => {
    api.DeleteVideosWithResult.mockResolvedValueOnce(result([{ id: 1, code: 'ok' }, { id: 2, code: 'ok' }], 'b-1'));
    const afterRestore = vi.fn().mockResolvedValue();
    const wrapper = mountBanner({ afterRestore });

    const outcome = await wrapper.vm.runDelete({ ids: [1, 2], deleteFile: true });
    wrapper.vm.showDeleteNotice(outcome);
    await flushPromises();

    expect(api.DeleteVideosWithResult).toHaveBeenCalledWith([1, 2], true, expect.stringMatching(/^[0-9a-f]{32}$/));
    expect(outcome.removedIDs).toEqual([1, 2]);
    expect(noticeText()).toContain('已移到废纸篓 2 个视频');
    expect(undoToast().action.label).toBe('撤销');
    expect(recentOperations.list()).toHaveLength(1);

    api.RestoreTrashBatch.mockResolvedValueOnce(result([{ id: 11, code: 'ok' }, { id: 12, code: 'ok' }], 'b-1'));
    expect(await undoNow()).toBe(true);
    await flushPromises();

    expect(api.RestoreTrashBatch).toHaveBeenCalledWith('video', 'b-1');
    expect(afterRestore).toHaveBeenCalledWith([1, 2], false);
    expect(undoToast()).toBeUndefined();
    expect(recentOperations.list()).toEqual([]);
    wrapper.unmount();
  });

  // M-3：撤销只回来一部分时整体算失败——提示「撤销失败：」带恢复与未恢复的条数和原因，「最近操作」留着可再试
  // （RestoreTrashBatch 只恢复仍是已删除的条目，再试是安全的），并交给宿主以回收站为准重读。
  it('LIB-12 撤销只回来一部分时报「撤销失败」与条数原因，「最近操作」留着可再试，交给宿主重新读取状态', async () => {
    api.DeleteVideosWithResult.mockResolvedValueOnce(result([{ id: 1, code: 'ok' }, { id: 2, code: 'ok' }], 'b-2'));
    const afterRestore = vi.fn().mockResolvedValue();
    const wrapper = mountBanner({ afterRestore });
    wrapper.vm.showDeleteNotice(await wrapper.vm.runDelete({ ids: [1, 2], deleteFile: true }));
    await flushPromises();
    const [operation] = recentOperations.list();

    api.RestoreTrashBatch.mockResolvedValueOnce(result([{ id: 11, code: 'ok' }, { id: 12, code: 'path_occupied', message: '原位置已收录了新文件' }], 'b-2'));
    expect(await undoNow()).toBe(false);

    const errors = feedbackState.toasts.filter(item => item.level === 'error').map(item => item.message);
    expect(errors).toEqual([expect.stringMatching(/^撤销失败：恢复 1 项，1 项未恢复：.*原位置已收录了新文件/)]);
    expect(afterRestore).toHaveBeenCalledWith([], true);
    expect(recentOperations.list().map(item => item.id)).toEqual([operation.id]);

    // 再试：剩下那项这回恢复了，记录移除。
    api.RestoreTrashBatch.mockResolvedValueOnce(result([{ id: 12, code: 'ok' }], 'b-2'));
    expect(await recentOperations.undo(operation.id)).toBe(true);
    expect(api.RestoreTrashBatch.mock.calls).toEqual([['video', 'b-2'], ['video', 'b-2']]);
    expect(recentOperations.list()).toEqual([]);
    wrapper.unmount();
  });

  // M-4：这批已经在回收站里恢复过了（恢复 0 项、也没有失败）：不再一直报错，记录移除并说一声。
  it('这批已经从回收站恢复过：撤销时提示「这批已不在回收站（已恢复或已清除）」，「最近操作」移除这一条', async () => {
    api.DeleteVideosWithResult.mockResolvedValueOnce(result([{ id: 1, code: 'ok' }], 'b-done'));
    const afterRestore = vi.fn().mockResolvedValue();
    const wrapper = mountBanner({ afterRestore });
    wrapper.vm.showDeleteNotice(await wrapper.vm.runDelete({ ids: [1], deleteFile: true }));
    await flushPromises();
    const [operation] = recentOperations.list();

    api.RestoreTrashBatch.mockResolvedValueOnce(result([], 'b-done'));
    expect(await recentOperations.undo(operation.id)).toBe(true);
    expect(feedback.notify).toHaveBeenCalledWith('这批已不在回收站（已恢复或已清除）');
    expect(feedbackState.toasts.filter(item => item.level === 'error')).toEqual([]);
    expect(recentOperations.list()).toEqual([]);
    expect(afterRestore).toHaveBeenCalledWith([], true);
    wrapper.unmount();
  });

  it('LIB-04 只删记录的提示写「已从片库移除（文件保留）」，不说移到废纸篓', async () => {
    api.DeleteVideosWithResult.mockResolvedValueOnce(result([{ id: 3, code: 'ok' }]));
    const wrapper = mountBanner();
    wrapper.vm.showDeleteNotice(await wrapper.vm.runDelete({ ids: [3], deleteFile: false }));
    await flushPromises();

    const text = noticeText();
    expect(text).toContain('已从片库移除 1 个视频（文件保留）');
    expect(text).not.toContain('废纸篓 1');
    wrapper.unmount();
  });

  it('LIB-04 磁盘离线、没有权限等失败按原因报告，不降级、不给撤销', async () => {
    api.DeleteVideosWithResult.mockResolvedValueOnce(result([
      { id: 1, code: 'volume_offline', message: '文件所在磁盘当前不可访问，未做任何改动' },
      { id: 2, code: 'permission_denied', message: '没有权限访问文件所在位置，未做任何改动' }
    ]));
    const wrapper = mountBanner();
    const outcome = await wrapper.vm.runDelete({ ids: [1, 2], deleteFile: true });
    wrapper.vm.showDeleteNotice(outcome);
    await flushPromises();

    expect(outcome.removedIDs).toEqual([]);
    expect(outcome.remainingIDs).toEqual([1, 2]);
    expect(api.DeleteVideosWithResult).toHaveBeenCalledTimes(1);
    expect(feedback.notifyError).toHaveBeenCalledWith(expect.stringContaining('所在磁盘未连接或不可访问'));
    expect(feedback.notifyError).toHaveBeenCalledWith(expect.stringContaining('没有权限'));
    expect(undoToast()).toBeUndefined();
    expect(feedback.notifySuccess).not.toHaveBeenCalled();
    expect(recentOperations.list()).toEqual([]);
    wrapper.unmount();
  });

  it('文件本就不在原处的项计为已移除，但不算进可撤销的数量', async () => {
    api.DeleteVideosWithResult.mockResolvedValueOnce(result([{ id: 1, code: 'ok' }, { id: 2, code: 'file_missing' }]));
    const wrapper = mountBanner();
    const outcome = await wrapper.vm.runDelete({ ids: [1, 2], deleteFile: true });
    wrapper.vm.showDeleteNotice(outcome);
    await flushPromises();

    expect(outcome.removedIDs).toEqual([1, 2]);
    expect(noticeText()).toContain('1 个视频的文件已不在原处，只移除了记录');
    // 只有移到废纸篓的那一项能撤销。
    api.RestoreTrashBatch.mockResolvedValueOnce(result([{ id: 11, code: 'ok' }], 'batch-1'));
    expect(await undoNow()).toBe(true);
    expect(wrapper.props('afterRestore')).toHaveBeenCalledWith([1], false);
    wrapper.unmount();
  });

  it('从回收站对话框恢复本类媒体时回调 afterRestore(ids, true)，别的类型不管', async () => {
    const afterRestore = vi.fn().mockResolvedValue();
    const wrapper = mountBanner({ afterRestore });

    await wrapper.vm.handleTrashRestored({ kind: 'image', entityIDs: [9] });
    expect(afterRestore).not.toHaveBeenCalled();
    await wrapper.vm.handleTrashRestored({ kind: 'video', entityIDs: [7] });
    expect(afterRestore).toHaveBeenCalledWith([7], true);
    wrapper.unmount();
  });

  it('openTrashDialog 默认停在本页那一类，也可以指定页签', async () => {
    const wrapper = mountBanner({ kind: 'image' });
    wrapper.vm.openTrashDialog();
    expect(wrapper.vm.trashDialog).toEqual({ show: true, tab: 'image' });
    wrapper.vm.openTrashDialog('staged');
    expect(wrapper.vm.trashDialog).toEqual({ show: true, tab: 'staged' });
    wrapper.unmount();
  });

  it('IMG-02 图片页的撤销走图片接口', async () => {
    api.DeleteImagesWithResult.mockResolvedValueOnce(result([{ id: 5, code: 'ok' }], 'img-b'));
    const wrapper = mountBanner({ kind: 'image' });
    wrapper.vm.showDeleteNotice(await wrapper.vm.runDelete({ ids: [5], deleteFile: false }));
    await flushPromises();

    expect(api.DeleteImagesWithResult).toHaveBeenCalledWith([5], false, expect.any(String));
    expect(noticeText()).toContain('已从图片库移除 1 张图片（文件保留）');
    api.RestoreTrashBatch.mockResolvedValueOnce(result([{ id: 50, code: 'ok' }], 'img-b'));
    await undoNow();
    expect(api.RestoreTrashBatch).toHaveBeenCalledWith('image', 'img-b');
    wrapper.unmount();
  });
});

describe('IMG-12 批量删除的进度与取消', () => {
  it('按 request_id 显示进度，点「取消」调用 CancelBatchDelete，未处理的项计为已取消', async () => {
    const listeners = {};
    window.runtime = {
      EventsOn: vi.fn((name, handler) => {
        listeners[name] = handler;
        return () => { delete listeners[name]; };
      })
    };
    let resolveDelete;
    api.DeleteVideosWithResult.mockReturnValueOnce(new Promise(resolve => { resolveDelete = resolve; }));
    const wrapper = mountBanner();

    const pending = wrapper.vm.runDelete({ ids: [1, 2, 3], deleteFile: true });
    await flushPromises();
    const requestID = api.DeleteVideosWithResult.mock.calls[0][2];
    expect(wrapper.get('[data-test="delete-progress"]').text()).toContain('正在移到废纸篓 0/3 个视频…');

    listeners['batch-delete-progress']({ request_id: 'someone-else', done: 3, total: 3 });
    listeners['batch-delete-progress']({ request_id: requestID, done: 1, total: 3 });
    await wrapper.vm.$nextTick();
    expect(wrapper.get('[data-test="delete-progress"]').text()).toContain('正在移到废纸篓 1/3 个视频…');

    await wrapper.get('[data-test="delete-progress-cancel"]').trigger('click');
    expect(api.CancelBatchDelete).toHaveBeenCalledWith(requestID);

    resolveDelete(result([{ id: 1, code: 'ok' }, { id: 2, code: 'cancelled' }, { id: 3, code: 'cancelled' }], 'b-c'));
    const outcome = await pending;
    wrapper.vm.showDeleteNotice(outcome);
    await flushPromises();

    expect(wrapper.find('[data-test="delete-progress"]').exists()).toBe(false);
    expect(listeners['batch-delete-progress']).toBeUndefined();
    expect(outcome.cancelled).toEqual([2, 3]);
    expect(noticeText()).toContain('已取消，2 个视频未处理');
    wrapper.unmount();
  });

  it('只删一项时不显示进度条', async () => {
    window.runtime = { EventsOn: vi.fn(() => () => {}) };
    api.DeleteVideosWithResult.mockResolvedValueOnce(result([{ id: 1, code: 'ok' }]));
    const wrapper = mountBanner();
    await wrapper.vm.runDelete({ ids: [1], deleteFile: true });
    expect(window.runtime.EventsOn).not.toHaveBeenCalled();
    wrapper.unmount();
  });
});

describe('LIB-04 D-PC02 磁盘不支持废纸篓', () => {
  async function startUnsupported(wrapper, names = { 1: 'a.mp4', 2: 'b.mp4' }) {
    api.DeleteVideosWithResult.mockResolvedValueOnce(result([
      { id: 1, code: 'trash_unsupported' }, { id: 2, code: 'trash_unsupported' }, { id: 3, code: 'ok' }
    ], 'b-u'));
    const pending = wrapper.vm.runDelete({ ids: [1, 2, 3], deleteFile: true, names });
    await flushPromises();
    return pending;
  }

  it('先完成其余项，再把不支持的项归到一个弹窗里列出文件名', async () => {
    const wrapper = mountBanner();
    const pending = startUnsupported(wrapper);
    await flushPromises();

    const names = wrapper.get('[data-test="trash-unsupported-names"]').text();
    expect(names).toContain('a.mp4');
    expect(names).toContain('b.mp4');
    await wrapper.get('[data-test="trash-unsupported-cancel"]').trigger('click');
    const outcome = await pending;
    expect(outcome.trashed).toEqual([3]);
    wrapper.unmount();
  });

  it('永久删除需要二次确认：退回或取消都不调用 PermanentlyDeleteVideos', async () => {
    const wrapper = mountBanner();
    const pending = startUnsupported(wrapper);
    await flushPromises();

    await wrapper.get('[data-test="trash-unsupported-permanent"]').trigger('click');
    expect(api.PermanentlyDeleteVideos).not.toHaveBeenCalled();
    expect(wrapper.get('[data-test="trash-unsupported-confirm-names"]').text()).toContain('a.mp4');
    await wrapper.get('[data-test="trash-unsupported-back"]').trigger('click');
    expect(api.PermanentlyDeleteVideos).not.toHaveBeenCalled();
    await wrapper.get('[data-test="trash-unsupported-cancel"]').trigger('click');

    const outcome = await pending;
    expect(api.PermanentlyDeleteVideos).not.toHaveBeenCalled();
    expect(api.DeleteVideosWithResult).toHaveBeenCalledTimes(1);
    expect(outcome.kept).toEqual([1, 2]);
    expect(outcome.removedIDs).toEqual([3]);
    wrapper.vm.showDeleteNotice(outcome);
    await flushPromises();
    expect(noticeText()).toContain('2 个视频未删除（所在磁盘不支持废纸篓）');
    wrapper.unmount();
  });

  it('二次确认后才永久删除；永久删除的项不算进可撤销数量', async () => {
    const wrapper = mountBanner();
    const pending = startUnsupported(wrapper);
    await flushPromises();
    api.PermanentlyDeleteVideos.mockResolvedValueOnce(result([{ id: 1, code: 'ok' }, { id: 2, code: 'ok' }], ''));

    await wrapper.get('[data-test="trash-unsupported-permanent"]').trigger('click');
    // 不可逆（D-UD-07 第 3 级）：没输入按钮上的文字之前点不了，也不会删。
    const confirmButton = () => wrapper.get('[data-test="trash-unsupported-permanent-confirm"]');
    expect(confirmButton().text()).toBe('永久删除');
    expect(confirmButton().element.disabled).toBe(true);
    await confirmButton().trigger('click');
    expect(api.PermanentlyDeleteVideos).not.toHaveBeenCalled();
    await wrapper.get('[data-test="trash-unsupported-confirm-input"]').setValue('永久');
    expect(confirmButton().element.disabled).toBe(true);
    await wrapper.get('[data-test="trash-unsupported-confirm-input"]').setValue('永久删除');
    expect(confirmButton().element.disabled).toBe(false);
    await confirmButton().trigger('click');
    const outcome = await pending;

    expect(api.PermanentlyDeleteVideos).toHaveBeenCalledWith([1, 2]);
    expect(outcome.permanent).toEqual([1, 2]);
    expect(outcome.removedIDs).toEqual([3, 1, 2]);
    wrapper.vm.showDeleteNotice(outcome);
    await flushPromises();
    expect(noticeText()).toContain('已永久删除 2 个视频');
    // 永久删除的两项没有回收站条目，撤销只恢复移到废纸篓的那一项。
    api.RestoreTrashBatch.mockResolvedValueOnce(result([{ id: 30, code: 'ok' }], 'b-u'));
    expect(await undoNow()).toBe(true);
    expect(wrapper.props('afterRestore')).toHaveBeenCalledWith([3], false);
    wrapper.unmount();
  });

  it('选「只删记录」时对这些项只删记录，撤销覆盖两个批次', async () => {
    const wrapper = mountBanner();
    const pending = startUnsupported(wrapper);
    await flushPromises();
    api.DeleteVideosWithResult.mockResolvedValueOnce(result([{ id: 1, code: 'ok' }, { id: 2, code: 'ok' }], 'b-r'));

    await wrapper.get('[data-test="trash-unsupported-record-only"]').trigger('click');
    const outcome = await pending;

    expect(api.DeleteVideosWithResult).toHaveBeenLastCalledWith([1, 2], false, '');
    expect(api.PermanentlyDeleteVideos).not.toHaveBeenCalled();
    expect(outcome.recordOnly).toEqual([1, 2]);
    expect(outcome.batchIDs).toEqual(['b-u', 'b-r']);
    wrapper.vm.showDeleteNotice(outcome);
    await flushPromises();
    expect(undoToast().action.label).toBe('撤销');

    api.RestoreTrashBatch.mockResolvedValue(result([{ id: 1, code: 'ok' }]));
    await undoNow();
    expect(api.RestoreTrashBatch).toHaveBeenCalledWith('video', 'b-u');
    expect(api.RestoreTrashBatch).toHaveBeenCalledWith('video', 'b-r');
    // 三项（两个批次）一起交回宿主。
    expect(wrapper.props('afterRestore')).toHaveBeenCalledWith([3, 1, 2], false);
    wrapper.unmount();
  });

  it('按文件夹删除时用调用方给的删除入口，文件名缺失时退回编号', async () => {
    const wrapper = mountBanner({ kind: 'image' });
    const invoke = vi.fn().mockResolvedValue(result([{ id: 8, code: 'trash_unsupported' }], 'b-f'));
    const pending = wrapper.vm.runDelete({ deleteFile: true, total: 1, invoke });
    await flushPromises();

    expect(invoke).toHaveBeenCalledWith(expect.stringMatching(/^[0-9a-f]{32}$/));
    expect(wrapper.get('[data-test="trash-unsupported-names"]').text()).toContain('图片（编号 8）');
    await wrapper.get('[data-test="trash-unsupported-cancel"]').trigger('click');
    await pending;
    expect(api.PermanentlyDeleteImages).not.toHaveBeenCalled();
    wrapper.unmount();
  });
});
