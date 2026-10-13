<template>
  <!-- 进度浮在最上层：删除常从确认框或清理审阅里发起，嵌在页面里的条会被弹窗遮住，「取消」就点不到了。 -->
  <div v-if="progress" class="delete-progress-float" role="status" data-test="delete-progress">
    <ProgressBar
      class="delete-progress-float__bar"
      :value="progressRatio"
      :label="`${progressVerb} ${progress.done}/${progress.total} ${unit}…`"
    />
    <button type="button" class="btn btn--secondary btn--sm" :disabled="progress.cancelling" data-test="delete-progress-cancel" @click="cancelDelete">
      {{ progress.cancelling ? '正在取消…' : '取消' }}
    </button>
  </div>
  <TrashUnsupportedDialog
    :visible="unsupported.show"
    :kind="kind"
    :items="unsupported.items"
    :busy="unsupported.busy"
    @permanent-delete="chooseUnsupported('permanent')"
    @record-only="chooseUnsupported('record_only')"
    @cancel="chooseUnsupported('cancel')"
  />

  <TrashCenterDialog
    :visible="trashDialog.show"
    :initial-tab="trashDialog.tab"
    @close="trashDialog.show = false"
    @restored="handleTrashRestored"
  />
</template>

<script>
import {
  CancelBatchDelete, DeleteImagesWithResult, DeleteVideosWithResult, PermanentlyDeleteImages, PermanentlyDeleteVideos, RestoreTrashBatch
} from '../../../wailsjs/go/main/App';
import TrashCenterDialog, { isTrashItemSuccess, summarizeTrashFailures, trashErrorText, trashResultText } from '../TrashCenterDialog.vue';
import TrashUnsupportedDialog from '../TrashUnsupportedDialog.vue';
import ProgressBar from '../ui/ProgressBar.vue';
import { notify, notifyError, notifySuccess, notifyUndoable } from '../../utils/feedback.js';

// 批量删除的请求标识：32 位十六进制（与后端随机标识同一格式）。
function newRequestID() {
  const bytes = globalThis.crypto.getRandomValues(new Uint8Array(16));
  return Array.from(bytes, byte => byte.toString(16).padStart(2, '0')).join('');
}

// 一次删除操作的结果（按项分类）。removedIDs = 已离开媒体库的项；remainingIDs = 仍在库里的项。
function emptyOutcome(kind, deleteFile) {
  return {
    kind,
    deleteFile,
    batchIDs: [],
    trashed: [],
    recordOnly: [],
    fileMissing: [],
    permanent: [],
    cancelled: [],
    kept: [],
    failures: [],
    get removedIDs() {
      return [...this.trashed, ...this.recordOnly, ...this.fileMissing, ...this.permanent];
    },
    get remainingIDs() {
      return [...this.failures.map(item => item.id), ...this.cancelled, ...this.kept];
    }
  };
}

// 按批次恢复一次删除留下的全部条目（RestoreTrashBatch），返回恢复数与没恢复的项；调用本身失败时抛出。
// RestoreTrashBatch 只恢复仍是已删除的条目：有项没回来时再试是安全的；一项都没恢复、也没有失败，
// 说明这批已经在回收站里恢复过了（见 ALREADY_RESTORED_MESSAGE）。
// 不依赖组件实例：人物页的删除对话框（PersonMediaDeleteDialog）关掉以后从「最近操作」撤销也走这一段。
// 恢复 0 项、失败 0 项：这批已经不在回收站里——可能已从回收站恢复，也可能已永久删除或从回收站移除。
export const ALREADY_RESTORED_MESSAGE = '这批已不在回收站（已恢复或已清除）';

// 撤销有项没回来时抛出的原因（提示里是「撤销失败：恢复 N 项，M 项未恢复：…」）。
export function partialRestoreError(restored, failures) {
  return new Error(`恢复 ${restored} 项，${failures.length} 项未恢复：${summarizeTrashFailures(failures)}`);
}

export async function restoreTrashBatches(kind, batchIDs) {
  let restored = 0;
  const failures = [];
  for (const batchID of batchIDs || []) {
    const result = await RestoreTrashBatch(kind, batchID);
    for (const item of result?.items || []) {
      if (isTrashItemSuccess(item.code)) restored += 1;
      else failures.push({ id: item.id, code: item.code, text: trashResultText(item.code, item.message) });
    }
  }
  return { restored, failures };
}

// 删除流程的宿主（片库页与图片页共用）：批量删除的进度与取消（D-PC51）、卷不支持废纸篓时的
// 二选一（D-PC02）、删除后的撤销（按 delete_batch_id 整批撤销，D-PC04）与回收站对话框。
// 撤销的出口（统一设计 D-UD-07/09）：删除成功后出带「撤销」的全局提示（8 秒），同时记进任务中心「最近操作」
// （notifyUndoable，两处是同一条记录，哪边先撤都只执行一次）；原来的页内撤销条已去掉。宿主的调用方式不变：
// runDelete → showDeleteNotice，openTrashDialog 打开回收站。
// 恢复之后要做的事（清理面板的已删标记、列表重载、清理状态刷新）仍归宿主页，
// 用 afterRestore(entityIDs, fromTrashDialog) 回调。
export default {
  name: 'TrashUndoBanner',
  components: { ProgressBar, TrashCenterDialog, TrashUnsupportedDialog },
  props: {
    kind: { type: String, default: 'video' },
    afterRestore: { type: Function, required: true }
  },
  data() {
    return {
      trashDialog: { show: false, tab: 'video' },
      // 提示或「最近操作」里的撤销正在恢复：洞察页据此锁住切页（恢复结果要传回宿主）。
      undoing: false,
      progress: null,
      unsupported: { show: false, items: [], busy: false, outcome: null, resolve: null }
    };
  },
  computed: {
    unit() {
      return this.kind === 'image' ? '张图片' : '个视频';
    },
    libraryName() {
      return this.kind === 'image' ? '图片库' : '片库';
    },
    progressRatio() {
      const total = Number(this.progress?.total || 0);
      if (total <= 0) return null;
      return Math.min(1, Number(this.progress.done || 0) / total);
    },
    // 进度条上的动作用删除的三个词（D-UD-08）：移文件的是「移到废纸篓」，只删记录的是「从片库移除」。
    progressVerb() {
      return this.progress?.deleteFile === false ? `正在从${this.libraryName}移除` : '正在移到废纸篓';
    }
  },
  beforeUnmount() {
    this.stopProgress();
    if (this.unsupported.resolve) this.unsupported.resolve();
  },
  methods: {
    openTrashDialog(tab = '') {
      this.trashDialog = { show: true, tab: tab || this.kind };
    },
    // 执行一次删除：ids 为要删的媒体 ID；names 用于不支持废纸篓时列出文件名；
    // invoke(requestID) 可替换默认的删除调用（例如按文件夹删除），total 是进度条的预计总数。
    // 返回 emptyOutcome 形状的结果，不弹提示；提示由 showDeleteNotice 负责。
    async runDelete({ ids = [], deleteFile = false, names = {}, invoke = null, total = null } = {}) {
      const unique = [...new Set((ids || []).map(Number).filter(Boolean))];
      const outcome = emptyOutcome(this.kind, deleteFile);
      if (!invoke && unique.length === 0) return outcome;
      const requestID = newRequestID();
      const expected = Number(total ?? unique.length) || 0;
      if (expected > 1) this.startProgress(requestID, expected, deleteFile);
      let result;
      try {
        result = invoke
          ? await invoke(requestID)
          : await this.deleteWithResult(unique, deleteFile, requestID);
      } finally {
        this.stopProgress();
      }
      this.classifyInto(outcome, result, deleteFile);
      if (outcome.kept.length > 0) {
        await this.resolveUnsupported(outcome, names);
      }
      return outcome;
    },
    deleteWithResult(ids, deleteFile, requestID) {
      return this.kind === 'image'
        ? DeleteImagesWithResult(ids, deleteFile, requestID)
        : DeleteVideosWithResult(ids, deleteFile, requestID);
    },
    // trash_unsupported 的项先记进 kept（仍在库里、未改动），等用户在弹窗里选择。
    classifyInto(outcome, result, deleteFile) {
      if (result?.batch_id) outcome.batchIDs.push(result.batch_id);
      for (const item of result?.items || []) {
        const id = Number(item.id);
        switch (item.code) {
          case 'ok':
            (deleteFile ? outcome.trashed : outcome.recordOnly).push(id);
            break;
          case 'file_missing':
            outcome.fileMissing.push(id);
            break;
          case 'trash_unsupported':
            outcome.kept.push(id);
            break;
          case 'cancelled':
            outcome.cancelled.push(id);
            break;
          default:
            outcome.failures.push({ id, code: item.code, text: trashResultText(item.code, item.message) });
        }
      }
    },
    resolveUnsupported(outcome, names) {
      const fallback = this.kind === 'image' ? '图片' : '视频';
      const items = outcome.kept.map(id => ({ id, name: names?.[id] || `${fallback}（编号 ${id}）` }));
      return new Promise(resolve => {
        this.unsupported = { show: true, items, busy: false, outcome, resolve };
      });
    },
    async chooseUnsupported(choice) {
      const state = this.unsupported;
      if (!state.show || state.busy || !state.outcome) return;
      const outcome = state.outcome;
      const ids = state.items.map(item => item.id);
      if (choice !== 'cancel') {
        state.busy = true;
        try {
          const result = choice === 'permanent'
            ? await (this.kind === 'image' ? PermanentlyDeleteImages(ids) : PermanentlyDeleteVideos(ids))
            : await this.deleteWithResult(ids, false, '');
          const handled = new Set();
          if (choice === 'record_only' && result?.batch_id) outcome.batchIDs.push(result.batch_id);
          for (const item of result?.items || []) {
            const id = Number(item.id);
            handled.add(id);
            if (isTrashItemSuccess(item.code)) {
              (choice === 'permanent' ? outcome.permanent : outcome.recordOnly).push(id);
            } else {
              outcome.failures.push({ id, code: item.code, text: trashResultText(item.code, item.message) });
            }
          }
          outcome.kept = outcome.kept.filter(id => !handled.has(id));
        } catch (err) {
          const text = trashErrorText(err);
          outcome.failures.push(...ids.map(id => ({ id, code: 'error', text })));
          outcome.kept = [];
        }
      }
      const resolve = state.resolve;
      this.unsupported = { show: false, items: [], busy: false, outcome: null, resolve: null };
      resolve?.();
    },
    startProgress(requestID, total, deleteFile = true) {
      this.stopProgress();
      this.progress = { requestID, done: 0, total, cancelling: false, deleteFile: deleteFile !== false };
      if (!window.runtime?.EventsOn) return;
      const off = window.runtime.EventsOn('batch-delete-progress', payload => {
        if (!this.progress || payload?.request_id !== this.progress.requestID) return;
        this.progress = {
          ...this.progress,
          done: Number(payload.done || 0),
          total: Number(payload.total || this.progress.total)
        };
      });
      this._progressOff = typeof off === 'function' ? off : null;
    },
    stopProgress() {
      try {
        this._progressOff?.();
      } catch (_err) {}
      this._progressOff = null;
      this.progress = null;
    },
    async cancelDelete() {
      const progress = this.progress;
      if (!progress || progress.cancelling) return;
      this.progress = { ...progress, cancelling: true };
      try {
        await CancelBatchDelete(progress.requestID);
      } catch (err) {
        notifyError(`取消删除失败：${trashErrorText(err)}`);
        if (this.progress?.requestID === progress.requestID) this.progress = { ...this.progress, cancelling: false };
      }
    },
    noticeText(outcome) {
      const unit = this.unit;
      const parts = [];
      if (outcome.trashed.length) parts.push(`已移到废纸篓 ${outcome.trashed.length} ${unit}`);
      if (outcome.recordOnly.length) parts.push(`已从${this.libraryName}移除 ${outcome.recordOnly.length} ${unit}（文件保留）`);
      if (outcome.fileMissing.length) parts.push(`${outcome.fileMissing.length} ${unit}的文件已不在原处，只移除了记录`);
      if (outcome.permanent.length) parts.push(`已永久删除 ${outcome.permanent.length} ${unit}`);
      if (outcome.cancelled.length) parts.push(`已取消，${outcome.cancelled.length} ${unit}未处理`);
      if (outcome.kept.length) parts.push(`${outcome.kept.length} ${unit}未删除（所在磁盘不支持废纸篓）`);
      return parts.length ? `${parts.join('；')}。` : '';
    },
    // 删除完成后的提示：成功的部分给整批撤销，失败的部分按原因报错。
    // reportFailures=false 时由调用方自己报告失败（清理面板有自己的失败提示）。
    showDeleteNotice(outcome, { reportFailures = true } = {}) {
      if (!outcome) return;
      const text = this.noticeText(outcome);
      if (reportFailures && outcome.failures.length) {
        notifyError(`${outcome.failures.length} ${this.unit}删除失败：${summarizeTrashFailures(outcome.failures)}`);
      }
      if (!text) return;
      // 只删记录与移到废纸篓的项可以整批撤销；文件本就不在的项恢复不了，永久删除的项没有条目。
      const restorable = [...outcome.trashed, ...outcome.recordOnly];
      const notice = {
        kind: outcome.kind || this.kind,
        text,
        batchIDs: [...outcome.batchIDs],
        restorableIDs: restorable,
        undoCount: outcome.batchIDs.length ? restorable.length : 0
      };
      // 可撤销的给「撤销」（并进任务中心「最近操作」）；没有可撤销的项（只剩文件本就不在、永久删除）只报结果。
      if (notice.undoCount > 0) notifyUndoable(text, () => this.undoFromToast(notice));
      else notifySuccess(text);
    },
    // 撤销提示是全局的，本组件没有页内条可收；保留给宿主调用（人物页删除在换一种媒体时会先收起另一个）。
    dismissNotice() {},
    restoreNoticeBatches(notice) {
      return restoreTrashBatches(notice.kind, notice.batchIDs);
    },
    // 有项没回来：让宿主以回收站为准重读（哪几项没回来只有回收站知道，不逐个放掉清理面板的标记），
    // 再整体算失败抛出——提示「撤销失败：恢复 N 项，M 项未恢复：原因」，「最近操作」里留着可以再试。
    // 一项都没恢复、也没有失败：这批已经从回收站恢复过了，按撤销完成处理（记录移除），告诉用户一声。
    async finishRestore(notice, { restored, failures }) {
      if (failures.length) {
        await this.afterRestore([], true);
        throw partialRestoreError(restored, failures);
      }
      if (restored === 0) {
        notify(ALREADY_RESTORED_MESSAGE);
        await this.afterRestore([], true);
        return;
      }
      await this.afterRestore(notice.restorableIDs, false);
    },
    // 全局提示或任务中心「最近操作」里的「撤销」：失败时抛出（提示「撤销失败：原因」，记录留着可再试）。
    async undoFromToast(notice) {
      this.undoing = true;
      try {
        let outcome;
        try {
          outcome = await this.restoreNoticeBatches(notice);
        } catch (err) {
          console.error('撤销删除失败:', err);
          throw new Error(trashErrorText(err));
        }
        await this.finishRestore(notice, outcome);
      } finally {
        this.undoing = false;
      }
    },
    // 回收站对话框恢复了本页这一类媒体：{ kind, entityIDs }。
    async handleTrashRestored(payload) {
      if (payload?.kind && payload.kind !== this.kind) return;
      await this.afterRestore(payload?.entityIDs || [], true);
    }
  }
};
</script>

<style scoped>
/* 压在弹窗之上：弹窗遮罩现在还是 components.css 的 1000（第 0 步 C 组改成 --z-modal 之后这里换 var(--z-toast)）。 */
.delete-progress-float {
  position: fixed;
  bottom: var(--space-5);
  left: 50%;
  z-index: 1100;
  transform: translateX(-50%);
  display: flex;
  align-items: flex-end;
  gap: var(--space-3);
  width: min(420px, calc(100vw - 32px));
  padding: var(--space-3) var(--space-3) var(--space-3) var(--space-4);
  border: 1px solid var(--accent-border);
  border-radius: var(--radius);
  background: var(--panel-bg);
  box-shadow: var(--shadow-modal);
  color: var(--text-secondary);
}

.delete-progress-float__bar { flex: 1; }
</style>
