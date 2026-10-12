import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'CancelDataHealthCheck', 'GetLatestDataHealthReport', 'RepairDataHealth', 'RunDataHealthCheck'
].map(name => [name, vi.fn()])));
const feedback = vi.hoisted(() => ({ confirmAction: vi.fn() }));
vi.mock('../../../wailsjs/go/main/App', () => api);
vi.mock('../../utils/feedback.js', () => feedback);

import DataHealthPanel, { DATA_HEALTH_CHECKS } from './DataHealthPanel.vue';

const wrappers = [];
let progressHandler = null;

const danglingCheck = {
  check: 'dangling_refs', status: 'problem', count: 3, bytes: 0, summary: '3 行引用的记录已彻底不存在，其中 2 行可以清理',
  examples: [], repairable: true, link: '',
  groups: [
    { key: 'playback_queue_entries', label: '待播队列', count: 2, detail: '', examples: ['id 4'], repairable: true },
    { key: 'migration_staged_sources', label: '迁移暂存来源（历史记录，只报）', count: 1, detail: '', examples: ['id 9'], repairable: false }
  ]
};
const report = {
  id: 3, backend: 'sqlite', started_at: '2026-10-12T08:00:00Z', finished_at: '2026-10-12T08:00:05Z',
  checks: [
    { check: 'sqlite_integrity', status: 'ok', count: 0, summary: '数据库文件完整', examples: [], groups: [], repairable: false, link: '' },
    danglingCheck,
    { check: 'backups', status: 'warning', count: 1, summary: '共 2 份备份', examples: [], groups: [], repairable: false, link: 'settings:backup' },
    { check: 'orphan_files', status: 'warning', count: 3, bytes: 2048, summary: '3 个孤儿文件', examples: [], groups: [], repairable: false, link: 'data-care:lost' }
  ]
};

beforeEach(() => {
  vi.resetAllMocks();
  progressHandler = null;
  window.runtime = {
    EventsOn: (name, handler) => {
      if (name === 'data-health-progress') progressHandler = handler;
      return () => {};
    }
  };
  api.GetLatestDataHealthReport.mockResolvedValue({ running: false, progress: { phase: 'idle' }, report: null });
  feedback.confirmAction.mockResolvedValue(true);
});

afterEach(() => {
  while (wrappers.length) wrappers.pop().unmount();
  delete window.runtime;
});

async function mountPanel() {
  const wrapper = mount(DataHealthPanel, { attachTo: document.body });
  wrappers.push(wrapper);
  await flushPromises();
  return wrapper;
}
const find = (wrapper, name) => wrapper.get(`[data-test="${name}"]`);
const exists = (wrapper, name) => wrapper.find(`[data-test="${name}"]`).exists();

describe('数据体检面板（D-DH-04 / D-DH-05）', () => {
  it('没有报告时提示开始体检；运行后逐项列出结论、数量、示例与说明，并显示上次体检时间', async () => {
    const wrapper = await mountPanel();
    expect(exists(wrapper, 'data-health-never')).toBe(true);
    expect(exists(wrapper, 'data-health-empty')).toBe(true);
    api.RunDataHealthCheck.mockResolvedValue(report);
    await find(wrapper, 'data-health-run').trigger('click');
    await flushPromises();
    expect(api.RunDataHealthCheck).toHaveBeenCalledTimes(1);
    expect(exists(wrapper, 'data-health-last-run')).toBe(true);
    const dangling = find(wrapper, 'data-health-check-dangling_refs');
    expect(dangling.get('[data-test="data-health-status"]').text()).toBe('有问题');
    expect(dangling.get('[data-test="data-health-summary"]').text()).toContain('2 行可以清理');
    expect(dangling.text()).toContain(DATA_HEALTH_CHECKS.dangling_refs.explain.slice(0, 10));
    expect(dangling.get('[data-test="data-health-group-playback_queue_entries"]').text()).toContain('id 4');
    expect(dangling.get('[data-test="data-health-group-migration_staged_sources"]').text()).toContain('（只报告）');
    expect(exists(wrapper, 'data-health-repair-sqlite_integrity')).toBe(false);
  });

  it('运行中显示进度并可取消；取消后提示没有保存报告', async () => {
    let reject;
    api.RunDataHealthCheck.mockReturnValue(new Promise((_, no) => { reject = no; }));
    const wrapper = await mountPanel();
    await find(wrapper, 'data-health-run').trigger('click');
    progressHandler({ phase: 'check', check: 'dangling_refs', done: 1, total: 7 });
    await flushPromises();
    expect(find(wrapper, 'data-health-progress').text()).toBe('正在检查：引用悬挂（2/7）');
    expect(find(wrapper, 'data-health-run').attributes('disabled')).toBeDefined();
    await find(wrapper, 'data-health-cancel').trigger('click');
    expect(api.CancelDataHealthCheck).toHaveBeenCalledTimes(1);
    reject(new Error('数据体检已取消'));
    await flushPromises();
    expect(find(wrapper, 'data-health-notice').text()).toContain('已取消体检');
    expect(exists(wrapper, 'data-health-error')).toBe(false);
    expect(exists(wrapper, 'data-health-cancel')).toBe(false);
  });

  it('修复先确认（只计可清理的行），执行后用重跑结果替换这一项；取消确认则不执行', async () => {
    api.GetLatestDataHealthReport.mockResolvedValue({ running: false, progress: { phase: 'idle' }, report });
    const wrapper = await mountPanel();
    feedback.confirmAction.mockResolvedValueOnce(false);
    await find(wrapper, 'data-health-repair-dangling_refs').trigger('click');
    await flushPromises();
    expect(api.RepairDataHealth).not.toHaveBeenCalled();
    expect(feedback.confirmAction.mock.calls[0][0].message).toContain('删除 2 行');

    const rerun = { ...danglingCheck, count: 1, repairable: false, summary: '1 行引用的记录已彻底不存在，其中 0 行可以清理', groups: [danglingCheck.groups[1]] };
    api.RepairDataHealth.mockResolvedValue({ check: 'dangling_refs', repaired: 2, cancelled: false, result: rerun });
    await find(wrapper, 'data-health-repair-dangling_refs').trigger('click');
    await flushPromises();
    expect(api.RepairDataHealth).toHaveBeenCalledWith('dangling_refs');
    expect(find(wrapper, 'data-health-notice').text()).toBe('已删除 2 行悬挂引用，已重新检查这一项');
    expect(feedback.confirmAction.mock.calls[1][0].danger).toBe(true);
    expect(exists(wrapper, 'data-health-repair-dangling_refs')).toBe(false);
    expect(find(wrapper, 'data-health-check-dangling_refs').get('[data-test="data-health-summary"]').text()).toContain('0 行可以清理');
  });

  it('备份项链到设置 · 备份，孤儿文件链到失联页签', async () => {
    api.GetLatestDataHealthReport.mockResolvedValue({ running: false, progress: { phase: 'idle' }, report });
    const wrapper = await mountPanel();
    await find(wrapper, 'data-health-link-backups').trigger('click');
    await find(wrapper, 'data-health-link-orphan_files').trigger('click');
    expect(wrapper.emitted('run-command')).toEqual([['action:settings:backup', '数据库备份'], ['action:data-care:lost', '失联']]);
    expect(find(wrapper, 'data-health-check-orphan_files').text()).toContain('2.0 KB');
  });

  it('回到页签时体检仍在跑：显示进度，跑完后读回新报告', async () => {
    api.GetLatestDataHealthReport.mockResolvedValueOnce({ running: true, progress: { phase: 'check', check: 'backups', done: 5, total: 7 }, report: null });
    const wrapper = await mountPanel();
    expect(find(wrapper, 'data-health-progress').text()).toBe('正在检查：备份状态（6/7）');
    api.GetLatestDataHealthReport.mockResolvedValueOnce({ running: false, progress: { phase: 'idle' }, report });
    progressHandler({ phase: 'idle', check: '', done: 0, total: 0 });
    await flushPromises();
    expect(api.GetLatestDataHealthReport).toHaveBeenCalledTimes(2);
    expect(exists(wrapper, 'data-health-check-backups')).toBe(true);
    expect(exists(wrapper, 'data-health-progress')).toBe(false);
  });

  it('清空失效路径：确认框是危险样式，完成提示用后端实际清空的条数', async () => {
    const managed = {
      check: 'managed_paths', status: 'problem', count: 4, bytes: 0, summary: '4 条记录指向已不存在的托管图片', examples: [], repairable: true, link: '',
      groups: [
        { key: 'videos.poster_path', label: '视频海报副本', count: 4, detail: '', examples: ['media-details/videos/1/a.jpg'], repairable: true },
        { key: 'unavailable:people', label: 'media-details/people（目录不可用）', count: 9, detail: '目录不存在或链接失效，其下 9 条未检查', examples: [], repairable: false }
      ]
    };
    api.GetLatestDataHealthReport.mockResolvedValue({ running: false, progress: { phase: 'idle' }, report: { ...report, checks: [managed] } });
    api.RepairDataHealth.mockResolvedValue({ check: 'managed_paths', repaired: 3, cancelled: false, result: { ...managed, count: 0, repairable: false, status: 'warning', groups: [managed.groups[1]] } });
    const wrapper = await mountPanel();
    expect(find(wrapper, 'data-health-group-unavailable:people').text()).toContain('其下 9 条未检查');
    await find(wrapper, 'data-health-repair-managed_paths').trigger('click');
    await flushPromises();
    const options = feedback.confirmAction.mock.calls[0][0];
    expect(options.danger).toBe(true);
    expect(options.message).toContain('清空 4 条');
    expect(api.RepairDataHealth).toHaveBeenCalledWith('managed_paths');
    expect(find(wrapper, 'data-health-notice').text()).toBe('已清空 3 条失效路径，已重新检查这一项');
  });
});
