<template>
  <article class="download-row" :class="`download-row--${task.state}`" data-test="download-item">
    <div class="download-row__head">
      <!-- 缩略图只有入库之后才有：它来自片库那份记录。没入库时留一个占位方块，
           不去猜一张图，也不让每行的高度跳来跳去。 -->
      <div class="download-thumb" :class="{ 'download-thumb--empty': !task.video_id }">
        <img v-if="task.video_id" :src="`/preview/thumbnail/${task.video_id}`" alt="" loading="lazy" />
      </div>
      <span class="download-pill" :class="`download-pill--${task.state}`">{{ stateLabel }}</span>
      <strong class="download-row__name" :title="task.output_path || task.filename">
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
    <div v-if="active" class="download-bar" :class="{ 'download-bar--remux': task.state === 'remuxing' }">
      <i></i>
    </div>

    <div class="download-row__meta">{{ metaText }}</div>

    <p v-if="task.error" class="download-row__error">{{ task.error }}</p>
    <p v-else-if="task.import_error" class="download-row__warn">
      文件已保存，但没有入库：{{ task.import_error }}
    </p>

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
  remuxing: '转封装',
  importing: '入库中',
  done: '已完成',
  failed: '失败',
  canceled: '已取消'
};

const ACTIVE_STATES = ['running', 'remuxing', 'importing'];

// 一条下载任务。只负责展示与发出取消意图，不自己调接口。
export default {
  name: 'DownloadRow',
  props: {
    task: { type: Object, required: true }
  },
  emits: ['cancel'],
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
    metaText() {
      const parts = [];
      if (this.task.processed_seconds > 0) parts.push(`已处理 ${this.formatDuration(this.task.processed_seconds)}`);
      if (this.task.bytes_written > 0) parts.push(this.formatBytes(this.task.bytes_written));
      if (this.task.state === 'remuxing') parts.push('正在转成 mp4，不重新编码');
      if (parts.length === 0) return this.task.state === 'queued' ? '等待开始' : '正在准备';
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

.download-row--failed { border-left-color: var(--danger-color, #c0392b); }
.download-row--done { border-left-color: var(--success-color, #1e8e3e); }
.download-row--running,
.download-row--remuxing,
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
.download-pill--remuxing,
.download-pill--importing {
  background: var(--accent-color, #2f6feb);
  color: #fff;
}

.download-pill--done { background: var(--success-color, #1e8e3e); color: #fff; }
.download-pill--failed { background: var(--danger-color, #c0392b); color: #fff; }

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

.download-bar--remux > i { background: var(--warning-color, #b26a00); }

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
