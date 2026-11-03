<template>
  <section class="clip-topics" data-test="clip-topics">
    <div class="notes-toolbar"><h3>片段专题</h3><button type="button" class="btn btn--primary" :disabled="busy" @click="editor = { id: 0, expected_revision: 0, name: '', note: '' }">新建专题</button><button type="button" class="btn btn--secondary" :disabled="busy" @click="$emit('browse-bookmarks')">去书签添加片段</button></div>
    <p v-if="error" role="alert">{{ error }}</p><p v-if="notice" role="status">{{ notice }}</p>
    <div class="clip-topics__layout">
      <aside aria-label="专题列表"><p v-if="!topics.length && !listLoading" class="help-text">还没有专题。专题保存书签的关联与顺序，不复制原片。</p><button v-for="item in topics" :key="item.topic.id" type="button" class="btn btn--secondary clip-topics__choice" :disabled="busy" :aria-pressed="selected?.topic.id === item.topic.id" @click="selectTopic(item)">{{ item.topic.name }} · {{ item.member_count }} 项</button><button v-if="topicCursor" type="button" class="btn btn--ghost" :disabled="listLoading || busy" @click="loadTopics(false)">更多专题</button></aside>
      <div v-if="selected" class="clip-topics__detail">
        <h3>{{ selected.topic.name }}</h3><p class="clip-topics__note">{{ selected.topic.note }}</p>
        <div class="notes-toolbar"><button type="button" class="btn btn--secondary btn--sm" :disabled="busy" @click="editTopic">编辑专题</button><button type="button" class="btn btn--secondary btn--sm" :disabled="busy || membersLoading" @click="refreshTopic">刷新成员</button><button type="button" class="btn btn--danger btn--sm" :disabled="busy" @click="deleteTopic">删除专题…</button></div>
        <p class="help-text">共 {{ selected.member_count }} 条书签，已选 {{ selectedIDs.length }} 条。时间点可以整理进专题，预览前需补充结束时间。</p>
        <div class="notes-toolbar"><button type="button" class="btn btn--primary" :disabled="busy || !selectedIDs.length || selectedIDs.length > 1000" data-test="clip-preview-selected" @click="prepare(false)">预览所选</button><button type="button" class="btn btn--secondary" :disabled="busy || !selected.member_count || selected.member_count > 1000" data-test="clip-preview-all" @click="prepare(true)">预览整个专题</button></div>
        <p v-if="selected.member_count > 1000 || selectedIDs.length > 1000" class="help-text">一次最多预览 1000 段，请分批选择。</p>
        <ol class="clip-topics__members"><li v-for="(member,index) in members" :key="member.bookmark.id">
          <label><input type="checkbox" :checked="selectedIDs.includes(member.bookmark.id)" :disabled="busy" @change="selectMember(member.bookmark.id,$event.target.checked)" /><strong>{{ member.bookmark.title }}</strong></label>
          <span>{{ member.bookmark.video_title }} · {{ time(member.bookmark.start_ms) }}<template v-if="member.bookmark.end_ms != null"> — {{ time(member.bookmark.end_ms) }}</template><template v-else> · 尚未填写结束时间</template></span>
          <small v-if="!member.bookmark.source_available">原片不可用，书签内容保留</small>
          <div class="notes-toolbar"><button type="button" class="btn btn--secondary btn--sm" :disabled="busy" @click="$refs.bookmarkEditor.edit(member.bookmark)">编辑时间与笔记</button><button type="button" class="btn btn--ghost btn--sm" :disabled="busy || index === 0" @click="editMembers('move',[member.bookmark.id],members[index-1].bookmark.id)">上移</button><button type="button" class="btn btn--ghost btn--sm" :disabled="busy || index === members.length-1 || (memberCursor && index >= members.length-2)" @click="moveDown(index)">下移</button><button type="button" class="btn btn--ghost btn--sm" :disabled="busy" @click="editMembers('remove',[member.bookmark.id])">移出专题</button></div>
        </li></ol>
        <p v-if="!members.length && !membersLoading" class="help-text">专题还是空的，可从片段书签的「加入专题」添加。</p>
        <button v-if="memberCursor" type="button" class="btn btn--secondary" :disabled="busy || membersLoading" @click="loadMembers(false)">加载更多成员</button>
      </div>
    </div>
    <Teleport v-if="editor && pageActive" to="body"><DialogShell :title="editor.id ? '编辑专题' : '新建专题'" :busy="busy" @close="editor = null"><label>名称<input v-model="editor.name" :disabled="busy" class="text-input" maxlength="100" aria-label="专题名称" /></label><label>说明<textarea v-model="editor.note" :disabled="busy" class="textarea-input" rows="4" maxlength="5000" /></label><p v-if="editorError" role="alert">{{ editorError }}</p><template #footer><button type="button" class="btn btn--primary" :disabled="busy || !editor.name.trim()" data-test="clip-topic-save" @click="saveTopic">保存</button></template></DialogShell></Teleport>
    <BookmarkPanel ref="bookmarkEditor" editor-only :page-active="pageActive" :clip-api="api" @saved="loadMembers(true)" @open-video="$emit('open-video',$event)" @open-bookmark="$emit('open-bookmark',$event)" />
    <ClipQueuePreviewDialog v-if="preview && pageActive" :key="preview.preview_token || generation" :preview="preview" :api="api" @close="preview = null" @queued="queued" />
  </section>
</template>
<script>
import BookmarkPanel from './BookmarkPanel.vue';
import ClipQueuePreviewDialog from './ClipQueuePreviewDialog.vue';
import DialogShell from './ui/DialogShell.vue';
import { confirmAction } from '../utils/feedback.js';
import { clipTopicAPI, clipError, clipTime } from '../utils/clipTopics.js';
export default {
  name: 'ClipTopicsPanel', components: { BookmarkPanel, ClipQueuePreviewDialog, DialogShell }, emits: ['browse-bookmarks','open-video','open-bookmark'],
  props: { pageActive: { type: Boolean, default: true }, api: { type: Object, default: () => clipTopicAPI } },
  data: () => ({ topics: [], topicCursor: 0, selected: null, members: [], memberCursor: null, selectedIDs: [], editor: null, editorError: '', preview: null, listLoading: false, membersLoading: false, busy: false, error: '', notice: '', generation: 0, listRequest: 0, memberRequest: 0, alive: true }),
  watch: { pageActive(active) { if (active) { this.loadTopics(true); if (this.selected) this.notice = '返回后可刷新成员，重新核对最新资料。'; } else { this.generation++; this.listRequest++; this.memberRequest++; this.listLoading = false; this.membersLoading = false; this.preview = null; } } },
  mounted() { if (this.pageActive) this.loadTopics(true); }, beforeUnmount() { this.alive = false; this.generation++; },
  methods: {
    time: clipTime,
    async loadTopics(reset) {
      if (!this.pageActive) return;
      const request = ++this.listRequest; this.listLoading = true;
      try { const result = await this.api.list({ cursor_id: reset ? 0 : this.topicCursor, limit: 50 }); if (!this.alive || request !== this.listRequest) return; this.topics = reset ? result.items : [...this.topics,...result.items]; this.topicCursor = result.next_cursor; }
      catch (error) { if (this.alive && request === this.listRequest) this.error = clipError(error); }
      finally { if (this.alive && request === this.listRequest) this.listLoading = false; }
    },
    async selectTopic(item) { if (!this.pageActive || this.busy) return; this.generation++; this.selected = item; this.selectedIDs = []; this.members = []; this.memberCursor = null; this.notice = ''; await this.loadMembers(true); },
    async loadMembers(reset) {
      if (!this.pageActive || !this.selected) return;
      const ticket = this.generation, request = ++this.memberRequest, id = this.selected.topic.id; this.membersLoading = true; this.error = '';
      try {
        const page = await this.api.members({ topic_id: id, revision: reset ? 0 : this.selected.topic.revision, cursor_position: reset ? 0 : this.memberCursor?.position || 0, cursor_bookmark_id: reset ? 0 : this.memberCursor?.id || 0, limit: 50 });
        if (!this.alive || ticket !== this.generation || request !== this.memberRequest) return;
        this.selected = page.topic; this.members = reset ? page.items : [...this.members,...page.items]; this.memberCursor = page.cursor_bookmark_id ? { id: page.cursor_bookmark_id, position: page.cursor_position } : null;
      } catch (error) { if (this.alive && ticket === this.generation) this.error = clipError(error); }
      finally { if (this.alive && ticket === this.generation && request === this.memberRequest) this.membersLoading = false; }
    },
    async refreshTopic() { const ticket = this.generation; if (!this.pageActive || this.busy) return; if (this.selectedIDs.length && !await confirmAction({ title: '刷新专题成员', message: '刷新会清空本次尚未入队的选择。', confirmText: '刷新' })) return; if (!this.pageActive || ticket !== this.generation) return; this.selectedIDs = []; this.generation++; await this.loadMembers(true); },
    selectMember(id, selected) { this.selectedIDs = selected ? [...new Set([...this.selectedIDs,id])] : this.selectedIDs.filter(value => value !== id); },
    editTopic() { this.editorError = ''; this.editor = { id: this.selected.topic.id, expected_revision: this.selected.topic.revision, name: this.selected.topic.name, note: this.selected.topic.note }; },
    async saveTopic() {
      if (!this.pageActive || !this.editor || this.busy) return; const draft = this.editor, ticket = this.generation; this.busy = true; this.editorError = '';
      try { const result = await this.api.save({ ...draft }); if (!this.alive) return; if (this.editor === draft) this.editor = null; if (ticket !== this.generation) return; if (this.selected?.topic.id !== result.topic.id) this.selectedIDs = []; this.selected = result; this.generation++; await this.loadMembers(true); await this.loadTopics(true); }
      catch (error) { if (this.alive) this.editorError = clipError(error); }
      finally { if (this.alive) this.busy = false; }
    },
    async editMembers(action,ids,before = 0) {
      if (!this.pageActive || !this.selected || this.busy) return; const selected = this.selected, ticket = this.generation; this.busy = true; this.error = '';
      try { const result = await this.api.edit({ topic_id: selected.topic.id, expected_revision: selected.topic.revision, action, bookmark_ids: [...ids], before_bookmark_id: before }); if (!this.alive || ticket !== this.generation) return; this.selected = result; if (action === 'remove') this.selectedIDs = this.selectedIDs.filter(id => !ids.includes(id)); await this.loadMembers(true); await this.loadTopics(true); }
      catch (error) { if (this.alive && ticket === this.generation) this.error = clipError(error); }
      finally { if (this.alive) this.busy = false; }
    },
    moveDown(index) { const after = this.members[index+2]?.bookmark.id || 0; return this.editMembers('move',[this.members[index].bookmark.id],after); },
    async deleteTopic() {
      const selected = this.selected, ticket = this.generation; if (!this.pageActive || !selected || this.busy) return;
      if (!await confirmAction({ title: '删除专题', message: `删除“${selected.topic.name}”及其中 ${selected.member_count} 个关联？书签和原视频都保留。`, confirmText: '删除专题', danger: true }) || !this.alive || ticket !== this.generation) return;
      this.busy = true; this.error = '';
      try { await this.api.remove(selected.topic.id,selected.topic.revision); if (!this.alive || ticket !== this.generation) return; this.selected = null; this.members = []; this.selectedIDs = []; await this.loadTopics(true); }
      catch (error) { if (this.alive && ticket === this.generation) this.error = clipError(error); }
      finally { if (this.alive) this.busy = false; }
    },
    async prepare(all) {
      if (!this.pageActive || this.busy || !this.selected || (!all && !this.selectedIDs.length)) return;
      const selected = this.selected, ids = [...this.selectedIDs], ticket = this.generation; this.busy = true; this.error = '';
      try { const queue = await this.api.queue(); if (!this.alive || ticket !== this.generation) return; const result = await this.api.preview({ topic_id: selected.topic.id, topic_revision: selected.topic.revision, bookmark_ids: all ? [] : ids, all, queue_revision: queue.state.revision }); if (this.alive && ticket === this.generation) this.preview = result; else if (result.preview_token) this.api.discard(result.preview_token).catch(() => {}); }
      catch (error) { if (this.alive && ticket === this.generation) this.error = clipError(error); }
      finally { if (this.alive) this.busy = false; }
    },
    queued(result) { this.preview = null; this.notice = result.warning || `已加入 ${result.entry_ids.length} 个预览片段，可在主窗口底部控制播放。`; }
  }
};
</script>
<style scoped>
.clip-topics__layout { display: grid; grid-template-columns: minmax(160px, 240px) minmax(0, 1fr); gap: var(--space-4); }
.clip-topics__choice { display: block; width: 100%; margin-bottom: var(--space-2); text-align: left; white-space: normal; }
.clip-topics__members { list-style: none; padding: 0; }
.clip-topics__members li { display: grid; gap: var(--space-2); padding-block: var(--space-3); border-bottom: 1px solid var(--hairline); }
.clip-topics__note { white-space: pre-wrap; overflow-wrap: anywhere; }
@media (max-width: 700px) { .clip-topics__layout { grid-template-columns: 1fr; } }
</style>
