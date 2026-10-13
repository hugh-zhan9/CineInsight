<template>
  <!-- 孤儿文件（D-LD-05）：数据目录里所属记录已彻底不存在的缓存与副本，按位置统计、勾选回收。 -->
  <section class="lost-section" data-test="lost-orphans">
    <header class="lost-section__head">
      <h3>孤儿文件</h3>
      <small>所属记录已经彻底不存在的缩略图、海报副本、字幕备份等；回收站里的记录不算。媒体目录里的字幕文件不在范围内</small>
      <span class="lost-spacer"></span>
      <button type="button" class="btn btn--secondary btn--sm" :disabled="loading || reclaiming" data-test="lost-orphans-refresh" @click="reload">刷新</button>
    </header>
    <AppBanner v-if="error" tone="danger" :title="error" data-test="lost-orphans-error" />
    <div class="lost-panel">
      <p v-if="loading && !report" class="lost-muted">正在统计…</p>
      <template v-else-if="report">
        <p v-if="report.total_files === 0" class="lost-muted" data-test="lost-orphans-empty">没有孤儿文件。</p>
        <ul v-else class="lost-orphans">
          <li v-for="location in report.locations" :key="location.key" data-test="lost-orphan-row">
            <label class="lost-check">
              <input
                type="checkbox"
                :checked="picked.has(location.key)"
                :disabled="location.files === 0 || reclaiming"
                :data-test="`lost-orphan-toggle-${location.key}`"
                @change="toggle(location.key)"
              />
              {{ location.label }}
            </label>
            <small>{{ location.files }} 个</small>
            <strong>{{ formatBytes(location.bytes) }}</strong>
          </li>
        </ul>
        <div v-if="report.total_files" class="lost-orphans__bar">
          <span class="lost-muted">共 {{ report.total_files }} 个 · {{ formatBytes(report.total_bytes) }}</span>
          <span class="lost-spacer"></span>
          <button type="button" class="btn btn--danger btn--sm" :disabled="picked.size === 0 || reclaiming" data-test="lost-orphans-reclaim" @click="reclaim">
            {{ reclaiming ? '回收中…' : `回收所选（${picked.size}）` }}
          </button>
        </div>
        <p v-if="result" class="lost-muted" data-test="lost-orphans-result">
          已回收 {{ result.files }} 个文件，释放 {{ formatBytes(result.bytes) }}<span v-if="result.failed">；{{ result.failed }} 个删不掉，已跳过</span>。
        </p>
      </template>
    </div>
  </section>
</template>

<script>
import { GetOrphanFileReport, ReclaimOrphanFiles } from '../../../wailsjs/go/main/App';
import { formatBytes } from '../../utils/mediaDetails.js';
import { lostMediaErrorText } from '../../utils/lostMedia.js';
import AppBanner from '../ui/AppBanner.vue';

export default {
  name: 'OrphanFilesSection',
  components: { AppBanner },
  data() {
    return { report: null, loading: false, reclaiming: false, picked: new Set(), result: null, error: '' };
  },
  mounted() {
    this.reload();
  },
  methods: {
    formatBytes,
    async reload() {
      this.loading = true;
      this.error = '';
      try {
        this.report = await GetOrphanFileReport();
        const available = new Set((this.report?.locations || []).filter(item => item.files > 0).map(item => item.key));
        this.picked = new Set([...this.picked].filter(key => available.has(key)));
      } catch (err) {
        this.error = lostMediaErrorText(err, '统计孤儿文件失败，请稍后重试。');
      } finally {
        this.loading = false;
      }
    },
    toggle(key) {
      const next = new Set(this.picked);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      this.picked = next;
    },
    async reclaim() {
      this.reclaiming = true;
      this.error = '';
      try {
        this.result = await ReclaimOrphanFiles([...this.picked]);
        this.picked = new Set();
      } catch (err) {
        this.error = lostMediaErrorText(err, '回收没有完成，请稍后重试。');
      } finally {
        this.reclaiming = false;
      }
      await this.reload();
    }
  }
};
</script>

<style scoped>
.lost-section { display: grid; gap: var(--space-3); }
.lost-section__head { display: flex; flex-wrap: wrap; align-items: baseline; gap: var(--space-1) var(--space-3); }
.lost-section__head h3 { margin: 0; font-size: var(--fs-section); font-weight: var(--fw-bold); }
.lost-section__head small { color: var(--text-muted); font-size: var(--fs-caption); }
.lost-spacer { flex: 1; }
.lost-panel { display: grid; gap: var(--space-3); padding: 14px var(--space-4); border: 1px solid var(--hairline); border-radius: var(--radius-lg); background: var(--panel-bg); }
.lost-muted { margin: 0; color: var(--text-muted); font-size: var(--fs-secondary); }
.lost-orphans { display: grid; gap: var(--space-2); margin: 0; padding: 0; list-style: none; }
.lost-orphans li { display: grid; grid-template-columns: minmax(0, 1fr) 72px 90px; align-items: center; gap: var(--space-3); font-size: var(--fs-secondary); }
.lost-orphans small { color: var(--text-muted); text-align: right; font-variant-numeric: tabular-nums; }
.lost-orphans strong { text-align: right; font-variant-numeric: tabular-nums; }
.lost-orphans__bar { display: flex; align-items: center; gap: var(--space-3); }
.lost-check { display: inline-flex; align-items: center; gap: 6px; cursor: pointer; }
</style>
