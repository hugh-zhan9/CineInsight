<template>
  <main class="viewing-notes-page" data-test="viewing-notes-page">
    <header class="notes-toolbar"><h2>观看笔记</h2><button type="button" class="btn-primary" data-test="diary-add" @click="editor = {}">手动补记</button></header>
    <nav class="notes-toolbar" aria-label="观看笔记分类"><button v-for="item in tabs" :key="item.key" type="button" :aria-pressed="tab === item.key" :data-test="`notes-tab-${item.key}`" @click="tab = item.key">{{ item.label }}</button></nav>
    <BookmarkPanel v-if="tab === 'bookmarks'" :page-active="pageActive" @open-bookmark="$emit('open-bookmark', $event)" @open-video="$emit('open-video', $event)" />
    <template v-else>
      <div class="notes-toolbar"><label v-if="tab === 'diary'">年份 <input v-model.number="year" type="number" min="1" max="9999" aria-label="日记年份" /></label><input v-model="keyword" aria-label="搜索观看记录" placeholder="搜索片名或短评" /><button type="button" :disabled="loading" @click="reset">刷新</button></div>
      <template v-if="tab === 'diary'">
        <p>每次有效观看独立记录，按观看日期汇总；记录数不代表完整看完的电影数。手动补记与自动记录各自保留。</p>
        <p>第三方播放器只有提供独立、可靠的观看会话才能自动记入；无法确认的重看可手动补记。</p>
        <p v-if="reviewError" role="alert">{{ reviewError }}</p>
        <section v-if="review" class="diary-recap" aria-label="年度回顾" data-test="diary-recap">
          <p>{{ review.year }}年 · {{ review.total }}条观看记录 · {{ review.days }}个观看日 · 自动{{ review.automatic }} / 历史{{ review.historical }} / 手动{{ review.manual }}</p>
          <p>{{ review.rated_count }}条已评分 · 平均分{{ review.average_rating == null ? '暂无' : Number(review.average_rating).toFixed(1) }}</p>
          <div class="diary-months"><div v-for="(count, index) in review.months" :key="index"><span :style="{ height: `${Math.max(2, 64 * count / Math.max(1, ...review.months))}px` }"></span><small>{{ index + 1 }}月 {{ count }}</small></div></div>
          <details v-if="review.most_watched?.length"><summary>记录最多的影片</summary><ol><li v-for="(item, index) in review.most_watched" :key="index">{{ item.title }} · {{ item.count }}次</li></ol></details>
        </section>
      </template>
      <p v-else>这里仅能确认曾启动播放，无法据此推算观看时长。补记时请确认观看日期；原始播放记录仍保留。</p>
      <p v-if="error" role="alert">{{ error }}</p><p v-if="loading">正在读取…</p><p v-else-if="!error && !items.length">当前条件没有记录</p>
      <template v-for="(item, index) in items" :key="item.id">
        <h3 v-if="tab === 'diary' && (index === 0 || item.watched_on !== items[index - 1].watched_on)">{{ item.watched_on }}</h3>
        <article class="notes-entry" :data-test="tab === 'diary' ? 'diary-row' : 'playback-history-row'">
          <h4>{{ item.title }}</h4>
          <template v-if="tab === 'diary'"><p>{{ origins[item.origin] || item.origin }} · {{ item.rating == null ? '未评分' : `${item.rating}分` }}<span v-if="item.date_basis === 'imported_local'"> · 历史日期按导入时本地时区解释</span></p><p class="notes-body">{{ item.note }}</p></template>
          <p v-else>{{ formatDate(item.played_at) }} · {{ sourceName(item.source) }}</p>
          <p v-if="item.video_id && !item.source_available">原片不可用，记录仍保留</p>
          <div class="notes-toolbar">
            <button v-if="item.video_id && item.source_available" type="button" @click="$emit('open-video-id', item.video_id)">打开原片</button>
            <button type="button" @click="editor = tab === 'diary' ? item : { title: item.title }">{{ tab === 'diary' ? '修订日记' : '手动补记' }}</button>
            <button v-if="tab === 'diary'" type="button" @click="remove(item)">删除日记</button>
          </div>
        </article>
      </template>
      <nav class="notes-toolbar" aria-label="记录分页"><button type="button" :disabled="loading || !cursors.length" @click="previous">上一页</button><button type="button" :disabled="loading || !hasMore" @click="next">下一页</button></nav>
    </template>
    <DiaryEditor v-if="editor" :key="editor.id || 'new'" :entry="editor" :active="pageActive" @close="editor = null" @saved="saved" />
  </main>
</template>
<script>
import { ListViewingDiary, DeleteViewingDiary, GetViewingYearReview, ListUnconfirmedPlaybackHistory } from '../../wailsjs/go/main/App';
import BookmarkPanel from './BookmarkPanel.vue';
import DiaryEditor from './DiaryEditor.vue';
import { confirmAction } from '../utils/feedback.js';
export default {
  name: 'ViewingNotesPage', props: { pageActive: { type: Boolean, default: true } }, components: { BookmarkPanel, DiaryEditor }, emits: ['open-bookmark', 'open-video', 'open-video-id'],
  data: () => ({ tabs: [{ key: 'diary', label: '观影日记' }, { key: 'bookmarks', label: '片段书签' }, { key: 'history', label: '播放记录' }], origins: { automatic: '自动记录', historical: '历史观看', manual: '手动补记' }, tab: 'diary', year: new Date().getFullYear(), keyword: '', items: [], cursor: null, cursors: [], hasMore: false, loading: false, error: '', review: null, reviewError: '', editor: null, generation: 0, reviewGeneration: 0 }),
  watch: { pageActive(active) { if (active) { this.load(); this.loadReview(); } }, tab() { this.reset(); }, year() { this.reset(); }, keyword() { this.reset(); } },
  mounted() { this.reset(); }, beforeUnmount() { this.generation++; this.reviewGeneration++; this._disposed = true; },
  methods: {
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
.viewing-notes-page { padding: 24px; overflow: auto; width: 100%; box-sizing: border-box; }.diary-recap { padding: 16px; background: var(--surface-hover, #8881); border-radius: 12px; margin-bottom: 16px; }.diary-months { display: flex; gap: 10px; align-items: end; flex-wrap: wrap; }.diary-months > div { display: grid; gap: 5px; text-align: center; min-width: 38px; }.diary-months span { background: var(--accent-color, #468c7d); border-radius: 4px 4px 0 0; }
</style>
