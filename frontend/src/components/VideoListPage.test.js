import { flushPromises, shallowMount } from '@vue/test-utils';

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
  'SearchLibraryVideoPage', 'SearchSemanticVideos', 'FindSimilarVideos', 'ListRecentlyPlayedWithFilter', 'GetLibrarySubtitleHits', 'PlayVideo', 'PlayRandomVideoWithFilter', 'PickRandomVideos', 'GetVideosByIDs', 'CountLibraryVideos', 'GetSemanticIndexStatus',
  'SetVideoFavorite', 'SetVideoWatched', 'UpdateVideoWatchProgress', 'ListSavedLibraryViews', 'SaveLibraryView',
  'DeleteSavedLibraryView', 'RejectSameSourceRelation', 'OpenDirectory', 'DeleteVideo', 'BatchDeleteVideos', 'ListTrashEntries',
  'RestoreTrashEntry', 'RemoveTagFromVideo', 'UpdateSettings', 'GetSubtitleEngineStatuses', 'PrepareSubtitleEngine',
  'GenerateSubtitle', 'ForceGenerateSubtitle', 'RenameVideo', 'RenameDirectory', 'MoveVideo', 'BatchMoveVideos', 'MoveDirectory',
  'SelectFolderToRename', 'SelectMigrationSourceDirectory', 'SelectMigrationDestinationDirectory', 'CancelSubtitle', 'CancelSubtitleTask',
  'GetSubtitleQueueState', 'GetCleanupStatus', 'GetAITaggingStatusSummary', 'StartCleanupAnalysis', 'GetSubtitleSegments',
  'GetPreviewSession', 'PreviewExternally', 'SyncScanDirectories', 'StartTechnicalBackfill', 'GetTechnicalBackfillStatus',
  'CancelTechnicalBackfill', 'StartLocalMetadataBackfill', 'GetLocalMetadataBackfillStatus', 'CancelLocalMetadataBackfill',
  'StartPerceptualHashBackfill', 'GetPerceptualHashBackfillStatus', 'CancelPerceptualHashBackfill',
  'ExportLocalMetadataNFO', 'StartLocalMetadataExport', 'GetLocalMetadataExportStatus', 'CancelLocalMetadataExport', 'GetSettings', 'LogFrontend',
  'CreatePlaybackProxy', 'BatchCreatePlaybackProxies', 'BatchCreatePlaybackProxiesForFilter'
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
import { commandList } from '../utils/commandRegistry.js';

async function mountPage(extraProps = {}, extraOptions = {}) {
  const wrapper = shallowMount(VideoListPage, {
    props: { tags: [], settings: {}, directories: [], ...extraProps },
    ...extraOptions
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
  api.GetSemanticIndexStatus.mockResolvedValue({ available: true, unavailable: '' });
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

  // 清理面板已抽成 video-list/CleanupReviewPanel.vue，其自身用例见同目录的
  // CleanupReviewPanel.test.js；这里只钉住片库页这一侧的接线。
  it('清理面板要删的候选仍由片库页删除：移出列表、给撤销条、再重载', async () => {
    const showDeleteUndo = vi.fn();
    const wrapper = await mountPage({}, {
      global: { stubs: { TrashUndoBanner: { template: '<div />', methods: { showDeleteUndo } } } }
    });
    wrapper.vm.videos = [{ id: 1, name: 'keep.mp4' }, { id: 2, name: 'copy.mp4' }];
    api.BatchDeleteVideos.mockResolvedValue({ requested: 1, succeeded: 1, failed: 0, errors: [] });
    // 第一步只删除并把行从已加载列表里摘掉；撤销条与重载是第二步，
    // 中间留给清理面板收窄勾选。
    api.SearchLibraryVideoPage.mockClear();

    const outcome = await wrapper.vm.trashCleanupVideos([2]);

    expect(api.BatchDeleteVideos).toHaveBeenCalledWith([2], true);
    expect(outcome.succeededIDs).toEqual([2]);
    expect(wrapper.vm.videos.map(video => video.id)).toEqual([1]);
    expect(showDeleteUndo).not.toHaveBeenCalled();
    expect(api.SearchLibraryVideoPage).not.toHaveBeenCalled();

    // 第二步：先撤销提示条，再重载列表。
    let reloadedBeforeUndo = false;
    showDeleteUndo.mockImplementation(async () => {
      reloadedBeforeUndo = api.SearchLibraryVideoPage.mock.calls.length > 0;
    });
    await wrapper.vm.afterTrashCleanupVideos(outcome.succeededIDs);

    expect(showDeleteUndo).toHaveBeenCalledWith([2], null);
    expect(reloadedBeforeUndo).toBe(false);
    expect(api.SearchLibraryVideoPage).toHaveBeenCalled();
    wrapper.unmount();
  });

  it('管理菜单的清理徽标与「分析中」文案读的是面板镜像出来的值', async () => {
    const wrapper = await mountPage();
    wrapper.vm.cleanupBadgeCount = 3;
    await wrapper.vm.$nextTick();
    // 徽标与文案由工具栏组件渲染（用例见 video-list/LibraryToolbar.test.js），
    // 片库页这一侧只负责把面板镜像出来的值挂到工具栏的 prop 上。
    expect(wrapper.findComponent({ name: 'LibraryToolbar' }).props('cleanupBadgeCount')).toBe(3);

    wrapper.vm.cleanupAnalyzing = true;
    await wrapper.vm.$nextTick();
    expect(wrapper.findComponent({ name: 'LibraryToolbar' }).props('cleanupAnalyzing')).toBe(true);
    wrapper.unmount();
  });

  // 两个重命名弹窗已抽成 video-list/RenameDialogs.vue，用例见同目录的 RenameDialogs.test.js。
  it('重命名回来的新文件名就地改掉那一行，不整表重载', async () => {
    const wrapper = await mountPage();
    wrapper.vm.videos = [{ id: 5, name: 'old.mkv', path: '/lib/old.mkv' }];

    wrapper.vm.applyVideoRename({ video: { id: 5, name: 'old.mkv', path: '/lib/old.mkv' }, finalName: 'new.mkv' });

    expect(wrapper.vm.videos[0]).toEqual(expect.objectContaining({ name: 'new.mkv', path: '/lib/new.mkv' }));
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

  // 后台任务状态条已抽成 video-list/BackgroundTaskStatusBars.vue，
  // 其自身用例见同目录的 BackgroundTaskStatusBars.test.js；这里只钉住片库页这一侧的接线。
  it('管理菜单的进度文案读的是状态条组件镜像回来的几份状态', async () => {
    const wrapper = await mountPage();
    wrapper.vm.applyBackgroundTaskState({
      technicalBackfill: { running: true, processed: 1, total: 4 },
      perceptualHash: { running: false },
      frameHash: { running: false },
      localMetadataBackfill: { running: false },
      localMetadataExport: { running: false }
    });
    await wrapper.vm.$nextTick();
    expect(wrapper.findComponent({ name: 'LibraryToolbar' }).props('technicalBackfill'))
      .toEqual(expect.objectContaining({ running: true, processed: 1, total: 4 }));
    wrapper.unmount();
  });

  it('NFO 写出把当前筛选算好再交给状态条组件', async () => {
    const startExport = vi.fn();
    const wrapper = await mountPage({}, {
      global: { stubs: { BackgroundTaskStatusBars: { template: '<div />', methods: { startLocalMetadataExport: startExport } } } }
    });
    wrapper.vm.searchKeyword = '导演剪辑版';
    wrapper.vm.selectedTags = [7];
    wrapper.vm.startLocalMetadataExport();

    expect(startExport).toHaveBeenCalledWith(expect.objectContaining({ keyword: '导演剪辑版', tag_ids: [7] }));
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
    // 文案由 video-list/RandomPickBanner.vue 渲染（用例见同目录的 RandomPickBanner.test.js）；这里钉住传过去的批次。
    expect(wrapper.findComponent({ name: 'RandomPickBanner' }).props('randomPick').reason)
      .toBe('在当前筛选范围内优先选择未看视频');
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
    api.PickRandomVideos.mockResolvedValueOnce({ videos: pickedVideos, selection_reason: '' });
    await wrapper.vm.enterRandomPick();
    api.GetVideosByIDs.mockRejectedValueOnce(new Error('数据库不可用'));

    await wrapper.vm.reloadCurrentView();

    expect(feedback.notifyError).toHaveBeenCalledWith(expect.stringContaining('刷新随机批次失败'));
    expect(wrapper.vm.randomPick.active).toBe(true);
    expect(wrapper.vm.videos.map(video => video.id)).toEqual([11, 12]);
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
    wrapper.vm.videos = [{ id: 1, name: 'kept.mp4', tags: [] }];
    api.PickRandomVideos.mockResolvedValueOnce({
      videos: [],
      reason_code: 'no_filtered_videos',
      user_message: '随机取样失败：当前筛选范围没有可用的视频。'
    });

    await wrapper.vm.enterRandomPick();

    expect(feedback.notifyError).toHaveBeenCalledWith('随机取样失败：当前筛选范围没有可用的视频。');
    expect(wrapper.vm.randomPick.active).toBe(false);
    expect(wrapper.vm.videos.map(video => video.id)).toEqual([1]);
    wrapper.unmount();
  });
});

describe('VideoListPage 工具栏接线', () => {
  // 工具栏本体已抽成 video-list/LibraryToolbar.vue，其用例见同目录的 LibraryToolbar.test.js。
  // 这两条钉的是「工具栏发回来的动作，片库页这一侧真的能执行完」——
  // 拆分时 isTagSelected / allVisibleSelected 被整体搬进工具栏，页面上的
  // toggleTagFilter / toggleSelectAllVisible 就地失效过，只有这种用例能抓住。
  it('点标签筛选真的会切换选中并重载，而不是半路抛错', async () => {
    const wrapper = await mountPage({ tags: [{ id: 3, name: '科幻' }] });
    api.SearchLibraryVideoPage.mockClear();

    wrapper.vm.toggleTagFilter(3);
    await flushPromises();
    expect(wrapper.vm.selectedTags).toEqual([3]);
    expect(api.SearchLibraryVideoPage).toHaveBeenCalled();

    wrapper.vm.toggleTagFilter(3);
    await flushPromises();
    expect(wrapper.vm.selectedTags).toEqual([]);
    wrapper.unmount();
  });

  it('「选择本页」在全选状态下必须能取消，而不是又选一遍', async () => {
    const wrapper = await mountPage();
    wrapper.vm.videos = [{ id: 1, name: 'a.mp4' }, { id: 2, name: 'b.mp4' }];
    await wrapper.vm.$nextTick();

    wrapper.vm.toggleSelectAllVisible();
    expect(wrapper.vm.selectedVideoIds).toEqual([1, 2]);

    wrapper.vm.toggleSelectAllVisible();
    expect(wrapper.vm.selectedVideoIds).toEqual([]);
    wrapper.unmount();
  });

  it('Esc 清除选择', async () => {
    const wrapper = await mountPage();
    wrapper.vm.videos = [{ id: 1, name: 'a.mp4' }, { id: 2, name: 'b.mp4' }];
    wrapper.vm.selectedVideoIds = [1, 2];
    await wrapper.vm.$nextTick();

    wrapper.vm.handleLibraryShortcut({ key: 'Escape', preventDefault() {} });
    expect(wrapper.vm.selectedVideoIds).toEqual([]);
    wrapper.unmount();
  });

  it('结果条的计数仍由后端筛选计数驱动', async () => {
    api.CountLibraryVideos.mockResolvedValue(218).mockResolvedValueOnce(218).mockResolvedValueOnce(3482);
    const wrapper = await mountPage();
    await flushPromises();

    expect(api.CountLibraryVideos).toHaveBeenCalled();
    expect(wrapper.vm.filteredCount).toBe(218);
    expect(wrapper.vm.libraryTotalCount).toBe(3482);
    wrapper.unmount();
  });

  it('浮层「应用」回来的草稿才写进生效条件', async () => {
    const wrapper = await mountPage();
    expect(wrapper.vm.minRating).toBe('');

    wrapper.vm.applyFilterConditions({ sizeRange: 'all', resRange: 'all', minRating: '7', maxRating: '' });
    await flushPromises();
    expect(wrapper.vm.minRating).toBe('7');
    wrapper.unmount();
  });

  it('「清除」把智能视图、标签与三类区间一次复位', async () => {
    const wrapper = await mountPage({ tags: [{ id: 3, name: '科幻' }] });
    wrapper.vm.smartView = 'unwatched';
    wrapper.vm.selectedTags = [3];
    wrapper.vm.selectedSizeRange = { min: 2 * 1024 ** 3, max: 4 * 1024 ** 3 };
    wrapper.vm.minRating = '6';
    await wrapper.vm.$nextTick();

    await wrapper.vm.clearAllConditions();
    expect(wrapper.vm.smartView).toBe('');
    expect(wrapper.vm.selectedTags).toEqual([]);
    expect(wrapper.vm.selectedSizeRange).toBe('all');
    expect(wrapper.vm.minRating).toBe('');
    wrapper.unmount();
  });

  it('语义模式下不向后端要结构化计数', async () => {
    const wrapper = await mountPage();
    wrapper.vm.searchMode = 'semantic';
    await wrapper.vm.$nextTick();

    api.CountLibraryVideos.mockClear();
    await wrapper.vm.refreshLibraryCounts();
    expect(api.CountLibraryVideos).not.toHaveBeenCalled();
    expect(wrapper.vm.filteredCount).toBeNull();
    wrapper.unmount();
  });

  it('随机菜单选中模式后仍由片库页记住模式', async () => {
    const wrapper = await mountPage();
    wrapper.vm.onRandomSelect({ id: 'mode:favorites' });
    expect(wrapper.vm.randomMode).toBe('favorites');
    wrapper.unmount();
  });
});

describe('VideoListPage 语义检索降级', () => {
  it('能力不可用时语义入口置灰并说明原因，而不是让用户搜出空结果', async () => {
    api.GetSemanticIndexStatus.mockResolvedValue({
      available: false,
      unavailable: '语义向量检索需要 PostgreSQL pgvector'
    });
    const wrapper = await mountPage();
    await flushPromises();

    expect(wrapper.vm.semanticAvailable).toBe(false);
    expect(wrapper.vm.semanticUnavailableNotice).toContain('pgvector');
    // 入口置灰由 LibraryToolbar 渲染、提示条由 SemanticNoticeBar 渲染，用例见 video-list/ 下的同名 .test.js。
    // 原因常驻可见，不用等到搜索之后。
    expect(wrapper.findComponent({ name: 'SemanticNoticeBar' }).props()).toEqual(expect.objectContaining({
      semanticAvailable: false,
      semanticUnavailableNotice: expect.stringContaining('pgvector')
    }));
    wrapper.unmount();
  });

  it('能力不可用时切不进语义模式', async () => {
    api.GetSemanticIndexStatus.mockResolvedValue({ available: false, unavailable: '缺少 pgvector' });
    const wrapper = await mountPage();
    await flushPromises();

    wrapper.vm.setSearchMode('semantic');
    expect(wrapper.vm.searchMode).not.toBe('semantic');
    wrapper.unmount();
  });

  it('停在语义模式时能力掉了会退回文件搜索', async () => {
    api.GetSemanticIndexStatus.mockResolvedValue({ available: false, unavailable: '缺少 pgvector' });
    const wrapper = await mountPage();
    wrapper.vm.searchMode = 'semantic';
    await wrapper.vm.loadSemanticStatus();
    await flushPromises();
    // 不能把用户卡在一个用不了的模式里。
    expect(wrapper.vm.searchMode).toBe('file');
    wrapper.unmount();
  });

  it('能力可用时语义入口正常，且状态未取回前不预判为不可用', async () => {
    const wrapper = await mountPage();
    await flushPromises();
    expect(wrapper.vm.semanticAvailable).toBe(true);
    expect(wrapper.findComponent({ name: 'SemanticNoticeBar' }).props('semanticAvailable')).toBe(true);

    // 状态还没回来时默认可用，避免启动瞬间入口闪一下灰。
    wrapper.vm.semanticStatus = null;
    await wrapper.vm.$nextTick();
    expect(wrapper.vm.semanticAvailable).toBe(true);
    wrapper.unmount();
  });
});

describe('IINA 进度同步不打乱列表', () => {
  it('就地更新受影响的行，不整表重载（重载会让刚看完的视频跳位置）', async () => {
    const wrapper = await mountPage();
    wrapper.vm.videos = [
      { id: 1, name: 'a.mp4', tags: [], watch_position_seconds: 0 },
      { id: 2, name: 'b.mp4', tags: [], watch_position_seconds: 30 },
      { id: 3, name: 'c.mp4', tags: [], watch_position_seconds: 0 }
    ];
    const reload = vi.spyOn(wrapper.vm, 'reloadCurrentView');
    api.SearchLibraryVideoPage.mockClear();

    const applied = wrapper.vm.applyWatchProgressUpdates([
      { video_id: 2, watch_position_seconds: 615.5 },
      { video_id: 99, watch_position_seconds: 10 }
    ]);
    await flushPromises();

    expect(applied).toBe(1);
    expect(wrapper.vm.videos[1].watch_position_seconds).toBe(615.5);
    // 顺序必须原样保持，用户才找得到刚才看的是哪个
    expect(wrapper.vm.videos.map(video => video.id)).toEqual([1, 2, 3]);
    expect(reload).not.toHaveBeenCalled();
    expect(api.SearchLibraryVideoPage).not.toHaveBeenCalled();

    wrapper.unmount();
  });

  it('没有变化时什么都不做', async () => {
    const wrapper = await mountPage();
    wrapper.vm.videos = [{ id: 1, name: 'a.mp4', tags: [], watch_position_seconds: 0 }];

    expect(wrapper.vm.applyWatchProgressUpdates([])).toBe(0);
    expect(wrapper.vm.applyWatchProgressUpdates(undefined)).toBe(0);

    wrapper.unmount();
  });
});

describe('命令面板接线', () => {
  it('挂载注册本页动作，卸载即注销', async () => {
    const wrapper = await mountPage();
    const ids = () => commandList().map(command => command.id);

    expect(ids()).toContain('action:scan-new');
    expect(ids()).toContain('action:random-play');

    wrapper.unmount();
    expect(ids()).not.toContain('action:scan-new');
  });

  it('扫描命令在迁移进行中灰显，执行时打开扫描弹窗', async () => {
    const wrapper = await mountPage();
    const command = commandList().find(item => item.id === 'action:scan-new');

    expect(command.enabled()).toBe(true);
    wrapper.vm.migrationRunning = true;
    expect(command.enabled()).toBe(false);

    wrapper.vm.migrationRunning = false;
    command.run();
    expect(wrapper.vm.showScanDialog).toBe(true);

    wrapper.unmount();
  });

  it('命令面板切智能视图走的还是工具栏那条重载路径', async () => {
    api.SearchLibraryVideoPage.mockResolvedValue({ videos: [], next_cursor: null });
    const wrapper = await mountPage();
    api.SearchLibraryVideoPage.mockClear();

    await wrapper.vm.applySmartViewCommand('favorites');
    await flushPromises();

    expect(wrapper.vm.smartView).toBe('favorites');
    expect(api.SearchLibraryVideoPage).toHaveBeenCalled();
    expect(api.SearchLibraryVideoPage.mock.calls[0][0].filter.smart_view).toBe('favorites');

    wrapper.unmount();
  });

  it('命令面板应用保存视图会套用该视图的全部条件', async () => {
    api.SearchLibraryVideoPage.mockResolvedValue({ videos: [], next_cursor: null });
    const wrapper = await mountPage();
    wrapper.vm.savedViews = [{
      id: 4, name: '收藏大文件', search_mode: 'file', keyword: 'sea', smart_view: 'favorites',
      tag_ids_json: '[]', min_size: 0, max_size: 0, min_height: 0, max_height: 0,
      min_rating: null, max_rating: null, sort_mode: 'balanced'
    }];
    api.SearchLibraryVideoPage.mockClear();

    await wrapper.vm.applySavedViewCommand(4);
    await flushPromises();

    expect(wrapper.vm.smartView).toBe('favorites');
    expect(wrapper.vm.searchKeyword).toBe('sea');
    expect(wrapper.vm.selectedSavedViewID).toBe(4);
    expect(api.SearchLibraryVideoPage).toHaveBeenCalled();

    wrapper.unmount();
  });
});

describe('播放代理入口（D-006）', () => {
  it('行菜单在「增强」组里追加生成播放代理', async () => {
    const wrapper = await mountPage();
    wrapper.vm.rowMenu = { video: { id: 7, name: 'seven.mkv' }, anchor: null, position: null };
    await wrapper.vm.$nextTick();
    const item = wrapper.vm.rowMenuItems.find(entry => entry.id === 'playback-proxy');
    expect(item).toBeTruthy();
    expect(item.label).toBe('生成播放代理');
    expect(item.disabled).toBe(false);
    wrapper.unmount();
  });

  it('任务在跑时行菜单项置灰', async () => {
    const wrapper = await mountPage();
    wrapper.vm.rowMenu = { video: { id: 7, name: 'seven.mkv' }, anchor: null, position: null };
    wrapper.vm.playbackProxy = { ...wrapper.vm.playbackProxy, running: true };
    await wrapper.vm.$nextTick();
    expect(wrapper.vm.rowMenuItems.find(entry => entry.id === 'playback-proxy').disabled).toBe(true);
    wrapper.unmount();
  });

  it('行菜单选中后为该视频入队', async () => {
    api.CreatePlaybackProxy.mockResolvedValue({ results: [{ video_id: 7, code: 'created' }] });
    const wrapper = await mountPage();
    wrapper.vm.rowMenu = { video: { id: 7, name: 'seven.mkv' }, anchor: null, position: null };
    await wrapper.vm.$nextTick();
    wrapper.vm.onRowMenuSelect({ id: 'playback-proxy' });
    await flushPromises();
    expect(api.CreatePlaybackProxy).toHaveBeenCalledWith(7);
    wrapper.unmount();
  });

  it('单个视频拿不到代理时把结果码翻成人话', async () => {
    api.CreatePlaybackProxy.mockResolvedValue({ results: [{ video_id: 7, code: 'probe_failed' }] });
    const wrapper = await mountPage();
    await wrapper.vm.createProxyForVideo({ id: 7, name: 'seven.mkv' });
    await flushPromises();
    expect(feedback.notify).toHaveBeenCalledWith(expect.stringContaining('读不出技术信息'));
    wrapper.unmount();
  });

  it('批量栏为选中的视频入队，未选中时不调后端', async () => {
    api.BatchCreatePlaybackProxies.mockResolvedValue({ total: 2 });
    const wrapper = await mountPage();
    await wrapper.vm.createProxiesForSelected();
    expect(api.BatchCreatePlaybackProxies).not.toHaveBeenCalled();

    wrapper.vm.selectedVideoIds = [3, 5];
    await wrapper.vm.createProxiesForSelected();
    await flushPromises();
    expect(api.BatchCreatePlaybackProxies).toHaveBeenCalledWith([3, 5]);
    wrapper.unmount();
  });

  it('管理菜单为当前筛选入队，走既有筛选 DTO', async () => {
    api.BatchCreatePlaybackProxiesForFilter.mockResolvedValue({ total: 4 });
    const wrapper = await mountPage();
    wrapper.vm.smartView = 'favorites';
    await wrapper.vm.$nextTick();
    wrapper.vm.onManageSelect({ id: 'backfill-playback-proxy' });
    await flushPromises();
    expect(api.BatchCreatePlaybackProxiesForFilter).toHaveBeenCalledTimes(1);
    expect(api.BatchCreatePlaybackProxiesForFilter.mock.calls[0][0].smart_view).toBe('favorites');
    wrapper.unmount();
  });

  it('当前筛选零命中时明确提示而不是静默', async () => {
    api.BatchCreatePlaybackProxiesForFilter.mockResolvedValue({ total: 0 });
    const wrapper = await mountPage();
    await wrapper.vm.createProxiesForCurrentFilter();
    await flushPromises();
    expect(feedback.notify).toHaveBeenCalledWith(expect.stringContaining('没有命中任何视频'));
    wrapper.unmount();
  });

  it('playback-proxy-state 事件驱动状态，只在跑完那一刻汇总提示一次', async () => {
    const handlers = {};
    window.runtime = { EventsOn: (name, handler) => { handlers[name] = handler; return () => {}; } };
    const wrapper = await mountPage();

    handlers['playback-proxy-state']({ running: true, processed: 1, total: 2, succeeded: 1, skipped: 0, failed: 0, results: [] });
    await wrapper.vm.$nextTick();
    expect(wrapper.vm.playbackProxy.running).toBe(true);
    expect(feedback.notify).not.toHaveBeenCalled();

    handlers['playback-proxy-state']({ running: false, completed: true, processed: 2, total: 2, succeeded: 1, skipped: 0, failed: 1, results: [] });
    await wrapper.vm.$nextTick();
    expect(feedback.notify).toHaveBeenCalledWith(expect.stringContaining('成功 1'));
    // 再来一条同样的终态不该重复提示。
    feedback.notify.mockClear();
    handlers['playback-proxy-state']({ running: false, completed: true, processed: 2, total: 2, succeeded: 1, skipped: 0, failed: 1, results: [] });
    await wrapper.vm.$nextTick();
    expect(feedback.notify).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it('取消的那一轮不弹汇总提示', async () => {
    const handlers = {};
    window.runtime = { EventsOn: (name, handler) => { handlers[name] = handler; return () => {}; } };
    const wrapper = await mountPage();
    handlers['playback-proxy-state']({ running: true, processed: 0, total: 2, results: [] });
    await wrapper.vm.$nextTick();
    handlers['playback-proxy-state']({ running: false, cancelled: true, processed: 1, total: 2, results: [] });
    await wrapper.vm.$nextTick();
    expect(feedback.notify).not.toHaveBeenCalled();
    wrapper.unmount();
  });
});

// 收藏 / 已看只改一列，返回值却要整行覆盖列表项；后端不带 tags 时一次收藏就把标签"清空"了。
describe('VideoListPage state toggles keep row tags', () => {
  it('preserves existing tags when the favorite response carries no tag array', async () => {
    const wrapper = await mountPage();
    wrapper.vm.videos = [{ id: 2, name: 'two.mp4', is_favorite: false, tags: [{ id: 1, name: '保留' }] }];
    api.SetVideoFavorite.mockResolvedValueOnce({ id: 2, name: 'two.mp4', is_favorite: true, tags: null });

    await wrapper.vm.toggleVideoFavorite(wrapper.vm.videos[0]);
    await flushPromises();

    expect(wrapper.vm.videos[0].is_favorite).toBe(true);
    expect(wrapper.vm.videos[0].tags).toEqual([{ id: 1, name: '保留' }]);
  });

  it('adopts the tag array when the response does carry one', async () => {
    const wrapper = await mountPage();
    wrapper.vm.videos = [{ id: 2, name: 'two.mp4', is_favorite: false, tags: [{ id: 1, name: '保留' }] }];
    api.SetVideoFavorite.mockResolvedValueOnce({ id: 2, name: 'two.mp4', is_favorite: true, tags: [] });

    await wrapper.vm.toggleVideoFavorite(wrapper.vm.videos[0]);
    await flushPromises();

    expect(wrapper.vm.videos[0].tags).toEqual([]);
  });
});
