import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils';
import { reactive } from 'vue';
const api = vi.hoisted(() => ({ GetStorageGoalVolumes: vi.fn(), GetStorageGoalPlan: vi.fn() }));
const review = vi.hoisted(() => ({ readCleanupReview: vi.fn(), handoffCleanupReview: vi.fn() }));
vi.mock('../../../wailsjs/go/main/App', () => api);
vi.mock('../../utils/cleanupReviewAccess.js', () => review);
import StorageGoalPanel from './StorageGoalPanel.vue';
enableAutoUnmount(afterEach);
const state = reactive({ video: null, image: null });
const plan = () => ({ count: 2, potential_bytes: 30e9, shortfall_bytes: 70e9, notices: ['废纸篓不是已释放空间'], items: [{ id: 2, kind: 'video', title: 'duplicate', reason: '精确重复', reason_kind: 'exact', keeper_title: 'keep.mp4', bytes: 20e9, suggested: true }, { id: 3, kind: 'video', title: 'old file', reason: '久未观看', reason_kind: 'old', bytes: 10e9, suggested: false }] });
beforeEach(() => {
  state.video = { analysis_key: 'batch', revision: 'revision-1', locked_ids: [1], candidates: [{ id: 2, kind: 'exact', keeper_id: 1 }] }; state.image = null;
  review.readCleanupReview.mockReset().mockImplementation(kind => state[kind]); review.handoffCleanupReview.mockReset();
  api.GetStorageGoalVolumes.mockReset().mockResolvedValue({ epoch: 'library-1', sampled_at: '2026-11-04T00:00:00Z', items: [{ id: 'disk-a', label: '媒体盘', available_bytes: 100e9, media_bytes: 500e9, count: 100 }] });
  api.GetStorageGoalPlan.mockReset().mockResolvedValue(plan());
});

describe('goal-based storage review', () => {
  it('requests a target in decimal GB and only selects exact duplicates by default', async () => {
    const wrapper = mount(StorageGoalPanel); await flushPromises();
    await wrapper.get('form').trigger('submit'); await flushPromises();
    expect(api.GetStorageGoalPlan).toHaveBeenLastCalledWith({ epoch: 'library-1', volume_id: 'disk-a', target_bytes: 100e9, video_review: { analysis_key: 'batch', locked_ids: [1], candidates: [{ id: 2, kind: 'exact', keeper_id: 1 }] }, image_review: null });
    expect(wrapper.get('[data-test="storage-goal-summary"]').text()).toContain('70.00 GB');
    expect(wrapper.vm.selection).toEqual(new Set(['video:2']));
    expect(wrapper.emitted('run-command')).toBeUndefined(); expect(review.handoffCleanupReview).not.toHaveBeenCalled();
    await wrapper.findAll('[data-test="storage-goal-row"] input')[1].setValue(true);
    await wrapper.get('[data-test="storage-goal-review-video"]').trigger('click');
    expect(review.handoffCleanupReview).toHaveBeenCalledWith('video', 'revision-1', [2]);
    expect(wrapper.emitted('run-command')[0]).toEqual(['library.openCleanup', '视频清理审阅']);
  });
  it('invalidates a proposal when keeper protections change', async () => {
    const wrapper = mount(StorageGoalPanel); await flushPromises(); await wrapper.get('form').trigger('submit'); await flushPromises();
    state.video = { ...state.video, revision: 'revision-2', locked_ids: [1, 2] }; await flushPromises();
    expect(wrapper.find('[data-test="storage-goal-row"]').exists()).toBe(false); expect(wrapper.text()).toContain('保留项或分析条件已变化');
  });
  it('ignores late results after protection changes during a query and rejects invalid targets', async () => {
    let resolve; api.GetStorageGoalPlan.mockImplementationOnce(() => new Promise(done => { resolve = done; }));
    const wrapper = mount(StorageGoalPanel); await flushPromises(); await wrapper.get('form').trigger('submit');
    state.video = { ...state.video, revision: 'new-protection' }; await flushPromises(); resolve(plan()); await flushPromises();
    expect(wrapper.find('[data-test="storage-goal-row"]').exists()).toBe(false);
    await wrapper.get('[data-test="storage-goal-gb"]').setValue(-1); await wrapper.get('form').trigger('submit');
    expect(api.GetStorageGoalPlan).toHaveBeenCalledTimes(1); expect(wrapper.text()).toContain('请输入有效');
  });
});
