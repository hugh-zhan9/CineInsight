<template>
  <span :class="['ui-filter-chip', { 'ui-filter-chip--active': active, 'ui-filter-chip--removable': removable }]">
    <button
      type="button"
      class="ui-filter-chip__toggle"
      :aria-pressed="active ? 'true' : 'false'"
      :data-test="toggleDataTest || undefined"
      @click="$emit('toggle')"
      @keydown.delete.prevent="removeFromKeyboard"
    >{{ label }}<span v-if="hasCount || $slots.count" class="ui-filter-chip__count"><slot name="count">{{ count }}</slot></span></button>
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
// count 是这一项的条数，原样写在文字后面（不截成 99+，筛选的条数要写全）；count 插槽可以换成自己的内容
// （例如条数后面再跟一段「已算 3 / 10」）。data-test 等属性落在外层胶囊上；toggleDataTest 落在可点的切换按钮上，
// 给要直接点这颗按钮的测试钩子用（v-for 出来的一排芯片共用一个钩子时尤其如此）。
export default {
  name: 'FilterChip',
  components: { AppIcon },
  props: {
    label: { type: String, required: true },
    active: { type: Boolean, default: false },
    removable: { type: Boolean, default: false },
    count: { type: [Number, String], default: null },
    toggleDataTest: { type: String, default: '' }
  },
  emits: ['toggle', 'remove'],
  computed: {
    hasCount() {
      return (typeof this.count === 'number' && Number.isFinite(this.count)) || (typeof this.count === 'string' && this.count !== '');
    }
  },
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
.ui-filter-chip__toggle { gap: 6px; padding: 0 var(--space-3); border-radius: var(--radius-pill); font-weight: var(--fw-medium); white-space: nowrap; }
.ui-filter-chip__count { display: inline-flex; align-items: baseline; gap: 6px; font-variant-numeric: tabular-nums; }
.ui-filter-chip--removable .ui-filter-chip__toggle { padding-right: var(--space-1); }
.ui-filter-chip__remove { padding: 0 var(--space-2) 0 2px; border-radius: 0 var(--radius-pill) var(--radius-pill) 0; opacity: 0.7; }
.ui-filter-chip__remove:hover { opacity: 1; }
.ui-filter-chip__toggle:focus-visible,
.ui-filter-chip__remove:focus-visible { outline: 2px solid var(--accent-color); outline-offset: 1px; }
</style>
