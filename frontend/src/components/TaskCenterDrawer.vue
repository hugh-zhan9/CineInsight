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
        <button type="button" class="btn-icon btn--sm task-center__close" aria-label="关闭任务中心" data-test="task-center-close" @click="$emit('close')"><AppIcon name="close" :size="16" /></button>
      </header>

      <!-- 三个分区（D-UD-06 AppTabs），每个页签带 task-center-tab-* 钩子；「最近操作」的条数写在徽标里。
           下面三块是各自的面板（role=tabpanel，与页签的 aria-controls / id 互相指向）。 -->
      <AppTabs v-model="section" class="task-center__tabs" :tabs="sectionTabs" id-prefix="task-center" aria-label="任务中心内容" />
      <div v-if="section === 'health'" id="task-center-panel-health" class="task-center__body" role="tabpanel" aria-labelledby="task-center-tab-health">
        <SystemHealthPanel @run-command="runHealthCommand" />
      </div>
      <!-- 最近操作（统一设计 D-UD-09）：事后撤销的入口，与提示里的「撤销」是同一条记录。只在内存里，重启后清空。 -->
      <div v-else-if="section === 'recent'" id="task-center-panel-recent" class="task-center__body" role="tabpanel" aria-labelledby="task-center-tab-recent" data-test="task-center-operations">
        <p class="task-center__note" data-test="task-center-operations-note">这里列出最近 {{ operationsLimit }} 次可以撤销的操作（移到废纸篓、批量编辑等），新的在前。只记在内存里，应用重启后清空。</p>
        <EmptyState
          v-if="!operations.length"
          icon="undo"
          title="还没有可以撤销的操作"
          message="移到废纸篓、批量编辑这类操作完成后会出现在这里，错过了提示里的「撤销」也能在这里撤销。"
          data-test="task-center-operations-empty"
        />
        <ul v-else class="task-center__items">
          <li v-for="operation in operations" :key="operation.id" class="task-item task-operation" :data-test="`task-operation-${operation.id}`">
            <div class="task-item__main">
              <span class="task-item__name" :title="operation.label">{{ operation.label }}</span>
              <span class="task-item__spacer"></span>
              <time class="task-item__state" :datetime="isoTime(operation.at)" :title="formatTime(operation.at)">{{ relativeTime(operation.at) }}</time>
              <button
                type="button"
                class="btn btn--secondary btn--sm task-item__action"
                :disabled="operation.undoing"
                :data-test="`task-operation-undo-${operation.id}`"
                @click="undoOperation(operation)"
              >{{ operation.undoing ? '撤销中…' : '撤销' }}</button>
            </div>
          </li>
        </ul>
      </div>
      <div v-else id="task-center-panel-tasks" class="task-center__body" role="tabpanel" aria-labelledby="task-center-tab-tasks">
        <AppBanner v-if="loadError" tone="danger" title="读取任务状态失败" :message="loadError" data-test="task-center-error" />
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
                  class="btn btn--secondary btn--sm task-item__action"
                  :disabled="isBusy(`item:${item.key}`)"
                  :data-test="`task-action-${item.key}-${action.action}`"
                  @click="runItemAction(item, action)"
                >{{ action.label }}</button>
              </div>
              <div v-if="item.progress && item.progress.total > 0" class="task-item__progress" :data-test="`task-item-progress-${item.key}`">
                <ProgressBar class="task-item__bar" :value="progressPercent(item) / 100" />
                <span class="task-item__progress-text">{{ item.progress.done }}/{{ item.progress.total }}</span>
              </div>
              <p v-if="runCountsText(item)" class="task-item__last" :data-test="`task-item-run-counts-${item.key}`">{{ runCountsText(item) }}</p>
              <p v-if="lastRunText(item)" class="task-item__last" :data-test="`task-item-last-${item.key}`">{{ lastRunText(item) }}</p>
              <p v-for="(note, index) in itemNotes(item)" :key="`${item.key}-note-${index}`" class="task-item__note" :data-test="`task-item-note-${item.key}`">{{ note }}</p>
              <details v-if="failures(item).length" class="task-item__failures" :data-test="`task-item-failures-${item.key}`">
                <summary>失败明细 {{ failures(item).length }} 条<template v-if="failuresTruncated(item)">（共 {{ item.last_run.failed }} 条失败，只保留了前 {{ failures(item).length }} 条明细）</template></summary>
                <ul>
                  <li v-for="(failure, index) in failures(item)" :key="`${item.key}-failure-${index}`" :title="failure">{{ failure }}</li>
                </ul>
              </details>
              <p v-if="failureLink(item)" class="task-item__note">
                <button
                  type="button"
                  class="btn-link"
                  :data-test="`task-item-failure-link-${item.key}`"
                  @click="openFailureLink(item)"
                >{{ failureLink(item).label }}</button>
              </p>
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
                    class="btn btn--secondary btn--sm task-item__action"
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
import * as recentOperations from '../utils/recentOperations.js';
import SystemHealthPanel from './SystemHealthPanel.vue';
import AppBanner from './ui/AppBanner.vue';
import AppIcon from './ui/AppIcon.vue';
import AppTabs from './ui/AppTabs.vue';
import EmptyState from './ui/EmptyState.vue';
import { hasOpenModalLayer } from './ui/modalSignals.js';
import ProgressBar from './ui/ProgressBar.vue';

// 事件驱动的重拉合并窗口：清理分析与字幕队列的进度事件可能一秒好几条，只取最后一次。
const REFRESH_DEBOUNCE_MS = 300;
// 「最近操作」里的相对时间（刚刚、3 分钟前）多久走一次。
const CLOCK_TICK_MS = 30000;

const ITEM_SECTIONS = [
  { key: 'running', label: '运行中' },
  { key: 'waiting_idle', label: '等待空闲' },
  { key: 'idle', label: '空闲' }
];

const ITEM_ACTION_LABELS = { start: '启动', cancel: '取消', run_now: '立即运行' };

// 有失败时能在片库管理里看到全部失败对象的任务：视频 AI 打标 → 「AI 打标失败」视图（失败数与它同口径）；
// 技术信息 → 缺失筛选「技术信息」（探测失败的视频都在里面，另含还没探测的）。明细只保留前 50 条，这里看全部。
const FAILURE_LINKS = {
  ai_tagging: { command: 'nav:smart:ai_tag_failed', label: '在片库管理中查看' },
  technical: { missing: 'technical', label: '在片库管理中查看缺技术信息的视频' }
};

// 片库页后台任务状态条原有的几种说法（D-P3-05 收进任务中心，文案照搬）。
const PREPARING_TEXT = '正在统计待处理项…';
// 「总数为 0」的一轮：补全类沿用状态条的「无需补全」，其余任务（索引、打标、代理、写出）说「无需处理」。
const BACKFILL_TASK_KEYS = new Set(['technical', 'frame_hash', 'phash', 'local_metadata', 'exif', 'image_phash']);
const NOTHING_TO_DO_TEXT = { backfill: '无需补全（已是最新状态）', other: '无需处理（已是最新状态）' };
// 近重复指纹条上的两句说明：指纹的用途（在跑、在等空闲或有过一轮时说），以及「失败」是什么意思（上一轮有失败时说）。
const TASK_NOTES = {
  phash: {
    about: '指纹用于后续查找近重复视频，候选结果请在清理中心查看。',
    failed: '处理失败表示未能生成指纹，部分视频仍可播放；具体原因见下方。'
  }
};

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

// 相对时间：一分钟内「刚刚」，一小时内「N 分钟前」，一天内「N 小时前」，再早写月日时分。
export function relativeTimeText(at, now = Date.now()) {
  const time = Number(at);
  if (!Number.isFinite(time) || time <= 0) return '';
  const minutes = Math.floor(Math.max(0, now - time) / 60000);
  if (minutes < 1) return '刚刚';
  if (minutes < 60) return `${minutes} 分钟前`;
  if (minutes < 24 * 60) return `${Math.floor(minutes / 60)} 小时前`;
  const date = new Date(time);
  return `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

// 任务中心抽屉（D-PC18、APP-03）。只读聚合 GetTaskCenterSnapshot：全部后台任务 key 的状态、
// 进度、上一轮结果与可用动作，外加字幕 / 超分 / 下载 / 播放代理 / 集中整理的最近任务。
// D-P3-05 起片库页的后台任务状态条收进这里：准备中、跳过数、已完成 / 已取消、近重复指纹的复用与
// 移出范围、「无需补全（已是最新状态）」，以及 NFO 写出（local_metadata_export）的取消。
// 常挂载（关着也在），这样顶栏角标能跟着 task-center-changed 走；打开时再主动拉一次。
// 「最近操作」页签（统一设计 D-UD-09）读 utils/recentOperations.js：生产方用 notifyUndoable 记下的可撤销操作，
// 撤销走同一条记录（recentOperations.undo），失败时记录留着并提示原因。
export default {
  name: 'TaskCenterDrawer',
  components: { AppBanner, AppIcon, AppTabs, EmptyState, ProgressBar, SystemHealthPanel },
  mixins: [runtimeEventsMixin],
  props: {
    open: { type: Boolean, default: false }
  },
  emits: ['close', 'badge-change', 'open-video', 'open-consolidation', 'open-video-edit', 'open-library-missing'],
  data() {
    return {
      snapshot: { items: [], recent: [], warnings: [] },
      loadError: '',
      section: 'tasks',
      busy: {},
      operationsLimit: recentOperations.RECENT_OPERATIONS_LIMIT,
      now: Date.now()
    };
  },
  computed: {
    // 新的在前（recentOperations.list 的顺序）；读的是响应式状态，别处记下或撤销后这里跟着变。
    operations() {
      return recentOperations.list();
    },
    sectionTabs() {
      return [
        { key: 'tasks', label: '任务', dataTest: 'task-center-tab-tasks' },
        { key: 'recent', label: '最近操作', count: this.operations.length || null, dataTest: 'task-center-tab-recent' },
        { key: 'health', label: '运行状态与诊断', dataTest: 'task-center-tab-health' }
      ];
    },
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
      this.syncClock();
    },
    section() {
      this.syncClock();
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
    clearInterval(this._clockTimer);
  },
  methods: {
    // 只在「最近操作」页签开着时走表，免得关着的抽屉每半分钟重渲染一次。
    syncClock() {
      clearInterval(this._clockTimer);
      this._clockTimer = null;
      this.now = Date.now();
      if (this.open && this.section === 'recent') this._clockTimer = setInterval(() => { this.now = Date.now(); }, CLOCK_TICK_MS);
    },
    relativeTime(at) {
      return relativeTimeText(at, this.now);
    },
    isoTime(at) {
      const date = new Date(Number(at));
      return Number.isNaN(date.getTime()) ? undefined : date.toISOString();
    },
    // 撤销成功后记录自己从列表里消失；失败时记录留着可再试，原因用全局提示说（与提示里的「撤销」同一口径）。
    async undoOperation(operation) {
      try {
        await recentOperations.undo(operation.id);
      } catch (err) {
        const detail = typeof err === 'string' ? err : err?.message;
        const reason = typeof detail === 'string' ? detail.trim() : '';
        notifyError(reason ? `撤销失败：${reason}` : '撤销失败');
      }
    },
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
    // 与 BaseModal 同口径：Esc 关闭。已被别的处理器接手（preventDefault）的按键不再处理；
    // 抽屉里点开的确认框这类模态层开着时让给它（见 ui/modalSignals.js）：抽屉常挂载、比它们先登记 keydown，
    // 不让的话一下 Esc 会先关抽屉、再关确认框。
    handleDocumentKeydown(event) {
      if (!this.open || event.key !== 'Escape' || event.defaultPrevented || hasOpenModalLayer()) return;
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
        this.loadError = String(err?.message || err || '').trim() || '请稍后重试';
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
      if (item.state === 'running') return ['运行中', item.preparing ? PREPARING_TEXT : '', reason].filter(Boolean).join(' · ');
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
    // 运行中这一轮到目前为止的计数（run_counts，只有近重复指纹给）：照搬片库页状态条在跑时的说法，
    // 新生成、复用、失败总是列，移出处理范围不为 0 才列（2026-10-14 起，此前只在这一轮结束后给出）。
    runCountsText(item) {
      const counts = item.run_counts;
      if (!counts) return '';
      const text = `本轮新生成 ${Number(counts.succeeded || 0)} 部，已有指纹复用 ${Number(counts.reused || 0)} 部，处理失败 ${Number(counts.failed || 0)} 部`;
      return Number(counts.out_of_scope) > 0 ? `${text}，已移出处理范围 ${Number(counts.out_of_scope)} 部` : text;
    },
    // 上一轮：总数为 0 的一轮只说「无需补全 / 无需处理（已是最新状态）」；其余列计数（跳过、未处理、
    // 复用、移出范围只在不为 0 时列），再按状态条的说法收尾：取消的写「已取消」，有结束时间的写
    // 「已完成」或「本轮结束，有失败项」（视频 AI 打标给的是累计数、没有结束时间，不收尾）。
    lastRunText(item) {
      const run = item.last_run;
      if (!run) return '';
      const time = this.formatTime(run.finished_at);
      const head = time ? `上一轮（${time}）` : '上一轮';
      if (run.nothing_to_do) return `${head}：${NOTHING_TO_DO_TEXT[BACKFILL_TASK_KEYS.has(item.key) ? 'backfill' : 'other']}`;
      const counts = [`成功 ${Number(run.succeeded || 0)}`, `失败 ${Number(run.failed || 0)}`];
      if (Number(run.skipped) > 0) counts.push(`跳过 ${Number(run.skipped)}`);
      if (Number(run.remaining) > 0) counts.push(`未处理 ${Number(run.remaining)}`);
      if (Number(run.reused) > 0) counts.push(`已有指纹复用 ${Number(run.reused)}`);
      if (Number(run.out_of_scope) > 0) counts.push(`已移出处理范围 ${Number(run.out_of_scope)}`);
      return `${head}：${counts.join(' · ')}${this.lastRunOutcome(item)}`;
    },
    lastRunOutcome(item) {
      const run = item.last_run;
      if (run.cancelled) return '（已取消）';
      if (!run.finished_at) return '';
      return this.lastRunFailed(item) ? '（本轮结束，有失败项）' : '（已完成）';
    },
    itemNotes(item) {
      const notes = TASK_NOTES[item.key];
      if (!notes || (item.state === 'idle' && !item.last_run)) return [];
      return this.lastRunFailed(item) ? [notes.about, notes.failed] : [notes.about];
    },
    failures(item) {
      return asArray(item.last_run?.failures);
    },
    // 后端明细截在 50 条，失败数是全部：多出来的照实说。
    failuresTruncated(item) {
      return Number(item.last_run?.failed || 0) > this.failures(item).length;
    },
    failureLink(item) {
      return Number(item.last_run?.failed || 0) > 0 ? FAILURE_LINKS[item.key] || null : null;
    },
    async openFailureLink(item) {
      const link = FAILURE_LINKS[item.key];
      if (!link) return;
      this.$emit('close');
      if (link.missing) {
        this.$emit('open-library-missing', { kind: 'video', field: link.missing });
        return;
      }
      const command = findCommand(link.command);
      if (!command || !isCommandEnabled(command)) {
        notifyError('片库管理暂时打不开，请稍后再试');
        return;
      }
      try { await command.run(); } catch (_) { notifyError('打开片库管理失败，请稍后再试'); }
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
.task-center__tabs { flex: none; padding: 0 var(--space-4); }
.task-center-overlay {
  position: fixed;
  inset: 0;
  z-index: var(--z-drawer);
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
  gap: var(--space-3);
  padding: var(--space-3) var(--space-4);
  border-bottom: 1px solid var(--hairline);
}
.task-center__head h2 { font-size: var(--fs-section); font-weight: var(--fw-bold); }
.task-center__summary { color: var(--text-muted); font-size: var(--fs-caption); }
.task-center__close { margin-left: auto; }
.task-center__body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  overscroll-behavior: contain;
  padding: var(--space-3) var(--space-4) var(--space-4);
}
.task-center__note { margin: 0 0 var(--space-3); color: var(--text-muted); font-size: var(--fs-caption); line-height: 1.6; }
.task-center__warnings {
  margin: 0 0 var(--space-3);
  padding: var(--space-2) var(--space-3) var(--space-2) 26px;
  border: 1px solid var(--warning-border);
  border-radius: var(--radius);
  color: var(--warning-text);
  font-size: var(--fs-secondary);
}
.task-center__section { margin-top: var(--space-3); }
.task-center__section h3 {
  margin-bottom: 6px;
  color: var(--text-secondary);
  font-size: var(--fs-caption);
  font-weight: var(--fw-semibold);
  letter-spacing: 0.04em;
}
.task-center__count { margin-left: 6px; color: var(--text-muted); font-family: var(--font-mono); }
.task-center__empty { color: var(--text-muted); font-size: var(--fs-secondary); }
.task-center__items { list-style: none; margin: 0; padding: 0; display: grid; gap: 6px; }
.task-recent-group h4 { margin: var(--space-2) 0 var(--space-1); color: var(--text-muted); font-size: var(--fs-caption); font-weight: var(--fw-semibold); }
.task-item,
.task-recent {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--border-color);
  border-radius: var(--radius);
  background: var(--control-bg);
  font-size: var(--fs-secondary);
}
.task-item--running { border-color: var(--border-strong); }
.task-item--failed { border-color: var(--warning-border); }
.task-item__main { display: flex; align-items: center; gap: var(--space-2); min-width: 0; }
.task-item__name { font-weight: var(--fw-semibold); min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.task-item__state { flex: none; color: var(--text-secondary); }
.task-item--waiting_idle .task-item__state { color: var(--warning-text); }
.task-item__spacer { flex: 1; }
.task-item__action { flex: none; }
.task-item__progress { display: flex; align-items: center; gap: var(--space-2); margin-top: 6px; }
.task-item__bar { flex: 1; }
.task-item__progress-text { color: var(--text-muted); font-family: var(--font-mono); font-size: var(--fs-caption); }
.task-item__last { margin-top: var(--space-1); color: var(--text-muted); font-size: var(--fs-caption); overflow-wrap: anywhere; }
.task-item__note { margin-top: var(--space-1); color: var(--text-secondary); font-size: var(--fs-caption); line-height: 1.6; }
.task-item__failures { margin-top: var(--space-1); color: var(--text-secondary); font-size: var(--fs-caption); }
.task-item__failures summary { cursor: pointer; color: var(--warning-text); }
.task-item__failures ul { margin: var(--space-1) 0 0; padding-left: 18px; max-height: 160px; overflow-y: auto; }
.task-item__failures li { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.task-operation .task-item__state { color: var(--text-muted); font-variant-numeric: tabular-nums; }
</style>
