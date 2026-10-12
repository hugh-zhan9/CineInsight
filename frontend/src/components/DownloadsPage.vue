<template>
  <section class="page-content downloads-page">
    <div class="downloads-heading">
      <div>
        <h2>下载</h2>
        <p>浏览器插件推过来的任务。进度由后端实时推送，不用手动刷新。</p>
      </div>
      <button
        type="button"
        class="btn-secondary"
        :disabled="finishedTasks.length === 0"
        @click="clearFinished"
      >
        清掉已结束
      </button>
    </div>

    <p v-if="errorMessage" class="downloads-error" role="alert" data-test="downloads-error">{{ errorMessage }}</p>
    <!-- 查重提示里「把这次下载移到废纸篓」走片库同一个删除宿主：进度、磁盘不支持废纸篓时的二选一、整批撤销。 -->
    <TrashUndoBanner ref="trashUndo" kind="video" :after-restore="afterTrashRestore" />

    <div class="downloads-summary" data-test="downloads-summary">
      <article>
        <span>进行中</span>
        <strong>{{ activeTasks.length }}</strong>
        <small>{{ activeHintText }}</small>
      </article>
      <article>
        <span>排队</span>
        <strong>{{ queuedCount }}</strong>
        <small>{{ queuedCount > 0 ? '等前面的跑完' : '没有排队的任务' }}</small>
      </article>
      <article>
        <span>已完成</span>
        <strong>{{ doneCount }}</strong>
        <small>{{ importIssueCount > 0 ? `${importIssueCount} 个没入库` : '都已进入片库' }}</small>
      </article>
      <article :class="{ 'downloads-summary--bad': failedCount > 0 }">
        <span>失败</span>
        <strong>{{ failedCount }}</strong>
        <small>{{ failedCount > 0 ? '展开看原因' : '没有失败的任务' }}</small>
      </article>
    </div>

    <div v-if="tasks.length === 0" class="downloads-empty" data-test="downloads-empty">
      <h3>还没有下载任务</h3>
      <!-- 下载目录不会自动加进扫描目录（D-B05），「下完自动进片库」只在目录已在扫描范围内时成立。 -->
      <p>在浏览器插件里抓到视频后点「发送到 CineInsight」，任务会出现在这里；下载目录在扫描范围内时，下完会自动入库。</p>
    </div>

    <template v-else>
      <div v-if="activeTasks.length || queuedTasks.length" class="downloads-group">
        <h3 class="downloads-group__title">进行中</h3>
        <div class="downloads-list">
          <DownloadRow
            v-for="task in [...activeTasks, ...queuedTasks]"
            :key="task.id"
            :task="task"
            :notice="notices[task.id] || null"
            :busy="busyIds.includes(task.id)"
            @cancel="cancelTask"
            @action="runAction"
            @duplicate-action="runDuplicateAction"
          />
        </div>
      </div>

      <div v-if="finishedTasks.length" class="downloads-group">
        <h3 class="downloads-group__title">已结束</h3>
        <div class="downloads-list">
          <DownloadRow
            v-for="task in finishedTasks"
            :key="task.id"
            :task="task"
            :notice="notices[task.id] || null"
            :busy="busyIds.includes(task.id)"
            @cancel="cancelTask"
            @action="runAction"
            @duplicate-action="runDuplicateAction"
          />
        </div>
      </div>
    </template>
  </section>
</template>

<script>
import {
  AddDownloadDirectoryToScan, CancelBrowserDownloadTask, ClearDownloadDuplicate, CreateVersionGroup, DismissDownloadDuplicate, ListDownloadTasks,
  ReimportDownload, RetryDownload, RevealDownload
} from '../../wailsjs/go/main/App';
import DownloadRow from './downloads/DownloadRow.vue';
import TrashUndoBanner from './video-list/TrashUndoBanner.vue';
import { confirmAction } from '../utils/feedback.js';

// 后端从不发出 remuxing（MEDIA-15），状态集合以 browser_download_service.go 为准。
const ACTIVE_STATES = ['running', 'importing'];
const TERMINAL_STATES = ['done', 'failed', 'canceled', 'interrupted'];

// 动作结果码（后端 BrowserDownloadCode*）的中文兜底：后端通常带 message，没带时用这里的说法。
// directory_* 三个是「加入扫描目录」的预检结果：加进去也不会入库，所以不当失败处理，
// 而是说清楚要去设置页做什么。
const ACTION_CODE_TEXTS = {
  directory_excluded: { level: 'warn', text: '下载目录在扫描黑名单里，加入扫描目录也不会入库，请先在设置页把它移出黑名单。' },
  directory_nested: { level: 'warn', text: '下载目录包含已有的扫描目录，直接加入会让扫描目录互相嵌套，请在设置页调整扫描目录。' },
  directory_missing: { level: 'error', text: '下载目录已不存在。' },
  not_in_scan_roots: { level: 'warn', text: '下载目录还不在片库扫描目录里。' },
  import_failed: { level: 'error', text: '入库没有成功。' },
  retry_requires_browser: { level: 'warn', text: '应用重启后下载请求已失效，请回浏览器重新推送。' },
  not_retryable: { level: 'warn', text: '只有失败或已取消的任务可以重试。' },
  queue_full: { level: 'warn', text: '下载队列已满，等前面的任务跑完再试。' },
  not_finished: { level: 'warn', text: '任务还没有下载完成。' },
  file_missing: { level: 'error', text: '下载的文件已经不在了。' },
  reveal_failed: { level: 'error', text: '无法打开所在目录。' },
  service_unavailable: { level: 'error', text: '下载服务未启用。' },
  task_not_found: { level: 'error', text: '没有这个下载任务。' },
  duplicate_not_found: { level: 'warn', text: '这条提示已经处理过了。' }
};

const ACTION_SUCCESS_TEXTS = {
  retry: '已重新排队下载',
  'add-directory': '已把下载目录加入扫描目录并入库',
  reimport: '已重新入库',
  reveal: '',
  dismiss: '已从提示里拿掉'
};

// 插件推过来的下载任务的独立页面。
//
// 为什么不放在设置页：进度是会动的东西，设置是"改完就走"的地方，把长任务塞在那里
// 既看不到又要手动刷新。这里由后端推 browser-download-tasks 事件驱动，不轮询。
export default {
  name: 'DownloadsPage',
  components: { DownloadRow, TrashUndoBanner },
  data() {
    return { tasks: [], errorMessage: '', unsubscribe: null, hiddenIds: [], notices: {}, busyIds: [] };
  },
  computed: {
    visibleTasks() {
      return this.tasks.filter((task) => !this.hiddenIds.includes(task.id));
    },
    activeTasks() {
      return this.visibleTasks.filter((task) => ACTIVE_STATES.includes(task.state));
    },
    queuedTasks() {
      return this.visibleTasks.filter((task) => task.state === 'queued');
    },
    finishedTasks() {
      return this.visibleTasks.filter((task) => TERMINAL_STATES.includes(task.state));
    },
    queuedCount() {
      return this.queuedTasks.length;
    },
    doneCount() {
      return this.visibleTasks.filter((task) => task.state === 'done').length;
    },
    failedCount() {
      return this.visibleTasks.filter((task) => task.state === 'failed').length;
    },
    importIssueCount() {
      return this.visibleTasks.filter((task) => task.state === 'done' && task.import_error).length;
    },
    activeHintText() {
      if (this.activeTasks.length === 0) return '当前没有任务在跑';
      return this.activeTasks.some((task) => task.state === 'importing') ? '含入库中' : '正在下载';
    }
  },
  mounted() {
    this.loadTasks();
    // 后端推送是唯一的刷新来源；这里不做轮询。
    if (window.runtime?.EventsOn) {
      this.unsubscribe = window.runtime.EventsOn('browser-download-tasks', (tasks) => {
        this.tasks = tasks || [];
      });
    }
  },
  beforeUnmount() {
    if (typeof this.unsubscribe === 'function') this.unsubscribe();
  },
  methods: {
    async loadTasks() {
      try {
        // 本次会话的任务 + 表里的历史（D-PC21），与 browser-download-tasks 事件同一份载荷。
        this.tasks = (await ListDownloadTasks()) || [];
        this.errorMessage = '';
      } catch (err) {
        this.tasks = [];
        this.errorMessage = `读取下载任务失败：${err}`;
      }
    },
    async cancelTask(id) {
      try {
        await CancelBrowserDownloadTask(id);
        await this.loadTasks();
      } catch (err) {
        this.errorMessage = `取消任务失败：${err}`;
      }
    },
    // 重试 / 加入扫描目录 / 重新入库 / 打开所在目录（D-PC25）。结果按行显示，
    // 不用一条全页报错盖住其他任务。
    async runAction({ action, task }) {
      const call = {
        retry: RetryDownload,
        'add-directory': AddDownloadDirectoryToScan,
        reimport: ReimportDownload,
        reveal: RevealDownload
      }[action];
      if (!call || !task?.id || this.busyIds.includes(task.id)) return;
      this.busyIds = [...this.busyIds, task.id];
      this.setNotice(task.id, null);
      try {
        const result = await call(task.id);
        this.applyActionResult(action, task, result);
      } catch (err) {
        this.setNotice(task.id, { level: 'error', text: `操作失败：${err}` });
      } finally {
        this.busyIds = this.busyIds.filter(id => id !== task.id);
      }
    },
    applyActionResult(action, task, result) {
      const code = result?.code || 'ok';
      if (result?.task?.id) this.mergeTask(result.task);
      if (code === 'ok') {
        const text = ACTION_SUCCESS_TEXTS[action];
        this.setNotice(task.id, text ? { level: 'success', text } : null);
        return;
      }
      const known = ACTION_CODE_TEXTS[code];
      if (code === 'not_in_scan_roots' || code === 'import_failed') {
        // 原因句已经在「文件已保存，但没有入库：…」那一行里了，这里不再重复一遍（MEDIA-15）。
        this.setNotice(task.id, { level: 'warn', text: '这次入库仍没有成功，原因见上方说明。' });
        return;
      }
      const text = String(result?.message || '').trim() || known?.text || `操作没有完成（${code}）`;
      this.setNotice(task.id, { level: known?.level || 'error', text });
    },
    // 查重提示的处理（D-DD-04）：把这次下载移到废纸篓、和对方建立版本组、不是同一部 / 保留两份。
    async runDuplicateAction({ action, task, match }) {
      if (!task?.id || !match?.video_id || this.busyIds.includes(task.id)) return;
      if (action === 'trash') return this.trashDownload(task, match);
      this.busyIds = [...this.busyIds, task.id];
      this.setNotice(task.id, null);
      try {
        if (action === 'version-group') {
          await CreateVersionGroup([task.video_id, match.video_id], '');
          // 建好版本组就算处理过了：从提示里拿掉（不写「不是同一部」的忽略）。
          const cleared = await ClearDownloadDuplicate(task.id, match.video_id);
          if (cleared?.task?.id) this.mergeTask(cleared.task);
          this.setNotice(task.id, { level: 'success', text: `已和《${match.title}》建立版本组` });
        } else if (action === 'dismiss') {
          this.applyActionResult('dismiss', task, await DismissDownloadDuplicate(task.id, match.video_id));
        }
      } catch (err) {
        this.setNotice(task.id, { level: 'error', text: `操作失败：${err}` });
      } finally {
        this.busyIds = this.busyIds.filter(id => id !== task.id);
      }
    },
    // 删之前按此刻的片库重取一次：提示可能已经过期（对方或这次下载已被删、文件换过），过期就不删。
    async trashDownload(staleTask, staleMatch) {
      await this.loadTasks();
      const task = this.tasks.find(item => item.id === staleTask.id);
      const match = (task?.duplicate?.status === 'found' ? task.duplicate.matches || [] : []).find(item => item.video_id === staleMatch.video_id);
      if (!task?.video_id || task.video_id !== staleTask.video_id || !match) {
        this.setNotice(staleTask.id, { level: 'warn', text: '这条提示已经过期，已按当前片库刷新。' });
        return;
      }
      const name = task.filename || task.title || '这次下载';
      const confirmed = await confirmAction({
        title: '把这次下载移到废纸篓',
        message: `把「${name}」移到废纸篓？片库里保留《${match.title}》。之后可在回收站撤销。`,
        confirmText: '移到废纸篓',
        danger: true
      });
      if (!confirmed || this.busyIds.includes(task.id)) return;
      this.busyIds = [...this.busyIds, task.id];
      try {
        const banner = this.$refs.trashUndo;
        const outcome = await banner.runDelete({ ids: [task.video_id], deleteFile: true, names: { [task.video_id]: name } });
        banner.showDeleteNotice(outcome);
        if (outcome.removedIDs.length) await this.loadTasks();
      } catch (err) {
        this.setNotice(task.id, { level: 'error', text: `移到废纸篓失败：${err}` });
      } finally {
        this.busyIds = this.busyIds.filter(id => id !== task.id);
      }
    },
    async afterTrashRestore() {
      await this.loadTasks();
    },
    // 动作结果里带回的任务快照先就地换上；随后的推送仍是唯一的整体刷新来源。
    mergeTask(fresh) {
      const index = this.tasks.findIndex(item => item.id === fresh.id);
      if (index < 0) return;
      this.tasks.splice(index, 1, { ...this.tasks[index], ...fresh });
    },
    setNotice(id, notice) {
      const next = { ...this.notices };
      if (notice) next[id] = notice;
      else delete next[id];
      this.notices = next;
    },
    // 只从这一次的界面里收起来。后端仍然留着最近的记录，重开页面还看得到——
    // 不假装这是"删除"。
    clearFinished() {
      this.hiddenIds = [...this.hiddenIds, ...this.finishedTasks.map((task) => task.id)];
    }
  }
};
</script>

<style scoped>
.downloads-page {
  padding: 20px 24px 40px;
  max-width: 1040px;
  margin: 0 auto;
}

.downloads-heading {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 18px;
}

.downloads-heading h2 { margin: 0 0 4px; }
.downloads-heading p { margin: 0; color: var(--text-secondary); font-size: 13px; }

.downloads-summary {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
  gap: 12px;
  margin-bottom: 22px;
}

.downloads-summary article {
  display: grid;
  gap: 2px;
  padding: 12px 14px;
  border-radius: 10px;
  background: var(--bg-color);
  border: 1px solid var(--border-color, transparent);
}

.downloads-summary span { color: var(--text-secondary); font-size: 12px; }
.downloads-summary strong { font-size: 24px; line-height: 1.2; }
.downloads-summary small { color: var(--text-secondary); font-size: 12px; }
.downloads-summary--bad strong { color: var(--danger-color, #c0392b); }

.downloads-group { margin-bottom: 24px; }

.downloads-group__title {
  margin: 0 0 10px;
  font-size: 13px;
  font-weight: 600;
  color: var(--text-secondary);
}

.downloads-list { display: grid; gap: 10px; }

.downloads-empty {
  padding: 40px 24px;
  text-align: center;
  border-radius: 12px;
  background: var(--bg-color);
}

.downloads-empty h3 { margin: 0 0 6px; }
.downloads-empty p { margin: 0; color: var(--text-secondary); }

.downloads-error {
  padding: 12px 14px;
  border-radius: 8px;
  margin-bottom: 16px;
  color: var(--danger-color, #c0392b);
  background: var(--bg-color);
}
</style>
