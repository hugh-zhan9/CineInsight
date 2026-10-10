<template>
  <BaseModal :close-on-overlay="false" @close="close">
    <form class="notes-editor" @submit.prevent="save" data-test="diary-editor">
      <fieldset :disabled="saving" class="notes-editor-fields">
      <h3>{{ entry?.id ? '修订观影日记' : '手动补记' }}</h3>
      <p v-if="entry?.origin && entry.origin !== 'manual'">保留原始观看事实；日期、评分与短评可以修订。</p>
      <template v-if="!entry?.id">
        <label>记录来源<select v-model="mode" :disabled="saving" data-test="diary-source"><option value="manual">手填片名</option><option value="library">选择片库视频</option></select></label>
        <div v-if="mode === 'library'">
          <div class="notes-toolbar"><input v-model="keyword" aria-label="查找片库视频" placeholder="输入片名或文件名" :disabled="saving" /><button type="button" :disabled="searching || saving" @click="search(false)">查找</button></div>
          <p v-if="searchError" role="alert">{{ searchError }}</p>
          <p v-if="searched && !searching && !searchError && !videos.length">没有找到视频</p>
          <button v-for="video in videos" :key="video.id" type="button" class="diary-video-choice" :disabled="saving" :aria-pressed="selected?.id === video.id" @click="selected = video">{{ video.display_title || video.name }}</button>
          <button v-if="nextCursor" type="button" :disabled="searching || saving" @click="search(true)">下一页视频</button>
          <p v-if="selected">已选择：{{ selected.display_title || selected.name }}</p>
        </div>
      </template>
      <label v-if="entry?.id || mode === 'manual'">片名<input v-model="title" required  data-test="diary-title" /></label>
      <label>观看日期<input v-model="date" type="date" required data-test="diary-date" /></label>
      <label>本次评分（可不填，0–10）<input v-model="rating" type="number" min="0" max="10" step="0.5" data-test="diary-rating" /></label>
      <label>短评<textarea v-model="note" rows="6"  data-test="diary-note" /></label>
      <p v-if="error" role="alert">{{ error }}</p>
      <footer class="notes-toolbar"><button type="submit" class="btn-primary" :disabled="saving" data-test="diary-save">{{ saving ? '保存中…' : '保存日记' }}</button><button type="button" :disabled="saving" @click="close">取消</button></footer>
      </fieldset>
    </form>
  </BaseModal>
</template>
<script>
import { SaveViewingDiary, SearchLibraryVideoPage } from '../../wailsjs/go/main/App';
import BaseModal from './ui/BaseModal.vue';
export default {
  name: 'DiaryEditor', components: { BaseModal }, props: { entry: { type: Object, default: null }, active: { type: Boolean, default: true } }, emits: ['close', 'saved'],
  data() { return { mode: 'manual', title: this.entry?.title || '', date: this.entry?.watched_on || '', rating: this.entry?.rating ?? '', note: this.entry?.note || '', selected: null, keyword: '', videos: [], nextCursor: null, searched: false, searching: false, searchError: '', searchGeneration: 0, saving: false, error: '' }; },
  beforeUnmount() { this.searchGeneration++; this._disposed = true; },
  methods: {
    close() { if (!this.saving && this.active) this.$emit('close'); },
    async search(next) {
      const generation = ++this.searchGeneration; this.searching = true; this.searchError = ''; this.searched = true;
      const keyword = this.keyword;
      // A changed query starts from its own first page.
      const cursor = next && this._searchedKeyword === keyword ? this.nextCursor : null;
      try { const page = await SearchLibraryVideoPage({ filter: { keyword, search_mode: 'file', sort_mode: 'balanced' }, cursor, limit: 20 }); if (generation !== this.searchGeneration) return; this.videos = page.videos || []; this.nextCursor = page.next_cursor || null; this._searchedKeyword = keyword; }
      catch (err) { if (generation === this.searchGeneration) this.searchError = `查找失败：${err}`; }
      finally { if (generation === this.searchGeneration) this.searching = false; }
    },
    async save() {
      if (this.saving) return;
      if (!this.entry?.id && this.mode === 'library' && !this.selected) { this.error = '请先选择片库视频。'; return; }
      const rating = this.rating === '' ? null : Number(this.rating);
      if (!this.date || (rating != null && (!Number.isFinite(rating) || rating < 0 || rating > 10 || !Number.isInteger(rating * 2)))) { this.error = '请填写有效观看日期；评分须为0–10的半分值。'; return; }
      this.saving = true; this.error = '';
      try {
        const result = await SaveViewingDiary({ id: this.entry?.id || 0, revision: this.entry?.revision || 0, video_id: this.entry?.id ? (this.entry.video_id ?? null) : (this.mode === 'library' ? this.selected.id : null), title: this.title, watched_on: this.date, rating, note: this.note });
        if (!this._disposed) this.$emit('saved', result);
      } catch (err) { if (!this._disposed) this.error = `保存失败：${err}`; } finally { this.saving = false; }
    }
  }
};
</script>
<style scoped>.diary-video-choice { display: block; width: 100%; text-align: left; padding: 8px; margin: 4px 0; }.diary-video-choice[aria-pressed=true] { outline: 2px solid var(--accent-color, #408479); }</style>
