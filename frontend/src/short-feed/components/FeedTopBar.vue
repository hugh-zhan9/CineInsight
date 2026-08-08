<template>
  <div class="top-bar" :class="{ visible }" @click.stop>
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
    muted: { type: Boolean, default: true }
  },
  emits: ['open-favorites', 'toggle-muted']
};
</script>
