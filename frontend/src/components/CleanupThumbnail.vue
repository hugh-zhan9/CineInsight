<template>
  <button
    v-if="video"
    type="button"
    class="cleanup-thumb"
    :title="`预览 ${video.name || ''}`"
    :aria-label="`预览 ${video.name || ''}`"
    data-test="cleanup-thumb"
    @click="$emit('preview', video)"
  >
    <img
      v-if="!failed"
      :src="`/preview/thumbnail/${video.id}`"
      :alt="`${video.name || ''} 缩略图`"
      loading="lazy"
      @error="failed = true"
    />
    <span v-else class="cleanup-thumb__fallback" aria-hidden="true">▶</span>
    <span class="cleanup-thumb__play" aria-hidden="true">▶</span>
  </button>
</template>

<script>
// 清理审阅里的缩略图：光看文件名和分辨率判断不了两个视频是不是真重复，
// 得让画面直接摆在一起。整块就是播放入口，点一下走和"预览"一样的抽屉。
export default {
  name: 'CleanupThumbnail',
  props: {
    video: { type: Object, default: null }
  },
  emits: ['preview'],
  data() {
    return { failed: false };
  },
  watch: {
    'video.id'() {
      this.failed = false;
    }
  }
};
</script>

<style scoped>
.cleanup-thumb {
  position: relative;
  flex: none;
  width: 96px;
  height: 54px;
  padding: 0;
  border: 1px solid var(--hairline);
  border-radius: var(--radius);
  background: var(--control-bg);
  overflow: hidden;
  cursor: pointer;
}

.cleanup-thumb img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}

.cleanup-thumb__fallback {
  display: flex;
  width: 100%;
  height: 100%;
  align-items: center;
  justify-content: center;
  color: var(--text-muted);
  font-size: 16px;
}

/* 播放标记常驻但很淡，hover 时才明确——避免一屏几十个候选时满屏都是高亮三角。 */
.cleanup-thumb__play {
  position: absolute;
  right: 4px;
  bottom: 3px;
  padding: 0 5px;
  border-radius: 8px;
  background: var(--media-badge-bg);
  color: var(--media-badge-fg);
  font-size: 9px;
  line-height: 14px;
  opacity: 0.65;
}

.cleanup-thumb:hover {
  border-color: var(--accent-color);
}

.cleanup-thumb:hover .cleanup-thumb__play {
  opacity: 1;
}
</style>
