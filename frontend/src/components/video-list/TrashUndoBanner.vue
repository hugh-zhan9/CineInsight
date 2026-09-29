<template>
  <!-- 进度浮在最上层：删除常从确认框或清理审阅里发起，嵌在页面里的条会被弹窗遮住，「取消」就点不到了。 -->
  <div v-if="progress" class="delete-progress-float" role="status" data-test="delete-progress">
    <span>正在删除 {{ progress.done }}/{{ progress.total }} {{ unit }}…</span>
    <span class="delete-progress-float__bar" aria-hidden="true"><i :style="{ width: progressPercent + '%' }"></i></span>
    <button type="button" class="btn-secondary btn-compact" :disabled="progress.cancelling" data-test="delete-progress-cancel" @click="cancelDelete">
      {{ progress.cancelling ? '正在取消...' : '取消' }}
    </button>
  </div>
  <div v-if="undoNotice" class="undo-delete-banner" role="status" data-test="delete-undo-banner">
    <span>{{ undoNotice.text }}</span>
    <button
      v-if="undoNotice.undoCount > 0"
      type="button"
      class="btn-primary btn-compact"
      :disabled="undoing"
      data-test="delete-undo"
      @click="undoLastDelete"
    >{{ undoing ? '撤销中...' : `撤销本次删除（${undoNotice.undoCount} 项）` }}</button>
    <button type="button" class="btn-secondary btn-compact" data-test="delete-open-trash" @click="openTrashDialog()">查看回收站</button>
    <button type="button" class="undo-delete-banner__close" aria-label="关闭提示" @click="dismissNotice">×</button>
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
import { notifyError } from '../../utils/feedback.js';

const NOTICE_TIMEOUT_MS = 12000;

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

// 删除流程的宿主（片库页与图片页共用）：批量删除的进度与取消（D-PC51）、卷不支持废纸篓时的
// 二选一（D-PC02）、删除后的撤销提示条（按 delete_batch_id 整批撤销，D-PC04）与回收站对话框。
// 恢复之后要做的事（清理面板的已删标记、列表重载、清理状态刷新）仍归宿主页，
// 用 afterRestore(entityIDs, fromTrashDialog) 回调。
export default {
  name: 'TrashUndoBanner',
  components: { TrashCenterDialog, TrashUnsupportedDialog },
  props: {
    kind: { type: String, default: 'video' },
    afterRestore: { type: Function, required: true }
  },
  data() {
    return {
      trashDialog: { show: false, tab: 'video' },
      undoNotice: null,
      undoNoticeTimer: null,
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
    progressPercent() {
      const total = Number(this.progress?.total || 0);
      if (total <= 0) return 0;
      return Math.min(100, Math.round((Number(this.progress.done || 0) / total) * 100));
    }
  },
  beforeUnmount() {
    this.clearNoticeTimer();
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
      if (expected > 1) this.startProgress(requestID, expected);
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
    startProgress(requestID, total) {
      this.stopProgress();
      this.progress = { requestID, done: 0, total, cancelling: false };
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
    // 删除完成后的提示：成功的部分给撤销条（整批撤销），失败的部分按原因报错。
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
      this.undoNotice = {
        kind: outcome.kind || this.kind,
        text,
        batchIDs: [...outcome.batchIDs],
        restorableIDs: restorable,
        undoCount: outcome.batchIDs.length ? restorable.length : 0
      };
      this.clearNoticeTimer();
      this.undoNoticeTimer = window.setTimeout(() => {
        this.undoNotice = null;
        this.undoNoticeTimer = null;
      }, NOTICE_TIMEOUT_MS);
    },
    clearNoticeTimer() {
      if (this.undoNoticeTimer) clearTimeout(this.undoNoticeTimer);
      this.undoNoticeTimer = null;
    },
    dismissNotice() {
      this.clearNoticeTimer();
      this.undoNotice = null;
    },
    // 「撤销本次删除（N 项）」：按批次恢复这一次删除留下的全部条目。
    async undoLastDelete() {
      const notice = this.undoNotice;
      if (!notice || this.undoing) return;
      if (!notice.undoCount) {
        this.openTrashDialog();
        return;
      }
      this.undoing = true;
      try {
        let restored = 0;
        const failures = [];
        for (const batchID of notice.batchIDs) {
          const result = await RestoreTrashBatch(notice.kind, batchID);
          for (const item of result?.items || []) {
            if (isTrashItemSuccess(item.code)) restored += 1;
            else failures.push({ id: item.id, code: item.code, text: trashResultText(item.code, item.message) });
          }
        }
        this.dismissNotice();
        if (failures.length) {
          notifyError(`撤销完成：恢复 ${restored} 项，${failures.length} 项未恢复：${summarizeTrashFailures(failures)}`);
          // 哪几项没回来只有回收站知道：不逐个放掉清理面板的标记，交给宿主重新读取状态。
          await this.afterRestore([], true);
        } else {
          await this.afterRestore(notice.restorableIDs, false);
        }
      } catch (err) {
        console.error('撤销删除失败:', err);
        notifyError(`撤销删除失败：${trashErrorText(err)}`);
      } finally {
        this.undoing = false;
      }
    },
    // 回收站对话框恢复了本页这一类媒体：{ kind, entityIDs }。
    async handleTrashRestored(payload) {
      if (payload?.kind && payload.kind !== this.kind) return;
      this.dismissNotice();
      await this.afterRestore(payload?.entityIDs || [], true);
    }
  }
};
</script>

<style scoped>
.undo-delete-banner {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0 0 10px;
  padding: 8px 10px;
  border: 1px solid var(--accent-border);
  border-radius: var(--radius);
  background: var(--control-bg);
  color: var(--text-secondary);
  font-size: 13px;
}

.delete-progress-float {
  position: fixed;
  bottom: 24px;
  left: 50%;
  z-index: 1100;
  transform: translateX(-50%);
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 12px;
  border: 1px solid var(--accent-border);
  border-radius: var(--radius);
  background: var(--panel-bg);
  box-shadow: var(--shadow-modal);
  color: var(--text-secondary);
  font-size: 13px;
}

.delete-progress-float__bar {
  width: 160px;
  height: 6px;
  overflow: hidden;
  border-radius: 3px;
  background: var(--border-color);
}

.delete-progress-float__bar i {
  display: block;
  height: 100%;
  background: var(--accent-color);
  transition: width var(--transition);
}

.undo-delete-banner__close {
  margin-left: auto;
  border: 0;
  background: transparent;
  color: var(--text-muted);
  cursor: pointer;
  font-size: 18px;
}
.btn-compact {
  height: 28px;
  padding: 0 10px;
  font-size: 12px;
}
</style>
