import { flushPromises, mount as mountComponent } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// 应用内确认框：默认答「取消」（删除后「是否立即重新分析」留在原地），需要别的答案的用例单独覆盖。
// notifyUndoable 不替换：撤销提示真的进 feedbackState，同一条也记进「最近操作」。
const feedback = vi.hoisted(() => ({
  confirmAction: vi.fn(() => Promise.resolve(false)),
  notify: vi.fn(),
  notifyError: vi.fn(),
  notifySuccess: vi.fn()
}));
vi.mock('../../utils/feedback.js', async (importOriginal) => ({
  ...(await importOriginal()),
  ...feedback
}));

const api = vi.hoisted(() => new Proxy({}, {
  get(target, prop) {
    if (typeof prop === 'symbol' || prop === 'then') return undefined;
    if (!target[prop]) target[prop] = vi.fn(() => Promise.resolve(null));
    return target[prop];
  }
}));
vi.mock('../../../wailsjs/go/main/App', () => api);

import CleanupCenterPage from './CleanupCenterPage.vue';
import { commandList, findCommand } from '../../utils/commandRegistry.js';
import { clearToasts, feedbackState, runToastAction } from '../../utils/feedback.js';
import { photoCleanupStore, resetPhotoCleanupReview, stopPhotoCleanupPolling } from '../../utils/photoCleanupStore.js';
import * as recentOperations from '../../utils/recentOperations.js';
import { formatLocalTime } from '../../utils/subtitleTools.js';

// 「不支持废纸篓」二选一出现时才 teleport 到 body：统一挂 teleport 桩，让它留在组件子树里查。
const liveWrappers = [];
function mount(props = {}, options = {}) {
  const wrapper = mountComponent(CleanupCenterPage, {
    attachTo: document.body,
    props: { pageActive: true, tab: 'video', request: null, ...props },
    ...options,
    global: { ...options.global, stubs: { teleport: true, ...options.global?.stubs } }
  });
  liveWrappers.push(wrapper);
  return wrapper;
}
afterEach(() => {
  for (const wrapper of liveWrappers.splice(0)) {
    if (!wrapper.vm.$.isUnmounted) wrapper.unmount();
  }
});

const FULL_MERGE = { skip_playback_state: false, skip_subtitle: false };

function video(id, extra = {}) {
  return { id, name: `v${id}.mp4`, directory: '/lib/a', path: `/lib/a/v${id}.mp4`, size: 100 * id, duration: 60, resolution: '1920x1080', ...extra };
}

function videoAnalysis(extra = {}) {
  return {
    duplicate_groups: [], near_duplicate_groups: [], same_source_groups: [], clip_groups: [],
    low_duration: [], low_resolution: [], ...extra
  };
}

const VIDEO_STARTED_AT = '2026-10-13T06:30:00Z';

function videoStatus(analysis, extra = {}) {
  return { running: false, completed: true, error: '', stale: false, started_at: VIDEO_STARTED_AT, progress: { stage: 'done' }, analysis, ...extra };
}

function okDelete(ids, batchID = 'batch-v') {
  return { batch_id: batchID, items: ids.map(id => ({ id, code: 'ok' })) };
}

function image(id, name, extra = {}) {
  return { id, name, path: `/photos/${name}`, directory: '/photos', width: 1000, height: 1000, file_size: 500000, tags: [], ...extra };
}

function imageStatus(analysis, extra = {}) {
  return {
    running: false, completed: true, error: '', stale: false, started_at: '2026-10-12T01:00:00Z', progress: { stage: 'done' },
    analysis: { duplicate_groups: [], near_duplicate_groups: [], stale_hash_count: 0, ...analysis },
    ...extra
  };
}

const exactImages = () => ({
  original: image(1, 'keep.jpg', { width: 4000, height: 3000 }),
  candidates: [image(2, 'copy.jpg', { width: 800, height: 600 })],
  reason: '文件大小和采样哈希一致'
});
const nearImages = () => ({
  original: image(3, 'near-big.jpg', { width: 4000, height: 3000 }),
  candidates: [image(4, 'near-small.jpg', { width: 1000, height: 750 })],
  reason: '感知哈希相近'
});

const panel = wrapper => wrapper.findComponent({ name: 'CleanupReviewPanel' });
const videoPanelEl = wrapper => wrapper.get('#cleanup-panel-video');
const lastEmitted = (wrapper, event) => wrapper.emitted(event)?.at(-1);
const undoToast = () => feedbackState.toasts.find(toast => toast.action?.label === '撤销');

async function mountVideo(status, props = {}) {
  api.GetCleanupStatus.mockResolvedValue(status);
  const wrapper = mount(props);
  await flushPromises();
  return wrapper;
}

async function confirmVideoDelete(wrapper) {
  await wrapper.vm.$nextTick();
  await videoPanelEl(wrapper).get('[data-test="cleanup-trash-selected"]').trigger('click');
  await flushPromises();
  await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
  await flushPromises();
}

async function mountImage(status, props = {}) {
  photoCleanupStore.status = null;
  api.GetImageCleanupStatus.mockResolvedValue(status);
  api.StartImageCleanupAnalysis.mockResolvedValue(status);
  const wrapper = mount({ tab: 'image', ...props });
  await flushPromises();
  return wrapper;
}

async function confirmImageDelete(wrapper) {
  await wrapper.get('[data-test="cleanup-delete-selected"]').trigger('click');
  await flushPromises();
  await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
  await flushPromises();
}

beforeEach(() => {
  vi.clearAllMocks();
  clearToasts();
  recentOperations.clear();
  delete window.runtime;
  document.body.innerHTML = '';
  stopPhotoCleanupPolling();
  photoCleanupStore.status = null;
  resetPhotoCleanupReview('');
  feedback.confirmAction.mockResolvedValue(false);
  api.GetCleanupStatus.mockResolvedValue(null);
  api.StartCleanupAnalysisFromSettings.mockResolvedValue({ running: true, completed: false, progress: { stage: 'load' } });
  api.GetImageCleanupStatus.mockResolvedValue({ running: false, completed: false, error: '', progress: {}, analysis: null });
  api.StartImageCleanupAnalysis.mockResolvedValue({ running: true, completed: false, progress: {} });
  api.MergeMediaMetadata.mockResolvedValue({ warnings: [] });
  api.DeleteVideosWithResult.mockImplementation(ids => Promise.resolve(okDelete(ids)));
  api.DeleteImagesWithResult.mockImplementation(ids => Promise.resolve(okDelete(ids, 'batch-i')));
  api.ListCleanupDismissals.mockResolvedValue({ items: [], next_cursor: 0, has_more: false });
  api.GetPerceptualHashBackfillStatus.mockResolvedValue({ running: false });
  api.GetFrameHashBackfillStatus.mockResolvedValue({ running: false });
});

describe('页面结构（D-P3-02）', () => {
  it('页头「清理中心」+ 视频 · 图片两个标签；视频标签显示上次分析时间与「重新分析」', async () => {
    const wrapper = await mountVideo(videoStatus(videoAnalysis({ duplicate_groups: [{ original: video(1), candidates: [video(2)], reason: '一致' }] })));
    expect(wrapper.get('h1').text()).toBe('清理中心');
    expect(wrapper.findAll('[role="tab"][data-test^="cleanup-tab-"]').map(tab => tab.text())).toEqual(['视频', '图片']);
    expect(wrapper.get('[data-test="cleanup-tab-video"]').attributes('aria-selected')).toBe('true');
    expect(wrapper.get('.ui-page-header__subtitle').text()).toBe(`视频：上次分析 ${formatLocalTime(VIDEO_STARTED_AT)}`);
    expect(wrapper.get('[data-test="cleanup-reanalyze"]').text()).toBe('重新分析');
    // 视频这一侧：统计卡、按目录分组、选择操作条（有默认勾选的精确重复副本）。
    expect(videoPanelEl(wrapper).findAll('[data-test="cleanup-category"]')).toHaveLength(4);
    expect(videoPanelEl(wrapper).find('[data-test="cleanup-dir-section"]').exists()).toBe(true);
    expect(videoPanelEl(wrapper).get('[data-test="cleanup-trash-selected"]').text()).toBe('移到废纸篓 (1)');

    api.StartCleanupAnalysisFromSettings.mockResolvedValue({ running: true, completed: false, progress: { stage: 'load' } });
    await wrapper.get('[data-test="cleanup-reanalyze"]').trigger('click');
    await flushPromises();
    expect(api.StartCleanupAnalysisFromSettings).toHaveBeenCalledTimes(1);
    expect(wrapper.get('.ui-page-header__subtitle').text()).toBe('视频：正在分析…');
    expect(wrapper.get('[data-test="cleanup-reanalyze"]').text()).toBe('分析中…');
    expect(wrapper.get('[data-test="cleanup-reanalyze"]').element.disabled).toBe(true);
  });

  it('切标签发 update:tab；图片标签显示图片的上次分析时间与「重新分析」（钩子 cleanup-start），去图片管理（钩子 cleanup-back）', async () => {
    const wrapper = await mountVideo(videoStatus(videoAnalysis()));
    await wrapper.get('[data-test="cleanup-tab-image"]').trigger('click');
    expect(lastEmitted(wrapper, 'update:tab')).toEqual(['image']);

    api.GetImageCleanupStatus.mockResolvedValue(imageStatus({ duplicate_groups: [exactImages()] }));
    await wrapper.setProps({ tab: 'image' });
    await flushPromises();
    expect(wrapper.find('[data-test="photo-cleanup-page"]').exists()).toBe(true);
    // 视频这一侧只隐藏、不卸载。
    expect(videoPanelEl(wrapper).attributes('style')).toContain('display: none');
    expect(wrapper.get('.ui-page-header__subtitle').text()).toBe(`图片：上次分析 ${formatLocalTime('2026-10-12T01:00:00Z')}`);
    expect(wrapper.get('[data-test="cleanup-start"]').text()).toBe('重新分析');
    await wrapper.get('[data-test="cleanup-back"]').trigger('click');
    expect(lastEmitted(wrapper, 'navigate')).toEqual(['photos-manage']);

    await wrapper.setProps({ tab: 'video' });
    await flushPromises();
    expect(wrapper.find('[data-test="photo-cleanup-page"]').exists()).toBe(false);
    await wrapper.get('[data-test="cleanup-to-library"]').trigger('click');
    expect(lastEmitted(wrapper, 'navigate')).toEqual(['library-manage']);
  });

  it('首次打开视频标签：读状态，没有结果就开始分析；离开再回来只读状态、不另起一轮', async () => {
    api.GetCleanupStatus.mockResolvedValue({ running: false, completed: false, cancelled: false });
    const wrapper = mount();
    await flushPromises();
    expect(api.StartCleanupAnalysisFromSettings).toHaveBeenCalledTimes(1);

    api.GetCleanupStatus.mockResolvedValue(videoStatus(videoAnalysis()));
    await wrapper.setProps({ pageActive: false });
    await flushPromises();
    api.GetCleanupStatus.mockClear();
    await wrapper.setProps({ pageActive: true });
    await flushPromises();
    expect(api.GetCleanupStatus).toHaveBeenCalledTimes(1);
    expect(api.StartCleanupAnalysisFromSettings).toHaveBeenCalledTimes(1);
  });

  it('首次在图片标签上打开：视频这一侧不挂载、不读视频清理状态；图片没结果时自动分析一次', async () => {
    const wrapper = await mountImage({ running: false, completed: false, error: '', progress: {}, analysis: null });
    expect(panel(wrapper).exists()).toBe(false);
    expect(api.GetCleanupStatus).not.toHaveBeenCalled();
    expect(api.StartImageCleanupAnalysis).toHaveBeenCalledTimes(1);
    expect(wrapper.find('[data-test="photo-cleanup-page"]').exists()).toBe(true);
  });

  it('视频标签首屏读状态时显示骨架，读到之前没有结果也不误报「还没有分析过」', async () => {
    let resolveStatus;
    api.GetCleanupStatus.mockImplementation(() => new Promise(resolve => { resolveStatus = resolve; }));
    const wrapper = mount();
    await flushPromises();
    expect(videoPanelEl(wrapper).find('[data-test="cleanup-status-loading"]').exists()).toBe(true);
    expect(videoPanelEl(wrapper).find('[data-test="cleanup-video-idle"]').exists()).toBe(false);
    expect(videoPanelEl(wrapper).find('[data-test="cleanup-tiles"]').exists()).toBe(false);
    expect(wrapper.get('.ui-page-header__subtitle').text()).toBe('视频：正在读取…');
    expect(wrapper.get('[data-test="cleanup-reanalyze"]').element.disabled).toBe(true);
    resolveStatus(videoStatus(videoAnalysis()));
    await flushPromises();
    expect(videoPanelEl(wrapper).find('[data-test="cleanup-status-loading"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="cleanup-reanalyze"]').element.disabled).toBe(false);
  });

  it('读不到视频清理状态：横幅说明并给「重新分析」', async () => {
    api.GetCleanupStatus.mockRejectedValue(new Error('数据库繁忙'));
    const wrapper = mount();
    await flushPromises();
    const banner = videoPanelEl(wrapper).get('[data-test="cleanup-analysis-error"]');
    expect(banner.text()).toContain('获取清理候选失败');
    expect(wrapper.get('.ui-page-header__subtitle').text()).toBe('视频：上一轮分析没有完成');
    // 没有结果时「已忽略」照样能进（撤销忽略不依赖本轮结果），只有这一张卡。
    expect(videoPanelEl(wrapper).get('[data-test="cleanup-tiles"]').classes()).toContain('cleanup-tiles--single');
    expect(videoPanelEl(wrapper).find('[data-test="cleanup-dismissed-tab"]').exists()).toBe(true);
    api.GetCleanupStatus.mockResolvedValue(null);
    await banner.get('[data-test="cleanup-retry"]').trigger('click');
    await flushPromises();
    expect(api.StartCleanupAnalysisFromSettings).toHaveBeenCalledTimes(1);
  });
});

describe('视频标签的删除宿主（原片库页 trashCleanupVideos，D-P3-02）', () => {
  it('勾中的候选先合并再移到废纸篓：deleteFile 恒为 true、文件名用面板给的；撤销提示进全局与「最近操作」，片库经 videos-changed 刷新', async () => {
    const wrapper = await mountVideo(videoStatus(videoAnalysis({
      duplicate_groups: [{ original: video(1), candidates: [video(2)], reason: '一致' }]
    })));
    expect(panel(wrapper).vm.cleanupSelection).toEqual([2]);
    await confirmVideoDelete(wrapper);

    expect(api.MergeMediaMetadata).toHaveBeenCalledWith('video', 1, [2], FULL_MERGE);
    expect(api.DeleteVideosWithResult).toHaveBeenCalledWith([2], true, expect.stringMatching(/^[0-9a-f]{32}$/));
    expect(lastEmitted(wrapper, 'videos-changed')).toEqual([{ removedIDs: [2] }]);
    expect(undoToast().message).toContain('已移到废纸篓 1 个视频');
    expect(recentOperations.list()).toHaveLength(1);
    // 已移走的留在结果里置灰，结果标为可能过期。
    expect(panel(wrapper).vm.cleanupTrashedIDs).toEqual([2]);
    expect(videoPanelEl(wrapper).find('[data-test="cleanup-outdated-hint"]').exists()).toBe(true);
    // 删完问一句要不要重新分析（默认答取消，不重跑）。
    expect(feedback.confirmAction).toHaveBeenCalledWith(expect.objectContaining({ title: '重新分析清理候选' }));
    expect(api.StartCleanupAnalysisFromSettings).not.toHaveBeenCalled();

    // 撤销：整批恢复，放掉「已移到废纸篓」标记，片库再刷新一次。
    api.RestoreTrashBatch.mockResolvedValue({ batch_id: 'batch-v', items: [{ id: 2, code: 'ok' }] });
    await runToastAction(undoToast().id);
    await flushPromises();
    expect(api.RestoreTrashBatch).toHaveBeenCalledWith('video', 'batch-v');
    expect(panel(wrapper).vm.cleanupTrashedIDs).toEqual([]);
    expect(wrapper.emitted('videos-changed')).toHaveLength(2);
    expect(lastEmitted(wrapper, 'videos-changed')).toEqual([{ removedIDs: [] }]);
    expect(recentOperations.list()).toEqual([]);
  });

  // 清理的删除前汇总确认（D-PC49）带着「合并元数据」的选择（D-PC48），从来不看设置里的「删除前确认」与「删除原文件」：
  // 两种设置下都先弹汇总确认、都是移到废纸篓。本页不读设置。
  it.each([
    [{ confirm_before_delete: true, delete_original_file: false }],
    [{ confirm_before_delete: false, delete_original_file: false }],
    [{ confirm_before_delete: false, delete_original_file: true }]
  ])('「删除前确认」与「删除原文件」设置为 %o 时：照样先弹汇总确认，确认后移到废纸篓', async (settings) => {
    api.GetSettings.mockResolvedValue(settings);
    const wrapper = await mountVideo(videoStatus(videoAnalysis({
      duplicate_groups: [{ original: video(1), candidates: [video(2)], reason: '一致' }]
    })));
    await wrapper.vm.$nextTick();
    await videoPanelEl(wrapper).get('[data-test="cleanup-trash-selected"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-test="cleanup-delete-confirm-dialog"]').exists()).toBe(true);
    expect(api.DeleteVideosWithResult).not.toHaveBeenCalled();
    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();
    expect(api.DeleteVideosWithResult).toHaveBeenCalledWith([2], true, expect.any(String));
    expect(api.GetSettings).not.toHaveBeenCalled();
  });

  it('部分失败：成功的置灰并进撤销，失败的留着勾选可重试并报原因；片库只按成功的摘掉', async () => {
    const wrapper = await mountVideo(videoStatus(videoAnalysis({
      duplicate_groups: [
        { original: video(1), candidates: [video(2)], reason: '一致' },
        { original: video(3), candidates: [video(4)], reason: '一致' }
      ]
    })));
    api.DeleteVideosWithResult.mockResolvedValue({ batch_id: 'batch-p', items: [{ id: 2, code: 'ok' }, { id: 4, code: 'volume_offline', message: '' }] });
    await confirmVideoDelete(wrapper);

    expect(api.DeleteVideosWithResult).toHaveBeenCalledWith([2, 4], true, expect.any(String));
    expect(panel(wrapper).vm.cleanupTrashedIDs).toEqual([2]);
    expect(panel(wrapper).vm.cleanupSelection).toEqual([4]);
    expect(lastEmitted(wrapper, 'videos-changed')).toEqual([{ removedIDs: [2] }]);
    const errors = feedback.notifyError.mock.calls.map(call => String(call[0])).join('\n');
    expect(errors).toContain('批量清理完成：成功 1 个，失败 1 个');
    expect(errors).toContain('视频 4');
    expect(undoToast().message).toContain('已移到废纸篓 1 个视频');
  });

  it('D-PC02 所在磁盘不支持废纸篓：先问怎么处理，列出面板给的文件名；永久删除要输入按钮上的文字', async () => {
    const wrapper = await mountVideo(videoStatus(videoAnalysis({
      duplicate_groups: [{ original: video(1), candidates: [video(7, { name: 'far-away.mp4' })], reason: '一致' }]
    })));
    api.DeleteVideosWithResult.mockResolvedValue({ batch_id: 'b1', items: [{ id: 7, code: 'trash_unsupported' }] });
    api.PermanentlyDeleteVideos.mockResolvedValue({ items: [{ id: 7, code: 'ok' }] });
    await wrapper.vm.$nextTick();
    await videoPanelEl(wrapper).get('[data-test="cleanup-trash-selected"]').trigger('click');
    await flushPromises();
    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();

    expect(wrapper.get('[data-test="trash-unsupported-names"]').text()).toContain('far-away.mp4');
    // 二选一开着：删除流程还没结束，不能离开这一页，也不能切到图片标签。
    expect(lastEmitted(wrapper, 'busy-change')).toEqual([true]);
    expect(wrapper.get('[data-test="cleanup-tab-image"]').element.disabled).toBe(true);
    await wrapper.get('[data-test="trash-unsupported-permanent"]').trigger('click');
    expect(wrapper.get('[data-test="trash-unsupported-confirm-names"]').text()).toContain('far-away.mp4');
    await wrapper.get('[data-test="trash-unsupported-confirm-input"]').setValue('永久删除');
    await wrapper.get('[data-test="trash-unsupported-permanent-confirm"]').trigger('click');
    await flushPromises();

    expect(api.PermanentlyDeleteVideos).toHaveBeenCalledWith([7]);
    expect(lastEmitted(wrapper, 'videos-changed')).toEqual([{ removedIDs: [7] }]);
    expect(lastEmitted(wrapper, 'busy-change')).toEqual([false]);
    // 永久删除的没有撤销。
    expect(undoToast()).toBeUndefined();
  });

  it('trashCleanupVideos / afterTrashCleanupVideos 的契约：结果形状与先删除、后提示与通知片库的顺序', async () => {
    const outcome = {
      kind: 'video', deleteFile: true, batchIDs: ['b1'], trashed: [2], recordOnly: [], fileMissing: [], permanent: [], cancelled: [],
      kept: [4], failures: [{ id: 3, code: 'volume_offline', text: '所在磁盘未连接或不可访问，未做任何改动' }],
      get removedIDs() { return [...this.trashed, ...this.recordOnly, ...this.fileMissing, ...this.permanent]; },
      get remainingIDs() { return [...this.failures.map(item => item.id), ...this.cancelled, ...this.kept]; }
    };
    const runDelete = vi.fn().mockResolvedValue(outcome);
    const showDeleteNotice = vi.fn();
    const wrapper = mount({}, { global: { stubs: { TrashUndoBanner: { template: '<div />', methods: { runDelete, showDeleteNotice } } } } });
    await flushPromises();

    const result = await wrapper.vm.trashCleanupVideos([2, 3, 4], { names: { 2: 'copy.mp4', 3: 'offline.mp4', 4: 'smb.mp4' } });
    expect(runDelete).toHaveBeenCalledWith({ ids: [2, 3, 4], deleteFile: true, names: { 2: 'copy.mp4', 3: 'offline.mp4', 4: 'smb.mp4' } });
    expect(result.succeededIDs).toEqual([2]);
    expect([...result.failedIDs]).toEqual([3, 4]);
    expect(result.result).toEqual({
      requested: 3,
      succeeded: 1,
      failed: 2,
      errors: [
        { video_id: 3, error: '所在磁盘未连接或不可访问，未做任何改动' },
        { video_id: 4, error: '所在磁盘不支持废纸篓，已保留' }
      ]
    });
    // 第一步只删除：撤销提示与通知片库是第二步（中间留给面板收窄勾选）。
    expect(showDeleteNotice).not.toHaveBeenCalled();
    expect(wrapper.emitted('videos-changed')).toBeUndefined();

    wrapper.vm.afterTrashCleanupVideos(result.succeededIDs);
    expect(showDeleteNotice).toHaveBeenCalledWith(outcome, { reportFailures: false });
    expect(lastEmitted(wrapper, 'videos-changed')).toEqual([{ removedIDs: [2] }]);
  });

  it('删除确认框开着或删除进行中：busy-change(true)，图片标签禁用、切不过去；结束后恢复', async () => {
    const wrapper = await mountVideo(videoStatus(videoAnalysis({
      duplicate_groups: [{ original: video(1), candidates: [video(2)], reason: '一致' }]
    })));
    expect(lastEmitted(wrapper, 'busy-change')).toEqual([false]);
    let finishDelete;
    api.DeleteVideosWithResult.mockImplementation(ids => new Promise(resolve => { finishDelete = () => resolve(okDelete(ids)); }));
    await wrapper.vm.$nextTick();
    await videoPanelEl(wrapper).get('[data-test="cleanup-trash-selected"]').trigger('click');
    await flushPromises();
    // 确认框开着：已经算删除流程。
    expect(lastEmitted(wrapper, 'busy-change')).toEqual([true]);
    expect(wrapper.get('[data-test="cleanup-tab-image"]').element.disabled).toBe(true);
    expect(wrapper.get('[data-test="cleanup-reanalyze"]').element.disabled).toBe(true);
    expect(wrapper.get('[data-test="cleanup-to-library"]').element.disabled).toBe(true);
    wrapper.vm.selectTab('image');
    expect(wrapper.emitted('update:tab')).toBeUndefined();

    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();
    expect(lastEmitted(wrapper, 'busy-change')).toEqual([true]);
    finishDelete();
    await flushPromises();
    expect(lastEmitted(wrapper, 'busy-change')).toEqual([false]);
    expect(wrapper.get('[data-test="cleanup-tab-image"]').element.disabled).toBe(false);
  });

  it('别处（片库管理的回收站）恢复了视频：放掉审阅里的已删标记，来自回收站对话框的再读一次状态', async () => {
    const wrapper = await mountVideo(videoStatus(videoAnalysis({
      duplicate_groups: [{ original: video(1), candidates: [video(2), video(3)], reason: '一致' }]
    })));
    await confirmVideoDelete(wrapper);
    expect(panel(wrapper).vm.cleanupTrashedIDs).toEqual([2, 3]);
    api.GetCleanupStatus.mockClear();
    await wrapper.vm.handleVideosRestored([2], false);
    expect(panel(wrapper).vm.cleanupTrashedIDs).toEqual([3]);
    expect(api.GetCleanupStatus).not.toHaveBeenCalled();
    await wrapper.vm.handleVideosRestored([], true);
    expect(api.GetCleanupStatus).toHaveBeenCalledTimes(1);
  });
});

describe('请求与命令落在对应标签（D-P3-02）', () => {
  it('AI 审阅「去清理」的请求：打开视频审阅并定位那一对同源', async () => {
    api.GetCleanupStatus.mockResolvedValue(videoStatus(videoAnalysis({
      duplicate_groups: [{ original: video(1), candidates: [video(2)], reason: '一致' }],
      same_source_groups: [{ relation_id: 42, preferred: video(5, { directory: '/lib/z' }), alternative: video(6, { directory: '/lib/z' }), reason: 'AI 判断同源', estimated_savings: 600 }]
    })));
    const wrapper = mount({ request: { token: 1, open: true, relationId: 42, consolidationTaskID: 0 } });
    await flushPromises();
    expect(panel(wrapper).vm.cleanupCategory).toBe('same-source');
    expect(panel(wrapper).vm.activeCleanupDirectory).toBe('/lib/z');
    expect(videoPanelEl(wrapper).get('[data-test="cleanup-group-card"][data-focused="true"]').attributes('data-kind')).toBe('same-source');
    // 同一个请求（token 不变）不再处理第二次。
    const reads = api.GetCleanupStatus.mock.calls.length;
    await wrapper.setProps({ request: { token: 1, open: true, relationId: 42, consolidationTaskID: 0 } });
    await flushPromises();
    expect(api.GetCleanupStatus.mock.calls.length).toBe(reads);
  });

  it('任务中心打开集中整理任务的请求：只打开那个任务，不为此发起分析', async () => {
    api.GetCleanupStatus.mockResolvedValue({ running: false, completed: false, cancelled: false });
    const wrapper = mount({ request: { token: 2, open: false, relationId: 0, consolidationTaskID: 42 } });
    await flushPromises();
    expect(api.GetCleanupConsolidationStatus).toHaveBeenCalledWith(42);
    expect(api.StartCleanupAnalysisFromSettings).not.toHaveBeenCalled();
  });

  it('请求在图片标签上先留着，切到视频标签时才处理', async () => {
    const wrapper = await mountImage(imageStatus({ duplicate_groups: [exactImages()] }));
    api.GetCleanupStatus.mockResolvedValue({ running: false, completed: false, cancelled: false });
    await wrapper.setProps({ request: { token: 3, open: false, relationId: 0, consolidationTaskID: 7 } });
    await flushPromises();
    expect(api.GetCleanupConsolidationStatus).not.toHaveBeenCalled();
    await wrapper.setProps({ tab: 'video' });
    await flushPromises();
    expect(api.GetCleanupConsolidationStatus).toHaveBeenCalledWith(7);
  });

  it('注册 library.openCleanup / photos.openCleanup：各自落在自己的标签上；卸载后注销', async () => {
    const wrapper = await mountImage(imageStatus({ duplicate_groups: [exactImages()] }));
    const visible = commandList().map(command => command.id);
    expect(visible).toEqual(expect.arrayContaining(['library.openCleanup', 'photos.openCleanup']));
    expect(findCommand('library.openCleanup').label).toBe('清理中心 · 视频');

    findCommand('photos.openCleanup').run();
    expect(wrapper.emitted('update:tab')).toBeUndefined();

    api.GetCleanupStatus.mockResolvedValue({ running: false, completed: false, cancelled: false });
    findCommand('library.openCleanup').run();
    expect(lastEmitted(wrapper, 'update:tab')).toEqual(['video']);
    await wrapper.setProps({ tab: 'video' });
    await flushPromises();
    // 「打开清理审阅」：读状态，没有结果就开始分析。
    expect(api.StartCleanupAnalysisFromSettings).toHaveBeenCalledTimes(1);

    findCommand('photos.openCleanup').run();
    expect(lastEmitted(wrapper, 'update:tab')).toEqual(['image']);
    wrapper.unmount();
    expect(findCommand('library.openCleanup')).toBeNull();
    expect(findCommand('photos.openCleanup')).toBeNull();
  });

  it('「补全感知哈希 / 帧哈希」由本页发起，运行态交给视频审阅的按钮', async () => {
    const handlers = {};
    window.runtime = { EventsOn: (name, handler) => { handlers[name] = handler; return vi.fn(); } };
    api.StartPerceptualHashBackfill.mockResolvedValue({ running: true });
    const wrapper = await mountVideo(videoStatus(videoAnalysis({ stale_hash_count: 3, stale_frame_hash_count: 2 })));
    await videoPanelEl(wrapper).get('[data-test="cleanup-stale-hash-hint"] button').trigger('click');
    await flushPromises();
    expect(api.StartPerceptualHashBackfill).toHaveBeenCalledTimes(1);
    expect(panel(wrapper).props('perceptualHashRunning')).toBe(true);
    expect(videoPanelEl(wrapper).get('[data-test="cleanup-stale-hash-hint"] button').text()).toBe('补全中…');

    handlers['frame-hash-backfill-state']({ running: true });
    await flushPromises();
    expect(panel(wrapper).props('frameHashRunning')).toBe(true);
    api.StartFrameHashBackfill.mockRejectedValue(new Error('忙'));
    await wrapper.vm.startFrameHashBackfill();
    expect(feedback.notifyError).toHaveBeenCalledWith(expect.stringContaining('启动帧哈希补全失败'));
  });

  it('集中整理在后台移动了文件：视频审阅标出结果可能过期', async () => {
    const handlers = {};
    window.runtime = { EventsOn: (name, handler) => { handlers[name] = handler; return vi.fn(); } };
    const wrapper = await mountVideo(videoStatus(videoAnalysis({
      duplicate_groups: [{ original: video(1), candidates: [video(2)], reason: '一致' }]
    })));
    expect(videoPanelEl(wrapper).find('[data-test="cleanup-outdated-hint"]').exists()).toBe(false);
    handlers['cleanup-consolidation-progress']({ id: 5, version: 2, status: 'running', completed: 1 });
    await flushPromises();
    expect(videoPanelEl(wrapper).find('[data-test="cleanup-outdated-hint"]').exists()).toBe(false);
    handlers['cleanup-consolidation-progress']({ id: 5, version: 3, status: 'completed', completed: 1 });
    await flushPromises();
    expect(videoPanelEl(wrapper).find('[data-test="cleanup-outdated-hint"]').exists()).toBe(true);
  });
});

// 原 PhotoLibraryPage.test.js 的「cleanup review」整节（D-P3-02 迁移）：图片清理从图片库的整页接管
// 改成清理中心的图片标签，用例照搬，入口从图片库「管理」菜单换成清理中心的图片标签，
// 「开始 / 重新分析」在页头（cleanup-start），图片库重载改为 images-changed。
describe('图片标签（原图片库清理审阅的用例）', () => {
  async function openCleanup(analysis, { staleHashCount = 0, skippedUnavailable = 0 } = {}) {
    const status = () => imageStatus({ ...analysis, stale_hash_count: staleHashCount, skipped_unavailable: skippedUnavailable });
    api.StartImageCleanupAnalysis.mockResolvedValue(status());
    return mountImage(status());
  }

  it('opens the panel and starts an analysis when the backend has nothing to show', async () => {
    const wrapper = await mountImage({ running: false, completed: false, error: '', progress: {}, analysis: null });
    expect(wrapper.find('[data-test="photo-cleanup-page"]').exists()).toBe(true);
    // 与视频侧同构（用户裁决）：既没在跑也没有可用结果时，打开就自动分析一次。
    expect(api.StartImageCleanupAnalysis).toHaveBeenCalledTimes(1);
  });

  it('IMG-04 renders both group sections grouped by directory, auto-checks only exact copies, and shows the stale hash hint', async () => {
    const wrapper = await openCleanup({ duplicate_groups: [exactImages()], near_duplicate_groups: [nearImages()] }, { staleHashCount: 7 });
    await wrapper.get('[data-test="cleanup-start"]').trigger('click');
    await flushPromises();
    expect(api.StartImageCleanupAnalysis).toHaveBeenCalledTimes(1);
    const cardKinds = () => wrapper.findAll('[data-test="cleanup-dir-section"]')
      .flatMap(section => section.findAll('[data-test="cleanup-group-card"]'))
      .map(card => card.attributes('data-kind'));
    expect(wrapper.findAll('[data-test="cleanup-dir-section"]')).toHaveLength(1);
    expect(cardKinds()).toEqual(['exact']);
    const checkedLabels = () => wrapper.findAll('[data-test="cleanup-candidate-toggle"]')
      .filter(toggle => toggle.element.checked === true)
      .map(toggle => toggle.attributes('aria-label'));
    expect(wrapper.findAll('[data-test="cleanup-candidate-toggle"]')).toHaveLength(2);
    expect(checkedLabels()).toEqual(['删除 copy.jpg']);
    expect(wrapper.findAll('[data-test="cleanup-dir-toggle"]')[0].text()).toContain('/photos');
    expect(wrapper.findAll('.cleanup-member__thumb img')[0].attributes('src')).toBe('/preview/image-thumbnail/1');
    expect(wrapper.text()).toContain('4000×3000');
    expect(wrapper.get('[data-test="cleanup-stale-hint"]').text()).toContain('7');
    expect(wrapper.get('[data-test="cleanup-stale-hint"]').text()).toContain('还没有可用的指纹');
    expect(wrapper.get('[data-test="cleanup-start-image-phash"]').exists()).toBe(true);
    expect(wrapper.get('[data-test="cleanup-delete-selected"]').text()).toContain('(1)');

    await wrapper.get('[data-test="cleanup-kind-near"]').trigger('click');
    expect(cardKinds()).toEqual(['near']);
    expect(checkedLabels()).toEqual([]);
  });

  it('reports images skipped this round and stays quiet when none were', async () => {
    const wrapper = await openCleanup({ duplicate_groups: [exactImages()] }, { skippedUnavailable: 1969 });
    const hint = wrapper.get('[data-test="image-cleanup-skipped-hint"]');
    expect(hint.text()).toContain('1969');
    expect(hint.text()).toContain('文件不可访问');
    wrapper.unmount();

    const clean = await openCleanup({ duplicate_groups: [exactImages()] });
    expect(clean.find('[data-test="image-cleanup-skipped-hint"]').exists()).toBe(false);
  });

  it('collapses and expands a directory group', async () => {
    const wrapper = await openCleanup({ duplicate_groups: [exactImages()] });
    expect(wrapper.findAll('[data-test="cleanup-candidate-toggle"]')).toHaveLength(2);
    await wrapper.get('[data-test="cleanup-dir-toggle"]').trigger('click');
    expect(wrapper.findAll('[data-test="cleanup-candidate-toggle"]')).toHaveLength(0);
    await wrapper.get('[data-test="cleanup-dir-toggle"]').trigger('click');
    expect(wrapper.findAll('[data-test="cleanup-candidate-toggle"]')).toHaveLength(2);
  });

  it('D-PC49 lets the user switch which copy to keep; the previous keeper becomes selectable but is not auto-checked', async () => {
    const wrapper = await openCleanup({ duplicate_groups: [exactImages()] });
    let keepRadios = wrapper.findAll('[data-test="cleanup-keep-toggle"]');
    expect(keepRadios[0].element.checked).toBe(true);
    await keepRadios[1].setValue(true);
    await flushPromises();
    keepRadios = wrapper.findAll('[data-test="cleanup-keep-toggle"]');
    expect(keepRadios[1].element.checked).toBe(true);
    expect(wrapper.get('[data-test="cleanup-tile-pending"] [data-test="stat-tile-value"]').text()).toBe('0张');
    expect(wrapper.find('[data-test="cleanup-delete-selected"]').exists()).toBe(false);
    const previousKeeper = wrapper.findAll('[data-test="cleanup-candidate-toggle"]')
      .find(toggle => toggle.attributes('aria-label').includes('keep.jpg'));
    expect(previousKeeper.element.disabled).toBe(false);
    await previousKeeper.setValue(true);
    await flushPromises();

    await confirmImageDelete(wrapper);
    expect(api.MergeMediaMetadata).toHaveBeenCalledWith('image', 2, [1], FULL_MERGE);
    expect(api.DeleteImagesWithResult).toHaveBeenCalledWith([1], true, expect.stringMatching(/^[0-9a-f]{32}$/));
    expect(wrapper.emitted('images-changed')).toHaveLength(1);
  });

  it('LIB-12 离开图片标签以后再从「最近操作」撤销图片的删除：图片库照样重载', async () => {
    const wrapper = await openCleanup({ duplicate_groups: [exactImages()] });
    api.DeleteImagesWithResult.mockResolvedValueOnce(okDelete([2], 'b-clean'));
    await confirmImageDelete(wrapper);
    expect(photoCleanupStore.review.deletedIDs).toEqual([2]);
    expect(wrapper.emitted('images-changed')).toHaveLength(1);
    // 离开这一页：图片标签的内容卸下（清理中心本身常挂载）。
    await wrapper.setProps({ pageActive: false });
    await flushPromises();
    expect(wrapper.find('[data-test="photo-cleanup-page"]').exists()).toBe(false);

    api.RestoreTrashBatch.mockResolvedValueOnce({ batch_id: 'b-clean', items: [{ id: 2, code: 'ok' }] });
    expect(await recentOperations.undo(recentOperations.list()[0].id)).toBe(true);
    await flushPromises();
    expect(api.RestoreTrashBatch).toHaveBeenCalledWith('image', 'b-clean');
    expect(wrapper.emitted('images-changed')).toHaveLength(2);
    expect(photoCleanupStore.review.deletedIDs).toEqual([]);
  });

  // P-032 评审 Minor 5（迁移）：删除进行中不能离开——「去图片管理」禁用、发 busy-change(true)（App 拦住离开）、
  // 视频标签禁用；删完恢复。
  it('D-PC49 删除进行中不能离开图片标签：去图片管理与视频标签禁用，发 busy-change；结束后恢复', async () => {
    const wrapper = await openCleanup({ duplicate_groups: [exactImages()] });
    let finishDelete;
    api.DeleteImagesWithResult.mockImplementation(ids => new Promise(resolve => { finishDelete = () => resolve(okDelete(ids, 'b')); }));
    await confirmImageDelete(wrapper);
    expect(api.DeleteImagesWithResult).toHaveBeenCalledTimes(1);

    expect(wrapper.get('[data-test="cleanup-back"]').element.disabled).toBe(true);
    expect(wrapper.get('[data-test="cleanup-back"]').attributes('title')).toBe('正在移到废纸篓，完成后再离开');
    expect(wrapper.get('[data-test="cleanup-start"]').element.disabled).toBe(true);
    expect(wrapper.get('[data-test="cleanup-tab-video"]').element.disabled).toBe(true);
    expect(lastEmitted(wrapper, 'busy-change')).toEqual([true]);
    wrapper.vm.selectTab('video');
    expect(wrapper.emitted('update:tab')).toBeUndefined();

    finishDelete();
    await flushPromises();
    expect(wrapper.get('[data-test="cleanup-back"]').element.disabled).toBe(false);
    expect(wrapper.get('[data-test="cleanup-tab-video"]').element.disabled).toBe(false);
    expect(lastEmitted(wrapper, 'busy-change')).toEqual([false]);
  });

  it('确认框打开期间「重新分析」禁用（图片标签报 locked）', async () => {
    const wrapper = await openCleanup({ duplicate_groups: [exactImages()] });
    await wrapper.get('[data-test="cleanup-delete-selected"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-test="cleanup-delete-confirm-dialog"]').exists()).toBe(true);
    expect(wrapper.get('[data-test="cleanup-start"]').element.disabled).toBe(true);
    await wrapper.get('[data-test="cleanup-delete-cancel"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="cleanup-start"]').element.disabled).toBe(false);
  });

  it.each([
    [{ confirm_before_delete: true, delete_original_file: false }],
    [{ confirm_before_delete: false, delete_original_file: false }]
  ])('「删除前确认」设置为 %o 时：照样先弹汇总确认（带合并元数据），确认后移到废纸篓', async (settings) => {
    api.GetSettings.mockResolvedValue(settings);
    const wrapper = await openCleanup({ duplicate_groups: [exactImages()] });
    await wrapper.get('[data-test="cleanup-delete-selected"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-test="cleanup-delete-confirm-dialog"]').exists()).toBe(true);
    expect(wrapper.get('[data-test="cleanup-merge-toggle"]').element.checked).toBe(true);
    expect(api.DeleteImagesWithResult).not.toHaveBeenCalled();
    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();
    expect(api.DeleteImagesWithResult).toHaveBeenCalledWith([2], true, expect.any(String));
    expect(api.GetSettings).not.toHaveBeenCalled();
  });

  it('lets the user skip a whole group so nothing in it is deleted, then restore it', async () => {
    const wrapper = await openCleanup({ duplicate_groups: [exactImages()] });
    expect(wrapper.get('[data-test="cleanup-delete-selected"]').text()).toContain('(1)');
    await wrapper.get('[data-test="cleanup-skip-group"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="cleanup-tile-pending"] [data-test="stat-tile-value"]').text()).toBe('0张');
    expect(wrapper.find('[data-test="cleanup-delete-selected"]').exists()).toBe(false);
    expect(wrapper.findAll('[data-test="cleanup-candidate-toggle"]').every(toggle => toggle.element.disabled)).toBe(true);
    await wrapper.get('[data-test="cleanup-skip-group"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="cleanup-delete-selected"]').text()).toContain('(1)');
  });

  it('keeps the reviewed result after deleting and only re-analyses when the user confirms', async () => {
    const wrapper = await openCleanup({ duplicate_groups: [exactImages()], near_duplicate_groups: [nearImages()] });
    await confirmImageDelete(wrapper);
    expect(feedback.confirmAction).toHaveBeenCalledTimes(1);
    expect(api.StartImageCleanupAnalysis).not.toHaveBeenCalled();
    expect(wrapper.findAll('[data-test="cleanup-group-card"]')).toHaveLength(1);
    expect(wrapper.findAll('[data-test="cleanup-member-deleted"]')).toHaveLength(1);
    expect(wrapper.get('[data-test="cleanup-outdated-hint"]').exists()).toBe(true);
    await wrapper.get('[data-test="cleanup-kind-near"]').trigger('click');
    expect(wrapper.findAll('[data-test="cleanup-group-card"]')).toHaveLength(1);
  });

  it('keeps the review progress when the user leaves the page and comes back', async () => {
    const status = imageStatus({ duplicate_groups: [exactImages()], near_duplicate_groups: [nearImages()] });
    status.started_at = '2026-08-08T09:00:00Z';
    const wrapper = await mountImage(status);
    expect(wrapper.get('[data-test="cleanup-delete-selected"]').text()).toContain('(1)');
    await wrapper.get('[data-test="cleanup-kind-near"]').trigger('click');
    const nearToggle = wrapper.findAll('[data-test="cleanup-candidate-toggle"]')
      .find(toggle => toggle.attributes('aria-label').includes('near-small'));
    await nearToggle.setValue(true);
    await wrapper.get('[data-test="cleanup-dir-toggle"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="cleanup-delete-selected"]').text()).toContain('(2)');

    await wrapper.setProps({ pageActive: false });
    await flushPromises();
    await wrapper.setProps({ pageActive: true });
    await flushPromises();
    expect(wrapper.get('[data-test="cleanup-delete-selected"]').text()).toContain('(2)');
    expect(wrapper.findAll('[data-test="cleanup-candidate-toggle"]')).toHaveLength(0);
  });

  it('keeps the results visible when part of the deletion fails and only greys the ones that went through', async () => {
    const wrapper = await openCleanup({ duplicate_groups: [exactImages()], near_duplicate_groups: [nearImages()] });
    await wrapper.get('[data-test="cleanup-kind-near"]').trigger('click');
    const nearToggle = wrapper.findAll('[data-test="cleanup-candidate-toggle"]')
      .find(toggle => toggle.attributes('aria-label').includes('near-small'));
    await nearToggle.setValue(true);
    api.DeleteImagesWithResult.mockResolvedValue({ batch_id: 'batch-1', items: [{ id: 2, code: 'ok' }, { id: 4, code: 'error', message: '记录不存在' }] });
    await confirmImageDelete(wrapper);

    expect(wrapper.findAll('[data-test="cleanup-group-card"]')).toHaveLength(1);
    expect(wrapper.get('[data-test="cleanup-error"]').text()).toContain('1 张图片删除失败');
    // 本页操作的失败不给「重新分析」（那是给分析本身失败用的）。
    expect(wrapper.find('[data-test="cleanup-error-retry"]').exists()).toBe(false);
    expect(wrapper.findAll('[data-test="cleanup-member-deleted"]')).toHaveLength(0);
    expect(wrapper.get('[data-test="cleanup-delete-selected"]').text()).toContain('(1)');
    await wrapper.get('[data-test="cleanup-kind-exact"]').trigger('click');
    expect(wrapper.findAll('[data-test="cleanup-member-deleted"]')).toHaveLength(1);
    expect(wrapper.emitted('images-changed')).toHaveLength(1);
  });

  it('shows per-item facts, diffs against the kept copy, and can open the directory or reveal a file', async () => {
    const wrapper = await openCleanup({
      duplicate_groups: [{
        original: { ...image(1, 'keep.jpg', { width: 4000, height: 3000 }), file_size: 8_000_000, mod_time_ns: Date.parse('2026-03-01T00:00:00Z') * 1e6, tags: [{ id: 7, name: '旅行', color: 'var(--accent-color)' }], is_favorite: true },
        candidates: [{ ...image(2, 'copy.jpg', { width: 800, height: 600, directory: '/backup' }), file_size: 1_000_000, mod_time_ns: Date.parse('2026-03-03T00:00:00Z') * 1e6 }],
        reason: '文件大小和采样哈希一致'
      }]
    });
    const text = wrapper.text();
    expect(text).toContain('4000×3000');
    expect(text).toContain('7.6 MB');
    expect(wrapper.get('[data-test="cleanup-member-favorite"]').text()).toBe('已收藏');
    expect(text).toContain('旅行');
    expect(text).toContain('仅 1/25');
    expect(text).toContain('小 87%');
    expect(text).toContain('晚 2 天');
    expect(wrapper.get('[data-test="cleanup-member-otherdir"]').text()).toContain('/backup');
    expect(wrapper.get('[data-test="cleanup-cross-dir"]').exists()).toBe(true);
    await wrapper.get('[data-test="cleanup-open-dir"]').trigger('click');
    await flushPromises();
    expect(api.OpenImageDirectory).toHaveBeenCalledWith('/photos');
    await wrapper.findAll('[data-test="cleanup-reveal"]')[1].trigger('click');
    await flushPromises();
    expect(api.RevealImage).toHaveBeenCalledWith(2);
  });

  it('IMG-04 marks every extra exact copy by default and leaves near duplicates unchecked', async () => {
    const wrapper = await openCleanup({
      duplicate_groups: [{
        original: image(11, 'keep.jpg'),
        candidates: [image(12, 'same-dir-copy.jpg'), image(13, 'other-dir-copy.jpg', { directory: '/backup' })],
        reason: '文件大小和采样哈希一致'
      }],
      near_duplicate_groups: [nearImages()]
    });
    const checked = () => wrapper.findAll('[data-test="cleanup-candidate-toggle"]')
      .filter(toggle => toggle.element.checked)
      .map(toggle => toggle.attributes('aria-label')).join(' ');
    expect(checked()).toContain('same-dir-copy.jpg');
    expect(checked()).toContain('other-dir-copy.jpg');
    expect(checked()).not.toContain('near-small.jpg');
    await wrapper.get('[data-test="cleanup-samedir-switch"] input').setValue(true);
    await flushPromises();
    expect(checked()).toContain('same-dir-copy.jpg');
    expect(checked()).not.toContain('other-dir-copy.jpg');
  });

  it('re-reads the backend status when the tab opens so an outdated result is flagged', async () => {
    const wrapper = await mountImage(imageStatus({ duplicate_groups: [exactImages()] }, { stale: true }));
    expect(wrapper.find('[data-test="cleanup-outdated-hint"]').exists()).toBe(true);
    expect(wrapper.findAll('[data-test="cleanup-group-card"]')).toHaveLength(1);
  });

  it('deletes only the selected candidates into the trash and re-analyses when confirmed', async () => {
    feedback.confirmAction.mockResolvedValue(true);
    const wrapper = await openCleanup({ duplicate_groups: [exactImages()], near_duplicate_groups: [nearImages()] });
    await wrapper.get('[data-test="cleanup-kind-near"]').trigger('click');
    const target = wrapper.findAll('[data-test="cleanup-candidate-toggle"]').find(toggle => toggle.attributes('aria-label').includes('near-small'));
    expect(target.element.checked).toBe(false);
    expect(wrapper.get('[data-test="cleanup-delete-selected"]').text()).toContain('(1)');
    await confirmImageDelete(wrapper);
    expect(api.DeleteImagesWithResult).toHaveBeenCalledWith([2], true, expect.any(String));
    expect(api.StartImageCleanupAnalysis).toHaveBeenCalledTimes(1);
    expect(wrapper.emitted('images-changed')).toHaveLength(1);
  });

  it('IMG-07 dismisses a near-duplicate group with every member id after confirming and removes it from the list', async () => {
    const wrapper = await openCleanup({ near_duplicate_groups: [nearImages()] });
    feedback.confirmAction.mockResolvedValueOnce(true);
    await wrapper.get('[data-test="cleanup-dismiss-group"]').trigger('click');
    await flushPromises();
    expect(feedback.confirmAction).toHaveBeenCalledWith(expect.objectContaining({ title: '不是重复' }));
    expect(api.DismissImageNearDuplicateGroup).toHaveBeenCalledWith([3, 4]);
    expect(wrapper.find('[data-test="cleanup-empty"]').exists()).toBe(true);
  });

  it('分析本身失败：横幅给「重新分析」；第一次读状态之前显示骨架', async () => {
    let resolveStatus;
    const failed = { running: false, completed: false, cancelled: false, error: '读取图片记录失败', progress: {} };
    api.GetImageCleanupStatus.mockImplementation(() => new Promise(resolve => { resolveStatus = resolve; }));
    // 上一轮失败（没在跑、没有结果）：进来照旧自动分析一次；这一次同样失败。
    api.StartImageCleanupAnalysis.mockResolvedValue(failed);
    const wrapper = mount({ tab: 'image' });
    await flushPromises();
    expect(wrapper.find('[data-test="cleanup-status-loading"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="cleanup-idle"]').exists()).toBe(false);
    resolveStatus(failed);
    await flushPromises();
    expect(api.StartImageCleanupAnalysis).toHaveBeenCalledTimes(1);
    expect(wrapper.find('[data-test="cleanup-status-loading"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="cleanup-error"]').text()).toContain('读取图片记录失败');
    await wrapper.get('[data-test="cleanup-error-retry"]').trigger('click');
    await flushPromises();
    expect(api.StartImageCleanupAnalysis).toHaveBeenCalledTimes(2);
  });
});
