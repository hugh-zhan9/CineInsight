import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'ApproveImageAITagCandidate', 'ListImageAITagCandidatePage',
  'RejectImageAITagCandidate', 'RejectImageAITagCandidatesByImage',
].map(name => [name, vi.fn()])));
vi.mock('../../wailsjs/go/main/App', () => api);
vi.mock('./FaceClusterReviewPanel.vue', () => ({ default: { template: '<div data-test="face-panel-stub">face panel</div>' } }));

import ImageAITagReviewPanel from './ImageAITagReviewPanel.vue';

// 后端按 id 游标分页：next_id 只在这一页满员时出现。
const page = (items, nextID = 0) => ({ items, next_id: nextID });

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
  api.ListImageAITagCandidatePage.mockResolvedValue(page([]));
});

describe('ImageAITagReviewPanel', () => {
  it('groups candidates by image instead of listing them flat', async () => {
    api.ListImageAITagCandidatePage.mockResolvedValue(page([
      candidate({ id: 1, image_id: 10, suggested_name: '海边' }),
      candidate({ id: 2, image_id: 10, suggested_name: '日落', confidence: 'medium' }),
      candidate({ id: 3, image_id: 11, image: { id: 11, name: 'snow.heic' }, suggested_name: '雪山' }),
    ]));
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
    api.ListImageAITagCandidatePage.mockResolvedValue(page([candidate()]));
    api.ApproveImageAITagCandidate.mockResolvedValue({ id: 1, status: 'approved' });
    const wrapper = mount(ImageAITagReviewPanel, { props: { visible: true } });
    await flushPromises();

    await wrapper.find('[data-test="image-ai-tag-approve-1"]').trigger('click');
    await flushPromises();

    expect(api.ApproveImageAITagCandidate).toHaveBeenCalledWith(1);
    expect(wrapper.emitted('changed')).toHaveLength(1);
    expect(wrapper.find('[data-test="image-ai-tag-review-empty"]').exists()).toBe(true);
    // 局部移除，不再整表重拉：分页之后重拉会把已翻出来的页丢掉。
    expect(api.ListImageAITagCandidatePage).toHaveBeenCalledTimes(1);
  });

  // 后端在图片已有手工标签时返回 superseded 而不是报错。用户点的是"接受"，
  // 界面必须解释标签为什么没挂上，否则看起来就是点了没反应。
  it('explains why nothing was tagged when the image already has manual tags', async () => {
    api.ListImageAITagCandidatePage.mockResolvedValue(page([candidate()]));
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
    api.ListImageAITagCandidatePage.mockResolvedValue(page([candidate(), candidate({ id: 2, suggested_name: '日落' })]));
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

  // 审阅图片标签时文件名判断不了对错，缩略图是证据；生成失败也不能留一个破图。
  it('shows a thumbnail per image group and falls back locally when it fails', async () => {
    api.ListImageAITagCandidatePage.mockResolvedValue(page([
      candidate({ id: 1, image_id: 10 }),
      candidate({ id: 3, image_id: 11, image: { id: 11, name: 'snow.heic' }, suggested_name: '雪山' }),
    ]));
    const wrapper = mount(ImageAITagReviewPanel, { props: { visible: true } });
    await flushPromises();

    const thumb = wrapper.find('[data-test="image-ai-tag-thumb-10"]');
    expect(thumb.attributes('src')).toBe('/preview/image-thumbnail/10');
    expect(thumb.attributes('loading')).toBe('lazy');
    expect(wrapper.find('[data-test="image-ai-tag-thumb-11"]').exists()).toBe(true);

    await thumb.trigger('error');
    expect(wrapper.find('[data-test="image-ai-tag-thumb-10"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="image-ai-tag-thumb-fallback-10"]').exists()).toBe(true);
    // 另一张图的缩略图不受这一张失败影响。
    expect(wrapper.find('[data-test="image-ai-tag-thumb-11"]').exists()).toBe(true);
  });

  // 一页候选也可能几十条：滚动条必须在弹窗内部的内容区，弹窗本身封高。
  it('keeps the candidate list inside its own scroll area', async () => {
    api.ListImageAITagCandidatePage.mockResolvedValue(page([candidate()]));
    const wrapper = mount(ImageAITagReviewPanel, { props: { visible: true } });
    await flushPromises();

    const scrollArea = wrapper.find('[data-test="image-ai-tag-review-scroll-area"]');
    expect(scrollArea.exists()).toBe(true);
    expect(scrollArea.find('[data-test="image-ai-tag-group"]').exists()).toBe(true);
    // 报错与说明留在滚动区外，滚到列表深处也看得见。
    expect(scrollArea.find('[data-test="image-ai-tag-review-count"]').exists()).toBe(false);
  });

  it('refuses to approve candidates whose image was deleted', async () => {
    api.ListImageAITagCandidatePage.mockResolvedValue(page([candidate({ image_deleted: true })]));
    const wrapper = mount(ImageAITagReviewPanel, { props: { visible: true } });
    await flushPromises();

    expect(wrapper.find('[data-test="image-ai-tag-deleted"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="image-ai-tag-approve-1"]').attributes('disabled')).toBeDefined();
  });

  it('passes the confidence filter through to the backend', async () => {
    const wrapper = mount(ImageAITagReviewPanel, { props: { visible: true } });
    await flushPromises();
    expect(api.ListImageAITagCandidatePage).toHaveBeenLastCalledWith(0, '', '', 0, 0);

    await wrapper.find('[data-test="image-ai-tag-confidence-high"]').trigger('click');
    await flushPromises();
    expect(api.ListImageAITagCandidatePage).toHaveBeenLastCalledWith(0, 'high', '', 0, 0);
  });

  it('surfaces load failures instead of silently showing an empty list', async () => {
    api.ListImageAITagCandidatePage.mockRejectedValue(new Error('boom'));
    const wrapper = mount(ImageAITagReviewPanel, { props: { visible: true } });
    await flushPromises();
    expect(wrapper.find('[data-test="image-ai-tag-review-error"]').text()).toContain('加载候选失败');
  });
});

// 原来一次全量拉回上千条：既压 IPC 又要一次渲染上千行。
// 现在按 id 游标翻页，"加载更多"追加到同一个列表里，同图的候选仍然并到一组。
describe('ImageAITagReviewPanel pagination', () => {
  it('loads the first page and appends the next one on demand', async () => {
    api.ListImageAITagCandidatePage
      .mockResolvedValueOnce(page([candidate({ id: 9, image_id: 10 })], 9))
      .mockResolvedValueOnce(page([
        candidate({ id: 8, image_id: 10, suggested_name: '日落' }),
        candidate({ id: 7, image_id: 11, image: { id: 11, name: 'snow.heic' }, suggested_name: '雪山' }),
      ]));
    const wrapper = mount(ImageAITagReviewPanel, { props: { visible: true } });
    await flushPromises();

    expect(api.ListImageAITagCandidatePage).toHaveBeenLastCalledWith(0, '', '', 0, 0);
    expect(wrapper.findAll('[data-test="image-ai-tag-group"]')).toHaveLength(1);
    expect(wrapper.find('[data-test="image-ai-tag-review-count"]').text()).toContain('还有更多未加载');

    await wrapper.find('[data-test="image-ai-tag-load-more"]').trigger('click');
    await flushPromises();

    // 续页用上一页最后一条的 id 作游标。
    expect(api.ListImageAITagCandidatePage).toHaveBeenLastCalledWith(0, '', '', 9, 0);
    const groups = wrapper.findAll('[data-test="image-ai-tag-group"]');
    expect(groups).toHaveLength(2);
    expect(groups[0].text()).toContain('海边');
    expect(groups[0].text()).toContain('日落');
    // 末页没有游标，按钮随之消失。
    expect(wrapper.find('[data-test="image-ai-tag-load-more"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="image-ai-tag-review-count"]').text()).not.toContain('还有更多未加载');
  });

  it('pulls the next page when local removal empties the loaded one', async () => {
    api.ListImageAITagCandidatePage
      .mockResolvedValueOnce(page([candidate({ id: 9, image_id: 10 })], 9))
      .mockResolvedValueOnce(page([candidate({ id: 8, image_id: 11, image: { id: 11, name: 'snow.heic' }, suggested_name: '雪山' })]));
    api.RejectImageAITagCandidate.mockResolvedValue();
    const wrapper = mount(ImageAITagReviewPanel, { props: { visible: true } });
    await flushPromises();

    await wrapper.find('[data-test="image-ai-tag-reject-9"]').trigger('click');
    await flushPromises();

    // 拒绝掉当页唯一一条后不能停在假空态上。
    expect(api.ListImageAITagCandidatePage).toHaveBeenLastCalledWith(0, '', '', 9, 0);
    expect(wrapper.find('[data-test="image-ai-tag-review-empty"]').exists()).toBe(false);
    expect(wrapper.findAll('[data-test="image-ai-tag-group"]')).toHaveLength(1);
    expect(wrapper.text()).toContain('雪山');
  });

  // 切换筛选时旧筛选的下一页可能才刚回来：追加它等于把被筛掉的候选混进列表。
  it('drops an in-flight page when the confidence filter changed', async () => {
    let resolveStalePage;
    api.ListImageAITagCandidatePage
      .mockResolvedValueOnce(page([candidate({ id: 9, image_id: 10 })], 9))
      .mockImplementationOnce(() => new Promise(resolve => { resolveStalePage = resolve; }))
      .mockResolvedValueOnce(page([candidate({
        id: 5, image_id: 12, image: { id: 12, name: 'high.heic' }, suggested_name: '高置信',
      })]));
    const wrapper = mount(ImageAITagReviewPanel, { props: { visible: true } });
    await flushPromises();

    await wrapper.find('[data-test="image-ai-tag-load-more"]').trigger('click');
    await wrapper.find('[data-test="image-ai-tag-confidence-high"]').trigger('click');
    await flushPromises();
    expect(wrapper.vm.candidates.map(item => item.id)).toEqual([5]);

    resolveStalePage(page([candidate({ id: 8, image_id: 10, suggested_name: '日落' })], 8));
    await flushPromises();

    expect(wrapper.vm.candidates.map(item => item.id)).toEqual([5]);
    expect(wrapper.vm.cursor).toBe(0);
    expect(wrapper.find('[data-test="image-ai-tag-load-more"]').exists()).toBe(false);
  });

  // 首页在途时点"加载更多"，用的是上一批查询的游标：必须挡住，否则会跳过一整段候选。
  it('refuses to page while a first-page load is in flight', async () => {
    api.ListImageAITagCandidatePage
      .mockResolvedValueOnce(page([candidate({ id: 100, image_id: 10 })], 99))
      .mockImplementationOnce(() => new Promise(() => {}));
    const wrapper = mount(ImageAITagReviewPanel, { props: { visible: true } });
    await flushPromises();

    await wrapper.find('[data-test="image-ai-tag-confidence-high"]').trigger('click');
    expect(wrapper.find('[data-test="image-ai-tag-load-more"]').attributes('disabled')).toBeDefined();

    await wrapper.vm.loadMore();
    await flushPromises();
    // 只有"首屏 + 切筛选"这两次请求，没有第三次带旧游标的翻页。
    expect(api.ListImageAITagCandidatePage).toHaveBeenCalledTimes(2);
  });

  // 动作改成局部移除之后，面板得留一个重新取数的入口，否则后台新产出的候选永远看不到。
  it('keeps an explicit refresh entry', async () => {
    api.ListImageAITagCandidatePage
      .mockResolvedValueOnce(page([candidate({ id: 1, image_id: 10 })]))
      .mockResolvedValueOnce(page([
        candidate({ id: 1, image_id: 10 }),
        candidate({ id: 2, image_id: 11, image: { id: 11, name: 'snow.heic' }, suggested_name: '雪山' }),
      ]));
    const wrapper = mount(ImageAITagReviewPanel, { props: { visible: true } });
    await flushPromises();
    expect(wrapper.findAll('[data-test="image-ai-tag-group"]')).toHaveLength(1);

    await wrapper.find('[data-test="image-ai-tag-review-refresh"]').trigger('click');
    await flushPromises();

    expect(api.ListImageAITagCandidatePage).toHaveBeenLastCalledWith(0, '', '', 0, 0);
    expect(wrapper.findAll('[data-test="image-ai-tag-group"]')).toHaveLength(2);
  });

  // 自动补页失败时列表是空的、但后端还有下一页：这时候说"没有待审候选"是假的。
  it('does not claim an empty pool while a next page is still pending', async () => {
    api.ListImageAITagCandidatePage
      .mockResolvedValueOnce(page([candidate({ id: 9, image_id: 10 })], 9))
      .mockRejectedValueOnce(new Error('boom'));
    api.RejectImageAITagCandidate.mockResolvedValue();
    const wrapper = mount(ImageAITagReviewPanel, { props: { visible: true } });
    await flushPromises();

    await wrapper.find('[data-test="image-ai-tag-reject-9"]').trigger('click');
    await flushPromises();

    expect(wrapper.find('[data-test="image-ai-tag-review-empty"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="image-ai-tag-review-error"]').text()).toContain('加载更多候选失败');
    expect(wrapper.find('[data-test="image-ai-tag-load-more"]').exists()).toBe(true);
  });

  it('drops the whole image locally when every candidate of it is rejected', async () => {
    api.ListImageAITagCandidatePage.mockResolvedValue(page([
      candidate({ id: 1, image_id: 10 }),
      candidate({ id: 2, image_id: 10, suggested_name: '日落' }),
      candidate({ id: 3, image_id: 11, image: { id: 11, name: 'snow.heic' }, suggested_name: '雪山' }),
    ]));
    api.RejectImageAITagCandidatesByImage.mockResolvedValue(2);
    const wrapper = mount(ImageAITagReviewPanel, { props: { visible: true } });
    await flushPromises();

    await wrapper.find('[data-test="image-ai-tag-reject-all-10"]').trigger('click');
    await flushPromises();

    const groups = wrapper.findAll('[data-test="image-ai-tag-group"]');
    expect(groups).toHaveLength(1);
    expect(groups[0].text()).toContain('snow.heic');
    expect(api.ListImageAITagCandidatePage).toHaveBeenCalledTimes(1);
  });
});

// P-013：图片侧复用视频侧那一个人物候选面板，不复制一份。
describe('ImageAITagReviewPanel face cluster section', () => {
  it('offers the shared face candidate panel as a second section', async () => {
    api.ListImageAITagCandidatePage.mockResolvedValue(page([]));
    const wrapper = mount(ImageAITagReviewPanel, { props: { visible: true } });
    await flushPromises();
    expect(wrapper.find('[data-test="face-panel-stub"]').exists()).toBe(false);

    await wrapper.find('[data-test="image-face-cluster-review-tab"]').trigger('click');
    expect(wrapper.find('[data-test="face-panel-stub"]').exists()).toBe(true);
    // 人物候选与标签候选是两批数据，标签侧的筛选与空态都不该出现在这一页。
    expect(wrapper.find('[data-test="image-ai-tag-confidence-high"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="image-ai-tag-review-empty"]').exists()).toBe(false);

    await wrapper.find('[data-test="image-ai-tag-review-tab"]').trigger('click');
    expect(wrapper.find('[data-test="face-panel-stub"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="image-ai-tag-review-empty"]').exists()).toBe(true);
  });
});
