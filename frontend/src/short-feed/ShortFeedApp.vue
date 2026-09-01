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
    <section v-if="view === 'feed'" class="feed-stage">
      <FeedStage
        ref="stage"
        :item="currentVideo"
        :prefetched="prefetchedVideo"
        :muted="muted"
        :zoomed="photoZoomed"
        :status-text="statusText"
        @press-start="startLongPress"
        @press-move="trackLongPressMove"
        @press-end="finishPointerPress"
        @press-cancel="cancelLongPress"
        @media-loaded="onMediaLoaded"
        @time-update="onTimeUpdate"
        @play="onVideoPlay"
        @pause="onVideoPause"
        @playing="onVideoPlaying"
        @media-error="onMediaError"
        @stage-tap="handleStageTap"
      />

      <FeedTopBar
        :visible="chromeVisible"
        :muted="muted"
        :counter="counterText"
        :scope-label="scopeLabel"
        @open-favorites="openFavorites"
        @toggle-muted="muted = !muted"
        @open-scope="openScopeSheet"
      />
      <FeedMeta :visible="chromeVisible" :item="currentVideo" @open-tags="openTagSheet" />
      <FeedActionRail
        :visible="chromeVisible"
        :item="currentVideo"
        :rail-label="isImageItem ? '图片操作' : '视频操作'"
        @toggle-like="toggleLike"
        @toggle-favorite="toggleFavorite"
        @toggle-watched="toggleWatched"
        @open-rating="openRatingSheet"
        @open-tags="openTagSheet"
        @request-delete="deleteDialogOpen = true"
      />
      <FeedDots v-if="items.length > 1" :total="items.length" :index="index" />
      <FeedProgress
        v-if="!isImageItem && currentVideo && currentVideo.media_url"
        :visible="chromeVisible"
        :value="progressValue"
        :value-text="`${formatTime(videoCurrentTime)} / ${formatTime(videoDuration)}`"
        @seek-start="startSeeking"
        @seek-move="moveSeeking"
        @seek-end="finishSeeking"
        @seek-cancel="cancelSeeking"
        @seek-key="seekByKeyboard"
      />
    </section>

    <FavoritesView
      v-else
      :items="favorites"
      @close="view = 'feed'"
      @refresh="loadFavorites"
      @select="selectFavorite"
    />

    <FeedSheet
      v-if="sheet === 'rating'"
      title="个人评分"
      hint="0–10，半分制，可清空"
      @close="sheet = null"
    >
      <template #head>
        <strong class="sheet__value">{{ currentRatingText }}</strong>
      </template>
      <div class="rating-grid">
        <button
          v-for="option in ratingOptions"
          :key="option"
          type="button"
          class="rating-cell"
          :class="{ active: currentVideo && currentVideo.personal_rating === option }"
          @click="applyRating(option)"
        >{{ option.toFixed(1) }}</button>
      </div>
      <template #footer>
        <button type="button" class="sheet-btn" @click="applyRating(null)">清空评分</button>
        <button type="button" class="sheet-btn sheet-btn--primary sheet-btn--wide" @click="sheet = null">完成</button>
      </template>
    </FeedSheet>

    <FeedSheet v-if="sheet === 'tags'" title="标签" hint="点击切换，改动即时写入本地库" @close="sheet = null">
      <input v-model="tagKeyword" class="sheet-input" type="text" placeholder="搜索标签…" aria-label="搜索标签" />
      <div class="tag-picker">
        <button
          v-for="tag in visibleFeedTags"
          :key="tag.id"
          type="button"
          class="tag-option"
          :class="{ active: isTagAttached(tag.id) }"
          :style="isTagAttached(tag.id) ? { backgroundColor: `${tag.color}44`, borderColor: `${tag.color}aa` } : null"
          @click="toggleTag(tag)"
        >
          <span class="tag-option__dot" :style="{ backgroundColor: tag.color || '#8fa0a5' }"></span>{{ tag.name }}
        </button>
        <p v-if="visibleFeedTags.length === 0" class="sheet__empty">没有匹配的标签。标签在桌面端「标签管理」里新建。</p>
      </div>
      <template #footer>
        <button type="button" class="sheet-btn sheet-btn--primary sheet-btn--wide" @click="sheet = null">完成</button>
      </template>
    </FeedSheet>

    <FeedSheet v-if="sheet === 'scope'" title="播放范围" @close="sheet = null">
      <button
        v-for="option in scopes"
        :key="option.scope"
        type="button"
        class="scope-option"
        :class="{ active: option.scope === scope }"
        @click="applyScope(option.scope)"
      >
        {{ option.name }}<span class="scope-option__count">{{ option.count }}</span>
      </button>
    </FeedSheet>

    <DeleteDialog
      v-if="deleteDialogOpen"
      :title="isImageItem ? '删除图片' : '删除视频'"
      :message="isImageItem
        ? '图片会移入回收站，可在桌面端恢复，并从图片库与手机 Feed 中移除。'
        : '文件会移入 trash 文件夹，并从普通列表和短视频 Feed 中移除。'"
      @cancel="deleteDialogOpen = false"
      @confirm="confirmDelete"
    />

    <div v-if="toast" class="feed-toast" role="status">
      <span class="feed-toast__text">{{ toast.message }}</span>
      <button v-if="toast.undo" type="button" class="feed-toast__undo" @click="undoDelete">撤销</button>
    </div>
  </main>
</template>

<script>
import {
  deleteItem, getFavorites, getFeedTags, getNextItem, getScopes, itemKey, recordPlay,
  restoreItem, setFavorited, setItemTag, setLiked, setRating, setWatched
} from './api.js';
import { createSwipeTracker, keyboardDirection, wheelDirection } from './gesture.js';
import { unsupportedStatusText } from './videoState.js';
import { createWakeLock } from './useWakeLock.js';
import FeedStage from './components/FeedStage.vue';
import FeedTopBar from './components/FeedTopBar.vue';
import FeedMeta from './components/FeedMeta.vue';
import FeedActionRail from './components/FeedActionRail.vue';
import FeedProgress from './components/FeedProgress.vue';
import FavoritesView from './components/FavoritesView.vue';
import DeleteDialog from './components/DeleteDialog.vue';
import FeedSheet from './components/FeedSheet.vue';
import FeedDots from './components/FeedDots.vue';

const swipeTracker = createSwipeTracker();
const wakeLock = createWakeLock();
// 图片没有播放态，控件不能靠"暂停中"常驻，改为定时收起。
const PHOTO_CONTROLS_HIDE_MS = 2600;
// 浏览历史的上限：一路划下去不能把整库都留在内存里。
const FEED_HISTORY_LIMIT = 40;

export default {
  name: 'ShortFeedApp',
  components: { FeedStage, FeedTopBar, FeedMeta, FeedActionRail, FeedProgress, FavoritesView, DeleteDialog, FeedSheet, FeedDots },
  data() {
    return {
      // 保留浏览历史而不是只留当前一条：能往回划，右侧圆点的位置才是个真实的东西。
      items: [],
      index: -1,
      scope: 'all',
      scopes: [],
      feedTags: [],
      tagKeyword: '',
      sheet: null,
      toast: null,
      toastTimer: null,
      pendingUndo: null,
      prefetchedVideo: null,
      prefetching: false,
      recentKeys: [],
      favorites: [],
      view: 'feed',
      loading: false,
      statusText: '加载中',
      muted: true,
      playbackRate: 1,
      recordedVideoID: null,
      deleteDialogOpen: false,
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
      photoZoomed: false
    };
  },
  computed: {
    currentVideo() {
      return this.items[this.index] || null;
    },
    counterText() {
      if (this.items.length === 0) return '';
      return `${this.index + 1} / ${this.items.length}`;
    },
    scopeLabel() {
      return this.scopes.find(item => item.scope === this.scope)?.name || '全部短视频';
    },
    currentRatingText() {
      const rating = this.currentVideo?.personal_rating;
      return rating === null || rating === undefined ? '未评分' : Number(rating).toFixed(1);
    },
    ratingOptions() {
      return Array.from({ length: 21 }, (_, step) => step * 0.5);
    },
    visibleFeedTags() {
      const keyword = this.tagKeyword.trim().toLowerCase();
      if (!keyword) return this.feedTags;
      return this.feedTags.filter(tag => tag.name.toLowerCase().includes(keyword));
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
  },
  beforeUnmount() {
    this.clearControlsHideTimer();
    this.clearLongPressTimer();
    this.releaseWakeLock();
    document.removeEventListener('visibilitychange', this.handleVisibilityChange);
  },
  async mounted() {
    document.addEventListener('visibilitychange', this.handleVisibilityChange);
    await this.nextVideo();
    this.$el.focus();
  },
  methods: {
    async nextVideo(direction = 1) {
      if (this.loading || direction === 0 || this.sheet) return;
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
      this.loading = true;
      this.statusText = '加载中';
      try {
        const video = this.takePrefetchedVideo() || await getNextItem(this.recentKeys.slice(-12), this.scope);
        this.appendItem(video);
      } catch (err) {
        this.items = [];
        this.index = -1;
        this.statusText = String(err.message || err);
      } finally {
        this.loading = false;
      }
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
      this.recordedVideoID = null;
      this.isPlaying = false;
      this.videoCurrentTime = 0;
      this.videoDuration = 0;
      this.scrubValue = 0;
      this.controlsVisible = false;
      this.photoZoomed = false;
      this.clearControlsHideTimer();
      const key = itemKey(video);
      if (!this.recentKeys.includes(key)) {
        this.recentKeys.push(key);
      }
      this.recentKeys = this.recentKeys.slice(-20);
      this.$nextTick(() => {
        const player = this.player();
        this.applyPlaybackRate();
        if (player?.play) player.play().catch(() => {});
      });
      this.prefetchNextVideo();
    },
    // 改动后的整条 DTO 由后端回来，就地换掉，不猜写入结果。
    replaceCurrent(updated) {
      if (!updated || this.index < 0) return;
      const next = [...this.items];
      next[this.index] = updated;
      this.items = next;
    },
    flashToast(message, undo = false) {
      this.toast = { message, undo };
      if (this.toastTimer) clearTimeout(this.toastTimer);
      this.toastTimer = window.setTimeout(() => { this.toast = null; }, 5000);
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
        const video = await getNextItem(excludeKeys, this.scope);
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
      if (!wasLongPress && event.pointerType !== 'touch') {
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
      try {
        if (!wasLiked) {
          await setLiked(this.currentVideo, true);
        }
        if (!wasFavorited) {
          await setFavorited(this.currentVideo, true);
        }
        if (this.isPlaying) {
          this.scheduleControlsHide();
        }
      } catch (err) {
        this.currentVideo.liked = wasLiked;
        this.currentVideo.favorited = wasFavorited;
      } finally {
        this.longPressActionInFlight = false;
      }
    },
    onVideoPlaying() {
      this.recordCurrentItemView();
    },
    async recordCurrentItemView() {
      const key = itemKey(this.currentVideo);
      if (!this.currentVideo || this.recordedVideoID === key) return;
      this.recordedVideoID = key;
      try {
        await recordPlay(this.currentVideo);
      } catch (err) {}
    },
    onMediaLoaded() {
      if (this.isImageItem) return;
      this.syncVideoTime();
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
    async toggleLike() {
      if (!this.currentVideo) return;
      const liked = !this.currentVideo.liked;
      this.currentVideo.liked = liked;
      try {
        await setLiked(this.currentVideo, liked);
      } catch (err) {
        this.currentVideo.liked = !liked;
      }
    },
    async toggleFavorite() {
      if (!this.currentVideo) return;
      const favorited = !this.currentVideo.favorited;
      this.currentVideo.favorited = favorited;
      try {
        await setFavorited(this.currentVideo, favorited);
      } catch (err) {
        this.currentVideo.favorited = !favorited;
      }
    },
    async confirmDelete() {
      if (!this.currentVideo) return;
      const deleted = this.currentVideo;
      const deletedKey = itemKey(deleted);
      this.deleteDialogOpen = false;
      try {
        await deleteItem(deleted);
        this.recentKeys = this.recentKeys.filter(key => key !== deletedKey);
        // 删掉的这条从历史里摘掉，往回划不该再翻到一条已经不存在的内容。
        this.items = this.items.filter((item, position) => position !== this.index);
        this.index = Math.min(this.index, this.items.length - 1);
        this.pendingUndo = deleted;
        this.flashToast(`已移入回收站 · ${deleted.name}`, true);
        if (this.index < 0 || this.items.length === 0) {
          await this.nextVideo();
        } else {
          this.activateCurrent();
        }
      } catch (err) {
        this.statusText = String(err.message || err);
      }
    },
    async undoDelete() {
      if (!this.pendingUndo) return;
      const target = this.pendingUndo;
      this.pendingUndo = null;
      this.toast = null;
      try {
        await restoreItem(target);
        this.flashToast(`已恢复 · ${target.name}`);
      } catch (err) {
        this.flashToast(`恢复失败：${String(err.message || err)}`);
      }
    },
    openRatingSheet() {
      this.sheet = 'rating';
      this.showControls();
      this.clearControlsHideTimer();
    },
    async openTagSheet() {
      this.sheet = 'tags';
      this.tagKeyword = '';
      this.showControls();
      this.clearControlsHideTimer();
      if (this.feedTags.length === 0) await this.loadFeedTags();
    },
    async openScopeSheet() {
      this.sheet = 'scope';
      this.showControls();
      this.clearControlsHideTimer();
      await this.loadScopes();
    },
    async loadFeedTags() {
      try {
        const payload = await getFeedTags();
        this.feedTags = payload?.tags || [];
      } catch (err) {
        this.feedTags = [];
      }
    },
    async loadScopes() {
      try {
        const payload = await getScopes();
        this.scopes = payload?.scopes || [];
      } catch (err) {
        this.scopes = [];
      }
    },
    async applyScope(scope) {
      this.sheet = null;
      if (scope === this.scope) return;
      this.scope = scope;
      // 换了范围就重开一条时间线：旧历史属于旧范围，留着会前后矛盾。
      this.items = [];
      this.index = -1;
      this.prefetchedVideo = null;
      this.recentKeys = [];
      await this.nextVideo();
    },
    async applyRating(rating) {
      if (!this.currentVideo) return;
      try {
        this.replaceCurrent(await setRating(this.currentVideo, rating));
      } catch (err) {
        this.flashToast(`评分失败：${String(err.message || err)}`);
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
      if (!this.currentVideo) return;
      const attached = !this.isTagAttached(tag.id);
      try {
        this.replaceCurrent(await setItemTag(this.currentVideo, tag.id, attached));
      } catch (err) {
        this.flashToast(`标签写入失败：${String(err.message || err)}`);
      }
    },
    async openFavorites() {
      this.view = 'favorites';
      await this.loadFavorites();
    },
    async loadFavorites() {
      try {
        const payload = await getFavorites();
        this.favorites = payload?.items || [];
      } catch (err) {
        this.favorites = [];
      }
    },
    selectFavorite(video) {
      this.view = 'feed';
      this.appendItem(video);
    },
    onTouchStart(event) {
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
      if (!this.isInteractiveControl(event.target)) {
        event.preventDefault();
      }
      this.cancelLongPress();
    },
    isInteractiveControl(target) {
      // 图片放大时整个舞台交给浏览器原生平移，不再拦截为翻页手势。
      if (this.photoZoomed) return true;
      return !!target?.closest?.('button, [role="slider"], .progress-dock, .modal-backdrop, .favorites-view');
    },
    onWheel(event) {
      this.nextVideo(wheelDirection(event.deltaY, Date.now(), this.wheelState));
    },
    onKeydown(event) {
      const direction = keyboardDirection(event.key);
      if (direction !== 0) {
        event.preventDefault();
        this.nextVideo(direction);
      }
    },
    tagColor(color) {
      if (!color) return 'rgba(255,255,255,0.18)';
      return `${color}66`;
    },
    formatTime(seconds) {
      if (!Number.isFinite(seconds) || seconds <= 0) return '00:00';
      const totalSeconds = Math.floor(seconds);
      const minutes = Math.floor(totalSeconds / 60);
      const remainingSeconds = totalSeconds % 60;
      return `${String(minutes).padStart(2, '0')}:${String(remainingSeconds).padStart(2, '0')}`;
    }
  }
};
</script>
