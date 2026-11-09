<template>
  <button type="button" :tabindex="tabStop ? 0 : -1" class="btn-plain media-tile" :class="{ 'is-selected': selected }" :data-entry-id="entry.entry_id" :aria-label="label" :aria-pressed="selected" @click="$emit('open', entry)">
    <img v-if="objectURL" :key="objectURL" :src="objectURL" alt="" draggable="false" @load="loaded" @error="failed" />
    <span v-if="!objectURL && !error && !card?.unavailable_reason" class="media-tile__loading" aria-hidden="true"></span>
    <span v-if="error || card?.unavailable_reason" class="media-tile__error" aria-label="暂不可用">—</span>
    <span v-if="showTitle" class="media-tile__title">{{ card?.title || '正在读取…' }}<span v-if="entry.kind === 'video'"> · 视频</span></span>
  </button>
</template>

<script>
export default {
  name: 'MediaTile',
  props: { entry: { type: Object, required: true }, card: { type: Object, default: null }, loader: { type: Object, required: true }, edge: { type: Number, default: 96 }, priority: { type: Number, default: 1 }, selected: Boolean, showTitle: Boolean, tabStop: Boolean, retrySignal: { type: Number, default: 0 } },
  emits: ['open', 'loaded', 'failed'],
  data: () => ({ objectURL: '', error: false, generation: 0 }),
  computed: { requestKey() { return `${this.card?.thumbnail_url || ''}|${this.card?.unavailable_reason || ''}|${this.edge}`; }, label() { return `${this.entry.kind === 'video' ? '视频' : '图片'}：${this.card?.title || '正在读取'}${this.card?.unavailable_reason ? `，${this.card.unavailable_reason}` : ''}`; } },
  watch: { priority(value) { this._request?.setPriority?.(value); }, retrySignal() { if (this.error) this.load(); }, requestKey: { immediate: true, handler() { this.load(); } } },
  beforeUnmount() { this.generation++; this._request?.cancel(); if (this.objectURL) URL.revokeObjectURL(this.objectURL); this.$emit('loaded', this.entry.entry_id, false); },
  methods: {
    async load() {
      const ticket = ++this.generation;
      this._request?.cancel(); this.error = false; this.$emit('failed', this.entry.entry_id, false); this.$emit('loaded', this.entry.entry_id, false);
      if (!this.card?.thumbnail_url || this.card.unavailable_reason) { if (this.objectURL) URL.revokeObjectURL(this.objectURL); this.objectURL = ''; return; }
      const url = `${this.card.thumbnail_url}&edge=${this.edge}`;
      this._request = this.loader.load(url, this.priority);
      try {
        const blob = await this._request.promise;
        if (ticket !== this.generation) return;
        const previous = this.objectURL;
        this.objectURL = URL.createObjectURL(blob);
        if (previous) URL.revokeObjectURL(previous);
      } catch (error) { if (ticket === this.generation && error.name !== 'AbortError') this.failed(); }
    },
    loaded(event) { if (event.target.src === this.objectURL) this.$emit('loaded', this.entry.entry_id, true); },
    failed() { this.error = true; this.$emit('failed', this.entry.entry_id, true); if (this.objectURL) URL.revokeObjectURL(this.objectURL); this.objectURL = ''; this.$emit('loaded', this.entry.entry_id, false); }
  }
};
</script>

<style scoped>
.media-tile { display: block; position: relative; margin: 0; padding: 0; border: 0; border-radius: 0; overflow: hidden; background: var(--player-bg); color: var(--cinema-text); cursor: pointer; }
.media-tile img { width: 100%; height: 100%; display: block; object-fit: cover; pointer-events: none; }
.media-tile.is-selected { outline: 3px solid var(--accent-color); outline-offset: -3px; z-index: 1; }
.media-tile__loading { display: block; width: 100%; height: 100%; background: var(--panel-muted-bg); }
.media-tile__error { position: absolute; inset: 0; display: grid; place-items: center; color: var(--text-secondary); }
.media-tile__title { position: absolute; bottom: 0; inset-inline: 0; padding: 8px; background: var(--overlay-strong); font-size: var(--fs-secondary); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; text-align: left; }
</style>
