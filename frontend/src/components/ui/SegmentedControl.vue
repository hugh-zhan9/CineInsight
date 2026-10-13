<template>
  <div class="ui-segmented" role="radiogroup" :aria-label="label" @keydown="handleKeydown">
    <button
      v-for="(option, index) in options"
      :key="String(option.value)"
      type="button"
      role="radio"
      :class="['ui-segmented__option', { 'ui-segmented__option--on': option.value === modelValue }]"
      :aria-checked="option.value === modelValue ? 'true' : 'false'"
      :aria-label="option.label ? undefined : accessibleName(option)"
      :title="option.title || undefined"
      :tabindex="index === focusIndex ? 0 : -1"
      :disabled="option.disabled || undefined"
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
// 只有选中项在 Tab 序列里（选中项不可用或没有选中项时，换成第一个可用项）；← ↑ / → ↓ 从当前有焦点的选项
// 选上一个 / 下一个可用项（首尾循环、跳过 disabled），Home / End 跳到两端，焦点跟着走，选中即生效。
// 选项 { value, label?, icon?, ariaLabel?, title?, disabled? }：只有图标、没有 label 时用 ariaLabel（或 title）
// 作读屏名，两者都没有在开发环境里告警。页内一级分区用 AppTabs，不用它。
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
    focusIndex() {
      const selected = this.options[this.selectedIndex];
      if (selected && !selected.disabled) return this.selectedIndex;
      return Math.max(0, this.options.findIndex(option => !option.disabled));
    }
  },
  watch: {
    options: {
      immediate: true,
      handler(options) {
        if (!import.meta.env.DEV) return;
        for (const option of options || []) {
          if (!option.label && !this.accessibleName(option)) {
            console.warn(`[SegmentedControl] 「${this.label}」的选项 ${String(option.value)} 只有图标，要给 ariaLabel 或 title 作读屏名`);
          }
        }
      }
    }
  },
  methods: {
    accessibleName(option) {
      return String(option.ariaLabel || option.title || '').trim() || undefined;
    },
    select(option) {
      if (!option.disabled && option.value !== this.modelValue) this.$emit('update:modelValue', option.value);
    },
    step(from, delta) {
      const count = this.options.length;
      for (let offset = 1; offset <= count; offset += 1) {
        const index = (from + delta * offset + count * count) % count;
        if (!this.options[index].disabled) return index;
      }
      return from;
    },
    // 方向键的起点：按键所在（或当前有焦点）的选项，都不是时用 Tab 序列里的那一个。
    currentIndex(event) {
      const buttons = [...this.$el.querySelectorAll('[role="radio"]')];
      const fromTarget = buttons.indexOf(event.target?.closest?.('[role="radio"]'));
      if (fromTarget !== -1) return fromTarget;
      const fromFocus = buttons.indexOf(document.activeElement);
      return fromFocus !== -1 ? fromFocus : this.focusIndex;
    },
    handleKeydown(event) {
      const count = this.options.length;
      if (!count) return;
      const current = this.currentIndex(event);
      let next;
      if (event.key === 'ArrowRight' || event.key === 'ArrowDown') next = this.step(current, 1);
      else if (event.key === 'ArrowLeft' || event.key === 'ArrowUp') next = this.step(current, -1);
      else if (event.key === 'Home') next = this.step(count - 1, 1);
      else if (event.key === 'End') next = this.step(0, -1);
      else return;
      event.preventDefault();
      if (this.options[next].disabled) return;
      this.select(this.options[next]);
      this.$el.querySelectorAll('[role="radio"]')[next]?.focus();
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
.ui-segmented__option:hover:not(:disabled) { color: var(--text-primary); }
.ui-segmented__option:disabled { opacity: 0.45; cursor: not-allowed; }
.ui-segmented__option--on { background: var(--accent-soft); color: var(--accent-text); font-weight: var(--fw-semibold); box-shadow: var(--shadow-segment); }
.ui-segmented__option:focus-visible { outline: 2px solid var(--accent-color); outline-offset: 1px; }
</style>
