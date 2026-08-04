<template>
  <section class="page-content insights-page">
    <div class="insights-heading">
      <div><h2>片库洞察</h2><p>基于本地片库与观看状态的只读统计</p></div>
      <button type="button" class="btn-secondary" :disabled="loading" @click="loadStats">{{ loading ? '统计中...' : '刷新' }}</button>
    </div>
    <p v-if="error" class="insights-error" role="alert">{{ error }}</p>
    <div v-else-if="loading && !stats" class="insights-empty">正在汇总片库...</div>
    <div v-else-if="!stats?.summary?.video_count" class="insights-empty">
      <h3>片库还没有视频</h3><p>完成一次扫描后，这里会显示存储、观看和标签分布。</p>
    </div>
    <template v-else>
      <div class="insights-summary">
        <article><span>视频</span><strong>{{ formatNumber(stats.summary.video_count) }}</strong></article>
        <article><span>总时长</span><strong>{{ formatDuration(stats.summary.total_duration) }}</strong></article>
        <article><span>存储</span><strong>{{ formatBytes(stats.summary.total_size) }}</strong></article>
        <article><span>已看比例</span><strong>{{ Number(stats.summary.watched_percent || 0).toFixed(1) }}%</strong></article>
      </div>

      <div class="insights-grid">
        <article class="insights-panel insights-panel--wide">
          <div class="insights-panel__heading"><h3>近一年观看</h3><span>按最后播放日期</span></div>
          <div class="watch-heatmap" aria-label="近一年观看热力图">
            <span v-for="day in heatmapDays" :key="day.date" :class="`heat-${day.level}`" :title="`${day.date} · ${day.count} 部`"></span>
          </div>
        </article>

        <BucketChart title="目录存储" :items="stats.storage_by_directory" value-key="bytes" :format-value="formatBytes" />
        <BucketChart title="标签存储" :items="stats.storage_by_tag" value-key="bytes" :format-value="formatBytes" />
        <BucketChart title="分辨率存储" :items="stats.storage_by_resolution" value-key="bytes" :format-value="formatBytes" />
        <BucketChart title="AI 标签 Top" :items="stats.top_ai_tags" value-key="count" :format-value="formatNumber" />

        <article class="insights-panel">
          <div class="insights-panel__heading"><h3>评分分布</h3><span>个人评分</span></div>
          <div v-if="stats.rating_distribution?.length" class="rating-bars">
            <div v-for="bucket in stats.rating_distribution" :key="bucket.rating" class="rating-bar">
              <span>{{ bucket.rating }}</span><i :style="{ height: `${ratingHeight(bucket.count)}%` }"></i><small>{{ bucket.count }}</small>
            </div>
          </div>
          <p v-else class="panel-empty">尚无个人评分。</p>
        </article>
      </div>
    </template>
  </section>
</template>

<script>
import { GetLibraryInsights } from '../../wailsjs/go/main/App';
import BucketChart from './insights/BucketChart.vue';

export default {
  name: 'InsightsPage',
  components: { BucketChart },
  data() { return { loading: false, error: '', stats: null }; },
  computed: {
    heatmapDays() {
      const counts = new Map((this.stats?.watch_heatmap || []).map(day => [String(day.date).slice(0, 10), Number(day.count || 0)]));
      // 后端按数据库会话时区 CAST(last_played_at AS DATE)（单机场景即本地
      // 时区）；坐标轴同样用本地日期构建，避免 UTC 轴导致的错位一天。
      const localDate = (value) => {
        const year = value.getFullYear();
        const month = String(value.getMonth() + 1).padStart(2, '0');
        const day = String(value.getDate()).padStart(2, '0');
        return `${year}-${month}-${day}`;
      };
      const end = new Date(this.stats?.generated_at || Date.now());
      const cursor = new Date(end.getFullYear(), end.getMonth(), end.getDate());
      cursor.setDate(cursor.getDate() - 364);
      const result = [];
      for (let index = 0; index < 365; index += 1) {
        const date = localDate(cursor);
        const count = counts.get(date) || 0;
        result.push({ date, count, level: count === 0 ? 0 : Math.min(4, Math.ceil(Math.log2(count + 1))) });
        cursor.setDate(cursor.getDate() + 1);
      }
      return result;
    },
    maxRatingCount() { return Math.max(1, ...(this.stats?.rating_distribution || []).map(item => Number(item.count || 0))); }
  },
  mounted() { this.loadStats(); },
  methods: {
    async loadStats() {
      if (this.loading) return;
      this.loading = true; this.error = '';
      try { this.stats = await GetLibraryInsights(); }
      catch (err) { this.error = '读取片库洞察失败：' + err; }
      finally { this.loading = false; }
    },
    formatNumber(value) { return new Intl.NumberFormat('zh-CN').format(Number(value || 0)); },
    formatBytes(value) {
      const bytes = Number(value || 0); if (bytes <= 0) return '0 B';
      const units = ['B', 'KB', 'MB', 'GB', 'TB']; const index = Math.min(4, Math.floor(Math.log(bytes) / Math.log(1024)));
      return `${(bytes / (1024 ** index)).toFixed(index > 1 ? 1 : 0)} ${units[index]}`;
    },
    formatDuration(seconds) {
      const hours = Number(seconds || 0) / 3600;
      return hours >= 24 ? `${(hours / 24).toFixed(1)} 天` : `${hours.toFixed(1)} 小时`;
    },
    ratingHeight(count) { return Math.max(8, Number(count || 0) / this.maxRatingCount * 100); }
  }
};
</script>

<style scoped>
.insights-page { display: grid; gap: 18px; }
.insights-heading { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
.insights-heading p, .insights-panel__heading span, .panel-empty { color: var(--text-secondary); }
.insights-summary { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; }
.insights-summary article, .insights-panel, .insights-empty { border: 1px solid var(--border-color); border-radius: var(--radius-lg); background: var(--panel-bg); padding: 16px; }
.insights-summary article { display: grid; gap: 8px; }
.insights-summary span { color: var(--text-secondary); font-size: 13px; }
.insights-summary strong { color: var(--text-primary); font-size: 24px; }
.insights-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 14px; }
.insights-panel--wide { grid-column: 1 / -1; }
.insights-panel__heading { display: flex; align-items: baseline; justify-content: space-between; margin-bottom: 14px; }
.watch-heatmap { display: grid; grid-template-columns: repeat(53, minmax(5px, 1fr)); grid-auto-flow: column; grid-template-rows: repeat(7, 8px); gap: 3px; }
.watch-heatmap span { border-radius: 2px; background: var(--control-hover-bg); }
.watch-heatmap .heat-1 { background: color-mix(in srgb, var(--accent-color) 30%, var(--control-hover-bg)); }
.watch-heatmap .heat-2 { background: color-mix(in srgb, var(--accent-color) 50%, var(--control-hover-bg)); }
.watch-heatmap .heat-3 { background: color-mix(in srgb, var(--accent-color) 72%, var(--control-hover-bg)); }
.watch-heatmap .heat-4 { background: var(--accent-color); }
.rating-bars { display: flex; align-items: end; height: 190px; gap: 8px; overflow-x: auto; }
.rating-bar { display: grid; grid-template-rows: 20px 130px 20px; align-items: end; min-width: 28px; text-align: center; color: var(--text-secondary); font-size: 11px; }
.rating-bar i { display: block; width: 18px; max-height: 130px; margin: 0 auto; border-radius: 5px 5px 2px 2px; background: var(--accent-color); }
.insights-error { color: var(--danger-color); }
@media (max-width: 900px) { .insights-summary, .insights-grid { grid-template-columns: 1fr 1fr; } }
@media (max-width: 620px) { .insights-summary, .insights-grid { grid-template-columns: 1fr; } .insights-panel--wide { grid-column: auto; } }
</style>
