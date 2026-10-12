<template>
  <!-- 骨架：元数据完整度（D-MC-02）。实现按 docs/loopx/design/2026-10-12-data-care 补齐。 -->
  <section class="data-care-panel" data-test="data-care-panel-completeness">
    <p>建设中</p>
  </section>
</template>

<script>
export default {
  name: 'CompletenessPanel',
  props: { directories: { type: Array, default: () => [] } },
  emits: ['open-video-id', 'run-command', 'videos-changed', 'open-library-missing', 'navigate']
};
</script>
