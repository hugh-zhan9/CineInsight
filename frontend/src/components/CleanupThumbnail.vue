<template>
  <button
    v-if="video"
    type="button"
    class="btn-link cleanup-thumb"
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
    <span v-else class="cleanup-thumb__fallback"><AppIcon name="play" :size="20" /></span>
    <span class="cleanup-thumb__play"><AppIcon name="play" :size="16" /></span>
  </button>
</template>

<script>
import AppIcon from './ui/AppIcon.vue';

// 清理审阅里的缩略图：光看文件名和分辨率判断不了两个视频是不是真重复，
// 得让画面直接摆在一起。整块就是播放入口，点一下走和"预览"一样的抽屉。
export default {
  name: 'CleanupThumbnail',
  components: { AppIcon },
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
/* 整块缩略图是一个无边框的点按区（.btn-link），尺寸、边框与底色在这里定。 */
.cleanup-thumb {
  position: relative;
  display: block;
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
}

/* 播放标记常驻但很淡，hover 时才明确——避免一屏几十个候选时满屏都是高亮三角。 */
.cleanup-thumb__play {
  position: absolute;
  right: 4px;
  bottom: 3px;
  display: inline-flex;
  padding: 0 2px;
  border-radius: var(--radius-pill);
  background: var(--media-badge-bg);
  color: var(--media-badge-fg);
  opacity: 0.65;
}

.cleanup-thumb:hover {
  border-color: var(--accent-color);
  text-decoration: none;
}

.cleanup-thumb:hover .cleanup-thumb__play {
  opacity: 1;
}
</style>
