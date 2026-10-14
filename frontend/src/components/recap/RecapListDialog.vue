<template>
  <DialogShell :title="dialogTitle" size="xl" class="recap-list" data-test="recap-list-dialog" @close="$emit('close')">
    <AppBanner v-if="error" tone="danger" :title="error" data-test="recap-list-error">
      <template #actions><button type="button" class="btn btn--secondary btn--sm" data-test="recap-list-retry" @click="retry">重试</button></template>
    </AppBanner>
    <p v-if="loading && !rows.length" class="recap-muted recap-list__status" data-test="recap-list-loading"><AppSpinner :size="16" /> 正在读取…</p>
    <p v-else-if="loaded && !rows.length && !error" class="recap-muted" data-test="recap-list-empty">这一期没有可列出的{{ meta.title }}。</p>

    <div v-if="layout === 'media' && rows.length" class="recap-list__grid" data-test="recap-list-items">
      <RecapMediaCard
        v-for="row in rows"
        :key="row.key"
        :kind="row.media"
        :id="row.id"
        :rank="row.rank"
        :badge="row.badge"
        :title="row.title"
        :subtitle="row.available ? row.subtitle : '已不在库中'"
        :available="row.available"
        data-test="recap-list-item"
        @open="pick(row)"
      />
    </div>
    <RecapLiveFeed v-else-if="layout === 'feed' && rows.length" :items="rows" :now="now" data-test="recap-list-items" @open="pick({ action: 'feed', payload: $event })" />
    <ol v-else-if="rows.length" class="recap-list__rows" data-test="recap-list-items">
      <li v-for="row in rows" :key="row.key">
        <button type="button" class="btn-plain recap-list__row" data-test="recap-list-item" @click="pick(row)">
          <b v-if="row.rank" class="recap-list__rank">{{ row.rank }}</b>
          <span v-if="layout === 'tags'" class="recap-list__dot" :style="{ '--chip-color': row.color || 'var(--accent-color)' }"></span>
          <span v-else-if="layout === 'entities'" class="recap-list__avatar"><img v-if="row.image" :src="row.image" alt="" loading="lazy" /><RecapIcon v-else :name="kind === 'people' ? 'person' : 'collection'" /></span>
          <span class="recap-list__text"><strong>{{ row.title }}</strong><small>{{ row.note }}</small></span>
          <span v-if="row.badge" class="recap-list__badge">{{ row.badge }}</span>
        </button>
      </li>
    </ol>

    <p v-if="hasMore && rows.length" class="recap-list__more">
      <button type="button" class="btn btn--secondary btn--sm" :disabled="loading" data-test="recap-list-more" @click="load(true)">{{ loading ? '正在加载…' : '加载更多' }}</button>
    </p>
  </DialogShell>
</template>

<script>
// 回顾「查看全部」：页面上的数字（新发现 137 部、看过的视频 N 部……）比展示出来的样本 / 排行多时，
// 这里按同一口径、同一顺序分页列出全部（ListRecapItems，每页 60）。已不在库中的项灰显、标「已不在库中」、不可点。
// 点一项交给回顾页原有的打开方式（pick → 页面关掉对话框再打开）。请求带代次，晚到的旧响应直接丢弃。
import { ListRecapItems } from '../../../wailsjs/go/main/App';
import AppBanner from '../ui/AppBanner.vue';
import AppSpinner from '../ui/AppSpinner.vue';
import DialogShell from '../ui/DialogShell.vue';
import RecapIcon from './RecapIcon.vue';
import RecapLiveFeed from './RecapLiveFeed.vue';
import RecapMediaCard from './RecapMediaCard.vue';

export const RECAP_LIST_PAGE = 60;

// 每一类的标题、单位与排版：media 是卡片网格，feed 复用今日流水，其余是紧凑的行。ranked 的显示名次。
export const RECAP_LIST_KINDS = {
  new: { title: '新发现', unit: '部', layout: 'media' },
  rewatched: { title: '重温', unit: '部', layout: 'media' },
  favorites: { title: '新收藏', unit: '项', layout: 'media' },
  diary: { title: '日记评分', unit: '条', layout: 'diary' },
  videos: { title: '看过的视频', unit: '部', layout: 'media', ranked: true },
  images: { title: '看过的图片', unit: '张', layout: 'media', ranked: true },
  feed: { title: '流水', unit: '条', layout: 'feed' },
  tags: { title: '标签排行', unit: '个', layout: 'tags', ranked: true },
  people: { title: '人物排行', unit: '位', layout: 'entities', ranked: true },
  collections: { title: '作品集排行', unit: '个', layout: 'entities', ranked: true }
};

const weightNote = item => `${item.weight} 次播放 · ${item.videos} 部`;
const videoRow = (video, badge = '') => ({
  key: `v${video.id}`, media: 'video', id: Number(video.id), title: video.title || '已删除的视频', subtitle: '', badge,
  available: Boolean(video.available), action: 'video', payload: video
});

// 把一页里这一类的条目统一成渲染用的行（feed 原样交给 RecapLiveFeed）。
function pageRows(kind, page) {
  switch (kind) {
    case 'new':
    case 'rewatched':
      return (page.videos || []).map(video => videoRow(video));
    case 'videos':
      return (page.video_ranks || []).map(item => videoRow(item.video, `${item.count} 次`));
    case 'images':
      return (page.image_ranks || []).map(item => ({
        key: `i${item.image.id}`, media: 'image', id: Number(item.image.id), title: item.image.name || '已删除的图片', subtitle: '',
        badge: `${item.count} 次`, available: Boolean(item.image.available), action: 'image', payload: item.image
      }));
    case 'favorites':
      return (page.favorites || []).map(item => ({
        key: `${item.kind}${item.id}`, media: item.kind === 'image' ? 'image' : 'video', id: Number(item.id), title: item.title,
        subtitle: item.kind === 'image' ? '图片' : '视频', badge: '', available: Boolean(item.available), action: 'feed', payload: item
      }));
    case 'diary':
      return (page.diary || []).map(entry => ({
        key: `d${entry.id}`, title: entry.title, badge: `${Number(entry.rating).toFixed(1)} 分`, action: 'diary', payload: entry,
        note: `${entry.watched_on}${entry.video_id && !entry.video_available ? ' · 关联的视频已不在库中' : ''}`
      }));
    case 'feed':
      return page.feed || [];
    case 'tags':
      return (page.tags || []).map(item => ({ key: `t${item.tag.id}`, title: item.tag.name, color: item.tag.color, note: weightNote(item), action: 'tag', payload: item.tag }));
    default:
      return (page.entities || []).map(item => ({
        key: `e${item.entity.id}`, title: item.entity.name, image: item.entity.image_url, note: weightNote(item), action: 'entity', payload: item.entity
      }));
  }
}

export default {
  name: 'RecapListDialog',
  components: { AppBanner, AppSpinner, DialogShell, RecapIcon, RecapLiveFeed, RecapMediaCard },
  props: {
    kind: { type: String, required: true, validator: kind => Object.hasOwn(RECAP_LIST_KINDS, kind) },
    // 当前显示的那一期：{ period, year, month }（取自回顾页的结果）。
    request: { type: Object, required: true },
    // 页面上的数字，第一页回来之前先用它做标题。
    total: { type: Number, default: 0 },
    now: { type: Number, default: () => Date.now() }
  },
  emits: ['close', 'pick'],
  data() {
    return { items: [], loadedTotal: null, hasMore: false, loading: false, loaded: false, error: '', generation: 0, lastAppend: false };
  },
  computed: {
    meta() { return RECAP_LIST_KINDS[this.kind]; },
    layout() { return this.meta.layout; },
    live() { return this.request.period === 'live'; },
    dialogTitle() {
      const title = this.kind === 'feed' && this.live ? '今日流水' : `${this.live ? '今天' : '本期'}${this.meta.title}`;
      return `${title}（${this.loadedTotal ?? this.total} ${this.meta.unit}）`;
    },
    rows() {
      return this.meta.ranked ? this.items.map((row, index) => ({ ...row, rank: index + 1 })) : this.items;
    },
    listKey() { return [this.kind, this.request.period, this.request.year, this.request.month].join('|'); }
  },
  watch: {
    listKey() {
      this.items = [];
      this.loadedTotal = null;
      this.hasMore = false;
      this.loaded = false;
      this.load();
    }
  },
  mounted() {
    this.load();
  },
  beforeUnmount() {
    this.generation++;
  },
  methods: {
    // append=false 从头读第一页；true 接着已有的往后读一页。只接受最新一次请求的响应。
    async load(append = false) {
      const generation = ++this.generation;
      const offset = append ? this.items.length : 0;
      const { period, year, month } = this.request;
      this.loading = true;
      this.error = '';
      this.lastAppend = append;
      try {
        const page = await ListRecapItems({ period, year, month, kind: this.kind, offset, limit: RECAP_LIST_PAGE });
        if (generation !== this.generation) return;
        const rows = pageRows(this.kind, page || {});
        this.items = append ? [...this.items, ...rows] : rows;
        this.loadedTotal = Number(page?.total || 0);
        this.hasMore = Boolean(page?.has_more);
        this.loaded = true;
      } catch (err) {
        if (generation === this.generation) this.error = `读取列表失败：${err?.message || err}`;
      } finally {
        if (generation === this.generation) this.loading = false;
      }
    },
    retry() {
      this.load(this.lastAppend);
    },
    // 已不在库中的不可点；其余交给回顾页按原来的方式打开（页面会先关掉对话框）。
    pick(row) {
      if (row.available === false || row.payload?.available === false) return;
      this.$emit('pick', { action: row.action, payload: row.payload });
    }
  }
};
</script>

<style scoped>
.recap-list__status { display: flex; align-items: center; gap: var(--space-2); }
/* 卡片网格：视频与图片混排（新收藏）时统一 16:9 裁切，行才整齐。 */
.recap-list__grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(160px, 1fr)); gap: 14px; }
.recap-list__grid :deep(.recap-card__media) { aspect-ratio: 16 / 9; }
.recap-list__rows { display: grid; gap: 6px; margin: 0; padding: 0; list-style: none; }
.recap-list__row {
  display: flex;
  align-items: center;
  gap: 12px;
  width: 100%;
  padding: 8px 12px;
  border: 1px solid var(--hairline);
  border-radius: var(--radius-md);
  background: var(--panel-bg);
  color: var(--text-primary);
  text-align: left;
  cursor: pointer;
  transition: border-color var(--transition);
}
.recap-list__row:hover { border-color: var(--accent-border); }
.recap-list__rank { flex: none; min-width: 28px; color: var(--text-muted); font-size: var(--fs-secondary); font-weight: var(--fw-bold); font-variant-numeric: tabular-nums; }
.recap-list__dot { flex: none; width: 10px; height: 10px; border-radius: 50%; background: var(--chip-color); box-shadow: 0 0 0 2px color-mix(in srgb, var(--chip-color) 35%, transparent); }
.recap-list__avatar { display: grid; flex: none; place-items: center; width: 36px; height: 36px; overflow: hidden; border-radius: var(--radius-sm); background: var(--thumb-bg); color: var(--thumb-fg); }
.recap-list__avatar img { width: 100%; height: 100%; object-fit: cover; }
.recap-list__text { display: grid; flex: 1; gap: 2px; min-width: 0; }
.recap-list__text strong { overflow: hidden; font-size: var(--fs-secondary); font-weight: var(--fw-semibold); text-overflow: ellipsis; white-space: nowrap; }
.recap-list__text small { color: var(--text-secondary); font-size: var(--fs-caption); }
.recap-list__badge { flex: none; padding: 2px 8px; border-radius: var(--radius-pill); background: var(--accent-soft); color: var(--accent-text); font-size: var(--fs-caption); font-weight: var(--fw-semibold); }
.recap-list__more { display: flex; justify-content: center; margin: 16px 0 0; }
@media (prefers-reduced-motion: reduce) {
  .recap-list__row { transition: none; }
}
</style>
