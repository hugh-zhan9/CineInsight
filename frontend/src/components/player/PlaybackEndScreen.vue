<template>
  <section class="playback-ending" aria-label="播放结束" data-test="playback-ending">
    <header><strong>接下来想看什么？</strong><button type="button" class="btn btn--secondary btn--sm" :disabled="busy" data-test="ending-replay" @click="repeat">重播</button></header>
    <p v-if="error" role="alert">{{ error }} <button type="button" class="btn btn--secondary btn--sm" :disabled="busy" @click="load">重试推荐</button></p>
    <p v-else-if="loading" role="status">正在挑选…</p>
    <p v-else-if="!items.length" role="status">暂时没有其他可推荐的视频。</p>
    <div class="playback-ending__cards">
      <button v-for="item in items" :key="item.video.id" type="button" class="btn-plain playback-ending__card" :disabled="busy" data-test="ending-choice" @click="choose(item.video.id)">
        <img :src="`/preview/thumbnail/${Number(item.video.id)}`" alt="" /><strong>{{ item.video.display_title || item.video.name }}</strong><small>{{ formatDuration(item.video.duration) }}</small>
      </button>
    </div>
  </section>
</template>
<script>
import { SuggestTonightVideos } from '../../../wailsjs/go/main/App';
import { formatDuration } from '../../utils/mediaDetails.js';
import { translateBackendError } from '../../utils/feedback.js';
export default {
  name: 'PlaybackEndScreen',
  inject: { openEndedVideo: { default: null } },
  props: { ending: { type: Object, required: true }, replay: { type: Function, required: true }, retire: { type: Function, required: true } },
  emits: ['selected'],
  data: () => ({ items: [], loading: false, busy: false, error: '', generation: 0 }),
  computed: { identity() { const e = this.ending; return `${e.videoID || e.video_id}:${e.viewSessionID || e.token}:${e.cycle || e.view_cycle}:${e.sourceVersion || e.source_version}`; } },
  watch: { identity: { immediate: true, handler() { this.load(); } } },
  beforeUnmount() { this.generation++; },
  methods: {
    formatDuration,
    async load() {
      const generation = ++this.generation, id = Number(this.ending.videoID || this.ending.video_id);
      this.items = []; this.error = ''; this.loading = true;
      try {
        const rows = await SuggestTonightVideos({ filter: {}, watch_state: 'any', favorites_only: false, max_duration_seconds: 0, exclude_ids: [id] }, 3);
        if (generation !== this.generation) return;
        const seen = new Set([id]);
        this.items = (rows || []).filter(item => { const next = Number(item?.video?.id); if (!next || seen.has(next)) return false; seen.add(next); return true; }).slice(0, 3);
      } catch (error) { if (generation === this.generation) this.error = `推荐暂时没取到：${translateBackendError(error?.message || error)}`; }
      finally { if (generation === this.generation) this.loading = false; }
    },
    async choose(id) {
      if (this.busy) return;
      const generation = this.generation; this.busy = true; this.error = '';
      try {
        if (!this.openEndedVideo) throw new Error('当前页面无法打开视频详情');
        await this.openEndedVideo(Number(id), { retire: this.retire, isCurrent: () => generation === this.generation, onNavigated: () => this.$emit('selected') });
      } catch (error) { if (generation === this.generation) this.error = `视频没有打开：${translateBackendError(error?.message || error)}`; }
      finally { if (generation === this.generation) this.busy = false; }
    },
    async repeat() {
      if (this.busy) return; this.busy = true; this.error = '';
      try { await this.replay(); } catch (error) { this.error = `重播没有开始：${translateBackendError(error?.message || error)}`; }
      finally { this.busy = false; }
    }
  }
};
</script>
<style scoped>
.playback-ending { box-sizing: border-box; padding: var(--space-4); width: 100%; background: var(--overlay-strong); color: var(--media-badge-fg); }
.playback-ending header { display: flex; align-items: center; justify-content: space-between; gap: var(--space-3); }
.playback-ending__cards { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: var(--space-3); margin-top: var(--space-3); }
.playback-ending__card { display: grid; text-align: left; gap: var(--space-1); min-width: 0; color: inherit; }
.playback-ending__card img { width: 100%; aspect-ratio: 16 / 9; object-fit: cover; border-radius: var(--radius-sm); }
.playback-ending__card strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: var(--fs-body); }
.playback-ending__card small { font-size: var(--fs-caption); }
</style>
