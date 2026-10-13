<template>
  <div :class="['ui-skeleton', `ui-skeleton--${shape}`]" aria-hidden="true">
    <template v-if="shape === 'line'">
      <span v-for="index in lineCount" :key="index" class="ui-skeleton__bone ui-skeleton__line"></span>
    </template>
    <span v-else class="ui-skeleton__bone ui-skeleton__media"></span>
    <template v-if="shape === 'card' || shape === 'poster'">
      <span v-for="index in lineCount" :key="index" class="ui-skeleton__bone ui-skeleton__line"></span>
    </template>
  </div>
</template>

<script>
const SHAPES = ['line', 'card', 'poster', 'thumb'];

// 骨架屏（D-UD-06）：只用于首次加载，占住内容的位置。纯装饰（aria-hidden），读屏的「正在加载」
// 由宿主区域自己给（例如 aria-busy 或一句文字）。line 是几行文字；card 是 16:9 图 + 文字；
// poster 是 2:3 海报 + 文字；thumb 只有一张 16:9 缩略图。
export default {
  name: 'SkeletonBlock',
  props: {
    lines: { type: Number, default: null },
    shape: { type: String, default: 'line', validator: shape => SHAPES.includes(shape) }
  },
  computed: {
    lineCount() {
      if (this.shape === 'thumb') return 0;
      const fallback = this.shape === 'line' ? 3 : 2;
      const count = Number.isInteger(this.lines) ? this.lines : fallback;
      return Math.max(0, Math.min(count, 12));
    }
  }
};
</script>

<style scoped>
.ui-skeleton { display: grid; gap: var(--space-2); min-width: 0; }
.ui-skeleton__bone {
  display: block;
  border-radius: var(--radius-sm);
  background: linear-gradient(90deg, var(--panel-muted-bg) 0%, var(--hairline-soft) 50%, var(--panel-muted-bg) 100%);
  background-size: 200% 100%;
  animation: ui-skeleton-shimmer 1.4s ease-in-out infinite;
}
.ui-skeleton__line { height: 12px; }
.ui-skeleton__line:last-child:not(:first-child) { width: 62%; }
.ui-skeleton__media { width: 100%; aspect-ratio: 16 / 9; border-radius: var(--radius); }
.ui-skeleton--poster .ui-skeleton__media { aspect-ratio: 2 / 3; }
@keyframes ui-skeleton-shimmer {
  from { background-position: 100% 0; }
  to { background-position: -100% 0; }
}
@media (prefers-reduced-motion: reduce) {
  .ui-skeleton__bone { animation: none; background: var(--panel-muted-bg); }
}
</style>
