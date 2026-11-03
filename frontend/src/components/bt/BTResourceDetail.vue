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
    <div class="bt-resource-detail__notes" aria-live="polite">
      <h4>站点详情</h4>
      <p v-if="loading" role="status" data-test="bt-detail-loading">正在读取站点详情…</p>
      <p v-else-if="detailError" role="alert" data-test="bt-detail-error">{{ detailError }}</p>
      <template v-else-if="detail">
        <p v-if="!detail.description && !detail.media_info && !imagePaths.length" class="help-text" data-test="bt-detail-empty">站点没有提供正文或截图。</p>
        <div v-if="detail.description" class="bt-resource-detail__text" data-test="bt-detail-description">{{ detail.description }}</div>
        <section v-if="detail.media_info" aria-label="MediaInfo"><h4>MediaInfo</h4><pre data-test="bt-detail-mediainfo">{{ detail.media_info }}</pre></section>
        <div v-if="imagePaths.length" class="bt-resource-detail__gallery">
          <figure v-for="(path, index) in imagePaths" :key="path">
            <span v-if="failedImages.includes(path)" class="help-text">截图加载失败</span>
            <img v-else :src="path" :alt="`资源截图 ${index + 1}`" loading="lazy" decoding="async" referrerpolicy="no-referrer" @error="failedImages.push(path)" />
          </figure>
        </div>
      </template>
      <button type="button" class="btn btn--secondary btn--sm" :disabled="loading || !active" data-test="bt-detail-retry" @click="load">{{ detailError ? '重试' : '刷新详情' }}</button>
    </div>
  </section>
</template>
<script>
import BTResourcePoster from './BTResourcePoster.vue';
import { btByteText } from '../../utils/btFormat.js';
export default {
  name: 'BTResourceDetail', components: { BTResourcePoster },
  props: { loadDetail: { type: Function, required: true }, active: { type: Boolean, default: true }, resource: { type: Object, required: true }, busy: Boolean, error: { type: String, default: '' } },
  emits: ['back', 'preview'],
  data: () => ({ detail: null, loading: false, detailError: '', failedImages: [], generation: 0, alive: true }),
  computed: {
    imagePaths() { return (this.detail?.image_paths || []).filter(path => /^\/preview\/bt-detail-image\/ci-bt-[a-f0-9]{48}\/[1-9][0-9]*\/(?:[0-9]|1[0-5])$/.test(path)); },
    published() {
      if (!this.resource.publish_time) return '未知';
      const date = new Date(this.resource.publish_time);
      return Number.isNaN(date.getTime()) ? '未知' : date.toLocaleString('zh-CN');
    }
  },
  watch: {
    'resource.result_token'() { this.load(); },
    active(value) { if (value) this.load(); else { this.generation++; this.loading = false; } }
  },
  mounted() { this.$refs.heading?.focus({ preventScroll: true }); this.load(); },
  beforeUnmount() { this.alive = false; this.generation++; },
  methods: {
    bytes: btByteText,
    async load() {
      if (!this.active) return;
      const generation = ++this.generation;
      this.loading = true; this.detail = null; this.detailError = ''; this.failedImages = [];
      try {
        const detail = await this.loadDetail(this.resource.result_token);
        if (this.alive && this.active && generation === this.generation) this.detail = detail;
      } catch (error) {
        if (this.alive && this.active && generation === this.generation) {
          const code = String(error?.message || error || '').split(':', 1)[0];
          this.detailError = ({ detail_dns_unsafe: '站点解析到了非公网或代理虚拟地址，请检查 DNS 与代理配置后重新读取。', detail_unsupported: '当前站点尚未适配详情读取。', bt_auth_failed: '站点或 Prowlarr 鉴权失败，请在连接设置及 Prowlarr 中核对凭据和 API 权限。', result_expired: '资源结果已过期，请返回列表重新搜索。', maintenance: '片库维护中，暂不能读取站点详情。', bt_service_unavailable: '无法连接本机 Prowlarr，请确认服务已启动。' })[code] || '暂时无法读取站点详情，请稍后重新读取。';
        }
      } finally { if (this.alive && generation === this.generation) this.loading = false; }
    }
  }
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
.bt-resource-detail__text, .bt-resource-detail pre { white-space: pre-wrap; overflow-wrap: anywhere; }
.bt-resource-detail pre { max-width: 100%; font-size: var(--fs-caption); }
.bt-resource-detail__gallery { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 280px), 1fr)); gap: var(--space-3); margin: var(--space-4) 0; }
.bt-resource-detail__gallery figure { min-width: 0; margin: 0; }
.bt-resource-detail__gallery img { width: 100%; height: auto; border-radius: var(--radius-sm); }
.bt-resource-detail__notes { border-top: 1px solid var(--hairline); }
@media (max-width: 600px) {
  .bt-resource-detail__hero { flex-direction: column; }
  .bt-resource-detail__poster { width: 160px; height: 216px; flex-basis: auto; }
}
</style>
