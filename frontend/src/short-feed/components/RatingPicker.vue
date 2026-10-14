<template>
  <!-- 评分（D-MW-04）：大号分数 + 10 颗星（点第 n 颗记 n 分）+ 半分滑杆（拖动只改显示，松手才写入）。
       与电脑端整页详情同一口径：0–10 分、步长 0.5。 -->
  <div class="rating-picker">
    <div class="rating-picker__score">
      <strong class="rating-picker__value" :class="{ 'rating-picker__value--empty': !valueText }" data-test="short-feed-rating-value">{{ valueText || '未评分' }}</strong>
      <div class="rating-stars" role="group" aria-label="点星星评分">
        <button
          v-for="star in 10"
          :key="star"
          type="button"
          class="btn-plain rating-star"
          :class="`rating-star--${starFill(value, star)}`"
          :aria-label="`评 ${star} 分`"
          :data-test="`short-feed-rating-star-${star}`"
          @click="$emit('commit', star)"
        >
          <svg viewBox="0 0 24 24" aria-hidden="true"><path d="m12 3.5 2.6 5.3 5.9.9-4.3 4.1 1 5.8L12 16.9l-5.2 2.7 1-5.8-4.3-4.1 5.9-.9L12 3.5Z" /></svg>
        </button>
      </div>
    </div>
    <div class="rating-slider">
      <label :for="sliderId" class="rating-slider__label">拖动微调（每格半分）</label>
      <input
        :id="sliderId"
        class="rating-slider__input"
        type="range"
        min="0"
        max="10"
        step="0.5"
        :value="value ?? 0"
        data-test="short-feed-rating-slider"
        @input="$emit('preview', Number($event.target.value))"
        @change="$emit('commit', Number($event.target.value))"
      />
      <div class="rating-slider__ticks" aria-hidden="true"><span>0</span><span>5</span><span>10</span></div>
    </div>
  </div>
</template>

<script>
import { formatRating, starFill } from '../format.js';

// 只管显示与手势；写入、失败回滚与「写入中先显示新分」都在宿主（value 由宿主给）。
export default {
  name: 'RatingPicker',
  props: {
    // 当前显示的分数（null 表示未评分）。
    value: { type: Number, default: null }
  },
  emits: ['preview', 'commit'],
  data() {
    return { sliderId: 'short-feed-rating-slider' };
  },
  computed: {
    valueText() {
      return formatRating(this.value);
    }
  },
  methods: {
    starFill
  }
};
</script>
