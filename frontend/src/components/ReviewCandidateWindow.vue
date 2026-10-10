<template>
  <VirtualVideoList
    :items="rows"
    :active="active"
    :query-key="queryKey"
    :scroll-owner-selector="scrollOwnerSelector"
    :estimate-height="estimateHeight"
    :item-version="itemVersion"
    :overscan="5"
  >
    <template #default="{ item }">
      <slot v-if="item.kind === 'header'" name="header" :group="item.group"></slot>
      <div v-else class="review-candidate-window__candidate">
        <div class="review-candidate-window__context">{{ groupName(item.group) }}</div>
        <slot name="candidate" :group="item.group" :candidate="item.candidate"></slot>
      </div>
    </template>
  </VirtualVideoList>
</template>

<script>
import VirtualVideoList from './VirtualVideoList.vue';

// Projection only: the parent still owns every candidate and group action.
// Individual candidate rows keep a single-media group bounded as well.
export default {
  name: 'ReviewCandidateWindow',
  components: { VirtualVideoList },
  props: {
    groups: { type: Array, default: () => [] },
    kind: { type: String, required: true },
    active: { type: Boolean, default: true },
    queryKey: { type: String, default: '' },
    scrollOwnerSelector: { type: String, required: true }
  },
  computed: {
    rows() {
      const rows = [];
      for (const group of this.groups) {
        const id = this.kind === 'image' ? group.imageID : group.videoId;
        rows.push({ id: `group:${id}`, kind: 'header', group });
        for (const candidate of group.items || group.candidates || []) {
          rows.push({ id: `candidate:${candidate.id}`, kind: 'candidate', group, candidate });
        }
      }
      return rows;
    }
  },
  methods: {
    groupName(group) { return group.name || group.videoName || ''; },
    estimateHeight(row) { return row.kind === 'header' ? (this.kind === 'image' ? 100 : 210) : 112; },
    itemVersion(row) {
      if (row.kind === 'header') return JSON.stringify([this.groupName(row.group), row.group.videoPath, row.group.deleted, row.group.videoDeleted, row.group.videoTags]);
      return JSON.stringify([this.groupName(row.group), row.candidate.suggested_name, row.candidate.reasoning, row.candidate.confidence, row.candidate.matched_tag]);
    }
  }
};
</script>

<style scoped>
.review-candidate-window__candidate { display: flow-root; padding: 4px 0 8px; }
.review-candidate-window__context { color: var(--text-secondary); font-size: 11px; overflow-wrap: anywhere; margin-bottom: 4px; }
</style>
