<template>
  <div ref="stage" class="image-viewport" :class="{ 'image-viewport--dragging': drag }"
    tabindex="0" :aria-label="`${name}，滚轮缩放，拖动查看，双击切换原始尺寸`"
    @wheel.prevent="wheel" @dblclick.prevent="toggleNatural" @pointerdown="startDrag"
    @pointermove="moveDrag" @pointerup="finishDrag" @pointercancel="endDrag" @lostpointercapture="endDrag">
    <img v-if="!imageReady && thumbnailSrc && !thumbnailFailed" class="image-viewport__thumbnail" :src="thumbnailSrc" alt="" :style="imageStyle" @error="thumbnailFailed = true" />
    <span v-if="!imageReady" class="image-viewport__loading" role="status">正在加载原图…</span>
    <img :key="src" :src="src" :alt="name" :data-test="imageDataTest || undefined" :style="[imageStyle, { visibility: imageReady ? 'visible' : 'hidden' }]"
      draggable="false" @load="loaded" @error="$emit('error')" />
    <span v-if="showScale" class="image-viewport__scale" aria-live="polite">{{ previewLimited ? '预览 ' : '' }}{{ Math.round(geometry.scale * 100) }}%</span>
    <span v-if="previewLimited" class="image-viewport__notice">当前为 {{ naturalWidth }} × {{ naturalHeight }} 预览，登记源图尺寸 {{ imageWidth }} × {{ imageHeight }}</span>
    <slot />
  </div>
</template>

<script>
import { imageGeometry, initialImageView, naturalImageView, panImage, zoomImageAt } from '../utils/imageViewport.js';

export default {
  name: 'ImageViewport',
  props: {
    thumbnailSrc: { type: String, default: '' },
    src: { type: String, required: true }, name: { type: String, default: '图片' },
    imageWidth: { type: Number, default: 1 }, imageHeight: { type: Number, default: 1 },
    modelValue: { type: Object, default: null }, imageDataTest: { type: String, default: '' },
    showScale: { type: Boolean, default: true }, maxScale: { type: Number, default: 1 }
  },
  emits: ['update:modelValue', 'load', 'error', 'background-click'],
  data: () => ({ imageReady: false, thumbnailFailed: false, width: 1, height: 1, naturalWidth: 0, naturalHeight: 0, localView: initialImageView(), drag: null, temporaryView: null }),
  computed: {
    size() { return { width: this.width, height: this.height, imageWidth: this.naturalWidth || this.imageWidth, imageHeight: this.naturalHeight || this.imageHeight, maxScale: this.maxScale }; },
    view() { return this.modelValue || this.localView; },
    geometry() { return imageGeometry(this.size, this.view); },
    previewLimited() { return this.naturalWidth > 0 && Math.max(this.imageWidth, this.imageHeight) > Math.max(this.naturalWidth, this.naturalHeight) + 1; },
    imageStyle() { const g = this.geometry; return { width: `${g.width}px`, height: `${g.height}px`, transform: `translate(${g.left}px, ${g.top}px)` }; }
  },
  watch: { src() { this.imageReady = false; this.thumbnailFailed = false; this.naturalWidth = 0; this.naturalHeight = 0; this.drag = null; this.temporaryView = null; this.reset(); } },
  mounted() {
    this.measure();
    if (typeof ResizeObserver === 'function') { this._resize = new ResizeObserver(() => this.measure()); this._resize.observe(this.$refs.stage); }
    window.addEventListener('resize', this.measure);
    window.addEventListener('blur', this.restoreTemporary);
  },
  beforeUnmount() { this._resize?.disconnect(); window.removeEventListener('resize', this.measure); window.removeEventListener('blur', this.restoreTemporary); this.drag = null; },
  methods: {
    measure() { const rect = this.$refs.stage?.getBoundingClientRect(); if (rect?.width > 0 && rect?.height > 0) { this.width = rect.width; this.height = rect.height; } },
    loaded(event) { this.imageReady = true; this.naturalWidth = event.target.naturalWidth; this.naturalHeight = event.target.naturalHeight; this.measure(); this.$emit('load', event); },
    publish(view) { this.localView = view; this.$emit('update:modelValue', view); },
    reset() { this.publish(initialImageView()); },
    point(event) { const rect = this.$refs.stage.getBoundingClientRect(); return { x: event.clientX - rect.left, y: event.clientY - rect.top }; },
    wheel(event) { this.restoreTemporary(); const delta = Math.max(-200, Math.min(200, event.deltaY * (event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? this.height : 1))); this.publish(zoomImageAt(this.size, this.view, this.geometry.zoom * Math.exp(-delta * 0.005), this.point(event))); },
    toggleNatural() { this.restoreTemporary(); this.publish(Math.abs(this.geometry.scale - 1) < 0.001 ? initialImageView() : naturalImageView(this.size, this.view)); },
    startDrag(event) {
      if (event.button !== 0 || event.target.closest('button, input, a')) return;
      this.$refs.stage.focus({ preventScroll: true });
      this.drag = { pointer: event.pointerId, x: event.clientX, y: event.clientY, view: { ...this.view }, background: this.isBackground(event), moved: false };
      this.$refs.stage.setPointerCapture(event.pointerId); event.preventDefault();
    },
    isBackground(event) {
      const { x, y } = this.point(event), g = this.geometry;
      return x >= 0 && y >= 0 && x <= this.width && y <= this.height &&
        (x < g.left || x > g.left + g.width || y < g.top || y > g.top + g.height);
    },
    moveDrag(event) {
      if (this.drag?.pointer !== event.pointerId) return;
      const dx = event.clientX - this.drag.x, dy = event.clientY - this.drag.y;
      if (Math.hypot(dx, dy) > 4) this.drag.moved = true;
      this.publish(panImage(this.size, this.drag.view, dx, dy));
    },
    finishDrag(event) {
      const drag = this.drag;
      if (!drag || drag.pointer !== event.pointerId) return;
      // 图片禁用命中测试以支持捕获拖动；用实际显示边界判断留白，不能依赖 event.target。
      const backgroundClick = event.button === 0 && drag.background && !drag.moved &&
        Math.hypot(event.clientX - drag.x, event.clientY - drag.y) <= 4 && this.isBackground(event);
      this.endDrag();
      if (backgroundClick) this.$emit('background-click');
    },
    endDrag() { this.drag = null; },
    temporaryZoom() { if (this.temporaryView) return; this.temporaryView = { ...this.view }; this.publish(zoomImageAt(this.size, this.view, Math.max(this.geometry.zoom * 2, 1 / this.geometry.fit), { x: this.width / 2, y: this.height / 2 })); },
    restoreTemporary() { if (this.temporaryView) { const view = this.temporaryView; this.temporaryView = null; this.publish(view); } }
  }
};
</script>

<style scoped>
.image-viewport { position: relative; width: 100%; height: 100%; min-width: 0; min-height: 0; overflow: hidden; touch-action: none; cursor: grab; outline-offset: -3px; }
.image-viewport--dragging { cursor: grabbing; }
.image-viewport > img { position: absolute; top: 0; left: 0; max-width: none; max-height: none; object-fit: contain; user-select: none; pointer-events: none; }
.image-viewport__scale { position: absolute; right: 12px; bottom: 12px; padding: 3px 8px; border-radius: var(--radius-sm); background: var(--overlay-strong); color: var(--recap-hero-fg); font-size: var(--fs-caption); pointer-events: none; }
.image-viewport__notice { position: absolute; top: 12px; left: 12px; max-width: calc(100% - 24px); padding: 4px 8px; border-radius: var(--radius-sm); background: var(--overlay-strong); color: var(--recap-hero-fg); font-size: var(--fs-caption); pointer-events: none; }
.image-viewport__loading { position: absolute; z-index: 1; left: 50%; top: 50%; transform: translate(-50%, -50%); padding: 6px 12px; border-radius: var(--radius-sm); background: var(--overlay-strong); color: var(--recap-hero-fg); pointer-events: none; }
</style>
