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
      <p>在浏览器插件里抓到视频后点「发送到 CineInsight」，任务会出现在这里，下完自动进片库。</p>
    </div>

    <template v-else>
      <div v-if="activeTasks.length || queuedTasks.length" class="downloads-group">
        <h3 class="downloads-group__title">进行中</h3>
        <div class="downloads-list">
          <DownloadRow
            v-for="task in [...activeTasks, ...queuedTasks]"
            :key="task.id"
            :task="task"
            @cancel="cancelTask"
          />
        </div>
      </div>

      <div v-if="finishedTasks.length" class="downloads-group">
        <h3 class="downloads-group__title">已结束</h3>
        <div class="downloads-list">
          <DownloadRow v-for="task in finishedTasks" :key="task.id" :task="task" @cancel="cancelTask" />
        </div>
      </div>
    </template>
  </section>
</template>

<script>
import { ListBrowserDownloadTasks, CancelBrowserDownloadTask } from '../../wailsjs/go/main/App';
import DownloadRow from './downloads/DownloadRow.vue';

const ACTIVE_STATES = ['running', 'remuxing', 'importing'];
const TERMINAL_STATES = ['done', 'failed', 'canceled'];

// 插件推过来的下载任务的独立页面。
//
// 为什么不放在设置页：进度是会动的东西，设置是"改完就走"的地方，把长任务塞在那里
// 既看不到又要手动刷新。这里由后端推 browser-download-tasks 事件驱动，不轮询。
export default {
  name: 'DownloadsPage',
  components: { DownloadRow },
  data() {
    return { tasks: [], errorMessage: '', unsubscribe: null, hiddenIds: [] };
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
      return this.activeTasks.some((task) => task.state === 'remuxing') ? '含转封装' : '正在下载';
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
        this.tasks = (await ListBrowserDownloadTasks()) || [];
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
