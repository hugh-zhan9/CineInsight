<template>
  <div class="feed-media">
    <!-- 同一条目在预取和正式展示时沿用同一DOM，最多当前+下一项。 -->
    <div v-for="entry in entries" :key="mediaKey(entry)" class="feed-media-slot"
      :class="{ 'feed-media-slot--pending': !isCurrent(entry) }" :aria-hidden="!isCurrent(entry) || undefined">
      <video v-if="entry.media_kind !== 'image'" :ref="el => setMedia(entry, el)"
        class="feed-video" :class="{ 'feed-video--landscape': isCurrent(entry) ? landscape : entry.width > entry.height }"
        :src="entry.media_url" :muted="!isCurrent(entry) || muted"
        :preload="isCurrent(entry) ? 'auto' : prefetchPreload" :autoplay="isCurrent(entry) && !paused" playsinline
        :loop="isCurrent(entry) && !autoBrowse"
        @pointerdown.prevent="forward(entry, 'press-start', $event)" @pointermove.prevent="forward(entry, 'press-move', $event)"
        @pointerup.prevent="forward(entry, 'press-end', $event)" @pointercancel.prevent="forward(entry, 'press-cancel', $event)"
        @pointerleave.prevent="forward(entry, 'press-cancel', $event)" @contextmenu.prevent
        @loadedmetadata="isCurrent(entry) && onVideoMetadata($event)"
        @canplay="ready(entry)" @canplaythrough="ready(entry); forward(entry, 'buffered')"
        @waiting="forward(entry, 'buffering')" @timeupdate="forward(entry, 'time-update')"
        @play="forward(entry, 'play')" @pause="forward(entry, 'pause')" @playing="forward(entry, 'playing')"
        @ended="forward(entry, 'ended')" @error="failed(entry, $event)"
        @webkitbeginfullscreen="forward(entry, 'native-fullscreen', true)" @webkitendfullscreen="forward(entry, 'native-fullscreen', false)" />
      <div v-else class="feed-photo-pane" :class="{ zoomed: isCurrent(entry) && zoomed }">
        <img :ref="el => setMedia(entry, el)" class="feed-photo" :class="{ zoomed: isCurrent(entry) && zoomed }"
          :src="entry.media_url" :alt="isCurrent(entry) ? entry.name : ''" draggable="false"
          @pointerdown.prevent="forward(entry, 'press-start', $event)" @pointermove.prevent="forward(entry, 'press-move', $event)"
          @pointerup.prevent="forward(entry, 'press-end', $event)" @pointercancel.prevent="forward(entry, 'press-cancel', $event)"
          @pointerleave.prevent="forward(entry, 'press-cancel', $event)" @contextmenu.prevent
          @load="imageLoaded(entry, $event)" @error="failed(entry, $event)" />
        <button v-if="isCurrent(entry) && zoomed" type="button" class="btn btn--secondary photo-zoom-reset" data-test="photo-zoom-reset" @click.stop="$emit('zoom-reset')">恢复适屏</button>
      </div>
    </div>

    <div v-if="!item || !item.media_url" class="feed-empty" @click="$emit('stage-tap', $event)">
      <div>{{ statusText }}</div>
    </div>
  </div>
</template>

<script>
export default {
  name: 'FeedStage',
  props: {
    paused: { type: Boolean, default: false },
    autoBrowse: { type: Boolean, default: false },
    item: { type: Object, default: null },
    prefetched: { type: Object, default: null },
    muted: { type: Boolean, default: true },
    zoomed: { type: Boolean, default: false },
    statusText: { type: String, default: '' },
    // 预取档位由当前播放的缓冲情况控制；auto允许浏览器提前缓冲下一项。
    prefetchPreload: { type: String, default: 'metadata' }
  },
  emits: [
    'press-start', 'press-move', 'press-end', 'press-cancel',
    'media-loaded', 'prepared', 'active-ready', 'ended', 'buffering', 'buffered', 'time-update', 'play', 'pause', 'playing', 'media-error', 'stage-tap', 'zoom-reset',
    // iPhone 系统播放器全屏只在 <video> 上发 webkitbegin/endfullscreen，宿主靠它同步全屏态。
    'native-fullscreen'
  ],
  data() {
    return {
      // 解码后的实际横竖屏结论；null 表示还没拿到元数据。
      readiness: {},
      intrinsicLandscape: null
    };
  },
  computed: {
    entries() {
      const entries = [this.item, this.prefetched].filter(entry => entry?.media_url && (this.isCurrent(entry) || entry.media_kind !== 'image' || this.prefetchPreload === 'auto'));
      return entries.filter((entry, index) => entries.findIndex(other => this.mediaKey(other) === this.mediaKey(entry)) === index);
    },
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
      this.$nextTick(() => {
        if (this.state(this.item) === 'error') { this.$emit('media-error'); return; }
        if (this.state(this.item) === 'ready') {
          this.$emit('active-ready'); this.onVideoMetadata();
          if (this.player()?.readyState === 4) this.$emit('buffered');
        }
      });
    }
  },
  beforeUnmount() { for (const el of Object.values(this._media || {})) this.release(el); },
  methods: {
    mediaKey(entry) { return entry ? `${entry.media_kind}:${entry.id}:${entry.media_url}` : ''; },
    isCurrent(entry) { return this.mediaKey(entry) === this.mediaKey(this.item); },
    state(entry) { return this.readiness[this.mediaKey(entry)] || 'loading'; },
    forward(entry, name, event) { if (this.isCurrent(entry)) this.$emit(name, event); },
    release(el) { if (el?.tagName === 'VIDEO') el.pause(); el?.removeAttribute('src'); if (el?.tagName === 'VIDEO') el.load(); },
    setMedia(entry, el) {
      const key = this.mediaKey(entry); this._media ||= {};
      if (el) this._media[key] = el;
      else { this.release(this._media[key]); delete this._media[key]; delete this.readiness[key]; }
    },
    retry(entry) {
      const key = this.mediaKey(entry), el = this._media?.[key];
      if (!el) return;
      delete this.readiness[key];
      if (el.tagName === 'VIDEO') { el.load(); if (this.isCurrent(entry)) el.play()?.catch(() => this.failed(entry)); }
      else { el.removeAttribute('src'); el.src = entry.media_url; }
    },
    ready(entry) {
      if (!this.entries.some(item => this.mediaKey(item) === this.mediaKey(entry))) return;
      const key = this.mediaKey(entry), first = this.readiness[key] !== 'ready';
      this.readiness[key] = 'ready';
      this.$emit('prepared', { key, state: 'ready' });
      if (first && this.isCurrent(entry)) this.$emit('active-ready');
    },
    failed(entry, event) {
      if (event && this._media?.[this.mediaKey(entry)] !== event.target) return;
      if (!this.entries.some(item => this.mediaKey(item) === this.mediaKey(entry))) return;
      this.readiness[this.mediaKey(entry)] = 'error';
      this.$emit('prepared', { key: this.mediaKey(entry), state: 'error' });
      this.forward(entry, 'media-error');
    },
    async imageLoaded(entry, event) {
      const el = event.target;
      try { if (el.decode) await el.decode(); } catch { if (this._media?.[this.mediaKey(entry)] === el) this.failed(entry); return; }
      if (this._media?.[this.mediaKey(entry)] !== el) return;
      this.ready(entry); this.forward(entry, 'media-loaded');
    },
    // 父组件的进度条与播放控制仍直接操作 <video>，通过它拿到元素。
    player() { return this.isVideo ? this._media?.[this.mediaKey(this.item)] || null : null; },
    onVideoMetadata(event) {
      const el = event?.target || this.player();
      if (el?.videoWidth > 0 && el?.videoHeight > 0) {
        this.intrinsicLandscape = el.videoWidth > el.videoHeight;
      }
      this.$emit('media-loaded');
    }
  }
};
</script>
