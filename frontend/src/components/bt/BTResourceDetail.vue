<template>
  <section class="bt-resource-detail" aria-label="资源详情" data-test="bt-resource-detail">
    <button type="button" class="btn btn--secondary" data-test="bt-resource-back" @click="$emit('back')">← 返回资源列表</button>
    <div class="bt-resource-detail__hero">
      <BTResourcePoster class="bt-resource-detail__poster" :path="resource.poster_path || ''" />
      <div class="bt-resource-detail__body">
        <p class="help-text">资源详情</p>
        <h3 ref="heading" tabindex="-1">{{ resource.title || '未命名资源' }}</h3>
        <dl>
          <div><dt>来源站点</dt><dd>{{ resource.indexer || '未知' }}</dd></div>
          <div><dt>资源大小</dt><dd>{{ bytes(resource.size) }}</dd></div>
          <div><dt>做种人数</dt><dd data-test="bt-resource-seeders">{{ resource.seeders ?? '未知' }}</dd></div>
          <div><dt>发布时间</dt><dd data-test="bt-resource-published">{{ published }}</dd></div>
        </dl>
        <button type="button" class="btn btn--primary" :disabled="busy || !resource.result_token" data-test="bt-resource-download" @click="$emit('preview', resource.result_token)">选择文件并下载…</button>
        <p class="help-text">选择文件并确认后才会下载影片。</p>
      </div>
    </div>
    <p v-if="error" role="alert">{{ error }}</p>
    <div class="bt-resource-detail__notes">
      <h4>更多信息</h4>
      <p class="help-text">当前搜索结果未提供简介、清晰度、编码和字幕信息。可在文件选择中核对文件名与大小。</p>
    </div>
  </section>
</template>
<script>
import BTResourcePoster from './BTResourcePoster.vue';
import { btByteText } from '../../utils/btFormat.js';
export default {
  name: 'BTResourceDetail', components: { BTResourcePoster },
  props: { resource: { type: Object, required: true }, busy: Boolean, error: { type: String, default: '' } },
  emits: ['back', 'preview'],
  computed: {
    published() {
      if (!this.resource.publish_time) return '未知';
      const date = new Date(this.resource.publish_time);
      return Number.isNaN(date.getTime()) ? '未知' : date.toLocaleString('zh-CN');
    }
  },
  mounted() { this.$refs.heading?.focus({ preventScroll: true }); },
  methods: { bytes: btByteText }
};
</script>
<style scoped>
.bt-resource-detail { display: grid; gap: var(--space-5); min-width: 0; }
.bt-resource-detail > button { justify-self: start; }
.bt-resource-detail__hero { display: flex; align-items: flex-start; gap: var(--space-5); }
.bt-resource-detail__poster { width: 220px; height: 300px; flex: 0 0 220px; }
.bt-resource-detail__body { flex: 1; min-width: 0; overflow-wrap: anywhere; }
.bt-resource-detail h3 { font-size: var(--fs-title); margin: 0 0 var(--space-4); }
.bt-resource-detail dl { display: grid; gap: var(--space-3); margin: 0 0 var(--space-5); }
.bt-resource-detail dl > div { display: grid; grid-template-columns: 6em minmax(0, 1fr); gap: var(--space-3); }
.bt-resource-detail dt { color: var(--text-secondary); }
.bt-resource-detail dd { margin: 0; }
.bt-resource-detail__notes { border-top: 1px solid var(--hairline); }
@media (max-width: 600px) {
  .bt-resource-detail__hero { flex-direction: column; }
  .bt-resource-detail__poster { width: 160px; height: 216px; flex-basis: auto; }
}
</style>
