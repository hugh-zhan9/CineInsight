<template>
  <section id="settings-bt" class="settings-section">
    <BTConnectionPanel :key="panelEpoch" :connection="connection" :busy="loading || saving || testing" :testing="testing" :error="error" :save-succeeded="saved" :test-results="testResults" @save="save" @test="test" @update:dirty="dirty = $event; $emit('update:dirty', $event)" />
    <p v-if="metadataStatus" class="help-text" data-test="bt-metadata-capability">磁力文件清单：{{ metadataCapability }}</p>
    <p class="help-text" data-test="bt-credential-status">{{ credentialStatus }}</p>
    <p class="help-text">API Key 和密码保存在系统钥匙串。站点账号在 Prowlarr 中配置；下载目录需在片库扫描范围内。</p>
    <button type="button" class="btn btn--secondary btn--sm" :disabled="loading || saving || testing" @click="load">刷新连接</button>
    <button type="button" class="btn btn--secondary btn--sm" @click="$emit('scan-settings')">配置扫描目录</button>
  </section>
</template>
<script>
import { confirmAction } from '../../utils/feedback.js';
import BTConnectionPanel from '../bt/BTConnectionPanel.vue';
import { btAPI, btConnectionError } from '../../utils/btAPI.js';
export default {
  name: 'BTConnectionSection', components: { BTConnectionPanel }, emits: ['update:dirty', 'scan-settings'],
  data: () => ({ connection: null, panelEpoch: 0, dirty: false, loading: false, saving: false, testing: false, error: '', testResults: [], metadataStatus: '', saved: 0, generation: 0, alive: true }),
  computed: { metadataCapability() { return ({ supported: '可用', unsupported: '此版本未验证', unavailable: '未能完成检查' }[this.metadataStatus] || '未能完成检查'); }, credentialStatus() { return ({ configured: '系统凭据已保存，连通情况以测试结果为准。', unconfigured: '尚未配置系统凭据。', unavailable: '系统钥匙串暂不可用，需要重新连接。', busy: '系统凭据正在使用，请稍后重试。' }[this.connection?.credential_status] || '尚未读取系统凭据状态。'); } },
  mounted() { this.load(); }, beforeUnmount() { this.alive = false; this.generation++; },
  methods: {
    async load() { if (this.loading || this.saving || this.testing) return; if (this.dirty && !await confirmAction({ title: '刷新连接', message: '放弃尚未保存的连接修改？', confirmText: '放弃修改' })) return; const g = ++this.generation; this.loading = true; this.error = ''; try { const c = await btAPI.GetBTConnection(); if (this.alive && g === this.generation) { this.connection = c; this.panelEpoch++; this.dirty = false; this.$emit('update:dirty', false); } } catch (e) { if (this.alive && g === this.generation) this.error = btConnectionError(e); } finally { if (this.alive && g === this.generation) this.loading = false; } },
    async save(request) { if (this.loading || this.saving || this.testing) return; const g = ++this.generation; this.saving = true; this.error = ''; this.testResults = []; this.metadataStatus = ''; try { const c = await btAPI.ConfigureBTConnection(request); if (this.alive && g === this.generation) { this.connection = c; this.saved++; } } catch (e) { if (this.alive && g === this.generation) this.error = btConnectionError(e); } finally { if (this.alive && g === this.generation) this.saving = false; } },
    async test() { if (this.loading || this.saving || this.testing) return; const g = ++this.generation; this.testing = true; this.error = ''; this.metadataStatus = ''; this.testResults = []; try { const r = await btAPI.TestBTConnection(); if (this.alive && g === this.generation) { this.metadataStatus = r.metadata_preview_status || 'unavailable'; this.testResults = [['Prowlarr', r.prowlarr_status, r.prowlarr_version], ['qBittorrent', r.qb_status, [r.qb_version, r.qb_api_version ? `API ${r.qb_api_version}` : ''].filter(Boolean).join(' · ')]].map(([service, status, version]) => ({ service, ok: status === 'ok', version: version || '', message: status === 'ok' ? '' : btConnectionError({ code: status }) })); } } catch (e) { if (this.alive && g === this.generation) { this.error = btConnectionError(e); this.metadataStatus = 'unavailable'; } } finally { if (this.alive && g === this.generation) this.testing = false; } }
  }
};
</script>
