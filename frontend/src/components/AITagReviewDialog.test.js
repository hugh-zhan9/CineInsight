import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'ApproveAITagCandidate', 'ConfirmSameSourceRelation', 'DeleteVideo', 'GetAITaggingStatusSummary', 'ListAITagCandidatePage', 'ListSameSourceRelations',
  'MarkSameSourceRelationRead', 'PreviewExternally', 'RejectAITagCandidate', 'RejectAITagCandidatesByVideo',
  'RejectSameSourceRelation', 'RenameVideo', 'RetryAITagging',
].map(name => [name, vi.fn()])));
vi.mock('../../wailsjs/go/main/App', () => api);
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
  it('offers an explicit confirm action and removes the handled relation locally', async () => {
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
    expect(wrapper.find('.same-source-row').exists()).toBe(false);
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
    api.DeleteVideo.mockResolvedValueOnce();
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

    expect(api.DeleteVideo).toHaveBeenCalledWith(7, false);
    expect(wrapper.vm.sameSourceDeleteConfirm.show).toBe(false);
    expect(wrapper.find('.same-source-row').exists()).toBe(false);
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
      tagCandidate({ id: 1, suggested_name: '动作', normalized_name: '动作' }),
      tagCandidate({ id: 2, suggested_name: '打斗', normalized_name: '动作' }),
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
