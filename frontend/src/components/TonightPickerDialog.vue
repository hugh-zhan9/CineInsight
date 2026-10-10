<template>
  <BaseModal class="tonight-picker" @close="$emit('close')">
    <header class="tonight-picker__header"><h3>今晚看什么</h3><button type="button" class="btn-secondary btn-compact" @click="$emit('close')">关闭</button></header>
    <p class="help-text">沿用打开弹窗时的片库筛选，包括关键词、标签、人物、评分和智能视图。需要改变这些条件时，可关闭后调整。</p>
    <p v-if="filter.keyword" class="help-text">当前关键词：{{ filter.keyword }}</p>
    <form class="tonight-picker__form" @submit.prevent="load">
      <label>可用时间（分钟）<input v-model.number="minutes" type="number" min="0" max="1440" step="1" data-test="tonight-minutes" /></label>
      <label><input v-model="unwatchedOnly" type="checkbox" data-test="tonight-unwatched" />仅未看</label>
      <label><input v-model="favoritesOnly" type="checkbox" data-test="tonight-favorites" />仅收藏</label>
      <button type="submit" class="btn-primary" :disabled="loading" data-test="tonight-search">{{ loading ? '正在挑选…' : '给我推荐' }}</button>
    </form>
    <p class="help-text">按整片时长筛选，0 表示不限；有时间上限时排除时长未知的视频。依次优先收藏、未看、个人评分，再按最近入库排序。</p>
    <p v-if="error" role="alert" class="tonight-picker__error" data-test="tonight-error">{{ error }}</p>
    <p v-else-if="queried && !loading && !items.length" role="status" data-test="tonight-empty">没有符合全部条件的视频，可调整时间或片库筛选。</p>
    <div class="tonight-picker__results">
      <article v-for="item in items" :key="item.video.id" data-test="tonight-result">
        <RelatedVideoItem :video="item.video" action-label="播放" @open="$emit('preview', item.video)" @action="$emit('play', item.video)" />
        <p class="help-text">{{ item.reasons.join(' · ') }}</p>
      </article>
    </div>
  </BaseModal>
</template>

<script>
import { SuggestTonightVideos } from '../../wailsjs/go/main/App';
import BaseModal from './ui/BaseModal.vue';
import RelatedVideoItem from './RelatedVideoItem.vue';

export default {
  name: 'TonightPickerDialog',
  components: { BaseModal, RelatedVideoItem },
  props: { filter: { type: Object, required: true } },
  emits: ['close', 'preview', 'play'],
  data() { return { minutes: 90, unwatchedOnly: this.filter.smart_view !== 'watched', favoritesOnly: false, items: [], loading: false, queried: false, error: '', generation: 0 }; },
  computed: { queryKey() { return JSON.stringify([this.filter, this.minutes, this.unwatchedOnly, this.favoritesOnly]); } },
  watch: { queryKey() { this.generation++; this.items = []; this.error = ''; this.queried = false; this.loading = false; } },
  mounted() { this.load(); },
  beforeUnmount() { this.generation++; },
  methods: {
    async load() {
      const generation = ++this.generation;
      this.items = []; this.error = ''; this.queried = false;
      if (!Number.isInteger(this.minutes) || this.minutes < 0 || this.minutes > 1440) {
        this.error = '可用时间请输入 0–1440 之间的整数分钟'; return;
      }
      this.loading = true;
      try {
        const result = await SuggestTonightVideos({ filter: this.filter, max_duration_seconds: this.minutes * 60, unwatched_only: this.unwatchedOnly, favorites_only: this.favoritesOnly, limit: 6 });
        if (generation !== this.generation) return;
        this.items = result || []; this.queried = true;
      } catch (err) {
        if (generation === this.generation) this.error = '挑选失败：' + err;
      } finally { if (generation === this.generation) this.loading = false; }
    }
  }
};
</script>

<style scoped>
:deep(.tonight-picker) { width: min(700px, calc(100vw - 40px)); max-width: min(700px, calc(100vw - 40px)); max-height: calc(100vh - 60px); display: flex; flex-direction: column; }
.tonight-picker__header { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.tonight-picker__form { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; }
.tonight-picker__form label { display: flex; align-items: center; gap: 6px; }
.tonight-picker__form input[type=number] { width: 90px; }
.tonight-picker__results { overflow-y: auto; min-height: 0; }
.tonight-picker__results article { margin: 10px 0; }
.tonight-picker__error { color: var(--danger-color); }
</style>
