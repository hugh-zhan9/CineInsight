<template>
  <div class="feed-media">
    <!-- 横屏素材加 landscape 类改为完整显示：竖屏 feed 默认的 cover 裁切只会剩下画面中间三分之一。 -->
    <video
      v-if="isVideo && item && item.media_url"
      ref="videoEl"
      class="feed-video"
      :class="{ 'feed-video--landscape': landscape }"
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
      @loadedmetadata="onVideoMetadata"
      @canplaythrough="$emit('buffered')"
      @timeupdate="$emit('time-update')"
      @play="$emit('play')"
      @pause="$emit('pause')"
      @playing="$emit('playing')"
      @error="$emit('media-error')"
      @webkitbeginfullscreen="$emit('native-fullscreen', true)"
      @webkitendfullscreen="$emit('native-fullscreen', false)"
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
      <!-- 放大后舞台交给原生平移，双击之外再给一个明确的出口，不然上下滑切换就被卡住。 -->
      <button v-if="zoomed" type="button" class="photo-zoom-reset" data-test="photo-zoom-reset" @click.stop="$emit('zoom-reset')">恢复适屏</button>
    </div>

    <!-- 预取的下一条不能一上来就整只下载：它会和正在播的这条抢带宽，短视频也会卡。
         当前条缓冲够了（canplaythrough）宿主才把 preload 升到 auto。 -->
    <video
      v-if="prefetched && prefetched.media_kind === 'video' && prefetched.media_url"
      class="preload-video"
      :src="prefetched.media_url"
      muted
      :preload="prefetchPreload"
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
    statusText: { type: String, default: '' },
    // 预取视频的 preload 档位：metadata 只拿头部，auto 才整只下载。
    prefetchPreload: { type: String, default: 'metadata' }
  },
  emits: [
    'press-start', 'press-move', 'press-end', 'press-cancel',
    'media-loaded', 'buffered', 'time-update', 'play', 'pause', 'playing', 'media-error', 'stage-tap', 'zoom-reset',
    // iPhone 系统播放器全屏只在 <video> 上发 webkitbegin/endfullscreen，宿主靠它同步全屏态。
    'native-fullscreen'
  ],
  data() {
    return {
      // 解码后的实际横竖屏结论；null 表示还没拿到元数据。
      intrinsicLandscape: null
    };
  },
  computed: {
    isVideo() { return this.item?.media_kind !== 'image'; },
    isImage() { return this.item?.media_kind === 'image'; },
    itemKey() { return this.item ? `${this.item.media_kind}:${this.item.id}` : ''; },
    // 浏览器给出的实际尺寸已经把旋转元数据算进去，优先用它；加载前先按后端记录的宽高预判，
    // 避免首帧先裁一下再跳成完整画面。
    landscape() {
      if (this.intrinsicLandscape !== null) return this.intrinsicLandscape;
      const width = Number(this.item?.width) || 0;
      const height = Number(this.item?.height) || 0;
      return width > 0 && height > 0 && width > height;
    }
  },
  watch: {
    // 换了条目就忘掉上一条的实际尺寸；同一条目被后端回传覆盖（评分、标签）时不重置。
    itemKey() {
      this.intrinsicLandscape = null;
    }
  },
  methods: {
    // 父组件的进度条与播放控制仍直接操作 <video>，通过它拿到元素。
    player() { return this.$refs.videoEl || null; },
    onVideoMetadata(event) {
      const el = event?.target || this.$refs.videoEl;
      if (el?.videoWidth > 0 && el?.videoHeight > 0) {
        this.intrinsicLandscape = el.videoWidth > el.videoHeight;
      }
      this.$emit('media-loaded');
    }
  }
};
</script>
