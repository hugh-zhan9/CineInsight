import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
  GetPendingWorkSummary: vi.fn(),
  ListCollectionSuggestions: vi.fn(),
  CountLibraryVideos: vi.fn(),
  SearchLibraryVideoPage: vi.fn(),
  PreviewLocalMetadataBatch: vi.fn(),
  ApplyLocalMetadataBatch: vi.fn(),
  LogFrontend: vi.fn()
}));
vi.mock('../../../wailsjs/go/main/App', () => api);

import PendingWorkPage, {
  buildLocalRows, LOCAL_BATCH_LIMIT, LOCAL_METADATA_FILTER, PENDING_SECTIONS, planLocalMetadataApply, summarizeLocalMetadataPlans
} from './PendingWorkPage.vue';
import { feedbackState, resetFeedback, resolveConfirm } from '../../utils/feedback.js';

const SUMMARY = {
  ai_video_candidates: 12,
  ai_video_failed_runs: 3,
  ai_image_candidates: 4,
  same_source_unconfirmed: 2,
  face_unnamed: 5,
  face_append_pending: 1,
  collection_suggestions: 2,
  cleanup_candidates: 7,
  local_metadata_updates: 2,
  total: 35
};

const SUGGESTIONS = [
  { id: 11, series_name: '漫长的季节', member_count: 12, scan_root: '/Volumes/A' },
  { id: 12, series_name: '繁花', member_count: 3, kind: 'append', target_collection_id: 4, target_collection_name: '繁花', scan_root: '/Volumes/A' }
];

const scalar = (change, extra = {}) => ({
  change_type: change,
  default_selected: change === 'fill',
  requires_overwrite: change === 'overwrite',
  ...extra
});

// 视频 1：只补空字段（片名、年份、海报），人物两个来源名都有默认映射。视频 2：片名要覆盖现有值。
function diffFor(id, overrides = {}) {
  return {
    video_id: id,
    video_name: `${id}.mkv`,
    manifest_sha256: `m${id}`,
    current_sha256: `c${id}`,
    status: 'update_available',
    title: scalar('none'),
    original_title: scalar('same'),
    description: scalar('none'),
    year: scalar('none'),
    people: { ...scalar('none'), source: [] },
    collection: { ...scalar('none'), source: [] },
    poster: scalar('none'),
    fanart: scalar('none'),
    ...overrides
  };
}

const READY_DIFF = diffFor(1, {
  title: scalar('fill'),
  year: scalar('fill'),
  poster: scalar('fill'),
  people: {
    ...scalar('fill'),
    source: [
      { source_name: '周迅', normalized_name: '周迅', default_mode: 'existing', default_entity_id: 9 },
      { source_name: 'New Face', normalized_name: 'new face', default_mode: 'create_new', default_entity_id: 0 }
    ]
  }
});
const REVIEW_DIFF = diffFor(2, { title: scalar('overwrite') });

const VIDEOS = [
  { id: 1, name: '1.mkv', display_title: '春光乍泄', year: 1997 },
  { id: 2, name: '2.mkv', display_title: '', year: 0 }
];

const FaceStub = { name: 'FaceClusterReviewPanel', emits: ['changed', 'loaded'], methods: { refresh: vi.fn() }, template: '<div data-test="face-panel-stub" />' };
const SuggestionStub = { name: 'CollectionSuggestionPanel', emits: ['close', 'confirmed'], template: '<div data-test="suggestion-panel-stub" />' };
const LocalDialogStub = { name: 'LocalMetadataDialog', props: ['visible', 'videoIds'], emits: ['close', 'applied'], template: '<div />' };

const wrappers = [];

async function mountPage() {
  const wrapper = mount(PendingWorkPage, {
    global: { stubs: { FaceClusterReviewPanel: FaceStub, CollectionSuggestionPanel: SuggestionStub, LocalMetadataDialog: LocalDialogStub } }
  });
  wrappers.push(wrapper);
  await flushPromises();
  return wrapper;
}

const section = (wrapper, key) => wrapper.get(`[data-test="pending-row-${key}"]`);
const toasts = () => feedbackState.toasts.map(toast => `${toast.level}:${toast.message}`);

beforeEach(() => {
  vi.clearAllMocks();
  resetFeedback();
  api.GetPendingWorkSummary.mockResolvedValue({ ...SUMMARY });
  api.ListCollectionSuggestions.mockResolvedValue(SUGGESTIONS.map(item => ({ ...item })));
  api.CountLibraryVideos.mockResolvedValue(2);
  api.SearchLibraryVideoPage.mockResolvedValue({ videos: VIDEOS.map(video => ({ ...video })), next_cursor: null });
  api.PreviewLocalMetadataBatch.mockImplementation(async ids => ({
    requested: ids.length,
    diffs: ids.map(id => ({ 1: READY_DIFF, 2: REVIEW_DIFF })[id] || diffFor(id)),
    failures: [],
    people_decisions: []
  }));
  api.ApplyLocalMetadataBatch.mockResolvedValue({ requested: 1, succeeded: 1, failed: 0, results: [], failures: [] });
});

afterEach(() => {
  while (wrappers.length) wrappers.pop().unmount();
  resetFeedback();
});

describe('待处理页的区块（D-P3-01）', () => {
  it('页头写总数；每类一块，各有数量与更新时间，顺序是先页内处理、后进入工具', async () => {
    const wrapper = await mountPage();

    expect(wrapper.getComponent({ name: 'PageHeader' }).props()).toMatchObject({ title: '待处理', subtitle: '共 35 项' });
    expect(wrapper.findAll('[data-test^="pending-row-"]').map(node => node.attributes('data-test').replace('pending-row-', ''))).toEqual([
      'face', 'collection_suggestions', 'local_metadata_updates', 'ai_video_candidates', 'same_source_unconfirmed', 'ai_image_candidates', 'cleanup_candidates'
    ]);
    const counts = Object.fromEntries(PENDING_SECTIONS.map(item => [item.key, wrapper.get(`[data-test="pending-count-${item.key}"]`).text()]));
    expect(counts).toEqual({
      face: '6',
      collection_suggestions: '2',
      local_metadata_updates: '2',
      ai_video_candidates: '12',
      same_source_unconfirmed: '2',
      ai_image_candidates: '4',
      cleanup_candidates: '7'
    });
    for (const item of PENDING_SECTIONS) expect(wrapper.get(`[data-test="pending-time-${item.key}"]`).text()).toMatch(/^更新于 \d{2}:\d{2}$/);
    expect(wrapper.get('[data-test="pending-face-detail"]').text()).toBe('待命名 5 组 · 待确认追加 1 组');
    expect(wrapper.emitted('badge-change')).toEqual([[35]]);
    // 本地资料与建议作品集按各自的接口读：同一个智能视图口径。
    expect(api.CountLibraryVideos).toHaveBeenCalledWith(LOCAL_METADATA_FILTER);
    expect(api.SearchLibraryVideoPage).toHaveBeenCalledWith({ filter: LOCAL_METADATA_FILTER, limit: 20 });
    expect(api.PreviewLocalMetadataBatch).toHaveBeenCalledWith([1, 2]);
  });

  it('首次读取时各块是骨架屏', async () => {
    api.GetPendingWorkSummary.mockReturnValue(new Promise(() => {}));
    api.ListCollectionSuggestions.mockReturnValue(new Promise(() => {}));
    api.CountLibraryVideos.mockReturnValue(new Promise(() => {}));
    const wrapper = await mountPage();

    expect(wrapper.getComponent({ name: 'PageHeader' }).props('subtitle')).toBe('正在统计…');
    for (const item of PENDING_SECTIONS) {
      expect(section(wrapper, item.key).attributes('aria-busy')).toBe('true');
      expect(section(wrapper, item.key).findComponent({ name: 'SkeletonBlock' }).exists()).toBe(true);
    }
    expect(wrapper.find('[data-test="pending-work-empty"]').exists()).toBe(false);
  });

  it('数量为 0 的区块收成一行「已处理完」，不再有内容与入口', async () => {
    api.GetPendingWorkSummary.mockResolvedValue({ ...SUMMARY, face_unnamed: 0, face_append_pending: 0, cleanup_candidates: 0, total: 28 });
    api.ListCollectionSuggestions.mockResolvedValue([]);
    const wrapper = await mountPage();

    for (const key of ['face', 'collection_suggestions', 'cleanup_candidates']) {
      expect(section(wrapper, key).classes()).toContain('pending-section--done');
      expect(wrapper.get(`[data-test="pending-done-${key}"]`).text()).toBe('已处理完');
      expect(wrapper.find(`[data-test="pending-count-${key}"]`).exists()).toBe(false);
    }
    expect(wrapper.find('[data-test="face-panel-stub"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="pending-suggestions-open"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="pending-go-cleanup_candidates"]').exists()).toBe(false);
    expect(section(wrapper, 'ai_video_candidates').classes()).not.toContain('pending-section--done');
  });

  it('全部处理完：整页空状态给下一步，失败的 AI 打标仍引到任务中心', async () => {
    api.GetPendingWorkSummary.mockResolvedValue(Object.fromEntries(Object.keys(SUMMARY).map(key => [key, key === 'ai_video_failed_runs' ? 2 : 0])));
    api.ListCollectionSuggestions.mockResolvedValue([]);
    api.CountLibraryVideos.mockResolvedValue(0);
    api.SearchLibraryVideoPage.mockResolvedValue({ videos: [], next_cursor: null });
    const wrapper = await mountPage();

    expect(wrapper.get('[data-test="pending-work-empty"]').text()).toContain('暂无待处理事项');
    expect(wrapper.findAll('[data-test^="pending-row-"]')).toHaveLength(0);
    await wrapper.get('[data-test="pending-work-go-home"]').trigger('click');
    expect(wrapper.emitted('navigate')).toEqual([['manage-home']]);
    expect(wrapper.get('[data-test="pending-ai-failed"]').text()).toContain('2 个视频 AI 打标失败（不计入待处理）');
    await wrapper.get('[data-test="pending-open-task-center"]').trigger('click');
    expect(wrapper.emitted('open-task-center')).toHaveLength(1);
  });

  // 摘要读失败只出一个页级横幅（后端的中文说明原样显示）+ 重试；靠摘要计数的五块各写一行「数量没读到」，
  // 不再五块各出一个一样的横幅。建议作品集与本地资料有自己的来源，照常。
  it('摘要读失败：页头下一个横幅 + 重试，靠摘要计数的各块只写「数量没读到」，其余两块照常；重试只重读摘要', async () => {
    api.GetPendingWorkSummary.mockRejectedValue('数据库后端已切换，请重启应用后再查看');
    const wrapper = await mountPage();

    const banners = wrapper.findAll('.ui-banner');
    expect(banners).toHaveLength(1);
    const banner = wrapper.get('[data-test="pending-summary-error"]');
    expect(banner.text()).toContain('待处理数量没读到');
    expect(banner.text()).toContain('数据库后端已切换，请重启应用后再查看');
    for (const key of ['face', 'ai_video_candidates', 'same_source_unconfirmed', 'ai_image_candidates', 'cleanup_candidates']) {
      expect(wrapper.find(`[data-test="pending-error-${key}"]`).exists(), key).toBe(false);
      expect(wrapper.get(`[data-test="pending-count-unread-${key}"]`).text()).toBe('数量没读到');
      expect(wrapper.find(`[data-test="pending-count-${key}"]`).exists(), key).toBe(false);
    }
    expect(wrapper.find('[data-test="pending-count-unread-collection_suggestions"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="pending-count-unread-local_metadata_updates"]').exists()).toBe(false);
    // 计数读不到时工具入口与人脸面板照样能用（它们各有各的数据）。
    expect(wrapper.find('[data-test="pending-go-cleanup_candidates"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="face-panel-stub"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="pending-error-collection_suggestions"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="pending-count-local_metadata_updates"]').text()).toBe('2');
    expect(wrapper.getComponent({ name: 'PageHeader' }).props('subtitle')).toBe('待处理总数没读到');
    expect(wrapper.emitted('badge-change').at(-1)).toEqual([0]);

    api.GetPendingWorkSummary.mockResolvedValue({ ...SUMMARY });
    api.CountLibraryVideos.mockClear();
    api.ListCollectionSuggestions.mockClear();
    await wrapper.get('[data-test="pending-summary-retry"]').trigger('click');
    await flushPromises();

    expect(wrapper.find('[data-test="pending-summary-error"]').exists()).toBe(false);
    expect(wrapper.find('[data-test^="pending-count-unread-"]').exists()).toBe(false);
    expect(wrapper.find('[data-test^="pending-error-"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="pending-count-cleanup_candidates"]').text()).toBe('7');
    expect(api.CountLibraryVideos).not.toHaveBeenCalled();
    expect(api.ListCollectionSuggestions).not.toHaveBeenCalled();
    expect(wrapper.emitted('badge-change').at(-1)).toEqual([35]);
  });

  it('本地资料读失败只影响这一块：横幅 + 重试只重读这一块', async () => {
    api.CountLibraryVideos.mockRejectedValueOnce('读取失败：磁盘未连接');
    const wrapper = await mountPage();

    expect(wrapper.get('[data-test="pending-error-local_metadata_updates"]').text()).toContain('本地资料有更新没读到');
    expect(wrapper.find('[data-test="pending-local-row-1"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="pending-count-cleanup_candidates"]').text()).toBe('7');
    expect(wrapper.get('[data-test="pending-count-collection_suggestions"]').text()).toBe('2');

    api.GetPendingWorkSummary.mockClear();
    await wrapper.get('[data-test="pending-retry-local_metadata_updates"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-test="pending-error-local_metadata_updates"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="pending-local-row-1"]').exists()).toBe(true);
    expect(api.GetPendingWorkSummary).not.toHaveBeenCalled();
  });

  it('建议作品集读失败只影响这一块', async () => {
    api.ListCollectionSuggestions.mockRejectedValueOnce('boom');
    const wrapper = await mountPage();

    expect(wrapper.get('[data-test="pending-error-collection_suggestions"]').text()).toContain('建议作品集没读到');
    expect(wrapper.get('[data-test="pending-suggestions-open"]').attributes('disabled')).toBeDefined();
    await wrapper.get('[data-test="pending-retry-collection_suggestions"]').trigger('click');
    await flushPromises();
    expect(wrapper.findAll('[data-test="pending-suggestion-item"]')).toHaveLength(2);
  });

  it('晚到的旧摘要不覆盖新摘要', async () => {
    const wrapper = await mountPage();
    let resolveOld;
    api.GetPendingWorkSummary.mockReturnValueOnce(new Promise(resolve => { resolveOld = resolve; }));
    wrapper.vm.refreshSummary();
    api.GetPendingWorkSummary.mockResolvedValueOnce({ ...SUMMARY, cleanup_candidates: 1, total: 29 });
    await wrapper.vm.refreshSummary();
    resolveOld({ ...SUMMARY, cleanup_candidates: 99, total: 127 });
    await flushPromises();

    expect(wrapper.get('[data-test="pending-count-cleanup_candidates"]').text()).toBe('1');
    expect(wrapper.emitted('badge-change').at(-1)).toEqual([29]);
  });
});

describe('进入专门工具（D-P3-01）', () => {
  // 清理候选交 library.openCleanup：清理中心的视频标签（候选数是视频清理的口径），不停在上次看的标签上。
  it('AI 视频 / 同源 / AI 图片 / 清理候选交出固定命令 ID；清理候选标出 ⌘⇧K', async () => {
    const wrapper = await mountPage();
    const expected = {
      ai_video_candidates: 'library.openAIReview',
      same_source_unconfirmed: 'library.openAIReview',
      ai_image_candidates: 'photos.openAIReview',
      cleanup_candidates: 'library.openCleanup'
    };
    const emitted = [];
    for (const key of Object.keys(expected)) {
      await wrapper.get(`[data-test="pending-go-${key}"]`).trigger('click');
      emitted.push([key, wrapper.emitted('run-command').at(-1)[0]]);
    }
    expect(Object.fromEntries(emitted)).toEqual(expected);
    expect(wrapper.emitted('run-command').at(-1)).toEqual(['library.openCleanup', '清理候选']);
    expect(section(wrapper, 'cleanup_candidates').text()).toContain('⌘⇧K');
    expect(PENDING_SECTIONS.filter(item => item.command).map(item => item.key).sort()).toEqual(Object.keys(expected).sort());
  });

  it('失败的 AI 打标作为「AI 视频标签」的页脚引到任务中心；这一块处理完也照样提', async () => {
    api.GetPendingWorkSummary.mockResolvedValue({ ...SUMMARY, ai_video_candidates: 0, total: 23 });
    const wrapper = await mountPage();

    const footer = section(wrapper, 'ai_video_candidates').get('[data-test="pending-ai-failed"]');
    expect(footer.text()).toContain('3 个视频 AI 打标失败（不计入待处理）');
    await footer.get('[data-test="pending-open-task-center"]').trigger('click');
    expect(wrapper.emitted('open-task-center')).toHaveLength(1);
  });
});

describe('页内处理：人脸与建议作品集', () => {
  it('人脸嵌现有审阅面板；面板改动后只重读摘要（这一块的数量与角标）', async () => {
    const wrapper = await mountPage();
    expect(section(wrapper, 'face').find('[data-test="face-panel-stub"]').exists()).toBe(true);
    vi.clearAllMocks();
    api.GetPendingWorkSummary.mockResolvedValue({ ...SUMMARY, face_unnamed: 0, face_append_pending: 0, total: 29 });

    wrapper.getComponent({ name: 'FaceClusterReviewPanel' }).vm.$emit('changed');
    await flushPromises();

    expect(api.GetPendingWorkSummary).toHaveBeenCalledTimes(1);
    expect(api.CountLibraryVideos).not.toHaveBeenCalled();
    expect(api.ListCollectionSuggestions).not.toHaveBeenCalled();
    expect(wrapper.get('[data-test="pending-done-face"]').text()).toBe('已处理完');
    expect(wrapper.emitted('badge-change').at(-1)).toEqual([29]);
  });

  it('建议作品集列出前几组；「逐条处理…」打开现有面板，关掉后只重读这一块与摘要', async () => {
    const wrapper = await mountPage();
    expect(wrapper.findAll('[data-test="pending-suggestion-item"]').map(item => item.text())).toEqual([
      '漫长的季节12 集',
      '繁花新增 3 集 → 「繁花」'
    ]);
    expect(wrapper.find('[data-test="suggestion-panel-stub"]').exists()).toBe(false);

    await wrapper.get('[data-test="pending-suggestions-open"]').trigger('click');
    expect(wrapper.find('[data-test="suggestion-panel-stub"]').exists()).toBe(true);
    vi.clearAllMocks();
    api.ListCollectionSuggestions.mockResolvedValue([]);
    api.GetPendingWorkSummary.mockResolvedValue({ ...SUMMARY, collection_suggestions: 0, total: 33 });

    wrapper.getComponent({ name: 'CollectionSuggestionPanel' }).vm.$emit('close');
    await flushPromises();

    expect(wrapper.find('[data-test="suggestion-panel-stub"]').exists()).toBe(false);
    expect(api.ListCollectionSuggestions).toHaveBeenCalledTimes(1);
    expect(api.GetPendingWorkSummary).toHaveBeenCalledTimes(1);
    expect(api.CountLibraryVideos).not.toHaveBeenCalled();
    expect(wrapper.get('[data-test="pending-done-collection_suggestions"]').text()).toBe('已处理完');
  });

  it('页头「刷新」三处都重读，人脸面板也自己重取', async () => {
    const wrapper = await mountPage();
    vi.clearAllMocks();
    await wrapper.get('[data-test="pending-work-refresh"]').trigger('click');
    await flushPromises();
    expect(api.GetPendingWorkSummary).toHaveBeenCalledTimes(1);
    expect(api.ListCollectionSuggestions).toHaveBeenCalledTimes(1);
    expect(api.CountLibraryVideos).toHaveBeenCalledTimes(1);
    expect(FaceStub.methods.refresh).toHaveBeenCalledTimes(1);
  });
});

describe('页内处理：本地资料有更新', () => {
  it('每行写清会改什么：只补空字段的可直接「应用」，要覆盖的只能逐项确认', async () => {
    const wrapper = await mountPage();

    const ready = wrapper.get('[data-test="pending-local-row-1"]');
    expect(ready.text()).toContain('春光乍泄');
    expect(ready.text()).toContain('1997');
    expect(ready.text()).toContain('补全：片名、年份、人物、海报');
    expect(ready.find('[data-test="pending-local-review-1"]').exists()).toBe(false);

    const review = wrapper.get('[data-test="pending-local-row-2"]');
    expect(review.text()).toContain('2.mkv');
    expect(review.text()).toContain('要覆盖现有的片名，需逐项确认');
    expect(review.find('[data-test="pending-local-apply-2"]').exists()).toBe(false);
  });

  it('「应用」一行：按默认勾选与默认映射发请求、不覆盖；成功提示后只重读这一块与摘要', async () => {
    const wrapper = await mountPage();
    vi.clearAllMocks();
    api.CountLibraryVideos.mockResolvedValue(1);
    api.SearchLibraryVideoPage.mockResolvedValue({ videos: [VIDEOS[1]], next_cursor: null });
    api.ApplyLocalMetadataBatch.mockResolvedValue({ requested: 1, succeeded: 1, failed: 0, results: [], failures: [] });

    await wrapper.get('[data-test="pending-local-apply-1"]').trigger('click');
    await flushPromises();

    expect(api.ApplyLocalMetadataBatch).toHaveBeenCalledWith({
      requests: [{
        video_id: 1,
        manifest_sha256: 'm1',
        current_sha256: 'c1',
        selected_fields: ['title', 'year', 'people', 'poster'],
        overwrite_fields: [],
        people_resolutions: [
          { normalized_name: '周迅', mode: 'existing', entity_id: 9 },
          { normalized_name: 'new face', mode: 'create_new', entity_id: 0 }
        ],
        collection_resolutions: []
      }]
    });
    expect(toasts()).toContain('success:已应用《春光乍泄》的本地资料');
    // 本地资料写进了片库：通知 App 让片库管理重取（独立评审 P3 #2）。
    expect(wrapper.emitted('videos-changed')).toEqual([[{}]]);
    expect(api.CountLibraryVideos).toHaveBeenCalledTimes(1);
    expect(api.GetPendingWorkSummary).toHaveBeenCalledTimes(1);
    expect(api.ListCollectionSuggestions).not.toHaveBeenCalled();
    expect(wrapper.find('[data-test="pending-local-row-1"]').exists()).toBe(false);
  });

  it('「应用」没成功：错误码给中文说明', async () => {
    const wrapper = await mountPage();
    api.ApplyLocalMetadataBatch.mockResolvedValue({ requested: 1, succeeded: 0, failed: 1, results: [], failures: [{ video_id: 1, error_code: 'metadata_conflict', message: 'metadata_conflict' }] });

    await wrapper.get('[data-test="pending-local-apply-1"]').trigger('click');
    await flushPromises();
    expect(toasts()).toContain('error:《春光乍泄》没应用上：本地资料或片库资料在这期间变了，刷新后再看一眼');
    expect(wrapper.emitted('videos-changed')).toBeUndefined();
  });

  it('「逐项确认…」打开导入本地资料对话框（只这一部），关掉后重读这一块', async () => {
    const wrapper = await mountPage();
    const dialog = wrapper.getComponent({ name: 'LocalMetadataDialog' });
    expect(dialog.props('visible')).toBe(false);

    await wrapper.get('[data-test="pending-local-review-2"]').trigger('click');
    expect(dialog.props()).toMatchObject({ visible: true, videoIds: [2] });
    vi.clearAllMocks();
    dialog.vm.$emit('close');
    await flushPromises();

    expect(dialog.props('visible')).toBe(false);
    expect(api.CountLibraryVideos).toHaveBeenCalledTimes(1);
    expect(api.GetPendingWorkSummary).toHaveBeenCalledTimes(1);
    // 只关掉、没应用：片库不用重取。对话框里应用了（applied）：通知 App 让片库管理重取。
    expect(wrapper.emitted('videos-changed')).toBeUndefined();
    await wrapper.get('[data-test="pending-local-review-2"]').trigger('click');
    dialog.vm.$emit('applied', { succeeded: 1 });
    await flushPromises();
    expect(wrapper.emitted('videos-changed')).toEqual([[{}]]);
  });

  it('「全部应用」先取全部做预览、确认框写清会补什么与跳过什么，确认后只应用能直接应用的', async () => {
    const wrapper = await mountPage();
    vi.clearAllMocks();
    api.SearchLibraryVideoPage
      .mockResolvedValueOnce({ videos: [{ id: 1 }, { id: 2 }], next_cursor: { id: 2 } })
      .mockResolvedValueOnce({ videos: [{ id: 3 }, { id: 4 }, { id: 5 }], next_cursor: null })
      .mockResolvedValue({ videos: [], next_cursor: null });
    api.PreviewLocalMetadataBatch.mockResolvedValueOnce({
      requested: 5,
      diffs: [READY_DIFF, REVIEW_DIFF, diffFor(3), diffFor(4, { status: 'missing' })],
      failures: [{ video_id: 5, error_code: 'metadata_failed', message: 'open /Volumes/Movies/x/movie.nfo: permission denied' }]
    });
    api.ApplyLocalMetadataBatch.mockResolvedValue({ requested: 2, succeeded: 2, failed: 0, results: [], failures: [] });

    await wrapper.get('[data-test="pending-local-apply-all"]').trigger('click');
    await flushPromises();

    expect(api.SearchLibraryVideoPage.mock.calls.map(call => call[0])).toEqual([
      { filter: LOCAL_METADATA_FILTER, limit: 200 },
      { filter: LOCAL_METADATA_FILTER, limit: 200, cursor: { id: 2 } }
    ]);
    expect(api.PreviewLocalMetadataBatch).toHaveBeenCalledWith([1, 2, 3, 4, 5]);
    expect(feedbackState.confirm).toMatchObject({ title: '全部应用本地资料', confirmText: '应用 2 个', level: 'confirm' });
    expect(feedbackState.confirm.message.split('\n')).toEqual([
      '补全 1 个视频：片名 1、年份 1、人物 1、海报 1。',
      '1 个的本地资料与片库一致，只标为已处理。',
      '1 个有要覆盖现有资料或要选人物的项，这次跳过，在列表里逐项确认。',
      '2 个读不出本地资料，这次跳过。'
    ]);
    expect(feedbackState.confirm.impact).toBe('只填空着的字段，不覆盖已有资料；应用之后不能撤销。');
    expect(wrapper.get('[data-test="pending-local-apply-all"]').attributes('disabled')).toBeDefined();
    expect(api.ApplyLocalMetadataBatch).not.toHaveBeenCalled();

    resolveConfirm(true);
    await flushPromises();

    const requests = api.ApplyLocalMetadataBatch.mock.calls[0][0].requests;
    expect(requests.map(request => [request.video_id, request.selected_fields])).toEqual([
      [1, ['title', 'year', 'people', 'poster']],
      [3, []]
    ]);
    expect(requests.every(request => request.overwrite_fields.length === 0)).toBe(true);
    expect(toasts()).toContain('success:已应用 2 个视频的本地资料');
    expect(wrapper.emitted('videos-changed')).toEqual([[{}]]);
    expect(api.GetPendingWorkSummary).toHaveBeenCalledTimes(1);
    expect(wrapper.get('[data-test="pending-local-apply-all"]').attributes('disabled')).toBeUndefined();
  });

  it('「全部应用」取消就什么也不改', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-test="pending-local-apply-all"]').trigger('click');
    await flushPromises();
    resolveConfirm(false);
    await flushPromises();

    expect(api.ApplyLocalMetadataBatch).not.toHaveBeenCalled();
    expect(wrapper.get('[data-test="pending-local-apply-all"]').text()).toBe('全部应用');
    expect(wrapper.emitted('videos-changed')).toBeUndefined();
  });

  it('「全部应用」没有能直接应用的：只提示，不弹确认', async () => {
    api.PreviewLocalMetadataBatch.mockImplementation(async ids => ({ requested: ids.length, diffs: ids.map(id => diffFor(id, { title: scalar('overwrite') })), failures: [] }));
    const wrapper = await mountPage();

    await wrapper.get('[data-test="pending-local-apply-all"]').trigger('click');
    await flushPromises();

    expect(feedbackState.confirm).toBeNull();
    expect(api.ApplyLocalMetadataBatch).not.toHaveBeenCalled();
    expect(toasts()).toContain('info:没有能直接应用的。2 个有要覆盖现有资料或要选人物的项，这次跳过，在列表里逐项确认。');
  });

  it('「全部应用」部分失败：成功与失败各给一条提示', async () => {
    const wrapper = await mountPage();
    api.ApplyLocalMetadataBatch.mockResolvedValue({ requested: 1, succeeded: 0, failed: 1, results: [], failures: [{ video_id: 1, error_code: 'video_not_found', message: 'record not found' }] });

    await wrapper.get('[data-test="pending-local-apply-all"]').trigger('click');
    await flushPromises();
    resolveConfirm(true);
    await flushPromises();

    expect(toasts()).toEqual(['error:1 个没应用上：这部视频已不在片库中']);
  });

  it('「全部应用」一次最多一批：取到上限就停，确认框说明余下的下次再点', async () => {
    const wrapper = await mountPage();
    let next = 0;
    api.SearchLibraryVideoPage.mockImplementation(async ({ limit }) => {
      const videos = Array.from({ length: limit }, () => ({ id: ++next }));
      return { videos, next_cursor: { id: next } };
    });
    api.PreviewLocalMetadataBatch.mockImplementation(async ids => ({ requested: ids.length, diffs: ids.map(id => diffFor(id, { year: scalar('fill') })), failures: [] }));

    await wrapper.get('[data-test="pending-local-apply-all"]').trigger('click');
    await flushPromises();

    expect(api.PreviewLocalMetadataBatch.mock.calls.at(-1)[0]).toHaveLength(LOCAL_BATCH_LIMIT);
    expect(api.SearchLibraryVideoPage.mock.calls.slice(-3).map(call => call[0].limit)).toEqual([200, 200, 100]);
    expect(feedbackState.confirm.confirmText).toBe(`应用 ${LOCAL_BATCH_LIMIT} 个`);
    expect(feedbackState.confirm.message).toContain(`一次最多 ${LOCAL_BATCH_LIMIT} 个，余下的应用完再点一次「全部应用」。`);
    resolveConfirm(false);
    await flushPromises();
  });

  it('「全部应用」翻页时游标不往前走（一页没有新视频）就停下，不空转', async () => {
    const wrapper = await mountPage();
    vi.clearAllMocks();
    api.SearchLibraryVideoPage.mockResolvedValue({ videos: VIDEOS.map(video => ({ ...video })), next_cursor: { id: 2 } });

    await wrapper.get('[data-test="pending-local-apply-all"]').trigger('click');
    await flushPromises();

    expect(api.SearchLibraryVideoPage).toHaveBeenCalledTimes(2);
    expect(api.PreviewLocalMetadataBatch).toHaveBeenCalledWith([1, 2]);
    expect(feedbackState.confirm.confirmText).toBe('应用 1 个');
    expect(feedbackState.confirm.message).not.toContain('一次最多');
    resolveConfirm(false);
    await flushPromises();
  });

  it('「加载更多」接着往下取并预览新的一页', async () => {
    api.SearchLibraryVideoPage
      .mockResolvedValueOnce({ videos: [VIDEOS[0]], next_cursor: { id: 1 } })
      .mockResolvedValueOnce({ videos: [VIDEOS[0], VIDEOS[1]], next_cursor: null });
    const wrapper = await mountPage();
    expect(wrapper.findAll('[data-test^="pending-local-row-"]')).toHaveLength(1);

    await wrapper.get('[data-test="pending-local-more"]').trigger('click');
    await flushPromises();

    expect(api.SearchLibraryVideoPage).toHaveBeenLastCalledWith({ filter: LOCAL_METADATA_FILTER, cursor: { id: 1 }, limit: 20 });
    expect(api.PreviewLocalMetadataBatch).toHaveBeenLastCalledWith([2]);
    expect(wrapper.findAll('[data-test^="pending-local-row-"]')).toHaveLength(2);
    expect(wrapper.find('[data-test="pending-local-more"]').exists()).toBe(false);
  });
});

describe('本地资料「直接应用」的判定（与后端 ApplyDefaults 同一口径）', () => {
  it('只选默认勾选的字段；人物 / 作品集要每个来源名都有默认映射', () => {
    expect(planLocalMetadataApply(READY_DIFF)).toMatchObject({
      kind: 'ready', fill: ['片名', '年份', '人物', '海报'], overwrite: [], unresolved: []
    });
  });

  it('有要覆盖的字段或重名要选的映射：review，不能一键应用', () => {
    const ambiguous = diffFor(7, {
      year: scalar('fill'),
      collection: { ...scalar('fill'), source: [{ source_name: '三部曲', normalized_name: '三部曲', default_mode: '' }] }
    });
    expect(planLocalMetadataApply(ambiguous)).toMatchObject({ kind: 'review', fill: ['年份'], unresolved: ['作品集'] });
    expect(planLocalMetadataApply(diffFor(8, { fanart: scalar('overwrite') }))).toMatchObject({ kind: 'review', overwrite: ['背景图'] });
  });

  it('没有要改的字段仍可应用（只把这一版本地资料标为已处理）；没找到资料文件的不能应用', () => {
    expect(planLocalMetadataApply(diffFor(3))).toMatchObject({ kind: 'ready', fill: [], request: { selected_fields: [] } });
    expect(planLocalMetadataApply(diffFor(4, { status: 'missing' })).kind).toBe('missing');
    expect(planLocalMetadataApply(undefined).kind).toBe('missing');
  });

  it('预览失败的视频记成 failed，原因擦掉路径', () => {
    const rows = buildLocalRows([{ id: 5, name: '5.mkv' }], { diffs: [], failures: [{ video_id: 5, error_code: 'metadata_failed', message: 'open /Volumes/Movies/x/movie.nfo: permission denied' }] });
    expect(rows).toEqual([{ id: 5, title: '5.mkv', year: null, plan: expect.objectContaining({ kind: 'failed', reason: 'open movie.nfo: permission denied' }) }]);
  });

  it('汇总：空列表没有说明', () => {
    expect(summarizeLocalMetadataPlans([])).toEqual({ ready: [], review: 0, skipped: 0, lines: [] });
  });
});
