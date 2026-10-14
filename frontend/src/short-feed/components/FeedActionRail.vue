<template>
  <!-- 右侧只留三个（D-MW-02）：收藏、点赞、更多。评分、标签、已看、全屏、移到废纸篓都收进「更多」，
       删除不再就在手边。长按画面同时点赞 + 收藏照旧（在宿主里）。 -->
  <nav class="action-rail" :class="{ visible }" :aria-label="railLabel" @click.stop>
    <button
      class="btn-plain rail-action"
      :class="{ 'rail-action--fav': item?.favorited }"
      type="button"
      :aria-pressed="item?.favorited ? 'true' : 'false'"
      :disabled="!item"
      data-test="short-feed-favorite"
      @click="$emit('toggle-favorite')"
    >
      <span class="rail-action__dot">
        <svg class="action-icon action-icon--bookmark" viewBox="0 0 24 24" aria-hidden="true">
          <path d="M6 4.8C6 3.8 6.8 3 7.8 3h8.4c1 0 1.8.8 1.8 1.8V21l-6-3.8L6 21V4.8Z" />
        </svg>
      </span>
      <span class="rail-action__label">{{ item?.favorited ? '已收藏' : '收藏' }}</span>
    </button>

    <button
      class="btn-plain rail-action"
      :class="{ 'rail-action--on': item?.liked }"
      type="button"
      :aria-pressed="item?.liked ? 'true' : 'false'"
      :disabled="!item"
      data-test="short-feed-like"
      @click="$emit('toggle-like')"
    >
      <span class="rail-action__dot">
        <svg class="action-icon action-icon--heart" viewBox="0 0 24 24" aria-hidden="true">
          <path d="M20.8 4.9c-2-2-5.2-1.9-7.1.2L12 6.9l-1.7-1.8C8.4 3 5.2 2.9 3.2 4.9c-2.1 2.1-2 5.5.2 7.6L12 21l8.6-8.5c2.2-2.1 2.3-5.5.2-7.6Z" />
        </svg>
      </span>
      <span class="rail-action__label">{{ item?.liked ? '已点赞' : '点赞' }}</span>
    </button>

    <button
      class="btn-plain rail-action"
      type="button"
      aria-haspopup="dialog"
      :disabled="!item"
      data-test="short-feed-more"
      @click="$emit('open-more')"
    >
      <span class="rail-action__dot">
        <svg class="action-icon" viewBox="0 0 24 24" aria-hidden="true">
          <circle cx="5" cy="12" r="1.8" />
          <circle cx="12" cy="12" r="1.8" />
          <circle cx="19" cy="12" r="1.8" />
        </svg>
      </span>
      <span class="rail-action__label">更多</span>
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
  emits: ['toggle-like', 'toggle-favorite', 'open-more']
};
</script>
