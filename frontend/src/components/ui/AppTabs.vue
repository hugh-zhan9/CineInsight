<template>
  <div class="ui-tabs" role="tablist" @keydown="handleKeydown">
    <button
      v-for="(tab, index) in tabs"
      :id="idPrefix ? `${idPrefix}-tab-${tab.key}` : undefined"
      :key="tab.key"
      type="button"
      role="tab"
      :class="['ui-tabs__tab', { 'ui-tabs__tab--on': tab.key === modelValue }]"
      :aria-selected="tab.key === modelValue ? 'true' : 'false'"
      :aria-controls="idPrefix ? `${idPrefix}-panel-${tab.key}` : undefined"
      :tabindex="index === focusIndex ? 0 : -1"
      :disabled="tab.disabled || undefined"
      :data-tab="tab.key"
      @click="select(tab)"
    >
      <span>{{ tab.label }}</span>
      <span v-if="hasCount(tab)" class="ui-tabs__count">{{ countText(tab.count) }}</span>
    </button>
  </div>
</template>

<script>
import { badgeCountText } from './AppBadge.vue';

// 标签页（D-UD-06）：页内一级分区，下划线式。role=tablist/tab，选中项 aria-selected，只有它在 Tab 序列里；
// ← / → 切到上一个 / 下一个可用页签（首尾循环、跳过 disabled），Home / End 到两端，切到即生效。
// 传 idPrefix 时每个页签带 id `${idPrefix}-tab-${key}` 与 aria-controls `${idPrefix}-panel-${key}`，
// 宿主给对应面板写 role="tabpanel"、同名 id 与 aria-labelledby。
export default {
  name: 'AppTabs',
  props: {
    tabs: { type: Array, required: true },
    modelValue: { type: [String, Number], default: null },
    idPrefix: { type: String, default: '' }
  },
  emits: ['update:modelValue'],
  computed: {
    selectedIndex() { return this.tabs.findIndex(tab => tab.key === this.modelValue); },
    focusIndex() {
      if (this.selectedIndex >= 0) return this.selectedIndex;
      return Math.max(0, this.tabs.findIndex(tab => !tab.disabled));
    }
  },
  methods: {
    hasCount(tab) { return typeof tab.count === 'number' && Number.isFinite(tab.count); },
    countText(count) { return badgeCountText(count); },
    select(tab) {
      if (!tab.disabled && tab.key !== this.modelValue) this.$emit('update:modelValue', tab.key);
    },
    step(from, delta) {
      const count = this.tabs.length;
      for (let offset = 1; offset <= count; offset += 1) {
        const index = (from + delta * offset + count * count) % count;
        if (!this.tabs[index].disabled) return index;
      }
      return from;
    },
    handleKeydown(event) {
      const count = this.tabs.length;
      if (!count) return;
      const current = this.focusIndex;
      let next;
      if (event.key === 'ArrowRight') next = this.step(current, 1);
      else if (event.key === 'ArrowLeft') next = this.step(current, -1);
      else if (event.key === 'Home') next = this.step(count - 1, 1);
      else if (event.key === 'End') next = this.step(0, -1);
      else return;
      event.preventDefault();
      this.select(this.tabs[next]);
      this.$nextTick(() => this.$el.querySelectorAll('[role="tab"]')[next]?.focus());
    }
  }
};
</script>

<style scoped>
.ui-tabs { display: flex; flex-wrap: wrap; gap: var(--space-1) var(--space-5); border-bottom: 1px solid var(--hairline); }
.ui-tabs__tab {
  position: relative;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: var(--h-unit);
  margin-bottom: -1px;
  padding: 0 2px;
  border: 0;
  border-bottom: 2px solid transparent;
  background: transparent;
  color: var(--text-secondary);
  font: inherit;
  font-size: var(--fs-body);
  font-weight: var(--fw-medium);
  cursor: pointer;
  white-space: nowrap;
}
.ui-tabs__tab:hover:not(:disabled) { color: var(--text-primary); }
.ui-tabs__tab--on { border-bottom-color: var(--accent-color); color: var(--text-primary); font-weight: var(--fw-semibold); }
.ui-tabs__tab:disabled { opacity: 0.45; cursor: not-allowed; }
.ui-tabs__tab:focus-visible { outline: 2px solid var(--accent-color); outline-offset: 2px; border-radius: var(--radius-sm); }
.ui-tabs__count {
  min-width: 18px;
  padding: 0 5px;
  border-radius: var(--radius-pill);
  background: var(--panel-muted-bg);
  color: var(--text-secondary);
  font-family: var(--font-mono);
  font-size: var(--fs-caption);
  font-weight: var(--fw-medium);
  line-height: 18px;
  text-align: center;
}
.ui-tabs__tab--on .ui-tabs__count { background: var(--accent-soft); color: var(--accent-text); }
</style>
