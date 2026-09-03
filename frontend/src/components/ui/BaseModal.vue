<template>
  <div class="modal-overlay" @click.self="handleOverlayClick">
    <div class="modal" role="dialog" aria-modal="true" v-bind="$attrs" @click="handleModalClick">
      <slot />
    </div>
  </div>
</template>

<script>
export default {
  name: 'BaseModal',
  inheritAttrs: false,
  props: {
    closeOnOverlay: { type: Boolean, default: false },
    stopModalClicks: { type: Boolean, default: false }
  },
  emits: ['close'],
  mounted() {
    // Esc 是弹窗的兜底逃生口：个别分支里没有按钮可点（例如能力不可用的提示态），
    // 没有它用户就只能重启应用。父组件没监听 close 时这里什么也不会发生。
    document.addEventListener('keydown', this.handleDocumentKeydown);
  },
  beforeUnmount() {
    document.removeEventListener('keydown', this.handleDocumentKeydown);
  },
  methods: {
    handleDocumentKeydown(event) {
      if (event.key !== 'Escape') return;
      event.stopPropagation();
      this.$emit('close');
    },

    handleOverlayClick() {
      if (this.closeOnOverlay) this.$emit('close');
    },
    handleModalClick(event) {
      if (this.stopModalClicks) event.stopPropagation();
    }
  }
};
</script>
