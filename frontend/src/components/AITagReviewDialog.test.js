import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'ApproveAITagCandidate', 'ConfirmSameSourceRelation', 'DeleteVideo', 'GetAITaggingStatusSummary', 'ListAITagCandidates', 'ListSameSourceRelations',
  'MarkSameSourceRelationRead', 'PreviewExternally', 'RejectAITagCandidate', 'RejectAITagCandidatesByVideo',
  'RejectSameSourceRelation', 'RenameVideo', 'RetryAITagging',
].map(name => [name, vi.fn()])));
vi.mock('../../wailsjs/go/main/App', () => api);
vi.mock('./AddTagDialog.vue', () => ({ default: { template: '<div />' } }));
vi.mock('./AIQualityPanel.vue', () => ({ default: { template: '<div data-test="quality-panel">quality panel</div>' } }));
vi.mock('./FaceClusterReviewPanel.vue', () => ({ default: { template: '<div data-test="face-panel-stub">face panel</div>' } }));

import AITagReviewDialog from './AITagReviewDialog.vue';

beforeEach(() => {
  vi.clearAllMocks();
  api.GetAITaggingStatusSummary.mockResolvedValue({ config_available: true });
  api.ListAITagCandidates.mockResolvedValue([]);
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
    api.ListAITagCandidates.mockRejectedValueOnce(new Error('boom'));
    const wrapper = mount(AITagReviewDialog, { props: { visible: true } });
    await wrapper.vm.loadCandidates();
    await flushPromises();
    expect(wrapper.find('.ai-tag-review-error').exists()).toBe(true);

    await wrapper.find('[data-test="face-cluster-review-tab"]').trigger('click');
    expect(wrapper.find('[data-test="face-panel-stub"]').exists()).toBe(true);
    expect(wrapper.find('.ai-tag-review-error').exists()).toBe(false);
  });
});
