<template>
  <button ref="trigger" type="button" class="btn btn--secondary btn--sm detail-tag-picker__trigger" data-test="detail-add-tag" :aria-expanded="open" @click="toggle"><AppIcon name="plus" :size="16" /> 标签</button>
  <BasePopover v-if="open" :anchor="$refs.trigger" @close="open = false">
    <div role="group" aria-label="添加标签" class="detail-tag-picker" data-test="detail-tag-picker">
      <input v-model="query" class="text-input" aria-label="搜索或创建标签" placeholder="搜索或创建标签…" :disabled="busy" @keydown.enter.prevent="addExact" />
      <p v-if="loading" class="detail-tag-picker__note">正在读取…</p>
      <div v-else class="detail-tag-picker__choices">
        <TagPill v-for="tag in matches.slice(0, 12)" :key="tag.id" clickable :label="tag.name" :disabled="busy" @click="add(tag)" />
        <button v-if="canCreate" type="button" class="btn btn--secondary btn--sm" :disabled="busy" data-test="detail-create-tag" @click="createAndAdd">创建「{{ query.trim() }}」</button>
        <small v-if="matches.length > 12" class="detail-tag-picker__note">还有 {{ matches.length - 12 }} 个，输入名称查找</small>
        <small v-if="!matches.length && !canCreate" class="detail-tag-picker__note">{{ query.trim() ? '没有匹配的可添加标签' : '输入名称创建标签' }}</small>
      </div>
      <p v-if="error" class="detail-tag-picker__error" role="alert">{{ error }} <button v-if="loadFailed" type="button" class="btn-link" @click="load">重试</button></p>
    </div>
  </BasePopover>
</template>
<script>
import { AddTagToVideo, CreateTag, GetAllTags } from '../../../wailsjs/go/main/App';
import BasePopover from '../ui/BasePopover.vue';
import AppIcon from '../ui/AppIcon.vue';
import TagPill from '../ui/TagPill.vue';

export default {
  name: 'DetailTagPicker', components: { BasePopover, AppIcon, TagPill },
  props: { videoId: { type: Number, required: true }, tags: { type: Array, default: () => [] } },
  emits: ['added', 'created'],
  data: () => ({ open: false, query: '', allTags: [], loading: false, busy: false, error: '', loadFailed: false }),
  computed: {
    available() {
      const selected = new Set(this.tags.map(tag => Number(tag.id)));
      return this.allTags.filter(tag => !selected.has(Number(tag.id)) && (!tag.automatic_kind || ['short_video', 'low_resolution'].includes(tag.automatic_kind)));
    },
    matches() { const query = this.query.trim().toLocaleLowerCase(); return this.available.filter(tag => tag.name.toLocaleLowerCase().includes(query)); },
    canCreate() { return !this.loading && !this.loadFailed && !!this.query.trim() && !this.allTags.some(tag => tag.name.toLocaleLowerCase() === this.query.trim().toLocaleLowerCase()); }
  },
  methods: {
    async toggle() { this.open = !this.open; if (this.open) { this.query = ''; await this.load(); } },
    async load() {
      this.loading = true; this.error = ''; this.loadFailed = false;
      try { this.allTags = await GetAllTags() || []; }
      catch (error) { this.loadFailed = true; this.error = `读取标签失败：${error}`; }
      finally { this.loading = false; }
    },
    async add(tag) {
      if (this.busy || !this.available.some(item => Number(item.id) === Number(tag.id))) return;
      this.busy = true; this.error = '';
      try { await AddTagToVideo(this.videoId, Number(tag.id)); this.$emit('added', { videoId: this.videoId, tag }); this.query = ''; }
      catch (error) { this.error = `添加标签失败：${error}`; }
      finally { this.busy = false; }
    },
    addExact() {
      if (this.loading || this.loadFailed || this.busy) return;
      const exact = this.matches.find(tag => tag.name.toLocaleLowerCase() === this.query.trim().toLocaleLowerCase());
      if (exact) return this.add(exact);
      if (this.canCreate) return this.createAndAdd();
    },
    async createAndAdd() {
      if (!this.canCreate || this.busy) return;
      this.busy = true; this.error = '';
      try {
        const tag = await CreateTag(this.query.trim(), '');
        this.allTags = [...this.allTags, tag];
        this.$emit('created', tag);
        await AddTagToVideo(this.videoId, Number(tag.id));
        this.$emit('added', { videoId: this.videoId, tag }); this.query = '';
      } catch (error) { this.error = `创建或添加标签失败：${error}`; }
      finally { this.busy = false; }
    }
  }
};
</script>
<style scoped>
.detail-tag-picker__trigger { height: 22px; min-height: 0; padding: 0 var(--space-2); border-radius: var(--radius-pill); font-size: var(--fs-caption); background: var(--recap-hero-chip); border-color: var(--recap-hero-chip-border); color: var(--recap-hero-fg); }
.detail-tag-picker { display: grid; gap: var(--space-3); width: min(300px, calc(100vw - 40px)); padding: var(--space-3); }
.detail-tag-picker__choices { display: flex; flex-wrap: wrap; gap: var(--space-2); }
.detail-tag-picker__note { margin: 0; color: var(--text-secondary); font-size: var(--fs-caption); }
.detail-tag-picker__error { margin: 0; color: var(--danger-color); font-size: var(--fs-caption); }
</style>
