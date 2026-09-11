<template>
  <div class="image-batch-tags" data-test="image-batch-tags">
    <span>已选 {{ selectedIDs.length }} 张</span>
    <button type="button" class="btn-secondary btn-compact" :disabled="busy" data-test="image-batch-select-all" @click="$emit('update:selectedIDs', allSelected ? [] : imageIDs)">{{ allSelected ? '取消全选' : '全选已加载' }}</button>
    <button type="button" class="btn-secondary btn-compact" :disabled="!selectedIDs.length || busy" data-test="image-batch-tag-open" @click="open">批量添加标签</button>
    <div v-if="opened" class="image-batch-tags__picker">
      <input v-model="keyword" placeholder="搜索标签" aria-label="搜索批量添加的标签" :disabled="busy" />
      <div class="image-batch-tags__options">
        <button v-for="tag in filteredTags" :key="tag.id" type="button" class="btn-secondary btn-compact" :disabled="busy || !selectedIDs.length" @click="add(tag)">{{ tag.name }}</button>
        <span v-if="!loading && !filteredTags.length">没有匹配标签</span>
      </div>
    </div>
    <p v-if="loading || busy" role="status">{{ busy ? '正在添加标签…' : '正在加载标签…' }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <p v-if="error" role="alert">{{ error }}</p>
  </div>
</template>
<script>
import { GetAllTags, BatchAddTagToImages } from '../../wailsjs/go/main/App';
export default {
  name: 'ImageBatchTagControls',
  props: { imageIDs: { type: Array, required: true }, selectedIDs: { type: Array, required: true } },
  emits: ['update:selectedIDs', 'added'],
  data: () => ({ tags: [], keyword: '', opened: false, loading: false, busy: false, error: '', notice: '' }),
  computed: {
    allSelected() { return this.imageIDs.length > 0 && this.imageIDs.every(id => this.selectedIDs.includes(id)); },
    filteredTags() { const q = this.keyword.trim().toLowerCase(); return this.tags.filter(tag => !tag.automatic_kind && tag.name.toLowerCase().includes(q)); }
  },
  beforeUnmount() { this._disposed = true; },
  methods: {
    async open() {
      if (this.loading) return;
      this.opened = true; this.loading = true; this.error = '';
      try { this.tags = await GetAllTags() || []; }
      catch (err) { this.error = `读取标签失败：${err}`; }
      finally { this.loading = false; }
    },
    async add(tag) {
      if (this.busy || !this.selectedIDs.length) return;
      const ids = [...this.selectedIDs]; this.busy = true; this.error = ''; this.notice = '';
      try {
        const result = await BatchAddTagToImages(ids, Number(tag.id));
        if (this._disposed) return;
        const failedIDs = new Set((result?.errors || []).map(item => Number(item.image_id)));
        if (result?.failed) {
          this.error = `${result.failed} 张图片添加失败，可重试。`;
          this.$emit('update:selectedIDs', failedIDs.size ? ids.filter(id => failedIDs.has(id)) : ids);
        }
        this.notice = `已为 ${Number(result?.succeeded || 0)} 张图片添加「${tag.name}」`;
        this.$emit('added', { tag, imageIDs: ids.filter(id => !failedIDs.has(id)) });
      } catch (err) { if (!this._disposed) this.error = `批量添加失败：${err}`; }
      finally { this.busy = false; }
    }
  }
};
</script>
<style scoped>
.image-batch-tags { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.image-batch-tags__picker { flex-basis: 100%; display: grid; gap: 8px; }
.image-batch-tags__picker input { border: 1px solid var(--hairline); border-radius: 6px; background: var(--control-bg); color: var(--text-primary); padding: 8px; }
.image-batch-tags__options { display: flex; flex-wrap: wrap; gap: 6px; max-height: 150px; overflow-y: auto; }
.image-batch-tags p { flex-basis: 100%; margin: 0; }
</style>
