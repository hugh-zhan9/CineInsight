<template>
  <span class="queue-action">
    <button type="button" :class="['btn', 'btn--secondary', { 'btn--sm': size === 'sm' }]" data-test="queue-add" :aria-pressed="toggles ? (queued ? 'true' : 'false') : undefined" :disabled="disabled || busy" @click.stop="apply">{{ buttonText }}</button>
    <small v-if="error" role="alert">{{ error }}</small>
    <button v-if="toggles && queued" type="button" class="btn-link" data-test="queue-show-added" @click.stop="openPlaybackQueue">查看队列</button>
    <button v-else-if="!toggles && added" type="button" :class="['btn', 'btn--secondary', { 'btn--sm': size === 'sm' }]" data-test="queue-show-added" @click.stop="openPlaybackQueue">已加入 · 查看队列</button>
  </span>
</template>
<script>
import { addToPlaybackQueue, findQueueEntriesForVideo, openPlaybackQueue, removeVideoFromPlaybackQueue } from '../utils/playbackQueue.js';
import { runtimeEventsMixin } from './video-list/runtimeEvents.js';
// size：按钮尺寸跟着所在的那一行走——抽屉与列表里是紧凑的 sm（默认），整页详情的动作行是常规的 md，
// 和旁边的「播放」「收藏」一样高。
// 只针对一部视频（加入单部、不替换队列）时按钮是开关：这部已在队列里就显示「移除待播」，点了把它在队列里的
// 每一条都删掉；不在就「加入待播」。是否在队列里打开时读一次，队列在别处变了（playback-queue-state）再读。
// 作品集、「从本集播放」「下一集」这类整批或替换队列的按钮照旧只加不减。
export default {
  name: 'QueueActionButton',
  mixins: [runtimeEventsMixin],
  props: { videoIds: { type: Array, default: () => [] }, collectionId: { type: Number, default: 0 }, startVideoId: { type: Number, default: 0 }, replace: Boolean, startAfter: Boolean, disabled: Boolean, label: { type: String, default: '加入待播队列' }, removeLabel: { type: String, default: '移除待播' }, size: { type: String, default: 'sm', validator: size => size === 'sm' || size === 'md' } },
  emits: ['started'],
  data: () => ({ busy: false, error: '', added: false, queued: false }),
  computed: {
    singleVideoID() {
      if (this.collectionId || this.replace || this.videoIds.length !== 1) return 0;
      return Number(this.videoIds[0]) || 0;
    },
    toggles() { return this.singleVideoID > 0; },
    buttonText() {
      if (this.busy) return '处理中…';
      return this.toggles && this.queued ? this.removeLabel : this.label;
    }
  },
  watch: {
    singleVideoID() { this.queued = false; this.refreshQueued(); }
  },
  mounted() {
    if (this.toggles) this.refreshQueued();
    this.registerRuntimeEvent('playback-queue-state', () => this.refreshQueued());
  },
  methods: {
    openPlaybackQueue,
    // 读这部是否在队列里；晚到的旧结果（换了视频、又读了一次）丢掉。读不到时不改现状，也不打扰用户。
    async refreshQueued() {
      const videoID = this.singleVideoID;
      if (!videoID) return;
      const seq = (this._queuedSeq || 0) + 1;
      this._queuedSeq = seq;
      try {
        const { entryIDs } = await findQueueEntriesForVideo(videoID);
        if (seq === this._queuedSeq && videoID === this.singleVideoID) this.queued = entryIDs.length > 0;
      } catch {
        // 队列暂时读不到：按钮保持原样，下一次队列变化或重新打开时再读。
      }
    },
    async apply() {
      if (this.busy || this.disabled) return;
      this.busy = true; this.error = '';
      try {
        if (this.toggles && this.queued) {
          await removeVideoFromPlaybackQueue(this.singleVideoID);
          this.queued = false;
          return;
        }
        const input = { videoIDs: [...this.videoIds], collectionID: this.collectionId, startVideoID: this.startVideoId, startAfter: this.startAfter, replace: this.replace };
        this.added = false;
        this.added = await addToPlaybackQueue(input);
        if (this.added && this.toggles) this.queued = true;
        if (this.added && input.replace) { this.$emit('started'); openPlaybackQueue(); }
      } catch (error) {
        this.error = String(error?.message || error);
      } finally {
        this.busy = false;
      }
    }
  }
};
</script>
<style scoped>
.queue-action { display: inline-flex; align-items: center; flex-wrap: wrap; gap: 6px; }
.queue-action small { color: var(--danger-text); font-size: var(--fs-caption); overflow-wrap: anywhere; }
</style>
