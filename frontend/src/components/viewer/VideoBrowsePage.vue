<template>
  <section class="page-content video-browse" data-test="video-browse">
    <PageHeader title="视频" :subtitle="countText">
      <template #actions>
        <!-- 随机拆分按钮（D-P2-01）：主键按随机条件在当前筛选内立即播放一部；▾ 里是今晚看什么与条件开关，
             与片库管理的同一组（utils/randomPlayback.js 共用一套流程、条件与排除表）。 -->
        <div class="video-browse__random" role="group" aria-label="随机播放">
          <button
            type="button"
            class="btn btn--primary video-browse__random-main"
            data-test="browse-random-play"
            :disabled="randomPlay.loading"
            title="在当前筛选内按随机条件立即播放一部，播放越少越容易被抽到"
            @click="playRandom"
          ><AppIcon name="play" :size="16" />{{ randomPlay.loading ? '正在抽…' : randomButtonLabel }}</button>
          <button
            ref="randomTrigger"
            type="button"
            class="btn btn--primary video-browse__random-caret"
            aria-label="随机选项"
            aria-haspopup="menu"
            :aria-expanded="menu === 'random' ? 'true' : 'false'"
            data-test="browse-random-menu"
            @click="toggleMenu('random', 'randomTrigger')"
          ><AppIcon name="chevron-down" :size="16" /></button>
        </div>
      </template>
      <template #below>
        <div class="video-browse__toolbar" role="search">
          <input
            ref="searchInput"
            v-model="keyword"
            type="search"
            class="search-input video-browse__search"
            placeholder="搜索片名"
            aria-label="搜索片名"
            data-test="browse-search"
            @input="scheduleReload"
            @keyup.enter="reloadNow"
          />
          <SegmentedControl
            label="观看状态"
            :options="watchOptions"
            :model-value="watch"
            data-test="browse-watch"
            @update:model-value="setWatch"
          />
          <button
            ref="tagTrigger"
            type="button"
            :class="['btn', 'btn--secondary', { 'video-browse__trigger--on': tagIDs.length > 0 }]"
            aria-haspopup="dialog"
            :aria-expanded="menu === 'tags' ? 'true' : 'false'"
            data-test="browse-tag-trigger"
            @click="toggleMenu('tags', 'tagTrigger')"
          >标签<AppBadge v-if="tagIDs.length" tone="accent" :count="tagIDs.length" /><AppIcon name="chevron-down" :size="16" /></button>
          <button
            ref="personTrigger"
            type="button"
            :class="['btn', 'btn--secondary', { 'video-browse__trigger--on': people.length > 0 }]"
            aria-haspopup="dialog"
            :aria-expanded="menu === 'people' ? 'true' : 'false'"
            data-test="browse-person-trigger"
            @click="toggleMenu('people', 'personTrigger')"
          >人物<AppBadge v-if="people.length" tone="accent" :count="people.length" /><AppIcon name="chevron-down" :size="16" /></button>
          <label class="video-browse__sort">
            <span>排序</span>
            <!-- 「最近播放」只按播放时间从近到远排（键集分页），排序锁住并写明；换回别的观看状态时恢复原来选的排序。 -->
            <select
              v-model="sortModel"
              class="select-input"
              aria-label="排序"
              data-test="browse-sort"
              :disabled="recentlyPlayed"
              :title="recentlyPlayed ? '最近播放按播放时间从近到远排列，不能换排序' : undefined"
              @change="reloadNow"
            >
              <option v-for="option in sortSelectOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
            </select>
          </label>
        </div>
        <div v-if="tagIDs.length || people.length" class="video-browse__chips" data-test="browse-active-filters">
          <FilterChip
            v-for="tag in selectedTagChips"
            :key="`tag-${tag.id}`"
            :label="tag.name"
            active
            removable
            :data-test="`browse-tag-chip-${tag.id}`"
            @toggle="toggleTag(tag.id)"
            @remove="toggleTag(tag.id)"
          />
          <FilterChip
            v-for="person in people"
            :key="`person-${person.id}`"
            :label="person.name"
            active
            removable
            :data-test="`browse-person-chip-${person.id}`"
            @toggle="removePerson(person.id)"
            @remove="removePerson(person.id)"
          />
          <button type="button" class="btn-link" data-test="browse-clear-chips" @click="clearFilters">清空筛选</button>
        </div>
      </template>
    </PageHeader>

    <!-- 随机播放的结果条（抽中了哪一部、30 秒内「换一个」不计入）：与片库页同一个组件，这里没有「随机 N 部」。 -->
    <RandomPickBanner :random-pick="inactiveRandomPick" :random-play="randomPlayBanner" @reroll="rerollRandomPlay" @dismiss-play="dismissRandomPlay" />

    <!-- 「追到哪了」（D-P2-02）：没有数据不显示；与首页同一套卡片（collectionProgressCard）。 -->
    <section v-if="progressVisible" class="recap-section video-browse__progress" data-test="browse-progress">
      <header class="video-browse__section-head">
        <h2><AppIcon name="collection" :size="20" /> 追到哪了</h2>
        <small>作品集看到第几部，点开接着看下一部</small>
      </header>
      <AppBanner v-if="progressBlock.error" tone="danger" title="读取「追到哪了」失败" :message="progressBlock.error" data-test="browse-progress-error">
        <template #actions><button type="button" class="btn btn--secondary btn--sm" data-test="browse-progress-retry" @click="loadProgress">重试</button></template>
      </AppBanner>
      <div v-else-if="progressBlock.loading && !progressCards.length" class="recap-strip video-browse__progress-strip" aria-busy="true" data-test="browse-progress-loading">
        <SkeletonBlock v-for="n in 5" :key="n" shape="poster" :lines="2" />
      </div>
      <div v-if="progressCards.length" class="recap-strip video-browse__progress-strip">
        <RecapMediaCard
          v-for="card in progressCards"
          :key="card.key"
          class="recap-strip__item"
          variant="poster"
          :id="card.videoID"
          :cover="card.cover"
          :title="card.title"
          :subtitle="card.subtitle"
          :badge="card.badge"
          data-test="browse-progress-item"
          @open="openVideoID(card.videoID)"
        />
      </div>
    </section>

    <section class="video-browse__wall" :aria-busy="list.loading ? 'true' : 'false'" aria-label="视频海报墙">
      <AppBanner v-if="list.error && !list.items.length" tone="danger" title="读取视频失败" :message="list.error" data-test="browse-error">
        <template #actions><button type="button" class="btn btn--secondary btn--sm" data-test="browse-retry" @click="reloadNow">重试</button></template>
      </AppBanner>
      <div v-else-if="list.loading && !list.items.length" class="video-browse__grid" data-test="browse-loading">
        <SkeletonBlock v-for="n in 12" :key="n" shape="poster" :lines="2" />
      </div>
      <EmptyState
        v-else-if="!list.items.length && list.loaded"
        icon="videos"
        :title="filtersActive ? '没有符合条件的视频' : '片库里还没有视频'"
        :message="filtersActive ? '换个片名，或者少选几个条件试试。' : '把视频放进扫描目录，扫描后就会出现在这里。'"
        data-test="browse-empty"
      >
        <template #actions>
          <button v-if="filtersActive" type="button" class="btn btn--secondary" data-test="browse-empty-clear" @click="clearFilters">清空筛选</button>
          <button v-else type="button" class="btn btn--secondary" data-test="browse-empty-scan" @click="$emit('navigate', 'settings')">去添加扫描目录</button>
        </template>
      </EmptyState>
      <div v-else class="video-browse__grid" data-test="browse-grid">
        <button
          v-for="video in list.items"
          :key="video.id"
          type="button"
          class="btn-plain video-browse__card"
          :title="cardTitle(video)"
          data-test="browse-card"
          @click="openVideoID(video.id)"
        >
          <span class="video-browse__poster">
            <img v-if="!posterFailed[video.id]" :src="posterURL(video.id)" alt="" loading="lazy" decoding="async" @error="markPosterFailed(video.id)" />
            <span v-else class="video-browse__poster-fallback"><AppIcon name="videos" :size="24" /></span>
            <span v-if="cardMark(video)" :class="['video-browse__mark', `video-browse__mark--${cardMark(video).kind}`]" data-test="browse-card-mark">{{ cardMark(video).label }}</span>
            <span v-if="versionCount(video) > 1" class="video-browse__versions">{{ versionCount(video) }} 个版本</span>
          </span>
          <span class="video-browse__card-text">
            <strong>{{ cardTitle(video) }}</strong>
            <small>{{ cardYear(video) }}</small>
          </span>
        </button>
      </div>

      <div ref="sentinel" class="video-browse__sentinel" aria-hidden="true"></div>
      <p v-if="list.loadingMore" class="video-browse__more" role="status" data-test="browse-loading-more"><AppSpinner :size="16" /> 正在加载更多…</p>
      <AppBanner v-else-if="list.moreError" tone="danger" title="加载更多失败" :message="list.moreError" data-test="browse-more-error">
        <template #actions><button type="button" class="btn btn--secondary btn--sm" data-test="browse-more-retry" @click="loadMore">重试</button></template>
      </AppBanner>
      <p v-else-if="list.hasMore && list.items.length" class="video-browse__more">
        <button type="button" class="btn btn--secondary btn--sm" data-test="browse-load-more" @click="loadMore">加载更多</button>
      </p>
    </section>

    <BaseMenu
      v-if="menu === 'random'"
      :anchor="menuAnchor"
      :items="randomMenu"
      :min-width="220"
      align="end"
      label="随机选项"
      @select="onRandomSelect"
      @close="closeMenu"
    />
    <BasePopover v-if="menu === 'tags'" :anchor="menuAnchor" :min-width="320" @close="closeMenu">
      <div class="video-browse__popover-body" role="group" aria-label="按标签筛选" data-test="browse-tag-popover">
        <p v-if="!tagGroups.length" class="video-browse__popover-empty">还没有可选的标签</p>
        <div v-for="group in tagGroups" :key="group.key" class="video-browse__tag-group">
          <span class="video-browse__tag-group-label">{{ group.label }}</span>
          <div class="video-browse__tag-list">
            <FilterChip
              v-for="tag in group.tags"
              :key="tag.id"
              :label="tag.name"
              :active="tagIDs.includes(Number(tag.id))"
              :toggle-data-test="`browse-tag-option-${tag.id}`"
              @toggle="toggleTag(tag.id)"
            />
          </div>
        </div>
      </div>
    </BasePopover>
    <BasePopover v-if="menu === 'people'" :anchor="menuAnchor" :min-width="280" @close="closeMenu">
      <div class="video-browse__popover-body" data-test="browse-person-popover">
        <input
          ref="personInput"
          v-model="personKeyword"
          type="search"
          class="search-input"
          placeholder="搜索人物"
          aria-label="搜索人物"
          data-test="browse-person-search"
          @input="schedulePersonSearch"
        />
        <p v-if="personSearch.error" class="video-browse__popover-empty" role="alert">{{ personSearch.error }}</p>
        <p v-else-if="personSearch.loading && !personSearch.items.length" class="video-browse__popover-empty"><AppSpinner :size="16" /> 正在查找…</p>
        <p v-else-if="!personSearch.items.length" class="video-browse__popover-empty">没有匹配的人物</p>
        <div v-else class="video-browse__person-list" role="group" aria-label="人物">
          <button
            v-for="item in personSearch.items"
            :key="item.person.id"
            type="button"
            :class="['btn-plain', 'video-browse__person', { 'video-browse__person--on': personSelected(item.person.id) }]"
            :aria-pressed="personSelected(item.person.id) ? 'true' : 'false'"
            :data-test="`browse-person-option-${item.person.id}`"
            @click="togglePerson(item.person)"
          >
            <span>{{ item.person.display_name }}</span>
            <small>{{ item.active_video_count }} 部</small>
            <AppIcon v-if="personSelected(item.person.id)" name="check" :size="16" />
          </button>
        </div>
      </div>
    </BasePopover>

    <TonightPickerDialog
      v-if="tonightOpen"
      :filter="tonightFilter"
      :conditions="randomConditions"
      :exclude-ids="recentRandomVideoIDs"
      @update:conditions="randomConditions = $event"
      @close="tonightOpen = false"
      @preview="openTonightSuggestion"
      @play="playTonightSuggestion"
      @play-random="playRandomFromTonight"
    />
  </section>
</template>

<script>
// 视频海报墙（UI 二期 D-P2-01，详细设计 §3.1）：观看侧的「视频」页，只做浏览。
// - 筛选状态归本页（不与片库管理共享）：片名搜索（file 模式）、观看状态（对应智能视图）、标签、人物、排序；
//   页面常挂载（App 里 v-show），切走再回来筛选、已加载的卡片和滚动位置都在。
// - 列表：SearchLibraryVideoPage，collapse_versions 恒为 true（版本组一张卡），每批 60，接近底部自动加载下一批；
//   总数另取 CountLibraryVideos（按卡片计），失败只是不显示数字。每次换条件换代，晚到的旧结果丢掉。
// - 「追到哪了」：ListCollectionProgress({ continue_only: true })，独立加载、独立失败，没有数据不显示；页面切回来时重取。
// - 随机播放与今晚看什么：randomPlaybackMixin（与片库管理同一套流程、条件与排除表）。
// - navigationRequest：App 的 navigateTo('videos', { keyword | watch | tagId }) 带来的一次新浏览——筛选回到默认再套上参数
//   （排序保留），滚动回到顶部。
// 事件：open-video(id) 进整页详情（App 按 from='videos' 分流）；navigate(key) 去别的页面（空片库的「去添加扫描目录」）。
import {
  CountLibraryVideos, ListCollectionProgress, ListPeople, ListRecentlyPlayedWithFilter, PlayVideo, SearchLibraryVideoPage
} from '../../../wailsjs/go/main/App';
import RecapMediaCard from '../recap/RecapMediaCard.vue';
import TonightPickerDialog from '../TonightPickerDialog.vue';
import RandomPickBanner from '../video-list/RandomPickBanner.vue';
import AppBadge from '../ui/AppBadge.vue';
import AppBanner from '../ui/AppBanner.vue';
import AppIcon from '../ui/AppIcon.vue';
import AppSpinner from '../ui/AppSpinner.vue';
import BaseMenu from '../ui/BaseMenu.vue';
import BasePopover from '../ui/BasePopover.vue';
import EmptyState from '../ui/EmptyState.vue';
import FilterChip from '../ui/FilterChip.vue';
import PageHeader from '../ui/PageHeader.vue';
import SegmentedControl from '../ui/SegmentedControl.vue';
import SkeletonBlock from '../ui/SkeletonBlock.vue';
import { notifyError, translateBackendError } from '../../utils/feedback.js';
import { scrubAbsolutePaths } from '../../utils/pathText.js';
import { randomConditionMenuItems, randomConditionTags, randomPlaybackMixin } from '../../utils/randomPlayback.js';
import { videoArtwork } from '../../utils/recap.js';
import { restoreScrollWhenReachable } from '../../utils/viewerNavigation.js';
import { resumable } from '../../utils/watchState.js';

export const VIDEO_BROWSE_PAGE_SIZE = 60;
export const VIDEO_BROWSE_PROGRESS_LIMIT = 12;
export const VIDEO_BROWSE_DEFAULT_SORT = 'added_desc';
// 观看状态就是这几个智能视图（详细设计 §3.1）；命令面板「视频 · 收藏」等落在这一页时也只认这几个（appCommands.js）。
export const VIDEO_BROWSE_WATCH_OPTIONS = Object.freeze([
  { value: '', label: '全部', dataTest: 'browse-watch-all' },
  { value: 'unwatched', label: '没看过', dataTest: 'browse-watch-unwatched' },
  { value: 'continue_watching', label: '在看', dataTest: 'browse-watch-continue' },
  { value: 'watched', label: '看过', dataTest: 'browse-watch-watched' },
  { value: 'favorites', label: '收藏', dataTest: 'browse-watch-favorites' },
  { value: 'liked', label: '点赞', dataTest: 'browse-watch-liked' },
  { value: 'recently_played', label: '最近播放', dataTest: 'browse-watch-recent' }
]);
// 「最近播放」按最近一次播放倒序：走 ListRecentlyPlayedWithFilter 的键集分页（last_played_at DESC, id DESC），
// 不走 SearchLibraryVideoPage，排序选择不适用（锁成这一项）。
export const RECENTLY_PLAYED_VIEW = 'recently_played';
export const RECENTLY_PLAYED_SORT_HINT = '按最近播放时间';
export const VIDEO_BROWSE_SORT_OPTIONS = Object.freeze([
  { value: 'added_desc', label: '最近添加' },
  { value: 'year_desc', label: '年份' },
  { value: 'title_asc', label: '片名' },
  { value: 'rating_desc', label: '评分' },
  { value: 'balanced', label: '均衡' }
]);
const PERSON_OPTION_LIMIT = 20;
const SEARCH_DEBOUNCE_MS = 300;
// 哨兵离可视区还有这么远就开始取下一批，滚到底时下一批多半已经在了。
const LOAD_AHEAD_MARGIN = '800px 0px';

// 本页的筛选状态 → LibraryFilter。只用片名（file）搜索，版本组合并成一张卡——「最近播放」除外：
// ListRecentlyPlayedWithFilter 不认 collapse_versions（每个播放过的文件各占一张卡），这里如实写 false，
// 让 CountLibraryVideos 的总数也按文件计、与列表对得上。
export function videoBrowseFilter({ keyword = '', watch = '', tagIDs = [], people = [], sort = VIDEO_BROWSE_DEFAULT_SORT } = {}) {
  return {
    search_mode: 'file',
    keyword: String(keyword || '').trim(),
    smart_view: watch || '',
    tag_ids: tagIDs.map(Number).filter(id => id > 0),
    person_ids: people.map(person => Number(person.id)).filter(id => id > 0),
    sort_mode: sort || VIDEO_BROWSE_DEFAULT_SORT,
    collapse_versions: watch !== RECENTLY_PLAYED_VIEW
  };
}

// 卡片角标：断点可续算「在看」（与「在看」视图同一判定），否则看过算「看过」。
export function videoBrowseMark(video) {
  if (resumable(video)) return { kind: 'progress', label: '在看' };
  if (video?.is_watched) return { kind: 'watched', label: '看过' };
  return null;
}

// 「追到哪了」的一张卡（首页与本页共用）：作品集封面（没有就用下一部的海报）、名称、「第 N / M 部」、下一部片名；
// 点开进下一部的整页详情。没有下一部（已看完）的返回 null。
export function collectionProgressCard(progress) {
  const next = progress?.next;
  const videoID = Number(next?.video_id || 0);
  const collectionID = Number(progress?.collection_id || 0);
  if (!videoID || !collectionID) return null;
  const position = `第 ${Number(next.work_index || 0)} / ${Number(progress.total_works || 0)} 部`;
  return {
    key: collectionID,
    collectionID,
    videoID,
    title: String(progress.name || '').trim() || '未命名作品集',
    cover: progress.has_cover ? `/preview/collection-cover/${collectionID}` : videoArtwork(videoID, 'poster'),
    position,
    nextTitle: String(next.title || '').trim(),
    subtitle: [position, String(next.title || '').trim()].filter(Boolean).join(' · '),
    badge: next.resume ? '续播' : ''
  };
}

// 「最近播放」下一页的游标：上一页最后一行的 last_played_at 原样回传（后端按 RFC3339Nano 解析）与它的 id，
// 同一时刻播放的几部靠 id 排开（键集 (last_played_at, id)）。接口只回数组：不满一页就是最后一页；
// 最后一行缺播放时间（不该出现）时也停，免得发出后端会拒绝的半截游标。
export function recentlyPlayedNextCursor(videos, limit = VIDEO_BROWSE_PAGE_SIZE) {
  const list = Array.isArray(videos) ? videos : [];
  const last = list[list.length - 1];
  if (list.length < limit || !last?.last_played_at || !Number(last.id)) return null;
  return { lastPlayedAt: String(last.last_played_at), id: Number(last.id) };
}

function emptyList() {
  return { items: [], cursor: null, hasMore: false, loading: false, loadingMore: false, loaded: false, error: '', moreError: '' };
}

export default {
  name: 'VideoBrowsePage',
  components: {
    AppBadge, AppBanner, AppIcon, AppSpinner, BaseMenu, BasePopover, EmptyState, FilterChip, PageHeader, RandomPickBanner,
    RecapMediaCard, SegmentedControl, SkeletonBlock, TonightPickerDialog
  },
  mixins: [randomPlaybackMixin],
  props: {
    pageActive: { type: Boolean, default: true },
    // 全部标签（App 的 GetAllTags），标签筛选从这里挑。
    tags: { type: Array, default: () => [] },
    // { keyword?, watch?, tagId?, token }：见文件头注释。
    navigationRequest: { type: Object, default: null }
  },
  emits: ['open-video', 'navigate'],
  data() {
    return {
      keyword: '',
      watch: '',
      tagIDs: [],
      people: [],
      sort: VIDEO_BROWSE_DEFAULT_SORT,
      list: emptyList(),
      versionGroups: {},
      totalCount: null,
      progressBlock: { loading: false, error: '', items: [] },
      posterFailed: {},
      menu: null,
      menuAnchor: null,
      personKeyword: '',
      personSearch: { loading: false, error: '', items: [] },
      tonightOpen: false,
      tonightFilter: {},
      inactiveRandomPick: Object.freeze({ active: false, ids: [], reason: '', loading: false }),
      savedScrollTop: 0
    };
  },
  computed: {
    watchOptions() { return VIDEO_BROWSE_WATCH_OPTIONS; },
    sortOptions() { return VIDEO_BROWSE_SORT_OPTIONS; },
    recentlyPlayed() { return this.watch === RECENTLY_PLAYED_VIEW; },
    sortSelectOptions() {
      return this.recentlyPlayed ? [{ value: RECENTLY_PLAYED_VIEW, label: RECENTLY_PLAYED_SORT_HINT }] : VIDEO_BROWSE_SORT_OPTIONS;
    },
    // 选「最近播放」时下拉显示那一句说明；用户原来选的排序留在 sort 里，换回别的观看状态时照用。
    sortModel: {
      get() { return this.recentlyPlayed ? RECENTLY_PLAYED_VIEW : this.sort; },
      set(value) { if (!this.recentlyPlayed) this.sort = value; }
    },
    filtersActive() {
      return Boolean(this.keyword.trim() || this.watch || this.tagIDs.length || this.people.length);
    },
    countText() {
      if (this.totalCount === null) return '';
      const count = Number(this.totalCount).toLocaleString('zh-CN');
      return this.filtersActive ? `筛选出 ${count} 部` : `共 ${count} 部`;
    },
    randomButtonLabel() {
      return ['随机播放', ...randomConditionTags(this.randomConditions)].join(' · ');
    },
    randomMenu() {
      return [{ id: 'tonight', label: '今晚看什么…' }, { divider: true }, ...randomConditionMenuItems(this.randomConditions)];
    },
    // 与片库工具栏同一分组：按命名空间，「自动」命名空间（自动分类）不列。
    tagGroups() {
      const groups = new Map();
      for (const tag of this.tags || []) {
        const key = String(tag?.namespace || '').trim();
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
    selectedTagChips() {
      return this.tagIDs.map(id => {
        const tag = (this.tags || []).find(item => Number(item?.id) === id);
        return { id, name: tag?.name || `标签 #${id}` };
      });
    },
    progressCards() {
      return (this.progressBlock.items || []).map(collectionProgressCard).filter(Boolean);
    },
    progressVisible() {
      return Boolean(this.progressBlock.error || (this.progressBlock.loading && !this.progressCards.length) || this.progressCards.length);
    }
  },
  watch: {
    navigationRequest(request) {
      if (this.applyNavigationRequest(request)) this.reloadNow();
    },
    // 滚动宿主（.main-view）是各页共用的：离开前记下位置，切回来等内容够高再放回去（与图片页同一做法）。
    pageActive(active) {
      if (!active) {
        this.closeMenu();
        this.cancelScrollRestore();
        this.savedScrollTop = this.scrollOwner()?.scrollTop || 0;
        return;
      }
      this.loadProgress();
      this.$nextTick(() => {
        const owner = this.scrollOwner();
        if (!owner) return;
        this.cancelScrollRestore();
        this._cancelScrollRestore = restoreScrollWhenReachable(owner, this.savedScrollTop);
      });
    }
  },
  created() {
    this._tokens = { list: 0, count: 0, progress: 0, person: 0 };
    this.applyNavigationRequest(this.navigationRequest);
  },
  mounted() {
    window.addEventListener('keydown', this.handleShortcut);
    this.reloadNow();
    this.loadProgress();
    this.observeSentinel();
  },
  beforeUnmount() {
    window.removeEventListener('keydown', this.handleShortcut);
    clearTimeout(this._searchTimer);
    clearTimeout(this._personTimer);
    this.cancelScrollRestore();
    this._observer?.disconnect();
    for (const key of Object.keys(this._tokens)) this._tokens[key] += 1;
  },
  methods: {
    currentFilter() {
      return videoBrowseFilter({ keyword: this.keyword, watch: this.watch, tagIDs: this.tagIDs, people: this.people, sort: this.sort });
    },
    // randomPlaybackMixin 的宿主钩子：随机与今晚看什么都在当前筛选里抽。
    randomPlayFilter() {
      return this.currentFilter();
    },
    // 播放结果：没播起来给出原因（擦掉路径）；标了失效的卡片拿掉（失效记录不在海报墙里），其余并上库里的新值。
    applyPlaybackAttemptResult(result) {
      if (!result) return;
      const reconcile = result.reconcile_result;
      if (!result.dispatch_succeeded) notifyError(scrubAbsolutePaths(String(result.user_message || '播放失败')));
      const id = Number(reconcile?.video_id || 0);
      if (!id) return;
      if (reconcile.did_mark_stale) {
        this.list.items = this.list.items.filter(video => Number(video.id) !== id);
        return;
      }
      if (reconcile.updated_video) this.mergeVideo(id, reconcile.updated_video);
    },
    // 整页详情改了收藏、已看、评分、进度或失效（App 转交 { id, changes }）：并到已加载的那张卡上，没加载到就不管。
    applyExternalVideoStateChange({ id, changes } = {}) {
      const videoID = Number(id);
      if (!videoID || !changes) return;
      if (changes.is_stale) {
        this.list.items = this.list.items.filter(video => Number(video.id) !== videoID);
        return;
      }
      this.mergeVideo(videoID, changes);
    },
    mergeVideo(id, changes) {
      const index = this.list.items.findIndex(video => Number(video.id) === id);
      if (index === -1) return;
      this.list.items.splice(index, 1, { ...this.list.items[index], ...changes, id: this.list.items[index].id });
    },
    // 带参数打开本页：筛选回到默认再套上参数（排序保留），滚动回到顶部。返回是否有参数被套用。
    applyNavigationRequest(request) {
      if (!request || typeof request !== 'object') return false;
      this.keyword = String(request.keyword || '');
      this.watch = VIDEO_BROWSE_WATCH_OPTIONS.some(option => option.value === request.watch) ? request.watch : '';
      const tagID = Number(request.tagId || 0);
      this.tagIDs = Number.isInteger(tagID) && tagID > 0 ? [tagID] : [];
      this.people = [];
      this.closeMenu();
      this.cancelScrollRestore();
      this.savedScrollTop = 0;
      this.$nextTick(() => {
        const owner = this.scrollOwner();
        if (owner && this.pageActive) owner.scrollTop = 0;
      });
      return true;
    },
    setWatch(value) {
      this.watch = value;
      this.reloadNow();
    },
    toggleTag(tagID) {
      const id = Number(tagID);
      this.tagIDs = this.tagIDs.includes(id) ? this.tagIDs.filter(item => item !== id) : [...this.tagIDs, id];
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
    clearFilters() {
      this.keyword = '';
      this.watch = '';
      this.tagIDs = [];
      this.people = [];
      this.reloadNow();
    },
    // 输入法组词期间（isComposing）不发请求，选定文字后再算。
    scheduleReload(event) {
      if (event?.isComposing) return;
      clearTimeout(this._searchTimer);
      this._searchTimer = setTimeout(() => this.reloadNow(), SEARCH_DEBOUNCE_MS);
    },
    // 换条件从头取：这一代之前的请求（含翻页与计数）作废。
    reloadNow() {
      clearTimeout(this._searchTimer);
      const token = ++this._tokens.list;
      this.list = { ...emptyList(), loading: true };
      this.versionGroups = {};
      this.loadCount();
      return this.fetchPage(token, null);
    },
    loadMore() {
      const list = this.list;
      if (list.loading || list.loadingMore || !list.hasMore || !list.cursor) return null;
      this.list = { ...list, loadingMore: true, moreError: '' };
      return this.fetchPage(this._tokens.list, list.cursor);
    },
    // 一页：「最近播放」走 ListRecentlyPlayedWithFilter 的键集分页（游标是 { lastPlayedAt, id }），其余走
    // SearchLibraryVideoPage（游标是后端给的 next_cursor）。两条路同样按代次丢弃晚到的旧页、翻页去重。
    async fetchRawPage(cursor) {
      const filter = this.currentFilter();
      if (this.recentlyPlayed) {
        const videos = (await ListRecentlyPlayedWithFilter(filter, cursor?.lastPlayedAt || '', Number(cursor?.id || 0), VIDEO_BROWSE_PAGE_SIZE)) || [];
        return { videos, next_cursor: recentlyPlayedNextCursor(videos), version_groups: {} };
      }
      const request = { filter, limit: VIDEO_BROWSE_PAGE_SIZE };
      if (cursor) request.cursor = cursor;
      return SearchLibraryVideoPage(request);
    },
    async fetchPage(token, cursor) {
      const first = !cursor;
      try {
        const page = await this.fetchRawPage(cursor);
        if (token !== this._tokens.list) return;
        const known = new Set(first ? [] : this.list.items.map(video => Number(video.id)));
        const fresh = (page?.videos || []).filter(video => !known.has(Number(video.id)));
        this.versionGroups = { ...this.versionGroups, ...(page?.version_groups || {}) };
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
        // 哨兵仍在可视区附近（一屏放得下这一批）时接着取，不等用户再滚。
        this.$nextTick(() => {
          if (this._sentinelVisible && this.pageActive) this.loadMore();
        });
      } catch (err) {
        if (token !== this._tokens.list) return;
        const message = translateBackendError(err?.message || err);
        this.list = first
          ? { ...this.list, loading: false, loaded: true, error: message }
          : { ...this.list, loadingMore: false, moreError: message };
      }
    },
    async loadCount() {
      const token = ++this._tokens.count;
      this.totalCount = null;
      try {
        const count = await CountLibraryVideos(this.currentFilter());
        if (token === this._tokens.count) this.totalCount = Number(count) || 0;
      } catch (_err) {
        if (token === this._tokens.count) this.totalCount = null;
      }
    },
    async loadProgress() {
      const token = ++this._tokens.progress;
      this.progressBlock = { ...this.progressBlock, loading: true, error: '' };
      try {
        const items = await ListCollectionProgress({ collection_ids: [], continue_only: true, limit: VIDEO_BROWSE_PROGRESS_LIMIT });
        if (token === this._tokens.progress) this.progressBlock = { loading: false, error: '', items: items || [] };
      } catch (err) {
        if (token === this._tokens.progress) {
          this.progressBlock = { ...this.progressBlock, loading: false, error: translateBackendError(err?.message || err) };
        }
      }
    },
    observeSentinel() {
      if (typeof IntersectionObserver !== 'function' || !this.$refs.sentinel) return;
      this._observer = new IntersectionObserver(entries => {
        const entry = entries[entries.length - 1];
        this._sentinelVisible = Boolean(entry?.isIntersecting);
        if (this._sentinelVisible && this.pageActive) this.loadMore();
      }, { root: this.scrollOwner(), rootMargin: LOAD_AHEAD_MARGIN });
      this._observer.observe(this.$refs.sentinel);
    },
    scrollOwner() {
      return this.$el?.closest?.('.main-view') || null;
    },
    cancelScrollRestore() {
      this._cancelScrollRestore?.();
      this._cancelScrollRestore = null;
    },
    posterURL(id) {
      return videoArtwork(id, 'poster');
    },
    markPosterFailed(id) {
      this.posterFailed = { ...this.posterFailed, [id]: true };
    },
    cardTitle(video) {
      return String(video?.display_title || video?.name || `视频 #${video?.id}`);
    },
    cardYear(video) {
      const year = Number(video?.year || 0);
      return year > 0 ? `${year}` : '年份未知';
    },
    cardMark: videoBrowseMark,
    versionCount(video) {
      return Number(this.versionGroups?.[video.id]?.member_count || 0);
    },
    openVideoID(id) {
      const videoID = Number(id);
      if (videoID > 0) this.$emit('open-video', videoID);
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
    onRandomSelect(item) {
      if (item?.id === 'tonight') {
        this.openTonightPicker();
        return;
      }
      this.applyRandomConditionItem(item?.id);
    },
    openTonightPicker() {
      this.tonightFilter = this.currentFilter();
      this.tonightOpen = true;
    },
    // 今晚看什么：点候选进整页详情（观看侧没有抽屉）；「播放」照旧直接播放；「随机播放一部」走主键同一流程。
    openTonightSuggestion(video) {
      this.tonightOpen = false;
      this.openVideoID(video?.id);
    },
    async playTonightSuggestion(video) {
      this.tonightOpen = false;
      try {
        await this.applyPlaybackAttemptResult(await PlayVideo(video.id));
      } catch (err) {
        notifyError('播放失败: ' + err);
      }
    },
    playRandomFromTonight() {
      this.tonightOpen = false;
      return this.playRandom();
    },
    // ⌘F 聚焦搜索框（与片库页同一快捷键）；本页不可见或有对话框开着时不接。
    handleShortcut(event) {
      if (!this.pageActive || !(event.metaKey || event.ctrlKey) || event.shiftKey || event.altKey) return;
      if (String(event.key || '').toLowerCase() !== 'f' || document.querySelector('[role="dialog"]')) return;
      event.preventDefault();
      this.$refs.searchInput?.focus();
      this.$refs.searchInput?.select();
    }
  }
};
</script>

<style scoped>
.video-browse {
  display: grid;
  gap: var(--space-5);
  align-content: start;
  padding-bottom: var(--space-7);
}
.video-browse__random { display: inline-flex; align-items: stretch; }
.video-browse__random-main { border-top-right-radius: 0; border-bottom-right-radius: 0; }
.video-browse__random-caret {
  width: 34px;
  padding: 0;
  border-left: 1px solid var(--accent-hover);
  border-top-left-radius: 0;
  border-bottom-left-radius: 0;
}
.video-browse__toolbar { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-2) var(--space-3); }
.video-browse__search { flex: 1 1 240px; width: auto; min-width: 200px; max-width: 420px; }
.video-browse__trigger--on { border-color: var(--accent-border); color: var(--accent-text); }
.video-browse__sort { display: inline-flex; align-items: center; gap: var(--space-2); margin-left: auto; color: var(--text-secondary); font-size: var(--fs-secondary); }
.video-browse__sort .select-input { width: auto; min-width: 120px; }
.video-browse__chips { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-2); margin-top: var(--space-3); }
.video-browse__section-head { display: flex; flex-wrap: wrap; align-items: baseline; gap: var(--space-1) var(--space-3); }
.video-browse__section-head h2 {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  margin: 0;
  font-size: var(--fs-section);
  font-weight: var(--fw-bold);
}
.video-browse__section-head h2 :deep(svg) { color: var(--accent-text); }
.video-browse__section-head small { color: var(--text-muted); font-size: var(--fs-caption); }
/* 条带里的卡与海报墙的卡一样大，不压过下面的主体。 */
.video-browse__progress-strip { grid-auto-columns: minmax(140px, 156px); }
.video-browse__wall { display: grid; gap: var(--space-4); }
.video-browse__grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(150px, 1fr)); gap: var(--space-5) var(--space-4); }
/* 每张卡离开可视区就跳过渲染（几千张的海报墙不做窗口化，详细设计 §3.1）；占位高度近似一张卡。 */
.video-browse__card {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  min-width: 0;
  color: var(--text-primary);
  text-align: left;
  content-visibility: auto;
  contain-intrinsic-size: auto 290px;
}
.video-browse__poster {
  position: relative;
  display: block;
  width: 100%;
  aspect-ratio: 2 / 3;
  overflow: hidden;
  border: 1px solid var(--hairline);
  border-radius: var(--radius);
  background: var(--thumb-bg);
  transition: border-color var(--transition);
}
.video-browse__poster img { display: block; width: 100%; height: 100%; object-fit: cover; transition: transform 400ms ease; }
.video-browse__card:hover .video-browse__poster { border-color: var(--accent-border); }
.video-browse__card:hover .video-browse__poster img { transform: scale(1.04); }
.video-browse__poster-fallback {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 100%;
  height: 100%;
  background: var(--thumb-fallback-bg);
  color: var(--thumb-fg);
}
.video-browse__mark,
.video-browse__versions {
  position: absolute;
  padding: 2px var(--space-2);
  border-radius: var(--radius-pill);
  background: var(--media-badge-bg);
  color: var(--media-badge-fg);
  font-size: var(--fs-caption);
  font-weight: var(--fw-semibold);
}
.video-browse__mark { top: var(--space-2); right: var(--space-2); }
.video-browse__mark--progress { background: var(--accent-color); color: var(--accent-on); }
.video-browse__versions { left: var(--space-2); bottom: var(--space-2); }
.video-browse__card-text { display: grid; gap: 2px; min-width: 0; }
.video-browse__card-text strong { overflow: hidden; font-size: var(--fs-secondary); font-weight: var(--fw-semibold); text-overflow: ellipsis; white-space: nowrap; }
.video-browse__card-text small { color: var(--text-muted); font-size: var(--fs-caption); }
.video-browse__sentinel { height: 1px; }
.video-browse__more { display: flex; align-items: center; justify-content: center; gap: var(--space-2); margin: 0; color: var(--text-muted); font-size: var(--fs-secondary); }
.video-browse__popover-body { display: grid; gap: var(--space-3); max-width: 440px; max-height: 360px; overflow-y: auto; padding: var(--space-3); }
.video-browse__popover-empty { display: flex; align-items: center; gap: var(--space-2); margin: 0; color: var(--text-muted); font-size: var(--fs-secondary); }
.video-browse__tag-group { display: grid; gap: var(--space-2); }
.video-browse__tag-group-label { color: var(--text-muted); font-size: var(--fs-caption); font-weight: var(--fw-semibold); }
.video-browse__tag-list { display: flex; flex-wrap: wrap; gap: var(--space-2); }
.video-browse__person-list { display: grid; gap: 2px; }
.video-browse__person {
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
.video-browse__person span { flex: 1 1 auto; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.video-browse__person small { color: var(--text-muted); font-size: var(--fs-caption); }
.video-browse__person:hover,
.video-browse__person--on { background: var(--panel-muted-bg); }
.video-browse__person--on { color: var(--accent-text); }
@media (prefers-reduced-motion: reduce) {
  .video-browse__poster img { transition: none; }
  .video-browse__card:hover .video-browse__poster img { transform: none; }
}
</style>
