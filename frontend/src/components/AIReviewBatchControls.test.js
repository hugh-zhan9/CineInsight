import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
const api = vi.hoisted(() => Object.fromEntries(['PreviewAIReviewApproval', 'StartAIReviewApproval', 'GetAIReviewApproval', 'CancelAIReviewApproval'].map(name => [name, vi.fn()])));
const feedback = vi.hoisted(() => ({ confirmAction: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);
vi.mock('../utils/feedback.js', () => feedback);
import AIReviewBatchControls from './AIReviewBatchControls.vue';
enableAutoUnmount(afterEach);
afterEach(() => vi.useRealTimers());
const preview = { token: 'frozen', matched: 8, eligible: 5, excluded: 3, media_count: 4, link_count: 4 };
const state = (extra = {}) => ({ token: 'frozen', state: 'running', total: 5, processed: 0, succeeded: 0, skipped: 0, failed: 0, remaining: 5, results: [], next_after: 0, has_more: false, ...extra });
beforeEach(() => {
  vi.resetAllMocks();
  api.GetAIReviewApproval.mockResolvedValue({ state: 'idle', results: [] });
  api.PreviewAIReviewApproval.mockResolvedValue(preview);
  api.StartAIReviewApproval.mockResolvedValue(state());
  api.CancelAIReviewApproval.mockResolvedValue();
  feedback.confirmAction.mockResolvedValue(true);
});
describe('AI review frozen confirmation', () => {
  it('shows authoritative counts and starts only the confirmed token despite later filter changes', async () => {
    let confirm;
    feedback.confirmAction.mockImplementationOnce(() => new Promise(resolve => { confirm = resolve; }));
    const wrapper = mount(AIReviewBatchControls, { props: { kind: 'image', query: { keyword: '海边', confidence: 'high' } } });
    await flushPromises();
    const request = wrapper.vm.previewFiltered(); await flushPromises();
    expect(feedback.confirmAction).toHaveBeenCalledWith(expect.objectContaining({ message: expect.stringContaining('匹配 8 条，其中可批准 5 条') }));
    expect(feedback.confirmAction.mock.calls[0][0].message).toContain('排除 3 条');
    await wrapper.setProps({ query: { keyword: '雪山' } });
    confirm(true); await request;
    expect(api.PreviewAIReviewApproval).toHaveBeenCalledWith('image', { scope: 'filtered', ids: [], filter: { keyword: '海边', confidence: 'high' } });
    expect(api.StartAIReviewApproval).toHaveBeenCalledWith('image', 'frozen');
    expect(api.PreviewAIReviewApproval).toHaveBeenCalledTimes(1);
  });
  it('releases a preview arriving after close and never starts it', async () => {
    vi.useFakeTimers();
    let resolvePreview;
    api.PreviewAIReviewApproval.mockImplementationOnce(() => new Promise(resolve => { resolvePreview = resolve; }));
    const wrapper = mount(AIReviewBatchControls, { props: { kind: 'video' } }); await flushPromises();
    const request = wrapper.vm.previewLoaded([10, 20]);
    await wrapper.setProps({ visible: false });
    resolvePreview(preview); await request;
    expect(api.CancelAIReviewApproval).toHaveBeenCalledWith('video', 'frozen');
    expect(feedback.confirmAction).not.toHaveBeenCalled();
    expect(api.StartAIReviewApproval).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(5000);
    expect(api.GetAIReviewApproval).toHaveBeenCalledTimes(1);
  });
  it('does not rebuild or retry an expired approval automatically', async () => {
    vi.useFakeTimers();
    api.StartAIReviewApproval.mockRejectedValueOnce(new Error('review_batch_expired'));
    const wrapper = mount(AIReviewBatchControls, { props: { kind: 'video' } }); await flushPromises();
    await wrapper.vm.previewLoaded([3]);
    await vi.advanceTimersByTimeAsync(20000);
    expect(api.PreviewAIReviewApproval).toHaveBeenCalledTimes(1);
    expect(api.StartAIReviewApproval).toHaveBeenCalledTimes(1);
    expect(wrapper.get('[data-test="review-batch-error"]').text()).toContain('review_batch_expired');
  });
});
describe('AI review progress lifecycle', () => {
  it('drains compact result pages and settles once without treating cancellation as completion', async () => {
    vi.useFakeTimers();
    api.GetAIReviewApproval.mockResolvedValueOnce(state({ state: 'cancelled', processed: 2, succeeded: 1, skipped: 1, remaining: 3, results: [{ id: 1, state: 'approved' }], next_after: 1, has_more: true }))
      .mockResolvedValueOnce(state({ state: 'cancelled', processed: 2, succeeded: 1, skipped: 1, remaining: 3, results: [{ id: 2, state: 'skipped', code: 'changed' }], next_after: 2 }));
    const wrapper = mount(AIReviewBatchControls, { props: { kind: 'video' } }); await flushPromises();
    await vi.advanceTimersByTimeAsync(1); await flushPromises();
    expect(api.GetAIReviewApproval).toHaveBeenLastCalledWith('video', 'frozen', 1, 200);
    expect(wrapper.emitted('outcomes')).toHaveLength(2);
    expect(wrapper.emitted('settled')).toHaveLength(1);
    expect(wrapper.get('[data-test="review-batch-summary"]').text()).toContain('未处理 3');
    expect(wrapper.text()).toContain('已取消剩余操作');
  });
  it('drops an older status response after cancellation and stops polling while hidden', async () => {
    vi.useFakeTimers();
    let resolveOld;
    api.GetAIReviewApproval.mockResolvedValueOnce(state()).mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve; }))
      .mockResolvedValueOnce(state({ state: 'cancelled', processed: 1, succeeded: 1, remaining: 4, next_after: 1, results: [{ id: 1, state: 'approved' }] }));
    const wrapper = mount(AIReviewBatchControls, { props: { kind: 'video' } }); await flushPromises();
    await vi.advanceTimersByTimeAsync(1000);
    await wrapper.get('[data-test="review-batch-cancel"]').trigger('click'); await flushPromises();
    resolveOld(state()); await flushPromises();
    expect(wrapper.vm.state.state).toBe('cancelled');
    expect(wrapper.vm.after).toBe(1);
    expect(wrapper.emitted('outcomes')).toHaveLength(1);
    expect(api.CancelAIReviewApproval).toHaveBeenCalledWith('video', 'frozen');
    await wrapper.setProps({ visible: false }); await vi.advanceTimersByTimeAsync(5000);
    expect(api.GetAIReviewApproval).toHaveBeenCalledTimes(3);
  });
});
