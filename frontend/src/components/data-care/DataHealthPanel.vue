<template>
  <!-- 骨架：数据体检（D-DH-04 / D-DH-05）。实现按 docs/loopx/design/2026-10-12-data-care 补齐。 -->
  <section class="data-care-panel" data-test="data-care-panel-health">
    <p>建设中</p>
  </section>
</template>

<script>
export default {
  name: 'DataHealthPanel',
  props: { directories: { type: Array, default: () => [] } },
  emits: ['open-video-id', 'run-command', 'videos-changed', 'open-library-missing', 'navigate']
};
</script>
