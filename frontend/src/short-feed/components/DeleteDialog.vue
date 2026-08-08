<template>
  <div class="modal-backdrop" @click="$emit('cancel')">
    <div class="confirm-modal" @click.stop>
      <h2>{{ title }}</h2>
      <p>{{ message }}</p>
      <div class="modal-actions">
        <button type="button" class="modal-btn" @click="$emit('cancel')">取消</button>
        <button type="button" class="modal-btn danger" @click="$emit('confirm')">删除</button>
      </div>
    </div>
  </div>
</template>

<script>
export default {
  name: 'DeleteDialog',
  props: {
    title: { type: String, default: '删除' },
    message: { type: String, default: '' }
  },
  emits: ['cancel', 'confirm']
};
</script>
