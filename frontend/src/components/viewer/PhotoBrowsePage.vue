<template>
  <section class="page-content photo-browse" data-test="photo-browse">
    <PageHeader title="图片" :subtitle="countText">
      <template #below>
        <div class="photo-browse__toolbar" role="search">
          <input
            v-model="keyword"
            type="search"
            class="search-input photo-browse__search"
            placeholder="搜索文件名"
            aria-label="搜索文件名"
            data-test="photo-browse-search"
            @input="scheduleReload"
            @keyup.enter="reloadNow"
          />
          <SegmentedControl
            label="范围"
            :options="scopeOptions"
            :model-value="favoriteOnly ? 'favorites' : ''"
            data-test="photo-browse-scope"
            @update:model-value="setFavoriteOnly"
          />
          <button
            ref="personTrigger"
            type="button"
            :class="['btn', 'btn--secondary', { 'photo-browse__trigger--on': people.length > 0 }]"
            aria-haspopup="dialog"
            :aria-expanded="menu === 'people' ? 'true' : 'false'"
            data-test="photo-browse-person-trigger"
            @click="toggleMenu('people', 'personTrigger')"
          >人物<AppBadge v-if="people.length" tone="accent" :count="people.length" /><AppIcon name="chevron-down" :size="16" /></button>
          <button
            ref="folderTrigger"
            type="button"
            :class="['btn', 'btn--secondary', { 'photo-browse__trigger--on': Boolean(folder) }]"
            aria-haspopup="dialog"
            :aria-expanded="menu === 'folders' ? 'true' : 'false'"
            data-test="photo-browse-folder-trigger"
            @click="toggleMenu('folders', 'folderTrigger')"
          >文件夹<AppIcon name="chevron-down" :size="16" /></button>
        </div>
        <div v-if="people.length || folder" class="photo-browse__chips" data-test="photo-browse-active-filters">
          <FilterChip
            v-for="person in people"
            :key="`person-${person.id}`"
            :label="person.name"
            active
            removable
            :data-test="`photo-browse-person-chip-${person.id}`"
            @toggle="removePerson(person.id)"
            @remove="removePerson(person.id)"
          />
          <FilterChip
            v-if="folder"
            :label="folder.name"
            :title="folder.directory"
            active
            removable
            data-test="photo-browse-folder-chip"
            @toggle="clearFolder"
            @remove="clearFolder"
          />
          <button type="button" class="btn-link" data-test="photo-browse-clear-chips" @click="clearFilters">清空筛选</button>
        </div>
      </template>
    </PageHeader>

    <section class="photo-browse__stream" :aria-busy="list.loading ? 'true' : 'false'" aria-label="照片流">
      <AppBanner v-if="list.error && !list.items.length" tone="danger" title="读取图片失败" :message="list.error" data-test="photo-browse-error">
        <template #actions><button type="button" class="btn btn--secondary btn--sm" data-test="photo-browse-retry" @click="reloadNow">重试</button></template>
      </AppBanner>
      <div v-else-if="list.loading && !list.items.length" class="photo-browse__skeleton" data-test="photo-browse-loading">
        <SkeletonBlock v-for="n in 18" :key="n" shape="thumb" />
      </div>
      <EmptyState
        v-else-if="!list.items.length && list.loaded && filtersActive"
        icon="filter"
        title="没有符合条件的图片"
        message="换个文件名，或者少选几个条件试试。"
        data-test="photo-browse-no-results"
      >
        <template #actions><button type="button" class="btn btn--secondary" data-test="photo-browse-empty-clear" @click="clearFilters">清空筛选</button></template>
      </EmptyState>
      <EmptyState
        v-else-if="!list.items.length && list.loaded"
        icon="images"
        title="图片库里还没有图片"
        message="在图片管理里添加图片目录并扫描，照片会按拍摄时间出现在这里。"
        data-test="photo-browse-empty"
      >
        <template #actions><button type="button" class="btn btn--secondary" data-test="photo-browse-empty-manage" @click="$emit('navigate', 'photos-manage')">去图片管理添加目录</button></template>
      </EmptyState>
      <!-- 按月分组的窗口化网格（utils/photoGrid.js）：只渲染视口附近的行，上下两个占位块撑出整条照片流的高度。 -->
      <div v-else-if="list.items.length" ref="grid" class="photo-browse__grid" :style="gridStyle" data-test="photo-browse-grid">
        <div v-if="windowState.topSpacer > 0" :style="{ height: `${windowState.topSpacer}px` }" aria-hidden="true"></div>
        <template v-for="row in renderedRows" :key="row.key">
          <h2 v-if="row.isHeader" class="photo-browse__month" :style="rowStyle(row)" data-test="photo-browse-month">{{ monthLabel(row) }}</h2>
          <div v-else class="photo-browse__row" :style="rowStyle(row)">
            <button
              v-for="(image, offset) in rowImages(row)"
              :key="image.id"
              type="button"
              class="btn-plain photo-browse__tile"
              :title="image.name"
              :aria-label="image.is_favorite ? `${image.name}，已收藏` : image.name"
              data-test="photo-browse-tile"
              @click="openLightbox(row.startIndex + offset)"
            >
              <img v-if="!failedThumbs[image.id]" :src="thumbnailURL(image.id)" alt="" loading="lazy" decoding="async" @error="markThumbFailed(image.id)" />
              <span v-else class="photo-browse__tile-fallback"><AppIcon name="image" :size="24" /><small>{{ formatBadge(image) }}</small></span>
              <span v-if="image.is_favorite" class="photo-browse__favorite-mark" aria-hidden="true" data-test="photo-browse-favorite-mark"><AppIcon name="star" :size="16" /></span>
            </button>
          </div>
        </template>
        <div v-if="windowState.bottomSpacer > 0" :style="{ height: `${windowState.bottomSpacer}px` }" aria-hidden="true"></div>
      </div>

      <p v-if="list.loadingMore" class="photo-browse__more" role="status" data-test="photo-browse-loading-more"><AppSpinner :size="16" /> 正在加载更多…</p>
      <AppBanner v-else-if="list.moreError" tone="danger" title="加载更多失败" :message="list.moreError" data-test="photo-browse-more-error">
        <template #actions><button type="button" class="btn btn--secondary btn--sm" data-test="photo-browse-more-retry" @click="loadMore">重试</button></template>
      </AppBanner>
      <p v-else-if="list.hasMore && list.items.length" class="photo-browse__more">
        <button type="button" class="btn btn--secondary btn--sm" data-test="photo-browse-load-more" @click="loadMore">加载更多</button>
      </p>
    </section>

    <BasePopover v-if="menu === 'people'" :anchor="menuAnchor" :min-width="280" @close="closeMenu">
      <div class="photo-browse__popover-body" data-test="photo-browse-person-popover">
        <input
          ref="personInput"
          v-model="personKeyword"
          type="search"
          class="search-input"
          placeholder="搜索人物"
          aria-label="搜索人物"
          data-test="photo-browse-person-search"
          @input="schedulePersonSearch"
        />
        <p v-if="personSearch.error" class="photo-browse__popover-empty" role="alert">{{ personSearch.error }}</p>
        <p v-else-if="personSearch.loading && !personSearch.items.length" class="photo-browse__popover-empty"><AppSpinner :size="16" /> 正在查找…</p>
        <p v-else-if="!personSearch.items.length" class="photo-browse__popover-empty">没有匹配的人物</p>
        <div v-else class="photo-browse__option-list" role="group" aria-label="人物">
          <button
            v-for="item in personSearch.items"
            :key="item.person.id"
            type="button"
            :class="['btn-plain', 'photo-browse__option', { 'photo-browse__option--on': personSelected(item.person.id) }]"
            :aria-pressed="personSelected(item.person.id) ? 'true' : 'false'"
            :data-test="`photo-browse-person-option-${item.person.id}`"
            @click="togglePerson(item.person)"
          >
            <span>{{ item.person.display_name }}</span>
            <small>{{ Number(item.active_image_count || 0) }} 张</small>
            <AppIcon v-if="personSelected(item.person.id)" name="check" :size="16" />
          </button>
        </div>
      </div>
    </BasePopover>
    <BasePopover v-if="menu === 'folders'" :anchor="menuAnchor" :min-width="320" @close="closeMenu">
      <div class="photo-browse__popover-body" data-test="photo-browse-folder-popover">
        <input
          ref="folderInput"
          v-model="folderKeyword"
          type="search"
          class="search-input"
          placeholder="按名称找文件夹"
          aria-label="按名称找文件夹"
          data-test="photo-browse-folder-search"
        />
        <AppBanner v-if="folderOptions.error" tone="danger" title="读取文件夹失败" :message="folderOptions.error" data-test="photo-browse-folder-error">
          <template #actions><button type="button" class="btn btn--secondary btn--sm" data-test="photo-browse-folder-retry" @click="loadFolderOptions">重试</button></template>
        </AppBanner>
        <p v-else-if="folderOptions.loading && !folderOptions.items.length" class="photo-browse__popover-empty"><AppSpinner :size="16" /> 正在读取文件夹…</p>
        <p v-else-if="!folderChoices.items.length" class="photo-browse__popover-empty">没有匹配的文件夹</p>
        <div v-else class="photo-browse__option-list" role="group" aria-label="文件夹">
          <button
            v-for="group in folderChoices.items"
            :key="group.directory"
            type="button"
            :class="['btn-plain', 'photo-browse__option', 'photo-browse__option--folder', { 'photo-browse__option--on': folder?.directory === group.directory }]"
            :aria-pressed="folder?.directory === group.directory ? 'true' : 'false'"
            :title="group.directory"
            data-test="photo-browse-folder-option"
            @click="selectFolder(group)"
          >
            <span class="photo-browse__option-text"><span>{{ group.name }}</span><small>{{ group.directory }}</small></span>
            <small>{{ Number(group.count || 0) }} 张</small>
            <AppIcon v-if="folder?.directory === group.directory" name="check" :size="16" />
          </button>
        </div>
        <p v-if="folderChoices.hidden > 0" class="photo-browse__popover-empty" data-test="photo-browse-folder-more">还有 {{ folderChoices.hidden }} 个文件夹，输入名称缩小范围</p>
      </div>
    </BasePopover>

    <PhotoLightbox
      v-if="lightboxImage"
      :image="lightboxImage"
      :has-prev="lightboxIndex > 0"
      :has-next="lightboxIndex < list.items.length - 1 || list.hasMore"
      :loading-next="list.loadingMore && lightboxIndex === list.items.length - 1"
      :next-error="lightboxIndex === list.items.length - 1 ? list.moreError : ''"
      @close="handleLightboxClose"
      @prev="lightboxPrev"
      @next="lightboxNext"
      @patch="patchImage"
      @changed="$emit('image-changed', $event)"
      @open-person="openPersonFromLightbox"
      @open-in-manage="openInManage"
    />
  </section>
</template>

<script>
// 观看侧照片流（D-P2-08，详细设计 §3.11）：「图片」页，只做浏览，与图片管理（PhotoLibraryPage）各自一个实例、筛选各自独立。
// - 照片按拍摄时间从新到旧（SearchImagePage 的 taken 排序，键 COALESCE(taken_at, created_at)，与图片管理的时间线同口径），
//   按年 / 月分组，组头的张数来自 ListImageTimelineBuckets（同一个筛选；读不到时组头只写年月、页头不写总数）。每批 60。
// - 筛选只有四样：文件名关键字、全部 / 收藏、人物（ListPeople，与图片管理的人物筛选同一个搜索）、文件夹（ListImageFolderGroups）。
// - 网格按行窗口化（utils/photoGrid.js：按月切行、算窗口、列宽变化时把视口顶部那张放回原处），滚动宿主是共用的 .main-view；
//   接近底部自动取下一批。切走再回来放回滚动位置。
// - 点一张进全屏看图（PhotoLightbox）；没有删除、打标签、AI、批量、目录操作。
// - navigationRequest：App 的 navigateTo('photos', { personId, personName? }) 带来的一次新浏览——筛选回到默认、只按这个人，回到顶部。
// - locateImage(image)：回顾、人物 / 作品集页点图片进来，打开那一张（找不到就清空筛选按文件名找，仍找不到提示已不在图片库中）。
// - imagesVersion：别处（图片管理、清理中心、数据维护…）改了图片时 App 递增；本页下次显示时按同一条件原地重取，滚动位置不动。
// 事件：navigate(key)、open-person({ id, name })、open-in-manage({ id, name })（「在图片管理中打开」）、image-changed(fields)（看图里存成功的收藏 / 评分）。
import { ListImageFolderGroups, ListImageTimelineBuckets, ListPeople, SearchImagePage } from '../../../wailsjs/go/main/App';
import PhotoLightbox from './PhotoLightbox.vue';
import AppBadge from '../ui/AppBadge.vue';
import AppBanner from '../ui/AppBanner.vue';
import AppIcon from '../ui/AppIcon.vue';
import AppSpinner from '../ui/AppSpinner.vue';
import BasePopover from '../ui/BasePopover.vue';
import EmptyState from '../ui/EmptyState.vue';
import FilterChip from '../ui/FilterChip.vue';
import PageHeader from '../ui/PageHeader.vue';
import SegmentedControl from '../ui/SegmentedControl.vue';
import SkeletonBlock from '../ui/SkeletonBlock.vue';
import { notify, translateBackendError } from '../../utils/feedback.js';
import { logFrontend } from '../../utils/frontendLog.js';
import {
  PHOTO_GROUP_MONTH, PHOTO_ROW_HEADER, buildPhotoLayout, calculatePhotoAnchorScrollTop, calculatePhotoWindow,
  findPhotoRowIndexForItem, firstVisiblePhotoItemIndex, formatPhotoTimelineLabel, resolvePhotoCellSize
} from '../../utils/photoGrid.js';
import { restoreScrollWhenReachable } from '../../utils/viewerNavigation.js';
// 只读复用片库列表的滚动宿主解析（.main-view）。
import { resolveScrollOwnerDescriptor } from '../../utils/virtualList.js';

export const PHOTO_BROWSE_PAGE_SIZE = 60;
// 原地刷新按已加载的张数从头重取，每批最多这么多（SearchImagePage 的上限是 200）。
export const PHOTO_BROWSE_REFRESH_BATCH = 200;
// 拍摄时间倒序（缺失按入库时间），与图片管理的时间线同一个排序值。
export const PHOTO_BROWSE_SORT = 'taken';
export const PHOTO_BROWSE_MISSING_MESSAGE = '这张图片已不在图片库中，可能已删除或被隐藏。';
// 格子是方形缩略图，没有信息条：列宽比图片管理的图片流（180）大，观看向。
const TILE_GEOMETRY = { minColumnWidth: 200, gap: 6, meta: 0, border: 0 };
const MONTH_HEADER_HEIGHT = 52;
const OVERSCAN_ROWS = 3;
// 视口底边离照片流底部不到这么远就取下一批，滚到底时下一批多半已经在了。
const LOAD_AHEAD_PX = 800;
const SEARCH_DEBOUNCE_MS = 300;
const PERSON_OPTION_LIMIT = 20;
// 文件夹浮层最多列这么多个（按名称筛），几千个文件夹时不把浮层撑成几千行。
export const PHOTO_BROWSE_FOLDER_LIMIT = 200;

// 本页的筛选 → ImageFilter：只用关键字、收藏、人物、文件夹，其余（标签、评分、大小、日期区间、AI、缺少某项）保持空。
export function photoBrowseFilter({ keyword = '', favoriteOnly = false, people = [], folder = null } = {}) {
  return {
    keyword: String(keyword || '').trim(),
    directory: String(folder?.directory || ''),
    tag_ids: [],
    person_ids: (people || []).map(person => Number(person.id)).filter(id => id > 0),
    favorite_only: Boolean(favoriteOnly),
    min_rating: null,
    max_rating: null,
    min_size: 0,
    max_size: 0,
    taken_after: null,
    taken_before: null,
    sort_mode: PHOTO_BROWSE_SORT,
    ai_tag_state: '',
    missing: ''
  };
}

// ListImageTimelineBuckets 的结果 → { 'YYYY-MM': 张数 }，键与 photoGrid 的分组键一致。
export function photoBucketCounts(buckets) {
  const counts = {};
  for (const bucket of buckets || []) {
    counts[`${bucket.year}-${String(bucket.month).padStart(2, '0')}`] = Number(bucket.count || 0);
  }
  return counts;
}

export function photoThumbnailURL(id) {
  return `/preview/image-thumbnail/${Number(id)}`;
}

function emptyList() {
  return { items: [], cursor: null, hasMore: false, loading: false, loadingMore: false, loaded: false, error: '', moreError: '' };
}

function emptyWindow() {
  return { startRow: 0, endRow: 0, topSpacer: 0, bottomSpacer: 0, totalHeight: 0 };
}

function errorText(err) {
  return translateBackendError(err?.message || err);
}

export default {
  name: 'PhotoBrowsePage',
  components: {
    AppBadge, AppBanner, AppIcon, AppSpinner, BasePopover, EmptyState, FilterChip, PageHeader, PhotoLightbox, SegmentedControl, SkeletonBlock
  },
  props: {
    pageActive: { type: Boolean, default: true },
    // { personId, personName?, token }：见文件头注释。
    navigationRequest: { type: Object, default: null },
    imagesVersion: { type: Number, default: 0 }
  },
  emits: ['navigate', 'open-person', 'open-in-manage', 'image-changed'],
  data() {
    return {
      keyword: '',
      favoriteOnly: false,
      people: [],
      folder: null,
      list: emptyList(),
      // { 'YYYY-MM': 张数 }；null = 还没读到或读失败（组头只写年月，页头不写总数）。
      buckets: null,
      failedThumbs: {},
      menu: null,
      menuAnchor: null,
      personKeyword: '',
      personSearch: { loading: false, error: '', items: [] },
      folderKeyword: '',
      folderOptions: { loading: false, error: '', items: [] },
      lightboxIndex: -1,
      columns: 1,
      cellHeight: 0,
      scrollOwnerEl: null,
      windowState: emptyWindow(),
      savedScrollTop: 0,
      // 别处改了图片、还没按同一条件重取过。
      imagesStale: false
    };
  },
  computed: {
    scopeOptions() {
      return [
        { value: '', label: '全部', dataTest: 'photo-browse-scope-all' },
        { value: 'favorites', label: '收藏', dataTest: 'photo-browse-scope-favorites' }
      ];
    },
    filtersActive() {
      return Boolean(this.keyword.trim() || this.favoriteOnly || this.people.length || this.folder);
    },
    countText() {
      if (!this.buckets) return '';
      const total = Object.values(this.buckets).reduce((sum, count) => sum + count, 0);
      const text = total.toLocaleString('zh-CN');
      return this.filtersActive ? `筛选出 ${text} 张` : `共 ${text} 张`;
    },
    layout() {
      return buildPhotoLayout({
        items: this.list.items,
        columns: this.columns,
        groupBy: PHOTO_GROUP_MONTH,
        cellHeight: this.cellHeight,
        headerHeight: MONTH_HEADER_HEIGHT,
        gap: TILE_GEOMETRY.gap
      });
    },
    // 找不到 .main-view（单独挂载的测试）时不窗口化，整条照片流照常渲染。
    virtualized() { return Boolean(this.scrollOwnerEl); },
    renderedRows() {
      const rows = this.virtualized ? this.layout.rows.slice(this.windowState.startRow, this.windowState.endRow) : this.layout.rows;
      return rows.map(row => ({
        ...row,
        isHeader: row.kind === PHOTO_ROW_HEADER,
        // 同一个年月可能出现两段（分批之间顺序有变时），组头的 key 带上 startIndex 才不撞。
        key: row.kind === PHOTO_ROW_HEADER ? `h:${row.startIndex}:${row.groupKey}` : `c:${row.startIndex}`
      }));
    },
    gridStyle() {
      return { '--photo-columns': String(this.columns), '--photo-gap': `${TILE_GEOMETRY.gap}px` };
    },
    lightboxImage() {
      return this.lightboxIndex >= 0 ? this.list.items[this.lightboxIndex] || null : null;
    },
    folderChoices() {
      const keyword = this.folderKeyword.trim().toLowerCase();
      const matched = keyword
        ? this.folderOptions.items.filter(group => String(group.name || '').toLowerCase().includes(keyword))
        : this.folderOptions.items;
      return { items: matched.slice(0, PHOTO_BROWSE_FOLDER_LIMIT), hidden: Math.max(0, matched.length - PHOTO_BROWSE_FOLDER_LIMIT) };
    }
  },
  watch: {
    navigationRequest(request) {
      if (this.applyNavigationRequest(request)) this.reloadNow();
    },
    // 别处改了图片：先记下；正在显示时等这一拍的显隐切换落定再重取（滚动位置要先放回去）。
    imagesVersion() {
      this.imagesStale = true;
      this.$nextTick(() => {
        if (this.pageActive && this.imagesStale) this.refreshInPlace();
      });
    },
    // 滚动宿主（.main-view）是各页共用的：离开前记下位置，切回来放回去；离开时看图与浮层一并收起
    // （看图挂在 body 上，不跟着页面隐藏）。
    pageActive(active) {
      if (!active) {
        this.closeMenu();
        this.lightboxIndex = -1;
        this.cancelScrollRestore();
        this.savedScrollTop = this.scrollOwnerEl?.scrollTop || 0;
        return;
      }
      this.$nextTick(() => {
        this.resolveScrollOwner();
        if (this.scrollOwnerEl) {
          this.cancelScrollRestore();
          this._cancelScrollRestore = restoreScrollWhenReachable(this.scrollOwnerEl, this.savedScrollTop);
        }
        this.syncWindow(true);
        if (this.imagesStale) this.refreshInPlace();
      });
    },
    // 翻页、换条件都整个替换 items；长度变了窗口要跟着重算（翻页后的新行、清空后的空网格）。
    'list.items.length'() {
      this.$nextTick(() => this.syncWindow());
    }
  },
  created() {
    this._tokens = { list: 0, buckets: 0, person: 0, folder: 0 };
    this._refreshing = 0;
    this._refreshSeq = 0;
    this.applyNavigationRequest(this.navigationRequest);
  },
  mounted() {
    this.reloadNow();
    this.$nextTick(() => {
      this.attachResizeObserver();
      this.syncWindow(true);
    });
  },
  beforeUnmount() {
    clearTimeout(this._searchTimer);
    clearTimeout(this._personTimer);
    this.cancelScrollRestore();
    this.detachScrollOwner();
    this._resizeObserver?.disconnect();
    for (const key of Object.keys(this._tokens)) this._tokens[key] += 1;
  },
  methods: {
    currentFilter() {
      return photoBrowseFilter({ keyword: this.keyword, favoriteOnly: this.favoriteOnly, people: this.people, folder: this.folder });
    },
    // 带参数打开本页（人物页「去图片」）：筛选回到默认、只按这个人。返回是否有参数被套用。
    applyNavigationRequest(request) {
      if (!request || typeof request !== 'object') return false;
      const personID = Number(request.personId || 0);
      this.keyword = '';
      this.favoriteOnly = false;
      this.folder = null;
      this.people = Number.isInteger(personID) && personID > 0
        ? [{ id: personID, name: String(request.personName || '').trim() || `人物 #${personID}` }]
        : [];
      this.closeMenu();
      return true;
    },
    setFavoriteOnly(value) {
      this.favoriteOnly = value === 'favorites';
      this.reloadNow();
    },
    personSelected(personID) {
      return this.people.some(person => Number(person.id) === Number(personID));
    },
    togglePerson(person) {
      const id = Number(person?.id);
      if (!id) return;
      this.people = this.personSelected(id)
        ? this.people.filter(item => Number(item.id) !== id)
        : [...this.people, { id, name: person.display_name || person.name || `人物 #${id}` }];
      this.reloadNow();
    },
    removePerson(personID) {
      this.people = this.people.filter(person => Number(person.id) !== Number(personID));
      this.reloadNow();
    },
    // 文件夹单选：再点一次已选的那个就是不按文件夹筛。
    selectFolder(group) {
      const directory = String(group?.directory || '');
      if (!directory) return;
      this.folder = this.folder?.directory === directory ? null : { directory, name: String(group.name || directory) };
      this.closeMenu();
      this.reloadNow();
    },
    clearFolder() {
      this.folder = null;
      this.reloadNow();
    },
    clearFilters() {
      this.keyword = '';
      this.favoriteOnly = false;
      this.people = [];
      this.folder = null;
      this.reloadNow();
    },
    // 输入法组词期间（isComposing）不发请求，选定文字后再算。
    scheduleReload(event) {
      if (event?.isComposing) return;
      clearTimeout(this._searchTimer);
      this._searchTimer = setTimeout(() => this.reloadNow(), SEARCH_DEBOUNCE_MS);
    },
    // 换条件从头取、回到顶部：这一代之前的请求（翻页、原地刷新、组头计数）作废。
    reloadNow() {
      clearTimeout(this._searchTimer);
      const token = ++this._tokens.list;
      this._morePromise = null;
      this._refreshing = 0;
      this.imagesStale = false;
      this.lightboxIndex = -1;
      this.list = { ...emptyList(), loading: true };
      this.failedThumbs = {};
      this.scrollToTop();
      this.loadBuckets();
      return this.fetchPage(token, null);
    },
    scrollToTop() {
      this.cancelScrollRestore();
      this.savedScrollTop = 0;
      this.windowState = emptyWindow();
      this.$nextTick(() => {
        this.resolveScrollOwner();
        if (this.pageActive && this.scrollOwnerEl) this.scrollOwnerEl.scrollTop = 0;
      });
    },
    // 下一批；已经在取时返回那一次（看图「下一张」要等它）。翻页出错后不自动重试，等用户点「重试」。
    loadMore() {
      if (this._morePromise) return this._morePromise;
      const list = this.list;
      if (list.loading || !list.hasMore || !list.cursor || this._refreshing) return Promise.resolve();
      this.list = { ...list, loadingMore: true, moreError: '' };
      const promise = this.fetchPage(this._tokens.list, list.cursor).finally(() => {
        if (this._morePromise === promise) this._morePromise = null;
      });
      this._morePromise = promise;
      return promise;
    },
    async fetchPage(token, cursor) {
      const first = !cursor;
      const request = { filter: this.currentFilter(), limit: PHOTO_BROWSE_PAGE_SIZE };
      if (cursor) request.cursor = cursor;
      try {
        const page = await SearchImagePage(request);
        if (token !== this._tokens.list) return;
        const known = new Set(first ? [] : this.list.items.map(image => Number(image.id)));
        const fresh = (page?.images || []).filter(image => !known.has(Number(image.id)));
        this.list = {
          ...this.list,
          items: first ? fresh : [...this.list.items, ...fresh],
          cursor: page?.next_cursor || null,
          hasMore: Boolean(page?.next_cursor),
          loading: false,
          loadingMore: false,
          loaded: true,
          error: '',
          moreError: ''
        };
      } catch (err) {
        if (token !== this._tokens.list) return;
        const message = errorText(err);
        this.list = first
          ? { ...this.list, loading: false, loaded: true, error: message }
          : { ...this.list, loadingMore: false, moreError: message };
      }
    },
    // 组头张数与页头总数：同一个筛选，读失败只是不显示数字。keep：原地刷新时先留着旧数字，读到新的再换（读失败也留着）。
    async loadBuckets({ keep = false } = {}) {
      const token = ++this._tokens.buckets;
      if (!keep) this.buckets = null;
      try {
        const buckets = await ListImageTimelineBuckets(this.currentFilter());
        if (token === this._tokens.buckets) this.buckets = photoBucketCounts(buckets);
      } catch (_err) {
        if (token === this._tokens.buckets && !keep) this.buckets = null;
      }
    },
    // 同一条件原地重取（别处改了图片）：列表不清空、不出「加载中」，从头按已加载的张数重取，取齐后整体替换，
    // 再把视口顶部那张放回原位（它被删了就放它原来位置上的那张）。取的途中条件变了作废；失败留着原来的列表，下次显示再取。
    async refreshInPlace() {
      this.imagesStale = false;
      const depth = this.list.items.length;
      if (!depth || this.list.loading) {
        if (!this.list.loading) this.reloadNow();
        return;
      }
      const token = this._tokens.list;
      const seq = this._refreshing = ++this._refreshSeq;
      this.loadBuckets({ keep: true });
      const refreshed = [];
      let cursor = null;
      let hasMore = true;
      try {
        while (hasMore && refreshed.length < depth) {
          const request = { filter: this.currentFilter(), limit: Math.min(PHOTO_BROWSE_REFRESH_BATCH, depth - refreshed.length) };
          if (cursor) request.cursor = cursor;
          const page = await SearchImagePage(request);
          if (token !== this._tokens.list) return;
          const images = page?.images || [];
          refreshed.push(...images);
          cursor = page?.next_cursor || null;
          hasMore = Boolean(cursor);
          if (!images.length) break;
        }
      } catch (err) {
        if (token === this._tokens.list) this.imagesStale = true;
        logFrontend('PhotoBrowsePage', 'refreshInPlace failed', { err: String(err) }, true);
        return;
      } finally {
        if (this._refreshing === seq) this._refreshing = 0;
      }
      if (token !== this._tokens.list) return;
      const anchor = this.captureAnchor();
      const openID = Number(this.lightboxImage?.id || 0);
      this.list = { ...this.list, items: refreshed, cursor, hasMore, loadingMore: false, moreError: '' };
      if (openID) this.lightboxIndex = refreshed.findIndex(image => Number(image.id) === openID);
      await this.$nextTick();
      this.syncWindow();
      await this.$nextTick();
      this.restoreAnchor(anchor);
    },
    // 视口顶部那张（不含预留行）：{ id, index, offset }，offset 是它那一行的顶边在视口里的位置。
    captureAnchor() {
      const owner = this.scrollOwnerEl;
      if (!owner || !this.virtualized || !this.pageActive) return null;
      const listTop = this.getListTop();
      const view = calculatePhotoWindow({
        layout: this.layout, scrollTop: owner.scrollTop, viewportHeight: owner.clientHeight, listTop, overscan: 0
      });
      const index = firstVisiblePhotoItemIndex(this.layout, view.startRow);
      if (index < 0) return null;
      const rowIndex = findPhotoRowIndexForItem(this.layout, index);
      return { id: Number(this.list.items[index].id), index, offset: listTop + this.layout.offsets[rowIndex] - owner.scrollTop };
    },
    restoreAnchor(anchor) {
      const owner = this.scrollOwnerEl;
      if (!anchor || !owner || !this.pageActive || !this.list.items.length) return;
      let index = this.list.items.findIndex(image => Number(image.id) === anchor.id);
      if (index < 0) index = Math.min(anchor.index, this.list.items.length - 1);
      const top = calculatePhotoAnchorScrollTop({ layout: this.layout, listTop: this.getListTop(), itemIndex: index }) - anchor.offset;
      owner.scrollTop = Math.max(0, top);
      this.syncWindow();
    },
    // 换了条件或进来时带着要找的那张：先在已加载的里找；没有就清空筛选、按文件名找（与图片管理的 locateImage 同口径）；
    // 仍找不到就提示已不在图片库中。
    async locateImage(target) {
      const id = Number(target?.id || 0);
      if (!id) return false;
      const loaded = this.list.items.findIndex(image => Number(image.id) === id);
      if (loaded >= 0) {
        this.openLightbox(loaded);
        return true;
      }
      this.keyword = String(target?.name || '');
      this.favoriteOnly = false;
      this.people = [];
      this.folder = null;
      this.closeMenu();
      await this.reloadNow();
      const found = this.list.items.findIndex(image => Number(image.id) === id);
      if (found < 0) {
        notify(PHOTO_BROWSE_MISSING_MESSAGE);
        return false;
      }
      this.openLightbox(found);
      return true;
    },
    openLightbox(index) {
      if (!this.list.items[index]) return;
      this.closeMenu();
      this.lightboxIndex = index;
    },
    // 关掉看图：刚才看的那张不在视口里（往后翻了很多张）时把它那一行滚进来。
    handleLightboxClose() {
      const index = this.lightboxIndex;
      this.lightboxIndex = -1;
      this.$nextTick(() => this.revealItem(index));
    },
    lightboxPrev() {
      if (this.lightboxIndex > 0) this.lightboxIndex -= 1;
    },
    // 下一张：已加载的末尾还有下一批时先取下一批再前进；取的途中换了条件或换了一张就不动。
    async lightboxNext() {
      const index = this.lightboxIndex;
      if (index < 0) return;
      if (index < this.list.items.length - 1) {
        this.lightboxIndex = index + 1;
        return;
      }
      if (!this.list.hasMore) return;
      const token = this._tokens.list;
      await this.loadMore();
      if (token !== this._tokens.list || this.lightboxIndex !== index) return;
      if (index < this.list.items.length - 1) this.lightboxIndex = index + 1;
    },
    // 看图改了这一张（收藏、评分、详情、查看次数）：就地并进列表。
    patchImage(fields) {
      const id = Number(fields?.id || 0);
      const index = this.list.items.findIndex(image => Number(image.id) === id);
      if (index < 0) return;
      this.list.items.splice(index, 1, { ...this.list.items[index], ...fields, id: this.list.items[index].id });
    },
    openPersonFromLightbox(person) {
      this.lightboxIndex = -1;
      this.$emit('open-person', person);
    },
    openInManage(image) {
      this.lightboxIndex = -1;
      this.$emit('open-in-manage', { id: Number(image?.id), name: image?.name || '' });
    },
    revealItem(index) {
      const owner = this.scrollOwnerEl;
      if (!owner || !this.virtualized || !this.pageActive) return;
      const rowIndex = findPhotoRowIndexForItem(this.layout, index);
      if (rowIndex < 0) return;
      const top = this.getListTop() + this.layout.offsets[rowIndex];
      const height = this.layout.rows[rowIndex].height;
      if (top >= owner.scrollTop && top + height <= owner.scrollTop + owner.clientHeight) return;
      owner.scrollTop = Math.max(0, top - Math.max(0, (owner.clientHeight - height) / 2));
      this.syncWindow();
    },
    // ===== 窗口化（与图片管理的网格同一套计算）=====
    resolveScrollOwner() {
      const { nextOwner, sameOwner } = resolveScrollOwnerDescriptor(this.$el, this.scrollOwnerEl);
      if (sameOwner) return;
      this.detachScrollOwner();
      this.scrollOwnerEl = nextOwner;
      this.scrollOwnerEl?.addEventListener('scroll', this.handleOwnerScroll, { passive: true });
    },
    detachScrollOwner() {
      this.scrollOwnerEl?.removeEventListener('scroll', this.handleOwnerScroll);
      this.scrollOwnerEl = null;
    },
    attachResizeObserver() {
      if (typeof ResizeObserver === 'undefined' || !this.$el?.nodeType) return;
      this._resizeObserver = new ResizeObserver(() => this.handleResize());
      this._resizeObserver.observe(this.$el);
    },
    // 宽度变了：列数与格子边长跟着变，视口顶部那张留在原处。隐藏时 ResizeObserver 报 0 宽，不理它。
    handleResize() {
      if (!this.pageActive || !this.list.items.length) return;
      const anchor = this.captureAnchor();
      if (!this.measureGrid()) {
        this.syncWindow();
        return;
      }
      this.syncWindow();
      this.$nextTick(() => this.restoreAnchor(anchor));
    },
    handleOwnerScroll() {
      if (this.pageActive) this.syncWindow();
    },
    getListTop() {
      const grid = this.$refs.grid;
      const owner = this.scrollOwnerEl;
      if (!owner || !grid) return 0;
      return owner.scrollTop + (grid.getBoundingClientRect().top - owner.getBoundingClientRect().top);
    },
    // 列数与格子边长只由网格宽度决定（格子是方的）；网格还没布局出宽度时保留上一次的值。
    measureGrid() {
      const width = this.$refs.grid?.getBoundingClientRect().width || 0;
      if (width <= 0) return false;
      const { columns, cellHeight } = resolvePhotoCellSize(width, TILE_GEOMETRY);
      const changed = columns !== this.columns || cellHeight !== this.cellHeight;
      this.columns = columns;
      this.cellHeight = cellHeight;
      return changed;
    },
    syncWindow(remeasure = false) {
      // 本页隐藏时滚的是别的页面，按那个 scrollTop 算窗口会一路触发翻页。
      if (!this.pageActive) return;
      this.resolveScrollOwner();
      if (remeasure || this.cellHeight === 0) this.measureGrid();
      if (!this.virtualized) {
        this.windowState = { startRow: 0, endRow: this.layout.rows.length, topSpacer: 0, bottomSpacer: 0, totalHeight: this.layout.totalHeight };
        return;
      }
      // 网格还没渲染出来、量不出格子边长（首批刚到、还没画）：这时的行高都是 0，按它算会误以为已经滚到底而多取一批。
      // 等网格画出来后的那次同步（items 长度的侦听）再算。
      if (!this.cellHeight) return;
      const owner = this.scrollOwnerEl;
      const listTop = this.getListTop();
      const next = calculatePhotoWindow({
        layout: this.layout, scrollTop: owner.scrollTop, viewportHeight: owner.clientHeight, listTop, overscan: OVERSCAN_ROWS
      });
      const current = this.windowState;
      if (next.startRow !== current.startRow || next.endRow !== current.endRow || next.topSpacer !== current.topSpacer
        || next.bottomSpacer !== current.bottomSpacer || next.totalHeight !== current.totalHeight) {
        this.windowState = next;
      }
      this.maybeLoadMore(listTop, next.totalHeight);
    },
    maybeLoadMore(listTop, totalHeight) {
      const owner = this.scrollOwnerEl;
      const list = this.list;
      if (!owner || !list.hasMore || list.loading || list.loadingMore || list.moreError || this._refreshing) return;
      if (owner.scrollTop + owner.clientHeight < listTop + totalHeight - LOAD_AHEAD_PX) return;
      this.loadMore();
    },
    cancelScrollRestore() {
      this._cancelScrollRestore?.();
      this._cancelScrollRestore = null;
    },
    rowImages(row) {
      return this.list.items.slice(row.startIndex, row.endIndex);
    },
    // 行间隙做成行自己的下内边距（border-box），占位块的高度与前缀和对得上。还没量出格子边长时不定高。
    rowStyle(row) {
      if (!this.cellHeight) return {};
      return { height: `${row.outerHeight}px`, paddingBottom: `${row.outerHeight - row.height}px` };
    },
    monthLabel(row) {
      return formatPhotoTimelineLabel(row, this.buckets ? this.buckets[row.groupKey] : undefined);
    },
    thumbnailURL: photoThumbnailURL,
    markThumbFailed(id) {
      this.failedThumbs = { ...this.failedThumbs, [id]: true };
    },
    formatBadge(image) {
      const format = String(image?.format || '').trim();
      if (format) return format.toUpperCase();
      const name = String(image?.name || '');
      const dot = name.lastIndexOf('.');
      return dot >= 0 ? name.slice(dot + 1).toUpperCase() : '图片';
    },
    toggleMenu(name, triggerRef) {
      if (this.menu === name) {
        this.closeMenu();
        return;
      }
      this.menu = name;
      this.menuAnchor = this.$refs[triggerRef] || null;
      if (name === 'people') {
        this.searchPeople();
        this.$nextTick(() => this.$refs.personInput?.focus());
      }
      if (name === 'folders') {
        this.loadFolderOptions();
        this.$nextTick(() => this.$refs.folderInput?.focus());
      }
    },
    closeMenu() {
      this.menu = null;
      this.menuAnchor = null;
    },
    schedulePersonSearch() {
      clearTimeout(this._personTimer);
      this._personTimer = setTimeout(() => this.searchPeople(), 200);
    },
    async searchPeople() {
      const token = ++this._tokens.person;
      this.personSearch = { ...this.personSearch, loading: true, error: '' };
      try {
        const items = await ListPeople(this.personKeyword.trim(), '', 0, PERSON_OPTION_LIMIT);
        if (token === this._tokens.person) this.personSearch = { loading: false, error: '', items: (items || []).filter(item => item?.person?.id) };
      } catch (_err) {
        if (token === this._tokens.person) this.personSearch = { loading: false, error: '人物搜索失败，请稍后再试', items: [] };
      }
    },
    // 文件夹选项：其余三样筛选下各文件夹的张数（不带当前选中的文件夹，免得只剩它一个）。每次打开浮层都重读。
    async loadFolderOptions() {
      const token = ++this._tokens.folder;
      this.folderOptions = { ...this.folderOptions, loading: true, error: '' };
      try {
        const groups = await ListImageFolderGroups(photoBrowseFilter({ keyword: this.keyword, favoriteOnly: this.favoriteOnly, people: this.people }));
        if (token === this._tokens.folder) this.folderOptions = { loading: false, error: '', items: (groups || []).filter(group => group?.directory) };
      } catch (err) {
        if (token === this._tokens.folder) this.folderOptions = { loading: false, error: errorText(err), items: [] };
      }
    }
  }
};
</script>

<style scoped>
.photo-browse {
  display: grid;
  gap: var(--space-4);
  align-content: start;
  padding-bottom: var(--space-7);
}
.photo-browse__toolbar { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-2) var(--space-3); }
.photo-browse__search { flex: 1 1 240px; width: auto; min-width: 200px; max-width: 420px; }
.photo-browse__trigger--on { border-color: var(--accent-border); color: var(--accent-text); }
.photo-browse__chips { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-2); margin-top: var(--space-3); }
.photo-browse__stream { display: grid; gap: var(--space-4); min-width: 0; }
.photo-browse__skeleton { display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); gap: 6px; }
.photo-browse__skeleton :deep(.ui-skeleton__media) { aspect-ratio: 1 / 1; }
/* 组头行的上半截是组与组之间的留白；第一组前面已有页头的间距，往上收一点，免得工具条和第一个组头之间空一大块。 */
.photo-browse__grid { min-width: 0; margin-top: calc(var(--space-5) * -1); }
.photo-browse__month {
  display: flex;
  align-items: flex-end;
  box-sizing: border-box;
  margin: 0;
  color: var(--text-primary);
  font-size: var(--fs-section);
  font-weight: var(--fw-semibold);
  line-height: 1.4;
}
.photo-browse__row {
  display: grid;
  grid-template-columns: repeat(var(--photo-columns, 1), minmax(0, 1fr));
  align-content: start;
  gap: var(--photo-gap, 6px);
  box-sizing: border-box;
}
.photo-browse__tile {
  position: relative;
  display: block;
  width: 100%;
  aspect-ratio: 1 / 1;
  overflow: hidden;
  border-radius: var(--radius-sm);
  background: var(--thumb-bg);
}
.photo-browse__tile img { display: block; width: 100%; height: 100%; object-fit: cover; transition: transform 400ms ease, filter var(--transition); }
.photo-browse__tile:hover img { transform: scale(1.03); filter: brightness(1.06); }
.photo-browse__tile-fallback {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: var(--space-1);
  width: 100%;
  height: 100%;
  background: var(--thumb-fallback-bg);
  color: var(--thumb-fg);
}
.photo-browse__tile-fallback small { font-size: var(--fs-caption); font-weight: var(--fw-semibold); }
.photo-browse__favorite-mark {
  position: absolute;
  top: var(--space-2);
  right: var(--space-2);
  display: inline-grid;
  place-items: center;
  width: 24px;
  height: 24px;
  border-radius: var(--radius-pill);
  background: var(--media-badge-bg);
  color: var(--warning-color);
}
.photo-browse__favorite-mark .ui-icon { fill: currentColor; }
.photo-browse__more { display: flex; align-items: center; justify-content: center; gap: var(--space-2); margin: 0; color: var(--text-muted); font-size: var(--fs-secondary); }
.photo-browse__popover-body { display: grid; gap: var(--space-3); max-width: 440px; max-height: 380px; overflow-y: auto; padding: var(--space-3); }
.photo-browse__popover-empty { display: flex; align-items: center; gap: var(--space-2); margin: 0; color: var(--text-muted); font-size: var(--fs-secondary); }
.photo-browse__option-list { display: grid; gap: 2px; }
.photo-browse__option {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  min-height: var(--h-compact);
  padding: 0 var(--space-2);
  border-radius: var(--radius-sm);
  color: var(--text-primary);
  font-size: var(--fs-secondary);
  text-align: left;
}
.photo-browse__option > span { flex: 1 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.photo-browse__option small { flex: none; color: var(--text-muted); font-size: var(--fs-caption); }
.photo-browse__option--folder { padding-top: var(--space-1); padding-bottom: var(--space-1); }
.photo-browse__option-text { display: grid; gap: 2px; }
.photo-browse__option-text > * { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.photo-browse__option:hover,
.photo-browse__option--on { background: var(--panel-muted-bg); }
.photo-browse__option--on { color: var(--accent-text); }
@media (prefers-reduced-motion: reduce) {
  .photo-browse__tile img { transition: none; }
  .photo-browse__tile:hover img { transform: none; }
}
</style>
