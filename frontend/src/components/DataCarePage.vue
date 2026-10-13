<template>
  <!-- 数据维护（docs/loopx/design/2026-10-12-data-care，D-DC-01）：失联 · 完整度 · 规则 · 体检。 -->
  <section class="page-content data-care-page">
    <PageHeader title="数据维护" subtitle="找回失联文件、补全元数据、自动整理与数据体检">
      <template #actions>
      <!-- 页签暂沿用原写法：AppTabs 还不能给每个页签挂 data-test（data-care-tab-*），见统一设计第 0 步 B3b 交接。 -->
      <nav class="data-care-tabs" role="tablist" aria-label="数据维护内容">
        <button
          v-for="item in tabs"
          :key="item.key"
          type="button"
          role="tab"
          :class="['toolbar-btn', 'toolbar-btn--compact', { 'toolbar-btn--on': tab === item.key }]"
          :aria-selected="tab === item.key"
          :data-test="`data-care-tab-${item.key}`"
          @click="tab = item.key"
        >{{ item.label }}</button>
      </nav>
      </template>
    </PageHeader>
    <component
      :is="activePanel"
      :key="tab"
      :directories="directories"
      @open-video-id="$emit('open-video-id', $event)"
      @run-command="(id, label) => $emit('run-command', id, label)"
      @videos-changed="$emit('videos-changed', $event)"
      @open-library-missing="$emit('open-library-missing', $event)"
      @navigate="$emit('navigate', $event)"
    />
  </section>
</template>

<script>
import LostMediaPanel from './data-care/LostMediaPanel.vue';
import CompletenessPanel from './data-care/CompletenessPanel.vue';
import AutomationRulesPanel from './data-care/AutomationRulesPanel.vue';
import DataHealthPanel from './data-care/DataHealthPanel.vue';
import PageHeader from './ui/PageHeader.vue';

export const DATA_CARE_TABS = [
  { key: 'lost', label: '失联', panel: 'LostMediaPanel' },
  { key: 'completeness', label: '完整度', panel: 'CompletenessPanel' },
  { key: 'rules', label: '规则', panel: 'AutomationRulesPanel' },
  { key: 'health', label: '体检', panel: 'DataHealthPanel' }
];

export default {
  name: 'DataCarePage',
  components: { LostMediaPanel, CompletenessPanel, AutomationRulesPanel, DataHealthPanel, PageHeader },
  props: {
    // { tab, token }：命令面板「数据维护 · 某页签」带进来；token 变化才切，避免回到页面时被旧请求拉回。
    tabRequest: { type: Object, default: null },
    directories: { type: Array, default: () => [] }
  },
  emits: ['open-video-id', 'run-command', 'videos-changed', 'open-library-missing', 'navigate'],
  data() {
    return { tab: this.validTab(this.tabRequest?.tab) || 'lost', tabs: DATA_CARE_TABS };
  },
  computed: {
    activePanel() {
      return (DATA_CARE_TABS.find(item => item.key === this.tab) || DATA_CARE_TABS[0]).panel;
    }
  },
  watch: {
    'tabRequest.token'() {
      const tab = this.validTab(this.tabRequest?.tab);
      if (tab) this.tab = tab;
    }
  },
  methods: {
    validTab(tab) {
      return DATA_CARE_TABS.some(item => item.key === tab) ? tab : '';
    }
  }
};
</script>

<style scoped>
.data-care-page { display: grid; gap: var(--space-4); }
.data-care-page :deep(.ui-page-header) { margin-bottom: 0; }
.data-care-tabs { display: inline-flex; gap: 6px; padding: var(--space-1); border: 1px solid var(--hairline); border-radius: var(--radius-pill); background: var(--panel-bg); }
.data-care-tabs .toolbar-btn { border-radius: var(--radius-pill); border-color: transparent; }
.data-care-tabs .toolbar-btn--on { border-color: var(--accent-color); }
</style>
