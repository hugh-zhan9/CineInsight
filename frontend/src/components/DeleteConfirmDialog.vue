<template>
  <!-- 删除用三个词（D-UD-08）：勾「同时把原文件移到废纸篓」是「移到废纸篓」（可在回收站恢复，完成后可撤销），
       不勾是「从片库移除」（只删记录，文件不动）。标题、说明与按钮跟着勾选走，写清数量与后果（D-UD-07 第 2 级）。 -->
  <DialogShell
    v-if="visible"
    :title="actionLabel"
    size="sm"
    stop-modal-clicks
    :busy="remembering"
    class="delete-confirm-modal"
    data-test="delete-confirm-dialog"
    @close="requestClose"
  >
    <p data-test="delete-confirm-message">{{ confirmMessage }}</p>
    <div class="form-group">
      <label>
        <input type="checkbox" v-model="deleteFile" :disabled="remembering" data-test="delete-file-choice" />
        同时把原文件移到废纸篓
      </label>
      <label v-if="settings.confirm_before_delete">
        <input type="checkbox" v-model="dontAskAgain" :disabled="remembering" />
        不再提示
      </label>
    </div>
    <p class="delete-hint" data-test="delete-consequence">{{ deleteFile ? trashHint : recordOnlyHint }}</p>
    <template #footer>
      <button type="button" class="btn btn--secondary" :disabled="remembering" data-test="delete-confirm-cancel" @click="requestClose">取消</button>
      <button type="button" class="btn btn--danger" :disabled="remembering" data-test="delete-confirm-ok" @click="handleConfirm">{{ actionLabel }}</button>
    </template>
  </DialogShell>
</template>

<script>
import DialogShell from './ui/DialogShell.vue';
import { rememberDeleteFileChoice } from '../utils/trashConfirm.js';
import { formatBytes } from '../utils/mediaDetails.js';

// 删除的两种后果（D-PC01 / D-PC03），视频与图片的确认框共用这两句。
export const DELETE_TRASH_HINT = '原文件会移到系统废纸篓，可在回收站恢复；在访达中清空废纸篓才会释放空间。所在磁盘不支持废纸篓时，会先问你怎么处理。';
export const DELETE_RECORD_ONLY_HINT = '仅删除记录：原文件保留在磁盘上。以后扫描到同一个文件不会再收录，可在回收站「允许重新收录」。';

export default {
  name: 'DeleteConfirmDialog',
  components: { DialogShell },
  props: {
    visible: { type: Boolean, default: false },
    video: { type: Object, default: null },
    videoIds: { type: Array, default: () => [] },
    videoCount: { type: Number, default: 0 },
    settings: { type: Object, required: true }
  },
  emits: ['close', 'confirm-delete'],
  data() {
    return {
      deleteFile: false,
      remembering: false,
      confirmation: 0,
      dontAskAgain: false,
      trashHint: DELETE_TRASH_HINT,
      recordOnlyHint: DELETE_RECORD_ONLY_HINT
    };
  },
  beforeUnmount() { this.confirmation++; },
  watch: {
    confirmationTargetKey: { flush: 'sync', handler() { this.confirmation++; } },
    visible(val) {
      if (val) {
        this.deleteFile = this.settings.delete_original_file;
        this.dontAskAgain = false;
      }
    }
  },
  computed: {
    confirmationTargetKey() { return JSON.stringify([this.visible, this.video?.id || 0, this.videoCount, this.videoIds]); },
    actionLabel() {
      return this.deleteFile ? '移到废纸篓' : '从片库移除';
    },
    // 写清数量与对象：批量只报数量；单个带文件名与大小（大小是最便宜的「删对了没」自检）。
    confirmMessage() {
      let subject;
      if (this.videoCount > 0) {
        subject = `选中的 ${this.videoCount} 个视频`;
      } else {
        const size = Number(this.video?.size || 0);
        const name = this.video?.name || '';
        subject = size > 0 ? `视频「${name}」（${formatBytes(size)}）` : `视频「${name}」`;
      }
      return this.deleteFile ? `将把${subject}移到废纸篓。` : `将从片库移除${subject}，文件不动。`;
    }
  },
  methods: {
    requestClose() { if (!this.remembering) this.$emit('close'); },
    async handleConfirm() {
      if (this.remembering) return;
      const request = ++this.confirmation;
      const choice = {
        videoIds: [...this.videoIds],
        video: this.video,
        deleteFile: this.deleteFile,
        dontAskAgain: this.dontAskAgain
      };
      this.remembering = true;
      try { await rememberDeleteFileChoice(choice.deleteFile); if (request === this.confirmation && this.visible) this.$emit('confirm-delete', choice); }
      finally { this.remembering = false; }
    }
  }
};
</script>

<style scoped>
.delete-hint {
  margin-top: var(--space-3);
  font-size: var(--fs-caption);
  color: var(--text-secondary);
}
</style>
