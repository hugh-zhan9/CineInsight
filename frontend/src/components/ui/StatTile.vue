<template>
  <component
    :is="tag"
    :type="tag === 'button' ? 'button' : undefined"
    :class="['stat-tile', `stat-tile--${size}`, { 'stat-tile--action': tag === 'button', 'stat-tile--on': selected }]"
  >
    <span v-if="iconList.length || $slots.icon" :class="['stat-tile__icon', `stat-tile__icon--${tone}`]">
      <slot name="icon"><svg viewBox="0 0 24 24" aria-hidden="true"><path v-for="(d, index) in iconList" :key="index" :d="d" /></svg></slot>
    </span>
    <span class="stat-tile__value" data-test="stat-tile-value"><slot name="value">{{ value }}</slot><small v-if="unit">{{ unit }}</small></span>
    <span class="stat-tile__label" data-test="stat-tile-label">{{ label }}</span>
    <slot />
    <small v-if="note" class="stat-tile__note" data-test="stat-tile-note">{{ note }}</small>
    <span v-if="hint" class="stat-tile__hint">{{ hint }}</span>
  </component>
</template>

<script>
import { iconPaths } from './icons.js';

// 统计卡（D-UI-08）：观看回顾、存储体检、图片清理审阅、管理首页共用的一张卡——图标、大数字（可带单位）、
// 标签、注释与提示。原先三处各抄一份「和回顾页统计卡同一个样子」，这里收拢成一个。
// size 只调字号与留白：lg 是观看侧（回顾页、首页）的大号，md 是图片清理审阅，sm 是存储体检。
// tag="button" 时整张卡可点（管理首页、图片清理审阅的区域切换），selected 标出当前选中的那张。
export default {
  name: 'StatTile',
  props: {
    tag: { type: String, default: 'article' },
    size: { type: String, default: 'md' },
    tone: { type: String, default: 'accent' },
    // 线性图标：图标集 ui/icons.js 里的名字（新写法），或路径数组（24×24 视窗，旧写法）；也可以用 icon 插槽。
    icon: { type: [Array, String], default: () => [] },
    value: { type: [String, Number], default: '' },
    unit: { type: String, default: '' },
    label: { type: String, default: '' },
    note: { type: String, default: '' },
    hint: { type: String, default: '' },
    selected: { type: Boolean, default: false }
  },
  computed: {
    iconList() { return typeof this.icon === 'string' ? iconPaths(this.icon) || [] : this.icon; }
  }
};
</script>

<style scoped>
.stat-tile {
  position: relative;
  display: grid;
  align-content: start;
  justify-items: start;
  gap: 4px;
  overflow: hidden;
  padding: 16px 18px 14px;
  border: 1px solid var(--hairline);
  border-radius: var(--radius-lg);
  background: var(--panel-bg);
  color: var(--text-primary);
  font: inherit;
  text-align: left;
}
.stat-tile::after {
  content: '';
  position: absolute;
  right: -30px;
  top: -30px;
  width: 110px;
  height: 110px;
  border-radius: 50%;
  background: radial-gradient(circle, color-mix(in srgb, var(--accent-color) 14%, transparent), transparent 70%);
  pointer-events: none;
}
.stat-tile--action { cursor: pointer; transition: border-color var(--transition), background var(--transition), transform var(--transition); }
.stat-tile--action:hover { border-color: var(--border-strong); transform: translateY(-1px); }
.stat-tile--on, .stat-tile--on:hover { border-color: var(--accent-color); background: color-mix(in srgb, var(--accent-soft) 55%, var(--panel-bg)); box-shadow: 0 0 0 1px var(--accent-color); }
.stat-tile__icon { display: inline-grid; place-items: center; width: 34px; height: 34px; border-radius: 10px; font-size: 18px; }
.stat-tile__icon svg { width: 18px; height: 18px; fill: none; stroke: currentColor; stroke-width: 1.75; stroke-linecap: round; stroke-linejoin: round; }
.stat-tile__icon--accent { background: var(--accent-soft); color: var(--accent-text); }
.stat-tile__icon--danger { background: var(--danger-soft); color: var(--danger-color); }
.stat-tile__icon--warning { background: var(--warning-soft); color: var(--warning-text); }
.stat-tile__value { display: flex; align-items: baseline; gap: 4px; margin-top: 6px; font-size: 34px; font-weight: 800; line-height: 1; letter-spacing: -0.03em; font-variant-numeric: tabular-nums; }
.stat-tile__value small { color: var(--text-secondary); font-size: 14px; font-weight: 600; letter-spacing: 0; }
.stat-tile__label { color: var(--text-primary); font-size: 13px; font-weight: 600; }
.stat-tile__note { color: var(--text-secondary); font-size: 12px; }
.stat-tile__hint { color: var(--text-muted); font-size: 11.5px; }
.stat-tile--md .stat-tile__note { font-variant-numeric: tabular-nums; }
.stat-tile--sm .stat-tile__value { font-size: 30px; }

/* 观看侧大号：留白更宽、数字更大，标签与注释更轻。 */
.stat-tile--lg { padding: 18px 18px 16px; }
.stat-tile--lg::after { background: radial-gradient(circle, color-mix(in srgb, var(--accent-color) 16%, transparent), transparent 70%); }
.stat-tile--lg .stat-tile__value { font-size: 38px; font-variant-numeric: normal; }
.stat-tile--lg .stat-tile__label { color: var(--text-secondary); font-weight: 400; }
.stat-tile--lg .stat-tile__note { color: var(--text-muted); }
</style>
