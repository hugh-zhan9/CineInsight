<template>
  <TransitionGroup tag="ol" name="recap-feed" class="recap-feed" data-test="recap-feed">
    <li v-for="item in items" :key="itemKey(item)" class="recap-feed__item">
      <button
        type="button"
        class="btn-plain recap-feed__open"
        :disabled="!item.available"
        :data-test="`recap-feed-${item.kind}`"
        @click="$emit('open', item)"
      >
        <span :class="['recap-feed__thumb', `recap-feed__thumb--${item.kind}`]">
          <img v-if="!failed[itemKey(item)]" :src="thumb(item)" alt="" loading="lazy" decoding="async" @error="markFailed(item)" />
        </span>
        <span class="recap-feed__text">
          <strong>{{ item.title || '已删除的项' }}</strong>
          <small>{{ item.available ? '' : '已不在库中 · ' }}{{ item.kind === 'image' ? '看图' : '播放' }} · {{ sourceLabel(item.kind, item.source) }}</small>
        </span>
        <time class="recap-feed__time" :datetime="item.at" :title="formatClock(item.at)">{{ absoluteTime ? formatClock(item.at) : relativeTime(item.at, now) }}</time>
      </button>
    </li>
  </TransitionGroup>
</template>

<script>
// 实时页的今日流水：最新的在上，新条目从上方滑入（TransitionGroup），减少动态效果时不做动画。
// 「查看全部」对话框也用它列全天流水：那里含已不在库中的项，灰显、标「已不在库中」、不可点。
import { formatClock, imageThumbnail, relativeTime, sourceLabel, videoThumbnail } from '../../utils/recap.js';

export default {
  name: 'RecapLiveFeed',
  props: {
    absoluteTime: { type: Boolean, default: false },
    items: { type: Array, default: () => [] },
    now: { type: Number, default: () => Date.now() }
  },
  emits: ['open'],
  data: () => ({ failed: {} }),
  methods: {
    formatClock,
    relativeTime,
    sourceLabel,
    itemKey(item) { return `${item.kind}-${item.id}-${item.at}`; },
    thumb(item) { return item.kind === 'image' ? imageThumbnail(item.id) : videoThumbnail(item.id); },
    markFailed(item) { this.failed = { ...this.failed, [this.itemKey(item)]: true }; }
  }
};
</script>

<style scoped>
.recap-feed { position: relative; display: grid; gap: 6px; margin: 0; padding: 0; list-style: none; }
.recap-feed__open {
  display: grid;
  grid-template-columns: 76px minmax(0, 1fr) auto;
  align-items: center;
  gap: 12px;
  width: 100%;
  padding: 6px 10px 6px 6px;
  border: 1px solid var(--hairline);
  border-radius: var(--radius-md);
  background: var(--panel-bg);
  color: var(--text-primary);
  text-align: left;
  cursor: pointer;
  transition: border-color var(--transition), transform var(--transition);
}
.recap-feed__open:not(:disabled):hover { border-color: var(--accent-border); transform: translateX(3px); }
.recap-feed__open:disabled { cursor: default; opacity: 0.6; }
.recap-feed__thumb { display: block; width: 76px; aspect-ratio: 16 / 9; overflow: hidden; border-radius: var(--radius-sm); background: var(--thumb-fallback-bg); }
.recap-feed__thumb--image { aspect-ratio: 4 / 3; width: 56px; justify-self: center; }
.recap-feed__thumb img { display: block; width: 100%; height: 100%; object-fit: cover; }
.recap-feed__text { display: grid; gap: 2px; min-width: 0; }
.recap-feed__text strong { overflow: hidden; font-size: var(--fs-secondary); text-overflow: ellipsis; white-space: nowrap; }
.recap-feed__text small { color: var(--text-secondary); font-size: var(--fs-caption); }
.recap-feed__time { color: var(--text-muted); font-size: var(--fs-caption); white-space: nowrap; font-variant-numeric: tabular-nums; }
.recap-feed-enter-active { transition: opacity 420ms ease, transform 420ms cubic-bezier(0.2, 0.8, 0.2, 1); }
.recap-feed-enter-from { opacity: 0; transform: translateY(-14px) scale(0.98); }
.recap-feed-move { transition: transform 420ms ease; }
@media (prefers-reduced-motion: reduce) {
  .recap-feed-enter-active, .recap-feed-move { transition: none; }
}
</style>
