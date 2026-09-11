<template>
  <section class="page-content insights-page">
    <div class="insights-heading">
      <div><h2>片库洞察</h2><p>基于本地片库与观看状态的只读统计</p></div>
      <button type="button" class="btn-secondary" :disabled="loading || imageLoading" @click="refresh">{{ loading || imageLoading ? '统计中...' : '刷新' }}</button>
    </div>
    <p v-if="error" class="insights-error" role="alert">{{ error }}</p>
    <div v-else-if="loading && !stats" class="insights-empty">正在汇总片库...</div>
    <div v-else-if="!stats?.summary?.video_count" class="insights-empty">
      <h3>片库还没有视频</h3><p>完成一次扫描后，这里会显示存储、观看和标签分布。</p>
    </div>
    <template v-else>
      <div class="insights-summary">
        <article>
          <span>视频总数</span>
          <strong>{{ formatNumber(stats.summary.video_count) }}</strong>
          <small>最近 30 天 +{{ formatNumber(stats.summary.recent_added_count) }}</small>
        </article>
        <article>
          <span>总时长</span>
          <strong>{{ formatDuration(stats.summary.total_duration) }}</strong>
          <small>平均单片 {{ averageDurationText }}</small>
        </article>
        <article>
          <span>存储占用</span>
          <strong>{{ formatBytes(stats.summary.total_size) }}</strong>
          <small>{{ volumeSummaryText }}</small>
        </article>
        <article>
          <span>观看覆盖率</span>
          <div class="insights-summary__inline" data-test="insights-viewed-coverage">
            <strong>{{ formatPercent(stats.summary.viewed_percent) }}</strong>
            <small>{{ formatNumber(stats.summary.viewed_count) }} / {{ formatNumber(stats.summary.video_count) }} 部</small>
          </div>
          <div class="insights-progress"><i :style="{ width: `${Math.min(100, Math.max(0, Number(stats.summary.viewed_percent || 0)))}%` }"></i></div>
          <small>播放过、有观看进度或已标记，每部只计一次</small>
          <small data-test="insights-watched-marks">标记已看 {{ formatNumber(stats.summary.watched_count) }} 部（{{ formatPercent(stats.summary.watched_percent) }}）</small>
        </article>
      </div>

      <div class="insights-grid">
        <article class="insights-panel insights-panel--wide">
          <div class="insights-panel__heading">
            <h3>近一年观看热力</h3>
            <span>共 {{ formatNumber(heatmapTotal) }} 次播放 · 最长连续 {{ longestWatchStreak }} 天</span>
            <div class="heatmap-legend" aria-hidden="true">
              少<i class="heat-0"></i><i class="heat-1"></i><i class="heat-2"></i><i class="heat-3"></i><i class="heat-4"></i>多
            </div>
          </div>
          <p data-test="insights-play-events">{{ playEventsSummaryText }}</p>
          <p class="insights-play-events-note">累计事件包含重复播放及已删除影片；历史事件每部仅补记一次。热力图仅展示近一年。</p>
          <div class="watch-heatmap" aria-label="近一年观看热力图">
            <span v-for="day in heatmapDays" :key="day.date" :class="`heat-${day.level}`" :title="`${day.date} · ${day.count} 次播放`"></span>
          </div>
        </article>

        <BucketChart title="目录存储" :items="stats.storage_by_directory" value-key="bytes" :format-value="formatBytes" />
        <BucketChart title="标签存储" :items="stats.storage_by_tag" value-key="bytes" :format-value="formatBytes" />
        <BucketChart title="分辨率存储" :items="stats.storage_by_resolution" value-key="bytes" :format-value="formatBytes" />
        <BucketChart title="AI 标签 Top" :items="stats.top_ai_tags" value-key="count" :format-value="formatNumber" />

        <article class="insights-panel insights-panel--wide">
          <div class="insights-panel__heading">
            <h3>个人评分分布</h3>
            <span>
              已评分 {{ formatNumber(ratedCount) }} / {{ formatNumber(stats.summary.video_count) }}
              <template v-if="ratingMedian !== null"> · 中位数 {{ ratingMedian.toFixed(1) }}</template>
              · 半分制 21 档
            </span>
          </div>
          <div v-if="ratedCount > 0" class="rating-bars">
            <div v-for="bucket in ratingBuckets" :key="bucket.rating" class="rating-bar">
              <i :style="{ height: `${ratingHeight(bucket.count)}%` }" :title="`${bucket.rating.toFixed(1)} · ${bucket.count} 部`"></i>
              <small>{{ bucket.rating.toFixed(1) }}</small>
            </div>
          </div>
          <p v-else class="panel-empty">尚无个人评分。</p>
        </article>
      </div>
    </template>

    <div class="insights-heading insights-heading--section">
      <div><h2>图片</h2><p>基于图片库的只读统计</p></div>
    </div>
    <p v-if="imageError" class="insights-error" role="alert">{{ imageError }}</p>
    <div v-else-if="imageLoading && !imageStats" class="insights-empty">正在汇总图片库...</div>
    <div v-else-if="!imageStats?.summary?.image_count" class="insights-empty">
      <h3>图片库还没有图片</h3><p>完成一次图片扫描后，这里会显示存储与格式分布。</p>
    </div>
    <template v-else>
      <div class="insights-summary insights-summary--images">
        <article><span>图片总数</span><strong>{{ formatNumber(imageStats.summary.image_count) }}</strong></article>
        <article><span>存储占用</span><strong>{{ formatBytes(imageStats.summary.total_size) }}</strong></article>
        <article><span>收藏</span><strong>{{ formatNumber(imageStats.summary.favorite_count) }}</strong></article>
      </div>
      <div class="insights-grid">
        <BucketChart title="图片目录存储" :items="imageStats.storage_by_directory" value-key="total_size" :format-value="formatBytes" />
        <BucketChart title="图片格式存储" :items="imageStats.storage_by_format" value-key="total_size" :format-value="formatBytes" />
      </div>
    </template>
  </section>
</template>

<script>
import { GetImageInsights, GetLibraryInsights } from '../../wailsjs/go/main/App';
import BucketChart from './insights/BucketChart.vue';
import { registerCommands, unregisterCommands } from '../utils/commandRegistry.js';

export default {
  name: 'InsightsPage',
  components: { BucketChart },
  props: {
    // 卷可用性由扫描目录与监听状态推出，不再单开一条后端统计。
    directories: { type: Array, default: () => [] }
  },
  data() { return { loading: false, error: '', stats: null, imageLoading: false, imageError: '', imageStats: null }; },
  computed: {
    averageDurationText() {
      const count = Number(this.stats?.summary?.video_count || 0);
      if (count === 0) return '—';
      return this.formatDuration(Number(this.stats.summary.total_duration || 0) / count);
    },
    // 「N 个卷 · M 个当前不可用」：卷取扫描目录路径的顶层挂载点，
    // 不可用以目录自己的监听状态为准。拿不到状态时只报卷数，不猜可用性。
    volumeSummaryText() {
      const roots = new Map();
      for (const directory of this.directories) {
        const path = String(directory?.path || '');
        if (!path) continue;
        const match = path.match(/^(\/Volumes\/[^/]+|\/)/);
        const root = match ? match[1] : path;
        const unavailable = ['unavailable', 'error'].includes(String(directory?.watch_state || ''));
        roots.set(root, (roots.get(root) || false) || unavailable);
      }
      if (roots.size === 0) return '';
      const broken = [...roots.values()].filter(Boolean).length;
      return broken > 0 ? `${roots.size} 个卷 · ${broken} 个当前不可用` : `${roots.size} 个卷`;
    },
    heatmapTotal() {
      return (this.stats?.watch_heatmap || []).reduce((total, day) => total + Number(day.count || 0), 0);
    },
    // 播放事件账本的两项：全库流水总数与按来源的拆分。总数不限一年窗口，
    // 与只看近一年的热力图刻意是两个数。
    playEventsSummaryText() {
      const total = Number(this.stats?.total_play_events || 0);
      const bySource = this.stats?.plays_by_source || {};
      const labels = { desktop_play: '桌面', desktop_random: '随机', mobile_feed: '手机', legacy: '历史' };
      const ordered = Object.keys(labels).filter(source => Number(bySource[source] || 0) > 0);
      // 后端将来加了新来源也要能显示出来，不认识的键按原样排在后面。
      const extra = Object.keys(bySource)
        .filter(source => !(source in labels) && Number(bySource[source] || 0) > 0)
        .sort();
      const parts = [...ordered, ...extra]
        .map(source => `${labels[source] || source} ${this.formatNumber(bySource[source])}`);
      const head = `播放事件 ${this.formatNumber(total)}`;
      return parts.length > 0 ? `${head} · ${parts.join(' · ')}` : head;
    },
    longestWatchStreak() {
      let best = 0;
      let current = 0;
      for (const day of this.heatmapDays) {
        current = day.count > 0 ? current + 1 : 0;
        if (current > best) best = current;
      }
      return best;
    },
    // 后端只返回出现过的评分值；21 档要补齐空档，否则柱状图的横轴会塌缩。
    ratingBuckets() {
      const counts = new Map((this.stats?.rating_distribution || [])
        .map(item => [Number(item.rating).toFixed(1), Number(item.count || 0)]));
      return Array.from({ length: 21 }, (_, step) => {
        const rating = step * 0.5;
        return { rating, count: counts.get(rating.toFixed(1)) || 0 };
      });
    },
    ratedCount() {
      return this.ratingBuckets.reduce((total, bucket) => total + bucket.count, 0);
    },
    ratingMedian() {
      const total = this.ratedCount;
      if (total === 0) return null;
      const middle = (total + 1) / 2;
      let seen = 0;
      for (const bucket of this.ratingBuckets) {
        seen += bucket.count;
        if (seen >= middle) return bucket.rating;
      }
      return null;
    },
    heatmapDays() {
      const counts = new Map((this.stats?.watch_heatmap || []).map(day => [String(day.date).slice(0, 10), Number(day.count || 0)]));
      // 后端读 play_events 后在 Go 侧按 time.Local 归并成 YYYY-MM-DD；
      // 坐标轴同样用本地日期构建，避免 UTC 轴导致的错位一天。
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
    maxRatingCount() { return Math.max(1, ...this.ratingBuckets.map(bucket => bucket.count)); }
  },
  mounted() {
    this.refresh();
    // 命令面板的本页动作（D-029）。
    registerCommands('insights-page', [{
      id: 'action:insights-refresh',
      group: 'action',
      label: '刷新洞察数据',
      keywords: ['refresh insights', '刷新洞察'],
      enabled: () => !this.loading && !this.imageLoading,
      run: () => this.refresh()
    }]);
  },
  beforeUnmount() { unregisterCommands('insights-page'); },
  methods: {
    formatPercent(value) {
      const percent = Number(value || 0);
      if (percent > 0 && percent < 0.1) return '<0.1%';
      return `${percent.toFixed(1)}%`;
    },
    // 视频与图片分区各自独立加载：任一侧失败只影响自己的分区。
    refresh() { this.loadStats(); this.loadImageStats(); },
    async loadStats() {
      if (this.loading) return;
      this.loading = true; this.error = '';
      try { this.stats = await GetLibraryInsights(); }
      catch (err) { this.error = '读取片库洞察失败：' + err; }
      finally { this.loading = false; }
    },
    async loadImageStats() {
      if (this.imageLoading) return;
      this.imageLoading = true; this.imageError = '';
      try { this.imageStats = await GetImageInsights(); }
      catch (err) { this.imageError = '读取图片洞察失败：' + err; }
      finally { this.imageLoading = false; }
    },
    formatNumber(value) { return new Intl.NumberFormat('zh-CN').format(Number(value || 0)); },
    formatBytes(value) {
      const bytes = Number(value || 0); if (bytes <= 0) return '0 B';
      const units = ['B', 'KB', 'MB', 'GB', 'TB']; const index = Math.min(4, Math.floor(Math.log(bytes) / Math.log(1024)));
      return `${(bytes / (1024 ** index)).toFixed(index > 1 ? 1 : 0)} ${units[index]}`;
    },
    formatDuration(seconds) {
      const total = Number(seconds || 0);
      const hours = total / 3600;
      if (hours >= 24) return `${(hours / 24).toFixed(1)} 天`;
      if (hours >= 1) return `${hours.toFixed(1)} 小时`;
      return `${Math.round(total / 60)} 分钟`;
    },
    ratingHeight(count) { return Number(count || 0) === 0 ? 2 : Math.max(6, Number(count) / this.maxRatingCount * 100); }
  }
};
</script>

<style scoped>
.insights-play-events-note { color: var(--text-muted); font-size: 12px; margin: 6px 0 12px; }
.insights-page { display: grid; gap: 18px; }
.insights-heading { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
.insights-heading p, .insights-panel__heading span, .panel-empty { color: var(--text-secondary); }
.insights-summary { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; }
.insights-summary--images { grid-template-columns: repeat(3, minmax(0, 1fr)); }
.insights-heading--section { margin-top: 6px; }
.insights-summary article, .insights-panel, .insights-empty { border: 1px solid var(--hairline); border-radius: var(--radius-md); background: var(--panel-bg); padding: 14px 16px; }
.insights-summary article { display: grid; gap: 4px; align-content: start; }
.insights-summary span { color: var(--text-muted); font-size: 12px; }
.insights-summary strong { color: var(--text-primary); font-size: 30px; font-weight: 700; font-family: var(--font-mono); letter-spacing: -0.02em; }
.insights-summary small { color: var(--text-muted); font-size: 11.5px; }
.insights-summary__inline { display: flex; align-items: baseline; gap: 8px; }
.insights-summary__inline small { font-family: var(--font-mono); }
.insights-progress { height: 5px; margin-top: 3px; border-radius: 3px; background: var(--neutral-softer); }
.insights-progress i { display: block; height: 100%; border-radius: inherit; background: var(--accent-color); }
.heatmap-legend { display: flex; align-items: center; gap: 5px; margin-left: auto; color: var(--text-muted); font-size: 11px; }
.heatmap-legend i { width: 10px; height: 10px; border-radius: 2px; }
.insights-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 14px; }
.insights-panel--wide { grid-column: 1 / -1; }
.insights-panel__heading { display: flex; align-items: baseline; gap: 10px; margin-bottom: 14px; }
.insights-panel__heading h3 { font-size: 13px; }
.watch-heatmap { display: grid; grid-template-columns: repeat(53, minmax(5px, 1fr)); grid-auto-flow: column; grid-template-rows: repeat(7, 8px); gap: 3px; }
.watch-heatmap span, .heatmap-legend .heat-0 { border-radius: 2px; background: var(--neutral-faint); }
.watch-heatmap .heat-1, .heatmap-legend .heat-1 { background: color-mix(in srgb, var(--accent-color) 30%, var(--neutral-faint)); }
.watch-heatmap .heat-2, .heatmap-legend .heat-2 { background: color-mix(in srgb, var(--accent-color) 50%, var(--neutral-faint)); }
.watch-heatmap .heat-3, .heatmap-legend .heat-3 { background: color-mix(in srgb, var(--accent-color) 72%, var(--neutral-faint)); }
.watch-heatmap .heat-4, .heatmap-legend .heat-4 { background: var(--accent-color); }
.rating-bars { display: flex; align-items: flex-end; height: 120px; gap: 4px; }
.rating-bar { flex: 1; display: grid; grid-template-rows: 1fr auto; align-items: end; min-width: 0; text-align: center; }
.rating-bar i { display: block; width: 100%; border-radius: 3px 3px 0 0; background: var(--accent-color); opacity: 0.9; }
.rating-bar small { color: var(--text-muted); font-size: 10px; font-family: var(--font-mono); }
.insights-error { color: var(--danger-color); }
@media (max-width: 900px) { .insights-summary, .insights-grid { grid-template-columns: 1fr 1fr; } }
@media (max-width: 620px) { .insights-summary, .insights-grid { grid-template-columns: 1fr; } .insights-panel--wide { grid-column: auto; } }
</style>
