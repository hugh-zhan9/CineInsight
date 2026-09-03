<template>
  <div class="image-ai-tasks">
    <div class="setting-item image-ai-tagging-controls">
      <label>图片 AI 打标</label>
      <p class="help-text">
        让 AI 从上方配置的标签库里为图片挑标签，复用同一份 AI 接口配置。产出的是<strong>待审候选</strong>，
        要在照片页「AI 标签审阅」里接受之后才会真正挂到图片上。标签库为空时整批都会被跳过。
      </p>
      <div class="image-task-status" :class="{ 'image-task-status--error': Boolean(taggingError) }" data-test="image-ai-tagging-status">
        <strong>{{ taggingStatusText }}</strong>
        <span v-if="taggingStatus?.running || taggingStatus?.completed || taggingStatus?.cancelled">
          进度 {{ taggingStatus.processed || 0 }}/{{ taggingStatus.total || 0 }}
          · 成功 {{ taggingStatus.succeeded || 0 }}
          · 跳过 {{ taggingStatus.skipped || 0 }}
          · 失败 {{ taggingStatus.failed || 0 }}
          · 产出候选 {{ taggingStatus.candidates || 0 }}
        </span>
        <div v-if="taggingProgressRatio !== null" class="image-task-bar" role="progressbar" :aria-valuenow="taggingStatus.processed || 0" :aria-valuemin="0" :aria-valuemax="taggingStatus.total || 0">
          <i :style="{ width: taggingProgressRatio + '%' }"></i>
        </div>
        <span v-if="taggingWaitingText" class="image-task-waiting-idle" data-test="image-ai-tagging-waiting-idle">{{ taggingWaitingText }}</span>
        <span v-if="taggingError" data-test="image-ai-tagging-error">{{ taggingError }}</span>
        <ul v-if="taggingFailures.length" class="image-task-failures" data-test="image-ai-tagging-failures">
          <li v-for="failure in taggingFailures" :key="`tag-${failure.image_id}`">
            {{ failure.name || `图片 ${failure.image_id}` }}（{{ failure.code || '未知错误' }}）
          </li>
        </ul>
      </div>
      <div class="image-task-actions">
        <button
          type="button"
          class="btn-primary"
          :disabled="taggingStatus?.running"
          data-test="image-ai-tagging-start"
          @click="startTagging"
        >开始/继续生成</button>
        <button
          v-if="taggingWaitingText"
          type="button"
          class="btn-secondary"
          data-test="image-ai-tagging-run-now"
          @click="runGatedTaskNow('image_ai_tagging')"
        >忽略空闲立即运行</button>
        <button
          v-if="taggingStatus?.running"
          type="button"
          class="btn-secondary"
          data-test="image-ai-tagging-cancel"
          @click="cancelTagging"
        >取消</button>
      </div>
    </div>

    <div class="setting-item image-exif-backfill-controls">
      <label>图片 EXIF 补全</label>
      <p class="help-text">
        为还没解析过 EXIF 的历史图片补全拍摄时间、相机参数与 GPS。新扫描入库的图片会在入库时自动解析，这里只补历史存量。
      </p>
      <div class="image-task-status" :class="{ 'image-task-status--error': Boolean(exifError) }" data-test="image-exif-backfill-status">
        <strong>{{ exifStatusText }}</strong>
        <span v-if="exifStatus?.running || exifStatus?.completed || exifStatus?.cancelled">
          进度 {{ exifStatus.processed || 0 }}/{{ exifStatus.total || 0 }}
          · 有 EXIF {{ exifStatus.succeeded || 0 }}
          · 无 EXIF {{ exifStatus.skipped || 0 }}
          · 失败 {{ exifStatus.failed || 0 }}
        </span>
        <div v-if="exifProgressRatio !== null" class="image-task-bar" role="progressbar" :aria-valuenow="exifStatus.processed || 0" :aria-valuemin="0" :aria-valuemax="exifStatus.total || 0">
          <i :style="{ width: exifProgressRatio + '%' }"></i>
        </div>
        <span v-if="exifWaitingText" class="image-task-waiting-idle" data-test="image-exif-backfill-waiting-idle">{{ exifWaitingText }}</span>
        <span v-if="exifError" data-test="image-exif-backfill-error">{{ exifError }}</span>
        <ul v-if="exifFailures.length" class="image-task-failures" data-test="image-exif-backfill-failures">
          <li v-for="failure in exifFailures" :key="`exif-${failure.image_id}`">
            {{ failure.name || `图片 ${failure.image_id}` }}（{{ failure.error || '未知错误' }}）
          </li>
        </ul>
      </div>
      <div class="image-task-actions">
        <button
          type="button"
          class="btn-primary"
          :disabled="exifStatus?.running"
          data-test="image-exif-backfill-start"
          @click="startEXIFBackfill"
        >开始/继续补全</button>
        <button
          v-if="exifWaitingText"
          type="button"
          class="btn-secondary"
          data-test="image-exif-backfill-run-now"
          @click="runGatedTaskNow('exif')"
        >忽略空闲立即运行</button>
        <button
          v-if="exifStatus?.running"
          type="button"
          class="btn-secondary"
          data-test="image-exif-backfill-cancel"
          @click="cancelEXIFBackfill"
        >取消</button>
      </div>
    </div>

    <div class="setting-item image-semantic-index-controls">
      <label>图片语义索引</label>
      <p class="help-text">
        索引文本 = 文件名 + 标签 + AI 描述，与视频语义索引共享同一个 embedding 模型与代次；同一时间只允许一个语义索引任务运行。
      </p>
      <div class="image-task-status" :class="{ 'image-task-status--error': semanticStatus && !semanticStatus.available }" data-test="image-semantic-index-status">
        <strong>{{ semanticStatusText }}</strong>
        <span v-if="semanticStatus?.model">
          模型 {{ semanticStatus.model }}<template v-if="semanticStatus.dimension"> · {{ semanticStatus.dimension }} 维</template>
        </span>
        <span v-if="semanticStatus?.running || semanticStatus?.completed || semanticStatus?.cancelled">
          进度 {{ semanticStatus.processed || 0 }}/{{ semanticStatus.total || 0 }}
          · 成功 {{ semanticStatus.succeeded || 0 }}
          · 跳过 {{ semanticStatus.skipped || 0 }}
          · 失败 {{ semanticStatus.failed || 0 }}
        </span>
        <div v-if="semanticProgressRatio !== null" class="image-task-bar" role="progressbar" :aria-valuenow="semanticStatus.processed || 0" :aria-valuemin="0" :aria-valuemax="semanticStatus.total || 0">
          <i :style="{ width: semanticProgressRatio + '%' }"></i>
        </div>
        <span v-if="semanticStatus?.needs_rebuild" data-test="image-semantic-rebuild-hint">
          共享的 embedding 模型或维度已变化，图片索引已过期，需要重新运行本任务。
        </span>
        <span v-if="semanticStatus?.unavailable">{{ semanticStatus.unavailable }}</span>
        <span v-if="semanticError" data-test="image-semantic-index-error">{{ semanticError }}</span>
        <ul v-if="semanticFailures.length" class="image-task-failures" data-test="image-semantic-index-failures">
          <li v-for="failure in semanticFailures" :key="`sem-${failure.image_id}`">
            {{ failure.name || `图片 ${failure.image_id}` }}（{{ failure.code || '未知错误' }}）
          </li>
        </ul>
      </div>
      <div class="image-task-actions">
        <button
          type="button"
          class="btn-primary"
          :disabled="semanticStatus?.running || !semanticStatus?.available"
          data-test="image-semantic-index-start"
          @click="startSemanticIndex"
        >开始/继续构建</button>
        <button
          v-if="semanticStatus?.running"
          type="button"
          class="btn-secondary"
          data-test="image-semantic-index-cancel"
          @click="cancelSemanticIndex"
        >取消</button>
      </div>
    </div>
  </div>
</template>

<script>
import {
  CancelImageAITagging, CancelImageEXIFBackfill, CancelImageSemanticIndex,
  GetIdleSchedulerStatus, GetImageAITaggingStatus, GetImageEXIFBackfillStatus, GetImageSemanticIndexStatus,
  RunGatedTaskNow, StartImageAITagging, StartImageEXIFBackfill, StartImageSemanticIndex
} from '../../wailsjs/go/main/App';
import { idleGateWaitingText, isIdleGateNotWaitingError } from '../utils/idleScheduling.js';

// 事件推送之外保留 1s 轮询兜底（镜像 PhotoCleanupPage）。
const POLL_INTERVAL_MS = 1000;
const MAX_FAILURES_SHOWN = 5;

function progressRatio(status) {
  if (!status || !status.total) return null;
  if (!status.running && !status.completed && !status.cancelled) return null;
  return Math.min(100, Math.round(((status.processed || 0) / status.total) * 100));
}

export default {
  name: 'PhotoAITaskPanel',
  data() {
    return {
      taggingStatus: null,
      semanticStatus: null,
      exifStatus: null,
      taggingError: '',
      semanticError: '',
      exifError: '',
      taggingOff: null,
      semanticOff: null,
      exifOff: null,
      // 空闲门里"还没开始就在排队"的任务：任务本身尚未运行，状态里读不到 gate 字段，
      // 只能从门的总览里拿（跑到一半被拦住则读各自的 gate）。
      idleWaitingReasons: {},
      idleOff: null
    };
  },
  computed: {
    taggingStatusText() {
      const status = this.taggingStatus;
      if (!status) return '正在读取图片打标任务状态...';
      if (status.running) return '图片打标进行中';
      if (status.cancelled) return '图片打标任务已取消，可继续';
      if (status.completed) return '图片打标任务已完成';
      return '图片打标任务未运行';
    },
    semanticStatusText() {
      const status = this.semanticStatus;
      if (!status) return '正在读取图片语义索引状态...';
      if (!status.available) return '图片语义索引不可用';
      if (status.running) return '图片语义索引构建中';
      if (status.needs_rebuild) return '模型或维度已变化，需要重新构建';
      if (status.cancelled) return '图片语义索引构建已取消，可继续';
      if (status.completed) return '图片语义索引构建完成';
      return '图片语义索引可用，尚未构建';
    },
    exifStatusText() {
      const status = this.exifStatus;
      if (!status) return '正在读取 EXIF 补全任务状态...';
      if (status.running) return 'EXIF 补全中';
      if (status.cancelled) return 'EXIF 补全任务已取消，可继续';
      if (status.completed) return 'EXIF 补全任务已完成';
      return 'EXIF 补全任务未运行';
    },
    // 自动触发的那一轮被空闲门挡住时，面板上直接说明原因并给一条出路；
    // 用户自己点「开始」的那一轮不会有 gate.waiting_idle，这两个元素也就不出现。
    taggingWaitingText() { return this.waitingTextFor('image_ai_tagging', this.taggingStatus?.gate); },
    exifWaitingText() { return this.waitingTextFor('exif', this.exifStatus?.gate); },
    taggingProgressRatio() { return progressRatio(this.taggingStatus); },
    semanticProgressRatio() { return progressRatio(this.semanticStatus); },
    exifProgressRatio() { return progressRatio(this.exifStatus); },
    taggingFailures() { return (this.taggingStatus?.failures || []).slice(0, MAX_FAILURES_SHOWN); },
    semanticFailures() { return (this.semanticStatus?.failures || []).slice(0, MAX_FAILURES_SHOWN); },
    exifFailures() { return (this.exifStatus?.failures || []).slice(0, MAX_FAILURES_SHOWN); }
  },
  mounted() {
    this._alive = true;
    this.loadTaggingStatus();
    this.loadSemanticStatus();
    this.loadEXIFStatus();
    this.loadIdleSchedulerStatus();
    if (window.runtime?.EventsOn) {
      const idleOff = window.runtime.EventsOn('idle-scheduler-state', status => {
        this.applyIdleSchedulerStatus(status);
      });
      if (typeof idleOff === 'function') this.idleOff = idleOff;
      const taggingOff = window.runtime.EventsOn('image-ai-tagging-progress', status => {
        this.taggingStatus = { ...(this.taggingStatus || {}), ...(status || {}) };
      });
      if (typeof taggingOff === 'function') this.taggingOff = taggingOff;
      const semanticOff = window.runtime.EventsOn('image-semantic-index-state', status => {
        this.semanticStatus = { ...(this.semanticStatus || {}), ...(status || {}) };
      });
      if (typeof semanticOff === 'function') this.semanticOff = semanticOff;
      const exifOff = window.runtime.EventsOn('image-exif-backfill-progress', status => {
        this.exifStatus = { ...(this.exifStatus || {}), ...(status || {}) };
      });
      if (typeof exifOff === 'function') this.exifOff = exifOff;
    }
  },
  beforeUnmount() {
    this._alive = false;
    clearTimeout(this._pollTimer);
    this.taggingOff?.();
    this.semanticOff?.();
    this.exifOff?.();
    this.idleOff?.();
  },
  methods: {
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
    async loadIdleSchedulerStatus() {
      try {
        this.applyIdleSchedulerStatus(await GetIdleSchedulerStatus());
      } catch {
        this.idleWaitingReasons = {};
      }
    },
    async runGatedTaskNow(taskKey) {
      try {
        await RunGatedTaskNow(taskKey);
      } catch (err) {
        // 任务刚好已经被放行：没什么可豁免的了，静默刷新即可，不该报错。
        if (!isIdleGateNotWaitingError(err)) {
          const message = `忽略空闲立即运行失败：${String(err?.message || err)}`;
          if (taskKey === 'exif') this.exifError = message;
          else this.taggingError = message;
        }
      }
      await this.loadIdleSchedulerStatus();
    },
    schedulePoll() {
      clearTimeout(this._pollTimer);
      if (!this._alive) return;
      if (!this.taggingStatus?.running && !this.semanticStatus?.running && !this.exifStatus?.running) return;
      this._pollTimer = setTimeout(async () => {
        if (!this._alive) return;
        if (this.taggingStatus?.running) await this.loadTaggingStatus();
        if (this.semanticStatus?.running) await this.loadSemanticStatus();
        if (this.exifStatus?.running) await this.loadEXIFStatus();
        this.schedulePoll();
      }, POLL_INTERVAL_MS);
    },
    async loadTaggingStatus() {
      try {
        this.taggingStatus = await GetImageAITaggingStatus() || null;
      } catch (err) {
        this.taggingError = `读取图片打标任务状态失败：${String(err?.message || err)}`;
        return;
      }
      this.schedulePoll();
    },
    async loadSemanticStatus() {
      try {
        this.semanticStatus = await GetImageSemanticIndexStatus() || null;
      } catch (err) {
        this.semanticStatus = { available: false, unavailable: String(err?.message || err) };
        return;
      }
      this.schedulePoll();
    },
    async loadEXIFStatus() {
      try {
        this.exifStatus = await GetImageEXIFBackfillStatus() || null;
      } catch (err) {
        this.exifError = `读取 EXIF 补全任务状态失败：${String(err?.message || err)}`;
        return;
      }
      this.schedulePoll();
    },
    async startEXIFBackfill() {
      if (this.exifStatus?.running) return;
      this.exifError = '';
      try {
        this.exifStatus = { ...(this.exifStatus || {}), ...(await StartImageEXIFBackfill() || {}) };
      } catch (err) {
        this.exifError = `启动 EXIF 补全任务失败：${String(err?.message || err)}`;
        await this.loadEXIFStatus();
        return;
      }
      this.schedulePoll();
    },
    async cancelEXIFBackfill() {
      this.exifError = '';
      try {
        await CancelImageEXIFBackfill();
      } catch (err) {
        this.exifError = `取消 EXIF 补全任务失败：${String(err?.message || err)}`;
      }
      await this.loadEXIFStatus();
    },
    async startTagging() {
      if (this.taggingStatus?.running) return;
      this.taggingError = '';
      try {
        this.taggingStatus = { ...(this.taggingStatus || {}), ...(await StartImageAITagging() || {}) };
      } catch (err) {
        const message = String(err?.message || err);
        this.taggingError = message.includes('AI 配置不可用')
          ? `启动图片打标任务失败：${message}。请先在上方配置 AI 接口的 BaseURL 与模型。`
          : `启动图片打标任务失败：${message}`;
        await this.loadTaggingStatus();
        return;
      }
      this.schedulePoll();
    },
    async cancelTagging() {
      this.taggingError = '';
      try {
        await CancelImageAITagging();
      } catch (err) {
        this.taggingError = `取消图片打标任务失败：${String(err?.message || err)}`;
      }
      await this.loadTaggingStatus();
    },
    async startSemanticIndex() {
      if (this.semanticStatus?.running) return;
      this.semanticError = '';
      try {
        this.semanticStatus = { ...(this.semanticStatus || {}), ...(await StartImageSemanticIndex() || {}) };
      } catch (err) {
        this.semanticError = `启动图片语义索引失败：${String(err?.message || err)}`;
        await this.loadSemanticStatus();
        return;
      }
      this.schedulePoll();
    },
    async cancelSemanticIndex() {
      this.semanticError = '';
      try {
        await CancelImageSemanticIndex();
      } catch (err) {
        this.semanticError = `取消图片语义索引失败：${String(err?.message || err)}`;
      }
      await this.loadSemanticStatus();
    }
  }
};
</script>

<style scoped>
.image-ai-tasks { display: block; }
.image-task-status {
  display: grid;
  gap: 5px;
  margin-bottom: 16px;
  padding: 12px;
  border: 1px solid var(--accent-border);
  border-radius: var(--radius);
  background: var(--accent-soft);
}
.image-task-status--error { border-color: var(--danger-border); background: var(--danger-soft); }
.image-task-status span { color: var(--text-secondary); font-size: 12px; }
.image-task-bar { height: 6px; border-radius: 999px; background: var(--control-bg); overflow: hidden; }
.image-task-bar i { display: block; height: 100%; background: var(--accent-color); transition: width var(--transition); }
.image-task-failures { margin: 0; padding-left: 18px; color: var(--text-muted); font-size: 12px; }
.image-task-actions { display: flex; flex-wrap: wrap; gap: 10px; }
.image-task-status span.image-task-waiting-idle { color: var(--warning-color); }
</style>
