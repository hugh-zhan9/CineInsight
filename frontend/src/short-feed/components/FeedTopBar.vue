<template>
  <!-- 顶栏（D-MW-01）：推荐流只有「推荐 · 收藏」、范围、静音；在收藏里看换成「‹ 收藏」与位置。
       去掉了「短视频」标题（流里混着图片）、计数、收藏夹与全屏按钮（全屏进了「更多」）。 -->
  <header class="top-bar" :class="{ visible, 'top-bar--back': mode === 'favorites' }" @click.stop>
    <template v-if="mode === 'favorites'">
      <button type="button" class="btn-plain top-bar__back" data-test="short-feed-favorites-back" @click="$emit('back')">
        <AppIcon name="chevron-left" :size="24" />收藏
      </button>
      <span class="top-bar__position" data-test="short-feed-favorites-position">{{ position }}</span>
    </template>
    <FeedTabs v-else current="feed" @switch="$emit('switch-view', $event)" />
    <div class="top-bar__spacer"></div>
    <button
      v-if="mode === 'feed'"
      type="button"
      class="btn-plain scope-button"
      data-test="short-feed-scope"
      :aria-label="scopeDescription"
      @click="$emit('open-scope')"
    >
      <span class="scope-button__pill">
        <svg class="top-icon top-icon--small" viewBox="0 0 24 24" aria-hidden="true">
          <path d="M4 7h10M18 7h2M4 17h4M12 17h8" />
          <circle cx="16" cy="7" r="2" />
          <circle cx="10" cy="17" r="2" />
        </svg>
        <span class="scope-button__text">{{ scopeLabel }}</span>
      </span>
    </button>
    <button
      type="button"
      class="btn-icon top-bar__icon"
      :aria-label="muted ? '打开声音' : '关闭声音'"
      @click="$emit('toggle-muted')"
    >
      <svg v-if="muted" class="top-icon" viewBox="0 0 24 24" aria-hidden="true">
        <path d="M4 9.5h4l5-4v13l-5-4H4v-5Z" />
        <path d="m17 9 4 4m0-4-4 4" />
      </svg>
      <svg v-else class="top-icon" viewBox="0 0 24 24" aria-hidden="true">
        <path d="M4 9.5h4l5-4v13l-5-4H4v-5Z" />
        <path d="M17 8.5c1.2.9 2 2.2 2 3.5s-.8 2.6-2 3.5" />
      </svg>
    </button>
  </header>
</template>

<script>
import AppIcon from '../../components/ui/AppIcon.vue';
import FeedTabs from './FeedTabs.vue';

export default {
  name: 'FeedTopBar',
  components: { AppIcon, FeedTabs },
  props: {
    visible: { type: Boolean, default: false },
    muted: { type: Boolean, default: true },
    // feed：推荐流；favorites：在收藏里看。
    mode: { type: String, default: 'feed' },
    scopeLabel: { type: String, default: '全部' },
    // 范围按钮的读屏名：「播放范围：全部，视频和图片」。
    scopeDescription: { type: String, default: '' },
    // 在收藏里看时的「n / N」。
    position: { type: String, default: '' }
  },
  emits: ['switch-view', 'toggle-muted', 'open-scope', 'back']
};
</script>
