<template>
  <!-- 数据维护（docs/loopx/design/2026-10-12-data-care，D-DC-01）：失联 · 完整度 · 规则 · 体检。 -->
  <section class="page-content data-care-page">
    <PageHeader title="数据维护" subtitle="找回失联文件、补全元数据、自动整理与数据体检">
      <!-- 页签（D-UD-06 AppTabs），每个页签带 data-care-tab-* 钩子。 -->
      <template #below>
        <AppTabs v-model="tab" :tabs="pageTabs" aria-label="数据维护内容" />
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
import AppTabs from './ui/AppTabs.vue';
import PageHeader from './ui/PageHeader.vue';

export const DATA_CARE_TABS = [
  { key: 'lost', label: '失联', panel: 'LostMediaPanel' },
  { key: 'completeness', label: '完整度', panel: 'CompletenessPanel' },
  { key: 'rules', label: '规则', panel: 'AutomationRulesPanel' },
  { key: 'health', label: '体检', panel: 'DataHealthPanel' }
];

export default {
  name: 'DataCarePage',
  components: { AppTabs, LostMediaPanel, CompletenessPanel, AutomationRulesPanel, DataHealthPanel, PageHeader },
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
    pageTabs() {
      return this.tabs.map(item => ({ key: item.key, label: item.label, dataTest: `data-care-tab-${item.key}` }));
    },
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
</style>
