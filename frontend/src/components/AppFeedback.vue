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
          v-if="toast.action"
          type="button"
          class="btn-link app-toast__action"
          data-test="app-toast-action"
          @click="runToastAction(toast.id)"
        >{{ toast.action.label }}</button>
        <button
          type="button"
          class="btn-icon btn--sm app-toast__close"
          aria-label="关闭提示"
          data-test="app-toast-close"
          @click="dismissToast(toast.id)"
        ><AppIcon name="close" :size="16" /></button>
      </div>
    </div>

    <DialogShell
      v-if="confirm"
      :title="confirm.title"
      size="sm"
      class="app-confirm"
      data-test="app-confirm"
      @close="resolveConfirm(false)"
    >
      <p class="app-confirm__message">{{ confirm.message }}</p>
      <p v-if="confirm.impact" class="app-confirm__impact" data-test="app-confirm-impact">{{ confirm.impact }}</p>
      <label v-if="confirm.requiredText" class="app-confirm__typed">
        <span>输入「{{ confirm.requiredText }}」以确认，此操作无法撤销</span>
        <input
          ref="typedInput"
          v-model="typed"
          type="text"
          class="text-input"
          autocomplete="off"
          spellcheck="false"
          data-test="app-confirm-input"
          @keydown.enter="handleTypedEnter"
        >
      </label>
      <template #footer>
        <button type="button" class="btn btn--secondary" data-test="app-confirm-cancel" @click="resolveConfirm(false)">
          {{ confirm.cancelText }}
        </button>
        <button
          type="button"
          :class="['btn', confirm.danger ? 'btn--danger' : 'btn--primary']"
          :disabled="!canConfirm"
          data-test="app-confirm-ok"
          @click="confirmOk"
        >{{ confirm.confirmText }}</button>
      </template>
    </DialogShell>
  </div>
</template>

<script>
import AppIcon from './ui/AppIcon.vue';
import DialogShell from './ui/DialogShell.vue';
import { confirmTextMatches, dismissToast, feedbackState, resolveConfirm, runToastAction } from '../utils/feedback.js';

// 全局唯一的提示宿主，挂在 App 根上。业务代码只调 utils/feedback.js 里的函数，
// 不需要把提示状态一层层往下传。提示最多带一个动作（撤销 / 重试 / 查看）；确认框分两级，
// 不可逆的要原样输入按钮上的文字才能点（D-UD-07）。
export default {
  name: 'AppFeedback',
  components: { AppIcon, DialogShell },
  data() {
    return { typed: '' };
  },
  computed: {
    toasts() { return feedbackState.toasts; },
    confirm() { return feedbackState.confirm; },
    canConfirm() { return confirmTextMatches(this.confirm, this.typed); }
  },
  watch: {
    // 换了一个确认框就清空输入；不可逆确认把焦点放进输入框。
    confirm: {
      handler(next) {
        this.typed = '';
        if (next?.requiredText) this.$nextTick(() => this.$refs.typedInput?.focus());
      }
    }
  },
  methods: {
    dismissToast,
    resolveConfirm,
    runToastAction,
    confirmOk() {
      if (this.canConfirm) resolveConfirm(true);
    },
    // 输入法组字时的回车是选字上屏，不是确认（WKWebView 里组字结束那一下 keyCode 是 229）。
    handleTypedEnter(event) {
      if (event.isComposing || event.keyCode === 229) return;
      event.preventDefault();
      this.confirmOk();
    }
  }
};
</script>

<style scoped>
/* 层级：确认框要盖在图片查看器、人脸预览等 teleport 浮层（最高 1300）之上，提示条要盖在弹窗（1000）
   和浮层（1200）之上，弹窗里触发的错误也必须能看见。这两个值等各页浮层统一换成 --z-* 刻度后
   （统一设计第 0 步 C 组）再改成 var(--z-critical) / var(--z-toast)。 */
.app-feedback :deep(.modal-overlay) { z-index: 1500; }
.app-toasts {
  position: fixed;
  z-index: 1400;
  right: var(--space-4);
  bottom: var(--space-4);
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  max-width: min(420px, calc(100vw - 32px));
  max-height: calc(100vh - 32px);
  overflow-y: auto;
}

.app-toast {
  display: flex;
  align-items: flex-start;
  gap: var(--space-2);
  padding: 10px var(--space-2) 10px var(--space-3);
  border: 1px solid var(--hairline);
  border-radius: var(--radius);
  background: var(--panel-bg);
  box-shadow: var(--shadow-popover);
  color: var(--text-primary);
  font-size: var(--fs-secondary);
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

/* 动作跟随提示的字色，下划线标出可点。 */
.app-toast__action {
  flex: none;
  color: inherit;
  font-weight: var(--fw-semibold);
  text-decoration: underline;
  text-underline-offset: 2px;
}

/* 比全局 .btn-icon.btn--sm（28×28，components.css 在构建产物里排在组件样式后面）多一层选择器，22×22 才生效。 */
.app-toast .app-toast__close { width: 22px; height: 22px; margin: -1px 0; color: inherit; opacity: 0.7; }
.app-toast .app-toast__close:hover:not(:disabled) { background: transparent; color: inherit; opacity: 1; }

.app-confirm__message {
  margin: 0;
  color: var(--text-secondary);
  font-size: var(--fs-secondary);
  line-height: 1.6;
  white-space: pre-line;
}
.app-confirm__message:empty { display: none; }
.app-confirm__impact { margin: var(--space-2) 0 0; color: var(--text-primary); font-size: var(--fs-secondary); font-weight: var(--fw-medium); }
.app-confirm__typed { display: grid; gap: 6px; margin-top: var(--space-4); color: var(--text-secondary); font-size: var(--fs-caption); }
</style>
