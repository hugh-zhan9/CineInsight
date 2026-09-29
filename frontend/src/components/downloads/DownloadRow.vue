<template>
  <article class="download-row" :class="`download-row--${task.state}`" data-test="download-item">
    <div class="download-row__head">
      <!-- 缩略图只有入库之后才有：它来自片库那份记录。没入库时留一个占位方块，
           不去猜一张图，也不让每行的高度跳来跳去。 -->
      <div class="download-thumb" :class="{ 'download-thumb--empty': !task.video_id }">
        <img v-if="task.video_id" :src="`/preview/thumbnail/${task.video_id}`" alt="" loading="lazy" />
      </div>
      <span class="download-pill" :class="`download-pill--${task.state}`">{{ stateLabel }}</span>
      <strong class="download-row__name" :title="task.filename || task.title">
        {{ task.filename || task.title || task.url }}
      </strong>
      <button
        v-if="cancellable"
        type="button"
        class="btn-secondary download-row__cancel"
        @click="$emit('cancel', task.id)"
      >
        取消
      </button>
    </div>

    <!-- 不确定态：桥接任务拿不到总时长（HLS 要额外探测才知道），
         与其画一个假的百分比，不如老实显示"在动"。 -->
    <div v-if="active" class="download-bar">
      <i></i>
    </div>

    <div class="download-row__meta">{{ metaText }}</div>

    <p v-if="task.error" class="download-row__error">{{ task.error }}</p>
    <p v-else-if="notImportedText" class="download-row__warn" data-test="download-import-warning">
      文件已保存，但没有入库：{{ notImportedText }}
    </p>
    <!-- 重启之后请求头已经不在内存里（D-PC21），这类任务只能回浏览器重新推送。 -->
    <p v-if="needsBrowserResend" class="download-row__hint" data-test="download-resend-hint">
      应用重启后下载请求已失效，请回到浏览器插件里重新推送这个视频。
    </p>

    <p
      v-if="notice"
      class="download-row__notice"
      :class="`download-row__notice--${notice.level || 'info'}`"
      data-test="download-action-notice"
      :role="notice.level === 'error' ? 'alert' : 'status'"
    >{{ notice.text }}</p>

    <div v-if="actions.length" class="download-row__actions">
      <button
        v-for="action in actions"
        :key="action.key"
        type="button"
        class="btn-secondary btn-compact"
        :disabled="busy"
        :data-test="`download-action-${action.key}`"
        @click="$emit('action', { action: action.key, task })"
      >{{ action.label }}</button>
    </div>

    <a
      v-if="task.page_url"
      class="download-row__source"
      :href="task.page_url"
      :title="task.page_url"
      target="_blank"
      rel="noreferrer"
    >{{ task.page_url }}</a>
  </article>
</template>

<script>
import { formatBytes } from '../../utils/mediaDetails.js';

const STATE_LABELS = {
  queued: '排队中',
  running: '下载中',
  importing: '入库中',
  done: '已完成',
  failed: '失败',
  canceled: '已取消',
  interrupted: '已中断'
};

const ACTIVE_STATES = ['running', 'importing'];

// 入库状态（后端 BrowserDownloadImport*）：没进扫描范围与扫描失败给不同的出口（D-PC25）。
const IMPORT_NOT_IN_SCAN_ROOTS = 'not_in_scan_roots';
const IMPORT_FAILED = 'import_failed';

// 一条下载任务。只负责展示与发出意图（取消 / 重试 / 入库 / 打开位置），不自己调接口；
// 动作的结果由下载页经 notice 传回来显示在这一行上。
export default {
  name: 'DownloadRow',
  props: {
    task: { type: Object, required: true },
    // 最近一次动作的结果：{ level: 'info' | 'warn' | 'error' | 'success', text }。
    notice: { type: Object, default: null },
    busy: { type: Boolean, default: false }
  },
  emits: ['cancel', 'action'],
  computed: {
    stateLabel() {
      return STATE_LABELS[this.task.state] || this.task.state;
    },
    active() {
      return ACTIVE_STATES.includes(this.task.state);
    },
    // 「入库中」那一步是扫描，不接受取消，所以不给按钮——摆一个点了没反应的更糟。
    cancellable() {
      return this.task.state === 'queued' || this.task.state === 'running';
    },
    // 未入库的原因只说一句（MEDIA-15）：后端给的就是原因句，前面的「文件已保存」由这里加。
    notImportedText() {
      if (this.task.state !== 'done') return '';
      const status = this.task.import_status;
      if (status && status !== IMPORT_NOT_IN_SCAN_ROOTS && status !== IMPORT_FAILED) return '';
      return this.task.import_error || (status ? '入库没有成功' : '');
    },
    needsBrowserResend() {
      if (this.task.retryable) return false;
      return this.task.state === 'interrupted' || this.task.state === 'failed' || this.task.state === 'canceled';
    },
    actions() {
      const task = this.task;
      const list = [];
      if (task.retryable && (task.state === 'failed' || task.state === 'canceled')) {
        list.push({ key: 'retry', label: '重试' });
      }
      if (task.state === 'done') {
        if (task.import_status === IMPORT_NOT_IN_SCAN_ROOTS) {
          list.push({ key: 'add-directory', label: '把下载目录加入扫描目录' });
        }
        if (task.import_status === IMPORT_NOT_IN_SCAN_ROOTS || task.import_status === IMPORT_FAILED) {
          list.push({ key: 'reimport', label: '重新入库' });
        }
        list.push({ key: 'reveal', label: '打开所在目录' });
      }
      return list;
    },
    metaText() {
      const parts = [];
      if (this.task.processed_seconds > 0) parts.push(`已处理 ${this.formatDuration(this.task.processed_seconds)}`);
      if (this.task.bytes_written > 0) parts.push(this.formatBytes(this.task.bytes_written));
      if (parts.length === 0) {
        if (this.task.state === 'queued') return '等待开始';
        if (this.active) return '正在准备';
        return '';
      }
      return parts.join(' · ');
    }
  },
  methods: {
    formatDuration(seconds) {
      const total = Math.round(Number(seconds) || 0);
      const hours = Math.floor(total / 3600);
      const minutes = Math.floor((total % 3600) / 60);
      const secs = String(total % 60).padStart(2, '0');
      return hours > 0
        ? `${hours}:${String(minutes).padStart(2, '0')}:${secs}`
        : `${minutes}:${secs}`;
    },
    formatBytes(bytes) {
      return formatBytes(Number(bytes) || 0);
    }
  }
};
</script>

<style scoped>
.download-row {
  padding: 12px 14px;
  border-radius: 10px;
  background: var(--bg-color);
  border-left: 3px solid transparent;
}

.download-row--failed,
.download-row--interrupted { border-left-color: var(--danger-color, #c0392b); }
.download-row--done { border-left-color: var(--success-color, #1e8e3e); }
.download-row--running,
.download-row--importing { border-left-color: var(--accent-color, #2f6feb); }

.download-row__head {
  display: flex;
  align-items: center;
  gap: 10px;
}

.download-thumb {
  flex: none;
  width: 64px;
  height: 36px;
  border-radius: 4px;
  overflow: hidden;
  background: var(--border-color, #e2e5ea);
}

.download-thumb img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}

.download-row__name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.download-row__cancel { flex: none; }

.download-pill {
  flex: none;
  padding: 2px 8px;
  border-radius: 999px;
  font-size: 12px;
  background: var(--border-color, #e2e5ea);
  color: var(--text-secondary);
}

.download-pill--running,
.download-pill--importing {
  background: var(--accent-color, #2f6feb);
  color: #fff;
}

.download-pill--done { background: var(--success-color, #1e8e3e); color: #fff; }
.download-pill--failed,
.download-pill--interrupted { background: var(--danger-color, #c0392b); color: #fff; }

.download-bar {
  height: 4px;
  border-radius: 999px;
  background: var(--border-color, #e2e5ea);
  overflow: hidden;
  margin: 10px 0 6px;
}

.download-bar > i {
  display: block;
  height: 100%;
  width: 35%;
  border-radius: 999px;
  background: var(--accent-color, #2f6feb);
  animation: download-slide 1.4s ease-in-out infinite;
}

@keyframes download-slide {
  0% { transform: translateX(-100%); }
  100% { transform: translateX(320%); }
}

/* 关掉动效偏好时不做无限动画，静态显示一段即可。 */
@media (prefers-reduced-motion: reduce) {
  .download-bar > i { animation: none; width: 100%; opacity: 0.6; }
}

.download-row__meta {
  color: var(--text-secondary);
  font-size: 13px;
  margin-top: 4px;
}

.download-row__error {
  margin: 6px 0 0;
  font-size: 13px;
  color: var(--danger-color, #c0392b);
  word-break: break-word;
}

.download-row__warn {
  margin: 6px 0 0;
  font-size: 13px;
  color: var(--warning-color, #b26a00);
  word-break: break-word;
}

.download-row__hint {
  margin: 6px 0 0;
  font-size: 12px;
  color: var(--text-secondary);
}

.download-row__notice {
  margin: 6px 0 0;
  font-size: 12.5px;
  word-break: break-word;
  color: var(--text-secondary);
}

.download-row__notice--success { color: var(--success-color, #1e8e3e); }
.download-row__notice--warn { color: var(--warning-color, #b26a00); }
.download-row__notice--error { color: var(--danger-color, #c0392b); }

.download-row__actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 8px;
}

.download-row__source {
  display: block;
  margin-top: 6px;
  font-size: 12px;
  color: var(--text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
