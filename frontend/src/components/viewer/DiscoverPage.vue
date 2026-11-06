<template>
  <section class="page-content discover-page" data-test="discover-page">
    <PageHeader title="发现" subtitle="从自己的片库里，发现下一部想看的视频" />
    <form class="discover-page__filters" @submit.prevent="load(true)" @change="changeConditions">
      <label>观看状态<select v-model="watchState" class="select-input" data-test="discover-watch"><option value="any">不限</option><option value="unwatched">没看过</option><option value="watched">重温</option></select></label>
      <label>可用时间（分钟）<input v-model.number="minutes" type="number" class="number-input" min="0" max="1440" data-test="discover-minutes" /></label>
      <label><input v-model="favoritesOnly" type="checkbox" data-test="discover-favorites" />仅收藏</label>
      <button type="submit" class="btn btn--primary" :disabled="loading" data-test="discover-reroll">{{ loading ? '正在挑选…' : '换一批' }}</button>
      <button v-if="hasFilter" type="button" class="btn btn--secondary" data-test="discover-clear-filter" @click="filter = {}; changeConditions()">清除带入的视频筛选</button>
    </form>
    <p v-if="error" role="alert" class="discover-page__error">{{ error }}</p>
    <p v-else-if="notice" role="status" class="help-text">{{ notice }}</p>
    <p v-if="!loading && !items.length && !error" class="empty-hint">没有符合当前条件的视频，可调整筛选。</p>
    <p v-if="loading && !items.length" role="status">正在挑选…</p>
    <div class="discover-page__grid">
      <RecapMediaCard v-for="item in items" :key="item.video.id" :id="item.video.id" :title="item.video.display_title || item.video.name"
        :subtitle="(item.reasons || []).join(' · ')" :data-discovery-id="item.video.id" data-test="discover-item" @open="$emit('open-video', Number(item.video.id))" />
    </div>
  </section>
</template>
<script>
import { SuggestTonightVideos } from '../../../wailsjs/go/main/App';
import PageHeader from '../ui/PageHeader.vue';
import RecapMediaCard from '../recap/RecapMediaCard.vue';
import { createDiscoverySession, resetDiscoverySession, fetchDiscoveryBatch, observeDiscoveryExposure } from '../../utils/discoverySession.js';
import { translateBackendError } from '../../utils/feedback.js';
export default {
  name: 'DiscoverPage', components: { PageHeader, RecapMediaCard },
  inject: { discoverySession: { default: createDiscoverySession } },
  props: { pageActive: { type: Boolean, default: true }, initialRequest: { type: Object, default: null } },
  emits: ['open-video'],
  data: () => ({ items: [], filter: {}, watchState: 'any', minutes: 0, favoritesOnly: false, loading: false, error: '', notice: '', generation: 0 }),
  computed: { hasFilter() { return Object.keys(this.filter).length > 0; } },
  watch: {
    'initialRequest.token'() { this.applyRequest(); },
    pageActive(active) {
      if (!active) { this.generation++; this.loading = false; this._observer?.disconnect(); }
      else if (!this.items.length) this.load();
      else this.observe();
    },
    items: { flush: 'post', handler() { this.observe(); } }
  },
  mounted() { this.applyRequest(); },
  beforeUnmount() { this.generation++; this._observer?.disconnect(); },
  methods: {
    removeDiscoveryVideos(ids) {
      const removed = new Set(ids.map(Number)); this.generation++;
      this.items = this.items.filter(item => !removed.has(Number(item.video.id))); this.loading = false;
    },
    observe() { this._observer?.disconnect(); this._observer = observeDiscoveryExposure(this.$el, this.discoverySession, () => this.pageActive); },
    applyRequest() {
      const filter = { ...(this.initialRequest?.filter || {}) };
      const changed = JSON.stringify(filter) !== JSON.stringify(this.filter);
      this.filter = filter;
      if (changed) this.changeConditions();
      else { this.generation++; this.items = []; if (this.pageActive) this.load(); }
    },
    changeConditions() { this.generation++; this.items = []; resetDiscoverySession(this.discoverySession); if (this.pageActive) this.load(); },
    async load(reroll = false) {
      if (!this.pageActive) return;
      const generation = ++this.generation;
      if (!Number.isInteger(this.minutes) || this.minutes < 0 || this.minutes > 1440) { this.error = '可用时间须为0到1440分钟的整数'; return; }
      this.loading = true; this.error = ''; this.notice = '';
      try {
        const result = await fetchDiscoveryBatch(SuggestTonightVideos, this.discoverySession,
          { filter: this.filter, watch_state: this.watchState, favorites_only: this.favoritesOnly, max_duration_seconds: this.minutes * 60 },
          12, reroll ? this.items.map(item => item.video.id) : [], () => generation === this.generation && this.pageActive);
        if (!result) return;
        if (result.items.length || !reroll) this.items = result.items;
        this.notice = !result.items.length && reroll ? '符合条件的内容都在这里了，暂时没有新的候选。' : result.newRound ? '开始新一轮发现' : '';
      } catch (error) { if (generation === this.generation) this.error = `读取推荐失败：${translateBackendError(error?.message || error)}`; }
      finally { if (generation === this.generation) this.loading = false; }
    }
  }
};
</script>
<style scoped>
.discover-page__filters { display: flex; flex-wrap: wrap; align-items: end; gap: var(--space-3); margin-bottom: var(--space-4); }
.discover-page__filters label { display: flex; align-items: center; gap: var(--space-2); font-size: var(--fs-secondary); }
.discover-page__grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: var(--space-4); }
.discover-page__error { color: var(--danger-text); }
</style>
