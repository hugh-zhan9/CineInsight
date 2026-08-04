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
  methods: {
    handleOverlayClick() {
      if (this.closeOnOverlay) this.$emit('close');
    },
    handleModalClick(event) {
      if (this.stopModalClicks) event.stopPropagation();
    }
  }
};
</script>
