<template>
  <div v-if="visible" class="modal-overlay" @click="$emit('close')">
    <div class="modal" @click.stop style="max-width: 400px; text-align: center;">
      <div style="font-size: 40px; margin-bottom: 16px;">⚠️</div>
      <h2 style="margin-bottom: 12px;">确认删除标签</h2>
      <p style="color: var(--text-primary); margin-bottom: 8px;">确定要永久删除标签 <strong style="color: var(--accent-color);">"{{ tag?.name }}"</strong> 吗？</p>
      <p class="help-text">此操作不可撤销，该标签将从所有视频中移除。</p>
      
      <div class="modal-actions" style="justify-content: center; margin-top: 32px;">
        <button @click="$emit('close')" class="btn-secondary" style="min-width: 100px;">取消</button>
        <button @click="$emit('confirm-delete', tag)" class="btn-danger" style="min-width: 100px;">确认删除</button>
      </div>
    </div>
  </div>
</template>

<script>
export default {
  name: 'TagDeleteDialog',
  props: {
    visible: { type: Boolean, default: false },
    tag: { type: Object, default: null }
  },
  emits: ['close', 'confirm-delete']
};
</script>
