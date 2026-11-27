<template>
  <section class="crop-editor" data-test="crop-editor" tabindex="-1" @keydown="onKeydown">
    <header class="crop-toolbar">
      <div class="crop-toolbar-title">
        <h3 :title="items.length === 1 ? videoName(items[0].video_id) : undefined">{{ items.length === 1 ? videoName(items[0].video_id) : `画面裁剪 · ${items.length} 部视频` }}</h3>
        <span class="crop-status" :class="{ 'crop-status--detected': detection?.status === 'detected' }">{{ detectionStatus }}</span>
      </div>
      <div class="crop-toolbar-actions">
        <button v-if="!analyzing" type="button" class="btn btn--secondary btn--sm" :disabled="!editable" data-test="crop-analyze" @click="$emit('analyze', 0)">自动检测黑边（可选）</button>
        <template v-else>
          <ProgressBar class="crop-analysis-progress" :value="analysisProgress" label="正在检测黑边" />
          <button type="button" class="btn btn--secondary btn--sm" data-test="crop-cancel-analysis" @click="$emit('cancel-analysis')">取消检测</button>
        </template>
        <button type="button" class="btn btn--secondary btn--sm crop-toggle" :aria-pressed="showGrid" data-test="crop-grid-toggle" title="参考线 (G)" @click="showGrid = !showGrid">参考线</button>
      </div>
    </header>
    <p v-if="analysis && analysis.kind === 'crop' && ['failed', 'cancelled'].includes(analysis.status)" role="status" class="edit-error crop-notice">{{ analysis.status === 'cancelled' ? '检测已取消，裁剪框没有改动。' : `检测失败：${analysis.error || '原因未知'}；裁剪框没有改动。` }}</p>
    <div class="crop-layout" :class="{ 'crop-layout--single': items.length === 1 }">
      <aside class="crop-sources" aria-label="裁剪素材">
        <label class="crop-select-label">素材 · {{ items.length }}
          <select v-model.number="selected" class="select-input" data-test="crop-select">
            <option v-for="(entry, index) in items" :key="entry.video_id" :value="index">{{ index + 1 }}. {{ videoName(entry.video_id) }} · {{ entry.rect ? '已设置' : '待设置' }}</option>
          </select>
        </label>
        <button v-for="(entry, index) in items" :key="entry.video_id" type="button" class="btn-plain crop-source" :class="{ 'crop-source--active': selected === index }" :aria-pressed="selected === index" :title="videoName(entry.video_id)" @click="selected = index">
          <span class="crop-source-number">{{ String(index + 1).padStart(2, '0') }}</span>
          <span class="crop-source-name">{{ videoName(entry.video_id) }}</span>
          <span class="crop-source-state" :class="{ 'crop-source-state--set': entry.rect }">{{ entry.rect ? '已设置' : '待设置' }}</span>
        </button>
      </aside>
      <template v-if="item">
        <div class="crop-workspace">
          <div class="crop-monitor-toolbar">
            <label class="crop-monitor-zoom">画面放大
              <select v-model.number="zoom" class="select-input" data-test="crop-zoom">
                <option :value="1">适应窗口</option><option :value="2">2×</option><option :value="4">4×</option>
              </select>
            </label>
            <div class="crop-mode-switch" role="group" aria-label="监视器模式">
              <button type="button" class="btn btn--ghost btn--sm" :aria-pressed="previewMode === 'source'" @click="previewMode = 'source'">裁剪</button>
              <button type="button" class="btn btn--ghost btn--sm" :aria-pressed="previewMode === 'result'" @click="previewMode = 'result'">结果预览</button>
            </div>
            <span class="crop-monitor-readout">
              <template v-if="rectValid">{{ previewMode === 'source' ? `X ${rect.x} · Y ${rect.y}` : `${rect.width}×${rect.height}` }}</template>
              <template v-else>等待画面</template>
            </span>
          </div>
          <div class="crop-monitor" data-test="crop-monitor" @pointerdown.capture="$el.focus({ preventScroll: true })">
            <template v-if="canvasWidth && canvasHeight">
              <div v-show="previewMode === 'source'" class="crop-stage" :style="stageStyle" data-test="crop-stage" @pointermove="moveDrag" @pointerup="endDrag" @pointercancel="cancelDrag" @lostpointercapture="cancelDrag" @pointerleave="leaveLoupe">
                <img :src="frameURL" alt="原始画面及裁剪区域" draggable="false" @load="frameLoaded = true; imageError = false" @error="frameLoaded = false; imageError = true" />
                <div class="crop-box" :class="{ 'crop-box--dragging': drag }" :style="boxStyle" data-test="crop-box" @pointerdown="beginBoxDrag" @pointermove="hoverBox">
                  <div v-if="showGrid" class="crop-grid" aria-hidden="true"></div>
                  <span class="crop-box-size" aria-hidden="true">{{ rect.width }} × {{ rect.height }}</span>
                  <button v-for="handle in handles" :key="handle" type="button" :class="['btn-icon', 'crop-handle', `crop-handle--${handle}`]" :disabled="!editable" :aria-label="`拖动裁剪框${handleLabels[handle]}`" :data-test="`crop-handle-${handle}`" @pointerdown.stop="beginDrag($event, handle)" @pointermove.stop="hoverLoupe($event, handle)" @pointerenter="hoverLoupe($event, handle)" @pointerleave="leaveLoupe"></button>
                </div>
              </div>
              <div v-show="previewMode === 'result'" class="crop-result" :style="resultStyle" data-test="crop-result"><img :src="frameURL" :style="resultImageStyle" alt="裁剪后画面" draggable="false" /></div>
            </template>
            <div v-else class="crop-empty" role="status">
              <strong>{{ sourceLoading ? '正在读取画面尺寸…' : '尚未读取到画面尺寸' }}</strong>
              <p>读取后即可手动裁剪，无需检测黑边。</p>
              <button type="button" class="btn btn--secondary btn--sm" :disabled="sourceLoading || preflightLoading || analyzing" data-test="crop-read-source" @click="readSource">刷新画面</button>
            </div>
          </div>
          <p v-if="imageError" role="alert" class="edit-error crop-notice">预览画面读取失败，请换一个时间点或刷新画面。</p>
          <p v-if="sourceError" role="alert" class="edit-error crop-notice" data-test="crop-source-error">{{ sourceError }} <button type="button" class="btn-link" :disabled="sourceLoading" @click="readSource">重试</button></p>
          <div class="crop-timeline">
            <CropTimeline ref="timeline" :key="item.video_id" :video-id="item.video_id" :duration-ms="durationMs(item.video_id)" :at-ms="previewMS" :range="item.range || null" :source-version="source.source_version || ''" :editable="editable" :can-step="Boolean(navigateFrame && source.source_version)" :busy="frameBusy" :error="frameError" @seek="seek" @range="setRange" @step-frame="stepFrame" />
          </div>
        </div>
        <aside class="crop-inspector" aria-label="裁剪设置">
          <div class="crop-group">
            <h4>实时结果</h4>
            <div v-if="canvasWidth && canvasHeight && rectValid" class="crop-live-preview">
              <div class="crop-result crop-result--live" :style="{ aspectRatio: `${rect.width}/${rect.height}`, width: `${Math.min(180, 120 * rect.width / rect.height)}px` }" data-test="crop-live-result"><img :src="frameURL" :style="resultImageStyle" alt="实时裁剪结果" draggable="false" /></div>
            </div>
            <p v-if="canvasWidth && canvasHeight" class="crop-output-size" data-test="crop-output-size">
              <span>原画布 {{ canvasWidth }}×{{ canvasHeight }} → 输出 {{ rect.width }}×{{ rect.height }}</span>
              <span v-if="rectValid && cropPercent > 0" class="crop-output-badge">裁去 {{ cropPercent }}%</span>
            </p>
          </div>
          <div v-if="loupe && !imageError" class="crop-loupe-panel" :style="loupePanelStyle" data-test="crop-loupe">
            <span class="help-text">边界放大 · {{ handleLabels[loupe.handle] }}</span>
            <div class="crop-loupe-view"><img :src="frameURL" :style="loupeImageStyle" alt="裁剪边界局部放大" draggable="false" /><div class="crop-loupe-box" :style="loupeBoxStyle"></div></div>
          </div>
          <p v-if="detectionText" class="crop-detection-note" data-test="crop-detection-result"><AppIcon name="info" /><span>{{ detectionText }}</span></p>
          <div class="crop-group">
            <h4>画面比例</h4>
            <div class="crop-ratio-chips" role="group" aria-label="快捷画面比例">
              <button v-for="ratio in ratioOptions" :key="ratio.value" type="button" class="btn btn--secondary btn--sm crop-toggle" :disabled="!editable || !canvasWidth" :aria-pressed="(ratios[item.video_id] || 'free') === ratio.value" :title="ratio.label" @click="chooseRatio(ratio.value)">{{ ratio.value === 'free' ? '自由' : ratio.value }}</button>
            </div>
            <select :value="ratios[item.video_id] || 'free'" :disabled="!editable || !canvasWidth" class="select-input" aria-label="画面比例" data-test="crop-ratio" @change="chooseRatio($event.target.value)">
              <option v-for="ratio in ratioOptions" :key="ratio.value" :value="ratio.value">{{ ratio.label }}</option>
            </select>
          </div>
          <div v-if="canvasWidth && canvasHeight" class="crop-group">
            <h4>边距微调 <span>px</span></h4>
            <div class="crop-margins">
              <div v-for="side in sides" :key="side.key" class="crop-margin">
                <label>{{ side.label }}<input type="number" min="0" step="2" :value="margins[side.key]" :disabled="!editable" class="text-input" :aria-label="`${side.label}边裁切像素`" :data-test="`crop-margin-${side.key}`" @change="setMargin(side.key, $event.target.value)" /></label>
                <div class="crop-nudge">
                  <button type="button" class="btn btn--secondary btn--sm" :disabled="!editable" :aria-label="`${side.label}少裁2像素`" :data-test="`crop-nudge-${side.key}-less`" @click="nudgeMargin(side.key, -2)">−2</button>
                  <button type="button" class="btn btn--secondary btn--sm" :disabled="!editable" :aria-label="`${side.label}多裁2像素`" :data-test="`crop-nudge-${side.key}-more`" @click="nudgeMargin(side.key, 2)">+2</button>
                </div>
              </div>
            </div>
          </div>
          <div class="crop-group">
            <p v-if="item.rect && item.source_version !== source.source_version" class="edit-error">来源已经变化，请重新检测或设置裁剪框。</p>
            <div class="crop-inspector-actions">
              <button type="button" class="btn btn--secondary btn--sm" :disabled="!editable || !canvasWidth" data-test="crop-reset" @click="resetRect">重置为整幅画面</button>
              <button type="button" class="btn-link" :disabled="!editable || items.length <= 1" data-test="crop-remove" @click="removeItem">移出此项</button>
            </div>
            <p class="help-text">拖动调整裁剪框，悬停边缘查看放大细节。框外画面与内嵌画面字幕会被裁掉。</p>
            <p class="crop-shortcuts">,/. 逐帧 · [/] 出入点 · V 预览 · G 网格</p>
          </div>
        </aside>
      </template>
    </div>
  </section>
</template>

<script>
import CropTimeline from './CropTimeline.vue';
import AppIcon from '../ui/AppIcon.vue';
import ProgressBar from '../ui/ProgressBar.vue';
import { workbenchVideoMixin } from './workbenchVideo.js';
import { cloneRecipe, editErrorText, editFrameURL, formatEditTime, preflightSource } from '../../utils/videoEdit.js';
import { CROP_RATIOS, cropRatio, cropFromMargins, dragCrop, fitCropRatio, setCropRect, validCrop } from '../../utils/videoCrop.js';

export default {
  name: 'CropEditor', components: { AppIcon, ProgressBar, CropTimeline }, mixins: [workbenchVideoMixin],
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
    return { loadedSource: null, sourceLoading: false, sourceError: '', sourceSequence: 0, frameLoaded: false, loupe: null, selected: 0, zoom: 1, showGrid: true, frameBusy: false, frameError: '', frameSequence: 0, previewMode: 'source', times: {}, ratios: {}, drag: null, imageError: false, ratioOptions: CROP_RATIOS,
      handles: ['nw', 'n', 'ne', 'w', 'e', 'sw', 's', 'se'], handleLabels: { n: '上边', s: '下边', w: '左边', e: '右边', nw: '左上角', ne: '右上角', sw: '左下角', se: '右下角' },
      sides: [{ key: 'top', label: '上' }, { key: 'bottom', label: '下' }, { key: 'left', label: '左' }, { key: 'right', label: '右' }] };
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
    stageStyle() {
      const ratio = this.canvasWidth / this.canvasHeight;
      return { aspectRatio: `${this.canvasWidth}/${this.canvasHeight}`, width: this.zoom === 1 ? '100%' : `${Math.min(960, 336 * ratio) * this.zoom}px`, maxWidth: this.zoom === 1 ? `min(960px, calc(var(--crop-fit-height) * ${ratio}))` : 'none' };
    },
    boxStyle() { return { left: `${this.rect.x / this.canvasWidth * 100}%`, top: `${this.rect.y / this.canvasHeight * 100}%`, width: `${this.rect.width / this.canvasWidth * 100}%`, height: `${this.rect.height / this.canvasHeight * 100}%` }; },
    resultStyle() { return { aspectRatio: `${this.rect.width}/${this.rect.height}`, maxWidth: `min(960px, calc(var(--crop-fit-height) * ${this.rect.width / this.rect.height}))` }; },
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
    cropPercent() { return Number(((1 - this.rect.width * this.rect.height / (this.canvasWidth * this.canvasHeight)) * 100).toFixed(1)); },
    detectionStatus() {
      if (this.analyzing) return '检测中';
      if (this.analysis?.kind === 'crop' && this.analysis.status === 'failed') return '检测失败';
      if (this.analysis?.kind === 'crop' && this.analysis.status === 'cancelled') return '检测已取消';
      return { detected: '已应用建议', undetected: '未可靠识别', no_borders: '未发现黑边', confirmed: '保留已设置' }[this.detection?.status] || '未自动检测';
    },
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
    onKeydown(event) {
      if (['INPUT', 'SELECT', 'TEXTAREA'].includes(event.target.tagName) || event.metaKey || event.ctrlKey || event.altKey) return;
      switch (event.key.toLowerCase()) {
        case ',':
        case '.':
          if (!this.navigateFrame || !this.source.source_version || this.frameBusy) return;
          this.stepFrame(event.key === ',' ? -1 : 1);
          break;
        case 'v':
          this.previewMode = this.previewMode === 'source' ? 'result' : 'source';
          break;
        case 'g':
          this.showGrid = !this.showGrid;
          break;
        case '[':
        case ']':
          if (!this.item || !this.editable || this.durationMs(this.item.video_id) <= 0) return;
          this.$refs.timeline.setSide(event.key === '[' ? 'start' : 'end', this.previewMS);
          break;
        case 'r':
          if (!this.editable || !this.canvasWidth) return;
          this.resetRect();
          break;
        default:
          return;
      }
      event.preventDefault();
    },
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
        if (sequence === this.sourceSequence) this.sourceError = `读取画面尺寸失败：${editErrorText(err)}`;
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
.crop-editor { min-width: 0; overflow: hidden; border: 1px solid var(--hairline); border-radius: var(--radius); background: var(--panel-bg); }
.crop-toolbar, .crop-toolbar-title, .crop-toolbar-actions { display: flex; align-items: center; gap: var(--space-2); }
.crop-toolbar { justify-content: space-between; padding: var(--space-2) var(--space-3); border-bottom: 1px solid var(--hairline); }
.crop-toolbar-title { min-width: 0; }
.crop-toolbar h3 { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: var(--fs-body); }
.crop-status, .crop-output-badge { flex: none; padding: var(--space-1) var(--space-2); border-radius: var(--radius-pill); background: var(--panel-muted-bg); color: var(--text-secondary); font-size: var(--fs-caption); white-space: nowrap; }
.crop-status--detected, .crop-output-badge { background: var(--accent-soft); color: var(--accent-text); }
.crop-toolbar-actions { flex: none; }
.crop-analysis-progress { width: 132px; }
.crop-toggle[aria-pressed='true'] { border-color: var(--accent-border); background: var(--accent-soft); color: var(--accent-text); }
.crop-notice { padding-inline: var(--space-3); }

.crop-layout { display: grid; grid-template-columns: 184px minmax(0, 1fr) 264px; align-items: stretch; }
.crop-layout--single { grid-template-columns: minmax(0, 1fr) 264px; }
.crop-layout--single .crop-sources { display: none; }
.crop-sources, .crop-inspector { min-width: 0; padding: var(--space-3); }
.crop-sources { max-height: 620px; overflow-y: auto; border-right: 1px solid var(--hairline); background: var(--panel-subtle-bg); }
.crop-select-label { display: grid; gap: var(--space-1); margin-bottom: var(--space-3); color: var(--text-secondary); font-size: var(--fs-caption); font-weight: var(--fw-semibold); }
.crop-editor .select-input { height: var(--h-compact); font-size: var(--fs-caption); }
.crop-source { display: grid; grid-template-columns: max-content minmax(0, 1fr); align-items: center; column-gap: var(--space-2); width: 100%; margin-bottom: var(--space-1); padding: var(--space-2); text-align: left; border: 1px solid var(--hairline); border-radius: var(--radius-sm); background: var(--row-bg); }
.crop-source:hover { background: var(--row-hover-bg); }
.crop-source--active, .crop-source--active:hover { border-color: var(--accent-border); background: var(--accent-soft); }
.crop-source-number { grid-row: span 2; display: grid; place-items: center; min-width: var(--h-compact); height: var(--h-compact); padding-inline: var(--space-1); border-radius: var(--radius-pill); background: var(--panel-muted-bg); color: var(--text-secondary); font: var(--fs-caption) var(--font-mono); }
.crop-source--active .crop-source-number { background: var(--accent-color); color: var(--accent-on); }
.crop-source-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: var(--fs-secondary); font-weight: var(--fw-semibold); }
.crop-source-state { display: flex; align-items: center; gap: var(--space-1); color: var(--text-secondary); font-size: var(--fs-caption); }
.crop-source-state::before { content: ''; width: 6px; height: 6px; border-radius: var(--radius-pill); background: var(--text-muted); }
.crop-source-state--set::before { background: var(--success-color); }

.crop-workspace { min-width: 0; background: var(--bg-color); }
.crop-monitor-toolbar { display: grid; grid-template-columns: minmax(0, 1fr) auto minmax(0, 1fr); align-items: center; gap: var(--space-2); padding: var(--space-1) var(--space-3); color: var(--text-secondary); font-size: var(--fs-caption); }
.crop-monitor-zoom { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-1); }
.crop-monitor-zoom select { width: auto; max-width: 100%; }
.crop-mode-switch { display: flex; padding: var(--space-1); border-radius: var(--radius); background: var(--panel-muted-bg); }
.crop-mode-switch .btn[aria-pressed='true'] { background: var(--control-bg); color: var(--accent-text); box-shadow: var(--shadow-segment); }
.crop-monitor-readout { text-align: right; font-family: var(--font-mono); }
.crop-monitor { --crop-fit-height: 336px; display: grid; grid-template-columns: minmax(0, 1fr); place-items: safe center; height: 376px; padding: var(--space-3); overflow: auto; background: var(--player-bg); }
.crop-empty { display: grid; justify-items: center; gap: var(--space-2); text-align: center; color: var(--media-badge-fg); font-size: var(--fs-secondary); }
.crop-stage, .crop-result { position: relative; width: 100%; overflow: hidden; background: var(--player-bg); }
.crop-stage { touch-action: none; user-select: none; }
.crop-stage > img { display: block; width: 100%; height: 100%; }
.crop-result > img { position: absolute; max-width: none; }
.crop-box { position: absolute; border: 1px solid var(--accent-color); box-shadow: 0 0 0 2000px var(--overlay-strong); cursor: move; }
.crop-grid, .crop-box-size { position: absolute; pointer-events: none; }
.crop-grid { inset: 0; color: var(--media-badge-fg); opacity: 0.42; }
.crop-grid::before, .crop-grid::after { content: ''; position: absolute; }
.crop-grid::before { inset: 0 33.333%; border-inline: 1px solid currentColor; }
.crop-grid::after { inset: 33.333% 0; border-block: 1px solid currentColor; }
.crop-box--dragging .crop-grid { opacity: 0.62; }
.crop-box-size { top: var(--space-4); left: var(--space-4); padding: var(--space-1); border-radius: var(--radius-sm); background: var(--media-badge-bg); color: var(--media-badge-fg); font: var(--fs-caption) var(--font-mono); white-space: nowrap; opacity: 0; }
.crop-box:hover .crop-box-size, .crop-box--dragging .crop-box-size { opacity: 1; }
.crop-handle.btn-icon, .crop-handle.btn-icon:hover:not(:disabled) { position: absolute; width: 18px; height: 18px; padding: 0; border: 0; background: transparent; color: var(--media-badge-fg); }
.crop-handle::before { content: ''; position: absolute; pointer-events: none; }
.crop-handle--n { top: 0; left: calc(50% - 9px); cursor: ns-resize; }
.crop-handle--s { bottom: 0; left: calc(50% - 9px); cursor: ns-resize; }
.crop-handle--w { left: 0; top: calc(50% - 9px); cursor: ew-resize; }
.crop-handle--e { right: 0; top: calc(50% - 9px); cursor: ew-resize; }
.crop-handle--n::before, .crop-handle--s::before { left: 2px; right: 2px; height: 3px; border-radius: var(--radius-pill); background: currentColor; }
.crop-handle--w::before, .crop-handle--e::before { top: 2px; bottom: 2px; width: 3px; border-radius: var(--radius-pill); background: currentColor; }
.crop-handle--n::before { top: 0; }
.crop-handle--s::before { bottom: 0; }
.crop-handle--w::before { left: 0; }
.crop-handle--e::before { right: 0; }
.crop-handle--nw { left: 0; top: 0; cursor: nwse-resize; }
.crop-handle--ne { right: 0; top: 0; cursor: nesw-resize; }
.crop-handle--sw { left: 0; bottom: 0; cursor: nesw-resize; }
.crop-handle--se { right: 0; bottom: 0; cursor: nwse-resize; }
.crop-handle--nw::before, .crop-handle--ne::before, .crop-handle--sw::before, .crop-handle--se::before { width: var(--space-3); height: var(--space-3); border: solid currentColor; border-width: 0; }
.crop-handle--nw::before { top: 0; left: 0; border-top-width: 2px; border-left-width: 2px; }
.crop-handle--ne::before { top: 0; right: 0; border-top-width: 2px; border-right-width: 2px; }
.crop-handle--sw::before { bottom: 0; left: 0; border-bottom-width: 2px; border-left-width: 2px; }
.crop-handle--se::before { bottom: 0; right: 0; border-bottom-width: 2px; border-right-width: 2px; }

.crop-timeline { padding: var(--space-3); border-top: 1px solid var(--hairline); background: var(--panel-subtle-bg); }
.crop-inspector { display: flex; flex-direction: column; gap: var(--space-2); border-left: 1px solid var(--hairline); font-size: var(--fs-secondary); }
.crop-group { display: grid; gap: var(--space-2); }
.crop-group + .crop-group, .crop-detection-note + .crop-group { padding-top: var(--space-2); border-top: 1px solid var(--hairline); }
.crop-group h4 { display: flex; justify-content: space-between; font-size: var(--fs-secondary); }
.crop-group h4 span, .crop-shortcuts { color: var(--text-secondary); font: var(--fs-caption) var(--font-mono); }
.crop-live-preview { min-height: 120px; display: grid; place-items: center; padding: var(--space-1); border-radius: var(--radius-sm); background: var(--player-bg); }
.crop-output-size { display: flex; align-items: center; flex-wrap: wrap; gap: var(--space-1); color: var(--text-secondary); font: var(--fs-caption) var(--font-mono); }
.crop-detection-note { display: flex; gap: var(--space-2); padding: var(--space-2); border-radius: var(--radius-sm); background: var(--accent-soft); color: var(--accent-text); font-size: var(--fs-caption); }
.crop-ratio-chips { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: var(--space-1); }
.crop-ratio-chips .btn { padding-inline: var(--space-1); }
.crop-margins { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: var(--space-1); }
.crop-margin { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: var(--space-1); padding: var(--space-1); border: 1px solid var(--hairline); border-radius: var(--radius-sm); background: var(--panel-subtle-bg); }
.crop-margin label { display: grid; gap: var(--space-1); color: var(--text-secondary); font-size: var(--fs-caption); }
.crop-margin .text-input { min-width: 0; height: var(--h-compact); padding-inline: var(--space-1); font: var(--fs-caption) var(--font-mono); appearance: textfield; }
.crop-margin .text-input::-webkit-inner-spin-button { appearance: none; }
.crop-nudge { display: flex; align-items: end; gap: 2px; }
.crop-nudge .btn { padding-inline: var(--space-1); font-family: var(--font-mono); }
.crop-inspector-actions { display: flex; align-items: center; justify-content: space-between; gap: var(--space-2); }
.crop-shortcuts { line-height: 1.5; }
.crop-loupe-panel { position: fixed; width: 200px; padding: var(--space-2); z-index: var(--z-menu); pointer-events: none; background: var(--panel-bg); border: 1px solid var(--hairline); border-radius: var(--radius); box-shadow: var(--shadow-popover); }
.crop-loupe-view { position: relative; height: 130px; overflow: hidden; background: var(--player-bg); border: 1px solid var(--hairline); border-radius: var(--radius); margin-top: var(--space-1); }
.crop-loupe-view > img { position: absolute; max-width: none; }
.crop-loupe-box { position: absolute; border: 1px solid var(--accent-color); box-shadow: 0 0 0 2000px var(--overlay-strong); }

@media (max-width: 900px) {
  .crop-layout { grid-template-columns: minmax(0, 1fr) 264px; }
  .crop-sources { grid-column: 1 / -1; display: grid; grid-template-columns: repeat(auto-fit, minmax(148px, 1fr)); align-content: start; gap: var(--space-2); max-height: 124px; border-right: 0; border-bottom: 1px solid var(--hairline); }
  .crop-select-label { grid-column: 1 / -1; margin: 0; }
  .crop-source { margin: 0; }
}

@media (max-width: 640px) {
  .crop-toolbar { flex-wrap: wrap; }
  .crop-toolbar-actions { flex-wrap: wrap; max-width: 100%; margin-left: auto; }
  .crop-layout { grid-template-columns: minmax(0, 1fr); }
  .crop-monitor { --crop-fit-height: 226px; height: 250px; }
  .crop-inspector { border-left: 0; border-top: 1px solid var(--hairline); }
}
</style>
