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

function result(items, batchID = 'batch-1') {
  const ok = items.filter(item => item.code === 'ok' || item.code === 'file_missing').length;
  const cancelled = items.filter(item => item.code === 'cancelled').length;
  return { batch_id: batchID, requested: items.length, succeeded: ok, cancelled, failed: items.length - ok - cancelled, items };
}

function mountBanner(props = {}) {
  return mount(TrashUndoBanner, {
    props: { afterRestore: vi.fn().mockResolvedValue(), ...props },
    global: { stubs: { TrashCenterDialog: { props: ['visible', 'initialTab'], template: '<div class="trash-center-stub" />' } } }
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.useRealTimers();
  delete window.runtime;
});

describe('LIB-12 删除与撤销条', () => {
  it('LIB-12 批量删除成功后给「撤销本次删除（N 项）」，撤销按批次调用 RestoreTrashBatch', async () => {
    api.DeleteVideosWithResult.mockResolvedValueOnce(result([{ id: 1, code: 'ok' }, { id: 2, code: 'ok' }], 'b-1'));
    const afterRestore = vi.fn().mockResolvedValue();
    const wrapper = mountBanner({ afterRestore });

    const outcome = await wrapper.vm.runDelete({ ids: [1, 2], deleteFile: true });
    wrapper.vm.showDeleteNotice(outcome);
    await flushPromises();

    expect(api.DeleteVideosWithResult).toHaveBeenCalledWith([1, 2], true, expect.stringMatching(/^[0-9a-f]{32}$/));
    expect(outcome.removedIDs).toEqual([1, 2]);
    const banner = wrapper.get('[data-test="delete-undo-banner"]');
    expect(banner.text()).toContain('已移到废纸篓 2 个视频');
    expect(banner.get('[data-test="delete-undo"]').text()).toBe('撤销本次删除（2 项）');

    api.RestoreTrashBatch.mockResolvedValueOnce(result([{ id: 11, code: 'ok' }, { id: 12, code: 'ok' }], 'b-1'));
    await banner.get('[data-test="delete-undo"]').trigger('click');
    await flushPromises();

    expect(api.RestoreTrashBatch).toHaveBeenCalledWith('video', 'b-1');
    expect(afterRestore).toHaveBeenCalledWith([1, 2], false);
    expect(wrapper.find('[data-test="delete-undo-banner"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('LIB-12 撤销只回来一部分时报告原因，交给宿主重新读取状态', async () => {
    api.DeleteVideosWithResult.mockResolvedValueOnce(result([{ id: 1, code: 'ok' }, { id: 2, code: 'ok' }], 'b-2'));
    const afterRestore = vi.fn().mockResolvedValue();
    const wrapper = mountBanner({ afterRestore });
    wrapper.vm.showDeleteNotice(await wrapper.vm.runDelete({ ids: [1, 2], deleteFile: true }));
    await flushPromises();

    api.RestoreTrashBatch.mockResolvedValueOnce(result([{ id: 11, code: 'ok' }, { id: 12, code: 'path_occupied', message: '原位置已收录了新文件' }], 'b-2'));
    await wrapper.vm.undoLastDelete();

    expect(feedback.notifyError).toHaveBeenCalledWith(expect.stringContaining('原位置已收录了新文件'));
    expect(afterRestore).toHaveBeenCalledWith([], true);
    wrapper.unmount();
  });

  it('LIB-04 只删记录的提示写「已从片库移除（文件保留）」，不说移到废纸篓', async () => {
    api.DeleteVideosWithResult.mockResolvedValueOnce(result([{ id: 3, code: 'ok' }]));
    const wrapper = mountBanner();
    wrapper.vm.showDeleteNotice(await wrapper.vm.runDelete({ ids: [3], deleteFile: false }));
    await flushPromises();

    const text = wrapper.get('[data-test="delete-undo-banner"]').text();
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
    expect(wrapper.find('[data-test="delete-undo-banner"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('文件本就不在原处的项计为已移除，但不算进可撤销的数量', async () => {
    api.DeleteVideosWithResult.mockResolvedValueOnce(result([{ id: 1, code: 'ok' }, { id: 2, code: 'file_missing' }]));
    const wrapper = mountBanner();
    const outcome = await wrapper.vm.runDelete({ ids: [1, 2], deleteFile: true });
    wrapper.vm.showDeleteNotice(outcome);
    await flushPromises();

    expect(outcome.removedIDs).toEqual([1, 2]);
    const banner = wrapper.get('[data-test="delete-undo-banner"]');
    expect(banner.text()).toContain('1 个视频的文件已不在原处，只移除了记录');
    expect(banner.get('[data-test="delete-undo"]').text()).toBe('撤销本次删除（1 项）');
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

  it('IMG-02 图片页的撤销条走图片接口', async () => {
    api.DeleteImagesWithResult.mockResolvedValueOnce(result([{ id: 5, code: 'ok' }], 'img-b'));
    const wrapper = mountBanner({ kind: 'image' });
    wrapper.vm.showDeleteNotice(await wrapper.vm.runDelete({ ids: [5], deleteFile: false }));
    await flushPromises();

    expect(api.DeleteImagesWithResult).toHaveBeenCalledWith([5], false, expect.any(String));
    expect(wrapper.get('[data-test="delete-undo-banner"]').text()).toContain('已从图片库移除 1 张图片（文件保留）');
    api.RestoreTrashBatch.mockResolvedValueOnce(result([{ id: 50, code: 'ok' }], 'img-b'));
    await wrapper.vm.undoLastDelete();
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
    expect(wrapper.get('[data-test="delete-progress"]').text()).toContain('正在删除 0/3');

    listeners['batch-delete-progress']({ request_id: 'someone-else', done: 3, total: 3 });
    listeners['batch-delete-progress']({ request_id: requestID, done: 1, total: 3 });
    await wrapper.vm.$nextTick();
    expect(wrapper.get('[data-test="delete-progress"]').text()).toContain('正在删除 1/3');

    await wrapper.get('[data-test="delete-progress-cancel"]').trigger('click');
    expect(api.CancelBatchDelete).toHaveBeenCalledWith(requestID);

    resolveDelete(result([{ id: 1, code: 'ok' }, { id: 2, code: 'cancelled' }, { id: 3, code: 'cancelled' }], 'b-c'));
    const outcome = await pending;
    wrapper.vm.showDeleteNotice(outcome);
    await flushPromises();

    expect(wrapper.find('[data-test="delete-progress"]').exists()).toBe(false);
    expect(listeners['batch-delete-progress']).toBeUndefined();
    expect(outcome.cancelled).toEqual([2, 3]);
    expect(wrapper.get('[data-test="delete-undo-banner"]').text()).toContain('已取消，2 个视频未处理');
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
    expect(wrapper.get('[data-test="delete-undo-banner"]').text()).toContain('2 个视频未删除（所在磁盘不支持废纸篓）');
    wrapper.unmount();
  });

  it('二次确认后才永久删除；永久删除的项不算进可撤销数量', async () => {
    const wrapper = mountBanner();
    const pending = startUnsupported(wrapper);
    await flushPromises();
    api.PermanentlyDeleteVideos.mockResolvedValueOnce(result([{ id: 1, code: 'ok' }, { id: 2, code: 'ok' }], ''));

    await wrapper.get('[data-test="trash-unsupported-permanent"]').trigger('click');
    await wrapper.get('[data-test="trash-unsupported-permanent-confirm"]').trigger('click');
    const outcome = await pending;

    expect(api.PermanentlyDeleteVideos).toHaveBeenCalledWith([1, 2]);
    expect(outcome.permanent).toEqual([1, 2]);
    expect(outcome.removedIDs).toEqual([3, 1, 2]);
    wrapper.vm.showDeleteNotice(outcome);
    await flushPromises();
    const banner = wrapper.get('[data-test="delete-undo-banner"]');
    expect(banner.text()).toContain('已永久删除 2 个视频');
    expect(banner.get('[data-test="delete-undo"]').text()).toBe('撤销本次删除（1 项）');
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
    expect(wrapper.get('[data-test="delete-undo"]').text()).toBe('撤销本次删除（3 项）');

    api.RestoreTrashBatch.mockResolvedValue(result([{ id: 1, code: 'ok' }]));
    await wrapper.vm.undoLastDelete();
    expect(api.RestoreTrashBatch).toHaveBeenCalledWith('video', 'b-u');
    expect(api.RestoreTrashBatch).toHaveBeenCalledWith('video', 'b-r');
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
