<template>
  <span :class="['ui-filter-chip', { 'ui-filter-chip--active': active, 'ui-filter-chip--removable': removable }]">
    <button
      type="button"
      class="ui-filter-chip__toggle"
      :aria-pressed="active ? 'true' : 'false'"
      @click="$emit('toggle')"
      @keydown.delete.prevent="removeFromKeyboard"
    >{{ label }}</button>
    <button
      v-if="removable"
      type="button"
      class="ui-filter-chip__remove"
      :aria-label="`移除筛选：${label}`"
      @click="$emit('remove')"
    ><AppIcon name="close" :size="16" /></button>
  </span>
</template>

<script>
import AppIcon from './AppIcon.vue';

// 筛选芯片（D-UD-06）：可叠加的筛选条件，胶囊形。点芯片本身切换（toggle，aria-pressed 表示开关），
// removable 时右侧 × 移除（remove）；焦点在芯片上按 Delete / Backspace 也移除。组件不存状态。
export default {
  name: 'FilterChip',
  components: { AppIcon },
  props: {
    label: { type: String, required: true },
    active: { type: Boolean, default: false },
    removable: { type: Boolean, default: false }
  },
  emits: ['toggle', 'remove'],
  methods: {
    removeFromKeyboard() {
      if (this.removable) this.$emit('remove');
    }
  }
};
</script>

<style scoped>
.ui-filter-chip {
  display: inline-flex;
  flex: none;
  align-items: center;
  height: var(--h-compact);
  border: 1px solid var(--border-color);
  border-radius: var(--radius-pill);
  background: var(--control-bg);
  color: var(--text-secondary);
  font-size: var(--fs-caption);
  transition: border-color var(--transition), background var(--transition), color var(--transition);
}
.ui-filter-chip:hover { border-color: var(--border-strong); }
.ui-filter-chip--active { border-color: var(--accent-border); background: var(--accent-soft); color: var(--accent-text); }
.ui-filter-chip__toggle,
.ui-filter-chip__remove {
  display: inline-flex;
  align-items: center;
  height: 100%;
  border: 0;
  background: transparent;
  color: inherit;
  font: inherit;
  cursor: pointer;
}
.ui-filter-chip__toggle { padding: 0 var(--space-3); border-radius: var(--radius-pill); font-weight: var(--fw-medium); white-space: nowrap; }
.ui-filter-chip--removable .ui-filter-chip__toggle { padding-right: var(--space-1); }
.ui-filter-chip__remove { padding: 0 var(--space-2) 0 2px; border-radius: 0 var(--radius-pill) var(--radius-pill) 0; opacity: 0.7; }
.ui-filter-chip__remove:hover { opacity: 1; }
.ui-filter-chip__toggle:focus-visible,
.ui-filter-chip__remove:focus-visible { outline: 2px solid var(--accent-color); outline-offset: 1px; }
</style>
