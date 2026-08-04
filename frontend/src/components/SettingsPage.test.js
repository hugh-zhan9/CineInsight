import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'UpdateSettings', 'SelectDirectory', 'GetAllDirectories', 'AddDirectory', 'UpdateDirectory', 'DeleteDirectory',
  'GetShortFeedServerStatus', 'GetAITagLibrary', 'SaveAITagLibrary', 'TriggerAITagging',
  'GetLibraryWatcherStatus', 'RetryLibraryWatcherRoot', 'GetBackupStatus', 'ListDatabaseBackups',
  'CreateDatabaseBackup', 'RestoreDatabaseBackup', 'GetSemanticIndexStatus', 'StartSemanticIndex', 'CancelSemanticIndex'
].map(name => [name, vi.fn()])));

vi.mock('../../wailsjs/go/main/App', () => api);

import SettingsPage from './SettingsPage.vue';

const baseSettings = () => ({
  video_extensions: '.mp4',
  play_weight: 2,
  random_half_life_days: 90,
  auto_scan_on_startup: false,
  library_watch_enabled: true,
  local_metadata_enabled: true,
  ai_quality_enabled: true,
  short_feed_max_duration_minutes: 5,
	short_feed_feedback_sync_enabled: true,
  theme: 'system',
  subtitle_translation_provider: 'deepl',
  subtitle_whisperx_model: 'medium',
  subtitle_whisperx_batch_size: 8,
  semantic_embedding_model: 'text-embedding-test',
  backup_directory: '',
  backup_retention_count: 7,
  backup_interval_hours: 24
});

async function mountPage(status = {
  running: true,
  roots: [{ directory_id: 7, state: 'watching', message: '实时同步中', watch_count: 3 }]
}) {
  api.GetShortFeedServerStatus.mockResolvedValue({ running: false });
  api.GetAITagLibrary.mockResolvedValue([]);
  api.GetLibraryWatcherStatus.mockResolvedValue(status);
  api.SaveAITagLibrary.mockResolvedValue([]);
  api.UpdateSettings.mockResolvedValue();
  api.TriggerAITagging.mockResolvedValue(false);
  api.GetBackupStatus.mockResolvedValue({
    available: true,
    backup_available: true,
    restore_available: true,
    backup_directory: '/data/backups',
    retention_count: 7,
    interval_hours: 24
  });
  api.ListDatabaseBackups.mockResolvedValue([]);
  api.CreateDatabaseBackup.mockResolvedValue({ name: 'cineinsight-now.dump', size: 100, created_at: '2026-08-04T12:00:00Z' });
  api.RestoreDatabaseBackup.mockResolvedValue();
  api.GetSemanticIndexStatus.mockResolvedValue({ available: true, running: false, completed: false, processed: 0, total: 0 });
  const wrapper = mount(SettingsPage, {
    props: {
      settings: baseSettings(),
      directories: [{ id: 7, alias: '电影', path: '/media/movies' }]
    }
  });
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  vi.clearAllMocks();
  delete window.runtime;
});

describe('SettingsPage library watcher', () => {
	it('starts and explicitly confirms semantic index rebuilds', async () => {
	  const wrapper = await mountPage();
	  api.StartSemanticIndex.mockResolvedValue({ available: true, running: true, processed: 0, total: 3 });
	  const buttons = wrapper.findAll('.semantic-index-controls button');
	  await buttons[0].trigger('click');
	  expect(api.StartSemanticIndex).toHaveBeenCalledWith({ rebuild: false });

	  wrapper.vm.semanticIndexStatus = { available: true, running: false };
	  await wrapper.vm.$nextTick();
	  const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(true);
	  await wrapper.findAll('.semantic-index-controls button')[1].trigger('click');
	  expect(api.StartSemanticIndex).toHaveBeenLastCalledWith({ rebuild: true });
	  confirm.mockRestore();
	});

	it('opens keyboard shortcut help from settings', async () => {
	  const wrapper = await mountPage();
	  await wrapper.get('[data-test="shortcut-help-button"]').trigger('click');
	  expect(wrapper.text()).toContain('选择下一个视频');
	  expect(wrapper.text()).toContain('输入框、下拉框和弹窗处于焦点时不会触发快捷键');
	});

  it('shows per-root watching state', async () => {
    const wrapper = await mountPage();

    expect(api.GetLibraryWatcherStatus).toHaveBeenCalled();
    expect(wrapper.text()).toContain('实时同步中（3 个目录）');
    expect(wrapper.find('[data-test="retry-library-watch-7"]').exists()).toBe(false);
  });

  it('shows failures and retries only the affected root', async () => {
    const wrapper = await mountPage({
      running: true,
      roots: [{ directory_id: 7, state: 'unavailable', message: '扫描目录当前不可用', watch_count: 0 }]
    });
    api.RetryLibraryWatcherRoot.mockResolvedValue({ directory_id: 7, state: 'watching' });

    await wrapper.find('[data-test="retry-library-watch-7"]').trigger('click');
    await flushPromises();

    expect(api.RetryLibraryWatcherRoot).toHaveBeenCalledWith(7);
    expect(api.GetLibraryWatcherStatus).toHaveBeenCalledTimes(2);
  });

  it('persists the global watcher switch', async () => {
    const wrapper = await mountPage();
    await wrapper.find('[data-test="library-watch-toggle"]').setValue(false);
    await wrapper.find('.settings-save-button').trigger('click');
    await flushPromises();

    expect(api.UpdateSettings).toHaveBeenCalledWith(expect.objectContaining({ library_watch_enabled: false }));
    expect(wrapper.text()).toContain('实时同步已关闭');
  });

	it('persists the short-feed feedback switch', async () => {
	  const wrapper = await mountPage();
	  await wrapper.find('[data-test="short-feed-feedback-sync-toggle"]').setValue(false);
	  await wrapper.find('.settings-save-button').trigger('click');
	  await flushPromises();

	  expect(api.UpdateSettings).toHaveBeenCalledWith(expect.objectContaining({ short_feed_feedback_sync_enabled: false }));
	});

  it('persists independent local workflow switches', async () => {
    const wrapper = await mountPage();
    await wrapper.find('[data-test="local-metadata-toggle"]').setValue(false);
    await wrapper.find('[data-test="ai-quality-toggle"]').setValue(false);
    await wrapper.find('.settings-save-button').trigger('click');
    await flushPromises();

    expect(api.UpdateSettings).toHaveBeenCalledWith(expect.objectContaining({
      local_metadata_enabled: false,
      ai_quality_enabled: false,
      semantic_embedding_model: 'text-embedding-test',
    }));
  });
});

describe('SettingsPage database backup', () => {
  it('creates a backup and exposes the result', async () => {
    const wrapper = await mountPage();
    const button = wrapper.findAll('button').find(item => item.text() === '立即备份');
    await button.trigger('click');
    await flushPromises();

    expect(api.CreateDatabaseBackup).toHaveBeenCalledTimes(1);
    expect(wrapper.text()).toContain('备份成功：cineinsight-now.dump');
  });

  it('requires selecting a listed backup and a second confirmation before restore', async () => {
    const wrapper = await mountPage();
    api.ListDatabaseBackups.mockResolvedValue([{
      name: 'cineinsight-20260804.dump',
      size: 2048,
      created_at: '2026-08-04T12:00:00Z',
      fingerprint: 'confirmed-hash'
    }]);
    const openButton = wrapper.findAll('button').find(item => item.text() === '从备份恢复');
    await openButton.trigger('click');
    await flushPromises();

    expect(api.RestoreDatabaseBackup).not.toHaveBeenCalled();
    const selectButton = wrapper.findAll('button').find(item => item.text() === '恢复');
    await selectButton.trigger('click');
    expect(api.RestoreDatabaseBackup).not.toHaveBeenCalled();

    const confirmButton = wrapper.findAll('button').find(item => item.text() === '确认恢复');
    await confirmButton.trigger('click');
    await flushPromises();

    expect(api.RestoreDatabaseBackup).toHaveBeenCalledWith({
      name: 'cineinsight-20260804.dump',
      size: 2048,
      fingerprint: 'confirmed-hash'
    });
  });
});
