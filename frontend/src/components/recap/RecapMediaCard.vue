<template>
  <button
    type="button"
    :class="['btn-plain', 'recap-card', `recap-card--${kind}`, `recap-card--${variant}`, { 'recap-card--unavailable': !available }]"
    :disabled="!available"
    :title="available ? title : `${title}（已不在库中）`"
    :data-test="`recap-card-${kind}`"
    @click="$emit('open')"
  >
    <span class="recap-card__media" :style="aspectStyle">
      <img
        v-if="src && !failed"
        ref="img"
        :src="src"
        :class="{ 'is-loaded': loaded }"
        alt=""
        loading="lazy"
        decoding="async"
        @load="loaded = true"
        @error="failed = true"
      />
      <span v-else class="recap-card__fallback"><RecapIcon :name="kind === 'image' ? 'images' : 'plays'" /></span>
      <span v-if="rank" class="recap-card__rank">{{ rank }}</span>
      <span v-if="badge" class="recap-card__badge">{{ badge }}</span>
      <span v-if="progressPercent > 0" class="recap-card__progress" aria-hidden="true">
        <span class="recap-card__progress-fill" :style="{ width: `${progressPercent}%` }"></span>
      </span>
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
    // card / tile（只有图）/ poster（2:3 海报，观看首页的海报墙）。
    variant: { type: String, default: 'card' },
    // 指定图片地址时用它代替缩略图（海报走 /preview/video-artwork，找不到时服务端已回落缩略图）。
    cover: { type: String, default: '' },
    // 图片按原始宽高比展示（马赛克墙），视频统一 16:9。
    width: { type: Number, default: 0 },
    height: { type: Number, default: 0 },
    // 观看进度（0..1），大于 0 时在封面底部渲染细影院进度条。
    progress: { type: Number, default: 0 }
  },
  emits: ['open'],
  data() {
    return { failed: false, loaded: false };
  },
  computed: {
    src() { return this.cover || (this.kind === 'image' ? imageThumbnail(this.id) : videoThumbnail(this.id)); },
    progressPercent() {
      const value = Number(this.progress || 0);
      if (!Number.isFinite(value) || value <= 0) return 0;
      return Math.min(100, Math.max(2, Math.round(value * 100)));
    },
    aspectStyle() {
      if (this.kind === 'image' && this.width > 0 && this.height > 0) {
        return { aspectRatio: `${this.width} / ${Math.min(this.height, this.width * 2.2)}` };
      }
      return {};
    }
  },
  mounted() {
    if (this.$refs.img?.complete && this.$refs.img.naturalWidth > 0) this.loaded = true;
  },
  watch: {
    src() {
      this.failed = false;
      this.loaded = false;
    }
  }
};
</script>

<style scoped>
.recap-card {
  display: flex;
  flex-direction: column;
  gap: 10px;
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
  border: 0;
  background: var(--thumb-bg);
  box-shadow: inset 0 0 0 1px var(--cinema-card-ring), var(--cinema-card-shadow);
  transition: transform 320ms cubic-bezier(0.22, 1, 0.36, 1), box-shadow 320ms cubic-bezier(0.22, 1, 0.36, 1);
}
.recap-card--image .recap-card__media { aspect-ratio: 4 / 3; }
.recap-card--poster .recap-card__media { aspect-ratio: 2 / 3; }
.recap-card__media img {
  display: block;
  width: 100%;
  height: 100%;
  object-fit: cover;
  opacity: 0;
  transition: opacity 240ms ease, transform 520ms cubic-bezier(0.22, 1, 0.36, 1);
}
.recap-card__media img.is-loaded { opacity: 1; }
.recap-card:not(:disabled):hover .recap-card__media {
  transform: translateY(-4px);
  box-shadow: inset 0 0 0 1px var(--cinema-card-hover-ring), var(--cinema-card-hover-shadow);
}
.recap-card:not(:disabled):hover .recap-card__media img { transform: scale(1.045); }
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
  font-size: var(--fs-display);
}
.recap-card__rank {
  position: absolute;
  left: 8px;
  bottom: 2px;
  color: var(--recap-hero-fg);
  font-size: var(--fs-display);
  font-weight: var(--fw-bold);
  line-height: 1;
  letter-spacing: -0.04em;
  text-shadow: 0 2px 12px var(--recap-scrim);
}
.recap-card__badge {
  position: absolute;
  top: 8px;
  right: 8px;
  padding: 2px 8px;
  border-radius: var(--radius-pill);
  background: var(--media-badge-bg);
  color: var(--media-badge-fg);
  box-shadow: inset 0 0 0 1px var(--cinema-card-ring);
  -webkit-backdrop-filter: blur(8px);
  backdrop-filter: blur(8px);
  font-size: var(--fs-caption);
  font-weight: var(--fw-semibold);
}
.recap-card__progress {
  position: absolute;
  left: 0;
  right: 0;
  bottom: 0;
  height: 3px;
  background: var(--media-progress-track);
}
.recap-card__progress-fill {
  display: block;
  height: 100%;
  background: var(--accent-color);
  border-radius: 0 var(--radius-pill) var(--radius-pill) 0;
}
.recap-card__text { display: grid; gap: 3px; min-width: 0; padding: 0 2px; }
.recap-card__text strong { overflow: hidden; font-size: var(--fs-secondary); font-weight: var(--fw-semibold); text-overflow: ellipsis; white-space: nowrap; letter-spacing: -0.005em; }
.recap-card__text small { color: var(--text-secondary); font-size: var(--fs-caption); }
.recap-card--tile .recap-card__media { border-radius: var(--radius); }
@media (prefers-reduced-motion: reduce) {
  .recap-card__media, .recap-card__media img { transition: none; }
  .recap-card__media img { opacity: 1; }
  .recap-card:not(:disabled):hover .recap-card__media, .recap-card:not(:disabled):hover .recap-card__media img { transform: none; }
}
</style>
