<template>
  <div class="image-ai-tasks">
    <div class="setting-item image-ai-description-controls">
      <label>图片 AI 描述</label>
      <p class="help-text">
        为还没有描述的图片批量生成中文描述，复用上方 AI 接口配置。描述是图片语义索引的输入，没有描述的图片会被语义索引跳过。
      </p>
      <div class="image-task-status" :class="{ 'image-task-status--error': Boolean(descriptionError) }" data-test="image-ai-description-status">
        <strong>{{ descriptionStatusText }}</strong>
        <span v-if="descriptionStatus?.running || descriptionStatus?.completed || descriptionStatus?.cancelled">
          进度 {{ descriptionStatus.processed || 0 }}/{{ descriptionStatus.total || 0 }}
          · 成功 {{ descriptionStatus.succeeded || 0 }}
          · 跳过 {{ descriptionStatus.skipped || 0 }}
          · 失败 {{ descriptionStatus.failed || 0 }}
        </span>
        <div v-if="descriptionProgressRatio !== null" class="image-task-bar" role="progressbar" :aria-valuenow="descriptionStatus.processed || 0" :aria-valuemin="0" :aria-valuemax="descriptionStatus.total || 0">
          <i :style="{ width: descriptionProgressRatio + '%' }"></i>
        </div>
        <span v-if="descriptionError" data-test="image-ai-description-error">{{ descriptionError }}</span>
        <ul v-if="descriptionFailures.length" class="image-task-failures" data-test="image-ai-description-failures">
          <li v-for="failure in descriptionFailures" :key="`desc-${failure.image_id}`">
            {{ failure.name || `图片 ${failure.image_id}` }}（{{ failure.code || '未知错误' }}）
          </li>
        </ul>
      </div>
      <div class="image-task-actions">
        <button
          type="button"
          class="btn-primary"
          :disabled="descriptionStatus?.running"
          data-test="image-ai-description-start"
          @click="startDescription"
        >开始/继续生成</button>
        <button
          v-if="descriptionStatus?.running"
          type="button"
          class="btn-secondary"
          data-test="image-ai-description-cancel"
          @click="cancelDescription"
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
  CancelImageAIDescription, CancelImageSemanticIndex, GetImageAIDescriptionStatus,
  GetImageSemanticIndexStatus, StartImageAIDescription, StartImageSemanticIndex
} from '../../wailsjs/go/main/App';

// 事件推送之外保留 1s 轮询兜底（镜像 PhotoCleanupPanel）。
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
      descriptionStatus: null,
      semanticStatus: null,
      descriptionError: '',
      semanticError: '',
      descriptionOff: null,
      semanticOff: null
    };
  },
  computed: {
    descriptionStatusText() {
      const status = this.descriptionStatus;
      if (!status) return '正在读取图片描述任务状态...';
      if (status.running) return '图片描述生成中';
      if (status.cancelled) return '图片描述任务已取消，可继续';
      if (status.completed) return '图片描述任务已完成';
      return '图片描述任务未运行';
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
    descriptionProgressRatio() { return progressRatio(this.descriptionStatus); },
    semanticProgressRatio() { return progressRatio(this.semanticStatus); },
    descriptionFailures() { return (this.descriptionStatus?.failures || []).slice(0, MAX_FAILURES_SHOWN); },
    semanticFailures() { return (this.semanticStatus?.failures || []).slice(0, MAX_FAILURES_SHOWN); }
  },
  mounted() {
    this._alive = true;
    this.loadDescriptionStatus();
    this.loadSemanticStatus();
    if (window.runtime?.EventsOn) {
      const descriptionOff = window.runtime.EventsOn('image-ai-description-progress', status => {
        this.descriptionStatus = { ...(this.descriptionStatus || {}), ...(status || {}) };
      });
      if (typeof descriptionOff === 'function') this.descriptionOff = descriptionOff;
      const semanticOff = window.runtime.EventsOn('image-semantic-index-state', status => {
        this.semanticStatus = { ...(this.semanticStatus || {}), ...(status || {}) };
      });
      if (typeof semanticOff === 'function') this.semanticOff = semanticOff;
    }
  },
  beforeUnmount() {
    this._alive = false;
    clearTimeout(this._pollTimer);
    this.descriptionOff?.();
    this.semanticOff?.();
  },
  methods: {
    schedulePoll() {
      clearTimeout(this._pollTimer);
      if (!this._alive) return;
      if (!this.descriptionStatus?.running && !this.semanticStatus?.running) return;
      this._pollTimer = setTimeout(async () => {
        if (!this._alive) return;
        if (this.descriptionStatus?.running) await this.loadDescriptionStatus();
        if (this.semanticStatus?.running) await this.loadSemanticStatus();
        this.schedulePoll();
      }, POLL_INTERVAL_MS);
    },
    async loadDescriptionStatus() {
      try {
        this.descriptionStatus = await GetImageAIDescriptionStatus() || null;
      } catch (err) {
        this.descriptionError = `读取图片描述任务状态失败：${String(err?.message || err)}`;
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
    async startDescription() {
      if (this.descriptionStatus?.running) return;
      this.descriptionError = '';
      try {
        this.descriptionStatus = { ...(this.descriptionStatus || {}), ...(await StartImageAIDescription() || {}) };
      } catch (err) {
        const message = String(err?.message || err);
        this.descriptionError = message.includes('AI 配置不可用')
          ? `启动图片描述任务失败：${message}。请先在上方配置 AI 接口的 BaseURL 与模型。`
          : `启动图片描述任务失败：${message}`;
        await this.loadDescriptionStatus();
        return;
      }
      this.schedulePoll();
    },
    async cancelDescription() {
      this.descriptionError = '';
      try {
        await CancelImageAIDescription();
      } catch (err) {
        this.descriptionError = `取消图片描述任务失败：${String(err?.message || err)}`;
      }
      await this.loadDescriptionStatus();
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
</style>
