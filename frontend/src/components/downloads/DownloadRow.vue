<template>
  <article class="download-row" :class="`download-row--${task.state}`" data-test="download-item">
    <div class="download-row__head">
      <!-- 缩略图只有入库之后才有：它来自片库那份记录。没入库时留一个占位方块，
           不去猜一张图，也不让每行的高度跳来跳去。 -->
      <div class="download-thumb" :class="{ 'download-thumb--empty': !task.video_id }">
        <img v-if="task.video_id" :src="`/preview/thumbnail/${task.video_id}`" alt="" loading="lazy" />
      </div>
      <AppBadge class="download-pill" :tone="stateTone">{{ stateLabel }}</AppBadge>
      <strong class="download-row__name" :title="task.filename || task.title">
        {{ task.filename || task.title || task.url }}
      </strong>
      <button
        v-if="cancellable"
        type="button"
        class="btn btn--secondary download-row__cancel"
        @click="$emit('cancel', task.id)"
      >
        取消
      </button>
    </div>

    <!-- 不确定态：桥接任务拿不到总时长（HLS 要额外探测才知道），
         与其画一个假的百分比，不如老实显示"在动"。 -->
    <ProgressBar v-if="active" class="download-bar" />

    <div class="download-row__meta">{{ metaText }}</div>

    <p v-if="task.error" class="download-row__error">{{ task.error }}</p>
    <p v-else-if="notImportedText" class="download-row__warn" data-test="download-import-warning">
      文件已保存，但没有入库：{{ notImportedText }}
    </p>
    <!-- 重启之后请求头已经不在内存里（D-PC21），这类任务只能回浏览器重新推送。 -->
    <p v-if="needsBrowserResend" class="download-row__hint" data-test="download-resend-hint">
      应用重启后下载请求已失效，请回到浏览器插件里重新推送这个视频。
    </p>

    <!-- 下载完自动查重（D-DD-04）：片库里已有相同文件或同一部的其他版本时给出处理入口。 -->
    <div v-if="duplicateMatches.length" class="download-dup" data-test="download-duplicate">
      <div v-for="match in duplicateMatches" :key="match.video_id" class="download-dup__item" data-test="download-duplicate-match">
        <p class="download-dup__text">{{ duplicateText(match) }}</p>
        <div class="download-dup__actions">
          <button
            v-if="task.video_id"
            type="button"
            class="btn btn--secondary btn--sm"
            :disabled="busy"
            data-test="download-dup-trash"
            @click="$emit('duplicate-action', { action: 'trash', task, match })"
          >把这次下载移到废纸篓</button>
          <button
            v-if="match.kind === 'version' && task.video_id"
            type="button"
            class="btn btn--secondary btn--sm"
            :disabled="busy"
            data-test="download-dup-compare"
            @click="$emit('duplicate-action', { action: 'compare', task, match })"
          >对比播放</button>
          <button
            v-if="match.kind === 'version' && task.video_id"
            type="button"
            class="btn btn--secondary btn--sm"
            :disabled="busy"
            data-test="download-dup-version-group"
            @click="$emit('duplicate-action', { action: 'version-group', task, match })"
          >建立版本组</button>
          <button
            type="button"
            class="btn btn--secondary btn--sm"
            :disabled="busy"
            data-test="download-dup-dismiss"
            @click="$emit('duplicate-action', { action: 'dismiss', task, match })"
          >{{ match.kind === 'exact' ? '保留两份' : '不是同一部' }}</button>
        </div>
      </div>
    </div>
    <p v-if="duplicateNote" class="download-row__hint" data-test="download-duplicate-note">{{ duplicateNote }}</p>

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
        class="btn btn--secondary btn--sm"
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
import AppBadge from '../ui/AppBadge.vue';
import ProgressBar from '../ui/ProgressBar.vue';

// 「对方」相对这次下载（后端 DownloadRelation*）。
const DUPLICATE_RELATION_TEXT = {
  higher: '比这次的更清楚',
  lower: '比这次的更模糊',
  larger: '文件比这次的大',
  smaller: '文件比这次的小',
  similar: '大小与这次的相近'
};

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

// 状态徽标的颜色（AppBadge 的 tone）。
const STATE_TONES = {
  running: 'accent',
  importing: 'accent',
  done: 'success',
  failed: 'danger',
  interrupted: 'danger'
};

// 入库状态（后端 BrowserDownloadImport*）：没进扫描范围与扫描失败给不同的出口（D-PC25）。
const IMPORT_NOT_IN_SCAN_ROOTS = 'not_in_scan_roots';
const IMPORT_FAILED = 'import_failed';

// 一条下载任务。只负责展示与发出意图（取消 / 重试 / 入库 / 在访达中显示），不自己调接口；
// 动作的结果由下载页经 notice 传回来显示在这一行上。
export default {
  name: 'DownloadRow',
  components: { AppBadge, ProgressBar },
  props: {
    task: { type: Object, required: true },
    // 最近一次动作的结果：{ level: 'info' | 'warn' | 'error' | 'success', text }。
    notice: { type: Object, default: null },
    busy: { type: Boolean, default: false }
  },
  emits: ['cancel', 'action', 'duplicate-action'],
  computed: {
    stateLabel() {
      return STATE_LABELS[this.task.state] || this.task.state;
    },
    stateTone() {
      return STATE_TONES[this.task.state] || 'neutral';
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
    duplicateMatches() {
      const duplicate = this.task.duplicate;
      return duplicate?.status === 'found' ? duplicate.matches || [] : [];
    },
    // 查重本身的说明：失败原因；「其他版本」没比完或片库还有没算指纹的视频时说清楚，不说成「没有重复」；
    // 没入库的下载只能保留两份，说清为什么没有删除按钮。
    duplicateNote() {
      const duplicate = this.task.duplicate;
      if (!duplicate) return '';
      if (duplicate.status === 'failed') return `查重没有完成：${duplicate.error || '原因未知'}`;
      const notes = [];
      if (duplicate.version_error) notes.push(`「其他版本」的比对没有完成：${duplicate.version_error}。`);
      else if (this.task.video_id && Number(duplicate.unhashed_videos) > 0) {
        notes.push(`片库里还有 ${duplicate.unhashed_videos} 部视频没算视频指纹，没参与「其他版本」的比对。`);
      }
      if (!this.task.video_id && this.duplicateMatches.length) {
        notes.push('这次下载还没进片库，不能从这里删除；可以在访达中显示后手动处理。');
      }
      return notes.join('');
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
        list.push({ key: 'reveal', label: '在访达中显示' });
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
    duplicateText(match) {
      const facts = [];
      if (match.width && match.height) facts.push(`${match.width}×${match.height}`);
      if (match.size > 0) facts.push(this.formatBytes(match.size));
      const relation = DUPLICATE_RELATION_TEXT[match.relation];
      if (match.kind !== 'exact' && relation) facts.push(relation);
      const suffix = facts.length ? `（${facts.join('，')}）` : '';
      return match.kind === 'exact'
        ? `片库里已有完全相同的文件：《${match.title}》${suffix}`
        : `片库里有同一部的其他版本：《${match.title}》${suffix}`;
    },
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
  padding: var(--space-3) 14px;
  border: 1px solid var(--hairline);
  border-left: 3px solid var(--hairline);
  border-radius: var(--radius);
  background: var(--panel-bg);
}

.download-row--failed,
.download-row--interrupted { border-left-color: var(--danger-color); }
.download-row--done { border-left-color: var(--success-color); }
.download-row--running,
.download-row--importing { border-left-color: var(--accent-color); }

.download-row__head {
  display: flex;
  align-items: center;
  gap: var(--space-3);
}

.download-thumb {
  flex: none;
  width: 64px;
  height: 36px;
  border-radius: var(--radius-sm);
  overflow: hidden;
  background: var(--thumb-bg);
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

.download-pill { flex: none; }

/* 不确定态：桥接任务拿不到总时长（HLS 要额外探测才知道），与其画一个假的百分比，不如老实显示"在动"。 */
.download-bar { margin: var(--space-3) 0 6px; }

.download-row__meta {
  color: var(--text-secondary);
  font-size: var(--fs-secondary);
  margin-top: var(--space-1);
}

.download-row__error {
  margin: 6px 0 0;
  font-size: var(--fs-secondary);
  color: var(--danger-text);
  word-break: break-word;
}

.download-row__warn {
  margin: 6px 0 0;
  font-size: var(--fs-secondary);
  color: var(--warning-text);
  word-break: break-word;
}

.download-row__hint {
  margin: 6px 0 0;
  font-size: var(--fs-caption);
  color: var(--text-secondary);
}

.download-row__notice {
  margin: 6px 0 0;
  font-size: var(--fs-secondary);
  word-break: break-word;
  color: var(--text-secondary);
}

.download-row__notice--success { color: var(--success-color); }
.download-row__notice--warn { color: var(--warning-text); }
.download-row__notice--error { color: var(--danger-text); }

.download-dup { display: grid; gap: var(--space-2); margin-top: var(--space-2); padding: var(--space-3) var(--space-3); border: 1px solid var(--warning-border); border-radius: var(--radius); background: var(--warning-soft); }
.download-dup__item { display: grid; gap: 6px; }
.download-dup__text { margin: 0; color: var(--text-primary); font-size: var(--fs-secondary); }
.download-dup__actions { display: flex; flex-wrap: wrap; gap: 6px; }
.download-row__actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
  margin-top: var(--space-2);
}

.download-row__source {
  display: block;
  margin-top: 6px;
  font-size: var(--fs-caption);
  color: var(--text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
