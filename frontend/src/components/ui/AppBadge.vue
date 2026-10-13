<template>
  <span :class="['ui-badge', `ui-badge--${tone}`, { 'ui-badge--count': hasCount }]">
    <template v-if="hasCount">{{ countText }}</template>
    <slot v-else />
  </span>
</template>

<script>
export const BADGE_TONES = ['neutral', 'accent', 'warning', 'danger', 'success'];

// 徽标（D-UD-06）：数量与状态，不可点——要点的用 TagPill 或按钮。count 给了就显示数字（超过 99 写 99+，
// 与顶栏角标同一口径），否则显示默认插槽里的文字。
export function badgeCountText(count) {
  const value = Math.max(0, Math.floor(Number(count) || 0));
  return value > 99 ? '99+' : String(value);
}

export default {
  name: 'AppBadge',
  props: {
    tone: { type: String, default: 'neutral', validator: tone => BADGE_TONES.includes(tone) },
    count: { type: Number, default: null }
  },
  computed: {
    hasCount() { return typeof this.count === 'number' && Number.isFinite(this.count); },
    countText() { return badgeCountText(this.count); }
  }
};
</script>

<style scoped>
.ui-badge {
  display: inline-flex;
  flex: none;
  align-items: center;
  height: 18px;
  padding: 0 6px;
  border: 1px solid transparent;
  border-radius: var(--radius-pill);
  background: var(--panel-muted-bg);
  color: var(--text-secondary);
  font-size: var(--fs-caption);
  font-weight: var(--fw-medium);
  line-height: 1;
  white-space: nowrap;
}
.ui-badge--count { min-width: 18px; justify-content: center; font-family: var(--font-mono); font-variant-numeric: tabular-nums; }
.ui-badge--accent { background: var(--accent-color); color: var(--accent-on); }
.ui-badge--warning { border-color: var(--warning-border); background: var(--warning-soft); color: var(--warning-text); }
.ui-badge--danger { border-color: var(--danger-border); background: var(--danger-soft); color: var(--danger-text); }
.ui-badge--success { border-color: var(--accent-border); background: var(--accent-soft); color: var(--success-color); }
</style>
