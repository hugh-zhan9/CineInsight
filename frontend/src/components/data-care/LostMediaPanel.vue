<template>
  <!-- 失联数据（docs/loopx/design/2026-10-12-data-care，D-LD-*）：按扫描根 → 目录分组，找回、清除与孤儿文件回收。 -->
  <section class="data-care-panel lost-media" data-test="data-care-panel-lost">
    <div class="lost-toolbar">
      <!-- 视频 / 图片（D-UD-06 SegmentedControl），每项带 lost-kind-* 钩子；处理中锁住。 -->
      <SegmentedControl label="媒体类型" :options="kindOptions" :model-value="kind" @update:model-value="setKind" />
      <span class="lost-muted">卷未挂载、读取出错的只展示，接上盘或下次扫描会自动恢复</span>
      <span class="lost-spacer"></span>
      <button
        type="button"
        class="btn btn--secondary btn--sm"
        :disabled="busy || !report || report.purgeable === 0"
        title="选一个文件夹，为全部可处理的失联条目找对应的文件"
        data-test="lost-search-all"
        @click="startSearch(null)"
      >从文件夹找回全部…</button>
      <button type="button" class="btn btn--secondary btn--sm" :disabled="busy || loading" data-test="lost-refresh" @click="loadReport">刷新</button>
    </div>

    <AppBanner v-if="error" tone="danger" :title="error" data-test="lost-error" />
    <div v-if="loading && !report" class="lost-loading" role="status" aria-label="正在读取失联条目" data-test="lost-loading">
      <SkeletonBlock :lines="4" />
    </div>

    <template v-else-if="report">
      <div class="lost-summary" data-test="lost-summary">
        <strong>{{ report.total }}</strong>
        <span>条失联{{ kindNoun }} · {{ formatBytes(report.total_size) }}</span>
        <span class="lost-chip lost-chip--ok">可找回或清除 {{ report.purgeable }}</span>
        <span v-for="chip in reasonChips(report.reasons)" :key="chip.key" :class="['lost-chip', { 'lost-chip--wait': !chip.actionable }]">{{ chip.label }} {{ chip.count }}</span>
      </div>
      <EmptyState v-if="report.total === 0" icon="check" :title="`没有失联的${kindNoun}`" message="扫描时发现找不到的文件会列在这里，可以找回或清除。" data-test="lost-empty" />

      <article v-for="root in report.roots" :key="root.root || 'other'" class="lost-root" data-test="lost-root">
        <header class="lost-root__head">
          <strong :title="root.root">{{ root.label }}</strong>
          <span v-if="root.state === 'removed'" class="lost-chip lost-chip--wait">已移除的扫描目录</span>
          <span class="lost-muted">{{ root.count }} 条 · {{ formatBytes(root.size) }}</span>
          <span class="lost-spacer"></span>
          <button
            v-if="root.root"
            type="button"
            class="btn btn--secondary btn--sm"
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
              <button type="button" class="btn btn--secondary btn--sm" data-test="lost-dir-expand" @click="toggleDirectory(dir)">{{ expanded === dir.directory ? '收起' : '明细' }}</button>
              <!-- 目录行上的三个按钮处理整个目录。这个目录里有勾选时禁用（破坏性流程复审 I-3）：
                   勾了几条再点行上的「清除…」，很容易以为只处理勾选的；那时请用底部的操作条。 -->
              <button
                type="button"
                class="btn btn--secondary btn--sm"
                :disabled="busy || !dir.purgeable || dirHasSelection(dir)"
                :title="dirHasSelection(dir) ? SELECTION_PENDING_TITLE : null"
                data-test="lost-dir-move"
                @click="startMove(dir.directory)"
              >目录搬家</button>
              <button
                type="button"
                class="btn btn--secondary btn--sm"
                :disabled="busy || !dir.purgeable || dirHasSelection(dir)"
                :title="dirHasSelection(dir) ? SELECTION_PENDING_TITLE : `选一个文件夹，为这个目录里全部 ${dir.purgeable} 条可处理的条目找对应的文件`"
                data-test="lost-dir-search"
                @click="startSearch(dir)"
              >从文件夹找回…</button>
              <button
                type="button"
                class="btn btn--danger btn--sm"
                :disabled="busy || !dir.purgeable || dirHasSelection(dir)"
                :title="dirHasSelection(dir) ? SELECTION_PENDING_TITLE : `清除这个目录里全部 ${dir.purgeable} 条可处理的记录，先列出连带影响再确认`"
                data-test="lost-dir-purge"
                @click="startPurge(dir)"
              >清除…</button>
            </div>
            <div v-if="expanded === dir.directory" class="lost-items" data-test="lost-items">
              <p class="lost-muted">目录行上的按钮处理整个目录；只处理其中几条时勾选它们，用底部的操作条。</p>
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
              <button v-if="itemsHasMore" type="button" class="btn btn--secondary btn--sm lost-items__more" :disabled="itemsLoading" data-test="lost-items-more" @click="loadItems(false)">加载更多</button>
              <!-- 选择操作条（D-UD-10）：只处理勾选的条目；要处理整个目录点「全选当前结果（N）」，或用目录行上的按钮。 -->
              <SelectionBar
                :count="selected.size"
                :total-matching="dir.purgeable"
                :all-matching-selected="allMatchingSelected"
                data-test="lost-selection-bar"
                @select-all-matching="selectAllMatching(dir)"
                @clear="clearSelection"
              >
                <template #actions>
                  <button type="button" class="btn btn--secondary btn--sm" :disabled="busy" data-test="lost-selection-search" @click="startSearch(dir, { selection: true })">从文件夹找回…</button>
                  <button type="button" class="btn btn--danger btn--sm" :disabled="busy" data-test="lost-selection-purge" @click="startPurge(dir, { selection: true })">清除…</button>
                </template>
              </SelectionBar>
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
      @done="(result, ids) => afterChange(result, ids)"
      @close="purgeDialog = null"
    />
  </section>
</template>

<script>
import { GetLostMediaReport, ListLostMediaItems, PreviewLostRelinkMove, PreviewLostRelinkSearch, SelectDirectory } from '../../../wailsjs/go/main/App';
import { formatBytes } from '../../utils/mediaDetails.js';
import { collectLostItems, LOST_REASON_LABELS, lostMediaErrorText, reasonChips } from '../../utils/lostMedia.js';
import AppBanner from '../ui/AppBanner.vue';
import EmptyState from '../ui/EmptyState.vue';
import SegmentedControl from '../ui/SegmentedControl.vue';
import SelectionBar from '../ui/SelectionBar.vue';
import SkeletonBlock from '../ui/SkeletonBlock.vue';
import LostPurgeDialog from './LostPurgeDialog.vue';
import LostRelinkDialog from './LostRelinkDialog.vue';
import OrphanFilesSection from './OrphanFilesSection.vue';

const KINDS = [{ key: 'video', label: '视频' }, { key: 'image', label: '图片' }];
const PAGE_SIZE = 100;
// 目录里有勾选时，行上处理整个目录的按钮禁用，并这样说明。
const SELECTION_PENDING_TITLE = '已勾选条目，请用底部操作条';

export default {
  name: 'LostMediaPanel',
  components: { AppBanner, EmptyState, LostPurgeDialog, LostRelinkDialog, OrphanFilesSection, SegmentedControl, SelectionBar, SkeletonBlock },
  props: { directories: { type: Array, default: () => [] } },
  emits: ['open-video-id', 'run-command', 'videos-changed', 'open-library-missing', 'navigate'],
  data() {
    return {
      KINDS,
      SELECTION_PENDING_TITLE,
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
      // 点了「全选当前结果（N）」：展开目录里全部可处理的条目，含还没加载出来的（D-UD-10）。
      allMatchingSelected: false,
      relinkDialog: null,
      purgeDialog: null
    };
  },
  computed: {
    kindOptions() {
      return KINDS.map(option => ({ value: option.key, label: option.label, disabled: this.busy, dataTest: `lost-kind-${option.key}` }));
    },
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
    // 勾选只存在于展开着的那个目录（换目录时清空）。
    dirHasSelection(dir) {
      return this.expanded === dir.directory && this.selected.size > 0;
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
        this.clearSelection();
      } else if (this.expanded) {
        await this.loadItems(true);
      }
    },
    setKind(kind) {
      if (kind === this.kind) return;
      this.kind = kind;
      this.report = null;
      this.expanded = '';
      this.clearSelection();
      this.loadReport();
    },
    async toggleDirectory(dir) {
      this.clearSelection();
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
        if (reset) this.clearSelection();
        // 「全选当前结果」时新加载出来的可处理条目也算选中。
        else if (this.allMatchingSelected) this.selected = new Set([...this.selected, ...page.items.filter(item => item.purgeable).map(item => item.id)]);
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
      // 取消勾掉任何一条，就不再是「全部」了。
      if (!next.has(id)) this.allMatchingSelected = false;
    },
    clearSelection() {
      this.selected = new Set();
      this.allMatchingSelected = false;
    },
    selectAllMatching(dir) {
      if (this.expanded !== dir.directory) return;
      this.selected = new Set(this.items.filter(item => item.purgeable).map(item => item.id));
      this.allMatchingSelected = true;
    },
    // targetIDs 是要处理的条目，范围由入口决定、不随勾选变（D-UD-10「不选不等于全选」）：
    //   目录行上的按钮 → 这个目录里全部可处理的条目（逐页读完）；
    //   选择操作条 → 勾选的条目；点过「全选当前结果」时同样是这个目录里全部可处理的条目。
    async targetIDs(dir, { selection = false } = {}) {
      if (selection && !this.allMatchingSelected) return this.expanded === dir.directory ? [...this.selected] : [];
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
    // dir 为 null 是工具栏的「从文件夹找回全部」：不传条目，后端按全部可处理的失联条目配对。
    startSearch(dir, options = {}) {
      return this.run(async () => {
        const directory = await SelectDirectory();
        if (!directory) return;
        const ids = dir ? await this.targetIDs(dir, options) : [];
        if (dir && !ids.length) return;
        const preview = await PreviewLostRelinkSearch({ kind: this.kind, directory, item_ids: ids });
        this.relinkDialog = { title: '从文件夹找回', preview };
      }, '查找没有完成，请稍后重试。');
    },
    startPurge(dir, options = {}) {
      return this.run(async () => {
        const ids = await this.targetIDs(dir, options);
        if (ids.length) this.purgeDialog = { ids };
      }, '读取失联条目失败，请稍后重试。');
    },
    // afterChange：找回或清除有条目生效后通知宿主（带 kind：视频按 removedIDs 重取片库，图片由宿主提示图片库刷新），
    // 并重读报告与孤儿统计。取消时不知道哪些条目处理过，removedIDs 留空让片库整体重取。
    afterChange(result, ids) {
      if (!result?.done) return;
      if (this.kind === 'image') {
        this.$emit('videos-changed', { kind: 'image' });
      } else {
        const notDone = new Set((result.items || []).map(item => item.id));
        const removedIDs = result.cancelled ? [] : ids.filter(id => !notDone.has(id));
        this.$emit('videos-changed', { kind: 'video', removedIDs });
      }
      this.clearSelection();
      this.loadReport();
      this.$refs.orphans?.reload();
    }
  }
};
</script>

<style scoped>
.lost-media { display: grid; gap: var(--space-4); }
.lost-toolbar { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-2) var(--space-3); }
.lost-spacer { flex: 1; }
.lost-muted { margin: 0; color: var(--text-muted); font-size: var(--fs-caption); }
.lost-loading { padding: var(--space-5) 0; }
.lost-summary { display: flex; flex-wrap: wrap; align-items: baseline; gap: 6px var(--space-3); }
.lost-summary strong { font-size: var(--fs-display); font-weight: var(--fw-bold); line-height: 1; font-variant-numeric: tabular-nums; }
.lost-chip { padding: 1px var(--space-2); border-radius: var(--radius-pill); background: var(--control-bg); color: var(--text-secondary); font-size: var(--fs-caption); white-space: nowrap; }
.lost-chip--ok { background: var(--accent-soft); color: var(--accent-text); }
.lost-chip--wait { background: var(--warning-soft); color: var(--warning-text); }
.lost-root { display: grid; gap: var(--space-2); padding: 14px var(--space-4); border: 1px solid var(--hairline); border-radius: var(--radius-lg); background: var(--panel-bg); }
.lost-root__head { display: flex; flex-wrap: wrap; align-items: center; gap: 6px var(--space-3); }
.lost-dirs { display: grid; gap: 6px; margin: 0; padding: 0; list-style: none; }
.lost-dir { display: grid; gap: 6px; padding: var(--space-2) var(--space-3); border-radius: var(--radius); }
.lost-dir:hover { background: var(--control-hover-bg); }
.lost-dir__row { display: flex; align-items: center; gap: var(--space-2); }
.lost-dir__main { display: grid; flex: 1; gap: var(--space-1); min-width: 0; }
.lost-dir__chips { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
.lost-path { overflow: hidden; font-size: var(--fs-secondary); font-family: var(--font-mono); text-overflow: ellipsis; white-space: nowrap; }
.lost-items { display: grid; gap: 6px; padding-left: var(--space-2); }
.lost-items ul { display: grid; gap: var(--space-1); margin: 0; padding: 0; list-style: none; }
.lost-items__more { justify-self: start; }
.lost-item { display: flex; align-items: center; gap: var(--space-2); font-size: var(--fs-secondary); }
.lost-item__name { overflow: hidden; flex: 1; min-width: 0; text-overflow: ellipsis; white-space: nowrap; }
</style>
