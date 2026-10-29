<template>
  <div class="recap-film" data-test="recap-month-strip">
    <MediaRail class="recap-film__track">
      <button
        v-for="item in months"
        :key="item.month"
        type="button"
        :class="['btn-plain', 'recap-film__frame', { 'is-empty': !item.best, 'is-unavailable': item.best && !item.best.video.available }]"
        :disabled="!item.best || !item.best.video.available"
        :title="item.best ? `${item.month}月之最：${item.best.video.title}（${item.best.count} 次）` : `${item.month}月没有播放`"
        @click="$emit('open', item.best.video)"
      >
        <span class="recap-film__month">{{ item.month }}<small>月</small></span>
        <span class="recap-film__shot">
          <img v-if="item.best && !failed[item.month]" :src="thumb(item)" alt="" loading="lazy" decoding="async" @error="failed = { ...failed, [item.month]: true }" />
        </span>
        <span class="recap-film__caption">
          <strong>{{ item.best ? item.best.video.title : '—' }}</strong>
          <small v-if="item.best">{{ item.best.count }} 次</small>
        </span>
      </button>
    </MediaRail>
  </div>
</template>

<script>
import MediaRail from '../ui/MediaRail.vue';
// 年度「每月之最」胶片条：12 格，每格是当月播放最多的那部（与本期最爱同一排序口径）。没有播放的月份留空格。
import { videoThumbnail } from '../../utils/recap.js';

export default {
  name: 'RecapMonthStrip',
  components: { MediaRail },
  props: { months: { type: Array, default: () => [] } },
  emits: ['open'],
  data: () => ({ failed: {} }),
  methods: {
    thumb(item) { return videoThumbnail(item.best?.video?.id); }
  }
};
</script>

<style scoped>
.recap-film {
  min-width: 0;
  padding: 16px 14px;
  border-radius: var(--radius-lg);
  background:
    radial-gradient(circle, var(--bg-color) 3px, transparent 3.5px) 0 5px / 18px 14px repeat-x,
    radial-gradient(circle, var(--bg-color) 3px, transparent 3.5px) 0 calc(100% - 5px) / 18px 14px repeat-x,
    var(--player-bg);
}
.recap-film__track { --rail-card-width: 100px; }
.recap-film__frame {
  display: grid;
  gap: 6px;
  padding: 0;
  border: 0;
  background: transparent;
  color: var(--recap-hero-fg);
  text-align: left;
  cursor: pointer;
}
.recap-film__frame:disabled { cursor: default; }
.recap-film__month { font-size: var(--fs-title); font-weight: var(--fw-bold); letter-spacing: -0.02em; }
.recap-film__month small { margin-left: 2px; font-size: var(--fs-caption); font-weight: var(--fw-medium); color: var(--recap-hero-muted); }
.recap-film__shot { display: block; aspect-ratio: 16 / 10; overflow: hidden; border-radius: var(--radius-sm); background: var(--recap-hero-chip); }
.recap-film__shot img { display: block; width: 100%; height: 100%; object-fit: cover; transition: transform 400ms ease; }
.recap-film__frame:not(:disabled):hover .recap-film__shot img { transform: scale(1.08); }
.recap-film__caption { display: grid; gap: 1px; min-width: 0; }
.recap-film__caption strong { overflow: hidden; font-size: var(--fs-caption); font-weight: var(--fw-semibold); text-overflow: ellipsis; white-space: nowrap; }
.recap-film__caption small { color: var(--recap-hero-muted); font-size: var(--fs-caption); }
.is-empty .recap-film__shot { opacity: 0.4; }
.is-unavailable .recap-film__shot { filter: grayscale(1); opacity: 0.6; }
@media (prefers-reduced-motion: reduce) { .recap-film__shot img { transition: none; } }
</style>
