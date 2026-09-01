<template>
  <div class="sheet-layer" @click.self="$emit('close')">
    <section class="sheet" role="dialog" :aria-label="title" @click.stop>
      <span class="sheet__grip" aria-hidden="true"></span>
      <header class="sheet__head">
        <h2>{{ title }}</h2>
        <p v-if="hint" class="sheet__hint">{{ hint }}</p>
        <div class="sheet__spacer"></div>
        <slot name="head" />
      </header>
      <div class="sheet__body">
        <slot />
      </div>
      <footer v-if="$slots.footer" class="sheet__footer">
        <slot name="footer" />
      </footer>
    </section>
  </div>
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
  mounted() {
    this.onKey = event => {
      if (event.key === 'Escape') this.$emit('close');
    };
    window.addEventListener('keydown', this.onKey);
  },
  beforeUnmount() {
    window.removeEventListener('keydown', this.onKey);
  }
};
</script>
