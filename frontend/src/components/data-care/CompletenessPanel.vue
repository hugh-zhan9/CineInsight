<template>
  <!-- 元数据完整度（D-MC-02）：按片库默认可见范围统计各项缺失；「去处理」带缺失筛选跳到片库或图片库（D-MC-03）。 -->
  <section class="completeness" data-test="data-care-panel-completeness">
    <header class="completeness-head">
      <div>
        <h3>元数据完整度</h3>
        <small>按片库默认范围统计：扫描目录之内、未排除、路径有效。个人评分与作品集不是每部片都该有，不计入。</small>
      </div>
      <button type="button" class="btn-secondary btn-compact" :disabled="loading" data-test="completeness-refresh" @click="load">{{ loading ? '统计中…' : '重新统计' }}</button>
    </header>

    <p v-if="error" class="completeness-alert" role="alert" data-test="completeness-error">{{ error }}</p>
    <div v-if="loading && !report" class="completeness-empty" data-test="completeness-loading">正在统计…</div>

    <template v-else-if="report">
      <section v-for="group in groups" :key="group.key" class="completeness-group" :data-test="`completeness-group-${group.key}`">
        <header class="completeness-group__head">
          <h4>{{ group.title }}</h4>
          <small>{{ group.note }}</small>
        </header>
        <p v-if="group.items.length === 0" class="completeness-muted">{{ group.emptyText }}</p>
        <ul v-else class="completeness-list">
          <li v-for="item in group.items" :key="item.key" class="completeness-item" :data-test="`completeness-item-${item.kind}-${item.field}`">
            <div class="completeness-item__row">
              <span class="completeness-item__label">{{ item.label }}</span>
              <span class="completeness-bar" :title="`${item.percentText}`" aria-hidden="true">
                <span :class="['completeness-bar__fill', `completeness-bar__fill--${group.tone}`]" :style="{ width: `${item.percent}%` }"></span>
              </span>
              <span class="completeness-item__count" data-test="completeness-count">{{ item.countText }}</span>
              <button
                v-if="item.missing > 0"
                type="button"
                class="row-btn"
                :aria-expanded="isOpen(item)"
                :data-test="`completeness-directories-${item.kind}-${item.field}`"
                @click="toggleDirectories(item)"
              >{{ isOpen(item) ? '收起目录' : '缺得最多的目录' }}</button>
              <button
                v-if="item.action && item.missing > 0"
                type="button"
                class="btn-secondary btn-compact"
                :data-test="`completeness-go-${item.kind}-${item.field}`"
                @click="goHandle(item)"
              >去处理</button>
              <small v-else-if="item.hint" class="completeness-muted">{{ item.hint }}</small>
            </div>
            <div v-if="isOpen(item)" class="completeness-dirs" :data-test="`completeness-dirs-${item.kind}-${item.field}`">
              <p v-if="directoryState(item).loading" class="completeness-muted">正在读取目录…</p>
              <p v-else-if="directoryState(item).error" class="completeness-alert" role="alert">{{ directoryState(item).error }}</p>
              <p v-else-if="directoryState(item).rows.length === 0" class="completeness-muted">没有缺这一项的目录了。</p>
              <ol v-else class="completeness-dirs__list">
                <li v-for="row in directoryState(item).rows" :key="row.directory" data-test="completeness-dir-row">
                  <span class="completeness-dirs__path" :title="row.directory">{{ row.directory || '（未知目录）' }}</span>
                  <small>缺 {{ formatCount(row.missing) }} / 共 {{ formatCount(row.total) }}</small>
                </li>
              </ol>
            </div>
          </li>
        </ul>
      </section>
    </template>

    <!-- 最近的批量编辑（D-MC-05）：保留最近 20 次，可撤销；撤销时之后又被改过的条目跳过并计数。 -->
    <section class="completeness-group" data-test="batch-edit-history">
      <header class="completeness-group__head">
        <h4>最近的批量编辑</h4>
        <small>保留最近 20 次；撤销时，之后又被改过的条目会跳过。</small>
      </header>
      <p v-if="historyError" class="completeness-alert" role="alert" data-test="batch-edit-history-error">{{ historyError }}</p>
      <p v-else-if="historyLoaded && history.length === 0" class="completeness-muted" data-test="batch-edit-history-empty">还没有批量编辑。片库或图片库里选中条目后点「批量编辑…」。</p>
      <ul v-else class="completeness-list">
        <li v-for="batch in history" :key="batch.id" class="completeness-item" :data-test="`batch-edit-history-${batch.id}`">
          <div class="completeness-batch">
            <span class="completeness-batch__title" :title="batchTitle(batch)">{{ batchTitle(batch) }}</span>
            <small class="completeness-muted">{{ formatTime(batch.created_at) }} · {{ formatCount(batch.item_count) }} 处改动</small>
            <button
              v-if="batch.undoable"
              type="button"
              class="btn-secondary btn-compact"
              :disabled="undoingID !== 0"
              data-test="batch-edit-history-undo"
              @click="undoBatch(batch)"
            >{{ undoingID === batch.id ? '撤销中…' : '撤销' }}</button>
            <small v-else class="completeness-muted" data-test="batch-edit-history-undone">{{ batch.undone_at ? '已撤销' : '无可撤销' }}</small>
          </div>
        </li>
      </ul>
      <p v-if="historyNotice" class="completeness-muted" role="status" data-test="batch-edit-history-notice">{{ historyNotice }}</p>
    </section>
  </section>
</template>

<script>
import { GetMetadataCompleteness, GetMetadataCompletenessByDirectory, ListMetadataBatchEdits, UndoMetadataBatchEdit } from '../../../wailsjs/go/main/App';
import { logFrontend } from '../../utils/frontendLog.js';
import { isMissingFilterField, missingFieldLabel } from '../../utils/metadataCompleteness.js';
import { batchEditErrorMessage, describeBatchOp, undoResultText } from '../../utils/metadataBatchEdit.js';

// 「缺得最多的目录」一次取几个（后端默认 10）。
const DIRECTORY_LIMIT = 10;

// 待处理项里能直接打开现有视图的：本地资料有更新 → 片库的「本地资料有更新」视图。
const PENDING_COMMANDS = {
  nfo_update: { id: 'library.openLocalMetadataUpdates', label: '查看本地资料有更新的视频' }
};

// 不能直接去处理的项给一句说明。
const ITEM_HINTS = {
  thumbnail: '浏览片库时自动生成'
};

export default {
  name: 'CompletenessPanel',
  props: { directories: { type: Array, default: () => [] } },
  emits: ['open-video-id', 'run-command', 'videos-changed', 'open-library-missing', 'navigate'],
  data() {
    return {
      report: null, loading: false, error: '', directoryStates: {}, loadToken: 0,
      history: [], historyLoaded: false, historyError: '', historyNotice: '', undoingID: 0
    };
  },
  computed: {
    groups() {
      const report = this.report || {};
      const imageNote = report.image_exif_unparsed > 0
        ? `共 ${this.formatCount(report.image_total)} 张；另有 ${this.formatCount(report.image_exif_unparsed)} 张还没解析 EXIF，不计入拍摄时间与地理位置`
        : `共 ${this.formatCount(report.image_total)} 张`;
      // 库里一条都没有时不列各项（全是「缺 0」没有意义），只给一句说明。
      const hasVideos = Number(report.video_total || 0) > 0;
      const hasImages = Number(report.image_total || 0) > 0;
      return [
        { key: 'videos', title: '视频', tone: 'accent', note: `共 ${this.formatCount(report.video_total)} 部`,
          emptyText: '片库里还没有视频。', items: hasVideos ? this.toItems('video', report.videos) : [] },
        { key: 'pending', title: '待处理', tone: 'warning', note: '片子旁的 NFO 有新内容或读不出来，不算缺失',
          emptyText: '没有待处理的本地资料。', items: hasVideos ? this.toItems('video', report.video_pending, true) : [] },
        { key: 'images', title: '图片', tone: 'accent', note: imageNote,
          emptyText: '图片库里还没有图片。', items: hasImages ? this.toItems('image', report.images) : [] }
      ];
    }
  },
  mounted() {
    this.load();
    this.loadHistory();
  },
  methods: {
    toItems(kind, items, pending = false) {
      return (items || []).map(item => {
        const missing = Number(item?.missing || 0);
        const total = Number(item?.total || 0);
        const percent = total > 0 ? Math.min(100, (missing * 100) / total) : 0;
        const rounded = Number(percent.toFixed(1));
        // 有缺失但不到 0.1% 时不写成 0%，免得看着像没有。
        const percentText = total <= 0 ? '—' : (missing > 0 && rounded === 0 ? '<0.1%' : `${rounded}%`);
        const field = String(item?.field || '');
        const action = isMissingFilterField(kind, field) ? 'filter' : (PENDING_COMMANDS[field] ? 'command' : '');
        return {
          key: `${kind}:${field}`, kind, field, missing, total, percent, percentText, action,
          label: missingFieldLabel(kind, field),
          countText: pending ? `${this.formatCount(missing)} 部 · ${percentText}` : `缺 ${this.formatCount(missing)} · ${percentText}`,
          hint: ITEM_HINTS[field] || ''
        };
      });
    },
    async load() {
      const token = ++this.loadToken;
      this.loading = true;
      this.error = '';
      try {
        const report = await GetMetadataCompleteness();
        if (token !== this.loadToken) return;
        this.report = report || null;
        // 数字变了，已展开的目录清单一起作废，重新展开时再取。
        this.directoryStates = {};
      } catch (err) {
        if (token !== this.loadToken) return;
        this.error = '读取完整度统计失败，请稍后重试。';
        logFrontend('completeness', 'GetMetadataCompleteness failed', { err: String(err) }, true);
      } finally {
        if (token === this.loadToken) this.loading = false;
      }
    },
    directoryState(item) {
      return this.directoryStates[item.key] || { open: false, loading: false, error: '', rows: [] };
    },
    isOpen(item) {
      return this.directoryState(item).open;
    },
    // 展开时才按目录取（D-MC-02「缺得最多的目录」）；收起再展开沿用已取到的结果。
    async toggleDirectories(item) {
      const current = this.directoryState(item);
      if (current.open) {
        this.directoryStates = { ...this.directoryStates, [item.key]: { ...current, open: false } };
        return;
      }
      if (current.loaded) {
        this.directoryStates = { ...this.directoryStates, [item.key]: { ...current, open: true } };
        return;
      }
      this.directoryStates = { ...this.directoryStates, [item.key]: { open: true, loading: true, error: '', rows: [] } };
      try {
        const rows = await GetMetadataCompletenessByDirectory(item.kind, item.field, DIRECTORY_LIMIT);
        this.setDirectoryState(item.key, { loading: false, loaded: true, rows: rows || [] });
      } catch (err) {
        this.setDirectoryState(item.key, { loading: false, error: '读取目录失败，请稍后重试。' });
        logFrontend('completeness', 'GetMetadataCompletenessByDirectory failed', { kind: item.kind, field: item.field, err: String(err) }, true);
      }
    },
    // 只在该项仍是这次展开时写回：期间重新统计会清空状态，旧请求的结果就丢掉。
    setDirectoryState(key, patch) {
      const current = this.directoryStates[key];
      if (!current?.loading) return;
      this.directoryStates = { ...this.directoryStates, [key]: { ...current, ...patch } };
    },
    goHandle(item) {
      if (item.action === 'filter') {
        this.$emit('open-library-missing', { kind: item.kind, field: item.field });
        return;
      }
      const command = PENDING_COMMANDS[item.field];
      if (command) this.$emit('run-command', command.id, command.label);
    },
    async loadHistory() {
      try {
        this.history = (await ListMetadataBatchEdits()) || [];
        this.historyError = '';
      } catch (err) {
        this.historyError = '读取最近的批量编辑失败，请稍后重试。';
        logFrontend('completeness', 'ListMetadataBatchEdits failed', { err: String(err) }, true);
      } finally {
        this.historyLoaded = true;
      }
    },
    batchTitle(batch) {
      const summary = batch?.summary || {};
      const ops = (summary.ops || []).filter(op => Number(op?.changes || 0) > 0).map(describeBatchOp).join('；');
      const scope = summary.scope === 'filter' ? '（当前筛选的全部结果）' : '';
      const stopped = summary.cancelled ? '（中途停止）' : '';
      return `${batch?.kind === 'image' ? '图片' : '视频'} · ${ops || '批量编辑'}${scope}${stopped}`;
    },
    // 撤销后统计、列表都可能变：重新统计、重读列表，并通知片库 / 图片库重取（videos-changed）。
    async undoBatch(batch) {
      if (this.undoingID) return;
      this.undoingID = batch.id;
      this.historyNotice = '';
      try {
        const result = await UndoMetadataBatchEdit(batch.id);
        this.historyNotice = undoResultText(result);
        this.$emit('videos-changed', { removedIDs: [], kind: batch.kind });
        this.load();
      } catch (err) {
        this.historyNotice = batchEditErrorMessage(err) || '撤销失败，请稍后重试。';
        logFrontend('completeness', 'UndoMetadataBatchEdit failed', { id: batch.id, err: String(err) }, true);
      } finally {
        this.undoingID = 0;
        await this.loadHistory();
      }
    },
    formatTime(value) {
      const date = new Date(value);
      return Number.isNaN(date.getTime()) ? '' : date.toLocaleString('zh-CN', { hour12: false });
    },
    formatCount(value) {
      return Number(value || 0).toLocaleString('zh-CN');
    }
  }
};
</script>

<style scoped>
/* 与洞察页「存储体检」同一套样子：面板、分组标题、静音说明、警示条。 */
.completeness { display: grid; gap: 22px; }
.completeness-head { display: flex; flex-wrap: wrap; align-items: flex-start; justify-content: space-between; gap: 10px 16px; }
.completeness-head h3 { margin: 0 0 4px; font-size: 17px; font-weight: 750; }
.completeness-head small { color: var(--text-muted); font-size: 12px; }
.completeness-alert { margin: 0; padding: 9px 14px; border: 1px solid var(--danger-color); border-radius: 10px; color: var(--danger-color); font-size: 12px; }
.completeness-empty { padding: 48px 8px; color: var(--text-secondary); text-align: center; }
.completeness-muted { margin: 0; color: var(--text-muted); font-size: 12px; }

.completeness-group { display: grid; gap: 10px; padding: 14px 16px; border: 1px solid var(--hairline); border-radius: var(--radius-lg); background: var(--panel-bg); }
.completeness-group__head { display: flex; flex-wrap: wrap; align-items: baseline; gap: 4px 12px; }
.completeness-group__head h4 { margin: 0; font-size: 15px; font-weight: 700; }
.completeness-group__head small { color: var(--text-muted); font-size: 12px; }

.completeness-list { display: grid; gap: 4px; margin: 0; padding: 0; list-style: none; }
.completeness-item { display: grid; gap: 6px; padding: 6px 8px; border-radius: 10px; }
.completeness-item:hover { background: var(--control-hover-bg); }
.completeness-item__row { display: grid; grid-template-columns: 96px minmax(80px, 1fr) 128px auto auto; align-items: center; gap: 12px; font-size: 13px; }
.completeness-item__label { font-weight: 600; white-space: nowrap; }
.completeness-item__count { color: var(--text-secondary); text-align: right; font-variant-numeric: tabular-nums; white-space: nowrap; }

.completeness-bar { position: relative; height: 8px; overflow: hidden; border-radius: 999px; background: var(--control-bg); }
.completeness-bar__fill { position: absolute; inset: 0 auto 0 0; border-radius: inherit; }
.completeness-bar__fill--accent { background: var(--accent-color); }
.completeness-bar__fill--warning { background: var(--warning-text); }

.completeness-batch { display: grid; grid-template-columns: minmax(0, 1fr) auto auto; align-items: center; gap: 12px; font-size: 13px; }
.completeness-batch__title { overflow: hidden; font-weight: 600; text-overflow: ellipsis; white-space: nowrap; }

.completeness-dirs { padding: 2px 0 4px 108px; }
.completeness-dirs__list { display: grid; gap: 4px; margin: 0; padding: 0; list-style: none; }
.completeness-dirs__list li { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: center; gap: 12px; font-size: 12px; }
.completeness-dirs__list small { color: var(--text-muted); font-variant-numeric: tabular-nums; }
.completeness-dirs__path { overflow: hidden; color: var(--text-secondary); font-family: var(--font-mono, monospace); text-overflow: ellipsis; white-space: nowrap; }

@media (max-width: 720px) {
  .completeness-item__row { grid-template-columns: 80px minmax(60px, 1fr) auto; }
  .completeness-dirs { padding-left: 0; }
}
</style>
