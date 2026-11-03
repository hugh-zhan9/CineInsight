<template>
  <AppBanner v-if="empty && !dismissed" title="从你想做的事开始" data-test="setup-prompt">
    <p>片库还是空的。可以按看片、整理、搜画面或制作字幕逐步准备。</p>
    <template #actions>
      <button type="button" class="btn btn--primary btn--sm" @click="$emit('open')">按目标准备</button>
      <button type="button" class="btn btn--ghost btn--sm" @click="dismiss">稍后再说</button>
    </template>
  </AppBanner>
</template>
<script>
import { CountLibraryVideos } from '../../wailsjs/go/main/App';
import AppBanner from './ui/AppBanner.vue';
const KEY = 'cineinsight-setup-dismissed';
export default {
  name: 'CapabilitySetupPrompt', components: { AppBanner }, emits: ['open'],
  props: { active: { type: Boolean, default: true } },
  data() { let dismissed = false; try { dismissed = window.localStorage.getItem(KEY) === '1'; } catch {} return { empty: false, dismissed }; },
  watch: { active(value) { if (value) this.load(); } },
  mounted() { this.load(); this._off = window.runtime?.EventsOn?.('library-scan-summary', () => this.load()); },
  beforeUnmount() { this._disposed = true; this._off?.(); },
  methods: {
    async load() { if (this.dismissed || !this.active) return; const request = this._request = (this._request || 0) + 1; try { const count = await CountLibraryVideos({}); if (!this._disposed && request === this._request) this.empty = count === 0; } catch { if (request === this._request) this.empty = false; } },
    dismiss() { this.dismissed = true; try { window.localStorage.setItem(KEY, '1'); } catch {} }
  }
};
</script>
