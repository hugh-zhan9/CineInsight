<template>
  <section class="edit-section" data-test="crop-editor">
    <h3>固定黑边裁剪（已确认 {{ confirmedCount }}/{{ items.length }}）</h3>
    <p class="help-text">每部视频整片使用同一个裁剪框。输出采用框内尺寸，保持画面比例；裁剪需要重新编码。</p>
    <div class="edit-inline">
      <button v-if="!analyzing" type="button" class="btn btn--secondary btn--sm" :disabled="!editable" data-test="crop-analyze" @click="$emit('analyze', 0)">自动检测未确认项的黑边</button>
      <template v-else>
        <ProgressBar :value="analysisProgress" label="正在检测黑边" />
        <button type="button" class="btn btn--secondary btn--sm" data-test="crop-cancel-analysis" @click="$emit('cancel-analysis')">取消检测</button>
      </template>
      <span class="help-text">多处取样只提供建议，请检查暗场、字幕和画面边缘。</span>
    </div>
    <p v-if="analysis && analysis.kind === 'crop' && ['failed', 'cancelled'].includes(analysis.status)" role="status" class="help-text">{{ analysis.status === 'cancelled' ? '检测已取消，裁剪框没有改动。' : `检测失败：${analysis.error || '原因未知'}；裁剪框没有改动。` }}</p>
    <label>选择视频
      <select v-model.number="selected" class="select-input" data-test="crop-select">
        <option v-for="(item, index) in items" :key="item.video_id" :value="index">{{ index + 1 }}. {{ videoName(item.video_id) }} · {{ item.confirmed ? '已确认' : '待确认' }}</option>
      </select>
    </label>
    <template v-if="item">
      <p v-if="detectionText" class="help-text" data-test="crop-detection-result">{{ detectionText }}</p>
      <p v-if="!canvasWidth || !canvasHeight" class="help-text">请先完成预检，读取画面尺寸后再裁剪。</p>
      <template v-else>
        <div class="edit-inline">
          <label>画面比例
            <select :value="ratios[item.video_id] || 'free'" :disabled="!editable" class="select-input" data-test="crop-ratio" @change="chooseRatio($event.target.value)">
              <option v-for="ratio in ratioOptions" :key="ratio.value" :value="ratio.value">{{ ratio.label }}</option>
            </select>
          </label>
          <button type="button" class="btn btn--secondary btn--sm" :disabled="!editable" data-test="crop-reset" @click="resetRect">重置为整幅画面</button>
          <button type="button" class="btn btn--secondary btn--sm" :disabled="!editable || items.length <= 1" data-test="crop-remove" @click="removeItem">移出此项</button>
        </div>
        <p class="help-text">拖动框内移动，拖动四角缩放；像素调整会切回自由比例。坐标按 2 像素对齐。</p>
        <div class="crop-stage" :style="stageStyle" data-test="crop-stage" @pointermove="moveDrag" @pointerup="endDrag" @pointercancel="cancelDrag" @lostpointercapture="cancelDrag">
          <img :src="frameURL" alt="原始画面及裁剪区域" draggable="false" @load="imageError = false" @error="imageError = true" />
          <div class="crop-box" :style="boxStyle" data-test="crop-box" @pointerdown="beginDrag($event, 'move')">
            <button v-for="handle in handles" :key="handle" type="button" :class="['btn-icon', 'crop-handle', `crop-handle--${handle}`]" :disabled="!editable" :aria-label="`拖动裁剪框${handleLabels[handle]}`" :data-test="`crop-handle-${handle}`" @pointerdown.stop="beginDrag($event, handle)"></button>
          </div>
        </div>
        <p v-if="imageError" role="alert" class="edit-error">预览画面读取失败，请换一个时间点或重新预检，检查后再确认。</p>
        <label class="crop-seek">预览时间 {{ time(previewMS) }}
          <input type="range" min="0" :max="Math.max(0, durationMs(item.video_id) - 1)" step="1" :value="previewMS" data-test="crop-seek" @change="times[item.video_id] = Number($event.target.value)" />
        </label>
        <div class="edit-inline crop-margins">
          <label v-for="side in sides" :key="side.key">{{ side.label }}裁掉
            <input type="number" min="0" step="2" :value="margins[side.key]" :disabled="!editable" class="text-input" :data-test="`crop-margin-${side.key}`" @change="setMargin(side.key, $event.target.value)" /> 像素
          </label>
        </div>
        <p data-test="crop-output-size">原画布 {{ canvasWidth }}×{{ canvasHeight }} → 输出 {{ rect.width }}×{{ rect.height }}</p>
        <details open>
          <summary>裁剪结果预览</summary>
          <div class="crop-result" :style="resultStyle" data-test="crop-result"><img :src="frameURL" :style="resultImageStyle" alt="裁剪后画面" draggable="false" /></div>
        </details>
        <p v-if="item.rect && item.source_version !== source.source_version" class="edit-error">来源已经变化，请重新检测或设置裁剪框。</p>
        <label class="edit-check">
          <input type="checkbox" :checked="item.confirmed" :disabled="!editable || (!item.confirmed && (imageError || !item.rect || item.source_version !== source.source_version || !rectValid))" data-test="crop-confirm" @change="setConfirmed($event.target.checked)" />
          已检查裁剪区域，确认这一部（框外画面及画面内字幕会被裁掉）
        </label>
      </template>
    </template>
  </section>
</template>

<script>
import ProgressBar from '../ui/ProgressBar.vue';
import { workbenchVideoMixin } from './workbenchVideo.js';
import { cloneRecipe, editFrameURL, formatEditTime, preflightSource } from '../../utils/videoEdit.js';
import { CROP_RATIOS, cropRatio, cropFromMargins, dragCrop, fitCropRatio, setCropRect, validCrop } from '../../utils/videoCrop.js';

export default {
  name: 'CropEditor', components: { ProgressBar }, mixins: [workbenchVideoMixin],
  props: {
    recipe: { type: Object, required: true }, editable: { type: Boolean, default: false },
    preflight: { type: Object, default: null }, analysis: { type: Object, default: null },
    analyzing: { type: Boolean, default: false }, analysisProgress: { type: Number, default: 0 }
  },
  emits: ['change', 'analyze', 'cancel-analysis'],
  data() {
    return { selected: 0, times: {}, ratios: {}, drag: null, imageError: false, ratioOptions: CROP_RATIOS,
      handles: ['nw', 'ne', 'sw', 'se'], handleLabels: { nw: '左上角', ne: '右上角', sw: '左下角', se: '右下角' },
      sides: [{ key: 'left', label: '左侧' }, { key: 'right', label: '右侧' }, { key: 'top', label: '上方' }, { key: 'bottom', label: '下方' }] };
  },
  computed: {
    items() { return this.recipe?.crop?.items || []; },
    item() { return this.items[this.selected]; },
    confirmedCount() { return this.items.filter(item => item.confirmed).length; },
    source() { return preflightSource(this.preflight, this.item?.video_id) || {}; },
    canvasWidth() { return this.source.display_width || 0; },
    canvasHeight() { return this.source.display_height || 0; },
    rect() { return this.drag?.current || this.item?.rect || { x: 0, y: 0, width: this.canvasWidth, height: this.canvasHeight }; },
    rectValid() { return validCrop(this.rect, this.canvasWidth, this.canvasHeight); },
    previewMS() { return this.times[this.item?.video_id] ?? Math.floor(this.durationMs(this.item?.video_id) / 8); },
    frameURL() { return `${editFrameURL(this.item?.video_id, this.previewMS, 640)}&v=${encodeURIComponent(this.source.source_version || '')}`; },
    stageStyle() { return { aspectRatio: `${this.canvasWidth}/${this.canvasHeight}`, maxWidth: `${Math.min(640, 380 * this.canvasWidth / this.canvasHeight)}px` }; },
    boxStyle() { return { left: `${this.rect.x / this.canvasWidth * 100}%`, top: `${this.rect.y / this.canvasHeight * 100}%`, width: `${this.rect.width / this.canvasWidth * 100}%`, height: `${this.rect.height / this.canvasHeight * 100}%` }; },
    resultStyle() { return { aspectRatio: `${this.rect.width}/${this.rect.height}`, maxWidth: `${Math.min(320, 260 * this.rect.width / this.rect.height)}px` }; },
    resultImageStyle() { return { width: `${this.canvasWidth / this.rect.width * 100}%`, height: `${this.canvasHeight / this.rect.height * 100}%`, left: `${-this.rect.x / this.rect.width * 100}%`, top: `${-this.rect.y / this.rect.height * 100}%` }; },
    margins() { return { left: this.rect.x, top: this.rect.y, right: this.canvasWidth - this.rect.x - this.rect.width, bottom: this.canvasHeight - this.rect.y - this.rect.height }; },
    detectionText() {
      const found = this.analysis?.status === 'completed' && this.analysis?.crops?.find(result => result.video_id === this.item?.video_id);
      return found ? ({ detected: '已给出黑边检测建议，请检查并确认。', undetected: '未能可靠识别黑边，请手动设置裁剪框。', no_borders: '未发现可裁掉的固定黑边，可手动调整或移出此项。', confirmed: '已确认的裁剪框保持不变。' }[found.status] || '') : '';
    }
  },
  watch: {
    recipe: {
      deep: true,
      handler() {
        for (const item of this.items) {
          const ratio = cropRatio(this.ratios[item.video_id]);
          if (ratio && (!item.rect || Math.abs(item.rect.width - item.rect.height * ratio) >= 2 * Math.max(1, ratio))) this.ratios[item.video_id] = 'free';
        }
      }
    },
    selected() { this.cancelDrag(); this.imageError = false; },
    editable(value) { if (!value) this.cancelDrag(); }
  },
  methods: {
    time: formatEditTime,
    commit(rect) { if (validCrop(rect, this.canvasWidth, this.canvasHeight)) this.$emit('change', setCropRect(this.recipe, this.selected, rect, this.source.source_version)); },
    resetRect() { this.ratios[this.item.video_id] = 'free'; this.commit({ x: 0, y: 0, width: this.canvasWidth, height: this.canvasHeight }); },
    chooseRatio(value) { this.ratios[this.item.video_id] = value; this.commit(fitCropRatio(this.rect, value)); },
    setMargin(side, value) { this.ratios[this.item.video_id] = 'free'; this.commit(cropFromMargins(this.rect, side, value, this.canvasWidth, this.canvasHeight)); },
    setConfirmed(value) { const next = cloneRecipe(this.recipe); next.crop.items[this.selected].confirmed = value; this.$emit('change', next); },
    removeItem() { const next = cloneRecipe(this.recipe); next.crop.items.splice(this.selected, 1); this.selected = Math.min(this.selected, next.crop.items.length - 1); this.$emit('change', next); },
    beginDrag(event, handle) {
      if (!this.editable || !this.rectValid || (event.button !== undefined && event.button !== 0)) return;
      const stage = this.$el.querySelector('[data-test="crop-stage"]');
      const bounds = stage.getBoundingClientRect();
      if (!bounds.width || !bounds.height) return;
      event.preventDefault();
      stage.setPointerCapture?.(event.pointerId);
      this.drag = { pointer: event.pointerId, handle, x: event.clientX, y: event.clientY, bounds, initial: { ...this.rect }, current: { ...this.rect } };
    },
    moveDrag(event) {
      if (!this.drag || event.pointerId !== this.drag.pointer) return;
      const d = this.drag;
      d.current = dragCrop(d.initial, d.handle, (event.clientX - d.x) * this.canvasWidth / d.bounds.width, (event.clientY - d.y) * this.canvasHeight / d.bounds.height, this.canvasWidth, this.canvasHeight, this.ratios[this.item.video_id] || 'free');
    },
    endDrag(event) { if (!this.drag || event.pointerId !== this.drag.pointer) return; const rect = this.drag.current; this.drag = null; this.commit(rect); },
    cancelDrag() { this.drag = null; }
  }
};
</script>

<style scoped>
.crop-stage, .crop-result { position: relative; width: 100%; overflow: hidden; background: var(--media-badge-bg); margin: var(--space-3) 0; }
.crop-stage { touch-action: none; user-select: none; }
.crop-stage > img { width: 100%; height: 100%; display: block; }
.crop-box { position: absolute; border: 2px solid var(--accent-color); box-sizing: border-box; box-shadow: 0 0 0 2000px var(--overlay-strong); cursor: move; }
.crop-handle { position: absolute; width: 14px; height: 14px; padding: 0; background: var(--media-badge-fg); border: 2px solid var(--accent-color); border-radius: 2px; }
.crop-handle--nw { left: 0; top: 0; cursor: nwse-resize; }
.crop-handle--ne { right: 0; top: 0; cursor: nesw-resize; }
.crop-handle--sw { left: 0; bottom: 0; cursor: nesw-resize; }
.crop-handle--se { right: 0; bottom: 0; cursor: nwse-resize; }
.crop-result > img { position: absolute; max-width: none; }
.crop-margins input { width: 90px; }
.crop-seek { display: grid; gap: var(--space-2); max-width: 640px; }
</style>
