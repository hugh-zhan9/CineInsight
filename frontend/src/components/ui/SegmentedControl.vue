<template>
  <div class="ui-segmented" role="radiogroup" :aria-label="label" @keydown="handleKeydown">
    <button
      v-for="(option, index) in options"
      :key="String(option.value)"
      type="button"
      role="radio"
      :class="['ui-segmented__option', { 'ui-segmented__option--on': option.value === modelValue }]"
      :aria-checked="option.value === modelValue ? 'true' : 'false'"
      :tabindex="index === focusIndex ? 0 : -1"
      @click="select(option)"
    >
      <AppIcon v-if="option.icon" :name="option.icon" :size="16" />
      <span v-if="option.label" class="ui-segmented__label">{{ option.label }}</span>
    </button>
  </div>
</template>

<script>
import AppIcon from './AppIcon.vue';

// 分段（D-UD-06）：同一内容的不同看法（视图切换、排序方向），单选。role=radiogroup，
// 只有选中项在 Tab 序列里；← ↑ / → ↓ 选上一个 / 下一个（首尾循环），Home / End 跳到两端，选中即生效。
// 页内一级分区用 AppTabs，不用它。
export default {
  name: 'SegmentedControl',
  components: { AppIcon },
  props: {
    options: { type: Array, required: true },
    modelValue: { type: [String, Number, Boolean], default: null },
    label: { type: String, required: true }
  },
  emits: ['update:modelValue'],
  computed: {
    selectedIndex() { return this.options.findIndex(option => option.value === this.modelValue); },
    focusIndex() { return this.selectedIndex >= 0 ? this.selectedIndex : 0; }
  },
  methods: {
    select(option) {
      if (option.value !== this.modelValue) this.$emit('update:modelValue', option.value);
    },
    handleKeydown(event) {
      const count = this.options.length;
      if (!count) return;
      const current = this.focusIndex;
      const targets = { ArrowLeft: current - 1, ArrowUp: current - 1, ArrowRight: current + 1, ArrowDown: current + 1, Home: 0, End: count - 1 };
      if (!(event.key in targets)) return;
      event.preventDefault();
      const next = (targets[event.key] + count) % count;
      this.select(this.options[next]);
      this.$nextTick(() => this.$el.querySelectorAll('[role="radio"]')[next]?.focus());
    }
  }
};
</script>

<style scoped>
.ui-segmented { display: inline-flex; flex: none; gap: 2px; padding: 2px; border: 1px solid var(--border-color); border-radius: var(--radius-sm); background: var(--control-bg); }
.ui-segmented__option {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: var(--space-1);
  height: calc(var(--h-unit) - 6px);
  padding: 0 var(--space-3);
  border: 0;
  border-radius: calc(var(--radius-sm) - 2px);
  background: transparent;
  color: var(--text-secondary);
  font: inherit;
  font-size: var(--fs-secondary);
  cursor: pointer;
  white-space: nowrap;
}
.ui-segmented__option:hover { color: var(--text-primary); }
.ui-segmented__option--on { background: var(--accent-soft); color: var(--accent-text); font-weight: var(--fw-semibold); box-shadow: var(--shadow-segment); }
.ui-segmented__option:focus-visible { outline: 2px solid var(--accent-color); outline-offset: 1px; }
</style>
