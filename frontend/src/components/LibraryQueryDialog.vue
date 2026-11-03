<template>
  <Teleport to="body">
    <DialogShell title="组合搜索" size="lg" :busy="applying" @close="close">
      <p class="help-text">先核对条件，再搜索当前片库。普通关键词搜索仍可在片库工具栏使用。</p>
      <div class="library-query__mode">
        <label><input v-model="mode" type="radio" value="local" :disabled="busy" />本地解析</label>
        <label><input v-model="mode" type="radio" value="external" :disabled="busy || !status?.query_enabled || !status?.available" />外部 AI 解析</label>
        <button type="button" class="btn btn--ghost btn--sm" :disabled="busy" @click="$emit('settings')">文本能力设置</button>
      </div>
      <p v-if="statusError" class="help-text">{{ statusError }}；仍可使用本地解析。</p>
      <p v-if="mode === 'local'" class="help-text">例如：未看 收藏 120分钟以内 人物 张三 标签 科幻。无法解释的部分会保留供你处理。</p>
      <label class="library-query__input">查询文字<textarea v-model="text" class="textarea-input" maxlength="500" rows="3" :disabled="busy" data-test="library-query-text" /></label>
      <p v-if="mode === 'external'" class="help-text" data-test="library-query-disclosure">点击解析将把上面的查询文字发送给 {{ status?.provider }} 的 {{ status?.model }}。不发送视频、图片、文件路径或片库清单；不会自动重试；取消不能撤回已经发送的文字。</p>
      <button type="button" class="btn btn--secondary" :disabled="busy || !text.trim()" data-test="library-query-parse" @click="parse">{{ busy ? '正在处理…' : '解析条件' }}</button>
      <button v-if="activeRequestID" type="button" class="btn btn--ghost" data-test="library-query-cancel" @click="cancelParsing">取消本次解析</button>
      <p v-if="error" role="alert">{{ error }}</p><p v-if="notice" role="status">{{ notice }}</p>
      <template v-if="draft">
        <h3>核对条件</h3>
        <p v-for="message in draft.notices" :key="message" role="status">{{ message }}</p>
        <ul class="library-query__conditions">
          <li v-for="condition in draft.conditions" :key="condition.condition_id">
            <strong>{{ condition.label }}</strong><span class="help-text">来自“{{ condition.source_text }}”</span>
            <label><input v-model="removed[condition.condition_id]" type="checkbox" :disabled="busy" />移除此条件</label>
            <template v-if="!removed[condition.condition_id]">
              <p v-if="issueFor(condition)?.reason" class="help-text">{{ issueFor(condition).reason }}</p>
              <select v-if="issueFor(condition)?.candidates.length > 1" v-model.number="choices[condition.condition_id].entity_id" class="select-input" :disabled="busy" :aria-label="`选择${condition.value}`">
                <option :value="0">请选择</option><option v-for="candidate in issueFor(condition).candidates" :key="candidate.id" :value="candidate.id">{{ candidate.label }} · {{ candidate.disambiguation }}</option>
              </select>
              <label v-if="condition.requires_relation_acceptance"><input v-model="choices[condition.condition_id].accept_person_relation" type="checkbox" :disabled="busy" />接受按人物关联筛选，不区分出演、导演等角色</label>
            </template>
          </li>
          <li v-for="issue in draft.unsupported" :key="issue.id"><strong>尚不支持：{{ issue.source_text }}</strong><p class="help-text">{{ issue.reason }}</p><label><input v-model="removed[issue.id]" type="checkbox" :disabled="busy" />明确移除这部分要求</label></li>
        </ul>
        <button type="button" class="btn btn--primary" :disabled="busy || !ready" data-test="library-query-search" @click="search(false)">搜索这些条件</button>
      </template>
      <template v-if="result">
        <p>已加载 {{ videos.length }} 部视频 · 按文件筛选</p>
        <p v-if="!videos.length" class="help-text">当前条件没有匹配的视频；可以修改条件后重新搜索。</p>
        <ul class="library-query__results"><li v-for="video in videos" :key="video.id">{{ video.display_title || video.name }}</li></ul>
        <button v-if="result.next_cursor" type="button" class="btn btn--secondary" :disabled="busy" @click="search(true)">加载更多</button>
      </template>
      <template #footer>
        <button type="button" class="btn btn--secondary" :disabled="!result || busy" @click="saveView">保存为视图…</button>
        <button type="button" class="btn btn--primary" :disabled="!result || busy" data-test="library-query-apply" @click="apply">应用到片库</button>
      </template>
    </DialogShell>
    <SaveViewDialog ref="saveView" :current-library-filter="() => savedFilter" :after-saved="afterSaved" />
  </Teleport>
</template>
<script>
import DialogShell from './ui/DialogShell.vue';
import SaveViewDialog from './video-list/SaveViewDialog.vue';
import { notifyError } from '../utils/feedback.js';
import { libraryQueryAPI, queryResolution, queryReady, queryError, newQueryRequestID } from '../utils/libraryQueryParser.js';
export default {
  name: 'LibraryQueryDialog', components: { DialogShell, SaveViewDialog }, emits: ['close', 'settings'],
  props: { api: { type: Object, default: () => libraryQueryAPI }, applyFilter: { type: Function, required: true } },
  data: () => ({ alive: true, statusRequest: 0, activeRequestID: '', text: '', mode: 'local', status: null, statusError: '', draft: null, choices: {}, removed: {}, result: null, videos: [], busy: false, applying: false, generation: 0, error: '', notice: '', savedFilter: null }),
  computed: {
    ready() { return queryReady(this.draft, this.choices, this.removed); },
    decisionSignature() { return JSON.stringify([this.choices, this.removed]); }
  },
  watch: {
    text() { this.reset(); }, mode() { this.reset(); },
    decisionSignature() { this.result = null; this.videos = []; }
  },
  mounted() { this.loadStatus(); },
  beforeUnmount() { this.cancelParsing(); this.alive = false; this.generation++; this.statusRequest++; },
  methods: {
    reset() { if (this.activeRequestID) this.cancelParsing(); this.generation++; this.draft = null; this.result = null; this.videos = []; this.error = ''; this.notice = ''; this.busy = false; },
    close() { this.cancelParsing(); this.$emit('close'); },
    async cancelParsing() {
      const requestID = this.activeRequestID; if (!requestID) return;
      this.activeRequestID = ''; this.generation++; this.busy = false;
      try { await this.api.cancel(requestID); }
      catch { notifyError('取消解析尚未确认；已发送的文字无法撤回，请等待原请求结束。'); }
    },
    async loadStatus() { const ticket = ++this.statusRequest; try { const status = await this.api.status(); if (this.alive && ticket === this.statusRequest) { this.status = status; this.statusError = ''; } } catch (error) { if (this.alive && ticket === this.statusRequest) this.statusError = queryError(error); } },
    issueFor(condition) { return this.draft.unresolved.find(issue => issue.id === condition.condition_id); },
    async parse() {
      if (this.busy || !this.text.trim()) return;
      if (this.mode === 'external' && (!this.status?.query_enabled || !this.status?.available)) return;
      const ticket = ++this.generation; const requestID = newQueryRequestID(); this.activeRequestID = requestID; this.busy = true; this.error = ''; this.notice = ''; this.result = null; this.videos = []; this.draft = null;
      try {
        const draft = await this.api.parse({ request_id: requestID, text: this.text, mode: this.mode, provider_revision: this.mode === 'external' ? this.status.provider_revision : '' });
        if (ticket !== this.generation) return;
        this.choices = Object.fromEntries(draft.conditions.map(condition => [condition.condition_id, { entity_id: condition.entity_id || 0, accept_person_relation: false }])); this.removed = {}; this.draft = draft;
      } catch (error) { if (ticket === this.generation) { this.error = queryError(error); if (['text_ai_config_changed', 'text_ai_disabled'].includes(error?.code)) await this.loadStatus(); } }
      finally { if (this.activeRequestID === requestID) this.activeRequestID = ''; if (ticket === this.generation) this.busy = false; }
    },
    async search(more) {
      if (this.busy || !this.ready || (more && !this.result?.next_cursor)) return;
      const ticket = this.generation; this.busy = true; this.error = '';
      const decisions = this.decisionSignature;
      const request = { ...queryResolution(this.draft, this.choices, this.removed), cursor: more ? this.result.next_cursor : null, limit: 30 };
      try {
        const result = await this.api.search(request); if (ticket !== this.generation || decisions !== this.decisionSignature) return;
        const previous = more ? this.videos : []; const seen = new Set(previous.map(video => video.id));
        this.videos = [...previous, ...result.page.videos.filter(video => !seen.has(video.id))]; this.result = result;
      } catch (error) { if (ticket === this.generation) { this.error = queryError(error); if (!more) { this.result = null; this.videos = []; } } }
      finally { if (ticket === this.generation) this.busy = false; }
    },
    saveView() { if (!this.result || this.busy) return; this.savedFilter = { ...this.result.filter, person_ids: [...(this.result.filter.person_ids || [])], tag_ids: [...(this.result.filter.tag_ids || [])] }; this.$refs.saveView.open(); },
    async afterSaved(view) { this.notice = `已保存视图“${view.name}”`; },
    async apply() {
      if (!this.result || this.busy) return;
      const ticket = this.generation; this.busy = true; this.applying = true; this.error = '';
      try { if (await this.applyFilter(this.result.filter, () => ticket === this.generation) !== false && ticket === this.generation) this.$emit('close'); }
      catch (error) { if (ticket === this.generation) this.error = queryError(error); }
      finally { if (ticket === this.generation) { this.busy = false; this.applying = false; } }
    }
  }
};
</script>
<style scoped>
.library-query__mode { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-3); }
.library-query__input { display: grid; gap: var(--space-2); margin-block: var(--space-3); }
.library-query__conditions { list-style: none; padding: 0; }
.library-query__conditions li { display: grid; gap: var(--space-2); padding-block: var(--space-3); border-bottom: 1px solid var(--hairline); }
.library-query__results { max-height: 20vh; overflow: auto; }
</style>
