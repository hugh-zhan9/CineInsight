<template>
  <section class="bt-search" aria-label="查找资源">
    <form class="bt-search-form" @submit.prevent="search"><input v-model="query" class="text-input" aria-label="片名或关键词" placeholder="片名或关键词" maxlength="200" data-test="bt-query" /><button type="submit" class="btn btn--primary" :disabled="busy || !query.trim()" data-test="bt-search-submit">{{ busy ? '正在搜索…' : '找资源' }}</button><button v-if="busy" type="button" class="btn btn--secondary" @click="$emit('cancel')">取消</button></form>
    <div class="bt-sites" aria-label="搜索站点"><label v-for="site in indexers.filter(s => s.enabled !== false)" :key="site.id"><input v-model="selected" type="checkbox" :value="site.id" :disabled="busy" />{{ site.name }}</label><span v-if="!indexers.length" class="help-text">请先连接 Prowlarr 并配置站点。</span><span v-else class="help-text">不勾选时搜索全部启用站点。</span></div>
    <p v-if="error" role="alert" class="bt-search-error">{{ error }}</p>
    <template v-if="result && currentResult">
      <ul v-if="result.indexers?.length" class="bt-site-status" aria-label="站点搜索结果"><li v-for="site in result.indexers" :key="site.indexer_id">{{ siteName(site.indexer_id) }}：{{ site.status === 'ok' ? (site.coverage === 'unknown' ? '已响应，结果可能不完整' : '搜索完成') : ({ failed: '搜索失败', timeout: '搜索超时', not_queried: '本轮未查询' }[site.status] || '状态待核实') }}</li></ul>
      <p v-if="result.truncated" class="help-text">结果较多，仅显示本次返回的部分资源，请缩小关键词范围。</p>
      <p v-if="!result.resources?.length" class="help-text" data-test="bt-no-results">本轮未返回资源，请调整关键词或检查站点状态。</p>
      <ul class="bt-resources"><li v-for="resource in result.resources || []" :key="resource.result_token" class="bt-resource"><div><strong>{{ resource.title }}</strong><p class="help-text">{{ resource.indexer }} · {{ bytes(resource.size) }} · 做种 {{ resource.seeders ?? '未知' }}<span v-if="resource.publish_time"> · {{ date(resource.publish_time) }}</span></p></div><button type="button" class="btn btn--secondary btn--sm" :disabled="busy || previewBusy || !resource.result_token" :data-test="`bt-preview-${resource.result_token}`" @click="$emit('preview', resource.result_token)">选择文件…</button></li></ul>
    </template>
    <p v-else-if="result" class="help-text" data-test="bt-stale-results">关键词或站点已变化，请重新搜索。</p>
  </section>
</template>
<script>
import { btByteText } from '../../utils/btFormat.js';
export default {
  name: 'BTResourceSearch',
  props: { initialQuery: { type: String, default: '' }, indexers: { type: Array, default: () => [] }, result: { type: Object, default: null }, resultQuery: { type: String, default: '' }, resultIndexerIds: { type: Array, default: () => [] }, busy: Boolean, previewBusy: Boolean, error: { type: String, default: '' } },
  emits: ['search', 'preview', 'cancel'],
  data() { return { query: this.initialQuery, selected: [] }; },
  computed: { currentResult() { return this.query.trim() === this.resultQuery && [...this.selected].map(String).sort().join(',') === [...this.resultIndexerIds].map(String).sort().join(','); } },
  watch: { initialQuery(value) { this.query = value; } },
  methods: { bytes: btByteText, date(value) { const d = new Date(value); return Number.isNaN(d.getTime()) ? '' : d.toLocaleDateString('zh-CN'); }, siteName(id) { return this.indexers.find(s => s.id === id)?.name || `站点 ${id}`; }, search() { if (!this.busy && this.query.trim()) this.$emit('search', { query: this.query.trim(), indexer_ids: [...this.selected] }); } }
};
</script>
<style scoped>
.bt-search { display: grid; gap: var(--space-3); min-width: 0; }
.bt-search-form, .bt-sites { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-2); }
.bt-search-form input { flex: 1; min-width: 160px; }
.bt-sites { font-size: var(--fs-secondary); }
.bt-search-error { color: var(--danger-text); }
.bt-site-status { padding-left: var(--space-4); margin: 0; color: var(--text-secondary); font-size: var(--fs-caption); }
.bt-resources { list-style: none; padding: 0; margin: 0; display: grid; gap: var(--space-2); }
.bt-resource { display: flex; flex-wrap: wrap; justify-content: space-between; gap: var(--space-2); padding: var(--space-3); border: 1px solid var(--hairline); border-radius: var(--radius); }
.bt-resource > div { min-width: 0; flex: 1; overflow-wrap: anywhere; }
.bt-resource p { margin: var(--space-1) 0 0; }
</style>
