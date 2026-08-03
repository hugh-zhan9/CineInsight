<template>
  <section class="ai-quality-panel" aria-label="AI 质量评估">
    <div class="ai-quality-filters">
      <label>
        时间范围
        <select v-model="filter.window" data-test="quality-window">
          <option value="7d">最近 7 天</option>
          <option value="30d">最近 30 天</option>
          <option value="all">全部</option>
        </select>
      </label>
      <label>
        标签
        <select v-model.number="filter.tag_id" data-test="quality-tag">
          <option :value="0">全部标签</option>
          <option v-for="tag in tags" :key="tag.id" :value="Number(tag.id)">{{ tag.name }}</option>
        </select>
      </label>
      <label>
        置信度
        <select v-model="filter.confidence" data-test="quality-confidence">
          <option value="">全部</option>
          <option value="high">高</option>
          <option value="medium">中</option>
          <option value="low">低</option>
        </select>
      </label>
      <label>
        模型
        <input v-model.trim="filter.model_identifier" type="text" placeholder="全部模型" data-test="quality-model" />
      </label>
      <label>
        标签提示版本
        <input v-model.trim="filter.prompt_schema_version" type="text" placeholder="全部版本" />
      </label>
      <label>
        同源提示版本
        <input v-model.trim="filter.comparison_prompt_version" type="text" placeholder="全部版本" />
      </label>
      <label>
        同源检测版本
        <input v-model.trim="filter.detection_version" type="text" placeholder="全部版本" />
      </label>
      <button type="button" class="btn-secondary" data-test="quality-apply" :disabled="loading" @click="loadReport">应用筛选</button>
    </div>

    <div v-if="loading" class="ai-quality-state">正在汇总本地质量记录...</div>
    <div v-else-if="error" class="ai-quality-state ai-quality-error">
      <span>{{ error }}</span>
      <button type="button" class="btn-secondary btn-compact" @click="loadReport">重试</button>
    </div>
    <template v-else-if="report">
      <div class="ai-quality-cards">
        <article>
          <span>标签接受率</span>
          <strong>{{ formatRate(report.tag_summary?.approval_rate) }}</strong>
          <small>{{ report.tag_summary?.approved || 0 }} 批准 / {{ report.tag_summary?.decided || 0 }} 已决</small>
        </article>
        <article>
          <span>同源否认率</span>
          <strong>{{ formatRate(report.same_source_summary?.rejection_rate) }}</strong>
          <small>{{ report.same_source_summary?.rejected || 0 }} 否认 / {{ report.same_source_summary?.decided || 0 }} 已决</small>
        </article>
        <article>
          <span>任务失败率</span>
          <strong>{{ formatRate(report.run_summary?.failure_rate) }}</strong>
          <small>{{ report.run_summary?.failed || 0 }} 失败 / {{ finishedRunCount }} 已结束</small>
        </article>
        <article>
          <span>任务耗时 P50 / P95</span>
          <strong>{{ formatDuration(report.run_summary?.duration_p50_ms) }} / {{ formatDuration(report.run_summary?.duration_p95_ms) }}</strong>
          <small>平均请求 {{ formatNumber(report.run_summary?.average_requests) }}，工具调用 {{ formatNumber(report.run_summary?.average_tool_calls) }}</small>
        </article>
      </div>

      <div v-if="isEmpty" class="ai-quality-state" data-test="quality-empty">当前筛选范围内没有已决样本或任务记录。</div>
      <template v-else>
        <section v-if="report.tag_groups?.length" class="ai-quality-table-section">
          <h4>标签质量</h4>
          <div class="ai-quality-table-wrap">
            <table>
              <thead><tr><th>标签</th><th>置信度</th><th>模型 / 提示版本</th><th>样本</th><th>接受率</th></tr></thead>
              <tbody>
                <tr v-for="(group, index) in report.tag_groups" :key="`${group.tag_id}-${group.confidence}-${group.model_identifier}-${group.prompt_schema_version}-${index}`">
                  <td>{{ group.tag_name || '未命名标签' }}</td>
                  <td>{{ confidenceLabel(group.confidence) }}</td>
                  <td>{{ dimensionLabel(group.model_identifier) }} / {{ dimensionLabel(group.prompt_schema_version) }}</td>
                  <td>{{ group.decided }}</td>
                  <td>{{ formatRate(group.approval_rate) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>

        <section v-if="report.same_source_groups?.length" class="ai-quality-table-section">
          <h4>同源判断质量</h4>
          <div class="ai-quality-table-wrap">
            <table>
              <thead><tr><th>模型 / 比较提示</th><th>检测版本</th><th>样本</th><th>否认率</th></tr></thead>
              <tbody>
                <tr v-for="(group, index) in report.same_source_groups" :key="`${group.model_identifier}-${group.comparison_prompt_version}-${group.detection_version}-${index}`">
                  <td>{{ dimensionLabel(group.model_identifier) }} / {{ dimensionLabel(group.comparison_prompt_version) }}</td>
                  <td>{{ dimensionLabel(group.detection_version) }}</td>
                  <td>{{ group.decided }}</td>
                  <td>{{ formatRate(group.rejection_rate) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>

        <section v-if="report.run_groups?.length" class="ai-quality-table-section">
          <h4>任务运行质量</h4>
          <div class="ai-quality-table-wrap">
            <table>
              <thead><tr><th>模型 / 提示版本</th><th>任务</th><th>失败</th><th>P50 / P95</th></tr></thead>
              <tbody>
                <tr v-for="group in report.run_groups" :key="`${group.model_identifier}-${group.prompt_schema_version}`">
                  <td>{{ dimensionLabel(group.model_identifier) }} / {{ dimensionLabel(group.prompt_schema_version) }}</td>
                  <td>{{ group.total }}</td>
                  <td>{{ group.failed }}（{{ formatRate(group.failure_rate) }}）</td>
                  <td>{{ formatDuration(group.duration_p50_ms) }} / {{ formatDuration(group.duration_p95_ms) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>
      </template>
      <p class="ai-quality-note">只读取本地归因与审核记录；打开或刷新本页不会调用 AI，也不会读取视频、字幕或帧。</p>
    </template>
  </section>
</template>

<script>
import { GetAIQualityReport } from '../../wailsjs/go/main/App';

export default {
  name: 'AIQualityPanel',
  props: {
    tags: { type: Array, default: () => [] },
  },
  data() {
    return {
      filter: {
        window: '30d', tag_id: 0, confidence: '', model_identifier: '',
        prompt_schema_version: '', comparison_prompt_version: '', detection_version: '',
      },
      loading: false,
      error: '',
      report: null,
    };
  },
  computed: {
    isEmpty() {
      return Number(this.report?.tag_summary?.decided || 0) === 0
        && Number(this.report?.same_source_summary?.decided || 0) === 0
        && Number(this.report?.run_summary?.total || 0) === 0;
    },
    finishedRunCount() {
      const summary = this.report?.run_summary || {};
      return Number(summary.completed || 0) + Number(summary.skipped || 0) + Number(summary.failed || 0);
    },
  },
  mounted() {
    this.loadReport();
  },
  methods: {
    async loadReport() {
      this.loading = true;
      this.error = '';
      try {
        this.report = await GetAIQualityReport({ ...this.filter });
      } catch (err) {
        this.error = '加载 AI 质量报告失败: ' + String(err);
      } finally {
        this.loading = false;
      }
    },
    formatRate(value) {
      if (value === null || value === undefined) return '—';
      return `${(Number(value) * 100).toFixed(1)}%`;
    },
    formatDuration(value) {
      if (value === null || value === undefined) return '—';
      const milliseconds = Number(value);
      return milliseconds >= 1000 ? `${(milliseconds / 1000).toFixed(1)}s` : `${Math.round(milliseconds)}ms`;
    },
    formatNumber(value) {
      if (value === null || value === undefined) return '—';
      return Number(value).toFixed(1);
    },
    confidenceLabel(value) {
      return ({ high: '高', medium: '中', low: '低' })[value] || value || '未知';
    },
    dimensionLabel(value) {
      return value === 'historical_unknown' || !value ? '历史未知' : value;
    },
  },
};
</script>

<style scoped>
.ai-quality-panel { min-height: 280px; overflow-y: auto; padding-top: 12px; }
.ai-quality-filters { display: grid; grid-template-columns: repeat(4, minmax(130px, 1fr)); gap: 10px; align-items: end; }
.ai-quality-filters label { display: grid; gap: 5px; color: var(--text-muted); font-size: 12px; }
.ai-quality-filters select, .ai-quality-filters input { min-width: 0; height: 34px; padding: 0 8px; border: 1px solid var(--border-color); border-radius: 6px; background: var(--panel-bg); color: var(--text-primary); }
.ai-quality-cards { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 10px; margin: 16px 0; }
.ai-quality-cards article { display: grid; gap: 5px; padding: 12px; border: 1px solid var(--border-color); border-radius: var(--radius-md); background: color-mix(in srgb, var(--accent-color) 5%, var(--panel-bg)); }
.ai-quality-cards span, .ai-quality-cards small { color: var(--text-muted); }
.ai-quality-cards strong { color: var(--text-primary); font-size: 19px; }
.ai-quality-state { display: flex; align-items: center; justify-content: center; gap: 10px; padding: 36px 0; color: var(--text-muted); }
.ai-quality-error { color: var(--danger-color); }
.ai-quality-table-section { margin-top: 18px; }
.ai-quality-table-section h4 { margin: 0 0 8px; }
.ai-quality-table-wrap { overflow-x: auto; }
table { width: 100%; border-collapse: collapse; font-size: 12px; }
th, td { padding: 8px; border-bottom: 1px solid var(--border-color); text-align: left; vertical-align: top; }
th { color: var(--text-muted); font-weight: 600; }
td { color: var(--text-primary); }
.ai-quality-note { margin: 18px 0 0; color: var(--text-muted); font-size: 12px; }
@media (max-width: 760px) {
  .ai-quality-filters, .ai-quality-cards { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
</style>
