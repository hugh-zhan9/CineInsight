// 断言原在 src/components/VideoListPage.test.js 的「多选批量栏」与「工具栏三层重排」
// 两节，以及「语义检索降级」里两条查 DOM 的用例：工具栏抽成独立组件后原样搬来，
// 只把「改片库页状态」的准备动作换成传 prop、把「改片库页状态」的断言换成查 emit。
import { flushPromises, mount, shallowMount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => new Proxy({}, {
  get(target, prop) {
    if (typeof prop === 'symbol' || prop === 'then') return undefined;
    if (!target[prop]) target[prop] = vi.fn(() => Promise.resolve(null));
    return target[prop];
  }
}));
vi.mock('../../../wailsjs/go/main/App', () => api);

import LibraryToolbar from './LibraryToolbar.vue';

const SIZE_OPTIONS = [
  { label: '2G-4G', value: { min: 2 * 1024 ** 3, max: 4 * 1024 ** 3 } }
];
const RES_OPTIONS = [
  { label: '4k以上', value: { min: 2160, max: 0 } }
];

function baseProps(extra = {}) {
  return {
    tags: [],
    videos: [],
    selectedVideoIds: [],
    sizeOptions: SIZE_OPTIONS,
    resOptions: RES_OPTIONS,
    incrementalScan: { running: false, state: 'idle', message: '' },
    directories: [],
    settings: {},
    aiTagSummary: { same_source_unread: 0 },
    technicalBackfill: { running: false },
    perceptualHash: { running: false },
    localMetadataBackfill: { running: false },
    localMetadataExport: { running: false },
    tagBgColor: (hex) => hex,
    libraryFilterFrom: (draft) => ({ draft }),
    ...extra
  };
}

const mountToolbar = (extra = {}, options = {}) =>
  shallowMount(LibraryToolbar, { props: baseProps(extra), ...options });

beforeEach(() => {
  vi.clearAllMocks();
});

describe('多选批量栏', () => {
  const rows = [
    { id: 1, name: 'a.mp4', size: 2 * 1024 ** 3, tags: [] },
    { id: 2, name: 'b.mp4', size: 1024 ** 3, tags: [] }
  ];

  it('批量栏顶替结果条，显示已选数与合计体积', async () => {
    const wrapper = mount(LibraryToolbar, { props: baseProps({ videos: [...rows], selectedVideoIds: [1, 2] }) });
    await wrapper.vm.$nextTick();

    expect(wrapper.find('.result-bar').exists()).toBe(false);
    expect(wrapper.find('.selection-toolbar').exists()).toBe(true);
    expect(wrapper.text()).toContain('已选 2 个');
    expect(wrapper.vm.selectedTotalSizeText).toBe('3.0 GB');
    wrapper.unmount();
  });

  it('跨页选中时不猜合计体积', async () => {
    // 第 2 条不在已加载页里，拿不到 size —— 宁可不显示也不显示一个偏小的数。
    const wrapper = mountToolbar({ videos: [rows[0]], selectedVideoIds: [1, 2] });
    await wrapper.vm.$nextTick();
    expect(wrapper.vm.selectedTotalSizeText).toBe('');
    wrapper.unmount();
  });

  it('批量栏的四个动作与两个选择动作都发回片库页', async () => {
    const wrapper = mount(LibraryToolbar, { props: baseProps({ videos: [...rows], selectedVideoIds: [1, 2] }) });
    await wrapper.vm.$nextTick();
    const buttons = wrapper.findAll('.selection-toolbar button');
    const byText = text => buttons.find(button => button.text().includes(text));
    await byText('批量标签编辑').trigger('click');
    await byText('批量迁移').trigger('click');
    await byText('导入本地资料').trigger('click');
    await byText('批量删除').trigger('click');
    await byText('清除选择').trigger('click');
    for (const name of ['batch-add-tag', 'batch-move', 'batch-local-metadata', 'batch-delete', 'clear-selection']) {
      expect(wrapper.emitted(name)).toHaveLength(1);
    }
    wrapper.unmount();
  });
});

describe('工具栏三层重排', () => {
  it('结果条用后端计数回显命中数与全库总数', async () => {
    const wrapper = mount(LibraryToolbar, { props: baseProps({ filteredCount: 218, libraryTotalCount: 3482 }) });
    await wrapper.vm.$nextTick();
    expect(wrapper.text()).toContain('筛选出');
    expect(wrapper.vm.filteredCountText).toBe('218');
    expect(wrapper.text()).toContain('3,482');
    wrapper.unmount();
  });

  it('条件回显把智能视图、标签和三类区间都写成中文', async () => {
    const wrapper = mountToolbar({
      tags: [{ id: 3, name: '科幻' }],
      smartView: 'unwatched',
      selectedTags: [3],
      selectedSizeRange: { min: 2 * 1024 ** 3, max: 4 * 1024 ** 3 },
      minRating: '6'
    });
    await wrapper.vm.$nextTick();

    const labels = wrapper.vm.activeConditionLabels;
    expect(labels[0]).toBe('未看');
    expect(labels).toContain('标签 科幻');
    expect(labels.some(text => text.startsWith('体积'))).toBe(true);
    expect(labels).toContain('评分 6–10');
    wrapper.unmount();
  });

  it('清除条件按钮把复位交给片库页', async () => {
    const wrapper = mount(LibraryToolbar, { props: baseProps({ smartView: 'unwatched' }) });
    await wrapper.vm.$nextTick();
    await wrapper.find('.result-bar__clear').trigger('click');
    expect(wrapper.emitted('clear-conditions')).toHaveLength(1);
    wrapper.unmount();
  });

  it('筛选徽标只数收进浮层的三类区间条件', async () => {
    const wrapper = mountToolbar();
    expect(wrapper.vm.activeFilterCount).toBe(0);
    // 智能视图有自己的常驻控件，不该算进徽标，否则徽标和浮层内容对不上。
    await wrapper.setProps({ smartView: 'unwatched' });
    expect(wrapper.vm.activeFilterCount).toBe(0);
    await wrapper.setProps({ selectedResRange: { min: 2160, max: 0 }, maxRating: '8' });
    expect(wrapper.vm.activeFilterCount).toBe(2);
    wrapper.unmount();
  });

  it('筛选浮层改的是草稿，应用后才生效', async () => {
    const wrapper = mountToolbar();
    wrapper.vm.toggleToolbarMenu('filter', 'filterTrigger');
    wrapper.vm.filterDraft.minRating = '7';
    await wrapper.vm.$nextTick();
    // 还没点应用，生效条件不动：片库页那边一个事件都没收到，草稿也只留在浮层里。
    expect(wrapper.emitted('apply-filter')).toBeUndefined();
    expect(wrapper.vm.filterDraft.minRating).toBe('7');

    wrapper.vm.applyFilterDraft();
    await flushPromises();
    expect(wrapper.emitted('apply-filter')[0][0]).toEqual(expect.objectContaining({ minRating: '7' }));
    expect(wrapper.vm.toolbarMenu).toBeNull();
    wrapper.unmount();
  });

  it('管理菜单保留全部维护动作并按四组分开（P-007 追加建议作品集、P-006 追加当前筛选生成代理）', async () => {
    const wrapper = mountToolbar({ settings: { local_metadata_enabled: true } });
    const items = wrapper.vm.manageMenuItems;
    expect(items.filter(item => item.heading).map(item => item.heading)).toEqual(['扫描', '整理', '补全', '维护']);
    expect(items.filter(item => item.id).map(item => item.id)).toEqual([
      'scan-new', 'scan-incremental',
      'move-folder', 'rename-folder', 'export-nfo',
      'backfill-technical', 'backfill-phash', 'backfill-playback-proxy', 'backfill-local-metadata',
      'ai-tags', 'tag-manager', 'cleanup', 'collection-suggestions', 'trash'
    ]);
    wrapper.unmount();
  });

  it('「为当前筛选生成代理」在任务运行时显示进度并置灰', async () => {
    const idle = mountToolbar();
    const idleItem = idle.vm.manageMenuItems.find(entry => entry.id === 'backfill-playback-proxy');
    expect(idleItem.label).toBe('为当前筛选生成代理');
    expect(idleItem.disabled).toBe(false);
    idle.unmount();

    const running = mountToolbar({ playbackProxy: { running: true, processed: 2, total: 5 } });
    const runningItem = running.vm.manageMenuItems.find(entry => entry.id === 'backfill-playback-proxy');
    expect(runningItem.label).toBe('为当前筛选生成代理（2/5）');
    expect(runningItem.disabled).toBe(true);
    // 这一项照旧交回片库页处理（筛选→ID 列表在后端解析）。
    running.vm.onManageSelect('backfill-playback-proxy');
    expect(running.emitted('manage-select')[0]).toEqual(['backfill-playback-proxy']);
    running.unmount();
  });

  it('批量栏「为选中生成代理」把事件转发回片库页', async () => {
    const wrapper = mount(LibraryToolbar, {
      props: baseProps({ videos: [{ id: 1, name: 'a.mkv', size: 1024, tags: [] }], selectedVideoIds: [1] })
    });
    await wrapper.vm.$nextTick();
    await wrapper.get('[data-test="batch-playback-proxy"]').trigger('click');
    expect(wrapper.emitted('batch-playback-proxy')).toHaveLength(1);
    wrapper.unmount();
  });

  it('建议作品集入口与清理审阅同级，面板由工具栏自己持有', async () => {
    api.GetCollectionSuggestionStatus.mockResolvedValue({ running: false });
    const wrapper = mountToolbar();
    await flushPromises();
    expect(wrapper.vm.collectionSuggestionOpen).toBe(false);

    // 其余菜单项照旧交回片库页，只有这一项由工具栏自己接。
    wrapper.vm.onManageSelect('cleanup');
    expect(wrapper.emitted('manage-select')[0]).toEqual(['cleanup']);
    wrapper.vm.onManageSelect('collection-suggestions');
    await wrapper.vm.$nextTick();
    expect(wrapper.vm.collectionSuggestionOpen).toBe(true);
    expect(wrapper.emitted('manage-select')).toHaveLength(1);
    wrapper.unmount();
  });

  it('剧集分析在跑时菜单文案说明进行中', async () => {
    api.GetCollectionSuggestionStatus.mockResolvedValue({ running: true });
    const wrapper = mountToolbar();
    await flushPromises();
    const item = wrapper.vm.manageMenuItems.find(entry => entry.id === 'collection-suggestions');
    expect(item.label).toBe('建议作品集（分析中）');
    wrapper.unmount();
  });

  it('随机菜单承载三种模式与随机 10 部，主键仍是按当前条件随机', async () => {
    const wrapper = mount(LibraryToolbar, { props: baseProps({ randomPickSize: 10 }) });
    const ids = wrapper.vm.randomMenuItems.filter(item => item.id).map(item => item.id);
    expect(ids).toEqual(['mode:balanced', 'mode:unwatched', 'mode:favorites', 'pick-ten']);
    expect(wrapper.vm.randomMenuItems[0].checked).toBe(true);
    expect(wrapper.vm.randomMenuItems.find(item => item.id === 'pick-ten').label).toBe('随机 10 部');

    const main = wrapper.findAll('button').find(button => button.text() === '按当前条件随机');
    await main.trigger('click');
    expect(wrapper.emitted('play-random')).toHaveLength(1);
    wrapper.unmount();
  });

  it('视图菜单合并了保存视图的三个控件，没有视图时删除项禁用', async () => {
    const wrapper = mountToolbar();
    let items = wrapper.vm.viewMenuItems;
    expect(items.find(item => item.id === 'delete-current').disabled).toBe(true);

    await wrapper.setProps({ savedViews: [{ id: 5, name: '未看 4K' }], selectedSavedViewID: 5 });
    items = wrapper.vm.viewMenuItems;
    expect(items[0]).toEqual(expect.objectContaining({ id: 'saved:5', label: '未看 4K', checked: true }));
    expect(items.find(item => item.id === 'delete-current').disabled).toBe(false);
    wrapper.unmount();
  });

  it('语义模式下不用结构化计数冒充命中数', async () => {
    const wrapper = mountToolbar({ searchMode: 'semantic', videos: [{ id: 1 }, { id: 2 }], hasMore: false, filteredCount: null });
    await wrapper.vm.$nextTick();
    expect(wrapper.vm.filteredCountText).toBe('2');
    wrapper.unmount();
  });
});

describe('语义入口置灰', () => {
  it('能力不可用时语义入口置灰并说明原因', async () => {
    const wrapper = mount(LibraryToolbar, {
      props: baseProps({ semanticAvailable: false, semanticUnavailableNotice: '语义搜索不可用：语义向量检索需要 PostgreSQL pgvector' })
    });
    await wrapper.vm.$nextTick();

    const semanticButton = wrapper.find('[data-test="search-mode-semantic"]');
    expect(semanticButton.attributes('disabled')).toBeDefined();
    expect(semanticButton.attributes('title')).toContain('语义搜索不可用');
    wrapper.unmount();
  });

  it('能力可用时语义入口正常', async () => {
    const wrapper = mount(LibraryToolbar, { props: baseProps() });
    await wrapper.vm.$nextTick();
    expect(wrapper.find('[data-test="search-mode-semantic"]').attributes('disabled')).toBeUndefined();
    wrapper.unmount();
  });
});
