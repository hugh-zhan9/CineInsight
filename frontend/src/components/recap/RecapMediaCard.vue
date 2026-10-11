<template>
  <button
    type="button"
    :class="['recap-card', `recap-card--${kind}`, `recap-card--${variant}`, { 'recap-card--unavailable': !available }]"
    :disabled="!available"
    :title="available ? title : `${title}（已不在库中）`"
    :data-test="`recap-card-${kind}`"
    @click="$emit('open')"
  >
    <span class="recap-card__media" :style="aspectStyle">
      <img v-if="src && !failed" :src="src" alt="" loading="lazy" decoding="async" @error="failed = true" />
      <span v-else class="recap-card__fallback"><RecapIcon :name="kind === 'image' ? 'images' : 'plays'" /></span>
      <span v-if="rank" class="recap-card__rank">{{ rank }}</span>
      <span v-if="badge" class="recap-card__badge">{{ badge }}</span>
    </span>
    <span v-if="variant !== 'tile'" class="recap-card__text">
      <strong>{{ title }}</strong>
      <small v-if="subtitle">{{ subtitle }}</small>
    </span>
  </button>
</template>

<script>
// 回顾页里的一张视频或图片卡片：缩略图走既有受控路由，懒加载；已进回收站或失效的项灰显且不可点。
import RecapIcon from './RecapIcon.vue';
import { imageThumbnail, videoThumbnail } from '../../utils/recap.js';

export default {
  name: 'RecapMediaCard',
  components: { RecapIcon },
  props: {
    kind: { type: String, default: 'video' },
    id: { type: Number, required: true },
    title: { type: String, default: '' },
    subtitle: { type: String, default: '' },
    badge: { type: String, default: '' },
    rank: { type: [Number, String], default: '' },
    available: { type: Boolean, default: true },
    variant: { type: String, default: 'card' },
    // 图片按原始宽高比展示（马赛克墙），视频统一 16:9。
    width: { type: Number, default: 0 },
    height: { type: Number, default: 0 }
  },
  emits: ['open'],
  data() {
    return { failed: false };
  },
  computed: {
    src() { return this.kind === 'image' ? imageThumbnail(this.id) : videoThumbnail(this.id); },
    aspectStyle() {
      if (this.kind === 'image' && this.width > 0 && this.height > 0) {
        return { aspectRatio: `${this.width} / ${Math.min(this.height, this.width * 2.2)}` };
      }
      return {};
    }
  },
  watch: {
    id() { this.failed = false; }
  }
};
</script>

<style scoped>
.recap-card {
  display: flex;
  flex-direction: column;
  gap: 8px;
  width: 100%;
  min-width: 0;
  padding: 0;
  border: 0;
  background: transparent;
  color: var(--text-primary);
  text-align: left;
  cursor: pointer;
}
.recap-card:disabled { cursor: default; }
.recap-card__media {
  position: relative;
  display: block;
  width: 100%;
  aspect-ratio: 16 / 9;
  overflow: hidden;
  border-radius: var(--radius-md);
  border: 1px solid var(--hairline);
  background: var(--thumb-bg);
  transition: transform 260ms ease, box-shadow 260ms ease;
}
.recap-card--image .recap-card__media { aspect-ratio: 4 / 3; }
.recap-card__media img {
  display: block;
  width: 100%;
  height: 100%;
  object-fit: cover;
  transition: transform 500ms ease;
}
.recap-card:not(:disabled):hover .recap-card__media { transform: translateY(-4px); box-shadow: var(--shadow-popover); }
.recap-card:not(:disabled):hover .recap-card__media img { transform: scale(1.06); }
.recap-card:focus-visible .recap-card__media { outline: 2px solid var(--accent-color); outline-offset: 2px; }
.recap-card--unavailable .recap-card__media { filter: grayscale(1); opacity: 0.55; }
.recap-card__fallback {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 100%;
  height: 100%;
  background: var(--thumb-fallback-bg);
  color: var(--thumb-fg);
  font-size: 28px;
}
.recap-card__rank {
  position: absolute;
  left: 8px;
  bottom: 2px;
  color: var(--recap-hero-fg);
  font-size: 34px;
  font-weight: 800;
  line-height: 1;
  letter-spacing: -0.04em;
  text-shadow: 0 2px 12px var(--recap-scrim);
}
.recap-card__badge {
  position: absolute;
  top: 8px;
  right: 8px;
  padding: 2px 8px;
  border-radius: 999px;
  background: var(--media-badge-bg);
  color: var(--media-badge-fg);
  font-size: 11.5px;
  font-weight: 600;
}
.recap-card__text { display: grid; gap: 2px; min-width: 0; }
.recap-card__text strong { overflow: hidden; font-size: 13px; font-weight: 600; text-overflow: ellipsis; white-space: nowrap; }
.recap-card__text small { color: var(--text-secondary); font-size: 12px; }
.recap-card--tile .recap-card__media { border-radius: var(--radius); }
@media (prefers-reduced-motion: reduce) {
  .recap-card__media, .recap-card__media img { transition: none; }
  .recap-card:not(:disabled):hover .recap-card__media, .recap-card:not(:disabled):hover .recap-card__media img { transform: none; }
}
</style>
