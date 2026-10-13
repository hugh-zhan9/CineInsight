<template>
  <button type="button" class="btn-plain notes-thumb" :disabled="!clickable" :title="title" data-test="notes-thumb" @click="$emit('open')">
    <img v-if="src && !failed" :src="src" alt="" loading="lazy" decoding="async" data-test="notes-thumb-image" @error="failed = true" />
    <span v-else class="notes-thumb__fallback" data-test="notes-thumb-fallback">{{ src ? '暂无预览' : fallback }}</span>
  </button>
</template>

<script>
// 观看笔记条目左侧的视频预览：日记与播放记录用片库缩略图，书签用书签位置的那一帧。
// 没有可用原片（原片不可用、片库外的手工条目）时显示 fallback；有原片但预览读取失败显示「暂无预览」，不出现破图。
export default {
  name: 'NotesThumb',
  props: {
    src: { type: String, default: '' },
    fallback: { type: String, default: '无预览' },
    clickable: { type: Boolean, default: false },
    title: { type: String, default: '' }
  },
  emits: ['open'],
  data() {
    return { failed: false };
  },
  watch: {
    src() { this.failed = false; }
  }
};
</script>

<style scoped>
.notes-thumb { display: block; width: 160px; aspect-ratio: 16 / 9; padding: 0; overflow: hidden; border: 1px solid var(--hairline); border-radius: var(--radius); background: var(--thumb-bg); cursor: pointer; }
.notes-thumb:disabled { cursor: default; }
.notes-thumb:not(:disabled):hover { border-color: var(--border-strong); }
.notes-thumb img { display: block; width: 100%; height: 100%; object-fit: cover; }
.notes-thumb__fallback { display: flex; align-items: center; justify-content: center; width: 100%; height: 100%; background: var(--thumb-fallback-bg); color: var(--thumb-fg); font-size: var(--fs-caption); }
</style>
