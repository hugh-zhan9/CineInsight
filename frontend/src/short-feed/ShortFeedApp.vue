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

      <FeedTopBar :visible="chromeVisible" :muted="muted" @open-favorites="openFavorites" @toggle-muted="muted = !muted" />
      <FeedMeta :visible="chromeVisible" :item="currentVideo" />
      <FeedActionRail
        :visible="chromeVisible"
        :item="currentVideo"
        :rail-label="isImageItem ? '图片操作' : '视频操作'"
        @toggle-like="toggleLike"
        @toggle-favorite="toggleFavorite"
        @request-delete="deleteDialogOpen = true"
      />
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

    <DeleteDialog
      v-if="deleteDialogOpen"
      :title="isImageItem ? '删除图片' : '删除视频'"
      :message="isImageItem
        ? '图片会移入回收站，可在桌面端恢复，并从图片库与手机 Feed 中移除。'
        : '文件会移入 trash 文件夹，并从普通列表和短视频 Feed 中移除。'"
      @cancel="deleteDialogOpen = false"
      @confirm="confirmDelete"
    />
  </main>
</template>

<script>
import { deleteItem, getFavorites, getNextItem, itemKey, recordPlay, setFavorited, setLiked } from './api.js';
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

const swipeTracker = createSwipeTracker();
const wakeLock = createWakeLock();
// 图片没有播放态，控件不能靠"暂停中"常驻，改为定时收起。
const PHOTO_CONTROLS_HIDE_MS = 2600;

export default {
  name: 'ShortFeedApp',
  components: { FeedStage, FeedTopBar, FeedMeta, FeedActionRail, FeedProgress, FavoritesView, DeleteDialog },
  data() {
    return {
      currentVideo: null,
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
      if (this.loading || direction === 0) return;
      this.loading = true;
      this.statusText = '加载中';
      try {
        const video = this.takePrefetchedVideo() || await getNextItem(this.recentKeys.slice(-12));
        this.applyVideo(video);
      } catch (err) {
        this.currentVideo = null;
        this.statusText = String(err.message || err);
      } finally {
        this.loading = false;
      }
    },
    applyVideo(video) {
      this.currentVideo = video;
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
        const video = await getNextItem(excludeKeys);
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
        this.currentVideo = { ...this.currentVideo, media_url: '' };
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
        await this.nextVideo();
      } catch (err) {
        this.statusText = String(err.message || err);
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
      this.applyVideo(video);
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
