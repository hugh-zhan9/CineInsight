<template>
  <Teleport v-if="request" to="body">
    <DialogShell title="还有任务在进行" size="sm" :busy="quitting" class="quit-confirm-modal" data-test="quit-confirm" @close="cancel">
      <p class="quit-confirm__lead">退出会中断下面这些任务：</p>
      <ul class="quit-confirm__tasks">
        <li v-for="task in tasks" :key="task.key" :data-test="`quit-task-${task.key}`">
          <strong>{{ taskLabel(task.key) }}</strong>
          <span class="quit-confirm__counts">{{ countText(task) }}</span>
          <span v-if="task.names.length" class="quit-confirm__names">{{ namesText(task) }}</span>
        </li>
      </ul>
      <p v-for="hint in hints" :key="hint" class="quit-confirm__hint">{{ hint }}</p>
      <template #footer>
        <button type="button" class="btn btn--secondary" :disabled="quitting" data-test="quit-cancel" @click="cancel">继续运行</button>
        <button type="button" class="btn btn--danger" :disabled="quitting" data-test="quit-confirm-button" @click="confirmQuit">
          {{ quitting ? '正在退出…' : '仍然退出' }}
        </button>
      </template>
    </DialogShell>
  </Teleport>
</template>

<script>
import { ConfirmQuit } from '../../wailsjs/go/main/App';
import DialogShell from './ui/DialogShell.vue';
import { runtimeEventsMixin } from './video-list/runtimeEvents.js';
import { backgroundTaskLabel } from '../utils/idleScheduling.js';
import { notifyError } from '../utils/feedback.js';

// 退出确认（D-PC21、MEDIA-10）。用户关窗口时后端 beforeClose 发现字幕、超分、播放代理或浏览器下载
// 还在跑（或排着队），会拦下关闭并发 quit-confirm-required {tasks:[{key, running, queued, names}]}；
// 这里列出这些任务，确认后调 ConfirmQuit 放行退出。应用自己发起的退出（恢复完成、立即重启）
// 后端直接放行，不会走到这里。
// 关窗口随时可能发生，那时最上面可能是 teleport 到 body 末尾的对话框（原图、人脸来源…）：对话框打开时才
// teleport 到 body 末尾，排在它们后面，同在 --z-modal 也压得住（层级规则见 tokens.css「层级」）。
export default {
  name: 'QuitConfirmDialog',
  components: { DialogShell },
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
      if (keys.has('video_edit')) hints.push('视频工作台的导出会标为「已中断」，未发布的半成品会清掉；下次可在工作台「继续」未完成项。');
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
/* 尺寸由 DialogShell 的 sm（420）给出。 */
.quit-confirm__lead { font-size: var(--fs-secondary); color: var(--text-secondary); }
.quit-confirm__tasks { margin: var(--space-2) 0 0; padding-left: 18px; display: grid; gap: 6px; font-size: var(--fs-secondary); }
.quit-confirm__counts { margin-left: var(--space-2); color: var(--text-muted); font-family: var(--font-mono); font-size: var(--fs-caption); }
.quit-confirm__names {
  display: block;
  color: var(--text-secondary);
  font-size: var(--fs-caption);
  overflow-wrap: anywhere;
}
.quit-confirm__hint { margin-top: var(--space-2); color: var(--text-muted); font-size: var(--fs-caption); line-height: 1.6; }
</style>
