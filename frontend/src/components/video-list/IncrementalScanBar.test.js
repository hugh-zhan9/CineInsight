// 手动增量扫描从 VideoListPage 抽出后的行为覆盖。
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

import IncrementalScanBar from './IncrementalScanBar.vue';

beforeEach(() => {
  vi.clearAllMocks();
});

function mountBar(props = {}) {
  return mount(IncrementalScanBar, {
    props: { directories: [{ path: '/lib' }], reloadView: vi.fn().mockResolvedValue(), ...props }
  });
}

describe('增量扫描状态条', () => {
  it('扫描完成后报出各项计数，并请片库页刷新目录与列表', async () => {
    api.SyncScanDirectories.mockResolvedValue({ scanned: 5, added: 1, restored: 2, relocated: 0, deleted: 0, metadata_refreshed: 2, skipped: 1, errors: [] });
    const reloadView = vi.fn().mockResolvedValue();
    const wrapper = mountBar({ reloadView });

    await wrapper.vm.runIncrementalScan();
    await flushPromises();

    expect(api.SyncScanDirectories).toHaveBeenCalledOnce();
    expect(wrapper.vm.incrementalScan.state).toBe('success');
    expect(wrapper.vm.incrementalScan.message).toContain('扫描 5 个文件');
    expect(wrapper.vm.incrementalScan.message).toContain('恢复显示 2');
    expect(wrapper.emitted('reload-directories')).toHaveLength(1);
    expect(reloadView).toHaveBeenCalledOnce();
    expect(wrapper.text()).toContain('增量扫描完成');
    wrapper.unmount();
  });

  it('有失败项时降级为 warning，整个失败时是 error', async () => {
    api.SyncScanDirectories.mockResolvedValueOnce({ scanned: 1, errors: [{ path: '/x', error: 'permission denied' }] });
    const wrapper = mountBar();
    await wrapper.vm.runIncrementalScan();
    expect(wrapper.vm.incrementalScan.state).toBe('warning');
    expect(wrapper.text()).toContain('/x：permission denied');

    api.SyncScanDirectories.mockRejectedValueOnce(new Error('磁盘不可用'));
    await wrapper.vm.runIncrementalScan();
    expect(wrapper.vm.incrementalScan.state).toBe('error');
    expect(wrapper.vm.incrementalScan.message).toContain('增量扫描失败');
    expect(wrapper.text()).not.toContain('permission denied');
    wrapper.unmount();
  });

  it('迁移进行中、已在扫描或没有扫描目录时不启动', async () => {
    const running = mountBar({ migrationRunning: true });
    await running.vm.runIncrementalScan();
    expect(api.SyncScanDirectories).not.toHaveBeenCalled();
    running.unmount();

    const empty = mountBar({ directories: [] });
    await empty.vm.runIncrementalScan();
    expect(api.SyncScanDirectories).not.toHaveBeenCalled();
    empty.unmount();
  });

  it('状态镜像回片库页，管理菜单与 ⌘R 才知道它在跑', async () => {
    api.SyncScanDirectories.mockResolvedValue({ scanned: 0, errors: [] });
    const wrapper = mountBar();
    await wrapper.vm.runIncrementalScan();
    await flushPromises();
    expect(wrapper.emitted('state-change').at(-1)[0]).toEqual(expect.objectContaining({ running: false, state: 'success' }));
    wrapper.unmount();
  });
});
