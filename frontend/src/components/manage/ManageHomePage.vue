<template>
  <section class="page-content manage-home" data-test="manage-home">
    <!-- 管理首页（D-UI-03）：四张统计卡点开对应动作；「最近的变化」只列本次会话收到的事件。 -->
    <PageHeader title="管理" subtitle="维护片库：待处理的事、能回收的空间、在跑的任务和磁盘状态" />

    <div class="manage-home__cards" data-test="manage-home-cards">
      <StatTile
        v-for="card in cards"
        :key="card.key"
        tag="button"
        :tone="card.tone"
        :icon="card.icon"
        :value="card.value"
        :unit="card.unit"
        :label="card.label"
        :note="card.note"
        :aria-busy="card.loading ? 'true' : undefined"
        :data-test="`manage-card-${card.key}`"
        @click="openCard(card.key)"
      />
    </div>

    <section class="recap-section" data-test="manage-home-changes">
      <header class="recap-section__head">
        <h3>最近的变化</h3>
        <small>本次打开应用以来收到的扫描、下载查重与备份</small>
      </header>
      <ul v-if="changes.length" class="manage-home__changes">
        <li v-for="item in changes" :key="item.id">
          <button
            type="button"
            class="btn manage-home__change"
            :disabled="!item.page"
            data-test="manage-change"
            @click="item.page && $emit('navigate', item.page)"
          >
            <time>{{ formatClock(item.at) }}</time>
            <strong>{{ item.title }}</strong>
            <span>{{ item.detail }}</span>
          </button>
        </li>
      </ul>
      <p v-else class="recap-muted" data-test="manage-changes-empty">本次打开应用以来还没有新的扫描、下载查重或备份。</p>
      <button
        v-if="allChanges.length > MANAGE_CHANGES_SHOWN"
        type="button"
        class="btn btn--secondary btn--sm manage-home__changes-toggle"
        data-test="manage-changes-toggle"
        @click="changesExpanded = !changesExpanded"
      >{{ changesExpanded ? '收起' : `显示全部 ${allChanges.length} 条` }}</button>
    </section>
  </section>
</template>

<script>
// 四张卡各自取数、各自失败：一张读不到只在这张卡上写一句，不影响别的卡。
//   待处理 → GetPendingWorkSummary；可回收 → 两份清理分析结果按存储体检的 reclaimSummary 口径折算；
//   运行中的任务 → 登记表快照（GetBackgroundTasks，之后跟 background-tasks 事件）；
//   离线的硬盘 → 扫描目录 + 实时同步的根状态（unavailable 即目录当前不可访问），按卷合并。
// 「最近的变化」读 utils/sessionChanges.js，由 App 在已有的事件处理里喂数据。
import { markRaw } from 'vue';
import {
  GetAllDirectories, GetBackgroundTasks, GetCleanupStatus, GetImageCleanupStatus, GetLibraryWatcherStatus, GetPendingWorkSummary
} from '../../../wailsjs/go/main/App';
import PageHeader from '../ui/PageHeader.vue';
import StatTile from '../ui/StatTile.vue';
import { runtimeEventsMixin } from '../video-list/runtimeEvents.js';
import { translateBackendError } from '../../utils/feedback.js';
import { backgroundTaskLabel } from '../../utils/idleScheduling.js';
import { formatClock } from '../../utils/recap.js';
import { sessionChanges } from '../../utils/sessionChanges.js';
import { reclaimHeadline, reclaimSummary } from '../../utils/storageReclaim.js';

export const MANAGE_CHANGES_SHOWN = 10;

// 统计卡图标：图标集 ui/icons.js 里的名字（D-UD-06）。
const ICONS = { pending: 'inbox', reclaim: 'trash', tasks: 'activity', disks: 'disk' };

// 扫描目录所在的卷：/Volumes/<名字> 一块盘，其余算系统盘（与洞察页的卷统计同一口径）。
export function volumeOf(path) {
  const match = String(path || '').match(/^(\/Volumes\/[^/]+|\/)/);
  return match ? match[1] : String(path || '');
}

// 只有实时同步在看着的根（watching / unavailable）才知道在不在线；其余状态不猜。
export function offlineDisks(directories, watcherStatus) {
  const states = new Map((watcherStatus?.roots || []).map(root => [Number(root.directory_id), root.state]));
  let known = 0;
  const offline = [];
  for (const directory of directories || []) {
    const state = states.get(Number(directory?.id));
    if (state === 'watching' || state === 'unavailable') known++;
    if (state === 'unavailable') offline.push(directory);
  }
  return { total: (directories || []).length, known, offlineDirectories: offline.length, offlineVolumes: new Set(offline.map(item => volumeOf(item.path))).size };
}

function blankBlock() {
  return { loading: true, error: '' };
}

function failureText(err) {
  return `读取失败：${translateBackendError(err?.message || err)}`;
}

export default {
  name: 'ManageHomePage',
  components: { PageHeader, StatTile },
  mixins: [runtimeEventsMixin],
  // pageActive：宿主把页面切回来时为 true，这里静默重取一遍（离开期间计数可能变了）。
  props: { pageActive: { type: Boolean, default: true } },
  emits: ['navigate', 'run-command', 'open-pending-work', 'open-task-center'],
  data() {
    return {
      pending: { ...blankBlock(), summary: null },
      reclaim: { ...blankBlock(), video: null, image: null },
      tasks: { ...blankBlock(), running: [] },
      disks: { ...blankBlock(), directories: [], watcher: null },
      generation: 0,
      // 「最近的变化」默认列前 MANAGE_CHANGES_SHOWN 条，内存里最多留 50 条（SESSION_CHANGE_LIMIT），展开看全部。
      changesExpanded: false,
      MANAGE_CHANGES_SHOWN
    };
  },
  computed: {
    allChanges() { return sessionChanges(); },
    changes() { return this.changesExpanded ? this.allChanges : this.allChanges.slice(0, MANAGE_CHANGES_SHOWN); },
    cards() {
      const card = (key, label, block, describe) => {
        if (block.loading && !block.ready) return { key, label, loading: true, value: '…', unit: '', note: '正在读取' };
        if (block.error) return { key, label, loading: false, value: '—', unit: '', note: block.error };
        return { key, label, loading: false, unit: '', ...describe() };
      };
      return [
        { tone: 'accent', icon: ICONS.pending, ...card('pending', '待处理', this.pending, () => {
          const total = Number(this.pending.summary?.total || 0);
          return { value: total, unit: '项', note: total > 0 ? '审阅、清理与资料更新，点开逐项处理' : '没有待处理的事项' };
        }) },
        { tone: 'danger', icon: ICONS.reclaim, ...card('reclaim', '可回收', this.reclaim, () => reclaimHeadline(reclaimSummary(this.reclaim.video, this.reclaim.image))) },
        { tone: 'accent', icon: ICONS.tasks, ...card('tasks', '运行中的任务', this.tasks, () => {
          const running = this.tasks.running;
          const names = running.slice(0, 2).map(backgroundTaskLabel).join('、');
          return { value: running.length, unit: '个', note: running.length ? `${names}${running.length > 2 ? ' 等' : ''}` : '没有在跑的后台任务' };
        }) },
        { tone: 'warning', icon: ICONS.disks, ...card('disks', '离线的硬盘', this.disks, () => this.diskHeadline()) }
      ];
    }
  },
  watch: {
    pageActive(active) { if (active) this.load(); }
  },
  mounted() {
    this.registerRuntimeEvent('background-tasks', running => { this.tasks = { loading: false, ready: true, error: '', running: running || [] }; });
    this.registerRuntimeEvent('library-watcher-status', status => { if (this.disks.ready) this.disks = { ...this.disks, watcher: status }; });
    this.load();
  },
  beforeUnmount() {
    this.generation++;
  },
  methods: {
    formatClock,
    // 四块并行；每块只认最新一轮的结果，重取时保留上一轮的数字（ready）直到新结果回来。
    load() {
      const generation = ++this.generation;
      const run = async (name, fetch) => {
        this[name] = { ...this[name], loading: true, error: '' };
        try {
          const patch = await fetch();
          if (generation === this.generation) this[name] = { ...this[name], ...patch, loading: false, ready: true, error: '' };
        } catch (err) {
          if (generation === this.generation) this[name] = { ...this[name], loading: false, error: failureText(err) };
        }
      };
      return Promise.all([
        run('pending', async () => ({ summary: await GetPendingWorkSummary() })),
        run('reclaim', async () => {
          const [video, image] = await Promise.all([GetCleanupStatus(), GetImageCleanupStatus()]);
          return { video: video ? markRaw(video) : null, image: image ? markRaw(image) : null };
        }),
        run('tasks', async () => ({ running: (await GetBackgroundTasks()) || [] })),
        run('disks', async () => {
          const [directories, watcher] = await Promise.all([GetAllDirectories(), GetLibraryWatcherStatus()]);
          return { directories: directories || [], watcher };
        })
      ]);
    },
    diskHeadline() {
      const disks = offlineDisks(this.disks.directories, this.disks.watcher);
      if (disks.total === 0) return { value: 0, unit: '块', note: '还没有添加扫描目录' };
      if (disks.known === 0) return { value: '—', unit: '', note: '实时同步没有在看这些目录，判断不了在不在线' };
      if (disks.offlineVolumes === 0) return { value: 0, unit: '块', note: `${disks.total} 个扫描目录都能访问` };
      return { value: disks.offlineVolumes, unit: '块', note: `${disks.offlineDirectories} 个扫描目录当前不可访问` };
    },
    openCard(key) {
      if (key === 'pending') this.$emit('open-pending-work');
      else if (key === 'reclaim') this.$emit('run-command', 'library.openCleanup', '清理中心');
      else if (key === 'tasks') this.$emit('open-task-center');
      else if (key === 'disks') this.$emit('navigate', 'settings');
    }
  }
};
</script>

<style scoped>
.manage-home { display: grid; gap: 22px; align-content: start; padding-bottom: 56px; }
.manage-home > :deep(.ui-page-header) { margin-bottom: 0; }
.manage-home__cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 14px; }
.manage-home__changes { display: grid; gap: 6px; margin: 0; padding: 0; list-style: none; }
.manage-home__changes-toggle { justify-self: start; }
/* 整行可点的变化记录：沿用 .btn 的焦点环与禁用口径，版式由这里接管。 */
.manage-home__change {
  display: grid;
  grid-template-columns: 48px minmax(120px, auto) minmax(0, 1fr);
  align-items: baseline;
  justify-content: stretch;
  gap: var(--space-3);
  width: 100%;
  height: auto;
  padding: var(--space-3) 14px;
  border: 1px solid var(--hairline);
  border-radius: var(--radius);
  background: var(--panel-bg);
  color: var(--text-primary);
  font-size: var(--fs-secondary);
  font-weight: var(--fw-regular);
  text-align: left;
  white-space: normal;
}
.manage-home__change:hover:not(:disabled) { border-color: var(--accent-border); }
.manage-home__change:disabled { opacity: 1; cursor: default; }
.manage-home__change time { color: var(--text-muted); font-size: var(--fs-caption); font-variant-numeric: tabular-nums; }
.manage-home__change strong { font-size: var(--fs-secondary); font-weight: var(--fw-semibold); }
.manage-home__change span { overflow: hidden; color: var(--text-secondary); font-size: var(--fs-secondary); text-overflow: ellipsis; white-space: nowrap; }
</style>
