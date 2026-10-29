<template>
  <div :class="['recap-calendar', `recap-calendar--${mode}`]" data-test="recap-calendar">
    <template v-if="mode === 'month'">
      <span v-for="label in weekdays" :key="label" class="recap-calendar__weekday">{{ label }}</span>
      <button
        type="button"
        class="btn-plain"
        v-for="cell in monthCells"
        :key="cell.key"
        :class="['recap-calendar__cell', cell.blank ? 'is-blank' : `level-${level(cell)}`, { 'is-future': isFuture(cell) }]"
        :title="cell.blank ? '' : tooltip(cell)"
        :aria-label="cell.blank ? undefined : `${tooltip(cell)}，查看当天记录`"
        :disabled="cell.blank || isFuture(cell)"
        @click="$emit('select-day', cell)"
      >
        <template v-if="!cell.blank">{{ Number(cell.date.slice(8)) }}</template>
      </button>
    </template>
    <div v-else class="recap-calendar__weeks">
      <div v-for="(week, index) in yearWeeks" :key="index" class="recap-calendar__week">
        <button
          type="button"
          class="btn-plain"
          v-for="cell in week"
          :key="cell.key"
          :class="['recap-calendar__dot', cell.blank ? 'is-blank' : `level-${level(cell)}`, { 'is-future': isFuture(cell) }]"
          :title="cell.blank ? '' : tooltip(cell)"
        :aria-label="cell.blank ? undefined : `${tooltip(cell)}，查看当天记录`"
        :disabled="cell.blank || isFuture(cell)"
        @click="$emit('select-day', cell)"
        ></button>
      </div>
    </div>
  </div>
</template>

<script>
// 日历热力：月度是一张周一开头的月历，年度是 GitHub 式按周排列的小格。颜色按当期最大值分五档，
// 由主色与面板色混出，深浅主题都只用令牌。还没到的日子淡显。
import { formatDateLabel, heatLevel, monthCalendarCells, yearCalendarWeeks } from '../../utils/recap.js';

export default {
  name: 'RecapCalendar',
  emits: ['select-day'],
  props: {
    days: { type: Array, default: () => [] },
    mode: { type: String, default: 'month' },
    today: { type: String, default: '' }
  },
  data: () => ({ weekdays: ['一', '二', '三', '四', '五', '六', '日'] }),
  computed: {
    max() { return Math.max(0, ...this.days.map(day => Number(day.plays || 0) + Number(day.views || 0))); },
    monthCells() { return monthCalendarCells(this.days); },
    yearWeeks() { return yearCalendarWeeks(this.days); }
  },
  methods: {
    level(cell) { return heatLevel(Number(cell.plays || 0) + Number(cell.views || 0), this.max); },
    isFuture(cell) { return !cell.blank && Boolean(this.today) && cell.date > this.today; },
    tooltip(cell) { return `${formatDateLabel(cell.date)} · 播放 ${cell.plays || 0} 次 · 看图 ${cell.views || 0} 次`; }
  }
};
</script>

<style scoped>
.recap-calendar--month { display: grid; grid-template-columns: repeat(7, minmax(0, 1fr)); gap: 6px; }
.recap-calendar__weekday { color: var(--text-muted); font-size: var(--fs-caption); text-align: center; }
.recap-calendar__cell {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 34px;
  border-radius: var(--radius-sm);
  color: var(--text-secondary);
  font-size: var(--fs-caption);
  font-variant-numeric: tabular-nums;
}
.recap-calendar__cell.level-3, .recap-calendar__cell.level-4 { color: var(--accent-on); font-weight: var(--fw-bold); }
.recap-calendar__weeks { display: flex; gap: 3px; overflow-x: auto; padding-bottom: 4px; }
.recap-calendar__week { display: grid; grid-template-rows: repeat(7, 11px); gap: 3px; }
.recap-calendar__dot { width: 11px; height: 11px; border-radius: 3px; }
.is-blank { background: transparent; }
.level-0 { background: var(--panel-muted-bg); }
.level-1 { background: color-mix(in srgb, var(--accent-color) 25%, var(--panel-bg)); }
.level-2 { background: color-mix(in srgb, var(--accent-color) 50%, var(--panel-bg)); }
.level-3 { background: color-mix(in srgb, var(--accent-color) 78%, var(--panel-bg)); }
.level-4 { background: var(--accent-color); }
.recap-calendar button { padding: 0; }
.recap-calendar button:not(:disabled) { cursor: pointer; }
.recap-calendar button:not(:disabled):hover { outline: 2px solid var(--accent-color); outline-offset: 1px; }
.is-future { opacity: 0.35; }
</style>
