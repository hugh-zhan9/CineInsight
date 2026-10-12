<template>
  <!-- 骨架：失联数据：找回、清除与孤儿文件回收（D-LD-*）。实现按 docs/loopx/design/2026-10-12-data-care 补齐。 -->
  <section class="data-care-panel" data-test="data-care-panel-lost">
    <p>建设中</p>
  </section>
</template>

<script>
export default {
  name: 'LostMediaPanel',
  props: { directories: { type: Array, default: () => [] } },
  emits: ['open-video-id', 'run-command', 'videos-changed', 'open-library-missing', 'navigate']
};
</script>
