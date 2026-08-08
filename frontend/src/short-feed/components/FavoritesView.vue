<template>
  <section class="favorites-view">
    <header class="favorites-header">
      <button class="icon-btn" type="button" title="返回" @click="$emit('close')">←</button>
      <h1>收藏夹</h1>
      <button class="icon-btn" type="button" title="刷新" @click="$emit('refresh')">↻</button>
    </header>
    <div class="favorite-list">
      <button
        v-for="item in items"
        :key="`${item.media_kind}-${item.id}`"
        class="favorite-item"
        type="button"
        @click="$emit('select', item)"
      >
        <span class="favorite-title">{{ item.name }}</span>
        <span class="favorite-tags">{{ (item.tags || []).map(tag => tag.name).join(' · ') }}</span>
      </button>
      <div v-if="items.length === 0" class="feed-empty">暂无收藏</div>
    </div>
  </section>
</template>

<script>
export default {
  name: 'FavoritesView',
  props: {
    items: { type: Array, default: () => [] }
  },
  emits: ['close', 'refresh', 'select']
};
</script>
