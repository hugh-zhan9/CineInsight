<template>
  <Teleport to="body">
    <DialogShell title="图片对比" size="full" class="image-compare" data-test="image-compare" @close="$emit('close')">
      <div class="image-compare__toolbar">
        <button type="button" class="btn btn--secondary btn--sm" :aria-pressed="linked" data-test="image-compare-link" @click="linked = !linked">{{ linked ? '同步缩放与拖动' : '分别查看' }}</button>
        <button type="button" class="btn btn--secondary btn--sm" data-test="image-compare-reset" @click="reset">适应窗口</button>
        <span class="help-text">滚轮缩放 · 拖动检查细节 · 双击原始尺寸</span>
      </div>
      <div class="image-compare__panes">
        <section v-for="(image, index) in [left, right]" :key="index" class="image-compare__pane">
          <header><strong>{{ image.name }}</strong><small>{{ image.width }} × {{ image.height }}</small></header>
          <div class="image-compare__canvas">
            <ImageViewport v-if="!failed[index]" :src="image.asset_url || `/preview/image/${Number(image.id)}`" :name="image.name"
              :image-width="Number(image.width) || 1" :image-height="Number(image.height) || 1" :max-scale="comparisonScale(image)" :model-value="linked ? sharedView : views[index]"
              @update:model-value="setView(index, $event)" @error="failed[index] = true" />
            <div v-else class="image-compare__error" role="status">这张图片没有加载出来。<button type="button" class="btn btn--secondary" @click="failed[index] = false">重试</button></div>
          </div>
        </section>
      </div>
    </DialogShell>
  </Teleport>
</template>
<script>
import DialogShell from './ui/DialogShell.vue';
import ImageViewport from './ImageViewport.vue';
import { initialImageView } from '../utils/imageViewport.js';
export default {
  name: 'ImageCompareDialog', components: { DialogShell, ImageViewport },
  props: { left: { type: Object, required: true }, right: { type: Object, required: true }, sameDisplaySize: Boolean }, emits: ['close'],
  data: () => ({ linked: true, sharedView: initialImageView(), views: [initialImageView(), initialImageView()], failed: [false, false], activeSide: 0 }),
  watch: {
    left() { this.reset(); this.failed = [false, false]; }, right() { this.reset(); this.failed = [false, false]; },
    linked(value) { if (value) this.sharedView = { ...this.views[this.activeSide] }; else this.views = [{ ...this.sharedView }, { ...this.sharedView }]; }
  },
  methods: {
    comparisonScale(image) { if (!this.sameDisplaySize) return 1; const width = Math.min(Number(this.left.width) || 1, Number(this.right.width) || 1), height = Math.min(Number(this.left.height) || 1, Number(this.right.height) || 1); return Math.min(1, width / (Number(image.width) || 1), height / (Number(image.height) || 1)); },
    setView(index, view) { this.activeSide = index; if (this.linked) { this.sharedView = view; this.views = [{ ...view }, { ...view }]; } else this.views[index] = view; },
    reset() { this.sharedView = initialImageView(); this.views = [initialImageView(), initialImageView()]; }
  }
};
</script>
<style scoped>
:deep(.image-compare .ui-dialog__body) { display: flex; flex-direction: column; padding: 0; overflow: hidden; }
.image-compare__toolbar { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-3); padding: var(--space-3); }
.image-compare__panes { flex: 1; min-height: 0; display: grid; grid-template-columns: 1fr 1fr; gap: 1px; background: var(--hairline-faint); }
.image-compare__pane { min-width: 0; min-height: 0; display: flex; flex-direction: column; background: var(--bg-color); }
.image-compare__pane > header { display: flex; gap: var(--space-3); padding: var(--space-2) var(--space-3); min-width: 0; }
.image-compare__pane strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: var(--fs-secondary); }
.image-compare__pane small { flex-shrink: 0; color: var(--text-muted); }
.image-compare__canvas { flex: 1; min-height: 0; background: var(--overlay-strong); }
.image-compare__error { display: grid; gap: var(--space-3); place-content: center; height: 100%; }
</style>
