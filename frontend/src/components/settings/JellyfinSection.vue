<template>
  <div class="jellyfin-settings">
    <h4>Jellyfin 客户端连接</h4>
    <p class="help-text">让 Fileball 等客户端浏览视频、标签、作品集和保存视图，收藏与观看进度同步到主库。仅局域网、原片直播放，不支持图片库或实时转码；编码是否能播放取决于客户端。</p>
    <p class="help-text">电脑需保持应用运行且不休眠。使用独立账号密码；应用重启、关闭或重新应用配置后，客户端需要重新登录。</p>
    <p role="status" data-test="jellyfin-status">{{ statusText }}</p>
    <p v-if="error" class="settings-error" role="alert">{{ error }}</p>
    <p v-if="status?.startup_error" class="settings-error" role="alert">{{ status.startup_error }}</p>
    <div v-if="status?.running" class="jellyfin-addresses">
      <p v-for="url in status.lan_urls" :key="url" data-test="jellyfin-address">{{ url }}</p>
      <p v-if="!status.lan_urls?.length" class="help-text">没有找到局域网地址，请检查电脑的网络连接。</p>
      <p class="help-text">在 Fileball 中添加 Jellyfin 服务器，填写上述地址和下方账号密码。</p>
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
    <p class="help-text">本区通过「应用 Jellyfin 配置」独立保存并立即生效。</p>
  </div>
</template>

<script>
import { ConfigureJellyfin, GetJellyfinStatus } from '../../../wailsjs/go/main/App';

export default {
  name: 'JellyfinSection',
  data() {
    return { status: null, loaded: false, busy: false, error: '', enabled: false, username: '', password: '', port: 8096 };
  },
  computed: {
    statusText() {
      if (!this.status) return '未加载';
      if (this.status.running) return `运行中（端口 ${this.status.port}）`;
      return this.status.enabled ? '已配置，未运行' : '未开启';
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
    },
    async apply() {
      this.busy = true;
      this.error = '';
      try {
        this.adopt(await ConfigureJellyfin({ enabled: this.enabled, username: this.username, password: this.password, port: this.port }));
        this.password = '';
      } catch (error) { this.error = String(error); }
      finally { this.busy = false; }
    }
  }
};
</script>

<style scoped>
.jellyfin-settings { margin-top: 24px; padding-top: 16px; border-top: 1px solid var(--border-color); }
.jellyfin-settings h4 { margin: 0 0 12px; }
.jellyfin-addresses { overflow-wrap: anywhere; }
.jellyfin-actions { display: flex; flex-wrap: wrap; gap: 8px; }
</style>
