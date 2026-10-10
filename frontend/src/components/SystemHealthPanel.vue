<template>
  <section class="system-health" data-test="system-health">
    <div class="system-health__toolbar">
      <p>检查各项能力的当前状态；不会自动安装、扫描或修复。</p>
      <button type="button" class="btn-secondary btn-compact" :disabled="loading" data-test="health-refresh" @click="refresh">刷新状态</button>
      <button type="button" class="btn-secondary btn-compact" :disabled="exporting" data-test="health-export" @click="exportReport">{{ exporting ? '正在导出…' : '导出诊断' }}</button>
    </div>
    <p class="system-health__privacy">诊断只保存到你选择的位置，包含状态与计数，不含路径、媒体内容、凭证或原始日志。</p>
    <p v-if="exportMessage" role="status" data-test="health-export-message">{{ exportMessage }}</p>
    <p v-if="exportError" class="settings-error" role="alert" data-test="health-export-error">{{ exportError }}</p>
    <section v-for="section in sections" :key="section.key" class="system-health__section" :data-test="`health-section-${section.key}`">
      <header><h3>{{ section.label }}</h3><span v-if="states[section.key]?.checked_at">读取于 {{ formatTime(states[section.key].checked_at) }}</span></header>
      <p v-if="pending[section.key]" role="status">正在读取…</p>
      <p v-if="errors[section.key]" class="settings-error" role="alert" :data-test="`health-error-${section.key}`">{{ errors[section.key] }}</p>
      <div v-for="item in states[section.key]?.items || []" :key="item.key" class="system-health__item" :data-test="`health-item-${item.key}`">
        <div class="system-health__item-head">
          <strong>{{ item.label }}</strong>
          <span :class="['system-health__state', `system-health__state--${item.state}`]">{{ stateLabel(item.state) }}</span>
          <button v-if="actionsEnabled && item.action_id" type="button" class="btn-secondary btn-compact" :data-test="`health-action-${item.key}`" @click="$emit('run-command', item.action_id)">查看 / 处理</button>
        </div>
        <p>{{ item.detail }}</p>
        <p v-if="item.local_detail" class="system-health__local" :title="item.local_detail">{{ item.local_detail }}</p>
        <dl v-if="item.metrics?.length" class="system-health__metrics">
          <div v-for="metric in item.metrics" :key="metric.key"><dt>{{ metric.label }}</dt><dd>{{ formatMetric(metric) }}</dd></div>
        </dl>
      </div>
    </section>
  </section>
</template>

<script>
import { ExportSystemHealthReport, GetSystemHealthSection } from '../../wailsjs/go/main/App';
import { formatBytes } from '../utils/mediaDetails.js';

const SECTIONS = [
  { key: 'database', label: '数据库与备份' },
  { key: 'storage', label: '扫描目录与磁盘' },
  { key: 'runtimes', label: '依赖与模型' },
  { key: 'indexes', label: '索引状态' },
  { key: 'tasks', label: '后台任务' },
  { key: 'caches', label: '缓存占用' }
];
const STATE_LABELS = { ok: '正常', warning: '请关注', unavailable: '不可用', disabled: '未启用', unknown: '未知' };

export default {
  name: 'SystemHealthPanel',
  props: { actionsEnabled: { type: Boolean, default: true } },
  emits: ['run-command'],
  data() {
    return { sections: SECTIONS, states: {}, pending: {}, errors: {}, exporting: false, exportMessage: '', exportError: '' };
  },
  computed: {
    loading() { return Object.values(this.pending).some(Boolean); }
  },
  mounted() { this.refresh(); },
  beforeUnmount() { this._requestGeneration = null; },
  methods: {
    stateLabel(state) { return STATE_LABELS[state] || STATE_LABELS.unknown; },
    formatTime(value) { return new Date(value).toLocaleTimeString(); },
    formatMetric(metric) {
      return metric.unit === 'bytes' ? formatBytes(metric.value) : `${metric.value} ${metric.unit || ''}`.trim();
    },
    async refresh() {
      const generation = Symbol('health-refresh');
      this._requestGeneration = generation;
      this.pending = Object.fromEntries(SECTIONS.map(section => [section.key, true]));
      this.errors = {};
      await Promise.all(SECTIONS.map(async section => {
        try {
          const snapshot = await GetSystemHealthSection(section.key);
          if (this._requestGeneration !== generation) return;
          this.states = { ...this.states, [section.key]: snapshot };
        } catch (_) {
          if (this._requestGeneration !== generation) return;
          this.errors = { ...this.errors, [section.key]: `读取${section.label}失败，请重试；已有结果保留。` };
        } finally {
          if (this._requestGeneration === generation) this.pending = { ...this.pending, [section.key]: false };
        }
      }));
    },
    async exportReport() {
      if (this.exporting) return;
      this.exporting = true;
      this.exportError = '';
      this.exportMessage = '';
      try {
        const result = await ExportSystemHealthReport();
        this.exportMessage = result?.message || (result?.saved ? '诊断报告已保存' : '已取消导出');
      } catch (_) {
        this.exportError = '导出失败，请检查文件是否已存在、目录权限和磁盘空间。';
      } finally {
        this.exporting = false;
      }
    }
  }
};
</script>

<style scoped>
.system-health { display: flex; flex-direction: column; gap: 16px; }
.system-health__toolbar { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.system-health__toolbar p { flex: 1 1 100%; margin: 0; }
.system-health__privacy, .system-health__local { color: var(--text-secondary); font-size: 12px; overflow-wrap: anywhere; margin: 0; }
.system-health__section { border-top: 1px solid var(--border-color); padding-top: 12px; }
.system-health__section header, .system-health__item-head { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
.system-health__section h3 { margin: 0; flex: 1; }
.system-health__section header > span { font-size: 12px; color: var(--text-secondary); }
.system-health__item { padding: 12px 0; }
.system-health__item-head strong { flex: 1; }
.system-health__item p { margin: 6px 0 0; }
.system-health__state { font-size: 12px; color: var(--text-secondary); }
.system-health__state--warning, .system-health__state--unavailable { color: var(--danger-color, #b34232); }
.system-health__metrics { display: flex; flex-wrap: wrap; gap: 8px 20px; margin: 8px 0 0; font-size: 12px; }
.system-health__metrics > div { display: flex; gap: 6px; }
.system-health__metrics dt { color: var(--text-secondary); }
.system-health__metrics dd { margin: 0; }
</style>
