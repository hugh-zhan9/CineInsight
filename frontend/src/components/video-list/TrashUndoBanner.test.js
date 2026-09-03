// 撤销提示条与回收站入口从 VideoListPage 抽出后的行为覆盖：拆分前它们只被
// scripts/trash-restore.test.mjs 的源码断言钉住，这里补上真正跑一遍的用例。
import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => new Proxy({}, {
  get(target, prop) {
    if (typeof prop === 'symbol' || prop === 'then') return undefined;
    if (!target[prop]) target[prop] = vi.fn(() => Promise.resolve(null));
    return target[prop];
  }
}));
vi.mock('../../../wailsjs/go/main/App', () => api);
vi.mock('../TrashRestoreDialog.vue', () => ({ default: { template: '<div />' } }));

import TrashUndoBanner from './TrashUndoBanner.vue';

beforeEach(() => {
  vi.clearAllMocks();
  vi.useFakeTimers();
});

function mountBanner(afterRestore = vi.fn()) {
  return mount(TrashUndoBanner, { props: { afterRestore } });
}

describe('撤销提示条', () => {
  it('删除成功后按回收站条目给出可直接撤销的提示，12 秒后自动收起', async () => {
    api.ListTrashEntries.mockResolvedValue([{ id: 9, video_id: 2 }]);
    const wrapper = mountBanner();

    await wrapper.vm.showDeleteUndo([2]);
    expect(wrapper.vm.undoNotice).toEqual({ count: 1, entry: { id: 9, video_id: 2 } });

    vi.advanceTimersByTime(12000);
    expect(wrapper.vm.undoNotice).toBeNull();
    wrapper.unmount();
  });

  it('批量删除拿不到单条回收站记录时退化成「查看回收站」', async () => {
    api.ListTrashEntries.mockResolvedValue([{ id: 9, video_id: 2 }]);
    const wrapper = mountBanner();

    await wrapper.vm.showDeleteUndo([2, 3]);
    expect(wrapper.vm.undoNotice).toEqual({ count: 2, entry: null });
    wrapper.unmount();
  });

  it('撤销走 RestoreTrashEntry，并把后续收尾交回片库页', async () => {
    api.ListTrashEntries.mockResolvedValue([{ id: 9, video_id: 2 }]);
    const afterRestore = vi.fn().mockResolvedValue();
    const wrapper = mountBanner(afterRestore);
    await wrapper.vm.showDeleteUndo([2]);

    await wrapper.vm.undoLastDelete();
    await flushPromises();

    expect(api.RestoreTrashEntry).toHaveBeenCalledWith(9);
    expect(afterRestore).toHaveBeenCalledWith(2, false);
    expect(wrapper.vm.undoNotice).toBeNull();
    expect(wrapper.vm.undoing).toBe(false);
    wrapper.unmount();
  });

  it('没有可直接撤销的条目时改为打开回收站', async () => {
    api.ListTrashEntries.mockResolvedValue([]);
    const wrapper = mountBanner();
    await wrapper.vm.showDeleteUndo([2, 3]);

    await wrapper.vm.undoLastDelete();
    expect(api.RestoreTrashEntry).not.toHaveBeenCalled();
    expect(wrapper.vm.trashDialog.show).toBe(true);
    wrapper.unmount();
  });

  it('从回收站对话框恢复时额外要求刷新清理分析状态', async () => {
    const afterRestore = vi.fn().mockResolvedValue();
    const wrapper = mountBanner(afterRestore);

    await wrapper.vm.handleTrashRestored({ id: 7 });
    expect(afterRestore).toHaveBeenCalledWith(7, true);
    wrapper.unmount();
  });
});
