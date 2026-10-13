<template>
  <section class="page-content insights-page">
    <PageHeader title="片库洞察" subtitle="基于本地片库与观看状态的只读统计">
      <template #actions>
      <button
        v-if="tab === 'overview'"
        type="button"
        class="btn btn--secondary"
        :disabled="loading || imageLoading"
        @click="refresh"
      >{{ loading || imageLoading ? '统计中…' : '刷新' }}</button>
      <button
        v-else
        type="button"
        class="btn btn--secondary"
        :disabled="storageStatus.loading || storageStatus.processing"
        data-test="storage-refresh"
        @click="refreshStorage"
      >{{ storageStatus.loading ? '体检中…' : '刷新' }}</button>
      </template>
      <!-- 「存储体检」（2026-10-12）：空间都去哪了、哪些能回收，列出的久未观看大文件可以直接移到废纸篓。
           页签（D-UD-06 AppTabs）带 insights-tab-* 钩子；体检正在删除时锁住，不让切走。 -->
      <template #below>
        <AppTabs v-model="tab" :tabs="pageTabs" aria-label="洞察内容" />
      </template>
    </PageHeader>
    <StorageHealthPanel
      v-if="tab === 'storage'"
      ref="storagePanel"
      @open-video-id="$emit('open-video-id', $event)"
      @run-command="(id, label) => $emit('run-command', id, label)"
      @videos-changed="handleVideosChanged"
      @status-change="setStorageStatus"
    />
    <template v-else>
    <AppBanner v-if="error" tone="danger" title="读取片库洞察失败" :message="error" data-test="insights-error" />
    <div v-else-if="loading && !stats" class="insights-loading" role="status" aria-label="正在汇总片库"><SkeletonBlock :lines="5" /></div>
    <EmptyState v-else-if="!stats?.summary?.video_count" icon="videos" title="片库还没有视频" message="完成一次扫描后，这里会显示存储、观看和标签分布。" />
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
        <!-- 观看记录（D-PC43、PLAY-07）：热力图按来源分列。桌面两种来源在应用启动外部播放器时即记，
             标为「启动播放」；内嵌、手机与 Jellyfin 是累计播放越过阈值才记的有效观看。 -->
        <article class="insights-panel insights-panel--wide" data-test="insights-watch-records">
          <div class="insights-panel__heading">
            <h3>观看记录</h3>
            <span data-test="insights-heatmap-total">近一年{{ heatmapSourceLabel }} {{ formatNumber(heatmapTotal) }} 条 · 最长连续 {{ longestWatchStreak }} 天</span>
            <div class="heatmap-legend" aria-hidden="true">
              少<i class="heat-0"></i><i class="heat-1"></i><i class="heat-2"></i><i class="heat-3"></i><i class="heat-4"></i>多
            </div>
          </div>
          <!-- 按来源看（D-UD-06 SegmentedControl）：选项名后面是近一年的条数，每项带 insights-heatmap-source-* 钩子。 -->
          <SegmentedControl
            class="heatmap-sources"
            label="按来源查看"
            :options="heatmapSourceSegments"
            :model-value="activeHeatmapSource"
            data-test="insights-heatmap-sources"
            @update:model-value="heatmapSource = $event"
          />
          <p class="insights-play-events" data-test="insights-play-events">{{ playEventsSummaryText }}</p>
          <p class="insights-play-events-note" data-test="insights-watch-records-note">口径：「启动播放」在应用启动外部播放器时即记一次，不代表看了多久；内嵌观看、手机观看与 Jellyfin 观看在同一次播放累计超过 60 秒（短片取时长的一半）或看完时记一次。累计事件包含重复播放及已删除影片；历史事件每部仅补记一次。热力图仅展示近一年。</p>
          <div class="watch-heatmap" aria-label="近一年观看记录热力图">
            <span v-for="day in heatmapDays" :key="day.date" :class="`heat-${day.level}`" :title="day.title"></span>
          </div>
        </article>

        <BucketChart title="目录存储" :items="stats.storage_by_directory" value-key="bytes" :format-value="formatBytes" />
        <BucketChart title="标签存储" :items="stats.storage_by_tag" value-key="bytes" :format-value="formatBytes" />
        <BucketChart title="分辨率存储" :items="stats.storage_by_resolution" value-key="bytes" :format-value="formatBytes" />
        <BucketChart title="标签 Top" :items="stats.top_ai_tags" value-key="count" :format-value="formatNumber" />

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

    <PageHeader class="insights-section-head" level="section" title="图片" subtitle="基于图片库的只读统计" />
    <AppBanner v-if="imageError" tone="danger" title="读取图片洞察失败" :message="imageError" data-test="insights-image-error" />
    <div v-else-if="imageLoading && !imageStats" class="insights-loading" role="status" aria-label="正在汇总图片库"><SkeletonBlock :lines="3" /></div>
    <EmptyState v-else-if="!imageStats?.summary?.image_count" icon="images" title="图片库还没有图片" message="完成一次图片扫描后，这里会显示存储与格式分布。" />
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
    </template>
  </section>
</template>

<script>
import { GetImageInsights, GetLibraryInsights } from '../../wailsjs/go/main/App';
import BucketChart from './insights/BucketChart.vue';
import StorageHealthPanel from './insights/StorageHealthPanel.vue';
import AppBanner from './ui/AppBanner.vue';
import AppTabs from './ui/AppTabs.vue';
import EmptyState from './ui/EmptyState.vue';
import PageHeader from './ui/PageHeader.vue';
import SegmentedControl from './ui/SegmentedControl.vue';
import SkeletonBlock from './ui/SkeletonBlock.vue';
import { registerCommands, unregisterCommands } from '../utils/commandRegistry.js';

// 播放账本各来源的叫法（D-PC43）。桌面两种来源在启动外部播放器时即记，叫「启动播放」；
// inline_view / mobile_feed / jellyfin_view 是累计播放越过阈值（或看完）才记的有效观看。
// 顺序即图例与摘要的顺序；后端将来加了新来源，按原样排在后面。
const PLAY_SOURCE_LABELS = {
  desktop_play: '启动播放',
  desktop_random: '随机启动播放',
  inline_view: '内嵌观看',
  queue_view: '队列观看',
  mobile_feed: '手机观看',
  jellyfin_view: 'Jellyfin 观看',
  legacy: '历史补记'
};

function orderedSources(counts) {
  const known = Object.keys(PLAY_SOURCE_LABELS).filter(source => Number(counts[source] || 0) > 0);
  const extra = Object.keys(counts).filter(source => !(source in PLAY_SOURCE_LABELS) && Number(counts[source] || 0) > 0).sort();
  return [...known, ...extra];
}

function playSourceLabel(source) {
  return PLAY_SOURCE_LABELS[source] || source;
}

export default {
  name: 'InsightsPage',
  components: { AppBanner, AppTabs, BucketChart, EmptyState, PageHeader, SegmentedControl, SkeletonBlock, StorageHealthPanel },
  // busy-change：存储体检正在删除视频，App 据此不让切走（撤销条挂在本页上）。
  emits: ['open-video-id', 'run-command', 'busy-change'],
  props: {
    // 卷可用性由扫描目录与监听状态推出，不再单开一条后端统计。
    directories: { type: Array, default: () => [] },
    // 宿主的 @videos-changed（存储体检删除或撤销删除了视频）。声明成 prop 而不是放进 emits：离开洞察页之后
    // 再从提示或「最近操作」撤销时本页已经卸下，Vue 会丢掉它的 $emit，拿着这个监听才能照样让 App 刷新片库。
    onVideosChanged: { type: Function, default: null }
  },
  data() {
    return {
      tab: 'overview',
      storageStatus: { loading: false, processing: false },
      statsReloadPending: false,
      tabs: [{ key: 'overview', label: '概览' }, { key: 'storage', label: '存储体检' }],
      loading: false, error: '', stats: null, imageLoading: false, imageError: '', imageStats: null, heatmapSource: 'all'
    };
  },
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
    // 近一年各来源的条数（每天的 by_source 相加），供「按来源分列」的切换与计数。
    heatmapSourceTotals() {
      const totals = {};
      for (const day of this.stats?.watch_heatmap || []) {
        for (const [source, count] of Object.entries(day?.by_source || {})) {
          totals[source] = (totals[source] || 0) + Number(count || 0);
        }
      }
      return totals;
    },
    heatmapSourceOptions() {
      const totals = this.heatmapSourceTotals;
      const all = (this.stats?.watch_heatmap || []).reduce((total, day) => total + Number(day.count || 0), 0);
      return [
        { source: 'all', label: '全部', count: all },
        ...orderedSources(totals).map(source => ({ source, label: playSourceLabel(source), count: totals[source] }))
      ];
    },
    heatmapSourceSegments() {
      return this.heatmapSourceOptions.map(option => ({
        value: option.source,
        label: `${option.label} ${this.formatNumber(option.count)}`,
        dataTest: `insights-heatmap-source-${option.source}`
      }));
    },
    pageTabs() {
      return this.tabs.map(item => ({ ...item, disabled: this.storageStatus.processing, dataTest: `insights-tab-${item.key}` }));
    },
    // 选中的来源刷新后不在了（比如一年窗口滑过），回到「全部」。
    activeHeatmapSource() {
      return this.heatmapSourceOptions.some(option => option.source === this.heatmapSource) ? this.heatmapSource : 'all';
    },
    heatmapSourceLabel() {
      return this.activeHeatmapSource === 'all' ? '' : playSourceLabel(this.activeHeatmapSource);
    },
    heatmapTotal() {
      return this.heatmapDays.reduce((total, day) => total + day.count, 0);
    },
    // 播放事件账本的两项：全库流水总数与按来源的拆分。总数不限一年窗口，
    // 与只看近一年的热力图刻意是两个数。
    playEventsSummaryText() {
      const total = Number(this.stats?.total_play_events || 0);
      const bySource = this.stats?.plays_by_source || {};
      const parts = orderedSources(bySource)
        .map(source => `${playSourceLabel(source)} ${this.formatNumber(bySource[source])}`);
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
      const selected = this.activeHeatmapSource;
      const byDate = new Map((this.stats?.watch_heatmap || []).map(day => [String(day.date).slice(0, 10), day]));
      const dayCount = (day) => {
        if (!day) return 0;
        return selected === 'all' ? Number(day.count || 0) : Number(day.by_source?.[selected] || 0);
      };
      // 悬停提示总带当天的来源拆分，选中某一来源时也看得到别的来源。
      const dayTitle = (date, day) => {
        const total = Number(day?.count || 0);
        const bySource = day?.by_source || {};
        const parts = orderedSources(bySource).map(source => `${playSourceLabel(source)} ${bySource[source]}`);
        return parts.length > 0 ? `${date} · 共 ${total} 条（${parts.join(' · ')}）` : `${date} · ${total} 条`;
      };
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
        const day = byDate.get(date);
        const count = dayCount(day);
        result.push({ date, count, level: count === 0 ? 0 : Math.min(4, Math.ceil(Math.log2(count + 1))), title: dayTitle(date, day) });
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
      enabled: () => (this.tab === 'storage'
        ? !this.storageStatus.loading && !this.storageStatus.processing
        : !this.loading && !this.imageLoading),
      run: () => (this.tab === 'storage' ? this.refreshStorage() : this.refresh())
    }]);
  },
  beforeUnmount() {
    this.disposed = true;
    unregisterCommands('insights-page');
    // 页面整个被卸掉时（正常切页在删除中会被 App 拦下）别把「删除进行中」留在 App 里。
    if (this.storageStatus.processing) this.$emit('busy-change', false);
  },
  methods: {
    formatPercent(value) {
      const percent = Number(value || 0);
      if (percent > 0 && percent < 0.1) return '<0.1%';
      return `${percent.toFixed(1)}%`;
    },
    // 视频与图片分区各自独立加载：任一侧失败只影响自己的分区。
    refresh() { this.loadStats(); this.loadImageStats(); },
    refreshStorage() { return this.$refs.storagePanel?.refresh(); },
    setStorageStatus(status) {
      const busy = Boolean(status?.processing);
      if (busy !== this.storageStatus.processing) this.$emit('busy-change', busy);
      this.storageStatus = { loading: Boolean(status?.loading), processing: busy };
    },
    // 存储体检删除或撤销了视频：概览的总数也跟着重取，再交给 App 刷新片库与顶栏。
    handleVideosChanged(payload) {
      // 已卸下（离开洞察页之后才撤销）：概览不用重取，$emit 也送不到，直接调宿主的同一个监听。
      if (this.disposed) {
        this.onVideosChanged?.(payload);
        return;
      }
      this.refresh();
      this.$emit('videos-changed', payload);
    },
    // 正在取时又要求重取（例如存储体检刚删了视频）：记下来，这一轮结束后再取一次，免得留着删除前的数。
    async loadStats() {
      if (this.loading) { this.statsReloadPending = true; return; }
      this.loading = true; this.error = '';
      try { this.stats = await GetLibraryInsights(); }
      catch (err) { this.error = String(err?.message || err || '').trim() || '请稍后重试'; }
      finally {
        this.loading = false;
        if (this.statsReloadPending) { this.statsReloadPending = false; this.loadStats(); }
      }
    },
    async loadImageStats() {
      if (this.imageLoading) return;
      this.imageLoading = true; this.imageError = '';
      try { this.imageStats = await GetImageInsights(); }
      catch (err) { this.imageError = String(err?.message || err || '').trim() || '请稍后重试'; }
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
.insights-play-events-note { color: var(--text-muted); font-size: var(--fs-caption); margin: 6px 0 var(--space-3); }
.insights-page { display: grid; gap: var(--space-4); }
.insights-page > :deep(.ui-page-header) { margin-bottom: 0; }
.insights-section-head { margin-top: 6px; }
.insights-panel__heading span, .panel-empty { color: var(--text-secondary); }
.insights-loading { padding: var(--space-4) 0; }
.insights-summary { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: var(--space-3); }
.insights-summary--images { grid-template-columns: repeat(3, minmax(0, 1fr)); }
.insights-summary article, .insights-panel { border: 1px solid var(--hairline); border-radius: var(--radius); background: var(--panel-bg); padding: 14px var(--space-4); }
.insights-summary article { display: grid; gap: var(--space-1); align-content: start; }
.insights-summary span { color: var(--text-muted); font-size: var(--fs-caption); }
.insights-summary strong { color: var(--text-primary); font-size: var(--fs-display); font-weight: var(--fw-bold); font-family: var(--font-mono); letter-spacing: -0.02em; }
.insights-summary small { color: var(--text-muted); font-size: var(--fs-caption); }
.insights-summary__inline { display: flex; align-items: baseline; gap: var(--space-2); }
.insights-summary__inline small { font-family: var(--font-mono); }
.insights-progress { height: 5px; margin-top: 3px; border-radius: 3px; background: var(--neutral-softer); }
.insights-progress i { display: block; height: 100%; border-radius: inherit; background: var(--accent-color); }
.heatmap-legend { display: flex; align-items: center; gap: 5px; margin-left: auto; color: var(--text-muted); font-size: var(--fs-caption); }
.heatmap-sources { flex-wrap: wrap; max-width: 100%; margin-bottom: var(--space-3); }
.heatmap-legend i { width: 10px; height: 10px; border-radius: 2px; }
.insights-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 14px; }
.insights-panel--wide { grid-column: 1 / -1; }
.insights-panel__heading { display: flex; align-items: baseline; gap: var(--space-3); margin-bottom: 14px; }
.insights-panel__heading h3 { font-size: var(--fs-body); font-weight: var(--fw-semibold); }
.insights-panel__heading span { font-size: var(--fs-secondary); }
.insights-play-events { font-size: var(--fs-secondary); }
.watch-heatmap { display: grid; grid-template-columns: repeat(53, minmax(5px, 1fr)); grid-auto-flow: column; grid-template-rows: repeat(7, 8px); gap: 3px; }
.watch-heatmap span, .heatmap-legend .heat-0 { border-radius: 2px; background: var(--neutral-faint); }
.watch-heatmap .heat-1, .heatmap-legend .heat-1 { background: color-mix(in srgb, var(--accent-color) 30%, var(--neutral-faint)); }
.watch-heatmap .heat-2, .heatmap-legend .heat-2 { background: color-mix(in srgb, var(--accent-color) 50%, var(--neutral-faint)); }
.watch-heatmap .heat-3, .heatmap-legend .heat-3 { background: color-mix(in srgb, var(--accent-color) 72%, var(--neutral-faint)); }
.watch-heatmap .heat-4, .heatmap-legend .heat-4 { background: var(--accent-color); }
.rating-bars { display: flex; align-items: flex-end; height: 120px; gap: var(--space-1); }
.rating-bar { flex: 1; display: grid; grid-template-rows: 1fr auto; align-items: end; min-width: 0; text-align: center; }
.rating-bar i { display: block; width: 100%; border-radius: 3px 3px 0 0; background: var(--accent-color); opacity: 0.9; }
.rating-bar small { color: var(--text-muted); font-size: var(--fs-caption); font-family: var(--font-mono); }
@media (max-width: 900px) { .insights-summary, .insights-grid { grid-template-columns: 1fr 1fr; } }
@media (max-width: 620px) { .insights-summary, .insights-grid { grid-template-columns: 1fr; } .insights-panel--wide { grid-column: auto; } }
</style>
