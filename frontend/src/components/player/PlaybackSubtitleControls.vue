<template>
  <div class="playback-subtitles" data-test="playback-subtitles">
    <button ref="settingsButton" type="button" class="btn-icon playback-controls__button" aria-label="字幕设置" title="字幕设置" :aria-expanded="menuOpen" data-test="playback-subtitle-toggle" @click="menuOpen = !menuOpen"><AppIcon name="subtitles" :size="20" /></button>
    <BasePopover v-if="menuOpen" :anchor="$refs.settingsButton" :teleport="false" align="end" placement="top" panel-class="surface-cinema" @close="menuOpen = false">
      <div class="playback-subtitles__panel">
        <label class="playback-subtitles__setting"><span>显示字幕</span><input v-model="enabled" type="checkbox" :disabled="status !== 'ready'"></label>
        <label v-if="status === 'ready'" class="playback-subtitles__setting"><span>偏移（秒）</span><input v-model.number="offset" class="number-input" type="number" min="-60" max="60" step="0.1" aria-label="字幕偏移（秒）"></label>
        <p v-if="status === 'absent'" role="status">无外置字幕</p>
        <p v-else-if="!status && !error" role="status">正在读取字幕…</p>
        <p v-if="error" role="alert">字幕不可用：{{ error }}</p>
        <button type="button" class="btn btn--ghost btn--sm" @click="reload"><AppIcon name="refresh" /> 刷新字幕</button>
      </div>
    </BasePopover>
  </div>
</template>
<script>
import { GetPlaybackSubtitle } from '../../../wailsjs/go/main/App';
import AppIcon from '../ui/AppIcon.vue';
import BasePopover from '../ui/BasePopover.vue';
import { subtitleVTT } from '../../utils/playbackSubtitles.js';
export default {
  name: 'PlaybackSubtitleControls', components: { AppIcon, BasePopover },
  emits: ['menu-change'],
  props: { videoId: Number, sourceVersion: String, queueToken: { type: String, default: '' }, videoElement: { type: Function, required: true } },
  data: () => ({ menuOpen: false, enabled: true, offset: 0, status: '', cues: [], error: '', request: 0 }),
  watch: {
    menuOpen(value) { this.$emit('menu-change', value); },
    identity: { immediate: true, handler() { this.offset = 0; this.enabled = true; this.reload(); } },
    offset() { this.project(); }, enabled() { this.updateMode(); }
  },
  computed: { identity() { return `${this.videoId}|${this.sourceVersion}|${this.queueToken}`; } },
  mounted() {
    if (window.runtime?.EventsOn) this._offSubtitle = window.runtime.EventsOn('subtitle-changed', () => this.reload());
  },
  beforeUnmount() { ++this.request; this._offSubtitle?.(); this.$emit('menu-change', false); this.release(); },
  methods: {
    release() { this._resize?.disconnect(); this._visibility?.disconnect(); this._surface?.removeEventListener('playback-controls-resize', this.positionCues); this._surface = null; this._track?.remove(); this._track = null; if (this._blob) URL.revokeObjectURL(this._blob); this._blob = null; },
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
      this._surface = video.parentElement;
      if (typeof ResizeObserver !== 'undefined') { this._resize = new ResizeObserver(this.positionCues); this._resize.observe(video); }
      if (this._surface) {
        this._surface.addEventListener('playback-controls-resize', this.positionCues);
        this._visibility = new MutationObserver(this.positionCues);
        this._visibility.observe(this._surface, { attributes: true, attributeFilter: ['userinactive', 'mediapaused'] });
      }
    },
    updateMode() { if (this._track?.track) { this._track.track.mode = this.enabled ? 'showing' : 'disabled'; this.positionCues(); } },
    positionCues() {
      const video = this.videoElement(); const surface = video?.parentElement;
      const height = video?.getBoundingClientRect().height;
      if (!height || !surface) return;
      const hidden = surface.hasAttribute('userinactive') && !video.paused;
      const barHeight = hidden ? 0 : (surface.querySelector('[data-test="playback-controls"]')?.getBoundingClientRect().height || 0);
      for (const cue of this._track?.track?.cues || []) {
        cue.snapToLines = false; cue.lineAlign = 'end';
        cue.line = Math.max(10, Math.min(95, (height - barHeight - 12) / height * 100));
      }
    }
  }
};
</script>
<style scoped>
.playback-subtitles { display: flex; }
.playback-subtitles__panel { display: grid; gap: var(--space-3); width: 220px; max-width: calc(100vw - 40px); padding: var(--space-3); font-size: var(--fs-caption); line-height: 1.5; }
.playback-subtitles__setting { display: flex; align-items: center; justify-content: space-between; gap: var(--space-3); }
.playback-subtitles input[type=number] { width: 5rem; }
.playback-subtitles__panel p { margin: 0; }
</style>
