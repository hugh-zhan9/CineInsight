<template>
  <BaseModal v-if="visible" class="consolidation-modal" @close="close">
    <header><h3>集中整理保留视频</h3><button class="btn-secondary" :disabled="starting" @click="close">{{ summary?.status === 'running' ? '后台继续' : '关闭' }}</button></header>
    <p>移动本次纳入整理的保留版本及可归属附件，不会删除重复副本。完成后可以单独审阅清理。</p>
    <p v-if="error" role="alert" data-test="consolidation-error">{{ error }}</p>
    <template v-if="!taskID">
      <div class="destination"><span data-test="consolidation-destination">{{ destination || '尚未选择目标目录' }}</span>
        <button class="btn-secondary" data-test="consolidation-recommend" :disabled="starting" @click="previewDestination('')">使用推荐目录</button>
        <button class="btn-secondary" data-test="consolidation-choose" :disabled="starting" @click="chooseDestination">选择目录</button>
        <button v-if="destination && !preview" class="btn-secondary" :disabled="starting || loading" @click="previewDestination(destination)">重新预览</button>
      </div>
      <p v-if="loading">正在核对保留视频、附件与目标目录…</p>
    </template>
    <template v-if="summary">
      <p data-test="consolidation-progress">{{ statusLabel }} · 已完成 {{ summary.completed || 0 }} / {{ summary.total || 0 }} 个 · {{ bytes(summary.bytes_done) }} / {{ bytes(summary.bytes_total) }}</p>
      <p v-if="summary.error" role="alert">{{ summary.error }}</p>
      <p v-if="summary.status !== 'running' && summary.status !== 'completed'">已成功的项目保留在目标位置，其余项目请查看下方结果。本次不会继续移动或自动清理。</p>
      <button v-if="canCancel && summary.status === 'running'" class="btn-secondary" data-test="consolidation-cancel" :disabled="cancelling" @click="cancel">{{ cancelling ? '正在请求取消…' : '取消整理' }}</button>
      <button v-if="!detail && !loading" class="btn-secondary" @click="loadTask(taskID)">重新读取任务</button>
    </template>
    <template v-if="displayPreview">
      <p data-test="consolidation-totals">目标：{{ displayPreview.destination }} · 保留 {{ previewCounts.kept }} 个视频，实际移动 {{ previewCounts.moved }} 个 · 后续待清理意向 {{ previewCounts.selected }} 个（本步不执行） · 移动 {{ bytes(displayPreview.move_bytes) }} · 跨磁盘复制 {{ bytes(displayPreview.cross_volume_bytes) }} · 预计复制 {{ bytes(displayPreview.copy_bytes) }} · 可用 {{ bytes(displayPreview.available_bytes) }}</p>
      <p v-for="(message, index) in displayPreview.errors || []" :key="`error-${index}`" role="alert">{{ message }}</p>
      <details v-if="displayPreview.warnings?.length"><summary>{{ displayPreview.warnings.length }} 条提示 / 未处理附件</summary><p v-for="(message, index) in pagedWarnings" :key="index">{{ message }}</p><button v-if="warningLimit < displayPreview.warnings.length" class="btn-secondary" @click="warningLimit += 40">显示更多提示</button></details>
      <p>以下是最终确认的保留视频与落点；精确重复可能改为保留目标中已有的等价副本。同名改名以此处路径为准。</p>
      <ol class="consolidation-items" :start="page * pageSize + 1">
        <li v-for="item in pagedItems" :key="item.video_id" data-test="consolidation-item">
          <strong>保留 {{ fileName(item.source_path) }} · {{ item.stay ? '保持原位' : '集中到目标' }}</strong>
          <div>{{ item.source_path }} → {{ item.destination_path }}</div>
          <ul><li v-for="file in item.files || []" :key="file.source.path" data-test="consolidation-file">{{ fileLabel(file.kind) }}：{{ file.source.path }} → {{ file.destination }} <b v-if="file.copy_only">复制，共享来源保留</b><span v-if="file.cross_volume"> · 跨磁盘</span></li></ul>
          <p v-for="warning in item.warnings || []" :key="warning">{{ warning }}</p>
          <template v-if="resultByID.get(item.video_id)"><p>{{ phaseLabel(resultByID.get(item.video_id).phase) }} {{ resultByID.get(item.video_id).error }}</p><p v-for="path in resultByID.get(item.video_id).retained_paths || []" :key="path">保留文件：{{ path }}</p></template>
        </li>
      </ol>
      <nav v-if="pageCount > 1" aria-label="集中整理清单分页"><button class="btn-secondary" :disabled="page === 0" @click="page--">上一页</button><span>{{ page + 1 }} / {{ pageCount }}</span><button class="btn-secondary" :disabled="page + 1 >= pageCount" @click="page++">下一页</button></nav>
      <label v-if="!taskID && !displayPreview.in_scan_roots"><input v-model="addToScanRoots" type="checkbox" data-test="consolidation-add-root" :disabled="starting" />将目标加入扫描目录</label>
      <footer v-if="!taskID"><button class="btn-primary" data-test="consolidation-start" :disabled="!canStart" @click="start">{{ starting ? '正在提交…' : '确认移动保留视频' }}</button></footer>
    </template>
    <footer v-if="summary?.status === 'completed'"><button class="btn-secondary" @click="close">只集中</button><button class="btn-primary" data-test="consolidation-review" :disabled="loading || !detail" @click="review">继续审阅清理</button></footer>
  </BaseModal>
</template>

<script>
import { markRaw } from 'vue';
import { PreviewCleanupConsolidation, StartCleanupConsolidation, GetCleanupConsolidationStatus, CancelCleanupConsolidation, SelectMigrationDestinationDirectory, GetBackgroundTasks } from '../../../wailsjs/go/main/App';
import BaseModal from '../ui/BaseModal.vue';
import { runtimeEventsMixin } from './runtimeEvents.js';
import { CONSOLIDATION_PAGE_SIZE, CONSOLIDATION_STATUS, isConsolidationTerminal } from '../../utils/cleanupConsolidation.js';

export default {
  name: 'CleanupConsolidationDialog', components: { BaseModal }, mixins: [runtimeEventsMixin],
  props: { request: { type: Object, default: null } }, emits: ['review', 'moved', 'started'],
  data: () => ({ visible: false, generation: 0, taskID: 0, destination: '', preview: null, detail: null, summary: null, loading: false, starting: false, cancelling: false, error: '', addToScanRoots: false, localRunning: false, page: 0, pageSize: CONSOLIDATION_PAGE_SIZE, warningLimit: 40, terminalVersion: 0 }),
  computed: {
    displayPreview() { return this.detail?.preview || this.preview; },
    previewCounts() {
      const preview = this.displayPreview;
      return { kept: preview?.items?.length || 0, moved: (preview?.items || []).filter(item => !item.stay).length, selected: new Set((preview?.groups || []).flatMap(group => group.selected_ids || [])).size };
    },
    pagedItems() { return (this.displayPreview?.items || []).slice(this.page * this.pageSize, (this.page + 1) * this.pageSize); },
    pagedWarnings() { return (this.displayPreview?.warnings || []).slice(0, this.warningLimit); },
    pageCount() { return Math.max(1, Math.ceil((this.displayPreview?.items?.length || 0) / this.pageSize)); },
    resultByID() { return new Map((this.detail?.items || []).map(item => [item.video_id, item])); },
    statusLabel() { return CONSOLIDATION_STATUS[this.summary?.status] || '状态未知'; },
    canCancel() { return this.localRunning && this.taskID === Number(this.summary?.id); },
    canStart() { return Boolean(this.preview?.preview_id && !this.preview.errors?.length && !this.loading && !this.starting && this.request?.groups?.length); }
  },
  watch: {
    request() {
      if (!this.visible || this.taskID || this.starting) return;
      this.generation++; this.preview = null; this.loading = false;
      this.error = '整理范围、保留项或勾选已变化，请重新预览。';
    }
  },
  mounted() {
    this.registerRuntimeEvent('cleanup-consolidation-progress', summary => this.onProgress(summary));
    this.registerRuntimeEvent('background-tasks', keys => { this.localRunning = (keys || []).includes('cleanup_consolidation'); });
  },
  beforeUnmount() { this.generation++; },
  methods: {
    fileName(path) { return String(path || '').split(/[\\/]/).pop() || '视频'; },
    fileLabel(kind) { return { video: '视频', subtitle: '字幕', nfo: '本地资料' }[kind] || '附件'; },
    phaseLabel(phase) { return { planned: '尚未开始', staging: '正在复制 / 准备文件', verified: '文件校验完成', publishing: '正在安放目标文件', committed: '路径已更新', cleaned: '整理完成', rolled_back: '未完成，已撤回临时文件' }[phase] || '请查看文件位置'; },
    bytes(value) { const n = Number(value || 0); if (!n) return '0 B'; const unit = Math.min(4, Math.floor(Math.log(n) / Math.log(1024))); return `${(n / 1024 ** unit).toFixed(unit ? 1 : 0)} ${['B', 'KB', 'MB', 'GB', 'TB'][unit]}`; },
    close() { if (this.starting) return; this.visible = false; this.generation++; this.loading = false; },
    async open(taskID = 0) {
      if (this.starting) return;
      this.generation++; this.visible = true; this.taskID = Number(taskID); this.preview = null; this.detail = null; this.summary = null; this.error = ''; this.page = 0; this.warningLimit = 40; this.terminalVersion = 0; this.cancelling = false; this.localRunning = false;
      const generation = this.generation;
      GetBackgroundTasks().then(keys => { if (generation === this.generation) this.localRunning = (keys || []).includes('cleanup_consolidation'); }).catch(() => {});
      if (this.taskID) return this.loadTask(this.taskID);
      this.destination = ''; this.addToScanRoots = false;
      return this.previewDestination('');
    },
    async chooseDestination() {
      const generation = ++this.generation; this.preview = null; this.loading = false;
      try { const path = await SelectMigrationDestinationDirectory(); if (generation !== this.generation || !this.visible) return; if (path) await this.previewDestination(path); }
      catch (err) { if (generation === this.generation) this.error = String(err?.message || err); }
    },
    async previewDestination(destination) {
      if (this.starting || this.taskID) return;
      const generation = ++this.generation;
      this.preview = null; this.destination = destination; this.page = 0; this.error = ''; this.addToScanRoots = false;
      if (!this.request?.groups?.length) { this.error = '请先选择需要纳入集中整理的组。'; return; }
      this.loading = true;
      try {
        const result = await PreviewCleanupConsolidation({ ...this.request, destination });
        if (generation !== this.generation || !this.visible) return;
        this.preview = result ? markRaw(result) : null; this.destination = result?.destination || destination;
        if (!result) this.error = '没有可用的预览，请重新分析后再试。';
      } catch (err) { if (generation === this.generation) this.error = String(err?.message || err); }
      finally { if (generation === this.generation) this.loading = false; }
    },
    async start() {
      if (!this.canStart) return;
      const generation = this.generation; const previewID = this.preview.preview_id;
      this.starting = true; this.error = '';
      try {
        const result = await StartCleanupConsolidation(previewID, this.addToScanRoots);
        if (generation !== this.generation || !result) return;
        this.taskID = Number(result.id); this.detail = markRaw(result); this.summary = this.scalarSummary(result);
        this.$emit('started', this.taskID);
        this.reportTerminal(result);
        // 小任务可在 Start 响应到达前完成；提交后只做一次对账，进度不轮询大清单。
        await this.loadTask(this.taskID);
        const keys = await GetBackgroundTasks();
        if (generation === this.generation) this.localRunning = (keys || []).includes('cleanup_consolidation');
      } catch (err) { if (generation === this.generation) this.error = String(err?.message || err); }
      finally { this.starting = false; }
    },
    scalarSummary({ preview: _preview, items: _items, ...summary }) { return summary; },
    async loadTask(taskID) {
      const generation = this.generation; this.loading = true; this.error = '';
      try {
        const result = await GetCleanupConsolidationStatus(taskID);
        if (generation !== this.generation || !this.visible || taskID !== this.taskID) return;
        if (!result) { this.error = '没有找到这项集中整理任务。'; return; }
        if (Number(result.version) < Number(this.summary?.version || 0)) return;
        this.detail = markRaw(result); this.summary = this.scalarSummary(result); this.reportTerminal(result);
      } catch (err) { if (generation === this.generation) this.error = String(err?.message || err); }
      finally { if (generation === this.generation) this.loading = false; }
    },
    reportTerminal(summary) {
      if (!isConsolidationTerminal(summary?.status) || Number(summary.version) <= this.terminalVersion) return;
      this.terminalVersion = Number(summary.version); this.cancelling = false;
      if (summary.completed > 0) this.$emit('moved', summary);
    },
    onProgress(summary) {
      if (!summary || Number(summary.id) !== this.taskID || Number(summary.version) <= Number(this.summary?.version || 0)) return;
      this.summary = summary; this.reportTerminal(summary);
      if (this.visible && isConsolidationTerminal(summary.status)) this.loadTask(this.taskID);
    },
    async cancel() {
      if (!this.canCancel || this.cancelling || this.summary?.status !== 'running') return;
      const generation = this.generation; this.cancelling = true;
      try { await CancelCleanupConsolidation(); }
      catch (err) { if (generation === this.generation) { this.error = String(err?.message || err); this.cancelling = false; } }
    },
    review() { if (this.summary?.status !== 'completed' || !this.detail || this.loading) return; const taskID = this.taskID; this.close(); this.$emit('review', taskID); }
  }
};
</script>

<style scoped>
:deep(.consolidation-modal) { width: min(960px, calc(100vw - 40px)); max-height: calc(100vh - 48px); overflow-y: auto; }
header, footer, nav, .destination { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
header { justify-content: space-between; } footer { margin-top: 16px; } p, li, .destination { overflow-wrap: anywhere; }
.consolidation-items { padding-left: 24px; }.consolidation-items > li { padding: 12px; border-bottom: 1px solid var(--hairline); } [role=alert] { color: var(--warning-text); }
</style>
