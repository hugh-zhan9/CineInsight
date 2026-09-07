<template>
  <Teleport to="body">
  <div class="sheet-layer" :style="viewportStyle" @click.self="$emit('close')">
    <section class="sheet" role="dialog" :aria-label="title" @click.stop>
      <span class="sheet__grip" aria-hidden="true"></span>
      <header class="sheet__head">
        <h2>{{ title }}</h2>
        <p v-if="hint" class="sheet__hint">{{ hint }}</p>
        <div class="sheet__spacer"></div>
        <slot name="head" />
        <!-- 显式关闭：点遮罩关闭依赖合成 click，浏览器里被舞台的 touch 拦截过一次就没了。 -->
        <button type="button" class="sheet__close" aria-label="关闭" data-test="sheet-close" @click="$emit('close')">✕</button>
      </header>
      <div class="sheet__body">
        <slot />
      </div>
      <footer v-if="$slots.footer" class="sheet__footer">
        <slot name="footer" />
      </footer>
    </section>
  </div>
  </Teleport>
</template>

<script>
// 手机端的底部动作面板：评分、标签、删除确认、播放范围共用同一个壳。
export default {
  name: 'FeedSheet',
  props: {
    title: { type: String, required: true },
    hint: { type: String, default: '' }
  },
  emits: ['close'],
  data() {
    return { viewportStyle: {} };
  },
  mounted() {
    this.onKey = event => {
      if (event.key === 'Escape') this.$emit('close');
    };
    window.addEventListener('keydown', this.onKey);
    // iOS 的软键盘只缩小 visualViewport，布局视口和 vh 不一定变化。
    this.viewport = window.visualViewport;
    this.updateViewport();
    this.viewport?.addEventListener('resize', this.updateViewport);
    this.viewport?.addEventListener('scroll', this.updateViewport);
  },
  beforeUnmount() {
    window.removeEventListener('keydown', this.onKey);
    this.viewport?.removeEventListener('resize', this.updateViewport);
    this.viewport?.removeEventListener('scroll', this.updateViewport);
  },
  methods: {
    updateViewport() {
      if (!this.viewport) return;
      this.viewportStyle = {
        top: `${this.viewport.offsetTop}px`,
        left: `${this.viewport.offsetLeft}px`,
        width: `${this.viewport.width}px`,
        height: `${this.viewport.height}px`,
        bottom: 'auto',
        right: 'auto'
      };
    }
  }
};
</script>
