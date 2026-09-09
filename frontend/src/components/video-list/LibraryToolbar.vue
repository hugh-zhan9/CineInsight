<template>
    <!-- 工具栏与结果条/批量栏一起吸顶：选中若干项后向下滚动时，批量按钮
         必须保持可见，否则要滚回顶部才能操作。 -->
    <div class="library-chrome">
    <!-- 工具栏按频率分三层（原型 A1）：第一行是跟着查询走的常驻控件，
         区间类条件收进「筛选」浮层，12 个库维护动作收进「管理」菜单，
         第二行放跟着列表内容走的标签筛选与选择动作。 -->
    <div class="toolbar">
      <div class="toolbar-row">
        <div class="segmented" role="group" aria-label="搜索模式">
          <button
            v-for="mode in searchModeOptions"
            :key="mode.value"
            type="button"
            :class="['segmented__btn', { active: searchMode === mode.value }]"
            :disabled="mode.value === 'semantic' && !semanticAvailable"
            :title="mode.value === 'semantic' && !semanticAvailable ? semanticUnavailableNotice : null"
            :data-test="`search-mode-${mode.value}`"
            @click="$emit('set-search-mode', mode.value)"
          >{{ mode.label }}</button>
        </div>

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

        <button
          ref="filterTrigger"
          type="button"
          :class="['toolbar-btn', { 'toolbar-btn--on': activeFilterCount > 0 }]"
          @click="toggleToolbarMenu('filter', 'filterTrigger')"
        >
          筛选
          <span v-if="activeFilterCount > 0" class="toolbar-btn__badge">{{ activeFilterCount }}</span>
          <span class="toolbar-btn__caret">▾</span>
        </button>

        <select v-model="sortModeModel" @change="$emit('search', true)" class="select-input toolbar-control" aria-label="排序方式">
          <option value="balanced">均衡排序</option>
          <option value="rating_desc">评分从高到低</option>
          <option value="rating_asc">评分从低到高</option>
        </select>

        <div class="split-btn">
          <button type="button" class="split-btn__main" @click="$emit('play-random')">按当前条件随机</button>
          <span class="split-btn__divider" aria-hidden="true"></span>
          <button
            ref="randomTrigger"
            type="button"
            class="split-btn__caret"
            aria-label="随机选项"
            @click="toggleToolbarMenu('random', 'randomTrigger')"
          >▾</button>
        </div>

        <button
          ref="viewTrigger"
          type="button"
          :class="['toolbar-btn', { 'toolbar-btn--on': selectedSavedViewID > 0 }]"
          @click="toggleToolbarMenu('view', 'viewTrigger')"
        >{{ selectedSavedViewName || '视图' }}<span class="toolbar-btn__caret">▾</span></button>

        <div class="segmented" role="group" aria-label="片库布局">
          <button type="button" :class="['segmented__btn', { active: viewMode === 'list' }]" @click="setViewMode('list')">列表</button>
          <button type="button" :class="['segmented__btn', { active: viewMode === 'grid' }]" @click="setViewMode('grid')">网格</button>
        </div>

        <button
          ref="manageTrigger"
          type="button"
          class="toolbar-btn toolbar-btn--strong"
          @click="toggleToolbarMenu('manage', 'manageTrigger')"
        >
          管理
          <span v-if="manageAttentionCount > 0" class="toolbar-btn__badge">{{ manageAttentionCount }}</span>
          <span class="toolbar-btn__caret">▾</span>
        </button>
      </div>

      <div class="toolbar-row toolbar-row--tags">
        <span class="toolbar-row__label">标签</span>
        <div class="tags-wrap">
          <button
            @click="$emit('clear-tags')"
            :class="['tag-chip', { active: selectedTags.length === 0 }]"
          >全部</button>
          <div
            v-for="tag in tags"
            :key="tag.id"
            class="tag-chip tag-chip-wrap"
            :class="{ active: isTagSelected(tag.id) }"
            :style="{ backgroundColor: tagBgColor(tag.color) }"
            @click="$emit('toggle-tag', tag.id)"
          >
            <span class="tag-chip-name">{{ tag.name }}</span>
            <span v-if="isTagSelected(tag.id)" class="tag-chip-check">✓</span>
            <button v-if="!tag.automatic_kind" type="button" class="tag-chip-delete" @click.stop="$emit('delete-tag', tag)">×</button>
          </div>
        </div>
        <button type="button" class="toolbar-btn toolbar-btn--compact" @click="$emit('open-tag-manager')">标签管理</button>
        <button
          type="button"
          class="toolbar-btn toolbar-btn--compact"
          :disabled="videos.length === 0"
          @click="$emit('toggle-select-all')"
        >{{ allVisibleSelected ? '取消全选' : '选择本页' }}</button>
      </div>
    </div>

    <!-- 结果条：筛选命中数、生效条件回显、一键清除与行高档位 -->
    <div v-if="selectedVideoIds.length === 0" class="result-bar">
      <span class="result-bar__count">
        筛选出 <b>{{ filteredCountText }}</b>
        <span v-if="libraryTotalCount !== null"> / {{ formatCount(libraryTotalCount) }}</span>
      </span>
      <template v-if="activeConditionLabels.length > 0">
        <span class="result-bar__sep">|</span>
        <span class="result-bar__conditions">条件：{{ activeConditionLabels.join(' · ') }}</span>
        <button type="button" class="result-bar__clear" @click="$emit('clear-conditions')">清除</button>
      </template>
      <span v-else class="result-bar__conditions">未设置筛选条件</span>
      <div class="result-bar__spacer"></div>
      <span class="result-bar__label">行高</span>
      <div class="segmented segmented--mini" role="group" aria-label="行高">
        <button type="button" :class="['segmented__btn', { active: rowDensity === 'compact' }]" @click="setRowDensity('compact')">紧凑</button>
        <button type="button" :class="['segmented__btn', { active: rowDensity === 'comfortable' }]" @click="setRowDensity('comfortable')">舒适</button>
      </div>
    </div>

    <BatchActionBar
      v-else
      :selected-count="selectedVideoIds.length"
      :selected-total-size-text="selectedTotalSizeText"
      :migration-running="migrationRunning"
      :all-visible-selected="allVisibleSelected"
      @batch-add-tag="$emit('batch-add-tag')"
      @batch-move="$emit('batch-move')"
      @batch-local-metadata="$emit('batch-local-metadata')"
      @batch-playback-proxy="$emit('batch-playback-proxy')"
      @batch-delete="$emit('batch-delete')"
      @toggle-select-all="$emit('toggle-select-all')"
      @clear-selection="$emit('clear-selection')"
    />
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
        <button type="button" class="link-btn" @click="clearFilterDraft">全部清除</button>
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
        <button type="button" class="btn-secondary" @click="$emit('open-save-view')">存为视图</button>
        <button type="button" class="btn-primary" @click="applyFilterDraft">应用{{ filterPreviewText }}</button>
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
import { CountLibraryVideos } from '../../../wailsjs/go/main/App';
import BaseMenu from '../ui/BaseMenu.vue';
import BasePopover from '../ui/BasePopover.vue';
import BatchActionBar from './BatchActionBar.vue';
import CollectionSuggestionPanel from '../CollectionSuggestionPanel.vue';
import { GetCollectionSuggestionStatus } from '../../../wailsjs/go/main/App';
import { runtimeEventsMixin } from './runtimeEvents.js';
import { logFrontend } from '../../utils/frontendLog.js';

// 吸顶的片库工具栏：三层控件、标签行、结果条与批量操作栏，外加筛选浮层与三个下拉菜单。
// 查询条件本身仍归 VideoListPage 持有，这里只负责呈现与把动作发回去；
// 浮层里的区间草稿是纯 UI 状态，留在本组件内，「应用」时才把结果 emit 出去。
export default {
  name: 'LibraryToolbar',
  components: { BaseMenu, BasePopover, BatchActionBar, CollectionSuggestionPanel },
  mixins: [runtimeEventsMixin],
  props: {
    tags: { type: Array, default: () => [] },
    videos: { type: Array, default: () => [] },
    selectedVideoIds: { type: Array, default: () => [] },
    searchKeyword: { type: String, default: '' },
    searchMode: { type: String, default: 'file' },
    smartView: { type: String, default: '' },
    sortMode: { type: String, default: 'balanced' },
    viewMode: { type: String, default: 'list' },
    rowDensity: { type: String, default: 'compact' },
    selectedTags: { type: Array, default: () => [] },
    selectedSizeRange: { type: [String, Object], default: 'all' },
    selectedResRange: { type: [String, Object], default: 'all' },
    minRating: { type: String, default: '' },
    maxRating: { type: String, default: '' },
    sizeOptions: { type: Array, default: () => [] },
    resOptions: { type: Array, default: () => [] },
    savedViews: { type: Array, default: () => [] },
    selectedSavedViewID: { type: [Number, String], default: 0 },
    randomMode: { type: String, default: 'balanced' },
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
    aiTagSummary: { type: Object, required: true },
    cleanupBadgeCount: { type: Number, default: 0 },
    cleanupAnalyzing: { type: Boolean, default: false },
    technicalBackfill: { type: Object, required: true },
    perceptualHash: { type: Object, required: true },
    localMetadataBackfill: { type: Object, required: true },
    localMetadataExport: { type: Object, required: true },
    // 播放代理任务状态（D-006）：管理菜单里的「为当前筛选生成代理」跑起来后要显示进度。
    playbackProxy: { type: Object, default: () => ({ running: false, processed: 0, total: 0 }) },
    // 标签底色的换算与筛选 DTO 的构造都还在片库页，这里按需调用。
    tagBgColor: { type: Function, required: true },
    libraryFilterFrom: { type: Function, required: true }
  },
  emits: [
    'update:searchKeyword', 'update:smartView', 'update:sortMode', 'update:viewMode', 'update:rowDensity',
    'search', 'set-search-mode', 'play-random', 'toggle-tag', 'clear-tags', 'delete-tag', 'open-tag-manager',
    'toggle-select-all', 'clear-selection', 'clear-conditions', 'apply-filter', 'open-save-view',
    'manage-select', 'view-select', 'random-select',
    'batch-add-tag', 'batch-move', 'batch-local-metadata', 'batch-playback-proxy', 'batch-delete'
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
        { label: '路径失效', value: 'stale' }
      ],
      toolbarMenu: null,
      toolbarMenuAnchor: null,
      filterDraft: { sizeRange: 'all', resRange: 'all', minRating: '', maxRating: '' },
      filterPreviewCount: null,
      filterPreviewTimer: null,
      collectionSuggestionOpen: false,
      collectionSuggestionAnalyzing: false
    };
  },
  mounted() {
    // 「建议作品集」的运行态没有从片库页传进来的 prop，工具栏自己取一次并跟事件走：
    // 分析在后台跑，菜单上得看得出它正在跑。
    this.refreshCollectionSuggestionStatus();
    this.registerRuntimeEvent('collection-suggestion-state', (data) => {
      this.collectionSuggestionAnalyzing = Boolean(data?.running);
    });
  },
  computed: {
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
      if (this.selectedTags.length > 0) {
        const names = this.selectedTags
          .map(id => this.tags.find(tag => tag.id === id)?.name)
          .filter(Boolean);
        if (names.length > 0) labels.push(`标签 ${names.join('、')}`);
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
    selectedTotalSizeText() {
      const ids = new Set(this.selectedVideoIds);
      // 只能合计当前已加载的行；跨页选中的条目没有 size 可用，宁可不显示也不猜。
      const loaded = this.videos.filter(video => ids.has(video.id));
      if (loaded.length !== ids.size || loaded.length === 0) return '';
      const bytes = loaded.reduce((total, video) => total + Number(video.size || 0), 0);
      const units = ['B', 'KB', 'MB', 'GB', 'TB'];
      const index = bytes > 0 ? Math.min(units.length - 1, Math.floor(Math.log(bytes) / Math.log(1024))) : 0;
      const value = bytes / Math.pow(1024, index);
      return `${value.toFixed(value >= 10 || index === 0 ? 0 : 1)} ${units[index]}`;
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
    manageAttentionCount() {
      return (this.aiTagSummary.same_source_unread || 0) + (this.cleanupBadgeCount || 0);
    },
    manageMenuItems() {
      const unread = this.aiTagSummary.same_source_unread || 0;
      const cleanup = this.cleanupAnalyzing
        ? '清理候选（分析中）'
        : (this.cleanupBadgeCount ? `清理候选（待审阅 ${this.cleanupBadgeCount} 项）` : '清理候选');
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
          id: 'backfill-playback-proxy',
          label: this.playbackProxy.running
            ? `为当前筛选生成代理（${this.playbackProxy.processed}/${this.playbackProxy.total}）`
            : '为当前筛选生成代理',
          disabled: this.playbackProxy.running
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
        { id: 'ai-tags', label: unread ? `AI 标签管理（${unread} 未读）` : 'AI 标签管理', shortcut: '⌘T' },
        { id: 'tag-manager', label: '标签管理' },
        { id: 'cleanup', label: cleanup, shortcut: '⌘K' },
        { id: 'collection-suggestions', label: this.collectionSuggestionAnalyzing ? '建议作品集（分析中）' : '建议作品集' },
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
      items.push({ id: 'delete-current', label: '删除该视图', danger: true, disabled: !this.selectedSavedViewID });
      return items;
    },
    randomMenuItems() {
      return [
        { id: 'mode:balanced', label: '均衡随机', checked: this.randomMode === 'balanced' },
        { id: 'mode:unwatched', label: '随机未看', checked: this.randomMode === 'unwatched' },
        { id: 'mode:favorites', label: '随机收藏', checked: this.randomMode === 'favorites' },
        { divider: true },
        { id: 'pick-ten', label: `随机 ${this.randomPickSize} 部`, disabled: this.randomPickLoading }
      ];
    },
    allVisibleSelected() {
      const ids = this.videos.map(video => video.id);
      return ids.length > 0 && ids.every(id => this.selectedVideoIds.includes(id));
    },
  },
  methods: {
    // 管理菜单里只有「建议作品集」由工具栏自己处理，其余照旧交回片库页。
    onManageSelect(id) {
      if (id === 'collection-suggestions') {
        this.collectionSuggestionOpen = true;
        return;
      }
      this.$emit('manage-select', id);
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
      const next = mode === 'grid' ? 'grid' : 'list';
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
  z-index: 90;
  margin-bottom: 10px;
}

.toolbar {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin: 0 -20px 0;
  padding: 10px 20px;
  border-bottom: 1px solid var(--hairline);
  background: var(--panel-bg);
}

.toolbar-row {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  flex-wrap: wrap;
}

/* 标签行：标签芯片多行铺开、全部可见；标签文字和两个按钮停在第一行 */
.toolbar-row--tags {
  flex-wrap: nowrap;
  align-items: flex-start;
}

.toolbar-row__label {
  flex: none;
  color: var(--text-muted);
  font-size: 12px;
}

.toolbar-row--tags .toolbar-row__label { line-height: 26px; }

.tags-wrap {
  display: flex;
  flex: 1;
  flex-wrap: wrap;
  gap: 6px;
  min-width: 0;
  min-height: 26px;
  align-items: center;
}

/* 三段器：搜索模式、列表/网格、行高共用 */
.segmented {
  display: inline-flex;
  flex: none;
  height: var(--h-unit);
  padding: 3px;
  gap: 2px;
  border: 1px solid var(--hairline);
  border-radius: var(--radius);
  background: var(--panel-muted-bg);
}

.segmented__btn {
  padding: 0 11px;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: var(--text-secondary);
  font-size: 12.5px;
  cursor: pointer;
  white-space: nowrap;
}

.segmented__btn.active {
  background: var(--panel-bg);
  color: var(--text-primary);
  font-weight: 600;
  box-shadow: var(--shadow-segment);
}

.segmented--mini {
  height: 22px;
  padding: 2px;
}

.segmented--mini .segmented__btn {
  padding: 0 8px;
  font-size: 11.5px;
}

.search-field {
  position: relative;
  display: flex;
  flex: 1 1 auto;
  align-items: center;
  min-width: 240px;
  height: var(--h-unit);
  padding: 0 10px 0 12px;
  border: 1px solid var(--hairline);
  border-radius: var(--radius);
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
  font-size: 13px;
  outline: none;
}

.search-field__hint {
  flex: none;
  padding: 1px 5px;
  border: 1px solid var(--hairline);
  border-radius: 4px;
  color: var(--text-muted);
  font-family: var(--font-mono);
  font-size: 11px;
}

.toolbar-control {
  width: auto;
  min-width: 116px;
  flex: none;
}

/* 随机拆分按钮：主键直接随机，▾ 里选模式或改抽十部 */
.split-btn {
  display: inline-flex;
  flex: none;
  align-items: stretch;
  height: var(--h-unit);
  border: 1px solid var(--hairline);
  border-radius: var(--radius);
  background: var(--control-bg);
  overflow: hidden;
}

.split-btn__main,
.split-btn__caret {
  border: 0;
  background: transparent;
  color: var(--text-primary);
  font-size: 13px;
  cursor: pointer;
}

.split-btn__main { padding: 0 12px; }
.split-btn__caret { padding: 0 9px; color: var(--text-muted); }
.split-btn__main:hover,
.split-btn__caret:hover { background: var(--control-hover-bg); }
.split-btn__divider { width: 1px; background: var(--hairline); }

/* 结果条与批量栏占同一个位置、同一个高度，互斥出现（批量栏的那一份在 BatchActionBar 里） */
.result-bar {
  display: flex;
  align-items: center;
  gap: 10px;
  height: 32px;
  margin: 0 -20px;
  padding: 0 20px;
  border-bottom: 1px solid var(--hairline-soft);
  font-size: 12px;
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

.result-bar__conditions {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.result-bar__clear,
.link-btn {
  flex: none;
  border: 0;
  background: transparent;
  color: var(--accent-text);
  font-size: 12px;
  cursor: pointer;
  padding: 0;
}

/* 筛选浮层 */
.filter-popover__head { display: flex; align-items: center; font-size: 13px; font-weight: 700; }
.filter-popover__spacer { flex: 1; }

.filter-popover__field {
  display: flex;
  flex-direction: column;
  gap: 6px;
  font-size: 11.5px;
  color: var(--text-muted);
}

.filter-popover__range { display: flex; align-items: center; gap: 8px; }
.filter-popover__range .text-input { flex: 1; }
.filter-popover__dash { color: var(--text-muted); }
.filter-popover__divider { height: 1px; background: var(--hairline-faint); }

.filter-popover__actions { display: flex; gap: 8px; }
.filter-popover__actions > * { flex: 1; }

.ai-review-badge {
  display: inline-flex;
  min-width: 18px;
  height: 18px;
  margin-left: 6px;
  padding: 0 5px;
  align-items: center;
  justify-content: center;
  border-radius: 999px;
  background: var(--danger-color);
  color: var(--accent-on);
  font-size: 11px;
  font-weight: 700;
}

@media (max-width: 920px) {
  .selection-toolbar {
    flex-wrap: wrap;
  }
}
.btn-compact {
  height: 28px;
  padding: 0 10px;
  font-size: 12px;
}
</style>
