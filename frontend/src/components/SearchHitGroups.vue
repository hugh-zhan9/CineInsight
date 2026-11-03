<template>
  <div class="search-hit-groups">
    <section v-for="group in groups" :key="group.video_id" class="search-hit-group">
      <div class="search-hit-group__heading">
        <button type="button" class="btn btn--ghost" :aria-expanded="!collapsed[group.video_id]" @click="collapsed[group.video_id] = !collapsed[group.video_id]">{{ group.title }} · {{ group.hit_count == null ? `已列出 ${group.hits.length} 处` : `${group.hit_count} 处命中` }}</button>
        <button type="button" class="btn btn--secondary btn--sm" :disabled="busy || !group.hits.length || group.hits.length > 1000" @click="$emit('preview', [...group.hits])">连续预览已加载的 {{ group.hits.length }} 段</button>
        <button type="button" class="btn btn--ghost btn--sm" :disabled="busy" @click="$emit('watch-whole', group.video_id)">观看整片</button>
      </div>
      <div v-show="!collapsed[group.video_id]">
        <p v-if="group.hits.length > 1000" class="help-text">连续预览一次最多 1000 段；当前可逐条预览，或缩小检索范围。</p>
        <article v-for="hit in group.hits" :key="hitKey(hit)" class="search-hit">
          <img :src="frame(hit.video_id, hit.start_ms)" alt="" loading="lazy" />
          <div><p>{{ hit.text || '相似画面' }}</p><small>{{ range(hit.start_ms, hit.end_ms) }} · {{ hit.source === 'dialogue' ? '对白' : '画面' }}</small><p v-if="!hit.source_version" class="help-text">原片不可用，请刷新并核对。</p>
            <div class="search-hit__actions"><button type="button" class="btn btn--secondary btn--sm" :disabled="busy || !hit.source_version" @click="$emit('preview', [hit])">预览</button><button type="button" class="btn btn--ghost btn--sm" :disabled="busy || !hit.source_version" @click="$emit('bookmark', hit)">保存到专题…</button><slot name="actions" :hit="hit" /></div>
          </div>
        </article>
        <p v-if="group.error" role="alert">{{ group.error }}</p>
        <button v-if="group.next_cursor" type="button" class="btn btn--secondary" :disabled="group.loading || busy" @click="$emit('more-hits', group)">{{ group.loading ? '正在加载…' : '更多本片命中' }}</button>
      </div>
    </section>
  </div>
</template>
<script>
import { searchResultKey } from '../utils/searchResults.js';
import { sceneFrameURL, formatSceneRange } from '../utils/sceneSearch.js';

export default {
  name: 'SearchHitGroups', props: { groups: { type: Array, default: () => [] }, busy: Boolean },
  emits: ['preview', 'bookmark', 'watch-whole', 'more-hits'], data: () => ({ collapsed: {} }),
  methods: { frame: sceneFrameURL, range: formatSceneRange, hitKey: searchResultKey }
};
</script>
<style scoped>
.search-hit-group { border-bottom: 1px solid var(--hairline); padding-block: var(--space-4); }
.search-hit-group__heading, .search-hit__actions { display: flex; flex-wrap: wrap; gap: var(--space-2); align-items: center; }
.search-hit { display: flex; gap: var(--space-3); margin-block: var(--space-3); }
.search-hit img { width: 120px; height: 68px; object-fit: cover; background: var(--control-bg); border-radius: var(--radius); }
.search-hit > div { min-width: 0; overflow-wrap: anywhere; }
.search-hit p { margin: 0 0 var(--space-2); }
.search-hit small { color: var(--text-secondary); }
</style>
