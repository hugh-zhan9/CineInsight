<template>
  <div class="feed-dots" aria-hidden="true">
    <span
      v-for="(item, position) in total"
      :key="position"
      class="feed-dots__dot"
      :class="{ 'feed-dots__dot--active': position === index }"
    ></span>
  </div>
</template>

<script>
// 右侧圆点指示当前在已加载的这一段里的位置。它有意义的前提是 Feed 保留了
// 浏览历史——只有能往回划，位置才是个真实的东西。
export default {
  name: 'FeedDots',
  props: {
    total: { type: Number, default: 0 },
    index: { type: Number, default: 0 }
  }
};
</script>
