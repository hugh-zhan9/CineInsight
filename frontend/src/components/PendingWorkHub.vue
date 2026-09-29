<template>
  <BaseModal v-if="open" class="pending-work-modal" close-on-overlay data-test="pending-work-hub" @close="$emit('close')">
    <div class="pending-work__head">
      <h2>待处理</h2>
      <span v-if="summary" class="pending-work__total" data-test="pending-work-total">共 {{ formatCount(summary.total) }} 项</span>
      <button type="button" class="pending-work__close" aria-label="关闭待处理" @click="$emit('close')">×</button>
    </div>

    <p v-if="error" class="pending-work__error" role="alert" data-test="pending-work-error">{{ error }}</p>
    <p v-else-if="!summary" class="pending-work__hint">正在读取…</p>

    <template v-if="summary && !error">
      <ul class="pending-work__list">
        <li
          v-for="row in rows"
          :key="row.field"
          :class="['pending-work__row', { 'pending-work__row--empty': row.count === 0 }]"
          :data-test="`pending-row-${row.field}`"
        >
          <span class="pending-work__label">{{ row.label }}</span>
          <span class="pending-work__count" :data-test="`pending-count-${row.field}`">{{ formatCount(row.count) }}</span>
          <kbd v-if="row.shortcut" class="pending-work__kbd">{{ row.shortcut }}</kbd>
          <button
            type="button"
            class="btn-secondary pending-work__go"
            :disabled="row.count === 0"
            :data-test="`pending-go-${row.field}`"
            @click="go(row)"
          >处理</button>
        </li>
      </ul>
      <p v-if="summary.ai_video_failed_runs > 0" class="pending-work__failed" data-test="pending-ai-failed">
        另有 {{ formatCount(summary.ai_video_failed_runs) }} 个视频 AI 打标失败（不计入待处理）。
        <button type="button" class="link-btn" data-test="pending-open-task-center" @click="openTaskCenter">在任务中心查看</button>
      </p>
      <p v-if="summary.total === 0" class="pending-work__hint" data-test="pending-work-empty">暂无待处理事项。</p>
    </template>
  </BaseModal>
</template>

<script>
import { GetPendingWorkSummary } from '../../wailsjs/go/main/App';
import BaseModal from './ui/BaseModal.vue';

// 顶栏角标与工作台的刷新周期：数量来自好几个面板背后的计数查询，不跟每个进度事件走。
const REFRESH_INTERVAL_MS = 60000;

// 工作台的各项与跳转命令（详细设计 §6.1、D-PC27、META-08）。命令 ID 是固定契约，由各宿主页注册；
// 工作台不重新挂载任何审阅面板，只列数量、点「处理」时把命令 ID 交给 App 去跳。
export const PENDING_WORK_ROWS = [
  { field: 'ai_video_candidates', label: '视频 AI 标签待审阅', command: 'library.openAIReview' },
  { field: 'same_source_unconfirmed', label: '同源视频待确认', command: 'library.openAIReview' },
  { field: 'ai_image_candidates', label: '图片 AI 标签待审阅', command: 'photos.openAIReview' },
  { field: 'face_unnamed', label: '人脸待命名', command: 'people.openFaceReview' },
  { field: 'face_append_pending', label: '人脸待确认追加', command: 'people.openFaceReview' },
  { field: 'collection_suggestions', label: '建议作品集', command: 'library.openCollectionSuggestions' },
  { field: 'cleanup_candidates', label: '清理候选', command: 'library.openCleanup', shortcut: '⌘K' },
  { field: 'local_metadata_updates', label: '本地资料有更新', command: 'library.openLocalMetadataUpdates' }
];

// 待处理工作台（D-PC27、META-08、APP-11）。常挂载，关着时也定时刷新，顶栏「待处理」角标读它。
export default {
  name: 'PendingWorkHub',
  components: { BaseModal },
  props: {
    open: { type: Boolean, default: false }
  },
  emits: ['close', 'badge-change', 'run-command', 'open-task-center'],
  data() {
    return { summary: null, error: '' };
  },
  computed: {
    rows() {
      return PENDING_WORK_ROWS.map(row => ({ ...row, count: Number(this.summary?.[row.field] || 0) }));
    }
  },
  watch: {
    open(open) {
      if (open) this.refresh();
    }
  },
  mounted() {
    this.refresh();
    this._refreshInterval = setInterval(() => this.refresh(), REFRESH_INTERVAL_MS);
  },
  beforeUnmount() {
    clearInterval(this._refreshInterval);
  },
  methods: {
    async refresh() {
      const token = Symbol('pending-work');
      this._refreshToken = token;
      try {
        const summary = await GetPendingWorkSummary();
        if (this._refreshToken !== token) return;
        this.summary = summary || null;
        this.error = '';
        this.$emit('badge-change', Number(summary?.total || 0));
      } catch (err) {
        if (this._refreshToken !== token) return;
        // 数据库维护、待重启等终态下后端直接给中文说明（不去读库），原样显示。
        this.summary = null;
        this.error = String(err?.message || err || '待处理数量读取失败');
        this.$emit('badge-change', 0);
      }
    },
    go(row) {
      this.$emit('run-command', row.command, row.label);
      this.$emit('close');
    },
    openTaskCenter() {
      this.$emit('open-task-center');
      this.$emit('close');
    },
    formatCount(value) {
      return Number(value || 0).toLocaleString('zh-CN');
    }
  }
};
</script>

<style scoped>
:deep(.pending-work-modal) {
  width: 460px;
  max-width: 100%;
  padding: 18px 20px 16px;
}
.pending-work__head { display: flex; align-items: baseline; gap: 10px; margin-bottom: 10px; }
.pending-work__head h2 { margin: 0; font-size: 17px; }
.pending-work__total { color: var(--text-muted); font-size: 12.5px; }
.pending-work__close {
  margin-left: auto;
  border: 0;
  background: transparent;
  color: var(--text-muted);
  font-size: 20px;
  line-height: 1;
  cursor: pointer;
}
.pending-work__list { list-style: none; margin: 0; padding: 0; display: grid; gap: 4px; }
.pending-work__row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 7px 10px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius);
  background: var(--control-bg);
  font-size: 13px;
}
.pending-work__row--empty { color: var(--text-muted); }
.pending-work__label { flex: 1; min-width: 0; }
.pending-work__count { font-family: var(--font-mono); font-weight: 600; }
.pending-work__row--empty .pending-work__count { font-weight: 400; }
.pending-work__kbd {
  padding: 1px 5px;
  border: 1px solid var(--hairline);
  border-radius: 4px;
  color: var(--text-muted);
  font-family: var(--font-mono);
  font-size: 11px;
}
.pending-work__go { height: 26px; padding: 0 10px; font-size: 12px; }
.pending-work__failed { margin-top: 10px; color: var(--warning-color); font-size: 12.5px; }
.pending-work__error { color: var(--danger-color); font-size: 13px; }
.pending-work__hint { margin-top: 8px; color: var(--text-muted); font-size: 12.5px; }
.link-btn {
  border: 0;
  background: transparent;
  color: var(--accent-text);
  font-size: 12.5px;
  cursor: pointer;
  padding: 0;
}
</style>
