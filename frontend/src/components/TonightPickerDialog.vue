<template>
  <DialogShell title="今晚看什么" size="lg" class="tonight-picker" @close="$emit('close')">
    <p class="help-text">沿用打开弹窗时的片库筛选，包括关键词、标签、人物、评分和智能视图。需要改变这些条件时，可关闭后调整。</p>
    <p v-if="filter.keyword" class="help-text">当前关键词：{{ filter.keyword }}</p>
    <form class="tonight-picker__form" @submit.prevent="reroll">
      <label>可用时间（分钟）<input v-model.number="minutes" class="number-input" type="number" min="0" max="1440" step="1" data-test="tonight-minutes" /></label>
      <label>观看状态
        <select v-model="watchState" class="select-input tonight-picker__watch" data-test="tonight-watch-state">
          <option value="unwatched" title="只推荐还没看过的">探索</option>
          <option value="any">不限</option>
          <option value="watched" title="只推荐看过的">重温</option>
        </select>
      </label>
      <label><input v-model="favoritesOnly" type="checkbox" data-test="tonight-favorites" />仅收藏</label>
      <span class="tonight-picker__actions">
        <button type="submit" class="btn btn--secondary" :disabled="loading" data-test="tonight-reroll">{{ loading ? '正在抽…' : '换一批' }}</button>
        <button type="button" class="btn btn--primary" :disabled="loading" data-test="tonight-play-random" @click="$emit('play-random')">随机播放一部</button>
      </span>
    </form>
    <p class="help-text">和「随机播放」是同一套条件与算法：按播放次数均衡随机，播放越少越容易被抽到，最近 {{ recentLimit }} 次随机播放过的不参与；这里改的条件也会用于「随机播放」。时长按整片计算，0 表示不限，有上限时排除时长未知的视频。</p>
    <p v-if="error" role="alert" class="tonight-picker__error" data-test="tonight-error">{{ error }}</p>
    <p v-else-if="notice" class="help-text" role="status" data-test="tonight-notice">{{ notice }}</p>
    <p v-else-if="queried && !loading && !items.length" class="empty-hint" role="status" data-test="tonight-empty">没有符合全部条件的视频，可调整时间、观看状态或片库筛选。</p>
    <div class="tonight-picker__results">
      <article v-for="item in items" :key="item.video.id" data-test="tonight-result">
        <RelatedVideoItem :video="item.video" action-label="播放" @open="$emit('preview', item.video)" @action="$emit('play', item.video)" />
        <div v-if="item.video.tags?.length" class="tonight-picker__tags" data-test="tonight-tags">
          <span v-for="tag in item.video.tags" :key="tag.id" class="tag-badge" :style="{ backgroundColor: tagColor(tag.color) }" :title="tag.name">
            <span class="tag-badge__name">{{ tag.name }}</span>
          </span>
        </div>
        <p class="help-text">{{ item.reasons.join(' · ') }}</p>
      </article>
    </div>
  </DialogShell>
</template>

<script>
import { SuggestTonightVideos } from '../../wailsjs/go/main/App';
import DialogShell from './ui/DialogShell.vue';
import RelatedVideoItem from './RelatedVideoItem.vue';
import { tagBackgroundColor } from '../utils/tagColor.js';
import { normalizeRandomConditions, randomConditionFields, RANDOM_RECENT_LIMIT, validRandomMinutes } from '../utils/randomPlayback.js';

const TONIGHT_LIMIT = 6;

// 今晚看什么与随机播放合并（2026-10-11）：用同一套随机条件和均衡加权抽 6 部候选，
// 条件改动回写给片库页（随机播放主键随之改用），播放候选仍是普通播放。
export default {
  name: 'TonightPickerDialog',
  components: { DialogShell, RelatedVideoItem },
  props: {
    filter: { type: Object, required: true },
    // 共用的随机条件 { watchState, favoritesOnly, maxMinutes }，只在打开时读一次。
    conditions: { type: Object, default: () => ({}) },
    // 最近随机播放过的视频，与随机播放一样不进候选。
    excludeIds: { type: Array, default: () => [] }
  },
  emits: ['close', 'preview', 'play', 'play-random', 'update:conditions'],
  data() {
    const conditions = normalizeRandomConditions(this.conditions);
    return { minutes: conditions.maxMinutes, watchState: conditions.watchState, favoritesOnly: conditions.favoritesOnly, items: [], loading: false, queried: false, error: '', notice: '', generation: 0, recentLimit: RANDOM_RECENT_LIMIT };
  },
  computed: { queryKey() { return JSON.stringify([this.filter, this.minutes, this.watchState, this.favoritesOnly]); } },
  // 条件一变就重新抽（实时生效）：旧结果立即作废，分钟数输入期间按 300ms 合并一次请求；合法的条件同步回片库页。
  watch: {
    queryKey() {
      this.generation++; this.items = []; this.error = ''; this.notice = ''; this.queried = false; this.loading = false;
      if (validRandomMinutes(this.minutes)) this.$emit('update:conditions', this.currentConditions());
      clearTimeout(this.reloadTimer);
      this.reloadTimer = setTimeout(() => this.load(), 300);
    }
  },
  mounted() { this.load(); },
  beforeUnmount() { this.generation++; clearTimeout(this.reloadTimer); },
  methods: {
    tagColor(hex) { return tagBackgroundColor(hex); },
    currentConditions() { return { watchState: this.watchState, favoritesOnly: this.favoritesOnly, maxMinutes: this.minutes }; },
    // 换一批：在原条件上再排除这一批，抽不出新的就留着这一批并说明。
    reroll() { return this.load(this.items.map(item => item.video.id)); },
    async load(extraExcludeIDs = []) {
      clearTimeout(this.reloadTimer);
      const generation = ++this.generation;
      const previous = this.items;
      this.items = []; this.error = ''; this.notice = ''; this.queried = false;
      if (!validRandomMinutes(this.minutes)) {
        this.error = '可用时间请输入 0–1440 之间的整数分钟'; return;
      }
      this.loading = true;
      try {
        const request = { filter: this.filter, ...randomConditionFields(this.currentConditions()), exclude_ids: [...new Set([...this.excludeIds, ...extraExcludeIDs])] };
        const result = (await SuggestTonightVideos(request, TONIGHT_LIMIT)) || [];
        if (generation !== this.generation) return;
        if (!result.length && extraExcludeIDs.length) {
          this.items = previous; this.notice = '符合条件的候选都在这里了，没有更多可换的。';
        } else {
          this.items = result;
        }
        this.queried = true;
      } catch (err) {
        if (generation === this.generation) this.error = '挑选失败：' + err;
      } finally { if (generation === this.generation) this.loading = false; }
    }
  }
};
</script>

<style scoped>
.tonight-picker__form { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-3); margin: var(--space-3) 0; }
.tonight-picker__form label { display: flex; align-items: center; gap: 6px; color: var(--text-secondary); font-size: var(--fs-secondary); white-space: nowrap; }
.tonight-picker__form .number-input { width: 96px; }
.tonight-picker__watch { width: auto; min-width: 120px; }
.tonight-picker__form input[type=checkbox] { width: 16px; height: 16px; margin: 0; accent-color: var(--accent-color); }
.tonight-picker__actions { display: flex; gap: var(--space-2); margin-left: auto; }
.tonight-picker__results article { margin: 10px 0; }
.tonight-picker__tags { display: flex; flex-wrap: wrap; gap: var(--space-1); margin: 6px 0 0; }
.tonight-picker__error { color: var(--danger-text); font-size: var(--fs-secondary); }
</style>
