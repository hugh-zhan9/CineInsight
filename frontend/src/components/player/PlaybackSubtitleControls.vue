<template>
  <div class="playback-subtitles" data-test="playback-subtitles">
    <button ref="settingsButton" type="button" class="btn-icon playback-controls__button" aria-label="字幕设置" title="字幕设置" :aria-expanded="menuOpen" data-test="playback-subtitle-toggle" @click="menuOpen = !menuOpen"><AppIcon name="subtitles" :size="20" /></button>
    <BasePopover v-if="menuOpen" :anchor="$refs.settingsButton" :teleport="false" align="end" placement="top" panel-class="surface-cinema" @close="menuOpen = false">
      <div class="playback-subtitles__panel">
        <label class="playback-subtitles__setting"><span>显示字幕</span><input v-model="enabled" type="checkbox" :disabled="status !== 'ready'"></label>
        <label v-if="status === 'ready'" class="playback-subtitles__setting"><span>偏移（秒）</span><input v-model.number="offset" class="number-input" type="number" min="-60" max="60" step="0.1" aria-label="字幕偏移（秒）"></label>
        <label class="playback-subtitles__setting"><span>字号</span><select class="select-input" v-model="preferences.size" aria-label="字幕字号"><option value="small">小</option><option value="medium">中</option><option value="large">大</option><option value="xlarge">特大</option></select></label>
        <label class="playback-subtitles__setting"><span>背景</span><select class="select-input" v-model="preferences.background" aria-label="字幕背景"><option value="none">无</option><option value="translucent">半透明</option><option value="solid">纯色</option></select></label>
        <label class="playback-subtitles__setting"><span>位置</span><select class="select-input" v-model="preferences.position" aria-label="字幕位置"><option value="bottom">底部</option><option value="middle">居中</option><option value="top">顶部</option></select></label>
        <button v-if="status === 'ready'" type="button" class="btn btn--ghost btn--sm" data-test="playback-transcript-toggle" :aria-pressed="transcriptOpen" @click="transcriptOpen = !transcriptOpen; menuOpen = false">{{ transcriptOpen ? '关闭台词' : '浏览台词' }}</button>
        <p v-if="status === 'absent'" role="status">无外置字幕</p>
        <p v-else-if="!status && !error" role="status">正在读取字幕…</p>
        <p v-if="error" role="alert">字幕不可用：{{ error }}</p>
        <template v-if="status === 'absent' || error">
          <button type="button" class="btn btn--ghost btn--sm" :disabled="toolBusy" data-test="playback-subtitle-edit" @click="openTool('edit')">{{ status === 'absent' ? '新建或提取字幕…' : '在字幕工作台修复…' }}</button>
          <button v-if="status === 'absent'" type="button" class="btn btn--ghost btn--sm" :disabled="toolBusy" @click="openTool('generate')">生成字幕…</button>
        </template>
        <p v-if="preferenceNotice" role="status">{{ preferenceNotice }}</p>
        <button type="button" class="btn btn--ghost btn--sm" @click="reload"><AppIcon name="refresh" /> 刷新字幕</button>
      </div>
    </BasePopover>
    <Teleport v-if="transcriptOpen && surface && status === 'ready'" :to="surface">
      <PlaybackTranscript :rows="transcriptRows" :time-m-s="timeMS" :range="playbackRange" @close="transcriptOpen = false" @seek="seekTranscript" />
    </Teleport>
  </div>
</template>
<script>
import { GetPlaybackSubtitle } from '../../../wailsjs/go/main/App';
import AppIcon from '../ui/AppIcon.vue';
import BasePopover from '../ui/BasePopover.vue';
import PlaybackTranscript from './PlaybackTranscript.vue';
import { requestSubtitleTool } from '../../utils/subtitleNavigation.js';
import { subtitleVTT, projectTranscript, readSubtitlePreferences, writeSubtitlePreferences, SUBTITLE_PREFERENCES_KEY } from '../../utils/playbackSubtitles.js';
export default {
  name: 'PlaybackSubtitleControls', components: { AppIcon, BasePopover, PlaybackTranscript },
  emits: ['menu-change'],
  props: { videoId: Number, sourceVersion: String, queueToken: { type: String, default: '' }, videoElement: { type: Function, required: true }, leaveFullscreen: { type: Function, default: null }, playbackRange: { type: Object, default: null } },
  data: () => ({ menuOpen: false, enabled: true, offset: 0, status: '', cues: [], error: '', request: 0, preferences: readSubtitlePreferences(), preferenceNotice: '', transcriptOpen: false, surface: null, timeMS: 0, toolBusy: false }),
  watch: {
    controlsHeld(value) { this.$emit('menu-change', value); },
    preferences: { deep: true, handler(value) { this.preferenceNotice = writeSubtitlePreferences(value) ? '' : '偏好暂时无法保存，本次播放仍可使用'; this.positionCues(); } },
    identity: { immediate: true, handler() { this.offset = 0; this.enabled = true; this.transcriptOpen = false; this.reload(); } },
    offset() { this.project(); }, enabled() { this.updateMode(); }
  },
  computed: { identity() { return `${this.videoId}|${this.sourceVersion}|${this.queueToken}`; }, controlsHeld() { return this.menuOpen || this.transcriptOpen; }, transcriptRows() { return projectTranscript(this.cues, this.offset); } },
  mounted() {
    if (window.runtime?.EventsOn) this._offSubtitle = window.runtime.EventsOn('subtitle-changed', () => this.reload());
    this._storageChanged = event => { if (event.key === SUBTITLE_PREFERENCES_KEY) this.preferences = readSubtitlePreferences(); };
    window.addEventListener('storage', this._storageChanged);
    this._preferenceChanged = event => { if (JSON.stringify(this.preferences) !== JSON.stringify(event.detail)) this.preferences = event.detail; };
    window.addEventListener('subtitle-display-changed', this._preferenceChanged);
  },
  beforeUnmount() { this._disposed = true; ++this.request; this._offSubtitle?.(); window.removeEventListener('storage', this._storageChanged); window.removeEventListener('subtitle-display-changed', this._preferenceChanged); this.$emit('menu-change', false); this.release(); },
  methods: {
    async openTool(kind) {
      const identity = this.identity; this.toolBusy = true;
      try {
        await this.leaveFullscreen?.();
        if (this._disposed || identity !== this.identity) return;
        if (document.fullscreenElement || document.webkitFullscreenElement || this.videoElement()?.closest('dialog')?.matches(':modal')) {
          this.error = '请先退出全屏，再打开字幕工具'; return;
        }
        this.videoElement()?.pause(); this.menuOpen = false;
        await requestSubtitleTool(this.videoId, kind, () => !this._disposed && identity === this.identity);
      } catch { if (identity === this.identity) this.error = '无法打开字幕工具，请稍后重试'; }
      finally { this.toolBusy = false; }
    },
    release() {
      this._resize?.disconnect(); this._visibility?.disconnect(); this._surface?.removeEventListener('playback-controls-resize', this.positionCues); this._surface = null;
      this._media?.removeEventListener('timeupdate', this.updateTime); this._media?.removeEventListener('seeked', this.updateTime);
      this._media?.style.removeProperty('--fs-subtitle-active'); this._media?.style.removeProperty('--subtitle-background'); this._media = null;
      this._track?.remove(); this._track = null; if (this._blob) URL.revokeObjectURL(this._blob); this._blob = null;
    },
    updateTime() { this.timeMS = Math.round((this._media?.currentTime || 0) * 1000); },
    seekTranscript(seconds) { if (seconds !== null && this._media && this.status === 'ready') { this._media.currentTime = seconds; this.updateTime(); } },
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
      this._media = video; this.surface = video.parentElement;
      video.addEventListener('timeupdate', this.updateTime); video.addEventListener('seeked', this.updateTime); this.updateTime();
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
      const width = video.getBoundingClientRect().width || 0;
      const tokens = getComputedStyle(video);
      const limits = { small: ['--fs-caption', '--fs-title', .025], medium: ['--fs-body', '--fs-display', .03], large: ['--fs-section', '--fs-hero', .04], xlarge: ['--fs-title', '--fs-hero', .05] };
      const [minToken, maxToken, ratio] = limits[this.preferences.size];
      const minimum = parseFloat(tokens.getPropertyValue(minToken)) || 14;
      const maximum = parseFloat(tokens.getPropertyValue(maxToken)) || 40;
      video.style.setProperty('--fs-subtitle-active', `${Math.max(minimum, Math.min(maximum, width * ratio))}px`);
      video.style.setProperty('--subtitle-background', { none: 'transparent', translucent: 'var(--media-badge-bg)', solid: 'var(--player-bg)' }[this.preferences.background]);
      const hidden = surface.hasAttribute('userinactive') && !video.paused;
      const barHeight = hidden ? 0 : (surface.querySelector('[data-test="playback-controls"]')?.getBoundingClientRect().height || 0);
      for (const cue of this._track?.track?.cues || []) {
        cue.snapToLines = false;
        cue.lineAlign = { bottom: 'end', middle: 'center', top: 'start' }[this.preferences.position];
        cue.line = this.preferences.position === 'bottom' ? Math.max(10, Math.min(95, (height - barHeight - 12) / height * 100)) : this.preferences.position === 'middle' ? 50 : 5;
      }
    }
  }
};
</script>
<style>
.playback-surface video { --fs-subtitle-active: var(--fs-body); --subtitle-background: var(--media-badge-bg); }
.playback-surface video::cue { font-size: var(--fs-subtitle-active); background-color: var(--subtitle-background); color: var(--media-badge-fg); }
</style>
<style scoped>
.playback-subtitles { display: flex; }
.playback-subtitles__panel { display: grid; gap: var(--space-3); width: 220px; max-width: calc(100vw - 40px); padding: var(--space-3); font-size: var(--fs-caption); line-height: 1.5; }
.playback-subtitles__setting { display: flex; align-items: center; justify-content: space-between; gap: var(--space-3); }
.playback-subtitles input[type=number] { width: 5rem; }
.playback-subtitles__panel p { margin: 0; }
</style>
