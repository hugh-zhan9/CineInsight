import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
  SelectDirectory: vi.fn(), SyncDirectoryWithProgress: vi.fn(), AddVideo: vi.fn(),
  DeleteVideo: vi.fn(), GetVideosByDirectory: vi.fn(), AddDirectory: vi.fn()
}));
vi.mock('../../wailsjs/go/main/App', () => api);
vi.mock('../utils/feedback.js', () => ({ notify: vi.fn(), notifyError: vi.fn() }));
import ScanDialog from './ScanDialog.vue';

let handlers, off, wrapper;
function deferred() {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
async function start() {
  wrapper = mount(ScanDialog, { props: { visible: true, directories: [{ path: '/share/videos' }] } });
  wrapper.vm.scanDirectory = '/share/videos';
  const running = wrapper.vm.startScan();
  await flushPromises();
  return { running, requestID: api.SyncDirectoryWithProgress.mock.calls[0][1] };
}

beforeEach(() => {
  vi.resetAllMocks();
  handlers = {};
  off = vi.fn();
  window.runtime = { EventsOn: (name, fn) => { handlers[name] = fn; return off; } };
  api.GetVideosByDirectory.mockResolvedValue([]);
});
afterEach(() => {
  wrapper?.unmount();
  vi.useRealTimers();
  delete window.runtime;
});

describe('手动目录扫描实时进度', () => {
  it('扫描尚未返回就显示已发现数量，核对期间保留数量且不显示 0/0', async () => {
    const scan = deferred();
    api.SyncDirectoryWithProgress.mockReturnValue(scan.promise);
    const { running, requestID } = await start();
    expect(wrapper.text()).toContain('正在检查目录');
    expect(wrapper.text()).not.toContain('已发现 0');
    handlers['directory-scan-progress']({ request_id: requestID, phase: 'reading', visited: 35, found: 12, current_path: '/share/videos/sub' });
    await flushPromises();
    expect(wrapper.get('[data-test="scan-discovery"]').text()).toContain('已检查 35 个文件，已发现 12 个视频');
    expect(wrapper.text()).toContain('/share/videos/sub');
    expect(wrapper.text()).not.toContain('正在处理 0/0');
    handlers['directory-scan-progress']({ request_id: requestID, phase: 'reconciling', found: 1 });
    await flushPromises();
    expect(wrapper.text()).toContain('正在核对片库记录');
    expect(wrapper.vm.scanProgress.found).toBe(1);
    // Events already queued by Wails must not regress the processing phase.
    handlers['directory-scan-progress']({ request_id: requestID, phase: 'reading', visited: 0, found: 0 });
    expect(wrapper.vm.scanProgress.phase).toBe('reconciling');
    scan.resolve({ scanned: 1, added: 0, restored: 0, deleted: 0, errors: [] });
    await running;
    expect(wrapper.text()).toContain('扫描完成');
    expect(off).toHaveBeenCalledOnce();
  });

  it('等待读取时显示经过时间，重开弹窗保留任务，忽略别的扫描事件', async () => {
    vi.useFakeTimers({ toFake: ['Date', 'setInterval', 'clearInterval'] });
    const scan = deferred();
    api.SyncDirectoryWithProgress.mockReturnValue(scan.promise);
    const { running, requestID } = await start();
    handlers['directory-scan-progress']({ request_id: requestID, phase: 'reading', visited: 3, found: 2, current_path: '/share/videos' });
    handlers['directory-scan-progress']({ request_id: 'another-scan', phase: 'reading', visited: 99, found: 99 });
    await wrapper.setProps({ visible: false });
    await wrapper.setProps({ visible: true });
    await vi.advanceTimersByTimeAsync(9000);
    expect(wrapper.get('[data-test="scan-waiting"]').text()).toContain('仍在等待目录读取返回');
    expect(wrapper.text()).toContain('已用时 9 秒');
    expect(wrapper.vm.scanProgress.found).toBe(2);
    expect(wrapper.vm.scanDirectory).toBe('/share/videos');
    expect(wrapper.text()).toContain('后台继续');
    expect(wrapper.findAll('button')[0].element.disabled).toBe(true);
    await wrapper.vm.startScan();
    expect(api.SyncDirectoryWithProgress).toHaveBeenCalledOnce();
    scan.resolve({ scanned: 0, errors: [] });
    await running;
    expect(vi.getTimerCount()).toBe(0);
  });

  it('目录读取失败时展示失败，不进入增删对账，并释放监听', async () => {
    const scan = deferred();
    api.SyncDirectoryWithProgress.mockReturnValue(scan.promise);
    const { running } = await start();
    scan.reject(new Error('目录读取失败'));
    await running;
    expect(wrapper.get('[data-test="scan-result"]').text()).toContain('扫描失败');
    expect(api.GetVideosByDirectory).not.toHaveBeenCalled();
    expect(api.DeleteVideo).not.toHaveBeenCalled();
    expect(off).toHaveBeenCalledOnce();
  });

  it('后端统一对账并展示恢复与删除计数，不调用用户删除接口', async () => {
    const scan = deferred();
    api.SyncDirectoryWithProgress.mockReturnValue(scan.promise);
    const { running, requestID } = await start();
    handlers['directory-scan-progress']({ request_id: requestID, phase: 'processing', found: 3, total: 4, processed: 0 });
    await flushPromises();
    expect(wrapper.text()).toContain('正在处理 0/4');
    scan.resolve({ scanned: 3, added: 1, restored: 2, deleted: 1, stale: 0, errors: [] });
    await running;
    expect(api.DeleteVideo).not.toHaveBeenCalled();
    expect(api.AddVideo).not.toHaveBeenCalled();
    expect(api.GetVideosByDirectory).not.toHaveBeenCalled();
    expect(wrapper.text()).toContain('扫描完成：新增 1 个，恢复 2 个，删除 1 个');
  });

  it('后端报告部分失败时保留原因，不显示成功', async () => {
    api.SyncDirectoryWithProgress.mockResolvedValue({ scanned: 1, errors: [{ error: '扫描根已离线' }] });
    const { running } = await start();
    await running;
    expect(wrapper.get('[data-test="scan-result"]').attributes('role')).toBe('alert');
    expect(wrapper.text()).toContain('扫描根已离线');
    expect(api.DeleteVideo).not.toHaveBeenCalled();
  });
});
