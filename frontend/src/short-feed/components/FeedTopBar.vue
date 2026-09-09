<template>
  <div class="top-bar" :class="{ visible }" @click.stop>
    <span class="top-bar__title">短视频</span>
    <span class="top-bar__counter">{{ counter }}</span>
    <div class="top-bar__spacer"></div>
    <button class="pill-btn" type="button" @click="$emit('open-scope')">{{ scopeLabel }} ▾</button>
    <!-- 浏览器地址栏与系统栏会把 feed 割成一块；能进全屏就给一个入口。
         iPhone Safari 没有元素全屏，宿主会退到系统播放器全屏，图片条目上按钮不出现。 -->
    <button
      v-if="fullscreenAvailable"
      class="icon-btn"
      type="button"
      data-test="short-feed-fullscreen"
      :title="fullscreen ? '退出全屏' : '全屏'"
      :aria-label="fullscreen ? '退出全屏' : '全屏'"
      @click="$emit('toggle-fullscreen')"
    >
      <svg v-if="fullscreen" class="top-icon" viewBox="0 0 24 24" aria-hidden="true">
        <path d="M9 4v5H4" />
        <path d="M15 9V4h5" />
        <path d="M20 15h-5v5" />
        <path d="M4 15h5v5" />
      </svg>
      <svg v-else class="top-icon" viewBox="0 0 24 24" aria-hidden="true">
        <path d="M4 9V4h5" />
        <path d="M15 4h5v5" />
        <path d="M20 15v5h-5" />
        <path d="M9 20H4v-5" />
      </svg>
    </button>
    <button class="icon-btn" type="button" title="收藏夹" aria-label="收藏夹" @click="$emit('open-favorites')">
      <svg class="top-icon" viewBox="0 0 24 24" aria-hidden="true">
        <path d="M4 6.8C4 5.8 4.8 5 5.8 5h12.4C19.2 5 20 5.8 20 6.8v9.4c0 1-.8 1.8-1.8 1.8H5.8C4.8 18 4 17.2 4 16.2V6.8Z" />
        <path d="M7 2.8h10M7 21.2h10" />
      </svg>
    </button>
    <button
      class="icon-btn"
      type="button"
      :title="muted ? '打开声音' : '静音'"
      :aria-label="muted ? '打开声音' : '静音'"
      @click="$emit('toggle-muted')"
    >
      <svg v-if="muted" class="top-icon" viewBox="0 0 24 24" aria-hidden="true">
        <path d="M4 9.5h4l5-4v13l-5-4H4v-5Z" />
        <path d="m17 9 4 4m0-4-4 4" />
      </svg>
      <svg v-else class="top-icon" viewBox="0 0 24 24" aria-hidden="true">
        <path d="M4 9.5h4l5-4v13l-5-4H4v-5Z" />
        <path d="M17 8.5c1.2.9 2 2.2 2 3.5s-.8 2.6-2 3.5" />
        <path d="M19.5 5.5A8.6 8.6 0 0 1 23 12a8.6 8.6 0 0 1-3.5 6.5" />
      </svg>
    </button>
  </div>
</template>

<script>
export default {
  name: 'FeedTopBar',
  props: {
    visible: { type: Boolean, default: false },
    muted: { type: Boolean, default: true },
    counter: { type: String, default: '' },
    scopeLabel: { type: String, default: '全部短视频' },
    fullscreenAvailable: { type: Boolean, default: false },
    fullscreen: { type: Boolean, default: false }
  },
  emits: ['open-favorites', 'toggle-muted', 'open-scope', 'toggle-fullscreen']
};
</script>
