<template>
  <div v-if="visible" class="modal-overlay" @click="$emit('close')">
    <div class="modal" @click.stop>
      <h2>确认删除标签</h2>
      <p>确定要删除标签 "{{ tag?.name }}" 吗？</p>
      <p class="hint-text">该标签会从所有视频中移除。</p>
      <div class="modal-actions">
        <button @click="$emit('confirm-delete', tag)" class="btn-danger">确认删除</button>
        <button @click="$emit('close')" class="btn-secondary">取消</button>
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
