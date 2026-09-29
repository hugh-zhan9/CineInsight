<template>
  <!-- IINA 进度同步的状态（D-PC47 / PLAY-14）：此前只有绑定、界面上看不到，断点不同步时
       用户无从判断是没在监听还是同步出了错。只读，没有可保存的设置。 -->
  <div id="settings-iina-sync" class="settings-section">
    <h3>IINA 进度同步</h3>
    <p class="help-text">用 IINA 播放时，应用会读取 IINA 记下的播放位置，同步到片库的观看进度与「已看」。</p>
    <div class="iina-sync-status" data-test="iina-sync-status" role="status">
      <template v-if="error">
        <span>读取 IINA 同步状态失败：{{ error }}</span>
      </template>
      <template v-else-if="!status">
        <span>正在读取 IINA 同步状态…</span>
      </template>
      <template v-else>
        <strong data-test="iina-sync-state">{{ stateText }}</strong>
        <span v-if="status.enabled && status.watching_dir">监听目录：<code>{{ status.watching_dir }}</code></span>
        <span data-test="iina-sync-last">{{ lastSyncText }}</span>
        <span v-if="status.last_error" class="iina-sync-error" data-test="iina-sync-error">最近一次同步失败：{{ status.last_error }}</span>
      </template>
      <button type="button" class="btn-secondary btn-compact" data-test="iina-sync-refresh" @click="load">刷新</button>
    </div>
  </div>
</template>

<script>
import { GetIINASyncStatus } from '../../../wailsjs/go/main/App';

export default {
  name: 'IINASyncSection',
  data() {
    return { status: null, error: '', syncedOff: null };
  },
  computed: {
    stateText() {
      if (!this.status) return '';
      if (this.status.enabled) return '正在同步';
      // 没在监听且没有别的报错时，最常见的原因是还没装 IINA 或还没用它播过视频。
      return this.status.last_error ? '未在同步' : '未在同步：没有找到 IINA 的播放断点目录（还没装 IINA，或还没用它播过视频）';
    },
    lastSyncText() {
      const value = this.status?.last_sync_at;
      const date = value ? new Date(value) : null;
      if (!date || Number.isNaN(date.getTime())) return '还没有成功同步过。';
      return `最近一次同步：${date.toLocaleString('zh-CN')}`;
    }
  },
  mounted() {
    this.load();
    // 每次同步完成后端都会发 iina-progress-synced，顺手刷新「最近一次同步」。
    if (window.runtime?.EventsOn) {
      const off = window.runtime.EventsOn('iina-progress-synced', () => this.load());
      if (typeof off === 'function') this.syncedOff = off;
    }
  },
  beforeUnmount() {
    this.syncedOff?.();
  },
  methods: {
    async load() {
      try {
        this.status = await GetIINASyncStatus();
        this.error = '';
      } catch (err) {
        this.status = null;
        this.error = String(err);
      }
    }
  }
};
</script>

<style scoped>
.iina-sync-status {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 6px;
  padding: 10px 12px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius);
  background: var(--control-bg);
  color: var(--text-secondary);
  font-size: 13px;
  overflow-wrap: anywhere;
}
.iina-sync-status code { font-family: var(--font-mono); font-size: 12px; word-break: break-all; }
.iina-sync-error { color: var(--danger-color); }
</style>
