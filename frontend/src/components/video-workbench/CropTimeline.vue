<template>
  <div class="crop-time-editor">
    <div class="edit-inline crop-time-tools">
      <button type="button" class="btn btn--secondary btn--sm" :disabled="!canStep || busy" data-test="crop-frame-prev" @click="$emit('step-frame', -1)">上一帧</button>
      <button type="button" class="btn btn--secondary btn--sm" :disabled="!canStep || busy" data-test="crop-frame-next" @click="$emit('step-frame', 1)">下一帧</button>
      <EditTimeField :model-value="atMs" label="当前时间" :disabled="!durationMs || busy" data-test="crop-current-time" @commit="seek" />
      <label>时间轴缩放 <select class="select-input edit-select" :value="zoom" data-test="crop-time-zoom" @change="setZoom(Number($event.target.value))"><option v-for="value in [1, 2, 5, 10, 50]" :key="value" :value="value">{{ value }}×</option></select></label>
    </div>
    <p v-if="error" role="alert" class="edit-error">{{ error }}</p>
    <p class="help-text">拖动两端把手选择保留片段；中间游标只浏览画面，不改变选区。</p>
    <div ref="strip" class="crop-filmstrip" data-test="crop-time-strip" @pointermove="move" @pointerup="end" @pointercancel="cancel" @lostpointercapture="cancel">
      <div class="crop-filmstrip-images" aria-hidden="true"><img v-for="(ms, index) in samples" :key="index" :src="frame(ms)" alt="" draggable="false" /></div>
      <input class="crop-playhead" aria-label="浏览时间点" type="range" :min="windowStart" :max="Math.max(windowStart, windowEnd - 1)" step="1" :value="atMs" data-test="crop-seek" @input="seek(Number($event.target.value))" @change="seek(Number($event.target.value))" />
      <div class="crop-time-selection" :style="selectionStyle" aria-hidden="true"></div>
      <button v-for="side in ['start', 'end']" :key="side" type="button" class="btn-plain crop-time-handle" :class="`crop-time-handle--${side}`" :style="{ left: `${percent(current[side + '_ms'])}%` }" :disabled="!editable || !durationMs" :aria-label="side === 'start' ? '拖动保留片段起点' : '拖动保留片段终点'" :data-test="`crop-time-${side}`" @pointerdown.stop="begin($event, side)" @keydown.left.prevent="nudge(side, -1)" @keydown.right.prevent="nudge(side, 1)">Ⅱ</button>
    </div>
    <div class="crop-time-ticks"><span>{{ time(windowStart) }}</span><span>{{ time(windowEnd) }}</span></div>
    <label v-if="zoom > 1" class="crop-window-pan">平移时间轴<input type="range" aria-label="平移时间轴" :min="0" :max="Math.max(0, durationMs - span)" step="1" :value="windowStart" data-test="crop-time-pan" @input="windowStart = Number($event.target.value)" /></label>
    <div class="edit-inline crop-range-fields">
      <label>入点 <EditTimeField :model-value="current.start_ms" label="保留片段入点" :disabled="!editable" data-test="crop-range-start" @commit="setSide('start', $event)" /></label>
      <label>出点 <EditTimeField :model-value="current.end_ms" label="保留片段出点（不含）" :disabled="!editable" data-test="crop-range-end" @commit="setSide('end', $event)" /></label>
      <button type="button" class="btn btn--secondary btn--sm" :disabled="!editable" data-test="crop-mark-in" @click="setSide('start', atMs)">当前帧设为入点</button>
      <button type="button" class="btn btn--secondary btn--sm" :disabled="!editable || atMs <= current.start_ms" data-test="crop-mark-out" @click="setSide('end', atMs)">当前帧设为出点</button>
      <button type="button" class="btn btn--secondary btn--sm" :disabled="!editable" data-test="crop-range-reset" @click="$emit('range', null)">恢复整片</button>
    </div>
    <p class="help-text" data-test="crop-range-summary">{{ range ? '导出所选片段' : '导出整片' }} · {{ time(current.start_ms) }} → {{ time(current.end_ms) }} · 时长 {{ time(current.end_ms - current.start_ms) }}（出点所在帧不保留）</p>
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
    selectionStyle() { const start = this.percent(this.current.start_ms), end = this.percent(this.current.end_ms); return { left: `${start}%`, width: `${end - start}%` }; }
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
.crop-time-editor { display: grid; gap: var(--space-2); min-width: 0; }
.crop-time-tools, .crop-range-fields { gap: var(--space-2); }
.crop-filmstrip { position: relative; height: 64px; margin: 0 10px; touch-action: none; user-select: none; }
.crop-filmstrip-images { position: absolute; inset: 5px 0; display: flex; overflow: hidden; background: var(--media-badge-bg); }
.crop-filmstrip-images img { width: calc(100% / 7); height: 100%; object-fit: cover; }
.crop-time-selection { position: absolute; inset-block: 1px; border: 3px solid var(--accent-color); box-shadow: 0 0 0 1px var(--hairline); box-sizing: border-box; pointer-events: none; }
.crop-time-handle.crop-time-handle { position: absolute; top: 0; bottom: 0; width: 20px; transform: translateX(-50%); border-radius: 4px; background: var(--accent-color); color: var(--accent-on); cursor: ew-resize; z-index: 2; }
.crop-playhead { position: absolute; inset: 0; width: 100%; height: 64px; margin: 0; background: transparent; cursor: crosshair; appearance: none; }
.crop-playhead::-webkit-slider-runnable-track { background: transparent; height: 64px; }
.crop-playhead::-webkit-slider-thumb { appearance: none; width: 3px; height: 64px; background: var(--media-badge-fg); border: 1px solid var(--media-badge-bg); }
.crop-playhead::-moz-range-track { background: transparent; }
.crop-playhead::-moz-range-thumb { width: 3px; height: 60px; background: var(--media-badge-fg); border: 1px solid var(--media-badge-bg); }
.crop-time-ticks { display: flex; justify-content: space-between; font-size: var(--fs-caption); }
.crop-window-pan { display: flex; gap: var(--space-2); align-items: center; font-size: var(--fs-caption); }
.crop-window-pan input { flex: 1; min-width: 0; }
.crop-range-fields label { display: inline-flex; align-items: center; gap: var(--space-1); }
</style>
