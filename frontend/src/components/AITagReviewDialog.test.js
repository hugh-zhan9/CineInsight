import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'ApproveAITagCandidate', 'GetAITaggingStatusSummary', 'ListAITagCandidates', 'ListSameSourceRelations',
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
