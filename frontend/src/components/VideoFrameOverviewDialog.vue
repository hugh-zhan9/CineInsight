<template>
  <Teleport to="body"><DialogShell :title="`${video.name} · 画面概览`" size="xl" data-test="video-frame-overview" @close="$emit('close')">
    <p class="help-text">点击画面，从该时间点进入预览。这里仅取帧，不记观看。</p>
    <p v-if="!validDuration" role="status">尚未取得视频时长，请先补全技术信息。</p>
    <div v-else class="frame-overview__grid">
      <div v-for="frame in frames" :key="frame.ms" class="frame-overview__item">
        <button v-if="frame.url" type="button" class="btn-plain" @click="preview(frame.ms)"><img :src="frame.url" :alt="formatDuration(frame.ms / 1000)" /><span>{{ formatDuration(frame.ms / 1000) }}</span></button>
        <button v-else-if="frame.failed" type="button" class="btn btn--secondary" @click="retry(frame)">此帧未能读取，重试</button>
        <span v-else role="status">正在读取 {{ formatDuration(frame.ms / 1000) }}…</span>
      </div>
    </div>
  </DialogShell></Teleport>
</template>
<script>
import DialogShell from './ui/DialogShell.vue';
import { formatDuration } from '../utils/mediaDetails.js';
import { createThumbnailReader, requestThumbnailPreview } from '../utils/videoThumbnail.js';
export default {
  name: 'VideoFrameOverviewDialog', components: { DialogShell }, props: { video: { type: Object, required: true } }, emits: ['close'],
  data() { return { frames: Array.from({ length: 12 }, (_, index) => ({ ms: Math.floor(Number(this.video.duration) * index / 12 * 1000), url: '', failed: false })) }; },
  computed: { validDuration() { return Number.isFinite(Number(this.video.duration)) && Number(this.video.duration) > 0; } },
  mounted() { this._readers = []; if (this.validDuration) { this._queue = [...this.frames]; this.loadNext(); this.loadNext(); } },
  beforeUnmount() { this._closed = true; for (const reader of this._readers) reader.cancel(); },
  methods: {
    formatDuration,
    preview(ms) { requestThumbnailPreview(this.video.id, ms); this.$emit('close'); },
    retry(frame) { frame.failed = false; this._queue.push(frame); this.loadNext(); },
    loadNext() { if (this._closed || !this._queue.length || (this._active || 0) >= 2) return; const frame = this._queue.shift(); this._active = (this._active || 0) + 1; const done = () => { this._active--; this.loadNext(); }; const reader = createThumbnailReader(url => { frame.url = url; done(); }, () => { frame.failed = true; done(); }); this._readers.push(reader); reader.request(Number(this.video.id), frame.ms); }
  }
};
</script>
<style scoped>
.frame-overview__grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 12px; }
.frame-overview__item { aspect-ratio: 16/10; min-width: 0; display: grid; place-items: center; background: var(--panel-muted-bg); border-radius: var(--radius-sm); overflow: hidden; }
.frame-overview__item > button { width: 100%; height: 100%; display: flex; flex-direction: column; }
.frame-overview__item img { width: 100%; min-height: 0; flex: 1; object-fit: contain; }
.frame-overview__item span { padding: 4px; font-size: var(--fs-caption); }
</style>
