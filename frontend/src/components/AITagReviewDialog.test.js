import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'ApproveAITagCandidate', 'GetAIReviewCandidates', 'PreviewAIReviewApproval', 'StartAIReviewApproval', 'GetAIReviewApproval', 'CancelAIReviewApproval',
  'ConfirmSameSourceRelation', 'DeleteVideo', 'DeleteVideosWithResult', 'RestoreTrashBatch', 'GetAITaggingStatusSummary', 'ListAITagCandidatePage', 'ListSameSourceRelations',
  'MarkSameSourceRelationRead', 'PreviewExternally', 'RejectAITagCandidate', 'RejectAITagCandidatesByVideo',
  'RejectSameSourceRelation', 'RenameVideo', 'RetryAITagging', 'SearchAITagCandidatePage',
].map(name => [name, vi.fn()])));
vi.mock('../../wailsjs/go/main/App', () => api);
// 批量批准走应用内确认框：默认答「确定」，需要「取消」的用例单独设置。
const feedback = vi.hoisted(() => ({ confirmAction: vi.fn(() => Promise.resolve(true)) }));
vi.mock('../utils/feedback.js', async (importOriginal) => ({ ...(await importOriginal()), ...feedback }));
vi.mock('./AddTagDialog.vue', () => ({ default: { template: '<div />' } }));
vi.mock('./AIQualityPanel.vue', () => ({ default: { template: '<div data-test="quality-panel">quality panel</div>' } }));
vi.mock('./FaceClusterReviewPanel.vue', () => ({ default: { template: '<div data-test="face-panel-stub">face panel</div>' } }));

import AITagReviewDialog from './AITagReviewDialog.vue';

enableAutoUnmount(afterEach);

// 后端按 id 游标分页，游标非零时可以继续读取。
const candidatePage = (items, nextID = 0) => ({ items, next_id: nextID });

const tagCandidate = (overrides = {}) => ({
  id: 1,
  video_id: 10,
  video: { id: 10, name: 'fight.mp4', path: '/library/fight.mp4', tags: [] },
  suggested_name: '动作',
  confidence: 'high',
  reasoning: 'fast cuts',
  status: 'pending',
  ...overrides,
});

beforeEach(() => {
  vi.resetAllMocks();
  api.GetAITaggingStatusSummary.mockResolvedValue({ config_available: true });
  api.ListAITagCandidatePage.mockResolvedValue(candidatePage([]));
  api.SearchAITagCandidatePage.mockResolvedValue(candidatePage([]));
  api.ListSameSourceRelations.mockResolvedValue([]);
  api.GetAIReviewApproval.mockResolvedValue({ state: 'idle', results: [] });
  api.GetAIReviewCandidates.mockResolvedValue({ video_items: [], image_items: [] });
  api.CancelAIReviewApproval.mockResolvedValue();
  feedback.confirmAction.mockResolvedValue(true);
});

describe('AITagReviewDialog server search', () => {
  it('does not present a loading or failed same-source query as an empty list', async () => {
    let failRelations;
    api.ListSameSourceRelations.mockImplementationOnce(() => new Promise((resolve, reject) => { failRelations = reject; }));
    const wrapper = mount(AITagReviewDialog, { props: { visible: true } });
    const initial = wrapper.vm.loadCandidates();
    await flushPromises();
    await wrapper.get('[data-test="same-source-review-tab"]').trigger('click');
    expect(wrapper.text()).toContain('正在加载视频同源关系');
    expect(wrapper.text()).not.toContain('暂无待处理');
    failRelations(new Error('unavailable'));
    await initial;
    await flushPromises();
    expect(wrapper.text()).toContain('同源列表未能加载');
    expect(wrapper.text()).not.toContain('暂无待处理');
  });

  it('keeps late initial summary and same-source results while dropping the old candidate page', async () => {
    let resolveSummary, resolveRelations, resolvePage;
    api.GetAITaggingStatusSummary.mockImplementationOnce(() => new Promise(resolve => { resolveSummary = resolve; }));
    api.ListSameSourceRelations.mockImplementationOnce(() => new Promise(resolve => { resolveRelations = resolve; }));
    api.ListAITagCandidatePage.mockImplementationOnce(() => new Promise(resolve => { resolvePage = resolve; }));
    api.SearchAITagCandidatePage.mockResolvedValueOnce(candidatePage([tagCandidate({ id: 3, suggested_name: '海边' })]));
    const wrapper = mount(AITagReviewDialog, { props: { visible: true } });
    const initial = wrapper.vm.loadCandidates();
    await wrapper.get('.ai-tag-review-search').setValue('海边');
    await wrapper.vm.loadCandidates({ queryOnly: true });
    resolveSummary({ pending: 100 });
    resolveRelations([{ id: 10, confirmed: false }]);
    resolvePage(candidatePage([tagCandidate({ id: 9 })], 9));
    await initial;
    await flushPromises();
    expect(wrapper.vm.candidates.map(item => item.id)).toEqual([3]);
    expect(wrapper.vm.pendingCandidateTotal).toBe(100);
    expect(wrapper.vm.pendingSameSourceCount).toBe(1);
    await wrapper.get('[data-test="same-source-review-tab"]').trigger('click');
    expect(wrapper.findAll('.same-source-row')).toHaveLength(1);
  });

  it('keeps the last successful results on failure and exposes tags outside the loaded page', async () => {
    api.ListAITagCandidatePage.mockResolvedValueOnce(candidatePage([tagCandidate()], 1));
    const wrapper = mount(AITagReviewDialog, { props: { visible: true, tags: [{ id: 500, name: '未加载标签' }] } });
    await wrapper.vm.loadCandidates();
    expect(wrapper.get('[data-test="ai-review-filter-tag"]').text()).toContain('未加载标签');
    api.SearchAITagCandidatePage.mockRejectedValueOnce(new Error('search failed'));
    await wrapper.get('.ai-tag-review-search').setValue('海边');
    await wrapper.vm.loadCandidates({ queryOnly: true });
    await flushPromises();
    expect(wrapper.find('.ai-video-group').text()).toContain('fight.mp4');
    expect(wrapper.find('.ai-tag-review-error').text()).toContain('保留上次已加载');
    expect(wrapper.get('[data-test="ai-approve-filtered"]').attributes('disabled')).toBeDefined();
    await wrapper.vm.loadMoreCandidates();
    expect(api.ListAITagCandidatePage).toHaveBeenCalledTimes(1);
  });

  it('does not apply a late response after the panel closes', async () => {
    let resolve;
    api.ListAITagCandidatePage.mockImplementationOnce(() => new Promise(done => { resolve = done; }));
    const wrapper = mount(AITagReviewDialog, { props: { visible: true } });
    const request = wrapper.vm.loadCandidates();
    await wrapper.setProps({ visible: false });
    resolve(candidatePage([tagCandidate()]));
    await request;
    expect(wrapper.vm.candidates).toEqual([]);
  });

  it('debounces a full-range query and only fetches matching pages on demand', async () => {
    vi.useFakeTimers();
    const wrapper = mount(AITagReviewDialog, { props: { visible: true } });
    try {
      api.ListAITagCandidatePage.mockResolvedValueOnce(candidatePage([tagCandidate()], 1));
      await wrapper.vm.loadCandidates();
      api.SearchAITagCandidatePage.mockResolvedValueOnce(candidatePage([tagCandidate({ id: 300, suggested_name: '海边' })], 300));
      await wrapper.get('.ai-tag-review-search').setValue('海');
      await vi.advanceTimersByTimeAsync(100);
      await wrapper.get('.ai-tag-review-search').setValue('海边');
      await vi.advanceTimersByTimeAsync(250);
      await flushPromises();
      expect(api.SearchAITagCandidatePage).toHaveBeenCalledTimes(1);
      expect(api.SearchAITagCandidatePage).toHaveBeenLastCalledWith(expect.objectContaining({ keyword: '海边', status: 'pending', cursor_id: 0 }));
      expect(api.ListAITagCandidatePage).toHaveBeenCalledTimes(1);
      expect(api.GetAITaggingStatusSummary).toHaveBeenCalledTimes(1);
      expect(wrapper.vm.candidates.map(item => item.id)).toEqual([300]);
      await wrapper.vm.loadMoreCandidates();
      expect(api.SearchAITagCandidatePage).toHaveBeenLastCalledWith(expect.objectContaining({ keyword: '海边', cursor_id: 300 }));
    } finally { wrapper.unmount(); vi.useRealTimers(); }
  });

  it('ignores an old query response even when its cursor matches the new query', async () => {
    vi.useFakeTimers();
    const wrapper = mount(AITagReviewDialog, { props: { visible: true } });
    try {
      let resolveOld;
      api.SearchAITagCandidatePage.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve; }));
      await wrapper.get('.ai-tag-review-search').setValue('动作');
      await vi.advanceTimersByTimeAsync(250);
      api.SearchAITagCandidatePage.mockResolvedValueOnce(candidatePage([tagCandidate({ id: 30, suggested_name: '海边' })], 30));
      await wrapper.get('.ai-tag-review-search').setValue('海边');
      await vi.advanceTimersByTimeAsync(250);
      resolveOld(candidatePage([tagCandidate({ id: 40 })], 40));
      await flushPromises();
      expect(wrapper.vm.candidates.map(item => item.id)).toEqual([30]);
      expect(wrapper.vm.candidateCursor).toBe(30);
    } finally { wrapper.unmount(); vi.useRealTimers(); }
  });
});

// 2026-09-01 起两个顶层页签合并成主从布局：待审在左，质量评估作为右侧常驻
// 只读面板——判断一条候选值不值得批准时，命中率就该在旁边，而不是隔一次点击。
describe('AITagReviewDialog quality entry', () => {
  it('can hide the quality view independently', async () => {
    const wrapper = mount(AITagReviewDialog, { props: { visible: true, qualityEnabled: false } });
    await flushPromises();
    expect(wrapper.find('[data-test="ai-quality-tab"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="quality-panel"]').exists()).toBe(false);
    // 关掉质量面板不影响待审工作台。
    expect(wrapper.find('.ai-tag-review-actions').exists()).toBe(true);
    expect(wrapper.find('.ai-review-split--with-quality').exists()).toBe(false);
  });

  it('keeps quality visible beside the review stream instead of behind a tab', async () => {
    const wrapper = mount(AITagReviewDialog, { props: { visible: true, qualityEnabled: true } });
    await flushPromises();
    // 不需要先点任何东西，面板就在那儿。
    expect(wrapper.find('[data-test="quality-panel"]').exists()).toBe(true);
    expect(wrapper.find('.ai-tag-review-actions').exists()).toBe(true);
    expect(wrapper.find('.ai-review-split--with-quality').exists()).toBe(true);
  });
});

describe('AITagReviewDialog same-source review', () => {
  // META-08（D-PC27）：确认同源后卡片不再消失，而是换成「已确认同源 · 去清理」。
  it('META-08 confirms a relation, keeps the card and offers going to cleanup', async () => {
    api.ListSameSourceRelations.mockResolvedValueOnce([{
      id: 9,
      video_a_id: 1,
      video_a: { id: 1, name: 'A.mp4', path: '/library/original/A.mp4' },
      video_b_id: 2,
      video_b: { id: 2, name: 'B.mp4', path: '/library/alternate/B.mp4' },
      confidence: 'high',
      is_unread: false,
    }]);
    api.ConfirmSameSourceRelation.mockResolvedValueOnce();
    const wrapper = mount(AITagReviewDialog, { props: { visible: true } });
    await wrapper.vm.loadCandidates();
    await flushPromises();
    expect(wrapper.find('.same-source-row').exists()).toBe(false);
    expect(wrapper.get('[data-test="ai-candidate-review-tab"]').classes()).toContain('active');
    await wrapper.get('[data-test="same-source-review-tab"]').trigger('click');

    const confirmButton = wrapper.findAll('.same-source-row button').find(button => button.text() === '确认同源');
    expect(confirmButton).toBeTruthy();
    expect(wrapper.text()).toContain('/library/original/A.mp4');
    expect(wrapper.text()).toContain('/library/alternate/B.mp4');
    const thumbnails = wrapper.findAll('.same-source-thumbnail img');
    expect(thumbnails).toHaveLength(2);
    expect(thumbnails[0].attributes('src')).toBe('/preview/thumbnail/1');
    expect(thumbnails[1].attributes('src')).toBe('/preview/thumbnail/2');
    await thumbnails[0].trigger('error');
    expect(wrapper.findAll('.same-source-thumbnail--failed')).toHaveLength(1);
    const previewButtons = wrapper.findAll('.same-source-row button').filter(button => button.text() === '预览');
    expect(previewButtons).toHaveLength(1);
    await previewButtons[0].trigger('click');
    await flushPromises();
    expect(api.PreviewExternally).toHaveBeenCalledTimes(2);
    expect(api.PreviewExternally).toHaveBeenNthCalledWith(1, 1);
    expect(api.PreviewExternally).toHaveBeenNthCalledWith(2, 2);
    await confirmButton.trigger('click');
    await flushPromises();

    expect(api.ConfirmSameSourceRelation).toHaveBeenCalledWith(9);
    expect(wrapper.find('.same-source-row').exists()).toBe(true);
    expect(wrapper.find('[data-test="same-source-confirmed-9"]').text()).toBe('已确认同源');
    expect(wrapper.findAll('.same-source-row button').some(button => button.text() === '确认同源')).toBe(false);
    // 确认过的不再算待审。
    expect(wrapper.get('[data-test="same-source-review-tab"]').text()).toContain('0');
    await wrapper.get('[data-test="same-source-go-cleanup-9"]').trigger('click');
    expect(wrapper.emitted('open-cleanup')).toEqual([[{ relationId: 9, videoIds: [1, 2] }]]);
  });

  it('deletes either same-source video record while keeping the original file', async () => {
    const relation = {
      id: 11,
      video_a_id: 7,
      video_a: { id: 7, name: 'Left.mp4', path: '/media/Left.mp4' },
      video_b_id: 8,
      video_b: { id: 8, name: 'Right.mp4', path: '/media/Right.mp4' },
      confidence: 'high',
      is_unread: false,
    };
    api.ListSameSourceRelations.mockResolvedValueOnce([relation]).mockResolvedValueOnce([]);
    api.DeleteVideosWithResult.mockResolvedValueOnce({ batch_id: 'b1', items: [{ id: 7, code: 'ok' }] });
    const wrapper = mount(AITagReviewDialog, { props: { visible: true } });
    await wrapper.vm.loadCandidates();
    await flushPromises();
    await wrapper.get('[data-test="same-source-review-tab"]').trigger('click');

    const deleteA = wrapper.findAll('.same-source-row button').find(button => button.text() === '删除 A');
    const deleteB = wrapper.findAll('.same-source-row button').find(button => button.text() === '删除 B');
    expect(deleteA).toBeTruthy();
    expect(deleteB).toBeTruthy();
    await deleteA.trigger('click');

    expect(wrapper.text()).toContain('磁盘上的原文件会保留');
    expect(wrapper.text()).toContain('/media/Left.mp4');
    await wrapper.findAll('.ai-confirm-actions button').find(button => button.text() === '确认删除记录').trigger('click');
    await flushPromises();

    // P-034：改走带结果码的删除，仍只删记录；删完给整批撤销条（D-PC04）。
    expect(api.DeleteVideosWithResult).toHaveBeenCalledWith([7], false, expect.any(String));
    expect(api.DeleteVideo).not.toHaveBeenCalled();
    expect(wrapper.vm.sameSourceDeleteConfirm.show).toBe(false);
    expect(wrapper.find('.same-source-row').exists()).toBe(false);
    expect(wrapper.get('[data-test="delete-undo-banner"]').text()).toContain('已从片库移除 1 个视频（文件保留）');
  });

  it('LIB-12 同源删除后可以整批撤销，撤销后重取同源与候选', async () => {
    const relation = {
      id: 12, video_a_id: 7, video_a: { id: 7, name: 'Left.mp4', path: '/media/Left.mp4' },
      video_b_id: 8, video_b: { id: 8, name: 'Right.mp4', path: '/media/Right.mp4' }, confidence: 'high', is_unread: false,
    };
    api.ListSameSourceRelations.mockResolvedValueOnce([relation]).mockResolvedValueOnce([]).mockResolvedValueOnce([relation]);
    api.DeleteVideosWithResult.mockResolvedValueOnce({ batch_id: 'batch-9', items: [{ id: 8, code: 'ok' }] });
    api.RestoreTrashBatch.mockResolvedValueOnce({ items: [{ id: 8, code: 'ok' }] });
    const wrapper = mount(AITagReviewDialog, { props: { visible: true } });
    await wrapper.vm.loadCandidates();
    await flushPromises();
    await wrapper.get('[data-test="same-source-review-tab"]').trigger('click');
    await wrapper.findAll('.same-source-row button').find(button => button.text() === '删除 B').trigger('click');
    await wrapper.findAll('.ai-confirm-actions button').find(button => button.text() === '确认删除记录').trigger('click');
    await flushPromises();
    expect(wrapper.emitted('changed')).toHaveLength(1);

    await wrapper.get('[data-test="delete-undo"]').trigger('click');
    await flushPromises();
    expect(api.RestoreTrashBatch).toHaveBeenCalledWith('video', 'batch-9');
    expect(wrapper.emitted('changed')).toHaveLength(2);
    expect(wrapper.find('.same-source-row').exists()).toBe(true);
  });

  it('同源删除失败时保留这一对并说明原因', async () => {
    const relation = {
      id: 13, video_a_id: 7, video_a: { id: 7, name: 'Left.mp4', path: '/media/Left.mp4' },
      video_b_id: 8, video_b: { id: 8, name: 'Right.mp4', path: '/media/Right.mp4' }, confidence: 'high', is_unread: false,
    };
    api.ListSameSourceRelations.mockResolvedValueOnce([relation]);
    api.DeleteVideosWithResult.mockResolvedValueOnce({ batch_id: '', items: [{ id: 7, code: 'volume_offline', message: '' }] });
    const wrapper = mount(AITagReviewDialog, { props: { visible: true } });
    await wrapper.vm.loadCandidates();
    await flushPromises();
    await wrapper.get('[data-test="same-source-review-tab"]').trigger('click');
    await wrapper.findAll('.same-source-row button').find(button => button.text() === '删除 A').trigger('click');
    await wrapper.findAll('.ai-confirm-actions button').find(button => button.text() === '确认删除记录').trigger('click');
    await flushPromises();
    expect(wrapper.vm.error).toContain('删除失败');
    expect(wrapper.vm.sameSourceRelations.map(item => item.id)).toEqual([13]);
    expect(wrapper.emitted('changed')).toBeUndefined();
  });
});

// P-013：人物候选是待审工作台的第三个 section，与 AI 标签、视频同源并列。
describe('AITagReviewDialog face cluster review section', () => {
  it('switches to the face candidate section and hands the panel its own toolbar', async () => {
    const wrapper = mount(AITagReviewDialog, { props: { visible: true } });
    await flushPromises();
    expect(wrapper.find('[data-test="face-panel-stub"]').exists()).toBe(false);

    await wrapper.find('[data-test="face-cluster-review-tab"]').trigger('click');
    expect(wrapper.find('[data-test="face-panel-stub"]').exists()).toBe(true);
    // 人物候选面板自带说明与刷新，标签侧的工具条不该跟着出现。
    expect(wrapper.find('.ai-tag-review-actions').exists()).toBe(false);
    expect(wrapper.find('.ai-tag-review-search').exists()).toBe(false);

    // 切回来标签待审照旧。
    await wrapper.find('[data-test="ai-candidate-review-tab"]').trigger('click');
    expect(wrapper.find('[data-test="face-panel-stub"]').exists()).toBe(false);
    expect(wrapper.find('.ai-tag-review-actions').exists()).toBe(true);
  });

  // AI 标签候选加载失败不该把人物候选一起挡掉：两批数据来自不同的接口。
  it('keeps the face section usable when loading tag candidates failed', async () => {
    api.ListAITagCandidatePage.mockRejectedValueOnce(new Error('boom'));
    const wrapper = mount(AITagReviewDialog, { props: { visible: true } });
    await wrapper.vm.loadCandidates();
    await flushPromises();
    expect(wrapper.find('.ai-tag-review-error').exists()).toBe(true);

    await wrapper.find('[data-test="face-cluster-review-tab"]').trigger('click');
    expect(wrapper.find('[data-test="face-panel-stub"]').exists()).toBe(true);
    expect(wrapper.find('.ai-tag-review-error').exists()).toBe(false);
  });
});

// 待审候选没有上限，全量下发在大库上既压 IPC 又要一次渲染上千行。
// 搜索覆盖全部待审候选，但每次只返回匹配的一页。
describe('AITagReviewDialog pagination', () => {
  it('loads the first page and appends the next one on demand', async () => {
    api.ListAITagCandidatePage
      .mockResolvedValueOnce(candidatePage([tagCandidate({ id: 9 })], 9))
      .mockResolvedValueOnce(candidatePage([
        tagCandidate({ id: 8, video_id: 11, video: { id: 11, name: 'dance.mp4', path: '/library/dance.mp4', tags: [] }, suggested_name: '舞蹈' }),
      ]));
    const wrapper = mount(AITagReviewDialog, { props: { visible: true } });
    await wrapper.vm.loadCandidates();
    await flushPromises();

    expect(api.ListAITagCandidatePage).toHaveBeenLastCalledWith(0, '', 'pending', 0, 0);
    expect(wrapper.findAll('.ai-video-group')).toHaveLength(1);

    await wrapper.get('[data-test="ai-candidate-load-more"]').trigger('click');
    await flushPromises();

    expect(api.ListAITagCandidatePage).toHaveBeenLastCalledWith(0, '', 'pending', 9, 0);
    expect(wrapper.findAll('.ai-video-group')).toHaveLength(2);
    expect(wrapper.text()).toContain('dance.mp4');
    // 末页没有游标，按钮随之消失。
    expect(wrapper.find('[data-test="ai-candidate-load-more"]').exists()).toBe(false);
  });

  it('drops an old unfiltered page after a server search has replaced the list', async () => {
    let resolveInFlight;
    api.ListAITagCandidatePage
      .mockResolvedValueOnce(candidatePage([tagCandidate({ id: 9 })], 9))
      .mockImplementationOnce(() => new Promise(resolve => { resolveInFlight = resolve; }));
    api.SearchAITagCandidatePage.mockResolvedValueOnce(candidatePage([tagCandidate({ id: 300, suggested_name: '海边' })], 9));
    const wrapper = mount(AITagReviewDialog, { props: { visible: true } });
    await wrapper.vm.loadCandidates();
    const inFlight = wrapper.vm.loadMoreCandidates();
    await wrapper.setData({ reviewSearch: '海边' });
    await wrapper.vm.loadCandidates({ queryOnly: true });
    resolveInFlight(candidatePage([tagCandidate({ id: 8, suggested_name: '日落' })], 8));
    await inFlight;
    await flushPromises();
    expect(wrapper.vm.candidates.map(item => item.id)).toEqual([300]);
    expect(wrapper.vm.candidateCursor).toBe(9);
    expect(api.ListAITagCandidatePage).toHaveBeenCalledTimes(2);
  });

  it('returns to the unfiltered first page when the query is cleared', async () => {
    api.SearchAITagCandidatePage.mockResolvedValueOnce(candidatePage([tagCandidate({ id: 8, suggested_name: '海边' })], 8));
    const wrapper = mount(AITagReviewDialog, { props: { visible: true } });
    await wrapper.setData({ reviewSearch: '海边' });
    await wrapper.vm.loadCandidates({ queryOnly: true });
    api.ListAITagCandidatePage.mockResolvedValueOnce(candidatePage([tagCandidate({ id: 10 })], 10));
    await wrapper.setData({ reviewSearch: '' });
    await wrapper.vm.loadCandidates({ queryOnly: true });
    expect(api.ListAITagCandidatePage).toHaveBeenLastCalledWith(0, '', 'pending', 0, 0);
    expect(wrapper.vm.candidates.map(item => item.id)).toEqual([10]);
    expect(wrapper.vm.candidateCursor).toBe(10);
  });

  it('removes an approved candidate locally instead of reloading the pages', async () => {
    api.ListAITagCandidatePage.mockResolvedValue(candidatePage([
      tagCandidate({ id: 1, suggested_name: '动作', normalized_name: '动作', matched_tag_id: 10 }),
      tagCandidate({ id: 2, suggested_name: '打斗', normalized_name: '动作', matched_tag_id: 10 }),
      tagCandidate({ id: 3, video_id: 11, video: { id: 11, name: 'dance.mp4', path: '/library/dance.mp4', tags: [] }, suggested_name: '舞蹈' }),
    ]));
    api.ApproveAITagCandidate.mockResolvedValue({ id: 1, status: 'approved' });
    const wrapper = mount(AITagReviewDialog, { props: { visible: true } });
    await wrapper.vm.loadCandidates();
    await flushPromises();

    const approve = wrapper.findAll('.ai-candidate-row button').find(button => button.text() === '批准');
    await approve.trigger('click');
    await flushPromises();

    expect(api.ListAITagCandidatePage).toHaveBeenCalledTimes(1);
    // 后端把同视频同名的另一条候选一并置 superseded，前端按同一规则移除。
    expect(wrapper.vm.candidates.map(candidate => candidate.id)).toEqual([3]);
  });
});

// 刷新与在途翻页的竞态：上一批查询的下一页在刷新之后才回来，不能追加进新列表。
describe('AITagReviewDialog stale page guard', () => {
  // 危险的那个顺序：先开始（静默）重新加载，再点"加载更多"。此时任何"重新加载计数器"
  // 都已经是新值，旧游标的那一页会被当成当前结果追加——列表跳过中间一整段候选，
  // 游标还越过了它们。判定必须按请求自身的游标，而不是计数器。
  it('drops a page requested after a silent reload had already started', async () => {
    let resolveReload;
    let resolveStalePage;
    api.ListAITagCandidatePage
      .mockResolvedValueOnce(candidatePage([
        tagCandidate({ id: 9 }),
        tagCandidate({ id: 8, suggested_name: '舞蹈' }),
      ], 8))
      .mockImplementationOnce(() => new Promise(resolve => { resolveReload = resolve; }))
      .mockImplementationOnce(() => new Promise(resolve => { resolveStalePage = resolve; }));
    const wrapper = mount(AITagReviewDialog, { props: { visible: true } });
    await wrapper.vm.loadCandidates();
    await flushPromises();
    expect(wrapper.vm.candidateCursor).toBe(8);

    const reload = wrapper.vm.loadCandidates({ silent: true });
    const stale = wrapper.vm.loadMoreCandidates();

    resolveReload(candidatePage([tagCandidate({ id: 5, suggested_name: '重新取数' })], 4));
    await reload;
    await flushPromises();
    expect(wrapper.vm.candidates.map(candidate => candidate.id)).toEqual([5]);

    resolveStalePage(candidatePage([tagCandidate({ id: 20, suggested_name: '陈旧页' })], 19));
    await stale;
    await flushPromises();

    // 旧游标那一页不能进来，游标也不能被它推到 19（19 到 5 之间的候选会永远翻不到）。
    expect(wrapper.vm.candidates.map(candidate => candidate.id)).toEqual([5]);
    expect(wrapper.vm.candidateCursor).toBe(4);
  });

  it('drops an in-flight page when the list was reloaded', async () => {
    let resolveStalePage;
    api.ListAITagCandidatePage
      .mockResolvedValueOnce(candidatePage([tagCandidate({ id: 9 })], 9))
      .mockImplementationOnce(() => new Promise(resolve => { resolveStalePage = resolve; }))
      .mockResolvedValueOnce(candidatePage([tagCandidate({ id: 4, suggested_name: '重新取数' })]));
    const wrapper = mount(AITagReviewDialog, { props: { visible: true } });
    await wrapper.vm.loadCandidates();
    await flushPromises();

    wrapper.vm.loadMoreCandidates();
    await wrapper.vm.loadCandidates();
    await flushPromises();
    expect(wrapper.vm.candidates.map(candidate => candidate.id)).toEqual([4]);

    resolveStalePage(candidatePage([tagCandidate({ id: 8, suggested_name: '陈旧页' })], 8));
    await flushPromises();

    expect(wrapper.vm.candidates.map(candidate => candidate.id)).toEqual([4]);
    expect(wrapper.vm.candidateCursor).toBe(0);
  });
});

// 截图里的问题：右侧质量面板占掉 352px 后弹窗没有加宽，左侧待审流里五个动作按钮
// 又和缩略图挤在标题同一行，标题被压成一个字一行；同源与候选卡片也都没给文件大小，
// 删 A 还是删 B 无从判断。这里钉住加宽、元信息行和动作独占一行三件事。
describe('AITagReviewDialog layout and media meta', () => {
  it('widens the modal only when the quality panel is shown', async () => {
    const wide = mount(AITagReviewDialog, { props: { visible: true, qualityEnabled: true } });
    await flushPromises();
    expect(wide.get('.modal').classes()).toContain('ai-tag-review-modal--wide');
    wide.unmount();

    const narrow = mount(AITagReviewDialog, { props: { visible: true, qualityEnabled: false } });
    await flushPromises();
    expect(narrow.get('.modal').classes()).toContain('ai-tag-review-modal');
    expect(narrow.get('.modal').classes()).not.toContain('ai-tag-review-modal--wide');
    narrow.unmount();
  });

  it('shows size, duration and resolution for each candidate video and keeps actions out of the title row', async () => {
    api.ListAITagCandidatePage.mockResolvedValueOnce(candidatePage([
      tagCandidate({ video: { id: 10, name: 'fight.mp4', path: '/library/fight.mp4', size: 1.5 * 1024 * 1024 * 1024, duration: 3723, width: 1920, height: 1080, tags: [] } }),
    ]));
    const wrapper = mount(AITagReviewDialog, { props: { visible: true } });
    await wrapper.vm.loadCandidates();
    await flushPromises();

    const meta = wrapper.get('[data-test="ai-video-meta"]');
    expect(meta.text()).toContain('1.5 GB');
    expect(meta.text()).toContain('01:02:03');
    expect(meta.text()).toContain('1920×1080');
    expect(wrapper.get('.ai-video-name-text').attributes('title')).toBe('fight.mp4');
    expect(wrapper.get('.ai-video-path').attributes('title')).toBe('/library/fight.mp4');
    expect(wrapper.find('.ai-video-title .ai-video-actions').exists()).toBe(false);
    expect(wrapper.find('.ai-video-group .ai-video-actions').exists()).toBe(true);
  });

  it('omits the meta line when the payload carries no size or duration', async () => {
    api.ListAITagCandidatePage.mockResolvedValueOnce(candidatePage([tagCandidate()]));
    const wrapper = mount(AITagReviewDialog, { props: { visible: true } });
    await wrapper.vm.loadCandidates();
    await flushPromises();
    expect(wrapper.find('[data-test="ai-video-meta"]').exists()).toBe(false);
  });

  it('shows file size on both sides of a same-source pair', async () => {
    api.ListSameSourceRelations.mockResolvedValue([{
      id: 5, video_a_id: 1, video_b_id: 2,
      video_a: { id: 1, name: 'A.mp4', path: '/library/original/A.mp4', size: 4 * 1024 * 1024 * 1024, duration: 60 },
      video_b: { id: 2, name: 'B.mp4', path: '/library/alternate/B.mp4', size: 700 * 1024 * 1024, duration: 60 },
    }]);
    const wrapper = mount(AITagReviewDialog, { props: { visible: true } });
    await wrapper.vm.loadCandidates();
    await flushPromises();
    await wrapper.get('[data-test="same-source-review-tab"]').trigger('click');

    const metas = wrapper.findAll('[data-test="same-source-meta"]');
    expect(metas).toHaveLength(2);
    expect(metas[0].text()).toBe('4.0 GB · 01:00');
    expect(metas[1].text()).toBe('700.0 MB · 01:00');
  });
});

// META-08（D-PC27）：打开弹窗不再把同源全部标已读，切到同源页签才算看过。
describe('AITagReviewDialog META-08 同源已读时机与待审总数', () => {
  const unreadRelation = {
    id: 21, video_a_id: 1, video_b_id: 2,
    video_a: { id: 1, name: 'A.mp4', path: '/a/A.mp4' }, video_b: { id: 2, name: 'B.mp4', path: '/b/B.mp4' },
    confidence: 'high', is_unread: true,
  };

  it('META-08 marks same-source relations read only after switching to that tab', async () => {
    api.ListSameSourceRelations.mockResolvedValue([unreadRelation]);
    api.MarkSameSourceRelationRead.mockResolvedValue();
    const wrapper = mount(AITagReviewDialog, { props: { visible: false } });
    await wrapper.setProps({ visible: true });
    await flushPromises();
    expect(api.MarkSameSourceRelationRead).not.toHaveBeenCalled();
    expect(wrapper.emitted('changed')).toBeUndefined();

    await wrapper.get('[data-test="same-source-review-tab"]').trigger('click');
    await flushPromises();
    expect(api.MarkSameSourceRelationRead).toHaveBeenCalledWith(21);
    expect(wrapper.vm.sameSourceRelations[0].is_unread).toBe(false);
    expect(wrapper.emitted('changed')).toHaveLength(1);

    // 已读过的不会在下一次切换时重复标记。
    await wrapper.get('[data-test="ai-candidate-review-tab"]').trigger('click');
    await wrapper.get('[data-test="same-source-review-tab"]').trigger('click');
    await flushPromises();
    expect(api.MarkSameSourceRelationRead).toHaveBeenCalledTimes(1);
  });

  it('META-08 shows the backend pending total instead of the loaded page size', async () => {
    api.GetAITaggingStatusSummary.mockResolvedValue({ config_available: true, pending: 137 });
    api.ListAITagCandidatePage.mockResolvedValue(candidatePage([tagCandidate({ id: 9 })], 9));
    const wrapper = mount(AITagReviewDialog, { props: { visible: true } });
    await wrapper.vm.loadCandidates();
    await flushPromises();
    expect(wrapper.get('[data-test="ai-candidate-review-tab"]').text()).toContain('137');
    expect(wrapper.get('[data-test="ai-review-counts"]').text()).toContain('AI 标签待审 137 条（已加载 1 条）');
  });
});

// META-11（D-PC29）：组头「批准本组全部」、筛选栏「批准筛选结果（N）」与按标签整批批准。
describe('AITagReviewDialog META-11 批量批准', () => {
  it('keeps a later manual-tag update when an older candidate recheck returns', async () => {
    const original = tagCandidate({ id: 20, matched_tag_id: 20 });
    api.ListAITagCandidatePage.mockResolvedValueOnce(candidatePage([original]));
    let finish;
    api.GetAIReviewCandidates.mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
    const wrapper = mount(AITagReviewDialog, { props: { visible: true } }); await wrapper.vm.loadCandidates();
    const request = wrapper.vm.applyBatchOutcomes([{ id: 20, state: 'skipped', code: 'changed' }]); await flushPromises();
    await wrapper.vm.handleManualTagAdded({ videoIds: [10], tagIds: [99], tags: [{ id: 99, name: '刚添加的标签' }] });
    finish({ video_items: [original], image_items: [] }); await request;
    expect(wrapper.vm.candidates[0].video.tags.map(tag => tag.name)).toContain('刚添加的标签');
  });

  it('applies a later non-pending recheck after an earlier changed candidate refresh', async () => {
    api.ListAITagCandidatePage.mockResolvedValueOnce(candidatePage([tagCandidate({ id: 20, matched_tag_id: 20 })]));
    let firstReply, secondReply;
    api.GetAIReviewCandidates.mockImplementationOnce(() => new Promise(resolve => { firstReply = resolve; }))
      .mockImplementationOnce(() => new Promise(resolve => { secondReply = resolve; }));
    const wrapper = mount(AITagReviewDialog, { props: { visible: true } }); await wrapper.vm.loadCandidates(); await flushPromises();
    const first = wrapper.vm.applyBatchOutcomes([{ id: 20, state: 'skipped', code: 'changed' }]);
    await flushPromises();
    const second = wrapper.vm.applyBatchOutcomes([{ id: 10, state: 'approved', media_id: 10, tag_id: 20 }]);
    firstReply({ video_items: [tagCandidate({ id: 20, matched_tag_id: 20, reasoning: '新版本仍待审' })], image_items: [] });
    await first; await flushPromises();
    secondReply({ video_items: [], image_items: [] }); await second;
    expect(wrapper.vm.candidates).toEqual([]);
  });

  it('does not apply a late candidate recheck to a freshly loaded page', async () => {
    const initial = tagCandidate({ id: 20, matched_tag_id: 20 });
    api.ListAITagCandidatePage.mockResolvedValueOnce(candidatePage([initial]));
    let finishRecheck;
    api.GetAIReviewCandidates.mockImplementationOnce(() => new Promise(resolve => { finishRecheck = resolve; }));
    const wrapper = mount(AITagReviewDialog, { props: { visible: true } }); await wrapper.vm.loadCandidates();
    const request = wrapper.vm.applyBatchOutcomes([{ id: 20, state: 'skipped', code: 'changed' }]);
    await flushPromises();
    api.ListAITagCandidatePage.mockResolvedValueOnce(candidatePage([tagCandidate({ id: 20, reasoning: '重新加载的当前证据' })]));
    await wrapper.vm.loadCandidates();
    finishRecheck({ video_items: [], image_items: [] }); await request;
    expect(wrapper.vm.candidates).toHaveLength(1);
    expect(wrapper.vm.candidates[0].reasoning).toBe('重新加载的当前证据');
  });

  it('does not hide current pending versions or new same-target candidates when replaying an old batch', async () => {
    const current = [tagCandidate({ id: 20, matched_tag_id: 21, reasoning: '已重新分析的新理由' }), tagCandidate({ id: 30, matched_tag_id: 20 })];
    api.ListAITagCandidatePage.mockResolvedValueOnce(candidatePage(current));
    api.GetAIReviewCandidates.mockResolvedValueOnce({ video_items: current, image_items: [] });
    const wrapper = mount(AITagReviewDialog, { props: { visible: true } }); await wrapper.vm.loadCandidates(); await flushPromises();
    await wrapper.vm.applyBatchOutcomes([{ id: 20, state: 'skipped', code: 'changed' }, { id: 10, media_id: 10, tag_id: 20, state: 'approved' }]);
    await flushPromises();
    expect(wrapper.vm.candidates.map(item => item.id)).toEqual([20, 30]);
    expect(api.GetAIReviewCandidates).toHaveBeenCalledWith('video', [20, 30]);
  });

  const pool = () => [
    tagCandidate({ id: 5, video_id: 10, matched_tag_id: 20, matched_tag: { id: 20, name: '动作' }, confidence: 'high' }),
    tagCandidate({ id: 4, video_id: 10, matched_tag_id: 21, matched_tag: { id: 21, name: '夜景' }, suggested_name: '夜景', confidence: 'medium' }),
    tagCandidate({ id: 3, video_id: 10, matched_tag_id: 22, matched_tag: { id: 22, name: '室内' }, suggested_name: '室内', confidence: 'low' }),
    tagCandidate({ id: 2, video_id: 11, video: { id: 11, name: 'dance.mp4', path: '/library/dance.mp4', tags: [] }, matched_tag_id: 20, matched_tag: { id: 20, name: '动作' }, confidence: 'high' }),
  ];

  async function mountWithPool() {
    api.ListAITagCandidatePage.mockResolvedValue(candidatePage(pool()));
    const wrapper = mount(AITagReviewDialog, { props: { visible: true } });
    await wrapper.vm.loadCandidates();
    await flushPromises();
    return wrapper;
  }

  function mockBatch(results, count = results.length) {
    api.PreviewAIReviewApproval.mockResolvedValue({ token: 'frozen', matched: count, eligible: count, excluded: 0, media_count: count, link_count: count });
    api.StartAIReviewApproval.mockImplementation(async () => {
      const completed = { token: 'frozen', state: 'completed', total: count, processed: count, remaining: 0, succeeded: results.filter(item => item.state === 'approved').length, skipped: results.filter(item => item.state === 'skipped').length, failed: 0, results, next_after: results.length, has_more: false };
      api.GetAIReviewApproval.mockResolvedValue(completed);
      return { ...completed, state: 'running', results: [] };
    });
  }

  it('previews the loaded group, releases cancellation, then starts only its frozen token', async () => {
    mockBatch([{ id: 5, state: 'approved', media_id: 10, tag_id: 20 }, { id: 4, state: 'approved', media_id: 10, tag_id: 21 }]);
    const wrapper = await mountWithPool();
    feedback.confirmAction.mockResolvedValueOnce(false);
    await wrapper.get('[data-test="ai-approve-group-10"]').trigger('click'); await flushPromises();
    expect(api.StartAIReviewApproval).not.toHaveBeenCalled();
    expect(api.CancelAIReviewApproval).toHaveBeenCalledWith('video', 'frozen');
    await wrapper.get('[data-test="ai-approve-group-10"]').trigger('click'); await flushPromises();
    expect(api.PreviewAIReviewApproval).toHaveBeenLastCalledWith('video', expect.objectContaining({ scope: 'loaded', ids: [5, 4] }));
    expect(api.StartAIReviewApproval).toHaveBeenCalledWith('video', 'frozen');
    expect(wrapper.vm.candidates.map(candidate => candidate.id)).toEqual([3, 2]);
    expect(wrapper.get('[data-test="review-batch-summary"]').text()).toContain('成功 2');
    expect(wrapper.emitted('changed')).toHaveLength(1);
  });

  it('keeps a loaded-only entry and applies compact outcomes locally', async () => {
    mockBatch([{ id: 5, state: 'approved', media_id: 10, tag_id: 20 }, { id: 2, state: 'skipped', code: 'not_pending' }]);
    const wrapper = await mountWithPool();
    expect(wrapper.get('[data-test="ai-approve-filtered"]').text()).toContain('批准已加载结果（3）');
    api.SearchAITagCandidatePage.mockResolvedValueOnce(candidatePage(wrapper.vm.candidates.filter(item => item.matched_tag_id === 20)));
    await wrapper.get('[data-test="ai-review-filter-tag"]').setValue('20'); await wrapper.vm.loadCandidates({ queryOnly: true }); await flushPromises();
    await wrapper.get('[data-test="ai-approve-filtered"]').trigger('click'); await flushPromises();
    expect(api.PreviewAIReviewApproval).toHaveBeenCalledWith('video', expect.objectContaining({ scope: 'loaded', ids: [5, 2], filter: expect.objectContaining({ tag_id: 20 }) }));
    expect(wrapper.vm.candidates).toEqual([]);
    expect(wrapper.get('[data-test="review-batch-summary"]').text()).toContain('跳过 1');
  });

  it('previews the complete combined filter and confirms its count', async () => {
    mockBatch([{ id: 5, state: 'approved', media_id: 10, tag_id: 20 }], 7);
    const wrapper = await mountWithPool();
    expect(wrapper.get('[data-test="ai-approve-by-tag"]').text()).toContain('批准全部筛选结果');
    await wrapper.get('[data-test="ai-review-filter-tag"]').setValue('20');
    await wrapper.get('[data-test="ai-review-filter-confidence"]').setValue('high');
    await wrapper.get('[data-test="ai-approve-by-tag"]').trigger('click'); await flushPromises();
    expect(api.PreviewAIReviewApproval).toHaveBeenCalledWith('video', expect.objectContaining({ scope: 'filtered', ids: [], filter: expect.objectContaining({ tag_id: 20, confidence: 'high' }) }));
    expect(feedback.confirmAction).toHaveBeenCalledWith(expect.objectContaining({ message: expect.stringContaining('可批准 7 条') }));
    expect(api.StartAIReviewApproval).toHaveBeenCalledWith('video', 'frozen');
    expect(wrapper.vm.candidates.map(candidate => candidate.id)).not.toContain(5);
  });

  it('does not start an empty or cancelled full-filter preview', async () => {
    const wrapper = await mountWithPool();
    api.PreviewAIReviewApproval.mockResolvedValueOnce({ matched: 0, eligible: 0, token: '' });
    await wrapper.get('[data-test="ai-approve-by-tag"]').trigger('click'); await flushPromises();
    expect(feedback.confirmAction).not.toHaveBeenCalled();
    expect(wrapper.get('[data-test="review-batch-notice"]').text()).toContain('没有可批准');
    api.PreviewAIReviewApproval.mockResolvedValueOnce({ matched: 3, eligible: 3, token: 'next', excluded: 0, media_count: 3, link_count: 3 });
    feedback.confirmAction.mockResolvedValueOnce(false);
    await wrapper.get('[data-test="ai-approve-by-tag"]').trigger('click'); await flushPromises();
    expect(api.StartAIReviewApproval).not.toHaveBeenCalled();
    expect(api.CancelAIReviewApproval).toHaveBeenCalledWith('video', 'next');
  });

});

// META-06：手动加标签后按 (video_id, tag_id) 局部移除候选，同视频其他标签的候选保留。
describe('AITagReviewDialog META-06 手动加标签', () => {
  it('META-06 removes only the same-tag candidates of that video without reloading', async () => {
    api.ListAITagCandidatePage.mockResolvedValue(candidatePage([
      tagCandidate({ id: 5, video_id: 10, matched_tag_id: 20 }),
      tagCandidate({ id: 4, video_id: 10, matched_tag_id: 21, suggested_name: '夜景' }),
      tagCandidate({ id: 3, video_id: 11, video: { id: 11, name: 'b.mp4', path: '/b.mp4', tags: [] }, matched_tag_id: 20 }),
    ]));
    const wrapper = mount(AITagReviewDialog, { props: { visible: true } });
    await wrapper.vm.loadCandidates();
    await flushPromises();
    const loads = api.ListAITagCandidatePage.mock.calls.length;

    wrapper.vm.openManualTagDialog(wrapper.vm.groups[0]);
    await wrapper.vm.handleManualTagAdded({ videoIds: [10], tagIds: [20], tags: [{ id: 20, name: '动作', color: '#ff0000' }] });
    await flushPromises();

    expect(api.ListAITagCandidatePage).toHaveBeenCalledTimes(loads);
    expect(wrapper.vm.candidates.map(candidate => candidate.id)).toEqual([4, 3]);
    expect(wrapper.vm.groups[0].videoTags.map(tag => tag.name)).toEqual(['动作']);
    expect(wrapper.get('[data-test="ai-review-notice"]').text()).toContain('其他候选保留');
    expect(wrapper.emitted('changed')).toHaveLength(1);
  });
});
