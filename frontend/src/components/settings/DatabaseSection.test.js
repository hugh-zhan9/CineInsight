import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'ClearMigrationTarget', 'CreateDatabaseBackup', 'GetBackupStatus', 'GetDatabaseBackendStatus', 'GetDatabaseSwitchStatus',
  'ListDatabaseBackups', 'PreflightDatabaseSwitch', 'RelaunchApp', 'RestoreDatabaseBackup', 'RevealBackupDirectory',
  'SelectDirectory', 'StartDatabaseSwitch', 'SwitchBackendConfigOnly'
].map(name => [name, vi.fn()])));
const feedback = vi.hoisted(() => ({ confirmAction: vi.fn() }));
vi.mock('../../../wailsjs/go/main/App', () => api);
vi.mock('../../utils/feedback.js', () => feedback);

import DatabaseSection, { databaseErrorReason, restoreErrorKind } from './DatabaseSection.vue';

const wrappers = [];
let switchHandler = null;

beforeEach(() => {
  vi.resetAllMocks();
  switchHandler = null;
  window.runtime = {
    EventsOn: (name, handler) => {
      if (name === 'database-switch-state') switchHandler = handler;
      return () => {};
    }
  };
  api.GetDatabaseBackendStatus.mockResolvedValue({ backend: 'postgres', location: 'pg://localhost/cineinsight', semantic_available: true, pending_restart: false });
  api.GetDatabaseSwitchStatus.mockResolvedValue({ running: false, completed: false, failed: false });
  api.GetBackupStatus.mockResolvedValue({
    available: true, backup_available: true, restore_available: true, backup_directory: '/Users/me/Library/CineInsight/backups'
  });
  api.ListDatabaseBackups.mockResolvedValue([{ name: 'b1.db', size: 10, created_at: '2026-09-01T00:00:00Z', fingerprint: 'f1' }]);
  feedback.confirmAction.mockResolvedValue(true);
});

afterEach(() => {
  while (wrappers.length) wrappers.pop().unmount();
  delete window.runtime;
});

async function mountSection(props = {}) {
  const wrapper = mount(DatabaseSection, {
    props: { form: { backup_directory: '', backup_retention_count: 7, backup_interval_hours: 24 }, ...props },
    attachTo: document.body
  });
  wrappers.push(wrapper);
  await flushPromises();
  return wrapper;
}
const find = (wrapper, name) => wrapper.get(`[data-test="${name}"]`);
const exists = (wrapper, name) => wrapper.find(`[data-test="${name}"]`).exists();

async function preflightNonEmpty(wrapper) {
  api.PreflightDatabaseSwitch.mockResolvedValue({ target: 'sqlite', reachable: true, empty: false, location: '/data/library.db', reason_code: 'not_empty', message: '目标库不是空的（videos 有 12 行）' });
  await find(wrapper, 'db-preflight').trigger('click');
  await flushPromises();
}

describe('备份（D-PC54 / D-PC56）', () => {
  it('APP-01 显示实际备份目录并可在访达中显示', async () => {
    const wrapper = await mountSection();
    expect(find(wrapper, 'backup-directory-actual').text()).toContain('/Users/me/Library/CineInsight/backups');
    api.RevealBackupDirectory.mockResolvedValue(undefined);
    await find(wrapper, 'backup-reveal').trigger('click');
    await flushPromises();
    expect(api.RevealBackupDirectory).toHaveBeenCalledTimes(1);
  });

  it('APP-09 间隔说明写明运行期间每小时检查，不再说「只在启动时」', async () => {
    const wrapper = await mountSection();
    const help = find(wrapper, 'backup-interval-help').text();
    expect(help).toContain('每小时检查一次');
    expect(help).not.toContain('启动时自动备份');
  });

  it('APP-01 恢复成功返回成功：提示应用即将自动退出，并停用其余数据库操作', async () => {
    const wrapper = await mountSection();
    api.RestoreDatabaseBackup.mockResolvedValue(undefined);
    await find(wrapper, 'backup-restore-open').trigger('click');
    await flushPromises();
    await wrapper.findAll('button').find(button => button.text() === '恢复').trigger('click');
    await wrapper.findAll('button').find(button => button.text() === '确认恢复').trigger('click');
    await flushPromises();
    expect(find(wrapper, 'backup-message').text()).toContain('恢复成功');
    expect(find(wrapper, 'backup-message').text()).toContain('自动退出');
    expect(find(wrapper, 'backup-now').attributes('disabled')).toBeDefined();
    expect(find(wrapper, 'db-preflight').attributes('disabled')).toBeDefined();
  });

  it('APP-01 两种 SQLite 恢复失败：空间不足是普通失败，WAL 未写回是致命错误', async () => {
    expect(restoreErrorKind('准备恢复用的库文件失败，数据库未被修改: 库文件所在磁盘剩余空间不足（需要约 2 GB，可用 1 GB）').fatal).toBe(false);
    expect(restoreErrorKind('库文件旁还有尚未写回主库的 WAL 日志（可能有未落盘的提交），为免丢数据已中止恢复，库文件未被替换；请重启应用后再恢复: WAL 日志非空').fatal).toBe(true);

    const wrapper = await mountSection();
    const restore = async (error) => {
      api.RestoreDatabaseBackup.mockRejectedValueOnce(error);
      await find(wrapper, 'backup-restore-open').trigger('click');
      await flushPromises();
      await wrapper.findAll('button').find(button => button.text() === '恢复').trigger('click');
      await wrapper.findAll('button').find(button => button.text() === '确认恢复').trigger('click');
      await flushPromises();
      return find(wrapper, 'backup-message').text();
    };
    const normal = await restore('准备恢复用的库文件失败，数据库未被修改: 库文件所在磁盘剩余空间不足');
    expect(normal).toContain('当前数据库没有被改动');
    expect(find(wrapper, 'backup-now').attributes('disabled')).toBeUndefined();

    const fatal = await restore('库文件旁还有尚未写回主库的 WAL 日志，为免丢数据已中止恢复；请重启应用后再恢复');
    expect(fatal).toContain('恢复已中止，应用即将退出');
    expect(find(wrapper, 'backup-now').attributes('disabled')).toBeDefined();
  });

  it('维护期间点立即备份被拒绝时给出中文原因', async () => {
    const wrapper = await mountSection();
    api.CreateDatabaseBackup.mockRejectedValue('数据库正在恢复或切换后端，暂时不能备份');
    await find(wrapper, 'backup-now').trigger('click');
    await flushPromises();
    expect(find(wrapper, 'backup-message').text()).toBe('备份失败：数据库正在恢复或切换后端，暂时不能备份');
  });

  it('迁移进行中备份与恢复都停用并说明原因', async () => {
    const wrapper = await mountSection();
    switchHandler({ running: true, target: 'sqlite', message: '正在复制 videos', table_index: 1, table_total: 30 });
    await flushPromises();
    expect(find(wrapper, 'backup-now').attributes('disabled')).toBeDefined();
    expect(find(wrapper, 'backup-restore-open').attributes('disabled')).toBeDefined();
    expect(find(wrapper, 'backup-maintenance-hint').text()).toContain('迁移结束前');
    expect(find(wrapper, 'db-switch-running-hint').text()).toContain('拒绝写入');
  });
});

describe('后端切换（D-PC55）', () => {
  it('APP-02 文案不再说「改回去只要选回来」，改为「切回之前的后端」', async () => {
    const wrapper = await mountSection();
    expect(wrapper.text()).not.toContain('只要把后端选回来');
    expect(wrapper.text()).toContain('切回之前的后端');
  });

  it('APP-02 目标库有数据时提供「切回之前的后端」，确认文案写明改动不会带回，成功进入待重启并上报', async () => {
    const wrapper = await mountSection();
    await preflightNonEmpty(wrapper);
    expect(find(wrapper, 'db-switch-start').attributes('disabled')).toBeDefined();

    feedback.confirmAction.mockResolvedValueOnce(false);
    await find(wrapper, 'db-switch-config-only').trigger('click');
    await flushPromises();
    expect(api.SwitchBackendConfigOnly).not.toHaveBeenCalled();
    const confirm = feedback.confirmAction.mock.calls[0][0];
    expect(confirm.message).toContain('不会带回');
    expect(confirm.danger).toBe(true);

    api.SwitchBackendConfigOnly.mockResolvedValue({ target: 'sqlite', switched: true, relaunch_required: true, message: '已改为使用 sqlite，重启应用后生效。' });
    await find(wrapper, 'db-switch-config-only').trigger('click');
    await flushPromises();
    expect(api.SwitchBackendConfigOnly).toHaveBeenCalledWith('sqlite');
    expect(find(wrapper, 'db-relaunch-panel').text()).toContain('已改为使用 sqlite');
    expect(wrapper.emitted('relaunch-required')).toHaveLength(1);
    // 待重启期间其余数据库操作全部停用。
    expect(find(wrapper, 'db-preflight').attributes('disabled')).toBeDefined();
    expect(find(wrapper, 'backup-now').attributes('disabled')).toBeDefined();
    expect(exists(wrapper, 'db-switch-config-only')).toBe(false);
  });

  it('APP-02 迁移成功的事件进入待重启：立即重启调用 RelaunchApp，只上报一次', async () => {
    const wrapper = await mountSection();
    switchHandler({ running: false, completed: true, relaunch_required: true, target: 'sqlite', message: '迁移完成，请立即重启应用；重启前数据库保持只读' });
    await flushPromises();
    switchHandler({ running: false, completed: true, relaunch_required: true, target: 'sqlite', message: '迁移完成，请立即重启应用；重启前数据库保持只读' });
    await flushPromises();
    expect(wrapper.emitted('relaunch-required')).toHaveLength(1);
    expect(find(wrapper, 'db-relaunch-panel').text()).toContain('重启前数据库保持只读');

    api.RelaunchApp.mockResolvedValue({ relaunched: false, message: '当前不是从应用包运行，应用将退出，请手动重新打开' });
    await find(wrapper, 'db-relaunch-now').trigger('click');
    await flushPromises();
    expect(api.RelaunchApp).toHaveBeenCalledTimes(1);
    expect(find(wrapper, 'db-relaunch-result').text()).toContain('请手动重新打开');
    // 已经在退出了，不能再点第二次。
    expect(find(wrapper, 'db-relaunch-now').attributes('disabled')).toBeDefined();
  });

  it('APP-02 页面重新挂载时补读到迁移已完成，照样进入待重启', async () => {
    api.GetDatabaseSwitchStatus.mockResolvedValue({ running: false, completed: true, relaunch_required: true, target: 'sqlite', message: '迁移完成，请立即重启应用' });
    const wrapper = await mountSection();
    expect(exists(wrapper, 'db-relaunch-panel')).toBe(true);
    expect(wrapper.emitted('relaunch-required')).toHaveLength(1);
  });

  it('APP-02 relaunch_pending 前缀的拒绝按待重启处理，backend_env_locked 给出说明', async () => {
    expect(databaseErrorReason('relaunch_pending: 数据库后端已切换完成，请先重启应用')).toEqual({ code: 'relaunch_pending', text: '数据库后端已切换完成，请先重启应用' });
    expect(databaseErrorReason(new Error('backend_env_locked: 当前后端由环境变量 DB_BACKEND 指定')).code).toBe('backend_env_locked');
    expect(databaseErrorReason('普通错误').code).toBe('');

    const wrapper = await mountSection();
    api.PreflightDatabaseSwitch.mockResolvedValue({ target: 'sqlite', reachable: true, empty: true, message: '目标可用，可以开始迁移' });
    await find(wrapper, 'db-preflight').trigger('click');
    await flushPromises();
    api.StartDatabaseSwitch.mockRejectedValueOnce('backend_env_locked: 当前后端由环境变量 DB_BACKEND 指定，应用内切换在重启后不会生效');
    await find(wrapper, 'db-switch-start').trigger('click');
    await flushPromises();
    expect(find(wrapper, 'db-switch-notice').text()).toContain('DB_BACKEND');
    expect(find(wrapper, 'db-switch-notice').text()).not.toContain('backend_env_locked');
    expect(exists(wrapper, 'db-relaunch-panel')).toBe(false);

    api.StartDatabaseSwitch.mockRejectedValueOnce('relaunch_pending: 数据库后端已切换完成，请先重启应用；重启前不能恢复备份或再次切换后端');
    await find(wrapper, 'db-switch-start').trigger('click');
    await flushPromises();
    expect(find(wrapper, 'db-relaunch-panel').text()).toContain('请先重启应用');
    expect(find(wrapper, 'db-relaunch-panel').text()).not.toContain('relaunch_pending');
    expect(wrapper.emitted('relaunch-required')).toHaveLength(1);
  });

  it('APP-02 预检报 backend_env_locked 时不给迁移与切回，只说明怎么改', async () => {
    const wrapper = await mountSection();
    api.PreflightDatabaseSwitch.mockResolvedValue({ target: 'sqlite', reachable: false, empty: false, reason_code: 'backend_env_locked', message: 'x' });
    await find(wrapper, 'db-preflight').trigger('click');
    await flushPromises();
    expect(find(wrapper, 'db-preflight-result').text()).toContain('DB_BACKEND');
    expect(find(wrapper, 'db-switch-start').attributes('disabled')).toBeDefined();
    expect(exists(wrapper, 'db-switch-config-only')).toBe(false);
    expect(exists(wrapper, 'db-clear-target')).toBe(false);
  });

  it('APP-02 迁移失败显示目标库位置；清空目标库必须手输「清空」才放行', async () => {
    const wrapper = await mountSection();
    switchHandler({ running: false, failed: true, target: 'sqlite', location: '/data/library.db', message: '复制 videos 失败' });
    await flushPromises();
    expect(find(wrapper, 'db-switch-progress').text()).toContain('迁移失败');
    expect(find(wrapper, 'db-failed-location').text()).toContain('/data/library.db');

    await find(wrapper, 'db-clear-target').trigger('click');
    await flushPromises();
    expect(find(wrapper, 'db-clear-dialog').text()).toContain('/data/library.db');
    const confirm = () => find(wrapper, 'db-clear-confirm');
    expect(confirm().attributes('disabled')).toBeDefined();
    await find(wrapper, 'db-clear-confirm-input').setValue('清 空');
    expect(confirm().attributes('disabled')).toBeDefined();
    await find(wrapper, 'db-clear-confirm-input').setValue('清空');
    expect(confirm().attributes('disabled')).toBeUndefined();

    api.ClearMigrationTarget.mockResolvedValue({ target: 'sqlite', cleared: true, removed: ['library.db'], message: '已删除目标库文件，可以重新迁移' });
    await confirm().trigger('click');
    await flushPromises();
    expect(api.ClearMigrationTarget).toHaveBeenCalledWith('sqlite', '清空');
    expect(exists(wrapper, 'db-clear-dialog')).toBe(false);
    expect(find(wrapper, 'db-switch-notice').text()).toContain('已清空目标库');
    expect(exists(wrapper, 'db-failed-location')).toBe(false);
  });

  it('APP-02 清空被后端拒绝时留在对话框里说明原因', async () => {
    const wrapper = await mountSection();
    await preflightNonEmpty(wrapper);
    await find(wrapper, 'db-clear-target').trigger('click');
    await find(wrapper, 'db-clear-confirm-input').setValue('清空');
    api.ClearMigrationTarget.mockResolvedValue({ target: 'sqlite', cleared: false, removed: [], reason_code: 'outside_data_dir', message: '目标库文件不在应用数据目录内，不会自动删除；请手动处理' });
    await find(wrapper, 'db-clear-confirm').trigger('click');
    await flushPromises();
    expect(find(wrapper, 'db-clear-error').text()).toContain('不在应用数据目录内');
  });

  it('APP-02 切回时目标库残留半迁移，给出清空入口', async () => {
    const wrapper = await mountSection();
    await preflightNonEmpty(wrapper);
    api.SwitchBackendConfigOnly.mockResolvedValue({ target: 'sqlite', switched: false, reason_code: 'target_half_migrated', message: '目标库残留着一次未完成的迁移，不能直接切回' });
    await find(wrapper, 'db-switch-config-only').trigger('click');
    await flushPromises();
    expect(find(wrapper, 'db-switch-notice').text()).toContain('未完成的迁移');
    expect(exists(wrapper, 'db-clear-target')).toBe(true);
    expect(exists(wrapper, 'db-relaunch-panel')).toBe(false);
  });
});
