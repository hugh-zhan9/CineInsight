<template>
  <aside class="playback-transcript surface-cinema" aria-label="同步台词" data-test="playback-transcript" @keydown.esc.stop.prevent="$emit('close')">
    <header class="playback-transcript__header">
      <strong>台词</strong>
      <button type="button" class="btn btn--ghost btn--sm" :aria-pressed="following" @click="resumeFollowing">跟随播放</button>
      <button type="button" class="btn-icon" aria-label="关闭台词" @click="$emit('close')"><AppIcon name="close" :size="16" /></button>
    </header>
    <p v-if="!rows.length" class="playback-transcript__empty">当前没有可显示的台词</p>
    <div v-else ref="list" class="playback-transcript__list" tabindex="0" @scroll="scrolled" @wheel.passive="following = false" @touchstart.passive="following = false" @pointerdown="following = false" @keydown="userScroll">
      <div :style="{ height: `${totalHeight}px`, position: 'relative' }">
        <button v-for="row in visibleRows" :key="row.id" type="button" class="btn playback-transcript__row" :data-cue-id="row.id"
          :aria-current="activeIDs.includes(row.id) ? 'true' : undefined" :disabled="seekPosition(row) === null"
          :style="{ top: `${rowTop(row)}px` }" @click="$emit('seek', seekPosition(row))">
          <time>{{ timeLabel(row.startMS) }}</time><span>{{ row.text }}</span>
        </button>
      </div>
    </div>
  </aside>
</template>
<script>
import AppIcon from '../ui/AppIcon.vue';
import { openModalLayer, closeModalLayer, isTopModalLayer } from '../ui/modalSignals.js';
import { VirtualHeightIndex } from '../../utils/virtualList.js';
import { activeTranscriptRows, subtitleSeekPosition } from '../../utils/playbackSubtitles.js';
export default {
  name: 'PlaybackTranscript', components: { AppIcon }, emits: ['close', 'seek'],
  props: { rows: { type: Array, default: () => [] }, timeMS: { type: Number, default: 0 }, range: { type: Object, default: null } },
  data: () => ({ following: true, scrollTop: 0, viewportHeight: 400, geometryVersion: 0 }),
  computed: {
    activeIDs() { return activeTranscriptRows(this.rows, this.timeMS); },
    totalHeight() { void this.geometryVersion; return this._geometry?.totalHeight || 0; },
    visibleRows() {
      void this.geometryVersion;
      if (!this._geometry) return [];
      const start = Math.max(0, this._geometry.rowAt(this.scrollTop) - 3);
      const end = Math.min(this.rows.length, this._geometry.rowAt(this.scrollTop + this.viewportHeight) + 4);
      return this.rows.slice(start, end);
    }
  },
  watch: {
    rows: { immediate: true, handler() { this._geometry = new VirtualHeightIndex(this.rows, () => 80); this.geometryVersion++; this.scrollTop = 0; this.following = true; this.$nextTick(this.follow); } },
    activeIDs() { this.follow(); }
  },
  beforeMount() { openModalLayer(this); },
  mounted() {
    window.addEventListener('keydown', this.closeOnEscape, true);
    if (typeof ResizeObserver !== 'undefined') {
      this._observer = new ResizeObserver(entries => {
        let changed = false;
        for (const entry of entries) {
          if (entry.target === this.$refs.list) { this.viewportHeight = entry.contentRect.height; continue; }
          const index = this._geometry.indexOf(entry.target.dataset.cueId);
          if (index >= 0) changed = this._geometry.setRowHeight(index, entry.target.getBoundingClientRect().height) || changed;
        }
        if (changed) this.geometryVersion++;
      });
      if (this.$refs.list) this._observer.observe(this.$refs.list);
    }
    this.observeRows(); this.follow();
  },
  updated() { this.observeRows(); },
  beforeUnmount() { this._observer?.disconnect(); window.removeEventListener('keydown', this.closeOnEscape, true); closeModalLayer(this); },
  methods: {
    closeOnEscape(event) {
      if (event.key !== 'Escape' || event.isComposing || !isTopModalLayer(this) || event.target?.closest?.('.base-popover')) return;
      event.preventDefault(); event.stopImmediatePropagation(); this.$emit('close');
    },
    observeRows() {
      this._observer?.disconnect();
      if (this.$refs.list) this._observer?.observe(this.$refs.list);
      for (const row of this.$el.querySelectorAll('[data-cue-id]')) this._observer?.observe(row);
    },
    seekPosition(row) { return subtitleSeekPosition(row, this.range); },
    rowTop(row) { void this.geometryVersion; return this._geometry.itemTop(this._geometry.indexOf(row.id)); },
    timeLabel(ms) { const seconds = Math.floor(ms / 1000); return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`; },
    scrolled() { this.scrollTop = this.$refs.list.scrollTop; },
    userScroll(event) { if (['ArrowUp', 'ArrowDown', 'PageUp', 'PageDown', 'Home', 'End', ' '].includes(event.key)) { this.following = false; event.stopPropagation(); } },
    resumeFollowing() { this.following = true; this.follow(); },
    follow() {
      if (!this.following || !this.activeIDs.length || !this.$refs.list || !this._geometry) return;
      const index = this._geometry.indexOf(this.activeIDs[0]);
      if (index < 0) return;
      const top = this._geometry.itemTop(index);
      if (top < this.scrollTop || top + this._geometry.heights[index] > this.scrollTop + this.viewportHeight) {
        this.$refs.list.scrollTop = Math.max(0, top - this.viewportHeight / 3);
        this.scrollTop = this.$refs.list.scrollTop;
      }
    }
  }
};
</script>
<style scoped>
.playback-transcript { position: absolute; inset: var(--space-2) var(--space-2) 100px auto; width: min(360px, 60%); display: flex; flex-direction: column; min-height: 0; z-index: 2; background: var(--panel-bg); color: var(--text-primary); border: 1px solid var(--hairline); border-radius: var(--radius); box-shadow: var(--shadow-popover); }
.playback-transcript__header { display: flex; align-items: center; gap: var(--space-2); padding: var(--space-2); border-bottom: 1px solid var(--hairline); font-size: var(--fs-body); }
.playback-transcript__header strong { margin-right: auto; }
.playback-transcript__list { flex: 1; min-height: 0; overflow: auto; overflow-anchor: none; }
.playback-transcript__row { position: absolute; left: 0; display: flex; justify-content: flex-start; align-items: flex-start; gap: var(--space-2); width: 100%; height: auto; min-height: var(--h-touch); box-sizing: border-box; padding: var(--space-3); text-align: left; border: 0; border-radius: 0; font-size: var(--fs-body); line-height: 1.5; }
.playback-transcript__row time { flex: none; color: var(--text-muted); font-size: var(--fs-caption); font-variant-numeric: tabular-nums; }
.playback-transcript__row span { min-width: 0; white-space: pre-wrap; overflow-wrap: anywhere; }
.playback-transcript__row[aria-current=true] { background: var(--accent-soft); color: var(--accent-color); }
.playback-transcript__empty { padding: var(--space-3); font-size: var(--fs-body); }
</style>
