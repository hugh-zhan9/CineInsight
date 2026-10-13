<template>
  <Teleport to="body">
    <div :class="['person-media-delete-layer', { 'person-media-delete-layer--notice': stage !== 'confirm', 'person-media-delete-layer--pending': deleteOriginal === null }]">
      <DialogShell v-if="stage === 'confirm'" :title="actionTitle" size="sm" :busy="busy" data-test="person-media-delete-dialog" @close="close">
        <p>{{ target.media.name }} · {{ formatBytes(target.media.size) }}</p>
        <p>移除后，该{{ kindLabel }}会从{{ libraryLabel }}及所有人物的活跃作品中移除。</p>
        <label class="person-media-delete-option"><input v-model="deleteFile" type="checkbox" :disabled="busy" data-test="person-media-delete-file" />同时把原文件移到废纸篓</label>
        <!-- 两种后果与片库删除确认框同一口径（D-PC01/03）。 -->
        <p data-test="person-media-delete-consequence">{{ deleteFile ? trashHint : recordOnlyHint }}</p>
        <p v-if="error" class="person-media-delete-error" role="alert">{{ error }}</p>
        <template #footer>
          <button type="button" class="btn btn--secondary" :disabled="busy" @click="close">取消</button>
          <button type="button" class="btn btn--danger" :disabled="busy" data-test="person-media-delete-confirm" @click="confirm">{{ busy ? '处理中…' : actionLabel }}</button>
        </template>
      </DialogShell>
      <!-- 删除宿主（D-PC02/04）：批量删除的进度、不支持废纸篓时二选一、从回收站恢复。视频与图片各一个、种类固定。
           删完的撤销走全局提示（notifyUndoable），不再用这里的页内撤销条。 -->
      <div class="person-media-delete-notice">
        <TrashUndoBanner ref="videoTrashUndo" kind="video" :after-restore="ids => afterRestore('video', ids)" />
        <TrashUndoBanner ref="imageTrashUndo" kind="image" :after-restore="ids => afterRestore('image', ids)" />
      </div>
    </div>
  </Teleport>
</template>
<script>
import DialogShell from './ui/DialogShell.vue';
import TrashUndoBanner, { ALREADY_RESTORED_MESSAGE, partialRestoreError, restoreTrashBatches } from './video-list/TrashUndoBanner.vue';
import { DELETE_RECORD_ONLY_HINT, DELETE_TRASH_HINT } from './DeleteConfirmDialog.vue';
import { feedbackState, notify, notifyUndoable } from '../utils/feedback.js';
import { formatBytes } from '../utils/mediaDetails.js';
import { readDeletePreferences } from '../utils/trashConfirm.js';

// 人物页 / 抽屉里删一部视频或一张图片（宿主：EntityLibraryPage、PreviewDrawer）。
// 一律先弹确认框（DialogShell），不看「删除前确认」开关（2026-10-13 破坏性流程复审 I-2，回到 master 的做法）：
// 人物页上的按钮容易被当成「从这个人物名下拿掉」，实际是把媒体从片库 / 图片库移除，必须先让用户看清后果。
// 框里的「同时把原文件移到废纸篓」默认取设置里的「删除原文件」；设置读回来之前框先挂着但不显示，免得勾选闪一下。
// 删除走带结果码的批量接口（TrashUndoBanner 的 runDelete：进度、不支持废纸篓时二选一，D-PC02/04）。
// 删完用全局提示带「撤销」（notifyUndoable，同时记进任务中心「最近操作」），撤销按批次恢复（TrashUndoBanner 的 restoreTrashBatches）。
// 提示还在或正在撤销时本组件保持挂载，提示收起后才发 close；之后从「最近操作」撤销时本组件已经卸下，
// 恢复照做，宿主照样经 restored 收到通知去重读（见 onRestored）。
export default {
  name: 'PersonMediaDeleteDialog', components: { DialogShell, TrashUndoBanner },
  props: {
    target: { type: Object, required: true },
    // 宿主的 @restored（撤销或从回收站恢复了这一类媒体，载荷 { kind, ids }；ids 为空表示以回收站为准重读）。
    // 声明成 prop 而不是放进 emits：Vue 会丢掉已卸下组件的 $emit，而从「最近操作」撤销时本组件早已卸下，
    // 拿着宿主传进来的这个监听才能照样通知到宿主。宿主写法不变，仍是 @restored。
    onRestored: { type: Function, default: null }
  },
  emits: ['close', 'deleted'],
  data: () => ({
    deleteFile: false, busy: false, error: '', trashHint: DELETE_TRASH_HINT, recordOnlyHint: DELETE_RECORD_ONLY_HINT,
    // confirm：确认框（设置读回来之前先挂着但不显示）；done：删完、等撤销提示收起。
    // deleteOriginal 是设置里的「删除原文件」，null 表示还没读回来。
    stage: 'confirm', deleteOriginal: null, undoToastID: null, undoing: false
  }),
  computed: {
    kindLabel() { return this.target.kind === 'video' ? '视频' : '图片'; },
    libraryLabel() { return this.target.kind === 'video' ? '片库' : '图片库'; },
    // 删除用词（D-UD-08）：勾了「同时把原文件移到废纸篓」就是移到废纸篓，否则只从库里移除记录、文件不动。
    actionLabel() { return this.deleteFile ? '移到废纸篓' : `从${this.libraryLabel}移除`; },
    actionTitle() { return this.deleteFile ? `把${this.kindLabel}移到废纸篓` : `从${this.libraryLabel}移除${this.kindLabel}`; },
    undoToastShown() { return this.undoToastID !== null && feedbackState.toasts.some(toast => toast.id === this.undoToastID); }
  },
  watch: {
    // 上一次删除的撤销提示还在时宿主又打开了另一条：回到确认步骤。
    target() { this.start(); },
    undoToastShown() { this.closeAfterNotice(); }
  },
  mounted() {
    window.addEventListener('keydown', this.escape, true);
    this.start();
  },
  beforeUnmount() { this.disposed = true; window.removeEventListener('keydown', this.escape, true); },
  methods: {
    formatBytes,
    close() { if (!this.busy) this.$emit('close'); },
    // 只在确认步骤里接管 Esc；其余阶段不拦别的弹窗与抽屉的 Esc。
    escape(event) { if (this.stage === 'confirm' && event.key === 'Escape') { event.preventDefault(); event.stopImmediatePropagation(); this.close(); } },
    // 每次都停在确认框，等用户点确认（confirm）；这里只把勾选恢复成设置里的默认值。
    async start() {
      this.error = ''; this.undoToastID = null; this.stage = 'confirm';
      if (this.deleteOriginal === null) await this.loadSettings();
      if (this.disposed || this.busy) return;
      this.deleteFile = this.deleteOriginal;
    },
    // 与洞察、下载页同一处读设置（utils/trashConfirm.js）；读不出来时按默认值：不删原文件。
    async loadSettings() {
      const { deleteOriginal } = await readDeletePreferences();
      this.deleteOriginal = deleteOriginal;
    },
    async confirm() {
      if (this.busy) return;
      const target = this.target;
      if (target.kind !== 'video' && target.kind !== 'image') { this.error = '删除失败：未知媒体类型'; return; }
      const id = Number(target.media.id);
      const banner = this.bannerFor(target.kind);
      this.busy = true; this.error = '';
      try {
        const outcome = await banner.runDelete({ ids: [id], deleteFile: this.deleteFile, names: { [id]: target.media.name } });
        const failure = outcome.failures.find(item => item.id === id);
        if (!outcome.removedIDs.includes(id)) {
          // 失败或在「不支持废纸篓」里选了暂不处理：记录原样保留，留在确认步骤可以重试。
          this.error = failure ? `删除失败：${failure.text}` : '未删除：所在磁盘不支持废纸篓，记录保持原样。';
          return;
        }
        this.$emit('deleted', target);
        this.stage = 'done';
        this.showUndo(outcome, banner.noticeText(outcome));
      } catch (err) { this.error = `删除失败：${err}`; }
      finally { this.busy = false; }
    },
    // 只删记录与移到废纸篓的项可以整批撤销；文件本就不在、或已永久删除的没有可撤销的批次，只提示结果。
    showUndo(outcome, text) {
      const kind = outcome.kind || this.target.kind;
      const batchIDs = [...outcome.batchIDs];
      const restorableIDs = [...outcome.trashed, ...outcome.recordOnly];
      if (!text || !batchIDs.length || !restorableIDs.length) {
        this.$emit('close');
        return;
      }
      this.undoToastID = notifyUndoable(text, () => this.undoDelete(kind, batchIDs, restorableIDs));
    },
    // 撤销：按批次恢复（与片库、图片页删除后的撤销同一段 restoreTrashBatches）。有项没回来时整体算失败
    // （提示「撤销失败：…」，「最近操作」里留着可以再试），并让宿主以回收站为准重读。
    // 一项都没恢复、也没有失败：已经从回收站恢复过了，记录移除，告诉用户一声，宿主同样以回收站为准重读。
    async undoDelete(kind, batchIDs, restorableIDs) {
      this.undoing = true;
      try {
        const { restored, failures } = await restoreTrashBatches(kind, batchIDs);
        if (failures.length) {
          this.afterRestore(kind, []);
          throw partialRestoreError(restored, failures);
        }
        if (restored === 0) {
          notify(ALREADY_RESTORED_MESSAGE);
          this.afterRestore(kind, []);
          return;
        }
        this.afterRestore(kind, restorableIDs);
      } finally {
        this.undoing = false;
        this.closeAfterNotice();
      }
    },
    closeAfterNotice() {
      if (this.stage === 'done' && !this.undoing && !this.undoToastShown && !this.disposed) this.$emit('close');
    },
    bannerFor(kind) {
      return kind === 'video' ? this.$refs.videoTrashUndo : this.$refs.imageTrashUndo;
    },
    // 挂着时照常发 restored；已卸下时 $emit 送不到，直接调宿主的同一个监听（onRestored）。
    afterRestore(kind, ids) {
      const payload = { kind, ids: [...(ids || [])] };
      if (this.disposed) this.onRestored?.(payload);
      else this.$emit('restored', payload);
    }
  }
};
</script>
<style scoped>
.person-media-delete-layer { position: fixed; inset: 0; z-index: 1500; }
/* 设置读回来之前确认框先挂着但不显示：「同时把原文件移到废纸篓」的默认勾选不会闪一下。 */
.person-media-delete-layer--pending { visibility: hidden; }
/* 确认框以外的阶段（等撤销提示收起）不挡住页面：只有删除进度、不支持废纸篓时的二选一
   与回收站可以点。里面的容器不定位、不做 transform：否则里面的 fixed 弹窗会以它为参照，层级也会被它困住。
   z-index 1500 暂不换刻度：从 ImageSourceDialog（1300）上打开时要压在它上面，等对话框层统一换到 --z-modal 再一起改。 */
.person-media-delete-layer--notice { pointer-events: none; }
.person-media-delete-notice { pointer-events: auto; }
.person-media-delete-option { display: flex; align-items: center; gap: 8px; }
.person-media-delete-option input { width: auto; margin: 0; }
.person-media-delete-error { color: var(--danger-text); }
</style>
