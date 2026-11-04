<template>
  <Teleport to="body">
    <DialogShell title="图片处理" size="full" class="image-tools" data-test="image-tools" @close="close">
      <div class="image-tools__workspace">
        <section ref="stage" class="image-tools__stage">
          <p v-if="loading" role="status">正在读取原图…</p>
          <div v-else-if="previewURL" class="image-tools__picture" :style="pictureStyle" @pointerdown="cropStart" @pointermove="cropMove" @pointerup="cropEnd" @pointercancel="cropEnd">
            <img :src="previewURL" :alt="image.name" draggable="false" data-test="image-tools-preview" />
            <div v-if="selecting" class="image-tools__crop" :style="cropStyle" />
          </div>
          <p v-if="selecting" class="help-text">在画面上拖动选择{{ mode === 'enhance' ? '试处理区域（最大1024×1024）' : '裁剪范围' }}</p>
        </section>
        <aside class="image-tools__controls">
          <strong>{{ image.name }}</strong>
          <div class="image-tools__tabs">
            <button v-for="tab in tabs" :key="tab.value" type="button" class="btn btn--secondary btn--sm" :aria-pressed="mode === tab.value" :disabled="busy" @click="mode = tab.value">{{ tab.label }}</button>
          </div>
          <AppBanner v-if="error" tone="danger" :title="error" />
          <template v-if="mode === 'edit'">
            <div class="image-tools__buttons">
              <button type="button" class="btn btn--secondary btn--sm" :disabled="busy || !sourceImage" @click="autoImprove">一键改善</button>
              <button type="button" class="btn btn--secondary btn--sm" :disabled="busy || historyIndex < 1" @click="undo(-1)">撤销</button>
              <button type="button" class="btn btn--secondary btn--sm" :disabled="busy || historyIndex >= history.length - 1" @click="undo(1)">重做</button>
              <button type="button" class="btn btn--ghost btn--sm" :disabled="busy" @click="restore">恢复原图</button>
            </div>
            <div class="image-tools__buttons">
              <button type="button" class="btn btn--secondary btn--sm" :disabled="busy" @click="params.rotation = (params.rotation + 1) % 4; commit()">旋转90°</button>
              <button type="button" class="btn btn--secondary btn--sm" :aria-pressed="params.flipX" :disabled="busy" @click="params.flipX = !params.flipX; commit()">水平镜像</button>
              <button type="button" class="btn btn--secondary btn--sm" :aria-pressed="params.flipY" :disabled="busy" @click="params.flipY = !params.flipY; commit()">垂直镜像</button>
              <button type="button" class="btn btn--secondary btn--sm" :aria-pressed="cropActive" :disabled="busy" @click="cropActive = !cropActive">{{ cropActive ? '查看裁剪结果' : '裁剪画面' }}</button>
            </div>
            <label v-for="control in controls" :key="control.key" class="image-tools__slider">{{ control.label }} <output>{{ params[control.key] }}</output>
              <input v-model.number="params[control.key]" type="range" :min="control.min" :max="control.max" :step="control.step || 1" :disabled="busy" :data-test="`image-edit-${control.key}`" @change="commit" />
            </label>
            <button type="button" class="btn btn--secondary" :disabled="busy" @pointerdown.prevent="showOriginal = true" @pointerup="showOriginal = false" @pointerleave="showOriginal = false" @keydown.space.prevent="showOriginal = true" @keyup.space.prevent="showOriginal = false" @blur="showOriginal = false">按住查看原图</button>
            <p class="help-text">导出原始分辨率的裁剪结果，格式为静态8位 sRGB PNG；动画取打开时的一帧。相机元数据与原有色彩配置留在原文件中。</p>
            <button type="button" class="btn btn--primary" :disabled="busy || !sourceImage" data-test="image-edit-export" @click="exportEdit">另存编辑结果…</button>
          </template>
          <template v-else-if="mode === 'compress'">
            <p class="help-text">严格无损优化PNG与JPEG，保留像素、透明度和原有元数据；不调整质量或尺寸。是否能变小取决于原文件。</p>
            <button type="button" class="btn btn--primary" :disabled="busy || !source" data-test="image-compress-start" @click="process('compress')">试压缩并对比</button>
          </template>
          <template v-else>
            <p class="help-text">对原图选区先试增强，按相同显示尺寸比较细节；满意后再处理整张。</p>
            <p v-if="enhanceCapability && !enhanceCapability.available" class="help-text" role="status">{{ enhanceCapability.hint || enhanceCapability.message }}</p>
            <label>内容类型<select v-model="profile" class="select-input" :disabled="busy"><option value="general">普通真人</option><option value="anime">动漫</option></select></label>
            <p class="help-text">当前选区：{{ selectedPixels.width }} × {{ selectedPixels.height }}；固定2×放大，使用本机模型。</p>
            <button type="button" class="btn btn--primary" :disabled="busy || !sourceImage || !enhanceCapability?.available" data-test="image-enhance-region" @click="process('enhance', false)">{{ enhanceCapability ? '试处理选区' : '正在检查本机增强能力…' }}</button>
            <button type="button" class="btn btn--secondary" :disabled="busy || !trialReady" data-test="image-enhance-full" @click="process('enhance', true)">增强整张图片</button>
          </template>
          <p v-if="busy" role="status">{{ exporting ? '正在生成完整图片…' : result?.state === 'queued' ? '等待处理资源…' : '正在处理…' }}<button type="button" class="btn btn--ghost btn--sm" @click="cancel">取消</button></p>
          <template v-if="result?.state === 'ready' && result.result_url">
            <p class="help-text">{{ resultLabel }} · {{ result.width }} × {{ result.height }} · {{ bytes(result.output_bytes) }}<template v-if="resultOperation === 'compress'">，比原图减少 {{ bytes(Math.max(0, result.source_bytes - result.output_bytes)) }}</template></p>
            <button type="button" class="btn btn--secondary" @click="compareResult">与原图对比</button>
            <button type="button" class="btn btn--primary" :disabled="saving" @click="saveResult">另存当前结果…</button>
          </template>
          <p v-if="result?.state === 'not_smaller'" role="status">没有进一步无损压缩空间，原图保持不变。</p>
          <p v-if="savedPath" role="status">副本已保存，原图仍保留。<template v-if="mode === 'compress'">请先确认副本已扫描入库，再在清理审阅中设为保留；可沿用原合并功能保留收藏、标签等整理信息。</template><button v-if="mode === 'compress' && compared" type="button" class="btn btn--ghost btn--sm" @click="reviewSource">打开图片清理审阅</button></p>
        </aside>
      </div>
    </DialogShell>
    <ImageCompareDialog v-if="comparison" :left="comparison.left" :right="comparison.right" :same-display-size="resultOperation === 'enhance'" @close="comparison = null" />
  </Teleport>
</template>

<script>
import { markRaw } from 'vue';
import { GetEnhancementCapability, SaveMediaProcessing, SetMediaProcessingDirty } from '../../wailsjs/go/main/App';
import DialogShell from './ui/DialogShell.vue';
import AppBanner from './ui/AppBanner.vue';
import ImageCompareDialog from './ImageCompareDialog.vue';
import { confirmAction } from '../utils/feedback.js';
import { formatBytes } from '../utils/mediaDetails.js';
import { createMediaProcessingController, mediaProcessingError } from '../utils/mediaProcessing.js';
import { automaticImageAdjustment, cloneImageRecipe, originalImageRecipe, renderEditedImage } from '../utils/imageEditing.js';
import { registerImageEditor } from '../utils/imageEditingGuard.js';

export default {
  name: 'ImageToolsDialog', components: { DialogShell, AppBanner, ImageCompareDialog },
  props: { image: { type: Object, required: true }, initialMode: { type: String, default: 'edit' } }, emits: ['close', 'review-source'],
  data() {
    const params = originalImageRecipe();
    return { mode: this.initialMode, params, savedRecipe: JSON.stringify(params), history: [cloneImageRecipe(params)], historyIndex: 0, dirtyRevision: 0,
      source: null, sourceImage: null, enhanceCapability: null, loading: true, error: '', previewURL: '', previewWidth: 1, previewHeight: 1, stageWidth: 640, stageHeight: 480, cropActive: false, selection: { x: 0.25, y: 0.25, width: 0.5, height: 0.5 }, drag: null,
      showOriginal: false, result: null, resultOperation: '', fullResult: false, profile: 'general', resultProfile: '', exporting: false, starting: false, saving: false, savedPath: '', comparison: null, compared: false, renderGeneration: 0,
      tabs: [{ value: 'edit', label: '编辑' }, { value: 'compress', label: '无损压缩' }, { value: 'enhance', label: '画质提升' }],
      controls: [{ key: 'straighten', label: '拉直', min: -15, max: 15, step: 0.1 }, { key: 'exposure', label: '曝光', min: -2, max: 2, step: 0.1 }, { key: 'shadows', label: '阴影', min: 0, max: 100 }, { key: 'contrast', label: '对比度', min: -100, max: 100 }, { key: 'temperature', label: '色温', min: -100, max: 100 }, { key: 'saturation', label: '饱和度', min: -100, max: 100 }] };
  },
  computed: {
    dirty() { return JSON.stringify(this.params) !== this.savedRecipe; },
    busy() { return this.exporting || this.starting || this.saving || ['queued', 'running'].includes(this.result?.state); },
    selecting() { return !this.showOriginal && (this.mode === 'enhance' || this.mode === 'edit' && this.cropActive); },
    pictureStyle() { const scale = Math.max(0.001, Math.min(Math.max(1, this.stageWidth - 40) / this.previewWidth, Math.max(1, this.stageHeight - (this.selecting ? 80 : 40)) / this.previewHeight)); return { width: `${this.previewWidth * scale}px`, height: `${this.previewHeight * scale}px` }; },
    activeSelection() { return this.mode === 'enhance' ? this.selection : this.params.crop; },
    cropStyle() { const s = this.activeSelection; return { left: `${s.x * 100}%`, top: `${s.y * 100}%`, width: `${s.width * 100}%`, height: `${s.height * 100}%` }; },
    selectedPixels() { const s = this.selection, width = this.sourceImage?.width || 1, height = this.sourceImage?.height || 1; const x = Math.min(width - 1, Math.floor(s.x * width)), y = Math.min(height - 1, Math.floor(s.y * height)); return { x, y, width: Math.max(1, Math.min(width - x, Math.round(s.width * width))), height: Math.max(1, Math.min(height - y, Math.round(s.height * height))) }; },
    trialReady() { return this.result?.state === 'ready' && this.resultOperation === 'enhance' && !this.fullResult && this.resultProfile === this.profile; },
    resultLabel() { return this.resultOperation === 'compress' ? '无损压缩结果' : this.resultOperation === 'edit' ? '编辑结果' : this.fullResult ? '整图增强结果' : '选区增强结果'; }
  },
  watch: {
    params: { deep: true, handler() { this.queueRender(); } },
    mode() { this.showOriginal = false; this.queueRender(); if (this.mode === 'enhance') this.loadEnhancementCapability(); }, cropActive() { this.queueRender(); }, showOriginal() { this.queueRender(); },
    dirty() { this.markDirty(); }
  },
  async mounted() {
    this._unregisterLeave = registerImageEditor(() => this.close());
    this._sourceController = createMediaProcessingController(() => {});
    this._controller = createMediaProcessingController(view => { this.result = view; if (view.state === 'failed') this.error = mediaProcessingError(view.error); if (view.state === 'ready' && this.resultOperation === 'edit' && this._autoSaveEdit) { this._autoSaveEdit = false; this.saveResult(); } });
    window.addEventListener('blur', this.restoreOriginal);
    this.measureStage(); window.addEventListener('resize', this.measureStage);
    if (typeof ResizeObserver === 'function') { this._stageResize = new ResizeObserver(() => this.measureStage()); this._stageResize.observe(this.$refs.stage); }
    if (this.mode === 'enhance') this.loadEnhancementCapability();
    try {
      const source = await this._sourceController.start({ kind: 'image', media_id: Number(this.image.id), operation: 'inspect' });
      if (!source) return; this.source = source;
      if (Math.max(source.width, source.height) > 16384) throw new Error('原图超出编辑画布边长上限，仍可尝试严格无损压缩');
      const image = new Image(); this._loadingImage = image;
      await new Promise((resolve, reject) => { this._cancelLoad = () => reject(new Error('读取已取消')); image.onload = resolve; image.onerror = () => reject(new Error('原图无法解码，暂不能在这里处理此格式')); image.src = source.source_url; });
      this._cancelLoad = null;
      if (this._closed) return;
      if (image.naturalWidth * image.naturalHeight > 50_000_000) throw new Error('图片超过5000万像素处理上限');
      const frozen = document.createElement('canvas'); frozen.width = image.naturalWidth; frozen.height = image.naturalHeight; frozen.getContext('2d', { colorSpace: 'srgb' }).drawImage(image, 0, 0); this.sourceImage = markRaw(frozen); image.removeAttribute('src');
      const width = Math.min(512, frozen.width), height = Math.min(512, frozen.height);
      this.selection = { x: (frozen.width - width) / (2 * frozen.width), y: (frozen.height - height) / (2 * frozen.height), width: width / frozen.width, height: height / frozen.height };
      await this.renderPreview(); this.markDirty();
    } catch (error) { if (!this._closed) this.error = mediaProcessingError(error); } finally { this.loading = false; }
  },
  beforeUnmount() { this._closed = true; this.renderGeneration++; this._unregisterLeave?.(); this._stageResize?.disconnect(); window.removeEventListener('resize', this.measureStage); clearTimeout(this._renderTimer); window.removeEventListener('blur', this.restoreOriginal); this._cancelLoad?.(); this._loadingImage?.removeAttribute('src'); if (this.sourceImage) this.sourceImage.width = this.sourceImage.height = 1; this._controller?.close().catch(() => {}); this._sourceController?.close().catch(() => {}); },
  methods: {
    bytes: formatBytes,
    measureStage() { const rect = this.$refs.stage?.getBoundingClientRect(); if (rect?.width > 0 && rect?.height > 0) { this.stageWidth = rect.width; this.stageHeight = rect.height; } },
    async loadEnhancementCapability() { if (this.enhanceCapability || this._capabilityLoading) return; this._capabilityLoading = true; try { const capability = await GetEnhancementCapability(); if (!this._closed) this.enhanceCapability = capability; } catch (error) { if (!this._closed) this.error = '读取本机增强能力失败：' + error; } finally { this._capabilityLoading = false; } },
    restoreOriginal() { this.showOriginal = false; },
    markDirty() { if (this.source) SetMediaProcessingDirty(this.source.id, ++this.dirtyRevision, this.dirty).catch(error => { if (!this._closed) this.error = mediaProcessingError(error); }); },
    commit() { const snapshot = cloneImageRecipe(this.params); if (JSON.stringify(snapshot) === JSON.stringify(this.history[this.historyIndex])) return; this.history = [...this.history.slice(0, this.historyIndex + 1), snapshot].slice(-100); this.historyIndex = this.history.length - 1; },
    undo(direction) { this.historyIndex += direction; this.params = cloneImageRecipe(this.history[this.historyIndex]); },
    restore() { this.params = originalImageRecipe(); this.commit(); },
    autoImprove() { const canvas = document.createElement('canvas'); canvas.width = canvas.height = 64; const context = canvas.getContext('2d', { willReadFrequently: true }); context.drawImage(this.sourceImage, 0, 0, 64, 64); Object.assign(this.params, automaticImageAdjustment(context.getImageData(0, 0, 64, 64).data)); this.commit(); },
    queueRender() { clearTimeout(this._renderTimer); this._renderTimer = setTimeout(() => this.renderPreview(), 100); },
    async renderPreview() {
      if (!this.sourceImage || this._closed) return; const ticket = ++this.renderGeneration;
      const params = this.showOriginal || this.mode !== 'edit' ? originalImageRecipe() : cloneImageRecipe(this.params);
      if (this.cropActive) params.crop = originalImageRecipe().crop;
      try { const canvas = await renderEditedImage(this.sourceImage, params, 1600, () => !this._closed && ticket === this.renderGeneration); if (ticket === this.renderGeneration) { this.previewWidth = canvas.width; this.previewHeight = canvas.height; this.previewURL = canvas.toDataURL('image/png'); } canvas.width = canvas.height = 1; } catch (error) { if (ticket === this.renderGeneration && !this._closed) this.error = mediaProcessingError(error); }
    },
    cropPoint(event) { const rect = event.currentTarget.getBoundingClientRect(); return { x: Math.max(0, Math.min(1, (event.clientX - rect.left) / rect.width)), y: Math.max(0, Math.min(1, (event.clientY - rect.top) / rect.height)) }; },
    cropStart(event) { if (!this.selecting || this.busy || event.button !== 0) return; this.drag = { ...this.cropPoint(event), pointer: event.pointerId }; event.currentTarget.setPointerCapture(event.pointerId); event.preventDefault(); },
    cropMove(event) { if (!this.drag || this.drag.pointer !== event.pointerId) return; const point = this.cropPoint(event), value = { x: Math.min(this.drag.x, point.x), y: Math.min(this.drag.y, point.y), width: Math.max(0.001, Math.abs(point.x - this.drag.x)), height: Math.max(0.001, Math.abs(point.y - this.drag.y)) }; if (this.mode === 'enhance') this.selection = value; else this.params.crop = value; },
    cropEnd() { if (this.drag && this.mode === 'edit') this.commit(); this.drag = null; },
    async discardResult() { if (this.result?.id) await this._controller.release(this.result.id); this.result = null; this.savedPath = ''; this.compared = false; },
    async process(operation, full = false) {
      if (!this.source || this.busy) return; this.error = ''; this.starting = true;
      try { await this.discardResult(); this.resultOperation = operation; this.fullResult = full; this.resultProfile = this.profile;
        await this._controller.start({ kind: 'image', media_id: Number(this.image.id), operation, parent_id: this.source.id, profile: this.profile, ...(operation === 'enhance' && !full ? this.selectedPixels : {}) });
      } catch (error) { this.error = mediaProcessingError(error); } finally { this.starting = false; }
    },
    async exportEdit() {
      if (!this.sourceImage || this.busy) return; this.error = ''; this.exporting = true; const recipe = cloneImageRecipe(this.params); const ticket = this._exportGeneration = (this._exportGeneration || 0) + 1;
      try { await this.discardResult(); const canvas = await renderEditedImage(this.sourceImage, recipe, 0, () => !this._closed && ticket === this._exportGeneration); if (this._closed || ticket !== this._exportGeneration) { canvas.width = canvas.height = 1; return; } const encoded = canvas.toDataURL('image/png').split(',')[1]; canvas.width = canvas.height = 1;
        this.resultOperation = 'edit'; this._exportRecipe = JSON.stringify(recipe); this._autoSaveEdit = true; await this._controller.start({ kind: 'image', media_id: Number(this.image.id), operation: 'edit', parent_id: this.source.id, png_base64: encoded });
      } catch (error) { if (!this._closed) this.error = mediaProcessingError(error); } finally { this.exporting = false; }
    },
    compareResult() { if (!this.result?.result_url) return; this.compared = true; const source = this.result.comparison_url ? { name: '原图选区', asset_url: this.result.comparison_url, width: this.result.width / 2, height: this.result.height / 2 } : { ...this.image, asset_url: this.source.source_url }; this.comparison = { left: source, right: { name: this.resultLabel, width: this.result.width, height: this.result.height, asset_url: this.result.result_url } }; },
    async saveResult() { if (!this.result?.result_url || this.saving) return; this.saving = true; try { const path = await SaveMediaProcessing(this.result.id); if (path) { this.savedPath = path; if (this.resultOperation === 'edit') this.savedRecipe = this._exportRecipe; } } catch (error) { this.error = mediaProcessingError(error); } finally { this.saving = false; } },
    async cancel() { this._exportGeneration = (this._exportGeneration || 0) + 1; this.exporting = false; this._controller.invalidate(); try { await this.discardResult(); } catch (error) { this.error = mediaProcessingError(error); } },
    async reviewSource() { if (await this.close()) this.$emit('review-source', this.image); },
    async close() { if (this.dirty && !await confirmAction({ title: '关闭图片处理', message: '放弃尚未导出的图片修改？', confirmText: '放弃修改' })) return false; try { this._closed = true; this.renderGeneration++; await this._controller.close(); await this._sourceController.close(); this._unregisterLeave?.(); this.$emit('close'); return true; } catch (error) { this._closed = false; this.error = '处理尚未停止，请重试关闭：' + error; return false; } }
  }
};
</script>

<style scoped>
:deep(.image-tools .ui-dialog__body) { padding: 0; display: flex; overflow: hidden; }
.image-tools__workspace { display: grid; grid-template-columns: minmax(0, 1fr) 320px; flex: 1; min-height: 0; min-width: 0; }
.image-tools__stage { display: flex; flex-direction: column; justify-content: center; align-items: center; padding: 20px; min-height: 0; min-width: 0; background: var(--overlay-strong); overflow: auto; }
.image-tools__picture { position: relative; flex: 0 0 auto; touch-action: none; user-select: none; }
.image-tools__picture > img { display: block; width: 100%; height: 100%; object-fit: contain; pointer-events: none; }
.image-tools__crop { position: absolute; border: 2px solid var(--accent-color); box-shadow: 0 0 0 9999px var(--overlay-bg); pointer-events: none; }
.image-tools__controls { padding: 16px; display: flex; flex-direction: column; gap: 14px; overflow: auto; min-width: 0; }
.image-tools__controls > strong { overflow-wrap: anywhere; }
.image-tools__tabs, .image-tools__buttons { display: flex; flex-wrap: wrap; gap: 6px; }
.image-tools__slider { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 6px; font-size: var(--fs-secondary); }
.image-tools__slider input { width: 100%; accent-color: var(--accent-color); }
@media (max-width: 760px) { .image-tools__workspace { grid-template-columns: 1fr; grid-template-rows: minmax(180px, 1fr) minmax(0, 45%); } }
</style>
