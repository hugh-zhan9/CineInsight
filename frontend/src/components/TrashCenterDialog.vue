<template>
  <DialogShell v-if="visible" title="回收站" size="xl" class="trash-center-modal" data-test="trash-center-dialog" @close="requestClose">
    <!-- 「回收站」只指应用内可恢复的删除记录，「废纸篓」只指系统废纸篓（D-UD-04）。 -->
    <p class="trash-center-hint">删除时勾选「同时把原文件移到废纸篓」，文件会进系统废纸篓；在访达中清空废纸篓才会释放空间。</p>

    <p v-if="usageError" class="trash-center-usage trash-center-error" role="alert">{{ usageError }}</p>
    <p v-else-if="usageText" class="trash-center-usage" data-test="trash-usage">{{ usageText }}</p>

    <!-- 页签（D-UD-06 AppTabs）：每项带 trash-tab-* 钩子；条数写全（不截成 99+），用量取回之前不显示。 -->
    <AppTabs class="trash-center-tabs" :tabs="tabs" :model-value="activeTab" id-prefix="trash-center" aria-label="回收站分类" @update:model-value="selectTab" />

    <section v-if="isEntryTab" v-bind="tabPanelAttrs" class="trash-center-panel">
      <div class="trash-center-filters">
        <select v-model="currentFilter.mode" class="select-input" aria-label="删除方式" data-test="trash-filter-mode" @change="reloadEntries(activeTab)">
          <option v-for="option in modeOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
        </select>
        <select v-model="currentFilter.deletedBy" class="select-input" aria-label="删除人" data-test="trash-filter-deleted-by" @change="reloadEntries(activeTab)">
          <option v-for="option in deletedByOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
        </select>
        <input
          v-model="currentFilter.query"
          type="search"
          class="search-input"
          placeholder="按名称或原位置搜索"
          aria-label="搜索回收站"
          data-test="trash-search"
          @keydown.enter.prevent="reloadEntries(activeTab)"
        />
        <button type="button" class="btn btn--secondary btn--sm" data-test="trash-search-submit" @click="reloadEntries(activeTab)">搜索</button>
      </div>

      <div class="trash-center-bulk">
        <label class="trash-center-select-all">
          <input type="checkbox" :checked="allEntriesSelected" :disabled="currentList.items.length === 0 || currentList.loading" data-test="trash-select-all" @change="toggleSelectAll" />
          全选当前结果
        </label>
        <span class="trash-center-muted">已选 {{ currentList.selected.length }} 项</span>
        <button type="button" class="btn btn--primary btn--sm" :disabled="currentList.busy || selectedWith('restore').length === 0" data-test="trash-bulk-restore" @click="restoreRows(selectedWith('restore'))">恢复所选（{{ selectedWith('restore').length }}）</button>
        <button type="button" class="btn btn--danger btn--sm" :disabled="currentList.busy || selectedWith('purge').length === 0" data-test="trash-bulk-purge" @click="purgeRows(selectedWith('purge'))">永久删除（{{ selectedWith('purge').length }}）</button>
        <button type="button" class="btn btn--secondary btn--sm" :disabled="currentList.busy || selectedWith('remove_record').length === 0" data-test="trash-bulk-remove" @click="removeRecordRows(selectedWith('remove_record'))">{{ removeRecordLabel }}（{{ selectedWith('remove_record').length }}）</button>
        <button v-if="selectedForceable.length > 0" type="button" class="btn btn--danger btn--sm" :disabled="currentList.busy" data-test="trash-bulk-force" @click="openForceConfirm(selectedForceable)">{{ forceLabel }}（{{ selectedForceable.length }}）</button>
      </div>
      <p v-if="currentList.notice" class="trash-center-notice" role="status" data-test="trash-notice">{{ currentList.notice }}</p>

      <div v-if="currentList.loading && currentList.items.length === 0" class="trash-center-state"><AppSpinner :size="16" />正在读取回收站…</div>
      <AppBanner v-else-if="currentList.error" class="trash-center-error-banner" tone="danger" role="alert" :title="currentList.error" />
      <EmptyState v-else-if="currentList.items.length === 0" icon="trash" :title="hasActiveFilter ? '没有符合条件的条目。' : '回收站为空。'" />
      <div v-else class="trash-center-list">
        <div v-for="entry in currentList.items" :key="entry.id" class="trash-center-entry" :data-test="`trash-entry-${entry.id}`">
          <input
            type="checkbox"
            class="trash-center-entry__check"
            :checked="currentList.selected.includes(entry.id)"
            :aria-label="`选择 ${entry.name}`"
            @change="toggleSelected(entry.id)"
          />
          <div class="trash-center-entry__body">
            <strong :title="entry.name">{{ entry.name }}</strong>
            <span :title="entry.original_path">{{ entry.original_path }}</span>
            <small>删除人：{{ deletionActor(entry.deleted_by) }} · {{ entryStatus(entry) }}<template v-if="entry.file_size > 0"> · {{ formatBytes(entry.file_size) }}</template> · {{ formatDate(entry.created_at) }}</small>
            <small v-for="note in entryNotes(entry)" :key="note" class="trash-center-entry__note" data-test="trash-entry-note">{{ note }}</small>
            <small v-if="entry.last_error && !rowFailure(entry)" class="trash-center-entry__error">上次处理失败：{{ cleanText(entry.last_error) }}</small>
            <small v-if="rowFailure(entry)" class="trash-center-entry__error" data-test="trash-entry-error">{{ rowFailure(entry).text }}</small>
          </div>
          <div class="trash-center-entry__actions">
            <button
              v-if="entry.actions?.includes('restore')"
              type="button"
              class="btn btn--primary btn--sm"
              :disabled="currentList.busy"
              data-test="trash-entry-restore"
              @click="restoreRows([entry])"
            >{{ restoreLabel(entry) }}</button>
            <button
              v-if="entry.actions?.includes('purge')"
              type="button"
              class="btn btn--danger btn--sm"
              :disabled="currentList.busy"
              data-test="trash-entry-purge"
              @click="purgeRows([entry])"
            >永久删除</button>
            <button
              v-if="entry.actions?.includes('remove_record')"
              type="button"
              class="btn btn--secondary btn--sm"
              :disabled="currentList.busy"
              data-test="trash-entry-remove"
              @click="removeRecordRows([entry])"
            >{{ removeRecordLabel }}</button>
            <button
              v-if="isForceEligible(entry)"
              type="button"
              class="btn btn--danger btn--sm"
              :disabled="currentList.busy"
              data-test="trash-entry-force"
              @click="openForceConfirm([entry])"
            >{{ forceLabel }}</button>
          </div>
        </div>
        <button
          v-if="currentList.hasMore"
          type="button"
          class="btn btn--secondary trash-center-more"
          :disabled="currentList.loading"
          data-test="trash-load-more"
          @click="loadEntries(activeTab, { append: true })"
        >{{ currentList.loading ? '正在加载…' : '加载更多' }}</button>
      </div>
    </section>

    <section v-else-if="activeTab === 'hidden'" v-bind="tabPanelAttrs" class="trash-center-panel">
      <p class="trash-center-muted">扫描时文件不在原处、所在磁盘未连接或目录已移出扫描范围的图片会被隐藏，这里列出它们和原因。图片暂不支持单独检查，「重新检查」会重新扫描全部图片目录，文件回来的图片自动恢复。</p>
      <div class="trash-center-bulk">
        <label class="trash-center-select-all">
          <input type="checkbox" :checked="hiddenAllSelected" :disabled="hidden.items.length === 0 || hidden.loading" data-test="hidden-select-all" @change="toggleHiddenSelectAll" />
          全选当前结果
        </label>
        <span class="trash-center-muted">已选 {{ hidden.selected.length }} 张</span>
        <button type="button" class="btn btn--primary btn--sm" :disabled="hidden.busy || hidden.selected.length === 0" data-test="hidden-recheck" @click="recheckHidden">{{ hidden.busy ? '正在检查…' : `重新检查所选（${hidden.selected.length}）` }}</button>
      </div>
      <p v-if="hidden.notice" class="trash-center-notice" role="status" data-test="hidden-notice">{{ hidden.notice }}</p>
      <div v-if="hidden.loading && hidden.items.length === 0" class="trash-center-state"><AppSpinner :size="16" />正在读取…</div>
      <AppBanner v-else-if="hidden.error" class="trash-center-error-banner" tone="danger" role="alert" :title="hidden.error" />
      <EmptyState v-else-if="hidden.items.length === 0" icon="image" title="没有被扫描隐藏的图片。" />
      <div v-else class="trash-center-list">
        <div v-for="image in hidden.items" :key="image.id" class="trash-center-entry" :data-test="`hidden-entry-${image.id}`">
          <input
            type="checkbox"
            class="trash-center-entry__check"
            :checked="hidden.selected.includes(image.id)"
            :aria-label="`选择 ${image.name}`"
            @change="toggleHiddenSelected(image.id)"
          />
          <div class="trash-center-entry__body">
            <strong :title="image.name">{{ image.name }}</strong>
            <span :title="image.path">{{ image.path }}</span>
            <small>{{ hiddenReason(image.reason) }}<template v-if="image.deleted_at"> · {{ formatDate(image.deleted_at) }}</template></small>
          </div>
        </div>
        <button
          v-if="hidden.hasMore"
          type="button"
          class="btn btn--secondary trash-center-more"
          :disabled="hidden.loading"
          data-test="hidden-load-more"
          @click="loadHidden({ append: true })"
        >{{ hidden.loading ? '正在加载…' : '加载更多' }}</button>
      </div>
    </section>

    <section v-else v-bind="tabPanelAttrs" class="trash-center-panel">
      <p class="trash-center-muted">跨盘迁移时，原位置保留了一份隐藏的源文件作为保险。确认新位置的文件正常后，可以把它移到废纸篓，或直接永久删除。</p>
      <div class="trash-center-bulk">
        <label class="trash-center-select-all">
          <input type="checkbox" :checked="stagedAllSelected" :disabled="staged.items.length === 0" data-test="staged-select-all" @change="toggleStagedSelectAll" />
          全选
        </label>
        <span class="trash-center-muted">已选 {{ staged.selected.length }} 项</span>
        <button type="button" class="btn btn--primary btn--sm" :disabled="staged.busy || staged.selected.length === 0" data-test="staged-trash" @click="trashStaged">移到废纸篓（{{ staged.selected.length }}）</button>
        <button type="button" class="btn btn--danger btn--sm" :disabled="staged.busy || staged.selected.length === 0" data-test="staged-delete" @click="deleteStaged">永久删除（{{ staged.selected.length }}）</button>
      </div>
      <p v-if="staged.notice" class="trash-center-notice" role="status" data-test="staged-notice">{{ staged.notice }}</p>
      <div v-if="staged.loading && staged.items.length === 0" class="trash-center-state"><AppSpinner :size="16" />正在读取…</div>
      <AppBanner v-else-if="staged.error" class="trash-center-error-banner" tone="danger" role="alert" :title="staged.error" />
      <EmptyState v-else-if="staged.items.length === 0" icon="folder" title="没有迁移残留。" />
      <div v-else class="trash-center-list">
        <div v-for="item in staged.items" :key="item.id" class="trash-center-entry" :data-test="`staged-entry-${item.id}`">
          <input
            type="checkbox"
            class="trash-center-entry__check"
            :checked="staged.selected.includes(item.id)"
            :aria-label="`选择 ${baseName(item.original_path)}`"
            @change="toggleStagedSelected(item.id)"
          />
          <div class="trash-center-entry__body">
            <strong :title="baseName(item.original_path)">{{ baseName(item.original_path) }}</strong>
            <span :title="item.original_path">原位置：{{ item.original_path }}</span>
            <small>{{ formatBytes(item.size) }} · 迁移于 {{ formatDate(item.created_at) }}</small>
          </div>
        </div>
      </div>
    </section>

    <template #footer>
      <button type="button" class="btn btn--secondary" data-test="trash-center-close" @click="requestClose">关闭</button>
    </template>
  </DialogShell>

  <!-- 不可逆（D-UD-07 第 3 级）：输入的就是按钮上的文字。后端要的确认词是协议常量 FORCE_REMOVE_CONFIRM_TEXT，另传。 -->
  <DialogShell v-if="forceConfirm" :title="forceLabel" size="sm" class="trash-force-modal" @close="closeForceConfirm">
    <p>将把 {{ forceConfirm.rows.length }} 条记录从回收站移除，不做任何文件操作：文件如果还在废纸篓或原处，会原样留在那里，需要你自己处理。移除后这些条目无法在应用里恢复。</p>
    <ul class="trash-force-names">
      <li v-for="row in forceConfirm.rows.slice(0, 8)" :key="row.id">{{ row.name }}</li>
      <li v-if="forceConfirm.rows.length > 8">等 {{ forceConfirm.rows.length }} 项</li>
    </ul>
    <label class="trash-force-label" for="trash-force-input">输入「{{ forceConfirmText }}」以确认，此操作无法撤销</label>
    <input id="trash-force-input" v-model="forceConfirm.text" type="text" class="text-input" autocomplete="off" data-test="trash-force-input" />
    <template #footer>
      <button type="button" class="btn btn--secondary" data-test="trash-force-cancel" @click="closeForceConfirm">取消</button>
      <button
        type="button"
        class="btn btn--danger"
        :disabled="!forceTypedMatches || currentList.busy"
        data-test="trash-force-confirm"
        @click="confirmForceRemove"
      >{{ forceConfirmText }}</button>
    </template>
  </DialogShell>
</template>

<script>
import {
  DeleteStagedSources, ForceRemoveTrashRecords, GetTrashUsage, ListHiddenImages, ListStagedSources, ListTrashEntries,
  PurgeTrashEntries, RecheckImages, RemoveGoneTrashEntries, RestoreTrashEntries, TrashStagedSources
} from '../../wailsjs/go/main/App';
import { formatBytes } from '../utils/mediaDetails.js';
import { deletionActor } from '../utils/deletionActor.js';
import { confirmAction, confirmTextMatches, feedbackState } from '../utils/feedback.js';
import AppBanner from './ui/AppBanner.vue';
import AppSpinner from './ui/AppSpinner.vue';
import AppTabs from './ui/AppTabs.vue';
import DialogShell from './ui/DialogShell.vue';
import EmptyState from './ui/EmptyState.vue';

export const TRASH_PAGE_SIZE = 50;
// 与后端 TrashForceRemoveConfirmText 一致：「仍然移除记录」接口必须原样传回这四个字（协议常量，不是界面文字）。
export const FORCE_REMOVE_CONFIRM_TEXT = '移除记录';
// 界面上的动作名（D-UD-04/08）：回收站只指应用内的删除记录，把记录从回收站拿掉叫「从回收站移除」；
// 不可逆，确认时输入的就是按钮上的这几个字（D-UD-07 第 3 级）。
export const REMOVE_RECORD_LABEL = '从回收站移除';
const FORCE_LABEL = '仍然从回收站移除（不动文件）';

// 删除与回收站接口的单项结果码（services/trash_service.go TrashResult*）。
const RESULT_TEXT = {
  ok: '已完成',
  file_missing: '文件已不在原处，只移除了记录',
  trash_unsupported: '所在磁盘不支持废纸篓',
  permission_denied: '没有权限访问文件所在位置，未做任何改动',
  volume_offline: '所在磁盘未连接或不可访问，未做任何改动',
  cancelled: '已取消，未处理',
  path_occupied: '原位置已收录了新文件',
  identity_mismatch: '文件与删除时记录的不一致（可能已被替换），未做任何改动',
  not_purgeable: '该条目不能执行这个操作',
  file_gone: '废纸篓里的文件已被清除，只能从回收站移除',
  error: '操作失败'
};
// 这几类同一个结果码背后有不同原因（放回原处、只删记录的屏蔽、原位置是符号链接……），
// 服务端的单项文案更具体，有就用它。
const PREFER_MESSAGE = new Set(['not_purgeable', 'path_occupied', 'error']);
// 清除或移除记录被这些原因拒绝时，才提供「仍然移除记录（不动文件）」这个出口（修复 G m3）。
const FORCE_TRIGGER_CODES = new Set(['volume_offline', 'permission_denied']);

// 面向用户的文案不带绝对路径（G-3）：服务端已擦除，这里对抛出的错误再兜一层。
export function cleanTrashMessage(value) {
  let text = String(value ?? '').trim();
  if (!text) return '';
  if (/database is in maintenance mode/i.test(text)) return '数据库正在维护（恢复或切换后端），暂时不能操作';
  text = text.replace(/(?:~|[A-Za-z]:)?(?:[\\/][^\\/\s:：，,；;）)」"']+){2,}/g, '（路径已隐藏）');
  return text;
}

export function trashErrorText(err) {
  return cleanTrashMessage(err?.message || err) || RESULT_TEXT.error;
}

export function trashResultText(code, message) {
  const text = cleanTrashMessage(message);
  if (PREFER_MESSAGE.has(code) && text) return text;
  return RESULT_TEXT[code] || text || RESULT_TEXT.error;
}

export function isTrashItemSuccess(code) {
  return code === 'ok' || code === 'file_missing';
}

// 把失败项按原因归并成一句话：「所在磁盘未连接或不可访问…（2 项）；没有权限…（1 项）」。
export function summarizeTrashFailures(items) {
  const counts = new Map();
  for (const item of items || []) {
    const text = item.text || trashResultText(item.code, item.message);
    counts.set(text, (counts.get(text) || 0) + 1);
  }
  return [...counts.entries()].map(([text, count]) => `${text}（${count} 项）`).join('；');
}

function emptyEntryList() {
  return { items: [], nextCursor: 0, hasMore: false, loading: false, loaded: false, error: '', selected: [], busy: false, notice: '', token: 0, listFilter: null };
}

function emptyFilter() {
  return { mode: '', deletedBy: '', query: '' };
}

const TAB_KEYS = ['video', 'image', 'hidden', 'staged'];

export default {
  name: 'TrashCenterDialog',
  components: { AppBanner, AppSpinner, AppTabs, DialogShell, EmptyState },
  props: {
    visible: { type: Boolean, default: false },
    // 打开时停在哪个页签：video / image / hidden / staged。
    initialTab: { type: String, default: 'video' }
  },
  // restored：{ kind, entityIDs }，恢复（或重新检查找回）了媒体，宿主页据此刷新列表；
  // changed：{ kind, action }，回收站内容有变化（清除、移除记录等）。
  emits: ['close', 'restored', 'changed'],
  data() {
    return {
      activeTab: 'video',
      usage: null,
      usageError: '',
      lists: { video: emptyEntryList(), image: emptyEntryList() },
      filters: { video: emptyFilter(), image: emptyFilter() },
      hidden: { items: [], nextCursor: 0, hasMore: false, loading: false, loaded: false, error: '', selected: [], busy: false, notice: '', token: 0 },
      staged: { items: [], loading: false, loaded: false, error: '', selected: [], busy: false, notice: '', token: 0 },
      // 本次打开期间各条目最近一次操作的失败（键为 kind:id）：显示原因，并决定是否提供「仍然移除记录」。
      rowFailures: {},
      forceConfirm: null,
      forceConfirmText: REMOVE_RECORD_LABEL,
      removeRecordLabel: REMOVE_RECORD_LABEL,
      forceLabel: FORCE_LABEL,
      modeOptions: [
        { value: '', label: '全部删除方式' },
        { value: 'trash', label: '移到废纸篓' },
        { value: 'legacy_trash', label: '旧版回收站目录' },
        { value: 'record_only', label: '从片库移除' },
        { value: 'missing', label: '扫描时文件已不在' }
      ],
      deletedByOptions: [
        { value: '', label: '全部删除人' },
        { value: 'user', label: '用户' },
        { value: 'scanner', label: '程序（扫描）' }
      ]
    };
  },
  computed: {
    tabs() {
      const usage = this.usage;
      const count = value => (usage ? String(Number(value?.count || 0)) : null);
      return [
        { key: 'video', label: '视频', count: count(usage?.video), dataTest: 'trash-tab-video' },
        { key: 'image', label: '图片', count: count(usage?.image), dataTest: 'trash-tab-image' },
        { key: 'hidden', label: '扫描隐藏', count: null, dataTest: 'trash-tab-hidden' },
        { key: 'staged', label: '迁移残留', count: count(usage?.staged), dataTest: 'trash-tab-staged' }
      ];
    },
    // 当前页签对应的面板：与 AppTabs 的 id-prefix 对上（trash-center-tab-* / trash-center-panel-*）。
    tabPanelAttrs() {
      return { id: `trash-center-panel-${this.activeTab}`, role: 'tabpanel', 'aria-labelledby': `trash-center-tab-${this.activeTab}` };
    },
    isEntryTab() {
      return this.activeTab === 'video' || this.activeTab === 'image';
    },
    currentList() {
      return this.lists[this.isEntryTab ? this.activeTab : 'video'];
    },
    currentFilter() {
      return this.filters[this.isEntryTab ? this.activeTab : 'video'];
    },
    hasActiveFilter() {
      const filter = this.currentFilter;
      return Boolean(filter.mode || filter.deletedBy || filter.query.trim());
    },
    selectedRows() {
      const selected = new Set(this.currentList.selected);
      return this.currentList.items.filter(entry => selected.has(entry.id));
    },
    selectedForceable() {
      return this.selectedRows.filter(entry => this.isForceEligible(entry));
    },
    allLoadedSelected() {
      const items = this.currentList.items;
      return items.length > 0 && items.every(entry => this.currentList.selected.includes(entry.id));
    },
    // 「全选当前结果」（D-UD-10）：结果全部加载完、且每一条都选中了才算全选。
    allEntriesSelected() {
      return this.allLoadedSelected && !this.currentList.hasMore;
    },
    hiddenAllSelected() {
      return !this.hidden.hasMore && this.hidden.items.length > 0 && this.hidden.items.every(item => this.hidden.selected.includes(item.id));
    },
    forceTypedMatches() {
      return confirmTextMatches({ requiredText: this.forceConfirmText }, this.forceConfirm?.text);
    },
    stagedAllSelected() {
      return this.staged.items.length > 0 && this.staged.items.every(item => this.staged.selected.includes(item.id));
    },
    usageText() {
      const usage = this.usage;
      if (!usage) return '';
      if (this.activeTab === 'staged') {
        const staged = usage.staged || {};
        return `迁移残留 ${Number(staged.count || 0)} 项，共 ${formatBytes(staged.bytes || 0)}。`;
      }
      if (this.activeTab === 'hidden') return '';
      const kind = usage[this.activeTab] || {};
      const unit = this.activeTab === 'image' ? '张图片' : '个视频';
      const parts = [`共 ${Number(kind.count || 0)} 条记录，废纸篓中占用 ${formatBytes(kind.bytes_in_trash || 0)}`];
      if (Number(kind.legacy_count || 0) > 0) {
        parts.push(`其中 ${kind.legacy_count} ${unit}在旧版回收站目录（${formatBytes(kind.legacy_bytes || 0)}）`);
      }
      if (Number(kind.gone_count || 0) > 0) {
        parts.push(`${kind.gone_count} 条的文件已从废纸篓清除`);
      }
      return `${parts.join('，')}。`;
    }
  },
  watch: {
    visible: {
      immediate: true,
      handler(value) {
        if (value) {
          this.open();
        } else {
          this.invalidate();
        }
      }
    }
  },
  methods: {
    formatBytes,
    deletionActor,
    open() {
      this.activeTab = TAB_KEYS.includes(this.initialTab) ? this.initialTab : 'video';
      // 令牌只增不减：上一次打开时还在路上的响应不能和这次的请求撞号。
      this.lists = {
        video: { ...emptyEntryList(), token: this.lists.video.token + 1 },
        image: { ...emptyEntryList(), token: this.lists.image.token + 1 }
      };
      this.hidden = { ...this.hidden, items: [], selected: [], loaded: false, notice: '', error: '' };
      this.staged = { ...this.staged, items: [], selected: [], loaded: false, notice: '', error: '' };
      this.rowFailures = {};
      this.forceConfirm = null;
      this.loadUsage();
      this.loadTab(this.activeTab);
    },
    // 关闭后仍在路上的响应一律丢弃。
    invalidate() {
      this.lists.video.token += 1;
      this.lists.image.token += 1;
      this.hidden.token += 1;
      this.staged.token += 1;
      this.forceConfirm = null;
    },
    requestClose() {
      // 二次确认（应用确认框或输入确认）还开着时，Esc 只关掉确认框，不连带关掉回收站。
      if (this.forceConfirm || feedbackState.confirm) return;
      this.$emit('close');
    },
    selectTab(key) {
      if (!TAB_KEYS.includes(key) || this.activeTab === key) return;
      this.activeTab = key;
      this.loadTab(key);
    },
    loadTab(key) {
      if (key === 'video' || key === 'image') {
        if (!this.lists[key].loaded) this.loadEntries(key);
      } else if (key === 'hidden') {
        if (!this.hidden.loaded) this.loadHidden();
      } else if (key === 'staged') {
        if (!this.staged.loaded) this.loadStaged();
      }
    },
    async loadUsage() {
      try {
        const usage = await GetTrashUsage();
        if (!this.visible) return;
        this.usage = usage || null;
        this.usageError = '';
      } catch (err) {
        if (!this.visible) return;
        this.usageError = `读取回收站用量失败：${trashErrorText(err)}`;
      }
    },
    reloadEntries(kind) {
      this.lists[kind].selected = [];
      return this.loadEntries(kind);
    },
    async loadEntries(kind, { append = false } = {}) {
      const state = this.lists[kind];
      const token = ++state.token;
      state.loading = true;
      state.error = '';
      // 往后翻页用这份列表第一页时的筛选：搜索框里打了字还没回车时，「加载更多」「全选当前结果」不能把新词的结果
      // 拼到旧列表后面。重读（非 append）才换成当前筛选。
      const filter = append && state.listFilter ? state.listFilter : { ...this.filters[kind] };
      if (!append) state.listFilter = filter;
      try {
        const page = await ListTrashEntries({
          kind,
          mode: filter.mode,
          deleted_by: filter.deletedBy,
          query: filter.query.trim(),
          cursor_id: append ? state.nextCursor : 0,
          limit: TRASH_PAGE_SIZE
        });
        if (token !== state.token || !this.visible) return;
        const items = page?.items || [];
        if (append) {
          const known = new Set(state.items.map(entry => entry.id));
          state.items = [...state.items, ...items.filter(entry => !known.has(entry.id))];
        } else {
          state.items = items;
          const visibleIDs = new Set(items.map(entry => entry.id));
          state.selected = state.selected.filter(id => visibleIDs.has(id));
        }
        state.nextCursor = Number(page?.next_cursor || 0);
        state.hasMore = Boolean(page?.has_more);
        state.loaded = true;
      } catch (err) {
        if (token !== state.token || !this.visible) return;
        state.error = `读取回收站失败：${trashErrorText(err)}`;
      } finally {
        if (token === state.token) state.loading = false;
      }
    },
    toggleSelected(id) {
      const state = this.currentList;
      state.selected = state.selected.includes(id)
        ? state.selected.filter(item => item !== id)
        : [...state.selected, id];
    },
    // 「全选当前结果」作用于整个筛选结果：还有没加载的页时先按当前筛选取完，再全部选中；
    // 取的途中失败、换了页签或筛选（请求令牌不是这次补取自己发的那个，或筛选与点的时候不同）就不选，
    // 免得只选中一部分却显示成全选，或把换了筛选之后的另一批结果当成用户点的那一批选上。
    async toggleSelectAll() {
      const kind = this.activeTab;
      const state = this.lists[kind];
      if (this.allEntriesSelected) {
        state.selected = [];
        return;
      }
      const listFilter = state.listFilter;
      let token = state.token;
      const unchanged = () => state.token === token && this.visible && this.activeTab === kind && state.listFilter === listFilter;
      while (state.hasMore && !state.error && unchanged()) {
        const before = state.items.length;
        await this.loadEntries(kind, { append: true });
        token += 1;
        if (state.items.length === before) break;
      }
      if (state.hasMore || state.error || !unchanged()) return;
      state.selected = state.items.map(entry => entry.id);
    },
    selectedWith(action) {
      return this.selectedRows.filter(entry => entry.actions?.includes(action));
    },
    failureKey(entry) {
      return `${entry.kind || this.activeTab}:${entry.id}`;
    },
    rowFailure(entry) {
      return this.rowFailures[this.failureKey(entry)] || null;
    },
    cleanText(value) {
      return cleanTrashMessage(value);
    },
    forcibleMode(entry) {
      if (['trash', 'legacy_trash', 'missing'].includes(entry.mode)) return true;
      // 回填 mode 之前的旧行：file_moved=true 的就是旧版 trash/ 条目。
      return !entry.mode && Boolean(entry.file_moved);
    },
    // 「仍然移除记录（不动文件）」只给清除、移除记录或恢复因磁盘离线、没有权限被拒绝过的条目；
    // 扫描时已消失（missing）的条目恢复失败后同样提供，否则它永远卡在回收站里。
    // 只删记录（record_only）的屏蔽只能用「允许重新收录」解除；已放回原处与已被收录的条目各有专门出口。
    isForceEligible(entry) {
      if (!entry || entry.put_back || entry.claimed_by_active) return false;
      if (!this.forcibleMode(entry)) return false;
      if (entry.state !== 'deleted' && entry.state !== 'file_gone') return false;
      const failure = this.rowFailure(entry);
      if (!failure) return false;
      if (FORCE_TRIGGER_CODES.has(failure.code)) return true;
      return entry.mode === 'missing' && failure.action === 'restore' && failure.code !== 'cancelled';
    },
    entryStatus(entry) {
      if (entry.state === 'pending_move') return '删除曾中断，可恢复原状态';
      if (entry.state === 'rollback') return '删除回滚曾中断，可继续恢复';
      if (entry.state === 'restoring') return '恢复曾中断，可继续处理';
      if (entry.state === 'file_gone') return '文件已从废纸篓清除';
      switch (entry.mode) {
        case 'trash': return '在废纸篓中';
        case 'legacy_trash': return '在旧版回收站目录中';
        case 'record_only': return '已从片库移除，文件保留在原处';
        case 'missing': return '删除时文件已不在原处';
        default: return entry.file_moved ? '在旧版回收站目录中' : '文件未由应用移动，仅恢复数据库记录';
      }
    },
    entryNotes(entry) {
      const notes = [];
      if (entry.put_back) {
        notes.push('已放回原处：文件已经回到原位置，恢复即可把记录还原。');
      }
      if (entry.claimed_by_active) {
        notes.push('原位置已由片库中的另一条记录收录，只能把这条旧记录从回收站移除（不动文件）');
      }
      if (entry.original_symlink) {
        const purgeBlocked = (entry.mode === 'trash' || entry.mode === 'legacy_trash') && !entry.actions?.includes('purge');
        notes.push(purgeBlocked
          ? '原位置是符号链接：恢复不会跟随它，因此不提供恢复；它正指向废纸篓里的这个文件，为免删掉仍在使用的内容，也不提供永久删除。'
          : '原位置是符号链接：恢复不会跟随它，因此不提供恢复。请先处理原位置。');
      }
      if (entry.mode === 'record_only' && entry.state === 'deleted') {
        notes.push('扫描到同一个文件不会再收录；「允许重新收录」会还原原记录（含标签与人物）。');
      }
      if (entry.state === 'file_gone' && !entry.claimed_by_active) {
        notes.push('废纸篓里已没有这个文件，只能把这条记录从回收站移除。');
      }
      return notes;
    },
    restoreLabel(entry) {
      if (entry.state === 'pending_move' || entry.state === 'rollback') return '恢复原状态';
      if (entry.mode === 'record_only') return '允许重新收录';
      return '恢复';
    },
    formatDate(value) {
      if (!value) return '时间未知';
      const date = new Date(value);
      if (Number.isNaN(date.getTime())) return '时间未知';
      return date.toLocaleString();
    },
    baseName(path) {
      const text = String(path || '');
      const parts = text.split(/[\\/]/).filter(Boolean);
      return parts.length ? parts[parts.length - 1] : text;
    },
    async restoreRows(rows) {
      return this.runEntryAction('restore', rows);
    },
    async purgeRows(rows) {
      if (!rows.length) return;
      const confirmed = await confirmAction({
        title: '永久删除',
        message: `将永久删除 ${rows.length} 个文件（废纸篓或旧版回收站目录里的那一份），并把这些记录从回收站移除。`,
        impact: '删除后无法恢复。',
        confirmText: '永久删除',
        level: 'irreversible',
        danger: true
      });
      if (!confirmed) return;
      return this.runEntryAction('purge', rows);
    },
    async removeRecordRows(rows) {
      if (!rows.length) return;
      const confirmed = await confirmAction({
        title: REMOVE_RECORD_LABEL,
        message: `将把 ${rows.length} 条记录从回收站移除。这些条目的文件已从废纸篓清除，或原位置已由另一条记录收录；不会改动任何文件。`,
        impact: '移除后这些条目无法在应用里恢复。',
        confirmText: REMOVE_RECORD_LABEL,
        level: 'irreversible',
        danger: true
      });
      if (!confirmed) return;
      return this.runEntryAction('remove_record', rows);
    },
    openForceConfirm(rows) {
      const eligible = rows.filter(entry => this.isForceEligible(entry));
      if (!eligible.length) return;
      this.forceConfirm = { kind: this.activeTab, rows: eligible, text: '' };
    },
    closeForceConfirm() {
      this.forceConfirm = null;
    },
    async confirmForceRemove() {
      const pending = this.forceConfirm;
      if (!pending || !this.forceTypedMatches) return;
      this.forceConfirm = null;
      return this.runEntryAction('force', pending.rows, FORCE_REMOVE_CONFIRM_TEXT);
    },
    async runEntryAction(action, rows, confirmText = '') {
      const kind = this.activeTab;
      if (kind !== 'video' && kind !== 'image') return;
      const state = this.lists[kind];
      if (!rows.length || state.busy) return;
      const ids = rows.map(entry => entry.id);
      state.busy = true;
      state.notice = '';
      const label = { restore: '恢复', purge: '永久删除', remove_record: REMOVE_RECORD_LABEL, force: '仍然从回收站移除' }[action];
      try {
        let result;
        if (action === 'restore') result = await RestoreTrashEntries(kind, ids);
        else if (action === 'purge') result = await PurgeTrashEntries(kind, ids);
        else if (action === 'remove_record') result = await RemoveGoneTrashEntries(kind, ids);
        else result = await ForceRemoveTrashRecords(kind, ids, confirmText);
        this.applyEntryResult(kind, action, label, rows, result);
      } catch (err) {
        state.notice = `${label}失败：${trashErrorText(err)}`;
      } finally {
        state.busy = false;
        this.loadUsage();
      }
    },
    applyEntryResult(kind, action, label, rows, result) {
      const state = this.lists[kind];
      const byID = new Map(rows.map(entry => [entry.id, entry]));
      const done = [];
      const failed = [];
      const failures = { ...this.rowFailures };
      for (const item of result?.items || []) {
        const key = `${kind}:${item.id}`;
        if (isTrashItemSuccess(item.code)) {
          done.push(item.id);
          delete failures[key];
          continue;
        }
        const text = trashResultText(item.code, item.message);
        failures[key] = { code: item.code, text: `${label}失败：${text}`, action };
        failed.push({ code: item.code, text });
      }
      this.rowFailures = failures;
      const doneSet = new Set(done);
      state.items = state.items.filter(entry => !doneSet.has(entry.id));
      state.selected = state.selected.filter(id => !doneSet.has(id));
      state.notice = failed.length
        ? `${label}：成功 ${done.length} 项，失败 ${failed.length} 项。${summarizeTrashFailures(failed)}`
        : `${label}完成：${done.length} 项。`;
      if (!done.length) return;
      if (action === 'restore') {
        const entityIDs = done.map(id => byID.get(id)?.entity_id).filter(Boolean);
        this.$emit('restored', { kind, entityIDs });
      }
      this.$emit('changed', { kind, action });
    },
    hiddenReason(reason) {
      return ({
        offline_root: '所在磁盘未连接',
        missing_file: '扫描时文件不在原处',
        removed_root: '所在目录已不在扫描范围'
      })[reason] || '原因未记录';
    },
    async loadHidden({ append = false } = {}) {
      const state = this.hidden;
      const token = ++state.token;
      state.loading = true;
      state.error = '';
      try {
        const page = await ListHiddenImages(append ? state.nextCursor : 0, TRASH_PAGE_SIZE);
        if (token !== state.token || !this.visible) return;
        const items = page?.items || [];
        if (append) {
          const known = new Set(state.items.map(item => item.id));
          state.items = [...state.items, ...items.filter(item => !known.has(item.id))];
        } else {
          state.items = items;
          const visibleIDs = new Set(items.map(item => item.id));
          state.selected = state.selected.filter(id => visibleIDs.has(id));
        }
        state.nextCursor = Number(page?.next_cursor || 0);
        state.hasMore = Boolean(page?.has_more);
        state.loaded = true;
      } catch (err) {
        if (token !== state.token || !this.visible) return;
        state.error = `读取扫描隐藏的图片失败：${trashErrorText(err)}`;
      } finally {
        if (token === state.token) state.loading = false;
      }
    },
    toggleHiddenSelected(id) {
      const state = this.hidden;
      state.selected = state.selected.includes(id) ? state.selected.filter(item => item !== id) : [...state.selected, id];
    },
    // 与 toggleSelectAll 同一做法：途中重读了列表（令牌不是自己发的那个）或换了页签就不选。
    async toggleHiddenSelectAll() {
      const state = this.hidden;
      if (this.hiddenAllSelected) {
        state.selected = [];
        return;
      }
      let token = state.token;
      const unchanged = () => state.token === token && this.visible && this.activeTab === 'hidden';
      while (state.hasMore && !state.error && unchanged()) {
        const before = state.items.length;
        await this.loadHidden({ append: true });
        token += 1;
        if (state.items.length === before) break;
      }
      if (state.hasMore || state.error || !unchanged()) return;
      state.selected = state.items.map(item => item.id);
    },
    async recheckHidden() {
      const state = this.hidden;
      const ids = [...state.selected];
      if (!ids.length || state.busy) return;
      state.busy = true;
      state.notice = '';
      try {
        const result = await RecheckImages(ids);
        const restored = Number(result?.restored || 0);
        const added = Number(result?.added || 0);
        const relocated = Number(result?.relocated || 0);
        const errors = (result?.errors || []).length;
        state.notice = `重新检查完成：恢复 ${restored} 张，新增 ${added} 张，重新定位 ${relocated} 张${errors ? `，${errors} 个文件读取失败` : ''}。`;
        if (restored + added + relocated > 0) this.$emit('restored', { kind: 'image', entityIDs: [] });
        state.selected = [];
        await this.loadHidden();
      } catch (err) {
        state.notice = `重新检查失败：${trashErrorText(err)}`;
      } finally {
        state.busy = false;
        this.loadUsage();
      }
    },
    async loadStaged() {
      const state = this.staged;
      const token = ++state.token;
      state.loading = true;
      state.error = '';
      try {
        const items = await ListStagedSources() || [];
        if (token !== state.token || !this.visible) return;
        state.items = items;
        const visibleIDs = new Set(items.map(item => item.id));
        state.selected = state.selected.filter(id => visibleIDs.has(id));
        state.loaded = true;
      } catch (err) {
        if (token !== state.token || !this.visible) return;
        state.error = `读取迁移残留失败：${trashErrorText(err)}`;
      } finally {
        if (token === state.token) state.loading = false;
      }
    },
    toggleStagedSelected(id) {
      const state = this.staged;
      state.selected = state.selected.includes(id) ? state.selected.filter(item => item !== id) : [...state.selected, id];
    },
    toggleStagedSelectAll() {
      this.staged.selected = this.stagedAllSelected ? [] : this.staged.items.map(item => item.id);
    },
    async trashStaged() {
      const state = this.staged;
      const ids = [...state.selected];
      if (!ids.length || state.busy) return;
      state.busy = true;
      state.notice = '';
      try {
        const result = await TrashStagedSources(ids);
        const failed = (result?.items || []).filter(item => !isTrashItemSuccess(item.code))
          .map(item => ({ code: item.code, text: trashResultText(item.code, item.message) }));
        const done = ids.length - failed.length;
        state.notice = failed.length
          ? `移到废纸篓：成功 ${done} 项，失败 ${failed.length} 项。${summarizeTrashFailures(failed)}`
          : `已移到废纸篓 ${done} 项。`;
        state.selected = [];
        await this.loadStaged();
        if (done > 0) this.$emit('changed', { kind: 'staged', action: 'trash' });
      } catch (err) {
        state.notice = `移到废纸篓失败：${trashErrorText(err)}`;
      } finally {
        state.busy = false;
        this.loadUsage();
      }
    },
    async deleteStaged() {
      const state = this.staged;
      const ids = [...state.selected];
      if (!ids.length || state.busy) return;
      const confirmed = await confirmAction({
        title: '永久删除迁移残留',
        message: `将永久删除 ${ids.length} 份迁移时保留的源文件，不经过废纸篓。请先确认新位置的文件正常。`,
        impact: '删除后无法恢复。',
        confirmText: '永久删除',
        level: 'irreversible',
        danger: true
      });
      if (!confirmed) return;
      state.busy = true;
      state.notice = '';
      try {
        const result = await DeleteStagedSources(ids);
        const failed = (result?.failed || []).map(item => ({ code: 'error', text: cleanTrashMessage(item.error) || RESULT_TEXT.error }));
        const cleaned = Number(result?.cleaned || 0);
        state.notice = failed.length
          ? `永久删除：成功 ${cleaned} 项，失败 ${failed.length} 项。${summarizeTrashFailures(failed)}`
          : `已永久删除 ${cleaned} 项。`;
        state.selected = [];
        await this.loadStaged();
        if (cleaned > 0) this.$emit('changed', { kind: 'staged', action: 'delete' });
      } catch (err) {
        state.notice = `永久删除失败：${trashErrorText(err)}`;
      } finally {
        state.busy = false;
        this.loadUsage();
      }
    }
  }
};
</script>

<style scoped>
.trash-center-hint,
.trash-center-usage,
.trash-center-muted,
.trash-center-state,
.trash-center-entry__body span,
.trash-center-entry__body small {
  color: var(--text-muted);
}

.trash-center-hint,
.trash-center-usage,
.trash-center-muted,
.trash-center-notice {
  margin: 0;
  font-size: var(--fs-caption);
}

.trash-center-hint,
.trash-center-usage { margin-bottom: var(--space-3); }

.trash-center-tabs { margin-bottom: var(--space-3); }

.trash-center-panel {
  min-height: 0;
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.trash-center-filters,
.trash-center-bulk {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-2);
}

.trash-center-filters .search-input {
  flex: 1 1 180px;
  min-width: 0;
}

/* 两个筛选下拉按内容宽度排在一行，搜索框占剩下的宽度。 */
.trash-center-filters .select-input {
  flex: 0 0 auto;
  width: auto;
  min-width: 150px;
}

.trash-center-select-all {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: var(--fs-caption);
}

.trash-center-notice {
  color: var(--text-secondary);
}

.trash-center-list {
  min-height: 0;
  max-height: 420px;
  overflow-y: auto;
  display: grid;
  gap: var(--space-2);
}

.trash-center-entry {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  padding: 10px var(--space-3);
  border: 1px solid var(--border-color);
  border-radius: var(--radius);
}

.trash-center-entry__check {
  flex: 0 0 auto;
}

.trash-center-entry__body {
  flex: 1 1 auto;
  min-width: 0;
  display: grid;
  gap: 3px;
}

.trash-center-entry__body strong,
.trash-center-entry__body span,
.trash-center-entry__body small {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.trash-center-entry__body .trash-center-entry__note {
  color: var(--warning-strong);
  white-space: normal;
}

.trash-center-entry__actions {
  flex: 0 0 auto;
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 6px;
  max-width: 280px;
}

.trash-center-entry__body .trash-center-entry__error,
.trash-center-error {
  color: var(--danger-text) !important;
  white-space: normal;
}

.trash-center-state {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: var(--space-2);
  padding: 28px var(--space-3);
  font-size: var(--fs-secondary);
}

.trash-center-more {
  justify-self: center;
}

.trash-force-names {
  margin: var(--space-2) 0 var(--space-3);
  padding-left: 18px;
  color: var(--text-secondary);
  font-size: var(--fs-secondary);
}

.trash-force-label {
  display: block;
  margin-bottom: 6px;
  color: var(--text-secondary);
  font-size: var(--fs-secondary);
}
</style>
