<template>
  <nav class="action-rail" :class="{ visible }" :aria-label="railLabel" @click.stop>
    <button
      class="rail-action"
      :class="{ 'rail-action--fav': item?.favorited }"
      type="button"
      :disabled="!item"
      @click="$emit('toggle-favorite')"
    >
      <span class="rail-action__dot">
        <svg class="action-icon action-icon--bookmark" viewBox="0 0 24 24" aria-hidden="true">
          <path d="M6 4.8C6 3.8 6.8 3 7.8 3h8.4c1 0 1.8.8 1.8 1.8V21l-6-3.8L6 21V4.8Z" />
        </svg>
      </span>
      <span class="rail-action__label">收藏</span>
    </button>

    <button
      class="rail-action"
      :class="{ 'rail-action--on': item?.liked }"
      type="button"
      :disabled="!item"
      @click="$emit('toggle-like')"
    >
      <span class="rail-action__dot">
        <svg class="action-icon action-icon--heart" viewBox="0 0 24 24" aria-hidden="true">
          <path d="M20.8 4.9c-2-2-5.2-1.9-7.1.2L12 6.9l-1.7-1.8C8.4 3 5.2 2.9 3.2 4.9c-2.1 2.1-2 5.5.2 7.6L12 21l8.6-8.5c2.2-2.1 2.3-5.5.2-7.6Z" />
        </svg>
      </span>
      <span class="rail-action__label">点赞</span>
    </button>

    <button
      class="rail-action"
      :class="{ 'rail-action--on': hasRating }"
      type="button"
      :disabled="!item"
      @click="$emit('open-rating')"
    >
      <span class="rail-action__dot rail-action__dot--text">{{ ratingShort }}</span>
      <span class="rail-action__label">评分</span>
    </button>

    <button class="rail-action" type="button" :disabled="!item" @click="$emit('open-tags')">
      <span class="rail-action__dot rail-action__dot--text">#</span>
      <span class="rail-action__label">{{ tagCountLabel }}</span>
    </button>

    <!-- 图片没有观看状态，这个按钮只对视频出现，而不是给一个按了没反应的按钮 -->
    <button
      v-if="item?.media_kind !== 'image'"
      class="rail-action"
      :class="{ 'rail-action--on': item?.watched }"
      type="button"
      :disabled="!item"
      @click="$emit('toggle-watched')"
    >
      <span class="rail-action__dot">✓</span>
      <span class="rail-action__label">已看</span>
    </button>

    <button class="rail-action rail-action--danger" type="button" :disabled="!item" @click="$emit('request-delete')">
      <span class="rail-action__dot">
        <svg class="action-icon" viewBox="0 0 24 24" aria-hidden="true">
          <path d="M5 7h14" />
          <path d="M9 7V5.5C9 4.7 9.7 4 10.5 4h3c.8 0 1.5.7 1.5 1.5V7" />
          <path d="M7 7l1 12c.1.8.8 1.5 1.6 1.5h4.8c.8 0 1.5-.7 1.6-1.5l1-12" />
          <path d="M10.5 11v5.5M13.5 11v5.5" />
        </svg>
      </span>
      <span class="rail-action__label">删除</span>
    </button>
  </nav>
</template>

<script>
export default {
  name: 'FeedActionRail',
  props: {
    visible: { type: Boolean, default: false },
    item: { type: Object, default: null },
    railLabel: { type: String, default: '视频操作' }
  },
  emits: ['toggle-like', 'toggle-favorite', 'toggle-watched', 'open-rating', 'open-tags', 'request-delete'],
  computed: {
    hasRating() {
      return this.item?.personal_rating !== null && this.item?.personal_rating !== undefined;
    },
    ratingShort() {
      return this.hasRating ? Number(this.item.personal_rating).toFixed(1) : '—';
    },
    tagCountLabel() {
      const count = this.item?.tags?.length || 0;
      return count > 0 ? `${count} 个` : '标签';
    }
  }
};
</script>
