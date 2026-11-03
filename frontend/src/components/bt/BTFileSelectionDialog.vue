<template>
  <Teleport v-if="visible" to="body">
    <DialogShell title="选择下载文件" size="lg" :busy="busy" :close-on-overlay="false" @close="close">
      <p v-if="localError || error" class="bt-files-error" role="alert">{{ localError || error }}</p>
      <div v-if="episodeOrigin" class="help-text" data-test="bt-episode-origin">
        <strong>补集：{{ episodeOrigin.collection_title }}</strong>
        <details><summary>{{ episodeOrigin.season === 0 ? '特别篇' : `第 ${episodeOrigin.season} 季` }} · {{ episodeOrigin.targets.length }} 个目标集号</summary>{{ episodeOrigin.targets.join('、') }}</details>
        <p>请手动选择文件。季集映射可以先留空，入库后仍需核对关联；不会自动替换已有同集。</p>
      </div>
      <p v-if="!ready" class="help-text" role="status">{{ preparation === 'metadata_pending' ? '正在准备文件清单，尚未开始下载影片。' : '文件清单尚未就绪。' }}</p>
      <template v-else>
        <p v-if="expired" class="bt-files-error" role="alert" data-test="bt-files-expired">{{ Number.isFinite(expiryMS) ? '文件清单已过期，请刷新清单后重新选择。' : '文件清单有效期无效，请刷新清单。' }}</p>
        <p><strong>{{ draft.title }}</strong></p>
        <p class="help-text bt-files-root">保存位置：{{ draft.save_root || '确认后由下载配置确定' }}。完成后原位加入片库，保留文件名和目录继续做种。</p>
        <div class="bt-files-actions"><button type="button" class="btn btn--secondary btn--sm" :disabled="busy || readonly || expired" data-test="bt-files-all" @click="selectAll">全选</button><button type="button" class="btn btn--secondary btn--sm" :disabled="busy || readonly || expired" data-test="bt-files-none" @click="selected = []">清空</button><span data-test="bt-files-total">已选 {{ selected.length }} 项 · {{ bytes(total) }}</span></div>
        <ul class="bt-files-list"><li v-for="file in shownFiles" :key="file.id">
          <label><input v-model="selected" type="checkbox" :value="String(file.id)" :disabled="busy || readonly || expired || !selectable(file)" :data-test="`bt-file-${file.id}`" /><span class="bt-file-path">{{ file.relative_path ?? file.path }}</span><span class="help-text">{{ file.padding ? '占位文件' : bytes(file.size) }}</span></label>
          <div v-if="episodeOrigin && file.kind === 'video' && selected.includes(String(file.id))" class="bt-episode-choice">
            <label>季（可留空）<input v-model="episodeChoices[file.id].season" type="number" class="number-input" min="0" max="999" :disabled="busy || readonly || expired" /></label>
            <label>集（可留空）<input v-model="episodeChoices[file.id].episode" type="number" class="number-input" min="1" max="1000000" :disabled="busy || readonly || expired" /></label>
          </div>
        </li></ul>
        <p v-if="episodeOrigin && selected.length && !hasVideo" class="help-text">补集下载至少选择一个视频。</p>
        <div v-if="files.length > 200" class="bt-files-actions"><button type="button" class="btn btn--secondary btn--sm" :disabled="page === 0" data-test="bt-files-prev" @click="page--">上一页</button><span>第 {{ page + 1 }} / {{ Math.ceil(files.length / 200) }} 页 · 全选作用于全部文件</span><button type="button" class="btn btn--secondary btn--sm" :disabled="(page + 1) * 200 >= files.length" data-test="bt-files-next" @click="page++">下一页</button></div>
        <p v-if="!files.length" class="help-text">该资源没有可下载文件。</p>
        <p v-if="total === null" class="bt-files-error" role="alert">文件大小无效，请刷新文件清单。</p>
      </template>
      <template #footer><button type="button" class="btn btn--secondary" :disabled="busy" @click="close" data-test="bt-files-close">{{ cancelLabel }}</button><button type="button" class="btn btn--primary" :disabled="!canSubmit" data-test="bt-files-submit" @click="submit">{{ busy ? '正在提交…' : '开始下载所选文件' }}</button></template>
    </DialogShell>
  </Teleport>
</template>
<script>
import DialogShell from '../ui/DialogShell.vue';
import { btByteText, btSelectedBytes } from '../../utils/btFormat.js';
import { episodeNumber } from '../../utils/episodeAcquisition.js';
export default {
  name: 'BTFileSelectionDialog', components: { DialogShell },
  props: { episodeOrigin: { type: Object, default: null }, cancelLabel: { type: String, default: '取消' }, visible: Boolean, draft: { type: Object, default: null }, busy: Boolean, readonly: Boolean, error: { type: String, default: '' } },
  emits: ['close', 'submit'], data: () => ({ selected: [], episodeChoices: {}, localError: '', page: 0, now: Date.now(), expiryTimer: null }),
  computed: { expiryMS() { const draft = typeof this.draft?.expires_at === 'string' ? Date.parse(this.draft.expires_at) : NaN; return this.episodeOrigin ? Math.min(draft, Date.parse(this.episodeOrigin.expires_at)) : draft; }, hasVideo() { return this.files.some(file => file.kind === 'video' && this.selected.includes(String(file.id))); }, expired() { return !Number.isFinite(this.expiryMS) || this.now >= this.expiryMS; }, shownFiles() { return this.files.slice(this.page * 200, (this.page + 1) * 200); }, files() { return this.draft?.files || []; }, preparation() { return this.draft?.preparation || 'ready'; }, ready() { return Boolean(this.draft && ['ready', 'metadata_ready', 'draft'].includes(this.preparation)); }, identity() { return `${this.draft?.draft_id || ''}:${this.draft?.revision || 0}:${this.episodeOrigin?.origin_token || ''}`; }, total() { return btSelectedBytes(this.files, this.selected); }, canSubmit() { return this.visible && this.ready && !this.expired && !this.busy && !this.readonly && this.selected.length > 0 && this.total !== null && (!this.episodeOrigin || this.hasVideo); } },
  watch: { visible: { immediate: true, handler() { this.reset(); } }, identity() { this.reset(); }, 'draft.expires_at'() { this.syncExpiry(); }, files(next, previous) { const shape = rows => JSON.stringify((rows || []).map(f => [f.id, f.relative_path ?? f.path, String(f.size), f.padding, f.selectable])); if (shape(next) !== shape(previous)) this.reset(); } },
  beforeUnmount() { this.clearExpiry(); },
  methods: { clearExpiry() { if (this.expiryTimer !== null) clearTimeout(this.expiryTimer); this.expiryTimer = null; }, syncExpiry() { this.clearExpiry(); this.now = Date.now(); if (this.visible && Number.isFinite(this.expiryMS) && this.expiryMS > this.now) this.expiryTimer = setTimeout(() => this.syncExpiry(), Math.min(this.expiryMS - this.now, 2147483647)); }, bytes: btByteText, selectable(file) { return file.selectable !== false && !file.padding; }, reset() { this.syncExpiry(); this.page = 0; this.localError = ''; this.episodeChoices = Object.fromEntries(this.files.map(file => [file.id, { season: '', episode: '' }])); this.selected = this.visible && !this.episodeOrigin ? this.files.filter(f => this.selectable(f) && f.selected === true).map(f => String(f.id)) : []; }, selectAll() { if (!this.busy && !this.readonly && !this.expired) this.selected = this.files.filter(this.selectable).map(f => String(f.id)); }, close() { if (!this.busy) this.$emit('close'); }, submit() {
      this.now = Date.now(); if (!this.canSubmit) return;
      this.localError = '';
      const request = { draft_id: this.draft.draft_id, expected_revision: this.draft.revision, selected_file_ids: [...this.selected] };
      if (this.episodeOrigin) {
        request.file_episode_choices = [];
        for (const file of this.files.filter(file => file.kind === 'video' && this.selected.includes(String(file.id)))) {
          const choice = this.episodeChoices[file.id];
          if (choice.season === '' && choice.episode === '') continue;
          const season = episodeNumber(choice.season, 999), episode = episodeNumber(choice.episode, 1000000, 1);
          if (season === null || episode === null) { this.localError = '请同时填写有效的季和集，或者两项都留空'; return; }
          request.file_episode_choices.push({ file_id: String(file.id), season, episode });
        }
        if (request.file_episode_choices.length > 200) { this.localError = '一次最多明确 200 个文件的季集，其余可以留到入库后核对'; return; }
      }
      this.$emit('submit', request);
    } }
};
</script>
<style scoped>
.bt-files-actions { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-2); margin: var(--space-3) 0; font-size: var(--fs-secondary); }
.bt-files-list { list-style: none; padding: 0; margin: 0; max-height: 45vh; overflow: auto; }
.bt-files-list li { padding: var(--space-2) 0; border-bottom: 1px solid var(--hairline); }
.bt-files-list label { display: flex; align-items: start; gap: var(--space-2); }
.bt-file-path { flex: 1; min-width: 0; overflow-wrap: anywhere; }
.bt-files-root { overflow-wrap: anywhere; }
.bt-files-error { color: var(--danger-text); }
.bt-episode-choice { display: flex; flex-wrap: wrap; gap: var(--space-3); margin: var(--space-2); }
.bt-episode-choice input { width: 6rem; }
</style>
