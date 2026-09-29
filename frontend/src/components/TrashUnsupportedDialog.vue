<template>
  <!-- 删除往往是从另一个弹窗（确认框、清理审阅）发起的：这一层要压在它们上面，但让开应用确认框（1500）。 -->
  <div v-if="visible" class="trash-unsupported-layer">
  <BaseModal stop-modal-clicks class="trash-unsupported-modal" aria-labelledby="trash-unsupported-title" @close="handleClose">
    <template v-if="step === 'choose'">
      <h2 id="trash-unsupported-title">该磁盘不支持废纸篓</h2>
      <p>下面 {{ items.length }} {{ unit }}所在的磁盘（例如网络共享盘、部分 exFAT 移动硬盘）不支持移到废纸篓。它们仍在{{ libraryName }}里，没有做任何改动。请选择如何处理：</p>
      <ul class="trash-unsupported-names" data-test="trash-unsupported-names">
        <li v-for="item in previewItems" :key="item.id">{{ item.name }}</li>
        <li v-if="items.length > previewItems.length">等 {{ items.length }} 项</li>
      </ul>
      <div class="trash-unsupported-options">
        <div class="trash-unsupported-option">
          <button type="button" class="btn-danger" :disabled="busy" data-test="trash-unsupported-permanent" @click="step = 'confirm'">永久删除文件…</button>
          <p>直接删除磁盘上的文件和记录，不经过废纸篓，无法恢复。下一步会再确认一次。</p>
        </div>
        <div class="trash-unsupported-option">
          <button type="button" class="btn-secondary" :disabled="busy" data-test="trash-unsupported-record-only" @click="$emit('record-only')">{{ busy ? '处理中...' : '只删记录' }}</button>
          <p>文件保留在磁盘上。以后扫描到同一个文件不会再收录，可在回收站「允许重新收录」。</p>
        </div>
      </div>
      <div class="modal-actions">
        <button type="button" class="btn-secondary" :disabled="busy" data-test="trash-unsupported-cancel" @click="cancel">暂不处理</button>
      </div>
    </template>

    <template v-else>
      <h2 id="trash-unsupported-title">确认永久删除</h2>
      <p class="trash-unsupported-warning">将永久删除以下 {{ items.length }} 个文件及其记录。它们不会进入废纸篓，删除后无法恢复。</p>
      <ul class="trash-unsupported-names" data-test="trash-unsupported-confirm-names">
        <li v-for="item in confirmItems" :key="item.id">{{ item.name }}</li>
        <li v-if="items.length > confirmItems.length">等 {{ items.length }} 项</li>
      </ul>
      <div class="modal-actions">
        <button type="button" class="btn-secondary" :disabled="busy" data-test="trash-unsupported-back" @click="step = 'choose'">返回</button>
        <button type="button" class="btn-danger" :disabled="busy" data-test="trash-unsupported-permanent-confirm" @click="$emit('permanent-delete')">
          {{ busy ? '正在删除...' : `永久删除 ${items.length} 个文件` }}
        </button>
      </div>
    </template>
  </BaseModal>
  </div>
</template>

<script>
import BaseModal from './ui/BaseModal.vue';

// 卷不支持废纸篓（D-PC02）时让用户二选一：永久删除（列出文件名并二次确认），或只删记录。
// 这里只收集选择，真正的删除由宿主（TrashUndoBanner）调用；取消即什么都不做，记录保持原样。
export default {
  name: 'TrashUnsupportedDialog',
  components: { BaseModal },
  props: {
    visible: { type: Boolean, default: false },
    kind: { type: String, default: 'video' },
    // [{ id, name }]
    items: { type: Array, default: () => [] },
    busy: { type: Boolean, default: false }
  },
  emits: ['permanent-delete', 'record-only', 'cancel'],
  data() {
    return { step: 'choose' };
  },
  computed: {
    unit() {
      return this.kind === 'image' ? '张图片' : '个视频';
    },
    libraryName() {
      return this.kind === 'image' ? '图片库' : '片库';
    },
    previewItems() {
      return this.items.slice(0, 10);
    },
    confirmItems() {
      return this.items.slice(0, 30);
    }
  },
  watch: {
    visible(value) {
      if (value) this.step = 'choose';
    }
  },
  methods: {
    cancel() {
      if (this.busy) return;
      this.$emit('cancel');
    },
    // Esc 在确认页只退回上一步，不直接放弃。
    handleClose() {
      if (this.busy) return;
      if (this.step === 'confirm') {
        this.step = 'choose';
        return;
      }
      this.cancel();
    }
  }
};
</script>

<style scoped>
.trash-unsupported-layer :deep(.modal-overlay) {
  z-index: 1100;
}

:deep(.trash-unsupported-modal) {
  max-width: 540px;
}

.trash-unsupported-names {
  max-height: 180px;
  overflow-y: auto;
  margin: 10px 0 14px;
  padding-left: 18px;
  color: var(--text-secondary);
  font-size: 13px;
  word-break: break-all;
}

.trash-unsupported-options {
  display: grid;
  gap: 12px;
}

.trash-unsupported-option {
  display: grid;
  gap: 4px;
  justify-items: start;
}

.trash-unsupported-option p {
  margin: 0;
  color: var(--text-muted);
  font-size: 12px;
}

.trash-unsupported-warning {
  color: var(--danger-color);
}
</style>
