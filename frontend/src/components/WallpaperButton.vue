<template>
  <div class="wallpaper-action">
    <button type="button" class="btn-secondary btn-compact" data-test="wallpaper-set"
      :disabled="busy || loading || !preflight?.eligible" :title="preflight?.message || hint" @click="apply">
      {{ busy ? '正在设置…' : kind === 'video' ? '设为动态壁纸' : '设为桌面壁纸' }}
    </button>
    <small v-if="error" role="alert" data-test="wallpaper-error">{{ error }}</small>
    <small v-else-if="notice" role="status" data-test="wallpaper-notice">{{ notice }}</small>
    <small v-else-if="!loading && preflight && !preflight.eligible" data-test="wallpaper-ineligible">{{ preflight.message }}</small>
    <small v-else>{{ hint }}</small>
    <button v-if="!loading && !preflight?.eligible" type="button" class="btn-secondary btn-compact"
      data-test="wallpaper-recheck" @click="check">重新检查</button>
  </div>
</template>
<script>
import { GetWallpaperPreflight, SetWallpaper } from '../../wailsjs/go/main/App';
export default {
  name: 'WallpaperButton',
  props: { kind: { type: String, required: true }, mediaId: { type: Number, required: true } },
  data: () => ({ preflight: null, loading: false, busy: false, error: '', notice: '', request: 0 }),
  computed: {
    hint() {
      return this.kind === 'video'
        ? '在所有显示器无声循环，退出应用后停止。最低长边 1920、短边 720 像素。'
        : '设置所有显示器的当前桌面。最低长边 1920、短边 720 像素。';
    }
  },
  watch: { mediaId: 'check', kind: 'check' },
  mounted() { this.check(); },
  beforeUnmount() { this.request++; },
  methods: {
    async check() {
      const request = ++this.request;
      this.loading = true; this.error = ''; this.notice = ''; this.preflight = null;
      try {
        const result = await GetWallpaperPreflight(this.kind, this.mediaId);
        if (request === this.request) this.preflight = result;
      } catch {
        if (request === this.request) this.error = '无法检查壁纸素材，请重新检查。';
      } finally { if (request === this.request) this.loading = false; }
    },
    async apply() {
      if (this.busy || !this.preflight?.eligible) return;
      const request = this.request;
      const kind = this.kind;
      this.busy = true; this.error = ''; this.notice = '';
      try {
        const status = await SetWallpaper(kind, this.mediaId);
        window.dispatchEvent(new Event('wallpaper-changed'));
        if (request === this.request) this.notice = kind === 'video' ? (status.message || '动态壁纸正在启动…') : '已设为桌面壁纸。';
      } catch (error) {
        if (request === this.request) this.error = String(error);
      } finally { this.busy = false; }
    }
  }
};
</script>
<style scoped>
.wallpaper-action { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.wallpaper-action small { flex: 1 1 180px; color: var(--text-secondary); overflow-wrap: anywhere; }
.wallpaper-action [role="alert"] { color: var(--danger-color, #c44); }
</style>
