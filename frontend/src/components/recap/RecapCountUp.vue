<template>
  <span class="recap-count" :aria-label="formatted(value)" data-test="recap-count">{{ formatted(shown) }}</span>
</template>

<script>
// 大数字从 0 滚到目标值（进入视口时开始，约 0.9 秒，缓出）。减少动态效果或测试环境里直接显示终值。
import { prefersReducedMotion } from '../../utils/recap.js';

export default {
  name: 'RecapCountUp',
  props: {
    value: { type: Number, default: 0 },
    duration: { type: Number, default: 900 }
  },
  data() {
    return { shown: 0 };
  },
  watch: {
    value(next, previous) { this.animate(this._started ? previous : 0, next); }
  },
  mounted() {
    if (prefersReducedMotion() || typeof window.requestAnimationFrame !== 'function' || typeof IntersectionObserver !== 'function') {
      this.shown = this.value;
      this._started = true;
      return;
    }
    this._observer = new IntersectionObserver(entries => {
      if (!entries.some(entry => entry.isIntersecting)) return;
      this._observer.disconnect();
      this._observer = null;
      this._started = true;
      this.animate(0, this.value);
    });
    this._observer.observe(this.$el);
  },
  beforeUnmount() {
    this._observer?.disconnect();
    if (this._frame) window.cancelAnimationFrame(this._frame);
  },
  methods: {
    formatted(value) {
      return Math.round(Number(value || 0)).toLocaleString('zh-CN');
    },
    animate(from, to) {
      if (this._frame) window.cancelAnimationFrame(this._frame);
      if (!this._started || prefersReducedMotion() || typeof window.requestAnimationFrame !== 'function') {
        this.shown = to;
        return;
      }
      const start = performance.now();
      const step = now => {
        const progress = Math.min(1, (now - start) / this.duration);
        const eased = 1 - Math.pow(1 - progress, 3);
        this.shown = from + (to - from) * eased;
        this._frame = progress < 1 ? window.requestAnimationFrame(step) : null;
      };
      this._frame = window.requestAnimationFrame(step);
    }
  }
};
</script>

<style scoped>
.recap-count { font-variant-numeric: tabular-nums; }
</style>
