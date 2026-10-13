<template>
  <AppBanner
    v-if="!semanticAvailable && semanticUnavailableNotice"
    class="semantic-notice"
    tone="warning"
    :title="semanticUnavailableNotice"
    data-test="semantic-unavailable"
  />

  <AppBanner
    v-else-if="searchMode === 'semantic' && semanticSearchError"
    class="semantic-notice"
    tone="danger"
    role="alert"
    title="语义搜索失败"
    :message="semanticSearchErrorText"
  />
  <AppBanner
    v-else-if="searchMode === 'semantic'"
    class="semantic-notice"
    :title="semanticCoverage ? `语义索引覆盖 ${semanticCoverage.indexed || 0}/${semanticCoverage.total || 0}` : '用自然语言描述想找的内容，回车开始搜索'"
    message="未建立索引的视频不会出现在结果中。"
  />
</template>

<script>
import AppBanner from '../ui/AppBanner.vue';

// 语义检索的页级横幅（D-UD-12 持续状态）：能力不可用时说明原因，模式生效时说明索引覆盖或失败原因。
// 这些是本页搜索方式的状态，不是后台任务，任务中心里没有对应信息，所以留在片库页（统一设计 D-UD-11 的例外）。
// 状态全部来自片库页，这里只做文案。
export default {
  name: 'SemanticNoticeBar',
  components: { AppBanner },
  props: {
    semanticAvailable: { type: Boolean, default: true },
    semanticUnavailableNotice: { type: String, default: '' },
    searchMode: { type: String, default: 'file' },
    semanticSearchError: { type: String, default: '' },
    semanticCoverage: { type: Object, default: null }
  },
  computed: {
    semanticSearchErrorText() {
      const raw = String(this.semanticSearchError || '');
      if (raw.includes('semantic_index_rebuild_required') || raw.includes('需要重建')) return '语义索引需要重建（模型或配置已变更），请到设置页重建索引。';
      if (raw.includes('尚未建立语义索引')) return '当前视频尚未建立语义索引，请先在设置页运行索引补全。';
      if (raw.includes('pgvector')) return '语义检索不可用：数据库缺少 pgvector 扩展。';
      return raw;
    },
  }
};
</script>

<style scoped>
.semantic-notice { margin-top: var(--space-3); }
</style>
