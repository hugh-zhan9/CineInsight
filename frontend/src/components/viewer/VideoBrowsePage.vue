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
        <div v-if="extraViewLabel || tagIDs.length || people.length" class="video-browse__chips" data-test="browse-active-filters">
          <!-- 观看状态里不单列的视图（首页「最近添加」的「查看全部」带来的 recently_added）：一个可去掉的筛选项。 -->
          <FilterChip
            v-if="extraViewLabel"
            :label="extraViewLabel"
            active
            removable
            data-test="browse-view-chip"
            @toggle="setWatch('')"
            @remove="setWatch('')"
          />
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
        <button v-if="progressBlock.more" type="button" class="btn btn--secondary btn--sm video-browse__section-action" data-test="browse-progress-all" @click="progressAllOpen = true">查看全部 <AppIcon name="arrow" /></button>
      </header>
      <AppBanner v-if="progressBlock.error" tone="danger" title="读取「追到哪了」失败" :message="progressBlock.error" data-test="browse-progress-error">
        <template #actions><button type="button" class="btn btn--secondary btn--sm" data-test="browse-progress-retry" @click="loadProgress">重试</button></template>
      </AppBanner>
      <MediaRail v-else-if="progressSkeleton" class="recap-strip video-browse__progress-strip" aria-busy="true" data-test="browse-progress-loading">
        <SkeletonBlock v-for="n in 5" :key="n" shape="poster" :lines="2" />
      </MediaRail>
      <MediaRail v-if="progressCards.length" class="recap-strip video-browse__progress-strip">
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
      </MediaRail>
    </section>

    <section ref="wall" class="video-browse__wall" :aria-busy="list.loading ? 'true' : 'false'" aria-label="视频海报墙">
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
      <!-- 按行窗口化：只渲染视口附近的行，上下占位撑住总高（见脚本里「海报墙窗口化」）。焦点所在的卡滚出窗口时
           焦点暂放在这一层（tabindex -1），方向键 / Tab 从那张卡接着走。 -->
      <div
        v-else
        ref="wallWindow"
        class="video-browse__window"
        tabindex="-1"
        data-test="browse-window"
        @keydown="onWallKeydown"
        @focusin="onWallFocusin"
      >
        <div v-if="wallWindowed && wallWindow.topSpacer > 0" :style="{ height: `${wallWindow.topSpacer}px` }" aria-hidden="true" data-test="browse-window-top"></div>
        <div ref="grid" class="video-browse__grid" :style="wallGridStyle" data-test="browse-grid">
          <button
            v-for="(video, offset) in renderedVideos"
            :key="video.id"
            type="button"
            class="btn-plain video-browse__card"
            :title="cardTitle(video)"
            :data-index="renderStart + offset"
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
        <div v-if="wallWindowed && wallWindow.bottomSpacer > 0" :style="{ height: `${wallWindow.bottomSpacer}px` }" aria-hidden="true" data-test="browse-window-bottom"></div>
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
    <ViewAllDialog
      v-if="progressAllOpen"
      title="追到哪了"
      data-test="browse-progress-dialog"
      :fetch-page="fetchProgressPage"
      :to-card="progressCard"
      @close="progressAllOpen = false"
      @open-video="openProgressFromDialog"
    />
  </section>
</template>

<script>
import MediaRail from '../ui/MediaRail.vue';
import { playVideoAfterProgress as PlayVideo } from '../../utils/watchProgress.js';
// 视频海报墙（UI 二期 D-P2-01，详细设计 §3.1）：观看侧的「视频」页，只做浏览。
// - 筛选状态归本页（不与片库管理共享）：片名搜索（file 模式）、观看状态（对应智能视图）、标签、人物、排序；
//   页面常挂载（App 里 v-show），切走再回来筛选、已加载的卡片和滚动位置都在。
// - 列表：SearchLibraryVideoPage，collapse_versions 恒为 true（版本组一张卡），每批 60，接近底部自动加载下一批；
//   总数另取 CountLibraryVideos（按卡片计），失败只是不显示数字。每次换条件换代，晚到的旧结果丢掉。
// - 海报墙按行窗口化（2026-10-14，utils/photoGrid.js 的行布局，与图片库同一套）：只渲染视口附近的行，上下占位撑住
//   总高；列数跟墙宽走，宽度变了、同一次浏览里卡片增删、墙上面的「追到哪了」或随机播放结果条变高变矮，视口顶部那张卡都留在原处；
//   方向键挪焦点，目标行没渲染时先滚进视口。
// - 「追到哪了」：ListCollectionProgress({ continue_only: true })，独立加载、独立失败，没有数据不显示；页面切回来时重取，
//   重取期间保持上一次的样子（只有第一次取和出错后重试出骨架）。
// - 随机播放与今晚看什么：randomPlaybackMixin（与片库管理同一套流程、条件与排除表）。
// - navigationRequest：App 的 navigateTo('videos', { keyword | watch | tagId }) 带来的一次新浏览——筛选回到默认再套上参数
//   （排序保留），滚动回到顶部。
// 事件：open-video(id) 进整页详情（App 按 from='videos' 分流）；navigate(key) 去别的页面（空片库的「去添加扫描目录」）。
import {
  CountLibraryVideos, ListCollectionProgress, ListCollectionProgressPage, ListPeople, ListRecentlyPlayedWithFilter, SearchLibraryVideoPage
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
import ViewAllDialog from './ViewAllDialog.vue';
import { notifyError, translateBackendError } from '../../utils/feedback.js';
import { scrubAbsolutePaths } from '../../utils/pathText.js';
import {
  buildPhotoLayout, calculatePhotoRevealScrollTop, calculatePhotoViewportAnchorScrollTop, calculatePhotoWindow,
  capturePhotoViewportAnchor, resolvePhotoColumns, stepPhotoGridItem
} from '../../utils/photoGrid.js';
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
// 观看状态里不单列、只经 navigationRequest 带进来的视图（首页「最近添加」的「查看全部」）：不进分段控件（分段与
// appCommands.js 的 VIDEO_BROWSE_SMART_VIEWS 逐项一致），在筛选项一行显示成一个可去掉的标签。
export const VIDEO_BROWSE_EXTRA_VIEWS = Object.freeze({ recently_added: '最近 30 天添加' });
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
// 哨兵离可视区还有这么远就开始取下一批，滚到底时下一批多半已经在了。窗口化后哨兵仍在下占位之后，即已加载内容的真实末尾。
const LOAD_AHEAD_MARGIN = '800px 0px';
// 海报墙几何：必须与样式里 .video-browse__grid 的 minmax(150px, 1fr) 和 gap: var(--space-5) var(--space-4) 一致（测试钉住）。
// 海报 2:3（border-box，边框算在内），卡片高 = 列宽 × 1.5 + 海报下面片名、年份那一块；那一块量到真卡片之前用估计值。
// 视口上下各多渲染 overscanRows 行：Tab 挪到下一张时它已经在 DOM 里，快速滚动也少露白。
export const VIDEO_BROWSE_GRID = Object.freeze({ minColumnWidth: 150, columnGap: 16, rowGap: 24, posterRatio: 1.5, captionEstimate: 45, overscanRows: 3 });
const WALL_ARROW_KEYS = Object.freeze({ ArrowLeft: 'left', ArrowRight: 'right', ArrowUp: 'up', ArrowDown: 'down' });
// 键盘把卡滚进视口时上下留的余量：焦点框（2px 描边 + 2px 间距）不贴着视口边被切掉。
const WALL_REVEAL_MARGIN = 8;

// 墙宽 → 列数（与 CSS 的 auto-fill 同一算法）与卡片高度。caption 是海报下面片名、年份那一块的高度。
export function videoBrowseCellSize(width, caption = VIDEO_BROWSE_GRID.captionEstimate) {
  const { minColumnWidth, columnGap, posterRatio } = VIDEO_BROWSE_GRID;
  const columns = resolvePhotoColumns(width, { minColumnWidth, gap: columnGap });
  const cardWidth = Math.max(0, ((Number(width) || 0) - columnGap * (columns - 1)) / columns);
  return { columns, cellHeight: cardWidth * posterRatio + caption };
}

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
  components: { MediaRail,
    AppBadge, AppBanner, AppIcon, AppSpinner, BaseMenu, BasePopover, EmptyState, FilterChip, PageHeader, RandomPickBanner,
    RecapMediaCard, SegmentedControl, SkeletonBlock, TonightPickerDialog, ViewAllDialog
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
      // loaded：上一次取成功了，条带现在显示的就是结果（空就是不显示）。
      // more：没放全（多取的那一条取到了），标题右边给「查看全部」，开 ViewAllDialog（progressAllOpen）按页取全。
      progressBlock: { loading: false, error: '', items: [], loaded: false, more: false },
      progressAllOpen: false,
      posterFailed: {},
      menu: null,
      menuAnchor: null,
      personKeyword: '',
      personSearch: { loading: false, error: '', items: [] },
      tonightOpen: false,
      tonightFilter: {},
      inactiveRandomPick: Object.freeze({ active: false, ids: [], reason: '', loading: false }),
      savedScrollTop: 0,
      // 海报墙窗口：墙宽量到之前（width 为 0：没有滚动宿主、页面从没显示过）整墙渲染。
      wallGeometry: { width: 0, columns: 1, cellHeight: 0 },
      wallWindow: { startRow: 0, endRow: 0, topSpacer: 0, bottomSpacer: 0, totalHeight: 0 }
    };
  },
  computed: {
    watchOptions() { return VIDEO_BROWSE_WATCH_OPTIONS; },
    sortOptions() { return VIDEO_BROWSE_SORT_OPTIONS; },
    recentlyPlayed() { return this.watch === RECENTLY_PLAYED_VIEW; },
    extraViewLabel() { return Object.hasOwn(VIDEO_BROWSE_EXTRA_VIEWS, this.watch) ? VIDEO_BROWSE_EXTRA_VIEWS[this.watch] : ''; },
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
    // 骨架只在条带还没有结果可显示时出（第一次取、出错后重试）。回到本页重取时条带保持上一次的样子、结果出来再换：
    // 上一次就是空的，这一次也多半是空的，先闪出一排骨架再收起只会把下面的墙推下去又拉回来。
    progressSkeleton() {
      return this.progressBlock.loading && !this.progressBlock.loaded && !this.progressCards.length;
    },
    progressVisible() {
      return Boolean(this.progressBlock.error || this.progressSkeleton || this.progressCards.length);
    },
    // 墙上面会自己变化的两块（随机播放结果条、「追到哪了」）的状态：任一变了，侦听在 DOM 更新后看墙顶挪没挪。
    aboveWallState() { return [this.randomPlayBanner, this.progressBlock]; },
    wallWindowed() { return this.wallGeometry.width > 0; },
    // 行布局只看条数与几何（卡片等高、不分组）：翻页追加、拿掉失效卡时重建，O(行数)；滚动时只做二分。
    wallLayout() {
      return buildPhotoLayout({
        items: this.list.items,
        columns: this.wallGeometry.columns,
        cellHeight: this.wallGeometry.cellHeight,
        gap: VIDEO_BROWSE_GRID.rowGap
      });
    },
    renderStart() {
      return this.wallWindowed ? this.wallLayout.rows[this.wallWindow.startRow]?.startIndex ?? 0 : 0;
    },
    renderedVideos() {
      if (!this.wallWindowed) return this.list.items;
      const last = this.wallLayout.rows[this.wallWindow.endRow - 1];
      return last ? this.list.items.slice(this.renderStart, last.endIndex) : [];
    },
    // 窗口化时列数由脚本定（与行布局同一个数），不交给 auto-fill 自己排。
    wallGridStyle() {
      return this.wallWindowed ? { gridTemplateColumns: `repeat(${this.wallGeometry.columns}, minmax(0, 1fr))` } : null;
    }
  },
  watch: {
    navigationRequest(request) {
      if (this.applyNavigationRequest(request)) this.reloadNow();
    },
    // 滚动宿主（.main-view）是各页共用的：离开前记下位置，切回来等内容够高再放回去（与图片页同一做法）。
    // 隐藏期间不跟滚动与宽度（滚的是别的页面，宽度报 0）；离开时视口顶部那张卡（_wallAnchor）留着，回来时按那张卡
    // 放回——窗口宽度变了、墙上面的「追到哪了」高度变了、或整页详情里把它前面的卡标成失效拿掉了都照样落在它上面；
    // 视口还在墙上面（没有锚点）时原样放回离开时的位置。回来后重取的「追到哪了」结果出来再变高变矮，由 aboveWallState 侦听接着放回。
    pageActive(active) {
      if (!active) {
        this.closeMenu();
        this.cancelScrollRestore();
        this.savedScrollTop = this.scrollOwner()?.scrollTop || 0;
        this.detachWall();
        return;
      }
      this.loadProgress();
      this.$nextTick(() => {
        const owner = this.scrollOwner();
        if (!owner) return;
        this.cancelScrollRestore();
        this.attachWall();
        this.measureWall();
        const target = this.wallAnchorTarget(this._wallAnchor, true) ?? this.savedScrollTop;
        this.syncWindow(target);
        this._cancelScrollRestore = restoreScrollWhenReachable(owner, target);
      });
    },
    // 墙上面自己会变高变矮的东西（aboveWallState）：「追到哪了」条带出现、收起、骨架换成卡片或出错横幅（回到本页时重取，
    // 结果晚于放回位置到达），随机播放结果条出现、换说法、关掉（⌘K「随机播放一部」时视口可能停在墙深处）。
    // DOM 更新完再看墙顶挪没挪，挪了就把视口顶部那张卡放回原处。
    aboveWallState: {
      flush: 'post',
      handler() { this.keepWallAnchorInPlace(); }
    },
    // 同一次浏览里卡片增删（翻页追加、失效的卡拿掉）：渲染前就按新的行布局定好窗口，视口顶部那张卡挪了位置就把它放回原处；
    // 渲染后再量一次（第一批卡片出来时才量得到片名那一块的真实高度）。换条件（reloadNow 换代）不放回。
    'list.items'() {
      if (!this.wallWindowed || !this._wallOwner) return;
      const target = this.wallAnchorTarget(this._wallAnchor, false);
      if (target === null) this.syncWindow();
      else this.scrollWallTo(target);
      this.$nextTick(() => this.refreshWallGeometry());
    }
  },
  created() {
    this._tokens = { list: 0, count: 0, progress: 0, person: 0 };
    this._wallCaption = VIDEO_BROWSE_GRID.captionEstimate;
    this.applyNavigationRequest(this.navigationRequest);
  },
  mounted() {
    window.addEventListener('keydown', this.handleShortcut);
    if (this.pageActive) {
      this.attachWall();
      this.measureWall();
    }
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
    this.detachWall();
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
      const known = VIDEO_BROWSE_WATCH_OPTIONS.some(option => option.value === request.watch) || Object.hasOwn(VIDEO_BROWSE_EXTRA_VIEWS, request.watch || '');
      this.watch = known ? request.watch : '';
      const tagID = Number(request.tagId || 0);
      this.tagIDs = Number.isInteger(tagID) && tagID > 0 ? [tagID] : [];
      this.people = [];
      this.closeMenu();
      this.cancelScrollRestore();
      this.savedScrollTop = 0;
      this._wallAnchor = null;
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
      this._focusedVideoID = null;
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
        // 多取一条判断有没有放全。
        const items = (await ListCollectionProgress({ collection_ids: [], continue_only: true, limit: VIDEO_BROWSE_PROGRESS_LIMIT + 1 })) || [];
        if (token === this._tokens.progress) {
          this.progressBlock = { loading: false, error: '', items: items.slice(0, VIDEO_BROWSE_PROGRESS_LIMIT), loaded: true, more: items.length > VIDEO_BROWSE_PROGRESS_LIMIT };
        }
      } catch (err) {
        if (token === this._tokens.progress) {
          this.progressBlock = { ...this.progressBlock, loading: false, error: translateBackendError(err?.message || err), loaded: false };
        }
      }
    },
    // 「追到哪了」查看全部：与条带同一口径（continue_only）按页取；点了一部先关弹窗再进整页详情。
    fetchProgressPage(offset, limit) {
      return ListCollectionProgressPage({ collection_ids: [], continue_only: true, offset, limit });
    },
    progressCard: collectionProgressCard,
    openProgressFromDialog(id) {
      this.progressAllOpen = false;
      this.openVideoID(id);
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
    // ===== 海报墙窗口化 =====
    // 跟着滚动宿主的滚动与墙宽走；页面隐藏时摘掉（pageActive 侦听）。ResizeObserver 一挂上就报一次当前尺寸。
    attachWall() {
      const owner = this.scrollOwner();
      if (!owner || owner === this._wallOwner) return;
      this.detachWall();
      this._wallOwner = owner;
      owner.addEventListener('scroll', this.syncWindow, { passive: true });
      if (typeof ResizeObserver === 'function' && this.$refs.wall) {
        this._wallResizeObserver = new ResizeObserver(() => this.refreshWallGeometry());
        this._wallResizeObserver.observe(this.$refs.wall);
      }
    },
    detachWall() {
      this._wallOwner?.removeEventListener('scroll', this.syncWindow);
      this._wallOwner = null;
      this._wallResizeObserver?.disconnect();
      this._wallResizeObserver = null;
    },
    // 墙顶在滚动宿主内容里的位置（墙上面是页头、工具条与「追到哪了」）。
    wallListTop() {
      const owner = this._wallOwner;
      const wall = this.$refs.wall;
      if (!owner || !wall) return 0;
      return owner.scrollTop + wall.getBoundingClientRect().top - owner.getBoundingClientRect().top;
    },
    // 量墙宽，再量一张卡的片名、年份那一块（与列宽无关），定出列数与卡片高度。墙宽为 0（隐藏）时保留上一次的值。
    // 返回几何是否变了。
    measureWall() {
      const width = this.$refs.wall?.getBoundingClientRect().width || 0;
      if (!this._wallOwner || width <= 0) return false;
      const card = this.$refs.grid?.querySelector('.video-browse__card')?.getBoundingClientRect();
      if (card?.width > 0 && card.height > 0) this._wallCaption = card.height - card.width * VIDEO_BROWSE_GRID.posterRatio;
      const { columns, cellHeight } = videoBrowseCellSize(width, this._wallCaption);
      const current = this.wallGeometry;
      if (columns === current.columns && Math.abs(width - current.width) < 0.5 && Math.abs(cellHeight - current.cellHeight) < 0.5) return false;
      this.wallGeometry = { width, columns, cellHeight };
      return true;
    },
    // 按滚动位置算出要渲染的行（滚动事件直接调它）；给了 targetTop 就按那个位置算——要先把那几行渲染出来再滚过去。
    // 顺带记下视口顶部那张卡（_wallAnchor，带上这次浏览的代次与当时的墙顶），宽度变化、卡片增删、墙被上面的条带推动、
    // 离开再回来时按它放回。
    syncWindow(targetTop) {
      const owner = this._wallOwner;
      if (!owner || !this.pageActive || !this.wallWindowed) return;
      const listTop = this.wallListTop();
      const scrollTop = typeof targetTop === 'number' ? targetTop : owner.scrollTop;
      const next = calculatePhotoWindow({
        layout: this.wallLayout,
        scrollTop,
        viewportHeight: owner.clientHeight,
        listTop,
        overscan: VIDEO_BROWSE_GRID.overscanRows
      });
      const current = this.wallWindow;
      // 绝大多数滚动帧窗口没变，原样赋值会白白重渲染一次。
      if (next.startRow !== current.startRow || next.endRow !== current.endRow
        || next.topSpacer !== current.topSpacer || next.bottomSpacer !== current.bottomSpacer) {
        this.parkFocusOutside(next);
        this.wallWindow = next;
      }
      const anchor = capturePhotoViewportAnchor({ layout: this.wallLayout, scrollTop, listTop });
      const video = anchor && this.list.items[anchor.itemIndex];
      this._wallAnchor = video ? { ...anchor, id: video.id, token: this._tokens.list, listTop } : null;
    },
    // 锚点那张卡要放回的 scrollTop（按当前墙顶算）：几何变了（geometryChanged；墙顶挪了、回到本页时调用方也传真），
    // 或它在列表里挪了位置（前面有卡被拿掉）时才需要；用不着、它已不在列表里、或是上一次浏览（换过条件）的锚点时返回 null。
    wallAnchorTarget(anchor, geometryChanged) {
      if (!anchor || anchor.token !== this._tokens.list || !this.wallWindowed) return null;
      const itemIndex = this.list.items.findIndex(video => video.id === anchor.id);
      if (itemIndex < 0 || (!geometryChanged && itemIndex === anchor.itemIndex)) return null;
      return calculatePhotoViewportAnchorScrollTop({ layout: this.wallLayout, listTop: this.wallListTop(), anchor: { ...anchor, itemIndex } });
    },
    // 先按目标位置渲染出那几行与占位，等 DOM 更新完再改 scrollTop（否则会被旧的总高夹住）。
    scrollWallTo(top) {
      const token = this._tokens.list;
      this.syncWindow(top);
      this.$nextTick(() => {
        const owner = this._wallOwner;
        if (!owner || !this.pageActive || token !== this._tokens.list) return;
        owner.scrollTop = top;
        this.syncWindow();
      });
    },
    // 墙上面的东西变了高度、墙顶挪了（与记下锚点时比）：视口顶部那张卡放回原处。墙顶没动、没有锚点（视口还在墙上面，
    // 条带、结果条就在眼前，跟着它挪是对的）、页面隐藏时不动。只比墙顶，不比 scrollTop：用户刚滚过、滚动事件还没到时不会被拉回去。
    keepWallAnchorInPlace() {
      const anchor = this._wallAnchor;
      if (!anchor || !this.pageActive || !this._wallOwner || Math.abs(this.wallListTop() - anchor.listTop) < 0.5) return;
      const target = this.wallAnchorTarget(anchor, true);
      if (target === null) return;
      this.cancelScrollRestore();
      this.scrollWallTo(target);
    },
    // 墙宽变了（窗口缩放），或第一次量到真卡片：重定列数与卡片高度，视口顶部那张卡留在原处。
    refreshWallGeometry() {
      if (!this.pageActive || !this._wallOwner) return;
      const anchor = this._wallAnchor;
      const target = this.wallAnchorTarget(anchor, this.measureWall());
      if (target === null) this.syncWindow();
      else this.scrollWallTo(target);
    },
    // 正聚焦的卡要随窗口移出 DOM 时，先把焦点放到墙上（不滚动），免得掉到 body；记着的那张卡（_focusedVideoID）
    // 之后由 onWallKeydown 接着走。
    parkFocusOutside(next) {
      const active = document.activeElement;
      if (!active || !this.$refs.grid?.contains(active)) return;
      const rows = this.wallLayout.rows;
      const index = Number(active.dataset.index);
      const start = rows[next.startRow]?.startIndex ?? 0;
      const end = rows[next.endRow - 1]?.endIndex ?? 0;
      if (index < start || index >= end) this.$refs.wallWindow?.focus({ preventScroll: true });
    },
    onWallFocusin(event) {
      const video = this.list.items[Number(event.target?.dataset?.index)];
      if (video) this._focusedVideoID = video.id;
    },
    // 方向键在海报间挪焦点（上下按列），Home / End 到第一张 / 最后一张已加载的卡。焦点暂放在墙上时（那张卡滚出了窗口），
    // 方向键从记着的那张起算，Tab / Shift+Tab 从它接着往后 / 往前走——与没窗口化时 Tab 的顺序一样。
    onWallKeydown(event) {
      if (!this.wallWindowed || event.altKey || event.ctrlKey || event.metaKey) return;
      const parked = event.target === this.$refs.wallWindow;
      const from = parked
        ? this.list.items.findIndex(video => video.id === this._focusedVideoID)
        : Number(event.target?.dataset?.index);
      if (!Number.isInteger(from) || from < 0) return;
      const last = this.list.items.length - 1;
      let target = -1;
      if (event.key === 'Tab') {
        if (!parked) return;
        target = Math.min(last, Math.max(0, from + (event.shiftKey ? -1 : 1)));
      } else if (event.shiftKey) {
        return;
      } else if (event.key === 'Home') {
        target = 0;
      } else if (event.key === 'End') {
        target = last;
      } else {
        target = stepPhotoGridItem(this.wallLayout, from, WALL_ARROW_KEYS[event.key]);
      }
      if (target < 0) return;
      event.preventDefault();
      return this.focusWallCard(target);
    },
    // 把焦点挪到第 index 张卡：那一行不在视口里（可能还没渲染）就先把它滚进视口、渲染出来，再聚焦。
    async focusWallCard(index) {
      const owner = this._wallOwner;
      if (owner && this.wallWindowed) {
        const top = calculatePhotoRevealScrollTop({
          layout: this.wallLayout,
          listTop: this.wallListTop(),
          itemIndex: index,
          scrollTop: owner.scrollTop,
          viewportHeight: owner.clientHeight,
          margin: WALL_REVEAL_MARGIN
        });
        if (top !== owner.scrollTop) {
          this.syncWindow(top);
          await this.$nextTick();
          owner.scrollTop = top;
          this.syncWindow();
        }
      }
      this.$refs.grid?.querySelector(`[data-index="${index}"]`)?.focus();
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
.video-browse__section-action { margin-left: auto; align-self: center; }
/* 条带里的卡与海报墙的卡一样大，不压过下面的主体。 */
.video-browse__progress-strip { --rail-card-width: 140px; }
/* 窗口化的墙：上下占位随滚动、翻页改高，浏览器自带的滚动锚定会跟着去改 scrollTop——锚到墙下面的「正在加载更多」时
   每追加一批就把视口推回底部、哨兵一直可见、一路翻完整个库；也会与本页按卡放回打架。整面墙（含哨兵）都不参与锚定
   （同 VirtualVideoList 的 overflow-anchor: none）。 */
.video-browse__wall { display: grid; gap: var(--space-4); overflow-anchor: none; }
/* 列宽下限与两向间隙同脚本里的 VIDEO_BROWSE_GRID（行布局按它算占位）；窗口化时列数由脚本经内联样式给出。 */
.video-browse__grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(150px, 1fr)); gap: var(--space-5) var(--space-4); }
/* 焦点暂放在这一层时那张卡已滚出视口，不画焦点框。 */
.video-browse__window:focus { outline: none; }
/* 卡片等高：海报 2:3 + 单行片名 + 年份。原先的 content-visibility 已由按行窗口化取代——视口外的卡不在 DOM 里，
   而留着它会让视口外几行按占位高度排，与行布局对不上。 */
.video-browse__card {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  min-width: 0;
  color: var(--text-primary);
  text-align: left;
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
