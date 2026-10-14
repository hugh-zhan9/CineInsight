import { flushPromises, mount, enableAutoUnmount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
  GetPendingWorkSummary: vi.fn(),
  GetCleanupStatus: vi.fn(),
  GetImageCleanupStatus: vi.fn(),
  GetBackgroundTasks: vi.fn(),
  GetAllDirectories: vi.fn(),
  GetLibraryWatcherStatus: vi.fn()
}));
vi.mock('../../../wailsjs/go/main/App', () => api);
import ManageHomePage, { offlineDisks, volumeOf } from './ManageHomePage.vue';
import { recordDownloadDuplicate, recordScanSummary, resetSessionChanges } from '../../utils/sessionChanges.js';

enableAutoUnmount(afterEach);

const GIB = 1024 ** 3;
const cleanup = groups => ({ completed: true, running: false, stale: false, analysis: { duplicate_groups: groups } });
const dirs = [{ id: 1, path: '/Volumes/Movies/a' }, { id: 2, path: '/Volumes/Movies/b' }, { id: 3, path: '/Volumes/Backup/c' }, { id: 4, path: '/Users/me/Videos' }];
let handlers;

beforeEach(() => {
  resetSessionChanges();
  api.GetPendingWorkSummary.mockResolvedValue({ total: 7 });
  api.GetCleanupStatus.mockResolvedValue(cleanup([{ original: { id: 1, size: GIB }, candidates: [{ id: 2, size: 2 * GIB }] }]));
  api.GetImageCleanupStatus.mockResolvedValue(null);
  api.GetBackgroundTasks.mockResolvedValue(['phash', 'subtitle', 'face']);
  api.GetAllDirectories.mockResolvedValue(dirs);
  api.GetLibraryWatcherStatus.mockResolvedValue({ running: true, roots: [
    { directory_id: 1, state: 'unavailable' }, { directory_id: 2, state: 'unavailable' }, { directory_id: 3, state: 'unavailable' }, { directory_id: 4, state: 'watching' }
  ] });
  handlers = {};
  window.runtime = { EventsOn: vi.fn((name, handler) => { handlers[name] = handler; return () => delete handlers[name]; }) };
});

afterEach(() => {
  delete window.runtime;
});

async function mountPage(props = {}) {
  const wrapper = mount(ManageHomePage, { props });
  await flushPromises();
  return wrapper;
}

const card = (wrapper, key) => wrapper.get(`[data-test="manage-card-${key}"]`);
const value = (wrapper, key) => card(wrapper, key).get('[data-test="stat-tile-value"]').text();
const note = (wrapper, key) => card(wrapper, key).get('[data-test="stat-tile-note"]').text();

describe('ManageHomePage', () => {
  it('shows the four stat cards from their own sources', async () => {
    const wrapper = await mountPage();
    expect(value(wrapper, 'pending')).toBe('7项');
    expect(value(wrapper, 'reclaim')).toBe('2.0 GB');
    expect(note(wrapper, 'reclaim')).toBe('确定 2.0 GB · 待确认 0 B');
    expect(value(wrapper, 'tasks')).toBe('3个');
    expect(note(wrapper, 'tasks')).toBe('近重复指纹、字幕生成 等');
    expect(value(wrapper, 'disks')).toBe('2块');
    expect(note(wrapper, 'disks')).toBe('3 个扫描目录当前不可访问');
  });

  it('opens the matching action for each card', async () => {
    const wrapper = await mountPage();
    for (const key of ['pending', 'reclaim', 'tasks', 'disks']) await card(wrapper, key).trigger('click');
    expect(wrapper.emitted('open-pending-work')).toHaveLength(1);
    expect(wrapper.emitted('run-command')).toEqual([['library.openCleanup', '清理中心']]);
    expect(wrapper.emitted('open-task-center')).toHaveLength(1);
    expect(wrapper.emitted('navigate')).toEqual([['settings']]);
  });

  it('fails each card on its own without affecting the others', async () => {
    api.GetPendingWorkSummary.mockRejectedValue('读取待审阅的人脸失败');
    api.GetLibraryWatcherStatus.mockRejectedValue(new Error('database is in maintenance mode'));
    const wrapper = await mountPage();
    expect(value(wrapper, 'pending')).toBe('—');
    expect(note(wrapper, 'pending')).toBe('读取失败：读取待审阅的人脸失败');
    expect(note(wrapper, 'disks')).toContain('数据库正在恢复备份或切换后端');
    expect(value(wrapper, 'reclaim')).toBe('2.0 GB');
    expect(value(wrapper, 'tasks')).toBe('3个');
  });

  it('shows loading on every card until its data arrives', async () => {
    api.GetBackgroundTasks.mockReturnValue(new Promise(() => {}));
    const wrapper = await mountPage();
    expect(value(wrapper, 'tasks')).toBe('…');
    expect(card(wrapper, 'tasks').attributes('aria-busy')).toBe('true');
    expect(value(wrapper, 'pending')).toBe('7项');
  });

  it('follows background-tasks events and reloads when the page becomes active again', async () => {
    const wrapper = await mountPage();
    handlers['background-tasks']([]);
    await flushPromises();
    expect(value(wrapper, 'tasks')).toBe('0个');
    expect(note(wrapper, 'tasks')).toBe('没有在跑的后台任务');
    api.GetPendingWorkSummary.mockResolvedValue({ total: 0 });
    await wrapper.setProps({ pageActive: false });
    await wrapper.setProps({ pageActive: true });
    await flushPromises();
    expect(value(wrapper, 'pending')).toBe('0项');
    expect(note(wrapper, 'pending')).toBe('没有待处理的事项');
  });

  it('says when it cannot tell whether disks are online', async () => {
    api.GetLibraryWatcherStatus.mockResolvedValue({ running: false, roots: dirs.map(dir => ({ directory_id: dir.id, state: 'disabled' })) });
    let wrapper = await mountPage();
    expect(value(wrapper, 'disks')).toBe('—');
    expect(note(wrapper, 'disks')).toContain('判断不了');
    wrapper.unmount();
    api.GetAllDirectories.mockResolvedValue([]);
    wrapper = await mountPage();
    expect(value(wrapper, 'disks')).toBe('0块');
    expect(note(wrapper, 'disks')).toBe('还没有添加扫描目录');
  });

  it('lists only changes received this session, newest first, and navigates from them', async () => {
    let wrapper = await mountPage();
    expect(wrapper.find('[data-test="manage-changes-empty"]').exists()).toBe(true);
    wrapper.unmount();
    recordScanSummary({ trigger: 'manual', result: { added: 2 } });
    recordDownloadDuplicate({ title: '片名', kind: 'exact' });
    wrapper = await mountPage();
    const rows = wrapper.findAll('[data-test="manage-change"]');
    expect(rows.map(row => row.find('strong').text())).toEqual(['下载查重', '手动扫描']);
    expect(rows[1].text()).toContain('新增 2');
    await rows[0].trigger('click');
    expect(wrapper.emitted('navigate')).toEqual([['downloads']]);
    recordScanSummary({ trigger: 'startup', result: {} });
    await flushPromises();
    expect(wrapper.findAll('[data-test="manage-change"]')).toHaveLength(3);
    expect(wrapper.find('[data-test="manage-changes-toggle"]').exists()).toBe(false);
  });

  // 默认列前 10 条，内存里留到 50 条：「显示全部 N 条」展开，「收起」回到前 10 条。
  it('shows the first 10 changes and expands to all kept changes', async () => {
    for (let index = 0; index < 23; index += 1) recordScanSummary({ trigger: 'manual', result: { added: index + 1 } });
    const wrapper = await mountPage();
    expect(wrapper.findAll('[data-test="manage-change"]')).toHaveLength(10);
    const toggle = wrapper.get('[data-test="manage-changes-toggle"]');
    expect(toggle.text()).toBe('显示全部 23 条');
    await toggle.trigger('click');
    expect(wrapper.findAll('[data-test="manage-change"]')).toHaveLength(23);
    expect(wrapper.get('[data-test="manage-changes-toggle"]').text()).toBe('收起');
    await wrapper.get('[data-test="manage-changes-toggle"]').trigger('click');
    expect(wrapper.findAll('[data-test="manage-change"]')).toHaveLength(10);
  });
});

describe('offlineDisks', () => {
  it('merges offline directories by volume and only counts watched roots as known', () => {
    expect(volumeOf('/Volumes/Movies/a/b')).toBe('/Volumes/Movies');
    expect(volumeOf('/Users/me')).toBe('/');
    expect(offlineDisks([], null)).toEqual({ total: 0, known: 0, offlineDirectories: 0, offlineVolumes: 0 });
    expect(offlineDisks(dirs, { roots: [{ directory_id: 1, state: 'unavailable' }, { directory_id: 2, state: 'error' }] }))
      .toEqual({ total: 4, known: 1, offlineDirectories: 1, offlineVolumes: 1 });
  });
});
