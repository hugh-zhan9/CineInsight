<template>
  <BaseModal v-if="request" class="quit-confirm-modal" data-test="quit-confirm" @close="cancel">
    <h2>还有任务在进行</h2>
    <p class="quit-confirm__lead">退出会中断下面这些任务：</p>
    <ul class="quit-confirm__tasks">
      <li v-for="task in tasks" :key="task.key" :data-test="`quit-task-${task.key}`">
        <strong>{{ taskLabel(task.key) }}</strong>
        <span class="quit-confirm__counts">{{ countText(task) }}</span>
        <span v-if="task.names.length" class="quit-confirm__names">{{ namesText(task) }}</span>
      </li>
    </ul>
    <p v-for="hint in hints" :key="hint" class="quit-confirm__hint">{{ hint }}</p>
    <div class="modal-actions">
      <button type="button" class="btn-secondary" :disabled="quitting" data-test="quit-cancel" @click="cancel">继续运行</button>
      <button type="button" class="btn-danger" :disabled="quitting" data-test="quit-confirm-button" @click="confirmQuit">
        {{ quitting ? '正在退出…' : '仍然退出' }}
      </button>
    </div>
  </BaseModal>
</template>

<script>
import { ConfirmQuit } from '../../wailsjs/go/main/App';
import BaseModal from './ui/BaseModal.vue';
import { runtimeEventsMixin } from './video-list/runtimeEvents.js';
import { backgroundTaskLabel } from '../utils/idleScheduling.js';
import { notifyError } from '../utils/feedback.js';

// 退出确认（D-PC21、MEDIA-10）。用户关窗口时后端 beforeClose 发现字幕、超分、播放代理或浏览器下载
// 还在跑（或排着队），会拦下关闭并发 quit-confirm-required {tasks:[{key, running, queued, names}]}；
// 这里列出这些任务，确认后调 ConfirmQuit 放行退出。应用自己发起的退出（恢复完成、立即重启）
// 后端直接放行，不会走到这里。
export default {
  name: 'QuitConfirmDialog',
  components: { BaseModal },
  mixins: [runtimeEventsMixin],
  data() {
    return { request: null, quitting: false };
  },
  computed: {
    tasks() {
      const tasks = Array.isArray(this.request?.tasks) ? this.request.tasks : [];
      return tasks.map(task => ({
        key: String(task?.key || ''),
        running: Number(task?.running || 0),
        queued: Number(task?.queued || 0),
        names: Array.isArray(task?.names) ? task.names.filter(Boolean) : []
      }));
    },
    // 只说与这次被中断的任务有关、且确实成立的后果（D-PC20 中断任务的启动提示、D-PC21 下载记录）。
    hints() {
      const keys = new Set(this.tasks.map(task => task.key));
      const hints = [];
      if (keys.has('subtitle')) hints.push('字幕任务下次启动时会提示「上次中断」，可以一键重新排队。');
      if (keys.has('browser_download')) hints.push('浏览器下载会显示为「已中断」，需要回到浏览器重新推送。');
      return hints;
    }
  },
  mounted() {
    this.registerRuntimeEvent('quit-confirm-required', payload => {
      // 正在退出时再来一次（用户又点了一下关闭）不重置按钮状态。
      if (this.quitting) return;
      this.request = payload || { tasks: [] };
    });
  },
  methods: {
    taskLabel(key) {
      return backgroundTaskLabel(key);
    },
    countText(task) {
      const parts = [];
      if (task.running > 0) parts.push(`进行中 ${task.running}`);
      if (task.queued > 0) parts.push(`排队 ${task.queued}`);
      return parts.join(' · ');
    },
    namesText(task) {
      const total = task.running + task.queued;
      const more = total > task.names.length ? ` 等 ${total} 个` : '';
      return `${task.names.join('、')}${more}`;
    },
    cancel() {
      if (this.quitting) return;
      this.request = null;
    },
    async confirmQuit() {
      if (this.quitting) return;
      this.quitting = true;
      try {
        await ConfirmQuit();
      } catch (err) {
        this.quitting = false;
        notifyError(`退出失败：${err}`);
      }
    }
  }
};
</script>

<style scoped>
:deep(.quit-confirm-modal) { width: 460px; max-width: 100%; }
.quit-confirm__lead { font-size: 13.5px; color: var(--text-secondary); }
.quit-confirm__tasks { margin: 10px 0 0; padding-left: 18px; display: grid; gap: 6px; font-size: 13px; }
.quit-confirm__counts { margin-left: 8px; color: var(--text-muted); font-family: var(--font-mono); font-size: 12px; }
.quit-confirm__names {
  display: block;
  color: var(--text-secondary);
  font-size: 12px;
  overflow-wrap: anywhere;
}
.quit-confirm__hint { margin-top: 8px; color: var(--text-muted); font-size: 12px; line-height: 1.6; }
</style>
