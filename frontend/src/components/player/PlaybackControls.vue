<template>
  <div class="playback-controls" data-test="playback-controls">
    <input class="playback-controls__seek" type="range" min="0" :max="duration || 0" step="0.1" :value="position" :disabled="!duration" aria-label="播放进度" data-test="playback-seek" @keydown="seekKey" @input="seek" @pointermove="$emit('seek-hover', $event)" @pointerleave="$emit('seek-leave')" />
    <div class="playback-controls__row">
      <button type="button" class="btn-icon btn--sm" :aria-label="paused ? '播放' : '暂停'" data-test="playback-toggle" @click="togglePlayback"><AppIcon :name="paused ? 'play' : 'pause'" /></button>
      <time class="playback-controls__time">{{ time(position) }} / {{ duration ? time(duration) : '--:--' }}</time>
      <button type="button" class="btn-icon btn--sm" :aria-label="muted ? '取消静音' : '静音'" @click="toggleMuted"><AppIcon :name="muted || volume === 0 ? 'volume-off' : 'volume'" /></button>
      <input class="playback-controls__volume" type="range" min="0" max="1" step="0.05" :value="muted ? 0 : volume" aria-label="音量" data-test="playback-volume" @input="setVolume" />
      <button ref="fullscreenButton" type="button" class="btn btn--secondary btn--sm playback-controls__fullscreen" data-test="inline-player-fullscreen" :aria-pressed="activeFullscreen" :aria-expanded="menuOpen" @click="fullscreenClick"><AppIcon name="fullscreen" /> {{ activeFullscreen ? '退出全屏' : '全屏' }}</button>
    </div>
    <p v-if="error" class="playback-controls__error" role="alert">{{ error }}</p>
    <BasePopover v-if="menuOpen" :anchor="$refs.fullscreenButton" align="end" @close="menuOpen = false">
      <div class="playback-controls__menu">
        <button type="button" class="btn btn--ghost" data-test="player-app-fullscreen" @click="enterAppFullscreen">应用内全屏</button>
        <button type="button" class="btn btn--ghost" data-test="player-system-fullscreen" @click="enterSystemFullscreen">系统全屏</button>
      </div>
    </BasePopover>
  </div>
</template>
<script>
import AppIcon from '../ui/AppIcon.vue';
import BasePopover from '../ui/BasePopover.vue';
import { closeModalLayer, isTopModalLayer, openModalLayer } from '../ui/modalSignals.js';
import { handlePlaybackKey } from '../../utils/playbackKeyboard.js';
import { formatDuration } from '../../utils/mediaDetails.js';
const EVENTS = ['loadedmetadata', 'durationchange', 'timeupdate', 'play', 'pause', 'ended', 'volumechange'];
export default {
  name: 'PlaybackControls', components: { AppIcon, BasePopover },
  props: { videoElement: { type: Function, required: true }, dialogElement: { type: Function, required: true }, surfaceElement: { type: Function, required: true } },
  emits: ['seek-hover', 'seek-leave'],
  data: () => ({ paused: true, duration: 0, position: 0, volume: 1, muted: false, appFullscreen: false, systemFullscreen: false, menuOpen: false, error: '' }),
  computed: { activeFullscreen() { return this.appFullscreen || this.systemFullscreen; } },
  async mounted() {
    await this.$nextTick();
    if (this._disposed) return;
    this._video = this.videoElement(); this._dialog = this.dialogElement(); this._surface = this.surfaceElement();
    for (const event of EVENTS) this._video?.addEventListener(event, this.sync);
    this._dialog?.addEventListener('cancel', this.cancelDialog);
    this._dialog?.addEventListener('close', this.dialogClosed);
    document.addEventListener('fullscreenchange', this.syncFullscreen);
    document.addEventListener('webkitfullscreenchange', this.syncFullscreen);
    window.addEventListener('keydown', this.fullscreenKey, true);
    this.sync(); this.syncFullscreen();
    const measure = () => {
      const height = this.$el.getBoundingClientRect().height;
      if (height > 0) this._surface?.style.setProperty('--playback-controls-height', `${height}px`);
    };
    measure();
    if (typeof ResizeObserver !== 'undefined') { this._resize = new ResizeObserver(measure); this._resize.observe(this.$el); }
  },
  beforeUnmount() {
    this._disposed = true; this._resize?.disconnect();
    this._surface?.style.removeProperty('--playback-controls-height');
    for (const event of EVENTS) this._video?.removeEventListener(event, this.sync);
    this._dialog?.removeEventListener('cancel', this.cancelDialog); this._dialog?.removeEventListener('close', this.dialogClosed);
    document.removeEventListener('fullscreenchange', this.syncFullscreen); document.removeEventListener('webkitfullscreenchange', this.syncFullscreen);
    window.removeEventListener('keydown', this.fullscreenKey, true);
    this.leaveFullscreen(); closeModalLayer(this);
  },
  methods: {
    time(value) { return formatDuration(value) || '00:00'; },
    sync() {
      const video = this._video; if (!video) return;
      this.paused = video.paused; this.muted = video.muted; this.volume = video.volume;
      this.duration = Number.isFinite(video.duration) && video.duration > 0 ? video.duration : 0;
      this.position = Number.isFinite(video.currentTime) ? Math.max(0, video.currentTime) : 0;
    },
    async togglePlayback() {
      const video = this._video; if (!video) return;
      this.error = '';
      try { if (video.paused) { if (video.ended) video.currentTime = 0; await video.play(); } else video.pause(); this.sync(); this._surface?.focus({ preventScroll: true }); }
      catch (error) { if (error?.name !== 'AbortError') this.error = '播放没有开始，请重试。'; }
    },
    seekKey(event) { handlePlaybackKey(event, this._video, { acceptRange: true }); this.sync(); },
    seek(event) { const value = Number(event.target.value); if (!this._video || !this.duration || !Number.isFinite(value)) return; this._video.currentTime = Math.max(0, Math.min(value, this.duration)); this.sync(); },
    setVolume(event) { const value = Number(event.target.value); if (!this._video || !Number.isFinite(value)) return; this._video.volume = Math.max(0, Math.min(1, value)); this._video.muted = false; this.sync(); },
    toggleMuted() { if (this._video) { this._video.muted = !this._video.muted; this.sync(); } },
    ownsSystemFullscreen() { const element = document.fullscreenElement || document.webkitFullscreenElement; return !!element && element === this._surface; },
    syncFullscreen() {
      if (this._disposed) return;
      this.systemFullscreen = this.ownsSystemFullscreen();
      if (this.activeFullscreen) { if (!this._layerOpen) { openModalLayer(this); this._layerOpen = true; } }
      else if (this._layerOpen) { closeModalLayer(this); this._layerOpen = false; }
    },
    fullscreenClick() { if (this.activeFullscreen) return this.leaveFullscreen(); this.menuOpen = !this.menuOpen; },
    enterAppFullscreen() {
      this.menuOpen = false; this.error = '';
      const dialog = this._dialog;
      if (!dialog?.showModal) { this.error = '当前环境不支持应用内全屏。'; return; }
      try {
        dialog.removeAttribute('open'); dialog.showModal();
        this.appFullscreen = true; this.syncFullscreen(); this._surface?.focus({ preventScroll: true });
      } catch { dialog.setAttribute('open', ''); this.error = '无法切换应用内全屏，请重试。'; }
    },
    async enterSystemFullscreen() {
      this.menuOpen = false; this.error = '';
      try {
        if (this._surface?.requestFullscreen) await this._surface.requestFullscreen();
        else if (this._surface?.webkitRequestFullscreen) await this._surface.webkitRequestFullscreen();
        else this.error = '当前环境不支持系统全屏。';
        if (this._disposed) { await this.leaveFullscreen(); return; }
        this.syncFullscreen(); this._surface?.focus({ preventScroll: true });
      } catch { if (!this._disposed) this.error = '无法切换全屏，请重试。'; }
    },
    async leaveFullscreen() {
      this.menuOpen = false;
      if (this.appFullscreen) {
        this.appFullscreen = false; this._dialog?.close(); this._dialog?.setAttribute('open', ''); this.syncFullscreen();
      }
      if (this.ownsSystemFullscreen()) {
        try { if (document.exitFullscreen) await document.exitFullscreen(); else await document.webkitExitFullscreen(); this.syncFullscreen(); }
        catch { this.error = '无法退出全屏，请按 Esc 重试。'; }
      }
    },
    dialogClosed() { if (this.appFullscreen) { this.appFullscreen = false; this._dialog?.setAttribute('open', ''); this.syncFullscreen(); } },
    cancelDialog(event) { if (this.appFullscreen) { event.preventDefault(); this.leaveFullscreen(); } },
    fullscreenKey(event) {
      if (event.key !== 'Escape' || !this.activeFullscreen || !isTopModalLayer(this)) return;
      event.preventDefault(); event.stopImmediatePropagation(); this.leaveFullscreen();
    }
  }
};
</script>
<style scoped>
.playback-controls { position: absolute; right: 0; bottom: 0; left: 0; z-index: var(--z-sticky); display: grid; gap: var(--space-1); padding: var(--space-2); background: var(--player-bg); color: var(--media-badge-fg); }
.playback-controls__seek { width: 100%; min-width: 0; margin: 0; padding: 0; accent-color: var(--accent-color); }
.playback-controls__row { display: flex; align-items: center; gap: var(--space-2); }
.playback-controls__time { font-size: var(--fs-caption); font-variant-numeric: tabular-nums; white-space: nowrap; }
.playback-controls__volume { width: 80px; min-width: 0; accent-color: var(--accent-color); }
.playback-controls__fullscreen { margin-left: auto; white-space: nowrap; }
.playback-controls__menu { display: grid; gap: var(--space-1); padding: var(--space-2); }
.playback-controls__error { margin: 0; font-size: var(--fs-caption); }
</style>
