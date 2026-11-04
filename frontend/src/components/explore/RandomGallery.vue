<template>
  <div ref="gallery" class="random-gallery" data-test="random-gallery" aria-label="随机画廊">
    <MediaTile v-for="entry in entries" :id="`gallery-entry-${entry.entry_id}`" :key="entry.entry_id" :entry="entry" :card="cards[entry.entry_id]" :loader="loader" :retry-signal="retrySignal" :edge="256" :selected="entry.entry_id === selectedId" show-title tab-stop @loaded="setLoaded" @failed="(id, failed) => $emit('failed', id, failed)" @open="$emit('open', entry)" />
  </div>
</template>

<script>
import { mediaVisibleClip, mediaVisibleFraction, mediaUnobscured } from '../../utils/mediaExposure.js';
import MediaTile from './MediaTile.vue';
export default {
  name: 'RandomGallery', components: { MediaTile },
  props: { entries: { type: Array, default: () => [] }, cards: { type: Object, default: () => ({}) }, loader: { type: Object, required: true }, selectedId: { type: Number, default: 0 }, retrySignal: { type: Number, default: 0 } },
  emits: ['visible', 'exposure', 'open', 'failed'], data: () => ({ loaded: {} }),
  watch: { entries: { immediate: true, handler(entries) { this.$emit('visible', entries.map(e => e.entry_id));  } } },
  mounted() { this._timer = setInterval(() => this.reportExposure(), 100); },
  beforeUnmount() { clearInterval(this._timer); },
  methods: {
    reportExposure() {
      const gallery = this.$refs.gallery; if (!gallery) return;
      const clip = mediaVisibleClip(gallery), nodes = new Map([...gallery.querySelectorAll('[data-entry-id]')].map(node => [Number(node.dataset.entryId), node]));
      this.$emit('exposure', this.entries.map(e => { const node = nodes.get(e.entry_id), loaded = !!this.loaded[e.entry_id], fraction = node ? mediaVisibleFraction(node.getBoundingClientRect(), clip) : 0; return { id: e.entry_id, loaded, fraction, presented: e.presented, unobscured: loaded && !e.presented && fraction >= .5 && mediaUnobscured(node, clip) }; }));
    },
    setLoaded(id, loaded) { this.loaded[id] = loaded; }
  }
};
</script>

<style scoped>
.random-gallery { display: grid; grid-template-columns: repeat(auto-fill, minmax(min(220px, 100%), 1fr)); gap: 12px; max-height: 72vh; overflow: auto; padding: 4px; }
.random-gallery :deep(.media-tile) { aspect-ratio: 4 / 3; border-radius: var(--radius-md); }
</style>
