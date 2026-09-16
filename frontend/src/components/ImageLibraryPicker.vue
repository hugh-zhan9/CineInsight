<template>
  <Teleport to="body">
    <div class="image-picker-layer">
      <BaseModal class="image-library-picker" aria-label="从图片库选择头像" @close="close">
        <div class="picker-heading"><h3>从图片库选择头像</h3><button class="btn-secondary" :disabled="busy" @click="close">取消</button></div>
        <form class="picker-search" @submit.prevent="search(true)">
          <input v-model="keyword" aria-label="搜索图片" placeholder="搜索图片名称" :disabled="busy" />
          <button class="btn-secondary" :disabled="busy">搜索</button>
        </form>
        <p>点击图片设为头像，原图片保持不变。</p>
        <p v-if="error || actionError" role="alert">{{ error || actionError }}</p>
        <p v-if="loading" role="status">正在加载图片…</p>
        <p v-else-if="!images.length && !error">没有符合条件的图片。</p>
        <div class="picker-grid">
          <button v-for="image in images" :key="image.id" type="button" :disabled="busy || loading" :aria-label="`使用 ${image.name} 作为头像`" @click="$emit('select', image)">
            <img :src="`/preview/image-thumbnail/${image.id}`" :alt="image.name" loading="lazy" /><span>{{ image.name }}</span>
          </button>
        </div>
        <button v-if="cursor" class="btn-secondary" :disabled="loading || busy" @click="search(false)">加载更多</button>
      </BaseModal>
    </div>
  </Teleport>
</template>
<script>
import BaseModal from './ui/BaseModal.vue';
import { SearchImagePage } from '../../wailsjs/go/main/App';
export default {
  components: { BaseModal },
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
.image-picker-layer { position: fixed; inset: 0; z-index: 1300; }
:deep(.image-library-picker) { width: min(760px, 92vw); max-height: 85vh; overflow: auto; }
.picker-heading, .picker-search { display: flex; align-items: center; gap: 12px; margin-bottom: 12px; }
.picker-heading { justify-content: space-between; }
.picker-search input { flex: 1; min-width: 0; }
.picker-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(110px, 1fr)); gap: 12px; margin-bottom: 12px; }
.picker-grid button { min-width: 0; padding: 6px; background: var(--bg-secondary); color: var(--text-primary); border: 1px solid var(--border-color); border-radius: 8px; cursor: pointer; }
.picker-grid button:disabled { opacity: .55; cursor: default; }
.picker-grid img { width: 100%; height: 110px; object-fit: cover; }
.picker-grid span { display: block; overflow-wrap: anywhere; }
</style>
