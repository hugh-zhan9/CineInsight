<template>
  <Teleport to="body">
    <DialogShell class="image-library-picker" size="lg" title="从图片库选择头像" :busy="busy" @close="close">
      <form class="picker-search" @submit.prevent="search(true)">
        <input v-model="keyword" class="search-input" aria-label="搜索图片" placeholder="搜索图片名称" :disabled="busy" />
        <button type="submit" class="btn btn--secondary" :disabled="busy">搜索</button>
      </form>
      <p class="picker-hint">点击图片设为头像，原图片保持不变。</p>
      <p v-if="error || actionError" class="picker-error" role="alert">{{ error || actionError }}</p>
      <p v-if="loading" class="picker-hint" role="status">正在加载图片…</p>
      <div v-if="loading && !images.length" class="picker-grid" aria-hidden="true">
        <SkeletonBlock v-for="index in 8" :key="index" shape="thumb" />
      </div>
      <EmptyState v-else-if="!loading && !images.length && !error" icon="image" title="没有符合条件的图片。" message="换个关键词再搜，或清空关键词查看全部图片。" />
      <div v-if="images.length" class="picker-grid">
        <button v-for="image in images" :key="image.id" type="button" class="btn btn--secondary picker-tile" :disabled="busy || loading" :aria-label="`使用 ${image.name} 作为头像`" @click="$emit('select', image)">
          <img :src="`/preview/image-thumbnail/${image.id}`" :alt="image.name" loading="lazy" /><span>{{ image.name }}</span>
        </button>
      </div>
      <div v-if="cursor" class="picker-more">
        <button type="button" class="btn btn--secondary" :disabled="loading || busy" @click="search(false)">{{ loading ? '加载中…' : '加载更多' }}</button>
      </div>
    </DialogShell>
  </Teleport>
</template>
<script>
import DialogShell from './ui/DialogShell.vue';
import EmptyState from './ui/EmptyState.vue';
import SkeletonBlock from './ui/SkeletonBlock.vue';
import { SearchImagePage } from '../../wailsjs/go/main/App';
// 从图片库挑一张作人物头像。保存进行中（busy）时 ×、Esc 与选择都不响应，DialogShell 在标题旁转圈。
export default {
  components: { DialogShell, EmptyState, SkeletonBlock },
  props: { busy: Boolean, actionError: { type: String, default: '' } },
  emits: ['close', 'select'],
  data: () => ({ keyword: '', appliedKeyword: '', images: [], cursor: null, loading: false, error: '', requestID: 0 }),
  mounted() { this.search(true); },
  beforeUnmount() { this.requestID++; },
  methods: {
    close() { if (!this.busy) this.$emit('close'); },
    async search(reset) {
      if (this.busy || (!reset && (this.loading || !this.cursor))) return;
      const requestID = ++this.requestID;
      if (reset) { this.appliedKeyword = this.keyword.trim(); this.images = []; this.cursor = null; }
      this.loading = true; this.error = '';
      try {
        const request = { filter: { keyword: this.appliedKeyword }, limit: 40 };
        if (!reset) request.cursor = this.cursor;
        const page = await SearchImagePage(request);
        if (requestID !== this.requestID) return;
        this.images = reset ? (page.images || []) : [...this.images, ...(page.images || [])];
        this.cursor = page.next_cursor || null;
      } catch (err) { if (requestID === this.requestID) this.error = `加载图片失败：${err}`; }
      finally { if (requestID === this.requestID) this.loading = false; }
    }
  }
};
</script>
<style scoped>
.picker-search { display: flex; align-items: center; gap: var(--space-3); margin-bottom: var(--space-3); }
.picker-search input { flex: 1; min-width: 0; }
.picker-hint { margin: 0 0 var(--space-3); color: var(--text-secondary); font-size: var(--fs-secondary); }
.picker-error { margin: 0 0 var(--space-3); color: var(--danger-text); font-size: var(--fs-secondary); }
.picker-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(110px, 1fr)); gap: var(--space-3); margin-bottom: var(--space-3); }
/* 图片格是整块可点的卡片：沿用次要按钮的底色与边框，尺寸与排版按卡片改。 */
.picker-tile { display: grid; gap: var(--space-1); height: auto; min-width: 0; padding: 6px; border-radius: var(--radius); font-weight: var(--fw-regular); white-space: normal; text-align: left; }
.picker-tile img { width: 100%; height: 110px; border-radius: var(--radius-sm); object-fit: cover; }
.picker-tile span { display: block; color: var(--text-primary); font-size: var(--fs-caption); overflow-wrap: anywhere; }
.picker-more { display: flex; justify-content: center; }
</style>
