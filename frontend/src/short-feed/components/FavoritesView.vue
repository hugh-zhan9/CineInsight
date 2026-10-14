<template>
  <!-- 收藏（D-MW-05）：三列缩略图墙，视频角上标时长；点一张进「在收藏里看」，顺序与墙一致。
       整页不归舞台的手势管（宿主按 .favorites-view 放行），滚轮也不往上冒，墙才能原生滚动。 -->
  <section class="favorites-view" @wheel.stop>
    <header class="favorites-header">
      <FeedTabs current="favorites" @switch="$emit('switch-view', $event)" />
      <div class="top-bar__spacer"></div>
      <button type="button" class="btn-icon top-bar__icon" aria-label="刷新" data-test="short-feed-favorites-refresh" :disabled="loading" @click="$emit('refresh')">
        <AppIcon name="refresh" :size="20" />
      </button>
    </header>
    <div class="favorites-toolbar">
      <SegmentedControl
        class="favorites-kind"
        label="类型"
        :options="kindOptions"
        :model-value="kind"
        data-test="short-feed-favorites-kind"
        @update:model-value="$emit('update:kind', $event)"
      />
      <span v-if="!loading || items.length" class="favorites-count" data-test="short-feed-favorites-count">{{ visibleItems.length }} 个 · 最近收藏在前</span>
    </div>
    <!-- 加载失败是错误态，不是「还没有收藏」（D-PC46、PLAY-13）；已经显示的收藏留着。 -->
    <AppBanner v-if="error" class="favorites-error" tone="danger" role="alert" :title="error" data-test="short-feed-favorites-error">
      <template #actions>
        <button type="button" class="btn btn--secondary" data-test="short-feed-favorites-retry" :disabled="loading" @click="$emit('refresh')">重试</button>
      </template>
    </AppBanner>
    <div v-if="loading && items.length === 0" class="favorites-grid" aria-busy="true" aria-label="正在读取收藏">
      <SkeletonBlock v-for="index in 9" :key="index" class="favorite-skeleton" shape="thumb" />
    </div>
    <EmptyState
      v-else-if="visibleItems.length === 0 && !error"
      class="favorites-empty"
      :title="emptyTitle"
      message="刷的时候点右侧书签就能收藏"
      data-test="short-feed-favorites-empty"
    />
    <div v-else class="favorites-grid" data-test="short-feed-favorites-grid">
      <button
        v-for="(item, position) in visibleItems"
        :key="keyOf(item)"
        type="button"
        class="btn-plain favorite-tile"
        :aria-label="tileLabel(item)"
        @click="$emit('select', visibleItems, position)"
      >
        <img
          v-if="!failedThumbs[keyOf(item)]"
          class="favorite-tile__thumb"
          :src="thumbnailURL(item)"
          alt=""
          loading="lazy"
          decoding="async"
          @error="failedThumbs[keyOf(item)] = true"
        />
        <span v-if="item.media_kind !== 'image'" class="favorite-tile__badge">
          <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M7 4.5v15l12-7.5-12-7.5Z" /></svg>{{ formatClock(item.duration) }}
        </span>
      </button>
    </div>
  </section>
</template>

<script>
import AppBanner from '../../components/ui/AppBanner.vue';
import AppIcon from '../../components/ui/AppIcon.vue';
import EmptyState from '../../components/ui/EmptyState.vue';
import SegmentedControl from '../../components/ui/SegmentedControl.vue';
import SkeletonBlock from '../../components/ui/SkeletonBlock.vue';
import FeedTabs from './FeedTabs.vue';
import { itemKey, thumbnailURL } from '../api.js';
import { formatClock } from '../format.js';

const KIND_OPTIONS = [
  { value: 'all', label: '全部' },
  { value: 'video', label: '视频' },
  { value: 'image', label: '图片' }
];

export default {
  name: 'FavoritesView',
  components: { AppBanner, AppIcon, EmptyState, FeedTabs, SegmentedControl, SkeletonBlock },
  props: {
    // 服务端给的全量收藏（按收藏时间倒序）；类型筛选在前端做。
    items: { type: Array, default: () => [] },
    // 类型筛选记在宿主：从「在收藏里看」回来还是原来的筛选。
    kind: { type: String, default: 'all' },
    loading: { type: Boolean, default: false },
    error: { type: String, default: '' }
  },
  emits: ['switch-view', 'update:kind', 'refresh', 'select'],
  data() {
    return { kindOptions: KIND_OPTIONS, failedThumbs: {} };
  },
  computed: {
    visibleItems() {
      return this.kind === 'all' ? this.items : this.items.filter(item => item.media_kind === this.kind);
    },
    emptyTitle() {
      if (this.kind === 'video') return '还没有收藏视频';
      if (this.kind === 'image') return '还没有收藏图片';
      return '还没有收藏';
    }
  },
  methods: {
    formatClock,
    thumbnailURL,
    keyOf: itemKey,
    tileLabel(item) {
      return item.media_kind === 'image' ? `${item.name}，图片` : `${item.name}，视频 ${formatClock(item.duration)}`;
    }
  }
};
</script>
