<template>
  <!-- 上次退出时没跑完的字幕任务（D-PC20、MEDIA-10）：启动时标成「已中断」，这里给出重新排队与忽略。 -->
  <div v-if="interruptedJobs.count > 0" class="subtitle-interrupted-bar glass-surface" role="status" data-test="subtitle-interrupted-bar">
    <span class="subtitle-interrupted-bar__text">上次退出应用时有 {{ interruptedJobs.count }} 个字幕任务没有完成。</span>
    <button type="button" class="btn-primary btn-compact" :disabled="!!interruptedBusy" data-test="subtitle-interrupted-requeue" @click="requeueInterruptedJobs">
      {{ interruptedBusy === 'requeue' ? '正在重新排队…' : '全部重新排队' }}
    </button>
    <button type="button" class="btn-secondary btn-compact" :disabled="!!interruptedBusy" data-test="subtitle-interrupted-dismiss" @click="dismissInterruptedJobs">忽略</button>
    <button type="button" class="btn-secondary btn-compact" data-test="subtitle-interrupted-task-center" @click="openTaskCenter">在任务中心查看</button>
  </div>

  <div v-if="subtitleQueue.total > 0" class="subtitle-queue-panel glass-surface">
    <div class="subtitle-queue-heading">
      <strong>字幕任务队列（{{ subtitleQueue.total }}）</strong>
      <button type="button" class="btn-secondary" @click="refreshSubtitleQueue">刷新</button>
    </div>
    <div v-if="subtitleQueue.active_task" class="subtitle-queue-task subtitle-queue-task--active">
      <span class="subtitle-queue-status">处理中</span>
      <span class="subtitle-queue-name">{{ subtitleQueue.active_task.video_name || `视频 #${subtitleQueue.active_task.video_id}` }}</span>
      <button v-if="subtitleQueue.active_task.can_cancel" type="button" class="btn-danger btn-compact" :disabled="cancellingSubtitleTaskIds.includes(subtitleQueue.active_task.task_id)" @click="cancelSubtitleTask(subtitleQueue.active_task.task_id)">取消</button>
    </div>
    <div v-for="task in subtitleQueue.queued_tasks" :key="task.task_id" class="subtitle-queue-task">
      <span class="subtitle-queue-status">排队中 #{{ task.position }}</span>
      <span class="subtitle-queue-name">{{ task.video_name || `视频 #${task.video_id}` }}</span>
      <button v-if="task.can_cancel" type="button" class="btn-secondary btn-compact" :disabled="cancellingSubtitleTaskIds.includes(task.task_id)" @click="cancelSubtitleTask(task.task_id)">取消</button>
    </div>
  </div>

  <!-- 字幕操作弹窗（确认/进度/结果） -->
  <BaseModal v-if="subtitleDialog.show" class="download-modal" data-test="subtitle-generate-dialog" @close="handleDialogClose">
      <h3>{{ subtitleDialog.title }}</h3>
      <p class="subtitle-dialog-msg" data-test="subtitle-dialog-msg">{{ subtitleDialog.msg }}</p>

      <!-- 覆盖提示（D-PC13、MEDIA-01）：已有同名字幕时说清会先备份；同名共用字幕的其他视频一并列出。 -->
      <div v-if="overwriteNotice" class="subtitle-overwrite-notice" data-test="subtitle-overwrite-notice">
        <p>{{ overwriteNotice }}</p>
        <template v-if="overwriteSharedNames.length">
          <p>{{ overwriteSharedLead }}</p>
          <ul data-test="subtitle-overwrite-shared">
            <li v-for="name in overwriteSharedNames" :key="name">{{ name }}</li>
          </ul>
        </template>
      </div>

      <!-- 引擎与语言选择 (确认生成时显示) -->
      <div v-if="subtitleDialog.mode === 'confirm' && !pendingForceRequest" class="lang-select-box">
        <label class="dialog-field-label">字幕引擎</label>
        <select v-model="selectedSubtitleEngine" @change="refreshSubtitleConfirmCopy" class="search-input dialog-select">
          <option v-for="status in subtitleEngineStatuses" :key="status.engine" :value="status.engine" :disabled="!status.supported">
            {{ status.display_name }}{{ !status.supported ? '（当前平台不可用）' : '' }}
          </option>
        </select>
        <p v-if="selectedSubtitleEngineStatus?.reason_message" class="dialog-field-hint">{{ selectedSubtitleEngineStatus.reason_message }}</p>

        <template v-if="subtitleSourceLangVisible">
          <label class="dialog-field-label dialog-field-label--spaced">识别源语言</label>
          <select v-model="sourceLang" class="search-input dialog-select">
            <option v-for="opt in languageOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
          </select>
          <p class="dialog-field-hint">如果自动检测不准，请手动指定视频中的语言。</p>
        </template>
      </div>

      <!-- 下载进度条 -->
      <template v-if="subtitleDialog.mode === 'progress'">
        <div class="progress-bar-container">
          <div class="progress-bar" :style="{ width: subtitleDialog.percent + '%' }"></div>
        </div>
        <p class="progress-text">{{ subtitleDialog.percent }}%</p>
        <p v-if="subtitleDialog.progressAction === 'generate'" class="progress-meta">
          当前阶段：{{ subtitleProgressPhaseLabel }}
          <span v-if="subtitleElapsedText"> · 已运行 {{ subtitleElapsedText }}</span>
        </p>
        <p v-if="subtitleDialog.progressAction === 'generate'" class="progress-hint">
          {{ subtitleProgressHint }}
        </p>
        <div class="modal-actions">
          <button v-if="subtitleDialog.progressAction === 'generate'" @click="minimizeSubtitleProgress" class="btn-secondary">后台继续</button>
          <button v-if="subtitleDialog.progressAction === 'generate'" @click="cancelSubtitle" class="btn-danger">取消生成</button>
          <template v-else>
            <button type="button" class="btn-secondary" data-test="subtitle-prepare-minimize" @click="minimizePrepare">后台继续准备</button>
            <!-- 引擎准备可取消（D-PC22、MEDIA-13）：与设置页「字幕」分区的「取消准备」是同一个入口。 -->
            <button type="button" class="btn-danger" :disabled="prepareState.cancelling" data-test="subtitle-prepare-cancel" @click="cancelPrepare">
              {{ prepareState.cancelling ? '正在取消…' : '取消准备' }}
            </button>
          </template>
        </div>
      </template>

      <!-- 待确认的结果（D-PC13）：原字幕没动，强制生成才替换；放弃即删除临时结果。 -->
      <div v-if="subtitleDialog.mode === 'confirm' && pendingForceRequest" class="modal-actions">
        <button v-if="!pendingForceRequest.retained" type="button" class="btn-secondary" data-test="subtitle-confirm-cancel" @click="cancelConfirm">取消</button>
        <button v-if="pendingForceRequest.retained" type="button" class="btn-secondary" data-test="subtitle-pending-later" @click="deferPendingDecision">稍后处理</button>
        <button v-if="pendingForceRequest.retained" type="button" class="btn-secondary" :disabled="discardingPending" data-test="subtitle-pending-discard" @click="discardPendingResult">
          {{ discardingPending ? '正在放弃…' : '放弃这次结果' }}
        </button>
        <button type="button" class="btn-primary" data-test="subtitle-confirm" @click="onSubtitleConfirm">{{ subtitleConfirmActionLabel }}</button>
      </div>

      <!-- 确认按钮 -->
      <div v-else-if="subtitleDialog.mode === 'confirm'" class="modal-actions">
        <button type="button" class="btn-secondary" data-test="subtitle-confirm-cancel" @click="cancelConfirm">取消</button>
        <button type="button" class="btn-primary" data-test="subtitle-confirm" :disabled="subtitleConfirmDisabled" @click="onSubtitleConfirm">{{ subtitleConfirmActionLabel }}</button>
      </div>

      <!-- 结果关闭按钮 -->
      <div v-if="subtitleDialog.mode === 'result'" class="modal-actions">
        <button @click="subtitleDialog.show = false" class="btn-primary" data-test="subtitle-result-close">确定</button>
      </div>
  </BaseModal>
</template>

<script>
import {
  CancelSubtitle, CancelSubtitleEnginePreparation, CancelSubtitleTask, DiscardPendingSubtitle, DismissInterruptedSubtitleJobs,
  ForceGenerateSubtitle, GenerateSubtitle, GetInterruptedSubtitleJobs, GetSubtitleEngineStatuses, GetSubtitleOverwriteInfo,
  GetSubtitleQueueState, PrepareSubtitleEngine, RequeueInterruptedSubtitleJobs
} from '../../../wailsjs/go/main/App';
import BaseModal from '../ui/BaseModal.vue';
import { notify, notifyError } from '../../utils/feedback.js';
import { logFrontend } from '../../utils/frontendLog.js';
import { findCommand } from '../../utils/commandRegistry.js';
import { fileBaseName } from '../../utils/pathText.js';
import { SUBTITLE_BACKUP_NOTE, subtitleExceptionText } from '../../utils/subtitleTools.js';
import { runtimeEventsMixin } from './runtimeEvents.js';
import { formatElapsedDuration } from './format.js';

// 写回同名 .srt 失败（D-PC13、D-PC20）：字幕已生成并通过校验，临时结果保留，「强制生成」即重试写回。
const REPLACE_FAILED = 'subtitle_replace_failed';
// PrepareSubtitleEngine 被取消时返回的就是这句（services.ErrSubtitleEnginePreparationCancelled）：不是失败。
const PREPARE_CANCELLED_TEXT = '已取消字幕引擎准备';

function idleBatch() {
  return { pendingIds: [], succeeded: 0, needsConfirmation: 0, failed: 0, cancelled: 0 };
}

// 字幕生成：常驻的任务队列面板 + 中断任务提示条 + 确认/进度/结果三态弹窗。
// 片库页通过 ref 调用 generate(video) / generateBatch(videos) 打开确认；行菜单要知道哪些视频正在生成，
// 所以把 generatingSubtitleIds 镜像回去。
export default {
  name: 'SubtitleGenerateDialog',
  components: { BaseModal },
  mixins: [runtimeEventsMixin],
  emits: ['generating-change'],
  data() {
    return {
      // Subtitle states
      generatingSubtitleIds: [],
      subtitleDialog: { show: false, mode: 'confirm', title: '', msg: '', percent: 0, progressAction: '', phase: '', requiresPrepare: false },
      subtitleEngineStatuses: [],
      selectedSubtitleEngine: 'whisperx',
      pendingSubtitleVideo: null,
      pendingForceRequest: null,
      discardingPending: false,
      // 生成前查的覆盖信息（D-PC13）：{ exists, shared_with[{id,name}] }；查不到时为 { unknown: true }。
      overwriteInfo: null,
      // 批量生成的确认与在途计数（D-PC23、MEDIA-14）：批量的任务不抢占弹窗，全部结束后汇总提示一次。
      batchConfirm: null,
      batch: idleBatch(),
      // 引擎准备（D-PC22、MEDIA-13）：只对本弹窗发起的准备弹进度；「后台继续准备」后不再被进度事件弹回。
      prepareState: { owned: false, minimized: false, cancelling: false },
      interruptedJobs: { count: 0, job_ids: [] },
      interruptedBusy: '',
      // 队列快照里见过的任务名：subtitle-failed 事件只带编号，提示里要说是哪部片子。
      taskNames: {},
      subtitleProgressStartedAt: 0,
      subtitleProgressNow: Date.now(),
      subtitleProgressTimer: null,
      subtitleProgressTaskID: null,
      subtitleProgressVideoID: null,
      minimizedSubtitleTaskIds: [],
      subtitleQueue: { active_task: null, queued_tasks: [], total: 0 },
      cancellingSubtitleTaskIds: [],
      sourceLang: 'auto',
      languageOptions: [
        { label: '自动检测', value: 'auto' },
        { label: '中文 (Chinese)', value: 'chinese' },
        { label: '英语 (English)', value: 'english' },
        { label: '日语 (Japanese)', value: 'japanese' },
        { label: '韩语 (Korean)', value: 'korean' },
        { label: '德语 (German)', value: 'german' },
        { label: '法语 (French)', value: 'french' },
        { label: '西班牙语 (Spanish)', value: 'spanish' }
      ],
    };
  },
  mounted() {
    this.refreshSubtitleQueue();
    this.loadInterruptedJobs();
      this.registerRuntimeEvent('subtitle-progress', (data) => {
        const nextAction = data?.action || '';
        if (nextAction === 'generate' && !this.acceptSubtitleTaskEvent(data)) return;
        if (nextAction !== 'generate') {
          // 设置页发起的准备、或已「后台继续准备」时不弹窗；取消的结果由 PrepareSubtitleEngine 的返回收尾。
          if (!this.prepareState.owned || this.prepareState.minimized || data?.phase === 'cancelled') return;
        }
        if (nextAction === 'generate') {
          this.startSubtitleProgressTracking();
        } else {
          this.resetSubtitleProgressTracking();
        }

        this.subtitleDialog.show = true;
        this.subtitleDialog.mode = 'progress';
        this.subtitleDialog.progressAction = nextAction;
        this.subtitleDialog.phase = data.phase || '';
        this.subtitleDialog.title = nextAction === 'generate' ? '正在生成字幕' : '正在准备组件';
        this.subtitleDialog.percent = data.percent;
        this.subtitleDialog.msg = data.message || '';
      });

      this.registerRuntimeEvent('subtitle-prepare-complete', async () => {
        await this.loadSubtitleEngineStatuses();
        if (this.prepareState.owned && !this.prepareState.minimized && this.subtitleDialog.show && this.subtitleDialog.progressAction === 'prepare') {
          this.subtitleDialog.mode = 'result';
          this.subtitleDialog.title = '✅ 组件准备完成';
          this.subtitleDialog.msg = '当前引擎已就绪，现在可以开始生成字幕。';
        }
      });

      this.registerRuntimeEvent('subtitle-success', (data) => {
        const idx = this.generatingSubtitleIds.indexOf(data.videoID);
        if (idx !== -1) this.generatingSubtitleIds.splice(idx, 1);
		this.refreshSubtitleQueue();
		if (!this.acceptSubtitleTaskCompletion(data)) return;
		this.resetSubtitleProgressTracking();
        this.subtitleDialog.show = true;
        this.subtitleDialog.mode = 'result';
        this.subtitleDialog.title = '✅ 字幕生成成功';
        this.subtitleDialog.msg = this.successMessage(data.path, data.warnings, data.backup_id);
      });

      this.registerRuntimeEvent('subtitle-cancelled', (data) => {
        const idx = this.generatingSubtitleIds.indexOf(data.videoID);
        if (idx !== -1) this.generatingSubtitleIds.splice(idx, 1);
		this.refreshSubtitleQueue();
		if (!this.acceptSubtitleTaskCompletion(data)) return;
		this.resetSubtitleProgressTracking();
        this.subtitleDialog.show = true;
        this.subtitleDialog.mode = 'result';
        this.subtitleDialog.title = '⏹️ 已取消字幕生成';
        this.subtitleDialog.msg = data.message || '当前字幕任务已取消。';
      });

      // 队列里的任务失败（D-PC20、MEDIA-04）：后台、排队或已「后台继续」的任务也要让人知道，并指向任务中心。
      this.registerRuntimeEvent('subtitle-failed', (data) => this.handleSubtitleFailed(data));

      this.registerRuntimeEvent('subtitle-queue', (data) => {
        this.applySubtitleQueueState(data);
      });
  },
  beforeUnmount() {
    this.resetSubtitleProgressTracking();
  },
  watch: {
    generatingSubtitleIds: {
      handler(ids) { this.$emit('generating-change', [...ids]); },
      deep: true,
      immediate: true
    }
  },
  computed: {
    selectedSubtitleEngineStatus() {
      return this.subtitleEngineStatuses.find(status => status.engine === this.selectedSubtitleEngine) || null;
    },
    subtitleSourceLangVisible() {
      return !!this.selectedSubtitleEngineStatus && this.selectedSubtitleEngineStatus.source_lang_mode !== 'ignored';
    },
    subtitleConfirmActionLabel() {
      if (this.pendingForceRequest) return this.pendingForceRequest.errorCode === REPLACE_FAILED ? '重试写回' : '强制生成';
      if (this.selectedSubtitleEngineStatus?.needs_prepare) return '准备组件';
      if (this.batchConfirm) return `加入队列（${this.batchConfirm.videos.length}）`;
      return '开始生成';
    },
    subtitleConfirmDisabled() {
      const status = this.selectedSubtitleEngineStatus;
      if (!status) return true;
      if (!status.supported) return true;
      if (!status.available && !status.needs_prepare) return true;
      return false;
    },
    // 只在「准备生成」这一步说覆盖；准备组件、引擎不可用时不说。
    confirmingGeneration() {
      const status = this.selectedSubtitleEngineStatus;
      return this.subtitleDialog.mode === 'confirm' && !this.pendingForceRequest && !!status
        && status.supported && status.available && !status.needs_prepare;
    },
    overwriteNotice() {
      if (!this.confirmingGeneration) return '';
      if (this.batchConfirm) return '已有同名字幕的视频会被覆盖（会先备份，可恢复）。';
      const info = this.overwriteInfo;
      if (!info) return '';
      if (info.unknown) return '如果已有同名字幕，会先备份再覆盖（可恢复）。';
      if (info.exists) return '将覆盖现有字幕（会先备份，可恢复）。';
      return this.overwriteSharedNames.length ? '生成的字幕会保存为同名 .srt。' : '';
    },
    overwriteSharedNames() {
      if (this.batchConfirm) return [];
      const shared = Array.isArray(this.overwriteInfo?.shared_with) ? this.overwriteInfo.shared_with : [];
      return shared.map(video => video?.name || `视频 #${video?.id}`);
    },
    overwriteSharedLead() {
      return this.overwriteInfo?.exists
        ? '同目录下这些视频与它共用这份字幕，也会一起被覆盖：'
        : '同目录下这些同名视频也会用上这份字幕：';
    },
    subtitleProgressPhaseLabel() {
      const phase = this.subtitleDialog.phase || '';
      const engineName = this.selectedSubtitleEngineStatus?.display_name || '当前引擎';
      switch (phase) {
        case 'preparing-runtime': return '准备运行时';
        case 'downloading-model': return '下载模型';
        case 'extracting-audio': return '提取音频';
        case 'transcribing': return `${engineName} 音频转写`;
        case 'normalizing': return '整理转写结果';
        case 'validating': return '字幕质量校验';
        case 'translating': return '双语翻译';
        case 'merging': return '双语字幕合并';
        case 'finalizing': return '完成收尾';
        default: return '初始化任务';
      }
    },
    subtitleElapsedText() {
      if (!this.subtitleProgressStartedAt) return '';
      return this.formatElapsedDuration(this.subtitleProgressNow - this.subtitleProgressStartedAt);
    },
    subtitleProgressHint() {
      if (this.subtitleDialog.progressAction !== 'generate') {
        return '';
      }

      const phase = this.subtitleProgressPhaseLabel;
      if (phase.includes('音频转写')) {
        return `当前正在进行 ${phase}。长视频或 CPU 模式下停留较久是正常现象，不代表任务假死。`;
      }
      if (phase === '提取音频' || phase === '初始化任务' || phase === '准备运行时' || phase === '下载模型') {
        return '字幕任务已经启动，完成音频准备后会自动进入转写阶段。';
      }
      if (phase === '字幕质量校验' || phase === '双语翻译' || phase === '双语字幕合并') {
        return '转写已经完成，当前正在做结果校验或双语处理，通常会继续向后推进。';
      }
      return '任务仍在继续处理，请等待当前阶段完成。';
    }
  },
  methods: {
    generate(video) {
      return this.generateSubtitle(video);
    },
    formatElapsedDuration,
    debugLog(message, payload = null, isError = false) {
      return logFrontend('VideoListPage', message, payload, isError);
    },
    videoLabel(video) {
      return video?.display_title || video?.name || `视频 #${video?.id}`;
    },
    // 成功文案只说文件名，不说绝对路径（G-3）；覆盖了旧字幕时说明已备份（MEDIA-05）。
    successMessage(path, warnings, backupID) {
      const name = fileBaseName(path);
      const lines = [name ? `字幕已保存为「${name}」（视频同目录）。` : '字幕文件已保存到视频同目录下。'];
      if (backupID) lines.push(SUBTITLE_BACKUP_NOTE);
      if (Array.isArray(warnings) && warnings.length > 0) lines.push(`\n注意：\n${warnings.join('\n')}`);
      return lines.join('\n');
    },
    applySubtitleQueueState(snapshot) {
      const next = snapshot || {};
      this.subtitleQueue = {
        active_task: next.active_task || null,
        queued_tasks: Array.isArray(next.queued_tasks) ? next.queued_tasks : [],
        total: Number(next.total || 0)
      };
      const ids = [];
      const names = { ...this.taskNames };
      for (const task of [this.subtitleQueue.active_task, ...this.subtitleQueue.queued_tasks].filter(Boolean)) {
        if (task.video_id) ids.push(task.video_id);
        if (task.task_id && task.video_name) names[task.task_id] = task.video_name;
      }
      this.taskNames = names;
      // 批量里还没进队列快照的视频也算「正在生成」，行菜单不让重复发起。
      this.generatingSubtitleIds = Array.from(new Set([...ids, ...this.batch.pendingIds]));
		if (!this.subtitleProgressTaskID && this.subtitleProgressVideoID) {
			const tasks = [this.subtitleQueue.active_task, ...this.subtitleQueue.queued_tasks].filter(Boolean);
			const task = tasks.find(item => item.video_id === this.subtitleProgressVideoID);
			if (task) this.subtitleProgressTaskID = task.task_id;
		}
    },
    acceptSubtitleTaskEvent(data) {
		const taskID = Number(data?.taskID || 0);
		const videoID = Number(data?.videoID || 0);
		if (taskID && this.minimizedSubtitleTaskIds.includes(taskID)) return false;
		// 批量排进去的任务在后台跑，不抢占弹窗（D-PC23）。
		if (videoID && this.batch.pendingIds.includes(videoID)) return false;
		if (this.subtitleProgressTaskID && taskID && this.subtitleProgressTaskID !== taskID) return false;
		if (this.subtitleProgressVideoID && videoID && this.subtitleProgressVideoID !== videoID) return false;
		if (taskID) this.subtitleProgressTaskID = taskID;
		if (videoID) this.subtitleProgressVideoID = videoID;
		return true;
	},
	acceptSubtitleTaskCompletion(data) {
		const taskID = Number(data?.taskID || 0);
		if (taskID && this.minimizedSubtitleTaskIds.includes(taskID)) {
			return false;
		}
		return this.acceptSubtitleTaskEvent(data);
	},
	consumeMinimizedSubtitleTask() {
		if (!this.subtitleProgressTaskID || !this.minimizedSubtitleTaskIds.includes(this.subtitleProgressTaskID)) return false;
		this.minimizedSubtitleTaskIds = this.minimizedSubtitleTaskIds.filter(id => id !== this.subtitleProgressTaskID);
		return true;
	},
    // 弹窗此刻正在显示这个任务（进度或结果）：它的失败由弹窗自己报，不再另发提示。
    isForegroundSubtitleTask(jobID, videoID) {
      if (!this.subtitleDialog.show) return false;
      if (jobID && this.minimizedSubtitleTaskIds.includes(jobID)) return false;
      if (jobID && this.subtitleProgressTaskID) return this.subtitleProgressTaskID === jobID;
      return !!videoID && this.subtitleProgressVideoID === videoID;
    },
    handleSubtitleFailed(data) {
      const jobID = Number(data?.job_id || 0);
      const videoID = Number(data?.video_id || 0);
      // 批量的失败在全部结束后汇总一次，不逐条弹。
      if (videoID && this.batch.pendingIds.includes(videoID)) return;
      if (this.isForegroundSubtitleTask(jobID, videoID)) return;
      const name = this.taskNames[jobID] || (videoID ? `视频 #${videoID}` : '字幕任务');
      if (data?.error_code === REPLACE_FAILED) {
        notifyError(`「${name}」的字幕已生成，但写回同名 .srt 失败，原字幕没有改动。临时结果已保留，可在任务中心「强制生成」重试写回。`);
        return;
      }
      const reason = String(data?.message || '').trim();
      notifyError(`「${name}」的字幕生成失败${reason ? `：${reason}` : ''}。可在任务中心查看。`);
    },
    async refreshSubtitleQueue() {
      try {
        this.applySubtitleQueueState(await GetSubtitleQueueState());
      } catch (err) {
        this.debugLog('refresh subtitle queue failed', { error: String(err) }, true);
      }
    },
    async cancelSubtitleTask(taskID) {
      if (!taskID || this.cancellingSubtitleTaskIds.includes(taskID)) return;
      this.cancellingSubtitleTaskIds.push(taskID);
      try {
        await CancelSubtitleTask(taskID);
        await this.refreshSubtitleQueue();
      } catch (err) {
        notifyError('取消字幕任务失败: ' + err);
      } finally {
        this.cancellingSubtitleTaskIds = this.cancellingSubtitleTaskIds.filter(id => id !== taskID);
      }
    },
    // ===== 上次中断的字幕任务（D-PC20）=====
    async loadInterruptedJobs() {
      try {
        const summary = await GetInterruptedSubtitleJobs();
        const jobIDs = Array.isArray(summary?.job_ids) ? summary.job_ids.map(Number).filter(Boolean) : [];
        this.interruptedJobs = { count: Number(summary?.count || jobIDs.length || 0), job_ids: jobIDs };
      } catch (err) {
        this.debugLog('load interrupted subtitle jobs failed', { error: String(err) }, true);
      }
    },
    async requeueInterruptedJobs() {
      if (this.interruptedBusy) return;
      const total = this.interruptedJobs.count;
      this.interruptedBusy = 'requeue';
      // 重新排队的任务在后台跑，与「后台继续」一样不抢占生成弹窗；失败照常经 subtitle-failed 提示。
      for (const id of this.interruptedJobs.job_ids) {
        if (!this.minimizedSubtitleTaskIds.includes(id)) this.minimizedSubtitleTaskIds.push(id);
      }
      try {
        const requeued = Number(await RequeueInterruptedSubtitleJobs() || 0);
        const missed = Math.max(0, total - requeued);
        if (missed > 0) {
          notifyError(`已重新排队 ${requeued} 个字幕任务；${missed} 个没能重新排队（例如视频已删除），原因见任务中心。`);
        } else {
          notify(`已重新排队 ${requeued} 个字幕任务。`);
        }
        await this.refreshSubtitleQueue();
      } catch (err) {
        notifyError('重新排队失败：' + subtitleExceptionText(err));
      } finally {
        this.interruptedBusy = '';
        await this.loadInterruptedJobs();
      }
    },
    async dismissInterruptedJobs() {
      if (this.interruptedBusy) return;
      this.interruptedBusy = 'dismiss';
      try {
        await DismissInterruptedSubtitleJobs();
        this.interruptedJobs = { count: 0, job_ids: [] };
        notify('已忽略这些中断的字幕任务，之后仍可在任务中心单独重试。');
      } catch (err) {
        notifyError('忽略失败：' + subtitleExceptionText(err));
      } finally {
        this.interruptedBusy = '';
      }
    },
    openTaskCenter() {
      const command = findCommand('action:task-center');
      if (command) {
        command.run();
        return;
      }
      notify('请点顶栏的「任务中心」查看字幕任务。');
    },
    async loadSubtitleEngineStatuses() {
      const statuses = await GetSubtitleEngineStatuses();
      this.subtitleEngineStatuses = Array.isArray(statuses) ? statuses : [];
      const current = this.subtitleEngineStatuses.find(status => status.engine === this.selectedSubtitleEngine && status.supported);
      if (current) return;
      const preferred = this.subtitleEngineStatuses.find(status => status.engine === 'whisperx' && status.supported)
        || this.subtitleEngineStatuses.find(status => status.supported)
        || this.subtitleEngineStatuses[0];
      this.selectedSubtitleEngine = preferred?.engine || 'whisperx';
    },
    refreshSubtitleConfirmCopy() {
      const status = this.selectedSubtitleEngineStatus;
      if (!status) return;
      if (!status.supported) {
        this.subtitleDialog.title = '当前引擎不可用';
        this.subtitleDialog.msg = status.reason_message || '当前平台暂不支持该字幕引擎。';
        this.subtitleDialog.requiresPrepare = false;
        return;
      }
      if (status.needs_prepare) {
        this.subtitleDialog.title = '需要准备组件';
        this.subtitleDialog.msg = status.prepare_hint || `${status.display_name} 需要先准备运行时组件。`;
        this.subtitleDialog.requiresPrepare = true;
        return;
      }
      if (!status.available) {
        this.subtitleDialog.title = '缺少前置条件';
        this.subtitleDialog.msg = status.reason_message || `${status.display_name} 当前还不可用。`;
        this.subtitleDialog.requiresPrepare = false;
        return;
      }
      this.subtitleDialog.requiresPrepare = false;
      if (this.batchConfirm) {
        const { videos, skipped } = this.batchConfirm;
        this.subtitleDialog.title = '批量生成字幕';
        this.subtitleDialog.msg = `将用 ${status.display_name} 为选中的 ${videos.length} 个视频生成字幕，按顺序排进字幕队列逐个处理，期间可以继续做别的事。`
          + (skipped > 0 ? `另有 ${skipped} 个已在字幕队列里，已跳过。` : '');
        return;
      }
      this.subtitleDialog.title = '准备生成字幕';
      this.subtitleDialog.msg = `我们将使用 ${status.display_name} 为您生成本地字幕，这可能需要几分钟。`;
    },
    buildSubtitleRequest(video) {
      return {
        video_id: video.id,
        engine: this.selectedSubtitleEngine,
        source_lang: this.subtitleSourceLangVisible ? this.sourceLang : 'auto',
      };
    },
    async handleSubtitleGenerateResult(result, video, forceMode = false) {
      if (!result) return;
		const current = !this.subtitleProgressVideoID || this.subtitleProgressVideoID === video.id;
		// 不是弹窗正在跟的那个任务，或已「后台继续」：结果不弹窗，但待确认的要让人知道（MEDIA-04）。
		if (!current || this.consumeMinimizedSubtitleTask()) {
			this.reportBackgroundResult(result, video);
			return;
		}
      if (result.status === 'validation_failed' && result.force_eligible) {
        const replaceFailed = result.error_code === REPLACE_FAILED;
        // pending_retained：临时结果还在磁盘上，强制生成 / 重试写回直接复用，也才有「放弃」可言。
        const retained = result.pending_retained !== false;
        this.subtitleDialog.show = true;
        this.subtitleDialog.mode = 'confirm';
        if (replaceFailed) {
          this.subtitleDialog.title = '⚠️ 字幕写回失败';
          this.subtitleDialog.msg = `字幕已生成并通过校验，但写回同名 .srt 时失败，原字幕没有改动。${result.message ? `\n${result.message}` : ''}\n\n`
            + (retained ? '临时结果已保留：「重试写回」不会重新识别；「放弃这次结果」会删除它。' : '临时结果没能保留，重试会重新识别。');
        } else {
          this.subtitleDialog.title = '⚠️ 字幕质量警告';
          this.subtitleDialog.msg = `${result.message || '字幕没有通过质量校验。'}\n\n原字幕没有改动，`
            + (retained ? '这次的结果暂存为待确认。「强制生成」会用它替换现有字幕（会先备份）；「放弃这次结果」会删除它。' : '这次的结果没有保留，「强制生成」会重新识别并跳过质量检测。');
        }
        this.overwriteInfo = null;
        this.pendingForceRequest = {
          video,
          retained,
          errorCode: result.error_code || '',
          request: {
            video_id: video.id,
            engine: result.engine,
            source_lang: result.source_lang || 'auto',
          },
        };
        return;
      }
      this.pendingForceRequest = null;
      this.subtitleDialog.show = true;
      this.subtitleDialog.mode = 'result';
      if (result.status === 'cancelled') {
        this.subtitleDialog.title = '⏹️ 已取消字幕生成';
        this.subtitleDialog.msg = result.message || '当前字幕任务已取消。';
        return;
      }
      this.subtitleDialog.title = '✅ 字幕生成完成';
      this.subtitleDialog.msg = forceMode
        ? `已用确认后的结果替换字幕。\n${this.successMessage(result.path, result.warnings, '')}`
        : this.successMessage(result.path, result.warnings, '');
    },
    // 后台任务的结果：写回失败另有 subtitle-failed 事件提示，这里只补幻觉待确认这一种（它不算失败）。
    reportBackgroundResult(result, video) {
      if (result.status !== 'validation_failed' || result.error_code === REPLACE_FAILED) return;
      notify(`「${this.videoLabel(video)}」的字幕没有通过质量校验，原字幕没有改动；结果暂存为待确认，可在任务中心「强制生成」或「放弃」。`);
    },
    startSubtitleProgressTracking() {
      if (!this.subtitleProgressStartedAt) {
        this.subtitleProgressStartedAt = Date.now();
      }
      this.subtitleProgressNow = Date.now();
      if (this.subtitleProgressTimer) {
        return;
      }
      this.subtitleProgressTimer = window.setInterval(() => {
        this.subtitleProgressNow = Date.now();
      }, 1000);
    },
    resetSubtitleProgressTracking() {
      if (this.subtitleProgressTimer) {
        clearInterval(this.subtitleProgressTimer);
        this.subtitleProgressTimer = null;
      }
      this.subtitleProgressStartedAt = 0;
      this.subtitleProgressNow = Date.now();
    },
    minimizeSubtitleProgress() {
		let taskID = this.subtitleProgressTaskID;
		if (!taskID && this.subtitleProgressVideoID) {
			const tasks = [this.subtitleQueue.active_task, ...this.subtitleQueue.queued_tasks].filter(Boolean);
			taskID = tasks.find(task => task.video_id === this.subtitleProgressVideoID)?.task_id || null;
		}
		if (taskID && !this.minimizedSubtitleTaskIds.includes(taskID)) {
			this.minimizedSubtitleTaskIds.push(taskID);
		}
      this.subtitleDialog.show = false;
    },
    // 「后台继续准备」：之后的进度事件不再把弹窗弹回来，结果改用提示条告知（MEDIA-13）。
    minimizePrepare() {
      if (this.prepareState.owned) this.prepareState = { ...this.prepareState, minimized: true };
      this.subtitleDialog.show = false;
    },
    async cancelPrepare() {
      if (this.prepareState.cancelling) return;
      this.prepareState = { ...this.prepareState, cancelling: true };
      try {
        await CancelSubtitleEnginePreparation();
      } catch (err) {
        this.prepareState = { ...this.prepareState, cancelling: false };
        notifyError('取消准备失败：' + subtitleExceptionText(err));
      }
    },
    // Esc 与弹窗的关闭：各状态下等同于那一步的「退出」按钮，不丢任务。
    handleDialogClose() {
      const mode = this.subtitleDialog.mode;
      if (mode === 'progress') {
        if (this.subtitleDialog.progressAction === 'generate') this.minimizeSubtitleProgress();
        else this.minimizePrepare();
        return;
      }
      if (mode === 'confirm') {
        if (this.pendingForceRequest?.retained) this.deferPendingDecision();
        else this.cancelConfirm();
        return;
      }
      this.subtitleDialog.show = false;
    },
    cancelConfirm() {
      this.subtitleDialog.show = false;
      this.pendingForceRequest = null;
      this.pendingSubtitleVideo = null;
      this.batchConfirm = null;
      this.overwriteInfo = null;
    },
    // 待确认的结果先不处理：临时结果留着，任务中心里仍可「强制生成」或「放弃」。
    deferPendingDecision() {
      this.subtitleDialog.show = false;
      this.pendingForceRequest = null;
      notify('这次的字幕结果暂存为待确认，可稍后在任务中心「强制生成」或「放弃」。');
    },
    async discardPendingResult() {
      const pending = this.pendingForceRequest;
      if (!pending || this.discardingPending) return;
      this.discardingPending = true;
      try {
        await DiscardPendingSubtitle(pending.video.id);
        this.pendingForceRequest = null;
        this.subtitleDialog.show = false;
        notify('已放弃这次的字幕结果，原字幕没有改动。');
      } catch (err) {
        notifyError('放弃失败：' + subtitleExceptionText(err));
      } finally {
        this.discardingPending = false;
      }
    },
    async loadOverwriteInfo(video) {
      this.overwriteInfo = null;
      try {
        const info = await GetSubtitleOverwriteInfo(video.id);
        if (this.pendingSubtitleVideo?.id !== video.id) return;
        this.overwriteInfo = info
          ? { exists: !!info.exists, shared_with: Array.isArray(info.shared_with) ? info.shared_with : [] }
          : { unknown: true };
      } catch (err) {
        if (this.pendingSubtitleVideo?.id === video.id) this.overwriteInfo = { unknown: true };
      }
    },
    async generateSubtitle(video) {
      console.log('[Subtitle] generateSubtitle called for video:', video.id);
      if (this.generatingSubtitleIds.includes(video.id)) return;

      try {
        await this.loadSubtitleEngineStatuses();
        this.pendingSubtitleVideo = video;
        this.pendingForceRequest = null;
        this.batchConfirm = null;
        this.subtitleDialog.show = true;
		this.subtitleProgressTaskID = null;
		this.subtitleProgressVideoID = video.id;
        this.subtitleDialog.mode = 'confirm';
        this.refreshSubtitleConfirmCopy();
        await this.loadOverwriteInfo(video);
      } catch (err) {
        console.error('[Subtitle] Error:', err);
        this.subtitleDialog.show = true;
        this.subtitleDialog.mode = 'result';
        this.subtitleDialog.title = '❌ 检查依赖失败';
        this.subtitleDialog.msg = subtitleExceptionText(err);
      }
    },
    // 批量生成字幕（D-PC23、MEDIA-14）：确认一次，逐个走既有的入队方法，全部结束后汇总提示。
    async generateBatch(videos) {
      const seen = new Set();
      const candidates = (videos || []).filter(video => {
        const id = Number(video?.id);
        if (!id || seen.has(id)) return false;
        seen.add(id);
        return true;
      });
      const list = candidates.filter(video => !this.generatingSubtitleIds.includes(video.id));
      if (list.length === 0) {
        notify(candidates.length ? '选中的视频都已在字幕队列里。' : '请先选择要生成字幕的视频。');
        return;
      }
      try {
        await this.loadSubtitleEngineStatuses();
      } catch (err) {
        notifyError('检查字幕引擎失败：' + subtitleExceptionText(err));
        return;
      }
      this.pendingSubtitleVideo = null;
      this.pendingForceRequest = null;
      this.overwriteInfo = null;
      this.batchConfirm = { videos: list, skipped: candidates.length - list.length };
      this.subtitleDialog.show = true;
      this.subtitleDialog.mode = 'confirm';
      this.refreshSubtitleConfirmCopy();
    },
    enqueueBatch(videos) {
      for (const video of videos) {
        const id = Number(video.id);
        if (!this.batch.pendingIds.includes(id)) this.batch.pendingIds.push(id);
        if (!this.generatingSubtitleIds.includes(id)) this.generatingSubtitleIds.push(id);
        GenerateSubtitle(this.buildSubtitleRequest(video))
          .then(result => this.settleBatchItem(id, result, null))
          .catch(err => this.settleBatchItem(id, null, err));
      }
      notify(`已把 ${videos.length} 个视频加入字幕队列，按顺序逐个生成；进度见字幕任务队列，结束后会汇总提示。`);
    },
    settleBatchItem(videoID, result, err) {
      const batch = this.batch;
      batch.pendingIds = batch.pendingIds.filter(id => id !== videoID);
      this.generatingSubtitleIds = this.generatingSubtitleIds.filter(id => id !== videoID);
      if (err || !result) batch.failed += 1;
      else if (result.status === 'success') batch.succeeded += 1;
      else if (result.status === 'validation_failed') batch.needsConfirmation += 1;
      else if (result.status === 'cancelled') batch.cancelled += 1;
      else batch.failed += 1;
      if (batch.pendingIds.length > 0) return;
      const parts = [`成功 ${batch.succeeded} 个`];
      if (batch.needsConfirmation) parts.push(`待确认 ${batch.needsConfirmation} 个`);
      if (batch.failed) parts.push(`失败 ${batch.failed} 个`);
      if (batch.cancelled) parts.push(`已取消 ${batch.cancelled} 个`);
      const text = `批量生成字幕结束：${parts.join('，')}。${batch.needsConfirmation || batch.failed ? '待确认与失败的任务可在任务中心处理。' : ''}`;
      if (batch.failed) notifyError(text);
      else notify(text);
      this.batch = idleBatch();
    },
    async onSubtitleConfirm() {
      // 场景一：用户确认强制生成字幕（跳过幻觉检测），或写回失败后重试写回
      if (this.pendingForceRequest) {
        const { video, request } = this.pendingForceRequest;
        this.pendingForceRequest = null;
		this.subtitleProgressTaskID = null;
		this.subtitleProgressVideoID = video.id;
        this.subtitleDialog.mode = 'progress';
        this.subtitleDialog.progressAction = 'generate';
        this.subtitleDialog.phase = 'validating';
        this.subtitleDialog.title = '正在强制生成字幕';
        this.subtitleDialog.percent = 0;
        this.subtitleDialog.msg = '跳过质量检测，重新生成...';
        this.startSubtitleProgressTracking();
        this.generatingSubtitleIds.push(video.id);
        try {
          const result = await ForceGenerateSubtitle(request);
          this.resetSubtitleProgressTracking();
          const idx = this.generatingSubtitleIds.indexOf(video.id);
          if (idx !== -1) this.generatingSubtitleIds.splice(idx, 1);
          await this.handleSubtitleGenerateResult(result, video, true);
        } catch (err) {
          this.resetSubtitleProgressTracking();
          const idx = this.generatingSubtitleIds.indexOf(video.id);
          if (idx !== -1) this.generatingSubtitleIds.splice(idx, 1);
			if (!this.consumeMinimizedSubtitleTask() && (!this.subtitleProgressVideoID || this.subtitleProgressVideoID === video.id)) {
				this.subtitleDialog.mode = 'result';
				this.subtitleDialog.title = '❌ 强制生成失败';
				this.subtitleDialog.msg = subtitleExceptionText(err);
			}
        }
        return;
      }

      // 场景二：用户确认准备依赖
      if (this.subtitleDialog.requiresPrepare) {
        this.resetSubtitleProgressTracking();
        this.prepareState = { owned: true, minimized: false, cancelling: false };
        this.subtitleDialog.mode = 'progress';
        this.subtitleDialog.progressAction = 'prepare';
        this.subtitleDialog.phase = 'preparing-runtime';
        this.subtitleDialog.title = '正在准备组件';
        this.subtitleDialog.percent = 0;
        this.subtitleDialog.msg = '准备中... 可关闭此窗口，后台会继续。';
        this.pendingSubtitleVideo = null;
        this.batchConfirm = null;
        this.overwriteInfo = null;
        try {
          await PrepareSubtitleEngine(this.selectedSubtitleEngine);
          await this.loadSubtitleEngineStatuses();
          if (this.subtitleDialog.show && !this.prepareState.minimized) {
            this.subtitleDialog.mode = 'result';
            this.subtitleDialog.title = '✅ 组件准备完成';
            this.subtitleDialog.msg = '现在可以点击字幕按钮生成字幕了。';
          } else {
            notify('字幕组件已准备好，现在可以生成字幕了。');
          }
        } catch (err) {
          const cancelled = String(err?.message || err).includes(PREPARE_CANCELLED_TEXT);
          if (this.subtitleDialog.show && !this.prepareState.minimized) {
            this.subtitleDialog.mode = 'result';
            this.subtitleDialog.title = cancelled ? '⏹️ 已取消准备组件' : '❌ 组件准备失败';
            this.subtitleDialog.msg = cancelled ? '已取消准备，可以随时重新开始。' : subtitleExceptionText(err);
          } else if (!cancelled) {
            // 「后台继续准备」之后失败：弹窗已关，改用提示条告知（MEDIA-13）。
            notifyError('字幕组件准备失败：' + subtitleExceptionText(err));
          }
        } finally {
          this.prepareState = { owned: false, minimized: false, cancelling: false };
        }
        return;
      }

      // 场景三：批量生成，确认后逐个入队，弹窗关掉（D-PC23）
      if (this.batchConfirm) {
        const { videos } = this.batchConfirm;
        this.batchConfirm = null;
        this.subtitleDialog.show = false;
        this.enqueueBatch(videos);
        return;
      }

      // 场景四：依赖已就绪，开始生成
      if (this.pendingSubtitleVideo) {
        const video = this.pendingSubtitleVideo;
        this.pendingSubtitleVideo = null;
        this.overwriteInfo = null;
		this.subtitleProgressTaskID = null;
		this.subtitleProgressVideoID = video.id;
        this.subtitleDialog.show = true;
        this.subtitleDialog.mode = 'progress';
        this.subtitleDialog.progressAction = 'generate';
        this.subtitleDialog.phase = 'checking';
        this.subtitleDialog.title = '正在生成字幕';
        this.subtitleDialog.percent = 0;
        this.subtitleDialog.msg = `任务已启动，正在准备 ${this.selectedSubtitleEngineStatus?.display_name || '当前引擎'}...`;
        this.startSubtitleProgressTracking();
        await this.doGenerateSubtitle(video);
      }
    },
    async doGenerateSubtitle(video) {
      this.generatingSubtitleIds.push(video.id);
      try {
        this.subtitleDialog.progressAction = 'generate';
        const result = await GenerateSubtitle(this.buildSubtitleRequest(video));
        this.resetSubtitleProgressTracking();
        // 成功后移除 ID（event 也会移除，双重保障）
        const idx = this.generatingSubtitleIds.indexOf(video.id);
        if (idx !== -1) this.generatingSubtitleIds.splice(idx, 1);
        await this.handleSubtitleGenerateResult(result, video, false);
      } catch (err) {
        console.error('[Subtitle] Generate error:', err);
        this.resetSubtitleProgressTracking();
        const idx = this.generatingSubtitleIds.indexOf(video.id);
        if (idx !== -1) this.generatingSubtitleIds.splice(idx, 1);
		if (!this.consumeMinimizedSubtitleTask() && (!this.subtitleProgressVideoID || this.subtitleProgressVideoID === video.id)) {
			this.subtitleDialog.show = true;
			this.subtitleDialog.mode = 'result';
			this.subtitleDialog.title = '❌ 生成字幕失败';
			this.subtitleDialog.msg = subtitleExceptionText(err);
		}
      }
    },
    async cancelSubtitle() {
      try {
		if (this.subtitleProgressTaskID) {
			await CancelSubtitleTask(this.subtitleProgressTaskID);
		} else {
			await CancelSubtitle();
		}
        this.resetSubtitleProgressTracking();
        this.subtitleDialog.show = false;
		await this.refreshSubtitleQueue();
      } catch (err) {
        console.error('取消失败:', err);
      }
    },
  }
};
</script>

<style scoped>
:deep(.download-modal) {
  width: 400px;
  text-align: center;
  padding: 30px;
}
.subtitle-dialog-msg {
  white-space: pre-line;
}
.subtitle-overwrite-notice {
  margin-top: 12px;
  padding: 8px 10px;
  border: 1px solid var(--warning-border);
  border-radius: var(--radius);
  color: var(--warning-color);
  font-size: 12px;
  text-align: left;
}
.subtitle-overwrite-notice p {
  margin: 0 0 4px;
}
.subtitle-overwrite-notice ul {
  margin: 0;
  padding-left: 18px;
  max-height: 120px;
  overflow-y: auto;
}
.subtitle-interrupted-bar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin: 12px 0;
  padding: 10px 16px;
  font-size: 13px;
}
.subtitle-interrupted-bar__text {
  flex: 1 1 auto;
  min-width: 0;
}
.lang-select-box {
  margin-top: 15px;
}
.dialog-field-label {
  display: block;
  margin-bottom: 8px;
  text-align: left;
  color: var(--text-secondary);
  font-size: 13px;
}
.dialog-field-label--spaced {
  margin-top: 12px;
}
.dialog-select {
  height: var(--h-unit);
  padding: 0 10px;
  text-align: left;
}
.dialog-field-hint {
  margin-top: 5px;
  color: var(--text-muted);
  font-size: 11px;
}
.progress-bar-container {
  width: 100%;
  height: 10px;
  background-color: var(--review-progress-track-bg);
  border-radius: 5px;
  margin: 20px 0;
  overflow: hidden;
}
.progress-bar {
  height: 100%;
  background-color: var(--success-bright);
  transition: width 0.3s ease;
}
.progress-text {
  font-size: 0.9em;
  color: var(--review-text-muted);
  margin: 0;
}
.progress-meta {
  font-size: 13px;
  color: var(--review-text-meta);
  margin: 10px 0 0;
}
.progress-hint {
  font-size: 12px;
  line-height: 1.6;
  color: var(--review-text-muted);
  margin: 8px 0 0;
}
.subtitle-queue-panel {
  margin: 12px 0;
  padding: 12px 16px;
}
.subtitle-queue-heading,
.subtitle-queue-task {
  display: flex;
  align-items: center;
  gap: 10px;
}
.subtitle-queue-heading {
  justify-content: space-between;
  margin-bottom: 8px;
}
.subtitle-queue-task {
  min-height: 32px;
  border-top: 1px solid var(--neutral-soft);
  font-size: 13px;
}
.subtitle-queue-task--active {
  padding: 0 8px;
  border-radius: var(--radius);
  background: var(--review-accent-soft);
}
.subtitle-queue-task--active .subtitle-queue-status {
  font-weight: 700;
}
.subtitle-queue-status {
  flex: 0 0 70px;
  color: var(--accent-deep);
  font-size: 12px;
}
.subtitle-queue-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.btn-compact {
  height: 28px;
  padding: 0 10px;
  font-size: 12px;
}
</style>
