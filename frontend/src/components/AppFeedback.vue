<template>
  <div class="app-feedback">
    <div v-if="toasts.length" class="app-toasts" role="status" aria-live="polite" data-test="app-toasts">
      <div
        v-for="toast in toasts"
        :key="toast.id"
        :class="['app-toast', `app-toast--${toast.level}`]"
        data-test="app-toast"
      >
        <span class="app-toast__text">{{ toast.message }}</span>
        <button
          type="button"
          class="app-toast__close"
          aria-label="关闭提示"
          data-test="app-toast-close"
          @click="dismissToast(toast.id)"
        >×</button>
      </div>
    </div>

    <BaseModal v-if="confirm" @close="resolveConfirm(false)">
      <h2>{{ confirm.title }}</h2>
      <p class="app-confirm__message">{{ confirm.message }}</p>
      <div class="modal-actions">
        <button type="button" class="btn-secondary" data-test="app-confirm-cancel" @click="resolveConfirm(false)">
          {{ confirm.cancelText }}
        </button>
        <button
          type="button"
          :class="confirm.danger ? 'btn-danger' : 'btn-primary'"
          data-test="app-confirm-ok"
          @click="resolveConfirm(true)"
        >{{ confirm.confirmText }}</button>
      </div>
    </BaseModal>
  </div>
</template>

<script>
import BaseModal from './ui/BaseModal.vue';
import { dismissToast, feedbackState, resolveConfirm } from '../utils/feedback.js';

// 全局唯一的提示宿主，挂在 App 根上。业务代码只调 utils/feedback.js 里的函数，
// 不需要把提示状态一层层往下传。
export default {
  name: 'AppFeedback',
  components: { BaseModal },
  computed: {
    toasts() { return feedbackState.toasts; },
    confirm() { return feedbackState.confirm; }
  },
  methods: { dismissToast, resolveConfirm }
};
</script>

<style scoped>
/* Confirmations must remain above teleported image and face previews. */
.app-feedback :deep(.modal-overlay) { z-index: 1500; }
/* 盖在弹窗（1000）和浮层（1200）之上：弹窗里触发的错误也必须能看见。 */
.app-toasts {
  position: fixed;
  z-index: 1400;
  right: 16px;
  bottom: 16px;
  display: flex;
  flex-direction: column;
  gap: 8px;
  max-width: min(420px, calc(100vw - 32px));
  max-height: calc(100vh - 32px);
  overflow-y: auto;
}

.app-toast {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  padding: 10px 12px;
  border: 1px solid var(--hairline);
  border-radius: var(--radius-md);
  background: var(--panel-bg);
  box-shadow: var(--shadow-popover);
  color: var(--text-primary);
  font-size: 12.5px;
  line-height: 1.5;
}

.app-toast--error {
  border-color: var(--danger-border);
  background: var(--danger-soft);
  color: var(--danger-text);
}

.app-toast--success {
  border-color: var(--accent-border);
  background: var(--accent-soft);
  color: var(--accent-text);
}

/* 批量结果用 \n 拼多行；单条太长也不能把关闭按钮顶出屏幕。 */
.app-toast__text {
  flex: 1;
  min-width: 0;
  max-height: 40vh;
  overflow-y: auto;
  overflow-wrap: anywhere;
  white-space: pre-line;
}

.app-toast__close {
  flex: none;
  border: 0;
  background: transparent;
  color: inherit;
  opacity: 0.6;
  font-size: 15px;
  line-height: 1;
  cursor: pointer;
}

.app-toast__close:hover { opacity: 1; }

.app-confirm__message {
  color: var(--text-secondary);
  font-size: 13px;
  line-height: 1.6;
  white-space: pre-line;
}
</style>
