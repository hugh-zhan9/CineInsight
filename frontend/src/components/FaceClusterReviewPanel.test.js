import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'ConfirmFaceClusterAppend', 'DismissFaceClusterAppend', 'IgnoreFaceCluster',
  'LinkFaceCluster', 'ListFaceClusters', 'ListPeople', 'NameFaceCluster',
].map(name => [name, vi.fn()])));
vi.mock('../../wailsjs/go/main/App', () => api);

import FaceClusterReviewPanel from './FaceClusterReviewPanel.vue';

const cluster = (overrides = {}) => ({
  id: 1,
  status: 'unnamed',
  observation_count: 4,
  video_count: 1,
  image_count: 2,
  representative_observation_id: 77,
  person_id: 0,
  person_name: '',
  candidates: [],
  append_pending_count: 0,
  append_pending_media: [],
  ...overrides,
});

// 面板一次加载会分别拉未命名簇与已命名簇（已命名的只在有追加候选时才显示）。
const setClusters = ({ unnamed = [], named = [] } = {}) => {
  api.ListFaceClusters.mockImplementation(async (filter) => (filter?.status === 'named' ? named : unnamed));
};

let handlers = {};

beforeEach(() => {
  vi.clearAllMocks();
  handlers = {};
  window.runtime = {
    EventsOn: (name, callback) => {
      handlers[name] = callback;
      return () => { delete handlers[name]; };
    },
  };
  setClusters();
  api.ListPeople.mockResolvedValue([]);
});

afterEach(() => {
  delete window.runtime;
});

describe('FaceClusterReviewPanel', () => {
  it('shows an empty state instead of a blank panel', async () => {
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();
    expect(wrapper.find('[data-test="face-cluster-review-empty"]').exists()).toBe(true);
  });

  it('renders the representative crop, media counts and person candidates', async () => {
    setClusters({
      unnamed: [cluster({
        candidates: [{ person_id: 5, display_name: '周迅', similarity: 0.83 }],
      })],
    });
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();

    const card = wrapper.find('[data-test="face-cluster-card-1"]');
    expect(card.exists()).toBe(true);
    expect(card.find('img').attributes('src')).toBe('/preview/face-crop/77');
    expect(card.text()).toContain('4 次出现');
    expect(card.text()).toContain('1 个视频');
    expect(card.text()).toContain('2 张图片');
    const candidate = wrapper.find('[data-test="face-cluster-candidate-1-5"]');
    expect(candidate.text()).toContain('可能是 周迅');
    expect(candidate.text()).toContain('83%');
  });

  // 已命名但没有待确认追加的簇不该占面板位置：那上面没有任何要做的动作。
  it('hides named clusters without pending appends', async () => {
    setClusters({ named: [cluster({ id: 2, status: 'named', person_id: 5, person_name: '周迅' })] });
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();
    expect(wrapper.find('[data-test="face-cluster-card-2"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="face-cluster-review-empty"]').exists()).toBe(true);
  });

  it('names a cluster as a new person', async () => {
    setClusters({ unnamed: [cluster()] });
    api.NameFaceCluster.mockResolvedValue({ id: 1, status: 'named' });
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();

    await wrapper.find('[data-test="face-cluster-name-open-1"]').trigger('click');
    await wrapper.find('[data-test="face-cluster-name-display"]').setValue('周迅');
    await wrapper.find('[data-test="face-cluster-name-original"]').setValue('Zhou Xun');
    setClusters();
    await wrapper.find('[data-test="face-cluster-name-submit"]').trigger('click');
    await flushPromises();

    expect(api.NameFaceCluster).toHaveBeenCalledWith(1, '周迅', 'Zhou Xun');
    expect(wrapper.emitted('changed')).toHaveLength(1);
    expect(wrapper.find('[data-test="face-cluster-review-notice"]').text()).toContain('周迅');
    // 动作后重新拉一次列表，处理过的簇随之消失。
    expect(wrapper.find('[data-test="face-cluster-card-1"]').exists()).toBe(false);
  });

  it('links a cluster to an existing person with the candidate on top', async () => {
    setClusters({
      unnamed: [cluster({ candidates: [{ person_id: 5, display_name: '周迅', similarity: 0.9 }] })],
    });
    api.ListPeople.mockResolvedValue([
      { person: { id: 9, display_name: '汤唯' } },
    ]);
    api.LinkFaceCluster.mockResolvedValue({ id: 1, status: 'named' });
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();

    await wrapper.find('[data-test="face-cluster-link-open-1"]').trigger('click');
    await flushPromises();
    expect(api.ListPeople).toHaveBeenCalledWith('', '', 0, 20);
    const options = wrapper.findAll('[data-test="face-cluster-link-options"] [role="option"]');
    expect(options).toHaveLength(2);
    // 候选人物置顶，搜索结果排在后面。
    expect(options[0].text()).toContain('周迅');
    expect(options[1].text()).toContain('汤唯');

    await options[0].trigger('mousedown');
    await flushPromises();
    expect(api.LinkFaceCluster).toHaveBeenCalledWith(1, 5);
    expect(wrapper.find('[data-test="face-cluster-review-notice"]').text()).toContain('周迅');
  });

  it('ignores a cluster', async () => {
    setClusters({ unnamed: [cluster()] });
    api.IgnoreFaceCluster.mockResolvedValue();
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();

    await wrapper.find('[data-test="face-cluster-ignore-1"]').trigger('click');
    await flushPromises();
    expect(api.IgnoreFaceCluster).toHaveBeenCalledWith(1);
    expect(wrapper.find('[data-test="face-cluster-review-notice"]').text()).toContain('忽略');
  });

  it('confirms and dismisses append candidates on a named cluster', async () => {
    const named = cluster({
      id: 3,
      status: 'named',
      person_id: 5,
      person_name: '周迅',
      append_pending_count: 2,
      append_pending_media: [
        { media_kind: 'image', media_id: 11, name: 'b.jpg' },
        { media_kind: 'video', media_id: 12, name: 'movie.mp4' },
      ],
    });
    setClusters({ named: [named] });
    api.ConfirmFaceClusterAppend.mockResolvedValue();
    api.DismissFaceClusterAppend.mockResolvedValue();
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();

    const card = wrapper.find('[data-test="face-cluster-card-3"]');
    expect(card.text()).toContain('周迅');
    expect(wrapper.find('[data-test="face-cluster-append-summary-3"]').text()).toContain('b.jpg');
    expect(wrapper.find('[data-test="face-cluster-append-summary-3"]').text()).toContain('movie.mp4');
    // 已命名簇上不该出现命名/忽略这类未命名簇的动作。
    expect(wrapper.find('[data-test="face-cluster-name-open-3"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="face-cluster-ignore-3"]').exists()).toBe(false);

    await wrapper.find('[data-test="face-cluster-append-confirm-3"]').trigger('click');
    await flushPromises();
    expect(api.ConfirmFaceClusterAppend).toHaveBeenCalledWith(3);

    await wrapper.find('[data-test="face-cluster-append-dismiss-3"]').trigger('click');
    await flushPromises();
    expect(api.DismissFaceClusterAppend).toHaveBeenCalledWith(3);
  });

  // 两个人（或两个窗口）同时命名同一个簇：第二次拿到 cluster_not_unnamed，
  // 界面要说清楚并把过期的卡片换掉，而不是留在那儿等着再点一次。
  it('explains a naming conflict and refreshes the stale card', async () => {
    setClusters({ unnamed: [cluster()] });
    api.NameFaceCluster.mockRejectedValue('cluster_not_unnamed');
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();

    await wrapper.find('[data-test="face-cluster-name-open-1"]').trigger('click');
    await wrapper.find('[data-test="face-cluster-name-display"]').setValue('周迅');
    setClusters();
    await wrapper.find('[data-test="face-cluster-name-submit"]').trigger('click');
    await flushPromises();

    expect(wrapper.find('[data-test="face-cluster-review-error"]').text()).toContain('已经被命名或忽略过了');
    expect(wrapper.find('[data-test="face-cluster-card-1"]').exists()).toBe(false);
    expect(api.ListFaceClusters.mock.calls.length).toBeGreaterThan(2);
  });

  it('surfaces the backend message for unmapped failures', async () => {
    setClusters({ unnamed: [cluster()] });
    api.IgnoreFaceCluster.mockRejectedValue(new Error('database is locked'));
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();

    await wrapper.find('[data-test="face-cluster-ignore-1"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-test="face-cluster-review-error"]').text()).toContain('database is locked');
    // 失败不该把卡片吃掉。
    expect(wrapper.find('[data-test="face-cluster-card-1"]').exists()).toBe(true);
  });

  // 事件驱动的局部刷新：不显示整页加载态，用户正在看的卡片直接被换成新数据。
  it('refreshes in place when review data or analysis state changes', async () => {
    setClusters({ unnamed: [cluster()] });
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();
    expect(wrapper.find('[data-test="face-cluster-card-1"]').exists()).toBe(true);

    setClusters({ unnamed: [cluster({ id: 4, observation_count: 9 })] });
    handlers['face-review-changed']();
    await flushPromises();
    expect(wrapper.text()).not.toContain('正在加载人物候选');
    expect(wrapper.find('[data-test="face-cluster-card-4"]').exists()).toBe(true);

    setClusters({ unnamed: [cluster({ id: 6 })] });
    handlers['face-analysis-state']({ running: true });
    await flushPromises();
    // 进度事件不刷新，跑完才刷。
    expect(wrapper.find('[data-test="face-cluster-card-6"]').exists()).toBe(false);
    handlers['face-analysis-state']({ running: false, completed: true });
    await flushPromises();
    expect(wrapper.find('[data-test="face-cluster-card-6"]').exists()).toBe(true);
  });

  it('keeps existing cards when a refresh fails', async () => {
    setClusters({ unnamed: [cluster()] });
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();

    api.ListFaceClusters.mockRejectedValue(new Error('boom'));
    handlers['face-review-changed']();
    await flushPromises();
    expect(wrapper.find('[data-test="face-cluster-review-error"]').text()).toContain('加载人物候选失败');
    expect(wrapper.find('[data-test="face-cluster-card-1"]').exists()).toBe(true);
  });
});

// 簇的数量没有上限：大库跑完人脸分析可能有成千上万个未命名簇，人物页顶部常驻这个面板后
// 一次全渲染会把整页卡死。这里只渲染前一页，其余靠「显示更多」放出；回报宿主的计数仍是全部。
describe('FaceClusterReviewPanel 卡片分批渲染', () => {
  const many = (count, firstID = 1) => Array.from({ length: count }, (_, index) => cluster({
    id: firstID + index,
    representative_observation_id: 1000 + firstID + index,
  }));

  it('只渲染前 20 张卡片，计数按全部簇回报，「显示更多」逐页放出', async () => {
    setClusters({ unnamed: many(45), named: [cluster({ id: 99, status: 'named', person_id: 5, person_name: '周迅', append_pending_count: 1 })] });
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();

    expect(wrapper.findAll('.face-review__card')).toHaveLength(20);
    expect(wrapper.emitted('loaded')[0][0]).toEqual({ unnamed: 45, appendPending: 1 });
    const more = wrapper.get('[data-test="face-cluster-show-more"]');
    expect(more.text()).toContain('还有 26 组');

    await more.trigger('click');
    expect(wrapper.findAll('.face-review__card')).toHaveLength(40);
    await wrapper.get('[data-test="face-cluster-show-more"]').trigger('click');
    expect(wrapper.findAll('.face-review__card')).toHaveLength(46);
    // 未命名簇排在追加候选前面，最后一张才是已命名簇的追加候选。
    expect(wrapper.findAll('.face-review__card').at(-1).attributes('data-test')).toBe('face-cluster-card-99');
    expect(wrapper.find('[data-test="face-cluster-show-more"]').exists()).toBe(false);
  });

  it('不足一页时没有「显示更多」', async () => {
    setClusters({ unnamed: many(20) });
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();
    expect(wrapper.findAll('.face-review__card')).toHaveLength(20);
    expect(wrapper.find('[data-test="face-cluster-show-more"]').exists()).toBe(false);
  });

  it('动作后重拉不把展开过的窗口缩回去', async () => {
    setClusters({ unnamed: many(45) });
    api.IgnoreFaceCluster.mockResolvedValue(undefined);
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();
    await wrapper.get('[data-test="face-cluster-show-more"]').trigger('click');
    expect(wrapper.findAll('.face-review__card')).toHaveLength(40);

    // 忽略第 1 簇之后后端少一簇：窗口仍是 40 张，第 41 簇补进来。
    setClusters({ unnamed: many(44, 2) });
    await wrapper.find('[data-test="face-cluster-ignore-1"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-test="face-cluster-card-1"]').exists()).toBe(false);
    expect(wrapper.findAll('.face-review__card')).toHaveLength(40);
    expect(wrapper.find('[data-test="face-cluster-card-41"]').exists()).toBe(true);
    expect(wrapper.get('[data-test="face-cluster-show-more"]').text()).toContain('还有 4 组');
  });
});
