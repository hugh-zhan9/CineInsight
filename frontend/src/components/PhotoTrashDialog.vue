<template>
  <BaseModal v-if="visible" close-on-overlay class="photo-trash-modal" role="dialog" aria-modal="true" aria-labelledby="photo-trash-title" @close="$emit('close')">
      <div class="photo-trash-header">
        <div>
          <h3 id="photo-trash-title">图片回收站</h3>
          <p class="photo-trash-hint">这里只显示本版本删除且仍可恢复的图片记录。</p>
        </div>
        <button type="button" class="btn-secondary btn-compact" @click="$emit('close')">关闭</button>
      </div>

      <div v-if="loading" class="photo-trash-state">正在读取回收站...</div>
      <div v-else-if="error" class="photo-trash-state photo-trash-state--error" role="alert">{{ error }}</div>
      <div v-else-if="entries.length === 0" class="photo-trash-state">回收站为空。</div>
      <div v-else class="photo-trash-list">
        <div v-for="entry in entries" :key="entry.id" class="photo-trash-entry">
          <div class="photo-trash-entry__body">
            <strong>{{ entry.image_name }}</strong>
            <span>{{ entry.original_path }}</span>
            <small>{{ entryStatus(entry) }} · {{ formatDate(entry.created_at) }}</small>
            <small v-if="entry.last_error" class="photo-trash-entry__error">上次处理失败：{{ entry.last_error }}</small>
            <small v-if="entry.error" class="photo-trash-entry__error">{{ entry.error }}</small>
          </div>
          <button
            type="button"
            class="btn-primary btn-compact"
            :disabled="restoringID === entry.id"
            @click="restore(entry)"
          >
            {{ restoringID === entry.id ? '处理中...' : restoreLabel(entry) }}
          </button>
        </div>
      </div>

      <div class="modal-actions">
        <button type="button" class="btn-secondary" @click="$emit('close')">完成</button>
      </div>
  </BaseModal>
</template>

<script>
import { ListImageTrashEntries, RestoreImageTrashEntry } from '../../wailsjs/go/main/App';
import BaseModal from './ui/BaseModal.vue';

export default {
  name: 'PhotoTrashDialog',
  components: { BaseModal },
  props: {
    visible: { type: Boolean, default: false }
  },
  emits: ['close', 'restored'],
  data() {
    return {
      entries: [],
      loading: false,
      error: '',
      restoringID: null,
      loadToken: 0
    };
  },
  watch: {
    visible(value) {
      if (value) {
        this.loadEntries();
      } else {
        this.loadToken += 1;
      }
    }
  },
  methods: {
    async loadEntries() {
      const token = ++this.loadToken;
      this.loading = true;
      this.error = '';
      try {
        const entries = await ListImageTrashEntries() || [];
        if (token !== this.loadToken || !this.visible) return;
        this.entries = entries;
      } catch (err) {
        if (token !== this.loadToken || !this.visible) return;
        this.error = `读取图片回收站失败：${String(err)}`;
      } finally {
        if (token === this.loadToken && this.visible) {
          this.loading = false;
        }
      }
    },
    async restore(entry) {
      if (!entry || this.restoringID) return;
      this.restoringID = entry.id;
      entry.error = '';
      try {
        const image = await RestoreImageTrashEntry(entry.id);
        this.entries = this.entries.filter(item => item.id !== entry.id);
        this.$emit('restored', image);
      } catch (err) {
        entry.error = `恢复失败：${String(err)}`;
      } finally {
        this.restoringID = null;
      }
    },
    formatDate(value) {
      if (!value) return '删除时间未知';
      const date = new Date(value);
      if (Number.isNaN(date.getTime())) return '删除时间未知';
      return date.toLocaleString();
    },
    entryStatus(entry) {
      if (entry.state === 'pending_move') return '删除曾中断，可恢复原状态';
      if (entry.state === 'rollback') return '删除回滚曾中断，可继续恢复';
      if (entry.state === 'restoring') return '恢复曾中断，可继续处理';
      return entry.file_moved ? '文件已移入回收站' : '文件未由应用移动，仅恢复数据库记录';
    },
    restoreLabel(entry) {
      return entry.state === 'pending_move' || entry.state === 'rollback' ? '恢复原状态' : '恢复';
    }
  }
};
</script>

<style scoped>
:deep(.photo-trash-modal) {
  width: min(720px, calc(100vw - 32px));
  max-height: min(720px, calc(100vh - 48px));
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.photo-trash-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
}

.photo-trash-hint,
.photo-trash-state,
.photo-trash-entry__body span,
.photo-trash-entry__body small {
  color: var(--text-muted);
}

.photo-trash-hint {
  margin: 6px 0 0;
  font-size: 12px;
}

.photo-trash-list {
  min-height: 0;
  overflow-y: auto;
  display: grid;
  gap: 8px;
}

.photo-trash-entry {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 12px;
  border: 1px solid var(--border-color);
  border-radius: 8px;
}

.photo-trash-entry__body {
  min-width: 0;
  display: grid;
  gap: 4px;
}

.photo-trash-entry__body span,
.photo-trash-entry__body small {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.photo-trash-entry__error,
.photo-trash-state--error {
  color: var(--danger-color) !important;
}

.photo-trash-state {
  padding: 32px 12px;
  text-align: center;
}
</style>
