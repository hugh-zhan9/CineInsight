<template>
  <Teleport to="body">
    <DialogShell class="image-source-dialog" size="xl" :title="image.name || '图片详情'" @close="$emit('close')">
      <div class="image-source-dialog__stage">
        <img v-if="!failed" :key="image.id" :src="`/preview/image/${image.id}`" :alt="image.name" @error="failed = true" />
        <p v-else role="status">无法加载原图，文件可能离线、已移动或格式不受支持。</p>
      </div>
      <p class="image-source-dialog__path">{{ image.path }}</p>
      <p class="image-source-dialog__size">{{ formatBytes(image.size) }}</p>
      <WallpaperButton kind="image" :media-id="Number(image.id)" />
      <p v-if="error || actionError" class="image-source-dialog__error" role="alert">{{ error || actionError }}</p>
      <template #footer>
        <button type="button" class="btn btn--secondary" data-test="image-source-directory" @click="reveal">在访达中显示</button>
        <button v-if="allowUnlink" type="button" class="btn btn--secondary" :disabled="busy" data-test="image-source-unlink" @click="$emit('unlink', image)">{{ busy ? '处理中…' : '解除人物关联' }}</button>
        <button v-if="allowDelete" type="button" class="btn btn--danger" :disabled="busy" data-test="image-source-delete" @click="$emit('delete', image)">删除图片…</button>
      </template>
    </DialogShell>
  </Teleport>
</template>
<script>
import DialogShell from './ui/DialogShell.vue';
import WallpaperButton from './WallpaperButton.vue';
import { RevealImage } from '../../wailsjs/go/main/App';
import { formatBytes } from '../utils/mediaDetails.js';
// 原图预览（人物页、详情抽屉、人脸来源共用）。Esc 与右上角 × 由 DialogShell 处理：只关最上面一层，
// 叠在上面的确认框或删除对话框先收到 Esc。「删除图片…」交给宿主弹删除对话框，在那里选移到废纸篓还是从图片库移除。
export default {
  name: 'ImageSourceDialog', components: { DialogShell, WallpaperButton },
  props: { image: { type: Object, required: true }, allowUnlink: Boolean, allowDelete: Boolean, busy: Boolean, actionError: { type: String, default: '' } },
  emits: ['close', 'unlink', 'delete'],
  data: () => ({ failed: false, error: '' }),
  watch: { 'image.id'() { this.failed = false; this.error = ''; } },
  methods: {
    formatBytes,
    async reveal() {
      const id = this.image.id; this.error = '';
      try { await RevealImage(Number(id)); }
      catch (err) { if (this.image.id === id) this.error = `在访达中显示失败：${err}`; }
    }
  }
};
</script>
<style scoped>
.image-source-dialog__stage { display: grid; place-items: center; min-height: 160px; margin-bottom: var(--space-3); border-radius: var(--radius); background: var(--thumb-bg); overflow: hidden; }
.image-source-dialog__stage img { max-width: 100%; max-height: 62vh; object-fit: contain; }
.image-source-dialog__stage p { margin: 0; padding: var(--space-4); color: var(--text-secondary); font-size: var(--fs-secondary); text-align: center; }
.image-source-dialog__path { margin: 0 0 var(--space-1); color: var(--text-muted); font-family: var(--font-mono); font-size: var(--fs-caption); overflow-wrap: anywhere; }
.image-source-dialog__size { margin: 0 0 var(--space-3); color: var(--text-secondary); font-size: var(--fs-secondary); }
.image-source-dialog__error { margin: var(--space-3) 0 0; color: var(--danger-text); font-size: var(--fs-secondary); }
</style>
