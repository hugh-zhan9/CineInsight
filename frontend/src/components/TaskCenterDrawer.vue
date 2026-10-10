<template>
  <div
    v-if="open"
    class="task-center-overlay"
    data-test="task-center-overlay"
    @click.self="$emit('close')"
  >
    <aside class="task-center" role="dialog" aria-modal="true" aria-label="任务中心" data-test="task-center">
      <header class="task-center__head">
        <h2>任务中心</h2>
        <span class="task-center__summary" data-test="task-center-summary">{{ summaryText }}</span>
        <button type="button" class="task-center__close" aria-label="关闭任务中心" data-test="task-center-close" @click="$emit('close')">×</button>
      </header>

      <nav class="task-center__tabs" aria-label="任务与运行状态">
        <button type="button" class="btn-secondary btn-compact" :aria-pressed="section === 'tasks'" data-test="task-center-tab-tasks" @click="section = 'tasks'">任务</button>
        <button type="button" class="btn-secondary btn-compact" :aria-pressed="section === 'health'" data-test="task-center-tab-health" @click="section = 'health'">运行状态与诊断</button>
      </nav>
      <div v-if="section === 'health'" class="task-center__body">
        <SystemHealthPanel @run-command="runHealthCommand" />
      </div>
      <div v-else class="task-center__body">
        <p v-if="loadError" class="task-center__error" role="alert" data-test="task-center-error">{{ loadError }}</p>
        <ul v-if="warnings.length" class="task-center__warnings" role="status" data-test="task-center-warnings">
          <li v-for="(warning, index) in warnings" :key="`warning-${index}`">{{ warning }}</li>
        </ul>

        <section v-for="section in itemSections" :key="section.key" class="task-center__section" :data-test="`task-center-section-${section.key}`">
          <h3>{{ section.label }}<span class="task-center__count">{{ section.items.length }}</span></h3>
          <ul class="task-center__items">
            <li
              v-for="item in section.items"
              :key="item.key"
              :class="['task-item', `task-item--${item.state}`, { 'task-item--failed': lastRunFailed(item) }]"
              :data-test="`task-item-${item.key}`"
            >
              <div class="task-item__main">
                <span class="task-item__name">{{ taskLabel(item.key) }}</span>
                <span class="task-item__state" :data-test="`task-item-state-${item.key}`">{{ stateText(item) }}</span>
                <span class="task-item__spacer"></span>
                <button
                  v-for="action in itemActions(item)"
                  :key="action.action"
                  type="button"
                  class="btn-secondary task-item__action"
                  :disabled="isBusy(`item:${item.key}`)"
                  :data-test="`task-action-${item.key}-${action.action}`"
                  @click="runItemAction(item, action)"
                >{{ action.label }}</button>
              </div>
              <div v-if="item.progress && item.progress.total > 0" class="task-item__progress" :data-test="`task-item-progress-${item.key}`">
                <span class="task-item__bar"><i :style="{ width: `${progressPercent(item)}%` }"></i></span>
                <span class="task-item__progress-text">{{ item.progress.done }}/{{ item.progress.total }}</span>
              </div>
              <p v-if="lastRunText(item)" class="task-item__last" :data-test="`task-item-last-${item.key}`">{{ lastRunText(item) }}</p>
              <details v-if="failures(item).length" class="task-item__failures" :data-test="`task-item-failures-${item.key}`">
                <summary>失败明细 {{ failures(item).length }} 条</summary>
                <ul>
                  <li v-for="(failure, index) in failures(item)" :key="`${item.key}-failure-${index}`" :title="failure">{{ failure }}</li>
                </ul>
              </details>
            </li>
          </ul>
        </section>

        <section class="task-center__section" data-test="task-center-recent">
          <h3>最近任务</h3>
          <p v-if="!recentGroups.length" class="task-center__empty">还没有字幕、超分、下载、播放代理、集中整理或视频工作台任务。</p>
          <div v-for="group in recentGroups" :key="group.kind" class="task-recent-group" :data-test="`task-recent-${group.kind}`">
            <h4>{{ group.label }}</h4>
            <ul class="task-center__items">
              <li
                v-for="job in group.jobs"
                :key="`${job.kind}-${job.id}-${job.status}`"
                class="task-recent"
                :data-test="`task-recent-${job.kind}-${job.id}`"
              >
                <div class="task-item__main">
                  <span class="task-item__name" :title="job.title">{{ job.title || '未命名任务' }}</span>
                  <span class="task-item__state">{{ recentStatusText(job) }}</span>
                  <span class="task-item__spacer"></span>
                  <button
                    v-for="action in recentActions(job)"
                    :key="action.action"
                    type="button"
                    class="btn-secondary task-item__action"
                    :disabled="isBusy(`recent:${job.kind}:${job.id}`)"
                    :data-test="`task-recent-action-${job.kind}-${job.id}-${action.action}`"
                    @click="runRecentAction(job, action)"
                  >{{ action.label }}</button>
                </div>
                <p v-if="job.message || job.finished_at" class="task-item__last">
                  <span v-if="job.finished_at">{{ formatTime(job.finished_at) }}</span>
                  <span v-if="job.message && job.finished_at"> · </span>
                  <span v-if="job.message">{{ job.message }}</span>
                </p>
              </li>
            </ul>
          </div>
        </section>
      </div>
    </aside>
  </div>
</template>

<script>
import {
  CancelBrowserDownloadTask, CancelEditProject, CancelEnhancementTask, CancelSubtitleTask, CreatePlaybackProxy, AddDownloadDirectoryToScan,
  GetTaskCenterSnapshot, OpenDirectory, ReimportDownload, ResolveSubtitleJob, RetryDownload, RetryEnhancementTask, RevealDownload
} from '../../wailsjs/go/main/App';
import { runtimeEventsMixin } from './video-list/runtimeEvents.js';
import { backgroundTaskLabel, idleWaitReasonLabel } from '../utils/idleScheduling.js';
import { taskActionRunner } from '../utils/taskCommands.js';
import { CONSOLIDATION_STATUS } from '../utils/cleanupConsolidation.js';
import { PLAYBACK_PROXY_CODE_LABELS } from '../utils/playbackProxy.js';
import { EDIT_STATUS_LABELS } from '../utils/videoEdit.js';
import { confirmAction, notify, notifyError } from '../utils/feedback.js';
import { findCommand, isCommandEnabled } from '../utils/commandRegistry.js';
import SystemHealthPanel from './SystemHealthPanel.vue';

// 事件驱动的重拉合并窗口：清理分析与字幕队列的进度事件可能一秒好几条，只取最后一次。
const REFRESH_DEBOUNCE_MS = 300;

const ITEM_SECTIONS = [
  { key: 'running', label: '运行中' },
  { key: 'waiting_idle', label: '等待空闲' },
  { key: 'idle', label: '空闲' }
];

const ITEM_ACTION_LABELS = { start: '启动', cancel: '取消', run_now: '立即运行' };

// 最近任务：字幕、超分、下载、播放代理与集中整理，顺序固定。
const RECENT_KINDS = [
  { kind: 'subtitle', label: '字幕' },
  { kind: 'enhancement', label: '视频超分' },
  { kind: 'download', label: '插件下载' },
  { kind: 'proxy', label: '播放代理' },
  { kind: 'cleanup_consolidation', label: '集中整理' },
  { kind: 'video_edit', label: '视频工作台' }
];

// 各类任务自己的状态码 → 中文。超分的这份在 P-033 的 utils/enhancement.js 落地后应改为引用那里。
const RECENT_STATUS_LABELS = {
  cleanup_consolidation: CONSOLIDATION_STATUS,
  subtitle: {
    queued: '排队中', running: '生成中', succeeded: '已完成', failed: '失败',
    cancelled: '已取消', needs_confirmation: '待确认', interrupted: '已中断'
  },
  enhancement: {
    queued: '排队中', running: '处理中', cancel_requested: '取消中', cancelled: '已取消', completed: '已完成', failed: '失败'
  },
  download: {
    queued: '排队中', running: '下载中', importing: '入库中', done: '已完成', failed: '失败', canceled: '已取消', interrupted: '已中断'
  },
  proxy: { running: '生成中', cancelled: '已取消', ...PLAYBACK_PROXY_CODE_LABELS },
  video_edit: EDIT_STATUS_LABELS
};

const RECENT_ACTION_LABELS = {
  cleanup_consolidation: { open_consolidation: '查看整理结果' },
  subtitle: { cancel: '取消', force: '强制生成', discard: '放弃', retry: '重试' },
  enhancement: { cancel: '取消', retry: '重试', reveal_output: '在访达中显示', open_output_in_library: '在片库中打开' },
  download: { cancel: '取消', retry: '重试', reveal: '在访达中显示', add_directory_to_scan: '加入扫描目录', reimport: '重新入库' },
  proxy: { retry: '重新生成' },
  video_edit: { cancel: '取消', open_video_edit: '在工作台中打开' }
};

function asArray(value) {
  return Array.isArray(value) ? value : [];
}

function pad(value) {
  return String(value).padStart(2, '0');
}

// TaskRecentJob.id 是字符串（D-PC18）；数字参数的绑定调用前换成数字，非法就拒绝，不传 NaN 过去。
function numericID(value) {
  const id = Number(value);
  if (!Number.isInteger(id) || id <= 0) throw new Error('任务编号无效');
  return id;
}

// 任务中心抽屉（D-PC18、APP-03）。只读聚合 GetTaskCenterSnapshot：22 个后台任务 key 的状态、
// 进度、上一轮结果与可用动作，外加字幕 / 超分 / 下载 / 播放代理 / 集中整理的最近任务。
// 常挂载（关着也在），这样顶栏角标能跟着 task-center-changed 走；打开时再主动拉一次。
export default {
  name: 'TaskCenterDrawer',
  components: { SystemHealthPanel },
  mixins: [runtimeEventsMixin],
  props: {
    open: { type: Boolean, default: false }
  },
  emits: ['close', 'badge-change', 'open-video', 'open-consolidation', 'open-video-edit'],
  data() {
    return {
      snapshot: { items: [], recent: [], warnings: [] },
      loadError: '',
      section: 'tasks',
      busy: {}
    };
  },
  computed: {
    items() {
      return asArray(this.snapshot.items);
    },
    warnings() {
      return asArray(this.snapshot.warnings);
    },
    itemSections() {
      return ITEM_SECTIONS
        .map(section => ({ ...section, items: this.items.filter(item => item.state === section.key) }))
        .filter(section => section.items.length > 0);
    },
    recentGroups() {
      const recent = asArray(this.snapshot.recent);
      return RECENT_KINDS
        .map(group => ({ ...group, jobs: recent.filter(job => job.kind === group.kind) }))
        .filter(group => group.jobs.length > 0);
    },
    // 顶栏角标：运行中数量；有任何一项上一轮有失败时标红点（D-PC18）。
    badge() {
      return {
        running: this.items.filter(item => item.state === 'running').length,
        failed: this.items.some(item => this.lastRunFailed(item))
      };
    },
    summaryText() {
      const waiting = this.items.filter(item => item.state === 'waiting_idle').length;
      const parts = [`运行中 ${this.badge.running}`];
      if (waiting) parts.push(`等待空闲 ${waiting}`);
      return parts.join(' · ');
    }
  },
  watch: {
    open(open) {
      if (open) this.refresh();
    },
    badge: {
      immediate: true,
      handler(badge) { this.$emit('badge-change', badge); }
    }
  },
  mounted() {
    document.addEventListener('keydown', this.handleDocumentKeydown);
    this.refresh();
    // 后端在 App 层把登记表、空闲门与各服务状态事件合并 500ms 后发这个事件，载荷就是快照。
    this.registerRuntimeEvent('task-center-changed', snapshot => this.applySnapshot(snapshot));
    // 字幕排队变化与清理分析进度由服务层直接发，App 层收不到，不会触发上面那个事件，自己重拉。
    this.registerRuntimeEvent('subtitle-queue', () => this.scheduleRefresh());
    this.registerRuntimeEvent('cleanup-progress', () => this.scheduleRefresh());
  },
  beforeUnmount() {
    document.removeEventListener('keydown', this.handleDocumentKeydown);
    clearTimeout(this._refreshTimer);
  },
  methods: {
    async runHealthCommand(id) {
      if (id === 'action:task-center') {
        this.section = 'tasks';
        return;
      }
      const command = findCommand(id);
      if (!command || !isCommandEnabled(command)) {
        notifyError('该处理入口当前不可用');
        return;
      }
      this.$emit('close');
      try { await command.run(); } catch (_) { notifyError('打开处理入口失败，请从设置页进入'); }
    },
    // 与 BaseModal 同口径：Esc 关闭。已被别的处理器接手（preventDefault）的按键不再处理。
    handleDocumentKeydown(event) {
      if (!this.open || event.key !== 'Escape' || event.defaultPrevented) return;
      this.$emit('close');
    },
    taskLabel(key) {
      return backgroundTaskLabel(key);
    },
    applySnapshot(snapshot) {
      if (!snapshot) return;
      this.snapshot = {
        items: asArray(snapshot.items),
        recent: asArray(snapshot.recent),
        warnings: asArray(snapshot.warnings)
      };
      this.loadError = '';
    },
    async refresh() {
      try {
        this.applySnapshot(await GetTaskCenterSnapshot());
      } catch (err) {
        this.loadError = `读取任务状态失败：${err}`;
      }
    },
    scheduleRefresh() {
      clearTimeout(this._refreshTimer);
      this._refreshTimer = setTimeout(() => {
        this._refreshTimer = null;
        this.refresh();
      }, REFRESH_DEBOUNCE_MS);
    },
    isBusy(key) {
      return Boolean(this.busy[key]);
    },
    async withBusy(key, work) {
      if (this.busy[key]) return;
      this.busy = { ...this.busy, [key]: true };
      try {
        await work();
      } finally {
        const next = { ...this.busy };
        delete next[key];
        this.busy = next;
        this.scheduleRefresh();
      }
    },
    stateText(item) {
      const reason = item.gate_reason ? `等待空闲（${idleWaitReasonLabel(item.gate_reason)}）` : '';
      if (item.state === 'running') return reason ? `运行中 · ${reason}` : '运行中';
      if (item.state === 'waiting_idle') return reason || '等待空闲';
      return '空闲';
    },
    progressPercent(item) {
      const total = Number(item.progress?.total || 0);
      if (total <= 0) return 0;
      return Math.min(100, Math.max(0, Math.round((Number(item.progress?.done || 0) / total) * 100)));
    },
    lastRunFailed(item) {
      return Number(item?.last_run?.failed || 0) > 0 || asArray(item?.last_run?.failures).length > 0;
    },
    lastRunText(item) {
      const run = item.last_run;
      if (!run) return '';
      const time = this.formatTime(run.finished_at);
      const counts = [`成功 ${Number(run.succeeded || 0)}`, `失败 ${Number(run.failed || 0)}`];
      if (Number(run.skipped) > 0) counts.push(`跳过 ${Number(run.skipped)}`);
      if (Number(run.remaining) > 0) counts.push(`未处理 ${Number(run.remaining)}`);
      return time ? `上一轮（${time}）：${counts.join(' · ')}` : `上一轮：${counts.join(' · ')}`;
    },
    failures(item) {
      return asArray(item.last_run?.failures);
    },
    // 只列出前端确实有绑定的动作；后端给了而前端没有绑定的（例如尚无零参绑定的 retry_failed）不渲染。
    itemActions(item) {
      return asArray(item.actions)
        .filter(action => ITEM_ACTION_LABELS[action] && taskActionRunner(item.key, action))
        .map(action => ({ action, label: ITEM_ACTION_LABELS[action] }));
    },
    runItemAction(item, action) {
      const runner = taskActionRunner(item.key, action.action);
      if (!runner) return undefined;
      return this.withBusy(`item:${item.key}`, async () => {
        try {
          await runner();
        } catch (err) {
          notifyError(`${action.label}「${this.taskLabel(item.key)}」失败：${err}`);
        }
      });
    },
    recentStatusText(job) {
      return RECENT_STATUS_LABELS[job.kind]?.[job.status] || '状态未知';
    },
    recentActions(job) {
      let labels = RECENT_ACTION_LABELS[job.kind] || {};
      // 字幕已生成、只是写回文件失败（MEDIA-04）：「强制生成」实际是再写一次，按用户看到的意思叫「重试写回」。
      if (job.kind === 'subtitle' && job.error_code === 'subtitle_replace_failed') {
        labels = { ...labels, force: '重试写回' };
      }
      return asArray(job.actions)
        .filter(action => labels[action])
        .map(action => ({ action, label: labels[action] }));
    },
    runRecentAction(job, action) {
      return this.withBusy(`recent:${job.kind}:${job.id}`, async () => {
        try {
          await this.performRecentAction(job, action.action);
        } catch (err) {
          notifyError(`${action.label}失败：${err?.message || err}`);
        }
      });
    },
    async performRecentAction(job, action) {
      if (job.kind === 'cleanup_consolidation' && action === 'open_consolidation') {
        this.$emit('open-consolidation', numericID(job.id)); this.$emit('close'); return;
      }
      if (job.kind === 'subtitle') {
        const jobID = numericID(job.id);
        if (action === 'cancel') return CancelSubtitleTask(jobID);
        if (action === 'discard') {
          const ok = await confirmAction({
            title: '放弃待确认的字幕',
            message: `放弃「${job.title || '该视频'}」这份待确认的字幕？生成的临时结果会被删除，已有的字幕文件不受影响。`,
            confirmText: '放弃',
            danger: true
          });
          if (!ok) return undefined;
        }
        const result = await ResolveSubtitleJob(jobID, action);
        if (result?.error_code) notifyError(result.message || '操作没有完成');
        return result;
      }
      if (job.kind === 'enhancement') {
        const taskID = numericID(job.id);
        if (action === 'cancel') return CancelEnhancementTask(taskID);
        if (action === 'retry') return RetryEnhancementTask(taskID);
        if (action === 'reveal_output') return OpenDirectory(numericID(job.output_video_id));
        if (action === 'open_output_in_library') {
          this.$emit('open-video', numericID(job.output_video_id));
          this.$emit('close');
        }
        return undefined;
      }
      if (job.kind === 'download') {
        const actions = {
          cancel: () => CancelBrowserDownloadTask(job.id),
          retry: () => RetryDownload(job.id),
          reveal: () => RevealDownload(job.id),
          add_directory_to_scan: () => AddDownloadDirectoryToScan(job.id),
          reimport: () => ReimportDownload(job.id)
        };
        const result = await actions[action]?.();
        if (result && result.code && result.code !== 'ok') notifyError(result.message || '操作没有完成');
        else if (action === 'add_directory_to_scan' || action === 'reimport') notify('已提交入库，完成后片库会自动刷新');
        return result;
      }
      if (job.kind === 'video_edit') {
        const projectID = numericID(job.id);
        if (action === 'cancel') return CancelEditProject(projectID);
        if (action === 'open_video_edit') {
          this.$emit('open-video-edit', projectID);
          this.$emit('close');
        }
        return undefined;
      }
      if (job.kind === 'proxy' && action === 'retry') {
        return CreatePlaybackProxy(numericID(job.video_id || job.id));
      }
      return undefined;
    },
    formatTime(value) {
      if (!value) return '';
      const date = new Date(value);
      if (Number.isNaN(date.getTime()) || date.getFullYear() < 2000) return '';
      return `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`;
    }
  }
};
</script>

<style scoped>
.task-center__tabs { display: flex; flex-wrap: wrap; gap: 8px; padding: 8px 18px; border-bottom: 1px solid var(--border-color); }
.task-center__tabs button[aria-pressed="true"] { background: var(--accent-soft); color: var(--accent-text); border-color: var(--accent-border); }
.task-center-overlay {
  position: fixed;
  inset: 0;
  z-index: 950;
  display: flex;
  justify-content: flex-end;
  background: var(--overlay-bg);
}
.task-center {
  width: 460px;
  max-width: 100%;
  height: 100%;
  display: flex;
  flex-direction: column;
  border-left: 1px solid var(--hairline);
  background: var(--panel-bg);
  box-shadow: var(--shadow-modal);
  color: var(--text-primary);
}
.task-center__head {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 14px 16px;
  border-bottom: 1px solid var(--hairline);
}
.task-center__head h2 { font-size: 16px; font-weight: 700; }
.task-center__summary { color: var(--text-muted); font-size: 12px; }
.task-center__close {
  margin-left: auto;
  border: 0;
  background: transparent;
  color: var(--text-muted);
  font-size: 20px;
  line-height: 1;
  cursor: pointer;
}
.task-center__body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  overscroll-behavior: contain;
  padding: 10px 16px 18px;
}
.task-center__error { color: var(--danger-color); font-size: 13px; margin-bottom: 8px; }
.task-center__warnings {
  margin: 0 0 10px;
  padding: 8px 10px 8px 26px;
  border: 1px solid var(--warning-border);
  border-radius: var(--radius);
  color: var(--warning-color);
  font-size: 12.5px;
}
.task-center__section { margin-top: 10px; }
.task-center__section h3 {
  margin-bottom: 6px;
  color: var(--text-secondary);
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.04em;
}
.task-center__count { margin-left: 6px; color: var(--text-muted); font-family: var(--font-mono); }
.task-center__empty { color: var(--text-muted); font-size: 12.5px; }
.task-center__items { list-style: none; margin: 0; padding: 0; display: grid; gap: 6px; }
.task-recent-group h4 { margin: 8px 0 4px; color: var(--text-muted); font-size: 11.5px; font-weight: 600; }
.task-item,
.task-recent {
  padding: 8px 10px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius);
  background: var(--control-bg);
  font-size: 12.5px;
}
.task-item--running { border-color: var(--border-strong); }
.task-item--failed { border-color: var(--warning-border); }
.task-item__main { display: flex; align-items: center; gap: 8px; min-width: 0; }
.task-item__name { font-weight: 600; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.task-item__state { flex: none; color: var(--text-secondary); }
.task-item--waiting_idle .task-item__state { color: var(--warning-color); }
.task-item__spacer { flex: 1; }
.task-item__action { flex: none; height: 26px; padding: 0 10px; font-size: 12px; }
.task-item__progress { display: flex; align-items: center; gap: 8px; margin-top: 6px; }
.task-item__bar { flex: 1; height: 4px; border-radius: 2px; background: var(--hairline); overflow: hidden; }
.task-item__bar i { display: block; height: 100%; background: var(--accent-color); }
.task-item__progress-text { color: var(--text-muted); font-family: var(--font-mono); font-size: 11.5px; }
.task-item__last { margin-top: 4px; color: var(--text-muted); font-size: 12px; overflow-wrap: anywhere; }
.task-item__failures { margin-top: 4px; color: var(--text-secondary); font-size: 12px; }
.task-item__failures summary { cursor: pointer; color: var(--warning-color); }
.task-item__failures ul { margin: 4px 0 0; padding-left: 18px; max-height: 160px; overflow-y: auto; }
.task-item__failures li { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
