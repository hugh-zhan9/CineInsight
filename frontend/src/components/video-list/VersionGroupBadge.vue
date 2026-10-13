<template>
  <button
    type="button"
    :class="['btn-link', 'version-badge', { 'version-badge--open': expanded }]"
    :aria-expanded="expanded ? 'true' : 'false'"
    :title="expanded ? '收起版本列表' : '展开版本列表'"
    data-test="version-badge"
    @click.stop="$emit('toggle')"
  >
    <span class="version-badge__count">{{ summary.member_count }} 个版本</span>
    <span class="version-badge__summary" data-test="version-badge-summary">{{ summaryText }}</span>
    <AppIcon class="version-badge__caret" :name="expanded ? 'chevron-up' : 'chevron-down'" :size="16" />
  </button>
</template>

<script>
import { versionGroupSummaryText } from '../../utils/versionGroups.js';
import AppIcon from '../ui/AppIcon.vue';

// 卡片上的「N 个版本」徽标与汇总（D-MW-VERSIONS）。点击只在卡片内展开/收起，不改任何数据。
export default {
  name: 'VersionGroupBadge',
  components: { AppIcon },
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
/* 胶囊形的展开开关：外观在这里定，.btn-link 只提供无边框按钮的基础（焦点环、继承字体）。 */
.version-badge {
  display: inline-flex;
  align-items: center;
  gap: var(--space-1);
  max-width: 100%;
  min-width: 0;
  padding: 0 var(--space-1) 0 var(--space-2);
  border: 1px solid var(--accent-border);
  border-radius: var(--radius-pill);
  background: var(--accent-soft);
  color: var(--accent-text);
  font-size: var(--fs-caption);
  font-weight: var(--fw-semibold);
  line-height: 18px;
  cursor: pointer;
  white-space: nowrap;
}
.version-badge:hover:not(:disabled) { text-decoration: none; border-color: var(--accent-color); }
.version-badge--open { background: var(--accent-color); color: var(--accent-on); }
.version-badge__summary { overflow: hidden; text-overflow: ellipsis; font-weight: var(--fw-medium); opacity: 0.9; }
.version-badge__caret { flex: none; }
</style>
