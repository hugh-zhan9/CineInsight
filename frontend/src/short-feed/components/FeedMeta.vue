<template>
  <!-- 左下信息（D-MW-02）：片名、类型 · 时长 · 评分 · 已看、标签行与「＋ 标签」。跟随控件一起收起。 -->
  <section
    v-if="item"
    class="feed-meta"
    :class="{ visible, 'feed-meta--photo': item.media_kind === 'image' }"
    :aria-label="item.media_kind === 'image' ? '当前图片' : '当前视频'"
  >
    <h1>{{ item.name }}</h1>
    <p v-if="item.description" class="feed-meta__description">{{ item.description }}</p>
    <p class="feed-meta__line">{{ metaLine }}</p>
    <p v-if="hint" class="feed-meta__hint">{{ hint }}</p>
    <div class="tag-row-wrap">
      <div ref="tagRow" class="tag-row" role="region" aria-label="标签" tabindex="0" @wheel.stop @keydown.stop @scroll.passive="updateOverflow">
        <span v-for="tag in (item.tags || [])" :key="tag.id" class="feed-tag">{{ tag.name }}</span>
        <button type="button" class="btn-plain feed-tag feed-tag--add" @click.stop="$emit('open-tags')">＋ 标签</button>
      </div>
      <!-- 标签行最多占三成屏高，超出可滚；深色背景上没有滚动条，得说一声下面还有。 -->
      <span v-if="hasMoreBelow" class="tag-row__more" data-test="tag-row-more" aria-hidden="true">更多标签 ↓</span>
    </div>
  </section>
</template>

<script>
import { metaLine } from '../format.js';

export default {
  name: 'FeedMeta',
  props: {
    visible: { type: Boolean, default: false },
    item: { type: Object, default: null },
    // 在收藏里看时的一行说明（D-MW-05）。
    hint: { type: String, default: '' }
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
      return metaLine(this.item);
    }
  },
  methods: {
    updateOverflow() {
      const row = this.$refs.tagRow;
      if (!row) return;
      this.hasMoreBelow = row.scrollHeight - row.clientHeight - row.scrollTop > 4;
    }
  }
};
</script>
