<template>
  <span
    :class="['ui-spinner', `ui-spinner--${pixelSize}`]"
    :role="label ? 'status' : undefined"
    :aria-label="label || undefined"
    :aria-hidden="label ? undefined : 'true'"
  >
    <svg viewBox="0 0 24 24" :width="pixelSize" :height="pixelSize" focusable="false" aria-hidden="true">
      <circle class="ui-spinner__track" cx="12" cy="12" r="9" />
      <circle class="ui-spinner__arc" cx="12" cy="12" r="9" />
    </svg>
  </span>
</template>

<script>
const SIZES = [16, 20];

// 转圈（D-UD-06）：按钮里与小区域的「正在进行」。只有 16 / 20 两档；颜色跟随 currentColor。
// 单独出现时传 label（例如「正在读取」）作为读屏文字；放在已有文字旁边时不传，作装饰处理。
export default {
  name: 'AppSpinner',
  props: {
    size: { type: [Number, String], default: 16, validator: size => SIZES.includes(Number(size)) },
    label: { type: String, default: '' }
  },
  computed: {
    pixelSize() { return SIZES.includes(Number(this.size)) ? Number(this.size) : 16; }
  }
};
</script>

<style scoped>
.ui-spinner { display: inline-flex; flex: none; vertical-align: middle; color: inherit; }
.ui-spinner svg { display: block; fill: none; stroke: currentColor; stroke-width: 2.5; animation: ui-spinner-turn 0.8s linear infinite; }
.ui-spinner__track { opacity: 0.2; }
.ui-spinner__arc { stroke-dasharray: 18 100; stroke-linecap: round; }
@keyframes ui-spinner-turn { to { transform: rotate(360deg); } }
@media (prefers-reduced-motion: reduce) {
  .ui-spinner svg { animation-duration: 2.4s; }
}
</style>
