import { flushPromises, mount } from '@vue/test-utils';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'ConfirmFaceClusterAppend', 'ConfirmFaceClusterAppendObservations', 'DismissFaceClusterAppend', 'GetFaceClusterObservations',
  'IgnoreFaceCluster', 'LinkFaceCluster', 'ListFaceClusterPage', 'ListIgnoredFaceClusters', 'ListPeople', 'NameFaceCluster',
  'PreviewFaceClusterUnlink', 'ReassignFaceCluster', 'RestoreFaceCluster', 'UnlinkFaceCluster',
].map(name => [name, vi.fn()])));
vi.mock('../../wailsjs/go/main/App', () => api);
// 忽略、解除关联这类动作走应用内确认框：默认答「确定」，需要「取消」的用例单独设置。
const feedback = vi.hoisted(() => ({ confirmAction: vi.fn(() => Promise.resolve(true)) }));
vi.mock('../utils/feedback.js', async (importOriginal) => ({ ...(await importOriginal()), ...feedback }));

import FaceClusterReviewPanel from './FaceClusterReviewPanel.vue';
import DialogShell from './ui/DialogShell.vue';
import { clearToasts, feedbackState, runToastAction } from '../utils/feedback.js';
import * as recentOperations from '../utils/recentOperations.js';

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

// 后端 ListFaceClusterPage 的替身：按给定顺序做键集分页（游标是上一页最后一张的 {count, id}）。
let clusterStore = { unnamed: [], named: [] };
const pageOf = (list, cursor, limit) => {
  const start = cursor?.id ? list.findIndex(item => item.id === cursor.id) + 1 : 0;
  const size = limit > 0 ? limit : 50;
  const clusters = list.slice(start, start + size);
  const hasMore = start + size < list.length;
  const last = clusters[clusters.length - 1];
  return { clusters, has_more: hasMore, next: hasMore ? { count: last.observation_count, id: last.id } : { count: 0, id: 0 } };
};
const setClusters = ({ unnamed = [], named = [] } = {}) => {
  clusterStore = { unnamed, named };
};

let handlers = {};

beforeEach(() => {
  vi.clearAllMocks();
  clearToasts();
  recentOperations.clear();
  handlers = {};
  window.runtime = {
    EventsOn: (name, callback) => {
      handlers[name] = callback;
      return () => { delete handlers[name]; };
    },
  };
  setClusters();
  api.ListFaceClusterPage.mockImplementation(async (filter, cursor, limit) => pageOf(filter?.status === 'named' ? clusterStore.named : clusterStore.unnamed, cursor, limit));
  api.ListIgnoredFaceClusters.mockResolvedValue({ clusters: [], next_cursor: 0 });
  api.ListPeople.mockResolvedValue([]);
  feedback.confirmAction.mockResolvedValue(true);
});

afterEach(() => {
  delete window.runtime;
});

const unnamedCalls = () => api.ListFaceClusterPage.mock.calls.filter(([filter]) => filter?.status === 'unnamed');

describe('FaceClusterReviewPanel', () => {
  it('shows an empty state instead of a blank panel', async () => {
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();
    expect(wrapper.find('[data-test="face-cluster-review-empty"]').exists()).toBe(true);
  });

  // 视图页签与卡片列表互相指向（role=tabpanel、id、aria-labelledby 对上页签的 id 与 aria-controls），← → 切换时跟着换。
  // 这个面板会同时挂在几处（人物与作品集、待处理、两个审阅对话框），每个实例的 id 各不相同。
  it('view tabs and the card list point at each other, follow ← / →, and stay unique per instance', async () => {
    setClusters({ unnamed: [cluster()] });
    const wrapper = mount(FaceClusterReviewPanel, { attachTo: document.body });
    const other = mount(FaceClusterReviewPanel, { attachTo: document.body });
    await flushPromises();
    for (const [key, next] of [['pending', 'ArrowRight'], ['named', 'ArrowRight'], ['ignored', null]]) {
      const tab = wrapper.get(`[data-test="face-cluster-view-${key}"]`);
      const panel = wrapper.get('[role="tabpanel"]');
      expect(tab.attributes('aria-selected')).toBe('true');
      expect(tab.attributes('aria-controls')).toBe(panel.attributes('id'));
      expect(panel.attributes('aria-labelledby')).toBe(tab.attributes('id'));
      expect(panel.attributes('id')).toMatch(new RegExp(`-panel-${key}$`));
      expect(document.querySelectorAll(`[id="${panel.attributes('id')}"]`)).toHaveLength(1);
      expect(document.querySelectorAll(`[id="${tab.attributes('id')}"]`)).toHaveLength(1);
      if (key === 'pending') expect(panel.find('[data-test="face-cluster-card-1"]').exists()).toBe(true);
      if (next) { await wrapper.get('[role="tablist"]').trigger('keydown', { key: next }); await flushPromises(); }
    }
    expect(wrapper.get('[role="tabpanel"]').find('[data-test="face-cluster-ignored-empty"]').exists()).toBe(true);
    other.unmount();
    wrapper.unmount();
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

  // 已命名但没有待确认追加的簇不占「待处理」的位置，但在「已命名」里能找到。
  it('keeps named clusters without pending appends out of the pending view', async () => {
    setClusters({ named: [cluster({ id: 2, status: 'named', person_id: 5, person_name: '周迅' })] });
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();
    expect(wrapper.find('[data-test="face-cluster-card-2"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="face-cluster-review-empty"]').exists()).toBe(true);

    await wrapper.get('[data-test="face-cluster-view-named"]').trigger('click');
    expect(wrapper.find('[data-test="face-cluster-card-2"]').exists()).toBe(true);
  });

  it('names a cluster as a new person and moves it out of the pending list locally', async () => {
    setClusters({ unnamed: [cluster()] });
    api.NameFaceCluster.mockResolvedValue(cluster({ status: 'named', person_id: 9, person_name: '周迅' }));
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();
    const loads = api.ListFaceClusterPage.mock.calls.length;

    await wrapper.find('[data-test="face-cluster-name-open-1"]').trigger('click');
    await wrapper.find('[data-test="face-cluster-name-display"]').setValue('周迅');
    await wrapper.find('[data-test="face-cluster-name-original"]').setValue('Zhou Xun');
    await wrapper.find('[data-test="face-cluster-name-submit"]').trigger('click');
    await flushPromises();

    expect(api.NameFaceCluster).toHaveBeenCalledWith(1, '周迅', 'Zhou Xun');
    expect(wrapper.emitted('changed')).toHaveLength(1);
    expect(wrapper.find('[data-test="face-cluster-review-notice"]').text()).toContain('周迅');
    expect(wrapper.find('[data-test="face-cluster-card-1"]').exists()).toBe(false);
    // META-11：动作后只局部更新，不整表重拉。
    expect(api.ListFaceClusterPage.mock.calls.length).toBe(loads);
    await wrapper.get('[data-test="face-cluster-view-named"]').trigger('click');
    expect(wrapper.get('[data-test="face-cluster-card-1"]').text()).toContain('周迅');
  });

  it('links a cluster to an existing person with the candidate on top', async () => {
    setClusters({
      unnamed: [cluster({ candidates: [{ person_id: 5, display_name: '周迅', similarity: 0.9 }] })],
    });
    api.ListPeople.mockResolvedValue([
      { person: { id: 9, display_name: '汤唯' } },
    ]);
    api.LinkFaceCluster.mockResolvedValue(cluster({ status: 'named', person_id: 5, person_name: '周迅' }));
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

  // Esc 只关最上面一层：面板嵌在视频侧「AI 标签管理」与图片侧「AI 标签审阅」对话框里，关联人物的候选下拉开着时
  // 按 Esc 曾连对话框一起关掉。下拉开着时 Esc 只收起下拉，收起之后再按才轮到对话框。
  it('Escape in the link search closes only its open option list; the next Escape reaches the dialog around the panel', async () => {
    setClusters({ unnamed: [cluster()] });
    const Host = {
      components: { DialogShell, FaceClusterReviewPanel },
      emits: ['close'],
      template: '<DialogShell title="AI 标签管理" @close="$emit(\'close\')"><FaceClusterReviewPanel /></DialogShell>'
    };
    const wrapper = mount(Host, { attachTo: document.body });
    await flushPromises();
    await wrapper.get('[data-test="face-cluster-link-open-1"]').trigger('click');
    await flushPromises();
    const search = wrapper.get('[data-test="face-cluster-link-search"]');
    const escape = () => search.element.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));

    escape();
    await flushPromises();
    expect(wrapper.find('[data-test="face-cluster-link-options"]').exists()).toBe(false);
    expect(wrapper.emitted('close')).toBeUndefined();

    escape();
    expect(wrapper.emitted('close')).toHaveLength(1);
    wrapper.unmount();
  });

  // META-04：忽略前二次确认，文案说清被忽略的组还会吸走同一个人的新人脸、可以恢复。
  it('META-04 asks before ignoring a cluster and does nothing when cancelled', async () => {
    setClusters({ unnamed: [cluster()] });
    api.IgnoreFaceCluster.mockResolvedValue();
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();

    feedback.confirmAction.mockResolvedValueOnce(false);
    await wrapper.find('[data-test="face-cluster-ignore-1"]').trigger('click');
    await flushPromises();
    expect(feedback.confirmAction).toHaveBeenCalledWith(expect.objectContaining({ message: '忽略后同一个人的新人脸会归入此组且不再提示，可在「已忽略」中恢复。' }));
    expect(api.IgnoreFaceCluster).not.toHaveBeenCalled();

    await wrapper.find('[data-test="face-cluster-ignore-1"]').trigger('click');
    await flushPromises();
    expect(api.IgnoreFaceCluster).toHaveBeenCalledWith(1);
    expect(wrapper.find('[data-test="face-cluster-card-1"]').exists()).toBe(false);
    // D-P3-06：说明从面板里的一行挪到带「撤销」的全局提示上（同时记进「最近操作」）。
    expect(feedbackState.toasts.at(-1).message).toContain('可在「已忽略」中恢复');
    expect(feedbackState.toasts.at(-1).action.label).toBe('撤销');
    expect(wrapper.find('[data-test="face-cluster-review-notice"]').exists()).toBe(false);
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
    // 全部确认之后卡片离开「待处理」。
    expect(wrapper.find('[data-test="face-cluster-card-3"]').exists()).toBe(false);

    setClusters({ named: [named] });
    await wrapper.get('[data-test="face-cluster-refresh"]').trigger('click');
    await flushPromises();
    await wrapper.find('[data-test="face-cluster-append-dismiss-3"]').trigger('click');
    await flushPromises();
    expect(api.DismissFaceClusterAppend).toHaveBeenCalledWith(3);
    expect(wrapper.find('[data-test="face-cluster-card-3"]').exists()).toBe(false);
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
    expect(unnamedCalls().length).toBe(2);
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

    api.ListFaceClusterPage.mockRejectedValue(new Error('boom'));
    handlers['face-review-changed']();
    await flushPromises();
    expect(wrapper.find('[data-test="face-cluster-review-error"]').text()).toContain('加载人物候选失败');
    expect(wrapper.find('[data-test="face-cluster-card-1"]').exists()).toBe(true);
  });
});

// META-11（D-PC29）：未命名簇没有上限，改为服务端键集分页、「加载更多」逐页取。
describe('FaceClusterReviewPanel META-11 服务端分页', () => {
  const many = (count, firstID = 1) => Array.from({ length: count }, (_, index) => cluster({
    id: firstID + index,
    observation_count: 1000 - index,
    representative_observation_id: 1000 + firstID + index,
  }));

  it('META-11 loads 20 unnamed clusters per page and appends the next page on demand', async () => {
    setClusters({ unnamed: many(45), named: [cluster({ id: 99, status: 'named', person_id: 5, person_name: '周迅', append_pending_count: 1, append_pending_media: [{ media_kind: 'video', media_id: 3, name: 'a.mp4' }] })] });
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();

    expect(unnamedCalls()[0]).toEqual([{ status: 'unnamed', media_kind: '' }, { count: 0, id: 0 }, 20]);
    // 追加候选在前，后面是第一页未命名。
    expect(wrapper.findAll('.face-review__card')).toHaveLength(21);
    expect(wrapper.findAll('.face-review__card').at(0).attributes('data-test')).toBe('face-cluster-card-99');
    expect(wrapper.emitted('loaded').at(-1)[0]).toEqual({ unnamed: 20, appendPending: 1, unnamedHasMore: true });
    expect(wrapper.get('[data-test="face-cluster-view-pending"]').text()).toContain('21+');

    await wrapper.get('[data-test="face-cluster-load-more"]').trigger('click');
    await flushPromises();
    expect(unnamedCalls().at(-1)).toEqual([{ status: 'unnamed', media_kind: '' }, { count: 981, id: 20 }, 20]);
    expect(wrapper.findAll('.face-review__card')).toHaveLength(41);

    await wrapper.get('[data-test="face-cluster-load-more"]').trigger('click');
    await flushPromises();
    expect(wrapper.findAll('.face-review__card')).toHaveLength(46);
    expect(wrapper.find('[data-test="face-cluster-load-more"]').exists()).toBe(false);
    expect(wrapper.emitted('loaded').at(-1)[0]).toEqual({ unnamed: 45, appendPending: 1, unnamedHasMore: false });
  });

  it('META-11 keeps the loaded extent when an event refreshes the list', async () => {
    setClusters({ unnamed: many(45) });
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();
    await wrapper.get('[data-test="face-cluster-load-more"]').trigger('click');
    await flushPromises();
    expect(wrapper.findAll('.face-review__card')).toHaveLength(40);

    handlers['face-review-changed']();
    await flushPromises();
    // 刷新按已加载的 40 张重取第一页，不把用户翻出来的卡片收回去。
    expect(unnamedCalls().at(-1)[2]).toBe(40);
    expect(wrapper.findAll('.face-review__card')).toHaveLength(40);
  });

  it('META-11 removes an ignored cluster locally without reloading the pages', async () => {
    setClusters({ unnamed: many(45) });
    api.IgnoreFaceCluster.mockResolvedValue(undefined);
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();
    await wrapper.get('[data-test="face-cluster-load-more"]').trigger('click');
    await flushPromises();
    const calls = api.ListFaceClusterPage.mock.calls.length;

    await wrapper.find('[data-test="face-cluster-ignore-1"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-test="face-cluster-card-1"]').exists()).toBe(false);
    expect(wrapper.findAll('.face-review__card')).toHaveLength(39);
    expect(api.ListFaceClusterPage.mock.calls.length).toBe(calls);
  });
});

// META-04（D-PC30）：已忽略列表可恢复；已命名簇可解除关联或改派，执行前预览关系来源。
describe('FaceClusterReviewPanel META-04 可逆操作', () => {
  const named = (overrides = {}) => cluster({ id: 3, status: 'named', person_id: 5, person_name: '周迅', ...overrides });
  const preview = [
    { media_kind: 'video', media_id: 11, name: 'a.mp4', has_relation: true, source: 'face', covered_by_other: false },
    { media_kind: 'image', media_id: 12, name: 'b.jpg', has_relation: true, source: 'unknown', covered_by_other: false },
    { media_kind: 'video', media_id: 13, name: 'c.mp4', has_relation: true, source: 'face', covered_by_other: true },
    { media_kind: 'video', media_id: 14, name: 'd.mp4', has_relation: false, source: 'unknown', covered_by_other: false },
  ];

  it('已忽略列表第一次加载失败：不说「没有已忽略的人脸组」，给「重试」，点了重新加载', async () => {
    api.ListIgnoredFaceClusters.mockRejectedValueOnce(new Error('数据库繁忙'));
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();
    await wrapper.get('[data-test="face-cluster-view-ignored"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-test="face-cluster-ignored-empty"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="face-cluster-ignored-unloaded"]').exists()).toBe(true);

    api.ListIgnoredFaceClusters.mockResolvedValueOnce({ clusters: [], next_cursor: 0 });
    await wrapper.get('[data-test="face-cluster-ignored-retry"]').trigger('click');
    await flushPromises();
    expect(api.ListIgnoredFaceClusters).toHaveBeenCalledTimes(2);
    expect(wrapper.find('[data-test="face-cluster-ignored-empty"]').exists()).toBe(true);
  });

  it('META-04 lists ignored clusters with absorbed counts and restores one to pending', async () => {
    api.ListIgnoredFaceClusters.mockResolvedValue({
      clusters: [{ cluster: cluster({ id: 8, status: 'ignored' }), ignored_at: '2026-09-01T00:00:00Z', absorbed_since_ignored: 3 }],
      next_cursor: 0,
    });
    api.RestoreFaceCluster.mockResolvedValue();
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();

    await wrapper.get('[data-test="face-cluster-view-ignored"]').trigger('click');
    await flushPromises();
    expect(api.ListIgnoredFaceClusters).toHaveBeenCalledWith(0, 20);
    expect(wrapper.get('[data-test="face-cluster-absorbed-8"]').text()).toContain('忽略后又并入 3 张新人脸');

    await wrapper.get('[data-test="face-cluster-restore-8"]').trigger('click');
    await flushPromises();
    expect(api.RestoreFaceCluster).toHaveBeenCalledWith(8);
    expect(wrapper.find('[data-test="face-cluster-ignored-8"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="face-cluster-ignored-empty"]').exists()).toBe(true);
    await wrapper.get('[data-test="face-cluster-view-pending"]').trigger('click');
    expect(wrapper.find('[data-test="face-cluster-card-8"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="face-cluster-name-open-8"]').exists()).toBe(true);
  });

  it('META-04 explains cluster_not_ignored on restore and refreshes', async () => {
    api.ListIgnoredFaceClusters.mockResolvedValue({ clusters: [{ cluster: cluster({ id: 8, status: 'ignored' }), absorbed_since_ignored: 0 }], next_cursor: 0 });
    api.RestoreFaceCluster.mockRejectedValue(new Error('cluster_not_ignored'));
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();
    await wrapper.get('[data-test="face-cluster-view-ignored"]').trigger('click');
    await flushPromises();
    await wrapper.get('[data-test="face-cluster-restore-8"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="face-cluster-review-error"]').text()).toContain('已经不在「已忽略」里了');
    expect(api.ListIgnoredFaceClusters).toHaveBeenCalledTimes(2);
  });

  it('META-04 previews relation sources before unlinking and only removes face-written ones', async () => {
    setClusters({ named: [named()] });
    api.PreviewFaceClusterUnlink.mockResolvedValue(preview);
    api.UnlinkFaceCluster.mockResolvedValue(cluster({ id: 3, status: 'unnamed' }));
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();
    await wrapper.get('[data-test="face-cluster-view-named"]').trigger('click');

    await wrapper.get('[data-test="face-cluster-unlink-open-3"]').trigger('click');
    await flushPromises();
    expect(api.PreviewFaceClusterUnlink).toHaveBeenCalledWith(3);
    // 只预览，还没有执行。
    expect(api.UnlinkFaceCluster).not.toHaveBeenCalled();
    const face = wrapper.get('[data-test="face-cluster-relink-item-video-11"]').text();
    expect(face).toContain('由这组人脸写入');
    expect(face).toContain('将删除');
    const unknown = wrapper.get('[data-test="face-cluster-relink-item-image-12"]').text();
    expect(unknown).toContain('来源不明');
    expect(unknown).toContain('保留');
    expect(wrapper.get('[data-test="face-cluster-relink-item-video-13"]').text()).toContain('其他人脸组仍覆盖');
    expect(wrapper.get('[data-test="face-cluster-relink-item-video-13"]').text()).toContain('保留');
    expect(wrapper.get('[data-test="face-cluster-relink-item-video-14"]').text()).toContain('关系已不存在');
    expect(wrapper.get('[data-test="face-cluster-relink-form"]').text()).toContain('同时删除由这组人脸写入的 1 条人物关系');

    await wrapper.get('[data-test="face-cluster-relink-submit"]').trigger('click');
    await flushPromises();
    expect(api.UnlinkFaceCluster).toHaveBeenCalledWith(3, true);
    expect(wrapper.find('[data-test="face-cluster-card-3"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="face-cluster-review-notice"]').text()).toContain('删除了 1 条由这组人脸写入的关系');
    // 解除后回到待命名。
    await wrapper.get('[data-test="face-cluster-view-pending"]').trigger('click');
    expect(wrapper.find('[data-test="face-cluster-name-open-3"]').exists()).toBe(true);
  });

  it('META-04 can unlink while keeping every relation, and cancelling does nothing', async () => {
    setClusters({ named: [named()] });
    api.PreviewFaceClusterUnlink.mockResolvedValue(preview);
    api.UnlinkFaceCluster.mockResolvedValue(cluster({ id: 3, status: 'unnamed' }));
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();
    await wrapper.get('[data-test="face-cluster-view-named"]').trigger('click');

    await wrapper.get('[data-test="face-cluster-unlink-open-3"]').trigger('click');
    await flushPromises();
    await wrapper.get('[data-test="face-cluster-relink-cancel"]').trigger('click');
    expect(wrapper.find('[data-test="face-cluster-relink-form"]').exists()).toBe(false);
    expect(api.UnlinkFaceCluster).not.toHaveBeenCalled();

    await wrapper.get('[data-test="face-cluster-unlink-open-3"]').trigger('click');
    await flushPromises();
    await wrapper.get('[data-test="face-cluster-relink-apply"]').setValue(false);
    expect(wrapper.get('[data-test="face-cluster-relink-item-video-11"]').text()).toContain('保留');
    await wrapper.get('[data-test="face-cluster-relink-submit"]').trigger('click');
    await flushPromises();
    expect(api.UnlinkFaceCluster).toHaveBeenCalledWith(3, false);
    expect(wrapper.get('[data-test="face-cluster-review-notice"]').text()).toContain('人物关系保留');
  });

  it('META-04 reassigns a named cluster to another person after picking a target', async () => {
    setClusters({ named: [named()] });
    api.PreviewFaceClusterUnlink.mockResolvedValue(preview);
    api.ListPeople.mockResolvedValue([{ person: { id: 5, display_name: '周迅' } }, { person: { id: 9, display_name: '汤唯' } }]);
    api.ReassignFaceCluster.mockResolvedValue(named({ person_id: 9, person_name: '汤唯' }));
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();
    await wrapper.get('[data-test="face-cluster-view-named"]').trigger('click');

    await wrapper.get('[data-test="face-cluster-reassign-open-3"]').trigger('click');
    await flushPromises();
    // 不能改派给自己。
    expect(wrapper.find('[data-test="face-cluster-reassign-option-5"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="face-cluster-relink-submit"]').attributes('disabled')).toBeDefined();
    expect(wrapper.get('[data-test="face-cluster-relink-item-video-11"]').text()).toContain('迁到新人物');
    expect(wrapper.get('[data-test="face-cluster-relink-item-image-12"]').text()).toContain('原关系保留');

    await wrapper.get('[data-test="face-cluster-reassign-option-9"]').trigger('click');
    await wrapper.get('[data-test="face-cluster-relink-submit"]').trigger('click');
    await flushPromises();
    expect(api.ReassignFaceCluster).toHaveBeenCalledWith(3, 9, true);
    expect(wrapper.get('[data-test="face-cluster-card-3"]').text()).toContain('汤唯');
    expect(wrapper.get('[data-test="face-cluster-review-notice"]').text()).toContain('改派给「汤唯」');
  });

  it('META-04 explains cluster_conflict and cluster_ignored codes', async () => {
    setClusters({ named: [named()] });
    api.PreviewFaceClusterUnlink.mockResolvedValue(preview);
    api.ReassignFaceCluster.mockRejectedValue(new Error('cluster_conflict'));
    api.ListPeople.mockResolvedValue([{ person: { id: 9, display_name: '汤唯' } }]);
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();
    await wrapper.get('[data-test="face-cluster-view-named"]').trigger('click');
    await wrapper.get('[data-test="face-cluster-reassign-open-3"]').trigger('click');
    await flushPromises();
    await wrapper.get('[data-test="face-cluster-reassign-option-9"]').trigger('click');
    const before = api.ListFaceClusterPage.mock.calls.length;
    await wrapper.get('[data-test="face-cluster-relink-submit"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="face-cluster-review-error"]').text()).toContain('刚刚被改派给了其他人物');
    expect(api.ListFaceClusterPage.mock.calls.length).toBeGreaterThan(before);

    expect(wrapper.vm.errorMessage(new Error('cluster_ignored')).text).toContain('请先在「已忽略」中恢复');
    expect(wrapper.vm.errorMessage('cluster_not_ignored').text).toContain('已经不在「已忽略」里了');
  });

  it('META-04 confirms only the selected append media, one by one', async () => {
    setClusters({ named: [named({
      append_pending_count: 3,
      append_pending_media: [
        { media_kind: 'image', media_id: 11, name: 'b.jpg' },
        { media_kind: 'video', media_id: 12, name: 'movie.mp4' },
      ],
    })] });
    api.GetFaceClusterObservations.mockResolvedValueOnce({
      observations: [
        { observation_id: 101, media_kind: 'image', media_id: 11 },
        { observation_id: 102, media_kind: 'video', media_id: 12 },
      ],
      next_id: 102,
    }).mockResolvedValueOnce({
      observations: [{ observation_id: 103, media_kind: 'video', media_id: 12 }],
      next_id: 0,
    });
    api.ConfirmFaceClusterAppendObservations.mockResolvedValue();
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();

    await wrapper.get('[data-test="face-cluster-append-item-3-image-11"]').setValue(false);
    expect(wrapper.get('[data-test="face-cluster-append-confirm-3"]').text()).toBe('确认所选（1）');
    // 部分确认之后剩下的那个媒体仍待确认。
    setClusters({ named: [named({ append_pending_count: 1, append_pending_media: [{ media_kind: 'image', media_id: 11, name: 'b.jpg' }] })] });
    await wrapper.get('[data-test="face-cluster-append-confirm-3"]').trigger('click');
    await flushPromises();

    expect(api.GetFaceClusterObservations).toHaveBeenNthCalledWith(1, 3, 0, 200);
    expect(api.GetFaceClusterObservations).toHaveBeenNthCalledWith(2, 3, 102, 200);
    expect(api.ConfirmFaceClusterAppendObservations).toHaveBeenCalledWith(3, [102, 103]);
    expect(api.ConfirmFaceClusterAppend).not.toHaveBeenCalled();
    expect(wrapper.get('[data-test="face-cluster-append-summary-3"]').text()).toContain('b.jpg');
    expect(wrapper.get('[data-test="face-cluster-append-summary-3"]').text()).not.toContain('movie.mp4');
    expect(wrapper.get('[data-test="face-cluster-review-notice"]').text()).toContain('其余继续等待确认');
  });
});

// D-P3-06：人脸组「忽略」进任务中心「最近操作」，提示与「最近操作」里都能撤销（RestoreFaceCluster）。
describe('FaceClusterReviewPanel D-P3-06 忽略可撤销', () => {
  it('忽略后撤销：恢复这一组，就地回到「待处理」，不整表重拉', async () => {
    setClusters({ unnamed: [cluster(), cluster({ id: 2, observation_count: 3 })] });
    api.IgnoreFaceCluster.mockResolvedValue();
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();
    await wrapper.get('[data-test="face-cluster-ignore-1"]').trigger('click');
    await flushPromises();
    expect(recentOperations.list().map(item => item.label)).toEqual(['忽略人脸组（4 次出现 · 1 个视频 · 2 张图片）']);
    const calls = api.ListFaceClusterPage.mock.calls.length;

    api.RestoreFaceCluster.mockResolvedValue();
    expect(await runToastAction(feedbackState.toasts.at(-1).id)).toBe(true);
    await flushPromises();
    expect(api.RestoreFaceCluster).toHaveBeenCalledWith(1);
    expect(wrapper.find('[data-test="face-cluster-card-1"]').exists()).toBe(true);
    expect(api.ListFaceClusterPage.mock.calls.length).toBe(calls);
    expect(wrapper.emitted('changed').length).toBe(2);
    expect(recentOperations.list()).toEqual([]);
    wrapper.unmount();
  });

  it('面板卸下以后从「最近操作」撤销照样恢复；已在「已忽略」里恢复过的说一声；其余失败给中文原因、记录留着', async () => {
    setClusters({ unnamed: [cluster()] });
    api.IgnoreFaceCluster.mockResolvedValue();
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();
    await wrapper.get('[data-test="face-cluster-ignore-1"]').trigger('click');
    await flushPromises();
    const [operation] = recentOperations.list();
    wrapper.unmount();

    api.RestoreFaceCluster.mockRejectedValueOnce(new Error('cluster_not_found'));
    await expect(recentOperations.undo(operation.id)).rejects.toThrow('这一簇已经不在了');
    expect(recentOperations.list()).toHaveLength(1);

    api.RestoreFaceCluster.mockRejectedValueOnce(new Error('cluster_not_ignored'));
    expect(await recentOperations.undo(operation.id)).toBe(true);
    expect(feedbackState.toasts.at(-1).message).toBe('这组人脸已经不在「已忽略」里了（可能已经恢复过），不用再撤销。');
    expect(recentOperations.list()).toEqual([]);
  });

  it('「查看来源详情」不被纵向卡片拉满宽度', async () => {
    setClusters({ unnamed: [cluster()] });
    const wrapper = mount(FaceClusterReviewPanel);
    await flushPromises();
    expect(wrapper.get('[data-test="face-cluster-detail-1"]').classes()).toContain('face-review__detail');
    // vitest 的根就是 frontend/（jsdom 下 import.meta.url 不是 file: 协议）。
    const source = readFileSync(resolve(process.cwd(), 'src/components/FaceClusterReviewPanel.vue'), 'utf8');
    expect(source).toMatch(/\.face-review__detail \{\s*align-self: flex-start;/);
    wrapper.unmount();
  });
});
