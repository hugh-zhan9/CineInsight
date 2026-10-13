<template>
  <!-- 元数据批量编辑（D-MC-04 / D-MC-05）：选范围、加若干操作、试运行看每项实际会改变几条、执行，执行后可撤销
       （结果页的「撤销」，或全局提示与任务中心「最近操作」里的「撤销」，D-UD-09）。片库与图片库共用。 -->
  <DialogShell :title="`批量编辑${kindLabel}`" size="lg" :busy="applying || undoing" class="batch-edit-modal" data-test="metadata-batch-edit" @close="requestClose">
    <div v-if="!result" class="batch-edit-content">
      <div class="batch-edit-scope" role="radiogroup" aria-label="作用范围">
        <label v-if="selectedIds.length" class="batch-edit-scope__option">
          <input v-model="scope" type="radio" value="selected" :disabled="applying" data-test="batch-edit-scope-selected" />
          所选 {{ selectedIds.length }} 项
        </label>
        <label v-if="filter" class="batch-edit-scope__option">
          <input v-model="scope" type="radio" value="filter" :disabled="applying" data-test="batch-edit-scope-filter" />
          当前筛选的全部结果
        </label>
      </div>
      <p v-if="scope === 'filter'" class="batch-edit-muted">
        执行时按当前筛选重新取一遍，不只是已加载的这些。先试运行确认总数再执行。<template v-if="collapsedVersions">列表折叠了版本，折叠起来的其他版本也会一起改。</template>
      </p>

      <ul class="batch-edit-ops">
        <li v-for="(row, index) in rows" :key="row.key" class="batch-edit-op" :data-test="`batch-edit-op-${index}`">
          <div class="batch-edit-op__line">
            <select v-model="row.field" class="select-input" aria-label="字段" :disabled="applying" data-test="batch-edit-field" @change="resetRow(row)">
              <option v-for="field in fieldOptions" :key="field.value" :value="field.value">{{ field.label }}</option>
            </select>
            <select v-model="row.action" class="select-input" aria-label="操作" :disabled="applying" data-test="batch-edit-action">
              <option v-for="action in actionOptions(row.field)" :key="action.value" :value="action.value">{{ action.label }}</option>
            </select>
            <input
              v-if="row.field === 'year' && row.action === 'set'"
              v-model="row.value"
              type="number"
              min="1870"
              max="2100"
              step="1"
              class="number-input batch-edit-year"
              placeholder="如 2001"
              aria-label="年份"
              :disabled="applying"
              data-test="batch-edit-year"
            />
            <select v-else-if="row.field === 'rating' && row.action === 'set'" v-model="row.value" class="select-input" aria-label="评分" :disabled="applying" data-test="batch-edit-rating">
              <option value="">选择评分</option>
              <option v-for="rating in ratingOptions" :key="rating" :value="String(rating)">{{ rating }}</option>
            </select>
            <span v-if="preview" class="batch-edit-op__count" data-test="batch-edit-op-count">会改变 {{ countFor(row) }} 项</span>
            <button type="button" class="btn btn--secondary btn--sm" :disabled="applying || rows.length === 1" data-test="batch-edit-remove-op" @click="removeRow(index)">移除</button>
          </div>
          <div v-if="!isScalar(row.field)" class="batch-edit-picker">
            <span v-for="item in row.items" :key="item.id" class="tag-badge" :data-test="`batch-edit-chosen-${item.id}`">
              {{ item.name }}
              <button type="button" class="btn-icon btn--sm batch-edit-chip-remove" :aria-label="`移除 ${item.name}`" :disabled="applying" @click="removeItem(row, item)"><AppIcon name="close" :size="16" /></button>
            </span>
            <input
              v-model="row.keyword"
              type="search"
              class="search-input batch-edit-picker__input"
              :placeholder="pickerPlaceholder(row.field)"
              :disabled="applying"
              data-test="batch-edit-picker-input"
              @focus="searchOptions(row)"
              @input="searchOptions(row)"
              @blur="closeOptions(row)"
            />
            <div v-if="row.open" class="batch-edit-options" role="listbox" data-test="batch-edit-options">
              <button
                v-for="option in row.options"
                :key="option.id"
                type="button"
                role="option"
                class="btn btn--ghost btn--sm batch-edit-option"
                :data-test="`batch-edit-option-${option.id}`"
                @mousedown.prevent="addItem(row, option)"
              >{{ option.name }}</button>
              <span v-if="row.options.length === 0" class="batch-edit-muted">没有匹配的{{ fieldLabel(row.field) }}</span>
            </div>
          </div>
        </li>
      </ul>
      <button type="button" class="btn btn--secondary btn--sm batch-edit-add" :disabled="applying" data-test="batch-edit-add-op" @click="addRow">添加一项操作</button>

      <p v-if="kind === 'video'" class="batch-edit-muted" data-test="batch-edit-nfo-note">
        已知限制：清空的年份等字段，如果片子旁有 NFO，「补全本地资料」会按「空就补」重新填上。
      </p>
      <p v-if="hasCollectionRemoval" class="batch-edit-muted" data-test="batch-edit-collection-note">
        撤销「移出作品集」时，视频会加回作品集末尾，不回到原来的位置。
      </p>
      <p v-if="preview" class="batch-edit-preview" data-test="batch-edit-preview">
        共 {{ preview.target_count }} 项{{ previewIncludesCollapsed ? '（含折叠起来的其他版本）' : '' }}，其中 {{ preview.changed_count }} 项会改变；已经是目标值的不会改动。
      </p>
      <p v-if="error" class="batch-edit-error" role="alert" data-test="batch-edit-error">{{ error }}</p>
    </div>

    <div v-else class="batch-edit-content">
      <p class="batch-edit-preview" data-test="batch-edit-result">{{ resultText }}</p>
      <ul class="batch-edit-result-ops">
        <li v-for="op in result.ops" :key="`${op.field}:${op.action}`">{{ describeOp(op) }}：{{ op.changes }} 项</li>
      </ul>
      <p v-if="undoMessage" class="batch-edit-preview" data-test="batch-edit-undo-result">{{ undoMessage }}</p>
      <p v-if="error" class="batch-edit-error" role="alert" data-test="batch-edit-error">{{ error }}</p>
    </div>

    <template v-if="!result" #footer>
      <button type="button" class="btn btn--secondary" :disabled="applying" @click="requestClose">取消</button>
      <button type="button" class="btn btn--secondary" :disabled="!requestReady || previewing || applying" data-test="batch-edit-preview-btn" @click="runPreview">{{ previewing ? '试运行中…' : '试运行' }}</button>
      <button type="button" class="btn btn--primary" :disabled="!canApply" data-test="batch-edit-apply" @click="apply">
        {{ applying ? '执行中…' : `执行（${preview ? preview.changed_count : 0} 项）` }}
      </button>
    </template>
    <template v-else #footer>
      <button v-if="result.batch_id && !undone" type="button" class="btn btn--secondary" :disabled="undoing" data-test="batch-edit-undo" @click="undo">{{ undoing ? '撤销中…' : '撤销' }}</button>
      <button type="button" class="btn btn--primary" :disabled="undoing" data-test="batch-edit-done" @click="$emit('close')">关闭</button>
    </template>
  </DialogShell>
</template>

<script>
import { ApplyMetadataBatchEdit, ListCollections, ListPeople, PreviewMetadataBatchEdit, UndoMetadataBatchEdit } from '../../wailsjs/go/main/App';
import { logFrontend } from '../utils/frontendLog.js';
import {
  BATCH_EDIT_FIELDS, BATCH_RATING_OPTIONS, batchActionOptions, batchEditErrorMessage, batchFieldIsScalar, batchFieldLabel,
  describeBatchOp, undoResultText
} from '../utils/metadataBatchEdit.js';
import { notifySuccess, notifyUndoable } from '../utils/feedback.js';
import AppIcon from './ui/AppIcon.vue';
import DialogShell from './ui/DialogShell.vue';

// 关联字段候选一次给几个。
const OPTION_LIMIT = 20;
let rowSeed = 0;

function newRow(field) {
  rowSeed += 1;
  return { key: rowSeed, field, action: batchActionOptions(field)[0].value, value: '', items: [], keyword: '', options: [], open: false, searchSeq: 0 };
}

export default {
  name: 'MetadataBatchEditDialog',
  components: { AppIcon, DialogShell },
  props: {
    kind: { type: String, default: 'video' },
    selectedIds: { type: Array, default: () => [] },
    // 打开时的当前筛选（视频 LibraryFilter / 图片 ImageFilter）；为空时不提供「当前筛选的全部结果」。
    filter: { type: Object, default: null },
    // 全部标签（标签候选在本地过滤，自动标签不出现）。
    tags: { type: Array, default: () => [] },
    // 片库列表折叠了版本：「当前筛选的全部结果」仍按全部版本取，总数会比列表上的多，要说清楚。
    collapsedVersions: { type: Boolean, default: false },
    // 宿主的 @undone（撤销完成，宿主按当前条件重取列表）。声明成 prop 而不是放进 emits：从提示或「最近操作」撤销时
    // 对话框可能早已关掉，Vue 会丢掉已卸下组件的 $emit，拿着宿主传进来的这个监听才能照样通知到宿主。宿主写法不变。
    onUndone: { type: Function, default: null }
  },
  // applied / undone：数据变了，宿主按当前条件重取列表；applied 的参数为 null 表示中途失败、已提交的部分保留。
  emits: ['close', 'applied'],
  data() {
    const fields = BATCH_EDIT_FIELDS[this.kind] || BATCH_EDIT_FIELDS.video;
    return {
      scope: this.selectedIds.length || !this.filter ? 'selected' : 'filter',
      rows: [newRow(fields[0].value)],
      preview: null,
      previewSignature: '',
      previewing: false,
      applying: false,
      result: null,
      undoing: false,
      undone: false,
      undoMessage: '',
      error: '',
      ratingOptions: BATCH_RATING_OPTIONS
    };
  },
  beforeUnmount() {
    this.disposed = true;
  },
  computed: {
    kindLabel() {
      return this.kind === 'image' ? '图片' : '视频';
    },
    fieldOptions() {
      return BATCH_EDIT_FIELDS[this.kind] || BATCH_EDIT_FIELDS.video;
    },
    requestReady() {
      const scopeReady = this.scope === 'filter' ? !!this.filter : this.selectedIds.length > 0;
      return scopeReady && this.rows.length > 0 && this.rows.every(row => this.rowReady(row));
    },
    signature() {
      return JSON.stringify(this.buildRequest());
    },
    previewIncludesCollapsed() {
      return this.collapsedVersions && this.scope === 'filter';
    },
    hasCollectionRemoval() {
      return this.rows.some(row => row.field === 'collection' && row.action === 'remove');
    },
    // 预览必须对应当前的范围与操作，且确有条目会改变，才能执行。
    canApply() {
      return !!this.preview && this.previewSignature === this.signature && !this.previewing && !this.applying && Number(this.preview.changed_count) > 0;
    },
    resultText() {
      const result = this.result || {};
      const changed = Number(result.changed_count || 0);
      if (result.cancelled) {
        return `中途停止：处理了 ${result.processed} / ${result.target_count} 项，其中 ${changed} 项已改动并保留，可以撤销。`;
      }
      if (!result.batch_id) return '没有需要改动的条目，什么也没改。';
      return `已完成：${result.target_count} 项里 ${changed} 项有改动。`;
    }
  },
  watch: {
    // 范围或操作一变，旧的预览数字就不作数了。
    signature() {
      if (this.preview && this.previewSignature !== this.signature) this.preview = null;
    }
  },
  methods: {
    isScalar: batchFieldIsScalar,
    fieldLabel: batchFieldLabel,
    describeOp: describeBatchOp,
    actionOptions: batchActionOptions,
    pickerPlaceholder(field) {
      return `搜索${batchFieldLabel(field)}，点选加入`;
    },
    rowReady(row) {
      if (batchFieldIsScalar(row.field)) {
        return row.action === 'clear' || (row.value !== '' && row.value !== null && Number.isFinite(Number(row.value)));
      }
      return row.items.length > 0;
    },
    buildRequest() {
      let target = { ids: this.selectedIds.map(Number) };
      if (this.scope === 'filter') {
        target = this.kind === 'image' ? { ids: [], image_filter: this.filter } : { ids: [], filter: this.filter };
      }
      return {
        kind: this.kind,
        target,
        ops: this.rows.map(row => ({
          field: row.field,
          action: row.action,
          value: batchFieldIsScalar(row.field) && row.action === 'set' && row.value !== '' ? Number(row.value) : null,
          ids: batchFieldIsScalar(row.field) ? [] : row.items.map(item => Number(item.id))
        }))
      };
    },
    countFor(row) {
      const op = (this.preview?.ops || []).find(item => item.field === row.field && item.action === row.action);
      return Number(op?.changes || 0);
    },
    errorText(err, fallback) {
      return batchEditErrorMessage(err) || fallback;
    },
    async runPreview() {
      if (!this.requestReady || this.previewing || this.applying) return;
      const signature = this.signature;
      this.previewing = true;
      this.error = '';
      try {
        const preview = await PreviewMetadataBatchEdit(this.buildRequest());
        if (signature !== this.signature) return;
        this.preview = preview || null;
        this.previewSignature = signature;
      } catch (err) {
        this.error = this.errorText(err, '预览失败，请稍后重试。');
        logFrontend('metadataBatchEdit', 'PreviewMetadataBatchEdit failed', { err: String(err) }, true);
      } finally {
        this.previewing = false;
      }
    },
    async apply() {
      if (!this.canApply) return;
      this.applying = true;
      this.error = '';
      try {
        this.result = await ApplyMetadataBatchEdit(this.buildRequest());
        this.$emit('applied', this.result);
        this.offerUndo(this.result);
      } catch (err) {
        const known = batchEditErrorMessage(err);
        logFrontend('metadataBatchEdit', 'ApplyMetadataBatchEdit failed', { err: String(err) }, true);
        if (known) {
          this.error = known;
        } else {
          // 写到一半失败：已提交的批次保留，在「数据维护 · 完整度」的最近批量编辑里可以撤销。
          this.error = '批量编辑没有全部完成：已提交的改动保留，可以在「数据维护 · 完整度」的最近批量编辑里撤销。';
          this.$emit('applied', null);
        }
      } finally {
        this.applying = false;
      }
    },
    // 有改动的批次（中途停止的也算）可撤销（D-UD-07、D-UD-09）：出带「撤销」的全局提示，并记进任务中心「最近操作」。
    // 提示 / 最近操作里的撤销与对话框结果页的「撤销」是同一批、同一条路（performUndo），已撤销过的再点什么也不做。
    offerUndo(result) {
      const batchID = result?.batch_id;
      if (!batchID) return;
      const changed = Number(result.changed_count || 0);
      const message = result.cancelled ? `批量编辑中途停止：${changed} 项已改动，可以撤销` : `已批量编辑 ${changed} 项${this.kindLabel}`;
      notifyUndoable(message, () => this.undoFromToast(batchID), { label: `批量编辑${this.kindLabel}（${changed} 项）` });
    },
    async undoFromToast(batchID) {
      if (this.undone) return;
      if (this.undoing) throw new Error('这次批量编辑正在撤销，请稍后再试');
      let undoResult;
      try {
        undoResult = await this.performUndo(batchID);
      } catch (err) {
        throw new Error(this.errorText(err, '请稍后重试'));
      }
      // 中途停止：抛出去让「最近操作」留着这一条，可以再点撤销继续。
      if (undoResult?.cancelled) throw new Error(undoResultText(undoResult));
      notifySuccess(undoResultText(undoResult));
    },
    async performUndo(batchID) {
      this.undoing = true;
      this.error = '';
      try {
        const undoResult = await UndoMetadataBatchEdit(batchID);
        this.undone = !undoResult?.cancelled;
        this.undoMessage = undoResultText(undoResult);
        if (this.disposed) this.onUndone?.(undoResult);
        else this.$emit('undone', undoResult);
        return undoResult;
      } catch (err) {
        logFrontend('metadataBatchEdit', 'UndoMetadataBatchEdit failed', { err: String(err) }, true);
        throw err;
      } finally {
        this.undoing = false;
      }
    },
    async undo() {
      if (!this.result?.batch_id || this.undoing || this.undone) return;
      try {
        await this.performUndo(this.result.batch_id);
      } catch (err) {
        this.error = this.errorText(err, '撤销失败，请稍后重试。');
      }
    },
    requestClose() {
      if (!this.applying && !this.undoing) this.$emit('close');
    },
    addRow() {
      const used = new Set(this.rows.map(row => row.field));
      const next = this.fieldOptions.find(field => !used.has(field.value)) || this.fieldOptions[0];
      this.rows.push(newRow(next.value));
    },
    removeRow(index) {
      if (this.rows.length > 1) this.rows.splice(index, 1);
    },
    resetRow(row) {
      Object.assign(row, { action: batchActionOptions(row.field)[0].value, value: '', items: [], keyword: '', options: [], open: false });
    },
    // 标签在本地过滤（自动标签由应用维护，不出现）；人物与作品集按关键字查。
    async searchOptions(row) {
      row.open = true;
      const keyword = String(row.keyword || '').trim();
      const chosen = new Set(row.items.map(item => Number(item.id)));
      if (row.field === 'tag') {
        const lower = keyword.toLowerCase();
        row.options = this.tags
          .filter(tag => !tag.automatic_kind && !chosen.has(Number(tag.id)) && (!lower || String(tag.name || '').toLowerCase().includes(lower)))
          .slice(0, OPTION_LIMIT)
          .map(tag => ({ id: Number(tag.id), name: tag.name }));
        return;
      }
      const seq = ++row.searchSeq;
      const field = row.field;
      try {
        const items = field === 'person' ? await ListPeople(keyword, '', 0, OPTION_LIMIT) : await ListCollections(keyword, '', 0, OPTION_LIMIT);
        if (seq !== row.searchSeq || field !== row.field) return;
        row.options = (items || [])
          .map(item => (field === 'person'
            ? { id: Number(item?.person?.id), name: item?.person?.display_name || '' }
            : { id: Number(item?.collection?.id), name: item?.collection?.name || '' }))
          .filter(option => option.id && !chosen.has(option.id));
      } catch (err) {
        if (seq === row.searchSeq) row.options = [];
        logFrontend('metadataBatchEdit', 'search options failed', { field, err: String(err) }, true);
      }
    },
    closeOptions(row) {
      row.open = false;
    },
    addItem(row, option) {
      if (!row.items.some(item => item.id === option.id)) row.items.push({ id: option.id, name: option.name });
      row.keyword = '';
      row.open = false;
    },
    removeItem(row, target) {
      row.items = row.items.filter(item => item.id !== target.id);
    }
  }
};
</script>

<style scoped>
.batch-edit-content { display: grid; gap: var(--space-3); align-content: start; }
.batch-edit-scope { display: flex; flex-wrap: wrap; gap: var(--space-2) 18px; }
.batch-edit-scope__option { display: inline-flex; align-items: center; gap: 6px; font-size: var(--fs-secondary); }
.batch-edit-ops { list-style: none; margin: 0; padding: 0; display: grid; gap: 10px; }
.batch-edit-op { display: grid; gap: 6px; padding: 10px; border: 1px solid var(--border-color); border-radius: var(--radius); }
.batch-edit-op__line { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-2); }
.batch-edit-op__line .select-input { width: auto; min-width: 120px; }
.batch-edit-op__count { font-size: var(--fs-caption); color: var(--accent-text); }
.batch-edit-year { width: 110px; }
.batch-edit-picker { position: relative; display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
.batch-edit-picker__input { flex: 1; min-width: 160px; }
.batch-edit-chip-remove.btn-icon { width: 20px; height: 20px; margin-left: 2px; }
.batch-edit-add { justify-self: start; }
.batch-edit-options {
  position: absolute; top: 100%; left: 0; z-index: var(--z-menu); width: 100%; max-height: 220px; overflow-y: auto;
  display: grid; padding: var(--space-1); border: 1px solid var(--border-color); border-radius: var(--radius-sm); background: var(--bg-elevated); box-shadow: var(--shadow-popover);
}
.batch-edit-option { justify-content: flex-start; color: var(--text-primary); font-weight: var(--fw-regular); text-align: left; }
.batch-edit-muted { margin: 0; font-size: var(--fs-caption); color: var(--text-secondary); }
.batch-edit-preview { margin: 0; font-size: var(--fs-secondary); }
.batch-edit-error { margin: 0; font-size: var(--fs-secondary); color: var(--danger-text); }
.batch-edit-result-ops { margin: 0; padding-left: 18px; font-size: var(--fs-secondary); }
</style>
