<template>
  <div v-if="visible" class="modal-overlay" @click="$emit('close')">
    <div class="modal" @click.stop>
      <h2>确认删除</h2>
      <p>确定要删除视频 "{{ video?.name }}" 吗？</p>
      <div class="form-group">
        <label>
          <input type="checkbox" v-model="deleteFile" />
          同时删除原始文件
        </label>
        <label v-if="settings.confirm_before_delete">
          <input type="checkbox" v-model="dontAskAgain" />
          不再提示
        </label>
      </div>
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
