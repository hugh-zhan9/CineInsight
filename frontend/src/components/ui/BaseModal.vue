<template>
  <div :class="['modal-overlay', overlayClass]" @click.self="handleOverlayClick">
    <div class="modal" role="dialog" aria-modal="true" v-bind="$attrs" @click="handleModalClick">
      <slot />
    </div>
  </div>
</template>

<script>
import { MODAL_OPEN_EVENT, closeModalLayer, isTopModalLayer, openModalLayer } from './modalSignals.js';

// 打开着的 BaseModal 按打开先后进 modalSignals.js 的模态层栈（与版本对比播放器、人脸来源详情同一个栈），
// Esc 只交给最上面那一层：确认框叠在对话框上时，一次 Esc 只关确认框。DialogShell（含 AppFeedback 的确认框）
// 与命令面板都建在 BaseModal 上。在 beforeMount 入栈：父组件的 beforeMount 早于子组件，嵌在对话框内容里
// 同时出现的子对话框排在它上面（mounted 的顺序正好相反，子先父后）。
export default {
  name: 'BaseModal',
  inheritAttrs: false,
  props: {
    closeOnOverlay: { type: Boolean, default: false },
    stopModalClicks: { type: Boolean, default: false },
    // 加在遮罩节点上的类（其余属性落在里面的对话框节点上），给 DialogShell 的全窗口尺寸去掉遮罩留白用。
    overlayClass: { type: String, default: '' }
  },
  // overlay-click：点到遮罩本身（不论 closeOnOverlay），给 DialogShell 按「有没有表单」自己决定关不关。
  emits: ['close', 'overlay-click'],
  beforeMount() {
    openModalLayer(this);
    // 通知开着的菜单与浮层收起（见 modalSignals.js）。
    window.dispatchEvent(new CustomEvent(MODAL_OPEN_EVENT));
  },
  mounted() {
    // Esc 是弹窗的兜底逃生口：个别分支里没有按钮可点（例如能力不可用的提示态），
    // 没有它用户就只能重启应用。父组件没监听 close 时这里什么也不会发生。
    document.addEventListener('keydown', this.handleDocumentKeydown);
  },
  beforeUnmount() {
    document.removeEventListener('keydown', this.handleDocumentKeydown);
    closeModalLayer(this);
  },
  methods: {
    handleDocumentKeydown(event) {
      if (event.key !== 'Escape') return;
      // 下面几层什么也不做；最上面那层接手：标记已处理（preventDefault），并拦下事件，
      // window 上的页面级 Esc（例如列表取消选择）收不到。
      if (!isTopModalLayer(this)) return;
      event.preventDefault();
      event.stopPropagation();
      this.$emit('close');
    },

    handleOverlayClick() {
      this.$emit('overlay-click');
      if (this.closeOnOverlay) this.$emit('close');
    },
    handleModalClick(event) {
      if (this.stopModalClicks) event.stopPropagation();
    }
  }
};
</script>
