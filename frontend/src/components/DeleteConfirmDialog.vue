<template>
  <BaseModal v-if="visible" close-on-overlay stop-modal-clicks @close="$emit('close')">
      <h2>确认删除</h2>
      <p>{{ confirmMessage }}</p>
      <div class="form-group">
        <label>
          <input type="checkbox" v-model="deleteFile" data-test="delete-file-choice" />
          同时把原文件移到废纸篓
        </label>
        <label v-if="settings.confirm_before_delete">
          <input type="checkbox" v-model="dontAskAgain" />
          不再提示
        </label>
      </div>
      <p class="delete-hint" data-test="delete-consequence">{{ deleteFile ? trashHint : recordOnlyHint }}</p>
      <div class="modal-actions">
        <button @click="handleConfirm" class="btn-danger">确认删除</button>
        <button @click="$emit('close')" class="btn-secondary">取消</button>
      </div>
  </BaseModal>
</template>

<script>
import BaseModal from './ui/BaseModal.vue';
import { formatBytes } from '../utils/mediaDetails.js';

// 删除的两种后果（D-PC01 / D-PC03），视频与图片的确认框共用这两句。
export const DELETE_TRASH_HINT = '原文件会移到系统废纸篓，可在回收站恢复；在访达中清空废纸篓才会释放空间。所在磁盘不支持废纸篓时，会先问你怎么处理。';
export const DELETE_RECORD_ONLY_HINT = '仅删除记录：原文件保留在磁盘上。以后扫描到同一个文件不会再收录，可在回收站「允许重新收录」。';

export default {
  name: 'DeleteConfirmDialog',
  components: { BaseModal },
  props: {
    visible: { type: Boolean, default: false },
    video: { type: Object, default: null },
    videoCount: { type: Number, default: 0 },
    settings: { type: Object, required: true }
  },
  emits: ['close', 'confirm-delete'],
  data() {
    return {
      deleteFile: false,
      dontAskAgain: false,
      trashHint: DELETE_TRASH_HINT,
      recordOnlyHint: DELETE_RECORD_ONLY_HINT
    };
  },
  watch: {
    visible(val) {
      if (val) {
        this.deleteFile = this.settings.delete_original_file;
        this.dontAskAgain = false;
      }
    }
  },
  computed: {
    confirmMessage() {
      if (this.videoCount > 0) {
        return `确定要删除选中的 ${this.videoCount} 个视频吗？`;
      }
      const size = Number(this.video?.size || 0);
      const name = this.video?.name || '';
      if (size > 0) return `确定要删除视频 "${name}"（${formatBytes(size)}）吗？`;
      return `确定要删除视频 "${name}" 吗？`;
    }
  },
  methods: {
    handleConfirm() {
      this.$emit('confirm-delete', {
        video: this.video,
        deleteFile: this.deleteFile,
        dontAskAgain: this.dontAskAgain
      });
    }
  }
};
</script>

<style scoped>
.delete-hint {
  margin-top: 12px;
  font-size: 12px;
  color: var(--text-secondary);
}
</style>
