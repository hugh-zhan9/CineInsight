<template>
  <figure class="recap-clock" data-test="recap-clock">
    <svg viewBox="0 0 240 240" role="img" :aria-label="ariaLabel">
      <circle class="recap-clock__dial" cx="120" cy="120" :r="inner - 6" />
      <g v-for="bar in bars" :key="bar.hour">
        <line class="recap-clock__track" :x1="bar.x0" :y1="bar.y0" :x2="bar.xt" :y2="bar.yt" />
        <line v-if="bar.plays" :class="['recap-clock__plays', { 'is-peak': bar.hour === peakHour }]" :x1="bar.x0" :y1="bar.y0" :x2="bar.xp" :y2="bar.yp">
          <title>{{ bar.hour }} 点 · 播放 {{ bar.plays }} 次 · 看图 {{ bar.views }} 次</title>
        </line>
        <line v-if="bar.views" class="recap-clock__views" :x1="bar.xp" :y1="bar.yp" :x2="bar.xv" :y2="bar.yv" />
      </g>
      <text v-for="mark in marks" :key="mark.label" class="recap-clock__mark" :x="mark.x" :y="mark.y">{{ mark.label }}</text>
      <text class="recap-clock__center" x="120" y="116">{{ peakHour >= 0 ? `${peakHour} 点` : '—' }}</text>
      <text class="recap-clock__caption" x="120" y="138">最常出现的钟点</text>
    </svg>
  </figure>
</template>

<script>
// 24 小时径向分布：每个钟点一根放射条，播放（主色）在内、看图（暖色）接在外；最高的钟点加亮。
const INNER = 58;
const SPAN = 50;

function point(hour, radius) {
  const angle = ((hour / 24) * 360 - 90) * (Math.PI / 180);
  return { x: 120 + Math.cos(angle) * radius, y: 120 + Math.sin(angle) * radius };
}

export default {
  name: 'RecapClock',
  props: {
    hours: { type: Array, default: () => [] },
    peakHour: { type: Number, default: -1 }
  },
  data: () => ({ inner: INNER }),
  computed: {
    max() { return Math.max(1, ...this.hours.map(hour => Number(hour.plays || 0) + Number(hour.views || 0))); },
    bars() {
      return this.hours.map(item => {
        const plays = Number(item.plays || 0);
        const views = Number(item.views || 0);
        const start = point(item.hour, INNER);
        const track = point(item.hour, INNER + SPAN);
        const playEnd = point(item.hour, INNER + (SPAN * plays) / this.max);
        const viewEnd = point(item.hour, INNER + (SPAN * (plays + views)) / this.max);
        return { hour: item.hour, plays, views, x0: start.x, y0: start.y, xt: track.x, yt: track.y, xp: playEnd.x, yp: playEnd.y, xv: viewEnd.x, yv: viewEnd.y };
      });
    },
    marks() {
      return [0, 6, 12, 18].map(hour => ({ label: String(hour), ...point(hour, INNER + SPAN + 13) }));
    },
    ariaLabel() {
      return this.peakHour >= 0 ? `24 小时分布，最常在 ${this.peakHour} 点` : '24 小时分布，暂无记录';
    }
  }
};
</script>

<style scoped>
.recap-clock { margin: 0; display: flex; justify-content: center; }
.recap-clock svg { width: 100%; max-width: 260px; height: auto; overflow: visible; }
.recap-clock__dial { fill: var(--panel-subtle-bg); stroke: var(--hairline); }
.recap-clock__track { stroke: var(--hairline-soft); stroke-width: 7; stroke-linecap: round; }
.recap-clock__plays { stroke: color-mix(in srgb, var(--accent-color) 70%, transparent); stroke-width: 7; stroke-linecap: round; }
.recap-clock__plays.is-peak { stroke: var(--accent-color); stroke-width: 9; }
.recap-clock__views { stroke: var(--warning-color); stroke-width: 7; stroke-linecap: round; }
.recap-clock__mark { fill: var(--text-muted); font-size: 11px; text-anchor: middle; dominant-baseline: middle; }
.recap-clock__center { fill: var(--text-primary); font-size: 26px; font-weight: 800; text-anchor: middle; }
.recap-clock__caption { fill: var(--text-secondary); font-size: 11px; text-anchor: middle; }
</style>
