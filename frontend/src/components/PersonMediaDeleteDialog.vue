<template>
  <Teleport to="body">
    <div :class="['person-media-delete-layer', { 'person-media-delete-layer--notice': stage === 'done' }]">
      <BaseModal v-if="stage === 'confirm'" aria-label="确认删除媒体" data-test="person-media-delete-dialog" @close="close">
        <h3>删除{{ kindLabel }}</h3>
        <p>{{ target.media.name }} · {{ formatBytes(target.media.size) }}</p>
        <p class="person-media-delete-path">{{ target.media.path }}</p>
        <p>删除后，该{{ kindLabel }}会从片库及所有人物的活跃作品中移除。</p>
        <label class="person-media-delete-option"><input v-model="deleteFile" type="checkbox" :disabled="busy" data-test="person-media-delete-file" />同时把原文件移到废纸篓</label>
        <!-- 两种后果与片库删除确认框同一口径（D-PC01/03）。 -->
        <p data-test="person-media-delete-consequence">{{ deleteFile ? trashHint : recordOnlyHint }}</p>
        <p v-if="error" role="alert">{{ error }}</p>
        <div class="modal-actions">
          <button type="button" class="btn-danger" :disabled="busy" data-test="person-media-delete-confirm" @click="confirm">{{ busy ? '删除中…' : '确认删除' }}</button>
          <button type="button" class="btn-secondary" :disabled="busy" @click="close">取消</button>
        </div>
      </BaseModal>
      <!-- 删除走带结果码的批量接口（D-PC02/04）：不支持废纸篓时二选一、删完给整批撤销条。
           撤销条消失之前本组件保持挂载，关掉之后才通知宿主收起。视频与图片各一个撤销条、种类固定：
           宿主在撤销条还在时换成另一种媒体，前一次的撤销仍按原来的种类恢复。 -->
      <div class="person-media-delete-notice">
        <TrashUndoBanner ref="videoTrashUndo" kind="video" :after-restore="ids => afterRestore('video', ids)" />
        <TrashUndoBanner ref="imageTrashUndo" kind="image" :after-restore="ids => afterRestore('image', ids)" />
      </div>
    </div>
  </Teleport>
</template>
<script>
import BaseModal from './ui/BaseModal.vue';
import TrashUndoBanner from './video-list/TrashUndoBanner.vue';
import { DELETE_RECORD_ONLY_HINT, DELETE_TRASH_HINT } from './DeleteConfirmDialog.vue';
import { formatBytes } from '../utils/mediaDetails.js';
export default {
  name: 'PersonMediaDeleteDialog', components: { BaseModal, TrashUndoBanner },
  props: { target: { type: Object, required: true } },
  // restored：撤销或从回收站恢复了这一类媒体，载荷 { kind, ids }（ids 为空表示以回收站为准重读）。
  emits: ['close', 'deleted', 'restored'],
  data: () => ({ deleteFile: false, busy: false, error: '', stage: 'confirm', trashHint: DELETE_TRASH_HINT, recordOnlyHint: DELETE_RECORD_ONLY_HINT }),
  computed: { kindLabel() { return this.target.kind === 'video' ? '视频' : '图片'; } },
  watch: {
    // 撤销条还在时宿主又打开了另一条的删除：回到确认步骤，撤销条留给新的一次删除覆盖。
    target() { this.stage = 'confirm'; this.deleteFile = false; this.error = ''; }
  },
  mounted() {
    window.addEventListener('keydown', this.escape, true);
    this.$watch(() => [this.$refs.videoTrashUndo?.undoNotice, this.$refs.imageTrashUndo?.undoNotice], notices => {
      if (this.stage === 'done' && !notices.some(Boolean)) this.$emit('close');
    });
  },
  beforeUnmount() { window.removeEventListener('keydown', this.escape, true); },
  methods: {
    formatBytes,
    close() { if (!this.busy) this.$emit('close'); },
    // 只在确认步骤里接管 Esc；撤销条阶段不拦别的弹窗与抽屉的 Esc。
    escape(event) { if (this.stage === 'confirm' && event.key === 'Escape') { event.preventDefault(); event.stopImmediatePropagation(); this.close(); } },
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
        // 同一时刻只留一个撤销条（两个会叠在同一个位置）：换了种类就收起另一种的。
        this.bannerFor(target.kind === 'video' ? 'image' : 'video')?.dismissNotice();
        banner.showDeleteNotice(outcome, { reportFailures: false });
        if (banner.undoNotice) this.stage = 'done';
        else this.$emit('close');
      } catch (err) { this.error = `删除失败：${err}`; }
      finally { this.busy = false; }
    },
    bannerFor(kind) {
      return kind === 'video' ? this.$refs.videoTrashUndo : this.$refs.imageTrashUndo;
    },
    afterRestore(kind, ids) {
      this.$emit('restored', { kind, ids: [...(ids || [])] });
    }
  }
};
</script>
<style scoped>
.person-media-delete-layer { position: fixed; inset: 0; z-index: 1500; }
/* 撤销条阶段不再挡住页面：只有撤销条（以及从它打开的回收站、二选一弹窗）可以点。
   容器本身不定位、不做 transform：否则里面的 fixed 弹窗会以它为参照，层级也会被它困住。 */
.person-media-delete-layer--notice { pointer-events: none; }
.person-media-delete-notice { pointer-events: auto; }
.person-media-delete-notice :deep(.undo-delete-banner) {
  position: fixed; left: 16px; right: 16px; bottom: 72px; z-index: 1;
  max-width: 640px; margin: 0 auto;
  background: var(--panel-bg); box-shadow: var(--shadow-modal);
}
.person-media-delete-path { overflow-wrap: anywhere; color: var(--text-muted); }
.person-media-delete-option { display: flex; align-items: center; gap: 8px; }
.person-media-delete-option input { width: auto; margin: 0; }
</style>
