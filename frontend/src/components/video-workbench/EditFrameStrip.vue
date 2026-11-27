<template>
  <div class="edit-frames">
    <div v-if="title || (actualMs !== null && actualMs !== undefined)" class="edit-frames__head">
      <span v-if="title" class="edit-frames__title">{{ title }}</span>
      <AppBadge v-if="actualMs !== null && actualMs !== undefined" tone="warning" data-test="edit-fast-actual">
        快速模式实际切点（关键帧）：<span class="edit-frames__time">{{ formatTime(actualMs) }}</span>
      </AppBadge>
    </div>
    <div class="edit-frames__row">
      <figure v-for="frame in frames" :key="`${frame.videoID}-${frame.ms}-${frame.label}`" class="edit-frames__frame" data-test="edit-frame">
        <img :src="frameURL(frame)" alt="" loading="lazy" />
        <figcaption>
          <span>{{ frame.label }}</span>
          <span class="edit-frames__time edit-frames__stamp">{{ formatTime(frame.ms) }}</span>
        </figcaption>
      </figure>
    </div>
  </div>
</template>

<script>
import AppBadge from '../ui/AppBadge.vue';
import { editFrameURL, formatEditTime } from '../../utils/videoEdit.js';

// 一个切点前后的帧（视频编辑合同「界面」：每个切点显示切点前后帧，快速模式显示实际关键帧）。
// frames: [{ videoID, ms, label }]；帧图走共享单帧预览路由，不暴露源路径。
export default {
  name: 'EditFrameStrip',
  components: { AppBadge },
  props: {
    title: { type: String, default: '' },
    frames: { type: Array, default: () => [] },
    actualMs: { type: Number, default: null }
  },
  methods: {
    frameURL(frame) {
      return editFrameURL(frame.videoID, frame.ms);
    },
    formatTime: formatEditTime
  }
};
</script>

<style scoped>
.edit-frames {
  display: grid;
  gap: var(--space-2);
  min-width: 0;
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--hairline);
  border-radius: var(--radius);
  background: var(--panel-subtle-bg);
}
.edit-frames__head, .edit-frames__row {
  display: flex;
  gap: var(--space-2);
}
.edit-frames__head { align-items: center; flex-wrap: wrap; }
.edit-frames__title { font-size: var(--fs-secondary); font-weight: var(--fw-semibold); }
.edit-frames__frame { display: grid; flex: 0 1 160px; min-width: 0; align-content: start; gap: var(--space-1); margin: 0; }
.edit-frames__frame img {
  display: block;
  width: 100%;
  aspect-ratio: 16 / 9;
  object-fit: contain;
  border: 1px solid var(--border-strong);
  border-radius: var(--radius-sm);
  background: var(--player-bg);
}
.edit-frames__frame figcaption {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-1);
  font-size: var(--fs-caption);
  color: var(--text-secondary);
}
.edit-frames__time { font-family: var(--font-mono); font-variant-numeric: tabular-nums; }
.edit-frames__stamp {
  margin-inline-start: auto;
  padding: var(--space-1);
  border-radius: var(--radius-sm);
  background: var(--panel-muted-bg);
  color: var(--text-primary);
  line-height: 1;
}
</style>
