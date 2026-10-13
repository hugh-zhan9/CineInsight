<template>
  <span class="queue-action">
    <button type="button" class="btn btn--secondary btn--sm" data-test="queue-add" :disabled="disabled || busy" @click.stop="apply">{{ busy ? '处理中…' : label }}</button>
    <small v-if="error" role="alert">{{ error }}</small>
    <button v-if="added" type="button" class="btn btn--secondary btn--sm" data-test="queue-show-added" @click.stop="openPlaybackQueue">已加入 · 查看队列</button>
  </span>
</template>
<script>
import { addToPlaybackQueue, openPlaybackQueue } from '../utils/playbackQueue.js';
export default {
  name: 'QueueActionButton',
  props: { videoIds: { type: Array, default: () => [] }, collectionId: { type: Number, default: 0 }, startVideoId: { type: Number, default: 0 }, replace: Boolean, startAfter: Boolean, disabled: Boolean, label: { type: String, default: '加入待播队列' } },
  emits: ['started'],
  data: () => ({ busy: false, error: '', added: false }),
  methods: {
    openPlaybackQueue,
    async apply() {
      if (this.busy || this.disabled) return;
      const input = { videoIDs: [...this.videoIds], collectionID: this.collectionId, startVideoID: this.startVideoId, startAfter: this.startAfter, replace: this.replace };
      this.busy = true; this.error = ''; this.added = false;
      try { this.added = await addToPlaybackQueue(input); if (this.added && input.replace) { this.$emit('started'); openPlaybackQueue(); } }
      catch (error) { this.error = String(error); }
      finally { this.busy = false; }
    }
  }
};
</script>
<style scoped>
.queue-action { display: inline-flex; align-items: center; flex-wrap: wrap; gap: 6px; }
.queue-action small { color: var(--danger-text); font-size: var(--fs-caption); overflow-wrap: anywhere; }
</style>
