<template>
  <main
    class="short-feed"
    tabindex="0"
    @touchstart.passive="onTouchStart"
    @touchend.passive="onTouchEnd"
    @wheel.prevent="onWheel"
    @keydown="onKeydown"
  >
    <section v-if="view === 'feed'" class="feed-stage">
      <video
        v-if="currentVideo && currentVideo.media_url"
        ref="videoEl"
        class="feed-video"
        :src="currentVideo.media_url"
        :muted="muted"
        autoplay
        playsinline
        loop
        @playing="onVideoPlaying"
        @error="onVideoError"
      ></video>

      <div v-else class="feed-empty">
        <div>{{ statusText }}</div>
      </div>

      <div class="top-bar">
        <button class="icon-btn" type="button" title="收藏夹" @click="openFavorites">★</button>
        <button class="icon-btn" type="button" :title="muted ? '打开声音' : '静音'" @click="muted = !muted">
          {{ muted ? '🔇' : '🔊' }}
        </button>
      </div>

      <div v-if="currentVideo" class="video-meta">
        <h1>{{ currentVideo.name }}</h1>
        <div class="tag-row">
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

      <nav class="action-rail" aria-label="视频操作">
        <button
          class="round-action"
          :class="{ active: currentVideo?.liked }"
          type="button"
          title="喜欢"
          :disabled="!currentVideo"
          @click="toggleLike"
        >
          ♥
        </button>
        <button
          class="round-action"
          :class="{ active: currentVideo?.favorited }"
          type="button"
          title="收藏"
          :disabled="!currentVideo"
          @click="toggleFavorite"
        >
          ★
        </button>
        <button
          class="round-action danger"
          type="button"
          title="删除"
          :disabled="!currentVideo"
          @click="deleteDialogOpen = true"
        >
          🗑
        </button>
      </nav>

      <button class="next-hit" type="button" title="下一个" @click="nextVideo">↓</button>
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
      recentIDs: [],
      favorites: [],
      view: 'feed',
      loading: false,
      statusText: '加载中',
      muted: true,
      recordedVideoID: null,
      deleteDialogOpen: false,
      wheelState: { lastWheelAt: 0 }
    };
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
        const video = await getNextVideo(this.recentIDs.slice(-12));
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
      if (!this.recentIDs.includes(video.id)) {
        this.recentIDs.push(video.id);
      }
      this.recentIDs = this.recentIDs.slice(-20);
      this.$nextTick(() => {
        const player = this.$refs.videoEl;
        if (player?.play) player.play().catch(() => {});
      });
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
      swipeTracker.start(event);
    },
    onTouchEnd(event) {
      this.nextVideo(swipeTracker.end(event));
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
    }
  }
};
</script>
