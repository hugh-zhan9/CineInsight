<template>
  <div class="crop-time-editor">
    <div class="crop-time-tools">
      <div class="crop-frame-tools" role="group" aria-label="逐帧浏览">
        <button type="button" class="btn btn--secondary btn--sm" :disabled="!canStep || busy" data-test="crop-frame-prev" title="上一帧 (,)" @click="$emit('step-frame', -1)">‹ 上一帧</button>
        <EditTimeField :model-value="atMs" label="当前时间" :disabled="!durationMs || busy" data-test="crop-current-time" @commit="seek" />
        <button type="button" class="btn btn--secondary btn--sm" :disabled="!canStep || busy" data-test="crop-frame-next" title="下一帧 (.)" @click="$emit('step-frame', 1)">下一帧 ›</button>
      </div>
      <div class="crop-mark-tools" role="group" aria-label="标记保留片段">
        <button type="button" class="btn btn--secondary btn--sm" :disabled="!editable" data-test="crop-mark-in" title="当前帧设为入点 ([)" @click="setSide('start', atMs)">设为入点</button>
        <button type="button" class="btn btn--secondary btn--sm" :disabled="!editable || atMs <= current.start_ms" data-test="crop-mark-out" title="当前帧设为出点 (])" @click="setSide('end', atMs)">设为出点</button>
      </div>
      <label class="crop-time-zoom">时间轴缩放
        <select class="select-input" :value="zoom" data-test="crop-time-zoom" @change="setZoom(Number($event.target.value))">
          <option v-for="value in [1, 2, 5, 10, 50]" :key="value" :value="value">{{ value }}×</option>
        </select>
      </label>
    </div>
    <p v-if="error" role="alert" class="edit-error">{{ error }}</p>
    <div ref="strip" class="crop-filmstrip" data-test="crop-time-strip" @pointermove="move" @pointerup="end" @pointercancel="cancel" @lostpointercapture="cancel">
      <div class="crop-filmstrip-images" aria-hidden="true"><img v-for="(ms, index) in samples" :key="index" :src="frame(ms)" alt="" draggable="false" /></div>
      <div class="crop-time-mask crop-time-mask--left" :style="{ width: selectionStyle.left }" aria-hidden="true"></div>
      <div class="crop-time-mask crop-time-mask--right" :style="{ width: selectionStyle.right }" aria-hidden="true"></div>
      <div class="crop-time-selection" :style="selectionStyle" aria-hidden="true"></div>
      <input class="crop-seek" aria-label="浏览时间点" title="浏览画面，不改变选区" type="range" :min="windowStart" :max="Math.max(windowStart, windowEnd - 1)" step="1" :value="atMs" data-test="crop-seek" @input="seek(Number($event.target.value))" @change="seek(Number($event.target.value))" />
      <div v-if="atMs >= windowStart && atMs < windowEnd" class="crop-playhead" :style="{ left: `${percent(atMs)}%` }" aria-hidden="true"></div>
      <button v-for="side in ['start', 'end']" :key="side" type="button" class="btn-plain crop-time-handle" :class="`crop-time-handle--${side}`" :style="{ left: `${percent(current[side + '_ms'])}%` }" :disabled="!editable || !durationMs" :aria-label="side === 'start' ? '拖动保留片段起点' : '拖动保留片段终点'" :data-test="`crop-time-${side}`" @pointerdown.stop="begin($event, side)" @keydown.left.prevent="nudge(side, -1)" @keydown.right.prevent="nudge(side, 1)"></button>
    </div>
    <div class="crop-time-ticks"><span>{{ time(windowStart) }}</span><span>{{ time(windowEnd) }}</span></div>
    <label v-if="zoom > 1" class="crop-window-pan">平移时间轴<input type="range" aria-label="平移时间轴" :min="0" :max="Math.max(0, durationMs - span)" step="1" :value="windowStart" data-test="crop-time-pan" @input="windowStart = Number($event.target.value)" /></label>
    <div class="crop-range-fields">
      <label>入点 <EditTimeField :model-value="current.start_ms" label="保留片段入点" :disabled="!editable" data-test="crop-range-start" @commit="setSide('start', $event)" /></label>
      <label>出点 <EditTimeField :model-value="current.end_ms" label="保留片段出点（不含）" :disabled="!editable" data-test="crop-range-end" @commit="setSide('end', $event)" /></label>
      <button type="button" class="btn btn--secondary btn--sm" :disabled="!editable" data-test="crop-range-reset" @click="$emit('range', null)">恢复整片</button>
      <p class="crop-range-summary" data-test="crop-range-summary" title="出点所在帧不保留"><strong>{{ range ? '导出所选片段' : '导出整片' }}</strong> · {{ time(current.start_ms) }} → {{ time(current.end_ms) }} · 时长 {{ time(current.end_ms - current.start_ms) }}</p>
    </div>
    <p v-if="rangeError" role="alert" class="edit-error">{{ rangeError }}</p>
  </div>
</template>
<script>
import EditTimeField from './EditTimeField.vue';
import { editFrameURL, formatEditTime } from '../../utils/videoEdit.js';
export default {
  name: 'CropTimeline', components: { EditTimeField },
  props: {
    videoId: { type: Number, required: true }, durationMs: { type: Number, default: 0 },
    atMs: { type: Number, default: 0 }, range: { type: Object, default: null },
    sourceVersion: { type: String, default: '' }, editable: { type: Boolean, default: false },
    busy: { type: Boolean, default: false }, error: { type: String, default: '' }, canStep: { type: Boolean, default: false }
  },
  emits: ['seek', 'range', 'step-frame'],
  data: () => ({ zoom: 1, windowStart: 0, drag: null, rangeError: '' }),
  computed: {
    span() { return Math.max(1, Math.ceil(this.durationMs / this.zoom)); },
    windowEnd() { return Math.min(this.durationMs, this.windowStart + this.span); },
    current() { return this.drag?.range || this.range || { start_ms: 0, end_ms: this.durationMs }; },
    samples() { return this.durationMs ? Array.from({ length: 7 }, (_, i) => Math.min(this.durationMs - 1, this.windowStart + Math.floor(this.span * i / 7))) : []; },
    selectionStyle() { return { left: `${this.percent(this.current.start_ms)}%`, right: `${100 - this.percent(this.current.end_ms)}%` }; }
  },
  watch: {
    videoId() { this.cancel(); this.windowStart = 0; this.zoom = 1; this.rangeError = ''; },
    editable(value) { if (!value) this.cancel(); },
    atMs(value) { if (!this.drag && (value < this.windowStart || value >= this.windowEnd)) this.center(value); }
  },
  methods: {
    time: formatEditTime,
    frame(ms) { return `${editFrameURL(this.videoId, ms, 160)}&v=${encodeURIComponent(this.sourceVersion)}`; },
    percent(ms) { return Math.max(0, Math.min(100, (ms - this.windowStart) / this.span * 100)); },
    seek(ms) { if (this.durationMs > 0) this.$emit('seek', Math.max(0, Math.min(this.durationMs - 1, Math.round(ms)))); },
    center(ms) { this.windowStart = Math.max(0, Math.min(this.durationMs - this.span, Math.round(ms - this.span / 2))); },
    setZoom(value) { this.zoom = value; this.center(this.atMs); },
    setSide(side, ms) {
      if (!this.editable) return;
      const range = { ...this.current, [side + '_ms']: ms };
      if (!Number.isInteger(ms) || range.start_ms < 0 || range.start_ms >= range.end_ms || range.end_ms > this.durationMs) { this.rangeError = '入点须早于出点，且都在视频时长内。'; return; }
      this.rangeError = ''; this.$emit('range', range);
    },
    nudge(side, delta) { this.setSide(side, this.current[side + '_ms'] + delta); },
    begin(event, side) {
      if (!this.editable || !this.durationMs || (event.button !== undefined && event.button !== 0)) return;
      const bounds = this.$refs.strip.getBoundingClientRect(); if (!bounds.width) return;
      event.preventDefault(); this.$refs.strip.setPointerCapture?.(event.pointerId);
      this.drag = { pointer: event.pointerId, side, bounds, window: this.windowStart, span: this.span, range: { ...this.current } };
    },
    move(event) {
      const d = this.drag; if (!d || event.pointerId !== d.pointer) return;
      const value = Math.round(d.window + (event.clientX - d.bounds.left) / d.bounds.width * d.span);
      const key = d.side + '_ms';
      d.range[key] = d.side === 'start' ? Math.max(0, Math.min(d.range.end_ms - 1, value)) : Math.min(this.durationMs, Math.max(d.range.start_ms + 1, value));
      this.seek(Math.min(this.durationMs - 1, d.range[key]));
    },
    end(event) { if (!this.drag || event.pointerId !== this.drag.pointer) return; const range = this.drag.range; this.drag = null; this.rangeError = ''; this.$emit('range', range); },
    cancel() { this.drag = null; }
  }
};
</script>
<style scoped>
.crop-time-editor { display: grid; grid-template-columns: minmax(0, 1fr); gap: var(--space-2); min-width: 0; }
.crop-time-tools, .crop-range-fields, .crop-frame-tools, .crop-mark-tools { display: flex; align-items: center; gap: var(--space-1); }
.crop-time-tools, .crop-range-fields { flex-wrap: wrap; column-gap: var(--space-2); font-size: var(--fs-caption); }
.crop-time-tools, .crop-range-fields label { color: var(--text-secondary); }
.crop-time-zoom, .crop-range-fields label { display: inline-flex; align-items: center; gap: var(--space-1); white-space: nowrap; }
.crop-time-zoom, .crop-range-summary { margin-left: auto; }
.crop-time-zoom .select-input { width: auto; height: var(--h-compact); font-size: var(--fs-caption); }
.crop-time-editor .edit-time.edit-time { width: 100px; height: var(--h-compact); padding-inline: var(--space-2); font: var(--fs-caption) var(--font-mono); }
.crop-frame-tools, .crop-frame-tools .edit-time { min-width: 0; }
.crop-frame-tools .edit-time { text-align: center; }

.crop-filmstrip { position: relative; isolation: isolate; height: var(--space-7); margin: var(--space-1) var(--space-2) 0; border-radius: var(--radius-sm); background: var(--player-bg); touch-action: none; user-select: none; }
.crop-filmstrip-images { position: absolute; inset: 0; display: flex; overflow: hidden; border-radius: inherit; }
.crop-filmstrip-images img { width: calc(100% / 7); height: 100%; object-fit: cover; }
.crop-filmstrip-images img + img { border-left: 1px solid var(--media-badge-bg); }
.crop-time-mask, .crop-time-selection { position: absolute; inset-block: 0; pointer-events: none; }
.crop-time-mask { background: var(--overlay-strong); }
.crop-time-mask--left { left: 0; border-radius: var(--radius-sm) 0 0 var(--radius-sm); }
.crop-time-mask--right { right: 0; border-radius: 0 var(--radius-sm) var(--radius-sm) 0; }
.crop-time-selection { border-block: 2px solid var(--accent-color); }
.crop-seek { position: absolute; inset: 0; width: 100%; height: 100%; margin: 0; background: transparent; cursor: crosshair; appearance: none; }
.crop-seek::-webkit-slider-runnable-track { height: var(--space-7); background: transparent; }
.crop-seek::-webkit-slider-thumb { appearance: none; width: 2px; height: var(--space-7); background: transparent; border: 0; }
.crop-seek::-moz-range-track { background: transparent; }
.crop-seek::-moz-range-thumb { width: 2px; background: transparent; border: 0; }
.crop-seek:focus-visible { outline: 2px solid var(--accent-color); outline-offset: 2px; }
.crop-playhead { position: absolute; inset-block: 0; z-index: 2; width: 2px; transform: translateX(-50%); background: var(--media-badge-fg); box-shadow: 0 0 0 1px var(--media-badge-bg); pointer-events: none; }
.crop-playhead::before { content: ''; position: absolute; top: -4px; left: 50%; transform: translateX(-50%); border-top: var(--space-2) solid var(--media-badge-fg); border-inline: var(--space-1) solid transparent; }
.crop-time-handle { position: absolute; inset-block: 0; display: grid; place-items: center; width: 16px; transform: translateX(-50%); border-radius: var(--radius-sm); background: var(--accent-color); color: var(--accent-on); cursor: ew-resize; }
.crop-time-handle::before { content: ''; width: 6px; height: var(--space-4); border-inline: 1px solid currentColor; }
.crop-time-handle:hover:not(:disabled) { background: var(--accent-hover); }
.crop-time-handle:disabled { cursor: default; }
.crop-time-handle:focus-visible { z-index: 1; }
.crop-time-ticks { display: flex; justify-content: space-between; margin-inline: var(--space-2); color: var(--text-secondary); font: var(--fs-caption) var(--font-mono); }
.crop-window-pan { display: flex; gap: var(--space-2); align-items: center; color: var(--text-secondary); font-size: var(--fs-caption); }
.crop-window-pan input { flex: 1; min-width: 0; accent-color: var(--accent-color); }
.crop-range-summary { padding: var(--space-1) var(--space-2); border-radius: var(--radius-pill); background: var(--panel-muted-bg); color: var(--text-secondary); font: var(--fs-caption) var(--font-mono); }
.crop-range-summary strong { color: var(--text-primary); font-weight: var(--fw-semibold); }
</style>
