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
      <video
        v-if="currentVideo && currentVideo.media_url"
        ref="videoEl"
        class="feed-video"
        :src="currentVideo.media_url"
        :muted="muted"
        preload="auto"
        autoplay
        playsinline
        loop
        @click="onStageClick"
        @dblclick="togglePlayback"
        @pointerdown.prevent="startLongPress"
        @pointermove.prevent="trackLongPressMove"
        @pointerup.prevent="finishPointerPress"
        @pointercancel.prevent="cancelLongPress"
        @pointerleave.prevent="cancelLongPress"
        @contextmenu.prevent
        @loadedmetadata="syncVideoTime"
        @timeupdate="onTimeUpdate"
        @play="onVideoPlay"
        @pause="onVideoPause"
        @playing="onVideoPlaying"
        @error="onVideoError"
      ></video>

      <video
        v-if="prefetchedVideo && prefetchedVideo.media_url"
        class="preload-video"
        :src="prefetchedVideo.media_url"
        muted
        preload="auto"
        playsinline
      ></video>

      <div v-else class="feed-empty" @click="onStageClick">
        <div>{{ statusText }}</div>
      </div>

      <div class="top-bar" :class="{ visible: controlsVisible || !isPlaying }" @click.stop>
        <button class="icon-btn" type="button" title="收藏夹" aria-label="收藏夹" @click="openFavorites">
          <span class="top-icon top-icon--stack" aria-hidden="true"></span>
        </button>
        <button class="icon-btn" type="button" :title="muted ? '打开声音' : '静音'" :aria-label="muted ? '打开声音' : '静音'" @click="muted = !muted">
          <span class="top-icon" :class="muted ? 'top-icon--muted' : 'top-icon--sound'" aria-hidden="true"></span>
        </button>
      </div>

      <div v-if="currentVideo" class="video-meta" :class="{ visible: controlsVisible || !isPlaying }">
        <h1>{{ currentVideo.name }}</h1>
        <div v-if="currentVideo.tags && currentVideo.tags.length" class="tag-row">
          <span
            v-for="tag in currentVideo.tags"
            :key="tag.id"
            class="tag-chip"
            :style="{ backgroundColor: tagColor(tag.color) }"
          >
            {{ tag.name }}
          </span>
        </div>
      </div>

      <nav class="action-rail" :class="{ visible: controlsVisible || !isPlaying }" aria-label="视频操作" @click.stop>
        <button
          class="round-action"
          :class="{ active: currentVideo?.liked }"
          type="button"
          title="喜欢"
          :disabled="!currentVideo"
          @click="toggleLike"
        >
          <span class="action-icon action-icon--heart" aria-hidden="true"></span>
          <span class="action-count">{{ currentVideo?.liked ? 1 : 0 }}</span>
        </button>
        <button
          class="round-action"
          :class="{ active: currentVideo?.favorited }"
          type="button"
          title="收藏"
          :disabled="!currentVideo"
          @click="toggleFavorite"
        >
          <span class="action-icon action-icon--bookmark" aria-hidden="true"></span>
          <span class="action-count">{{ currentVideo?.favorited ? 1 : 0 }}</span>
        </button>
        <button
          class="round-action danger"
          type="button"
          title="删除"
          :disabled="!currentVideo"
          @click="deleteDialogOpen = true"
        >
          <span class="action-icon action-icon--trash" aria-hidden="true"></span>
        </button>
      </nav>

      <div
        v-if="currentVideo && currentVideo.media_url"
        class="progress-dock"
        :class="{ visible: controlsVisible || !isPlaying }"
        @click.stop
        @touchstart.stop
        @touchend.stop
        @wheel.stop
      >
        <div
          class="progress-scrubber"
          role="slider"
          tabindex="0"
          aria-label="播放进度"
          aria-valuemin="0"
          aria-valuemax="1000"
          :aria-valuenow="progressValue"
          :aria-valuetext="`${formatTime(videoCurrentTime)} / ${formatTime(videoDuration)}`"
          @pointerdown.stop.prevent="startSeeking"
          @pointermove.stop.prevent="moveSeeking"
          @pointerup.stop.prevent="finishSeeking"
          @pointercancel.stop.prevent="cancelSeeking"
          @keydown.stop.prevent="seekByKeyboard"
        >
          <div class="progress-track">
            <div class="progress-fill" :style="{ width: `${progressValue / 10}%` }"></div>
            <div class="progress-thumb" :style="{ left: `${progressValue / 10}%` }"></div>
          </div>
        </div>
      </div>
    </section>

    <section v-else class="favorites-view">
      <header class="favorites-header">
        <button class="icon-btn" type="button" title="返回" @click="view = 'feed'">←</button>
        <h1>收藏夹</h1>
        <button class="icon-btn" type="button" title="刷新" @click="loadFavorites">↻</button>
      </header>
      <div class="favorite-list">
        <button
          v-for="video in favorites"
          :key="video.id"
          class="favorite-item"
          type="button"
          @click="selectFavorite(video)"
        >
          <span class="favorite-title">{{ video.name }}</span>
          <span class="favorite-tags">{{ video.tags.map(tag => tag.name).join(' · ') }}</span>
        </button>
        <div v-if="favorites.length === 0" class="feed-empty">暂无收藏</div>
      </div>
    </section>

    <div v-if="deleteDialogOpen" class="modal-backdrop" @click="deleteDialogOpen = false">
      <div class="confirm-modal" @click.stop>
        <h2>删除视频</h2>
        <p>文件会移入 trash 文件夹，并从普通列表和短视频 Feed 中移除。</p>
        <div class="modal-actions">
          <button type="button" class="modal-btn" @click="deleteDialogOpen = false">取消</button>
          <button type="button" class="modal-btn danger" @click="confirmDelete">删除</button>
        </div>
      </div>
    </div>
  </main>
</template>

<script>
import { deleteVideo, getFavorites, getNextVideo, recordPlay, setFavorited, setLiked } from './api.js';
import { createSwipeTracker, keyboardDirection, wheelDirection } from './gesture.js';
import { unsupportedStatusText } from './videoState.js';

const swipeTracker = createSwipeTracker();

export default {
  name: 'ShortFeedApp',
  data() {
    return {
      currentVideo: null,
      prefetchedVideo: null,
      prefetching: false,
      recentIDs: [],
      favorites: [],
      view: 'feed',
      loading: false,
      statusText: '加载中',
      muted: true,
      playbackRate: 1,
      playbackRates: [0.75, 1, 1.25, 1.5, 2],
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
      longPressActionInFlight: false
    };
  },
  computed: {
    progressValue() {
      if (this.seeking) return this.scrubValue;
      if (!this.videoDuration) return 0;
      return Math.round((this.videoCurrentTime / this.videoDuration) * 1000);
    },
    playbackRateLabel() {
      return `${this.playbackRate}x`;
    }
  },
  beforeUnmount() {
    this.clearControlsHideTimer();
    this.clearLongPressTimer();
  },
  async mounted() {
    await this.nextVideo();
    this.$el.focus();
  },
  methods: {
    async nextVideo(direction = 1) {
      if (this.loading || direction === 0) return;
      this.loading = true;
      this.statusText = '加载中';
      try {
        const video = this.takePrefetchedVideo() || await getNextVideo(this.recentIDs.slice(-12));
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
      this.clearControlsHideTimer();
      if (!this.recentIDs.includes(video.id)) {
        this.recentIDs.push(video.id);
      }
      this.recentIDs = this.recentIDs.slice(-20);
      this.$nextTick(() => {
        const player = this.$refs.videoEl;
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
        const excludeIDs = [...new Set([...this.recentIDs.slice(-12), this.currentVideo.id])];
        const video = await getNextVideo(excludeIDs);
        if (video?.id && video.id !== this.currentVideo?.id) {
          this.prefetchedVideo = video;
        }
      } catch (err) {
      } finally {
        this.prefetching = false;
      }
    },
    onStageClick() {
      if (!this.currentVideo?.media_url) return;
      this.showControls();
      if (this.isPlaying) {
        this.scheduleControlsHide();
      }
    },
    onVideoPlay() {
      this.isPlaying = true;
      this.scheduleControlsHide(600);
    },
    onVideoPause() {
      this.isPlaying = false;
      this.showControls();
      this.clearControlsHideTimer();
    },
    togglePlayback() {
      const player = this.$refs.videoEl;
      if (!player) return;
      if (player.paused) {
        player.play().catch(() => {});
      } else {
        player.pause();
      }
      this.showControls();
    },
    cyclePlaybackRate() {
      const index = this.playbackRates.indexOf(this.playbackRate);
      this.playbackRate = this.playbackRates[(index + 1) % this.playbackRates.length];
      this.applyPlaybackRate();
      this.showControls();
      if (this.isPlaying) {
        this.scheduleControlsHide();
      }
    },
    applyPlaybackRate() {
      const player = this.$refs.videoEl;
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
      if (!wasLongPress && event.pointerType !== 'mouse') {
        this.onStageClick();
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
          await setLiked(this.currentVideo.id, true);
        }
        if (!wasFavorited) {
          await setFavorited(this.currentVideo.id, true);
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
    async onVideoPlaying() {
      if (!this.currentVideo || this.recordedVideoID === this.currentVideo.id) return;
      this.recordedVideoID = this.currentVideo.id;
      try {
        await recordPlay(this.currentVideo.id);
      } catch (err) {}
    },
    onVideoError() {
      if (!this.currentVideo) return;
      this.statusText = '当前视频无法在浏览器中播放';
      setTimeout(() => this.nextVideo(), 350);
    },
    syncVideoTime() {
      const player = this.$refs.videoEl;
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
      const player = this.$refs.videoEl;
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
      const player = this.$refs.videoEl;
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
        await setLiked(this.currentVideo.id, liked);
      } catch (err) {
        this.currentVideo.liked = !liked;
      }
    },
    async toggleFavorite() {
      if (!this.currentVideo) return;
      const favorited = !this.currentVideo.favorited;
      this.currentVideo.favorited = favorited;
      try {
        await setFavorited(this.currentVideo.id, favorited);
      } catch (err) {
        this.currentVideo.favorited = !favorited;
      }
    },
    async confirmDelete() {
      if (!this.currentVideo) return;
      const deletedID = this.currentVideo.id;
      this.deleteDialogOpen = false;
      try {
        await deleteVideo(deletedID);
        this.recentIDs = this.recentIDs.filter(id => id !== deletedID);
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
        this.favorites = payload?.videos || [];
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
        this.onStageClick();
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
