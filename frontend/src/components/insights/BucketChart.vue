<template>
  <article class="insights-panel">
    <div class="insights-panel__heading"><h3>{{ title }}</h3><span>Top {{ Math.min(items.length, 8) }}</span></div>
    <div v-if="items.length" class="bucket-list">
      <div v-for="item in items.slice(0, 8)" :key="item.label" class="bucket-row">
        <span :title="item.label">{{ item.label || '未命名' }}</span>
        <div><i :style="{ width: `${barWidth(item)}%` }"></i></div>
        <strong>{{ formatValue(item[valueKey]) }}</strong>
      </div>
    </div>
    <p v-else class="panel-empty">暂无数据。</p>
  </article>
</template>

<script>
export default {
  name: 'BucketChart',
  props: {
    title: { type: String, required: true },
    items: { type: Array, default: () => [] },
    valueKey: { type: String, required: true },
    formatValue: { type: Function, required: true }
  },
  computed: { maxValue() { return Math.max(1, ...this.items.map(item => Number(item[this.valueKey] || 0))); } },
  methods: { barWidth(item) { return Math.max(2, Number(item[this.valueKey] || 0) / this.maxValue * 100); } }
};
</script>

<style scoped>
.insights-panel { border: 1px solid var(--border-color); border-radius: var(--radius-lg); background: var(--panel-bg); padding: 16px; }
.insights-panel__heading { display: flex; align-items: baseline; justify-content: space-between; margin-bottom: 14px; }
.insights-panel__heading span, .panel-empty { color: var(--text-secondary); }
.bucket-list { display: grid; gap: 10px; }
.bucket-row { display: grid; grid-template-columns: minmax(80px, 0.9fr) minmax(80px, 1.4fr) max-content; align-items: center; gap: 10px; font-size: 12px; }
.bucket-row > span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.bucket-row > div { height: 8px; overflow: hidden; border-radius: 999px; background: var(--control-hover-bg); }
.bucket-row i { display: block; height: 100%; border-radius: inherit; background: var(--accent-color); }
.bucket-row strong { color: var(--text-secondary); font-variant-numeric: tabular-nums; }
</style>
