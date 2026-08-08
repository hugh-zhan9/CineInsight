import { flushPromises, shallowMount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'SearchLibraryVideoPage', 'SearchSemanticVideos', 'FindSimilarVideos', 'ListRecentlyPlayedWithFilter', 'GetLibrarySubtitleHits', 'PlayVideo', 'PlayRandomVideoWithFilter',
  'SetVideoFavorite', 'SetVideoWatched', 'UpdateVideoWatchProgress', 'ListSavedLibraryViews', 'SaveLibraryView',
  'DeleteSavedLibraryView', 'RejectSameSourceRelation', 'OpenDirectory', 'DeleteVideo', 'BatchDeleteVideos', 'ListTrashEntries',
  'RestoreTrashEntry', 'RemoveTagFromVideo', 'UpdateSettings', 'GetSubtitleEngineStatuses', 'PrepareSubtitleEngine',
  'GenerateSubtitle', 'ForceGenerateSubtitle', 'RenameVideo', 'RenameDirectory', 'MoveVideo', 'BatchMoveVideos', 'MoveDirectory',
  'SelectFolderToRename', 'SelectMigrationSourceDirectory', 'SelectMigrationDestinationDirectory', 'CancelSubtitle', 'CancelSubtitleTask',
  'GetSubtitleQueueState', 'GetCleanupStatus', 'GetAITaggingStatusSummary', 'StartCleanupAnalysis', 'GetSubtitleSegments',
  'GetPreviewSession', 'PreviewExternally', 'SyncScanDirectories', 'StartTechnicalBackfill', 'GetTechnicalBackfillStatus',
  'CancelTechnicalBackfill', 'StartLocalMetadataBackfill', 'GetLocalMetadataBackfillStatus', 'CancelLocalMetadataBackfill',
  'StartPerceptualHashBackfill', 'GetPerceptualHashBackfillStatus', 'CancelPerceptualHashBackfill',
  'ExportLocalMetadataNFO', 'StartLocalMetadataExport', 'GetLocalMetadataExportStatus', 'CancelLocalMetadataExport', 'GetSettings', 'LogFrontend'
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

async function mountPage(extraProps = {}) {
  const wrapper = shallowMount(VideoListPage, {
    props: { tags: [], settings: {}, directories: [], ...extraProps }
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
  api.SearchSemanticVideos.mockResolvedValue({ hits: [], coverage: { indexed: 0, total: 0 }, has_more: false });
  api.FindSimilarVideos.mockResolvedValue({ hits: [], coverage: { indexed: 0, total: 0 }, has_more: false });
  api.ListSavedLibraryViews.mockResolvedValue([]);
  api.GetSubtitleQueueState.mockResolvedValue({ active_task: null, queued_tasks: [], total: 0 });
  api.GetAITaggingStatusSummary.mockResolvedValue({ same_source_unread: 0 });
  api.GetTechnicalBackfillStatus.mockResolvedValue({ running: false, preparing: false, completed: false, cancelled: false, failed: 0, failures: [] });
  api.GetPerceptualHashBackfillStatus.mockResolvedValue({ running: false, completed: false, cancelled: false, failed: 0, failures: [] });
  api.GetLocalMetadataBackfillStatus.mockResolvedValue({ running: false, completed: false, cancelled: false, failed: 0, failures: [] });
	api.GetLocalMetadataExportStatus.mockResolvedValue({ running: false, completed: false, cancelled: false, failed: 0, failures: [] });
  api.GetSettings.mockResolvedValue({ scan_exclude_paths: '' });
  api.LogFrontend.mockResolvedValue();
});

describe('VideoListPage media-detail integration', () => {
	it('loads scored semantic results with the existing structured filters', async () => {
	  const wrapper = await mountPage();
	  wrapper.vm.searchMode = 'semantic';
	  wrapper.vm.searchKeyword = '雨夜里的公路电影';
	  wrapper.vm.selectedTags = [7];
	  wrapper.vm.videos = [];
	  wrapper.vm.loading = false;
	  wrapper.vm.hasMore = true;
	  api.SearchSemanticVideos.mockResolvedValueOnce({
	    hits: [{ video: { id: 9, name: 'road.mp4', tags: [] }, score: 0.87 }],
	    coverage: { indexed: 8, total: 10 },
	    has_more: false
	  });

	  await wrapper.vm.loadVideos();

	  expect(api.SearchSemanticVideos).toHaveBeenCalledWith(expect.objectContaining({
	    query: '雨夜里的公路电影',
	    // 语义查询只通过 query 传递；共享筛选 DTO 保持后端可归一化的模式，
	    // 保证随机播放/保存视图/批量导出在语义模式下拿到纯结构化筛选。
	    filter: expect.objectContaining({ search_mode: 'file', keyword: '', tag_ids: [7] })
	  }));

	  const sharedFilter = wrapper.vm.currentLibraryFilter();
	  expect(sharedFilter.search_mode).toBe('file');
	  expect(sharedFilter.keyword).toBe('');
	  expect(wrapper.vm.videos[0]).toEqual(expect.objectContaining({ id: 9, _semanticScore: 0.87 }));
	  expect(wrapper.vm.semanticCoverage).toEqual({ indexed: 8, total: 10 });
	  expect(wrapper.vm.hasMore).toBe(false);
	  wrapper.unmount();
	});

  it('shows near-duplicate groups without selecting either video by default', async () => {
    const wrapper = await mountPage();
    wrapper.vm.cleanupDialog.show = true;
    wrapper.vm.cleanupDialog.analysis = {
      duplicate_groups: [],
      near_duplicate_groups: [{
        original: { id: 41, name: 'source.mkv', duration: 120, resolution: '1080p' },
        candidates: [{ id: 42, name: 'transcode.mp4', duration: 120, resolution: '720p' }],
        reason: '三帧感知哈希接近'
      }],
      same_source_groups: [],
      low_duration: [],
      low_resolution: []
    };
    await wrapper.vm.$nextTick();

    expect(wrapper.vm.cleanupSelection).toEqual([]);
    expect(wrapper.vm.getAllCleanupCandidates().map(video => video.id)).toEqual([41, 42]);

    wrapper.vm.selectAllCleanupCandidates();
    expect(wrapper.vm.cleanupSelection).toEqual([]);

    wrapper.vm.cleanupDialog.analysis.duplicate_groups = [{
      original: { id: 51, name: 'orig.mkv' },
      candidates: [{ id: 52, name: 'copy.mkv' }]
    }];
    wrapper.vm.selectAllCleanupCandidates();
    expect(wrapper.vm.cleanupSelection).toEqual([51, 52]);
    wrapper.unmount();
  });

  it('restores a background analysis on reopen and groups candidates by directory', async () => {
    const analysis = {
      duplicate_groups: [{
        original: { id: 1, name: 'keep.mp4', directory: '/lib/a', path: '/lib/a/keep.mp4' },
        candidates: [{ id: 2, name: 'copy.mp4', directory: '/lib/b', path: '/lib/b/copy.mp4' }],
        reason: '文件大小和采样哈希一致'
      }],
      near_duplicate_groups: [],
      same_source_groups: [],
      low_duration: [],
      low_resolution: []
    };
    // 后台跑完的结果（并且期间库变过被标记为过期）：重开面板必须直接看到它，不是重新分析。
    api.GetCleanupStatus.mockResolvedValue({
      running: false, completed: true, error: '', stale: true, progress: { stage: 'done' }, analysis
    });
    const wrapper = await mountPage();

    await wrapper.vm.openCleanupDialog();
    await flushPromises();

    expect(api.StartCleanupAnalysis).not.toHaveBeenCalled();
    expect(wrapper.vm.cleanupDialog.loading).toBe(false);
    expect(wrapper.vm.cleanupDialog.analysis).toBeTruthy();
    expect(wrapper.vm.cleanupResultStale).toBe(true);

    // 整组归到建议保留项所在目录，另一份仍在 /lib/b 但跟着组走。
    const sections = wrapper.vm.cleanupDirectorySections;
    expect(sections.map(section => section.directory)).toEqual(['/lib/a']);
    expect(sections[0].entries[0].kind).toBe('exact');
    expect(sections[0].videoCount).toBe(2);
    wrapper.unmount();
  });

  it('asks before re-analysing after trashing cleanup candidates', async () => {
    const analysis = {
      duplicate_groups: [{
        original: { id: 1, name: 'keep.mp4', directory: '/lib/a' },
        candidates: [{ id: 2, name: 'copy.mp4', directory: '/lib/a' }],
        reason: '文件大小和采样哈希一致'
      }],
      near_duplicate_groups: [], same_source_groups: [], low_duration: [], low_resolution: []
    };
    api.GetCleanupStatus.mockResolvedValue({
      running: false, completed: true, error: '', stale: false, progress: { stage: 'done' }, analysis
    });
    api.BatchDeleteVideos.mockResolvedValue({ requested: 1, succeeded: 1, failed: 0, errors: [] });
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(false);

    const wrapper = await mountPage();
    await wrapper.vm.openCleanupDialog();
    await flushPromises();

    wrapper.vm.cleanupSelection = [2];
    await wrapper.vm.trashSelectedCleanupCandidates();
    await flushPromises();

    expect(api.BatchDeleteVideos).toHaveBeenCalledWith([2], true);
    // 答"取消"：不重跑，结果留在原地继续审阅，只标记为已过期。
    expect(confirmSpy).toHaveBeenCalledTimes(1);
    expect(api.StartCleanupAnalysis).not.toHaveBeenCalled();
    expect(wrapper.vm.cleanupDialog.analysis).toBeTruthy();
    expect(wrapper.vm.cleanupResultStale).toBe(true);

    // 全选候选不能把已移入回收站的项重新选上，否则会对着已删的视频再删一次。
    wrapper.vm.selectAllCleanupCandidates();
    expect(wrapper.vm.cleanupSelection).not.toContain(2);
    expect(wrapper.vm.cleanupSelection).toContain(1);

    confirmSpy.mockRestore();
    wrapper.unmount();
  });

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

  it('starts NFO export for the complete current filter', async () => {
	  const wrapper = await mountPage();
	  const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(true);
	  wrapper.vm.searchKeyword = '导演剪辑版';
	  wrapper.vm.selectedTags = [7];
	  api.StartLocalMetadataExport.mockResolvedValueOnce({ running: true, total: 3, processed: 0 });

	  await wrapper.vm.startLocalMetadataExport();

	  expect(api.StartLocalMetadataExport).toHaveBeenCalledWith({
		filter: expect.objectContaining({ keyword: '导演剪辑版', tag_ids: [7] })
	  });
	  expect(wrapper.vm.localMetadataExport).toEqual(expect.objectContaining({ running: true, total: 3 }));
	  confirm.mockRestore();
	  wrapper.unmount();
	});

	it('moves review focus and reuses favorite behavior from keyboard input', async () => {
	  const wrapper = await mountPage();
	  wrapper.vm.videos = [
		{ id: 1, name: 'one.mp4', is_favorite: false },
		{ id: 2, name: 'two.mp4', is_favorite: false }
	  ];
	  const preventDefault = vi.fn();
	  wrapper.vm.handleLibraryShortcut({ key: 'j', target: document.body, preventDefault });
	  expect(wrapper.vm.selectedVideoIds).toEqual([1]);
	  wrapper.vm.handleLibraryShortcut({ key: 'j', target: document.body, preventDefault: vi.fn() });
	  expect(wrapper.vm.selectedVideoIds).toEqual([2]);
	  expect(preventDefault).toHaveBeenCalledOnce();

	  api.SetVideoFavorite.mockResolvedValueOnce({ id: 2, name: 'two.mp4', is_favorite: true });
	  wrapper.vm.handleLibraryShortcut({ key: 'f', target: document.body, preventDefault: vi.fn() });
	  await flushPromises();
	  expect(api.SetVideoFavorite).toHaveBeenCalledWith(2, true);

	  const inputPrevented = vi.fn();
	  wrapper.vm.handleLibraryShortcut({ key: 'w', target: document.createElement('input'), preventDefault: inputPrevented });
	  expect(inputPrevented).not.toHaveBeenCalled();
	  wrapper.unmount();
	});


	it('keeps multi-selection on focus move and ignores action keys without focus', async () => {
	  const wrapper = await mountPage();
	  wrapper.vm.videos = [
		{ id: 1, name: 'one.mp4', is_favorite: false },
		{ id: 2, name: 'two.mp4', is_favorite: false },
		{ id: 3, name: 'three.mp4', is_favorite: false }
	  ];
	  wrapper.vm.handleLibraryShortcut({ key: 'f', target: document.body, preventDefault: vi.fn() });
	  expect(api.SetVideoFavorite).not.toHaveBeenCalled();

	  wrapper.vm.selectedVideoIds = [1, 2];
	  wrapper.vm.keyboardFocusVideoID = 2;
	  wrapper.vm.handleLibraryShortcut({ key: 'j', target: document.body, preventDefault: vi.fn() });
	  expect(wrapper.vm.keyboardFocusVideoID).toBe(3);
	  expect(wrapper.vm.selectedVideoIds).toEqual([1, 2]);
	  wrapper.unmount();
	});

	it('scrolls the keyboard-focused row into view when moving focus', async () => {
	  const wrapper = await mountPage();
	  wrapper.vm.videos = [
		{ id: 1, name: 'one.mp4' },
		{ id: 2, name: 'two.mp4' }
	  ];
	  const scrollSpy = vi.spyOn(wrapper.vm, 'scrollKeyboardFocusIntoView');
	  wrapper.vm.keyboardFocusVideoID = 1;
	  wrapper.vm.handleLibraryShortcut({ key: 'j', target: document.body, preventDefault: vi.fn() });
	  expect(scrollSpy).toHaveBeenCalledWith(2);
	  wrapper.unmount();
	});

	it('ignores shortcuts while the library page is inactive or a dialog is open', async () => {
	  const wrapper = await mountPage({ pageActive: false });
	  wrapper.vm.videos = [{ id: 1, name: 'one.mp4', is_favorite: false }];
	  const preventDefault = vi.fn();
	  wrapper.vm.handleLibraryShortcut({ key: 'f', target: document.body, preventDefault });
	  expect(preventDefault).not.toHaveBeenCalled();
	  expect(api.SetVideoFavorite).not.toHaveBeenCalled();
	  wrapper.unmount();

	  const activeWrapper = await mountPage();
	  activeWrapper.vm.videos = [{ id: 1, name: 'one.mp4', is_favorite: false }];
	  const dialog = document.createElement('div');
	  dialog.setAttribute('role', 'dialog');
	  document.body.appendChild(dialog);
	  const dialogPrevented = vi.fn();
	  activeWrapper.vm.handleLibraryShortcut({ key: 'f', target: document.body, preventDefault: dialogPrevented });
	  expect(dialogPrevented).not.toHaveBeenCalled();
	  expect(api.SetVideoFavorite).not.toHaveBeenCalled();
	  dialog.remove();
	  activeWrapper.unmount();
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
