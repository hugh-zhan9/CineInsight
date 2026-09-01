<template>
  <div v-if="item" class="video-meta" :class="{ visible }">
    <h1>{{ item.name }}</h1>
    <p v-if="item.description" class="meta-description">{{ item.description }}</p>
    <p class="meta-line">{{ metaLine }}</p>
    <div class="tag-row">
      <span
        v-for="tag in (item.tags || [])"
        :key="tag.id"
        class="tag-chip"
        :style="{ backgroundColor: tagColor(tag.color) }"
      >{{ tag.name }}</span>
      <button type="button" class="tag-chip tag-chip--add" @click.stop="$emit('open-tags')">+ 标签</button>
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
