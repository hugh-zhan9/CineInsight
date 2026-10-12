<template>
  <!-- 骨架：本地规则（D-RA-*）。实现按 docs/loopx/design/2026-10-12-data-care 补齐。 -->
  <section class="data-care-panel" data-test="data-care-panel-rules">
    <p>建设中</p>
  </section>
</template>

<script>
export default {
  name: 'AutomationRulesPanel',
  props: { directories: { type: Array, default: () => [] } },
  emits: ['open-video-id', 'run-command', 'videos-changed', 'open-library-missing', 'navigate']
};
</script>
