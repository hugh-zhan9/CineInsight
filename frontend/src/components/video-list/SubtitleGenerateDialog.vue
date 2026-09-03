<template>
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
  <BaseModal v-if="subtitleDialog.show" class="download-modal">
      <h3>{{ subtitleDialog.title }}</h3>
      <p>{{ subtitleDialog.msg }}</p>

      <!-- 引擎与语言选择 (确认生成时显示) -->
      <div v-if="subtitleDialog.mode === 'confirm'" class="lang-select-box">
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
          <button v-else @click="subtitleDialog.show = false" class="btn-secondary">后台继续准备</button>
        </div>
      </template>

      <!-- 确认按钮 -->
      <div v-if="subtitleDialog.mode === 'confirm'" class="modal-actions">
        <button @click="subtitleDialog.show = false; pendingForceRequest = null; pendingSubtitleVideo = null;" class="btn-secondary">取消</button>
        <button @click="onSubtitleConfirm" class="btn-primary" :disabled="subtitleConfirmDisabled">{{ subtitleConfirmActionLabel }}</button>
      </div>

      <!-- 结果关闭按钮 -->
      <div v-if="subtitleDialog.mode === 'result'" class="modal-actions">
        <button @click="subtitleDialog.show = false" class="btn-primary">确定</button>
      </div>
  </BaseModal>
</template>

<script>
import { GetSubtitleEngineStatuses, PrepareSubtitleEngine, GenerateSubtitle, ForceGenerateSubtitle, CancelSubtitle, CancelSubtitleTask, GetSubtitleQueueState } from '../../../wailsjs/go/main/App';
import BaseModal from '../ui/BaseModal.vue';
import { notifyError } from '../../utils/feedback.js';
import { logFrontend } from '../../utils/frontendLog.js';
import { runtimeEventsMixin } from './runtimeEvents.js';
import { formatElapsedDuration } from './format.js';

// 字幕生成：常驻的任务队列面板 + 确认/进度/结果三态弹窗。
// 片库页通过 ref 调用 generate(video) 打开确认；行菜单要知道哪些视频正在生成，
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
      this.registerRuntimeEvent('subtitle-progress', (data) => {
        const nextAction = data?.action || '';
        if (nextAction === 'generate' && !this.acceptSubtitleTaskEvent(data)) return;
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
        if (this.subtitleDialog.show && this.subtitleDialog.progressAction === 'prepare') {
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
        const warnings = Array.isArray(data.warnings) && data.warnings.length > 0 ? `\n\n注意：\n${data.warnings.join('\n')}` : '';
        this.subtitleDialog.msg = '文件: ' + data.path + warnings;
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
      if (this.pendingForceRequest) return '强制生成';
      if (this.selectedSubtitleEngineStatus?.needs_prepare) return '准备组件';
      return '开始生成';
    },
    subtitleConfirmDisabled() {
      const status = this.selectedSubtitleEngineStatus;
      if (!status) return true;
      if (!status.supported) return true;
      if (!status.available && !status.needs_prepare) return true;
      return false;
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
    applySubtitleQueueState(snapshot) {
      const next = snapshot || {};
      this.subtitleQueue = {
        active_task: next.active_task || null,
        queued_tasks: Array.isArray(next.queued_tasks) ? next.queued_tasks : [],
        total: Number(next.total || 0)
      };
      const ids = [];
      if (this.subtitleQueue.active_task?.video_id) ids.push(this.subtitleQueue.active_task.video_id);
      for (const task of this.subtitleQueue.queued_tasks) {
        if (task.video_id) ids.push(task.video_id);
      }
      this.generatingSubtitleIds = Array.from(new Set(ids));
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
      this.subtitleDialog.title = '准备生成字幕';
      this.subtitleDialog.msg = `我们将使用 ${status.display_name} 为您生成本地字幕，这可能需要几分钟。`;
      this.subtitleDialog.requiresPrepare = false;
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
		if (this.subtitleProgressVideoID && this.subtitleProgressVideoID !== video.id) return;
		if (this.consumeMinimizedSubtitleTask()) return;
      if (result.status === 'validation_failed' && result.force_eligible) {
        this.subtitleDialog.show = true;
        this.subtitleDialog.mode = 'confirm';
        this.subtitleDialog.title = '⚠️ 字幕质量警告';
        this.subtitleDialog.msg = `${result.message}\n\n是否强制生成，保留当前结果？`;
        this.pendingForceRequest = {
          video,
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
      const warningText = Array.isArray(result.warnings) && result.warnings.length > 0
        ? `\n\n注意：\n${result.warnings.join('\n')}`
        : '';
      this.subtitleDialog.msg = (forceMode ? '字幕文件已保存到视频同目录下（已确认保留上次校验结果）。' : `字幕文件已保存到视频同目录下。\n${result.path || ''}`) + warningText;
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
    async generateSubtitle(video) {
      console.log('[Subtitle] generateSubtitle called for video:', video.id);
      if (this.generatingSubtitleIds.includes(video.id)) return;

      try {
        await this.loadSubtitleEngineStatuses();
        this.pendingSubtitleVideo = video;
        this.pendingForceRequest = null;
        this.subtitleDialog.show = true;
		this.subtitleProgressTaskID = null;
		this.subtitleProgressVideoID = video.id;
        this.subtitleDialog.mode = 'confirm';
        this.refreshSubtitleConfirmCopy();
      } catch (err) {
        console.error('[Subtitle] Error:', err);
        this.subtitleDialog.show = true;
        this.subtitleDialog.mode = 'result';
        this.subtitleDialog.title = '❌ 检查依赖失败';
        this.subtitleDialog.msg = String(err);
      }
    },
    async onSubtitleConfirm() {
      // 场景一：用户确认强制生成字幕（跳过幻觉检测）
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
				this.subtitleDialog.msg = String(err);
			}
        }
        return;
      }

      // 场景二：用户确认准备依赖
      if (this.subtitleDialog.requiresPrepare) {
        this.resetSubtitleProgressTracking();
        this.subtitleDialog.mode = 'progress';
        this.subtitleDialog.progressAction = 'prepare';
        this.subtitleDialog.phase = 'preparing-runtime';
        this.subtitleDialog.title = '正在准备组件';
        this.subtitleDialog.percent = 0;
        this.subtitleDialog.msg = '准备中... 可关闭此窗口，后台会继续。';
        try {
          await PrepareSubtitleEngine(this.selectedSubtitleEngine);
          await this.loadSubtitleEngineStatuses();
          if (this.subtitleDialog.show) {
            this.subtitleDialog.mode = 'result';
            this.subtitleDialog.title = '✅ 组件准备完成';
            this.subtitleDialog.msg = '现在可以点击字幕按钮生成字幕了。';
          }
        } catch (err) {
          this.subtitleDialog.mode = 'result';
          this.subtitleDialog.title = '❌ 组件准备失败';
          this.subtitleDialog.msg = String(err);
        }
        this.pendingSubtitleVideo = null;
        return;
      }

      // 场景三：依赖已就绪，开始生成
      if (this.pendingSubtitleVideo) {
        const video = this.pendingSubtitleVideo;
        this.pendingSubtitleVideo = null;
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
			this.subtitleDialog.msg = String(err);
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
.lang-select-box {
  margin-top: 15px;
}
.dialog-field-label {
  display: block;
  margin-bottom: 8px;
  color: var(--text-secondary);
  font-size: 13px;
}
.dialog-field-label--spaced {
  margin-top: 12px;
}
.dialog-select {
  height: 36px;
  padding: 0 10px;
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
