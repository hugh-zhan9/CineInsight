// WebKit 下滚轮事件偶尔落不到共用的 .main-view 滚动容器上，这一组方法把它转发过去。
// 逻辑与拆分前 VideoListPage 逐字一致，只是收进 mixin，减小页面文件体积。
export const wheelForwardingMixin = {
  data() {
    return {
      wheelFallbackTarget: null,
      wheelFallbackHandler: null
    };
  },
  methods: {
    attachWheelFallback() {
      this.$nextTick(() => {
        const scrollOwner = this.$el?.closest?.('.main-view');
        if (!scrollOwner || this.wheelFallbackTarget === scrollOwner) {
          return;
        }
        this.detachWheelFallback();
        this.wheelFallbackTarget = scrollOwner;
        this.wheelFallbackHandler = (event) => {
          if (!this.$el?.contains(event.target)) {
            return;
          }
          this.forwardWheelToScrollOwner(event);
        };
        scrollOwner.addEventListener('wheel', this.wheelFallbackHandler, { capture: true, passive: false });
      });
    },
    detachWheelFallback() {
      if (this.wheelFallbackTarget && this.wheelFallbackHandler) {
        this.wheelFallbackTarget.removeEventListener('wheel', this.wheelFallbackHandler, { capture: true });
      }
      this.wheelFallbackTarget = null;
      this.wheelFallbackHandler = null;
    },
    forwardWheelToScrollOwner(event) {
      if (!event || event.defaultPrevented) return;
      if (this.findScrollableWheelTarget(event.target, event.deltaY)) return;
      const scrollOwner = this.$el?.closest?.('.main-view');
      if (!scrollOwner) return;
      const before = scrollOwner.scrollTop;
      scrollOwner.scrollTop += event.deltaY;
      if (scrollOwner.scrollTop !== before) {
        event.preventDefault();
      }
    },
    findScrollableWheelTarget(target, deltaY) {
      let node = target;
      while (node && node !== this.$el) {
        if (node instanceof HTMLElement) {
          const style = window.getComputedStyle(node);
          const canScrollY = /(auto|scroll)/.test(style.overflowY);
          if (canScrollY && node.scrollHeight > node.clientHeight) {
            if (deltaY > 0 && node.scrollTop < node.scrollHeight - node.clientHeight) return node;
            if (deltaY < 0 && node.scrollTop > 0) return node;
          }
        }
        node = node.parentNode;
      }
      return null;
    },
  }
};
