<template>
  <Teleport to="body">
    <div class="person-media-delete-layer">
      <BaseModal aria-label="确认删除媒体" data-test="person-media-delete-dialog" @close="close">
        <h3>删除{{ kindLabel }}</h3>
        <p>{{ target.media.name }} · {{ formatBytes(target.media.size) }}</p>
        <p class="person-media-delete-path">{{ target.media.path }}</p>
        <p>删除后，该{{ kindLabel }}会从片库及所有人物的活跃作品中移除。</p>
        <label class="person-media-delete-option"><input v-model="deleteFile" type="checkbox" :disabled="busy" data-test="person-media-delete-file" />同时将原始文件移入回收站</label>
        <p>不勾选时仅移除片库记录，原文件保留；勾选后可从回收站恢复。</p>
        <p v-if="error" role="alert">{{ error }}</p>
        <div class="modal-actions">
          <button type="button" class="btn-danger" :disabled="busy" data-test="person-media-delete-confirm" @click="confirm">{{ busy ? '删除中…' : '确认删除' }}</button>
          <button type="button" class="btn-secondary" :disabled="busy" @click="close">取消</button>
        </div>
      </BaseModal>
    </div>
  </Teleport>
</template>
<script>
import BaseModal from './ui/BaseModal.vue';
import { DeleteImage, DeleteVideo } from '../../wailsjs/go/main/App';
import { formatBytes } from '../utils/mediaDetails.js';
export default {
  name: 'PersonMediaDeleteDialog', components: { BaseModal },
  props: { target: { type: Object, required: true } },
  emits: ['close', 'deleted'],
  data: () => ({ deleteFile: false, busy: false, error: '' }),
  computed: { kindLabel() { return this.target.kind === 'video' ? '视频' : '图片'; } },
  mounted() { window.addEventListener('keydown', this.escape, true); },
  beforeUnmount() { window.removeEventListener('keydown', this.escape, true); },
  methods: {
    formatBytes,
    close() { if (!this.busy) this.$emit('close'); },
    escape(event) { if (event.key === 'Escape') { event.preventDefault(); event.stopImmediatePropagation(); this.close(); } },
    async confirm() {
      if (this.busy) return;
      const target = this.target;
      this.busy = true; this.error = '';
      try {
        if (target.kind === 'video') await DeleteVideo(Number(target.media.id), this.deleteFile);
        else if (target.kind === 'image') await DeleteImage(Number(target.media.id), this.deleteFile);
        else throw new Error('未知媒体类型');
        this.$emit('deleted', target);
        this.$emit('close');
      } catch (err) { this.error = `删除失败：${err}`; }
      finally { this.busy = false; }
    }
  }
};
</script>
<style scoped>
.person-media-delete-layer { position: fixed; inset: 0; z-index: 1500; }
.person-media-delete-path { overflow-wrap: anywhere; color: var(--text-muted); }
.person-media-delete-option { display: flex; align-items: center; gap: 8px; }
.person-media-delete-option input { width: auto; margin: 0; }
</style>
