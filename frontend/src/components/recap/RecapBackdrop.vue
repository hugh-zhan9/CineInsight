<template>
  <section :class="['recap-backdrop', `recap-backdrop--${size}`]" data-test="recap-backdrop">
    <div class="recap-backdrop__stage" aria-hidden="true">
      <div
        v-for="(src, index) in shown"
        :key="src"
        :class="['recap-backdrop__frame', `recap-backdrop__frame--pan${index % 3}`, { 'is-active': index === active }]"
        :style="{ backgroundImage: `url(&quot;${src}&quot;)` }"
      ></div>
      <div v-if="!shown.length" class="recap-backdrop__placeholder"></div>
    </div>
    <div class="recap-backdrop__scrim" aria-hidden="true"></div>
    <div class="recap-backdrop__content"><slot /></div>
  </section>
</template>

<script>
// 本期最爱 / 正在播放的大幅背景：真实画面帧逐张预载（一次只拉一张，单帧路由本身也限并发），
// 加载成功的帧交叉淡入、慢速推拉（Ken Burns）。全部失败时退回缩略图，再失败显示主题色渐变。
// 偏好减少动态效果时只显示第一帧，不轮播不推拉。
import { prefersReducedMotion } from '../../utils/recap.js';

const ROTATE_MS = 6000;

export default {
  name: 'RecapBackdrop',
  props: {
    frames: { type: Array, default: () => [] },
    fallback: { type: String, default: '' },
    size: { type: String, default: 'large' }
  },
  data() {
    return { shown: [], active: 0 };
  },
  computed: {
    sourceKey() { return [...this.frames, this.fallback].join('|'); }
  },
  watch: {
    sourceKey: { immediate: true, handler() { this.start(); } }
  },
  beforeUnmount() {
    this.stop();
  },
  methods: {
    stop() {
      this._token = Symbol('backdrop');
      clearInterval(this._timer);
      this._timer = null;
    },
    start() {
      this.stop();
      const token = this._token;
      this.shown = [];
      this.active = 0;
      const queue = [...this.frames];
      const loadNext = () => {
        if (this._token !== token) return;
        const src = queue.shift();
        if (!src) {
          if (!this.shown.length && this.fallback && this._fallbackTried !== token) {
            this._fallbackTried = token;
            queue.push(this.fallback);
            loadNext();
          }
          return;
        }
        if (typeof Image !== 'function') return;
        const image = new Image();
        image.decoding = 'async';
        image.onload = () => {
          if (this._token !== token) return;
          this.shown.push(src);
          if (this.shown.length === 2) this.rotate(token);
          if (!prefersReducedMotion()) loadNext();
        };
        image.onerror = () => loadNext();
        image.src = src;
      };
      loadNext();
    },
    rotate(token) {
      if (prefersReducedMotion() || this._timer) return;
      this._timer = setInterval(() => {
        if (this._token !== token || this.shown.length < 2) return;
        this.active = (this.active + 1) % this.shown.length;
      }, ROTATE_MS);
    }
  }
};
</script>

<style scoped>
.recap-backdrop {
  display: flex;
  position: relative;
  overflow: hidden;
  isolation: isolate;
  border-radius: var(--radius-lg);
  background: var(--player-bg);
  color: var(--recap-hero-fg);
}
.recap-backdrop--large { min-height: 420px; }
.recap-backdrop--medium { min-height: 300px; }
.recap-backdrop--small { min-height: 200px; }
.recap-backdrop__stage { position: absolute; inset: 0; z-index: -2; }
.recap-backdrop__frame {
  position: absolute;
  inset: -4%;
  background-size: cover;
  background-position: center;
  opacity: 0;
  transition: opacity 1.6s ease;
  animation: recap-kenburns-a 18s ease-in-out infinite alternate;
  will-change: transform, opacity;
}
.recap-backdrop__frame.is-active { opacity: 1; }
.recap-backdrop__frame--pan1 { animation-name: recap-kenburns-b; }
.recap-backdrop__frame--pan2 { animation-name: recap-kenburns-c; }
.recap-backdrop__placeholder {
  position: absolute;
  inset: 0;
  background:
    radial-gradient(120% 90% at 85% 10%, color-mix(in srgb, var(--warning-color) 55%, transparent), transparent 60%),
    radial-gradient(90% 90% at 10% 100%, color-mix(in srgb, var(--accent-color) 75%, transparent), transparent 65%),
    var(--player-bg);
}
.recap-backdrop__scrim {
  position: absolute;
  inset: 0;
  z-index: -1;
  background:
    linear-gradient(90deg, var(--recap-scrim) 0%, var(--recap-scrim-mid) 48%, transparent 82%),
    linear-gradient(0deg, var(--recap-scrim) 0%, transparent 55%);
}
.recap-backdrop__content { position: relative; flex: 1; display: flex; flex-direction: column; justify-content: flex-end; min-width: 0; }
@keyframes recap-kenburns-a { from { transform: scale(1.02) translate(0, 0); } to { transform: scale(1.12) translate(-2%, -1.5%); } }
@keyframes recap-kenburns-b { from { transform: scale(1.1) translate(2%, 1%); } to { transform: scale(1.02) translate(-1%, 0); } }
@keyframes recap-kenburns-c { from { transform: scale(1.04) translate(-1.5%, 1.5%); } to { transform: scale(1.13) translate(1.5%, -1%); } }
@media (prefers-reduced-motion: reduce) {
  .recap-backdrop__frame { animation: none; transition: none; }
}
</style>
