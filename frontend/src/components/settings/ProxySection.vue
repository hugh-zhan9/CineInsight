<template>
  <div id="settings-playback-proxy" class="settings-section">
    <h3>播放代理</h3>
    <p class="help-text">
      mkv、avi 这类容器在应用内和手机端都播不了。生成一份 mp4 播放代理之后，预览与手机端会自动改用它，
      源文件一个字节都不会动，正式播放仍然打开源文件。代理放在 <code>~/.CineInsight/proxies/</code>，
      删掉随时可以重新生成。
    </p>
    <p class="help-text">重编码用的是 VideoToolbox，仅 macOS 可用；其他平台只有 remux（换容器）这一条路走得通。</p>

    <div class="proxy-usage" data-test="proxy-usage" role="status">
      <template v-if="usageError">
        <span>读取代理占用失败：{{ usageError }}</span>
      </template>
      <template v-else-if="!usage">
        <span>正在读取代理占用…</span>
      </template>
      <template v-else>
        <span data-test="proxy-usage-text">
          已占用 {{ formatBytes(usage.total_bytes) }}（{{ usage.count }} 份），上限 {{ limitText }}
        </span>
        <!-- 孤儿：文件名对得上但表里没行（回退过旧版本、表被清过都会留下），
             它们照样占磁盘，只统计表行会看不见。「清空全部」会一并删掉。 -->
        <span v-if="usage.orphan_count" data-test="proxy-orphan-text">
          另有 {{ usage.orphan_count }} 份无主产物占 {{ formatBytes(usage.orphan_bytes) }}，「清空全部代理」会一并删掉。
        </span>
        <span v-if="usage.foreign_files" data-test="proxy-foreign-text">
          代理目录里还有 {{ usage.foreign_files }} 个不是代理的文件，清空时会跳过它们。
        </span>
      </template>
      <button type="button" class="btn-secondary btn-compact" data-test="proxy-usage-refresh" @click="loadUsage">刷新</button>
    </div>

    <div class="setting-item">
      <label>代理目录体积上限（GiB）</label>
      <input
        data-test="proxy-cache-limit"
        type="number"
        min="0"
        step="1"
        class="number-input"
        :value="limitGiB"
        @input="onLimitInput"
      />
      <p class="help-text">
        超过上限时按「最久没用过」淘汰，刚用过 60 秒内的那份不动。填 0 表示不限。
        调小上限不会立刻清理，下一次生成代理时生效——想马上生效就点下面的「立即整理」。
      </p>
    </div>

    <div class="setting-item">
      <div class="proxy-actions">
        <button
          type="button"
          class="btn-secondary btn-compact"
          data-test="proxy-enforce-limit"
          :disabled="busy"
          @click="enforceLimit"
        >立即按上限整理</button>
        <button
          type="button"
          class="btn-secondary btn-compact btn-danger-outline"
          data-test="proxy-clear-all"
          :disabled="busy || !usage || (usage.count === 0 && !usage.orphan_count)"
          @click="clearAll"
        >清空全部代理</button>
      </div>
      <p v-if="actionMessage" class="help-text" data-test="proxy-action-message">{{ actionMessage }}</p>
    </div>

    <div class="proxy-task" data-test="proxy-task" role="status">
      <span data-test="proxy-task-text">{{ taskText }}</span>
      <button
        v-if="status && status.running"
        type="button"
        class="btn-secondary btn-compact"
        data-test="proxy-task-cancel"
        @click="cancelTask"
      >取消</button>
      <TaskFailureList :failures="failedResultItems" key-prefix="proxy-" data-test="proxy-failures" />
    </div>
  </div>
</template>

<script>
import {
  CancelPlaybackProxyTask,
  ClearPlaybackProxies,
  EnforcePlaybackProxyLimit,
  GetPlaybackProxyStatus,
  GetPlaybackProxyUsage
} from '../../../wailsjs/go/main/App';
import { confirmAction, notifyError } from '../../utils/feedback.js';
import { formatBytes } from '../../utils/mediaDetails.js';
import { PLAYBACK_PROXY_CODE_LABELS } from '../../utils/playbackProxy.js';
import TaskFailureList from '../video-list/TaskFailureList.vue';

const BYTES_PER_GIB = 1024 * 1024 * 1024;

// 播放代理分区：占用、数量、上限、清空全部、立即整理，外加当前任务状态与取消。
// 上限在设置表单里是字节，界面上按 GiB 填——用字节填 50 GiB 要数 11 位数。
export default {
  name: 'ProxySection',
  components: { TaskFailureList },
  props: {
    form: { type: Object, required: true }
  },
  data() {
    return {
      usage: null,
      usageError: '',
      status: null,
      actionMessage: '',
      busy: false,
      proxyStateOff: null
    };
  },
  computed: {
    limitGiB() {
      const bytes = Number(this.form.proxy_cache_limit_bytes);
      if (!Number.isFinite(bytes) || bytes <= 0) return 0;
      return Math.round((bytes / BYTES_PER_GIB) * 10) / 10;
    },
    limitText() {
      const bytes = Number(this.usage?.limit_bytes);
      if (!Number.isFinite(bytes) || bytes <= 0) return '不限';
      return formatBytes(bytes);
    },
    // 只列真失败：created / already_exists / in_progress 都不是失败。
    failedResults() {
      const skipped = ['created', 'already_exists', 'in_progress'];
      return (this.status?.results || []).filter(item => item.code && !skipped.includes(item.code));
    },
    failedResultItems() {
      return this.failedResults.map(item => ({ video_id: item.video_id, name: item.name, error: this.codeLabel(item.code) }));
    },
    taskText() {
      const status = this.status;
      if (!status || (!status.running && !status.total)) return '当前没有代理任务。';
      if (status.running) {
        const current = status.current_video_name ? `，正在处理 ${status.current_video_name}` : '';
        return `正在生成代理 ${status.processed}/${status.total}${current}。`;
      }
      if (status.cancelled) return `代理任务已取消，已处理 ${status.processed}/${status.total}。`;
      return `上一轮完成：成功 ${status.succeeded}，跳过 ${status.skipped}，失败 ${status.failed}。`;
    }
  },
  mounted() {
    this.loadUsage();
    this.loadStatus();
    if (window.runtime?.EventsOn) {
      const off = window.runtime.EventsOn('playback-proxy-state', (status) => {
        if (!status) return;
        this.status = status;
        // 一轮跑完淘汰也跑完了，占用要跟着刷新。
        if (!status.running) this.loadUsage();
      });
      if (typeof off === 'function') this.proxyStateOff = off;
    }
  },
  beforeUnmount() {
    this.proxyStateOff?.();
  },
  methods: {
    formatBytes,
    codeLabel(code) {
      return PLAYBACK_PROXY_CODE_LABELS[code] || code;
    },
    onLimitInput(event) {
      const gib = Number(event.target.value);
      if (!Number.isFinite(gib) || gib <= 0) {
        this.form.proxy_cache_limit_bytes = 0;
        return;
      }
      this.form.proxy_cache_limit_bytes = Math.round(gib * BYTES_PER_GIB);
    },
    async loadUsage() {
      try {
        this.usage = await GetPlaybackProxyUsage();
        this.usageError = '';
      } catch (err) {
        this.usageError = String(err);
      }
    },
    async loadStatus() {
      try {
        this.status = await GetPlaybackProxyStatus();
      } catch (err) {
        // 状态读不到不该挡住这一整块设置：占用与上限仍然可用。
        this.status = null;
      }
    },
    async enforceLimit() {
      this.busy = true;
      this.actionMessage = '';
      try {
        this.usage = await EnforcePlaybackProxyLimit();
        this.usageError = '';
        this.actionMessage = `整理完成，现在占用 ${formatBytes(this.usage.total_bytes)}（${this.usage.count} 份）。`;
      } catch (err) {
        notifyError('整理代理失败: ' + err);
      } finally {
        this.busy = false;
      }
    },
    async clearAll() {
      const confirmed = await confirmAction({
        title: '清空全部播放代理',
        message: '这会删掉全部已生成的代理文件。源文件不受影响，需要时可以重新生成。确认继续吗？',
        confirmText: '清空',
        danger: true
      });
      if (!confirmed) return;
      this.busy = true;
      this.actionMessage = '';
      try {
        this.usage = await ClearPlaybackProxies();
        this.usageError = '';
        this.actionMessage = '已清空全部播放代理。';
      } catch (err) {
        notifyError('清空代理失败: ' + err);
      } finally {
        this.busy = false;
      }
    },
    async cancelTask() {
      try {
        await CancelPlaybackProxyTask();
      } catch (err) {
        notifyError('取消代理任务失败: ' + err);
      }
      await this.loadStatus();
    }
  }
};
</script>

<style scoped>
.proxy-usage,
.proxy-task {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 8px;
  margin-top: 16px;
  padding: 10px 12px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius);
  background: var(--control-bg);
  color: var(--text-secondary);
  font-size: 13px;
}
.proxy-actions { display: flex; flex-wrap: wrap; gap: 8px; }
.btn-compact { height: 28px; padding: 0 10px; font-size: 12px; }
</style>
