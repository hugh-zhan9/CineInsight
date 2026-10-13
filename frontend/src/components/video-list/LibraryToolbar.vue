<template>
    <!-- 工具栏与结果条/批量栏一起吸顶：选中若干项后向下滚动时，批量按钮
         必须保持可见，否则要滚回顶部才能操作。 -->
    <div class="library-chrome">
    <!-- 工具栏按频率分三层（原型 A1）：第一行是跟着查询走的常驻控件，
         区间类条件收进「筛选」浮层，12 个库维护动作收进「管理」菜单，
         第二行放跟着列表内容走的标签筛选与选择动作。 -->
    <div class="toolbar">
      <div class="toolbar-row">
        <!-- 搜索模式（D-UD-06 SegmentedControl）：每个选项带 search-mode-* 钩子；语义能力不可用时置灰并说明原因。 -->
        <SegmentedControl
          class="search-mode"
          label="搜索模式"
          :options="searchModeSegments"
          :model-value="searchMode"
          @update:model-value="$emit('set-search-mode', $event)"
        />

        <div class="search-field">
          <input
            ref="searchInput"
            v-model="searchKeywordModel"
            @input="$emit('search', false, true)"
            @keyup.enter="$emit('search', true, true)"
            type="text"
            :placeholder="searchPlaceholder"
            class="search-field__input"
            aria-label="搜索"
          />
          <kbd class="search-field__hint">⌘F</kbd>
        </div>

        <select v-model="smartViewModel" @change="$emit('search', true)" class="select-input toolbar-control" aria-label="智能视图">
          <option v-for="view in smartViewOptions" :key="view.value" :value="view.value">{{ view.label }}</option>
        </select>

        <!-- 「缺失某项」（D-MC-03）：与智能视图正交，AND 叠加；完整度页的「去处理」也落在这里。 -->
        <select v-model="missingModel" @change="$emit('search', true)" class="select-input toolbar-control" aria-label="缺少某项" data-test="library-missing-filter">
          <option value="">缺少…</option>
          <option v-for="option in missingOptions" :key="option.value" :value="option.value">缺少{{ option.label }}</option>
        </select>

        <button
          ref="filterTrigger"
          type="button"
          :class="['btn', 'btn--secondary', 'toolbar-trigger', { 'toolbar-trigger--on': activeFilterCount > 0 }]"
          @click="toggleToolbarMenu('filter', 'filterTrigger')"
        >
          筛选
          <AppBadge v-if="activeFilterCount > 0" tone="accent" :count="activeFilterCount" />
          <AppIcon class="toolbar-trigger__caret" name="chevron-down" :size="16" />
        </button>

        <select v-model="sortModeModel" @change="$emit('search', true)" class="select-input toolbar-control" aria-label="排序方式">
          <option value="balanced">均衡排序</option>
          <option value="rating_desc">评分从高到低</option>
          <option value="rating_asc">评分从低到高</option>
        </select>

        <!-- 随机与「今晚看什么」合并（2026-10-11）：主键按当前随机条件立即播放一部，▾ 里打开今晚看什么、
             随机 N 部和条件开关；主键带上生效的条件（PLAY-08）。语义模式下随机会忽略搜索词、从全库抽，所以禁用并说明原因。 -->
        <div class="split-btn">
          <button
            type="button"
            class="btn btn--ghost split-btn__main"
            data-test="random-play-main"
            :disabled="randomDisabled"
            :title="randomDisabled ? randomDisabledReason : '在当前片库筛选内按随机条件立即播放一部，播放越少越容易被抽到'"
            @click="$emit('play-random')"
          >{{ randomButtonLabel }}</button>
          <span class="split-btn__divider" aria-hidden="true"></span>
          <button
            ref="randomTrigger"
            type="button"
            class="btn-icon split-btn__caret"
            aria-label="随机选项"
            @click="toggleToolbarMenu('random', 'randomTrigger')"
          ><AppIcon name="chevron-down" :size="16" /></button>
        </div>

        <button type="button" class="btn btn--secondary toolbar-trigger" data-test="toolbar-search-scenes" title="带上当前筛选条件进入场景检索（语义搜索词不带入）" @click="$emit('search-scenes')">在当前筛选中搜场景</button>

        <button
          ref="viewTrigger"
          type="button"
          :class="['btn', 'btn--secondary', 'toolbar-trigger', { 'toolbar-trigger--on': selectedSavedViewID > 0 }]"
          @click="toggleToolbarMenu('view', 'viewTrigger')"
        >{{ selectedSavedViewName || '视图' }}<AppIcon class="toolbar-trigger__caret" name="chevron-down" :size="16" /></button>

        <SegmentedControl
          label="片库布局"
          :options="layoutOptions"
          :model-value="viewMode"
          data-test="library-layout"
          @update:model-value="setViewMode"
        />

        <button
          ref="manageTrigger"
          type="button"
          class="btn btn--secondary toolbar-trigger toolbar-trigger--strong"
          @click="toggleToolbarMenu('manage', 'manageTrigger')"
        >
          管理
          <AppIcon class="toolbar-trigger__caret" name="chevron-down" :size="16" />
        </button>
      </div>

      <div class="toolbar-row toolbar-row--tags">
        <div class="tag-browser">
          <div class="tags-wrap">
            <span class="toolbar-row__label">标签</span>
            <FilterChip class="tag-all-chip" label="全部" :active="selectedTags.length === 0" data-test="tag-filter-all" @toggle="$emit('clear-tags')" />
            <span v-if="tagGroups.length === 0" class="tag-browser__empty">暂无可选标签</span>
            <div class="tag-browser__actions">
              <button type="button" class="btn btn--secondary btn--sm" @click="$emit('open-tag-manager')">标签管理</button>
              <!-- 选择（D-UD-10）：「全选当前结果（N）」与底部选择操作条是同一个范围、同一句文案（N = 点了会选中的条数，
                   片库页的 selectAllScope）；筛选结果会先把没加载的页取回来，期间写「正在选择…」。全选之后同一个位置是「取消选择」。 -->
              <button
                type="button"
                class="btn btn--secondary btn--sm"
                data-test="toolbar-select-all-matching"
                :disabled="videos.length === 0 || selectingAll"
                @click="$emit('toggle-select-all')"
              >{{ selectAllButtonLabel }}</button>
            </div>
          </div>
          <!-- 人物筛选（D-PC33、META-02）：多选取交集，选中项与标签一样点一下取消；筛选条件归片库页持有，
               这里只把选中的人物 emit 出去。 -->
          <div class="person-filter-row" role="group" aria-label="人物筛选" data-test="person-filter">
            <div class="person-filter-label">
              <span class="person-filter-label__name">人物</span>
              <span v-if="selectedPeople.length" class="person-filter-label__count">已选 {{ selectedPeople.length }}</span>
            </div>
            <div class="tags-wrap">
              <!-- 已选人物（D-UD-06 筛选芯片）：点芯片或 × 都取消这个人物的筛选。 -->
              <FilterChip
                v-for="person in selectedPeople"
                :key="person.id"
                :label="personName(person)"
                active
                removable
                :data-test="`person-filter-chip-${person.id}`"
                @toggle="removePersonFilter(person.id)"
                @remove="removePersonFilter(person.id)"
              />
              <div class="person-filter-combobox">
                <input
                  v-model="personKeyword"
                  type="search"
                  class="search-input person-filter-input"
                  placeholder="搜索人物，回车添加"
                  role="combobox"
                  autocomplete="off"
                  aria-label="搜索人物"
                  aria-controls="library-person-filter-options"
                  :aria-expanded="personMenuOpen"
                  data-test="person-filter-search"
                  @focus="openPersonMenu"
                  @input="onPersonInput"
                  @keydown="onPersonKeydown"
                  @blur="closePersonMenu"
                />
                <div v-if="personMenuOpen" id="library-person-filter-options" class="person-filter-options" role="listbox" data-test="person-filter-options">
                  <button
                    v-for="(item, index) in personOptions"
                    :key="item.person.id"
                    type="button"
                    role="option"
                    :aria-selected="index === personActiveIndex"
                    :class="['btn-link', 'person-filter-option', { active: index === personActiveIndex }]"
                    :data-test="`person-filter-option-${item.person.id}`"
                    @mouseenter="personActiveIndex = index"
                    @mousedown.prevent="addPersonFilter(item)"
                  >{{ item.person.display_name }}</button>
                  <span v-if="personOptions.length === 0" class="person-filter-options__empty">{{ personSearchError || '没有匹配的人物' }}</span>
                </div>
              </div>
            </div>
          </div>
          <div v-for="group in tagGroups" :key="group.key" class="tag-category-row" role="group" :aria-label="group.label">
            <div class="tag-category-label">
              <span class="tag-category-label__name">{{ group.label }}</span>
              <span class="tag-category-label__count">{{ group.tags.length }}<span v-if="groupSelectedCount(group)"> · 已选 {{ groupSelectedCount(group) }}</span></span>
            </div>
            <div class="tags-wrap">
              <div
                v-for="tag in group.tags"
                :key="tag.id"
                class="tag-chip tag-chip-wrap"
                :class="{ active: isTagSelected(tag.id) }"
                :style="{ backgroundColor: tagBgColor(tag.color) }"
                @click="$emit('toggle-tag', tag.id)"
              >
                <span class="tag-chip-name">{{ tag.name }}</span>
                <AppIcon v-if="isTagSelected(tag.id)" class="tag-chip-check" name="check" :size="16" />
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- 结果条：筛选命中数、生效条件回显、一键清除与行高档位。批量操作条（D-UD-10）浮在内容区底部，由片库页放置，
         有选中时结果条照常显示。 -->
    <div class="result-bar">
      <span class="result-bar__count">
        筛选出 <b>{{ filteredCountText }}</b>
        <span v-if="libraryTotalCount !== null"> / {{ formatCount(libraryTotalCount) }}</span>
      </span>
      <template v-if="activeConditionLabels.length > 0">
        <span class="result-bar__sep">|</span>
        <span class="result-bar__conditions">条件：{{ activeConditionLabels.join(' · ') }}</span>
        <button type="button" class="btn-link result-bar__clear" @click="$emit('clear-conditions')">清除</button>
      </template>
      <span v-else class="result-bar__conditions">未设置筛选条件</span>
      <div class="result-bar__spacer"></div>
      <!-- 多版本聚合（D-MW-VERSIONS）：开时每个版本组只出一张代表卡片，关时与逐个文件的视图完全一致。 -->
      <label class="result-bar__toggle" data-test="collapse-versions-toggle" title="把同一作品的多个版本合并成一张卡片">
        <input type="checkbox" :checked="collapseVersions" @change="$emit('update:collapseVersions', $event.target.checked)" />
        合并版本
      </label>
      <span class="result-bar__label">行高</span>
      <SegmentedControl
        class="result-bar__density"
        label="行高"
        :options="densityOptions"
        :model-value="rowDensity"
        data-test="row-density"
        @update:model-value="setRowDensity"
      />
    </div>
    </div>

    <!-- 筛选浮层：区间类条件先改草稿，「应用」才提交，按钮上的数字是草稿的预览计数 -->
    <BasePopover
      v-if="toolbarMenu === 'filter'"
      :anchor="toolbarMenuAnchor"
      :min-width="392"
      panel-class="filter-popover"
      @close="closeToolbarMenu"
    >
      <div class="filter-popover__head">
        <strong>筛选条件</strong>
        <div class="filter-popover__spacer"></div>
        <button type="button" class="btn-link" @click="clearFilterDraft">全部清除</button>
      </div>
      <label class="filter-popover__field">
        <span>体积区间</span>
        <select v-model="filterDraft.sizeRange" class="select-input" @change="scheduleFilterPreview">
          <option value="all">不限</option>
          <option v-for="opt in sizeOptions" :key="opt.label" :value="opt.value">{{ opt.label }}</option>
        </select>
      </label>
      <label class="filter-popover__field">
        <span>分辨率区间</span>
        <select v-model="filterDraft.resRange" class="select-input" @change="scheduleFilterPreview">
          <option value="all">不限</option>
          <option v-for="opt in resOptions" :key="opt.label" :value="opt.value">{{ opt.label }}</option>
        </select>
      </label>
      <div class="filter-popover__field">
        <span>评分区间（0–10，半分制）</span>
        <div class="filter-popover__range">
          <input v-model="filterDraft.minRating" @input="scheduleFilterPreview" type="text" inputmode="decimal" maxlength="4" class="text-input" placeholder="最低" aria-label="最低评分" />
          <span class="filter-popover__dash">–</span>
          <input v-model="filterDraft.maxRating" @input="scheduleFilterPreview" type="text" inputmode="decimal" maxlength="4" class="text-input" placeholder="最高" aria-label="最高评分" />
        </div>
      </div>
      <div class="filter-popover__divider"></div>
      <div class="filter-popover__actions">
        <button type="button" class="btn btn--secondary" @click="$emit('open-save-view')">存为视图</button>
        <button type="button" class="btn btn--primary" @click="applyFilterDraft">应用{{ filterPreviewText }}</button>
      </div>
    </BasePopover>

    <BaseMenu
      v-if="toolbarMenu === 'manage'"
      :anchor="toolbarMenuAnchor"
      :items="manageMenuItems"
      :min-width="268"
      align="end"
      label="片库管理"
      @select="onManageSelect"
      @close="closeToolbarMenu"
    />
    <BaseMenu
      v-if="toolbarMenu === 'view'"
      :anchor="toolbarMenuAnchor"
      :items="viewMenuItems"
      :min-width="240"
      label="保存视图"
      @select="$emit('view-select', $event)"
      @close="closeToolbarMenu"
    />
    <BaseMenu
      v-if="toolbarMenu === 'random'"
      :anchor="toolbarMenuAnchor"
      :items="randomMenuItems"
      :min-width="200"
      align="end"
      label="随机选项"
      @select="$emit('random-select', $event)"
      @close="closeToolbarMenu"
    />

    <!-- 建议作品集面板与清理审阅同级：入口在管理菜单里，面板本身由工具栏持有
         （它已经持有若干浮层），片库页不用为它加一份状态。 -->
    <CollectionSuggestionPanel
      v-if="collectionSuggestionOpen"
      @close="collectionSuggestionOpen = false"
      @confirmed="collectionSuggestionConfirmed"
    />
</template>

<script>
import { CountLibraryVideos, ListPeople } from '../../../wailsjs/go/main/App';
import AppBadge from '../ui/AppBadge.vue';
import AppIcon from '../ui/AppIcon.vue';
import BaseMenu from '../ui/BaseMenu.vue';
import BasePopover from '../ui/BasePopover.vue';
import FilterChip from '../ui/FilterChip.vue';
import SegmentedControl from '../ui/SegmentedControl.vue';
import { selectAllMatchingLabel } from '../ui/SelectionBar.vue';
import CollectionSuggestionPanel from '../CollectionSuggestionPanel.vue';
import { GetCollectionSuggestionStatus } from '../../../wailsjs/go/main/App';
import { runtimeEventsMixin } from './runtimeEvents.js';
import { logFrontend } from '../../utils/frontendLog.js';
import { registerCommands, unregisterCommands } from '../../utils/commandRegistry.js';

import { normalizeRandomConditions, randomConditionTags } from '../../utils/randomPlayback.js';
import { normalizeLibraryLayout } from '../../utils/virtualList.js';
import { VIDEO_MISSING_OPTIONS } from '../../utils/metadataCompleteness.js';

// 人物组合框一次最多列出的候选数。
const PERSON_OPTION_LIMIT = 20;

// 吸顶的片库工具栏：三层控件、标签行与结果条，外加筛选浮层与三个下拉菜单（批量操作条在片库页的内容区底部）。
// 查询条件本身仍归 VideoListPage 持有，这里只负责呈现与把动作发回去；
// 浮层里的区间草稿是纯 UI 状态，留在本组件内，「应用」时才把结果 emit 出去。
export default {
  name: 'LibraryToolbar',
  components: { AppBadge, AppIcon, BaseMenu, BasePopover, CollectionSuggestionPanel, FilterChip, SegmentedControl },
  mixins: [runtimeEventsMixin],
  props: {
    tags: { type: Array, default: () => [] },
    videos: { type: Array, default: () => [] },
    // 当前结果是否已全选（D-UD-10）：决定标签行那个选择按钮写「全选当前结果」还是「取消选择」。
    allMatchingSelected: { type: Boolean, default: false },
    // 「全选当前结果（N）」的 N（片库页 selectAllScope.total，与选择操作条同一个数）；不知道时为 null，不带括号。
    selectAllTotal: { type: Number, default: null },
    // 片库页正在为「全选当前结果」补取剩余的页。
    selectingAll: { type: Boolean, default: false },
    searchKeyword: { type: String, default: '' },
    searchMode: { type: String, default: 'file' },
    smartView: { type: String, default: '' },
    // 「缺失某项」筛选（D-MC-03），取值见 VIDEO_MISSING_OPTIONS；空串不筛。
    missing: { type: String, default: '' },
    sortMode: { type: String, default: 'balanced' },
    viewMode: { type: String, default: 'list' },
    rowDensity: { type: String, default: 'compact' },
    selectedTags: { type: Array, default: () => [] },
    // 人物筛选的选中项 [{ id, name }]（D-PC33）。真值在片库页，这里只显示并 emit update:selectedPeople。
    selectedPeople: { type: Array, default: () => [] },
    selectedSizeRange: { type: [String, Object], default: 'all' },
    selectedResRange: { type: [String, Object], default: 'all' },
    minRating: { type: String, default: '' },
    maxRating: { type: String, default: '' },
    sizeOptions: { type: Array, default: () => [] },
    resOptions: { type: Array, default: () => [] },
    savedViews: { type: Array, default: () => [] },
    selectedSavedViewID: { type: [Number, String], default: 0 },
    // 随机播放、随机 N 部与今晚看什么共用的条件：{ watchState, favoritesOnly, maxMinutes }。
    randomConditions: { type: Object, default: () => ({}) },
    randomPickLoading: { type: Boolean, default: false },
    randomPickSize: { type: Number, default: 10 },
    semanticAvailable: { type: Boolean, default: true },
    semanticUnavailableNotice: { type: String, default: '' },
    filteredCount: { type: Number, default: null },
    libraryTotalCount: { type: Number, default: null },
    hasMore: { type: Boolean, default: true },
    migrationRunning: { type: Boolean, default: false },
    incrementalScan: { type: Object, required: true },
    directories: { type: Array, default: () => [] },
    settings: { type: Object, required: true },
    technicalBackfill: { type: Object, required: true },
    perceptualHash: { type: Object, required: true },
    localMetadataBackfill: { type: Object, required: true },
    localMetadataExport: { type: Object, required: true },
    // 播放代理任务状态（D-006）：管理菜单里的「为当前筛选生成代理」跑起来后要显示进度。
    playbackProxy: { type: Object, default: () => ({ running: false, processed: 0, total: 0 }) },
    // 标签底色的换算与筛选 DTO 的构造都还在片库页，这里按需调用。
    tagBgColor: { type: Function, required: true },
    libraryFilterFrom: { type: Function, required: true },
    // 管理菜单「批量编辑当前筛选…」（D-MC-04）：列表不是筛选结果（语义检索、随机批次、相似视频）时不给。
    filterBatchEditAvailable: { type: Boolean, default: true },
    // 「合并版本」开关（默认开，本机记住）；真值在片库页。
    collapseVersions: { type: Boolean, default: true }
  },
  emits: [
    'search-scenes',
    'update:searchKeyword', 'update:smartView', 'update:missing', 'update:sortMode', 'update:viewMode', 'update:rowDensity',
    'update:selectedPeople',
    'search', 'set-search-mode', 'play-random', 'toggle-tag', 'clear-tags', 'open-tag-manager',
    'toggle-select-all', 'clear-conditions', 'apply-filter', 'open-save-view',
    'manage-select', 'view-select', 'random-select',
    'update:collapseVersions'
  ],
  data() {
    return {
      searchModeOptions: [
        { label: '文件', value: 'file' },
        { label: '字幕', value: 'subtitle' },
        { label: '语义', value: 'semantic' }
      ],
      smartViewOptions: [
        { label: '全部视频', value: '' },
        { label: '继续观看', value: 'continue_watching' },
        { label: '收藏', value: 'favorites' },
        { label: '点赞', value: 'liked' },
        { label: '最近播放', value: 'recently_played' },
        { label: '未看', value: 'unwatched' },
        { label: '已看', value: 'watched' },
        { label: '最近添加', value: 'recently_added' },
        { label: '未打标签', value: 'untagged' },
        { label: '无字幕', value: 'no_subtitle' },
        { label: '本地资料有更新', value: 'local_metadata_updated' },
        { label: '路径失效', value: 'stale' }
      ],
      missingOptions: VIDEO_MISSING_OPTIONS,
      layoutOptions: [
        { value: 'list', label: '列表' },
        { value: 'grid', label: '网格' },
        { value: 'wall', label: '缩略图墙', title: '只显示缩略图的紧凑平铺，标题悬停可见，点一下打开预览' }
      ],
      densityOptions: [
        { value: 'compact', label: '紧凑' },
        { value: 'comfortable', label: '舒适' }
      ],
      toolbarMenu: null,
      toolbarMenuAnchor: null,
      filterDraft: { sizeRange: 'all', resRange: 'all', minRating: '', maxRating: '' },
      filterPreviewCount: null,
      filterPreviewTimer: null,
      collectionSuggestionOpen: false,
      collectionSuggestionAnalyzing: false,
      personKeyword: '',
      personCandidates: [],
      personMenuOpen: false,
      personActiveIndex: 0,
      personSearchError: ''
    };
  },
  mounted() {
    // 「建议作品集」的运行态没有从片库页传进来的 prop，工具栏自己取一次并跟事件走：
    // 分析在后台跑，菜单上得看得出它正在跑。
    this.refreshCollectionSuggestionStatus();
    this.registerRuntimeEvent('collection-suggestion-state', (data) => {
      this.collectionSuggestionAnalyzing = Boolean(data?.running);
    });
    // 待处理工作台与 App 全局命令的跳转目标（详细设计 §6.1 的固定命令 ID）。工具栏常挂载在片库页里，
    // 这几个面板与弹窗也都从这里打开；hidden：面板里另有 App 注册的同名全局命令，这里只作跳转目标。
    registerCommands('library-toolbar', [
      {
        id: 'library.openCollectionSuggestions',
        group: 'action',
        label: '打开建议作品集',
        keywords: ['collection', '建议作品集'],
        hidden: true,
        run: () => { this.collectionSuggestionOpen = true; }
      },
      {
        id: 'library.openTrash',
        group: 'action',
        label: '打开回收站',
        keywords: ['trash', '回收站'],
        hidden: true,
        run: () => { this.$emit('manage-select', { id: 'trash' }); }
      },
      {
        id: 'library.openTagManager',
        group: 'action',
        label: '打开标签管理',
        keywords: ['tag', '标签管理'],
        hidden: true,
        run: () => { this.$emit('open-tag-manager'); }
      }
    ]);
  },
  beforeUnmount() {
    unregisterCommands('library-toolbar');
    clearTimeout(this._personSearchTimer);
  },
  computed: {
    selectAllButtonLabel() {
      if (this.allMatchingSelected && !this.selectingAll) return '取消选择';
      return selectAllMatchingLabel(this.selectAllTotal, this.selectingAll);
    },
    // 搜索模式的分段选项：语义能力不可用时那一项置灰，悬停说明原因。
    searchModeSegments() {
      return this.searchModeOptions.map(mode => {
        const unavailable = mode.value === 'semantic' && !this.semanticAvailable;
        return {
          ...mode,
          disabled: unavailable,
          title: unavailable ? this.semanticUnavailableNotice : '',
          dataTest: `search-mode-${mode.value}`
        };
      });
    },
	tagGroups() {
	  const groups = new Map();
	  for (const tag of this.tags) {
		const key = String(tag.namespace || '').trim();
		if (key === '自动') continue;
		if (!groups.has(key)) groups.set(key, { key, label: key || '未分类', tags: [] });
		groups.get(key).tags.push(tag);
	  }
	  return [...groups.values()].sort((a, b) => {
		if (!a.key) return 1;
		if (!b.key) return -1;
		return a.label.localeCompare(b.label, 'zh-Hans-CN');
	  });
	},
    // 查询条件归片库页持有，这里用 getter/setter 计算属性接回 v-model：
    // 必须是真的 v-model，否则中文输入法的组词保护（el.composing）就没了。
    searchKeywordModel: {
      get() { return this.searchKeyword; },
      set(value) { this.$emit('update:searchKeyword', value); }
    },
    smartViewModel: {
      get() { return this.smartView; },
      set(value) { this.$emit('update:smartView', value); }
    },
    missingModel: {
      get() { return this.missing; },
      set(value) { this.$emit('update:missing', value); }
    },
    sortModeModel: {
      get() { return this.sortMode; },
      set(value) { this.$emit('update:sortMode', value); }
    },
    searchPlaceholder() {
      if (this.searchMode === 'subtitle') return '搜索字幕内容…';
      if (this.searchMode === 'semantic') return '用自然语言描述想找的内容，回车搜索…';
      return '搜索标题、文件名或路径…';
    },
    selectedSavedViewName() {
      return this.savedViews.find(view => view.id === Number(this.selectedSavedViewID))?.name || '';
    },
    // 「筛选」按钮徽标只数收进浮层的那三类区间条件；智能视图有自己的常驻控件，
    // 数进来会让徽标和浮层里看到的内容对不上。
    activeFilterCount() {
      let count = 0;
      if (this.selectedSizeRange !== 'all') count += 1;
      if (this.selectedResRange !== 'all') count += 1;
      if (this.minRating !== '' || this.maxRating !== '') count += 1;
      return count;
    },
    // 结果条的条件回显：智能视图 + 三类区间 + 标签，用中文写全，
    // 免得折叠之后用户不知道自己还开着什么条件。
    activeConditionLabels() {
      const labels = [];
      if (this.smartView) {
        const view = this.smartViewOptions.find(option => option.value === this.smartView);
        if (view) labels.push(view.label);
      }
      if (this.missing) {
        const option = this.missingOptions.find(item => item.value === this.missing);
        if (option) labels.push(`缺少${option.label}`);
      }
      if (this.selectedTags.length > 0) {
        const names = this.selectedTags
          .map(id => this.tags.find(tag => tag.id === id)?.name)
          .filter(Boolean);
        if (names.length > 0) labels.push(`标签 ${names.join('、')}`);
      }
      if (this.selectedPeople.length > 0) {
        labels.push(`人物 ${this.selectedPeople.map(person => this.personName(person)).join('、')}`);
      }
      // 按 min/max 取值比对，不比对象引用：保存视图恢复出来的区间是新对象。
      const rangeLabel = (options, range) => options.find(
        item => item.value.min === range.min && item.value.max === range.max
      )?.label;
      if (this.selectedSizeRange !== 'all') {
        const label = rangeLabel(this.sizeOptions, this.selectedSizeRange);
        if (label) labels.push(`体积 ${label}`);
      }
      if (this.selectedResRange !== 'all') {
        const label = rangeLabel(this.resOptions, this.selectedResRange);
        if (label) labels.push(`分辨率 ${label}`);
      }
      if (this.minRating !== '' || this.maxRating !== '') {
        labels.push(`评分 ${this.minRating === '' ? '0' : this.minRating}–${this.maxRating === '' ? '10' : this.maxRating}`);
      }
      return labels;
    },
    filteredCountText() {
      // 语义搜索的命中数由检索接口决定，不能用结构化筛选计数冒充。
      if (this.searchMode === 'semantic') return `${this.formatCount(this.videos.length)}${this.hasMore ? '+' : ''}`;
      if (this.filteredCount === null) return '—';
      return this.formatCount(this.filteredCount);
    },
    filterPreviewText() {
      return this.filterPreviewCount === null ? '' : `（${this.formatCount(this.filterPreviewCount)}）`;
    },
    // 「管理」菜单（D-PC59）：「AI 标签管理」「清理审阅」与菜单徽标已并入顶栏的待处理工作台
    // （⌘⇧K 打开清理中心），这里只留扫描、整理、补全与标签管理等库维护动作（APP-11）。
    manageMenuItems() {
      const items = [
        { heading: '扫描' },
        { id: 'scan-new', label: '扫描新目录', shortcut: '⇧⌘N', disabled: this.migrationRunning },
        {
          id: 'scan-incremental',
          label: this.incrementalScan.running ? '增量扫描（进行中）' : '增量扫描',
          shortcut: '⌘R',
          disabled: this.migrationRunning || this.incrementalScan.running || this.directories.length === 0
        },
        { heading: '整理' },
        { id: 'move-folder', label: this.migrationRunning ? '迁移文件夹（进行中）' : '迁移文件夹', disabled: this.migrationRunning },
        { id: 'rename-folder', label: this.migrationRunning ? '重命名文件夹（进行中）' : '重命名文件夹', disabled: this.migrationRunning },
        {
          id: 'export-nfo',
          label: this.localMetadataExport.running ? `当前筛选写出 NFO（${this.localMetadataExport.processed}/${this.localMetadataExport.total}）` : '当前筛选写出 NFO',
          disabled: this.localMetadataExport.running
        },
        ...(this.filterBatchEditAvailable ? [{ id: 'batch-edit-filter', label: '批量编辑当前筛选…' }] : []),
        { heading: '补全' },
        {
          id: 'backfill-technical',
          label: this.technicalBackfill.running ? `补全技术信息（${this.technicalBackfill.processed}/${this.technicalBackfill.total}）` : '补全技术信息',
          disabled: this.technicalBackfill.running
        },
        {
          id: 'backfill-phash',
          label: this.perceptualHash.running ? `补全近重复指纹（${this.perceptualHash.processed}/${this.perceptualHash.total}）` : '补全近重复指纹',
          disabled: this.perceptualHash.running
        },
        {
          // 只有预览自动代理在跑（auto_only）时既不置灰也不显示进度：那是播放器自己的事（2026-10-13）。
          id: 'backfill-playback-proxy',
          label: this.playbackProxy.running && !this.playbackProxy.auto_only
            ? `为当前筛选生成代理（${this.playbackProxy.processed}/${this.playbackProxy.total}）`
            : '为当前筛选生成代理',
          disabled: !!this.playbackProxy.running && !this.playbackProxy.auto_only
        }
      ];
      if (this.settings.local_metadata_enabled) {
        items.push({
          id: 'backfill-local-metadata',
          label: this.localMetadataBackfill.running ? `补全本地资料（${this.localMetadataBackfill.processed}/${this.localMetadataBackfill.total}）` : '补全本地资料',
          disabled: this.localMetadataBackfill.running
        });
      }
      items.push(
        { heading: '维护' },
        { id: 'tag-manager', label: '标签管理' },
        { id: 'collection-suggestions', label: this.collectionSuggestionAnalyzing ? '建议作品集（分析中）' : '建议作品集' },
        { id: 'version-groups', label: '版本组' },
        { id: 'trash', label: '回收站' }
      );
      return items;
    },
    viewMenuItems() {
      const items = this.savedViews.map(view => ({
        id: `saved:${view.id}`,
        label: view.name,
        checked: Number(this.selectedSavedViewID) === Number(view.id)
      }));
      if (items.length === 0) items.push({ id: 'none', label: '还没有保存的视图', disabled: true });
      items.push({ divider: true });
      items.push({ id: 'save-current', label: '保存当前视图' });
      // 选中了某个视图时才能「用当前条件更新」或「重命名」（D-PC35、LIB-15），由片库页打开同一个弹窗的不同模式。
      items.push({ id: 'update-current', label: '用当前条件更新该视图', disabled: !this.selectedSavedViewID });
      items.push({ id: 'rename-current', label: '重命名该视图', disabled: !this.selectedSavedViewID });
      items.push({ id: 'delete-current', label: '删除该视图', danger: true, disabled: !this.selectedSavedViewID });
      return items;
    },
    // 观看状态与「仅收藏」是硬过滤（不是「优先」，PLAY-08）；时长上限在今晚看什么里设，这里只能取消。
    randomMenuItems() {
      const conditions = normalizeRandomConditions(this.randomConditions);
      const items = [
        { id: 'tonight', label: '今晚看什么…', disabled: this.tonightDisabled },
        { id: 'pick-ten', label: `随机 ${this.randomPickSize} 部`, disabled: this.randomPickLoading || this.randomDisabled },
        { divider: true },
        { heading: '观看状态' },
        { id: 'watch:unwatched', label: '探索 · 只抽没看过的', checked: conditions.watchState === 'unwatched' },
        { id: 'watch:any', label: '不限', checked: conditions.watchState === 'any' },
        { id: 'watch:watched', label: '重温 · 只抽看过的', checked: conditions.watchState === 'watched' },
        { divider: true },
        { id: 'favorites', label: '仅收藏', checked: conditions.favoritesOnly }
      ];
      if (conditions.maxMinutes > 0) {
        items.push({ id: 'duration:clear', label: `取消时长上限（≤${conditions.maxMinutes} 分钟）` });
      }
      return items;
    },
    randomButtonLabel() {
      return ['随机播放', ...randomConditionTags(this.randomConditions)].join(' · ');
    },
    // 今晚看什么沿用原入口的禁用条件：语义搜索没有可复用的筛选边界，迁移与增量扫描期间不选片。
    tonightDisabled() {
      return this.searchMode === 'semantic' || this.migrationRunning || Boolean(this.incrementalScan?.running);
    },
    randomDisabled() {
      return this.searchMode === 'semantic';
    },
    randomDisabledReason() {
      return '语义搜索模式下无法按当前条件随机（随机会忽略搜索词、从全库抽取），请切回文件或字幕搜索';
    },
    personOptions() {
      const selected = new Set(this.selectedPeople.map(person => Number(person.id)));
      return this.personCandidates.filter(item => item?.person?.id && !selected.has(Number(item.person.id)));
    },
  },
  methods: {
	groupSelectedCount(group) {
	  const selected = new Set(this.selectedTags.map(Number));
	  return group.tags.filter(tag => selected.has(Number(tag.id))).length;
	},
    // 管理菜单里只有「建议作品集」由工具栏自己处理，其余照旧交回片库页（原样转发菜单项）。
    // BaseMenu 发的是菜单项对象，旧用例传的是 id 字符串，两种都认。
    onManageSelect(item) {
      const id = typeof item === 'string' ? item : item?.id;
      if (id === 'collection-suggestions') {
        this.collectionSuggestionOpen = true;
        return;
      }
      this.$emit('manage-select', item);
    },
    personName(person) {
      return String(person?.name || '').trim() || '已选人物';
    },
    addPersonFilter(item) {
      const id = Number(item?.person?.id || 0);
      if (!id || this.selectedPeople.some(person => Number(person.id) === id)) return;
      this.$emit('update:selectedPeople', [...this.selectedPeople, { id, name: item.person.display_name || '' }]);
      this.personKeyword = '';
      this.personMenuOpen = false;
      this.personActiveIndex = 0;
    },
    removePersonFilter(id) {
      this.$emit('update:selectedPeople', this.selectedPeople.filter(person => Number(person.id) !== Number(id)));
    },
    openPersonMenu() {
      this.personMenuOpen = true;
      this.personActiveIndex = 0;
      this.searchPeople();
    },
    closePersonMenu() {
      this.personMenuOpen = false;
    },
    onPersonInput() {
      this.personMenuOpen = true;
      this.personActiveIndex = 0;
      clearTimeout(this._personSearchTimer);
      this._personSearchTimer = setTimeout(() => this.searchPeople(), 200);
    },
    onPersonKeydown(event) {
      const options = this.personOptions;
      if (event.key === 'ArrowDown' && options.length) {
        event.preventDefault();
        this.personMenuOpen = true;
        this.personActiveIndex = (this.personActiveIndex + 1) % options.length;
      } else if (event.key === 'ArrowUp' && options.length) {
        event.preventDefault();
        this.personMenuOpen = true;
        this.personActiveIndex = (this.personActiveIndex - 1 + options.length) % options.length;
      } else if (event.key === 'Enter') {
        event.preventDefault();
        const option = options[this.personActiveIndex] || options[0];
        if (option) this.addPersonFilter(option);
      } else if (event.key === 'Escape') {
        this.personMenuOpen = false;
      }
    },
    async searchPeople() {
      const token = Symbol('library-person-filter');
      this._personSearchToken = token;
      try {
        const results = await ListPeople(this.personKeyword.trim(), '', 0, PERSON_OPTION_LIMIT);
        if (this._personSearchToken !== token) return;
        this.personCandidates = Array.isArray(results) ? results : [];
        this.personSearchError = '';
      } catch (err) {
        if (this._personSearchToken !== token) return;
        this.personCandidates = [];
        this.personSearchError = '人物搜索失败，请稍后再试';
        this.debugLog('searchPeople failed', { err: String(err) }, true);
      }
    },
    collectionSuggestionConfirmed() {
      // 新建的作品集不在片库列表里，不用重载列表；候选少了一组由面板自己收。
      this.refreshCollectionSuggestionStatus();
    },
    async refreshCollectionSuggestionStatus() {
      if (!GetCollectionSuggestionStatus) return;
      try {
        const status = await GetCollectionSuggestionStatus();
        this.collectionSuggestionAnalyzing = Boolean(status?.running);
      } catch (err) {
        this.debugLog('读取剧集分析状态失败', { error: String(err) }, true);
      }
    },
    focusSearch() {
      this.$refs.searchInput?.focus();
      this.$refs.searchInput?.select();
    },
    debugLog(message, payload = null, isError = false) {
      return logFrontend('VideoListPage', message, payload, isError);
    },
    toggleToolbarMenu(name, triggerRef) {
      if (this.toolbarMenu === name) {
        this.closeToolbarMenu();
        return;
      }
      this.toolbarMenuAnchor = this.$refs[triggerRef] || null;
      this.toolbarMenu = name;
      if (name === 'filter') this.resetFilterDraft();
    },
    closeToolbarMenu() {
      this.toolbarMenu = null;
      this.toolbarMenuAnchor = null;
      if (this.filterPreviewTimer) {
        clearTimeout(this.filterPreviewTimer);
        this.filterPreviewTimer = null;
      }
    },
    resetFilterDraft() {
      this.filterDraft = {
        sizeRange: this.selectedSizeRange,
        resRange: this.selectedResRange,
        minRating: this.minRating,
        maxRating: this.maxRating
      };
      this.filterPreviewCount = this.filteredCount;
    },
    clearFilterDraft() {
      this.filterDraft = { sizeRange: 'all', resRange: 'all', minRating: '', maxRating: '' };
      this.scheduleFilterPreview();
    },
    // 草稿只在停止输入后预览一次计数；不预览每一次击键，否则改评分区间会连打请求。
    scheduleFilterPreview() {
      if (this.filterPreviewTimer) clearTimeout(this.filterPreviewTimer);
      this.filterPreviewTimer = window.setTimeout(() => {
        this.filterPreviewTimer = null;
        this.previewFilterDraft();
      }, 300);
    },
    async previewFilterDraft() {
      if (this.searchMode === 'semantic') {
        this.filterPreviewCount = null;
        return;
      }
      try {
        this.filterPreviewCount = await CountLibraryVideos(this.libraryFilterFrom(this.filterDraft));
      } catch (err) {
        this.filterPreviewCount = null;
        this.debugLog('previewFilterDraft failed', { err: String(err) }, true);
      }
    },
    applyFilterDraft() {
      const draft = { ...this.filterDraft };
      this.closeToolbarMenu();
      this.$emit('apply-filter', draft);
    },
    formatCount(value) {
      return Number(value || 0).toLocaleString('zh-CN');
    },
    setViewMode(mode) {
      const next = normalizeLibraryLayout(mode);
      window.localStorage?.setItem('cineinsight-library-layout', next);
      this.$emit('update:viewMode', next);
    },
    setRowDensity(density) {
      const next = density === 'comfortable' ? 'comfortable' : 'compact';
      window.localStorage?.setItem('cineinsight-library-density', next);
      this.$emit('update:rowDensity', next);
    },
    isTagSelected(tagID) {
      return this.selectedTags.includes(Number(tagID));
    },
  }
};
</script>

<style scoped>
/* 工具栏（原型 A1）：不透明面板 + 底部发丝线，两行常驻，不再吸顶成圆角浮块。 */
.library-chrome {
  position: sticky;
  top: 0;
  z-index: var(--z-sticky);
  margin-bottom: var(--space-3);
}

.toolbar {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  margin: 0 -20px 0;
  padding: 10px 20px;
  border-bottom: 1px solid var(--hairline);
  background: var(--panel-bg);
}

.toolbar-row {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  min-width: 0;
  flex-wrap: wrap;
}

/* 操作按钮只占顶部一行，各分类使用整行宽度，不为按钮预留侧栏。 */
.toolbar-row--tags {
  flex-wrap: nowrap;
  align-items: flex-start;
}

.toolbar-row__label {
  flex: none;
  color: var(--text-muted);
  font-size: var(--fs-caption);
}

.toolbar-row--tags .toolbar-row__label { line-height: 26px; }

/* 工具栏里的触发按钮（筛选、视图、管理、搜场景）：统一的次要按钮，常规字重；有生效条件时用强调色。 */
.toolbar-trigger { flex: none; gap: 6px; padding: 0 var(--space-3); font-weight: var(--fw-regular); }
.toolbar-trigger--strong { font-weight: var(--fw-semibold); }
.toolbar-trigger--on,
.toolbar-trigger--on:hover:not(:disabled) { border-color: var(--accent-color); background: var(--accent-soft); color: var(--accent-text); font-weight: var(--fw-semibold); }
.toolbar-trigger__caret { color: var(--text-muted); }
.toolbar-trigger--on .toolbar-trigger__caret { color: var(--accent-text); }

.tags-wrap {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  min-width: 0;
  min-height: 26px;
  align-items: center;
}

/* 用户标签沿用全局 .tag-chip（带各自的底色）；「全部」与已选人物是筛选芯片（FilterChip）。 */
.tag-chip-check { flex: none; }

.tag-browser { display: flex; flex: 1; flex-direction: column; gap: 6px; min-width: 0; }
.tag-browser__actions { display: flex; flex-wrap: wrap; gap: var(--space-2); margin-left: auto; }
.tag-category-row {
  /* WebKit 会在宽度变化后保留嵌套换行 flex 的旧 grid 行高。 */
  display: flex;
  align-items: flex-start;
  gap: var(--space-3);
  padding-top: 6px;
  border-top: 1px solid var(--hairline-faint);
}
.tag-category-row > .tags-wrap { flex: 1; align-content: flex-start; }
.person-filter-row {
  display: flex;
  align-items: flex-start;
  gap: var(--space-3);
  padding-top: 6px;
  border-top: 1px solid var(--hairline-faint);
}
.person-filter-row > .tags-wrap { flex: 1; align-content: flex-start; }
/* 与分类标签列同宽同字号；类名单独起，免得与标签分类行的计数、名称混在一起。 */
.person-filter-label {
  display: flex;
  flex: 0 0 104px;
  flex-wrap: wrap;
  align-items: baseline;
  column-gap: 6px;
  padding: var(--space-1) 0;
  color: var(--text-secondary);
  font-size: var(--fs-caption);
  line-height: 1.4;
}
.person-filter-label__name { font-weight: var(--fw-semibold); }
.person-filter-label__count { color: var(--text-muted); font-size: var(--fs-caption); }
.person-filter-combobox { position: relative; width: 200px; }
.person-filter-input { height: 26px; font-size: var(--fs-caption); }
/* 候选浮层在工具栏自己的叠放上下文里（.library-chrome 吸顶），压过下面的分类行即可。 */
.person-filter-options {
  position: absolute;
  top: calc(100% + 4px);
  left: 0;
  right: 0;
  z-index: 3;
  max-height: 220px;
  overflow-y: auto;
  padding: var(--space-1);
  border: 1px solid var(--border-color);
  border-radius: var(--radius-sm);
  background: var(--panel-bg);
  box-shadow: var(--shadow-modal);
}
.person-filter-option {
  display: block;
  width: 100%;
  padding: 6px var(--space-2);
  border: 0;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-primary);
  font-size: var(--fs-caption);
  font-weight: var(--fw-regular);
  text-align: left;
  cursor: pointer;
}
.person-filter-option:hover:not(:disabled) { text-decoration: none; }
.person-filter-option.active { background: var(--accent-soft); color: var(--accent-text); }
.person-filter-options__empty { display: block; padding: var(--space-2); color: var(--text-muted); font-size: var(--fs-caption); }
/* 名称与计数同一行，名称过长才折行：分类行在标签只有一行时不被计数撑高。 */
.tag-category-label {
  display: flex;
  flex: 0 0 104px;
  flex-wrap: wrap;
  align-items: baseline;
  column-gap: 6px;
  padding: var(--space-1) 0;
  color: var(--text-secondary);
  font-size: var(--fs-caption);
  line-height: 1.4;
  overflow-wrap: anywhere;
}
.tag-category-label__name { font-weight: var(--fw-semibold); }
.tag-category-label__count { color: var(--text-muted); font-size: var(--fs-caption); }
.tag-browser__empty { color: var(--text-muted); font-size: var(--fs-caption); }


.search-field {
  position: relative;
  display: flex;
  flex: 1 1 auto;
  align-items: center;
  min-width: 240px;
  height: var(--h-unit);
  padding: 0 10px 0 var(--space-3);
  border: 1px solid var(--hairline);
  border-radius: var(--radius-sm);
  background: var(--input-bg);
}

.search-field:focus-within {
  border-color: var(--accent-color);
  box-shadow: 0 0 0 3px var(--accent-soft);
}

.search-field__input {
  flex: 1;
  min-width: 0;
  border: 0;
  background: transparent;
  color: var(--text-primary);
  font-size: var(--fs-secondary);
  outline: none;
}

.search-field__hint {
  flex: none;
  padding: 1px 5px;
  border: 1px solid var(--hairline);
  border-radius: var(--radius-sm);
  color: var(--text-muted);
  font-family: var(--font-mono);
  font-size: var(--fs-caption);
}

.toolbar-control {
  width: auto;
  min-width: 116px;
  flex: none;
}

/* 随机拆分按钮：主键直接随机，▾ 里选模式或改抽十部。两半是无底色的统一按钮，外框由容器给。 */
.split-btn {
  display: inline-flex;
  flex: none;
  align-items: stretch;
  height: var(--h-unit);
  border: 1px solid var(--border-color);
  border-radius: var(--radius-sm);
  background: var(--control-bg);
  overflow: hidden;
}

.split-btn__main,
.split-btn__caret {
  height: 100%;
  border: 0;
  border-radius: 0;
}

.split-btn__main { padding: 0 var(--space-3); color: var(--text-primary); font-weight: var(--fw-regular); }
.split-btn__caret { width: 30px; color: var(--text-muted); }
.split-btn__main:disabled { color: var(--text-muted); }
.split-btn__divider { width: 1px; background: var(--hairline); }

/* 结果条：筛选命中数与条件回显，常驻在工具栏下沿。 */
.result-bar {
  display: flex;
  align-items: center;
  gap: 10px;
  min-height: 36px;
  margin: 0 -20px;
  padding: 0 20px;
  border-bottom: 1px solid var(--hairline-soft);
  font-size: var(--fs-caption);
  background: var(--bg-color);
  color: var(--text-secondary);
}

.result-bar__count b {
  color: var(--text-primary);
  font-family: var(--font-mono);
}

.result-bar__sep { color: var(--hairline); }
.result-bar__spacer { flex: 1; }
.result-bar__label { color: var(--text-muted); }
.result-bar__toggle { display: inline-flex; align-items: center; gap: var(--space-1); color: var(--text-muted); cursor: pointer; white-space: nowrap; }
.result-bar__clear { flex: none; font-size: var(--fs-caption); }
/* 行高分段器压在 36px 的结果条里，用紧凑高度。 */
.result-bar__density :deep(.ui-segmented__option) { height: calc(var(--h-compact) - 6px); padding: 0 var(--space-2); font-size: var(--fs-caption); }

.result-bar__conditions {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 筛选浮层 */
.filter-popover__head { display: flex; align-items: center; font-size: var(--fs-secondary); font-weight: var(--fw-bold); }
.filter-popover__spacer { flex: 1; }

.filter-popover__field {
  display: flex;
  flex-direction: column;
  gap: 6px;
  font-size: var(--fs-caption);
  color: var(--text-muted);
}

.filter-popover__range { display: flex; align-items: center; gap: var(--space-2); }
.filter-popover__range .text-input { flex: 1; }
.filter-popover__dash { color: var(--text-muted); }
.filter-popover__divider { height: 1px; background: var(--hairline-faint); }

.filter-popover__actions { display: flex; gap: var(--space-2); }
.filter-popover__actions > * { flex: 1; }
</style>
