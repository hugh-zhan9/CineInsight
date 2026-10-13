// 片库管理的布局（D-P3-03）：列表与网格。2026-10-12 加的「缩略图墙」三期拿掉了（浏览海报去观看侧「视频」）：
// 本机还记着它的打开时退回列表，提示一次并改写偏好。另钉住网格卡「预览」打开抽屉、版本列表照常展开。
// 这里让 VideoListRow 真实渲染，虚拟列表换成逐项渲染插槽的桩，才点得到卡片本身。
import { flushPromises, shallowMount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => new Proxy({}, {
  get(target, prop) {
    if (typeof prop === 'symbol' || prop === 'then') return undefined;
    if (!target[prop]) target[prop] = vi.fn(() => Promise.resolve(null));
    return target[prop];
  }
}));
vi.mock('../../wailsjs/go/main/App', () => api);
vi.mock('./PreviewDrawer.vue', () => ({ default: { template: '<div />' } }));
vi.mock('./VirtualVideoList.vue', () => ({
  default: {
    name: 'VirtualVideoList',
    props: ['items', 'layoutMode'],
    template: '<div class="virtual-stub"><div v-for="item in items" :key="item.id" :data-row="item.id"><slot :item="item" /></div></div>'
  }
}));

const feedback = vi.hoisted(() => ({ notify: vi.fn() }));
vi.mock('../utils/feedback.js', async (importOriginal) => ({ ...(await importOriginal()), ...feedback }));

import VideoListPage, { readLibraryLayout } from './VideoListPage.vue';
import { estimateVideoGridHeight } from '../utils/virtualList.js';

const summary = { group_id: 5, title: '', revision: 2, member_count: 3, watched_count: 0, rated_count: 0, min_rating: null, max_rating: null, members: [] };
const rows = [
  { id: 1, name: 'v1.mp4', display_title: '第一部', tags: [] },
  { id: 4, name: 'v4.mp4', display_title: '第四部', tags: Array.from({ length: 20 }, (_, i) => ({ id: i + 1, name: `标签${i}` })) }
];

let store;
async function mountPage() {
  const wrapper = shallowMount(VideoListPage, {
    props: { tags: [], settings: {}, directories: [] },
    global: { stubs: { VirtualVideoList: false, VideoListRow: false } }
  });
  await flushPromises();
  return wrapper;
}

function previewState(wrapper) {
  return {
    open: wrapper.vm.previewOpen,
    id: wrapper.vm.selectedPreviewVideoId,
    snapshot: wrapper.vm.previewVideoSnapshot?.id,
    sessionRequest: api.GetPreviewSession.mock.calls.at(-1)
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  delete window.runtime;
  store = {};
  Object.defineProperty(window, 'localStorage', {
    configurable: true,
    value: { getItem: vi.fn(key => store[key] ?? null), setItem: vi.fn((key, value) => { store[key] = value; }), removeItem: vi.fn(), clear: vi.fn() }
  });
  api.SearchLibraryVideoPage.mockResolvedValue({ videos: rows, version_groups: { 4: summary }, automatic_override_kinds: {} });
  api.CountLibraryVideos.mockResolvedValue(2);
  api.GetSemanticIndexStatus.mockResolvedValue({ available: true, unavailable: '' });
  api.ListSavedLibraryViews.mockResolvedValue([]);
  api.GetSubtitleQueueState.mockResolvedValue({ active_task: null, queued_tasks: [], total: 0 });
  api.GetAITaggingStatusSummary.mockResolvedValue({ same_source_unread: 0 });
  api.GetTechnicalBackfillStatus.mockResolvedValue({ running: false, failures: [] });
  api.GetPerceptualHashBackfillStatus.mockResolvedValue({ running: false, failures: [] });
  api.GetLocalMetadataBackfillStatus.mockResolvedValue({ running: false, failures: [] });
  api.GetLocalMetadataExportStatus.mockResolvedValue({ running: false, failures: [] });
  api.GetSettings.mockResolvedValue({ scan_exclude_paths: '' });
  api.GetEnhancementCapability.mockResolvedValue({ available: true });
  api.ListStaleReasonCounts.mockResolvedValue({});
  api.GetAutomaticOverrideKinds.mockResolvedValue({});
  api.GetSubtitleIndexSyncStatus.mockResolvedValue({ running: false, checked: 0 });
  api.ListRecentlyPlayedWithFilter.mockResolvedValue([]);
  api.GetPreviewSession.mockImplementation(id => Promise.resolve({ video_id: id, mode: 'inline' }));
});

describe('布局偏好', () => {
  it.each([
    ['grid', 'grid'],
    ['list', 'list'],
    [null, 'list'],
    ['mosaic', 'list']
  ])('localStorage 存 %s 时以 %s 打开，不提示', async (stored, expected) => {
    if (stored !== null) store['cineinsight-library-layout'] = stored;
    const wrapper = await mountPage();
    expect(wrapper.vm.viewMode).toBe(expected);
    expect(wrapper.findComponent({ name: 'VirtualVideoList' }).props('layoutMode')).toBe(expected);
    expect(feedback.notify).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it('D-P3-03 本机记着已拿掉的缩略图墙：按列表打开、提示一次并改写偏好，下次不再提示', async () => {
    store['cineinsight-library-layout'] = 'wall';
    const wrapper = await mountPage();
    expect(wrapper.vm.viewMode).toBe('list');
    expect(wrapper.findComponent({ name: 'VirtualVideoList' }).props('layoutMode')).toBe('list');
    expect(wrapper.findAll('[data-test="library-wall-tile"]')).toHaveLength(0);
    expect(feedback.notify).toHaveBeenCalledTimes(1);
    expect(feedback.notify).toHaveBeenCalledWith('片库管理不再有缩略图墙，已换成列表。浏览海报请去观看侧「视频」。');
    expect(store['cineinsight-library-layout']).toBe('list');
    wrapper.unmount();

    const again = await mountPage();
    expect(again.vm.viewMode).toBe('list');
    expect(feedback.notify).toHaveBeenCalledTimes(1);
    again.unmount();
  });

  it('D-P3-03 应用启动时本页还不在前台（观看侧）：先改写偏好，等进到片库管理再提示', async () => {
    store['cineinsight-library-layout'] = 'wall';
    const wrapper = shallowMount(VideoListPage, {
      props: { tags: [], settings: {}, directories: [], pageActive: false },
      global: { stubs: { VirtualVideoList: false, VideoListRow: false } }
    });
    await flushPromises();
    expect(wrapper.vm.viewMode).toBe('list');
    expect(store['cineinsight-library-layout']).toBe('list');
    expect(feedback.notify).not.toHaveBeenCalled();
    await wrapper.setProps({ pageActive: true });
    expect(feedback.notify).toHaveBeenCalledTimes(1);
    await wrapper.setProps({ pageActive: false });
    await wrapper.setProps({ pageActive: true });
    expect(feedback.notify).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });

  it('读偏好时存储不可用（访问即抛错）按列表打开，不提示', () => {
    Object.defineProperty(window, 'localStorage', { configurable: true, get() { throw new Error('SecurityError'); } });
    expect(readLibraryLayout()).toEqual({ layout: 'list', removed: false });
  });
});

describe('网格卡与版本组', () => {
  it('点网格卡上的「预览」打开预览抽屉', async () => {
    store['cineinsight-library-layout'] = 'grid';
    const wrapper = await mountPage();
    const gridCard = wrapper.get('[data-row="4"]');
    await gridCard.findAll('.video-row-btn').find(button => button.text() === '预览').trigger('click');
    await flushPromises();
    expect(previewState(wrapper)).toEqual({ open: true, id: 4, snapshot: 4, sessionRequest: [4] });
    expect(wrapper.vm.previewSession).toEqual({ video_id: 4, mode: 'inline' });
    wrapper.unmount();
  });

  it('网格里展开的版本列表照常显示，行高预估随之加高', async () => {
    store['cineinsight-library-layout'] = 'grid';
    const wrapper = await mountPage();
    const geometry = { columns: 4, cardWidth: 240 };
    const before = wrapper.vm.estimateVideoHeight(rows[1], 15, false, geometry);
    expect(before).toBe(estimateVideoGridHeight(rows[1], 240));
    wrapper.vm.toggleVersionGroup(rows[1]);
    await flushPromises();
    expect(wrapper.findComponent({ name: 'VersionGroupMembers' }).exists()).toBe(true);
    expect(wrapper.vm.estimateVideoHeight(rows[1], 15, false, geometry)).toBeGreaterThan(before);
    wrapper.unmount();
  });
});
