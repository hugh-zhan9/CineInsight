<template>
  <svg class="recap-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
    <path v-for="(d, index) in paths" :key="index" :d="d" />
  </svg>
</template>

<script>
import { ICON_PATHS, iconPaths } from '../ui/icons.js';

// 回顾页的线性图标（称号、统计卡与小标题）。路径已收进全站图标集 ui/icons.js（D-UD-06），
// 这里只保留回顾页的尺寸（跟随字号 1em）与「认不出的称号退回 spark」的规则。新代码直接用 ui/AppIcon。
export default {
  name: 'RecapIcon',
  props: { name: { type: String, default: 'spark' } },
  computed: {
    paths() { return iconPaths(this.name) || ICON_PATHS.spark; }
  }
};
</script>

<style scoped>
.recap-icon { width: 1em; height: 1em; flex: none; }
</style>
