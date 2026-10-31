<template>
  <section class="bt-search" aria-label="查找资源">
    <div v-show="!selectedResource" class="bt-search-list">
      <form class="bt-search-form" @submit.prevent="search"><input v-model="query" class="text-input" aria-label="片名或关键词" placeholder="片名或关键词" maxlength="200" data-test="bt-query" /><button type="submit" class="btn btn--primary" :disabled="busy" data-test="bt-search-submit">{{ busy ? (query.trim() ? '正在搜索…' : '正在加载…') : (query.trim() ? '找资源' : '刷新最新资源') }}</button><button v-if="query.trim()" type="button" class="btn btn--secondary" :disabled="busy" data-test="bt-show-latest" @click="latest">最新资源</button><button v-if="busy" type="button" class="btn btn--secondary" @click="$emit('cancel')">取消</button></form>
      <div class="bt-sites" aria-label="搜索站点"><label v-for="site in indexers.filter(s => s.enabled !== false)" :key="site.id"><input v-model="selected" type="checkbox" :value="site.id" :disabled="busy" @change="sitesChanged" />{{ site.name }}</label><span v-if="!indexers.length" class="help-text">请先连接 Prowlarr 并配置站点。</span><span v-else class="help-text">不勾选时搜索全部启用站点。</span></div>
      <p v-if="error" role="alert" class="bt-search-error">{{ error }}</p>
      <template v-if="result && currentResult">
        <h3>{{ resultQuery ? '搜索结果' : '最新资源' }}</h3>
        <ul v-if="result.indexers?.length" class="bt-site-status" aria-label="站点搜索结果"><li v-for="site in result.indexers" :key="site.indexer_id">{{ siteName(site.indexer_id) }}：{{ site.status === 'ok' ? (site.coverage === 'unknown' ? '已响应，结果可能不完整' : '搜索完成') : ({ failed: '搜索失败', timeout: '搜索超时', not_queried: '本轮未查询' }[site.status] || '状态待核实') }}</li></ul>
        <p v-if="result.truncated" class="help-text">结果较多，按发布时间显示本次返回的前一部分资源，可输入关键词缩小范围。</p>
        <p v-if="!result.resources?.length" class="help-text" data-test="bt-no-results">本轮未返回资源，请调整关键词或检查站点状态。</p>
        <ul class="bt-resources"><li v-for="resource in result.resources || []" :key="resource.result_token" class="bt-resource"><button type="button" class="bt-resource-poster-link" :aria-label="`查看资源详情：${resource.title}`" :data-test="`bt-resource-poster-${resource.result_token}`" @click="openDetail(resource, $event)"><BTResourcePoster :path="resource.poster_path || ''" /></button><div class="bt-resource-info"><button type="button" class="bt-resource-title" :data-test="`bt-resource-title-${resource.result_token}`" @click="openDetail(resource, $event)">{{ resource.title }}</button><p class="help-text">{{ resource.indexer }} · {{ bytes(resource.size) }} · 做种 {{ resource.seeders ?? '未知' }}<span v-if="resource.publish_time"> · {{ date(resource.publish_time) }}</span></p></div><button type="button" class="btn btn--secondary btn--sm" :disabled="busy || previewBusy || !resource.result_token" :data-test="`bt-preview-${resource.result_token}`" @click="$emit('preview', resource.result_token)">选择文件…</button></li></ul>
      </template>
      <p v-else-if="result" class="help-text" data-test="bt-stale-results">关键词或站点已变化，请重新搜索。</p>
    </div>
    <BTResourceDetail v-if="selectedResource" :resource="selectedResource" :busy="busy || previewBusy" :error="error" @back="closeDetail" @preview="$emit('preview', $event)" />
  </section>
</template>
<script>
import BTResourceDetail from './BTResourceDetail.vue';
import BTResourcePoster from './BTResourcePoster.vue';
import { btByteText } from '../../utils/btFormat.js';
export default {
  name: 'BTResourceSearch', components: { BTResourcePoster, BTResourceDetail },
  props: { initialQuery: { type: String, default: '' }, indexers: { type: Array, default: () => [] }, result: { type: Object, default: null }, resultQuery: { type: String, default: '' }, resultIndexerIds: { type: Array, default: () => [] }, active: { type: Boolean, default: true }, busy: Boolean, previewBusy: Boolean, error: { type: String, default: '' } },
  emits: ['search', 'preview', 'cancel'],
  data() { return { query: this.initialQuery, selected: [], selectedResource: null }; },
  computed: { currentResult() { return this.query.trim() === this.resultQuery && [...this.selected].map(String).sort().join(',') === [...this.resultIndexerIds].map(String).sort().join(','); } },
  watch: {
    initialQuery(value) { this.query = value; },
    result() { if (this.selectedResource) this.closeDetail(); },
    currentResult(value) { if (!value && this.selectedResource) this.closeDetail(); }
  },
  beforeUnmount() { this._disposed = true; },
  methods: {
    async openDetail(resource, event) {
      if (!this.active || !this.currentResult) return;
      this._returnTarget = event.currentTarget;
      this._scrollOwner = this.$el.closest('.main-view');
      this._listScrollTop = this._scrollOwner?.scrollTop || 0;
      this.selectedResource = resource;
      await this.$nextTick();
      if (!this._disposed && this.active && this.selectedResource && this._scrollOwner) this._scrollOwner.scrollTop = 0;
    },
    async closeDetail() {
      this.selectedResource = null;
      await this.$nextTick();
      if (this._disposed || !this.active) return;
      this._returnTarget?.focus({ preventScroll: true });
      if (this._scrollOwner) this._scrollOwner.scrollTop = this._listScrollTop;
      this._returnTarget = null;
      this._scrollOwner = null;
    },
    bytes: btByteText, date(value) { const d = new Date(value); return Number.isNaN(d.getTime()) ? '' : d.toLocaleDateString('zh-CN'); }, siteName(id) { return this.indexers.find(s => s.id === id)?.name || `站点 ${id}`; }, latest() { if (this.busy) return; this.query = ''; this.search(); }, sitesChanged() { if (!this.query.trim()) this.search(); }, search() { if (!this.busy) this.$emit('search', { query: this.query.trim(), indexer_ids: [...this.selected] }); } }
};
</script>
<style scoped>
.bt-search, .bt-search-list { display: grid; gap: var(--space-3); min-width: 0; }
.bt-search-form, .bt-sites { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-2); }
.bt-search-form input { flex: 1; min-width: 160px; }
.bt-sites { font-size: var(--fs-secondary); }
.bt-search-error { color: var(--danger-text); }
.bt-site-status { padding-left: var(--space-4); margin: 0; color: var(--text-secondary); font-size: var(--fs-caption); }
.bt-resources { list-style: none; padding: 0; margin: 0; display: grid; gap: var(--space-2); }
.bt-resource { display: flex; align-items: center; flex-wrap: wrap; justify-content: space-between; gap: var(--space-2); padding: var(--space-3); border: 1px solid var(--hairline); border-radius: var(--radius); }
.bt-resource-poster-link, .bt-resource-title { border: 0; padding: 0; background: transparent; color: inherit; cursor: pointer; }
.bt-resource-title { font: inherit; font-weight: 600; text-align: left; overflow-wrap: anywhere; }
.bt-resource-title:hover { text-decoration: underline; }
.bt-resource-poster-link:focus-visible, .bt-resource-title:focus-visible { outline: 2px solid var(--accent-color); outline-offset: 3px; }
.bt-resource-info { min-width: 0; flex: 1; overflow-wrap: anywhere; }
.bt-resource p { margin: var(--space-1) 0 0; }
</style>
