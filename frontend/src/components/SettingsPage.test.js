import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'UpdateSettings', 'SelectDirectory', 'GetAllDirectories', 'AddDirectory', 'UpdateDirectory', 'DeleteDirectory',
  'GetShortFeedServerStatus', 'GetAITagLibrary', 'SaveAITagLibrary', 'TriggerAITagging',
  'GetLibraryWatcherStatus', 'RetryLibraryWatcherRoot'
].map(name => [name, vi.fn()])));

vi.mock('../../wailsjs/go/main/App', () => api);

import SettingsPage from './SettingsPage.vue';

const baseSettings = () => ({
  video_extensions: '.mp4',
  play_weight: 2,
  auto_scan_on_startup: false,
  library_watch_enabled: true,
  local_metadata_enabled: true,
  ai_quality_enabled: true,
  short_feed_max_duration_minutes: 5,
  theme: 'system',
  subtitle_translation_provider: 'deepl',
  subtitle_whisperx_model: 'medium',
  subtitle_whisperx_batch_size: 8
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

  it('persists independent local workflow switches', async () => {
    const wrapper = await mountPage();
    await wrapper.find('[data-test="local-metadata-toggle"]').setValue(false);
    await wrapper.find('[data-test="ai-quality-toggle"]').setValue(false);
    await wrapper.find('.settings-save-button').trigger('click');
    await flushPromises();

    expect(api.UpdateSettings).toHaveBeenCalledWith(expect.objectContaining({
      local_metadata_enabled: false,
      ai_quality_enabled: false,
    }));
  });
});
