import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'UpdateSettings', 'SelectDirectory', 'GetAllDirectories', 'AddDirectory', 'UpdateDirectory', 'DeleteDirectory',
  'GetShortFeedServerStatus', 'GetAITagLibrary', 'SaveAITagLibrary', 'ClearAITagLibrary', 'TriggerAITagging',
  'GetLibraryWatcherStatus', 'RetryLibraryWatcherRoot', 'GetBackupStatus', 'ListDatabaseBackups',
  'CreateDatabaseBackup', 'RestoreDatabaseBackup', 'GetSemanticIndexStatus', 'StartSemanticIndex', 'CancelSemanticIndex',
  'GetAllImageDirectories', 'AddImageDirectory', 'UpdateImageDirectory', 'DeleteImageDirectory',
  'GetImageAITaggingStatus', 'StartImageAITagging', 'CancelImageAITagging',
  'GetImageSemanticIndexStatus', 'StartImageSemanticIndex', 'CancelImageSemanticIndex',
  'GetImageEXIFBackfillStatus', 'StartImageEXIFBackfill', 'CancelImageEXIFBackfill'
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
}, imageTasks = {}) {
  api.GetShortFeedServerStatus.mockResolvedValue({ running: false });
  if (imageTasks.aiTagLibraryError) {
	api.GetAITagLibrary.mockRejectedValue(imageTasks.aiTagLibraryError);
  } else {
	api.GetAITagLibrary.mockResolvedValue(imageTasks.aiTags || []);
  }
  api.GetLibraryWatcherStatus.mockResolvedValue(status);
  api.SaveAITagLibrary.mockResolvedValue([]);
  api.ClearAITagLibrary.mockResolvedValue([]);
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
  api.GetAllImageDirectories.mockResolvedValue([{ id: 3, alias: '相册', path: '/media/photos' }]);
  api.AddImageDirectory.mockResolvedValue({ id: 4, alias: '', path: '/media/raw' });
  api.UpdateImageDirectory.mockResolvedValue();
  api.DeleteImageDirectory.mockResolvedValue();
  api.GetImageAITaggingStatus.mockResolvedValue(
    imageTasks.description || { running: false, completed: false, total: 0, processed: 0, failures: [] }
  );
  api.GetImageSemanticIndexStatus.mockResolvedValue(
    imageTasks.semantic || { available: true, running: false, completed: false, processed: 0, total: 0, failures: [] }
  );
  api.StartImageAITagging.mockResolvedValue({ running: true, total: 5, processed: 0, failures: [] });
  api.CancelImageAITagging.mockResolvedValue();
  api.StartImageSemanticIndex.mockResolvedValue({ available: true, running: true, total: 5, processed: 0, failures: [] });
  api.CancelImageSemanticIndex.mockResolvedValue();
  api.GetImageEXIFBackfillStatus.mockResolvedValue(
    imageTasks.exif || { running: false, completed: false, total: 0, processed: 0, failures: [] }
  );
  api.StartImageEXIFBackfill.mockResolvedValue({ running: true, total: 4, processed: 0, failures: [] });
  api.CancelImageEXIFBackfill.mockResolvedValue();
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
	it('blocks all settings saves when the AI tag library failed to load', async () => {
	  const wrapper = await mountPage(undefined, { aiTagLibraryError: 'database unavailable' });

	  expect(wrapper.get('.settings-save-button').attributes('disabled')).toBeDefined();
	  expect(wrapper.get('[data-test="reload-ai-tag-library"]').text()).toContain('重新加载');
	  await wrapper.vm.saveSettings();
	  await flushPromises();

	  expect(api.SaveAITagLibrary).not.toHaveBeenCalled();
	  expect(api.ClearAITagLibrary).not.toHaveBeenCalled();
	  expect(api.UpdateSettings).not.toHaveBeenCalled();
	  expect(wrapper.text()).toContain('AI 标签库尚未成功加载');
	});

	it('requires explicit confirmation and the dedicated API to clear a loaded library', async () => {
	  const wrapper = await mountPage(undefined, {
		aiTags: [{ id: 7, namespace: '行为', name: '动作', color: '#123456', is_active: true }]
	  });
	  wrapper.vm.localAITagGroups = [];
	  await wrapper.vm.$nextTick();
	  const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(true);

	  await wrapper.get('.settings-save-button').trigger('click');
	  await flushPromises();

	  expect(api.ClearAITagLibrary).toHaveBeenCalledTimes(1);
	  expect(api.SaveAITagLibrary).not.toHaveBeenCalled();
	  expect(api.UpdateSettings).toHaveBeenCalledTimes(1);
	  confirm.mockRestore();
	});

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

describe('SettingsPage image library', () => {
  it('lists image scan directories independently from video directories', async () => {
    const wrapper = await mountPage();

    expect(api.GetAllImageDirectories).toHaveBeenCalledTimes(1);
    const items = wrapper.findAll('[data-test="image-directory-item"]');
    expect(items).toHaveLength(1);
    expect(items[0].text()).toContain('相册');
    expect(items[0].text()).toContain('/media/photos');
  });

  it('adds an image directory through the dialog', async () => {
    const wrapper = await mountPage();
    api.SelectDirectory.mockResolvedValue('/media/raw');

    await wrapper.get('[data-test="add-image-directory"]').trigger('click');
    const dialogButtons = wrapper.findAll('button').filter(item => item.text() === '选择');
    await dialogButtons[dialogButtons.length - 1].trigger('click');
    await flushPromises();
    await wrapper.get('[data-test="image-directory-alias"]').setValue('原片');
    await wrapper.get('[data-test="save-image-directory"]').trigger('click');
    await flushPromises();

    expect(api.AddImageDirectory).toHaveBeenCalledWith('/media/raw', '原片');
    expect(api.AddDirectory).not.toHaveBeenCalled();
    expect(api.GetAllImageDirectories).toHaveBeenCalledTimes(2);
  });

  it('edits and deletes image directories via the image APIs', async () => {
    const wrapper = await mountPage();
    const item = wrapper.get('[data-test="image-directory-item"]');

    await item.findAll('button').find(button => button.text() === '编辑').trigger('click');
    await wrapper.get('[data-test="image-directory-alias"]').setValue('相册2');
    await wrapper.get('[data-test="save-image-directory"]').trigger('click');
    await flushPromises();
    expect(api.UpdateImageDirectory).toHaveBeenCalledWith(3, '/media/photos', '相册2');

    const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(true);
    await wrapper.get('[data-test="image-directory-item"]').findAll('button').find(button => button.text() === '删除').trigger('click');
    await flushPromises();
    expect(api.DeleteImageDirectory).toHaveBeenCalledWith(3);
    expect(api.DeleteDirectory).not.toHaveBeenCalled();
    confirm.mockRestore();
  });

  it('persists image_extensions with the full settings payload', async () => {
    const wrapper = await mountPage();

    await wrapper.get('[data-test="image-extensions"]').setValue('.jpg,.png,.heic');
    await wrapper.find('.settings-save-button').trigger('click');
    await flushPromises();

    expect(api.UpdateSettings).toHaveBeenCalledWith(expect.objectContaining({
      image_extensions: '.jpg,.png,.heic',
      video_extensions: '.mp4'
    }));
  });

  it('keeps image_extensions empty so the backend default list applies', async () => {
    const wrapper = await mountPage();

    expect(wrapper.get('[data-test="image-extensions"]').element.value).toBe('');
    await wrapper.find('.settings-save-button').trigger('click');
    await flushPromises();

    expect(api.UpdateSettings).toHaveBeenCalledWith(expect.objectContaining({ image_extensions: '' }));
  });
});

describe('SettingsPage image AI task panels', () => {
  const watcher = () => ({ running: true, roots: [] });

  it('renders both image task panels next to the video semantic index panel', async () => {
    const wrapper = await mountPage();

    expect(api.GetImageAITaggingStatus).toHaveBeenCalledTimes(1);
    expect(api.GetImageSemanticIndexStatus).toHaveBeenCalledTimes(1);
    expect(wrapper.get('[data-test="image-ai-tagging-status"]').text()).toContain('图片打标任务未运行');
    expect(wrapper.get('[data-test="image-semantic-index-status"]').text()).toContain('图片语义索引可用，尚未构建');
    expect(wrapper.find('.semantic-index-controls').exists()).toBe(true);
  });

  it('starts and cancels the image AI description task', async () => {
    const wrapper = await mountPage();

    expect(wrapper.find('[data-test="image-ai-tagging-cancel"]').exists()).toBe(false);
    await wrapper.get('[data-test="image-ai-tagging-start"]').trigger('click');
    await flushPromises();

    expect(api.StartImageAITagging).toHaveBeenCalledTimes(1);
    expect(wrapper.get('[data-test="image-ai-tagging-status"]').text()).toContain('图片打标进行中');
    expect(wrapper.get('[data-test="image-ai-tagging-status"]').text()).toContain('进度 0/5');
    expect(wrapper.get('[data-test="image-ai-tagging-start"]').attributes('disabled')).toBeDefined();

    api.GetImageAITaggingStatus.mockResolvedValue({ running: false, cancelled: true, total: 5, processed: 2, failures: [] });
    await wrapper.get('[data-test="image-ai-tagging-cancel"]').trigger('click');
    await flushPromises();

    expect(api.CancelImageAITagging).toHaveBeenCalledTimes(1);
    expect(wrapper.get('[data-test="image-ai-tagging-status"]').text()).toContain('图片打标任务已取消');
    wrapper.unmount();
  });

  it('surfaces an unavailable AI configuration when the description task cannot start', async () => {
    const wrapper = await mountPage();
    api.StartImageAITagging.mockRejectedValueOnce('AI 配置不可用: BaseURL 或 Model 为空');

    await wrapper.get('[data-test="image-ai-tagging-start"]').trigger('click');
    await flushPromises();

    const error = wrapper.get('[data-test="image-ai-tagging-error"]');
    expect(error.text()).toContain('AI 配置不可用');
    expect(error.text()).toContain('请先在上方配置 AI 接口');
  });

  it('renders description failures returned by the status snapshot', async () => {
    const wrapper = await mountPage(watcher(), {
      description: {
        running: false,
        completed: true,
        total: 3,
        processed: 3,
        succeeded: 1,
        skipped: 1,
        failed: 1,
        failures: [{ image_id: 9, name: 'raw.nef', code: 'decode_unsupported', error: '无法解码' }]
      }
    });

    expect(wrapper.get('[data-test="image-ai-tagging-status"]').text()).toContain('图片打标任务已完成');
    expect(wrapper.get('[data-test="image-ai-tagging-failures"]').text()).toContain('raw.nef');
    expect(wrapper.get('[data-test="image-ai-tagging-failures"]').text()).toContain('decode_unsupported');
  });

  it('starts and cancels the image semantic index task', async () => {
    const wrapper = await mountPage();

    await wrapper.get('[data-test="image-semantic-index-start"]').trigger('click');
    await flushPromises();

    expect(api.StartImageSemanticIndex).toHaveBeenCalledTimes(1);
    expect(api.StartImageSemanticIndex).toHaveBeenCalledWith();
    expect(wrapper.get('[data-test="image-semantic-index-status"]').text()).toContain('图片语义索引构建中');

    api.GetImageSemanticIndexStatus.mockResolvedValue({ available: true, running: false, cancelled: true, total: 5, processed: 1, failures: [] });
    await wrapper.get('[data-test="image-semantic-index-cancel"]').trigger('click');
    await flushPromises();

    expect(api.CancelImageSemanticIndex).toHaveBeenCalledTimes(1);
    expect(wrapper.get('[data-test="image-semantic-index-status"]').text()).toContain('构建已取消');
    wrapper.unmount();
  });

  it('disables the semantic index start button and shows why it is unavailable', async () => {
    const wrapper = await mountPage(watcher(), {
      semantic: { available: false, running: false, unavailable: 'pgvector 扩展不可用', failures: [] }
    });

    expect(wrapper.get('[data-test="image-semantic-index-status"]').text()).toContain('图片语义索引不可用');
    expect(wrapper.get('[data-test="image-semantic-index-status"]').text()).toContain('pgvector 扩展不可用');
    expect(wrapper.get('[data-test="image-semantic-index-start"]').attributes('disabled')).toBeDefined();
  });

  it('warns that a shared model change made the image index stale', async () => {
    const wrapper = await mountPage(watcher(), {
      semantic: { available: true, running: false, needs_rebuild: true, model: 'text-embedding-3-small', dimension: 1536, failures: [] }
    });

    expect(wrapper.get('[data-test="image-semantic-rebuild-hint"]').text()).toContain('需要重新运行本任务');
    expect(wrapper.get('[data-test="image-semantic-index-status"]').text()).toContain('text-embedding-3-small');
  });

  it('subscribes to all image task events and unbinds them on unmount', async () => {
    const handlers = {};
    const off = { tagging: vi.fn(), semantic: vi.fn(), exif: vi.fn() };
    window.runtime = {
      EventsOn: vi.fn((event, handler) => {
        handlers[event] = handler;
        if (event === 'image-ai-tagging-progress') return off.tagging;
        if (event === 'image-semantic-index-state') return off.semantic;
        if (event === 'image-exif-backfill-progress') return off.exif;
        return () => {};
      })
    };

    const wrapper = await mountPage();

    expect(typeof handlers['image-ai-tagging-progress']).toBe('function');
    expect(typeof handlers['image-semantic-index-state']).toBe('function');
    expect(typeof handlers['image-exif-backfill-progress']).toBe('function');

    handlers['image-ai-tagging-progress']({ running: true, total: 8, processed: 3, succeeded: 3, failures: [] });
    handlers['image-semantic-index-state']({ available: true, running: true, total: 8, processed: 4, failures: [] });
    handlers['image-exif-backfill-progress']({ running: true, total: 8, processed: 5, succeeded: 5, failures: [] });
    await wrapper.vm.$nextTick();

    expect(wrapper.get('[data-test="image-ai-tagging-status"]').text()).toContain('进度 3/8');
    expect(wrapper.get('[data-test="image-semantic-index-status"]').text()).toContain('进度 4/8');
    expect(wrapper.get('[data-test="image-exif-backfill-status"]').text()).toContain('进度 5/8');

    wrapper.unmount();
    expect(off.tagging).toHaveBeenCalledTimes(1);
    expect(off.semantic).toHaveBeenCalledTimes(1);
    expect(off.exif).toHaveBeenCalledTimes(1);
  });

  it('starts and cancels the image EXIF backfill task', async () => {
    const wrapper = await mountPage();

    expect(api.GetImageEXIFBackfillStatus).toHaveBeenCalledTimes(1);
    expect(wrapper.get('[data-test="image-exif-backfill-status"]').text()).toContain('EXIF 补全任务未运行');
    expect(wrapper.find('[data-test="image-exif-backfill-cancel"]').exists()).toBe(false);

    await wrapper.get('[data-test="image-exif-backfill-start"]').trigger('click');
    await flushPromises();

    expect(api.StartImageEXIFBackfill).toHaveBeenCalledTimes(1);
    const running = wrapper.get('[data-test="image-exif-backfill-status"]').text();
    expect(running).toContain('EXIF 补全中');
    expect(running).toContain('进度 0/4');
    expect(wrapper.get('[data-test="image-exif-backfill-start"]').attributes('disabled')).toBeDefined();

    api.GetImageEXIFBackfillStatus.mockResolvedValue({ running: false, cancelled: true, total: 4, processed: 2, failures: [] });
    await wrapper.get('[data-test="image-exif-backfill-cancel"]').trigger('click');
    await flushPromises();

    expect(api.CancelImageEXIFBackfill).toHaveBeenCalledTimes(1);
    expect(wrapper.get('[data-test="image-exif-backfill-status"]').text()).toContain('EXIF 补全任务已取消');
    wrapper.unmount();
  });

  it('renders EXIF backfill counters and failures from the status snapshot', async () => {
    const wrapper = await mountPage(watcher(), {
      exif: {
        running: false,
        completed: true,
        total: 3,
        processed: 3,
        succeeded: 1,
        skipped: 1,
        failed: 1,
        failures: [{ image_id: 12, name: 'gone.jpg', error: '文件不存在' }]
      }
    });

    const status = wrapper.get('[data-test="image-exif-backfill-status"]').text();
    expect(status).toContain('EXIF 补全任务已完成');
    expect(status).toContain('有 EXIF 1');
    expect(status).toContain('无 EXIF 1');
    expect(wrapper.get('[data-test="image-exif-backfill-failures"]').text()).toContain('gone.jpg');
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

describe('SettingsPage 锚点导航', () => {
  it('13 个分区各有锚点，导航项与分区一一对应', async () => {
    const wrapper = await mountPage();
    const keys = wrapper.vm.navSections.map(section => section.key);
    expect(keys).toHaveLength(13);
    for (const key of keys) {
      expect(wrapper.find(`#settings-${key}`).exists()).toBe(true);
    }
    expect(wrapper.findAll('.settings-nav__item')).toHaveLength(13);
    wrapper.unmount();
  });

  it('点击导航项高亮并滚到对应分区', async () => {
    const wrapper = await mountPage();
    const target = wrapper.find('#settings-scan-dirs').element;
    target.scrollIntoView = vi.fn();
    wrapper.vm.scrollToSection('scan-dirs');
    expect(wrapper.vm.activeSection).toBe('scan-dirs');
    expect(target.scrollIntoView).toHaveBeenCalled();
    wrapper.unmount();
  });
});
