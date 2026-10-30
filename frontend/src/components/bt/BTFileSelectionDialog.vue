<template>
  <Teleport v-if="visible" to="body">
    <DialogShell title="选择下载文件" size="lg" :busy="busy" :close-on-overlay="false" @close="close">
      <p v-if="error" class="bt-files-error" role="alert">{{ error }}</p>
      <p v-if="!ready" class="help-text" role="status">{{ preparation === 'metadata_pending' ? '正在准备文件清单，尚未开始下载影片。' : '文件清单尚未就绪。' }}</p>
      <template v-else>
        <p v-if="expired" class="bt-files-error" role="alert" data-test="bt-files-expired">{{ Number.isFinite(expiryMS) ? '文件清单已过期，请刷新清单后重新选择。' : '文件清单有效期无效，请刷新清单。' }}</p>
        <p><strong>{{ draft.title }}</strong></p>
        <p class="help-text bt-files-root">保存位置：{{ draft.save_root || '确认后由下载配置确定' }}。完成后原位加入片库，保留文件名和目录继续做种。</p>
        <div class="bt-files-actions"><button type="button" class="btn btn--secondary btn--sm" :disabled="busy || readonly || expired" data-test="bt-files-all" @click="selectAll">全选</button><button type="button" class="btn btn--secondary btn--sm" :disabled="busy || readonly || expired" data-test="bt-files-none" @click="selected = []">清空</button><span data-test="bt-files-total">已选 {{ selected.length }} 项 · {{ bytes(total) }}</span></div>
        <ul class="bt-files-list"><li v-for="file in shownFiles" :key="file.id"><label><input v-model="selected" type="checkbox" :value="String(file.id)" :disabled="busy || readonly || expired || !selectable(file)" :data-test="`bt-file-${file.id}`" /><span class="bt-file-path">{{ file.relative_path ?? file.path }}</span><span class="help-text">{{ file.padding ? '占位文件' : bytes(file.size) }}</span></label></li></ul>
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
export default {
  name: 'BTFileSelectionDialog', components: { DialogShell },
  props: { cancelLabel: { type: String, default: '取消' }, visible: Boolean, draft: { type: Object, default: null }, busy: Boolean, readonly: Boolean, error: { type: String, default: '' } },
  emits: ['close', 'submit'], data: () => ({ selected: [], page: 0, now: Date.now(), expiryTimer: null }),
  computed: { expiryMS() { return typeof this.draft?.expires_at === 'string' ? Date.parse(this.draft.expires_at) : NaN; }, expired() { return !Number.isFinite(this.expiryMS) || this.now >= this.expiryMS; }, shownFiles() { return this.files.slice(this.page * 200, (this.page + 1) * 200); }, files() { return this.draft?.files || []; }, preparation() { return this.draft?.preparation || 'ready'; }, ready() { return Boolean(this.draft && ['ready', 'metadata_ready', 'draft'].includes(this.preparation)); }, identity() { return `${this.draft?.draft_id || ''}:${this.draft?.revision || 0}`; }, total() { return btSelectedBytes(this.files, this.selected); }, canSubmit() { return this.visible && this.ready && !this.expired && !this.busy && !this.readonly && this.selected.length > 0 && this.total !== null; } },
  watch: { visible: { immediate: true, handler() { this.reset(); } }, identity() { this.reset(); }, 'draft.expires_at'() { this.syncExpiry(); }, files(next, previous) { const shape = rows => JSON.stringify((rows || []).map(f => [f.id, f.relative_path ?? f.path, String(f.size), f.padding, f.selectable])); if (shape(next) !== shape(previous)) this.reset(); } },
  beforeUnmount() { this.clearExpiry(); },
  methods: { clearExpiry() { if (this.expiryTimer !== null) clearTimeout(this.expiryTimer); this.expiryTimer = null; }, syncExpiry() { this.clearExpiry(); this.now = Date.now(); if (this.visible && Number.isFinite(this.expiryMS) && this.expiryMS > this.now) this.expiryTimer = setTimeout(() => this.syncExpiry(), Math.min(this.expiryMS - this.now, 2147483647)); }, bytes: btByteText, selectable(file) { return file.selectable !== false && !file.padding; }, reset() { this.syncExpiry(); this.page = 0; this.selected = this.visible ? this.files.filter(f => this.selectable(f) && f.selected === true).map(f => String(f.id)) : []; }, selectAll() { if (!this.busy && !this.readonly && !this.expired) this.selected = this.files.filter(this.selectable).map(f => String(f.id)); }, close() { if (!this.busy) this.$emit('close'); }, submit() { this.now = Date.now(); if (this.canSubmit) this.$emit('submit', { draft_id: this.draft.draft_id, expected_revision: this.draft.revision, selected_file_ids: [...this.selected] }); } }
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
</style>
