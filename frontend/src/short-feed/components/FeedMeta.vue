<template>
  <div v-if="item" class="video-meta" :class="{ visible }">
    <h1>{{ item.name }}</h1>
    <p v-if="item.description" class="meta-description">{{ item.description }}</p>
    <p class="meta-line">{{ metaLine }}</p>
    <div class="tag-row-wrap">
      <div ref="tagRow" class="tag-row" role="region" aria-label="视频标签" tabindex="0" @wheel.stop @keydown.stop @scroll.passive="updateOverflow">
        <span
          v-for="tag in (item.tags || [])"
          :key="tag.id"
          class="tag-chip"
          :style="{ backgroundColor: tagColor(tag.color) }"
        >{{ tag.name }}</span>
        <button type="button" class="tag-chip tag-chip--add" @click.stop="$emit('open-tags')">+ 标签</button>
      </div>
      <!-- 标签行最多占三成屏高，超出可滚；深色背景上没有滚动条，得说一声下面还有。 -->
      <span v-if="hasMoreBelow" class="tag-row__more" data-test="tag-row-more" aria-hidden="true">更多标签 ↓</span>
    </div>
  </div>
</template>

<script>
export default {
  name: 'FeedMeta',
  props: {
    visible: { type: Boolean, default: false },
    item: { type: Object, default: null }
  },
  emits: ['open-tags'],
  data() {
    return { hasMoreBelow: false };
  },
  mounted() {
    this.$nextTick(this.updateOverflow);
  },
  updated() {
    this.updateOverflow();
  },
  computed: {
    metaLine() {
      const parts = [];
      if (this.item?.duration) parts.push(this.formatDuration(this.item.duration));
      if (this.item?.width && this.item?.height) parts.push(`${this.item.width}×${this.item.height}`);
      const rating = this.item?.personal_rating;
      parts.push(rating === null || rating === undefined ? '未评分' : `评分 ${Number(rating).toFixed(1)}`);
      return parts.join(' · ');
    }
  },
  methods: {
    updateOverflow() {
      const row = this.$refs.tagRow;
      if (!row) return;
      this.hasMoreBelow = row.scrollHeight - row.clientHeight - row.scrollTop > 4;
    },
    formatDuration(seconds) {
      const total = Math.floor(Number(seconds) || 0);
      const minutes = Math.floor(total / 60);
      const secs = total % 60;
      return `${String(minutes).padStart(2, '0')}:${String(secs).padStart(2, '0')}`;
    },
    tagColor(color) {
      if (!color) return 'rgba(255,255,255,0.18)';
      return `${color}66`;
    }
  }
};
</script>
