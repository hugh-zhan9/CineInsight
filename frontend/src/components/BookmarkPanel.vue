<template>
  <section class="viewing-notes-panel" data-test="bookmark-panel">
    <header class="notes-toolbar">
      <h3>片段书签</h3>
      <button v-if="video" type="button" class="btn-primary" :disabled="!session?.source_version" data-test="bookmark-add" @click="create">记下当前位置</button>
      <input v-model="keyword" aria-label="搜索片段书签" placeholder="搜索片名、笔记或标签" data-test="bookmark-search" />
    </header>
    <p v-if="error" role="alert">{{ error }}</p>
    <p v-if="loading">正在读取书签…</p>
    <p v-else-if="!error && !items.length">还没有片段书签。在视频详情中可以记下当前位置。</p>
    <article v-for="item in items" :key="item.id" class="notes-entry" data-test="bookmark-row">
      <h4>{{ item.title }}</h4><p>{{ item.video_title }} · {{ position(item.start_ms) }}<template v-if="item.end_ms != null"> — {{ position(item.end_ms) }}</template></p>
      <p class="notes-body">{{ item.note }}</p><p>{{ (item.tags || []).join(' · ') }}</p>
      <small v-if="!item.source_available">原片不可用；笔记仍保留</small>
      <div class="notes-toolbar">
        <button type="button" class="btn-secondary" @click="resolve(item)">定位 / 核对原片</button>
        <button type="button" class="btn-secondary" @click="edit(item)">编辑笔记</button>
        <button type="button" class="btn-secondary" @click="remove(item)">删除书签</button>
      </div>
    </article>
    <nav class="notes-toolbar" aria-label="书签分页"><button type="button" :disabled="loading || !cursors.length" @click="previous">上一页</button><button type="button" :disabled="loading || !hasMore" @click="next">下一页</button></nav>
    <div v-if="draft && editorSuspended" class="notes-toolbar" data-test="bookmark-checking"><span>核对原片后可继续编辑，草稿已保留。</span><button type="button" @click="editorSuspended = false">返回书签编辑</button></div>
    <BaseModal v-if="draft && !editorSuspended" :close-on-overlay="false" @close="closeEditor">
      <form class="notes-editor" @submit.prevent="save">
        <fieldset :disabled="saving" class="notes-editor-fields">
        <h3>{{ draft.id ? '编辑片段书签' : '新建片段书签' }}</h3>
        <p v-if="sourceChanged" role="alert">原片内容已改变。请打开当前原片，核对起止位置后再确认；只保存文字不会接受新原片。</p>
        <button v-if="sourceChanged && currentVideo" type="button" @click="previewCurrent">打开当前原片核对</button>
        <label>标题<input v-model="draft.title" required  data-test="bookmark-title" /></label>
        <label>开始（秒）<input v-model="draft.startSeconds" type="number" min="0" step="0.001" required data-test="bookmark-start" /></label>
        <label>结束（秒，留空为时间点）<input v-model="draft.endSeconds" type="number" min="0" step="0.001" data-test="bookmark-end" /></label>
        <label>笔记<textarea v-model="draft.note"  rows="5" /></label>
        <label>标签（每行一个，最多20个）<textarea v-model="draft.tagText" rows="3" /></label>
        <label v-if="sourceChanged"><input v-model="acceptSource" type="checkbox" data-test="bookmark-accept-source" />我已核对当前原片与以上位置</label>
        <p v-if="formError" role="alert">{{ formError }}</p>
        <footer class="notes-toolbar"><button type="submit" class="btn-primary" :disabled="saving" data-test="bookmark-save">{{ saving ? '保存中…' : '保存' }}</button><button type="button" :disabled="saving" @click="closeEditor">取消</button></footer>
        </fieldset>
      </form>
    </BaseModal>
  </section>
</template>
<script>
import { ListVideoBookmarks, SaveVideoBookmark, DeleteVideoBookmark, ResolveVideoBookmark } from '../../wailsjs/go/main/App';
import BaseModal from './ui/BaseModal.vue';
import { confirmAction } from '../utils/feedback.js';
export default {
  name: 'BookmarkPanel', components: { BaseModal },
  props: { pageActive: { type: Boolean, default: true }, video: { type: Object, default: null }, session: { type: Object, default: null }, currentPosition: { type: Function, default: () => 0 } },
  emits: ['open-bookmark', 'open-video'],
  data: () => ({ items: [], keyword: '', cursor: 0, cursors: [], hasMore: false, loading: false, error: '', draft: null, saving: false, formError: '', sourceChanged: false, editorSuspended: false, currentVideo: null, currentToken: '', acceptSource: false, generation: 0, resolveGeneration: 0 }),
  watch: { pageActive(active) { if (active) this.load(); }, keyword() { this.reset(); }, 'video.id'() { this.draft = null; this.resolveGeneration++; this.reset(); } },
  mounted() { this.load(); }, beforeUnmount() { this.generation++; this.resolveGeneration++; this._disposed = true; },
  methods: {
    position(ms) { return `${(Number(ms) / 1000).toFixed(3)} 秒`; },
    reset() { this.cursor = 0; this.cursors = []; this.items = []; this.load(); },
    async load(cursor = this.cursor, history = this.cursors) {
      const generation = ++this.generation; this.loading = true; this.error = '';
      try { const page = await ListVideoBookmarks({ video_id: this.video?.id || 0, keyword: this.keyword, cursor_id: cursor, limit: 50 }); if (generation !== this.generation) return; this.items = page.items || []; this.hasMore = !!page.has_more; this.cursor = cursor; this.cursors = history; return true; }
      catch (err) { if (generation === this.generation) this.error = `读取书签失败：${err}`; }
      finally { if (generation === this.generation) this.loading = false; }
    },
    next() { return this.load(this.items.at(-1)?.id || 0, [...this.cursors, this.cursor]); },
    previous() { return this.load(this.cursors.at(-1) || 0, this.cursors.slice(0, -1)); },
    create() {
      if (!this.video?.id || !this.session?.source_version) return;
      // Capture once when opening the editor; later session refreshes cannot
      // authorize old draft positions against a different source.
      const start = Math.max(0, Math.round(Number(this.currentPosition()) * 1000) || 0);
      this.edit({ video_id: this.video.id, start_ms: start, end_ms: null, title: '', note: '', tags: [], source_token: this.session.source_version });
    },
    edit(item, resolution = null) {
      this.resolveGeneration++; this.editorSuspended = false; this.sourceChanged = resolution?.status === 'source_changed'; this.acceptSource = false;
      this.currentVideo = resolution?.video || null; this.currentToken = resolution?.source_token || ''; this.formError = '';
      this.draft = { id: item.id || 0, revision: item.revision || 0, video_id: item.video_id, title: item.title, note: item.note, startSeconds: item.start_ms / 1000, endSeconds: item.end_ms == null ? '' : item.end_ms / 1000, tagText: (item.tags || []).join('\n'), source_token: item.source_token || '' };
    },
    previewCurrent() {
      this.editorSuspended = true;
      this.$emit('open-video', this.currentVideo);
    },
    closeEditor() { if (!this.saving && this.pageActive) { this.draft = null; this.resolveGeneration++; } },
    async resolve(item) {
      const generation = ++this.resolveGeneration; this.error = '';
      try {
        const result = await ResolveVideoBookmark(item.id); if (generation !== this.resolveGeneration) return;
        if (result.status === 'unavailable') { this.error = '原片不可用，书签内容仍可编辑。'; return; }
        if (result.status === 'source_changed') { this.edit(result.bookmark, result); return; }
        this.$emit('open-bookmark', result);
      } catch (err) { if (generation === this.resolveGeneration) this.error = `核对原片失败：${err}`; }
    },
    async save() {
      const draft = this.draft; if (!draft || this.saving) return;
      const start = Number(draft.startSeconds) * 1000, end = draft.endSeconds === '' ? null : Number(draft.endSeconds) * 1000;
      // Decimal seconds may have sub-ULP floating error; round only when within
      // that representation tolerance, never silently round a finer user cut.
      const validMS = value => Number.isFinite(value) && Number.isSafeInteger(Math.round(value)) && Math.abs(value - Math.round(value)) < 0.00001;
      if (draft.startSeconds === '' || !validMS(start) || start < 0 || (end != null && (!validMS(end) || end <= start))) { this.formError = '请输入毫秒精度的有效起止时间，结束须晚于开始。'; return; }
      this.saving = true; this.formError = '';
      try {
        await SaveVideoBookmark({ id: draft.id, revision: draft.revision, video_id: draft.video_id, title: draft.title, note: draft.note, start_ms: Math.round(start), end_ms: end == null ? null : Math.round(end), tags: draft.tagText.split('\n').map(value => value.trim()).filter(Boolean), source_token: draft.source_token, accept_source_token: this.acceptSource ? this.currentToken : '' });
        if (this._disposed || this.draft !== draft) return;
        this.draft = null; await this.load();
      } catch (err) { this.formError = `保存失败：${err}`; } finally { this.saving = false; }
    },
    async remove(item) {
      if (!await confirmAction({ title: '删除书签', message: '删除这条笔记和位置记录？原视频不会删除。', danger: true })) return;
      try { await DeleteVideoBookmark(item.id, item.revision); await this.load(); } catch (err) { this.error = `删除失败：${err}`; }
    }
  }
};
</script>
<style>
.viewing-notes-panel { min-width: 0; }
.notes-toolbar { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; margin-bottom: 12px; }
.notes-toolbar h3 { margin-right: auto; }
.notes-entry { padding: 16px 0; border-bottom: 1px solid var(--border-color, #8884); overflow-wrap: anywhere; }
.notes-entry h4 { margin: 0 0 6px; }.notes-entry p { margin: 6px 0; }.notes-body { white-space: pre-wrap; }
.notes-editor { width: min(620px, 88vw); padding: 24px; display: grid; gap: 14px; max-height: 85vh; overflow: auto; }
.notes-editor-fields { border: 0; padding: 0; margin: 0; min-width: 0; display: grid; gap: 14px; }
.notes-editor label { display: grid; gap: 6px; }.notes-editor input:not([type=checkbox]),.notes-editor textarea { width: 100%; box-sizing: border-box; }
</style>
