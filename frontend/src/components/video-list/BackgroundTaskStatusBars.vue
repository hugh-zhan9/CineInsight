<template>
  <div v-if="technicalBackfill.running || technicalBackfill.completed || technicalBackfill.cancelled || technicalBackfill.failed || technicalWaitingText" :class="['scan-sync-status', barStateClass(technicalBackfill)]" :role="technicalBackfill.failed ? 'alert' : 'status'">
    <span v-if="!technicalBackfill.running && !technicalBackfill.completed && !technicalBackfill.cancelled && !technicalBackfill.failed">技术信息</span>
    <span v-else-if="technicalBackfill.preparing">正在统计待补全视频...</span>
    <span v-else-if="technicalBackfill.completed && technicalBackfill.total === 0 && !technicalBackfill.failed">技术信息无需补全（已是最新状态）。</span>
    <span v-else-if="technicalBackfill.running">技术信息 {{ technicalBackfill.processed }}/{{ technicalBackfill.total }}</span>
    <span v-else>
      技术信息：成功 {{ technicalBackfill.succeeded }}，跳过 {{ technicalBackfill.skipped }}，失败 {{ technicalBackfill.failed }}
      <span v-if="technicalBackfill.cancelled">（已取消）</span>
      <span v-else-if="technicalBackfill.completed">（已完成）</span>
    </span>
    <span v-if="technicalWaitingText" class="status-waiting-idle" data-test="technical-waiting-idle">{{ technicalWaitingText }}</span>
    <button
      v-if="technicalWaitingText"
      type="button"
      class="btn-secondary btn-compact status-run-now"
      data-test="technical-run-now"
      @click="runGatedTaskNow('technical')"
    >忽略空闲立即运行</button>
    <button v-if="technicalBackfill.running" type="button" class="btn-secondary btn-compact status-cancel" @click="cancelTechnicalBackfill">取消</button>
    <TaskFailureList :failures="technicalBackfill.failures || []" key-prefix="technical-" data-test="technical-backfill-failures" />
  </div>

  <div v-if="perceptualHash.running || perceptualHash.completed || perceptualHashWaitingText" :class="['scan-sync-status', barStateClass(perceptualHash)]" :role="perceptualHash.failed ? 'alert' : 'status'">
    <span v-if="perceptualHash.running">近重复指纹 {{ perceptualHash.processed }}/{{ perceptualHash.total }}</span>
    <span v-else-if="!perceptualHash.completed">近重复指纹</span>
    <span v-else>
      近重复指纹：成功 {{ perceptualHash.succeeded }}，跳过 {{ perceptualHash.skipped }}，失败 {{ perceptualHash.failed }}
      <span v-if="perceptualHash.cancelled">（已取消）</span><span v-else-if="perceptualHash.completed">（已完成）</span>
    </span>
    <span v-if="perceptualHashWaitingText" class="status-waiting-idle" data-test="phash-waiting-idle">{{ perceptualHashWaitingText }}</span>
    <button
      v-if="perceptualHashWaitingText"
      type="button"
      class="btn-secondary btn-compact status-run-now"
      data-test="phash-run-now"
      @click="runGatedTaskNow('phash')"
    >忽略空闲立即运行</button>
    <button v-if="perceptualHash.running" type="button" class="btn-secondary btn-compact status-cancel" @click="cancelPerceptualHashBackfill">取消</button>
    <TaskFailureList :failures="perceptualHash.failures || []" key-prefix="phash-" data-test="phash-failures" />
  </div>

  <div v-if="frameHash.running || frameHash.completed || frameHash.cancelled || frameHash.failed || frameHashWaitingText" :class="['scan-sync-status', barStateClass(frameHash)]" :role="frameHash.failed ? 'alert' : 'status'">
    <span v-if="!frameHash.running && !frameHash.completed && !frameHash.cancelled && !frameHash.failed">帧哈希</span>
    <span v-else-if="frameHash.preparing">正在统计待补全帧哈希的视频...</span>
    <span v-else-if="frameHash.completed && frameHash.total === 0 && !frameHash.failed">帧哈希无需补全（已是最新状态）。</span>
    <span v-else-if="frameHash.running">帧哈希 {{ frameHash.processed }}/{{ frameHash.total }}</span>
    <span v-else>
      帧哈希：成功 {{ frameHash.succeeded }}，跳过 {{ frameHash.skipped }}，失败 {{ frameHash.failed }}
      <span v-if="frameHash.cancelled">（已取消）</span><span v-else-if="frameHash.completed">（已完成）</span>
    </span>
    <span v-if="frameHashWaitingText" class="status-waiting-idle" data-test="frame-hash-waiting-idle">{{ frameHashWaitingText }}</span>
    <button
      v-if="frameHashWaitingText"
      type="button"
      class="btn-secondary btn-compact status-run-now"
      data-test="frame-hash-run-now"
      @click="runGatedTaskNow('frame_hash')"
    >忽略空闲立即运行</button>
    <button v-if="frameHash.running" type="button" class="btn-secondary btn-compact status-cancel" @click="cancelFrameHashBackfill">取消</button>
    <TaskFailureList :failures="frameHash.failures || []" key-prefix="frame-hash-" data-test="frame-hash-failures" />
  </div>

  <div v-if="localMetadataBackfill.running || localMetadataBackfill.completed" :class="['scan-sync-status', barStateClass(localMetadataBackfill)]" :role="localMetadataBackfill.failed ? 'alert' : 'status'">
    <span v-if="localMetadataBackfill.running">本地资料 {{ localMetadataBackfill.processed }}/{{ localMetadataBackfill.total }}</span>
    <span v-else>
      本地资料：成功 {{ localMetadataBackfill.succeeded }}，跳过 {{ localMetadataBackfill.skipped }}，失败 {{ localMetadataBackfill.failed }}
      <span v-if="localMetadataBackfill.cancelled">（已取消）</span><span v-else-if="localMetadataBackfill.completed">（已完成）</span>
    </span>
    <button v-if="localMetadataBackfill.running" type="button" class="btn-secondary btn-compact status-cancel" @click="cancelLocalMetadataBackfill">取消</button>
  </div>
	<div v-if="localMetadataExport.running || localMetadataExport.completed" :class="['scan-sync-status', barStateClass(localMetadataExport)]" :role="localMetadataExport.failed ? 'alert' : 'status'">
	  <span v-if="localMetadataExport.running">写出 NFO {{ localMetadataExport.processed }}/{{ localMetadataExport.total }}</span>
	  <span v-else>
	    NFO 写出：成功 {{ localMetadataExport.succeeded }}，失败 {{ localMetadataExport.failed }}
	    <span v-if="localMetadataExport.cancelled">（已取消）</span><span v-else-if="localMetadataExport.completed">（已完成）</span>
	  </span>
	  <button v-if="localMetadataExport.running" type="button" class="btn-secondary btn-compact status-cancel" @click="cancelLocalMetadataExport">取消</button>
	</div>
</template>

<script>
import { StartTechnicalBackfill, GetTechnicalBackfillStatus, CancelTechnicalBackfill, StartPerceptualHashBackfill, GetPerceptualHashBackfillStatus, CancelPerceptualHashBackfill, StartFrameHashBackfill, GetFrameHashBackfillStatus, CancelFrameHashBackfill, StartLocalMetadataBackfill, GetLocalMetadataBackfillStatus, CancelLocalMetadataBackfill, ExportLocalMetadataNFO, StartLocalMetadataExport, GetLocalMetadataExportStatus, CancelLocalMetadataExport, RunGatedTaskNow, GetIdleSchedulerStatus } from '../../../wailsjs/go/main/App';
import { confirmAction, notify, notifyError } from '../../utils/feedback.js';
import { logFrontend } from '../../utils/frontendLog.js';
import { idleGateWaitingText, isIdleGateNotWaitingError } from '../../utils/idleScheduling.js';
import { runtimeEventsMixin } from './runtimeEvents.js';
import TaskFailureList from './TaskFailureList.vue';

// 五条常驻的后台任务状态条：技术信息、近重复指纹、帧哈希、本地资料补全、NFO 写出。
// 启动入口留在「管理」菜单、行菜单与清理面板里，片库页通过 ref 调进来；
// 五份状态再镜像回去，菜单项的进度文案与清理面板的两个补全按钮才有数据可读。
export default {
  name: 'BackgroundTaskStatusBars',
  components: { TaskFailureList },
  mixins: [runtimeEventsMixin],
  emits: ['state-change', 'library-changed'],
  data() {
    return {
      technicalBackfill: { running: false, preparing: false, cancelled: false, completed: false, total: 0, processed: 0, succeeded: 0, skipped: 0, failed: 0, failures: [] },
      perceptualHash: { running: false, cancelled: false, completed: false, total: 0, processed: 0, succeeded: 0, skipped: 0, failed: 0, failures: [] },
      frameHash: { running: false, preparing: false, cancelled: false, completed: false, total: 0, processed: 0, succeeded: 0, skipped: 0, failed: 0, failures: [] },
      localMetadataBackfill: { running: false, cancelled: false, completed: false, total: 0, processed: 0, succeeded: 0, skipped: 0, failed: 0, failures: [] },
	  localMetadataExport: { running: false, cancelled: false, completed: false, total: 0, processed: 0, succeeded: 0, failed: 0, failures: [] },
      // 空闲门里"还没开始就在排队"的任务：任务本身尚未运行，状态条读不到它的
      // gate 字段，只能从门的总览里拿（跑到一半被拦住则读各自的 gate）。
      idleWaitingReasons: {},
    };
  },
  mounted() {
    this.refreshTechnicalBackfillStatus();
    this.refreshPerceptualHashBackfillStatus();
    this.refreshFrameHashBackfillStatus();
    this.refreshLocalMetadataBackfillStatus();
    this.refreshLocalMetadataExportStatus();
    this.refreshIdleSchedulerStatus();

    this.registerRuntimeEvent('idle-scheduler-state', (data) => {
      this.applyIdleSchedulerStatus(data);
    });

    this.registerRuntimeEvent('technical-backfill-state', (data) => {
      this.technicalBackfill = { ...this.technicalBackfill, ...(data || {}) };
    });

    this.registerRuntimeEvent('perceptual-hash-state', (data) => {
      this.perceptualHash = { ...this.perceptualHash, ...(data || {}) };
    });

    this.registerRuntimeEvent('frame-hash-backfill-state', (data) => {
      this.frameHash = { ...this.frameHash, ...(data || {}) };
    });

    this.registerRuntimeEvent('local-metadata-backfill', (data) => {
      this.localMetadataBackfill = { ...this.localMetadataBackfill, ...(data || {}) };
      if (data?.completed && data?.succeeded) this.$emit('library-changed');
    });

    this.registerRuntimeEvent('local-metadata-export', (data) => {
      this.localMetadataExport = { ...this.localMetadataExport, ...(data || {}) };
    });
  },
  watch: {
    taskState: { handler(state) { this.$emit('state-change', state); }, deep: true, immediate: true }
  },
  computed: {
    // 自动触发的那一轮被空闲门挡住时，状态条上直接说明原因并给一条出路。
    // 用户自己点的那一轮不会有 gate.waiting_idle，这两个元素也就不会出现。
    technicalWaitingText() {
      return this.waitingTextFor('technical', this.technicalBackfill.gate);
    },
    perceptualHashWaitingText() {
      return this.waitingTextFor('phash', this.perceptualHash.gate);
    },
    frameHashWaitingText() {
      return this.waitingTextFor('frame_hash', this.frameHash.gate);
    },
    taskState() {
      return {
        technicalBackfill: this.technicalBackfill,
        perceptualHash: this.perceptualHash,
        frameHash: this.frameHash,
        localMetadataBackfill: this.localMetadataBackfill,
        localMetadataExport: this.localMetadataExport
      };
    }
  },
  methods: {
    // 跑着的用强边框，跑完有失败的用警示色；成功不上色，免得五条常驻条一片绿。
    barStateClass(state) {
      return {
        'scan-sync-status--running': Boolean(state?.running),
        'scan-sync-status--warning': !state?.running && Number(state?.failed) > 0,
      };
    },
    debugLog(message, payload = null, isError = false) {
      return logFrontend('VideoListPage', message, payload, isError);
    },
    waitingTextFor(taskKey, gate) {
      const fromGate = idleGateWaitingText(gate);
      if (fromGate) return fromGate;
      const reason = this.idleWaitingReasons[taskKey];
      return reason ? idleGateWaitingText({ waiting_idle: true, reason }) : '';
    },
    applyIdleSchedulerStatus(status) {
      const reasons = {};
      for (const item of status?.waiting || []) {
        if (item?.task_key) reasons[item.task_key] = item.reason;
      }
      this.idleWaitingReasons = reasons;
    },
    async refreshIdleSchedulerStatus() {
      try {
        this.applyIdleSchedulerStatus(await GetIdleSchedulerStatus());
      } catch (err) {
        this.debugLog('idle scheduler status failed', { err: String(err) }, true);
      }
    },
    async runGatedTaskNow(taskKey) {
      try {
        await RunGatedTaskNow(taskKey);
      } catch (err) {
        // 任务刚好已经被放行：没什么可豁免的了，静默刷新即可，不该弹红条。
        if (!isIdleGateNotWaitingError(err)) {
          notifyError('立即运行失败: ' + err);
        }
      }
      await this.refreshIdleSchedulerStatus();
    },
    async refreshTechnicalBackfillStatus() {
      try {
        this.technicalBackfill = { ...this.technicalBackfill, ...(await GetTechnicalBackfillStatus()) };
      } catch (err) {
        this.debugLog('technical backfill status failed', { err: String(err) }, true);
      }
    },
    async startTechnicalBackfill() {
      try {
        this.technicalBackfill = { ...this.technicalBackfill, ...(await StartTechnicalBackfill()) };
      } catch (err) {
        notifyError('启动技术信息补全失败: ' + err);
      }
    },
    async cancelTechnicalBackfill() {
      try {
        await CancelTechnicalBackfill();
        await this.refreshTechnicalBackfillStatus();
      } catch (err) {
        notifyError('取消技术信息补全失败: ' + err);
      }
    },
    async refreshPerceptualHashBackfillStatus() {
      try {
        this.perceptualHash = { ...this.perceptualHash, ...(await GetPerceptualHashBackfillStatus()) };
      } catch (err) {
        this.debugLog('perceptual hash status failed', { err: String(err) }, true);
      }
    },
    async startPerceptualHashBackfill() {
      try {
        this.perceptualHash = { ...this.perceptualHash, ...(await StartPerceptualHashBackfill()) };
      } catch (err) {
        notifyError('启动近重复指纹补全失败: ' + err);
      }
    },
    async cancelPerceptualHashBackfill() {
      try {
        await CancelPerceptualHashBackfill();
        await this.refreshPerceptualHashBackfillStatus();
      } catch (err) {
        notifyError('取消近重复指纹补全失败: ' + err);
      }
    },
    async refreshFrameHashBackfillStatus() {
      try {
        this.frameHash = { ...this.frameHash, ...(await GetFrameHashBackfillStatus()) };
      } catch (err) {
        this.debugLog('frame hash status failed', { err: String(err) }, true);
      }
    },
    async startFrameHashBackfill() {
      try {
        this.frameHash = { ...this.frameHash, ...(await StartFrameHashBackfill()) };
      } catch (err) {
        notifyError('启动帧哈希补全失败: ' + err);
      }
    },
    async cancelFrameHashBackfill() {
      try {
        await CancelFrameHashBackfill();
        await this.refreshFrameHashBackfillStatus();
      } catch (err) {
        notifyError('取消帧哈希补全失败: ' + err);
      }
    },
    async refreshLocalMetadataBackfillStatus() {
      try {
        this.localMetadataBackfill = { ...this.localMetadataBackfill, ...(await GetLocalMetadataBackfillStatus()) };
      } catch (err) {
        this.debugLog('local metadata backfill status failed', { err: String(err) }, true);
      }
    },
    async startLocalMetadataBackfill() {
      try {
        this.localMetadataBackfill = { ...this.localMetadataBackfill, ...(await StartLocalMetadataBackfill()) };
      } catch (err) {
        notifyError('启动本地资料补全失败: ' + err);
      }
    },
    async cancelLocalMetadataBackfill() {
      try {
        await CancelLocalMetadataBackfill();
        await this.refreshLocalMetadataBackfillStatus();
      } catch (err) {
        notifyError('取消本地资料补全失败: ' + err);
      }
    },
	async refreshLocalMetadataExportStatus() {
	  try {
		this.localMetadataExport = { ...this.localMetadataExport, ...(await GetLocalMetadataExportStatus()) };
	  } catch (err) {
		this.debugLog('local metadata export status failed', { err: String(err) }, true);
	  }
	},
	async exportLocalMetadataNFO(video) {
	  if (!video?.id) return;
	  try {
		const result = await ExportLocalMetadataNFO(video.id);
		const warning = Array.isArray(result?.warnings) && result.warnings.length ? `\n${result.warnings.join('\n')}` : '';
		notify(`NFO 已写出：${result?.nfo_path || video.name}${warning}`);
	  } catch (err) {
		notifyError('写出 NFO 失败: ' + err);
	  }
	},
	async startLocalMetadataExport(filter) {
	  if (!await confirmAction({ title: '写出 NFO', message: '将当前筛选结果逐个写出为同名 NFO；已有 NFO 会保留未知字段并合并应用管理字段。继续吗？', confirmText: '开始写出' })) return;
	  try {
		this.localMetadataExport = { ...this.localMetadataExport, ...(await StartLocalMetadataExport({ filter })) };
	  } catch (err) {
		notifyError('启动 NFO 写出失败: ' + err);
	  }
	},
	async cancelLocalMetadataExport() {
	  try {
		await CancelLocalMetadataExport();
		await this.refreshLocalMetadataExportStatus();
	  } catch (err) {
		notifyError('取消 NFO 写出失败: ' + err);
	  }
	},
  }
};
</script>

<style scoped>
.status-cancel { margin-left: 10px; }

.status-waiting-idle { margin-left: 10px; color: var(--warning-color); }

.status-run-now { margin-left: 8px; }

.scan-sync-status {
  margin: 10px 0 0;
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  padding: 8px 10px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius);
  background: var(--control-bg);
  color: var(--text-secondary);
  font-size: 13px;
}

.scan-sync-status--running {
  border-color: var(--border-strong);
}

.scan-sync-status--success {
  border-color: var(--accent-border);
  color: var(--accent-color);
}

.scan-sync-status--warning {
  border-color: var(--warning-border);
  color: var(--warning-color);
}

.scan-sync-status--error {
  border-color: var(--danger-border);
  color: var(--danger-color);
}
.btn-compact {
  height: 28px;
  padding: 0 10px;
  font-size: 12px;
}
</style>
