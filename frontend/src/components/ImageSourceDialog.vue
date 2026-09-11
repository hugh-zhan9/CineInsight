<template>
  <Teleport to="body">
    <div class="image-source-layer">
    <BaseModal class="image-source-dialog" aria-label="图片详情" @close="$emit('close')">
      <div class="image-source-dialog__header">
        <h3>{{ image.name }}</h3>
        <button type="button" class="btn-secondary btn-compact" @click="$emit('close')">关闭</button>
      </div>
      <div class="image-source-dialog__stage">
        <img v-if="!failed" :key="image.id" :src="`/preview/image/${image.id}`" :alt="image.name" @error="failed = true" />
        <p v-else role="status">无法加载原图，文件可能离线、已移动或格式不受支持。</p>
      </div>
      <p class="image-source-dialog__path">{{ image.path }}</p>
      <p>{{ formatBytes(image.size) }}</p>
      <p v-if="error || actionError" role="alert">{{ error || actionError }}</p>
      <div class="image-source-dialog__actions">
        <button type="button" class="btn-secondary" data-test="image-source-directory" @click="reveal">打开所在目录</button>
        <button v-if="allowUnlink" type="button" class="btn-secondary" :disabled="busy" data-test="image-source-unlink" @click="$emit('unlink', image)">{{ busy ? '处理中…' : '解除人物关联' }}</button>
      </div>
    </BaseModal>
    </div>
  </Teleport>
</template>
<script>
import BaseModal from './ui/BaseModal.vue';
import { RevealImage } from '../../wailsjs/go/main/App';
import { formatBytes } from '../utils/mediaDetails.js';
import { feedbackState, resolveConfirm } from '../utils/feedback.js';
export default {
  name: 'ImageSourceDialog', components: { BaseModal },
  props: { image: { type: Object, required: true }, allowUnlink: Boolean, busy: Boolean, actionError: { type: String, default: '' } },
  emits: ['close', 'unlink'],
  data: () => ({ failed: false, error: '' }),
  watch: { 'image.id'() { this.failed = false; this.error = ''; } },
  mounted() { window.addEventListener('keydown', this.escape, true); },
  beforeUnmount() { window.removeEventListener('keydown', this.escape, true); },
  methods: {
    formatBytes,
    escape(event) {
      if (event.key !== 'Escape') return;
      // A confirmation above this preview owns Escape until it is answered.
      if (feedbackState.confirm) { event.preventDefault(); event.stopImmediatePropagation(); resolveConfirm(false); return; }
      event.preventDefault(); event.stopImmediatePropagation(); this.$emit('close');
    },
    async reveal() {
      const id = this.image.id; this.error = '';
      try { await RevealImage(Number(id)); }
      catch (err) { if (this.image.id === id) this.error = `打开目录失败：${err}`; }
    }
  }
};
</script>
<style scoped>
.image-source-layer { position: fixed; inset: 0; z-index: 1300; }
:deep(.image-source-dialog) { width: min(1100px, 94vw); max-width: 94vw; max-height: 92vh; display: flex; flex-direction: column; gap: 10px; overflow: auto; }
.image-source-dialog__header,.image-source-dialog__actions { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.image-source-dialog__header h3 { margin: 0; overflow-wrap: anywhere; }
.image-source-dialog__stage { display: grid; place-items: center; min-height: 160px; background: var(--thumb-bg); }
.image-source-dialog__stage img { max-width: 100%; max-height: 65vh; object-fit: contain; }
.image-source-dialog__path { overflow-wrap: anywhere; color: var(--text-muted); }
</style>
