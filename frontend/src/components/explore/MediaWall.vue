<template>
  <section class="media-wall" data-test="media-wall">
    <div ref="viewport" class="media-wall__viewport" role="grid" tabindex="0" :aria-label="`媒体墙，${entries.length}项，缩放${Math.round(camera.scale * 100)}%`" :aria-activedescendant="visibleEntries.some(entry => entry.entry_id === selectedId) ? `wall-entry-${selectedId}` : undefined" @wheel.prevent="wheel" @pointerdown="pointerDown" @pointermove="pointerMove" @pointerup="pointerUp" @pointercancel="pointerUp" @keydown="keyDown">
      <div class="media-wall__plane" :style="planeStyle">
        <MediaTile v-for="entry in visibleEntries" :id="`wall-entry-${entry.entry_id}`" :key="entry.entry_id" :entry="entry" :card="cards[entry.entry_id]" :loader="loader" :retry-signal="retrySignal" :edge="camera.scale * 160 <= 80 ? 96 : 256" :selected="selectedId === entry.entry_id" :show-title="camera.scale >= .8 && selectedId === entry.entry_id" :style="cellStyle(entry.slot)" @loaded="setLoaded" @failed="(id, failed) => $emit('failed', id, failed)" @open="open" />
      </div>
    </div>
    <div class="media-wall__controls">
      <span class="media-wall__hint">拖动浏览 · 捏合缩放 · 点击放大</span>
      <div class="media-wall__zoom">
        <button type="button" class="btn btn--ghost btn--sm" aria-label="缩小" @click="zoom(camera.scale / 1.25)">−</button>
        <input type="range" min="0.2" max="2" step="0.01" :value="camera.scale" aria-label="媒体墙缩放比例" data-test="media-wall-scale" @input="zoom(Number($event.target.value))" />
        <button type="button" class="btn btn--ghost btn--sm" aria-label="放大" @click="zoom(camera.scale * 1.25)">＋</button>
        <span>{{ Math.round(camera.scale * 100) }}%</span>
        <button type="button" class="btn btn--secondary btn--sm" @click="fit">适应窗口</button>
      </div>
    </div>
  </section>
</template>

<script>
import { mediaVisibleClip, mediaVisibleFraction, mediaUnobscured } from '../../utils/mediaExposure.js';
import MediaTile from './MediaTile.vue';
import { clampWallView, fitWall, revealWallSlot, stepWallSelection, wallBounds, wallCellRect, wallVisibleSlots, zoomWallAt } from '../../utils/mediaWallGeometry.js';

export default {
  name: 'MediaWall', components: { MediaTile },
  props: { entries: { type: Array, default: () => [] }, cards: { type: Object, default: () => ({}) }, loader: { type: Object, required: true }, view: { type: Object, required: true }, selectedId: { type: Number, default: 0 }, retrySignal: { type: Number, default: 0 } },
  emits: ['view', 'visible', 'exposure', 'open', 'select', 'failed'],
  data() { return { camera: { scale: this.view.scale, x: this.view.x, y: this.view.y }, viewport: { width: 0, height: 0 }, loaded: {}, drag: null, suppressClick: false }; },
  computed: {
    visibleEntries() { return wallVisibleSlots(this.entries.length, this.camera, this.viewport).map(slot => this.entries[slot]).filter(Boolean); },
    planeStyle() { const bounds = wallBounds(this.entries.length); return { width: `${bounds.width}px`, height: `${bounds.height}px`, transform: `translate(${this.camera.x}px, ${this.camera.y}px) scale(${this.camera.scale})` }; }
  },
  watch: {
    view: { deep: true, handler(value) { this.camera = clampWallView(value, this.entries.length, this.viewport); } },
    visibleEntries: { immediate: true, handler(entries) { this.$emit('visible', entries.map(e => e.entry_id)); } }
  },
  mounted() {
    this._resize = new ResizeObserver(() => this.measure()); this._resize.observe(this.$refs.viewport); this.measure();
    this._exposure = setInterval(() => this.reportExposure(), 100);
    this._gestureStart = event => { event.preventDefault(); this._gestureScale = this.camera.scale; };
    this._gestureChange = event => { event.preventDefault(); const rect = this.$refs.viewport.getBoundingClientRect(); this.zoom(this._gestureScale * event.scale, { x: event.clientX - rect.left || rect.width / 2, y: event.clientY - rect.top || rect.height / 2 }); };
    this.$refs.viewport.addEventListener('gesturestart', this._gestureStart, { passive: false });
    this.$refs.viewport.addEventListener('gesturechange', this._gestureChange, { passive: false });
  },
  beforeUnmount() { this._resize?.disconnect(); clearInterval(this._exposure); cancelAnimationFrame(this._frame); this.$refs.viewport?.removeEventListener('gesturestart', this._gestureStart); this.$refs.viewport?.removeEventListener('gesturechange', this._gestureChange); },
  methods: {
    cellStyle(slot) { return { position: 'absolute', left: `${slot % 32 * 162}px`, top: `${Math.floor(slot / 32) * 162}px`, width: '160px', height: '160px' }; },
    measure() { const rect = this.$refs.viewport.getBoundingClientRect(); this.viewport = { width: rect.width, height: rect.height }; this.setCamera(clampWallView(this.camera, this.entries.length, this.viewport)); },
    setCamera(value) { this.camera = value; cancelAnimationFrame(this._frame); this._frame = requestAnimationFrame(() => this.$emit('view', { ...this.camera })); },
    zoom(scale, point = { x: this.viewport.width / 2, y: this.viewport.height / 2 }) { this.setCamera(zoomWallAt(this.camera, scale, point, this.entries.length, this.viewport)); },
    fit() { this.setCamera(fitWall(this.entries.length, this.viewport)); },
    wheel(event) { if (event.ctrlKey) { const rect = this.$refs.viewport.getBoundingClientRect(); this.zoom(this.camera.scale * Math.exp(-event.deltaY * .008), { x: event.clientX - rect.left, y: event.clientY - rect.top }); } else this.setCamera(clampWallView({ ...this.camera, x: this.camera.x - (event.shiftKey ? event.deltaY : event.deltaX), y: this.camera.y - (event.shiftKey ? 0 : event.deltaY) }, this.entries.length, this.viewport)); },
    pointerDown(event) { if (event.button !== 0) return; this.drag = { id: event.pointerId, x: event.clientX, y: event.clientY, camera: { ...this.camera }, moved: false }; },
    pointerMove(event) { const d = this.drag; if (!d || d.id !== event.pointerId) return; const dx = event.clientX - d.x, dy = event.clientY - d.y; if (Math.hypot(dx, dy) > 5) { d.moved = true; this.$refs.viewport.setPointerCapture?.(event.pointerId); } if (d.moved) { event.preventDefault(); this.setCamera(clampWallView({ ...d.camera, x: d.camera.x + dx, y: d.camera.y + dy }, this.entries.length, this.viewport)); } },
    pointerUp(event) { if (!this.drag) return; this.suppressClick = this.drag.moved; this.drag = null; if (this.$refs.viewport.hasPointerCapture?.(event.pointerId)) this.$refs.viewport.releasePointerCapture(event.pointerId); setTimeout(() => { this.suppressClick = false; }, 0); },
    open(entry) { if (this.suppressClick) return; this.$emit('select', entry.entry_id); this.$emit('open', entry); },
    setLoaded(id, loaded) { this.loaded[id] = loaded; },
    reportExposure() {
      const viewport = this.$refs.viewport; if (!viewport) return;
      const clip = mediaVisibleClip(viewport), offset = viewport.getBoundingClientRect();
      this.$emit('exposure', this.visibleEntries.map(entry => {
        const cell = wallCellRect(entry.slot, this.camera), rect = { left: cell.x + offset.left, top: cell.y + offset.top, right: cell.x + offset.left + cell.width, bottom: cell.y + offset.top + cell.height, width: cell.width, height: cell.height };
        const fraction = mediaVisibleFraction(rect, clip), loaded = !!this.loaded[entry.entry_id];
        const unobscured = loaded && !entry.presented && fraction >= .5 ? mediaUnobscured(document.getElementById(`wall-entry-${entry.entry_id}`), clip) : false;
        return { id: entry.entry_id, loaded, fraction, unobscured, presented: entry.presented };
      }));
    },
    keyDown(event) {
      if (event.key === '+' || event.key === '=') { event.preventDefault(); this.zoom(this.camera.scale * 1.25); return; }
      if (event.key === '-') { event.preventDefault(); this.zoom(this.camera.scale / 1.25); return; }
      const selected = this.entries.findIndex(e => e.entry_id === this.selectedId);
      if (event.key === 'Enter') { event.preventDefault(); const entry = this.entries[Math.max(0, selected)]; if (entry) this.open(entry); return; }
      if (!event.key.startsWith('Arrow')) return;
      event.preventDefault(); const index = stepWallSelection(Math.max(0, selected), event.key, this.entries.length), entry = this.entries[index];
      if (entry) { this.$emit('select', entry.entry_id); this.setCamera(revealWallSlot(index, this.camera, this.entries.length, this.viewport)); }
    },
    focus() { this.$refs.viewport?.focus({ preventScroll: true }); }
  }
};
</script>

<style scoped>
.media-wall { position: relative; }
.media-wall__viewport { position: relative; height: min(70vh, 820px); min-height: 300px; overflow: hidden; background: var(--player-bg); touch-action: none; user-select: none; cursor: grab; border-radius: var(--radius-lg); }
.media-wall__viewport:active { cursor: grabbing; }
.media-wall__plane { position: absolute; top: 0; left: 0; transform-origin: 0 0; }
.media-wall__controls { display: flex; align-items: center; justify-content: space-between; gap: 12px; flex-wrap: wrap; padding-block: 12px; }
.media-wall__hint { font-size: var(--fs-caption); color: var(--text-secondary); }
.media-wall__zoom { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.media-wall__zoom input { width: clamp(100px, 16vw, 200px); accent-color: var(--accent-color); }
.media-wall__zoom span { min-width: 42px; font-variant-numeric: tabular-nums; font-size: var(--fs-caption); }
</style>
