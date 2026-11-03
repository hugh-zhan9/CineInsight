<template>
  <section class="episode-acquisitions" data-test="episode-acquisitions">
    <header><h3>补集来源</h3><button type="button" class="btn btn--ghost btn--sm" :disabled="busy || loading" @click="refresh">刷新来源</button></header>
    <p v-if="error" role="alert">{{ error }}</p><p v-if="notice" role="status">{{ notice }}</p>
    <p v-if="loading" role="status">正在读取来源…</p>
    <p v-else-if="!items.length && !error" class="help-text">还没有从这里发起的补集下载。</p>
    <ul class="episode-acquisitions__list">
      <li v-for="item in items" :key="item.origin_id">
        <strong>{{ item.title }}</strong><span>{{ item.collection_title }} · {{ states[item.state] }}</span>
        <p v-if="item.reason" class="help-text">{{ item.reason }}</p>
        <div v-if="item.can_associate" class="episode-acquisitions__mapping">
          <label><input type="checkbox" :checked="selected.includes(item.origin_id)" :disabled="busy || loading" @change="select(item, $event)" />核对此文件</label>
          <label>季 <input v-model="drafts[item.origin_id].season" type="number" class="number-input" min="0" max="999" :disabled="busy" @input="dirty = true" /></label>
          <label>集 <input v-model="drafts[item.origin_id].episode" type="number" class="number-input" min="1" max="1000000" :disabled="busy" @input="dirty = true" /></label>
        </div>
      </li>
    </ul>
    <p v-if="items.some(item => item.can_associate)" class="help-text">未确定的季集请明确填写；同一批按一次下载、一个作品集核对。已有同集时不会自动加入新版本。</p>
    <button v-if="cursor" type="button" class="btn btn--secondary btn--sm" :disabled="busy || loading" @click="load(false)">加载更多来源</button>
    <button v-if="selected.length" type="button" class="btn btn--primary" :disabled="busy || loading" data-test="episode-association-preview" @click="prepare">预览关联 {{ selected.length }} 个文件</button>
    <Teleport to="body"><DialogShell v-if="preview" title="核对作品集关联" size="lg" :busy="busy" @close="preview = null">
      <p>{{ preview.collection_title }} · {{ preview.items.length }} 个文件；确认只会修改以下映射。</p>
      <ul><li v-for="item in preview.items" :key="item.origin_id">
        {{ item.title }} → 第 {{ item.season }} 季第 {{ item.episode }} 集
        <span v-if="item.already_linked">（已经关联）</span>
        <p v-if="item.different_from_request" class="help-text">与最初请求的集号不同，请核对文件内容。</p>
        <p v-if="item.message" role="alert">{{ item.message }}</p>
      </li></ul>
      <p v-if="preview.conflicts" role="alert">有 {{ preview.conflicts }} 项需要重新核对，本次不会部分提交。</p>
      <template #footer><button type="button" class="btn btn--secondary" :disabled="busy" @click="preview = null">返回核对</button><button type="button" class="btn btn--primary" :disabled="busy || !preview.preview_token || preview.conflicts > 0" data-test="episode-association-confirm" @click="confirm">确认关联</button></template>
    </DialogShell></Teleport>
  </section>
</template>
<script>
import DialogShell from './ui/DialogShell.vue';
import { confirmAction } from '../utils/feedback.js';
import { episodeAPI, episodeError, episodeNumber, EPISODE_SOURCE_STATES } from '../utils/episodeAcquisition.js';
export default {
  name: 'EpisodeAcquisitionPanel', components: { DialogShell }, emits: ['associated'],
  props: { collectionId: { type: Number, default: 0 }, taskId: { type: String, default: '' }, api: { type: Object, default: () => episodeAPI } },
  data: () => ({ states: EPISODE_SOURCE_STATES, items: [], selected: [], drafts: {}, cursor: null, loading: false, busy: false, error: '', notice: '', dirty: false, preview: null, generation: 0 }),
  computed: { identity() { return `${this.collectionId}|${this.taskId}`; } },
  watch: { identity: { immediate: true, handler() { this.notice = ''; this.load(true); } } },
  beforeUnmount() { this.generation++; },
  methods: {
    async refresh() { if ((this.dirty || this.selected.length) && !await confirmAction({ title: '刷新补集来源', message: '刷新会清空尚未确认的季集输入。', confirmText: '刷新' })) return; await this.load(true); },
    async load(reset) {
      const ticket = reset ? ++this.generation : this.generation;
      if (reset) { this.items = []; this.selected = []; this.drafts = {}; this.cursor = null; this.preview = null; this.dirty = false; this.busy = false; }
      if (!this.collectionId && !this.taskId) return;
      this.loading = true; this.error = '';
      try {
        const page = await this.api.list({ collection_id: this.collectionId, task_id: this.taskId, cursor: reset ? null : this.cursor, limit: 50 });
        if (ticket !== this.generation) return;
        const known = new Set(this.items.map(item => item.origin_id));
        const fresh = page.items.filter(item => !known.has(item.origin_id));
        this.items = [...this.items, ...fresh];
        for (const item of fresh) this.drafts[item.origin_id] = { season: item.season ?? '', episode: item.episode ?? '' };
        this.cursor = page.next_cursor;
      } catch (error) { if (ticket === this.generation) this.error = episodeError(error); }
      finally { if (ticket === this.generation) this.loading = false; }
    },
    select(item, event) {
      if (!event.target.checked) { this.selected = this.selected.filter(id => id !== item.origin_id); return; }
      const first = this.items.find(item => item.origin_id === this.selected[0]);
      if (first && (first.task_id !== item.task_id || first.collection_id !== item.collection_id)) { event.target.checked = false; this.error = '请按一次下载、一个作品集分别核对'; return; }
      this.selected = [...this.selected, item.origin_id];
    },
    async prepare() {
      if (this.busy || this.loading || !this.selected.length) return;
      const items = this.items.filter(item => this.selected.includes(item.origin_id));
      const choices = items.map(item => ({ file_id: item.file_id, origin_id: item.origin_id, expected_origin_revision: item.revision, season: episodeNumber(this.drafts[item.origin_id].season, 999), episode: episodeNumber(this.drafts[item.origin_id].episode, 1000000, 1) }));
      if (choices.length > 200 || choices.some(item => item.season === null || item.episode === null)) { this.error = '一次最多核对 200 个文件，请为选中文件明确填写有效的季和集'; return; }
      const ticket = this.generation; this.busy = true; this.error = '';
      try { const preview = await this.api.preview({ task_id: items[0].task_id, items: choices }); if (ticket === this.generation) this.preview = preview; }
      catch (error) { if (ticket === this.generation) this.error = episodeError(error); }
      finally { if (ticket === this.generation) this.busy = false; }
    },
    async confirm() {
      if (this.busy || !this.preview?.preview_token || this.preview.conflicts) return;
      const ticket = this.generation; this.busy = true; this.error = '';
      try {
        const result = await this.api.confirm(this.preview.preview_token);
        if (ticket !== this.generation) return;
        this.notice = `已关联 ${result.linked_count} 部视频`; this.preview = null; this.selected = []; this.dirty = false;
        this.$emit('associated', result); await this.load(true);
      } catch (error) { if (ticket === this.generation) { this.error = episodeError(error); this.preview = null; } }
      finally { if (ticket === this.generation) this.busy = false; }
    }
  }
};
</script>
<style scoped>
.episode-acquisitions { margin-block: var(--space-4); padding: var(--space-3); border: 1px solid var(--hairline); border-radius: var(--radius); }
.episode-acquisitions header { display: flex; align-items: center; justify-content: space-between; gap: var(--space-3); }
.episode-acquisitions h3 { margin: 0; font-size: var(--fs-section); }
.episode-acquisitions__list { list-style: none; padding: 0; }
.episode-acquisitions__list li { display: grid; gap: var(--space-2); padding-block: var(--space-3); border-bottom: 1px solid var(--hairline); overflow-wrap: anywhere; }
.episode-acquisitions__mapping { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-3); }
.episode-acquisitions__mapping input[type=number] { width: 6rem; }
</style>
