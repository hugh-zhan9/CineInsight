import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'ApproveImageAITagCandidate', 'ListImageAITagCandidates',
  'RejectImageAITagCandidate', 'RejectImageAITagCandidatesByImage',
].map(name => [name, vi.fn()])));
vi.mock('../../wailsjs/go/main/App', () => api);

import ImageAITagReviewPanel from './ImageAITagReviewPanel.vue';

const candidate = (overrides = {}) => ({
  id: 1,
  image_id: 10,
  image: { id: 10, name: 'beach.heic' },
  image_deleted: false,
  suggested_name: '海边',
  confidence: 'high',
  reasoning: '画面主体是海岸线',
  status: 'pending',
  ...overrides,
});

beforeEach(() => {
  vi.clearAllMocks();
  api.ListImageAITagCandidates.mockResolvedValue([]);
});

describe('ImageAITagReviewPanel', () => {
  it('groups candidates by image instead of listing them flat', async () => {
    api.ListImageAITagCandidates.mockResolvedValue([
      candidate({ id: 1, image_id: 10, suggested_name: '海边' }),
      candidate({ id: 2, image_id: 10, suggested_name: '日落', confidence: 'medium' }),
      candidate({ id: 3, image_id: 11, image: { id: 11, name: 'snow.heic' }, suggested_name: '雪山' }),
    ]);
    const wrapper = mount(ImageAITagReviewPanel, { props: { visible: true } });
    await flushPromises();

    const groups = wrapper.findAll('[data-test="image-ai-tag-group"]');
    expect(groups).toHaveLength(2);
    expect(groups[0].text()).toContain('beach.heic');
    expect(groups[0].text()).toContain('海边');
    expect(groups[0].text()).toContain('日落');
    expect(wrapper.find('[data-test="image-ai-tag-review-count"]').text()).toContain('待审 3 条');
  });

  it('shows an empty state instead of a blank panel', async () => {
    const wrapper = mount(ImageAITagReviewPanel, { props: { visible: true } });
    await flushPromises();
    expect(wrapper.find('[data-test="image-ai-tag-review-empty"]').exists()).toBe(true);
  });

  it('approves a candidate and reloads the pending list', async () => {
    api.ListImageAITagCandidates.mockResolvedValueOnce([candidate()]).mockResolvedValueOnce([]);
    api.ApproveImageAITagCandidate.mockResolvedValue({ id: 1, status: 'approved' });
    const wrapper = mount(ImageAITagReviewPanel, { props: { visible: true } });
    await flushPromises();

    await wrapper.find('[data-test="image-ai-tag-approve-1"]').trigger('click');
    await flushPromises();

    expect(api.ApproveImageAITagCandidate).toHaveBeenCalledWith(1);
    expect(wrapper.emitted('changed')).toHaveLength(1);
    expect(wrapper.find('[data-test="image-ai-tag-review-empty"]').exists()).toBe(true);
  });

  // 后端在图片已有手工标签时返回 superseded 而不是报错。用户点的是"接受"，
  // 界面必须解释标签为什么没挂上，否则看起来就是点了没反应。
  it('explains why nothing was tagged when the image already has manual tags', async () => {
    api.ListImageAITagCandidates.mockResolvedValueOnce([candidate()]).mockResolvedValueOnce([]);
    api.ApproveImageAITagCandidate.mockResolvedValue({ id: 1, status: 'superseded' });
    const wrapper = mount(ImageAITagReviewPanel, { props: { visible: true } });
    await flushPromises();

    await wrapper.find('[data-test="image-ai-tag-approve-1"]').trigger('click');
    await flushPromises();

    const notice = wrapper.find('[data-test="image-ai-tag-review-notice"]');
    expect(notice.exists()).toBe(true);
    expect(notice.text()).toContain('手工打的标签');
    // 是说明不是报错：不能占用 error 位，否则会被紧随其后的刷新清掉。
    expect(wrapper.find('[data-test="image-ai-tag-review-error"]').exists()).toBe(false);
  });

  it('rejects a single candidate and the whole image', async () => {
    api.ListImageAITagCandidates.mockResolvedValue([candidate(), candidate({ id: 2, suggested_name: '日落' })]);
    api.RejectImageAITagCandidate.mockResolvedValue();
    api.RejectImageAITagCandidatesByImage.mockResolvedValue(2);
    const wrapper = mount(ImageAITagReviewPanel, { props: { visible: true } });
    await flushPromises();

    await wrapper.find('[data-test="image-ai-tag-reject-1"]').trigger('click');
    await flushPromises();
    expect(api.RejectImageAITagCandidate).toHaveBeenCalledWith(1);

    await wrapper.find('[data-test="image-ai-tag-reject-all-10"]').trigger('click');
    await flushPromises();
    expect(api.RejectImageAITagCandidatesByImage).toHaveBeenCalledWith(10);
  });

  it('refuses to approve candidates whose image was deleted', async () => {
    api.ListImageAITagCandidates.mockResolvedValue([candidate({ image_deleted: true })]);
    const wrapper = mount(ImageAITagReviewPanel, { props: { visible: true } });
    await flushPromises();

    expect(wrapper.find('[data-test="image-ai-tag-deleted"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="image-ai-tag-approve-1"]').attributes('disabled')).toBeDefined();
  });

  it('passes the confidence filter through to the backend', async () => {
    const wrapper = mount(ImageAITagReviewPanel, { props: { visible: true } });
    await flushPromises();
    expect(api.ListImageAITagCandidates).toHaveBeenLastCalledWith(0, '', '');

    await wrapper.find('[data-test="image-ai-tag-confidence-high"]').trigger('click');
    await flushPromises();
    expect(api.ListImageAITagCandidates).toHaveBeenLastCalledWith(0, 'high', '');
  });

  it('surfaces load failures instead of silently showing an empty list', async () => {
    api.ListImageAITagCandidates.mockRejectedValue(new Error('boom'));
    const wrapper = mount(ImageAITagReviewPanel, { props: { visible: true } });
    await flushPromises();
    expect(wrapper.find('[data-test="image-ai-tag-review-error"]').text()).toContain('加载候选失败');
  });
});
