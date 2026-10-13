<template>
  <div v-if="visible" class="ui-selection-bar" role="toolbar" aria-label="已选条目的批量操作">
    <span class="ui-selection-bar__count" aria-live="polite" data-test="selection-bar-count">{{ countText }}</span>
    <button
      v-if="canSelectAllMatching"
      type="button"
      class="btn-link"
      data-test="selection-bar-select-all-matching"
      @click="$emit('select-all-matching')"
    >全选当前结果（{{ totalMatching }}）</button>
    <button type="button" class="btn-link" data-test="selection-bar-clear" @click="$emit('clear')">
      取消选择<kbd class="ui-selection-bar__kbd">Esc</kbd>
    </button>
    <div v-if="$slots.actions" class="ui-selection-bar__actions"><slot name="actions" /></div>
  </div>
</template>

<script>
// 选择操作条（D-UD-10）：有选中时才浮在内容区底部，没选中时页面上没有批量动作。默认只作用于勾选的条目；
// 要作用于整个筛选结果必须点「全选当前结果（N）」（select-all-matching），宿主据此把 allMatchingSelected 置真。
// 文案全站固定；Esc 发 clear（有对话框开着、焦点在输入框里、或按键已被别人处理时不抢）。
// actions 插槽放批量动作按钮（主操作放最后）。宿主负责把它放在滚动内容的末尾，吸底靠 position: sticky。
export default {
  name: 'SelectionBar',
  props: {
    count: { type: Number, required: true },
    totalMatching: { type: Number, default: null },
    allMatchingSelected: { type: Boolean, default: false }
  },
  emits: ['select-all-matching', 'clear'],
  computed: {
    visible() { return this.count > 0 || this.allMatchingSelected; },
    selectedTotal() {
      return this.allMatchingSelected && Number.isFinite(this.totalMatching) ? this.totalMatching : this.count;
    },
    countText() {
      return this.allMatchingSelected ? `已选当前结果全部 ${this.selectedTotal} 项` : `已选 ${this.selectedTotal} 项`;
    },
    canSelectAllMatching() {
      return !this.allMatchingSelected && Number.isFinite(this.totalMatching) && this.totalMatching > this.count;
    }
  },
  mounted() { document.addEventListener('keydown', this.handleKeydown); },
  beforeUnmount() { document.removeEventListener('keydown', this.handleKeydown); },
  methods: {
    handleKeydown(event) {
      if (event.key !== 'Escape' || !this.visible || event.defaultPrevented) return;
      if (document.querySelector('[role="dialog"]')) return;
      const target = event.target;
      if (target && (target.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(target.tagName || ''))) return;
      event.preventDefault();
      this.$emit('clear');
    }
  }
};
</script>

<style scoped>
.ui-selection-bar {
  position: sticky;
  bottom: var(--space-4);
  z-index: var(--z-sticky);
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-2) var(--space-4);
  margin-top: var(--space-3);
  padding: var(--space-2) var(--space-2) var(--space-2) var(--space-4);
  border: 1px solid var(--accent-border);
  border-radius: var(--radius-lg);
  background: var(--panel-bg);
  box-shadow: var(--shadow-popover);
  font-size: var(--fs-secondary);
}
.ui-selection-bar__count { color: var(--text-primary); font-weight: var(--fw-semibold); font-variant-numeric: tabular-nums; }
.ui-selection-bar__kbd { margin-left: var(--space-1); padding: 0 4px; border: 1px solid var(--hairline); border-radius: 4px; color: var(--text-muted); font-family: var(--font-mono); font-size: var(--fs-caption); }
.ui-selection-bar__actions { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-2); margin-left: auto; }
</style>
