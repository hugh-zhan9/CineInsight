<template>
  <div v-if="visible" class="modal-overlay" @click="$emit('close')">
    <div class="modal" @click.stop>
      <h2>确认删除</h2>
      <p>{{ confirmMessage }}</p>
      <div class="form-group">
        <label>
          <input type="checkbox" v-model="deleteFile" />
          同时将原始文件移入回收站
        </label>
        <label v-if="settings.confirm_before_delete">
          <input type="checkbox" v-model="dontAskAgain" />
          不再提示
        </label>
      </div>
      <p class="delete-hint">不勾选时仅移除数据库记录，原文件会保留在磁盘上。</p>
      <div class="modal-actions">
        <button @click="handleConfirm" class="btn-danger">确认删除</button>
        <button @click="$emit('close')" class="btn-secondary">取消</button>
      </div>
    </div>
  </div>
</template>

<script>
export default {
  name: 'DeleteConfirmDialog',
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
      dontAskAgain: false
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
      return `确定要删除视频 "${this.video?.name || ''}" 吗？`;
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
  color: #666;
}
</style>
