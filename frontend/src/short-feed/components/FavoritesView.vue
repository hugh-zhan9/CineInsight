<template>
  <section class="favorites-view">
    <header class="favorites-header">
      <button class="icon-btn" type="button" title="返回" @click="$emit('close')">←</button>
      <h1>收藏夹</h1>
      <button class="icon-btn" type="button" title="刷新" :disabled="loading" @click="$emit('refresh')">↻</button>
    </header>
    <!-- 加载失败是错误态，不是「暂无收藏」（D-PC46、PLAY-13）；已经显示的收藏留着。 -->
    <div v-if="error" class="favorites-error" role="alert" data-test="short-feed-favorites-error">
      <span>{{ error }}</span>
      <button type="button" class="feed-retry__btn" data-test="short-feed-favorites-retry" :disabled="loading" @click="$emit('refresh')">重试</button>
    </div>
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
      <!-- 不用舞台的 .feed-empty：它是整屏绝对定位，会盖住上面的返回按钮。 -->
      <p v-if="items.length === 0 && loading" class="favorites-empty">加载中</p>
      <p v-else-if="items.length === 0 && !error" class="favorites-empty">暂无收藏</p>
    </div>
  </section>
</template>

<script>
export default {
  name: 'FavoritesView',
  props: {
    items: { type: Array, default: () => [] },
    loading: { type: Boolean, default: false },
    error: { type: String, default: '' }
  },
  emits: ['close', 'refresh', 'select']
};
</script>
