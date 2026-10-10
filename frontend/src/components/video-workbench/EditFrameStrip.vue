<template>
  <div class="edit-frames">
    <span v-if="title" class="edit-frames__title">{{ title }}</span>
    <figure v-for="frame in frames" :key="`${frame.videoID}-${frame.ms}-${frame.label}`" class="edit-frames__frame" data-test="edit-frame">
      <img :src="frameURL(frame)" alt="" loading="lazy" />
      <figcaption>{{ frame.label }} · {{ formatTime(frame.ms) }}</figcaption>
    </figure>
    <span v-if="actualMs !== null && actualMs !== undefined" class="edit-frames__fast" data-test="edit-fast-actual">
      快速模式实际切点（关键帧）：{{ formatTime(actualMs) }}
    </span>
  </div>
</template>

<script>
import { editFrameURL, formatEditTime } from '../../utils/videoEdit.js';

// 一个切点前后的帧（视频编辑合同「界面」：每个切点显示切点前后帧，快速模式显示实际关键帧）。
// frames: [{ videoID, ms, label }]；帧图走共享单帧预览路由，不暴露源路径。
export default {
  name: 'EditFrameStrip',
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
.edit-frames { display: flex; flex-wrap: wrap; align-items: flex-start; gap: 8px; margin: 6px 0; }
.edit-frames__title { flex-basis: 100%; font-size: 12px; color: var(--text-secondary); }
.edit-frames__frame { margin: 0; display: grid; gap: 3px; }
.edit-frames__frame img { width: 160px; height: 90px; object-fit: contain; border-radius: 4px; background: var(--control-bg); }
.edit-frames__frame figcaption { font-size: 11px; color: var(--text-secondary); font-variant-numeric: tabular-nums; }
.edit-frames__fast { flex-basis: 100%; font-size: 12px; color: var(--warning-color, var(--accent-text)); }
</style>
