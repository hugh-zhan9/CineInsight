import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'ApproveAITagCandidate', 'ConfirmSameSourceRelation', 'GetAITaggingStatusSummary', 'ListAITagCandidates', 'ListSameSourceRelations',
  'MarkSameSourceRelationRead', 'PreviewExternally', 'RejectAITagCandidate', 'RejectAITagCandidatesByVideo',
  'RejectSameSourceRelation', 'RenameVideo', 'RetryAITagging',
].map(name => [name, vi.fn()])));
vi.mock('../../wailsjs/go/main/App', () => api);
vi.mock('./AddTagDialog.vue', () => ({ default: { template: '<div />' } }));
vi.mock('./AIQualityPanel.vue', () => ({ default: { template: '<div data-test="quality-panel">quality panel</div>' } }));

import AITagReviewDialog from './AITagReviewDialog.vue';

beforeEach(() => {
  vi.clearAllMocks();
  api.GetAITaggingStatusSummary.mockResolvedValue({ config_available: true });
  api.ListAITagCandidates.mockResolvedValue([]);
  api.ListSameSourceRelations.mockResolvedValue([]);
});

describe('AITagReviewDialog quality entry', () => {
  it('can hide the quality view independently', async () => {
    const wrapper = mount(AITagReviewDialog, { props: { visible: true, qualityEnabled: false } });
    await flushPromises();
    expect(wrapper.find('[data-test="ai-quality-tab"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="ai-review-tab"]').exists()).toBe(true);
  });

  it('opens quality as a separate read-only tab', async () => {
    const wrapper = mount(AITagReviewDialog, { props: { visible: true, qualityEnabled: true } });
    await flushPromises();
    await wrapper.find('[data-test="ai-quality-tab"]').trigger('click');
    expect(wrapper.find('[data-test="quality-panel"]').exists()).toBe(true);
    expect(wrapper.find('.ai-tag-review-actions').exists()).toBe(false);
  });
});

describe('AITagReviewDialog same-source review', () => {
  it('offers an explicit confirm action and removes the handled relation locally', async () => {
    api.ListSameSourceRelations.mockResolvedValueOnce([{
      id: 9,
      video_a_id: 1,
      video_a: { id: 1, name: 'A.mp4' },
      video_b_id: 2,
      video_b: { id: 2, name: 'B.mp4' },
      confidence: 'high',
      is_unread: false,
    }]);
    api.ConfirmSameSourceRelation.mockResolvedValueOnce();
    const wrapper = mount(AITagReviewDialog, { props: { visible: true } });
    await wrapper.vm.loadCandidates();
    await flushPromises();

    const confirmButton = wrapper.findAll('.same-source-row button').find(button => button.text() === '确认同源');
    expect(confirmButton).toBeTruthy();
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
});
