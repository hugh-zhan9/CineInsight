import { flushPromises, shallowMount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'SearchLibraryVideoPage', 'ListRecentlyPlayedWithFilter', 'GetLibrarySubtitleHits', 'PlayVideo', 'PlayRandomVideoWithFilter',
  'SetVideoFavorite', 'SetVideoWatched', 'UpdateVideoWatchProgress', 'ListSavedLibraryViews', 'SaveLibraryView',
  'DeleteSavedLibraryView', 'RejectSameSourceRelation', 'OpenDirectory', 'DeleteVideo', 'BatchDeleteVideos', 'ListTrashEntries',
  'RestoreTrashEntry', 'RemoveTagFromVideo', 'UpdateSettings', 'GetSubtitleEngineStatuses', 'PrepareSubtitleEngine',
  'GenerateSubtitle', 'ForceGenerateSubtitle', 'RenameVideo', 'RenameDirectory', 'MoveVideo', 'BatchMoveVideos', 'MoveDirectory',
  'SelectFolderToRename', 'SelectMigrationSourceDirectory', 'SelectMigrationDestinationDirectory', 'CancelSubtitle', 'CancelSubtitleTask',
  'GetSubtitleQueueState', 'GetCleanupStatus', 'GetAITaggingStatusSummary', 'StartCleanupAnalysis', 'GetSubtitleSegments',
  'GetPreviewSession', 'PreviewExternally', 'SyncScanDirectories', 'StartTechnicalBackfill', 'GetTechnicalBackfillStatus',
  'CancelTechnicalBackfill', 'StartLocalMetadataBackfill', 'GetLocalMetadataBackfillStatus', 'CancelLocalMetadataBackfill', 'GetSettings', 'LogFrontend'
].map(name => [name, vi.fn()])));

vi.mock('../../wailsjs/go/main/App', () => api);
vi.mock('./ScanDialog.vue', () => ({ default: { template: '<div />' } }));
vi.mock('./TagManagerDialog.vue', () => ({ default: { template: '<div />' } }));
vi.mock('./AddTagDialog.vue', () => ({ default: { template: '<div />' } }));
vi.mock('./DeleteConfirmDialog.vue', () => ({ default: { template: '<div />' } }));
vi.mock('./TagDeleteDialog.vue', () => ({ default: { template: '<div />' } }));
vi.mock('./PreviewDrawer.vue', () => ({ default: { template: '<div />' } }));
vi.mock('./SubtitleWorkbench.vue', () => ({ default: { template: '<div />' } }));
vi.mock('./LocalMetadataDialog.vue', () => ({ default: { template: '<div />' } }));
vi.mock('./TrashRestoreDialog.vue', () => ({ default: { template: '<div />' } }));
vi.mock('./VirtualVideoList.vue', () => ({ default: { template: '<div />' } }));
vi.mock('./VideoListRow.vue', () => ({ default: { template: '<div />' } }));
vi.mock('./AITagReviewDialog.vue', () => ({ default: { template: '<div />' } }));

import VideoListPage from './VideoListPage.vue';

async function mountPage() {
  const wrapper = shallowMount(VideoListPage, {
    props: { tags: [], settings: {}, directories: [] }
  });
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  vi.clearAllMocks();
  delete window.runtime;
  Object.defineProperty(window, 'localStorage', {
    configurable: true,
    value: { getItem: vi.fn(() => null), setItem: vi.fn(), removeItem: vi.fn(), clear: vi.fn() }
  });
  api.SearchLibraryVideoPage.mockResolvedValue({ videos: [] });
  api.ListSavedLibraryViews.mockResolvedValue([]);
  api.GetSubtitleQueueState.mockResolvedValue({ active_task: null, queued_tasks: [], total: 0 });
  api.GetAITaggingStatusSummary.mockResolvedValue({ same_source_unread: 0 });
  api.GetTechnicalBackfillStatus.mockResolvedValue({ running: false, preparing: false, completed: false, cancelled: false, failed: 0, failures: [] });
  api.GetLocalMetadataBackfillStatus.mockResolvedValue({ running: false, completed: false, cancelled: false, failed: 0, failures: [] });
  api.GetSettings.mockResolvedValue({ scan_exclude_paths: '' });
  api.LogFrontend.mockResolvedValue();
});

describe('VideoListPage media-detail integration', () => {
  it('renames a selected managed folder and refreshes paths', async () => {
    const wrapper = await mountPage();
    const alert = vi.spyOn(window, 'alert').mockImplementation(() => {});
    api.SelectFolderToRename.mockResolvedValueOnce('/library/Old Name');
    api.RenameDirectory.mockResolvedValueOnce({ videos_updated: 3, directories_updated: 1 });
    api.GetSettings.mockResolvedValueOnce({ scan_exclude_paths: '/library/New Name/private' });
    wrapper.vm.reloadCurrentView = vi.fn().mockResolvedValue();

    await wrapper.vm.renameFolder();
    expect(wrapper.vm.folderRenameDialog).toEqual(expect.objectContaining({
      show: true, source: '/library/Old Name', currentName: 'Old Name', newName: 'Old Name'
    }));

    wrapper.vm.folderRenameDialog.newName = 'New Name';
    await wrapper.vm.executeFolderRename();

    expect(api.RenameDirectory).toHaveBeenCalledWith('/library/Old Name', 'New Name');
    expect(wrapper.emitted('reload-directories')).toHaveLength(1);
    expect(wrapper.emitted('update-settings')[0][0]).toEqual({ scan_exclude_paths: '/library/New Name/private' });
    expect(wrapper.vm.reloadCurrentView).toHaveBeenCalledOnce();
    expect(wrapper.vm.folderRenameDialog.show).toBe(false);
    expect(alert).toHaveBeenCalledWith('文件夹重命名完成：更新 3 个视频、1 个扫描目录。');
    alert.mockRestore();
    wrapper.unmount();
  });

  it('opens the subtitle workbench for the selected video', async () => {
    const wrapper = await mountPage();
    const video = { id: 12, name: 'editable.mp4' };

    wrapper.vm.openSubtitleWorkbench(video);

    expect(wrapper.vm.subtitleWorkbench).toEqual({ show: true, video });
    wrapper.vm.closeSubtitleWorkbench();
    expect(wrapper.vm.subtitleWorkbench).toEqual({ show: false, video: null });
    wrapper.unmount();
  });

  it('sends nullable rating filters and sort mode through the generated request DTO', async () => {
    const wrapper = await mountPage();
    wrapper.vm.minRating = '0';
    wrapper.vm.maxRating = '10';
    wrapper.vm.sortMode = 'rating_desc';
    wrapper.vm.loading = false;
    wrapper.vm.hasMore = true;
    wrapper.vm.libraryCursor = null;

    await wrapper.vm.loadVideos();

    expect(api.SearchLibraryVideoPage).toHaveBeenLastCalledWith(expect.objectContaining({
      filter: expect.objectContaining({ min_rating: 0, max_rating: 10, sort_mode: 'rating_desc' }),
      limit: 20
    }));
    expect(api.SearchLibraryVideoPage.mock.calls.at(-1)[0]).not.toHaveProperty('cursor');
    wrapper.unmount();
  });

  it('renders preparing, empty, failure, and failure-summary backfill states', async () => {
    const wrapper = await mountPage();

    wrapper.vm.technicalBackfill = { ...wrapper.vm.technicalBackfill, running: true, preparing: true };
    await wrapper.vm.$nextTick();
    expect(wrapper.text()).toContain('正在统计待补全视频');

    wrapper.vm.technicalBackfill = { ...wrapper.vm.technicalBackfill, running: false, preparing: false, completed: true, total: 0, failed: 0 };
    await wrapper.vm.$nextTick();
    expect(wrapper.text()).toContain('技术信息无需补全');

    wrapper.vm.technicalBackfill = {
      ...wrapper.vm.technicalBackfill,
      completed: true,
      total: 1,
      processed: 1,
      succeeded: 0,
      skipped: 0,
      failed: 1,
      failures: [{ video_id: 4, name: 'broken.mkv', error: 'ffprobe failed' }]
    };
    await wrapper.vm.$nextTick();
    expect(wrapper.text()).toContain('失败 1');
    expect(wrapper.text()).toContain('broken.mkv：ffprobe failed');
    wrapper.unmount();
  });

  it('starts backfill from the mounted page and applies the returned state', async () => {
    const wrapper = await mountPage();
    api.StartTechnicalBackfill.mockResolvedValueOnce({ running: true, preparing: true, total: 0 });

    await wrapper.vm.startTechnicalBackfill();

    expect(api.StartTechnicalBackfill).toHaveBeenCalledOnce();
    expect(wrapper.vm.technicalBackfill.running).toBe(true);
    expect(wrapper.vm.technicalBackfill.preparing).toBe(true);
    wrapper.unmount();
  });

  it('applies runtime backfill events and refreshes state after cancellation', async () => {
    const handlers = new Map();
    window.runtime = {
      EventsOn: vi.fn((name, handler) => {
        handlers.set(name, handler);
        return () => handlers.delete(name);
      })
    };
    const wrapper = await mountPage();
    handlers.get('technical-backfill-state')({ running: true, preparing: false, total: 3, processed: 1, succeeded: 1 });
    await wrapper.vm.$nextTick();
    expect(wrapper.vm.technicalBackfill).toEqual(expect.objectContaining({ running: true, total: 3, processed: 1, succeeded: 1 }));
    expect(wrapper.text()).toContain('技术信息 1/3');

    api.CancelTechnicalBackfill.mockResolvedValueOnce();
    api.GetTechnicalBackfillStatus.mockResolvedValueOnce({ running: false, cancelled: true, completed: false, total: 3, processed: 1, succeeded: 1, failed: 0, failures: [] });
    await wrapper.vm.cancelTechnicalBackfill();

    expect(api.CancelTechnicalBackfill).toHaveBeenCalledOnce();
    expect(wrapper.vm.technicalBackfill.cancelled).toBe(true);
    wrapper.unmount();
    expect(handlers.has('technical-backfill-state')).toBe(false);
  });
});
