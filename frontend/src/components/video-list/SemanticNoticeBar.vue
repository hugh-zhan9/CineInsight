<template>
  <div v-if="!semanticAvailable && semanticUnavailableNotice" class="scan-sync-status" role="status" data-test="semantic-unavailable">
    {{ semanticUnavailableNotice }}
  </div>

  <div v-else-if="searchMode === 'semantic'" :class="['scan-sync-status', { 'scan-sync-status--error': semanticSearchError }]" :role="semanticSearchError ? 'alert' : 'status'">
    <span v-if="semanticSearchError">语义搜索失败：{{ semanticSearchErrorText }}</span>
    <span v-else-if="semanticCoverage">语义索引覆盖 {{ semanticCoverage.indexed || 0 }}/{{ semanticCoverage.total || 0 }}；未建立索引的视频不会出现在结果中。</span>
    <span v-else>用自然语言描述想找的内容，回车开始搜索；未建立索引的视频不会出现在结果中。</span>
  </div>
</template>

<script>
// 语义检索的常驻提示条：能力不可用时说明原因，模式生效时说明索引覆盖或失败原因。
// 状态全部来自片库页，这里只做文案。
export default {
  name: 'SemanticNoticeBar',
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
.scan-sync-status {
  margin: 10px 0 0;
  display: flex;
  align-items: center;
  padding: 8px 10px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius);
  background: var(--control-bg);
  color: var(--text-secondary);
  font-size: 13px;
}

.scan-sync-status--running {
  border-color: var(--border-strong);
}

.scan-sync-status--success {
  border-color: var(--accent-border);
  color: var(--accent-color);
}

.scan-sync-status--warning {
  border-color: var(--warning-border);
  color: var(--warning-color);
}

.scan-sync-status--error {
  border-color: var(--danger-border);
  color: var(--danger-color);
}
</style>
