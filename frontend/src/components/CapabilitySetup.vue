<template>
  <DialogShell title="按目标准备" size="lg" data-test="capability-setup" @close="$emit('close')">
    <p>选择你想做的事。这里读取当前状态；扫描、下载模型和准备语言都需要你明确操作。</p>
    <div class="capability-setup__goals" aria-label="使用目标">
      <button v-for="item in goals" :key="item.key" type="button" class="btn btn--secondary" :aria-pressed="goal === item.key" @click="goal = item.key">{{ item.label }}</button>
    </div>
    <p v-if="error" role="alert">{{ error }}</p>
    <ul class="capability-setup__steps" :aria-busy="loading">
      <li v-for="step in steps" :key="step.key" :data-test="`setup-step-${step.key}`">
        <div><strong>{{ step.label }}</strong><span>{{ labels[step.state] }}</span></div>
        <p>{{ step.detail }}</p>
        <small v-if="step.checkedAt">检查于 {{ formatTime(step.checkedAt) }}</small>
        <p v-if="step.metrics?.length">{{ step.metrics.map(metric => `${metric.label}：${metric.value}${metric.unit || ''}`).join(' · ') }}</p>
        <button type="button" class="btn btn--secondary btn--sm" :disabled="busy || !actionAvailable(step.command)" @click="run(step.command)">{{ step.actionLabel }}</button>
      </li>
    </ul>
    <template #footer>
      <button type="button" class="btn btn--ghost" :disabled="loading || busy" @click="check">{{ loading ? '检查中…' : '检查当前状态' }}</button>
      <button type="button" class="btn btn--secondary" @click="$emit('close')">稍后再说</button>
    </template>
  </DialogShell>
</template>
<script>
import { CountLibraryVideos, GetPendingWorkSummary, GetSceneRuntimeStatus, GetSubtitleEngineStatuses, GetSystemHealthSection } from '../../wailsjs/go/main/App';
import DialogShell from './ui/DialogShell.vue';
import { findCommand, isCommandEnabled } from '../utils/commandRegistry.js';
import { SETUP_GOALS, SETUP_STATE_LABELS, capabilityGoalSteps } from '../utils/capabilitySetup.js';
export default {
  name: 'CapabilitySetup', components: { DialogShell }, emits: ['close'],
  data: () => ({ goals: SETUP_GOALS, labels: SETUP_STATE_LABELS, goal: 'watch', snapshots: {}, facts: {}, loading: false, busy: false, error: '', request: 0 }),
  computed: { steps() { return capabilityGoalSteps(this.goal, this.snapshots, this.facts); } },
  mounted() {
    this.refresh();
    this._off = ['background-tasks', 'scene-runtime-state', 'subtitle-engine-status', 'library-scan-summary'].map(event => window.runtime?.EventsOn?.(event, () => this.refresh()));
  },
  beforeUnmount() { this._disposed = true; ++this.request; for (const off of this._off || []) off?.(); },
  methods: {
    formatTime(value) { return new Date(value).toLocaleString(); },
    actionAvailable(id) { const command = findCommand(id); return command && isCommandEnabled(command); },
    async run(id) {
      const command = findCommand(id); if (!command || !isCommandEnabled(command) || this.busy) return;
      this.busy = true; this.error = '';
      try { if (await command.run() !== false) this.$emit('close'); }
      catch { this.error = '操作没有完成，请查看对应设置或任务中心'; }
      finally { this.busy = false; }
    },
    async check() {
      if (this.loading || this.busy) return;
      this.loading = true; this.busy = true; this.error = '';
      try {
        if (this.goal === 'subtitles') await GetSubtitleEngineStatuses();
        else if (this.goal === 'scenes') await GetSceneRuntimeStatus();
      } catch { this.error = '检查未完成，可打开对应设置查看原因'; }
      finally { this.busy = false; if (!this._disposed) await this.refresh(); }
    },
    async refresh() {
      const request = ++this.request; this.loading = true;
      const sources = ['storage', 'runtimes', 'indexes'];
      const results = await Promise.allSettled([...sources.map(key => GetSystemHealthSection(key)), CountLibraryVideos({}), GetPendingWorkSummary()]);
      if (this._disposed || request !== this.request) return;
      const snapshots = {};
      sources.forEach((key, i) => { if (results[i].status === 'fulfilled') snapshots[key] = results[i].value; });
      this.snapshots = snapshots;
      this.facts = { count: results[3].status === 'fulfilled' ? results[3].value : undefined, pending: results[4].status === 'fulfilled' ? results[4].value.total : undefined };
      if (results.some(result => result.status === 'rejected')) this.error = '部分状态未能读取，其它可用操作不受影响';
      this.loading = false;
    }
  }
};
</script>
<style scoped>
.capability-setup__goals { display: flex; flex-wrap: wrap; gap: var(--space-2); }
.capability-setup__goals [aria-pressed=true] { background: var(--accent-soft); color: var(--accent-color); border-color: var(--accent-color); }
.capability-setup__steps { list-style: none; padding: 0; display: grid; grid-template-columns: repeat(auto-fit, minmax(240px, 1fr)); gap: var(--space-3); }
.capability-setup__steps li { padding: var(--space-4); border: 1px solid var(--hairline); border-radius: var(--radius); }
.capability-setup__steps li > div { display: flex; justify-content: space-between; gap: var(--space-2); }
.capability-setup__steps span, .capability-setup__steps small { font-size: var(--fs-caption); color: var(--text-muted); }
.capability-setup__steps p { font-size: var(--fs-body); color: var(--text-secondary); line-height: 1.5; }
</style>
