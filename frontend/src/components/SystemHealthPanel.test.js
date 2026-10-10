import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ GetSystemHealthSection: vi.fn(), ExportSystemHealthReport: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);
import SystemHealthPanel from './SystemHealthPanel.vue';

const snapshot = (key, detail = key) => ({ key, checked_at: '2026-10-10T06:00:00Z', items: [{ key, label: key, state: 'unknown', detail, metrics: [] }] });
beforeEach(() => {
  vi.clearAllMocks();
  api.GetSystemHealthSection.mockImplementation(key => Promise.resolve(snapshot(key)));
  api.ExportSystemHealthReport.mockResolvedValue({ saved: true, message: '已保存' });
});

describe('运行状态与诊断', () => {
  it('启动失败入口隐藏处理动作，但保留读取和导出', async () => {
    api.GetSystemHealthSection.mockImplementation(key => Promise.resolve({ ...snapshot(key), items: [{ ...snapshot(key).items[0], action_id: 'action:settings:database' }] }));
    const wrapper = mount(SystemHealthPanel, { props: { actionsEnabled: false } });
    await flushPromises();
    expect(wrapper.find('[data-test="health-action-database"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="health-export"]').exists()).toBe(true);
    expect(api.GetSystemHealthSection).toHaveBeenCalledTimes(6);
    wrapper.unmount();
  });
  it('各分区独立显示，慢检查和单项失败不遮住其他结果，也不暴露原始错误', async () => {
    let finishRuntime;
    api.GetSystemHealthSection.mockImplementation(key => {
      if (key === 'runtimes') return new Promise(resolve => { finishRuntime = resolve; });
      if (key === 'storage') return Promise.reject(new Error('/Users/SECRET?api_key=SECRET'));
      return Promise.resolve(snapshot(key));
    });
    const wrapper = mount(SystemHealthPanel);
    await flushPromises();
    expect(api.GetSystemHealthSection).toHaveBeenCalledTimes(6);
    expect(wrapper.get('[data-test="health-item-database"]').text()).toContain('database');
    expect(wrapper.get('[data-test="health-section-runtimes"]').text()).toContain('正在读取');
    expect(wrapper.get('[data-test="health-error-storage"]').text()).toContain('读取扫描目录与磁盘失败');
    expect(wrapper.text()).not.toContain('SECRET');
    finishRuntime(snapshot('runtimes'));
    await flushPromises();
    expect(wrapper.get('[data-test="health-item-runtimes"]').text()).toContain('未知');
    wrapper.unmount();
  });

  it('迟到的旧刷新结果不覆盖新结果，卸载后不再更新', async () => {
    let finishOld;
    api.GetSystemHealthSection.mockImplementationOnce(() => new Promise(resolve => { finishOld = resolve; }));
    const wrapper = mount(SystemHealthPanel);
    await flushPromises();
    await wrapper.vm.refresh();
    finishOld(snapshot('database', 'old'));
    await flushPromises();
    expect(wrapper.get('[data-test="health-item-database"]').text()).not.toContain('old');
    wrapper.unmount();
    expect(wrapper.vm._requestGeneration).toBe(null);
  });

  it('失败刷新保留旧结果，查看入口只发命令而不自动修复', async () => {
    api.GetSystemHealthSection.mockImplementation(key => Promise.resolve({ ...snapshot(key), items: [{ ...snapshot(key).items[0], action_id: 'action:settings:database', local_detail: '/Volumes/本机目录' }] }));
    const wrapper = mount(SystemHealthPanel);
    await flushPromises();
    await wrapper.get('[data-test="health-action-database"]').trigger('click');
    expect(wrapper.emitted('run-command')[0]).toEqual(['action:settings:database']);
    api.GetSystemHealthSection.mockRejectedValue(new Error('offline'));
    await wrapper.vm.refresh();
    expect(wrapper.get('[data-test="health-item-database"]').text()).toContain('/Volumes/本机目录');
    expect(wrapper.get('[data-test="health-error-database"]').exists()).toBe(true);
    wrapper.unmount();
  });

  it('导出只在显式点击时执行，并区分取消、成功与失败', async () => {
    const wrapper = mount(SystemHealthPanel);
    await flushPromises();
    expect(api.ExportSystemHealthReport).not.toHaveBeenCalled();
    api.ExportSystemHealthReport.mockResolvedValueOnce({ saved: false, message: '已取消导出' });
    await wrapper.get('[data-test="health-export"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="health-export-message"]').text()).toBe('已取消导出');
    await wrapper.get('[data-test="health-export"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="health-export-message"]').text()).toBe('已保存');
    api.ExportSystemHealthReport.mockRejectedValueOnce(new Error('/Users/SECRET'));
    await wrapper.get('[data-test="health-export"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="health-export-error"]').text()).toContain('导出失败');
    expect(wrapper.text()).not.toContain('SECRET');
    wrapper.unmount();
  });
});
