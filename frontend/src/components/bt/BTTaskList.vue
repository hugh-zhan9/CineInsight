<template>
  <section class="bt-tasks" aria-label="BT 下载任务">
    <div class="bt-task-toolbar"><h3>BT 下载</h3><button type="button" class="btn btn--secondary btn--sm" :disabled="loading" @click="$emit('refresh')">刷新</button></div>
    <p v-if="error" role="alert" class="bt-task-error">{{ error }}</p><p v-if="loading" role="status" class="help-text">正在读取任务…</p><p v-else-if="!tasks.length" class="help-text">还没有从这里创建的 BT 下载。</p>
    <ul class="bt-task-list"><li v-for="task in tasks" :key="task.id" class="bt-task">
      <div class="bt-task-heading"><strong>{{ task.title || '未命名资源' }}</strong><span>{{ status(task.stage) }}</span></div>
      <ProgressBar :value="task.progress" label="所选文件下载进度" />
      <p class="help-text">下载：{{ status(task.download_state || task.remote_state) }} · 入库：{{ task.imported_count ?? 0 }}/{{ task.selected_video_count ?? '待核实' }}</p>
      <p v-if="task.read_only_reason === 'connection_retired'" class="help-text" data-test="bt-retired-connection">这是旧连接的任务，仅供查看</p>
      <p v-if="task.error_message" role="alert" class="bt-task-error">{{ task.error_message }}</p>
      <div class="bt-task-actions"><button v-for="action in actions(task)" :key="action" type="button" class="btn btn--secondary btn--sm" :disabled="readonly || busyIds.includes(task.id)" :data-test="`bt-task-${task.id}-${action}`" @click="act(task, action)">{{ labels[action] }}</button><button type="button" class="btn-link" @click="$emit('detail', task.id)">文件明细</button></div>
      <ul v-if="detail?.id === task.id" class="bt-task-files"><li v-for="file in detail.files || []" :key="file.id"><span>{{ file.relative_path ?? file.path }}</span><span>{{ file.selected ? '已选择' : '未选择' }} · 下载 {{ progressText(file.progress) }} · {{ importStatus(file.import_state) }}</span><span v-if="file.error_message || file.error_code" class="bt-task-error" role="status" :data-test="`bt-file-error-${file.id}`">{{ fileError(file) }}</span><span v-if="file.duplicate?.status === 'found'" class="help-text">片库已有：<span v-for="match in file.duplicate.matches || []" :key="match.video_id">{{ match.title }}（{{ match.kind === 'exact' ? '相同文件' : '其他版本' }}） </span></span><span v-else-if="file.duplicate?.status === 'failed'" class="help-text">查重未完成，不影响下载和入库。</span><button v-if="file.import_state === 'imported' && file.video_id" type="button" class="btn-link" @click="$emit('play', file.video_id)">播放</button></li></ul>
    </li></ul>
    <button v-if="hasMore" type="button" class="btn btn--secondary btn--sm" :disabled="loading" @click="$emit('load-more')">加载更多</button>
    <Teleport v-if="pending" to="body"><DialogShell :title="labels[pending.action]" size="md" :close-on-overlay="false" @close="pending = null"><p>{{ confirmation }}</p><p><strong>{{ pending.title }}</strong></p><template #footer><button type="button" class="btn btn--secondary" @click="pending = null">取消</button><button type="button" class="btn btn--primary" :disabled="!pendingCurrent" data-test="bt-task-confirm" @click="confirm">{{ labels[pending.action] }}</button></template></DialogShell></Teleport>
  </section>
</template>
<script>
import DialogShell from '../ui/DialogShell.vue';
import ProgressBar from '../ui/ProgressBar.vue';
import { btTaskStatus, btImportStatus, btFileErrorText } from '../../utils/btFormat.js';
const LABELS = { stop: '停止下载', start: '继续下载', cancel_draft: '取消准备', reimport: '重新入库', release: '解除下载管理…', reconcile: '重新核实', reconfirm_submit: '重新确认操作…', abandon: '放弃本次提交…' };
const PENDING_OPERATION = {
  metadata: '准备文件清单（不会启动影片下载）',
  add: '继续下载已选文件', priority_zero: '继续下载已选文件', priority_select: '继续下载已选文件', start: '继续下载已选文件',
  stop: '停止该任务', cancel_stop: '取消清单准备且保留文件', cancel_delete: '取消清单准备且保留文件',
  restore: '核实恢复记录并在需要时重新选择文件',
};
const CONFIRM = { release: '解除后不再由应用管理这批下载。文件保留，操作前会重新核实目录内是否仍有未完成内容。', reconfirm_submit: '前次操作结果尚不确定。应用会先核实下载器状态，再按原意图执行，可能与迟到的原请求重复。', abandon: '放弃后，应用不再继续操作这次任务；文件和目录保护保留。已发送的操作可能稍后生效，请在 qBittorrent 中核实。', cancel_draft: '取消本次文件准备。已有文件保留；元数据任务的移除结果会继续核实。' };
export default {
  name: 'BTTaskList', components: { DialogShell, ProgressBar },
  props: { tasks: { type: Array, default: () => [] }, detail: { type: Object, default: null }, loading: Boolean, readonly: Boolean, hasMore: Boolean, busyIds: { type: Array, default: () => [] }, error: { type: String, default: '' } },
  emits: ['refresh', 'load-more', 'detail', 'play', 'control'], data: () => ({ pending: null, labels: LABELS }),
  computed: { confirmation() { if (this.pending?.action === 'reconfirm_submit') { const task = this.tasks.find(t => t.id === this.pending.id); const operation = PENDING_OPERATION[task?.pending_action]; if (operation) return `前次操作结果尚不确定。应用会先核实下载器状态，然后${operation}，可能与迟到的原请求重复。`; } return CONFIRM[this.pending?.action] || ''; }, pendingCurrent() { const t = this.tasks.find(t => t.id === this.pending?.id); return Boolean(t && t.revision === this.pending.revision && this.actions(t).includes(this.pending.action) && !this.readonly && !this.busyIds.includes(t.id)); } },
  methods: { fileError: btFileErrorText, progressText(value) { return typeof value === 'number' && Number.isFinite(value) ? `${Math.round(Math.max(0, Math.min(1, value)) * 100)}%` : '待核实'; }, status: btTaskStatus, importStatus: btImportStatus, actions(task) { return (task.allowed_actions || []).filter(a => Object.hasOwn(LABELS, a)); }, act(task, action) { if (this.readonly || this.busyIds.includes(task.id) || !this.actions(task).includes(action)) return; if (CONFIRM[action]) this.pending = { id: task.id, revision: task.revision, title: task.title, action }; else this.$emit('control', { task_id: task.id, expected_revision: task.revision, action }); }, confirm() { if (!this.pendingCurrent) return; const p = this.pending; this.pending = null; this.$emit('control', { task_id: p.id, expected_revision: p.revision, action: p.action, confirmed: true }); } }
};
</script>
<style scoped>
.bt-tasks { display: grid; gap: var(--space-3); }
.bt-task-toolbar, .bt-task-heading, .bt-task-actions { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: var(--space-2); }
.bt-task-toolbar h3 { margin: 0; }
.bt-task-list, .bt-task-files { list-style: none; margin: 0; padding: 0; }
.bt-task-list { display: grid; gap: var(--space-3); }
.bt-task { display: grid; gap: var(--space-2); border: 1px solid var(--hairline); border-radius: var(--radius); padding: var(--space-3); }
.bt-task p { margin: 0; }
.bt-task-error { color: var(--danger-text); }
.bt-task-actions { justify-content: start; }
.bt-task-files { font-size: var(--fs-secondary); max-height: 35vh; overflow: auto; }
.bt-task-files li { display: flex; flex-wrap: wrap; gap: var(--space-2); padding: var(--space-2) 0; border-top: 1px solid var(--hairline); overflow-wrap: anywhere; }
</style>
