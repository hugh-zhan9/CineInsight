<template>
  <BaseModal
    v-bind="$attrs"
    :class="['ui-dialog', `ui-dialog--${size}`]"
    :aria-labelledby="titleId"
    :aria-busy="busy ? 'true' : undefined"
    tabindex="-1"
    @close="requestClose"
    @overlay-click="handleOverlayClick"
  >
    <header class="ui-dialog__head">
      <h2 :id="titleId" class="ui-dialog__title">{{ title }}</h2>
      <AppSpinner v-if="busy" class="ui-dialog__busy" :size="16" label="正在处理" />
      <button
        type="button"
        class="btn-icon btn--sm ui-dialog__close"
        aria-label="关闭"
        data-test="dialog-shell-close"
        :disabled="busy"
        @click="requestClose"
      ><AppIcon name="close" :size="16" /></button>
    </header>
    <div ref="body" class="ui-dialog__body"><slot /></div>
    <footer v-if="$slots.footer" class="ui-dialog__footer"><slot name="footer" /></footer>
  </BaseModal>
</template>

<script>
import AppIcon from './AppIcon.vue';
import AppSpinner from './AppSpinner.vue';
import BaseModal from './BaseModal.vue';

export const DIALOG_SIZES = ['sm', 'md', 'lg', 'xl'];
const FORM_CONTROLS = 'input:not([type="hidden"]), select, textarea, [contenteditable="true"]';
let titleSeq = 0;

// 对话框骨架（D-UD-06、D-UD-13），由 BaseModal 扩展；页面里的对话框都用它，BaseModal 只给这里用（守卫 5）。
// 标题 h2（--fs-section）+ 右上 ×（aria-label「关闭」）；Esc 与 × 总能关，busy 时都忽略并在标题旁转圈。
// closeOnOverlay 不传时按内容决定：正文里没有表单控件才允许点遮罩关（有表单的点外面不关，免得丢输入）。
// footer 插槽放按钮：第一个（「取消」）靠左，其余靠右，主操作放最后。尺寸 sm 420 / md 560 / lg 760 / xl 940。
// 其余属性（class、data-test 等）透传到对话框节点上。
export default {
  name: 'DialogShell',
  components: { AppIcon, AppSpinner, BaseModal },
  inheritAttrs: false,
  props: {
    title: { type: String, required: true },
    size: { type: String, default: 'md', validator: size => DIALOG_SIZES.includes(size) },
    closeOnOverlay: { type: Boolean, default: null },
    busy: { type: Boolean, default: false }
  },
  emits: ['close'],
  data() {
    titleSeq += 1;
    return { titleId: `ui-dialog-title-${titleSeq}` };
  },
  mounted() {
    this._returnFocus = document.activeElement;
    this.$el.querySelector?.('.ui-dialog')?.focus({ preventScroll: true });
  },
  beforeUnmount() {
    const target = this._returnFocus;
    if (target && target !== document.body && document.contains(target)) target.focus?.({ preventScroll: true });
  },
  methods: {
    requestClose() {
      if (!this.busy) this.$emit('close');
    },
    hasFormControls() {
      return Boolean(this.$refs.body?.querySelector(FORM_CONTROLS));
    },
    handleOverlayClick() {
      const allowed = this.closeOnOverlay === null ? !this.hasFormControls() : this.closeOnOverlay;
      if (allowed) this.requestClose();
    }
  }
};
</script>

<style scoped>
/* .ui-dialog 落在 BaseModal 内部的 .modal 节点上，不在本组件模板里，要用 :deep 才命中；
   遮罩节点是本组件的根，带本组件的 scope 属性。 */
:deep(.ui-dialog) { display: flex; flex-direction: column; max-height: calc(100vh - 36px); padding: 0; overflow: hidden; }
:deep(.ui-dialog:focus) { outline: none; }
:deep(.ui-dialog--sm) { max-width: 420px; }
:deep(.ui-dialog--md) { max-width: 560px; }
:deep(.ui-dialog--lg) { max-width: 760px; }
:deep(.ui-dialog--xl) { max-width: 940px; }
.ui-dialog__head { display: flex; flex: none; align-items: center; gap: var(--space-2); padding: var(--space-4) var(--space-4) var(--space-3) var(--space-5); }
.ui-dialog__title { flex: 1; min-width: 0; margin: 0; color: var(--text-primary); font-size: var(--fs-section); font-weight: var(--fw-semibold); line-height: 1.4; overflow-wrap: anywhere; }
.ui-dialog__busy { color: var(--accent-text); }
.ui-dialog__close { flex: none; margin: -2px 0; }
.ui-dialog__body { flex: 1 1 auto; min-height: 0; overflow: auto; padding: 0 var(--space-5) var(--space-5); color: var(--text-primary); font-size: var(--fs-body); }
.ui-dialog__footer { display: flex; flex: none; flex-wrap: wrap; align-items: center; justify-content: flex-end; gap: var(--space-2); padding: var(--space-3) var(--space-5); border-top: 1px solid var(--hairline-faint); }
.ui-dialog__footer > :slotted(:first-child:not(:last-child)) { margin-right: auto; }
</style>
