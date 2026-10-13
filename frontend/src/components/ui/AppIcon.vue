<template>
  <svg
    class="ui-icon"
    :width="pixelSize"
    :height="pixelSize"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="1.75"
    stroke-linecap="round"
    stroke-linejoin="round"
    focusable="false"
    :role="label ? 'img' : undefined"
    :aria-label="label || undefined"
    :aria-hidden="label ? undefined : 'true'"
    :data-icon="name"
  >
    <path v-for="(d, index) in paths" :key="index" :d="d" />
  </svg>
</template>

<script>
import { iconPaths } from './icons.js';

const SIZES = [16, 20, 24];

// 线性图标（D-UD-06）。name 取自 ui/icons.js；没有 label 时是装饰图标（aria-hidden），
// 有 label 时作为图片读出。尺寸只有 16 / 20 / 24 三档，颜色跟随 currentColor。
export default {
  name: 'AppIcon',
  props: {
    name: { type: String, required: true, validator: name => iconPaths(name) !== null },
    size: { type: [Number, String], default: 16, validator: size => SIZES.includes(Number(size)) },
    label: { type: String, default: '' }
  },
  computed: {
    paths() { return iconPaths(this.name) || []; },
    pixelSize() { return SIZES.includes(Number(this.size)) ? Number(this.size) : 16; }
  }
};
</script>

<style scoped>
.ui-icon { flex: none; display: inline-block; vertical-align: middle; }
</style>
