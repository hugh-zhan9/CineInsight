<template>
  <div :class="['ui-progress', { 'ui-progress--indeterminate': indeterminate }]">
    <div v-if="label" class="ui-progress__head">
      <span class="ui-progress__label">{{ label }}</span>
      <span v-if="!indeterminate" class="ui-progress__value">{{ percent }}%</span>
    </div>
    <div
      class="ui-progress__track"
      role="progressbar"
      aria-valuemin="0"
      aria-valuemax="100"
      :aria-valuenow="indeterminate ? undefined : percent"
      :aria-label="label || '进度'"
      :aria-busy="indeterminate ? 'true' : undefined"
    >
      <span class="ui-progress__fill" :style="indeterminate ? null : { width: `${percent}%` }"></span>
    </div>
  </div>
</template>

<script>
// 全站唯一的进度条（D-UD-06、D-UD-11）。value 是 0–1 的比例；不传（或不是数字）就是不确定进度。
export default {
  name: 'ProgressBar',
  props: {
    value: { type: Number, default: null },
    label: { type: String, default: '' }
  },
  computed: {
    indeterminate() { return typeof this.value !== 'number' || Number.isNaN(this.value); },
    percent() {
      if (this.indeterminate) return 0;
      return Math.round(Math.min(1, Math.max(0, this.value)) * 100);
    }
  }
};
</script>

<style scoped>
.ui-progress { display: grid; gap: var(--space-1); min-width: 0; }
.ui-progress__head { display: flex; align-items: baseline; justify-content: space-between; gap: var(--space-2); color: var(--text-secondary); font-size: var(--fs-caption); }
.ui-progress__label { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ui-progress__value { flex: none; font-family: var(--font-mono); font-variant-numeric: tabular-nums; }
.ui-progress__track { position: relative; height: 4px; overflow: hidden; border-radius: var(--radius-pill); background: var(--panel-muted-bg); }
.ui-progress__fill { position: absolute; inset: 0 auto 0 0; border-radius: inherit; background: var(--accent-color); transition: width var(--transition); }
.ui-progress--indeterminate .ui-progress__fill { width: 35%; animation: ui-progress-slide 1.2s ease-in-out infinite; }
@keyframes ui-progress-slide {
  from { transform: translateX(-100%); }
  to { transform: translateX(290%); }
}
@media (prefers-reduced-motion: reduce) {
  .ui-progress--indeterminate .ui-progress__fill { width: 100%; animation: none; opacity: 0.45; }
}
</style>
