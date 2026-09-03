<template>
  <div :id="`settings-browser-bridge`" class="settings-section">
    <h3>浏览器插件</h3>
    <p class="help-text">
      浏览器插件抓到网页上的视频流之后，可以一键推给桌面端下载并入库。这条通道只在
      本机 127.0.0.1 上监听，且每个请求都要带令牌——它能让桌面端按外部请求去取地址、
      往磁盘写文件，所以默认关闭，由你决定什么时候开。
    </p>

    <div class="bridge-status">
      <div class="bridge-status-main">
        <strong data-test="bridge-status-text">{{ statusText }}</strong>
        <span v-if="status && status.running" class="bridge-url">{{ status.url }}</span>
        <span v-else-if="status && status.startup_error" class="bridge-error" data-test="bridge-status-error">
          {{ status.startup_error }}
        </span>
      </div>
      <div class="bridge-actions">
        <button type="button" class="btn-secondary" @click="loadStatus">刷新</button>
      </div>
    </div>

    <div class="setting-item">
      <label class="checkbox-label">
        <input data-test="bridge-enabled-toggle" type="checkbox" v-model="form.browser_bridge_enabled" />
        <span>开启浏览器插件桥接</span>
      </label>
      <p class="help-text">开关保存后立即生效：关闭时端口不再监听，已配对的插件会显示未检测到。</p>
    </div>

    <div class="setting-item">
      <label>配对令牌</label>
      <div class="bridge-token-row">
        <input
          data-test="bridge-token"
          class="bridge-token"
          :type="tokenVisible ? 'text' : 'password'"
          :value="form.browser_bridge_token"
          readonly
        />
        <button type="button" class="btn-secondary" @click="tokenVisible = !tokenVisible">
          {{ tokenVisible ? '隐藏' : '显示' }}
        </button>
        <button type="button" class="btn-secondary" :disabled="!form.browser_bridge_token" @click="copyToken">
          复制
        </button>
        <button type="button" class="btn-secondary" data-test="bridge-regenerate" @click="regenerate">
          {{ form.browser_bridge_token ? '重新生成' : '生成令牌' }}
        </button>
      </div>
      <p class="help-text">
        把这串令牌填到插件的选项页里完成配对。重新生成会让旧令牌立刻失效，已经配好的
        插件需要重新填一次。令牌不随「保存设置」一起提交，只由这里的按钮改写。
      </p>
      <p v-if="tokenMessage" class="bridge-hint" data-test="bridge-token-message">{{ tokenMessage }}</p>
    </div>

    <div class="setting-item">
      <label>下载目录</label>
      <div class="bridge-token-row">
        <input class="bridge-token" type="text" v-model="form.browser_download_directory" placeholder="尚未选择" />
        <button type="button" class="btn-secondary" @click="chooseDirectory">选择目录</button>
      </div>
      <p class="help-text">
        插件推过来的视频落到这里。没有设置时桥接会拒绝建任务而不是替你挑一个目录。
        这个目录不会自动加进扫描目录——想让下载的视频进片库，请在「扫描目录管理」里
        把它（或它的上级目录）加进去。
      </p>
    </div>

    <div class="setting-item">
      <label>同时下载数</label>
      <input
        type="number"
        v-model.number="form.browser_download_concurrency"
        min="1"
        max="4"
        step="1"
        class="number-input"
      />
      <p class="help-text">同时跑几个下载任务，1–4，默认 2。这些任务在跑 ffmpeg，开太多只会互相抢带宽。</p>
    </div>

    <p class="help-text">
      下载任务的进度在顶部导航的「下载」页里看——那里由后端推送实时刷新，不用手动点。
    </p>
  </div>
</template>

<script>
import {
  GetBrowserBridgeStatus,
  RegenerateBrowserBridgeToken,
  SelectBrowserDownloadDirectory
} from '../../../wailsjs/go/main/App';

// 浏览器插件分区：桥接状态、配对令牌、下载目录与任务列表。
export default {
  name: 'BrowserBridgeSection',
  props: {
    form: { type: Object, required: true }
  },
  data() {
    return {
      status: null,
      tokenVisible: false,
      tokenMessage: ''
    };
  },
  mounted() {
    this.loadStatus();
  },
  computed: {
    statusText() {
      if (!this.status) return '未加载';
      if (this.status.running) return `运行中（端口 ${this.status.port}）`;
      if (!this.status.enabled) return '未开启';
      return '未运行';
    }
  },
  methods: {
    async loadStatus() {
      try {
        this.status = await GetBrowserBridgeStatus();
      } catch (err) {
        this.status = { running: false, startup_error: String(err) };
      }
    },
    async regenerate() {
      try {
        const token = await RegenerateBrowserBridgeToken();
        this.form.browser_bridge_token = token;
        this.tokenVisible = true;
        this.tokenMessage = '新令牌已生效，旧令牌立即失效；已配对的插件需要重新填一次。';
        await this.loadStatus();
      } catch (err) {
        this.tokenMessage = '';
        this.$emit('error', `生成令牌失败：${err}`);
      }
    },
    async copyToken() {
      try {
        await navigator.clipboard.writeText(this.form.browser_bridge_token || '');
        this.tokenMessage = '令牌已复制，粘到插件选项页里。';
      } catch (err) {
        this.$emit('error', `复制失败：${err}`);
      }
    },
    async chooseDirectory() {
      try {
        const directory = await SelectBrowserDownloadDirectory();
        // 用户取消选择时返回空串，此时保持原值而不是清空。
        if (directory) this.form.browser_download_directory = directory;
      } catch (err) {
        this.$emit('error', `选择目录失败：${err}`);
      }
    },
  }
};
</script>

<style scoped>
.bridge-status {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 12px;
  border-radius: 6px;
  background: var(--bg-color);
  margin-bottom: 12px;
}

.bridge-status-main {
  display: grid;
  gap: 4px;
}

.bridge-url {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  color: var(--text-secondary);
}

.bridge-error { color: var(--danger-color, #c0392b); }
.bridge-warn { color: var(--warning-color, #b26a00); }
.bridge-hint { color: var(--text-secondary); margin: 6px 0 0; }

.bridge-token-row {
  display: flex;
  gap: 8px;
  align-items: center;
}

.bridge-token { flex: 1; min-width: 0; }

</style>
