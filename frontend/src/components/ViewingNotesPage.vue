<template>
  <main class="page-content viewing-notes-page" data-test="viewing-notes-page">
    <PageHeader title="观看笔记" subtitle="观影日记、片段书签，以及无法确认观看的播放记录。">
      <template #actions><button type="button" class="btn btn--primary" data-test="diary-add" @click="editor = {}">手动补记</button></template>
    </PageHeader>
    <nav class="notes-tabs" aria-label="观看笔记分类"><button v-for="item in tabs" :key="item.key" type="button" :class="['notes-tabs__tab', { 'notes-tabs__tab--on': tab === item.key }]" :aria-pressed="tab === item.key" :data-test="`notes-tab-${item.key}`" @click="tab = item.key">{{ item.label }}</button></nav>
    <BookmarkPanel v-if="tab === 'bookmarks'" :page-active="pageActive" @open-bookmark="$emit('open-bookmark', $event)" @open-video="$emit('open-video', $event)" />
    <template v-else>
      <div class="notes-toolbar notes-filters"><label v-if="tab === 'diary'" class="notes-year">年份 <input v-model.number="year" class="number-input" type="number" min="1" max="9999" aria-label="日记年份" /></label><input v-model="keyword" class="search-input notes-search" aria-label="搜索观看记录" placeholder="搜索片名或短评" /><button type="button" class="btn btn--secondary btn--sm" :disabled="loading" @click="reset">刷新</button></div>
      <template v-if="tab === 'diary'">
        <p class="help-text">每次有效观看独立记录，按观看日期汇总；记录数不代表完整看完的电影数。手动补记与自动记录各自保留。</p>
        <p class="help-text">第三方播放器只有提供独立、可靠的观看会话才能自动记入；无法确认的重看可手动补记。</p>
        <p v-if="reviewError" class="notes-alert" role="alert">{{ reviewError }}</p>
        <section v-if="review" class="diary-recap" aria-label="年度回顾" data-test="diary-recap">
          <p class="diary-recap__summary">{{ review.year }}年 · {{ review.total }}条观看记录 · {{ review.days }}个观看日 · 自动{{ review.automatic }} / 历史{{ review.historical }} / 手动{{ review.manual }}</p>
          <p class="help-text">{{ review.rated_count }}条已评分 · 平均分{{ review.average_rating == null ? '暂无' : Number(review.average_rating).toFixed(1) }}</p>
          <div class="diary-months"><div v-for="(count, index) in review.months" :key="index"><span :style="{ height: `${Math.max(2, 64 * count / Math.max(1, ...review.months))}px` }"></span><small>{{ index + 1 }}月 {{ count }}</small></div></div>
          <details v-if="review.most_watched?.length"><summary>记录最多的影片</summary><ol><li v-for="(item, index) in review.most_watched" :key="index">{{ item.title }} · {{ item.count }}次</li></ol></details>
        </section>
      </template>
      <p v-else class="help-text">这里仅能确认曾启动播放，无法据此推算观看时长。补记时请确认观看日期；原始播放记录仍保留。</p>
      <p v-if="error" class="notes-alert" role="alert">{{ error }}</p>
      <SkeletonBlock v-if="loading && !items.length" :lines="3" data-test="notes-loading" />
      <p v-else-if="loading" class="empty-hint">正在读取…</p>
      <EmptyState v-else-if="!error && !items.length" title="当前条件没有记录" :message="tab === 'diary' ? '换个年份或关键词试试，也可以用「手动补记」记下一次观看。' : '换个关键词试试。'" data-test="notes-empty" />
      <template v-for="(item, index) in items" :key="item.id">
        <h3 v-if="tab === 'diary' && (index === 0 || item.watched_on !== items[index - 1].watched_on)" class="notes-date">{{ item.watched_on }}</h3>
        <article class="notes-entry notes-entry--media" :data-test="tab === 'diary' ? 'diary-row' : 'playback-history-row'">
          <NotesThumb :src="thumbnailFor(item)" :fallback="item.video_id ? '原片不可用' : '片库外'" :clickable="Boolean(item.video_id && item.source_available)" :title="item.title" @open="$emit('open-video-id', item.video_id)" />
          <div class="notes-entry__body">
          <h4>{{ item.title }}</h4>
          <template v-if="tab === 'diary'"><p>{{ origins[item.origin] || item.origin }} · {{ item.rating == null ? '未评分' : `${item.rating}分` }}<span v-if="item.date_basis === 'imported_local'"> · 历史日期按导入时本地时区解释</span></p><p class="notes-body">{{ item.note }}</p></template>
          <p v-else>{{ formatDate(item.played_at) }} · {{ sourceName(item.source) }}</p>
          <p v-if="item.video_id && !item.source_available" class="notes-unavailable">原片不可用，记录仍保留</p>
          <div class="notes-toolbar">
            <button v-if="item.video_id && item.source_available" type="button" class="btn btn--secondary btn--sm" @click="$emit('open-video-id', item.video_id)">打开原片</button>
            <button type="button" class="btn btn--secondary btn--sm" @click="editor = tab === 'diary' ? item : { title: item.title }">{{ tab === 'diary' ? '修订日记' : '手动补记' }}</button>
            <button v-if="tab === 'diary'" type="button" class="btn btn--secondary btn--sm" @click="remove(item)">删除日记</button>
          </div>
          </div>
        </article>
      </template>
      <nav class="notes-toolbar notes-pager" aria-label="记录分页"><button type="button" class="btn btn--secondary btn--sm" :disabled="loading || !cursors.length" @click="previous">上一页</button><button type="button" class="btn btn--secondary btn--sm" :disabled="loading || !hasMore" @click="next">下一页</button></nav>
    </template>
    <DiaryEditor v-if="editor" :key="editor.id || 'new'" :entry="editor" :active="pageActive" @close="editor = null" @saved="saved" />
  </main>
</template>
<script>
import { ListViewingDiary, DeleteViewingDiary, GetViewingYearReview, ListUnconfirmedPlaybackHistory } from '../../wailsjs/go/main/App';
import BookmarkPanel from './BookmarkPanel.vue';
import DiaryEditor from './DiaryEditor.vue';
import NotesThumb from './NotesThumb.vue';
import EmptyState from './ui/EmptyState.vue';
import PageHeader from './ui/PageHeader.vue';
import SkeletonBlock from './ui/SkeletonBlock.vue';
import { confirmAction } from '../utils/feedback.js';
export default {
  name: 'ViewingNotesPage', props: { pageActive: { type: Boolean, default: true } }, components: { BookmarkPanel, DiaryEditor, EmptyState, NotesThumb, PageHeader, SkeletonBlock }, emits: ['open-bookmark', 'open-video', 'open-video-id'],
  data: () => ({ tabs: [{ key: 'diary', label: '观影日记' }, { key: 'bookmarks', label: '片段书签' }, { key: 'history', label: '播放记录' }], origins: { automatic: '自动记录', historical: '历史观看', manual: '手动补记' }, tab: 'diary', year: new Date().getFullYear(), keyword: '', items: [], cursor: null, cursors: [], hasMore: false, loading: false, error: '', review: null, reviewError: '', editor: null, generation: 0, reviewGeneration: 0 }),
  watch: { pageActive(active) { if (active) { this.load(); this.loadReview(); } }, tab() { this.reset(); }, year() { this.reset(); }, keyword() { this.reset(); } },
  mounted() { this.reset(); }, beforeUnmount() { this.generation++; this.reviewGeneration++; this._disposed = true; },
  methods: {
    thumbnailFor(item) {
      return item.video_id && item.source_available ? `/preview/thumbnail/${item.video_id}` : '';
    },
    formatDate(value) { if (!value || String(value).startsWith('0001-01-01')) return '日期未知'; const date = new Date(value); return Number.isFinite(date.getTime()) ? date.toLocaleString() : '日期未知'; },
    sourceName(value) { return ({ desktop_play: '外部播放启动', desktop_random: '随机播放启动', mobile_feed: '手机端历史播放', legacy: '旧版播放记录', queue_view: '队列观看', inline_view: '应用内记录', jellyfin_view: 'Jellyfin记录' })[value] || '播放记录'; },
    reset() { this.cursor = null; this.cursors = []; this.items = []; this.hasMore = false; this.load(); this.loadReview(); },
    async loadReview() {
      const generation = ++this.reviewGeneration; this.review = null; this.reviewError = '';
      if (this.tab !== 'diary' || !Number.isInteger(this.year) || this.year < 1 || this.year > 9999) return;
      try { const result = await GetViewingYearReview(this.year); if (generation === this.reviewGeneration) this.review = result; }
      catch (err) { if (generation === this.reviewGeneration) this.reviewError = `年度回顾读取失败：${err}`; }
    },
    async load(cursor = this.cursor, history = this.cursors) {
      const generation = ++this.generation; this.error = ''; this.loading = false;
      if (this.tab === 'bookmarks') return;
      if (this.tab === 'diary' && (!Number.isInteger(this.year) || this.year < 1 || this.year > 9999)) { this.error = '请输入1–9999之间的年份。'; return; }
      this.loading = true;
      try {
        const request = { keyword: this.keyword, cursor_id: cursor?.id || 0, limit: 50 };
        const page = this.tab === 'diary' ? await ListViewingDiary({ ...request, year: this.year, cursor_date: cursor?.date || '' }) : await ListUnconfirmedPlaybackHistory(request);
        if (generation !== this.generation) return; this.items = page.items || []; this.hasMore = !!page.has_more; this.cursor = cursor; this.cursors = history; return true;
      } catch (err) { if (generation === this.generation) this.error = `读取记录失败：${err}`; }
      finally { if (generation === this.generation) this.loading = false; }
    },
    next() { const last = this.items.at(-1); return this.load({ id: last.id, date: last.watched_on || '' }, [...this.cursors, this.cursor]); },
    previous() { return this.load(this.cursors.at(-1) || null, this.cursors.slice(0, -1)); },
    saved(entry) { this.editor = null; this.tab = 'diary'; this.year = Number(entry.watched_on.slice(0, 4)); this.reset(); },
    async remove(item) {
      if (!await confirmAction({ title: '删除日记', message: '删除这次观看的日记正文和评分？原视频与播放事实不会删除。', danger: true })) return;
      try { await DeleteViewingDiary(item.id, item.revision); if (!this._disposed) this.reset(); } catch (err) { if (!this._disposed) this.error = `删除日记失败：${err}`; }
    }
  }
};
</script>
<style scoped>
.viewing-notes-page { display: grid; gap: 12px; align-content: start; overflow: auto; box-sizing: border-box; }
/* 外观照 AppTabs（页签要带各自的 data-test 钩子，组件还不能透传，先保留这组按钮的标记，只换外观；
   AppTabs 支持逐项钩子后换成组件，删掉这几条）。 */
.notes-tabs { display: flex; flex-wrap: wrap; gap: var(--space-1) var(--space-5); border-bottom: 1px solid var(--hairline); }
.notes-tabs__tab { display: inline-flex; align-items: center; gap: 6px; height: var(--h-unit); margin-bottom: -1px; padding: 0 2px; border: 0; border-bottom: 2px solid transparent; background: transparent; color: var(--text-secondary); font: inherit; font-size: var(--fs-body); font-weight: var(--fw-medium); cursor: pointer; white-space: nowrap; }
.notes-tabs__tab:hover { color: var(--text-primary); }
.notes-tabs__tab--on { border-bottom-color: var(--accent-color); color: var(--text-primary); font-weight: var(--fw-semibold); }
.notes-tabs__tab:focus-visible { outline: 2px solid var(--accent-color); outline-offset: 2px; border-radius: var(--radius-sm); }
.notes-filters { margin-bottom: 0; }
.notes-year { display: inline-flex; align-items: center; gap: 6px; color: var(--text-secondary); font-size: var(--fs-secondary); white-space: nowrap; }
.notes-year .number-input { width: 96px; }
.viewing-notes-page > .help-text { margin: 0; }
.diary-recap { display: grid; gap: 8px; padding: 14px 16px; border: 1px solid var(--hairline); border-radius: var(--radius-lg); background: var(--panel-bg); }
.diary-recap p { margin: 0; }
.diary-recap__summary { font-weight: var(--fw-semibold); }
.diary-recap details { font-size: var(--fs-secondary); color: var(--text-secondary); }
.diary-months { display: flex; gap: 10px; align-items: end; flex-wrap: wrap; padding-top: 4px; }
.diary-months > div { display: grid; gap: 5px; text-align: center; min-width: 38px; }
.diary-months span { background: var(--accent-color); border-radius: 4px 4px 0 0; }
.diary-months small { color: var(--text-muted); font-size: var(--fs-caption); }
.notes-date { margin: 8px 0 0; font-size: var(--fs-secondary); color: var(--text-secondary); font-weight: var(--fw-semibold); }
</style>
