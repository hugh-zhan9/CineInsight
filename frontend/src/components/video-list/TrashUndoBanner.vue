<template>
  <div v-if="undoNotice" class="undo-delete-banner" role="status">
    <span>已移入回收站 {{ undoNotice.count }} 个视频。</span>
    <button v-if="undoNotice.entry" type="button" class="btn-primary btn-compact" :disabled="undoing" @click="undoLastDelete">
      {{ undoing ? '撤销中...' : '撤销' }}
    </button>
    <button v-else type="button" class="btn-secondary btn-compact" @click="openTrashDialog">查看回收站</button>
    <button type="button" class="undo-delete-banner__close" aria-label="关闭提示" @click="undoNotice = null">×</button>
  </div>

  <TrashRestoreDialog
    :visible="trashDialog.show"
    @close="trashDialog.show = false"
    @restored="handleTrashRestored"
  />
</template>

<script>
import { ListTrashEntries, RestoreTrashEntry } from '../../../wailsjs/go/main/App';
import TrashRestoreDialog from '../TrashRestoreDialog.vue';
import { notifyError } from '../../utils/feedback.js';

// 删除后的撤销提示条与回收站对话框。恢复之后要做的事（清理面板的已删标记、
// 列表重载、清理状态刷新）仍归片库页，用 afterRestore 函数 prop 按原顺序回调。
export default {
  name: 'TrashUndoBanner',
  components: { TrashRestoreDialog },
  props: {
    afterRestore: { type: Function, required: true }
  },
  data() {
    return {
      trashDialog: { show: false },
      undoNotice: null,
      undoNoticeTimer: null,
      undoing: false
    };
  },
  beforeUnmount() {
    if (this.undoNoticeTimer) {
      clearTimeout(this.undoNoticeTimer);
    }
  },
  methods: {
    openTrashDialog() {
      this.trashDialog.show = true;
    },
    async showDeleteUndo(videoIDs, preferredVideoID = null) {
      const ids = [...new Set((videoIDs || []).filter(Boolean))];
      if (ids.length === 0) return;
      try {
        const entries = await ListTrashEntries() || [];
        const entry = preferredVideoID
          ? entries.find(item => item.video_id === preferredVideoID) || null
          : ids.length === 1
            ? entries.find(item => item.video_id === ids[0]) || null
            : null;
        this.undoNotice = { count: ids.length, entry };
        if (this.undoNoticeTimer) clearTimeout(this.undoNoticeTimer);
        this.undoNoticeTimer = window.setTimeout(() => {
          this.undoNotice = null;
          this.undoNoticeTimer = null;
        }, 12000);
      } catch (err) {
        console.error('读取回收站失败:', err);
        this.undoNotice = { count: ids.length, entry: null };
      }
    },
    async undoLastDelete() {
      const entry = this.undoNotice?.entry;
      if (!entry) {
        this.openTrashDialog();
        return;
      }
      if (this.undoing) return;
      this.undoing = true;
      try {
        await RestoreTrashEntry(entry.id);
        this.undoNotice = null;
        if (this.undoNoticeTimer) clearTimeout(this.undoNoticeTimer);
        this.undoNoticeTimer = null;
        // 恢复回来的视频不该继续在清理审阅里显示为"已移入回收站"。
        await this.afterRestore(entry.video_id, false);
      } catch (err) {
        console.error('撤销删除失败:', err);
        notifyError('撤销删除失败: ' + err);
      } finally {
        this.undoing = false;
      }
    },
    // TrashRestoreDialog 的 restored 事件带的是恢复出来的视频对象。
    async handleTrashRestored(video) {
      this.undoNotice = null;
      if (this.undoNoticeTimer) clearTimeout(this.undoNoticeTimer);
      this.undoNoticeTimer = null;
      await this.afterRestore(video?.id, true);
    },
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
