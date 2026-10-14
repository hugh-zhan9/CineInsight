<template>
  <!-- 「更多」面板（D-MW-02）：个人评分、标签、已看过（只对视频）、全屏播放（浏览器支持时）、移到废纸篓（放最后）、取消。 -->
  <FeedSheet :title="item.name" :subtitle="kindLine" label="更多操作" close-style="none" @close="$emit('close')">
    <div class="sheet-rows">
      <button type="button" class="btn-plain more-row" data-test="short-feed-more-rating" @click="$emit('open-rating')">
        <AppIcon name="star" :size="20" />
        <span class="more-row__label">个人评分</span>
        <span class="more-row__value" :class="{ 'more-row__value--accent': ratingText }">{{ ratingText || '未评分' }}</span>
        <AppIcon class="more-row__chevron" name="chevron-right" :size="16" />
      </button>
      <button type="button" class="btn-plain more-row" data-test="short-feed-more-tags" @click="$emit('open-tags')">
        <svg class="more-row__icon" viewBox="0 0 24 24" aria-hidden="true"><path d="M5 9h14M5 15h14M10 4 8 20M16 4l-2 16" /></svg>
        <span class="more-row__label">标签</span>
        <span class="more-row__value">{{ tagCount }} 个</span>
        <AppIcon class="more-row__chevron" name="chevron-right" :size="16" />
      </button>
      <!-- 图片没有观看状态，这一项只对视频出现，而不是给一个按了没反应的开关。 -->
      <button
        v-if="item.media_kind !== 'image'"
        type="button"
        class="btn-plain more-row"
        role="switch"
        :aria-checked="item.watched ? 'true' : 'false'"
        data-test="short-feed-watched"
        @click="$emit('toggle-watched')"
      >
        <AppIcon name="check" :size="20" />
        <span class="more-row__label">已看过</span>
        <span class="more-switch" :class="{ 'more-switch--on': item.watched }" aria-hidden="true"></span>
      </button>
      <!-- 浏览器地址栏与系统栏会把画面割成一块；能进全屏才给这一项。
           iPhone Safari 没有元素全屏，宿主会退到系统播放器全屏，图片条目上不出现。 -->
      <button
        v-if="fullscreenAvailable"
        type="button"
        class="btn-plain more-row"
        data-test="short-feed-fullscreen"
        @click="$emit('toggle-fullscreen')"
      >
        <svg class="more-row__icon" viewBox="0 0 24 24" aria-hidden="true">
          <path v-if="fullscreen" d="M9 4v5H4M15 4v5h5M20 15h-5v5M4 15h5v5" />
          <path v-else d="M4 9V4h5M15 4h5v5M20 15v5h-5M9 20H4v-5" />
        </svg>
        <span class="more-row__label">{{ fullscreen ? '退出全屏' : '全屏播放' }}</span>
      </button>
      <!-- 移到废纸篓不确认、不撤销（D-MW-03）：恢复在电脑端回收站。删除进行中禁用，防止连点。 -->
      <button
        type="button"
        class="btn-plain more-row more-row--danger"
        data-test="short-feed-delete"
        :disabled="deleting"
        @click="$emit('delete')"
      >
        <AppIcon name="trash" :size="20" />
        <span class="more-row__label more-row__label--stack">
          <span>移到废纸篓</span>
          <span class="more-row__sub">点了直接移走，可在电脑端回收站恢复</span>
        </span>
      </button>
    </div>
    <template #footer>
      <button type="button" class="btn btn--secondary" data-test="short-feed-more-cancel" @click="$emit('close')">取消</button>
    </template>
  </FeedSheet>
</template>

<script>
import AppIcon from '../../components/ui/AppIcon.vue';
import FeedSheet from './FeedSheet.vue';
import { formatRating, kindLine } from '../format.js';

export default {
  name: 'MoreSheet',
  components: { AppIcon, FeedSheet },
  props: {
    item: { type: Object, required: true },
    fullscreenAvailable: { type: Boolean, default: false },
    fullscreen: { type: Boolean, default: false },
    deleting: { type: Boolean, default: false }
  },
  emits: ['close', 'open-rating', 'open-tags', 'toggle-watched', 'toggle-fullscreen', 'delete'],
  computed: {
    kindLine() {
      return kindLine(this.item);
    },
    ratingText() {
      return formatRating(this.item.personal_rating);
    },
    tagCount() {
      return this.item.tags?.length || 0;
    }
  }
};
</script>
