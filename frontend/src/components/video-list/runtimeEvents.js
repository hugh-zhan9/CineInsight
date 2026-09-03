// 从 VideoListPage 拆出子组件后，原来集中在父组件的 Wails 运行时事件注册跟着各自的
// 领域走。注册/注销这一对逻辑与拆分前逐字一致，只是放进 mixin 供多个子组件复用。
export const runtimeEventsMixin = {
  data() {
    return { runtimeOffHandlers: [] };
  },
  beforeUnmount() {
    this.teardownRuntimeEvents();
  },
  methods: {
    registerRuntimeEvent(eventName, handler) {
      if (!window.runtime?.EventsOn) {
        return;
      }
      const off = window.runtime.EventsOn(eventName, handler);
      if (typeof off === 'function') {
        this.runtimeOffHandlers.push(off);
      }
    },
    teardownRuntimeEvents() {
      while (this.runtimeOffHandlers.length > 0) {
        const off = this.runtimeOffHandlers.pop();
        try {
          off?.();
        } catch (_err) {}
      }
    }
  }
};
