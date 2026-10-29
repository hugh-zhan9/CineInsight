<template>
  <!-- 浮层是 position: fixed，overflow:hidden 的祖先不会裁剪它；
       teleport 主要是为了避开 transform 造成的包含块。少数需要留在
       组件子树里的场景（例如便于按组件边界断言）可以关掉。 -->
  <Teleport to="body" :disabled="!teleport">
    <div
      ref="panel"
      class="base-popover"
      :class="panelClass"
      :style="panelStyle"
      role="dialog"
      aria-modal="false"
      tabindex="-1"
      @keydown="onKeydown"
    >
      <slot />
    </div>
  </Teleport>
</template>

<script>
import { MODAL_OPEN_EVENT } from './modalSignals.js';

const VIEWPORT_MARGIN = 8;
const ANCHOR_GAP = 6;

// 锚定式浮层：负责定位、点外部关闭、Esc 关闭和焦点归还，不关心内容。
// 用 Teleport 挂到 body 是因为工具栏和列表沿途有 overflow:hidden，
// 留在原地会被裁掉。
export default {
  name: 'BasePopover',
  inheritAttrs: false,
  props: {
    // 触发元素。用于定位，也用于判断"点在触发器上"不算点外部——
    // 否则按钮的 click 会在浮层关闭后立刻再把它打开。
    anchor: { type: Object, default: null },
    // 右键菜单这类没有触发元素的场景，直接给视口坐标。
    position: { type: Object, default: null },
    align: { type: String, default: 'start' },
    placement: { type: String, default: 'bottom', validator: value => ['top', 'bottom'].includes(value) },
    minWidth: { type: Number, default: 0 },
    panelClass: { type: String, default: '' },
    teleport: { type: Boolean, default: true }
  },
  emits: ['close'],
  data() {
    return { top: 0, left: 0, ready: false };
  },
  computed: {
    panelStyle() {
      return {
        top: `${this.top}px`,
        left: `${this.left}px`,
        minWidth: this.minWidth ? `${this.minWidth}px` : null,
        visibility: this.ready ? null : 'hidden'
      };
    }
  },
  mounted() {
    this.restoreFocusTo = document.activeElement;
    document.addEventListener('mousedown', this.onDocumentMouseDown, true);
    window.addEventListener(MODAL_OPEN_EVENT, this.onModalOpen);
    window.addEventListener('resize', this.reposition);
    // 滚动用捕获监听，才能收到列表容器自己的滚动。
    window.addEventListener('scroll', this.reposition, true);
    this.$nextTick(() => {
      this.reposition();
      this.$refs.panel?.focus({ preventScroll: true });
    });
  },
  beforeUnmount() {
    document.removeEventListener('mousedown', this.onDocumentMouseDown, true);
    window.removeEventListener(MODAL_OPEN_EVENT, this.onModalOpen);
    window.removeEventListener('resize', this.reposition);
    window.removeEventListener('scroll', this.reposition, true);
    if (this.restoreFocusTo?.isConnected) {
      this.restoreFocusTo.focus({ preventScroll: true });
    }
  },
  methods: {
    onKeydown(event) {
      if (event.key === 'Escape') {
        event.stopPropagation();
        event.preventDefault();
        this.$emit('close');
      }
    },
    // 有对话框打开（菜单项打开对话框、⌘K、异步弹出的确认框、退出确认）：浮层收起，焦点不还给它的触发器——
    // 触发器在对话框后面，还给它会让随后的 Enter 落到页面上。
    onModalOpen() {
      this.restoreFocusTo = null;
      this.$emit('close');
    },
    onDocumentMouseDown(event) {
      const panel = this.$refs.panel;
      if (panel?.contains(event.target)) return;
      if (this.anchor?.contains?.(event.target)) return;
      this.$emit('close');
    },
    reposition() {
      const panel = this.$refs.panel;
      if (!panel) return;
      const width = panel.offsetWidth || this.minWidth;
      const height = panel.offsetHeight;
      const viewportWidth = window.innerWidth || 0;
      const viewportHeight = window.innerHeight || 0;

      let top;
      let left;
      if (this.position) {
        top = this.position.y;
        left = this.position.x;
      } else if (this.anchor?.getBoundingClientRect) {
        const rect = this.anchor.getBoundingClientRect();
        top = this.placement === 'top' ? rect.top - ANCHOR_GAP - height : rect.bottom + ANCHOR_GAP;
        left = this.align === 'end' ? rect.right - width : rect.left;
        // 下方放不下就翻到上方，翻上去仍放不下时保留在下方由钳制兜底。
        if (this.placement === 'top' && top < VIEWPORT_MARGIN) {
          const below = rect.bottom + ANCHOR_GAP;
          if (below + height <= viewportHeight - VIEWPORT_MARGIN) top = below;
        } else if (this.placement !== 'top' && top + height > viewportHeight - VIEWPORT_MARGIN) {
          const above = rect.top - ANCHOR_GAP - height;
          if (above >= VIEWPORT_MARGIN) top = above;
        }
      } else {
        top = VIEWPORT_MARGIN;
        left = VIEWPORT_MARGIN;
      }

      const maxLeft = Math.max(VIEWPORT_MARGIN, viewportWidth - width - VIEWPORT_MARGIN);
      const maxTop = Math.max(VIEWPORT_MARGIN, viewportHeight - height - VIEWPORT_MARGIN);
      this.left = Math.min(Math.max(left, VIEWPORT_MARGIN), maxLeft);
      this.top = Math.min(Math.max(top, VIEWPORT_MARGIN), maxTop);
      this.ready = true;
    }
  }
};
</script>

<style>
.base-popover {
  position: fixed;
  z-index: var(--z-menu);
  border: 1px solid var(--hairline);
  border-radius: var(--radius-md);
  background: var(--panel-bg);
  box-shadow: var(--shadow-popover);
  color: var(--text-primary);
  outline: none;
}
</style>
