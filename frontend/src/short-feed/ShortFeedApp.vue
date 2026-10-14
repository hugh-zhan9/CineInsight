<template>
  <main
    class="short-feed"
    tabindex="0"
    @touchstart="onTouchStart"
    @touchmove="onTouchMove"
    @touchend="onTouchEnd"
    @touchcancel="onTouchCancel"
    @wheel.prevent="onWheel"
    @keydown="onKeydown"
    @contextmenu.prevent
  >
    <!-- 服务端设了 PIN 而没有会话：任何数据请求都回 401 pin_required，先登录（D-PC45）。 -->
    <PinGate v-if="authRequired" @authenticated="onAuthenticated" />

    <!-- 收藏墙（D-MW-05）：与推荐流各自保存状态，切过来再回去推荐流的历史与位置不变。 -->
    <FavoritesView
      v-else-if="view === 'favorites'"
      :items="favorites"
      :kind="favoritesKind"
      :loading="favoritesLoading"
      :error="favoritesError"
      @switch-view="switchView"
      @update:kind="favoritesKind = $event"
      @refresh="loadFavorites"
      @select="openFavoriteStream"
    />

    <!-- 推荐流与「在收藏里看」共用一个舞台、一套右侧按钮与「更多」。 -->
    <section v-else class="feed-stage" :class="{ 'feed-stage--notice': !!toast || !!loadError }">
      <FeedStage
        ref="stage"
        :item="currentVideo"
        :prefetched="stagePrefetched"
        :muted="muted"
        :zoomed="photoZoomed"
        :status-text="statusText"
        :prefetch-preload="prefetchPreload"
        @buffered="onVideoBuffered"
        @press-start="startLongPress"
        @press-move="trackLongPressMove"
        @press-end="finishPointerPress"
        @press-cancel="cancelLongPress"
        @media-loaded="onMediaLoaded"
        @time-update="onTimeUpdate"
        @play="onVideoPlay"
        @pause="onVideoPause"
        @media-error="onMediaError"
        @stage-tap="handleStageTap"
        @zoom-reset="resetPhotoZoom"
        @native-fullscreen="isFullscreen = $event"
      />

      <FeedTopBar
        :visible="chromeVisible"
        :muted="muted"
        :mode="inFavoriteStream ? 'favorites' : 'feed'"
        :scope-label="scopeLabel"
        :scope-description="scopeDescription"
        :position="favoritePositionText"
        @switch-view="switchView"
        @toggle-muted="muted = !muted"
        @open-scope="openScopeSheet"
        @back="closeFavoriteStream"
      />
      <FeedMeta :visible="chromeVisible" :item="currentVideo" :hint="inFavoriteStream ? favoriteStreamHint : ''" @open-tags="openTagSheet" />
      <span v-if="chromeVisible && isImageItem && currentVideo.media_url && !photoZoomed" class="photo-hint" aria-hidden="true">双击放大 · 再双击还原</span>
      <FeedActionRail
        v-if="currentVideo"
        :visible="chromeVisible"
        :item="currentVideo"
        :rail-label="isImageItem ? '图片操作' : '视频操作'"
        @toggle-like="toggleLike"
        @toggle-favorite="toggleFavorite"
        @open-more="openMoreSheet"
      />
      <FeedProgress
        v-if="!isImageItem && currentVideo && currentVideo.media_url"
        :visible="chromeVisible"
        :value="progressValue"
        :value-text="`${formatClock(videoCurrentTime)} / ${formatClock(videoDuration)}`"
        @seek-start="startSeeking"
        @seek-move="moveSeeking"
        @seek-end="finishSeeking"
        @seek-cancel="cancelSeeking"
        @seek-key="seekByKeyboard"
      />
      <!-- 空范围（D-MW-07）：一条能播的都没有时整屏说明原因，给「换个范围」。 -->
      <EmptyState
        v-if="emptyScope"
        class="feed-empty-scope"
        icon="plays"
        title="这个范围里没有能播的"
        :message="emptyScopeMessage"
        data-test="short-feed-empty-scope"
      >
        <template #actions>
          <button type="button" class="btn btn--primary" data-test="short-feed-change-scope" @click="openScopeSheet">换个范围</button>
        </template>
      </EmptyState>
      <!-- 取下一条失败（D-PC46、PLAY-13）：浏览历史原样保留，停在当前条目，给一个重试。 -->
      <AppBanner v-if="loadError" class="feed-retry" tone="danger" role="alert" title="没取到下一条" :message="loadError" data-test="short-feed-retry-bar">
        <template #actions>
          <button type="button" class="btn btn--secondary feed-retry__btn" data-test="short-feed-retry" :disabled="loading" @click="retryLoad">重试</button>
        </template>
      </AppBanner>
    </section>

    <MoreSheet
      v-if="sheet === 'more' && currentVideo"
      :item="currentVideo"
      :fullscreen-available="fullscreenAvailable"
      :fullscreen="isFullscreen"
      :deleting="deleting"
      @close="sheet = null"
      @open-rating="openRatingSheet"
      @open-tags="openTagSheet"
      @toggle-watched="toggleWatched"
      @toggle-fullscreen="toggleFullscreenFromSheet"
      @delete="moveCurrentToTrash"
    />

    <FeedSheet
      v-if="sheet === 'rating'"
      title="个人评分"
      hint="0–10 分，可以打半分"
      close-style="none"
      @close="sheet = null"
    >
      <RatingPicker :value="displayRating" @preview="previewRating" @commit="applyRating" />
      <template #footer>
        <button type="button" class="btn btn--secondary" data-test="short-feed-rating-clear" @click="applyRating(null)">清空评分</button>
        <button type="button" class="btn btn--primary sheet-btn--wide" data-test="short-feed-rating-done" @click="sheet = null">完成</button>
      </template>
    </FeedSheet>

    <FeedSheet v-if="sheet === 'tags'" title="标签" close-style="done" tall @close="sheet = null">
      <label class="sheet-search">
        <AppIcon name="search" :size="16" />
        <input v-model="tagKeyword" class="sheet-search__input" type="text" maxlength="64" placeholder="搜索或新建标签" aria-label="搜索或新建标签" />
      </label>
      <!-- 已打上的排在最前、点一下取消；其余按服务端的名字序。自动标签（如「短视频」）只读展示：
           与桌面端看到同一套标签，但不能手动增删。 -->
      <div v-if="attachedFeedTags.length" class="tag-group">
        <p class="tag-group__label">已添加 · 点一下取消</p>
        <div class="tag-picker">
          <button
            v-for="tag in attachedFeedTags"
            :key="tag.id"
            type="button"
            class="btn-plain tag-option active"
            :class="{ 'tag-option--auto': tag.automatic }"
            aria-pressed="true"
            :disabled="tag.automatic"
            :title="tag.automatic ? '自动标签，由系统维护' : null"
            @click="toggleTag(tag)"
          >
            <AppIcon class="tag-option__check" name="check" :size="16" />{{ tag.name }}<span v-if="tag.automatic" class="tag-option__auto">自动</span>
          </button>
        </div>
      </div>
      <div v-if="otherFeedTags.length" class="tag-group">
        <p class="tag-group__label">{{ tagKeyword.trim() ? `匹配「${tagKeyword.trim()}」的标签` : '点一下添加' }}</p>
        <div class="tag-picker">
          <button
            v-for="tag in otherFeedTags"
            :key="tag.id"
            type="button"
            class="btn-plain tag-option"
            :class="{ 'tag-option--auto': tag.automatic }"
            aria-pressed="false"
            :disabled="tag.automatic"
            :title="tag.automatic ? '自动标签，由系统维护' : null"
            @click="toggleTag(tag)"
          >{{ tag.name }}<span v-if="tag.automatic" class="tag-option__auto">自动</span></button>
        </div>
      </div>
      <!-- 搜不到就地新建：手机上不该为一个新标签回到桌面端。 -->
      <button
        v-if="canCreateTag"
        type="button"
        class="btn-plain tag-create"
        data-test="short-feed-create-tag"
        :disabled="creatingTag"
        @click="createTagFromKeyword"
      >{{ creatingTag ? '新建中…' : `＋ 新建「${tagKeyword.trim()}」并添加` }}</button>
      <p v-if="visibleFeedTags.length === 0 && !canCreateTag" class="sheet__empty">还没有标签，输入名称即可新建。</p>
      <template #footer>
        <p class="sheet__note">改动立即写回电脑上的库；自动标签只能看，不能改。</p>
      </template>
    </FeedSheet>

    <!-- 范围面板（D-MW-07）：「刷什么」= 类型 × 五个范围，选了就关面板、按新范围取一条。 -->
    <FeedSheet v-if="sheet === 'scope'" title="刷什么" label="播放范围" @close="sheet = null">
      <SegmentedControl
        class="media-kind-switch"
        label="类型"
        :options="mediaKindSegments"
        :model-value="mediaKind"
        data-test="short-feed-media-kind"
        @update:model-value="applyMediaKind"
      />
      <div class="sheet-rows" role="radiogroup" aria-label="范围">
        <button
          v-for="option in scopes"
          :key="option.scope"
          type="button"
          role="radio"
          class="btn-plain scope-option"
          :class="{ active: option.scope === scope }"
          :aria-checked="option.scope === scope ? 'true' : 'false'"
          :data-test="`short-feed-scope-${option.scope}`"
          @click="applyScope(option.scope)"
        >
          <span class="scope-option__name">{{ option.name }}</span>
          <span class="scope-option__count">{{ formatCount(option.count) }}</span>
          <AppIcon v-if="option.scope === scope" class="scope-option__check" name="check" :size="20" />
          <span v-else class="scope-option__check" aria-hidden="true"></span>
        </button>
      </div>
      <!-- 被格式门排除的视频不再无声消失（D-PC46、PLAY-13）：说清有几条、怎么让它能播。 -->
      <p v-if="unplayableHint" class="scope-note" role="note" :data-test="`short-feed-unplayable-${scope}`">{{ unplayableHint }}</p>
      <p v-if="scopesError" class="sheet__empty" role="alert" data-test="short-feed-scopes-error">{{ scopesError }}</p>
    </FeedSheet>

    <!-- 全局提示挂到 body：面板（同在 body 上的 --z-modal 层）开着时也看得见。 -->
    <Teleport to="body">
      <div v-if="toast" class="feed-toast" :class="{ 'feed-toast--raised': view !== 'favorites' && !!loadError }" role="status" data-test="short-feed-toast">
        <AppIcon v-if="toast.icon" class="feed-toast__icon" :name="toast.icon" :size="20" />
        <span class="feed-toast__text">
          <span class="feed-toast__message">{{ toast.message }}</span>
          <span v-if="toast.detail" class="feed-toast__detail">{{ toast.detail }}</span>
        </span>
      </div>
    </Teleport>
  </main>
</template>

<script>
import { deleteItem, getFavorites, getFeedTags, createFeedTag, getNextItem, getScopes, itemKey, recordPlay, setAuthRequiredHandler, setFavorited, setItemTag, setLiked, setRating, setWatched } from './api.js';
import { emptyScopeText, feedErrorText, unplayableHintText } from './errors.js';
import { formatClock, formatCount } from './format.js';
import { createPlaybackAccumulator, newViewSessionID, viewThreshold } from '../utils/viewThreshold.js';

// 资源类型筛选与播放范围正交，记在本机：手机上选过"只看图片"，下次打开还是。
// short 是范围按钮上跟在范围名后面的那一截（D-MW-01：类型不是「视频和图片」时加「· 视频 / · 图片」）。
const MEDIA_KIND_STORAGE_KEY = 'short-feed-media-kind';
const MEDIA_KIND_OPTIONS = [
  { kind: 'all', name: '视频和图片', short: '' },
  { kind: 'video', name: '只看视频', short: '视频' },
  { kind: 'image', name: '只看图片', short: '图片' }
];
function readStoredMediaKind() {
  try {
    const stored = window.localStorage?.getItem(MEDIA_KIND_STORAGE_KEY);
    return MEDIA_KIND_OPTIONS.some(option => option.kind === stored) ? stored : 'all';
  } catch (_err) {
    return 'all';
  }
}
function storeMediaKind(kind) {
  try {
    window.localStorage?.setItem(MEDIA_KIND_STORAGE_KEY, kind);
  } catch (_err) {}
}
import { createSwipeTracker, keyboardDirection, wheelDirection } from './gesture.js';
import { isScopeExhaustedHint, unsupportedStatusText } from './videoState.js';
import { createWakeLock } from './useWakeLock.js';
import AppBanner from '../components/ui/AppBanner.vue';
import AppIcon from '../components/ui/AppIcon.vue';
import EmptyState from '../components/ui/EmptyState.vue';
import SegmentedControl from '../components/ui/SegmentedControl.vue';
import FeedStage from './components/FeedStage.vue';
import FeedTopBar from './components/FeedTopBar.vue';
import FeedMeta from './components/FeedMeta.vue';
import FeedActionRail from './components/FeedActionRail.vue';
import FeedProgress from './components/FeedProgress.vue';
import FavoritesView from './components/FavoritesView.vue';
import FeedSheet from './components/FeedSheet.vue';
import MoreSheet from './components/MoreSheet.vue';
import RatingPicker from './components/RatingPicker.vue';
import PinGate from './components/PinGate.vue';

const swipeTracker = createSwipeTracker();
const wakeLock = createWakeLock();
// 图片没有播放态，控件不能靠"暂停中"常驻，改为定时收起。
const PHOTO_CONTROLS_HIDE_MS = 2600;
// 浏览历史的上限：一路划下去不能把整库都留在内存里。
const FEED_HISTORY_LIMIT = 40;
// 提示停留时长：删除后的说明与失败原因都要来得及读完。
const TOAST_MS = 5000;

export default {
  name: 'ShortFeedApp',
  components: { AppBanner, AppIcon, EmptyState, SegmentedControl, FeedStage, FeedTopBar, FeedMeta, FeedActionRail, FeedProgress, FavoritesView, FeedSheet, MoreSheet, RatingPicker, PinGate },
  watch: {
    // 标签搜索实时查服务端：每次输入变化都重新拉一次（100ms 内的连续按键合并成一次）。
    tagKeyword(value) {
      if (this.sheet !== 'tags') return;
      clearTimeout(this.tagSearchTimer);
      this.tagSearchTimer = setTimeout(() => this.loadFeedTags(value), 100);
    }
  },
  data() {
    return {
      // 保留浏览历史而不是只留当前一条：往回划走的是历史，不重新抽签。
      items: [],
      index: -1,
      scope: 'all',
      scopes: [],
      mediaKind: readStoredMediaKind(),
      mediaKindSegments: MEDIA_KIND_OPTIONS.map(option => ({ value: option.kind, label: option.name })),
      // 范围里一条能播的都没有（D-MW-07）：整屏空态，不当作取下一条失败。
      emptyScope: false,
      feedTags: [],
      tagKeyword: '',
      // 标签搜索实时查服务端：序号丢弃过期响应，定时器合并连续按键。
      tagQuerySeq: 0,
      tagSearchTimer: null,
      creatingTag: false,
      // 这一段触摸是在图片放大态开始的：touchstart 那一刻已经让给了原生平移与 pointer 路径，
      // 就算中途双击退出了放大，收尾的 touchend 也不能再当成手势处理。
      touchBeganZoomed: false,
      sheet: null,
      toast: null,
      toastTimer: null,
      // 移到废纸篓的请求还没回来：「更多」里的这一项禁用，防止连点（D-MW-03）。
      deleting: false,
      // 评分面板里正在拖或正在写的分（{ value }）；写完或失败后清掉，显示回到条目上的分（D-MW-04）。
      ratingDraft: null,
      prefetchedVideo: null,
      // 预取视频的下载档位：换到新的一条先回到 metadata，当前条缓冲够了再放开。
      prefetchPreload: 'metadata',
      prefetching: false,
      recentKeys: [],
      favorites: [],
      favoritesKind: 'all',
      favoritesLoading: false,
      // 收藏页加载失败的说明（D-PC46）：不再显示成「还没有收藏」。
      favoritesError: '',
      // 「在收藏里看」（D-MW-05）：墙上当时筛出来的列表与位置，上下滑只在这里面走，到两端停住。
      favoriteStream: { items: [], index: -1 },
      favoriteStreamHint: '上下滑只在收藏之间切换；取消收藏后这一条留到你划走为止',
      scopesError: '',
      // feed：推荐流；favorites：收藏墙；favorites-feed：在收藏里看。
      view: 'feed',
      loading: false,
      // 取下一条失败的说明（网络异常等），配「重试」；为空表示没有待重试的失败。
      loadError: '',
      // 服务端要求 PIN（401 pin_required）时显示 PIN 页。
      authRequired: false,
      statusText: '加载中',
      muted: true,
      playbackRate: 1,
      recordedVideoID: null,
      wheelState: { lastWheelAt: 0 },
      controlsVisible: false,
      controlsHideTimer: null,
      isPlaying: false,
      videoCurrentTime: 0,
      videoDuration: 0,
      seeking: false,
      scrubValue: 0,
      longPressTimer: null,
      longPressStart: null,
      longPressTriggered: false,
      longPressActionInFlight: false,
      lastStageTapAt: 0,
      lastStageTapPoint: null,
      photoZoomed: false,
      // 全屏：能用元素全屏（安卓 Chrome、iPad、桌面）就整页进全屏，滑动切换照常；
      // iPhone Safari 只有 <video> 的系统播放器全屏，元数据到了才知道当前条目能不能进。
      isFullscreen: false,
      fullscreenSupported: false,
      nativeFullscreenAvailable: false
    };
  },
  computed: {
    inFavoriteStream() {
      return this.view === 'favorites-feed';
    },
    currentVideo() {
      if (this.inFavoriteStream) return this.favoriteStream.items[this.favoriteStream.index] || null;
      return this.items[this.index] || null;
    },
    // 舞台预取的下一条：推荐流是后台抽好的那一条，收藏流就是列表里的下一张。
    stagePrefetched() {
      if (this.inFavoriteStream) return this.favoriteStream.items[this.favoriteStream.index + 1] || null;
      return this.prefetchedVideo;
    },
    favoritePositionText() {
      const { items, index } = this.favoriteStream;
      return items.length ? `${index + 1} / ${items.length}` : '';
    },
    currentScopeEntry() {
      return this.scopes.find(item => item.scope === this.scope) || null;
    },
    // 范围名来自服务端（白话名，D-MW-07）；还没读过范围时只可能是默认的「全部」。
    scopeName() {
      return this.currentScopeEntry?.name || '全部';
    },
    mediaKindOption() {
      return MEDIA_KIND_OPTIONS.find(option => option.kind === this.mediaKind) || MEDIA_KIND_OPTIONS[0];
    },
    scopeLabel() {
      const short = this.mediaKindOption.short;
      return short ? `${this.scopeName} · ${short}` : this.scopeName;
    },
    scopeDescription() {
      return `播放范围：${this.scopeName}，${this.mediaKindOption.name}`;
    },
    unplayableHint() {
      return unplayableHintText(this.currentScopeEntry?.unplayable_count);
    },
    emptyScopeMessage() {
      return emptyScopeText(this.scopeName, this.mediaKind, this.currentScopeEntry?.unplayable_count);
    },
    displayRating() {
      if (this.ratingDraft) return this.ratingDraft.value;
      const rating = this.currentVideo?.personal_rating;
      return rating === null || rating === undefined ? null : Number(rating);
    },
    attachedFeedTags() {
      return this.visibleFeedTags.filter(tag => this.isTagAttached(tag.id));
    },
    otherFeedTags() {
      return this.visibleFeedTags.filter(tag => !this.isTagAttached(tag.id));
    },
    visibleFeedTags() {
      // 服务端已按关键词筛过；这里再做一次即时预筛（等待响应期间不闪旧结果），
      // 并把已打在当前条目上的标签排到最前，其余保持服务端的名字序。
      const keyword = this.tagKeyword.trim().toLowerCase();
      const matched = keyword ? this.feedTags.filter(tag => tag.name.toLowerCase().includes(keyword)) : this.feedTags;
      const attached = matched.filter(tag => this.isTagAttached(tag.id));
      if (attached.length === 0) return matched;
      return [...attached, ...matched.filter(tag => !this.isTagAttached(tag.id))];
    },
    canCreateTag() {
      const keyword = this.tagKeyword.trim().toLowerCase();
      if (!keyword) return false;
      return !this.feedTags.some(tag => tag.name.toLowerCase() === keyword);
    },
    isImageItem() {
      return this.currentVideo?.media_kind === 'image';
    },
    // 视频靠"暂停中"常驻控件；图片没有播放态，只认 controlsVisible。
    chromeVisible() {
      if (this.isImageItem) return this.controlsVisible;
      return this.controlsVisible || !this.isPlaying;
    },
    progressValue() {
      if (this.seeking) return this.scrubValue;
      if (!this.videoDuration) return 0;
      return Math.round((this.videoCurrentTime / this.videoDuration) * 1000);
    },
    fullscreenAvailable() {
      return this.fullscreenSupported || this.nativeFullscreenAvailable;
    }
  },
  beforeUnmount() {
    setAuthRequiredHandler(null);
    this.clearControlsHideTimer();
    this.clearLongPressTimer();
    this.releaseWakeLock();
    document.removeEventListener('visibilitychange', this.handleVisibilityChange);
    document.removeEventListener('fullscreenchange', this.syncFullscreen);
    document.removeEventListener('webkitfullscreenchange', this.syncFullscreen);
    clearTimeout(this.tagSearchTimer);
  },
  async mounted() {
    // 任何一次请求回 401 pin_required 都切到 PIN 页；登录后回到原来的位置接着看。
    setAuthRequiredHandler(() => { this.authRequired = true; });
    document.addEventListener('visibilitychange', this.handleVisibilityChange);
    this.fullscreenSupported = !!(document.fullscreenEnabled || document.webkitFullscreenEnabled);
    document.addEventListener('fullscreenchange', this.syncFullscreen);
    document.addEventListener('webkitfullscreenchange', this.syncFullscreen);
    this.syncFullscreen();
    await this.nextVideo();
    this.$el.focus();
  },
  methods: {
    formatClock,
    formatCount,
    async nextVideo(direction = 1) {
      if (this.loading || direction === 0 || this.sheet || this.authRequired) return;
      // 收藏墙上没有「下一条」；在收藏里看只在那份列表里走。
      if (this.view === 'favorites') return;
      if (this.inFavoriteStream) {
        this.moveFavoriteStream(direction);
        return;
      }
      // 往回划走历史，不重新抽签：抽签回来的是另一条，那不叫"上一条"。
      if (direction < 0) {
        if (this.index > 0) {
          this.index -= 1;
          this.activateCurrent();
        }
        return;
      }
      if (this.index + 1 < this.items.length) {
        this.index += 1;
        this.activateCurrent();
        return;
      }
      await this.loadNextItem();
    },
    // 取一条新的接到历史末尾并切过去。失败时原地不动：浏览历史与当前条目都保留。
    async loadNextItem() {
      if (this.loading || this.authRequired) return;
      this.loading = true;
      this.loadError = '';
      if (this.items.length === 0) this.statusText = '加载中';
      try {
        const video = this.takePrefetchedVideo() || await getNextItem(this.recentKeys.slice(-12), this.scope, this.mediaKind);
        // 范围里一条能播的都没有（服务端回一条放不了的视频当说明）：历史为空时整屏说明并给「换个范围」（D-MW-07）。
        if (this.items.length === 0 && isScopeExhaustedHint(video)) {
          this.enterEmptyScope();
          return;
        }
        this.emptyScope = false;
        this.appendItem(video);
      } catch (err) {
        // PIN 页接管，不在底下再叠一条失败提示。
        if (this.authRequired) return;
        // 404 no_eligible_videos 是「这个范围里没有」，不是取失败：历史为空时同样给空态（D-MW-07）。
        if (this.items.length === 0 && err?.code === 'no_eligible_videos') {
          this.enterEmptyScope();
          return;
        }
        // 失败不清空浏览历史（D-PC46、PLAY-13）：停在当前条目，往回划照样能看，另给一条「没取到下一条 · 重试」。
        const message = feedErrorText(err);
        this.loadError = message;
        if (this.items.length === 0) this.statusText = message;
      } finally {
        this.loading = false;
      }
    },
    // 「重试」总是去取新的一条：用户中途往回划过，也不必先划回末尾。
    retryLoad() {
      return this.loadNextItem();
    },
    // 空范围：舞台上不留「加载中」，读一次范围计数好写明有几个视频是因为格式放不了（D-MW-07）。
    enterEmptyScope() {
      this.emptyScope = true;
      this.statusText = '';
      this.loadScopes();
    },
    // 登录成功：回到原来的位置；还没有任何条目就重新取一条。收藏页开着就重读收藏。
    async onAuthenticated() {
      this.authRequired = false;
      this.loadError = '';
      if (this.view === 'favorites') {
        await this.loadFavorites();
        return;
      }
      if (!this.inFavoriteStream && this.items.length === 0) {
        await this.nextVideo();
        return;
      }
      await this.$nextTick();
      this.activateCurrent();
    },
    appendItem(video) {
      // 历史有上限，否则一路划下去会把整库都留在内存里。
      this.items = [...this.items, video].slice(-FEED_HISTORY_LIMIT);
      this.index = this.items.length - 1;
      this.activateCurrent();
    },
    activateCurrent() {
      const video = this.currentVideo;
      if (!video) return;
      this.statusText = unsupportedStatusText(video);
      // 每次划到一条都是一次新的观看：累计播放从零算起，越过阈值才记（D-PC43）。
      this.recordedVideoID = null;
      this._viewSessionID = newViewSessionID();
      this._viewAccumulator = createPlaybackAccumulator();
      this.isPlaying = false;
      this.videoCurrentTime = 0;
      this.videoDuration = 0;
      this.scrubValue = 0;
      this.controlsVisible = false;
      this.photoZoomed = false;
      this.nativeFullscreenAvailable = false;
      this.clearControlsHideTimer();
      // 排除集与后台预取只属于推荐流：在收藏里看不影响推荐流接下来抽什么。
      if (!this.inFavoriteStream) {
        const key = itemKey(video);
        if (!this.recentKeys.includes(key)) {
          this.recentKeys.push(key);
        }
        this.recentKeys = this.recentKeys.slice(-20);
      }
      this.$nextTick(() => {
        const player = this.player();
        this.applyPlaybackRate();
        if (player?.play) player.play().catch(() => {});
      });
      // 图片没有缓冲过程，也不占带宽，预取可以直接整只下载。
      this.prefetchPreload = this.isImageItem ? 'auto' : 'metadata';
      if (!this.inFavoriteStream) this.prefetchNextVideo();
    },
    onVideoBuffered() {
      this.prefetchPreload = 'auto';
    },
    // 改动后的整条 DTO 由后端回来，就地换掉当前这一条，不猜写入结果。推荐流、收藏流与收藏墙里同一条都换掉，
    // 切回去不显示旧值。
    replaceCurrent(updated) {
      if (!updated || !this.currentVideo) return;
      const key = itemKey(this.currentVideo);
      const swap = list => (list.some(item => itemKey(item) === key) ? list.map(item => (itemKey(item) === key ? updated : item)) : list);
      this.items = swap(this.items);
      this.favoriteStream = { ...this.favoriteStream, items: swap(this.favoriteStream.items) };
      this.favorites = swap(this.favorites);
    },
    // 一条提示：message 一行；detail 是第二行（删除后的「去哪找回」）；icon 取图标集里的名字。没有按钮（D-MW-03）。
    flashToast(message, { detail = '', icon = '' } = {}) {
      this.toast = { message, detail, icon };
      if (this.toastTimer) clearTimeout(this.toastTimer);
      this.toastTimer = window.setTimeout(() => { this.toast = null; }, TOAST_MS);
    },
    takePrefetchedVideo() {
      if (!this.prefetchedVideo) return null;
      const video = this.prefetchedVideo;
      this.prefetchedVideo = null;
      return video;
    },
    async prefetchNextVideo() {
      if (this.prefetching || this.prefetchedVideo || !this.currentVideo) return;
      this.prefetching = true;
      try {
        const excludeKeys = [...new Set([...this.recentKeys.slice(-12), itemKey(this.currentVideo)])];
        const video = await getNextItem(excludeKeys, this.scope, this.mediaKind);
        if (video?.id && video.id !== this.currentVideo?.id) {
          this.prefetchedVideo = video;
        }
      } catch (err) {
      } finally {
        this.prefetching = false;
      }
    },
    handleStageTap(event) {
      if (!this.currentVideo?.media_url) return;
      const now = Date.now();
      const point = this.eventPoint(event);
      const previous = this.lastStageTapPoint;
      const isDoubleTap = previous &&
        now - this.lastStageTapAt <= 320 &&
        Math.hypot(point.x - previous.x, point.y - previous.y) <= 28;
      this.lastStageTapAt = now;
      this.lastStageTapPoint = point;
      if (isDoubleTap) {
        // 双击已消费：清掉记录，免得同一下触摸的另一条事件路径把它凑成第三击。
        this.lastStageTapAt = 0;
        this.lastStageTapPoint = null;
        if (this.isImageItem) {
          this.photoZoomed = !this.photoZoomed;
          this.showControls();
          this.schedulePhotoControlsHide();
        } else {
          this.togglePlayback();
        }
        return;
      }
      this.showPlaybackControls();
    },
    eventPoint(event) {
      const touch = event?.changedTouches?.[0] || event?.touches?.[0];
      if (touch) return { x: touch.clientX, y: touch.clientY };
      return { x: event?.clientX || 0, y: event?.clientY || 0 };
    },
    showPlaybackControls() {
      if (!this.currentVideo?.media_url) return;
      this.showControls();
      if (this.isImageItem) {
        this.schedulePhotoControlsHide();
        return;
      }
      if (this.isPlaying) {
        this.scheduleControlsHide();
      }
    },
    // 图片没有 isPlaying，scheduleControlsHide 会直接返回，所以单独排一个定时器。
    schedulePhotoControlsHide() {
      this.clearControlsHideTimer();
      this.controlsHideTimer = window.setTimeout(() => {
        this.controlsVisible = false;
      }, PHOTO_CONTROLS_HIDE_MS);
    },
    onVideoPlay() {
      this.isPlaying = true;
      this.scheduleControlsHide(600);
      this.requestWakeLock();
    },
    onVideoPause() {
      this.isPlaying = false;
      this._viewAccumulator?.breakSegment();
      this.showControls();
      this.clearControlsHideTimer();
      this.releaseWakeLock();
    },
    togglePlayback() {
      const player = this.player();
      if (!player) return;
      if (player.paused) {
        player.play().catch(() => {});
      } else {
        player.pause();
      }
      this.showControls();
      if (this.isPlaying) {
        this.scheduleControlsHide();
      }
    },
    player() {
      return this.$refs.stage?.player?.() || null;
    },
    requestWakeLock() {
      return wakeLock.request();
    },
    releaseWakeLock() {
      return wakeLock.release();
    },
    handleVisibilityChange() {
      if (!document.hidden && this.isPlaying) {
        this.requestWakeLock();
      }
    },
    applyPlaybackRate() {
      const player = this.player();
      if (player) {
        player.playbackRate = this.playbackRate;
      }
    },
    startLongPress(event) {
      if (!this.currentVideo?.media_url || event.pointerType === 'mouse' && event.button !== 0) return;
      this.startLongPressAt(event.clientX, event.clientY);
    },
    startLongPressAt(clientX, clientY) {
      if (!this.currentVideo?.media_url) return;
      this.clearLongPressTimer();
      this.longPressTriggered = false;
      this.longPressStart = { x: clientX, y: clientY };
      this.longPressTimer = window.setTimeout(() => {
        this.longPressTriggered = true;
        this.likeAndFavoriteCurrentVideo();
      }, 650);
    },
    trackLongPressMove(event) {
      if (!this.longPressStart || !this.longPressTimer) return;
      const dx = event.clientX - this.longPressStart.x;
      const dy = event.clientY - this.longPressStart.y;
      if (Math.hypot(dx, dy) > 12) {
        this.cancelLongPress();
      }
    },
    cancelLongPress() {
      this.clearLongPressTimer();
      this.longPressStart = null;
    },
    finishPointerPress(event) {
      const wasLongPress = this.longPressTriggered;
      this.cancelLongPress();
      // 触屏平时由舞台的 touch 事件识别点击；图片放大后那一路整体让给了原生平移，
      // 双击退出放大只能从这里的 pointer 事件走，否则放大就回不来、上下滑也切不了。
      if (!wasLongPress && (event.pointerType !== 'touch' || this.photoZoomed)) {
        this.handleStageTap(event);
      }
    },
    clearLongPressTimer() {
      if (this.longPressTimer) {
        window.clearTimeout(this.longPressTimer);
        this.longPressTimer = null;
      }
    },
    async likeAndFavoriteCurrentVideo() {
      if (!this.currentVideo || this.longPressActionInFlight) return;
      this.longPressActionInFlight = true;
      const wasLiked = this.currentVideo.liked;
      const wasFavorited = this.currentVideo.favorited;
      this.currentVideo.liked = true;
      this.currentVideo.favorited = true;
      this.showControls();
      this.clearControlsHideTimer();
      const item = this.currentVideo;
      try {
        if (!wasLiked) {
          await setLiked(item, true);
        }
        if (!wasFavorited) {
          await setFavorited(item, true);
        }
        if (this.isPlaying) {
          this.scheduleControlsHide();
        }
      } catch (err) {
        // 失败回滚并说一声（D-PC46），不再静默恢复原状。
        item.liked = wasLiked;
        item.favorited = wasFavorited;
        this.flashToast(`点赞收藏失败：${feedErrorText(err)}`);
      } finally {
        this.longPressActionInFlight = false;
      }
    },
    // 手机端的有效观看（D-PC43、PLAY-07）：不再在 playing 那一刻就记，改为这一条累计播放越过
    // viewThreshold（min(60 秒, 时长一半)）才上报一次，后端来源仍是 mobile_feed。拖动、循环回片头不算播放。
    trackViewProgress() {
      const player = this.player();
      const item = this.currentVideo;
      if (!player || !item || this.isImageItem || player.paused || this.seeking) return;
      const accumulator = this._viewAccumulator || (this._viewAccumulator = createPlaybackAccumulator());
      const seconds = accumulator.sample(player.currentTime, Date.now(), player.playbackRate);
      const duration = Number.isFinite(player.duration) && player.duration > 0 ? player.duration : Number(item.duration);
      if (seconds >= viewThreshold(duration)) this.recordCurrentItemView();
    },
    async recordCurrentItemView() {
      const key = itemKey(this.currentVideo);
      if (!this.currentVideo || this.recordedVideoID === key) return;
      this.recordedVideoID = key;
      try {
        await recordPlay(this.currentVideo, this._viewSessionID);
      } catch (err) {}
    },
    onMediaLoaded() {
      if (this.isImageItem) return;
      this.syncVideoTime();
      // iPhone Safari：没有元素全屏，但 <video> 元数据到了就能进系统播放器全屏。
      this.nativeFullscreenAvailable = !this.fullscreenSupported && typeof this.player()?.webkitEnterFullscreen === 'function';
    },
    syncFullscreen() {
      this.isFullscreen = !!(document.fullscreenElement || document.webkitFullscreenElement);
    },
    async toggleFullscreen() {
      this.showControls();
      try {
        if (document.fullscreenElement || document.webkitFullscreenElement) {
          await (document.exitFullscreen?.() ?? document.webkitExitFullscreen?.());
          return;
        }
        // 整页进全屏而不是只把 <video> 放大：面板、动作栏与滑动切换都要留在里面。
        // 以 fullscreenEnabled 为准而不是方法存在与否：iPhone 上方法可能存在但永远被拒。
        if (this.fullscreenSupported) {
          const root = document.documentElement;
          if (root.requestFullscreen) {
            await root.requestFullscreen({ navigationUI: 'hide' });
          } else if (root.webkitRequestFullscreen) {
            root.webkitRequestFullscreen();
          }
          return;
        }
        // iPhone 只能进系统播放器：退出后回到本页，暂停态会把控件带出来。
        const player = this.player();
        if (typeof player?.webkitEnterFullscreen === 'function') {
          player.webkitEnterFullscreen();
        }
      } catch (_err) {
        // 浏览器拒绝（没有用户手势、iframe 策略等）时保持原样，不弹错误。
      } finally {
        if (this.isPlaying) this.scheduleControlsHide();
      }
    },
    onMediaError() {
      if (!this.currentVideo) return;
      if (this.isImageItem) {
        // 图片没有自动前进的节奏；停在原地把原因说清楚，由用户自己划走。
        this.statusText = '当前图片无法在浏览器中显示';
        // currentVideo 现在是历史列表的派生值，只能就地换掉这一条。
        this.replaceCurrent({ ...this.currentVideo, media_url: '' });
        return;
      }
      this.statusText = '当前视频无法在浏览器中播放';
      // 仍然自动跳到下一条，但提示留在下一条上，不再一闪而过（PLAY-13）。
      this.flashToast(`「${this.currentVideo.name || '这条视频'}」无法在浏览器中播放，已跳过`);
      setTimeout(() => this.nextVideo(), 350);
    },
    syncVideoTime() {
      const player = this.player();
      if (!player) return;
      this.videoDuration = Number.isFinite(player.duration) ? player.duration : 0;
      this.videoCurrentTime = Number.isFinite(player.currentTime) ? player.currentTime : 0;
    },
    onTimeUpdate() {
      if (this.seeking) return;
      this.syncVideoTime();
      this.trackViewProgress();
    },
    startSeeking(event) {
      if (!this.videoDuration) return;
      event.currentTarget?.setPointerCapture?.(event.pointerId);
      this.scrubValue = this.videoDuration
        ? Math.round((this.videoCurrentTime / this.videoDuration) * 1000)
        : 0;
      this.seeking = true;
      this.updateScrubFromPointer(event);
      this.showControls();
      this.clearControlsHideTimer();
    },
    moveSeeking(event) {
      if (!this.seeking) return;
      this.updateScrubFromPointer(event);
    },
    updateScrubFromPointer(event) {
      if (!this.videoDuration) return;
      const rect = event.currentTarget.getBoundingClientRect();
      const ratio = Math.min(1, Math.max(0, (event.clientX - rect.left) / rect.width));
      const value = Math.round(ratio * 1000);
      const nextTime = (value / 1000) * this.videoDuration;
      this.scrubValue = value;
      this.videoCurrentTime = nextTime;
    },
    finishSeeking(event) {
      if (!this.seeking) return;
      if (event) {
        this.updateScrubFromPointer(event);
        event.currentTarget?.releasePointerCapture?.(event.pointerId);
      }
      const player = this.player();
      if (player && this.videoDuration) {
        this._viewAccumulator?.breakSegment();
        player.currentTime = (this.scrubValue / 1000) * this.videoDuration;
      }
      this.seeking = false;
      this.syncVideoTime();
      if (this.isPlaying) {
        this.scheduleControlsHide();
      }
    },
    cancelSeeking(event) {
      this.seeking = false;
      event?.currentTarget?.releasePointerCapture?.(event.pointerId);
      this.syncVideoTime();
      if (this.isPlaying) {
        this.scheduleControlsHide();
      }
    },
    seekByKeyboard(event) {
      if (!this.videoDuration) return;
      const step = event.shiftKey ? 10 : 5;
      if (event.key === 'ArrowLeft') {
        this.commitSeek(Math.max(0, this.videoCurrentTime - step));
      } else if (event.key === 'ArrowRight') {
        this.commitSeek(Math.min(this.videoDuration, this.videoCurrentTime + step));
      } else if (event.key === 'Home') {
        this.commitSeek(0);
      } else if (event.key === 'End') {
        this.commitSeek(this.videoDuration);
      }
    },
    commitSeek(seconds) {
      const player = this.player();
      if (!player || !this.videoDuration) return;
      // 拖动与键盘定位不算播放：断开当前一段累计。
      this._viewAccumulator?.breakSegment();
      player.currentTime = seconds;
      this.videoCurrentTime = seconds;
      this.scrubValue = Math.round((seconds / this.videoDuration) * 1000);
      this.showControls();
      if (this.isPlaying) {
        this.scheduleControlsHide();
      }
    },
    showControls() {
      this.controlsVisible = true;
    },
    scheduleControlsHide(delay = 2600) {
      this.clearControlsHideTimer();
      if (!this.isPlaying) return;
      this.controlsHideTimer = window.setTimeout(() => {
        this.controlsVisible = false;
      }, delay);
    },
    clearControlsHideTimer() {
      if (this.controlsHideTimer) {
        window.clearTimeout(this.controlsHideTimer);
        this.controlsHideTimer = null;
      }
    },
    // 点赞、收藏先改界面再写库；失败回滚到原状态并提示（D-PC46、PLAY-13），不再静默。
    async toggleLike() {
      const item = this.currentVideo;
      if (!item) return;
      const liked = !item.liked;
      item.liked = liked;
      try {
        await setLiked(item, liked);
      } catch (err) {
        item.liked = !liked;
        this.flashToast(`${liked ? '点赞' : '取消点赞'}失败：${feedErrorText(err)}`);
      }
    },
    async toggleFavorite() {
      const item = this.currentVideo;
      if (!item) return;
      const favorited = !item.favorited;
      item.favorited = favorited;
      try {
        await setFavorited(item, favorited);
      } catch (err) {
        item.favorited = !favorited;
        this.flashToast(`${favorited ? '收藏' : '取消收藏'}失败：${feedErrorText(err)}`);
      }
    },
    // 移到废纸篓（D-MW-03）：不弹确认、不给撤销，恢复在电脑端回收站。成功后这一条从两个流里摘掉，
    // 当前流前进到下一条；失败留在原条目，按 409 的三种原因说清楚。
    async moveCurrentToTrash() {
      const target = this.currentVideo;
      if (!target || this.deleting) return;
      this.sheet = null;
      this.deleting = true;
      try {
        await deleteItem(target);
      } catch (err) {
        // 409 volume_offline / permission_denied / trash_unsupported：什么都没动，说清原因（D-PC01、PLAY-13）。
        this.flashToast(`删除失败：${feedErrorText(err)}`);
        return;
      } finally {
        this.deleting = false;
      }
      this.flashToast(`「${target.name}」已移到废纸篓`, { detail: '要找回，在电脑端「回收站」里恢复', icon: 'trash' });
      await this.forgetDeleted(target);
    },
    // 删掉的这条从历史、收藏流与收藏墙里摘掉，往回划不该再翻到一条已经不存在的内容。按条目认，
    // 不按请求发出时的位置认：请求在路上时可能已经划走了。
    async forgetDeleted(target) {
      const key = itemKey(target);
      const matches = item => itemKey(item) === key;
      this.recentKeys = this.recentKeys.filter(recent => recent !== key);
      if (this.prefetchedVideo && matches(this.prefetchedVideo)) this.prefetchedVideo = null;
      this.favorites = this.favorites.filter(item => !matches(item));

      const wasCurrent = !!this.currentVideo && matches(this.currentVideo);
      const streamPosition = this.favoriteStream.items.findIndex(matches);
      if (streamPosition !== -1) {
        const items = this.favoriteStream.items.filter((_, position) => position !== streamPosition);
        let index = this.favoriteStream.index;
        if (streamPosition < index) index -= 1;
        this.favoriteStream = { items, index: Math.min(index, items.length - 1) };
      }
      const position = this.items.findIndex(matches);
      if (position !== -1) {
        this.items = this.items.filter((_, offset) => offset !== position);
        if (position < this.index) this.index -= 1;
      }
      if (!wasCurrent) return;

      if (this.inFavoriteStream) {
        // 收藏删空了就回到墙上（回墙时重新取一次）。
        if (this.favoriteStream.items.length === 0) {
          await this.closeFavoriteStream();
          return;
        }
        this.activateCurrent();
        return;
      }
      // 推荐流：后面还有历史就前进到它；删的是最后一条就取新的一条，取不到停在前一条并给重试。
      if (this.index < this.items.length) {
        this.activateCurrent();
        return;
      }
      this.index = this.items.length - 1;
      const fallback = this.currentVideo;
      await this.loadNextItem();
      // 没取到新的（失败或正在取）：停在前一条，把它重新激活。
      if (fallback && this.currentVideo === fallback) this.activateCurrent();
    },
    openMoreSheet() {
      this.sheet = 'more';
      this.showControls();
      this.clearControlsHideTimer();
    },
    // 全屏在「更多」里（D-MW-01）：选了就关面板再切换，请求仍在这次点击的手势里发出。
    toggleFullscreenFromSheet() {
      this.sheet = null;
      return this.toggleFullscreen();
    },
    openRatingSheet() {
      this.ratingDraft = null;
      this.sheet = 'rating';
      this.showControls();
      this.clearControlsHideTimer();
    },
    async openTagSheet() {
      this.sheet = 'tags';
      this.tagKeyword = '';
      this.showControls();
      this.clearControlsHideTimer();
      // 每次打开都重拉：桌面端刚建的标签不该等到刷新页面才出现。
      await this.loadFeedTags();
    },
    async openScopeSheet() {
      this.sheet = 'scope';
      this.showControls();
      this.clearControlsHideTimer();
      await this.loadScopes();
    },
    async loadFeedTags(keyword = this.tagKeyword) {
      const seq = ++this.tagQuerySeq;
      try {
        const payload = await getFeedTags(keyword);
        // 更新的查询已经发出，这份结果过期了。
        if (seq !== this.tagQuerySeq) return;
        this.feedTags = payload?.tags || [];
      } catch (err) {
        if (seq !== this.tagQuerySeq) return;
        // 拉不到就沿用上一次的列表，别把面板清空；但要说一声，否则空面板像是真没标签。
        this.flashToast(`标签列表加载失败：${String(err.message || err)}`);
      }
    },
    async createTagFromKeyword() {
      const name = this.tagKeyword.trim();
      if (!name || !this.currentVideo || this.creatingTag) return;
      this.creatingTag = true;
      try {
        const tag = await createFeedTag(name);
        if (!this.feedTags.some(item => item.id === tag.id)) {
          this.feedTags = [...this.feedTags, tag].sort((a, b) => a.name.localeCompare(b.name, 'zh-Hans-CN'));
        }
        if (!this.isTagAttached(tag.id)) await this.toggleTag(tag);
        else this.tagKeyword = '';
      } catch (err) {
        this.flashToast(`新建标签失败：${String(err.message || err)}`);
      } finally {
        this.creatingTag = false;
      }
    },
    async loadScopes() {
      this.scopesError = '';
      try {
        const payload = await getScopes(this.mediaKind);
        this.scopes = payload?.scopes || [];
      } catch (err) {
        this.scopes = [];
        this.scopesError = `播放范围加载失败：${feedErrorText(err)}`;
      }
    },
    async applyScope(scope) {
      this.sheet = null;
      if (scope === this.scope) return;
      this.scope = scope;
      await this.restartTimeline();
    },
    async applyMediaKind(kind) {
      if (!MEDIA_KIND_OPTIONS.some(option => option.kind === kind) || kind === this.mediaKind) return;
      this.mediaKind = kind;
      storeMediaKind(kind);
      this.sheet = null;
      await this.restartTimeline();
    },
    // 换了范围或资源类型就重开一条时间线：旧历史属于旧筛选，留着会前后矛盾。
    async restartTimeline() {
      this.items = [];
      this.index = -1;
      this.prefetchedVideo = null;
      this.recentKeys = [];
      this.emptyScope = false;
      this.loadError = '';
      await this.nextVideo();
    },
    resetPhotoZoom() {
      this.photoZoomed = false;
      this.showControls();
    },
    // 拖滑杆只改显示，松手（change）才写入（D-MW-04）。
    previewRating(value) {
      this.ratingDraft = { value };
    },
    // 点星、松开滑杆、清空都走这里：写入期间先显示新分；失败时显示回到写入前的分并说原因。
    async applyRating(rating) {
      if (!this.currentVideo) return;
      this.ratingDraft = { value: rating };
      try {
        this.replaceCurrent(await setRating(this.currentVideo, rating));
      } catch (err) {
        this.flashToast(`评分失败：${String(err.message || err)}`);
      } finally {
        this.ratingDraft = null;
      }
    },
    async toggleWatched() {
      if (!this.currentVideo) return;
      try {
        this.replaceCurrent(await setWatched(this.currentVideo, !this.currentVideo.watched));
      } catch (err) {
        this.flashToast(`标记失败：${String(err.message || err)}`);
      }
    },
    isTagAttached(tagID) {
      return (this.currentVideo?.tags || []).some(tag => tag.id === tagID);
    },
    async toggleTag(tag) {
      // 自动标签由系统维护，面板只读展示。
      if (!this.currentVideo || tag.automatic) return;
      const attached = !this.isTagAttached(tag.id);
      try {
        this.replaceCurrent(await setItemTag(this.currentVideo, tag.id, attached));
        if (attached) this.tagKeyword = '';
      } catch (err) {
        this.flashToast(`标签写入失败：${String(err.message || err)}`);
      }
    },
    // 顶部标签（D-MW-01）：推荐 ↔ 收藏。
    async switchView(key) {
      if (key === 'favorites') {
        await this.openFavorites();
        return;
      }
      if (key === 'feed' && this.view === 'favorites') await this.returnToFeed();
    },
    async openFavorites() {
      this.leaveStage();
      this.sheet = null;
      this.view = 'favorites';
      await this.loadFavorites();
    },
    // 回到推荐流：历史与位置原样（D-MW-05），当前这一条重新开始一次观看。
    async returnToFeed() {
      this.view = 'feed';
      if (this.items.length === 0) {
        await this.nextVideo();
        return;
      }
      await this.$nextTick();
      this.activateCurrent();
    },
    // 舞台要卸下（去收藏墙）：<video> 直接拿掉不会发 pause，播放态、唤醒锁与定时器在这里收拾。
    leaveStage() {
      this.isPlaying = false;
      this.clearControlsHideTimer();
      this.cancelLongPress();
      this.releaseWakeLock();
    },
    // 点墙上一张：用墙上当前筛选后的列表、从这一张开始（D-MW-05）。
    openFavoriteStream(list, index) {
      if (!list?.[index]) return;
      this.favoriteStream = { items: [...list], index };
      this.view = 'favorites-feed';
      this.activateCurrent();
    },
    // 「‹ 收藏」：回到墙上并重新取列表（在里面取消收藏、删除过的都以服务端为准）。
    async closeFavoriteStream() {
      this.leaveStage();
      this.sheet = null;
      this.favoriteStream = { items: [], index: -1 };
      this.view = 'favorites';
      await this.loadFavorites();
    },
    // 收藏流里上下滑：到两端停住，不取新条目。在这里取消了收藏的那一条留到划走为止，划走时从流里拿掉。
    moveFavoriteStream(direction) {
      const { items, index } = this.favoriteStream;
      const target = index + direction;
      if (target < 0 || target >= items.length) return;
      const leaving = items[index];
      if (leaving && !leaving.favorited) {
        this.favoriteStream = { items: items.filter((_, position) => position !== index), index: direction > 0 ? index : target };
      } else {
        this.favoriteStream = { items, index: target };
      }
      this.activateCurrent();
    },
    // 收藏页加载失败显示错误态与重试（D-PC46、PLAY-13）：之前清空列表，看起来像「暂无收藏」。
    // 已经显示的收藏不清掉，重试成功再换。
    async loadFavorites() {
      this.favoritesLoading = true;
      this.favoritesError = '';
      try {
        const payload = await getFavorites();
        this.favorites = payload?.items || [];
      } catch (err) {
        this.favoritesError = `收藏加载失败：${feedErrorText(err)}`;
      } finally {
        this.favoritesLoading = false;
      }
    },
    onTouchStart(event) {
      this.touchBeganZoomed = this.photoZoomed;
      if (this.isInteractiveControl(event.target)) return;
      event.preventDefault();
      swipeTracker.start(event);
      const touch = event.touches?.[0];
      if (touch) {
        this.startLongPressAt(touch.clientX, touch.clientY);
      }
    },
    onTouchMove(event) {
      if (this.isInteractiveControl(event.target)) return;
      event.preventDefault();
      const touch = event.touches?.[0];
      if (touch) {
        this.trackLongPressMove(touch);
      }
    },
    onTouchEnd(event) {
      if (this.touchBeganZoomed) {
        // 放大态里开始的触摸由 pointer 路径处理完了（双击退出放大也在那边），
        // 这里没有对应的 start，拿陈旧的起点算手势只会误翻页或再放大。
        this.touchBeganZoomed = false;
        return;
      }
      if (this.isInteractiveControl(event.target)) return;
      event.preventDefault();
      const wasLongPress = this.longPressTriggered;
      this.cancelLongPress();
      if (wasLongPress) return;
      const direction = swipeTracker.end(event);
      if (direction === 0) {
        this.handleStageTap(event);
        return;
      }
      this.nextVideo(direction);
    },
    onTouchCancel(event) {
      this.touchBeganZoomed = false;
      if (!this.isInteractiveControl(event.target)) {
        event.preventDefault();
      }
      this.cancelLongPress();
    },
    isInteractiveControl(target) {
      // 图片放大时整个舞台交给浏览器原生平移，不再拦截为翻页手势。
      if (this.photoZoomed) return true;
      if (target?.closest?.('.tag-row')) return true;
      // 底部面板整层（含遮罩、输入框）都不归舞台管：舞台一 preventDefault，
      // 点遮罩关闭的合成 click 与输入框聚焦就都没了。
      return !!target?.closest?.('button, input, textarea, select, [role="slider"], .progress-dock, .favorites-view, .pin-gate, .feed-retry, .sheet-layer');
    },
    onWheel(event) {
      this.nextVideo(wheelDirection(event.deltaY, Date.now(), this.wheelState));
    },
    onKeydown(event) {
      // PIN 页与输入框里的空格、方向键属于输入，不是翻页；收藏墙上的方向键留给墙自己滚动。
      if (this.authRequired || this.view === 'favorites' || event.target?.closest?.('input, textarea, select')) return;
      const direction = keyboardDirection(event.key);
      if (direction !== 0) {
        event.preventDefault();
        this.nextVideo(direction);
      }
    },
  }
};
</script>
