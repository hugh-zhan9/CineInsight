<template>
  <section class="bt-connection" aria-label="BT 服务连接">
    <h3>Prowlarr 与 qBittorrent</h3>
    <p class="help-text">连接本机服务，选择资源后交给 qBittorrent 下载。</p>
    <p v-if="error" role="alert" class="bt-error">{{ error }}</p>
    <form @submit.prevent="save">
      <fieldset :disabled="busy || readonly">
        <label><input v-model="form.enabled" type="checkbox" data-test="bt-enabled" />启用 BT 资源</label>
        <div class="bt-connection-grid">
          <label>Prowlarr 地址<input v-model.trim="form.prowlarr_base" class="text-input" data-test="bt-prowlarr-base" type="url" required /></label>
          <label>qBittorrent 地址<input v-model.trim="form.qb_base" class="text-input" data-test="bt-qb-base" type="url" required /></label>
          <label>qBittorrent 账号<input v-model="form.qb_username" class="text-input" data-test="bt-qb-user" autocomplete="off" /></label>
          <label>下载根目录<input v-model="form.download_root" class="text-input" data-test="bt-download-root" required /></label>
          <label v-for="secret in secretFields" :key="secret.key">{{ secret.label }}<span class="help-text">{{ connection?.[secret.key + '_configured'] ? '已保存' : '尚未保存' }}</span>
            <select v-model="secrets[secret.key].operation" class="select-input" :data-test="`bt-${secret.key}-op`" @change="secrets[secret.key].value = ''"><option value="keep">保持当前值</option><option value="replace">填写新值</option><option value="clear">清除已保存值</option></select>
            <input v-if="secrets[secret.key].operation === 'replace'" v-model="secrets[secret.key].value" type="password" class="text-input" autocomplete="new-password" :aria-label="secret.label" :data-test="`bt-${secret.key}-value`" required />
          </label>
        </div>
      </fieldset>
      <div class="bt-connection-actions"><button type="submit" class="btn btn--primary" :disabled="busy || readonly" data-test="bt-save">{{ busy ? '正在保存…' : '保存连接' }}</button><button type="button" class="btn btn--secondary" :disabled="busy || testing || !connection" data-test="bt-test-connection" @click="$emit('test')">{{ testing ? '正在测试…' : '测试已保存的连接' }}</button></div>
    </form>
    <p v-if="saved" role="status" class="help-text">连接配置已保存；测试结果单独显示。</p>
    <ul v-if="testResults.length" class="bt-connection-results" aria-label="连接测试结果"><li v-for="result in testResults" :key="result.service">{{ result.service }}：{{ result.ok ? '连接成功' : (result.message || '连接失败') }}{{ result.version ? ` · ${result.version}` : '' }}</li></ul>
  </section>
</template>
<script>
const freshSecrets = () => ({ prowlarr_key: { operation: 'keep', value: '' }, qb_password: { operation: 'keep', value: '' } });
export default {
  name: 'BTConnectionPanel',
  props: { connection: { type: Object, default: null }, busy: Boolean, testing: Boolean, readonly: Boolean, error: { type: String, default: '' }, saveSucceeded: { type: Number, default: 0 }, testResults: { type: Array, default: () => [] } },
  emits: ['save', 'test', 'update:dirty'],
  computed: { dirty() { const c = this.connection; const original = { expected_revision: c?.revision || 0, prowlarr_base: c?.prowlarr_base || 'http://127.0.0.1:9696', qb_base: c?.qb_base || 'http://127.0.0.1:8081', qb_username: c?.qb_username || '', download_root: c?.download_root || '', enabled: Boolean(c?.enabled) }; return Object.keys(original).some(k => this.form[k] !== original[k]) || Object.values(this.secrets).some(s => s.operation !== 'keep'); } },
  data() { return { form: {}, secrets: freshSecrets(), saved: false, secretFields: [{ key: 'prowlarr_key', label: 'Prowlarr API Key' }, { key: 'qb_password', label: 'qBittorrent 密码' }] }; },
  watch: {
    dirty(value) { this.$emit('update:dirty', value); },
    connection: { immediate: true, handler(value, previous) { if (value && previous && value.revision === previous.revision && value.instance_id === previous.instance_id) return; this.reset(value); } },
    saveSucceeded() { this.secrets = freshSecrets(); this.saved = true; }
  },
  beforeUnmount() { this.secrets = freshSecrets(); },
  methods: {
    reset(value) { this.form = { expected_revision: value?.revision || 0, prowlarr_base: value?.prowlarr_base || 'http://127.0.0.1:9696', qb_base: value?.qb_base || 'http://127.0.0.1:8081', qb_username: value?.qb_username || '', download_root: value?.download_root || '', enabled: Boolean(value?.enabled) }; this.secrets = freshSecrets(); this.saved = false; },
    save() { if (this.busy || this.readonly) return; if (Object.values(this.secrets).some(s => s.operation === 'replace' && !s.value)) return; this.saved = false; this.$emit('save', { ...this.form, prowlarr_key: { ...this.secrets.prowlarr_key }, qb_password: { ...this.secrets.qb_password } }); }
  }
};
</script>
<style scoped>
.bt-connection { display: grid; gap: var(--space-3); }
.bt-connection h3, .bt-connection p { margin: 0; }
.bt-connection fieldset { border: 0; padding: 0; margin: 0; min-width: 0; }
.bt-connection-grid { display: grid; grid-template-columns: repeat(auto-fit,minmax(min(100%,240px),1fr)); gap: var(--space-3); margin: var(--space-3) 0; }
.bt-connection-grid label { display: grid; gap: var(--space-1); min-width: 0; }
.bt-connection-actions { display: flex; gap: var(--space-2); flex-wrap: wrap; }
.bt-error { color: var(--danger-text); }
.bt-connection-results { margin: 0; padding-left: var(--space-4); font-size: var(--fs-secondary); }
</style>
