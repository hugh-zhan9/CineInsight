<template>
  <div
    :class="['related-video-card', { 'related-video-card--draggable': draggable }]"
    :draggable="draggable"
    @dragstart="$emit('dragstart', $event)"
    @dragover.prevent
    @drop="$emit('drop', $event)"
  >
    <input
      v-if="selectable"
      type="checkbox"
      class="related-video-card__select"
      :checked="selected"
      :aria-label="`选择 ${video.display_title || video.name}`"
      @change="$emit('select', $event.target.checked)"
    />
    <button type="button" class="related-video-card__main" @click="$emit('open')">
      <span :class="['related-video-card__thumbnail', { 'related-video-card__thumbnail--failed': thumbnailFailed }]">
        <img
          v-if="!thumbnailFailed"
          :src="thumbnailURL"
          :alt="`${video.name} 缩略图`"
          loading="lazy"
          @error="thumbnailFailed = true"
        />
        <span v-else aria-hidden="true">▶</span>
      </span>
      <span class="related-video-card__copy">
        <strong>{{ positionLabel }}{{ video.display_title || video.name }}</strong>
        <small>{{ video.name }}</small>
      </span>
    </button>
    <button
      v-if="actionLabel"
      type="button"
      :class="[destructive ? 'btn-danger' : 'btn-secondary', 'btn-compact', 'related-video-card__action']"
      :disabled="actionDisabled"
      @click="$emit('action')"
    >
      {{ actionLabel }}
    </button>
  </div>
</template>

<script>
export default {
  name: 'RelatedVideoItem',
  props: {
    video: { type: Object, required: true },
    positionLabel: { type: String, default: '' },
    actionLabel: { type: String, default: '' },
    actionDisabled: { type: Boolean, default: false },
    destructive: { type: Boolean, default: false },
    draggable: { type: Boolean, default: false },
    selectable: { type: Boolean, default: false },
    selected: { type: Boolean, default: false }
  },
  emits: ['open', 'action', 'select', 'dragstart', 'drop'],
  data() { return { thumbnailFailed: false }; },
  computed: {
    thumbnailURL() { return `/preview/thumbnail/${this.video.id}`; }
  },
  watch: {
    'video.id'() { this.thumbnailFailed = false; }
  }
};
</script>

<style scoped>
.related-video-card { display: flex; min-width: 0; align-items: center; gap: 8px; padding: 7px; border: 1px solid var(--border-color); border-radius: 11px; background: transparent; }
.related-video-card__select { width: 16px; height: 16px; flex: 0 0 auto; accent-color: var(--accent-color); }
.related-video-card__main { display: grid; min-width: 0; flex: 1; grid-template-columns: 88px minmax(0, 1fr); align-items: center; gap: 10px; border: 0; padding: 0; background: transparent; color: var(--text-primary); text-align: left; cursor: pointer; }
.related-video-card__thumbnail { display: grid; width: 88px; aspect-ratio: 16 / 9; place-items: center; overflow: hidden; border-radius: 8px; background: var(--thumb-bg); color: rgba(255, 255, 255, .72); }
.related-video-card__thumbnail img { width: 100%; height: 100%; display: block; object-fit: cover; }
.related-video-card__thumbnail--failed { background: var(--thumb-fallback-bg); }
.related-video-card__copy { min-width: 0; display: grid; gap: 4px; }
.related-video-card__copy strong,.related-video-card__copy small { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.related-video-card__copy strong { font-size: 13px; }
.related-video-card__copy small { color: var(--text-muted); font-size: 11px; }
.related-video-card__action { flex: 0 0 auto; }
.related-video-card--draggable { cursor: grab; }
.related-video-card--draggable:active { cursor: grabbing; }
@media (max-width: 460px) { .related-video-card__main { grid-template-columns: 72px minmax(0, 1fr); }.related-video-card__thumbnail { width: 72px; } }
</style>
