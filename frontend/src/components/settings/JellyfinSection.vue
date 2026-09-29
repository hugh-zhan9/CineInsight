<template>
  <!-- D-PC59：Jellyfin 从「手机端浏览」里拆出来，成为有自己锚点的分区。 -->
  <div id="settings-jellyfin" class="settings-section jellyfin-settings">
    <h3>Jellyfin 客户端连接</h3>
    <p class="help-text">让 Fileball 等客户端浏览视频、标签、作品集和保存视图，收藏与观看进度同步到主库。仅局域网、原片直播放，不支持图片库或实时转码；编码是否能播放取决于客户端。</p>
    <!-- 会话已持久化（D-PC47）：应用重启后令牌仍有效；关掉服务、改账号或重新应用配置才会让客户端重新登录。 -->
    <p class="help-text">电脑需保持应用运行且不休眠。使用独立账号密码；应用重启后客户端不用重新登录，关闭服务、修改账号或重新应用配置后需要重新登录。</p>
    <p role="status" data-test="jellyfin-status">{{ statusText }}</p>
    <p v-if="error" class="settings-error" role="alert">{{ error }}</p>
    <p v-if="status?.startup_error" class="settings-error" role="alert">{{ status.startup_error }}</p>
    <div v-if="status?.running" class="jellyfin-addresses">
      <p v-for="url in status.lan_urls" :key="url" data-test="jellyfin-address">{{ url }}</p>
      <p v-if="!status.lan_urls?.length" class="help-text">没有找到局域网地址，请检查电脑的网络连接。</p>
      <p class="help-text">在 Fileball 中添加 Jellyfin 服务器，填写上述地址和下方账号密码。</p>
    </div>

    <!-- 诊断（PLAY-14）：最近一次来自内网客户端的请求与最近一次失败，便于判断「连不上」卡在哪一步。 -->
    <div class="jellyfin-diagnostics" data-test="jellyfin-diagnostics">
      <template v-if="diagnosticsError">
        <span>读取连接诊断失败：{{ diagnosticsError }}</span>
      </template>
      <template v-else-if="!diagnostics">
        <span>正在读取连接诊断…</span>
      </template>
      <template v-else>
        <span data-test="jellyfin-last-request">{{ lastRequestText }}</span>
        <span data-test="jellyfin-last-failure">{{ lastFailureText }}</span>
      </template>
    </div>

    <div class="setting-item">
      <label class="checkbox-label"><input v-model="enabled" type="checkbox" :disabled="busy || !loaded" data-test="jellyfin-enabled" />启用 Jellyfin 兼容服务</label>
    </div>
    <div class="setting-item">
      <label for="jellyfin-username">登录账号</label>
      <input id="jellyfin-username" v-model="username" type="text" maxlength="64" autocomplete="off" :disabled="busy || !loaded" />
    </div>
    <div class="setting-item">
      <label for="jellyfin-password">{{ status?.password_set ? '修改密码（留空保持）' : '登录密码' }}</label>
      <input id="jellyfin-password" v-model="password" type="password" autocomplete="new-password" :disabled="busy || !loaded" />
      <p class="help-text">密码为 8–72 字节，仅用于此服务。局域网 HTTP 连接不加密，请使用独立密码。</p>
    </div>
    <div class="setting-item">
      <label for="jellyfin-port">端口</label>
      <input id="jellyfin-port" v-model.number="port" class="number-input" type="number" min="1024" max="65535" :disabled="busy || !loaded" />
    </div>
    <div class="jellyfin-actions">
      <button type="button" class="btn-primary" data-test="jellyfin-apply" :disabled="busy || !loaded" @click="apply">{{ busy ? '应用中…' : '应用 Jellyfin 配置' }}</button>
      <button type="button" class="btn-secondary" :disabled="busy" @click="load">刷新状态</button>
    </div>
    <p class="help-text">本区通过「应用 Jellyfin 配置」独立保存并立即生效，不随页面底部的「保存所有设置」提交。</p>
  </div>
</template>

<script>
import { ConfigureJellyfin, GetJellyfinDiagnostics, GetJellyfinStatus } from '../../../wailsjs/go/main/App';
import { confirmAction } from '../../utils/feedback.js';

function formatTime(value) {
  if (!value) return '';
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? '' : date.toLocaleString('zh-CN');
}

export default {
  name: 'JellyfinSection',
  data() {
    return {
      status: null, loaded: false, busy: false, error: '', enabled: false, username: '', password: '', port: 8096,
      diagnostics: null, diagnosticsError: ''
    };
  },
  computed: {
    statusText() {
      if (!this.status) return '未加载';
      if (this.status.running) return `运行中（端口 ${this.status.port}）`;
      return this.status.enabled ? '已配置，未运行' : '未开启';
    },
    lastRequestText() {
      const at = formatTime(this.diagnostics?.last_request_at);
      if (!at) return '还没有客户端连接过。';
      const client = String(this.diagnostics?.last_client || '').trim();
      return client ? `最近连接：${at} · ${client}` : `最近连接：${at}`;
    },
    // last_failure 可能为 null（P-022）：没有失败就明说，不留空行。
    lastFailureText() {
      const failure = this.diagnostics?.last_failure;
      if (!failure) return '最近没有失败的请求。';
      const at = formatTime(failure.at);
      const parts = [at, failure.route_shape, failure.status ? `HTTP ${failure.status}` : ''].filter(Boolean);
      return `最近失败：${parts.join(' · ')}`;
    }
  },
  mounted() { this.load(); },
  methods: {
    adopt(status) {
      this.status = status;
      this.enabled = status.enabled;
      this.username = status.username || '';
      this.port = status.port || 8096;
      this.loaded = true;
    },
    async load() {
      this.busy = true;
      this.error = '';
      try { this.adopt(await GetJellyfinStatus()); }
      catch (error) { this.loaded = false; this.error = String(error); }
      finally { this.busy = false; }
      await this.loadDiagnostics();
    },
    async loadDiagnostics() {
      try {
        this.diagnostics = await GetJellyfinDiagnostics();
        this.diagnosticsError = '';
      } catch (error) {
        this.diagnostics = null;
        this.diagnosticsError = String(error);
      }
    },
    async apply() {
      // 重新应用配置会作废全部已登录的会话（P-022 的 Configure 语义）。服务此前没开过就没有会话，
      // 不必打扰；开着时先说清楚后果再动手。
      if (this.status?.enabled) {
        const confirmed = await confirmAction({
          title: '应用 Jellyfin 配置',
          message: '应用后，所有已登录的客户端需要重新登录（Fileball 等要重新输入账号密码）。继续吗？',
          confirmText: '应用配置'
        });
        if (!confirmed) return;
      }
      this.busy = true;
      this.error = '';
      try {
        this.adopt(await ConfigureJellyfin({ enabled: this.enabled, username: this.username, password: this.password, port: this.port }));
        this.password = '';
      } catch (error) { this.error = String(error); }
      finally { this.busy = false; }
      await this.loadDiagnostics();
    }
  }
};
</script>

<style scoped>
.jellyfin-addresses { overflow-wrap: anywhere; }
.jellyfin-actions { display: flex; flex-wrap: wrap; gap: 8px; }
.jellyfin-diagnostics {
  display: grid;
  gap: 4px;
  margin: 12px 0 16px;
  padding: 10px 12px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius);
  background: var(--control-bg);
  color: var(--text-secondary);
  font-size: 12.5px;
  overflow-wrap: anywhere;
}
</style>
