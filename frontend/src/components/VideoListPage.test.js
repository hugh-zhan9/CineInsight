import { flushPromises, shallowMount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'SearchLibraryVideoPage', 'SearchSemanticVideos', 'FindSimilarVideos', 'ListRecentlyPlayedWithFilter', 'GetLibrarySubtitleHits', 'PlayVideo', 'PlayRandomVideoWithFilter', 'PickRandomVideos', 'GetVideosByIDs', 'CountLibraryVideos',
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
  api.CountLibraryVideos.mockResolvedValue(0);
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

describe('VideoListPage random pick of 10', () => {
  const pickedVideos = [
    { id: 11, name: 'a.mp4', tags: [] },
    { id: 12, name: 'b.mp4', tags: [] }
  ];

  it('抽取后接管主列表，沿用随机播放的筛选、模式和最近排除', async () => {
    const wrapper = await mountPage();
    wrapper.vm.randomMode = 'unwatched';
    wrapper.vm.selectedTags = [3];
    wrapper.vm.recentRandomVideoIDs = [99];
    wrapper.vm.hasMore = true;
    api.PickRandomVideos.mockResolvedValueOnce({
      videos: pickedVideos,
      selection_reason: '在当前筛选范围内优先选择未看视频'
    });

    await wrapper.vm.enterRandomPick();

    expect(api.PickRandomVideos).toHaveBeenCalledWith(
      expect.objectContaining({
        mode: 'unwatched',
        exclude_ids: [99],
        filter: expect.objectContaining({ tag_ids: [3] })
      }),
      10
    );
    expect(wrapper.vm.randomPick.active).toBe(true);
    expect(wrapper.vm.randomPick.ids).toEqual([11, 12]);
    expect(wrapper.vm.videos.map(video => video.id)).toEqual([11, 12]);
    // 固定批次不再分页加载，避免后续滚动把普通结果追加进来。
    expect(wrapper.vm.hasMore).toBe(false);
    expect(wrapper.text()).toContain('在当前筛选范围内优先选择未看视频');
    wrapper.unmount();
  });

  it('批次内的刷新按 ID 取最新记录，不会重新抽一批', async () => {
    const wrapper = await mountPage();
    api.PickRandomVideos.mockResolvedValueOnce({ videos: pickedVideos, selection_reason: '' });
    await wrapper.vm.enterRandomPick();
    api.SearchLibraryVideoPage.mockClear();
    api.PickRandomVideos.mockClear();
    // 加标签这类列表内操作会触发 reloadCurrentView：批次要保持原样，只更新内容。
    api.GetVideosByIDs.mockResolvedValueOnce([
      { id: 11, name: 'a.mp4', tags: [{ id: 5, name: '新标签' }] },
      { id: 12, name: 'b.mp4', tags: [] }
    ]);

    await wrapper.vm.reloadCurrentView();

    expect(api.GetVideosByIDs).toHaveBeenCalledWith([11, 12]);
    expect(api.PickRandomVideos).not.toHaveBeenCalled();
    expect(api.SearchLibraryVideoPage).not.toHaveBeenCalled();
    expect(wrapper.vm.videos[0].tags).toHaveLength(1);
    expect(wrapper.vm.randomPick.active).toBe(true);
    wrapper.unmount();
  });

  it('换一批会把当前批次一起排除掉', async () => {
    const wrapper = await mountPage();
    api.PickRandomVideos.mockResolvedValueOnce({ videos: pickedVideos, selection_reason: '' });
    await wrapper.vm.enterRandomPick();
    api.PickRandomVideos.mockResolvedValueOnce({ videos: [{ id: 21, name: 'c.mp4', tags: [] }], selection_reason: '' });

    await wrapper.vm.reshuffleRandomPick();

    expect(api.PickRandomVideos).toHaveBeenLastCalledWith(
      expect.objectContaining({ exclude_ids: [11, 12] }),
      10
    );
    expect(wrapper.vm.randomPick.ids).toEqual([21]);
    wrapper.unmount();
  });

  it('批次里的条目被删光后自动退出随机并回到普通列表', async () => {
    const wrapper = await mountPage();
    api.PickRandomVideos.mockResolvedValueOnce({ videos: pickedVideos, selection_reason: '' });
    await wrapper.vm.enterRandomPick();
    api.GetVideosByIDs.mockResolvedValueOnce([]);
    api.SearchLibraryVideoPage.mockClear();

    await wrapper.vm.reloadCurrentView();

    expect(wrapper.vm.randomPick.active).toBe(false);
    expect(api.SearchLibraryVideoPage).toHaveBeenCalled();
    wrapper.unmount();
  });

  it('改筛选条件会退出随机批次并按新条件重新加载', async () => {
    const wrapper = await mountPage();
    api.PickRandomVideos.mockResolvedValueOnce({ videos: pickedVideos, selection_reason: '' });
    await wrapper.vm.enterRandomPick();
    api.SearchLibraryVideoPage.mockClear();
    api.GetVideosByIDs.mockClear();

    await wrapper.vm.handleSearch(false);
    await flushPromises();

    expect(wrapper.vm.randomPick.active).toBe(false);
    expect(api.GetVideosByIDs).not.toHaveBeenCalled();
    expect(api.SearchLibraryVideoPage).toHaveBeenCalled();
    wrapper.unmount();
  });

  it('刷新批次失败时报错并留住当前批次', async () => {
    const wrapper = await mountPage();
    const alertSpy = vi.spyOn(window, 'alert').mockImplementation(() => {});
    api.PickRandomVideos.mockResolvedValueOnce({ videos: pickedVideos, selection_reason: '' });
    await wrapper.vm.enterRandomPick();
    api.GetVideosByIDs.mockRejectedValueOnce(new Error('数据库不可用'));

    await wrapper.vm.reloadCurrentView();

    expect(alertSpy).toHaveBeenCalledWith(expect.stringContaining('刷新随机批次失败'));
    expect(wrapper.vm.randomPick.active).toBe(true);
    expect(wrapper.vm.videos.map(video => video.id)).toEqual([11, 12]);
    alertSpy.mockRestore();
    wrapper.unmount();
  });

  it('抽取途中改了筛选，回来的旧批次不会再装回列表', async () => {
    const wrapper = await mountPage();
    let resolvePick;
    api.PickRandomVideos.mockReturnValueOnce(new Promise(resolve => { resolvePick = resolve; }));
    const pending = wrapper.vm.enterRandomPick();

    wrapper.vm.searchKeyword = '换个关键词';
    await wrapper.vm.handleSearch(true);
    resolvePick({ videos: pickedVideos, selection_reason: '' });
    await pending;
    await flushPromises();

    expect(wrapper.vm.randomPick.active).toBe(false);
    expect(wrapper.vm.videos.map(video => video.id)).not.toContain(11);
    wrapper.unmount();
  });

  it('筛选范围内抽不到视频时保持原列表并提示', async () => {
    const wrapper = await mountPage();
    const alertSpy = vi.spyOn(window, 'alert').mockImplementation(() => {});
    wrapper.vm.videos = [{ id: 1, name: 'kept.mp4', tags: [] }];
    api.PickRandomVideos.mockResolvedValueOnce({
      videos: [],
      reason_code: 'no_filtered_videos',
      user_message: '随机取样失败：当前筛选范围没有可用的视频。'
    });

    await wrapper.vm.enterRandomPick();

    expect(alertSpy).toHaveBeenCalledWith('随机取样失败：当前筛选范围没有可用的视频。');
    expect(wrapper.vm.randomPick.active).toBe(false);
    expect(wrapper.vm.videos.map(video => video.id)).toEqual([1]);
    alertSpy.mockRestore();
    wrapper.unmount();
  });
});

describe('VideoListPage 多选批量栏', () => {
  const rows = [
    { id: 1, name: 'a.mp4', size: 2 * 1024 ** 3, tags: [] },
    { id: 2, name: 'b.mp4', size: 1024 ** 3, tags: [] }
  ];

  it('批量栏顶替结果条，显示已选数与合计体积', async () => {
    const wrapper = await mountPage();
    wrapper.vm.videos = [...rows];
    wrapper.vm.selectedVideoIds = [1, 2];
    await wrapper.vm.$nextTick();

    expect(wrapper.find('.result-bar').exists()).toBe(false);
    expect(wrapper.find('.selection-toolbar').exists()).toBe(true);
    expect(wrapper.text()).toContain('已选 2 个');
    expect(wrapper.vm.selectedTotalSizeText).toBe('3.0 GB');
    wrapper.unmount();
  });

  it('跨页选中时不猜合计体积', async () => {
    const wrapper = await mountPage();
    wrapper.vm.videos = [rows[0]];
    // 第 2 条不在已加载页里，拿不到 size —— 宁可不显示也不显示一个偏小的数。
    wrapper.vm.selectedVideoIds = [1, 2];
    await wrapper.vm.$nextTick();
    expect(wrapper.vm.selectedTotalSizeText).toBe('');
    wrapper.unmount();
  });

  it('Esc 清除选择', async () => {
    const wrapper = await mountPage();
    wrapper.vm.videos = [...rows];
    wrapper.vm.selectedVideoIds = [1, 2];
    await wrapper.vm.$nextTick();

    wrapper.vm.handleLibraryShortcut({ key: 'Escape', preventDefault() {} });
    expect(wrapper.vm.selectedVideoIds).toEqual([]);
    wrapper.unmount();
  });
});

describe('VideoListPage 工具栏三层重排', () => {
  it('结果条用后端计数回显命中数与全库总数', async () => {
    api.CountLibraryVideos.mockResolvedValue(218).mockResolvedValueOnce(218).mockResolvedValueOnce(3482);
    const wrapper = await mountPage();
    await flushPromises();

    expect(api.CountLibraryVideos).toHaveBeenCalled();
    expect(wrapper.vm.filteredCount).toBe(218);
    expect(wrapper.vm.libraryTotalCount).toBe(3482);
    expect(wrapper.text()).toContain('筛选出');
    wrapper.unmount();
  });

  it('条件回显把智能视图、标签和三类区间都写成中文，清除能一次复位', async () => {
    const wrapper = await mountPage({ tags: [{ id: 3, name: '科幻' }] });
    wrapper.vm.smartView = 'unwatched';
    wrapper.vm.selectedTags = [3];
    wrapper.vm.selectedSizeRange = { min: 2 * 1024 ** 3, max: 4 * 1024 ** 3 };
    wrapper.vm.minRating = '6';
    await wrapper.vm.$nextTick();

    const labels = wrapper.vm.activeConditionLabels;
    expect(labels[0]).toBe('未看');
    expect(labels).toContain('标签 科幻');
    expect(labels.some(text => text.startsWith('体积'))).toBe(true);
    expect(labels).toContain('评分 6–10');

    await wrapper.vm.clearAllConditions();
    expect(wrapper.vm.smartView).toBe('');
    expect(wrapper.vm.selectedTags).toEqual([]);
    expect(wrapper.vm.selectedSizeRange).toBe('all');
    expect(wrapper.vm.minRating).toBe('');
    expect(wrapper.vm.activeConditionLabels).toEqual([]);
    wrapper.unmount();
  });

  it('筛选徽标只数收进浮层的三类区间条件', async () => {
    const wrapper = await mountPage();
    expect(wrapper.vm.activeFilterCount).toBe(0);
    wrapper.vm.smartView = 'unwatched';
    await wrapper.vm.$nextTick();
    // 智能视图有自己的常驻控件，不该算进徽标，否则徽标和浮层内容对不上。
    expect(wrapper.vm.activeFilterCount).toBe(0);
    wrapper.vm.selectedResRange = { min: 2160, max: 0 };
    wrapper.vm.maxRating = '8';
    await wrapper.vm.$nextTick();
    expect(wrapper.vm.activeFilterCount).toBe(2);
    wrapper.unmount();
  });

  it('筛选浮层改的是草稿，应用后才生效', async () => {
    const wrapper = await mountPage();
    wrapper.vm.toggleToolbarMenu('filter', 'filterTrigger');
    wrapper.vm.filterDraft.minRating = '7';
    await wrapper.vm.$nextTick();
    // 还没点应用，生效条件不动。
    expect(wrapper.vm.minRating).toBe('');

    wrapper.vm.applyFilterDraft();
    await flushPromises();
    expect(wrapper.vm.minRating).toBe('7');
    expect(wrapper.vm.toolbarMenu).toBeNull();
    wrapper.unmount();
  });

  it('管理菜单保留全部 12 个维护动作并按四组分开', async () => {
    const wrapper = await mountPage({ settings: { local_metadata_enabled: true } });
    const items = wrapper.vm.manageMenuItems;
    expect(items.filter(item => item.heading).map(item => item.heading)).toEqual(['扫描', '整理', '补全', '维护']);
    expect(items.filter(item => item.id).map(item => item.id)).toEqual([
      'scan-new', 'scan-incremental',
      'move-folder', 'rename-folder', 'export-nfo',
      'backfill-technical', 'backfill-phash', 'backfill-local-metadata',
      'ai-tags', 'tag-manager', 'cleanup', 'trash'
    ]);
    wrapper.unmount();
  });

  it('随机菜单承载三种模式与随机 10 部，主键仍是按当前条件随机', async () => {
    const wrapper = await mountPage();
    const ids = wrapper.vm.randomMenuItems.filter(item => item.id).map(item => item.id);
    expect(ids).toEqual(['mode:balanced', 'mode:unwatched', 'mode:favorites', 'pick-ten']);
    expect(wrapper.vm.randomMenuItems[0].checked).toBe(true);

    wrapper.vm.onRandomSelect({ id: 'mode:favorites' });
    expect(wrapper.vm.randomMode).toBe('favorites');
    wrapper.unmount();
  });

  it('视图菜单合并了保存视图的三个控件，没有视图时删除项禁用', async () => {
    const wrapper = await mountPage();
    let items = wrapper.vm.viewMenuItems;
    expect(items.find(item => item.id === 'delete-current').disabled).toBe(true);

    wrapper.vm.savedViews = [{ id: 5, name: '未看 4K' }];
    wrapper.vm.selectedSavedViewID = 5;
    await wrapper.vm.$nextTick();
    items = wrapper.vm.viewMenuItems;
    expect(items[0]).toEqual(expect.objectContaining({ id: 'saved:5', label: '未看 4K', checked: true }));
    expect(items.find(item => item.id === 'delete-current').disabled).toBe(false);
    wrapper.unmount();
  });

  it('语义模式下不用结构化计数冒充命中数', async () => {
    const wrapper = await mountPage();
    wrapper.vm.searchMode = 'semantic';
    wrapper.vm.videos = [{ id: 1 }, { id: 2 }];
    wrapper.vm.hasMore = false;
    await wrapper.vm.$nextTick();
    expect(wrapper.vm.filteredCountText).toBe('2');

    api.CountLibraryVideos.mockClear();
    await wrapper.vm.refreshLibraryCounts();
    expect(api.CountLibraryVideos).not.toHaveBeenCalled();
    expect(wrapper.vm.filteredCount).toBeNull();
    wrapper.unmount();
  });
});

describe('VideoListPage 清理弹窗重排', () => {
  const analysis = {
    duplicate_groups: [{
      original: { id: 1, name: 'keep.mkv', directory: '/lib/a', path: '/lib/a/keep.mkv', size: 100 },
      candidates: [{ id: 2, name: 'copy.mkv', directory: '/lib/a', path: '/lib/a/copy.mkv', size: 100 }]
    }],
    near_duplicate_groups: [],
    same_source_groups: [],
    low_duration: [{ id: 3, name: 'short.mov', directory: '/lib/b', path: '/lib/b/short.mov', size: 10 }],
    low_resolution: []
  };

  async function openCleanup() {
    const wrapper = await mountPage();
    wrapper.vm.cleanupDialog.show = true;
    wrapper.vm.cleanupDialog.analysis = analysis;
    await wrapper.vm.$nextTick();
    return wrapper;
  }

  // 弹窗内容在 BaseModal 的插槽里，shallowMount 下不渲染，
  // 所以这里查状态；按钮的 disabled 绑定由源码断言钉住。
  it('默认零选中，可释放空间按整组扣掉建议保留项', async () => {
    const wrapper = await openCleanup();
    expect(wrapper.vm.cleanupSelection).toEqual([]);
    // 两组各有一个建议保留项：重复组保留 1（100B），短视频组只有它自己且是保留项。
    expect(wrapper.vm.cleanupReleasableText).toBe('100 B');
    wrapper.unmount();
  });

  it('类别筛选只收窄看到的候选，不改分析结果', async () => {
    const wrapper = await openCleanup();
    expect(wrapper.vm.cleanupFilteredSections).toHaveLength(2);
    wrapper.vm.cleanupCategory = 'low-duration';
    await wrapper.vm.$nextTick();
    expect(wrapper.vm.cleanupFilteredSections).toHaveLength(1);
    expect(wrapper.vm.cleanupFilteredSections[0].directory).toBe('/lib/b');
    // 分析结果本身没有被改动。
    expect(wrapper.vm.cleanupDialog.analysis.duplicate_groups).toHaveLength(1);
    wrapper.unmount();
  });

  it('「按建议勾选本组」只勾非保留项', async () => {
    const wrapper = await openCleanup();
    const section = wrapper.vm.cleanupDirectorySections.find(item => item.directory === '/lib/a');
    wrapper.vm.selectSuggestedInSection(section);
    // 建议保留的 1 不该被勾上，只勾副本 2。
    expect(wrapper.vm.cleanupSelection).toEqual([2]);
    wrapper.unmount();
  });

  it('底栏的可释放空间只算选中项，不含建议保留项', async () => {
    const wrapper = await openCleanup();
    expect(wrapper.vm.cleanupSelectedSizeText).toBe('');
    wrapper.vm.cleanupSelection = [2];
    await wrapper.vm.$nextTick();
    expect(wrapper.vm.cleanupSelectedSizeText).toBe('100 B');
    wrapper.unmount();
  });
});
