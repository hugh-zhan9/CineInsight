<template>
  <div class="action-menu" @click.stop>
    <button
      type="button"
      class="btn-secondary action-menu__trigger"
      :aria-expanded="open ? 'true' : 'false'"
      @click="toggle"
    >
      {{ label }}
      <span class="action-menu__chevron" aria-hidden="true">⌄</span>
    </button>
    <div v-if="open" class="action-menu__panel glass-surface">
      <slot :close="close"></slot>
    </div>
  </div>
</template>

<script>
export default {
  name: 'ActionMenu',
  props: {
    label: { type: String, default: '更多' }
  },
  data() {
    return {
      open: false
    };
  },
  mounted() {
    document.addEventListener('click', this.close);
  },
  beforeUnmount() {
    document.removeEventListener('click', this.close);
  },
  methods: {
    toggle() {
      this.open = !this.open;
    },
    close() {
      this.open = false;
    }
  }
};
</script>

<style scoped>
.action-menu {
  position: relative;
  display: inline-flex;
}

.action-menu__trigger {
  min-width: 82px;
}

.action-menu__chevron {
  margin-left: 2px;
  color: var(--text-muted);
  font-size: 12px;
}

.action-menu__panel {
  position: absolute;
  top: calc(100% + 8px);
  right: 0;
  z-index: 160;
  min-width: 168px;
  padding: 6px;
  border-radius: var(--radius-md);
}

.action-menu__panel :deep(button) {
  width: 100%;
  justify-content: flex-start;
  border: 0;
  background: transparent;
  color: var(--text-primary);
}

.action-menu__panel :deep(button:hover:not(:disabled)) {
  background: var(--accent-soft);
  transform: none;
}
</style>
