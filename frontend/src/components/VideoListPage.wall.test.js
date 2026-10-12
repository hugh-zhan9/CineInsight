// 片库「缩略图墙」接线（2026-10-12 用户裁决「不要颜色，只要一个紧凑的缩略图墙」）：
// 布局偏好的读取与回退、虚拟列表与行组件拿到的布局、格子行高预估，以及点格子与点网格卡「预览」打开同一个抽屉。
// 这里让 VideoListRow 真实渲染，虚拟列表换成逐项渲染插槽的桩，才点得到格子本身。
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

import VideoListPage from './VideoListPage.vue';
import { estimateVideoWallHeight } from '../utils/virtualList.js';

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
    ['wall', 'wall'],
    ['grid', 'grid'],
    ['list', 'list'],
    [null, 'list'],
    ['mosaic', 'list']
  ])('localStorage 存 %s 时以 %s 打开', async (stored, expected) => {
    if (stored !== null) store['cineinsight-library-layout'] = stored;
    const wrapper = await mountPage();
    expect(wrapper.vm.viewMode).toBe(expected);
    expect(wrapper.findComponent({ name: 'VirtualVideoList' }).props('layoutMode')).toBe(expected);
    wrapper.unmount();
  });

  it('工具栏切到缩略图墙后虚拟列表与行组件都换成 wall', async () => {
    const wrapper = await mountPage();
    wrapper.findComponent({ name: 'LibraryToolbar' }).vm.$emit('update:viewMode', 'wall');
    await flushPromises();
    expect(wrapper.findComponent({ name: 'VirtualVideoList' }).props('layoutMode')).toBe('wall');
    const rowsRendered = wrapper.findAllComponents({ name: 'VideoListRow' });
    expect(rowsRendered).toHaveLength(2);
    expect(rowsRendered.every(item => item.props('layoutMode') === 'wall' && item.props('narrow') === false)).toBe(true);
    expect(wrapper.findAll('[data-test="library-wall-tile"]').map(tile => tile.attributes('title'))).toEqual(['第一部', '第四部']);
    wrapper.unmount();
  });
});

describe('点格子打开预览', () => {
  it('点缩略图墙的格子与点网格卡上的「预览」打开同一个预览抽屉', async () => {
    store['cineinsight-library-layout'] = 'grid';
    const wrapper = await mountPage();
    const gridCard = wrapper.get('[data-row="4"]');
    await gridCard.findAll('.row-btn').find(button => button.text() === '预览').trigger('click');
    await flushPromises();
    const fromGrid = previewState(wrapper);
    expect(fromGrid).toEqual({ open: true, id: 4, snapshot: 4, sessionRequest: [4] });

    wrapper.vm.closePreview();
    api.GetPreviewSession.mockClear();
    wrapper.vm.viewMode = 'wall';
    await flushPromises();
    await wrapper.get('[data-row="4"] [data-test="library-wall-tile"]').trigger('click');
    await flushPromises();
    expect(previewState(wrapper)).toEqual(fromGrid);
    expect(wrapper.vm.previewSession).toEqual({ video_id: 4, mode: 'inline' });
    wrapper.unmount();
  });
});

describe('格子高度与版本组', () => {
  it('行高预估只由格子宽度决定：标签再多、版本组展开也不加高', async () => {
    store['cineinsight-library-layout'] = 'wall';
    const wrapper = await mountPage();
    wrapper.vm.toggleVersionGroup(rows[1]);
    expect(wrapper.vm.isVersionGroupExpanded(rows[1])).toBe(true);
    const geometry = { columns: 8, cardWidth: 144.75 };
    expect(wrapper.vm.estimateVideoHeight(rows[1], 15, false, geometry)).toBe(estimateVideoWallHeight(144.75));
    expect(wrapper.vm.estimateVideoHeight(rows[0], 15, false, geometry)).toBe(estimateVideoWallHeight(144.75));
    wrapper.unmount();
  });

  it('展开的版本列表不塞进缩略图墙的格子，切回网格后照旧展开', async () => {
    store['cineinsight-library-layout'] = 'wall';
    const wrapper = await mountPage();
    wrapper.vm.toggleVersionGroup(rows[1]);
    await flushPromises();
    expect(wrapper.findComponent({ name: 'VersionGroupMembers' }).exists()).toBe(false);
    wrapper.vm.viewMode = 'grid';
    await flushPromises();
    expect(wrapper.findComponent({ name: 'VersionGroupMembers' }).exists()).toBe(true);
    wrapper.unmount();
  });
});
