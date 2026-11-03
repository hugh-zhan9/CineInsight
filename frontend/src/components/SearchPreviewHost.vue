<template>
  <p v-if="error" role="alert">{{ error }}</p><p v-if="notice" role="status">{{ notice }}</p>
  <ClipQueuePreviewDialog v-if="preview && pageActive" :preview="preview" :api="api" @close="preview = null" @queued="queued" />
  <BookmarkPanel ref="editor" editor-only require-topic :page-active="pageActive" :clip-api="api" @saved="notice = '片段已保存到专题'" />
</template>
<script>
import BookmarkPanel from './BookmarkPanel.vue';
import ClipQueuePreviewDialog from './ClipQueuePreviewDialog.vue';
import { searchResultsAPI } from '../utils/searchResults.js';
export default {
  name: 'SearchPreviewHost', components: { BookmarkPanel, ClipQueuePreviewDialog },
  props: { api: { type: Object, default: () => searchResultsAPI }, pageActive: { type: Boolean, default: true }, contextKey: { default: 0 }, watchWholeVideo: { type: Function, default: null } },
  data: () => ({ preview: null, error: '', notice: '', busy: false, generation: 0, alive: true }),
  watch: { contextKey() { this.generation++; this.preview = null; }, pageActive(active) { if (!active) { this.generation++; this.preview = null; } } },
  beforeUnmount() { this.alive = false; this.generation++; },
  methods: {
    async prepare(hits) {
      if (!this.pageActive || this.busy || this.preview || !hits.length || hits.length > 1000) return;
      const ticket = ++this.generation; this.busy = true; this.error = ''; this.notice = '';
      const items = hits.map(hit => ({ video_id: hit.video_id, title: hit.title, start_ms: hit.preview_start_ms, end_ms: hit.preview_end_ms, source_version: hit.source_version }));
      try {
        const queue = await this.api.queue(); if (!this.alive || ticket !== this.generation) return;
        const result = await this.api.preview({ items, queue_revision: queue.state.revision });
        if (this.alive && ticket === this.generation) this.preview = result;
        else if (result.preview_token) this.api.discard(result.preview_token).catch(() => {});
      } catch (error) { if (this.alive && ticket === this.generation) this.error = error.message || String(error); }
      finally { if (this.alive) this.busy = false; }
    },
    bookmark(hit) {
      if (!this.pageActive || this.busy || !hit.source_version) return;
      this.$refs.editor.edit({ video_id: hit.video_id, title: hit.title || '场景片段', note: hit.text || '', start_ms: hit.start_ms, end_ms: hit.end_ms, source_token: hit.source_version, tags: [] });
    },
    async watchWhole(id) {
      if (!this.pageActive || this.busy || !this.watchWholeVideo) return;
      this.busy = true; this.error = ''; const ticket = this.generation;
      try {
        await this.watchWholeVideo(id, async () => {
          const queue = await this.api.queue(); if (!this.alive || ticket !== this.generation) return false;
          if (queue.state.active_token) await this.api.stop(queue.state.active_token);
          const after = await this.api.queue();
          if (!this.alive || ticket !== this.generation) return false;
          if (after.state.active_token) throw new Error('队列已切换，请重新选择整片播放');
          return true;
        });
      } catch (error) { if (this.alive && ticket === this.generation) this.error = error.message || String(error); }
      finally { if (this.alive) this.busy = false; }
    },
    queued(result) { this.preview = null; this.notice = result.warning || `已加入 ${result.entry_ids.length} 段，可在主窗口底部控制预览`; }
  }
};
</script>
