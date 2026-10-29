<template>
  <div class="playback-controls" data-test="playback-controls">
    <component :is="'media-control-bar'" class="playback-controls__timeline">
      <component :is="'media-time-range'" class="playback-controls__seek" aria-label="播放进度" data-test="playback-seek" @pointermove="$emit('seek-hover', $event)" @pointerleave="$emit('seek-leave')">
        <span v-if="$slots['seek-preview']" slot="preview"><slot name="seek-preview" /></span>
      </component>
    </component>
    <component :is="'media-control-bar'" class="playback-controls__row">
      <component :is="'media-play-button'" class="playback-controls__play" data-test="playback-toggle" />
      <component :is="'media-time-display'" showduration class="playback-controls__time" />
      <div class="playback-controls__actions">
        <button ref="volumeButton" type="button" class="btn-icon playback-controls__button" :aria-label="`音量设置${muted || volume === 0 ? '（静音）' : ''}`" :aria-expanded="volumeOpen" data-test="playback-volume-toggle" @click="volumeOpen = !volumeOpen; menuOpen = false"><AppIcon :name="muted || volume === 0 ? 'volume-off' : 'volume'" :size="20" /></button>
        <slot name="settings" />
        <button ref="fullscreenButton" type="button" class="btn-icon playback-controls__button" data-test="inline-player-fullscreen" :aria-label="activeFullscreen ? '退出全屏' : '全屏'" :title="activeFullscreen ? '退出全屏' : '全屏'" :aria-pressed="activeFullscreen" :aria-expanded="menuOpen" @click="fullscreenClick"><AppIcon name="fullscreen" :size="20" /></button>
      </div>
    </component>
    <p v-if="error" class="playback-controls__error" role="alert">{{ error }}</p>
    <BasePopover v-if="volumeOpen" :anchor="$refs.volumeButton" :teleport="false" align="end" placement="top" panel-class="surface-cinema" @close="volumeOpen = false">
      <div class="playback-controls__volume-panel" data-test="playback-volume-panel">
        <div class="playback-controls__volume-label"><span>音量</span><span>{{ muted ? 0 : Math.round(volume * 100) }}%</span></div>
        <component :is="'media-volume-range'" ref="volumeRange" class="playback-controls__volume" aria-label="音量" data-test="playback-volume" />
        <button type="button" class="btn btn--ghost btn--sm" data-test="playback-mute" @click="toggleMuted">{{ muted ? '取消静音' : '静音' }}</button>
      </div>
    </BasePopover>
    <BasePopover v-if="menuOpen" :anchor="$refs.fullscreenButton" :teleport="false" align="end" placement="top" panel-class="surface-cinema" @close="menuOpen = false">
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
import './mediaChrome.js';
const EVENTS = ['volumechange'];
export default {
  name: 'PlaybackControls', components: { AppIcon, BasePopover },
  props: { videoElement: { type: Function, required: true }, dialogElement: { type: Function, required: true }, surfaceElement: { type: Function, required: true } },
  emits: ['seek-hover', 'seek-leave'],
  data: () => ({ volume: 1, muted: false, appFullscreen: false, systemFullscreen: false, menuOpen: false, volumeOpen: false, settingsOpen: false, error: '' }),
  computed: { activeFullscreen() { return this.appFullscreen || this.systemFullscreen; } },
  watch: {
    volumeOpen() { this.holdControls(); this.$nextTick(() => { this.$refs.volumeRange?.setAttribute('mediacontroller', this._surface?.id || ''); }); },
    menuOpen() { this.holdControls(); }
  },
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
      if (height > 0) this._surface?.dispatchEvent(new Event('playback-controls-resize'));
    };
    measure();
    if (typeof ResizeObserver !== 'undefined') { this._resize = new ResizeObserver(measure); this._resize.observe(this.$el); }
  },
  beforeUnmount() {
    this._disposed = true; this._resize?.disconnect();
    for (const event of EVENTS) this._video?.removeEventListener(event, this.sync);
    this._dialog?.removeEventListener('cancel', this.cancelDialog); this._dialog?.removeEventListener('close', this.dialogClosed);
    document.removeEventListener('fullscreenchange', this.syncFullscreen); document.removeEventListener('webkitfullscreenchange', this.syncFullscreen);
    window.removeEventListener('keydown', this.fullscreenKey, true);
    this.leaveFullscreen(); closeModalLayer(this);
  },
  methods: {
    holdControls(settingsOpen = this.settingsOpen) {
      this.settingsOpen = settingsOpen;
      const open = this.volumeOpen || this.menuOpen || this.settingsOpen;
      this._surface?.setAttribute('autohide', open ? '-1' : '2');
      if (open) this._surface?.removeAttribute('userinactive');
    },
    sync() {
      const video = this._video; if (!video) return;
      this.muted = video.muted; this.volume = video.volume;
    },
    async togglePlayback() {
      const video = this._video; if (!video) return;
      this.error = '';
      try { if (video.paused) { if (video.ended) video.currentTime = 0; await video.play(); } else video.pause(); this.sync(); this._surface?.focus({ preventScroll: true }); }
      catch (error) { if (error?.name !== 'AbortError') this.error = '播放没有开始，请重试。'; }
    },
    toggleMuted() { if (this._video) { this._video.muted = !this._video.muted; this.sync(); } },
    ownsSystemFullscreen() { const element = document.fullscreenElement || document.webkitFullscreenElement; return !!element && element === this._surface; },
    syncFullscreen() {
      if (this._disposed) return;
      this.systemFullscreen = this.ownsSystemFullscreen();
      if (this.activeFullscreen) { if (!this._layerOpen) { openModalLayer(this); this._layerOpen = true; } }
      else if (this._layerOpen) { closeModalLayer(this); this._layerOpen = false; }
    },
    fullscreenClick() { this.volumeOpen = false; if (this.activeFullscreen) return this.leaveFullscreen(); this.menuOpen = !this.menuOpen; },
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
      if (event.target?.closest?.('.base-popover')) return;
      if (event.key !== 'Escape' || !this.activeFullscreen || !isTopModalLayer(this)) return;
      event.preventDefault(); event.stopImmediatePropagation(); this.leaveFullscreen();
    }
  }
};
</script>
<style scoped>
.playback-controls { width: 100%; box-sizing: border-box; padding: var(--space-4) var(--space-3) var(--space-2); background: linear-gradient(transparent, var(--overlay-strong)); color: var(--media-badge-fg); line-height: normal; }
.playback-controls__timeline, .playback-controls__row { display: flex; width: 100%; align-items: center; }
.playback-controls__seek { width: 100%; min-width: 0; --media-range-padding: 8px 0; }
.playback-controls__play { flex: none; }
.playback-controls__time { font-size: var(--fs-caption); font-variant-numeric: tabular-nums; white-space: nowrap; --media-control-padding: 0 var(--space-2); }
.playback-controls__actions { display: flex; align-items: center; gap: var(--space-1); margin-left: auto; }
.playback-controls__volume-panel { display: grid; gap: var(--space-2); width: 170px; padding: var(--space-3); }
.playback-controls__volume-label { display: flex; justify-content: space-between; font-size: var(--fs-caption); }
.playback-controls__volume { width: 100%; --media-range-padding: 8px 0; }
.playback-controls__menu { display: grid; gap: var(--space-1); padding: var(--space-2); }
.playback-controls__error { margin: 0; font-size: var(--fs-caption); }
</style>
