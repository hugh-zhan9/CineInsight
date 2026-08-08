<template>
  <div v-if="item" class="video-meta" :class="{ visible }">
    <h1>{{ item.name }}</h1>
    <p v-if="item.description" class="meta-description">{{ item.description }}</p>
    <div v-if="item.tags && item.tags.length" class="tag-row">
      <span
        v-for="tag in item.tags"
        :key="tag.id"
        class="tag-chip"
        :style="{ backgroundColor: tagColor(tag.color) }"
      >
        {{ tag.name }}
      </span>
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
  methods: {
    tagColor(color) {
      if (!color) return 'rgba(255,255,255,0.18)';
      return `${color}66`;
    }
  }
};
</script>
