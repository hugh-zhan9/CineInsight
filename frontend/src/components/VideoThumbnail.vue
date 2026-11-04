<template>
  <span class="video-time-thumbnail" @pointermove="move" @pointerleave="leave" @click="select">
    <img :src="frameURL || src" :alt="alt" :style="frameURL ? { objectFit: 'contain', transform: 'none' } : undefined" loading="lazy" decoding="async" @error="imageError" />
    <span v-if="active && timeMs != null" class="video-time-thumbnail__time">{{ unavailable ? '暂不能预览' : formatDuration(timeMs / 1000) }}</span>
    <span v-if="active && timeMs != null" class="video-time-thumbnail__line" :style="{ left: `${ratio * 100}%` }" />
  </span>
</template>
<script>
import { createThumbnailReader, requestThumbnailPreview, thumbnailTime } from '../utils/videoThumbnail.js';
import { formatDuration } from '../utils/mediaDetails.js';
export default {
  name: 'VideoThumbnail',
  props: { video: { type: Object, required: true }, src: { type: String, required: true }, alt: { type: String, default: '' } }, emits: ['error'],
  data: () => ({ active: false, ratio: 0, timeMs: null, frameURL: '', unavailable: false }),
  watch: { 'video.id'() { this.leave(); }, 'video.duration'() { this.leave(); } },
  mounted() { this._reader = createThumbnailReader(url => { this.frameURL = url; this.unavailable = false; }, () => { this.frameURL = ''; this.unavailable = true; }); document.addEventListener('visibilitychange', this.visibility); window.addEventListener('blur', this.leave); },
  beforeUnmount() { this.leave(); document.removeEventListener('visibilitychange', this.visibility); window.removeEventListener('blur', this.leave); },
  methods: {
    formatDuration,
    visibility() { if (document.hidden) this.leave(); },
    move(event) { if (event.pointerType === 'touch' || this.video.is_stale) return; const rect = event.currentTarget.getBoundingClientRect(); if (!rect.width) return; this.active = true; this.ratio = Math.max(0, Math.min(1, (event.clientX - rect.left) / rect.width)); const ms = thumbnailTime(Number(this.video.duration), this.ratio); if (ms != null && ms !== this.timeMs) { this.timeMs = ms; this._reader.request(Number(this.video.id), ms); } },
    leave() { this._reader?.cancel(); this.active = false; this.timeMs = null; this.frameURL = ''; },
    imageError() { if (this.frameURL) { this.frameURL = ''; this.unavailable = true; } else this.$emit('error'); },
    select(event) { if (this.active && this.timeMs != null) { event.stopPropagation(); requestThumbnailPreview(this.video.id, this.timeMs); } }
  }
};
</script>
<style scoped>
.video-time-thumbnail { position: relative; display: block; width: 100%; height: 100%; overflow: hidden; }
.video-time-thumbnail img { display: block; width: 100%; height: 100%; object-fit: cover; }
.video-time-thumbnail__time { position: absolute; bottom: 4px; left: 4px; padding: 2px 5px; background: var(--overlay-strong); color: var(--recap-hero-fg); border-radius: var(--radius-sm); font-size: var(--fs-caption); pointer-events: none; }
.video-time-thumbnail__line { position: absolute; top: 0; bottom: 0; width: 2px; background: var(--accent-color); pointer-events: none; }
</style>
