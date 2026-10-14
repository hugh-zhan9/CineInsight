<template>
  <!-- 顶部两个标签「推荐 · 收藏」（D-MW-01）：切的是两个页面，所以是导航、用 aria-current，不是页内 tablist。 -->
  <nav class="feed-tabs" aria-label="页面">
    <button
      v-for="tab in tabs"
      :key="tab.key"
      type="button"
      class="btn-plain feed-tabs__tab"
      :class="{ 'feed-tabs__tab--on': tab.key === current }"
      :aria-current="tab.key === current ? 'page' : undefined"
      :data-test="`short-feed-tab-${tab.key}`"
      @click="select(tab.key)"
    >{{ tab.label }}</button>
  </nav>
</template>

<script>
const TABS = [
  { key: 'feed', label: '推荐' },
  { key: 'favorites', label: '收藏' }
];

// 推荐页顶栏与收藏页顶栏共用这一组标签（D-MW-05「顶部标签与推荐页同一组件」）。
export default {
  name: 'FeedTabs',
  props: {
    current: { type: String, default: 'feed' }
  },
  emits: ['switch'],
  data() {
    return { tabs: TABS };
  },
  methods: {
    select(key) {
      if (key !== this.current) this.$emit('switch', key);
    }
  }
};
</script>
