<template>
  <!-- 失联数据（docs/loopx/design/2026-10-12-data-care，D-LD-*）：按扫描根 → 目录分组，找回、清除与孤儿文件回收。 -->
  <section class="data-care-panel lost-media" data-test="data-care-panel-lost">
    <div class="lost-toolbar">
      <div class="lost-kinds" role="group" aria-label="媒体类型">
        <button
          v-for="option in KINDS"
          :key="option.key"
          type="button"
          :class="['toolbar-btn', 'toolbar-btn--compact', { 'toolbar-btn--on': kind === option.key }]"
          :disabled="busy"
          :data-test="`lost-kind-${option.key}`"
          @click="setKind(option.key)"
        >{{ option.label }}</button>
      </div>
      <span class="lost-muted">卷未挂载、读取出错的只展示，接上盘或下次扫描会自动恢复</span>
      <span class="lost-spacer"></span>
      <button type="button" class="btn-secondary btn-compact" :disabled="busy || !report || report.purgeable === 0" data-test="lost-search-all" @click="startSearch(null)">在文件夹里找</button>
      <button type="button" class="btn-secondary btn-compact" :disabled="busy || loading" data-test="lost-refresh" @click="loadReport">刷新</button>
    </div>

    <p v-if="error" class="lost-alert" role="alert" data-test="lost-error">{{ error }}</p>
    <p v-if="loading && !report" class="lost-empty" data-test="lost-loading">正在读取失联条目…</p>

    <template v-else-if="report">
      <div class="lost-summary" data-test="lost-summary">
        <strong>{{ report.total }}</strong>
        <span>条失联{{ kindNoun }} · {{ formatBytes(report.total_size) }}</span>
        <span class="lost-chip lost-chip--ok">可找回或清除 {{ report.purgeable }}</span>
        <span v-for="chip in reasonChips(report.reasons)" :key="chip.key" :class="['lost-chip', { 'lost-chip--wait': !chip.actionable }]">{{ chip.label }} {{ chip.count }}</span>
      </div>
      <p v-if="report.total === 0" class="lost-empty" data-test="lost-empty">没有失联的{{ kindNoun }}。</p>

      <article v-for="root in report.roots" :key="root.root || 'other'" class="lost-root" data-test="lost-root">
        <header class="lost-root__head">
          <strong :title="root.root">{{ root.label }}</strong>
          <span v-if="root.state === 'removed'" class="lost-chip lost-chip--wait">已移除的扫描目录</span>
          <span class="lost-muted">{{ root.count }} 条 · {{ formatBytes(root.size) }}</span>
          <span class="lost-spacer"></span>
          <button
            v-if="root.root"
            type="button"
            class="btn-secondary btn-compact"
            :disabled="busy || root.purgeable === 0"
            data-test="lost-root-move"
            @click="startMove(root.root)"
          >整个目录搬家</button>
        </header>
        <ul class="lost-dirs">
          <li v-for="dir in root.directories" :key="dir.directory" class="lost-dir" data-test="lost-directory">
            <div class="lost-dir__row">
              <div class="lost-dir__main">
                <span class="lost-path" :title="dir.directory">{{ dir.directory }}</span>
                <span class="lost-dir__chips">
                  <span class="lost-muted">{{ dir.count }} 条 · {{ formatBytes(dir.size) }}</span>
                  <span v-for="chip in reasonChips(dir.reasons)" :key="chip.key" :class="['lost-chip', { 'lost-chip--wait': !chip.actionable }]">{{ chip.label }} {{ chip.count }}</span>
                  <span v-if="dir.purgeable" class="lost-chip lost-chip--ok" data-test="lost-dir-purgeable">可清除 {{ dir.purgeable }}</span>
                  <span v-else class="lost-chip lost-chip--wait" data-test="lost-dir-wait">只展示</span>
                </span>
              </div>
              <button type="button" class="row-btn" data-test="lost-dir-expand" @click="toggleDirectory(dir)">{{ expanded === dir.directory ? '收起' : '明细' }}</button>
              <button type="button" class="row-btn" :disabled="busy || !dir.purgeable" data-test="lost-dir-move" @click="startMove(dir.directory)">目录搬家</button>
              <button type="button" class="row-btn" :disabled="busy || !dir.purgeable" data-test="lost-dir-search" @click="startSearch(dir)">在文件夹里找</button>
              <button type="button" class="row-btn row-btn--danger" :disabled="busy || !dir.purgeable" data-test="lost-dir-purge" @click="startPurge(dir)">清除</button>
            </div>
            <div v-if="expanded === dir.directory" class="lost-items" data-test="lost-items">
              <p class="lost-muted">勾选后「在文件夹里找」与「清除」只处理勾选的条目；不勾选时处理这个目录里全部可处理的条目。</p>
              <ul>
                <li v-for="item in items" :key="item.id" class="lost-item" data-test="lost-item">
                  <input
                    type="checkbox"
                    :checked="selected.has(item.id)"
                    :disabled="!item.purgeable || busy"
                    :aria-label="`选择 ${item.name}`"
                    data-test="lost-item-toggle"
                    @change="toggleItem(item.id)"
                  />
                  <span class="lost-item__name" :title="item.path">{{ item.name }}</span>
                  <span :class="['lost-chip', { 'lost-chip--wait': !item.purgeable }]">{{ reasonLabel(item.reason) }}</span>
                  <span v-if="item.source === 'scanner_trash'" class="lost-chip">{{ kind === 'image' ? '扫描隐藏' : '回收站' }}</span>
                  <small class="lost-muted">{{ formatBytes(item.size) }}</small>
                </li>
              </ul>
              <button v-if="itemsHasMore" type="button" class="btn-secondary btn-compact" :disabled="itemsLoading" data-test="lost-items-more" @click="loadItems(false)">加载更多</button>
            </div>
          </li>
        </ul>
      </article>
    </template>

    <OrphanFilesSection ref="orphans" />

    <LostRelinkDialog
      v-if="relinkDialog"
      :kind="kind"
      :title="relinkDialog.title"
      :preview="relinkDialog.preview"
      @done="afterChange($event, [])"
      @close="relinkDialog = null"
    />
    <LostPurgeDialog
      v-if="purgeDialog"
      :kind="kind"
      :ids="purgeDialog.ids"
      @backup="$emit('run-command', 'action:backup-now', '立即备份数据库')"
      @done="afterChange($event, purgeDialog.ids)"
      @close="purgeDialog = null"
    />
  </section>
</template>

<script>
import { GetLostMediaReport, ListLostMediaItems, PreviewLostRelinkMove, PreviewLostRelinkSearch, SelectDirectory } from '../../../wailsjs/go/main/App';
import { formatBytes } from '../../utils/mediaDetails.js';
import { collectLostItems, LOST_REASON_LABELS, lostMediaErrorText, reasonChips } from '../../utils/lostMedia.js';
import LostPurgeDialog from './LostPurgeDialog.vue';
import LostRelinkDialog from './LostRelinkDialog.vue';
import OrphanFilesSection from './OrphanFilesSection.vue';

const KINDS = [{ key: 'video', label: '视频' }, { key: 'image', label: '图片' }];
const PAGE_SIZE = 100;

export default {
  name: 'LostMediaPanel',
  components: { LostPurgeDialog, LostRelinkDialog, OrphanFilesSection },
  props: { directories: { type: Array, default: () => [] } },
  emits: ['open-video-id', 'run-command', 'videos-changed', 'open-library-missing', 'navigate'],
  data() {
    return {
      KINDS,
      kind: 'video',
      report: null,
      loading: false,
      error: '',
      busy: false,
      expanded: '',
      items: [],
      itemsHasMore: false,
      itemsLoading: false,
      selected: new Set(),
      relinkDialog: null,
      purgeDialog: null
    };
  },
  computed: {
    kindNoun() {
      return this.kind === 'image' ? '图片' : '视频';
    }
  },
  mounted() {
    this.loadReport();
  },
  methods: {
    formatBytes,
    reasonChips,
    reasonLabel(reason) {
      return LOST_REASON_LABELS[reason] || reason;
    },
    async loadReport() {
      this.loading = true;
      this.error = '';
      try {
        this.report = await GetLostMediaReport(this.kind);
      } catch (err) {
        this.error = lostMediaErrorText(err, '读取失联条目失败，请稍后重试。');
      } finally {
        this.loading = false;
      }
      if (this.expanded && !this.report?.roots?.some(root => root.directories.some(dir => dir.directory === this.expanded))) {
        this.expanded = '';
      } else if (this.expanded) {
        await this.loadItems(true);
      }
    },
    setKind(kind) {
      if (kind === this.kind) return;
      this.kind = kind;
      this.report = null;
      this.expanded = '';
      this.loadReport();
    },
    async toggleDirectory(dir) {
      if (this.expanded === dir.directory) {
        this.expanded = '';
        return;
      }
      this.expanded = dir.directory;
      await this.loadItems(true);
    },
    async loadItems(reset) {
      const directory = this.expanded;
      this.itemsLoading = true;
      try {
        const afterID = reset || !this.items.length ? 0 : this.items[this.items.length - 1].id;
        const page = await ListLostMediaItems({ kind: this.kind, directory, after_id: afterID, limit: PAGE_SIZE });
        if (directory !== this.expanded) return;
        this.items = reset ? page.items : [...this.items, ...page.items];
        this.itemsHasMore = page.has_more;
        if (reset) this.selected = new Set();
      } catch (err) {
        this.error = lostMediaErrorText(err, '读取失联条目失败，请稍后重试。');
      } finally {
        this.itemsLoading = false;
      }
    },
    toggleItem(id) {
      const next = new Set(this.selected);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      this.selected = next;
    },
    // targetIDs 是一个目录分组要处理的条目：展开且勾选了就用勾选的，否则逐页读出这个目录里全部可处理的。
    async targetIDs(dir) {
      if (this.expanded === dir.directory && this.selected.size) return [...this.selected];
      const items = await collectLostItems(ListLostMediaItems, this.kind, dir.directory);
      return items.filter(item => item.purgeable).map(item => item.id);
    },
    async run(task, fallback) {
      this.busy = true;
      this.error = '';
      try {
        await task();
      } catch (err) {
        this.error = lostMediaErrorText(err, fallback);
      } finally {
        this.busy = false;
      }
    },
    startMove(oldDirectory) {
      return this.run(async () => {
        const newDirectory = await SelectDirectory();
        if (!newDirectory) return;
        const preview = await PreviewLostRelinkMove({ kind: this.kind, old_directory: oldDirectory, new_directory: newDirectory });
        this.relinkDialog = { title: '目录搬家', preview };
      }, '配对没有完成，请稍后重试。');
    },
    startSearch(dir) {
      return this.run(async () => {
        const directory = await SelectDirectory();
        if (!directory) return;
        const ids = dir ? await this.targetIDs(dir) : [];
        if (dir && !ids.length) return;
        const preview = await PreviewLostRelinkSearch({ kind: this.kind, directory, item_ids: ids });
        this.relinkDialog = { title: '在文件夹里找', preview };
      }, '查找没有完成，请稍后重试。');
    },
    startPurge(dir) {
      return this.run(async () => {
        const ids = await this.targetIDs(dir);
        if (ids.length) this.purgeDialog = { ids };
      }, '读取失联条目失败，请稍后重试。');
    },
    // afterChange：找回或清除有条目生效后通知宿主重载计数，并重读报告与孤儿统计。
    afterChange(result, ids) {
      if (!result?.done) return;
      const notDone = new Set((result.items || []).map(item => item.id));
      const removedIDs = this.kind === 'video' && !result.cancelled ? ids.filter(id => !notDone.has(id)) : [];
      this.$emit('videos-changed', { removedIDs });
      this.loadReport();
      this.$refs.orphans?.reload();
    }
  }
};
</script>

<style scoped>
.lost-media { display: grid; gap: 16px; }
.lost-toolbar { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 12px; }
.lost-kinds { display: inline-flex; gap: 6px; }
.lost-spacer { flex: 1; }
.lost-muted { margin: 0; color: var(--text-muted); font-size: 12px; }
.lost-alert { margin: 0; padding: 9px 14px; border: 1px solid var(--danger-color); border-radius: 10px; color: var(--danger-color); font-size: 12px; }
.lost-empty { padding: 36px 8px; color: var(--text-secondary); text-align: center; }
.lost-summary { display: flex; flex-wrap: wrap; align-items: baseline; gap: 6px 10px; }
.lost-summary strong { font-size: 30px; font-weight: 800; line-height: 1; font-variant-numeric: tabular-nums; }
.lost-chip { padding: 1px 8px; border-radius: 999px; background: var(--control-bg); color: var(--text-secondary); font-size: 11px; white-space: nowrap; }
.lost-chip--ok { background: var(--accent-soft); color: var(--accent-text); }
.lost-chip--wait { background: var(--warning-soft); color: var(--warning-text); }
.lost-root { display: grid; gap: 8px; padding: 14px 16px; border: 1px solid var(--hairline); border-radius: var(--radius-lg); background: var(--panel-bg); }
.lost-root__head { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 10px; }
.lost-dirs { display: grid; gap: 6px; margin: 0; padding: 0; list-style: none; }
.lost-dir { display: grid; gap: 6px; padding: 8px 10px; border-radius: 10px; }
.lost-dir:hover { background: var(--control-hover-bg); }
.lost-dir__row { display: flex; align-items: center; gap: 8px; }
.lost-dir__main { display: grid; flex: 1; gap: 4px; min-width: 0; }
.lost-dir__chips { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
.lost-path { overflow: hidden; font-size: 12.5px; font-family: var(--font-mono, monospace); text-overflow: ellipsis; white-space: nowrap; }
.row-btn--danger { color: var(--danger-color); }
.lost-items { display: grid; gap: 6px; padding-left: 8px; }
.lost-items ul { display: grid; gap: 4px; margin: 0; padding: 0; list-style: none; }
.lost-item { display: flex; align-items: center; gap: 8px; font-size: 12.5px; }
.lost-item__name { overflow: hidden; flex: 1; min-width: 0; text-overflow: ellipsis; white-space: nowrap; }
</style>
