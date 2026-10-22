<template>
  <div class="playback-subtitles" data-test="playback-subtitles">
    <label><input v-model="enabled" type="checkbox" :disabled="status !== 'ready'"> 字幕</label>
    <label v-if="status === 'ready'">偏移（秒） <input v-model.number="offset" class="number-input" type="number" min="-60" max="60" step="0.1" aria-label="字幕偏移（秒）"></label>
    <button type="button" class="btn btn--secondary" @click="reload">刷新字幕</button>
    <span v-if="status === 'absent'" role="status">无外置字幕</span>
    <span v-if="error" role="alert">字幕不可用：{{ error }}</span>
  </div>
</template>
<script>
import { GetPlaybackSubtitle } from '../../../wailsjs/go/main/App';
import { subtitleVTT } from '../../utils/playbackSubtitles.js';
export default {
  name: 'PlaybackSubtitleControls',
  props: { videoId: Number, sourceVersion: String, queueToken: { type: String, default: '' }, videoElement: { type: Function, required: true } },
  data: () => ({ enabled: true, offset: 0, status: '', cues: [], error: '', request: 0 }),
  watch: {
    identity: { immediate: true, handler() { this.offset = 0; this.enabled = true; this.reload(); } },
    offset() { this.project(); }, enabled() { this.updateMode(); }
  },
  computed: { identity() { return `${this.videoId}|${this.sourceVersion}|${this.queueToken}`; } },
  mounted() {
    if (window.runtime?.EventsOn) this._offSubtitle = window.runtime.EventsOn('subtitle-changed', () => this.reload());
  },
  beforeUnmount() { ++this.request; this._offSubtitle?.(); this.release(); },
  methods: {
    release() { this._track?.remove(); this._track = null; if (this._blob) URL.revokeObjectURL(this._blob); this._blob = null; },
    async reload() {
      const request = ++this.request;
      this.release(); this.status = ''; this.error = ''; this.cues = [];
      if (!this.videoId || !this.sourceVersion) return;
      try {
        const result = await GetPlaybackSubtitle({ video_id: this.videoId, source_version: this.sourceVersion, queue_token: this.queueToken });
        if (request !== this.request) return;
        this.status = result.status; this.cues = result.cues || []; await this.project();
      } catch (error) { if (request === this.request) this.error = String(error); }
    },
    async project() {
      const request = this.request;
      const projection = this._projection = (this._projection || 0) + 1;
      await this.$nextTick();
      if (request !== this.request || projection !== this._projection) return;
      this.release();
      const video = this.videoElement();
      if (this.status !== 'ready' || !video) return;
      this._blob = URL.createObjectURL(new Blob([subtitleVTT(this.cues, this.offset)], { type: 'text/vtt' }));
      const track = document.createElement('track');
      track.kind = 'subtitles'; track.label = '外置字幕'; track.srclang = 'und'; track.src = this._blob;
      track.default = true; track.addEventListener('load', () => { if (this._track === track) this.updateMode(); });
      track.addEventListener('error', () => { if (this._track === track) this.error = '播放器无法加载字幕，请刷新字幕'; });
      this._track = track; video.appendChild(track); this.updateMode();
    },
    updateMode() { if (this._track?.track) this._track.track.mode = this.enabled ? 'showing' : 'disabled'; }
  }
};
</script>
<style scoped>
.playback-subtitles { display: flex; align-items: center; flex-wrap: wrap; gap: var(--space-2); padding: var(--space-2); }
.playback-subtitles input[type=number] { width: 5rem; }
</style>
