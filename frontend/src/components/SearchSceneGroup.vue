<template>
  <section class="search-scene-group">
    <h3>{{ kind === 'dialogue' ? '对白' : kind === 'image' ? '以图搜片' : '画面' }}</h3>
    <p v-if="coverage" class="help-text">{{ coverageText }}</p>
    <p v-if="kind !== 'dialogue' && loaded && !skipped" class="help-text">最相关的前 {{ resultCount }} 段{{ truncated ? '；还有未列出的结果，请缩小范围' : '' }}。不代表全库命中总数。</p>
    <p v-if="kind === 'dialogue'" class="help-text">每部影片独立加载命中；翻页期间片库可能变化，刷新会重新计算。</p>
    <p v-for="notice in notices" :key="notice" class="help-text">{{ noticeText(notice) }}</p>
    <p v-if="error" role="alert">{{ error }} <button type="button" class="btn btn--secondary btn--sm" @click="load(false)">重试本组</button></p>
    <p v-if="loading" role="status">正在检索…</p><p v-else-if="loaded && !groups.length && !error" class="help-text">{{ skipped ? '检索未执行，请先处理上方提示。' : '已索引范围内没有命中。' }}</p>
    <SearchHitGroups :groups="groups" :busy="busy" @preview="$emit('preview',$event)" @bookmark="$emit('bookmark',$event)" @watch-whole="$emit('watch-whole',$event)" @more-hits="moreHits" />
    <button v-if="nextCursor" type="button" class="btn btn--secondary" :disabled="loading || busy" @click="load(true)">更多命中影片</button>
  </section>
</template>
<script>
import SearchHitGroups from './SearchHitGroups.vue';
import { searchResultsAPI, uniqueSearchHits, rankedSearchGroups } from '../utils/searchResults.js';
import { sceneCoverageParts, sceneNoticeText, sceneVisualSkipped } from '../utils/sceneSearch.js';
export default {
  name: 'SearchSceneGroup', components: { SearchHitGroups },
  props: { request: { type: Object, default: null }, kind: { type: String, default: 'dialogue' }, pageActive: { type: Boolean, default: true }, busy: Boolean, api: { type: Object, default: () => searchResultsAPI } },
  emits: ['preview','bookmark','watch-whole'],
  data: () => ({ groups: [], nextCursor: '', coverage: null, notices: [], error: '', loading: false, loaded: false, generation: 0, alive: true, resultCount: 0, truncated: false }),
  computed: { skipped() { return this.kind !== 'dialogue' && sceneVisualSkipped(this.notices); }, coverageText() { return sceneCoverageParts(this.coverage).join(' · '); } },
  watch: { request: { immediate: true, handler() { this.reset(); } }, kind() { this.reset(); } },
  beforeUnmount() { this.alive = false; this.generation++; },
  methods: {
    noticeText: sceneNoticeText,
    reset() { this.generation++; this.groups = []; this.nextCursor = ''; this.coverage = null; this.notices = []; this.error = ''; this.loaded = false; this.loading = false; if (this.request) this.load(false); },
    async load(more) {
      if (this.loading || !this.request) return;
      const ticket = this.generation, request = { ...this.request }; this.loading = true; this.error = '';
      try {
        const result = this.kind === 'dialogue'
          ? await this.api.dialogue({ query: request.query, filter: request.filter, cursor: more ? this.nextCursor : '', limit: 20 })
          : this.kind === 'image'
            ? await this.api.image({ image_base64: request.image?.base64 || request.image?.dataURL?.split(',')[1] || '', video_id: request.similar?.videoID || 0, at_ms: request.similar?.atMS || 0, filter: request.filter, limit: 200 })
            : await this.api.visual({ query: request.query, filter: request.filter, mode: 'visual', limit: 200 });
        if (!this.alive || ticket !== this.generation) return;
        if (this.kind === 'dialogue') {
          const seen = new Set(more ? this.groups.map(group => group.video_id) : []);
          this.groups = [...(more ? this.groups : []), ...result.items.filter(group => !seen.has(group.video_id))]; this.nextCursor = result.next_cursor || '';
        } else { this.groups = rankedSearchGroups(result.items); this.resultCount = result.items.length; this.truncated = result.truncated; }
        this.coverage = result.coverage; this.notices = result.notices || []; this.loaded = true;
      } catch (error) { if (this.alive && ticket === this.generation) this.error = error.message || String(error); }
      finally { if (this.alive && ticket === this.generation) this.loading = false; }
    },
    async moreHits(group) {
      if (group.loading || !group.next_cursor) return;
      const ticket = this.generation; group.loading = true; group.error = '';
      try {
        const result = await this.api.hits({ query: this.request.query, filter: this.request.filter, video_id: group.video_id, cursor: group.next_cursor, limit: 30 });
        if (!this.alive || ticket !== this.generation) return;
        group.hits = uniqueSearchHits(group.hits, result.items); group.next_cursor = result.next_cursor || ''; group.hit_count = result.hit_count;
      } catch (error) { if (this.alive && ticket === this.generation) group.error = error.message || String(error); }
      finally { if (this.alive && ticket === this.generation) group.loading = false; }
    }
  }
};
</script>
