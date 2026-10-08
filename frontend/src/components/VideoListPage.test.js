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
  'RemoveTagFromVideo', 'UpdateSettings', 'GetSubtitleEngineStatuses', 'PrepareSubtitleEngine',
  'GenerateSubtitle', 'ForceGenerateSubtitle', 'RenameVideo', 'RenameDirectory', 'MoveVideo', 'BatchMoveVideos', 'MoveDirectory',
  'SelectFolderToRename', 'SelectMigrationSourceDirectory', 'SelectMigrationDestinationDirectory', 'CancelSubtitle', 'CancelSubtitleTask',
  'GetSubtitleQueueState', 'GetCleanupStatus', 'GetAITaggingStatusSummary', 'GetSubtitleSegments',
  'GetPreviewSession', 'PreviewExternally', 'SyncScanDirectories', 'StartTechnicalBackfill', 'GetTechnicalBackfillStatus',
  'CancelTechnicalBackfill', 'StartLocalMetadataBackfill', 'GetLocalMetadataBackfillStatus', 'CancelLocalMetadataBackfill',
  'StartPerceptualHashBackfill', 'GetPerceptualHashBackfillStatus', 'CancelPerceptualHashBackfill',
  'ExportLocalMetadataNFO', 'StartLocalMetadataExport', 'GetLocalMetadataExportStatus', 'CancelLocalMetadataExport', 'GetSettings', 'LogFrontend',
  'CreatePlaybackProxy', 'BatchCreatePlaybackProxies', 'BatchCreatePlaybackProxiesForFilter',
  // P-034 接入的绑定。
  'ListContinueWatchingWithFilter', 'GetAutomaticOverrideKinds', 'SetVideoLiked', 'FilterActiveTagIDs', 'FilterActivePersonIDs',
  'GetPersonDetail', 'CheckMoveTarget', 'ListStaleReasonCounts', 'RecheckVideos', 'ReaddRemovedRoot', 'ValidateScanDirectory',
  'AddDirectory', 'RetryAITagging', 'GetEnhancementCapability',
  // P-036 接入的绑定（随机「换一个」、重新定位、字幕备份与索引同步），以及撤销条真身用到的删除绑定。
  'RerollRandom', 'SelectVideoFile', 'RelocateVideo', 'ListSubtitleBackups', 'RestoreSubtitleBackup',
  'GetSubtitleIndexSyncStatus', 'SyncSubtitleIndexNow',
  'DeleteVideosWithResult', 'DeleteImagesWithResult', 'PermanentlyDeleteVideos', 'PermanentlyDeleteImages', 'CancelBatchDelete', 'RestoreTrashBatch'
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
vi.mock('./TrashCenterDialog.vue', async (importOriginal) => ({ ...(await importOriginal()), default: { template: '<div />' } }));
vi.mock('./VirtualVideoList.vue', () => ({ default: { template: '<div />' } }));
// 行组件另有失效原因表的具名导出（片库页的「路径失效」分组复用），桩只换掉默认导出。
vi.mock('./VideoListRow.vue', async (importOriginal) => ({ ...(await importOriginal()), default: { template: '<div />' } }));
vi.mock('./AITagReviewDialog.vue', () => ({ default: { template: '<div />' } }));

import VideoListPage from './VideoListPage.vue';
import { commandList, registerCommands, unregisterCommands } from '../utils/commandRegistry.js';

// 撤销条 runDelete 返回的结果形状（见 video-list/TrashUndoBanner.vue emptyOutcome）。
function deleteOutcome({ trashed = [], recordOnly = [], fileMissing = [], permanent = [], cancelled = [], kept = [], failures = [], batchIDs = ['b1'] } = {}) {
  return {
    kind: 'video', deleteFile: true, batchIDs, trashed, recordOnly, fileMissing, permanent, cancelled, kept, failures,
    get removedIDs() { return [...this.trashed, ...this.recordOnly, ...this.fileMissing, ...this.permanent]; },
    get remainingIDs() { return [...this.failures.map(item => item.id), ...this.cancelled, ...this.kept]; }
  };
}

async function mountPage(extraProps = {}, extraOptions = {}) {
  const wrapper = shallowMount(VideoListPage, {
    props: { tags: [], settings: {}, directories: [], ...extraProps },
    ...extraOptions
  });
  await flushPromises();
  return wrapper;
}

it('标签转为人物后清掉旧标签筛选并重新加载片库', async () => {
  const wrapper = await mountPage();
  const reload = vi.spyOn(wrapper.vm, 'reloadCurrentView').mockResolvedValue();
  wrapper.vm.selectedTags = [4, 5];
  const result = { tag_id: 4, person: { id: 9 } };
  wrapper.vm.handleTagPersonConverted(result);
  expect(wrapper.vm.selectedTags).toEqual([5]);
  expect(reload).toHaveBeenCalledOnce();
  expect(wrapper.emitted('person-converted')[0]).toEqual([result]);
  wrapper.unmount();
});

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
  api.GetEnhancementCapability.mockResolvedValue({ available: true });
  api.ListStaleReasonCounts.mockResolvedValue({});
  api.GetAutomaticOverrideKinds.mockResolvedValue({});
  api.GetSubtitleIndexSyncStatus.mockResolvedValue({ running: false, checked: 0 });
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
    const outcome = deleteOutcome({ trashed: [2], failures: [{ id: 3, code: 'volume_offline', text: '所在磁盘未连接或不可访问，未做任何改动' }], kept: [4] });
    const runDelete = vi.fn().mockResolvedValue(outcome);
    const showDeleteNotice = vi.fn();
    const wrapper = await mountPage({}, {
      global: { stubs: { TrashUndoBanner: { template: '<div />', methods: { runDelete, showDeleteNotice } } } }
    });
    wrapper.vm.videos = [{ id: 1, name: 'keep.mp4' }, { id: 2, name: 'copy.mp4' }, { id: 3, name: 'offline.mp4' }, { id: 4, name: 'smb.mp4' }];
    // 第一步只删除并把行从已加载列表里摘掉；撤销条与重载是第二步，
    // 中间留给清理面板收窄勾选。
    api.SearchLibraryVideoPage.mockClear();

    const result = await wrapper.vm.trashCleanupVideos([2, 3, 4]);

    expect(runDelete).toHaveBeenCalledWith({ ids: [2, 3, 4], deleteFile: true, names: { 2: 'copy.mp4', 3: 'offline.mp4', 4: 'smb.mp4' } });
    expect(result.succeededIDs).toEqual([2]);
    expect([...result.failedIDs]).toEqual([3, 4]);
    // 面板认识的结果形状：失败项带视频 ID 与中文原因。
    expect(result.result).toEqual({
      requested: 3,
      succeeded: 1,
      failed: 2,
      errors: [
        { video_id: 3, error: '所在磁盘未连接或不可访问，未做任何改动' },
        { video_id: 4, error: '所在磁盘不支持废纸篓，已保留' }
      ]
    });
    expect(wrapper.vm.videos.map(video => video.id)).toEqual([1, 3, 4]);
    expect(showDeleteNotice).not.toHaveBeenCalled();
    expect(api.SearchLibraryVideoPage).not.toHaveBeenCalled();

    // 第二步：先撤销提示条，再重载列表；失败项由清理面板自己报告。
    let reloadedBeforeUndo = false;
    showDeleteNotice.mockImplementation(() => {
      reloadedBeforeUndo = api.SearchLibraryVideoPage.mock.calls.length > 0;
    });
    await wrapper.vm.afterTrashCleanupVideos(result.succeededIDs);

    expect(showDeleteNotice).toHaveBeenCalledWith(outcome, { reportFailures: false });
    expect(reloadedBeforeUndo).toBe(false);
    expect(api.SearchLibraryVideoPage).toHaveBeenCalled();
    wrapper.unmount();
  });

  // 2026-09-29（D-PC27、D-PC59、APP-11）：「管理」菜单的徽标与清理项并入顶栏的待处理工作台，
  // 片库页不再给工具栏传这几项，也不再每分钟拉一次 AI 汇总。
  it('APP-11 不再给工具栏传徽标与分析中镜像，也不再轮询 AI 汇总；标签上的删除入口已拆掉', async () => {
    const wrapper = await mountPage();
    const toolbar = wrapper.findComponent({ name: 'LibraryToolbar' });
    // P-040 已把这三个 prop 从工具栏删掉：既不再声明，页面也不再以任何写法传入。
    const attrs = Object.keys(toolbar.vm.$attrs);
    for (const [camel, kebab] of [['cleanupBadgeCount', 'cleanup-badge-count'], ['cleanupAnalyzing', 'cleanup-analyzing'], ['aiTagSummary', 'ai-tag-summary']]) {
      expect(Object.keys(toolbar.props())).not.toContain(camel);
      expect(attrs).not.toContain(camel);
      expect(attrs).not.toContain(kebab);
    }
    expect(api.GetAITaggingStatusSummary).not.toHaveBeenCalled();
    expect(toolbar.vm.$attrs.onDeleteTag).toBeUndefined();
    const panel = wrapper.findComponent({ name: 'CleanupReviewPanel' });
    expect(panel.vm.$attrs.onBadgeChange).toBeUndefined();
    expect(panel.vm.$attrs.onAnalyzingChange).toBeUndefined();
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
      selection_reason: '在当前筛选范围内仅选择未看视频'
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
      .toBe('在当前筛选范围内仅选择未看视频');
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

// 回归：后台对账、扫描汇总、行内操作之后的重载曾经先清空列表再从第一页取，
// 往下翻了几百条的用户会被弹回顶部，还看得到一闪的「加载中」。
describe('同一条件下的重载原地刷新，不清空、不回顶', () => {
  const rows = (from, count) => Array.from({ length: count }, (_, index) => ({ id: from + index, name: `v${from + index}.mp4`, tags: [] }));

  async function mountWithTwoPages() {
    api.SearchLibraryVideoPage
      .mockResolvedValueOnce({ videos: rows(1, 20), next_cursor: 'c1' })
      .mockResolvedValueOnce({ videos: rows(21, 20), next_cursor: 'c2' });
    const wrapper = await mountPage();
    await wrapper.vm.loadVideos();
    expect(wrapper.vm.videos).toHaveLength(40);
    expect(wrapper.vm.libraryCursor).toBe('c2');
    api.SearchLibraryVideoPage.mockClear();
    return wrapper;
  }

  it('取数期间旧列表原样留着，按已加载的条数从头重取，取齐后整体替换并留下仍在的选择', async () => {
    const wrapper = await mountWithTwoPages();
    wrapper.vm.selectedVideoIds = [5, 30];
    let resolveRefresh;
    api.SearchLibraryVideoPage.mockReturnValueOnce(new Promise(resolve => { resolveRefresh = resolve; }));

    const pending = wrapper.vm.reloadCurrentView();
    await flushPromises();
    expect(wrapper.vm.videos).toHaveLength(40);
    expect(wrapper.vm.loading).toBe(false);
    expect(wrapper.vm.refreshingInPlace).toBe(true);
    // 游标正被刷新占用，触底加载不能拿它往后翻
    await wrapper.vm.loadVideos();
    expect(api.SearchLibraryVideoPage).toHaveBeenCalledTimes(1);
    const request = api.SearchLibraryVideoPage.mock.calls[0][0];
    expect(request.limit).toBe(40);
    expect(request).not.toHaveProperty('cursor');

    // 第 30 条在后台被删掉了：第一批只回 39 条，再补 1 条把深度补齐
    api.SearchLibraryVideoPage.mockResolvedValueOnce({ videos: rows(41, 1), next_cursor: 'n2' });
    resolveRefresh({ videos: [...rows(1, 29), ...rows(31, 10)], next_cursor: 'n1' });
    await pending;

    expect(api.SearchLibraryVideoPage.mock.calls[1][0]).toMatchObject({ limit: 1, cursor: 'n1' });
    expect(wrapper.vm.videos.map(video => video.id)).toEqual([...rows(1, 29), ...rows(31, 11)].map(video => video.id));
    expect(wrapper.vm.selectedVideoIds).toEqual([5]);
    expect(wrapper.vm.libraryCursor).toBe('n2');
    expect(wrapper.vm.hasMore).toBe(true);
    expect(wrapper.vm.refreshingInPlace).toBe(false);
    wrapper.unmount();
  });

  it('已加载超过单次上限时分批重取，后一批接着前一批的游标', async () => {
    const wrapper = await mountPage();
    wrapper.vm.videos = rows(1, 150);
    wrapper.vm.libraryCursor = 'old';
    api.SearchLibraryVideoPage.mockClear();
    api.SearchLibraryVideoPage
      .mockResolvedValueOnce({ videos: rows(1, 100), next_cursor: 'p1' })
      .mockResolvedValueOnce({ videos: rows(101, 50), next_cursor: null });

    await wrapper.vm.reloadCurrentView();

    expect(api.SearchLibraryVideoPage.mock.calls.map(([request]) => [request.limit, request.cursor])).toEqual([[100, undefined], [50, 'p1']]);
    expect(wrapper.vm.videos).toHaveLength(150);
    expect(wrapper.vm.hasMore).toBe(false);
    wrapper.unmount();
  });

  it('重取失败：旧列表与游标原样保留并报错，触底加载照旧往后翻', async () => {
    const wrapper = await mountWithTwoPages();
    api.SearchLibraryVideoPage.mockRejectedValueOnce(new Error('数据库不可用'));

    await wrapper.vm.reloadCurrentView();

    expect(wrapper.vm.videos).toHaveLength(40);
    expect(wrapper.vm.libraryCursor).toBe('c2');
    expect(wrapper.vm.refreshingInPlace).toBe(false);
    expect(feedback.notifyError).toHaveBeenCalledWith(expect.stringContaining('加载视频失败'));
    wrapper.unmount();
  });

  it('取数途中条件变了：这次结果作废，旧列表与游标留着等新条件的重载', async () => {
    const wrapper = await mountWithTwoPages();
    let resolveRefresh;
    api.SearchLibraryVideoPage.mockReturnValueOnce(new Promise(resolve => { resolveRefresh = resolve; }));

    const pending = wrapper.vm.reloadCurrentView();
    await flushPromises();
    wrapper.vm.searchKeyword = '雨夜';
    resolveRefresh({ videos: rows(500, 40), next_cursor: 'stale' });
    await pending;

    expect(wrapper.vm.videos.map(video => video.id)).toEqual(rows(1, 40).map(video => video.id));
    expect(wrapper.vm.libraryCursor).toBe('c2');
    wrapper.unmount();
  });

  it('筛选条件变了仍然清空重来，从第一页取', async () => {
    const wrapper = await mountWithTwoPages();
    api.SearchLibraryVideoPage.mockResolvedValueOnce({ videos: rows(100, 3), next_cursor: null });

    wrapper.vm.toggleTagFilter(7);
    await flushPromises();

    expect(api.SearchLibraryVideoPage).toHaveBeenCalledTimes(1);
    expect(api.SearchLibraryVideoPage.mock.calls[0][0].limit).toBe(20);
    expect(wrapper.vm.videos.map(video => video.id)).toEqual([100, 101, 102]);
    wrapper.unmount();
  });

  it('退出随机批次回到普通列表时从第一页取，而不是按批次条数原地刷新', async () => {
    const wrapper = await mountWithTwoPages();
    api.PickRandomVideos.mockResolvedValueOnce({ videos: rows(900, 2), selection_reason: '' });
    await wrapper.vm.enterRandomPick();
    api.SearchLibraryVideoPage.mockResolvedValueOnce({ videos: rows(1, 20), next_cursor: 'c1' });

    await wrapper.vm.exitRandomPick();

    expect(api.SearchLibraryVideoPage).toHaveBeenCalledTimes(1);
    expect(api.SearchLibraryVideoPage.mock.calls[0][0].limit).toBe(20);
    expect(wrapper.vm.videos).toHaveLength(20);
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

describe('续播位置', () => {
  // D-PC41 推翻了 2026-09-13 的 1 秒容差：片尾区间 = min(时长 × 5%, 180 秒)，
  // 判定只在 utils/watchState.js 一处（样例与 Go 的 TestWatchCompletionSamplesPLAY11 共用）。
  it('PLAY-11 落进片尾区间就从头播：跳过片尾字幕的片子不再挂在「继续观看」', async () => {
    const wrapper = await mountPage();
    const at = (position, extra = {}) => wrapper.vm.resumePositionFor({
      id: 1, duration: 7200, watch_position_seconds: position, is_watched: false, ...extra
    });

    expect(at(3600)).toBe(3600);
    expect(at(7019)).toBe(7019);
    // 两小时片停在 1:57:30（片尾字幕开始）：tail=180，7050 ≥ 7020，按看完从头播。
    expect(at(7050)).toBe(0);
    expect(at(7200)).toBe(0);

    // 28 秒的短片按 5%（1.4 秒）收紧。
    const shortClip = position => wrapper.vm.resumePositionFor({ id: 3, duration: 28, watch_position_seconds: position, is_watched: false });
    expect(shortClip(26.5)).toBe(26.5);
    expect(shortClip(26.6)).toBe(0);
    wrapper.unmount();
  });

  it('PLAY-10 已看片按「断点是否在标已看之后写的」决定续不续播', async () => {
    const wrapper = await mountPage();
    const watchedAt = '2026-09-30T12:00:00+08:00';
    const resumeOf = progressAt => wrapper.vm.resumePositionFor({
      id: 1, duration: 7200, watch_position_seconds: 600, is_watched: true, watched_at: watchedAt, watch_progress_updated_at: progressAt
    });
    expect(resumeOf('2026-09-30T13:00:00+08:00')).toBe(600);
    expect(resumeOf('2026-09-30T11:00:00+08:00')).toBe(0);
    expect(resumeOf(null)).toBe(0);
    wrapper.unmount();
  });

  it('IINA 判成看完时就地补上已看，而不是只让进度条消失', async () => {
    const wrapper = await mountPage();
    wrapper.vm.videos = [
      { id: 1, name: 'a.mp4', tags: [], watch_position_seconds: 26, is_watched: false },
      { id: 2, name: 'b.mp4', tags: [], watch_position_seconds: 0, is_watched: false }
    ];
    wrapper.vm.applyWatchProgressUpdates([
      { video_id: 1, watch_position_seconds: 0, watched: true },
      { video_id: 2, watch_position_seconds: 14, watched: false }
    ]);

    expect(wrapper.vm.videos[0].is_watched).toBe(true);
    expect(wrapper.vm.videos[0].watch_position_seconds).toBe(0);
    expect(wrapper.vm.videos[1].is_watched).toBe(false);
    expect(wrapper.vm.videos[1].watch_position_seconds).toBe(14);
    wrapper.unmount();
  });
});

describe('字幕翻译入口', () => {
  it('翻译进行中整条入口置灰，而不是点了没反应', async () => {
    const wrapper = await mountPage();
    wrapper.vm.rowMenu = { video: { id: 7, name: 'seven.mkv' }, anchor: null, position: null };
    await wrapper.vm.$nextTick();
    const idle = wrapper.vm.rowMenuItems.find(entry => entry.id === 'subtitle-translate');
    expect(idle).toBeTruthy();
    expect(idle.disabled).toBe(false);

    // 翻译弹窗是单例：别的视频正在翻译时，这一项点了也只会被早返回吃掉。
    wrapper.vm.translatingSubtitleVideoId = 9;
    await wrapper.vm.$nextTick();
    const busyOther = wrapper.vm.rowMenuItems.find(entry => entry.id === 'subtitle-translate');
    expect(busyOther.disabled).toBe(true);
    // 「进行中」只该标在真正在翻译的那一行。
    expect(busyOther.label).not.toContain('进行中');

    wrapper.vm.translatingSubtitleVideoId = 7;
    await wrapper.vm.$nextTick();
    const busySelf = wrapper.vm.rowMenuItems.find(entry => entry.id === 'subtitle-translate');
    expect(busySelf.disabled).toBe(true);
    expect(busySelf.label).toContain('进行中');
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

describe('P-030 删除与回收站接线', () => {
  function bannerStub(outcome) {
    const runDelete = vi.fn().mockResolvedValue(outcome);
    const showDeleteNotice = vi.fn();
    const openTrashDialog = vi.fn();
    return {
      runDelete, showDeleteNotice, openTrashDialog,
      stubs: { TrashUndoBanner: { template: '<div />', methods: { runDelete, showDeleteNotice, openTrashDialog } } }
    };
  }

  it('LIB-12 批量删除经撤销条执行，只把真正删掉的行移出列表并给整批撤销提示', async () => {
    const banner = bannerStub(deleteOutcome({ trashed: [1, 2], cancelled: [3] }));
    const wrapper = await mountPage({}, { global: { stubs: banner.stubs } });
    const reload = vi.spyOn(wrapper.vm, 'reloadCurrentView').mockResolvedValue();
    wrapper.vm.videos = [{ id: 1, name: 'a.mp4' }, { id: 2, name: 'b.mp4' }, { id: 3, name: 'c.mp4' }];
    wrapper.vm.selectedVideoIds = [1, 2, 3];

    await wrapper.vm.deleteVideos([1, 2, 3], true);
    await flushPromises();

    expect(banner.runDelete).toHaveBeenCalledWith({ ids: [1, 2, 3], deleteFile: true, names: { 1: 'a.mp4', 2: 'b.mp4', 3: 'c.mp4' } });
    expect(banner.showDeleteNotice).toHaveBeenCalledTimes(1);
    expect(wrapper.vm.videos.map(video => video.id)).toEqual([3]);
    expect(wrapper.vm.selectedVideoIds).toEqual([3]);
    expect(reload).toHaveBeenCalledTimes(1);
    expect(wrapper.vm.deletingIds).toEqual([]);
    wrapper.unmount();
  });

  it('LIB-12 单个删除走同一条带结果码的路径', async () => {
    const banner = bannerStub(deleteOutcome({ recordOnly: [5] }));
    const wrapper = await mountPage({}, { global: { stubs: banner.stubs } });
    wrapper.vm.videos = [{ id: 5, name: 'e.mp4' }];

    await wrapper.vm.deleteVideo({ id: 5, name: 'e.mp4' }, false);

    expect(banner.runDelete).toHaveBeenCalledWith({ ids: [5], deleteFile: false, names: { 5: 'e.mp4' } });
    expect(api.DeleteVideo).not.toHaveBeenCalled();
    expect(api.BatchDeleteVideos).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it('确认删除时先关确认框，删除进度与「不支持废纸篓」的选择框不被它挡住', async () => {
    let dialogOpenDuringDelete = null;
    const banner = bannerStub(deleteOutcome({ trashed: [1, 2] }));
    const wrapper = await mountPage({}, { global: { stubs: banner.stubs } });
    banner.runDelete.mockImplementation(async () => {
      dialogOpenDuringDelete = wrapper.vm.deleteDialog.show;
      return deleteOutcome({ trashed: [1, 2] });
    });
    wrapper.vm.deleteDialog = { show: true, video: null, videoIds: [1, 2] };

    await wrapper.vm.executeDelete({ video: null, deleteFile: true, dontAskAgain: false });

    expect(dialogOpenDuringDelete).toBe(false);
    expect(banner.runDelete).toHaveBeenCalledWith(expect.objectContaining({ ids: [1, 2], deleteFile: true }));
    wrapper.unmount();
  });

  it('从回收站恢复后放掉清理面板的已删标记、重载列表，来自回收站对话框的再刷新清理状态', async () => {
    const wrapper = await mountPage();
    const forget = vi.spyOn(wrapper.vm, 'forgetCleanupTrashed').mockImplementation(() => {});
    const reload = vi.spyOn(wrapper.vm, 'reloadCurrentView').mockResolvedValue();
    const refresh = vi.spyOn(wrapper.vm, 'refreshCleanupStatus').mockResolvedValue();

    await wrapper.vm.afterTrashRestore([2, 3], false);
    expect(forget.mock.calls.map(call => call[0])).toEqual([2, 3]);
    expect(reload).toHaveBeenCalledTimes(1);
    expect(refresh).not.toHaveBeenCalled();

    await wrapper.vm.afterTrashRestore([], true);
    expect(refresh).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });

  it('管理菜单的「回收站」打开回收站中心的视频页签', async () => {
    const banner = bannerStub(deleteOutcome());
    const wrapper = await mountPage({}, { global: { stubs: banner.stubs } });
    wrapper.vm.onManageSelect({ id: 'trash' });
    expect(banner.openTrashDialog).toHaveBeenCalledWith('video');
    wrapper.unmount();
  });

  it('注册待处理工作台要用的三条命令：清理、AI 审阅、本地资料有更新', async () => {
    const wrapper = await mountPage();
    const byID = id => commandList().find(item => item.id === id);
    const openCleanup = vi.spyOn(wrapper.vm, 'openCleanupDialog').mockImplementation(() => {});
    const applyView = vi.spyOn(wrapper.vm, 'applySmartViewCommand').mockResolvedValue();

    byID('library.openCleanup').run();
    expect(openCleanup).toHaveBeenCalledTimes(1);
    byID('library.openAIReview').run();
    expect(wrapper.vm.aiTagReviewDialog.show).toBe(true);
    byID('library.openLocalMetadataUpdates').run();
    expect(applyView).toHaveBeenCalledWith('local_metadata_updated');

    wrapper.unmount();
    expect(byID('library.openCleanup')).toBeUndefined();
  });
});

describe('P-034 观看进度与继续观看', () => {
  it('PLAY-10 UpdateVideoWatchProgress 走 5 参契约：抽屉带了 origin 与时长就照用', async () => {
    const wrapper = await mountPage();
    api.UpdateVideoWatchProgress.mockResolvedValue({ id: 5, watch_position_seconds: 30 });
    wrapper.vm.handlePreviewWatchProgress({ videoID: 5, positionSeconds: 30, completed: false, origin: 'jump', durationSeconds: 1800 });
    await wrapper.vm._watchProgressPromise;
    expect(api.UpdateVideoWatchProgress).toHaveBeenCalledWith(5, 30, 1800, false, 'jump');
    wrapper.unmount();
  });

  it('PLAY-10 抽屉没带 origin 时按打开方式推断：字幕命中为 jump、续播为 resume、从头为 start；时长未知传 0', async () => {
    const wrapper = await mountPage();
    api.UpdateVideoWatchProgress.mockResolvedValue(null);
    const video = { id: 5, name: 'e.mp4', duration: 7200, watch_position_seconds: 600, is_watched: false, tags: [] };
    wrapper.vm.videos = [video];
    wrapper.vm.selectedPreviewVideoId = 5;

    wrapper.vm.previewStartTimeMs = 42000;
    wrapper.vm.handlePreviewWatchProgress({ videoID: 5, positionSeconds: 50 });
    await wrapper.vm._watchProgressPromise;
    expect(api.UpdateVideoWatchProgress).toHaveBeenLastCalledWith(5, 50, 0, false, 'jump');

    wrapper.vm.previewStartTimeMs = null;
    wrapper.vm.handlePreviewWatchProgress({ videoID: 5, positionSeconds: 610, durationSeconds: -1 });
    await wrapper.vm._watchProgressPromise;
    expect(api.UpdateVideoWatchProgress).toHaveBeenLastCalledWith(5, 610, 0, false, 'resume');

    wrapper.vm.videos = [{ ...video, watch_position_seconds: 0 }];
    wrapper.vm.handlePreviewWatchProgress({ videoID: 5, positionSeconds: 5, completed: true });
    await wrapper.vm._watchProgressPromise;
    expect(api.UpdateVideoWatchProgress).toHaveBeenLastCalledWith(5, 5, 0, true, 'start');

    // 抽屉里点进去的嵌套条目：起播点由抽屉决定，按 resume 上报。
    wrapper.vm.handlePreviewWatchProgress({ videoID: 9, positionSeconds: 12 });
    await wrapper.vm._watchProgressPromise;
    expect(api.UpdateVideoWatchProgress).toHaveBeenLastCalledWith(9, 12, 0, false, 'resume');
    wrapper.unmount();
  });

  it('PLAY-09 「继续观看」在均衡排序下走键集接口，游标原样回传上一页最后一行的进度时间与 id', async () => {
    const wrapper = await mountPage();
    const page1 = Array.from({ length: 20 }, (_, index) => ({ id: 100 - index, name: `v${index}.mp4`, tags: [], watch_progress_updated_at: `2026-09-30T12:00:${String(59 - index).padStart(2, '0')}.123456789+08:00` }));
    api.ListContinueWatchingWithFilter.mockResolvedValueOnce({ videos: page1, automatic_override_kinds: { 100: ['short_video'] } })
      .mockResolvedValueOnce({ videos: [{ id: 3, name: 'old.mp4', tags: [], watch_progress_updated_at: null }], automatic_override_kinds: {} });
    api.SearchLibraryVideoPage.mockClear();

    wrapper.vm.smartView = 'continue_watching';
    await wrapper.vm.handleSearch(true);
    await flushPromises();
    expect(api.ListContinueWatchingWithFilter).toHaveBeenCalledWith(expect.objectContaining({ smart_view: 'continue_watching' }), '', 0, 20);
    expect(api.SearchLibraryVideoPage).not.toHaveBeenCalled();
    expect(wrapper.vm.overrideKindsFor({ id: 100 })).toEqual(['short_video']);
    expect(wrapper.vm.hasMore).toBe(true);

    await wrapper.vm.loadVideos();
    expect(api.ListContinueWatchingWithFilter).toHaveBeenLastCalledWith(expect.any(Object), '2026-09-30T12:00:40.123456789+08:00', 81, 20);
    expect(wrapper.vm.hasMore).toBe(false);
    expect(wrapper.vm.videos).toHaveLength(21);

    // 换了排序就回到共享分页：排序语义由用户选。
    api.SearchLibraryVideoPage.mockResolvedValue({ videos: [] });
    wrapper.vm.sortMode = 'size_desc';
    await wrapper.vm.handleSearch(true);
    await flushPromises();
    expect(api.SearchLibraryVideoPage).toHaveBeenCalled();
    wrapper.unmount();
  });

  it('PLAY-10 matchesSmartView 的「继续观看」按 resumable：重看中的已看片仍属于它', async () => {
    const wrapper = await mountPage();
    wrapper.vm.smartView = 'continue_watching';
    const watchedAt = '2026-09-30T12:00:00+08:00';
    expect(wrapper.vm.matchesSmartView({ watch_position_seconds: 10, is_watched: false })).toBe(true);
    expect(wrapper.vm.matchesSmartView({ watch_position_seconds: 10, is_watched: true, watched_at: watchedAt, watch_progress_updated_at: '2026-09-30T13:00:00+08:00' })).toBe(true);
    expect(wrapper.vm.matchesSmartView({ watch_position_seconds: 10, is_watched: true, watched_at: watchedAt, watch_progress_updated_at: '2026-09-30T11:00:00+08:00' })).toBe(false);
    wrapper.unmount();
  });
});

describe('P-034 智能视图口径', () => {
  it('META-10 「未打标签」只看非自动标签', async () => {
    const wrapper = await mountPage();
    wrapper.vm.smartView = 'untagged';
    expect(wrapper.vm.matchesSmartView({ tags: [{ id: 1, automatic_kind: 'short_video' }] })).toBe(true);
    expect(wrapper.vm.matchesSmartView({ tags: [{ id: 2, automatic_kind: '' }] })).toBe(false);
    expect(wrapper.vm.matchesSmartView({ tags: [] })).toBe(true);
    wrapper.unmount();
  });

  it('META-09 「本地资料有更新」的行就地修改后仍留在视图里（状态只由本地资料弹窗改变）', async () => {
    const wrapper = await mountPage();
    wrapper.vm.smartView = 'local_metadata_updated';
    expect(wrapper.vm.matchesSmartView({ id: 1, tags: [] })).toBe(true);
    wrapper.unmount();
  });

  it('LIB-10 失效记录只属于「路径失效」视图，按原因筛时只认同一原因（空原因归 unknown）', async () => {
    const wrapper = await mountPage();
    expect(wrapper.vm.matchesSmartView({ is_stale: true })).toBe(false);
    wrapper.vm.smartView = 'favorites';
    expect(wrapper.vm.matchesSmartView({ is_stale: true, is_favorite: true })).toBe(false);
    wrapper.vm.smartView = 'stale';
    expect(wrapper.vm.matchesSmartView({ is_stale: true, stale_reason: 'offline_root' })).toBe(true);
    wrapper.vm.staleReasonFilter = 'unknown';
    expect(wrapper.vm.matchesSmartView({ is_stale: true, stale_reason: '' })).toBe(true);
    expect(wrapper.vm.matchesSmartView({ is_stale: true, stale_reason: 'offline_root' })).toBe(false);
    expect(wrapper.vm.matchesSmartView({ is_stale: false })).toBe(false);
    wrapper.unmount();
  });
});

describe('P-034 行标签「手动」角标', () => {
  it('META-13 列表载荷自带的 automatic_override_kinds 按视频 ID 交给行组件', async () => {
    api.SearchLibraryVideoPage.mockResolvedValueOnce({ videos: [{ id: 7, name: 'a.mp4', tags: [{ id: 1, automatic_kind: 'short_video' }] }], automatic_override_kinds: { 7: ['short_video'] } });
    const wrapper = await mountPage();
    expect(wrapper.vm.overrideKindsFor({ id: 7 })).toEqual(['short_video']);
    expect(wrapper.vm.overrideKindsFor({ id: 8 })).toEqual([]);
    expect(api.GetAutomaticOverrideKinds).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it('META-13 最近播放、语义搜索、随机批次这些返回数组的接口用 GetAutomaticOverrideKinds 批量补齐，只查带自动标签的行', async () => {
    const wrapper = await mountPage();
    api.ListRecentlyPlayedWithFilter.mockResolvedValueOnce([
      { id: 1, name: 'a.mp4', tags: [{ id: 3, automatic_kind: 'low_resolution' }] },
      { id: 2, name: 'b.mp4', tags: [{ id: 4, name: '动作' }] }
    ]);
    api.GetAutomaticOverrideKinds.mockResolvedValueOnce({ 1: ['low_resolution'] });
    wrapper.vm.smartView = 'recently_played';
    await wrapper.vm.handleSearch(true);
    await flushPromises();
    expect(api.GetAutomaticOverrideKinds).toHaveBeenCalledWith([1]);
    expect(wrapper.vm.overrideKindsFor({ id: 1 })).toEqual(['low_resolution']);

    api.GetVideosByIDs.mockResolvedValueOnce([{ id: 5, name: 'e.mp4', tags: [{ id: 9, automatic_kind: 'short_video' }] }]);
    api.GetAutomaticOverrideKinds.mockResolvedValueOnce({});
    wrapper.vm.randomPick = { active: true, ids: [5], reason: '', loading: false };
    await wrapper.vm.refreshRandomPick();
    expect(api.GetAutomaticOverrideKinds).toHaveBeenLastCalledWith([5]);
    expect(wrapper.vm.overrideKindsFor({ id: 5 })).toEqual([]);
    wrapper.unmount();
  });
});

describe('P-034 人物筛选与保存视图', () => {
  it('META-02 工具栏选的人物进入筛选 DTO 的 person_ids，清除条件一并清掉', async () => {
    const wrapper = await mountPage();
    api.SearchLibraryVideoPage.mockClear();
    wrapper.vm.selectedSavedViewID = 3;
    wrapper.findComponent({ name: 'LibraryToolbar' }).vm.$emit('update:selectedPeople', [{ id: 9, name: '张三' }, { id: 4, name: '李四' }]);
    await flushPromises();
    expect(wrapper.vm.currentLibraryFilter().person_ids).toEqual([9, 4]);
    expect(wrapper.findComponent({ name: 'LibraryToolbar' }).props('selectedPeople')).toEqual([{ id: 9, name: '张三' }, { id: 4, name: '李四' }]);
    expect(wrapper.vm.selectedSavedViewID).toBe(0);
    expect(api.SearchLibraryVideoPage).toHaveBeenLastCalledWith(expect.objectContaining({ filter: expect.objectContaining({ person_ids: [9, 4] }) }));

    wrapper.vm.clearAllConditions();
    expect(wrapper.vm.selectedPeople).toEqual([]);
    expect(wrapper.vm.currentLibraryFilter().person_ids).toEqual([]);
    wrapper.unmount();
  });

  it('LIB-15 应用保存视图时剔除已删除的标签与人物，照常应用其余条件并提示「N 个条件已失效」', async () => {
    const open = vi.fn();
    const wrapper = await mountPage({}, { global: { stubs: { SaveViewDialog: { template: '<div />', methods: { open } } } } });
    wrapper.vm.savedViews = [{
      id: 4, name: '张三的动作片', search_mode: 'file', keyword: '', smart_view: '',
      tag_ids_json: '[3, 8]', person_ids_json: '[9, 12]', min_size: 0, max_size: 0, min_height: 0, max_height: 0,
      min_rating: null, max_rating: null, sort_mode: 'balanced'
    }];
    api.FilterActiveTagIDs.mockResolvedValueOnce({ tag_ids: [3], dropped: 1 });
    api.FilterActivePersonIDs.mockResolvedValueOnce({ person_ids: [9], dropped: 1 });
    api.GetPersonDetail.mockResolvedValueOnce({ person: { person: { id: 9, display_name: '张三' } } });

    await wrapper.vm.applySavedViewCommand(4);
    await flushPromises();

    expect(api.FilterActiveTagIDs).toHaveBeenCalledWith([3, 8]);
    expect(api.FilterActivePersonIDs).toHaveBeenCalledWith([9, 12]);
    expect(wrapper.vm.selectedTags).toEqual([3]);
    expect(wrapper.vm.selectedPeople).toEqual([{ id: 9, name: '张三' }]);
    expect(wrapper.get('[data-test="saved-view-notice"]').text()).toContain('保存视图「张三的动作片」有 2 个条件已失效');

    await wrapper.get('[data-test="saved-view-notice-update"]').trigger('click');
    expect(open).toHaveBeenCalledWith('update');

    // 条件一改，当前条件就不再是那个视图了，提示随之消失。
    wrapper.vm.toggleTagFilter(3);
    await wrapper.vm.$nextTick();
    expect(wrapper.find('[data-test="saved-view-notice"]').exists()).toBe(false);
    expect(wrapper.vm.lastAppliedSavedViewID).toBe(4);
    wrapper.unmount();
  });

  it('META-07 视图菜单的「用当前条件更新」「重命名」打开同一个弹窗的对应模式', async () => {
    const open = vi.fn();
    const wrapper = await mountPage({}, { global: { stubs: { SaveViewDialog: { template: '<div />', methods: { open } } } } });
    wrapper.vm.onViewSelect({ id: 'update-current' });
    wrapper.vm.onViewSelect({ id: 'rename-current' });
    wrapper.vm.onViewSelect({ id: 'save-current' });
    expect(open.mock.calls).toEqual([['update'], ['rename'], ['create']]);
    wrapper.unmount();
  });
});

describe('P-034 路径失效：分组、重新检查、加回目录', () => {
  it('LIB-10 进「路径失效」视图按原因分组显示计数，点一组只看这一组', async () => {
    api.ListStaleReasonCounts.mockResolvedValue({ offline_root: 5, removed_root: 2, unknown: 1 });
    const wrapper = await mountPage();
    wrapper.vm.smartView = 'stale';
    await flushPromises();
    const bar = wrapper.get('[data-test="stale-reason-bar"]');
    expect(bar.get('[data-test="stale-reason-all"]').text()).toBe('全部 8');
    expect(bar.get('[data-test="stale-reason-offline_root"]').text()).toBe('磁盘未连接 5');
    expect(bar.get('[data-test="stale-reason-removed_root"]').text()).toBe('目录已移除 2');
    expect(bar.get('[data-test="stale-reason-unknown"]').text()).toBe('原因未记录 1');
    expect(bar.find('[data-test="stale-reason-missing_file"]').exists()).toBe(false);

    api.SearchLibraryVideoPage.mockClear();
    await bar.get('[data-test="stale-reason-removed_root"]').trigger('click');
    await flushPromises();
    expect(api.SearchLibraryVideoPage).toHaveBeenLastCalledWith(expect.objectContaining({ filter: expect.objectContaining({ smart_view: 'stale', stale_reason: 'removed_root' }) }));

    // 离开失效视图时放掉按原因的筛选。
    wrapper.vm.smartView = '';
    await flushPromises();
    expect(wrapper.vm.staleReasonFilter).toBe('');
    expect(wrapper.vm.currentLibraryFilter().stale_reason).toBe('');
    wrapper.unmount();
  });

  it('LIB-10 「重新检查所选」走窄对账，完成后报结果、重载列表并刷新分组计数', async () => {
    api.ListStaleReasonCounts.mockResolvedValue({ missing_file: 2 });
    api.RecheckVideos.mockResolvedValueOnce({ restored: 1, relocated: 1, stale: 0, error_count: 0 });
    const wrapper = await mountPage();
    wrapper.vm.smartView = 'stale';
    await flushPromises();
    wrapper.vm.videos = [{ id: 1, is_stale: true }, { id: 2, is_stale: true }];
    wrapper.vm.selectedVideoIds = [1, 2];
    await wrapper.vm.$nextTick();
    api.ListStaleReasonCounts.mockClear();

    await wrapper.get('[data-test="stale-recheck-selected"]').trigger('click');
    await flushPromises();
    expect(api.RecheckVideos).toHaveBeenCalledWith([1, 2]);
    expect(feedback.notify).toHaveBeenCalledWith('重新检查完成：恢复 1 个，找到新位置 1 个。');
    expect(api.ListStaleReasonCounts).toHaveBeenCalled();
    wrapper.unmount();
  });

  it('LIB-10 失效行的行菜单有「重新检查」；目录被移除或不在扫描范围的还有「加回目录…」', async () => {
    const wrapper = await mountPage();
    wrapper.vm.rowMenu = { video: { id: 1, name: 'a.mp4', is_stale: true, stale_reason: 'removed_root' }, anchor: null, position: null };
    let ids = wrapper.vm.rowMenuItems.map(item => item.id);
    expect(ids).toEqual(expect.arrayContaining(['recheck', 'readd-root']));
    wrapper.vm.rowMenu = { video: { id: 2, name: 'b.mp4', is_stale: true, stale_reason: 'offline_root' }, anchor: null, position: null };
    ids = wrapper.vm.rowMenuItems.map(item => item.id);
    expect(ids).toContain('recheck');
    expect(ids).not.toContain('readd-root');
    wrapper.vm.rowMenu = { video: { id: 3, name: 'c.mp4', is_stale: false }, anchor: null, position: null };
    ids = wrapper.vm.rowMenuItems.map(item => item.id);
    expect(ids).not.toContain('recheck');

    api.RecheckVideos.mockResolvedValueOnce({ restored: 0, relocated: 0 });
    wrapper.vm.rowMenu = { video: { id: 2, name: 'b.mp4', is_stale: true, stale_reason: 'offline_root' }, anchor: null, position: null };
    wrapper.vm.onRowMenuSelect({ id: 'recheck' });
    await flushPromises();
    expect(api.RecheckVideos).toHaveBeenCalledWith([2]);
    expect(feedback.notify).toHaveBeenCalledWith('重新检查完成：文件仍不在原处，记录保持失效。');
    wrapper.unmount();
  });

  it('LIB-01 「加回目录」找到原来的扫描目录，确认后加回；文案只写目录名', async () => {
    const wrapper = await mountPage({ directories: [{ id: 1, path: '/Volumes/Media', alias: '媒体盘' }] });
    api.ReaddRemovedRoot.mockResolvedValueOnce('/Volumes/Media/Movies');
    api.ValidateScanDirectory.mockResolvedValueOnce({ exists: true, duplicate_of: '', nested_in: '/Volumes/Media', contains: [] });
    api.AddDirectory.mockResolvedValueOnce({ id: 2 });

    await wrapper.vm.readdRemovedRoot({ id: 7, name: 'a.mp4' });
    await flushPromises();

    const confirm = feedback.confirmAction.mock.calls.at(-1)[0];
    expect(confirm.message).toContain('把目录「Movies」加回扫描目录');
    expect(confirm.message).toContain('扫描目录「媒体盘」之内');
    expect(confirm.message).not.toContain('/Volumes');
    expect(api.AddDirectory).toHaveBeenCalledWith('/Volumes/Media/Movies', 'Movies');
    expect(wrapper.emitted('reload-directories')).toHaveLength(1);
    wrapper.unmount();
  });

  it('LIB-01 原目录不在（磁盘未连接）时不加回；已经是扫描目录时改为重新检查', async () => {
    const wrapper = await mountPage();
    api.ReaddRemovedRoot.mockResolvedValue('/Volumes/Gone/Movies');
    api.ValidateScanDirectory.mockResolvedValueOnce({ exists: false, duplicate_of: '', nested_in: '', contains: [] });
    await wrapper.vm.readdRemovedRoot({ id: 7 });
    expect(feedback.notifyError).toHaveBeenCalledWith(expect.stringContaining('原来的目录「Movies」现在找不到'));
    expect(api.AddDirectory).not.toHaveBeenCalled();

    api.ValidateScanDirectory.mockResolvedValueOnce({ exists: true, duplicate_of: '/Volumes/Gone/Movies', nested_in: '', contains: [] });
    api.RecheckVideos.mockResolvedValueOnce({ restored: 1 });
    await wrapper.vm.readdRemovedRoot({ id: 7 });
    await flushPromises();
    expect(api.AddDirectory).not.toHaveBeenCalled();
    expect(api.RecheckVideos).toHaveBeenCalledWith([7]);
    wrapper.unmount();
  });
});

describe('P-034 播放失败与后台重定位', () => {
  const failure = (reason, extra = {}) => ({
    video: { id: 5, name: 'e.mp4', path: '/Volumes/Media/e.mp4' },
    dispatch_succeeded: false,
    reason,
    user_message: '播放失败: e.mp4 (/Volumes/Media/e.mp4)\n原因: 源文件不存在或已被移动。',
    reconcile_result: {
      video_id: 5, reason, did_mark_stale: true, needs_reload: false,
      updated_video: { id: 5, name: 'e.mp4', path: '/Volumes/Media/e.mp4', is_stale: true, stale_reason: reason, tags: null }
    },
    ...extra
  });

  it('PLAY-12 离线盘：那一行就地标成「磁盘未连接」，不整页重载，提示给出下一步', async () => {
    const wrapper = await mountPage();
    wrapper.vm.videos = [{ id: 4, name: 'd.mp4', tags: [] }, { id: 5, name: 'e.mp4', tags: [{ id: 1, name: '保留' }] }];
    api.PlayVideo.mockResolvedValueOnce(failure('offline_root'));
    api.SearchLibraryVideoPage.mockClear();

    await wrapper.vm.playVideo(5);
    await wrapper.vm.$nextTick();

    expect(wrapper.vm.videos.map(video => video.id)).toEqual([4, 5]);
    expect(wrapper.vm.videos[1]).toEqual(expect.objectContaining({ is_stale: true, stale_reason: 'offline_root', tags: [{ id: 1, name: '保留' }] }));
    expect(api.SearchLibraryVideoPage).not.toHaveBeenCalled();
    expect(feedback.notifyError).not.toHaveBeenCalled();
    const notice = wrapper.get('[data-test="play-failure-notice"]');
    expect(notice.text()).toContain('「e.mp4」所在的磁盘未连接');
    expect(notice.text()).not.toContain('/Volumes');

    const openStale = vi.spyOn(wrapper.vm, 'handleSearch').mockResolvedValue();
    await notice.get('[data-test="play-failure-open-stale"]').trigger('click');
    expect(wrapper.vm.smartView).toBe('stale');
    expect(wrapper.vm.staleReasonFilter).toBe('offline_root');
    expect(openStale).toHaveBeenCalled();
    wrapper.unmount();
  });

  it('PLAY-12 文件不在原处：提示后台在找新位置；找到后 video-relocated 就地恢复那一行', async () => {
    const handlers = {};
    window.runtime = { EventsOn: (name, handler) => { handlers[name] = handler; return () => {}; } };
    const wrapper = await mountPage();
    wrapper.vm.videos = [{ id: 5, name: 'e.mp4', path: '/Volumes/Media/e.mp4', tags: [] }];
    api.PlayVideo.mockResolvedValueOnce(failure('missing_file'));

    await wrapper.vm.playVideo(5);
    await wrapper.vm.$nextTick();
    expect(wrapper.get('[data-test="play-failure-notice"]').text()).toContain('正在后台查找');

    handlers['video-relocated']({ video_id: 5, new_path: '/Volumes/Media/Moved/e2.mp4' });
    await wrapper.vm.$nextTick();
    expect(wrapper.vm.videos[0]).toEqual(expect.objectContaining({ is_stale: false, stale_reason: '', name: 'e2.mp4', directory: '/Volumes/Media/Moved', path: '/Volumes/Media/Moved/e2.mp4' }));
    expect(wrapper.find('[data-test="play-failure-notice"]').exists()).toBe(false);
    expect(feedback.notify).toHaveBeenCalledWith(expect.stringContaining('已找到「e.mp4」的新位置'));
    wrapper.unmount();
  });

  it('PLAY-12 在「路径失效」视图里重定位成功的行直接移走', async () => {
    const handlers = {};
    window.runtime = { EventsOn: (name, handler) => { handlers[name] = handler; return () => {}; } };
    const wrapper = await mountPage();
    wrapper.vm.smartView = 'stale';
    await flushPromises();
    wrapper.vm.videos = [{ id: 5, name: 'e.mp4', is_stale: true }, { id: 6, name: 'f.mp4', is_stale: true }];
    handlers['video-relocated']({ video_id: 5, new_path: '/lib/e.mp4' });
    expect(wrapper.vm.videos.map(video => video.id)).toEqual([6]);
    wrapper.unmount();
  });

  it('PLAY-12 没有标失效的播放失败照常报错，但文案里不带完整路径', async () => {
    const wrapper = await mountPage();
    api.PlayVideo.mockResolvedValueOnce({
      video: { id: 5, name: 'e.mp4', path: '/Volumes/Media/e.mp4' }, dispatch_succeeded: false, reason: 'error',
      user_message: '播放失败: e.mp4 (/Volumes/Media/e.mp4)\n原因: 没有找到 IINA'
    });
    await wrapper.vm.playVideo(5);
    expect(feedback.notifyError).toHaveBeenCalledWith('播放失败: e.mp4\n原因: 没有找到 IINA');
    wrapper.unmount();
  });
});

describe('P-034 扫描摘要', () => {
  function scanBarStub() {
    const showSummary = vi.fn();
    return {
      showSummary,
      stubs: { IncrementalScanBar: { template: '<div />', data: () => ({ incrementalScan: { running: false } }), methods: { showSummary, runIncrementalScan() {} } } }
    };
  }

  it('LIB-08 启动扫描与改目录触发的扫描结果交给增量扫描条显示，有变化时重载列表与总数', async () => {
    const handlers = {};
    window.runtime = { EventsOn: (name, handler) => { handlers[name] = handler; return () => {}; } };
    const bar = scanBarStub();
    const wrapper = await mountPage({}, { global: { stubs: bar.stubs } });
    const reload = vi.spyOn(wrapper.vm, 'reloadCurrentView').mockResolvedValue();
    wrapper.vm.libraryTotalCount = 10;

    const startup = { trigger: 'startup', result: { added: 0, deleted: 0, restored: 3, stale: 0, skip_breakdown: { legacy_trash: 2 } } };
    handlers['library-scan-summary'](startup);
    await flushPromises();
    expect(bar.showSummary).toHaveBeenCalledWith(startup);
    expect(reload).toHaveBeenCalledTimes(1);
    expect(wrapper.vm.libraryTotalCount).toBeNull();

    handlers['library-scan-summary']({ trigger: 'directory_change', result: { added: 0, deleted: 0, restored: 0, stale: 0 } });
    await flushPromises();
    expect(bar.showSummary).toHaveBeenCalledTimes(2);
    expect(reload).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });

  it('LIB-08 增量扫描条自己发起的手动扫描由它自己汇报，事件不重复处理', async () => {
    const handlers = {};
    window.runtime = { EventsOn: (name, handler) => { handlers[name] = handler; return () => {}; } };
    const bar = scanBarStub();
    const wrapper = await mountPage({}, { global: { stubs: bar.stubs } });
    const reload = vi.spyOn(wrapper.vm, 'reloadCurrentView').mockResolvedValue();
    wrapper.vm.incrementalScan = { running: true, state: 'running', message: '' };
    handlers['library-scan-summary']({ trigger: 'manual', result: { added: 4 } });
    await flushPromises();
    expect(bar.showSummary).not.toHaveBeenCalled();
    expect(reload).not.toHaveBeenCalled();
    wrapper.unmount();
  });
});

describe('P-034 迁移到扫描目录之外', () => {
  // 确认框要真的渲染出来：浅挂载默认会把 BaseModal 换成不渲染内容的桩。
  const withModal = { global: { stubs: { BaseModal: false } } };

  it('LIB-09 目标不在扫描目录里先确认；取消就不迁移', async () => {
    const wrapper = await mountPage({}, withModal);
    api.SelectMigrationDestinationDirectory.mockResolvedValueOnce('/Volumes/Backup/Out');
    api.CheckMoveTarget.mockResolvedValueOnce({ in_scan_roots: false });
    const running = wrapper.vm.moveVideo({ id: 5, name: 'e.mp4' });
    await flushPromises();
    const dialog = wrapper.get('[data-test="move-target-confirm"]');
    expect(dialog.text()).toContain('「Out」不在任何扫描目录里');
    expect(dialog.text()).not.toContain('/Volumes');
    wrapper.vm.resolveMoveTargetConfirm(false);
    await running;
    expect(api.MoveVideo).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it('LIB-09 勾上「同时把目标加入扫描目录」：迁移成功后把目标加进扫描目录', async () => {
    const wrapper = await mountPage({}, withModal);
    api.SelectMigrationDestinationDirectory.mockResolvedValueOnce('/Volumes/Backup/Out');
    api.CheckMoveTarget.mockResolvedValueOnce({ in_scan_roots: false });
    api.MoveVideo.mockResolvedValueOnce({ video_id: 5 });
    api.AddDirectory.mockResolvedValueOnce({ id: 3 });
    const running = wrapper.vm.moveVideo({ id: 5, name: 'e.mp4' });
    await flushPromises();
    await wrapper.get('[data-test="move-target-add-root"]').setValue(true);
    await wrapper.get('[data-test="move-target-continue"]').trigger('click');
    await running;
    expect(api.MoveVideo).toHaveBeenCalledWith(5, '/Volumes/Backup/Out');
    expect(api.AddDirectory).toHaveBeenCalledWith('/Volumes/Backup/Out', 'Out');
    expect(api.MoveVideo.mock.invocationCallOrder[0]).toBeLessThan(api.AddDirectory.mock.invocationCallOrder[0]);
    wrapper.unmount();
  });

  it('LIB-09 目标在扫描目录里就不打扰；迁移的正是某个扫描目录本身时也不问', async () => {
    const wrapper = await mountPage({ directories: [{ id: 1, path: '/lib/A', alias: 'A' }] });
    api.SelectMigrationDestinationDirectory.mockResolvedValueOnce('/lib/B');
    api.CheckMoveTarget.mockResolvedValueOnce({ in_scan_roots: true });
    api.BatchMoveVideos.mockResolvedValueOnce({ succeeded: 1, failed: 0, errors: [], warnings: [] });
    wrapper.vm.selectedVideoIds = [5];
    await wrapper.vm.moveSelectedVideos();
    expect(wrapper.find('[data-test="move-target-confirm"]').exists()).toBe(false);
    expect(api.BatchMoveVideos).toHaveBeenCalledWith([5], '/lib/B');

    api.SelectMigrationSourceDirectory.mockResolvedValueOnce('/lib/A');
    api.SelectMigrationDestinationDirectory.mockResolvedValueOnce('/Volumes/Other');
    api.MoveDirectory.mockResolvedValueOnce({ videos_updated: 2, directories_updated: 1 });
    api.CheckMoveTarget.mockClear();
    await wrapper.vm.moveFolder();
    expect(api.CheckMoveTarget).not.toHaveBeenCalled();
    expect(api.MoveDirectory).toHaveBeenCalledWith('/lib/A', '/Volumes/Other');
    wrapper.unmount();
  });
});

describe('P-034 空状态', () => {
  it('APP-10 没配扫描目录：给「添加扫描目录」按钮，点了打开扫描弹窗', async () => {
    const wrapper = await mountPage({ directories: [] });
    const empty = wrapper.get('[data-test="library-empty-no-directories"]');
    expect(empty.text()).toContain('还没有扫描目录');
    await empty.get('[data-test="library-empty-add-directory"]').trigger('click');
    expect(wrapper.vm.showScanDialog).toBe(true);
    wrapper.unmount();
  });

  it('APP-10 有筛选条件却没结果：说「没有符合条件的视频」，并能一键清除条件', async () => {
    const wrapper = await mountPage({ directories: [{ id: 1, path: '/lib' }] });
    wrapper.vm.selectedPeople = [{ id: 9, name: '张三' }];
    await wrapper.vm.$nextTick();
    const empty = wrapper.get('[data-test="library-empty-no-match"]');
    expect(empty.text()).toContain('没有符合条件的视频');
    expect(empty.text()).not.toContain('暂无视频');
    await empty.get('[data-test="library-empty-clear-conditions"]').trigger('click');
    expect(wrapper.vm.selectedPeople).toEqual([]);
    wrapper.unmount();
  });

  it('APP-10 目录里确实没有视频：给「重新扫描」', async () => {
    const run = vi.fn();
    const wrapper = await mountPage({ directories: [{ id: 1, path: '/lib' }] }, {
      global: { stubs: { IncrementalScanBar: { template: '<div />', methods: { runIncrementalScan: run } } } }
    });
    const empty = wrapper.get('[data-test="library-empty-no-videos"]');
    expect(empty.text()).toContain('扫描目录里还没有找到视频');
    await empty.get('[data-test="library-empty-rescan"]').trigger('click');
    expect(run).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });
});

describe('P-034 行菜单新增项', () => {
  it('PLAY-02 行菜单有点赞 / 取消点赞，走 SetVideoLiked；「点赞」视图里取消点赞会重载', async () => {
    const wrapper = await mountPage();
    wrapper.vm.videos = [{ id: 2, name: 'b.mp4', is_liked: false, tags: [] }];
    wrapper.vm.rowMenu = { video: wrapper.vm.videos[0], anchor: null, position: null };
    expect(wrapper.vm.rowMenuItems.find(item => item.id === 'like').label).toBe('点赞');
    api.SetVideoLiked.mockResolvedValueOnce({ id: 2, name: 'b.mp4', is_liked: true, tags: [] });
    wrapper.vm.onRowMenuSelect({ id: 'like' });
    await flushPromises();
    expect(api.SetVideoLiked).toHaveBeenCalledWith(2, true);
    expect(wrapper.vm.videos[0].is_liked).toBe(true);

    const reload = vi.spyOn(wrapper.vm, 'reloadCurrentView').mockResolvedValue();
    wrapper.vm.smartView = 'liked';
    api.SetVideoLiked.mockResolvedValueOnce({ id: 2, name: 'b.mp4', is_liked: false, tags: [] });
    await wrapper.vm.toggleVideoLiked(wrapper.vm.videos[0]);
    expect(api.SetVideoLiked).toHaveBeenLastCalledWith(2, false);
    expect(reload).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });

  it('META-01 「重新分析 AI 标签」对已有人工标签的视频照样排进分析（D-PC28 规则 5）', async () => {
    const wrapper = await mountPage();
    api.RetryAITagging.mockResolvedValueOnce();
    wrapper.vm.rowMenu = { video: { id: 3, name: 'c.mp4', tags: [{ id: 1, name: '动作' }] }, anchor: null, position: null };
    wrapper.vm.onRowMenuSelect({ id: 'ai-reanalyze' });
    await flushPromises();
    expect(api.RetryAITagging).toHaveBeenCalledWith(3);
    expect(feedback.notify).toHaveBeenCalledWith(expect.stringContaining('已安排重新分析「c.mp4」'));
    wrapper.unmount();
  });

  it('MEDIA-11 超分运行时未就绪时菜单项写「未就绪」，点了打开设置页的超分分区', async () => {
    api.GetEnhancementCapability.mockResolvedValueOnce({ available: false, reason_code: 'runtime_missing' });
    const openSection = vi.fn();
    registerCommands('test-settings', [{ id: 'action:settings:enhance', group: 'action', label: '设置 · 视频超分', run: openSection }]);
    const wrapper = await mountPage();
    const openDialog = vi.spyOn(wrapper.vm, 'openEnhanceDialog').mockImplementation(() => {});
    wrapper.vm.rowMenu = { video: { id: 3, name: 'c.mp4' }, anchor: null, position: null };
    expect(wrapper.vm.rowMenuItems.find(item => item.id === 'enhance').label).toBe('视频超分（未就绪）');
    wrapper.vm.onRowMenuSelect({ id: 'enhance' });
    expect(openSection).toHaveBeenCalledTimes(1);
    expect(openDialog).not.toHaveBeenCalled();

    wrapper.vm.enhanceCapability = { available: true };
    expect(wrapper.vm.rowMenuItems.find(item => item.id === 'enhance').label).toBe('视频超分…');
    wrapper.vm.onRowMenuSelect({ id: 'enhance' });
    expect(openDialog).toHaveBeenCalledTimes(1);
    unregisterCommands('test-settings');
    wrapper.unmount();
  });
});

describe('P-034 审阅与标签管理的回调', () => {
  it('META-08 审阅里点「去清理」：关掉审阅，打开清理中心并把这一对交给面板', async () => {
    const open = vi.fn();
    const wrapper = await mountPage({}, { global: { stubs: { CleanupReviewPanel: { template: '<div />', methods: { open, refreshStatus() {}, forgetTrashed() {} } } } } });
    wrapper.vm.openAITagReviewDialog();
    wrapper.findComponent({ name: 'AITagReviewDialog' }).vm.$emit('open-cleanup', { relationId: 9, videoIds: [1, 2] });
    await flushPromises();
    expect(wrapper.vm.aiTagReviewDialog.show).toBe(false);
    expect(open).toHaveBeenCalledWith({ relationId: 9 });
    wrapper.unmount();
  });

  it('D-PC02 清理面板传来的文件名表优先（P-032 约定），候选不在已加载列表里时二选一弹窗也能列出文件名', async () => {
    const outcome = deleteOutcome({ trashed: [2] });
    const runDelete = vi.fn().mockResolvedValue(outcome);
    const wrapper = await mountPage({}, {
      global: { stubs: { TrashUndoBanner: { template: '<div />', methods: { runDelete, showDeleteNotice: vi.fn() } } } }
    });
    wrapper.vm.videos = [{ id: 2, name: 'loaded.mp4' }];
    await wrapper.vm.trashCleanupVideos([2, 7], { names: { 2: 'copy.mp4', 7: 'far-away.mp4' } });
    expect(runDelete).toHaveBeenCalledWith({ ids: [2, 7], deleteFile: true, names: { 2: 'copy.mp4', 7: 'far-away.mp4' } });
    wrapper.unmount();
  });

  it('META-02 撤销标签转人物：重载标签与列表，筛选里已被删掉的人物剔除，图片页经 person-converted 同步', async () => {
    const wrapper = await mountPage();
    const reload = vi.spyOn(wrapper.vm, 'reloadCurrentView').mockResolvedValue();
    wrapper.vm.selectedPeople = [{ id: 9, name: '张三' }, { id: 4, name: '李四' }];
    api.FilterActivePersonIDs.mockResolvedValueOnce({ person_ids: [4], dropped: 1 });
    wrapper.findComponent({ name: 'TagManagerDialog' }).vm.$emit('conversion-undone', { conversion_id: 1, tag: { id: 12, name: '张三' }, person_deleted: true });
    await flushPromises();
    expect(wrapper.emitted('reload-tags')).toBeTruthy();
    expect(wrapper.vm.selectedPeople).toEqual([{ id: 4, name: '李四' }]);
    expect(wrapper.emitted('person-converted').at(-1)[0]).toEqual(expect.objectContaining({ tag_id: 12, undone: true }));
    expect(reload).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });
});

describe('P-036 随机播放：换一个与本机记忆（PLAY-08）', () => {
  const played = (id, token = 'tok-' + id) => ({
    dispatch_succeeded: true,
    video: { id, name: `v${id}.mp4`, tags: [] },
    selection_reason: '在当前筛选范围内仅选择未看视频',
    reroll_token: token
  });

  it('PLAY-08 随机播放后出结果条（30 秒内可换），并把这一部记进本机排除表', async () => {
    const wrapper = await mountPage();
    api.PlayRandomVideoWithFilter.mockResolvedValueOnce(played(5));
    await wrapper.vm.playRandom();
    await flushPromises();

    const banner = wrapper.findComponent({ name: 'RandomPickBanner' }).props('randomPlay');
    expect(banner).toEqual(expect.objectContaining({ active: true, videoName: 'v5.mp4', rerollable: true, reason: '在当前筛选范围内仅选择未看视频' }));
    expect(window.localStorage.setItem).toHaveBeenCalledWith('library-random-recent', '[5]');
    // 结果条取代了旧的「正在随机播放」提示。
    expect(feedback.notify).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it('PLAY-08 30 秒内「换一个」走 RerollRandom，新结果接管结果条，不再另起一次普通随机', async () => {
    const wrapper = await mountPage();
    api.PlayRandomVideoWithFilter.mockResolvedValueOnce(played(5, 'tok-a'));
    await wrapper.vm.playRandom();
    api.RerollRandom.mockResolvedValueOnce(played(6, 'tok-b'));
    wrapper.findComponent({ name: 'RandomPickBanner' }).vm.$emit('reroll');
    await flushPromises();

    expect(api.RerollRandom).toHaveBeenCalledWith('tok-a');
    expect(api.PlayRandomVideoWithFilter).toHaveBeenCalledTimes(1);
    expect(wrapper.vm.randomPlay).toEqual(expect.objectContaining({ token: 'tok-b', rerollable: true }));
    expect(wrapper.vm.randomPlayBanner.videoName).toBe('v6.mp4');
    expect(wrapper.vm.recentRandomVideoIDs).toEqual([5, 6]);
    wrapper.unmount();
  });

  it('PLAY-08 后端答 reroll_expired（已提交）时改为普通的再随机一次', async () => {
    const wrapper = await mountPage();
    api.PlayRandomVideoWithFilter.mockResolvedValueOnce(played(5, 'tok-a'));
    await wrapper.vm.playRandom();
    api.RerollRandom.mockResolvedValueOnce({ dispatch_succeeded: false, reason_code: 'reroll_expired', user_message: '「换一个」已失效' });
    api.PlayRandomVideoWithFilter.mockResolvedValueOnce(played(8, 'tok-c'));
    await wrapper.vm.rerollRandomPlay();
    await flushPromises();

    expect(api.RerollRandom).toHaveBeenCalledWith('tok-a');
    expect(api.PlayRandomVideoWithFilter).toHaveBeenCalledTimes(2);
    expect(api.PlayRandomVideoWithFilter).toHaveBeenLastCalledWith(expect.objectContaining({ exclude_ids: [5] }));
    expect(feedback.notifyError).not.toHaveBeenCalled();
    expect(wrapper.vm.randomPlay.token).toBe('tok-c');
    wrapper.unmount();
  });

  it('PLAY-08 过了 30 秒「换一个」直接普通随机，不再拿旧令牌', async () => {
    const wrapper = await mountPage();
    api.PlayRandomVideoWithFilter.mockResolvedValueOnce(played(5, 'tok-a'));
    await wrapper.vm.playRandom();
    wrapper.vm.randomPlay = { ...wrapper.vm.randomPlay, startedAt: Date.now() - 31_000 };
    api.PlayRandomVideoWithFilter.mockResolvedValueOnce(played(9));
    await wrapper.vm.rerollRandomPlay();

    expect(api.RerollRandom).not.toHaveBeenCalled();
    expect(api.PlayRandomVideoWithFilter).toHaveBeenCalledTimes(2);
    wrapper.unmount();
  });

  it('PLAY-08 随机模式与最近排除表从本机读回，改模式写回', async () => {
    window.localStorage.getItem.mockImplementation(key => ({
      'library-random-mode': 'unwatched',
      'library-random-recent': '[3,4]'
    })[key] ?? null);
    const wrapper = await mountPage();
    expect(wrapper.vm.randomMode).toBe('unwatched');
    expect(wrapper.vm.recentRandomVideoIDs).toEqual([3, 4]);
    api.PlayRandomVideoWithFilter.mockResolvedValueOnce({ dispatch_succeeded: false, reason_code: 'no_filtered_videos', user_message: '没有可播放的视频' });
    await wrapper.vm.playRandom();
    expect(api.PlayRandomVideoWithFilter).toHaveBeenCalledWith(expect.objectContaining({ mode: 'unwatched', exclude_ids: [3, 4] }));

    wrapper.vm.onRandomSelect({ id: 'mode:favorites' });
    await flushPromises();
    expect(window.localStorage.setItem).toHaveBeenCalledWith('library-random-mode', 'favorites');
    wrapper.unmount();
  });

  it('PLAY-08 语义搜索模式下随机被挡住，命令面板的「随机播放」同样不可用', async () => {
    const wrapper = await mountPage();
    wrapper.vm.searchMode = 'semantic';
    await wrapper.vm.playRandom();
    expect(api.PlayRandomVideoWithFilter).not.toHaveBeenCalled();
    expect(feedback.notify).toHaveBeenCalledWith('语义搜索结果不支持随机，请切回文件或字幕搜索。');
    const command = commandList().find(item => item.id === 'action:random-play');
    expect(command.enabled()).toBe(false);
    wrapper.unmount();
  });
});

describe('P-036 行菜单：重新定位与恢复字幕', () => {
  const staleVideo = { id: 3, name: 'lost.mp4', path: '/old/lost.mp4', is_stale: true, stale_reason: 'missing_file', tags: [] };

  it('LIB-10 失效行的菜单有「重新定位文件…」：选了文件就改路径、就地刷新该行；取消选择什么都不做', async () => {
    const wrapper = await mountPage();
    wrapper.vm.videos = [{ ...staleVideo }, { id: 4, name: 'other.mp4', tags: [] }];
    wrapper.vm.rowMenu = { video: wrapper.vm.videos[0], anchor: null, position: null };
    expect(wrapper.vm.rowMenuItems.map(item => item.id)).toContain('relocate');

    api.SelectVideoFile.mockResolvedValueOnce('');
    wrapper.vm.onRowMenuSelect({ id: 'relocate' });
    await flushPromises();
    expect(api.RelocateVideo).not.toHaveBeenCalled();

    api.SelectVideoFile.mockResolvedValueOnce('/Volumes/新盘/lost.mp4');
    api.RelocateVideo.mockResolvedValueOnce(undefined);
    api.GetVideosByIDs.mockResolvedValueOnce([{ id: 3, name: 'lost.mp4', path: '/Volumes/新盘/lost.mp4', is_stale: false, stale_reason: '', tags: [] }]);
    wrapper.vm.rowMenu = { video: wrapper.vm.videos[0], anchor: null, position: null };
    wrapper.vm.onRowMenuSelect({ id: 'relocate' });
    await flushPromises();

    expect(api.RelocateVideo).toHaveBeenCalledWith(3, '/Volumes/新盘/lost.mp4');
    expect(api.GetVideosByIDs).toHaveBeenCalledWith([3]);
    expect(wrapper.vm.videos[0]).toEqual(expect.objectContaining({ id: 3, path: '/Volumes/新盘/lost.mp4', is_stale: false }));
    expect(feedback.notify).toHaveBeenCalledWith('已重新定位「lost.mp4」，记录已恢复。');
    wrapper.unmount();
  });

  it('LIB-10 在「路径失效」视图里重新定位成功后，这一行离开列表', async () => {
    const wrapper = await mountPage();
    wrapper.vm.smartView = 'stale';
    await flushPromises();
    wrapper.vm.videos = [{ ...staleVideo }];
    wrapper.vm.selectedVideoIds = [3];
    api.SelectVideoFile.mockResolvedValueOnce('/new/lost.mp4');
    api.RelocateVideo.mockResolvedValueOnce(undefined);
    api.GetVideosByIDs.mockResolvedValueOnce([{ id: 3, name: 'lost.mp4', path: '/new/lost.mp4', is_stale: false, tags: [] }]);
    await wrapper.vm.relocateVideoFile(wrapper.vm.videos[0]);
    expect(wrapper.vm.videos).toEqual([]);
    expect(wrapper.vm.selectedVideoIds).toEqual([]);
    wrapper.unmount();
  });

  it('LIB-10 重新定位失败给不含路径的中文提示', async () => {
    const wrapper = await mountPage();
    wrapper.vm.videos = [{ ...staleVideo }];
    api.SelectVideoFile.mockResolvedValue('/Volumes/盘/dup.mp4');
    api.RelocateVideo.mockRejectedValueOnce('目标路径已被其他记录占用: /Volumes/盘/dup.mp4');
    await wrapper.vm.relocateVideoFile(wrapper.vm.videos[0]);
    expect(feedback.notifyError).toHaveBeenLastCalledWith('重新定位失败：所选文件已被片库中的另一条记录收录。');

    api.RelocateVideo.mockRejectedValueOnce(new Error('目标文件不存在: stat /Volumes/盘/dup.mp4: no such file or directory'));
    await wrapper.vm.relocateVideoFile(wrapper.vm.videos[0]);
    expect(feedback.notifyError).toHaveBeenLastCalledWith('重新定位失败：所选文件不存在或无法读取。');

    api.RelocateVideo.mockRejectedValueOnce(new Error('database is in maintenance mode'));
    await wrapper.vm.relocateVideoFile(wrapper.vm.videos[0]);
    expect(feedback.notifyError.mock.calls.at(-1)[0]).toContain('database is in maintenance mode');
    expect(wrapper.vm.videos[0].is_stale).toBe(true);
    wrapper.unmount();
  });

  it('MEDIA-05 行菜单「恢复上一版字幕」：没有备份时说明；有备份时确认后恢复最近一份并刷新', async () => {
    const wrapper = await mountPage();
    const video = { id: 3, name: 'movie.mp4', tags: [] };
    wrapper.vm.rowMenu = { video, anchor: null, position: null };
    expect(wrapper.vm.rowMenuItems.map(item => item.id)).toContain('subtitle-restore');

    api.ListSubtitleBackups.mockResolvedValueOnce([]);
    await wrapper.vm.restoreLatestSubtitleBackup(video);
    expect(feedback.notify.mock.calls.at(-1)[0]).toContain('还没有字幕备份');
    expect(api.RestoreSubtitleBackup).not.toHaveBeenCalled();

    const reload = vi.spyOn(wrapper.vm, 'reloadCurrentView').mockResolvedValue();
    api.ListSubtitleBackups.mockResolvedValueOnce([
      { id: 'b2', created_at: '2026-09-30T10:20:00+08:00', size: 20 },
      { id: 'b1', created_at: '2026-09-29T10:20:00+08:00', size: 10 }
    ]);
    api.RestoreSubtitleBackup.mockResolvedValueOnce({ backup_id: 'b3' });
    await wrapper.vm.restoreLatestSubtitleBackup(video);
    expect(feedback.confirmAction).toHaveBeenLastCalledWith(expect.objectContaining({ title: '恢复上一版字幕' }));
    expect(feedback.confirmAction.mock.calls.at(-1)[0].message).toContain('共有 2 份备份');
    expect(api.RestoreSubtitleBackup).toHaveBeenCalledWith(3, 'b2');
    expect(reload).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });

  it('MEDIA-12 后台翻译的进度标在行菜单上', async () => {
    const wrapper = await mountPage();
    const video = { id: 3, name: 'movie.mp4', tags: [] };
    const dialog = wrapper.findComponent({ name: 'SubtitleTranslateDialog' });
    dialog.vm.$emit('translating-change', 3);
    dialog.vm.$emit('translate-progress', { videoId: 3, percent: 45 });
    await flushPromises();
    wrapper.vm.rowMenu = { video, anchor: null, position: null };
    expect(wrapper.vm.rowMenuItems.find(item => item.id === 'subtitle-translate').label).toBe('翻译字幕（进行中 45%）');
    wrapper.unmount();
  });
});

describe('P-036 批量生成字幕与字幕索引同步', () => {
  it('MEDIA-14 批量栏的「批量生成字幕」把选中的视频交给字幕弹窗，未加载的按 ID 补上', async () => {
    const generateBatch = vi.fn();
    const wrapper = await mountPage({}, {
      global: { stubs: { SubtitleGenerateDialog: { template: '<div />', methods: { generate() {}, generateBatch } } } }
    });
    wrapper.vm.videos = [{ id: 1, name: 'a.mp4', tags: [] }];
    wrapper.vm.selectedVideoIds = [1, 9];
    wrapper.findComponent({ name: 'LibraryToolbar' }).vm.$emit('batch-subtitle');
    await flushPromises();
    expect(generateBatch).toHaveBeenCalledWith([
      expect.objectContaining({ id: 1, name: 'a.mp4' }),
      { id: 9, name: '视频 #9' }
    ]);
    wrapper.unmount();
  });

  it('MEDIA-14 「无字幕」视图显示上次同步时间与「立即同步」；同步完成后刷新列表', async () => {
    api.GetSubtitleIndexSyncStatus.mockResolvedValue({ running: false, last_synced_at: '2026-09-30T08:05:00+08:00', checked: 120 });
    const wrapper = await mountPage();
    expect(wrapper.find('[data-test="subtitle-index-sync-bar"]').exists()).toBe(false);

    wrapper.vm.smartView = 'no_subtitle';
    await flushPromises();
    expect(api.GetSubtitleIndexSyncStatus).toHaveBeenCalled();
    expect(wrapper.find('[data-test="subtitle-index-sync-text"]').text()).toContain('字幕索引上次同步：2026-09-30 08:05');

    api.SyncSubtitleIndexNow.mockResolvedValueOnce({ running: true, checked: 120 });
    await wrapper.find('[data-test="subtitle-index-sync-now"]').trigger('click');
    await flushPromises();
    expect(api.SyncSubtitleIndexNow).toHaveBeenCalledTimes(1);
    expect(wrapper.find('[data-test="subtitle-index-sync-text"]').text()).toContain('正在后台同步字幕索引');
    expect(wrapper.find('[data-test="subtitle-index-sync-now"]').attributes('disabled')).toBeDefined();

    const reload = vi.spyOn(wrapper.vm, 'reloadCurrentView').mockResolvedValue();
    await wrapper.vm.handleSubtitleIndexSynced({ running: false, last_synced_at: '2026-09-30T08:20:00+08:00', checked: 121 });
    await flushPromises();
    expect(reload).toHaveBeenCalledTimes(1);
    expect(wrapper.find('[data-test="subtitle-index-sync-text"]').text()).toContain('08:20');
    wrapper.unmount();
  });

  it('MEDIA-14 不在依赖字幕索引的视图时，同步完成不刷新列表', async () => {
    const wrapper = await mountPage();
    const reload = vi.spyOn(wrapper.vm, 'reloadCurrentView').mockResolvedValue();
    await wrapper.vm.handleSubtitleIndexSynced({ running: false, checked: 1 });
    expect(reload).not.toHaveBeenCalled();
    wrapper.unmount();
  });
});

describe('P-032 评审 I-3 的父组件用例', () => {
  it('D-PC02 清理候选不在当前已加载列表里：不支持废纸篓 → 永久删除的二次确认列出面板传来的文件名，而不是「视频（编号 N）」', async () => {
    api.DeleteVideosWithResult.mockResolvedValue({ batch_id: 'b1', items: [{ id: 7, code: 'trash_unsupported' }] });
    api.PermanentlyDeleteVideos.mockResolvedValue({ items: [{ id: 7, code: 'ok' }] });
    const wrapper = await mountPage({}, {
      attachTo: document.body,
      global: { stubs: { TrashUndoBanner: false, TrashUnsupportedDialog: false, BaseModal: false } }
    });
    wrapper.vm.videos = [{ id: 1, name: 'keep.mp4', tags: [] }];

    const pending = wrapper.vm.trashCleanupVideos([7], { names: { 7: 'far-away.mp4' } });
    await flushPromises();
    expect(api.DeleteVideosWithResult).toHaveBeenCalledWith([7], true, expect.any(String));
    expect(wrapper.find('[data-test="trash-unsupported-names"]').text()).toContain('far-away.mp4');

    await wrapper.find('[data-test="trash-unsupported-permanent"]').trigger('click');
    const confirmNames = wrapper.find('[data-test="trash-unsupported-confirm-names"]').text();
    expect(confirmNames).toContain('far-away.mp4');
    expect(confirmNames).not.toContain('编号');

    await wrapper.find('[data-test="trash-unsupported-permanent-confirm"]').trigger('click');
    const outcome = await pending;
    expect(api.PermanentlyDeleteVideos).toHaveBeenCalledWith([7]);
    expect(outcome.succeededIDs).toEqual([7]);
    expect([...outcome.failedIDs]).toEqual([]);
    wrapper.unmount();
  });
});

describe('P-036 抽屉事件接线（P-037 的动作条与人物详情）', () => {
  async function mountWithDrawer() {
    const wrapper = await mountPage();
    wrapper.vm.videos = [{ id: 4, name: 'd.mp4', tags: [] }, { id: 5, name: 'e.mp4', tags: [{ id: 1, name: '保留' }] }];
    wrapper.vm.selectedPreviewVideoId = 5;
    wrapper.vm.previewOpen = true;
    await flushPromises();
    return wrapper;
  }

  it('PLAY-12 抽屉动作条「播放」失败并已标失效：列表那一行就地标成失效，不整页重载', async () => {
    const wrapper = await mountWithDrawer();
    api.SearchLibraryVideoPage.mockClear();
    const drawer = wrapper.findComponent({ name: 'PreviewDrawer' });
    expect(drawer.exists()).toBe(true);

    drawer.vm.$emit('playback-attempted', {
      video: { id: 5, name: 'e.mp4', path: '/Volumes/Media/e.mp4' },
      dispatch_succeeded: false,
      reason: 'offline_root',
      user_message: '播放失败: e.mp4 (/Volumes/Media/e.mp4)',
      reconcile_result: {
        video_id: 5, reason: 'offline_root', did_mark_stale: true, needs_reload: false,
        updated_video: { id: 5, name: 'e.mp4', is_stale: true, stale_reason: 'offline_root', tags: null }
      }
    });
    await flushPromises();

    expect(wrapper.vm.videos[1]).toEqual(expect.objectContaining({ id: 5, is_stale: true, stale_reason: 'offline_root', tags: [{ id: 1, name: '保留' }] }));
    expect(wrapper.get('[data-test="play-failure-notice"]').text()).toContain('「e.mp4」所在的磁盘未连接');
    expect(api.SearchLibraryVideoPage).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it('PLAY-12 抽屉里播放成功不打扰列表', async () => {
    const wrapper = await mountWithDrawer();
    api.SearchLibraryVideoPage.mockClear();
    wrapper.findComponent({ name: 'PreviewDrawer' }).vm.$emit('playback-attempted', {
      video: { id: 5, name: 'e.mp4' }, dispatch_succeeded: true
    });
    await flushPromises();
    expect(wrapper.vm.videos[1].is_stale).toBeFalsy();
    expect(feedback.notifyError).not.toHaveBeenCalled();
    expect(api.SearchLibraryVideoPage).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it('LIB-12 抽屉里人物详情撤销删除后（media-restored），片库页按当前条件刷新列表', async () => {
    const wrapper = await mountWithDrawer();
    const reload = vi.spyOn(wrapper.vm, 'reloadCurrentView').mockResolvedValue();
    wrapper.findComponent({ name: 'PreviewDrawer' }).vm.$emit('media-restored');
    await flushPromises();
    expect(reload).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });
});

