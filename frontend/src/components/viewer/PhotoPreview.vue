<template>
  <PhotoLightboxCore v-bind="$props" :image="currentImage" :asset-url="selected ? '' : assetUrl"
    :allow-remove="!selected && allowRemove" :mixed-media="!selected && mixedMedia"
    :neighbors="selected ? items : neighbors" :has-prev="canPrev" :has-next="canNext"
    :loading-next="selected ? loading : loadingNext" :next-error="selected ? '' : nextError"
    @close="$emit('close')" @prev="navigate(-1)" @next="navigate(1)" @select-neighbor="selectNeighbor"
    @patch="patch" @changed="$emit('changed', $event)" @delete-image="$emit('delete-image', $event)"
    @open-person="$emit('open-person', $event)" @open-in-manage="$emit('open-in-manage', $event)" @open-cleanup="$emit('open-cleanup')">
    <template #related>
      <section class="photo-directory" aria-label="同目录图片" data-test="photo-directory">
        <h3>同目录图片</h3>
        <button v-if="selected" type="button" class="btn btn--secondary btn--sm" data-test="photo-directory-return" @click="selected = null">返回原浏览位置</button>
        <p v-if="!directory" class="photo-directory__status">尚未读取到文件夹信息</p>
        <template v-else>
          <p class="photo-directory__status" :title="directory">{{ folderName }} · 仅当前文件夹</p>
          <p v-if="loading" class="photo-directory__status" role="status">正在读取…</p>
          <AppBanner v-if="error" tone="danger" :title="error">
            <template #actions><button type="button" class="btn btn--secondary btn--sm" :disabled="loading" data-test="photo-directory-retry" @click="retry">重试</button></template>
          </AppBanner>
          <p v-if="!loading && !error && !items.length" class="photo-directory__status">这个文件夹暂无可显示的图片</p>
          <div class="photo-directory__grid">
            <button v-for="item in items" :key="item.id" type="button" class="btn btn--ghost photo-directory__tile"
              :aria-label="`查看同目录图片 ${item.name}`" :aria-current="Number(item.id) === Number(currentImage.id) ? 'true' : undefined"
              :title="item.name" @click="select(item)">
              <img :src="`/preview/image-thumbnail/${Number(item.id)}`" :alt="item.name" loading="lazy" />
              <span>{{ item.name }}</span>
            </button>
          </div>
          <div v-if="page > 1 || cursor" class="photo-directory__pages">
            <button type="button" class="btn btn--secondary btn--sm" :disabled="loading || page <= 1" data-test="photo-directory-prev" @click="turnPage(-1)">上一页</button>
            <span>第 {{ page }} 页</span>
            <button type="button" class="btn btn--secondary btn--sm" :disabled="loading || !cursor" data-test="photo-directory-next" @click="turnPage(1)">下一页</button>
          </div>
        </template>
      </section>
    </template>
  </PhotoLightboxCore>
</template>
<script>
import PhotoLightboxCore, { photoFolderName } from './PhotoLightbox.vue';
import AppBanner from '../ui/AppBanner.vue';
import { SearchImagePage } from '../../../wailsjs/go/main/App';

// Host keeps its original list/selection. This local directory excursion never
// inserts siblings into that list or into an exploration scene.
export default {
  name: 'PhotoPreview',
  components: { PhotoLightboxCore, AppBanner },
  props: PhotoLightboxCore.props,
  emits: PhotoLightboxCore.emits,
  data: () => ({ selected: null, originalPatch: {}, items: [], cursor: null, history: [], page: 1, loading: false, error: '', generation: 0 }),
  computed: {
    original() { return Number(this.originalPatch.id) === Number(this.image.id) ? { ...this.image, ...this.originalPatch } : this.image; },
    currentImage() { return this.selected || this.original; },
    directory() { return this.original.directory || ''; },
    sourceKey() { return JSON.stringify([this.image.id, this.directory]); },
    folderName() { return photoFolderName(this.directory); },
    selectedIndex() { return this.items.findIndex(item => Number(item.id) === Number(this.selected?.id)); },
    canPrev() { return this.selected ? !this.loading && (this.selectedIndex > 0 || this.page > 1) : this.hasPrev; },
    canNext() { return this.selected ? !this.loading && (this.selectedIndex >= 0 && this.selectedIndex < this.items.length - 1 || Boolean(this.cursor)) : this.hasNext; }
  },
  watch: {
    sourceKey: { immediate: true, handler() { this.generation++; this.selected = null; this.items = []; this.cursor = null; this.history = []; this.page = 1; this.loading = false; this.error = ''; this._pending = null; if (this.directory) this.loadPage(null, [], false); } }
  },
  beforeUnmount() { this.generation++; },
  methods: {
    patch(fields) {
      if (Number(fields.id) === Number(this.selected?.id)) this.selected = { ...this.selected, ...fields };
      if (Number(fields.id) === Number(this.image.id)) this.originalPatch = Number(this.originalPatch.id) === Number(fields.id) ? { ...this.originalPatch, ...fields } : { ...fields };
      this.items = this.items.map(item => Number(item.id) === Number(fields.id) ? { ...item, ...fields } : item);
      this.$emit('patch', fields);
    },
    select(item) { this.selected = Number(item.id) === Number(this.image.id) ? null : { ...item }; },
    selectNeighbor(id) { if (!this.selected) { this.$emit('select-neighbor', id); return; } const item = this.items.find(item => Number(item.id) === Number(id)); if (item) this.select(item); },
    navigate(delta) {
      if (!this.selected) { this.$emit(delta < 0 ? 'prev' : 'next'); return; }
      const item = this.items[this.selectedIndex + delta];
      if (item) this.select(item); else this.turnPage(delta, true);
    },
    turnPage(delta, selectAfter = false) {
      if (this.loading) return;
      if (delta > 0 && this.cursor) this.loadPage(this.cursor, [...this.history, this._pageCursor || null], selectAfter ? 'first' : false);
      else if (delta < 0 && this.history.length) this.loadPage(this.history.at(-1), this.history.slice(0, -1), selectAfter ? 'last' : false);
    },
    retry() { if (!this.loading && this._pending) this.loadPage(...this._pending); },
    async loadPage(cursor, history, selectAfter) {
      const ticket = ++this.generation, directory = this.directory;
      this._pending = [cursor, history, selectAfter]; this.loading = true; this.error = '';
      try {
        const request = { filter: { directory, sort_mode: 'name' }, limit: 24 };
        if (cursor) request.cursor = cursor;
        const result = await SearchImagePage(request);
        if (ticket !== this.generation) return;
        this.items = result?.images || []; this.cursor = result?.next_cursor || null; this.history = history; this.page = history.length + 1; this._pageCursor = cursor;
        if (selectAfter && this.items.length) this.select(selectAfter === 'last' ? this.items.at(-1) : this.items[0]);
      } catch { if (ticket === this.generation) this.error = '同目录图片没有加载出来'; }
      finally { if (ticket === this.generation) this.loading = false; }
    }
  }
};
</script>
<style scoped>
.photo-directory { display: grid; gap: var(--space-2); padding-top: var(--space-4); border-top: 1px solid var(--hairline-faint); }
.photo-directory h3 { margin: 0; color: var(--text-secondary); font-size: var(--fs-secondary); font-weight: var(--fw-semibold); }
.photo-directory__status { margin: 0; color: var(--text-muted); font-size: var(--fs-caption); overflow-wrap: anywhere; }
.photo-directory__grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: var(--space-2); }
.photo-directory__tile { display: grid; gap: var(--space-1); min-width: 0; height: auto; padding: 4px; }
.photo-directory__tile[aria-current] { outline: 2px solid var(--accent-color); outline-offset: -2px; }
.photo-directory__tile img { display: block; width: 100%; aspect-ratio: 1; object-fit: cover; border-radius: var(--radius-sm); }
.photo-directory__tile span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: var(--fs-caption); }
.photo-directory__pages { display: flex; justify-content: space-between; align-items: center; gap: var(--space-2); font-size: var(--fs-caption); }
</style>
