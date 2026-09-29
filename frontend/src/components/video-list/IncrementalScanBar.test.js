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

import IncrementalScanBar, { formatSkipBreakdown } from './IncrementalScanBar.vue';

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
    // trigger=manual：后端据此发 library-scan-summary（§1.2b）。
    expect(api.SyncScanDirectories).toHaveBeenCalledWith('manual');
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

describe('扫描摘要（D-PC09）', () => {
  it('LIB-14 「跳过 N」拆成各项原因，含旧版回收站目录；没有跳过就不显示明细', async () => {
    api.SyncScanDirectories.mockResolvedValue({
      scanned: 9, skipped: 6, errors: [],
      skip_breakdown: { existing: 3, blocked_user_delete: 1, recently_modified: 0, temp_file: 0, not_video: 0, read_error: 0, legacy_trash: 2 }
    });
    const wrapper = mountBar();
    await wrapper.vm.runIncrementalScan();
    await flushPromises();
    const detail = wrapper.get('[data-test="scan-skip-breakdown"]').text();
    expect(detail).toBe('跳过明细：已在库 3，删除过、不再收录 1，旧版回收站目录 2');
    expect(formatSkipBreakdown({ skipped: 0, skip_breakdown: {} })).toBe('');
    expect(formatSkipBreakdown({})).toBe('');
    wrapper.unmount();
  });

  it('LIB-08 启动扫描、改目录触发的扫描由片库页转交过来，按来源写标题；不自己重载列表', async () => {
    const reloadView = vi.fn().mockResolvedValue();
    const wrapper = mountBar({ reloadView });
    wrapper.vm.showSummary({ trigger: 'startup', result: { scanned: 4, added: 2, restored: 1, errors: [{ path: '/lib/bad.mkv', error: '读取失败' }], skip_breakdown: { read_error: 1 } } });
    await flushPromises();
    expect(wrapper.text()).toContain('启动扫描完成：扫描 4 个文件，新增 2，恢复显示 1');
    expect(wrapper.vm.incrementalScan.state).toBe('warning');
    expect(wrapper.text()).toContain('读取失败');
    expect(reloadView).not.toHaveBeenCalled();
    expect(api.SyncScanDirectories).not.toHaveBeenCalled();

    wrapper.vm.showSummary({ trigger: 'directory_change', result: { scanned: 1, errors: [] } });
    await flushPromises();
    expect(wrapper.text()).toContain('扫描目录变更后的扫描完成：扫描 1 个文件');

    await wrapper.get('[data-test="scan-summary-close"]').trigger('click');
    expect(wrapper.find('.scan-sync-status').exists()).toBe(false);
    wrapper.unmount();
  });

  it('LIB-08 自己正在扫描时不被别处的摘要覆盖', async () => {
    let resolveScan;
    api.SyncScanDirectories.mockImplementation(() => new Promise(resolve => { resolveScan = resolve; }));
    const wrapper = mountBar();
    const running = wrapper.vm.runIncrementalScan();
    wrapper.vm.showSummary({ trigger: 'startup', result: { scanned: 99, errors: [] } });
    expect(wrapper.vm.incrementalScan.message).toBe('正在扫描已配置目录...');
    resolveScan({ scanned: 1, errors: [] });
    await running;
    expect(wrapper.vm.incrementalScan.message).toContain('增量扫描完成');
    wrapper.unmount();
  });
});
