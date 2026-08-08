<template>
  <nav class="action-rail" :class="{ visible }" :aria-label="railLabel" @click.stop>
    <button
      class="round-action"
      :class="{ active: item?.liked }"
      type="button"
      title="喜欢"
      :disabled="!item"
      @click="$emit('toggle-like')"
    >
      <svg class="action-icon action-icon--heart" viewBox="0 0 24 24" aria-hidden="true">
        <path d="M20.8 4.9c-2-2-5.2-1.9-7.1.2L12 6.9l-1.7-1.8C8.4 3 5.2 2.9 3.2 4.9c-2.1 2.1-2 5.5.2 7.6L12 21l8.6-8.5c2.2-2.1 2.3-5.5.2-7.6Z" />
      </svg>
      <span class="action-count">{{ item?.liked ? 1 : 0 }}</span>
    </button>
    <button
      class="round-action"
      :class="{ active: item?.favorited }"
      type="button"
      title="收藏"
      :disabled="!item"
      @click="$emit('toggle-favorite')"
    >
      <svg class="action-icon action-icon--bookmark" viewBox="0 0 24 24" aria-hidden="true">
        <path d="M6 4.8C6 3.8 6.8 3 7.8 3h8.4c1 0 1.8.8 1.8 1.8V21l-6-3.8L6 21V4.8Z" />
      </svg>
      <span class="action-count">{{ item?.favorited ? 1 : 0 }}</span>
    </button>
    <button
      class="round-action danger"
      type="button"
      title="删除"
      :disabled="!item"
      @click="$emit('request-delete')"
    >
      <svg class="action-icon" viewBox="0 0 24 24" aria-hidden="true">
        <path d="M5 7h14" />
        <path d="M9 7V5.5C9 4.7 9.7 4 10.5 4h3c.8 0 1.5.7 1.5 1.5V7" />
        <path d="M7 7l1 12c.1.8.8 1.5 1.6 1.5h4.8c.8 0 1.5-.7 1.6-1.5l1-12" />
        <path d="M10.5 11v5.5M13.5 11v5.5" />
      </svg>
    </button>
  </nav>
</template>

<script>
export default {
  name: 'FeedActionRail',
  props: {
    visible: { type: Boolean, default: false },
    item: { type: Object, default: null },
    railLabel: { type: String, default: '内容操作' }
  },
  emits: ['toggle-like', 'toggle-favorite', 'request-delete']
};
</script>
