<template>
  <div v-if="status && status.state !== 'idle'" class="wallpaper-status" role="status" data-test="wallpaper-status">
    <span>动态壁纸：{{ status.display_name }} · {{ status.message }}</span>
    <button type="button" class="btn-secondary btn-compact" data-test="wallpaper-stop" :disabled="busy" @click="stop">
      {{ busy ? '正在停止…' : status.state === 'failed' ? '关闭提示' : '停止动态壁纸' }}
    </button>
    <small v-if="error" role="alert">{{ error }}</small>
  </div>
</template>
<script>
import { GetWallpaperStatus, StopVideoWallpaper } from '../../wailsjs/go/main/App';
export default {
  name: 'WallpaperStatusBar',
  data: () => ({ status: null, error: '', busy: false, request: 0 }),
  mounted() {
    this.refresh();
    this.timer = setInterval(() => this.refresh(), 2000);
    window.addEventListener('wallpaper-changed', this.refresh);
  },
  beforeUnmount() {
    clearInterval(this.timer);
    window.removeEventListener('wallpaper-changed', this.refresh);
    this.request++;
  },
  methods: {
    async refresh() {
      if (this.busy) return;
      const request = ++this.request;
      try {
        const status = await GetWallpaperStatus();
        if (request === this.request) this.status = status;
      } catch { /* 读取失败保留上一次状态和停止入口，下一次正常轮询再核对。 */ }
    },
    async stop() {
      if (this.busy) return;
      this.busy = true; this.error = ''; this.request++;
      try { this.status = await StopVideoWallpaper(); }
      catch { this.error = '停止动态壁纸失败，请重试。'; }
      finally { this.request++; this.busy = false; }
    }
  }
};
</script>
<style scoped>
.wallpaper-status { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; padding: 8px 16px; border-bottom: 1px solid var(--hairline); background: var(--panel-bg); font-size: 12px; }
.wallpaper-status span { flex: 1; min-width: 0; overflow-wrap: anywhere; }
</style>
