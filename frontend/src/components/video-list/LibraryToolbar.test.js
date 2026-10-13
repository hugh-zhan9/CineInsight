// 断言原在 src/components/VideoListPage.test.js 的「多选批量栏」与「工具栏三层重排」
// 两节，以及「语义检索降级」里两条查 DOM 的用例：工具栏抽成独立组件后原样搬来，
// 只把「改片库页状态」的准备动作换成传 prop、把「改片库页状态」的断言换成查 emit。
import { flushPromises, mount, shallowMount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => new Proxy({}, {
  get(target, prop) {
    if (typeof prop === 'symbol' || prop === 'then') return undefined;
    if (!target[prop]) target[prop] = vi.fn(() => Promise.resolve(null));
    return target[prop];
  }
}));
vi.mock('../../../wailsjs/go/main/App', () => api);

import LibraryToolbar from './LibraryToolbar.vue';
import { commandList, findCommand } from '../../utils/commandRegistry.js';

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

// 选择（D-UD-10）：批量操作条移到片库页的内容区底部（BatchActionBar / SelectionBar），工具栏不再顶替结果条；
// 标签行那个选择按钮是「全选当前结果」，全选之后同一个位置是「取消选择」。批量条本身的用例在 BatchActionBar.test.js。
describe('工具栏的选择入口', () => {
  const rows = [
    { id: 1, name: 'a.mp4', size: 2 * 1024 ** 3, tags: [] },
    { id: 2, name: 'b.mp4', size: 1024 ** 3, tags: [] }
  ];

  it('结果条常驻（有选中也不被顶替），工具栏里没有批量动作', async () => {
    const wrapper = mount(LibraryToolbar, { props: baseProps({ videos: [...rows] }) });
    expect(wrapper.find('.result-bar').exists()).toBe(true);
    expect(wrapper.find('.selection-toolbar').exists()).toBe(false);
    expect(wrapper.find('[data-test="batch-delete"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('「全选当前结果」与「取消选择」随全选状态切换，都发 toggle-select-all 交给片库页', async () => {
    const wrapper = mount(LibraryToolbar, { props: baseProps({ videos: [...rows] }) });
    const button = () => wrapper.get('[data-test="toolbar-select-all-matching"]');
    expect(button().text()).toBe('全选当前结果');
    expect(wrapper.text()).not.toContain('选择本页');
    await button().trigger('click');
    expect(wrapper.emitted('toggle-select-all')).toHaveLength(1);

    await wrapper.setProps({ allMatchingSelected: true });
    expect(button().text()).toBe('取消选择');
    await button().trigger('click');
    expect(wrapper.emitted('toggle-select-all')).toHaveLength(2);

    await wrapper.setProps({ videos: [], allMatchingSelected: false });
    expect(button().element.disabled).toBe(true);
    wrapper.unmount();
  });

  // I-1 / M-2：按钮写出点了会选中的条数（与底部选择操作条同一个数、同一句文案）；补取剩余的页时禁用并写「正在选择…」。
  it('「全选当前结果（N）」写出片库页给的 N；正在补取时禁用并写「正在选择…」', async () => {
    const wrapper = mount(LibraryToolbar, { props: baseProps({ videos: [...rows], selectAllTotal: 20 }) });
    const button = () => wrapper.get('[data-test="toolbar-select-all-matching"]');
    expect(button().text()).toBe('全选当前结果（20）');
    expect(button().element.disabled).toBe(false);

    await wrapper.setProps({ selectingAll: true });
    expect(button().text()).toBe('正在选择…');
    expect(button().element.disabled).toBe(true);
    wrapper.unmount();
  });
});

describe('工具栏三层重排', () => {
  it('所有分类左侧标注、对应标签同时展示，直接跨分类选择和清除', async () => {
    const tags = [
      { id: 1, name: '科幻', namespace: '题材', color: '#111111' },
      { id: 2, name: '悬疑', namespace: '题材', color: '#222222' },
      { id: 3, name: '旅行', namespace: '场景', color: '#333333' },
      { id: 4, name: '自定义', namespace: '', color: '#444444' }
    ];
    // 「全部」是筛选芯片（FilterChip），浅渲染下也要真渲染它才能点。
    const wrapper = mountToolbar({ tags, selectedTags: [3] }, { global: { stubs: { FilterChip: false } } });
    const groups = wrapper.findAll('.tag-category-row');
    expect(groups.map(group => group.get('.tag-category-label__name').text())).toEqual(['场景', '题材', '未分类']);
    expect(groups.map(group => group.findAll('.tag-chip-name').map(chip => chip.text()))).toEqual([
      ['旅行'], ['科幻', '悬疑'], ['自定义']
    ]);
    expect(groups[0].get('.tag-chip-wrap').classes()).toContain('active');
    await groups[1].findAll('.tag-chip-wrap')[0].trigger('click');
    expect(wrapper.emitted('toggle-tag')[0]).toEqual([1]);
    await wrapper.setProps({ selectedTags: [1, 3] });
    expect(wrapper.findAll('.tag-category-label__count').map(label => label.text())).toEqual(['1 · 已选 1', '2 · 已选 1', '1']);
    expect(wrapper.findAll('.tag-chip-wrap.active .tag-chip-name').map(chip => chip.text())).toEqual(['旅行', '科幻']);
    await groups[0].get('.tag-chip-wrap').trigger('click');
    expect(wrapper.emitted('toggle-tag')[1]).toEqual([3]);
    await wrapper.setProps({ selectedTags: [1] });
    expect(wrapper.findAll('.tag-chip-wrap.active .tag-chip-name').map(chip => chip.text())).toEqual(['科幻']);
    await wrapper.get('[data-test="tag-filter-all"] .ui-filter-chip__toggle').trigger('click');
    expect(wrapper.emitted('clear-tags')).toHaveLength(1);
    await wrapper.setProps({ selectedTags: [] });
    expect(wrapper.findAll('.tag-chip-wrap.active')).toHaveLength(0);
    expect(wrapper.get('[data-test="tag-filter-all"] .ui-filter-chip__toggle').attributes('aria-pressed')).toBe('true');
    wrapper.unmount();
  });

  it('单分类仍显示分类名，分类增删后同步展示，空库保留空态', async () => {
    const wrapper = mountToolbar({ tags: [{ id: 1, name: '旅行', namespace: '' }] });
    expect(wrapper.get('.tag-category-label__name').text()).toBe('未分类');
    expect(wrapper.findAll('.tag-chip-name').map(chip => chip.text())).toEqual(['旅行']);
    await wrapper.setProps({ tags: [{ id: 1, name: '旅行', namespace: '' }, { id: 2, name: '悬疑', namespace: '题材' }] });
    expect(wrapper.findAll('.tag-chip-name').map(chip => chip.text())).toEqual(['悬疑', '旅行']);
    await wrapper.setProps({ tags: [{ id: 2, name: '悬疑', namespace: '题材' }] });
    expect(wrapper.findAll('.tag-category-label__name').map(label => label.text())).toEqual(['题材']);
    expect(wrapper.findAll('.tag-chip-name').map(chip => chip.text())).toEqual(['悬疑']);
    await wrapper.setProps({ tags: [] });
    expect(wrapper.findAll('.tag-category-row')).toHaveLength(0);
    expect(wrapper.text()).toContain('暂无可选标签');
    wrapper.unmount();
  });

  // META-14：筛选标签上的 × 实为全局删除，误触就删掉一个标签；删除只留在标签管理里。
  it('META-14 普通分类的大量标签不隐藏，标签上不再有删除 ×，点一下只切换筛选，自动分类不展示', async () => {
    const tags = Array.from({ length: 80 }, (_, index) => ({ id: index + 1, name: `标签 ${index + 1}`, namespace: '题材' }));
    const automaticTag = { id: 81, name: '短视频', namespace: '自动', automatic_kind: 'short_video' };
    const wrapper = mountToolbar({ tags: [...tags, automaticTag] });
    expect(wrapper.findAll('.tag-chip-name')).toHaveLength(80);
    expect(wrapper.findAll('.tag-category-label__name').map(label => label.text())).toEqual(['题材']);
    const chips = wrapper.findAll('.tag-chip-wrap');
    const automaticChip = chips.find(chip => chip.get('.tag-chip-name').text() === '短视频');
    expect(automaticChip).toBeUndefined();
    expect(wrapper.find('.tag-chip-delete').exists()).toBe(false);
    const manualChip = chips.find(chip => chip.get('.tag-chip-name').text() === '标签 1');
    await manualChip.trigger('click');
    expect(wrapper.emitted('toggle-tag')[0]).toEqual([1]);
    expect(wrapper.emitted('delete-tag')).toBeUndefined();
    wrapper.unmount();
  });

  it('只有自动分类时展示空态，已保存的自动标签筛选仍可回显和清除', async () => {
    const wrapper = mountToolbar({
      tags: [{ id: 81, name: '短视频', namespace: '自动', automatic_kind: 'short_video' }],
      selectedTags: [81]
    }, { global: { stubs: { FilterChip: false } } });
    expect(wrapper.findAll('.tag-category-row')).toHaveLength(0);
    expect(wrapper.findAll('.tag-chip-name')).toHaveLength(0);
    expect(wrapper.get('.tag-browser__empty').text()).toBe('暂无可选标签');
    expect(wrapper.get('.result-bar__conditions').text()).toContain('标签 短视频');
    await wrapper.get('[data-test="tag-filter-all"] .ui-filter-chip__toggle').trigger('click');
    expect(wrapper.emitted('clear-tags')).toHaveLength(1);
    wrapper.unmount();
  });

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
      smartView: 'untagged',
      selectedTags: [3],
      selectedSizeRange: { min: 2 * 1024 ** 3, max: 4 * 1024 ** 3 },
      minRating: '6'
    });
    await wrapper.vm.$nextTick();

    const labels = wrapper.vm.activeConditionLabels;
    expect(labels[0]).toBe('未打标签');
    expect(labels).toContain('标签 科幻');
    expect(labels.some(text => text.startsWith('体积'))).toBe(true);
    expect(labels).toContain('评分 6–10');
    wrapper.unmount();
  });

  it('清除条件按钮把复位交给片库页', async () => {
    const wrapper = mount(LibraryToolbar, { props: baseProps({ smartView: 'no_subtitle' }) });
    await wrapper.vm.$nextTick();
    await wrapper.find('.result-bar__clear').trigger('click');
    expect(wrapper.emitted('clear-conditions')).toHaveLength(1);
    wrapper.unmount();
  });

  it('筛选徽标只数收进浮层的三类区间条件', async () => {
    const wrapper = mountToolbar();
    expect(wrapper.vm.activeFilterCount).toBe(0);
    // 智能视图有自己的常驻控件，不该算进徽标，否则徽标和浮层内容对不上。
    await wrapper.setProps({ smartView: 'stale' });
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

  // APP-11 / D-PC59：「AI 标签管理」「清理审阅」并入顶栏的待处理工作台（⌘K 打开清理中心），
  // 菜单只留库维护动作，徽标也一起移走（META-08：待处理数量改看顶栏角标）。
  it('APP-11 管理菜单去掉 AI 标签管理与清理审阅，保留其余维护动作并按四组分开，按钮不再带徽标', async () => {
    const wrapper = mount(LibraryToolbar, {
      props: baseProps({ settings: { local_metadata_enabled: true } })
    });
    const items = wrapper.vm.manageMenuItems;
    expect(items.filter(item => item.heading).map(item => item.heading)).toEqual(['扫描', '整理', '补全', '维护']);
    expect(items.filter(item => item.id).map(item => item.id)).toEqual([
      'scan-new', 'scan-incremental',
      'move-folder', 'rename-folder', 'export-nfo', 'batch-edit-filter',
      'backfill-technical', 'backfill-phash', 'backfill-playback-proxy', 'backfill-local-metadata',
      'tag-manager', 'collection-suggestions', 'version-groups', 'trash'
    ]);
    const manage = wrapper.findAll('button').find(button => button.text().startsWith('管理'));
    expect(manage.exists()).toBe(true);
    expect(manage.find('.ui-badge').exists()).toBe(false);
    wrapper.unmount();
  });

  it('D-MC-04 管理菜单有「批量编辑当前筛选…」，不用先选中；列表不是筛选结果时不给', async () => {
    const wrapper = mountToolbar();
    const item = wrapper.vm.manageMenuItems.find(entry => entry.id === 'batch-edit-filter');
    expect(item.label).toBe('批量编辑当前筛选…');
    wrapper.vm.onManageSelect(item);
    expect(wrapper.emitted('manage-select')[0]).toEqual([item]);
    wrapper.unmount();
    const hidden = mountToolbar({ filterBatchEditAvailable: false });
    expect(hidden.vm.manageMenuItems.some(entry => entry.id === 'batch-edit-filter')).toBe(false);
    hidden.unmount();
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

    // 只有预览自动代理在跑（auto_only，2026-10-13）：不显示进度、不置灰。
    const autoOnly = mountToolbar({ playbackProxy: { running: true, auto_only: true, processed: 0, total: 1 } });
    const autoOnlyItem = autoOnly.vm.manageMenuItems.find(entry => entry.id === 'backfill-playback-proxy');
    expect(autoOnlyItem.label).toBe('为当前筛选生成代理');
    expect(autoOnlyItem.disabled).toBe(false);
    autoOnly.unmount();
  });

  it('建议作品集入口与其他维护动作同级，面板由工具栏自己持有', async () => {
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

  // D-P3-03：片库管理只留管理。随机播放（含随机 N 部）与今晚看什么、观看向的智能视图都拿掉了（去处在观看侧「视频」）。
  it('D-P3-03 没有随机拆分按钮与今晚看什么；智能视图只剩管理向的五个', async () => {
    const wrapper = mount(LibraryToolbar, { props: baseProps() });
    expect(wrapper.find('[data-test="random-play-main"]').exists()).toBe(false);
    expect(wrapper.find('.split-btn').exists()).toBe(false);
    expect(wrapper.text()).not.toContain('随机播放');
    expect(wrapper.text()).not.toContain('今晚看什么');
    const select = wrapper.get('select[aria-label="智能视图"]');
    expect(select.findAll('option').map(option => [option.element.value, option.text()])).toEqual([
      ['', '全部视频'], ['untagged', '未打标签'], ['no_subtitle', '无字幕'], ['local_metadata_updated', '本地资料有更新'], ['stale', '路径失效']
    ]);
    for (const removed of ['continue_watching', 'favorites', 'liked', 'recently_played', 'unwatched', 'watched', 'recently_added']) {
      expect(wrapper.vm.smartViewOptions.some(option => option.value === removed), removed).toBe(false);
    }
    // 「缺少…」照旧。
    expect(wrapper.find('[data-test="library-missing-filter"]').exists()).toBe(true);
    wrapper.unmount();
  });

  it('META-09 智能视图里有「本地资料有更新」', () => {
    const wrapper = mountToolbar({ smartView: 'local_metadata_updated' });
    expect(wrapper.vm.smartViewOptions).toContainEqual({ label: '本地资料有更新', value: 'local_metadata_updated' });
    expect(wrapper.vm.activeConditionLabels).toContain('本地资料有更新');
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

  it('LIB-15 视图菜单提供「用当前条件更新」「重命名」，没有选中视图时禁用', async () => {
    const wrapper = mountToolbar();
    let items = wrapper.vm.viewMenuItems;
    expect(items.find(item => item.id === 'update-current').disabled).toBe(true);
    expect(items.find(item => item.id === 'rename-current').disabled).toBe(true);

    await wrapper.setProps({ savedViews: [{ id: 5, name: '未看 4K' }], selectedSavedViewID: 5 });
    items = wrapper.vm.viewMenuItems;
    expect(items.find(item => item.id === 'update-current')).toEqual(expect.objectContaining({ label: '用当前条件更新该视图', disabled: false }));
    expect(items.find(item => item.id === 'rename-current')).toEqual(expect.objectContaining({ label: '重命名该视图', disabled: false }));
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

  it('搜索模式是 SegmentedControl：当前模式 aria-checked，点别的模式发 set-search-mode', async () => {
    const wrapper = mount(LibraryToolbar, { props: baseProps({ searchMode: 'file' }) });
    await wrapper.vm.$nextTick();
    const group = wrapper.get('[role="radiogroup"][aria-label="搜索模式"]');
    expect(group.findAll('[role="radio"]').map(radio => radio.attributes('data-test'))).toEqual(['search-mode-file', 'search-mode-subtitle', 'search-mode-semantic']);
    expect(wrapper.get('[data-test="search-mode-file"]').attributes('aria-checked')).toBe('true');
    await wrapper.get('[data-test="search-mode-file"]').trigger('click');
    await wrapper.get('[data-test="search-mode-subtitle"]').trigger('click');
    expect(wrapper.emitted('set-search-mode')).toEqual([['subtitle']]);
    wrapper.unmount();
  });
});

describe('人物筛选组合框（META-02）', () => {
  const people = [
    { person: { id: 7, display_name: '张三' } },
    { person: { id: 8, display_name: '李四' } }
  ];

  it('META-02 聚焦即按关键字搜人物，选中后 emit 新的人物列表并清空输入', async () => {
    api.ListPeople.mockResolvedValue(people);
    const wrapper = mount(LibraryToolbar, { props: baseProps() });

    await wrapper.get('[data-test="person-filter-search"]').trigger('focus');
    await flushPromises();
    expect(api.ListPeople).toHaveBeenCalledWith('', '', 0, 20);
    expect(wrapper.findAll('[data-test^="person-filter-option-"]').map(option => option.text())).toEqual(['张三', '李四']);

    await wrapper.get('[data-test="person-filter-option-8"]').trigger('mousedown');
    expect(wrapper.emitted('update:selectedPeople')[0]).toEqual([[{ id: 8, name: '李四' }]]);
    expect(wrapper.vm.personKeyword).toBe('');
    wrapper.unmount();
  });

  it('META-02 已选人物以选中态显示、写进条件回显，点一下取消；候选里不再重复出现', async () => {
    api.ListPeople.mockResolvedValue(people);
    const wrapper = mount(LibraryToolbar, { props: baseProps({ selectedPeople: [{ id: 7, name: '张三' }] }) });

    expect(wrapper.get('[data-test="person-filter-chip-7"]').text()).toContain('张三');
    expect(wrapper.vm.activeConditionLabels).toContain('人物 张三');

    await wrapper.get('[data-test="person-filter-search"]').trigger('focus');
    await flushPromises();
    expect(wrapper.findAll('[data-test^="person-filter-option-"]').map(option => option.text())).toEqual(['李四']);

    await wrapper.get('[data-test="person-filter-chip-7"] .ui-filter-chip__toggle').trigger('click');
    expect(wrapper.emitted('update:selectedPeople')[0]).toEqual([[]]);
    wrapper.unmount();
  });

  it('META-02 键盘上下选、回车添加；人物搜索失败给中文提示', async () => {
    api.ListPeople.mockResolvedValue(people);
    const wrapper = mount(LibraryToolbar, { props: baseProps({ selectedPeople: [{ id: 1, name: '王五' }] }) });
    const input = wrapper.get('[data-test="person-filter-search"]');

    await input.trigger('focus');
    await flushPromises();
    await input.trigger('keydown', { key: 'ArrowDown' });
    await input.trigger('keydown', { key: 'Enter' });
    expect(wrapper.emitted('update:selectedPeople')[0]).toEqual([[{ id: 1, name: '王五' }, { id: 8, name: '李四' }]]);

    api.ListPeople.mockRejectedValue(new Error('db down'));
    await input.trigger('focus');
    await flushPromises();
    expect(wrapper.get('[data-test="person-filter-options"]').text()).toContain('人物搜索失败');
    wrapper.unmount();
  });
});

describe('待处理工作台的跳转目标（APP-11）', () => {
  it('APP-11 工具栏注册 library.openCollectionSuggestions 等固定命令，只作跳转目标、不在面板里重复出现', async () => {
    const wrapper = mountToolbar();

    expect(commandList().map(command => command.id)).not.toContain('library.openCollectionSuggestions');
    findCommand('library.openCollectionSuggestions').run();
    await wrapper.vm.$nextTick();
    expect(wrapper.vm.collectionSuggestionOpen).toBe(true);

    findCommand('library.openTrash').run();
    expect(wrapper.emitted('manage-select')[0]).toEqual([{ id: 'trash' }]);
    findCommand('library.openTagManager').run();
    expect(wrapper.emitted('open-tag-manager')).toHaveLength(1);

    wrapper.unmount();
    expect(findCommand('library.openCollectionSuggestions')).toBeNull();
  });

  it('管理菜单发来的是菜单项对象时，「建议作品集」同样由工具栏自己打开', async () => {
    const wrapper = mountToolbar();
    wrapper.vm.onManageSelect({ id: 'collection-suggestions', label: '建议作品集' });
    await wrapper.vm.$nextTick();
    expect(wrapper.vm.collectionSuggestionOpen).toBe(true);
    expect(wrapper.emitted('manage-select')).toBeUndefined();

    wrapper.vm.onManageSelect({ id: 'trash', label: '回收站' });
    expect(wrapper.emitted('manage-select')[0]).toEqual([{ id: 'trash', label: '回收站' }]);
    wrapper.unmount();
  });
});

describe('场景检索入口（P-007）', () => {
  it('「在当前筛选中搜场景」只发事件，筛选由片库页带上', async () => {
    const wrapper = mountToolbar();
    await wrapper.get('[data-test="toolbar-search-scenes"]').trigger('click');
    expect(wrapper.emitted('search-scenes')).toHaveLength(1);
  });
});

// D-P3-03：片库管理的布局只有列表与网格（缩略图墙拿掉了，浏览海报去观看侧「视频」），偏好仍记在同一个键里。
describe('片库布局切换', () => {
  let store;
  let original;
  beforeEach(() => {
    store = {};
    original = Object.getOwnPropertyDescriptor(window, 'localStorage');
    Object.defineProperty(window, 'localStorage', {
      configurable: true,
      value: { getItem: key => store[key] ?? null, setItem: (key, value) => { store[key] = String(value); }, removeItem: key => { delete store[key]; } }
    });
  });
  afterEach(() => {
    if (original) Object.defineProperty(window, 'localStorage', original);
    else delete window.localStorage;
  });

  it('列表 / 网格两段，点哪段记住选择并通知片库页；没有缩略图墙', async () => {
    // 布局是同一内容的不同看法，用统一的 SegmentedControl（role=radiogroup，选中项 aria-checked）。
    const wrapper = mount(LibraryToolbar, { props: baseProps({ viewMode: 'grid' }) });
    const group = wrapper.get('[data-test="library-layout"]');
    expect(group.attributes('role')).toBe('radiogroup');
    expect(group.attributes('aria-label')).toBe('片库布局');
    const buttons = () => group.findAll('[role="radio"]');
    expect(buttons().map(button => button.text())).toEqual(['列表', '网格']);
    expect(buttons().map(button => button.attributes('aria-checked'))).toEqual(['false', 'true']);

    await buttons()[0].trigger('click');
    expect(store['cineinsight-library-layout']).toBe('list');
    expect(wrapper.emitted('update:viewMode')).toEqual([['list']]);
    await wrapper.setProps({ viewMode: 'list' });
    await buttons()[1].trigger('click');
    expect(store['cineinsight-library-layout']).toBe('grid');
    expect(wrapper.emitted('update:viewMode')[1]).toEqual(['grid']);
    wrapper.unmount();
  });

  it('未知布局按列表处理，偏好里也只记列表', () => {
    const wrapper = mountToolbar();
    wrapper.vm.setViewMode('mosaic');
    expect(store['cineinsight-library-layout']).toBe('list');
    expect(wrapper.emitted('update:viewMode')).toEqual([['list']]);
    wrapper.unmount();
  });
});

describe('缺失某项筛选（D-MC-03）', () => {
  it('「缺少…」下拉列出视频项，选中后发 update:missing 并立即搜索', async () => {
    const wrapper = mount(LibraryToolbar, { props: baseProps() });
    const select = wrapper.get('[data-test="library-missing-filter"]');
    const values = select.findAll('option').map(option => option.element.value);
    expect(values).toEqual(['', 'title', 'year', 'description', 'poster', 'fanart', 'people', 'tags', 'subtitles', 'technical']);
    await select.setValue('poster');
    expect(wrapper.emitted('update:missing')).toEqual([['poster']]);
    expect(wrapper.emitted('search')).toEqual([[true]]);
    wrapper.unmount();
  });

  it('生效的缺失条件写进结果条回显', async () => {
    const wrapper = mount(LibraryToolbar, { props: baseProps({ missing: 'year', smartView: 'untagged' }) });
    expect(wrapper.get('[data-test="library-missing-filter"]').element.value).toBe('year');
    expect(wrapper.vm.activeConditionLabels).toEqual(['未打标签', '缺少年份']);
    expect(wrapper.get('.result-bar__conditions').text()).toContain('缺少年份');
    wrapper.unmount();
  });
});
