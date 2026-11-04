<template>
  <section class="crop-editor" data-test="crop-editor">
    <header class="crop-toolbar">
      <div><h3>画面裁剪</h3><span class="help-text">{{ items.length }} 部视频 · 拖动裁剪框后直接导出</span></div>
      <div class="edit-inline">
        <button v-if="!analyzing" type="button" class="btn btn--secondary btn--sm" :disabled="!editable" data-test="crop-analyze" @click="$emit('analyze', 0)">自动检测黑边（可选）</button>
        <template v-else><ProgressBar :value="analysisProgress" label="正在检测黑边" /><button type="button" class="btn btn--secondary btn--sm" data-test="crop-cancel-analysis" @click="$emit('cancel-analysis')">取消检测</button></template>
      </div>
    </header>
    <p v-if="analysis && analysis.kind === 'crop' && ['failed', 'cancelled'].includes(analysis.status)" role="status" class="edit-error">{{ analysis.status === 'cancelled' ? '检测已取消，裁剪框没有改动。' : `检测失败：${analysis.error || '原因未知'}；裁剪框没有改动。` }}</p>
    <div class="crop-layout">
      <aside class="crop-sources" aria-label="裁剪素材">
        <h4>素材 <span class="help-text">{{ items.length }}</span></h4>
        <label class="crop-select-label">选择视频<select v-model.number="selected" class="select-input" data-test="crop-select"><option v-for="(entry, index) in items" :key="entry.video_id" :value="index">{{ index + 1 }}. {{ videoName(entry.video_id) }} · {{ entry.rect ? '已设置' : '待设置' }}</option></select></label>
        <button v-for="(entry, index) in items" :key="entry.video_id" type="button" class="btn-plain crop-source" :class="{ 'crop-source--active': selected === index }" @click="selected = index">
          <span class="crop-source-number">{{ String(index + 1).padStart(2, '0') }}</span><span class="crop-source-name">{{ videoName(entry.video_id) }}</span><span class="help-text">{{ entry.rect ? '已设置' : '待设置' }}</span>
        </button>
      </aside>
      <template v-if="item">
        <div class="crop-workspace">
          <div class="crop-monitor-toolbar"><label>画面放大 <select v-model.number="zoom" class="select-input edit-select" data-test="crop-zoom"><option :value="1">适应窗口</option><option :value="2">2×</option><option :value="4">4×</option></select></label><span>{{ previewMode === 'source' ? '原画面 · 拖动边线或四角调整裁剪框' : '裁剪结果' }}</span><div class="edit-inline"><button type="button" class="btn btn--secondary btn--sm" :aria-pressed="previewMode === 'source'" @click="previewMode = 'source'">裁剪</button><button type="button" class="btn btn--secondary btn--sm" :aria-pressed="previewMode === 'result'" @click="previewMode = 'result'">结果预览</button></div></div>
          <div class="crop-monitor" data-test="crop-monitor">
            <template v-if="canvasWidth && canvasHeight">
              <div v-show="previewMode === 'source'" class="crop-stage" :style="stageStyle" data-test="crop-stage" @pointermove="moveDrag" @pointerup="endDrag" @pointercancel="cancelDrag" @lostpointercapture="cancelDrag" @pointerleave="leaveLoupe">
                <img :src="frameURL" alt="原始画面及裁剪区域" draggable="false" @load="frameLoaded = true; imageError = false" @error="frameLoaded = false; imageError = true" />
                <div class="crop-box" :style="boxStyle" data-test="crop-box" @pointerdown="beginBoxDrag" @pointermove="hoverBox"><button v-for="handle in handles" :key="handle" type="button" :class="['btn-icon', 'crop-handle', `crop-handle--${handle}`]" :disabled="!editable" :aria-label="`拖动裁剪框${handleLabels[handle]}`" :data-test="`crop-handle-${handle}`" @pointerdown.stop="beginDrag($event, handle)" @pointermove.stop="hoverLoupe($event, handle)" @pointerenter="hoverLoupe($event, handle)" @pointerleave="leaveLoupe"></button></div>
              </div>
              <div v-show="previewMode === 'result'" class="crop-result" :style="resultStyle" data-test="crop-result"><img :src="frameURL" :style="resultImageStyle" alt="裁剪后画面" draggable="false" /></div>
            </template>
            <div v-else class="crop-empty" role="status"><strong>{{ sourceLoading ? '正在读取画面尺寸…' : '尚未读取到画面尺寸' }}</strong><p>读取后即可手动裁剪，无需检测黑边。</p><button type="button" class="btn btn--secondary btn--sm" :disabled="sourceLoading || preflightLoading || analyzing" data-test="crop-read-source" @click="readSource">刷新画面</button></div>
          </div>
          <p v-if="imageError" role="alert" class="edit-error">预览画面读取失败，请换一个时间点或刷新画面。</p>
          <p v-if="sourceError" role="alert" class="edit-error" data-test="crop-source-error">{{ sourceError }} <button type="button" class="btn-link" :disabled="sourceLoading" @click="readSource">重试</button></p>
          <div class="crop-timeline">
            <CropTimeline :key="item.video_id" :video-id="item.video_id" :duration-ms="durationMs(item.video_id)" :at-ms="previewMS" :range="item.range || null" :source-version="source.source_version || ''" :editable="editable" :can-step="Boolean(navigateFrame && source.source_version)" :busy="frameBusy" :error="frameError" @seek="seek" @range="setRange" @step-frame="stepFrame" />
          </div>
        </div>
        <aside class="crop-inspector" aria-label="裁剪设置">
          <h4>裁剪设置</h4>
          <div v-if="canvasWidth && canvasHeight && rectValid" class="crop-live-preview">
            <span class="help-text">实时结果</span>
            <div class="crop-result crop-result--live" :style="{ aspectRatio: `${rect.width}/${rect.height}`, width: `${Math.min(180, 150 * rect.width / rect.height)}px` }" data-test="crop-live-result"><img :src="frameURL" :style="resultImageStyle" alt="实时裁剪结果" draggable="false" /></div>
          </div>
          <div v-if="loupe && !imageError" class="crop-loupe-panel" :style="loupePanelStyle" data-test="crop-loupe">
            <span class="help-text">边界放大 · {{ handleLabels[loupe.handle] }}</span>
            <div class="crop-loupe-view"><img :src="frameURL" :style="loupeImageStyle" alt="裁剪边界局部放大" draggable="false" /><div class="crop-loupe-box" :style="loupeBoxStyle"></div></div>
          </div>
          <p class="help-text">悬停或拖动边线、四角，可放大查看边界。</p>
          <p v-if="detectionText" class="help-text" data-test="crop-detection-result">{{ detectionText }}</p>
          <label>画面比例<select :value="ratios[item.video_id] || 'free'" :disabled="!editable || !canvasWidth" class="select-input" data-test="crop-ratio" @change="chooseRatio($event.target.value)"><option v-for="ratio in ratioOptions" :key="ratio.value" :value="ratio.value">{{ ratio.label }}</option></select></label>
          <div v-if="canvasWidth && canvasHeight" class="crop-margins"><label v-for="side in sides" :key="side.key">{{ side.label }}裁掉<input type="number" min="0" step="2" :value="margins[side.key]" :disabled="!editable" class="text-input" :data-test="`crop-margin-${side.key}`" @change="setMargin(side.key, $event.target.value)" /><span class="crop-nudge"><button type="button" class="btn btn--secondary btn--sm" :disabled="!editable" :aria-label="`${side.label}少裁2像素`" :data-test="`crop-nudge-${side.key}-less`" @click="nudgeMargin(side.key, -2)">−2</button><button type="button" class="btn btn--secondary btn--sm" :disabled="!editable" :aria-label="`${side.label}多裁2像素`" :data-test="`crop-nudge-${side.key}-more`" @click="nudgeMargin(side.key, 2)">+2</button></span></label></div>
          <p class="help-text">拖动框内移动，拖动边线或四角缩放；像素调整切回自由比例，按 2 像素对齐。</p>
          <p v-if="canvasWidth && canvasHeight" class="crop-output-size" data-test="crop-output-size">原画布 {{ canvasWidth }}×{{ canvasHeight }} → 输出 {{ rect.width }}×{{ rect.height }}</p>
          <button type="button" class="btn btn--secondary btn--sm" :disabled="!editable || !canvasWidth" data-test="crop-reset" @click="resetRect">重置为整幅画面</button>
          <p v-if="item.rect && item.source_version !== source.source_version" class="edit-error">来源已经变化，请重新检测或设置裁剪框。</p>

          <p class="help-text">框外画面及画面内字幕会被裁掉。成品按框内尺寸重新编码，原片保持不变。</p>
          <button type="button" class="btn-link" :disabled="!editable || items.length <= 1" data-test="crop-remove" @click="removeItem">移出此项</button>
        </aside>
      </template>
    </div>
  </section>
</template>

<script>
import CropTimeline from './CropTimeline.vue';
import ProgressBar from '../ui/ProgressBar.vue';
import { workbenchVideoMixin } from './workbenchVideo.js';
import { cloneRecipe, editFrameURL, formatEditTime, preflightSource } from '../../utils/videoEdit.js';
import { CROP_RATIOS, cropRatio, cropFromMargins, dragCrop, fitCropRatio, setCropRect, validCrop } from '../../utils/videoCrop.js';

export default {
  name: 'CropEditor', components: { ProgressBar, CropTimeline }, mixins: [workbenchVideoMixin],
  props: {
    recipe: { type: Object, required: true }, editable: { type: Boolean, default: false },
    preflight: { type: Object, default: null }, analysis: { type: Object, default: null },
    navigateFrame: { type: Function, default: null },
    loadSource: { type: Function, default: null },
    preflightLoading: { type: Boolean, default: false },
    analyzing: { type: Boolean, default: false }, analysisProgress: { type: Number, default: 0 }
  },
  emits: ['change', 'analyze', 'cancel-analysis', 'run-preflight', 'ready', 'interacting'],
  data() {
    return { loadedSource: null, sourceLoading: false, sourceError: '', sourceSequence: 0, frameLoaded: false, loupe: null, selected: 0, zoom: 1, frameBusy: false, frameError: '', frameSequence: 0, previewMode: 'source', times: {}, ratios: {}, drag: null, imageError: false, ratioOptions: CROP_RATIOS,
      handles: ['nw', 'n', 'ne', 'w', 'e', 'sw', 's', 'se'], handleLabels: { n: '上边', s: '下边', w: '左边', e: '右边', nw: '左上角', ne: '右上角', sw: '左下角', se: '右下角' },
      sides: [{ key: 'left', label: '左侧' }, { key: 'right', label: '右侧' }, { key: 'top', label: '上方' }, { key: 'bottom', label: '下方' }] };
  },
  computed: {
    items() { return this.recipe?.crop?.items || []; },
    item() { return this.items[this.selected]; },
    exportReady() { return Boolean(this.item?.rect && this.rectValid && this.item.source_version === this.source.source_version && this.frameLoaded && !this.imageError && !this.sourceLoading && !this.sourceError && !this.drag); },
    detection() { return this.analysis?.kind === 'crop' && this.analysis?.status === 'completed' ? this.analysis.crops?.find(result => result.video_id === this.item?.video_id) : null; },
    source() {
      if (this.loadedSource && this.loadedSource.video_id === this.item?.video_id) return this.loadedSource;
      const source = preflightSource(this.preflight, this.item?.video_id);
      if (source?.display_width && source?.display_height && !source.error) return source;
      return this.detection?.source_version ? this.detection : (source || {});
    },
    canvasWidth() { return this.source.display_width || 0; },
    canvasHeight() { return this.source.display_height || 0; },
    rect() { return this.drag?.current || this.item?.rect || { x: 0, y: 0, width: this.canvasWidth, height: this.canvasHeight }; },
    rectValid() { return validCrop(this.rect, this.canvasWidth, this.canvasHeight); },
    previewMS() { return this.times[this.item?.video_id] ?? Math.floor(this.durationMs(this.item?.video_id) / 8); },
    frameURL() { return `${editFrameURL(this.item?.video_id, this.previewMS, 960)}&v=${encodeURIComponent(this.source.source_version || '')}`; },
    stageStyle() { return { aspectRatio: `${this.canvasWidth}/${this.canvasHeight}`, width: this.zoom === 1 ? '100%' : `${Math.min(960, 300 * this.canvasWidth / this.canvasHeight) * this.zoom}px`, maxWidth: this.zoom === 1 ? `${Math.min(960, 300 * this.canvasWidth / this.canvasHeight)}px` : 'none' }; },
    boxStyle() { return { left: `${this.rect.x / this.canvasWidth * 100}%`, top: `${this.rect.y / this.canvasHeight * 100}%`, width: `${this.rect.width / this.canvasWidth * 100}%`, height: `${this.rect.height / this.canvasHeight * 100}%` }; },
    resultStyle() { return { aspectRatio: `${this.rect.width}/${this.rect.height}`, maxWidth: `${Math.min(960, 460 * this.rect.width / this.rect.height)}px` }; },
    resultImageStyle() { return { width: `${this.canvasWidth / this.rect.width * 100}%`, height: `${this.canvasHeight / this.rect.height * 100}%`, left: `${-this.rect.x / this.rect.width * 100}%`, top: `${-this.rect.y / this.rect.height * 100}%` }; },
    loupePanelStyle() {
      if (!this.loupe) return {};
      const { clientX, clientY } = this.loupe;
      const viewport = this.$el.ownerDocument.defaultView;
      return { left: `${Math.max(8, clientX + 224 > viewport.innerWidth ? clientX - 224 : clientX + 24)}px`, top: `${Math.max(8, Math.min(clientY + 24, viewport.innerHeight - 190))}px` };
    },
    loupeScale() { return 4 * Math.min(1, 960 / this.canvasWidth); },
    loupePoint() {
      if (!this.loupe) return { x: 0, y: 0 };
      const { handle, x, y } = this.loupe, r = this.rect;
      return {
        x: handle.includes('w') ? r.x : handle.includes('e') ? r.x + r.width : Math.max(r.x, Math.min(r.x + r.width, x)),
        y: handle.includes('n') ? r.y : handle.includes('s') ? r.y + r.height : Math.max(r.y, Math.min(r.y + r.height, y))
      };
    },
    loupeImageStyle() {
      const k = this.loupeScale, p = this.loupePoint;
      return { width: `${this.canvasWidth * k}px`, height: `${this.canvasHeight * k}px`, left: `calc(50% - ${p.x * k}px)`, top: `calc(50% - ${p.y * k}px)` };
    },
    loupeBoxStyle() {
      const k = this.loupeScale, p = this.loupePoint, r = this.rect;
      return { width: `${r.width * k}px`, height: `${r.height * k}px`, left: `calc(50% + ${(r.x - p.x) * k}px)`, top: `calc(50% + ${(r.y - p.y) * k}px)` };
    },
    margins() { return { left: this.rect.x, top: this.rect.y, right: this.canvasWidth - this.rect.x - this.rect.width, bottom: this.canvasHeight - this.rect.y - this.rect.height }; },
    detectionText() {
      const found = this.analysis?.status === 'completed' && this.analysis?.crops?.find(result => result.video_id === this.item?.video_id);
      return found ? ({ detected: '已应用黑边检测建议，可继续拖动微调。', undetected: '未能可靠识别黑边，请手动设置裁剪框。', no_borders: '未发现可裁掉的固定黑边，可手动调整或移出此项。', confirmed: '此前确认的裁剪框保持不变；手动调整后可重新检测。' }[found.status] || '') : '';
    }
  },
  watch: {
    exportReady: { immediate: true, handler(value) { this.$emit('ready', value); } },
    drag(value) { this.$emit('interacting', Boolean(value)); },
    frameURL() { this.frameLoaded = false; this.imageError = false; },
    'item.video_id': { immediate: true, handler() { this.readSource(); } },
    'analysis.finished_at'() { this.readSource(); },
    preflight(value, previous) {
      const source = preflightSource(value, this.item?.video_id);
      if (!source?.source_version || !source.display_width || !source.display_height || source.error) return;
      const current = this.loadedSource || preflightSource(previous, this.item?.video_id) || this.detection || {};
      const changed = source.source_version !== current.source_version || source.display_width !== current.display_width || source.display_height !== current.display_height;
      this.sourceSequence++;
      this.loadedSource = source;
      this.sourceLoading = false;
      this.sourceError = '';
      if (changed) this.cancelDrag();
    },
    recipe: {
      deep: true,
      handler() {
        for (const item of this.items) {
          const ratio = cropRatio(this.ratios[item.video_id]);
          if (ratio && (!item.rect || Math.abs(item.rect.width - item.rect.height * ratio) >= 2 * Math.max(1, ratio))) this.ratios[item.video_id] = 'free';
        }
      }
    },
    selected() { this.cancelDrag(); this.imageError = false; this.zoom = 1; this.frameSequence++; this.frameError = ''; },
    times: { deep: true, handler() { this.frameSequence++; } },
    'source.source_version'() { this.frameSequence++; },
    editable(value) { if (!value) this.cancelDrag(); }
  },
  beforeUnmount() { this.frameSequence++; this.sourceSequence++; },
  methods: {
    async readSource() {
      const sequence = ++this.sourceSequence;
      if (!this.loadSource || !this.item) { this.sourceLoading = false; return; }
      const videoID = this.item.video_id;
      this.loadedSource = null; this.sourceLoading = true; this.sourceError = ''; this.cancelDrag();
      try {
        const source = await this.loadSource(videoID);
        if (sequence !== this.sourceSequence) return;
        if (!source?.source_version || !(source.display_width >= 2 && source.display_height >= 2)) throw new Error('未取得有效画面尺寸');
        this.loadedSource = source;
        if (!this.item.rect && this.editable) this.commit({ x: 0, y: 0, width: source.display_width, height: source.display_height });
      } catch (err) {
        if (sequence === this.sourceSequence) this.sourceError = `读取画面尺寸失败：${String(err?.message || err)}`;
      } finally { if (sequence === this.sourceSequence) this.sourceLoading = false; }
    },
    edgeHandle(event) {
      const bounds = event.currentTarget.getBoundingClientRect();
      const x = event.clientX - bounds.left, y = event.clientY - bounds.top, tolerance = 8;
      return `${y <= tolerance ? 'n' : y >= bounds.height - tolerance ? 's' : ''}${x <= tolerance ? 'w' : x >= bounds.width - tolerance ? 'e' : ''}`;
    },
    beginBoxDrag(event) { this.beginDrag(event, this.edgeHandle(event) || 'move'); },
    hoverBox(event) {
      if (this.drag) return;
      const handle = this.edgeHandle(event);
      if (handle) this.hoverLoupe(event, handle); else this.loupe = null;
    },
    hoverLoupe(event, handle) {
      if (handle === 'move' || (this.drag && event.pointerId !== this.drag.pointer)) return;
      const bounds = this.$el.querySelector('[data-test="crop-stage"]').getBoundingClientRect();
      if (!bounds.width || !bounds.height) return;
      this.loupe = { handle, clientX: event.clientX, clientY: event.clientY, x: (event.clientX - bounds.left) * this.canvasWidth / bounds.width, y: (event.clientY - bounds.top) * this.canvasHeight / bounds.height };
    },
    leaveLoupe() { if (!this.drag) this.loupe = null; },
    seek(ms) { this.times[this.item.video_id] = ms; this.frameError = ''; },
    setRange(range) {
      if (!this.editable) return;
      const next = cloneRecipe(this.recipe);
      Object.assign(next.crop.items[this.selected], { range, confirmed: false });
      this.$emit('change', next);
    },
    async stepFrame(direction) {
      if (this.frameBusy || !this.navigateFrame || !this.source.source_version) return;
      const sequence = ++this.frameSequence;
      this.frameBusy = true; this.frameError = '';
      try {
        const position = await this.navigateFrame({ video_id: this.item.video_id, source_version: this.source.source_version, at_ms: this.previewMS, direction });
        if (sequence === this.frameSequence) this.seek(position.at_ms);
      } catch (err) {
        if (sequence === this.frameSequence) this.frameError = `逐帧定位失败：${String(err?.message || err)}`;
      } finally { this.frameBusy = false; }
    },
    nudgeMargin(side, delta) { this.setMargin(side, this.margins[side] + delta); },
    durationMs(videoID) {
      if (videoID === this.item?.video_id && this.source.duration_ms > 0) return this.source.duration_ms;
      return Math.round(Number(this.videos?.[videoID]?.duration || 0) * 1000);
    },
    time: formatEditTime,
    commit(rect) { if (validCrop(rect, this.canvasWidth, this.canvasHeight)) this.$emit('change', setCropRect(this.recipe, this.selected, rect, this.source.source_version)); },
    resetRect() { this.ratios[this.item.video_id] = 'free'; this.commit({ x: 0, y: 0, width: this.canvasWidth, height: this.canvasHeight }); },
    chooseRatio(value) { this.ratios[this.item.video_id] = value; this.commit(fitCropRatio(this.rect, value)); },
    setMargin(side, value) { this.ratios[this.item.video_id] = 'free'; this.commit(cropFromMargins(this.rect, side, value, this.canvasWidth, this.canvasHeight)); },
    removeItem() { const next = cloneRecipe(this.recipe); next.crop.items.splice(this.selected, 1); this.selected = Math.min(this.selected, next.crop.items.length - 1); this.$emit('change', next); },
    beginDrag(event, handle) {
      if (!this.editable || !this.rectValid || (event.button !== undefined && event.button !== 0)) return;
      const stage = this.$el.querySelector('[data-test="crop-stage"]');
      const bounds = stage.getBoundingClientRect();
      if (!bounds.width || !bounds.height) return;
      event.preventDefault();
      this.loupe = null;
      this.hoverLoupe(event, handle);
      stage.setPointerCapture?.(event.pointerId);
      this.drag = { pointer: event.pointerId, handle, x: event.clientX, y: event.clientY, bounds, initial: { ...this.rect }, current: { ...this.rect } };
    },
    moveDrag(event) {
      if (!this.drag || event.pointerId !== this.drag.pointer) return;
      const d = this.drag;
      this.hoverLoupe(event, d.handle);
      d.current = dragCrop(d.initial, d.handle, (event.clientX - d.x) * this.canvasWidth / d.bounds.width, (event.clientY - d.y) * this.canvasHeight / d.bounds.height, this.canvasWidth, this.canvasHeight, this.ratios[this.item.video_id] || 'free');
    },
    endDrag(event) { if (!this.drag || event.pointerId !== this.drag.pointer) return; const rect = this.drag.current; this.drag = null; this.loupe = null; this.commit(rect); },
    cancelDrag() { this.drag = null; this.loupe = null; }
  }
};
</script>

<style scoped>
.crop-editor { border: 1px solid var(--hairline); border-radius: var(--radius); background: var(--panel-bg); overflow: hidden; min-width: 0; }
.crop-toolbar { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: var(--space-3); padding: var(--space-3); border-bottom: 1px solid var(--hairline); }
.crop-toolbar h3, .crop-layout h4 { margin: 0 0 var(--space-2); }
.crop-layout { display: grid; grid-template-columns: 150px minmax(0, 1fr) 230px; align-items: stretch; }
.crop-sources, .crop-inspector { padding: var(--space-3); min-width: 0; }
.crop-sources { border-right: 1px solid var(--hairline); max-height: 650px; overflow-y: auto; }
.crop-select-label { display: block; font-size: var(--fs-caption); }
.crop-select-label select { margin: var(--space-2) 0; }
.crop-source { display: grid; gap: var(--space-1); width: 100%; text-align: left; border: 1px solid var(--hairline); border-radius: var(--radius); margin: var(--space-2) 0; padding: var(--space-2); }
.crop-source--active { border-color: var(--accent-color); background: var(--accent-soft); }
.crop-source-number { color: var(--text-secondary); font-size: var(--fs-caption); }
.crop-source-name { overflow-wrap: anywhere; font-size: var(--fs-secondary); }
.crop-workspace { min-width: 0; display: flex; flex-direction: column; background: var(--bg-color); }
.crop-monitor-toolbar { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: var(--space-2); padding: var(--space-2) var(--space-3); font-size: var(--fs-caption); color: var(--text-secondary); }
.crop-monitor { min-height: 320px; height: 350px; overflow: auto; flex: none; display: grid; grid-template-columns: minmax(0, 1fr); place-items: safe center; padding: var(--space-3); background: var(--media-badge-bg); }
.crop-empty { text-align: center; color: var(--media-badge-fg); max-width: 330px; font-size: var(--fs-secondary); }
.crop-timeline { padding: var(--space-3); border-top: 1px solid var(--hairline); }
.crop-inspector { display: flex; flex-direction: column; align-items: stretch; gap: var(--space-3); border-left: 1px solid var(--hairline); font-size: var(--fs-secondary); }
.crop-inspector p { margin: 0; }
.crop-inspector > label:not(.crop-confirm) { display: grid; gap: var(--space-2); }
.crop-margins { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: var(--space-3); }
.crop-margins label { display: grid; gap: var(--space-1); }
.crop-margins .text-input { width: 100%; min-width: 0; }
.crop-output-size { font-size: var(--fs-caption); padding: var(--space-2); background: var(--control-bg); border-radius: var(--radius); }
.crop-live-preview { display: grid; justify-items: center; gap: var(--space-1); }
.crop-result--live { max-height: 150px; max-width: 100%; height: auto; margin: 0; }
.crop-loupe-panel { position: fixed; width: 200px; padding: var(--space-2); box-sizing: border-box; z-index: var(--z-menu); pointer-events: none; background: var(--panel-bg); border: 1px solid var(--hairline); border-radius: var(--radius); box-shadow: var(--shadow-popover); }
.crop-loupe-view { position: relative; height: 130px; overflow: hidden; background: var(--media-badge-bg); border: 1px solid var(--hairline); border-radius: var(--radius); margin-top: var(--space-1); }
.crop-loupe-view > img { position: absolute; max-width: none; }
.crop-loupe-box { position: absolute; border: 1px solid var(--accent-color); box-sizing: border-box; box-shadow: 0 0 0 2000px var(--overlay-strong); }
@media (max-width: 1000px) { .crop-layout { grid-template-columns: minmax(0, 1fr) 220px; } .crop-sources { grid-column: 1 / -1; border-right: 0; border-bottom: 1px solid var(--hairline); display: flex; align-items: center; gap: var(--space-2); overflow-x: auto; } .crop-sources h4 { flex: none; margin: 0; } .crop-select-label { min-width: 160px; } .crop-source { flex: 0 0 130px; } }
@media (max-width: 640px) { .crop-layout { grid-template-columns: minmax(0, 1fr); } .crop-inspector { border-left: 0; border-top: 1px solid var(--hairline); } .crop-monitor { min-height: 240px; } }

.crop-stage, .crop-result { position: relative; width: 100%; overflow: hidden; background: var(--media-badge-bg); margin: var(--space-3) 0; }
.crop-stage { touch-action: none; user-select: none; }
.crop-stage > img { width: 100%; height: 100%; display: block; }
.crop-box { position: absolute; border: 2px solid var(--accent-color); box-sizing: border-box; box-shadow: 0 0 0 2000px var(--overlay-strong); cursor: move; }
.crop-handle { position: absolute; width: 14px; height: 14px; padding: 0; background: var(--media-badge-fg); border: 2px solid var(--accent-color); border-radius: 2px; }
.crop-nudge { display: flex; gap: var(--space-1); }
.crop-handle--n { top: 0; left: calc(50% - 7px); cursor: ns-resize; }
.crop-handle--s { bottom: 0; left: calc(50% - 7px); cursor: ns-resize; }
.crop-handle--w { left: 0; top: calc(50% - 7px); cursor: ew-resize; }
.crop-handle--e { right: 0; top: calc(50% - 7px); cursor: ew-resize; }
.crop-handle--nw { left: 0; top: 0; cursor: nwse-resize; }
.crop-handle--ne { right: 0; top: 0; cursor: nesw-resize; }
.crop-handle--sw { left: 0; bottom: 0; cursor: nesw-resize; }
.crop-handle--se { right: 0; bottom: 0; cursor: nwse-resize; }
.crop-result > img { position: absolute; max-width: none; }

</style>
