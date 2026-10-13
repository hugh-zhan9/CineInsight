<template>
  <div class="viewer-stars" role="group" aria-label="个人评分，0 到 10，支持 0.5 分" data-test="detail-stars">
    <!-- 个人评分（0–10，支持半分）：每颗星左右两半各是一个按钮，点一下就发 rate，由宿主即时保存。 -->
    <span v-for="star in 10" :key="star" :class="['viewer-star', `viewer-star--${fill(star)}`]">
      <button type="button" class="btn-icon viewer-star__half viewer-star__half--left" :aria-label="`评 ${star - 0.5} 分`" :aria-pressed="value === star - 0.5" :data-test="`detail-star-${star - 0.5}`" :disabled="disabled" @click="$emit('rate', star - 0.5)"></button>
      <button type="button" class="btn-icon viewer-star__half viewer-star__half--right" :aria-label="`评 ${star} 分`" :aria-pressed="value === star" :data-test="`detail-star-${star}`" :disabled="disabled" @click="$emit('rate', star)"></button>
    </span>
    <span class="viewer-stars__value" data-test="detail-stars-value">{{ value === null ? '未评分' : `${value.toFixed(1)} / 10` }}</span>
    <button v-if="value !== null" type="button" class="btn btn--secondary btn--sm" data-test="detail-stars-clear" :disabled="disabled" @click="$emit('rate', null)">清除</button>
  </div>
</template>

<script>
// 与详情抽屉动作条的星级同一口径（PreviewDrawer 的 drawer-stars 样式是它私有的，这里另起一套类名）。
export default {
  name: 'RatingStars',
  props: {
    value: { type: Number, default: null },
    disabled: { type: Boolean, default: false }
  },
  emits: ['rate'],
  methods: {
    fill(star) {
      if (this.value === null) return 'empty';
      if (this.value >= star) return 'full';
      return this.value >= star - 0.5 ? 'half' : 'empty';
    }
  }
};
</script>

<style scoped>
.viewer-stars { display: flex; flex-wrap: wrap; align-items: center; gap: 2px; }
.viewer-star { position: relative; display: inline-block; width: 22px; height: 22px; flex: none; }
.viewer-star::before,.viewer-star::after { content: '★'; position: absolute; inset: 0; font-size: var(--fs-title); line-height: 22px; text-align: left; pointer-events: none; }
.viewer-star::before { color: color-mix(in srgb, currentColor 35%, transparent); }
.viewer-star::after { width: 0; overflow: hidden; color: var(--warning-color); }
.viewer-star--half::after { width: 50%; }
.viewer-star--full::after { width: 100%; }
/* 半颗星是只有读屏名的透明按钮（.btn-icon）：尺寸与位置由星格决定，不要 .btn-icon 的方块尺寸、底色与悬停。 */
.viewer-star__half { position: absolute; top: 0; bottom: 0; width: 50%; height: auto; z-index: 1; border: 0; padding: 0; background: transparent; cursor: pointer; }
.viewer-star__half.btn-icon:hover:not(:disabled) { background: transparent; }
.viewer-star__half--left { left: 0; }
.viewer-star__half--right { right: 0; }
.viewer-star__half:disabled { cursor: progress; }
.viewer-star__half:focus-visible { outline: 2px solid var(--accent-color); outline-offset: 1px; border-radius: 3px; }
.viewer-stars__value { margin: 0 6px 0 8px; color: var(--recap-hero-muted); font-size: var(--fs-secondary); font-variant-numeric: tabular-nums; }
</style>
