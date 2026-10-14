<template>
  <Teleport v-if="visible" to="body">
    <DialogShell
      :title="step === 'choose' ? '该磁盘不支持废纸篓' : '确认永久删除'"
      size="md"
      :busy="busy"
      :close-on-overlay="false"
      class="trash-unsupported-modal"
      @close="handleClose"
    >
      <template v-if="step === 'choose'">
        <p>下面 {{ items.length }} {{ unit }}所在的磁盘（例如网络共享盘、部分 exFAT 移动硬盘）不支持移到废纸篓。它们仍在{{ libraryName }}里，没有做任何改动。请选择如何处理：</p>
        <ul class="trash-unsupported-names" data-test="trash-unsupported-names">
          <li v-for="item in previewItems" :key="item.id">{{ item.name }}</li>
          <li v-if="items.length > previewItems.length">
            还有 {{ items.length - previewItems.length }} 项
            <button type="button" class="btn-link" data-test="trash-unsupported-show-all" @click="showAll = true">显示全部 {{ items.length }} 项</button>
          </li>
        </ul>
        <div class="trash-unsupported-options">
          <div class="trash-unsupported-option">
            <button type="button" class="btn btn--danger" :disabled="busy" data-test="trash-unsupported-permanent" @click="step = 'confirm'">永久删除…</button>
            <p>直接删除磁盘上的文件和记录，不经过废纸篓，无法恢复。下一步会再确认一次。</p>
          </div>
          <div class="trash-unsupported-option">
            <button type="button" class="btn btn--secondary" :disabled="busy" data-test="trash-unsupported-record-only" @click="$emit('record-only')">{{ busy ? '处理中…' : `从${libraryName}移除` }}</button>
            <p>文件保留在磁盘上，不做任何改动。以后扫描到同一个文件不会再收录，可在回收站「允许重新收录」。</p>
          </div>
        </div>
      </template>

      <template v-else>
        <p class="trash-unsupported-warning">将永久删除以下 {{ items.length }} 个文件及其记录。它们不会进入废纸篓，删除后无法恢复。</p>
        <ul class="trash-unsupported-names" data-test="trash-unsupported-confirm-names">
          <li v-for="item in confirmItems" :key="item.id">{{ item.name }}</li>
          <li v-if="items.length > confirmItems.length">
            还有 {{ items.length - confirmItems.length }} 项
            <button type="button" class="btn-link" data-test="trash-unsupported-confirm-show-all" @click="showAll = true">显示全部 {{ items.length }} 项</button>
          </li>
        </ul>
        <!-- 不可逆（D-UD-07 第 3 级）：输入的就是按钮上的文字。 -->
        <label class="trash-unsupported-typed">
          <span>输入「{{ confirmWord }}」以确认，此操作无法撤销</span>
          <input
            v-model="typed"
            type="text"
            class="text-input"
            autocomplete="off"
            spellcheck="false"
            :disabled="busy"
            data-test="trash-unsupported-confirm-input"
          >
        </label>
      </template>

      <template #footer>
        <template v-if="step === 'choose'">
          <button type="button" class="btn btn--secondary" :disabled="busy" data-test="trash-unsupported-cancel" @click="cancel">暂不处理</button>
        </template>
        <template v-else>
          <button type="button" class="btn btn--secondary" :disabled="busy" data-test="trash-unsupported-back" @click="step = 'choose'">返回</button>
          <button type="button" class="btn btn--danger" :disabled="busy || !typedMatches" data-test="trash-unsupported-permanent-confirm" @click="confirmPermanent">
            {{ busy ? '正在删除…' : confirmWord }}
          </button>
        </template>
      </template>
    </DialogShell>
  </Teleport>
</template>

<script>
import DialogShell from './ui/DialogShell.vue';
import { confirmTextMatches } from '../utils/feedback.js';

// 永久删除这一步要原样输入的文字：就是按钮上的字（D-UD-07 第 3 级，全站同一规则）。
const PERMANENT_CONFIRM_WORD = '永久删除';

// 卷不支持废纸篓（D-PC02）时让用户二选一：永久删除（列出文件名，输入确认词后才能点），或从片库移除（只删记录）。
// 这里只收集选择，真正的删除由宿主（TrashUndoBanner）调用；取消即什么都不做，记录保持原样。
// 删除常从另一个对话框或查看器里发起（清理审阅、图片查看器、人物媒体删除），宿主 TrashUndoBanner 在页面里的位置
// 却可能排在它们前面：出现时才 teleport 到 body 末尾，DOM 顺序就是打开顺序，同在 --z-modal 也压在它们上面。
export default {
  name: 'TrashUnsupportedDialog',
  components: { DialogShell },
  props: {
    visible: { type: Boolean, default: false },
    kind: { type: String, default: 'video' },
    // [{ id, name }]
    items: { type: Array, default: () => [] },
    busy: { type: Boolean, default: false }
  },
  emits: ['permanent-delete', 'record-only', 'cancel'],
  data() {
    // showAll：名单默认只列前几项（选择页 10、确认页 30），永久删除前能展开看全每一项。
    return { step: 'choose', typed: '', confirmWord: PERMANENT_CONFIRM_WORD, showAll: false };
  },
  computed: {
    unit() {
      return this.kind === 'image' ? '张图片' : '个视频';
    },
    libraryName() {
      return this.kind === 'image' ? '图片库' : '片库';
    },
    previewItems() {
      return this.showAll ? this.items : this.items.slice(0, 10);
    },
    confirmItems() {
      return this.showAll ? this.items : this.items.slice(0, 30);
    },
    typedMatches() {
      return confirmTextMatches({ requiredText: this.confirmWord }, this.typed);
    }
  },
  watch: {
    visible(value) {
      if (value) {
        this.step = 'choose';
        this.showAll = false;
      }
    },
    // 每次进确认页都从空的输入框开始。
    step() {
      this.typed = '';
    }
  },
  methods: {
    confirmPermanent() {
      if (this.busy || !this.typedMatches) return;
      this.$emit('permanent-delete');
    },
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
.trash-unsupported-names {
  max-height: 180px;
  overflow-y: auto;
  margin: var(--space-3) 0;
  padding-left: 18px;
  color: var(--text-secondary);
  font-size: var(--fs-secondary);
  word-break: break-all;
}

.trash-unsupported-options {
  display: grid;
  gap: var(--space-3);
}

.trash-unsupported-option {
  display: grid;
  gap: var(--space-1);
  justify-items: start;
}

.trash-unsupported-option p {
  margin: 0;
  color: var(--text-muted);
  font-size: var(--fs-caption);
}

.trash-unsupported-warning {
  color: var(--danger-text);
}

.trash-unsupported-typed {
  display: grid;
  gap: 6px;
  color: var(--text-secondary);
  font-size: var(--fs-secondary);
}
</style>
