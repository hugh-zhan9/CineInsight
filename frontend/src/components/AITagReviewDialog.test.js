import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'ApproveAITagCandidate', 'ApproveAITagCandidates', 'ApproveAITagCandidatesByFilter', 'CountAITagCandidatesByFilter',
  'ConfirmSameSourceRelation', 'DeleteVideo', 'DeleteVideosWithResult', 'RestoreTrashBatch', 'GetAITaggingStatusSummary', 'ListAITagCandidatePage', 'ListSameSourceRelations',
  'MarkSameSourceRelationRead', 'PreviewExternally', 'RejectAITagCandidate', 'RejectAITagCandidatesByVideo',
  'RejectSameSourceRelation', 'RenameVideo', 'RetryAITagging',
].map(name => [name, vi.fn()])));
vi.mock('../../wailsjs/go/main/App', () => api);
// 批量批准走应用内确认框：默认答「确定」，需要「取消」的用例单独设置。
const feedback = vi.hoisted(() => ({ confirmAction: vi.fn(() => Promise.resolve(true)) }));
vi.mock('../utils/feedback.js', async (importOriginal) => ({ ...(await importOriginal()), ...feedback }));
vi.mock('./AddTagDialog.vue', () => ({ default: { template: '<div />' } }));
vi.mock('./AIQualityPanel.vue', () => ({ default: { template: '<div data-test="quality-panel">quality panel</div>' } }));
vi.mock('./FaceClusterReviewPanel.vue', () => ({ default: { template: '<div data-test="face-panel-stub">face panel</div>' } }));

import AITagReviewDialog from './AITagReviewDialog.vue';

// 后端按 id 游标分页：next_id 只在这一页满员时出现。
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
  vi.clearAllMocks();
  api.GetAITaggingStatusSummary.mockResolvedValue({ config_available: true });
  api.ListAITagCandidatePage.mockResolvedValue(candidatePage([]));
  api.ListSameSourceRelations.mockResolvedValue([]);
  feedback.confirmAction.mockResolvedValue(true);
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
// 现在按 id 游标翻页；搜索的口径仍是"全部待审候选"，所以输入关键词会把剩下的页翻完。
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

  it('waits for an in-flight page instead of silently searching only what is loaded', async () => {
    let resolveInFlight;
    api.ListAITagCandidatePage
      .mockResolvedValueOnce(candidatePage([tagCandidate({ id: 9 })], 9))
      .mockImplementationOnce(() => new Promise(resolve => { resolveInFlight = resolve; }))
      .mockResolvedValueOnce(candidatePage([
        tagCandidate({ id: 7, video_id: 12, video: { id: 12, name: 'dance.mp4', path: '/library/dance.mp4', tags: [] }, suggested_name: '舞蹈' }),
      ]));
    const wrapper = mount(AITagReviewDialog, { props: { visible: true } });
    await wrapper.vm.loadCandidates();
    await flushPromises();

    const inFlight = wrapper.vm.loadMoreCandidates();
    wrapper.vm.reviewSearch = 'dance';
    await flushPromises();

    resolveInFlight(candidatePage([tagCandidate({ id: 8, suggested_name: '日落' })], 8));
    await inFlight;
    await flushPromises();

    // 关键词命中的候选在最后一页：搜索必须等在途那一页翻完后继续翻，而不是就此收手。
    expect(wrapper.vm.candidateCursor).toBe(0);
    const groups = wrapper.findAll('.ai-video-group');
    expect(groups).toHaveLength(1);
    expect(groups[0].text()).toContain('dance.mp4');
  });

  it('loads every remaining page before filtering by keyword', async () => {
    api.ListAITagCandidatePage
      .mockResolvedValueOnce(candidatePage([tagCandidate({ id: 9 })], 9))
      .mockResolvedValueOnce(candidatePage([
        tagCandidate({ id: 8, video_id: 11, video: { id: 11, name: 'dance.mp4', path: '/library/dance.mp4', tags: [] }, suggested_name: '舞蹈' }),
      ], 8))
      .mockResolvedValueOnce(candidatePage([]));
    const wrapper = mount(AITagReviewDialog, { props: { visible: true } });
    await wrapper.vm.loadCandidates();
    await flushPromises();
    expect(wrapper.findAll('.ai-video-group')).toHaveLength(1);

    wrapper.vm.reviewSearch = 'dance';
    await flushPromises();

    // 关键词命中的候选在第二页上：只筛已加载的页会把它漏掉。
    expect(api.ListAITagCandidatePage).toHaveBeenCalledTimes(3);
    // 搜索那一趟按服务端上限翻，少发几倍请求。
    expect(api.ListAITagCandidatePage).toHaveBeenNthCalledWith(2, 0, '', 'pending', 9, 200);
    const groups = wrapper.findAll('.ai-video-group');
    expect(groups).toHaveLength(1);
    expect(groups[0].text()).toContain('dance.mp4');
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

  it('META-11 approves the approvable candidates of one group after confirmation', async () => {
    api.ApproveAITagCandidates.mockResolvedValue({
      requested: 2, succeeded: 2, failed: 0, superseded: 0,
      results: [{ id: 5, ok: true, item: { id: 5, video_id: 10, matched_tag_id: 20 } }, { id: 4, ok: true, item: { id: 4, video_id: 10, matched_tag_id: 21 } }],
    });
    const wrapper = await mountWithPool();

    feedback.confirmAction.mockResolvedValueOnce(false);
    await wrapper.get('[data-test="ai-approve-group-10"]').trigger('click');
    await flushPromises();
    expect(api.ApproveAITagCandidates).not.toHaveBeenCalled();

    await wrapper.get('[data-test="ai-approve-group-10"]').trigger('click');
    await flushPromises();
    // 低置信的那条批不了，不交出去。
    expect(api.ApproveAITagCandidates).toHaveBeenCalledWith([5, 4]);
    expect(wrapper.vm.candidates.map(candidate => candidate.id)).toEqual([3, 2]);
    expect(wrapper.get('[data-test="ai-review-notice"]').text()).toContain('成功 2 条');
    expect(wrapper.emitted('changed')).toHaveLength(1);
  });

  it('META-11 approves only the loaded and filtered IDs, and needs a filter first', async () => {
    api.ApproveAITagCandidates.mockResolvedValue({
      requested: 2, succeeded: 1, failed: 0, superseded: 1,
      results: [
        { id: 5, ok: true, item: { id: 5, video_id: 10, matched_tag_id: 20 } },
        { id: 2, superseded: true, message: '已手动添加该标签' },
      ],
    });
    const wrapper = await mountWithPool();
    const button = wrapper.get('[data-test="ai-approve-filtered"]');
    expect(button.attributes('disabled')).toBeDefined();
    expect(button.text()).toContain('（0）');

    await wrapper.get('[data-test="ai-review-filter-tag"]').setValue('20');
    expect(wrapper.get('[data-test="ai-approve-filtered"]').text()).toContain('（2）');
    await wrapper.get('[data-test="ai-approve-filtered"]').trigger('click');
    await flushPromises();

    expect(api.ApproveAITagCandidates).toHaveBeenCalledWith([5, 2]);
    expect(api.ApproveAITagCandidatesByFilter).not.toHaveBeenCalled();
    expect(wrapper.vm.candidates.map(candidate => candidate.id)).toEqual([4, 3]);
    // superseded 不是失败，原因要写出来。
    const notice = wrapper.get('[data-test="ai-review-notice"]').text();
    expect(notice).toContain('1 条已失效（已手动添加该标签 1 条）');
    expect(notice).not.toContain('失败');
  });

  it('META-11 previews the count before approving everything under a tag', async () => {
    api.CountAITagCandidatesByFilter.mockResolvedValue(7);
    api.ApproveAITagCandidatesByFilter.mockResolvedValue({
      requested: 7, succeeded: 7, failed: 0, superseded: 0,
      results: [{ id: 5, ok: true, item: { id: 5, video_id: 10, matched_tag_id: 20 } }],
    });
    const wrapper = await mountWithPool();
    expect(wrapper.get('[data-test="ai-approve-by-tag"]').attributes('disabled')).toBeDefined();

    await wrapper.get('[data-test="ai-review-filter-tag"]').setValue('20');
    await wrapper.get('[data-test="ai-review-filter-confidence"]').setValue('high');
    await wrapper.get('[data-test="ai-approve-by-tag"]').trigger('click');
    await flushPromises();

    expect(api.CountAITagCandidatesByFilter).toHaveBeenCalledWith({ tag_id: 20, confidence: 'high' });
    expect(feedback.confirmAction).toHaveBeenCalledWith(expect.objectContaining({ message: expect.stringContaining('为 7 个视频批准') }));
    expect(api.ApproveAITagCandidatesByFilter).toHaveBeenCalledWith({ tag_id: 20, confidence: 'high' });
    expect(wrapper.vm.candidates.map(candidate => candidate.id)).not.toContain(5);
  });

  it('META-11 does not approve by tag when the preview finds nothing or is cancelled', async () => {
    const wrapper = await mountWithPool();
    await wrapper.get('[data-test="ai-review-filter-tag"]').setValue('21');

    api.CountAITagCandidatesByFilter.mockResolvedValueOnce(0);
    await wrapper.get('[data-test="ai-approve-by-tag"]').trigger('click');
    await flushPromises();
    expect(feedback.confirmAction).not.toHaveBeenCalled();
    expect(wrapper.get('[data-test="ai-review-notice"]').text()).toContain('没有可批准');

    api.CountAITagCandidatesByFilter.mockResolvedValueOnce(3);
    feedback.confirmAction.mockResolvedValueOnce(false);
    await wrapper.get('[data-test="ai-approve-by-tag"]').trigger('click');
    await flushPromises();
    expect(api.ApproveAITagCandidatesByFilter).not.toHaveBeenCalled();
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
