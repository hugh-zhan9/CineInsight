<template>
  <section class="page-content search-results-page" data-test="search-results-page" @dragover.prevent @drop.prevent="drop">
    <PageHeader title="搜索汇总" subtitle="视频、人物、作品集、对白与画面分别检索；片段预览不进入观看记录。" />
    <form class="search-results-toolbar" @submit.prevent="search"><input v-model="query" type="search" class="text-input" maxlength="200" aria-label="汇总搜索" placeholder="片名、人物、台词或画面描述" /><button type="submit" class="btn btn--primary" :disabled="!query.trim() && !image">检索</button><button type="button" class="btn btn--secondary" @click="$refs.file.click()">以图搜片</button><input ref="file" type="file" accept="image/*" hidden @change="pick" /><button v-if="image" type="button" class="btn btn--ghost" @click="image = null">清除图片</button></form>
    <img v-if="image" :src="image.dataURL" class="search-results-image" alt="查询图片" />
    <p v-if="imageError" role="alert">{{ imageError }}</p>
    <p v-if="filter" class="help-text">视频、对白与画面限于带入的片库筛选；人物与作品集按名称检索。</p>
    <p v-if="stale" role="status">片库已有更新，当前结果保持原位。<button type="button" class="btn btn--secondary btn--sm" @click="search">刷新结果</button></p>
    <template v-if="request">
      <section v-for="key in request.query ? ['video','person','collection'] : []" :key="key">
        <h3>{{ labels[key] }}</h3><p v-if="states[key].loading" role="status">正在检索…</p>
        <p v-if="states[key].error" role="alert">{{ states[key].error }} <button type="button" class="btn btn--secondary btn--sm" @click="load(key, false)">重试本组</button></p>
        <p v-if="!states[key].loading && !states[key].error && !states[key].items.length" class="help-text">没有匹配的{{ labels[key] }}。</p>
        <div class="search-results-items"><button v-for="item in states[key].items" :key="id(key,item)" type="button" class="btn btn--secondary" @click="openItem(key,item)">{{ title(key,item) }}</button></div>
        <button v-if="states[key].cursor" type="button" class="btn btn--secondary" :disabled="states[key].loading" @click="load(key,true)">更多{{ labels[key] }}</button>
      </section>
      <SearchSceneGroup v-for="kind in sceneKinds" :key="kind" :kind="kind" :request="request" :api="api" :page-active="pageActive" :busy="$refs.previewHost?.busy" @preview="$refs.previewHost.prepare($event)" @bookmark="$refs.previewHost.bookmark($event)" @watch-whole="$refs.previewHost.watchWhole($event)" />
    </template>
    <SearchPreviewHost ref="previewHost" :api="api" :context-key="generation" :page-active="pageActive" :watch-whole-video="watchWholeVideo" />
  </section>
</template>
<script>
import PageHeader from './ui/PageHeader.vue';
import SearchSceneGroup from './SearchSceneGroup.vue';
import SearchPreviewHost from './SearchPreviewHost.vue';
import { searchResultsAPI } from '../utils/searchResults.js';
import { prepareSearchImage, firstImageFile } from '../utils/unifiedSearch.js';
const fresh = () => Object.fromEntries(['video','person','collection'].map(key => [key, { items: [], cursor: null, error: '', loading: false, sequence: 0 }]));
export default {
  name: 'SearchResultsPage', components: { PageHeader, SearchSceneGroup, SearchPreviewHost },
  props: { pageActive: { type: Boolean, default: true }, initialRequest: { type: Object, default: null }, api: { type: Object, default: () => searchResultsAPI }, watchWholeVideo: { type: Function, default: null } },
  emits: ['open-video','open-entity'],
  data: () => ({ query: '', filter: null, image: null, imageError: '', request: null, states: fresh(), labels: { video: '视频', person: '人物', collection: '作品集' }, generation: 0, imageGeneration: 0, alive: true, stale: false }),
  computed: { sceneKinds() { return [...(this.request?.query ? ['dialogue','visual'] : []), ...(this.request?.image ? ['image'] : [])]; } },
  watch: { pageActive(active) { if (!active) this.imageGeneration++; }, initialRequest: { immediate: true, handler(value) { if (!value) return; this.imageGeneration++; this.query = value.query || ''; this.filter = value.filter || null; this.image = value.image || null; if (this.query.trim() || this.image) this.search(); } } },
  mounted() { window.addEventListener('paste',this.paste); this._offs = ['library-watcher-reconciled','library-scan-summary','subtitle-index-synced','scene-index-state'].map(name => window.runtime?.EventsOn?.(name, () => { if (this.request) this.stale = true; })).filter(off => typeof off === 'function'); },
  beforeUnmount() { this.alive = false; this.generation++; this.imageGeneration++; window.removeEventListener('paste',this.paste); this._offs.forEach(off => off()); },
  methods: {
    search() { const query = this.query.trim(); if (!query && !this.image) return; this.generation++; this.stale = false; this.states = fresh(); this.request = { query, filter: this.filter, image: this.image }; if (query) for (const key of ['video','person','collection']) this.load(key,false); },
    id(key,item) { return key === 'video' ? item.id : item[key].id; },
    title(key,item) { return key === 'video' ? item.display_title || item.name : key === 'person' ? item.person.display_name || item.person.original_name : item.collection.name; },
    openItem(key,item) { if (!this.pageActive) return; if (key === 'video') this.$emit('open-video',item); else this.$emit('open-entity',{ type: key, id: this.id(key,item), name: this.title(key,item) }); },
    async load(key,more) {
      const state = this.states[key]; if (state.loading || !this.request) return;
      const ticket = this.generation, sequence = ++state.sequence; state.loading = true; state.error = '';
      try {
        const result = key === 'video' ? await this.api.video({ filter: { ...this.request.filter, search_mode: 'file', keyword: this.request.query, collapse_versions: true }, cursor: more ? state.cursor : null, limit: 20 }) : await this.api[key](this.request.query, more ? state.cursor : null, 20);
        if (!this.alive || ticket !== this.generation || sequence !== state.sequence) return;
        const incoming = key === 'video' ? result.videos : result;
        const seen = new Set((more ? state.items : []).map(item => this.id(key,item)));
        state.items = [...(more ? state.items : []), ...incoming.filter(item => !seen.has(this.id(key,item)))];
        if (key === 'video') state.cursor = result.next_cursor || null;
        else { const last = incoming.at(-1); state.cursor = incoming.length === 20 ? { id: this.id(key,last), name: last.cursor_name } : null; }
      } catch (error) { if (this.alive && ticket === this.generation && sequence === state.sequence) state.error = error.message || String(error); }
      finally { if (this.alive && ticket === this.generation && sequence === state.sequence) state.loading = false; }
    },
    async useImage(file) { const ticket = ++this.imageGeneration; this.imageError = ''; try { const image = await prepareSearchImage(file); if (!this.alive || !this.pageActive || ticket !== this.imageGeneration) return; this.image = image; this.search(); } catch (error) { if (this.alive && ticket === this.imageGeneration) this.imageError = error.message; } },
    pick(event) { const file = event.target.files?.[0]; event.target.value = ''; if (file) this.useImage(file); },
    drop(event) { const file = firstImageFile(event.dataTransfer); if (file && this.pageActive) this.useImage(file); },
    paste(event) { if (!this.pageActive || ['INPUT','TEXTAREA'].includes(event.target?.tagName) || this.$refs.previewHost?.preview || this.$refs.previewHost?.$refs.editor?.draft) return; const file = firstImageFile(event.clipboardData); if (file) { event.preventDefault(); this.useImage(file); } }
  }
};
</script>
<style scoped>
.search-results-page { max-width: 1100px; margin: 0 auto; }
.search-results-toolbar, .search-results-items { display: flex; flex-wrap: wrap; gap: var(--space-2); margin-block: var(--space-3); }
.search-results-toolbar .text-input { flex: 1; min-width: 200px; }
.search-results-image { max-width: 160px; max-height: 100px; object-fit: contain; }
</style>
