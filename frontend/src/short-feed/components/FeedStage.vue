<template>
  <div class="feed-media">
    <video
      v-if="isVideo && item && item.media_url"
      ref="videoEl"
      class="feed-video"
      :src="item.media_url"
      :muted="muted"
      preload="auto"
      autoplay
      playsinline
      loop
      @pointerdown.prevent="$emit('press-start', $event)"
      @pointermove.prevent="$emit('press-move', $event)"
      @pointerup.prevent="$emit('press-end', $event)"
      @pointercancel.prevent="$emit('press-cancel', $event)"
      @pointerleave.prevent="$emit('press-cancel', $event)"
      @contextmenu.prevent
      @loadedmetadata="$emit('media-loaded')"
      @timeupdate="$emit('time-update')"
      @play="$emit('play')"
      @pause="$emit('pause')"
      @playing="$emit('playing')"
      @error="$emit('media-error')"
    ></video>

    <!-- 图片没有播放态，也不自动翻页；双击在适屏与原图之间切换，放大后交给浏览器原生平移。 -->
    <div
      v-else-if="isImage && item && item.media_url"
      class="feed-photo-pane"
      :class="{ zoomed }"
    >
      <img
        class="feed-photo"
        :class="{ zoomed }"
        :src="item.media_url"
        :alt="item.name"
        draggable="false"
        @pointerdown.prevent="$emit('press-start', $event)"
        @pointermove.prevent="$emit('press-move', $event)"
        @pointerup.prevent="$emit('press-end', $event)"
        @pointercancel.prevent="$emit('press-cancel', $event)"
        @pointerleave.prevent="$emit('press-cancel', $event)"
        @contextmenu.prevent
        @load="$emit('media-loaded')"
        @error="$emit('media-error')"
      />
    </div>

    <video
      v-if="prefetched && prefetched.media_kind === 'video' && prefetched.media_url"
      class="preload-video"
      :src="prefetched.media_url"
      muted
      preload="auto"
      playsinline
    ></video>
    <img
      v-else-if="prefetched && prefetched.media_kind === 'image' && prefetched.media_url"
      class="preload-video"
      :src="prefetched.media_url"
      alt=""
      aria-hidden="true"
    />

    <div v-if="!item || !item.media_url" class="feed-empty" @click="$emit('stage-tap', $event)">
      <div>{{ statusText }}</div>
    </div>
  </div>
</template>

<script>
export default {
  name: 'FeedStage',
  props: {
    item: { type: Object, default: null },
    prefetched: { type: Object, default: null },
    muted: { type: Boolean, default: true },
    zoomed: { type: Boolean, default: false },
    statusText: { type: String, default: '' }
  },
  emits: [
    'press-start', 'press-move', 'press-end', 'press-cancel',
    'media-loaded', 'time-update', 'play', 'pause', 'playing', 'media-error', 'stage-tap'
  ],
  computed: {
    isVideo() { return this.item?.media_kind !== 'image'; },
    isImage() { return this.item?.media_kind === 'image'; }
  },
  methods: {
    // 父组件的进度条与播放控制仍直接操作 <video>，通过它拿到元素。
    player() { return this.$refs.videoEl || null; }
  }
};
</script>
