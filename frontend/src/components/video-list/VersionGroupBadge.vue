<template>
  <button
    type="button"
    :class="['version-badge', { 'version-badge--open': expanded }]"
    :aria-expanded="expanded ? 'true' : 'false'"
    :title="expanded ? '收起版本列表' : '展开版本列表'"
    data-test="version-badge"
    @click.stop="$emit('toggle')"
  >
    <span class="version-badge__count">{{ summary.member_count }} 个版本</span>
    <span class="version-badge__summary" data-test="version-badge-summary">{{ summaryText }}</span>
    <span class="version-badge__caret" aria-hidden="true">{{ expanded ? '▴' : '▾' }}</span>
  </button>
</template>

<script>
import { versionGroupSummaryText } from '../../utils/versionGroups.js';

// 卡片上的「N 个版本」徽标与汇总（D-MW-VERSIONS）。点击只在卡片内展开/收起，不改任何数据。
export default {
  name: 'VersionGroupBadge',
  props: {
    summary: { type: Object, required: true },
    expanded: { type: Boolean, default: false }
  },
  emits: ['toggle'],
  computed: {
    summaryText() {
      return versionGroupSummaryText(this.summary);
    }
  }
};
</script>

<style scoped>
.version-badge {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  max-width: 100%;
  padding: 1px 8px;
  border: 1px solid var(--accent-border);
  border-radius: 999px;
  background: var(--accent-soft);
  color: var(--accent-text);
  font-size: 11px;
  font-weight: 650;
  line-height: 18px;
  cursor: pointer;
  white-space: nowrap;
}
.version-badge--open { background: var(--accent-color); color: var(--accent-on); }
.version-badge__summary { overflow: hidden; text-overflow: ellipsis; font-weight: 500; opacity: 0.9; }
.version-badge__caret { font-size: 9px; }
</style>
