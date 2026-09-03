import { flushPromises, mount } from '@vue/test-utils';

// 应用内确认框取代了失效的 window.confirm：默认答"确定"，需要"取消"的用例单独覆盖。
const feedback = vi.hoisted(() => ({
  confirmAction: vi.fn(() => Promise.resolve(true)),
  notify: vi.fn(),
  notifyError: vi.fn(),
  notifySuccess: vi.fn()
}));
vi.mock('../utils/feedback.js', async (importOriginal) => ({
  ...(await importOriginal()),
  ...feedback
}));
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'GetDatabaseBackendStatus', 'PreflightDatabaseSwitch', 'StartDatabaseSwitch',
  'UpdateSettings', 'SelectDirectory', 'GetAllDirectories', 'AddDirectory', 'UpdateDirectory', 'DeleteDirectory',
  'GetShortFeedServerStatus', 'GetAITagLibrary', 'SaveAITagLibrary', 'ClearAITagLibrary', 'TriggerAITagging',
  'GetEnhancementCapability', 'GetEnhancementModelStatus', 'StartEnhancementModelDownload', 'CancelEnhancementModelDownload',
  'GetLibraryWatcherStatus', 'RetryLibraryWatcherRoot', 'GetBackupStatus', 'ListDatabaseBackups',
  'CreateDatabaseBackup', 'RestoreDatabaseBackup', 'GetSemanticIndexStatus', 'StartSemanticIndex', 'CancelSemanticIndex',
  'GetAllImageDirectories', 'AddImageDirectory', 'UpdateImageDirectory', 'DeleteImageDirectory',
  'GetImageAITaggingStatus', 'StartImageAITagging', 'CancelImageAITagging',
  'GetImageSemanticIndexStatus', 'StartImageSemanticIndex', 'CancelImageSemanticIndex',
  'GetImageEXIFBackfillStatus', 'StartImageEXIFBackfill', 'CancelImageEXIFBackfill',
  'ListGlossaryEntries', 'UpsertGlossaryEntry', 'DeleteGlossaryEntry',
  'GetBrowserBridgeStatus', 'RegenerateBrowserBridgeToken', 'SelectBrowserDownloadDirectory',
  'ListBrowserDownloadTasks', 'CancelBrowserDownloadTask'
].map(name => [name, vi.fn()])));

vi.mock('../../wailsjs/go/main/App', () => api);

import SettingsPage, { SETTINGS_SECTIONS } from './SettingsPage.vue';
import { commandList } from '../utils/commandRegistry.js';

// P-002 把设置分区拆成 components/settings/** 的子组件；分区自己持有的状态与方法
// 通过 findComponent 深入取用，断言本身不变。
const sectionVm = (wrapper, name) => wrapper.findComponent({ name }).vm;

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

	  sectionVm(wrapper, 'AITagSection').semanticIndexStatus = { available: true, running: false };
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
    // 直接定位图片目录弹窗里那一行，别再靠「页面上最后一个『选择』」这种顺序假设。
    const imageDialogRow = wrapper.findAll('.directory-dialog-row')
      .find(row => row.find('[data-test="image-directory-path"]').exists());
    await imageDialogRow.find('button').trigger('click');
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
  // 这条断言必须比对"id 与它下面的标题是否一致"，只查 id 存在是不够的——
  // 最初的实现就是因为 settings-section-heading 的类名以 settings-section 开头
  // 被误匹配，吃掉两个 id 让后面全体错位：点「扫描目录管理」滚到了「字幕翻译」。
  it('每个锚点都落在标题相符的分区上', async () => {
    const wrapper = await mountPage();
    const keys = wrapper.vm.navSections.map(section => section.key);
    expect(wrapper.findAll('.settings-nav__item')).toHaveLength(keys.length);

    for (const section of wrapper.vm.navSections) {
      const element = wrapper.find(`#settings-${section.key}`);
      expect(element.exists()).toBe(true);
      const heading = element.find('h3');
      expect(heading.exists()).toBe(true);
      expect(heading.text()).toBe(section.label);
    }
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

describe('SettingsPage 数据库后端切换', () => {
  it('展示当前后端、库位置与语义检索可用性', async () => {
    api.GetDatabaseBackendStatus.mockResolvedValue({
      backend: 'sqlite', location: '/home/me/.video-master/library.db',
      semantic_available: false, semantic_reason: '语义向量检索需要 PostgreSQL pgvector',
      pending_restart: false
    });
    const wrapper = await mountPage();
    expect(wrapper.find('[data-test="db-backend"]').text()).toBe('SQLite');
    expect(wrapper.text()).toContain('/home/me/.video-master/library.db');
    expect(wrapper.text()).toContain('pgvector');
    // 目标默认选另一个后端——选中当前后端没有意义。
    expect(sectionVm(wrapper, 'DatabaseSection').switchTarget).toBe('postgres');
    wrapper.unmount();
  });

  it('目标库非空时不允许开始迁移', async () => {
    const wrapper = await mountPage();
    api.PreflightDatabaseSwitch.mockResolvedValue({
      target: 'sqlite', reachable: true, empty: false,
      reason_code: 'not_empty', message: '目标库不是空的（videos 有 12 行）'
    });
    await sectionVm(wrapper, 'DatabaseSection').preflightDatabaseSwitch();
    await wrapper.vm.$nextTick();

    expect(wrapper.find('[data-test="db-preflight-result"]').text()).toContain('不是空的');
    expect(wrapper.find('[data-test="db-switch-start"]').attributes('disabled')).toBeDefined();
    wrapper.unmount();
  });

  it('预检通过后才放开迁移按钮', async () => {
    const wrapper = await mountPage();
    expect(wrapper.find('[data-test="db-switch-start"]').attributes('disabled')).toBeDefined();

    api.PreflightDatabaseSwitch.mockResolvedValue({
      target: 'sqlite', reachable: true, empty: true, message: '目标可用，可以开始迁移'
    });
    await sectionVm(wrapper, 'DatabaseSection').preflightDatabaseSwitch();
    await wrapper.vm.$nextTick();
    expect(wrapper.find('[data-test="db-switch-start"]').attributes('disabled')).toBeUndefined();
    wrapper.unmount();
  });

  it('迁移进度按表上报，失败与完成各有明确文案', async () => {
    const wrapper = await mountPage();
    sectionVm(wrapper, 'DatabaseSection').switchStatus = { running: true, message: '正在复制 videos', table_index: 3, table_total: 38 };
    await wrapper.vm.$nextTick();
    expect(sectionVm(wrapper, 'DatabaseSection').switchProgressText).toBe('正在复制 videos（3/38）');

    sectionVm(wrapper, 'DatabaseSection').switchStatus = { running: false, completed: true, message: '迁移完成，重启应用后生效' };
    expect(sectionVm(wrapper, 'DatabaseSection').switchProgressText).toContain('重启');

    sectionVm(wrapper, 'DatabaseSection').switchStatus = { running: false, failed: true, message: '目标库不是空的' };
    expect(sectionVm(wrapper, 'DatabaseSection').switchProgressText).toContain('迁移失败');
    wrapper.unmount();
  });

  it('配置已切换但未重启时给出明确提示', async () => {
    api.GetDatabaseBackendStatus.mockResolvedValue({
      backend: 'sqlite', location: '/tmp/library.db',
      semantic_available: false, semantic_reason: '', pending_restart: true
    });
    const wrapper = await mountPage();
    const notice = wrapper.find('[data-test="db-pending-restart"]');
    expect(notice.exists()).toBe(true);
    expect(notice.text()).toContain('重启应用后生效');
    wrapper.unmount();
  });
});

describe('手机端浏览地址', () => {
  async function mountWithShortFeed(status) {
    const wrapper = await mountPage();
    api.GetShortFeedServerStatus.mockResolvedValue(status);
    await sectionVm(wrapper, 'MobileSection').loadShortFeedStatus();
    await flushPromises();
    return wrapper;
  }

  it('主位显示手机能连的局域网地址，界面上不出现 127.0.0.1', async () => {
    const wrapper = await mountWithShortFeed({
      running: true,
      url: 'http://127.0.0.1:18088/short/',
      lan_urls: ['http://192.168.162.250:18088/short/']
    });

    expect(wrapper.get('[data-test="short-feed-phone-url"]').text()).toBe('http://192.168.162.250:18088/short/');
    expect(wrapper.find('[data-test="short-feed-local-url"]').exists()).toBe(false);
    // 只看手机端这一节：环回地址不该出现在"手机上输哪个地址"的位置。
    // 别的分区（例如浏览器插件桥接）说明自己只绑 127.0.0.1 是正当的，
    // 断言整页文本会把那种正当说明也一起判错。
    expect(wrapper.findComponent({ name: 'MobileSection' }).text()).not.toContain('127.0.0.1');
  });

  it('多网卡时其余地址作为备用列出', async () => {
    const wrapper = await mountWithShortFeed({
      running: true,
      url: 'http://127.0.0.1:18088/short/',
      lan_urls: ['http://192.168.162.250:18088/short/', 'http://192.168.8.20:18088/short/']
    });

    const alternates = wrapper.findAll('[data-test="short-feed-alt-url"]');
    expect(alternates).toHaveLength(1);
    expect(alternates[0].text()).toContain('http://192.168.8.20:18088/short/');
  });

  it('一个局域网地址都没有时直接说明手机连不上', async () => {
    const wrapper = await mountWithShortFeed({
      running: true,
      url: 'http://127.0.0.1:18088/short/',
      lan_urls: []
    });

    expect(wrapper.get('[data-test="short-feed-no-lan"]').text()).toContain('手机连不上');
    expect(wrapper.find('[data-test="short-feed-phone-url"]').exists()).toBe(false);
  });
});

describe('视频超分：模型按需下载', () => {
  async function mountWithEnhance(capability, modelStatus = { running: false }) {
    const wrapper = await mountPage();
    api.GetEnhancementCapability.mockResolvedValue(capability);
    api.GetEnhancementModelStatus.mockResolvedValue(modelStatus);
    await sectionVm(wrapper, 'EnhanceSection').loadEnhanceStatus();
    await flushPromises();
    return wrapper;
  }

  it('模型没下载时给出下载入口，而不是一句"不可用"', async () => {
    const wrapper = await mountWithEnhance({
      available: false,
      reason_code: 'models_missing',
      models_installable: true,
      message: '超分模型尚未下载（约 52 MB），在设置里下载后即可使用'
    });

    const button = wrapper.get('[data-test="enhance-model-download"]');
    expect(button.text()).toContain('下载模型');

    api.StartEnhancementModelDownload.mockResolvedValue({ running: true, total_bytes: 100, downloaded_bytes: 0 });
    await button.trigger('click');
    await flushPromises();

    expect(api.StartEnhancementModelDownload).toHaveBeenCalledTimes(1);
    expect(wrapper.find('[data-test="enhance-model-progress"]').exists()).toBe(true);
  });

  it('校验失败时按钮变成重新下载', async () => {
    const wrapper = await mountWithEnhance({
      available: false,
      reason_code: 'models_corrupt',
      models_installable: true,
      message: '超分模型校验失败，请重新下载'
    });

    expect(wrapper.get('[data-test="enhance-model-download"]').text()).toContain('重新下载');
  });

  it('平台不支持这类原因不给下载入口', async () => {
    const wrapper = await mountWithEnhance({
      available: false,
      reason_code: 'platform_unsupported',
      models_installable: false,
      message: '视频超分首版只支持 Apple Silicon macOS'
    });

    expect(wrapper.find('[data-test="enhance-model-download"]').exists()).toBe(false);
    expect(wrapper.text()).toContain('只支持 Apple Silicon');
  });

  it('下载中显示进度并可取消', async () => {
    const wrapper = await mountWithEnhance(
      { available: false, reason_code: 'models_missing', models_installable: true, message: '' },
      { running: true, total_bytes: 200, downloaded_bytes: 50, message: '正在下载超分模型…' }
    );

    expect(wrapper.get('[data-test="enhance-model-progress"]').text()).toContain('25%');
    api.CancelEnhancementModelDownload.mockResolvedValue({ running: false, cancelled: true });
    await wrapper.get('[data-test="enhance-model-cancel"]').trigger('click');
    await flushPromises();

    expect(api.CancelEnhancementModelDownload).toHaveBeenCalledTimes(1);
  });
});

describe('扫描后的自动任务', () => {
  it('四个开关都会随设置一起提交', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-test="auto-technical-backfill-toggle"]').setValue(true);
    await wrapper.get('[data-test="auto-perceptual-hash-toggle"]').setValue(true);
    await wrapper.get('[data-test="auto-cleanup-analysis-toggle"]').setValue(true);
    await wrapper.get('[data-test="auto-image-exif-toggle"]').setValue(true);

    await wrapper.vm.saveSettings();
    await flushPromises();

    expect(api.UpdateSettings).toHaveBeenCalledWith(expect.objectContaining({
      auto_technical_backfill: true,
      auto_perceptual_hash: true,
      auto_cleanup_analysis: true,
      auto_image_exif_backfill: true
    }));
  });

  it('默认是关的，不会装完就替用户占着机器', async () => {
    const wrapper = await mountPage();

    for (const key of ['auto-technical-backfill-toggle', 'auto-perceptual-hash-toggle', 'auto-cleanup-analysis-toggle', 'auto-image-exif-toggle']) {
      expect(wrapper.get(`[data-test="${key}"]`).element.checked).toBe(false);
    }
  });

  // 建议作品集的自动开关（P-007）：默认关，且必须出现在保存载荷里——
  // 少一个键后端就会用 Go 零值覆盖用户的选择。
  it('建议作品集自动开关默认关且随设置一起提交', async () => {
    const wrapper = await mountPage();
    expect(wrapper.get('[data-test="auto-collection-suggestions-toggle"]').element.checked).toBe(false);

    await wrapper.vm.saveSettings();
    await flushPromises();
    let payload = api.UpdateSettings.mock.calls.at(-1)[0];
    expect(Object.prototype.hasOwnProperty.call(payload, 'auto_collection_suggestions')).toBe(true);
    expect(payload.auto_collection_suggestions).toBe(false);

    await wrapper.get('[data-test="auto-collection-suggestions-toggle"]').setValue(true);
    await wrapper.vm.saveSettings();
    await flushPromises();
    payload = api.UpdateSettings.mock.calls.at(-1)[0];
    expect(payload.auto_collection_suggestions).toBe(true);
  });

  // 帧哈希的自动开关（P-008）：同样默认关，且必须出现在保存载荷里——
  // 少一个键后端就会用 Go 零值覆盖用户的选择。
  it('帧哈希自动开关默认关且随设置一起提交', async () => {
    const wrapper = await mountPage();
    expect(wrapper.get('[data-test="auto-frame-hash-sequence-toggle"]').element.checked).toBe(false);

    await wrapper.vm.saveSettings();
    await flushPromises();
    let payload = api.UpdateSettings.mock.calls.at(-1)[0];
    expect(Object.prototype.hasOwnProperty.call(payload, 'auto_frame_hash_sequence')).toBe(true);
    expect(payload.auto_frame_hash_sequence).toBe(false);

    await wrapper.get('[data-test="auto-frame-hash-sequence-toggle"]').setValue(true);
    await wrapper.vm.saveSettings();
    await flushPromises();
    payload = api.UpdateSettings.mock.calls.at(-1)[0];
    expect(payload.auto_frame_hash_sequence).toBe(true);
  });
});

describe('后台任务空闲调度（P-003）', () => {
  it('五个 idle_* 字段随设置一起提交，并按合法区间归一化', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-test="idle-scheduling-toggle"]').setValue(true);
    await wrapper.get('[data-test="idle-threshold-minutes"]').setValue(999);
    await wrapper.get('[data-test="idle-require-ac-power"]').setValue(true);
    await wrapper.get('[data-test="idle-window-start"]').setValue('22:00');
    await wrapper.get('[data-test="idle-window-end"]').setValue('06:00');

    await wrapper.vm.saveSettings();
    await flushPromises();

    expect(api.UpdateSettings).toHaveBeenCalledWith(expect.objectContaining({
      idle_scheduling_enabled: true,
      idle_threshold_minutes: 120,
      idle_require_ac_power: true,
      idle_window_start: '22:00',
      idle_window_end: '06:00'
    }));
  });

  // 载荷里少一个键，后端就会拿 Go 的零值覆盖用户的选择（四个自动开关此前正是
  // 因为后端漏赋值而永远存不下来）。这里把九个键的存在性钉住。
  it('保存载荷同时带上四个自动开关与五个空闲调度字段', async () => {
    const wrapper = await mountPage();
    await wrapper.vm.saveSettings();
    await flushPromises();

    const payload = api.UpdateSettings.mock.calls.at(-1)[0];
    for (const key of [
      'auto_technical_backfill', 'auto_perceptual_hash', 'auto_cleanup_analysis', 'auto_image_exif_backfill',
      'idle_scheduling_enabled', 'idle_threshold_minutes', 'idle_require_ac_power', 'idle_window_start', 'idle_window_end'
    ]) {
      expect(Object.prototype.hasOwnProperty.call(payload, key)).toBe(true);
    }
    expect(payload).toEqual(expect.objectContaining({
      idle_scheduling_enabled: true,
      idle_threshold_minutes: 5,
      idle_require_ac_power: false,
      idle_window_start: '',
      idle_window_end: ''
    }));
  });
});

describe('字幕翻译术语表（P-010）', () => {
  it('挂在字幕翻译分区里，按全局作用域加载', async () => {
    const wrapper = await mountPage();
    expect(api.ListGlossaryEntries).toHaveBeenCalledWith(0);
    expect(wrapper.findComponent({ name: 'GlossaryEditor' }).props('collectionId')).toBe(0);
  });

  it('provider 是 DeepL 时说明术语表不适用', async () => {
    const wrapper = await mountPage();
    const notice = wrapper.get('[data-test="glossary-deepl-notice"]');
    expect(notice.text()).toContain('术语表不适用于 DeepL');
  });

  it('切到 OpenAI 兼容接口后不再显示这条说明', async () => {
    const wrapper = await mountPage();
    sectionVm(wrapper, 'SubtitleSection').form.subtitle_translation_provider = 'llm';
    await flushPromises();
    expect(wrapper.find('[data-test="glossary-deepl-notice"]').exists()).toBe(false);
  });
});

describe('桌面通知开关（P-004）', () => {
  it('老库读回 undefined 时界面显示成开着', async () => {
    const wrapper = await mountPage();
    expect(wrapper.get('[data-test="desktop-notifications-toggle"]').element.checked).toBe(true);
  });

  it('desktop_notifications_enabled 随设置一起提交', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-test="desktop-notifications-toggle"]').setValue(false);

    await wrapper.vm.saveSettings();
    await flushPromises();

    const payload = api.UpdateSettings.mock.calls.at(-1)[0];
    expect(Object.prototype.hasOwnProperty.call(payload, 'desktop_notifications_enabled')).toBe(true);
    expect(payload.desktop_notifications_enabled).toBe(false);
  });
});

describe('命令面板接线', () => {
  it('挂载注册保存设置命令，卸载即注销', async () => {
    const wrapper = await mountPage();
    const saveCommand = () => commandList().find(command => command.id === 'action:settings-save');

    expect(saveCommand()).toBeTruthy();
    expect(saveCommand().enabled()).toBe(true);

    wrapper.unmount();
    expect(saveCommand()).toBeUndefined();
  });

  it('分区清单对外导出，命令面板的分区锚点动作读的是同一份', () => {
    expect(SETTINGS_SECTIONS.length).toBeGreaterThan(0);
    expect(SETTINGS_SECTIONS.every(section => section.key && section.label)).toBe(true);
  });
});

describe('浏览器插件桥接', () => {
  async function mountWithBridge({ status, tasks = [] } = {}) {
    api.GetBrowserBridgeStatus.mockResolvedValue(status || { running: false, enabled: false });
    api.ListBrowserDownloadTasks.mockResolvedValue(tasks);
    const wrapper = await mountPage();
    await flushPromises();
    return wrapper;
  }

  // 这条是回归用例：保存设置的载荷是逐字段白名单拼的，漏掉任何一个 browser_ 字段，
  // 后端都会无条件把它写成零值——开关被写回 false、下载目录被清空，而界面上表单
  // 还显示着刚设的值，看起来像保存成功了。桥接因此永远起不来。
  it('保存设置时必须带上桥接三项，否则它们会被静默写成零值', async () => {
    const wrapper = await mountWithBridge();
    const section = sectionVm(wrapper, 'BrowserBridgeSection');
    section.form.browser_bridge_enabled = true;
    section.form.browser_download_directory = '/library/downloads';
    section.form.browser_download_concurrency = 3;

    await wrapper.vm.saveSettings();
    await flushPromises();

    expect(api.UpdateSettings).toHaveBeenCalledWith(expect.objectContaining({
      browser_bridge_enabled: true,
      browser_download_directory: '/library/downloads',
      browser_download_concurrency: 3
    }));
    // 令牌有意不走这条路：它只由「生成令牌」按钮写，混进来的话前端某次漏带就会把它抹空
    expect(api.UpdateSettings.mock.calls.at(-1)[0]).not.toHaveProperty('browser_bridge_token');
    wrapper.unmount();
  });

  it('未开启时状态说"未开启"，开启后显示端口', async () => {
    const off = await mountWithBridge();
    expect(off.get('[data-test="bridge-status-text"]').text()).toBe('未开启');
    off.unmount();

    const on = await mountWithBridge({ status: { running: true, enabled: true, port: 18110, url: 'http://127.0.0.1:18110/' } });
    expect(on.get('[data-test="bridge-status-text"]').text()).toContain('18110');
    on.unmount();
  });

  it('开着桥接但没有令牌时，把原因直接显示出来', async () => {
    const wrapper = await mountWithBridge({
      status: { running: false, enabled: true, startup_error: '桥接已开启但还没有令牌，先在设置里生成一个' }
    });
    expect(wrapper.get('[data-test="bridge-status-error"]').text()).toContain('令牌');
    wrapper.unmount();
  });

  it('重新生成令牌会更新表单并说明旧令牌立即失效', async () => {
    api.RegenerateBrowserBridgeToken.mockResolvedValue('new-token-value');
    const wrapper = await mountWithBridge();

    await wrapper.get('[data-test="bridge-regenerate"]').trigger('click');
    await flushPromises();

    expect(api.RegenerateBrowserBridgeToken).toHaveBeenCalled();
    expect(wrapper.get('[data-test="bridge-token"]').element.value).toBe('new-token-value');
    expect(wrapper.get('[data-test="bridge-token-message"]').text()).toContain('旧令牌立即失效');
    wrapper.unmount();
  });

  it('选择目录时用户取消（返回空串）不会清掉已有目录', async () => {
    const wrapper = await mountWithBridge();
    const section = sectionVm(wrapper, 'BrowserBridgeSection');
    section.form.browser_download_directory = '/library/downloads';

    api.SelectBrowserDownloadDirectory.mockResolvedValue('');
    await section.chooseDirectory();
    expect(section.form.browser_download_directory).toBe('/library/downloads');

    api.SelectBrowserDownloadDirectory.mockResolvedValue('/library/new');
    await section.chooseDirectory();
    expect(section.form.browser_download_directory).toBe('/library/new');
    wrapper.unmount();
  });



});
