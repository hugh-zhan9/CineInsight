<template>
  <!-- 骨架：观看侧整页详情（D-UI-05 / D-UI-10 / D-UI-11）。实现按 docs/loopx/design/2026-10-12-viewer-manager-split 补齐。 -->
  <section class="page-content viewer-detail" data-test="viewer-detail">
    <p>建设中</p>
  </section>
</template>

<script>
export default {
  name: 'VideoDetailPage',
  props: {
    // 要展示的视频 ID 与可选的起播位置（毫秒，场景检索 / 书签定位进来时有）。
    videoId: { type: Number, default: 0 },
    startAtMs: { type: Number, default: null }
  },
  emits: ['back', 'open-video', 'navigate', 'edit-in-manage']
};
</script>
