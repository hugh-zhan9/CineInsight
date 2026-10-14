<template>
  <DialogShell :title="title" size="xl" close-data-test="view-all-close" :data-test="dataTest" @close="$emit('close')">
    <p v-if="countText" class="help-text view-all__count" data-test="view-all-count">{{ countText }}</p>
    <AppBanner v-if="error" tone="danger" :title="error" data-test="view-all-error">
      <template #actions><button type="button" class="btn btn--secondary btn--sm" data-test="view-all-retry" @click="loadMore">重试</button></template>
    </AppBanner>
    <div v-if="loading && !cards.length" class="view-all__grid" aria-busy="true" data-test="view-all-loading">
      <SkeletonBlock v-for="n in 12" :key="n" shape="poster" :lines="2" />
    </div>
    <EmptyState v-else-if="loaded && !cards.length && !error" icon="collection" title="这里已经没有了" message="刚才那些已经看完或不再符合条件。" data-test="view-all-empty" />
    <div v-if="cards.length" class="view-all__grid">
      <RecapMediaCard
        v-for="card in cards"
        :key="card.key"
        variant="poster"
        :id="card.videoID"
        :cover="card.cover"
        :title="card.title"
        :subtitle="card.subtitle"
        :badge="card.badge"
        data-test="view-all-item"
        @open="$emit('open-video', card.videoID)"
      />
    </div>
    <button v-if="hasMore && cards.length && !error" type="button" class="btn btn--secondary view-all__more" :disabled="loading" data-test="view-all-more" @click="loadMore">{{ loading ? '正在加载…' : '加载更多' }}</button>
  </DialogShell>
</template>

<script>
// 「查看全部」弹窗（首页「追到哪了」「想看的片片库里已有」、「视频」页「追到哪了」）：区块里只放前几个，这里按页取全。
// fetchPage(offset, limit) → { items, total, has_more }（后端的分页接口原样给）；toCard(item) 把一条换成海报卡
// { key, videoID, cover, title, subtitle, badge }，返回 null 的不显示。翻页期间数据变了（刚看完一部）可能挪出重复的，按 key 去重。
// 点卡片发 open-video(videoID)，宿主关掉弹窗再进整页详情。
import RecapMediaCard from '../recap/RecapMediaCard.vue';
import AppBanner from '../ui/AppBanner.vue';
import DialogShell from '../ui/DialogShell.vue';
import EmptyState from '../ui/EmptyState.vue';
import SkeletonBlock from '../ui/SkeletonBlock.vue';
import { translateBackendError } from '../../utils/feedback.js';

export const VIEW_ALL_PAGE_SIZE = 50;

export default {
  name: 'ViewAllDialog',
  components: { AppBanner, DialogShell, EmptyState, RecapMediaCard, SkeletonBlock },
  props: {
    title: { type: String, required: true },
    fetchPage: { type: Function, required: true },
    toCard: { type: Function, required: true },
    // 数量的量词（「共 N 个」/「共 N 部」）。
    unit: { type: String, default: '个' },
    dataTest: { type: String, default: 'view-all-dialog' }
  },
  emits: ['close', 'open-video'],
  data() {
    return { items: [], total: null, hasMore: true, loading: false, loaded: false, error: '' };
  },
  computed: {
    cards() {
      const seen = new Set();
      return this.items.map(item => this.toCard(item)).filter(card => card && !seen.has(card.key) && seen.add(card.key));
    },
    countText() { return this.total === null ? '' : `共 ${Number(this.total).toLocaleString('zh-CN')} ${this.unit}`; }
  },
  created() { this._token = 0; },
  mounted() { this.loadMore(); },
  beforeUnmount() { this._token += 1; },
  methods: {
    async loadMore() {
      if (this.loading) return;
      const token = ++this._token;
      this.loading = true;
      this.error = '';
      try {
        const page = await this.fetchPage(this.items.length, VIEW_ALL_PAGE_SIZE);
        if (token !== this._token) return;
        const items = Array.isArray(page?.items) ? page.items : [];
        this.items = [...this.items, ...items];
        this.total = Number(page?.total ?? this.items.length);
        this.hasMore = Boolean(page?.has_more) && items.length > 0;
        this.loaded = true;
      } catch (err) {
        if (token === this._token) this.error = `读取失败：${translateBackendError(err?.message || err)}`;
      } finally {
        if (token === this._token) this.loading = false;
      }
    }
  }
};
</script>

<style scoped>
.view-all__count { margin: 0 0 var(--space-3); }
.view-all__grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(132px, 1fr)); gap: var(--space-4) var(--space-3); }
.view-all__more { display: flex; margin: var(--space-4) auto 0; }
</style>
