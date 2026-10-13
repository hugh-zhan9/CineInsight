<template>
  <DialogShell title="高清替换：选择高清版" size="md" class="hd-picker" data-test="hd-picker" @close="$emit('close')">
    <p class="help-text">长版（主时间线）：{{ longVideo.name }}。在片库里找到同一部片的高清版本，之后在视频工作台自动对齐并逐段确认。</p>
    <form class="hd-picker__search" @submit.prevent="search()">
      <input v-model="keyword" type="search" class="text-input" placeholder="按文件名查找" data-test="hd-picker-keyword" />
      <button type="submit" class="btn btn--secondary" :disabled="searching" data-test="hd-picker-search">{{ searching ? '查找中…' : '查找' }}</button>
    </form>
    <p v-if="error" class="hd-picker__error" role="alert">{{ error }}</p>
    <ul class="hd-picker__results">
      <li v-for="video in candidates" :key="video.id">
        <button type="button" class="btn hd-picker__item" :disabled="busy" :data-test="`hd-picker-pick-${video.id}`" @click="$emit('pick', video)">
          <span class="hd-picker__name">{{ video.name }}</span>
          <span class="help-text">{{ facts(video) }}</span>
        </button>
      </li>
      <li v-if="searched && !searching && !candidates.length" class="help-text" data-test="hd-picker-empty">没有找到其他视频。</li>
    </ul>
    <button v-if="nextCursor" type="button" class="btn btn--secondary btn--sm hd-picker__more" :disabled="searching" @click="search(true)">加载更多</button>
    <template #footer>
      <button type="button" class="btn btn--secondary" @click="$emit('close')">取消</button>
    </template>
  </DialogShell>
</template>

<script>
import { SearchLibraryVideoPage } from '../../../wailsjs/go/main/App';
import DialogShell from '../ui/DialogShell.vue';
import { formatEditTime } from '../../utils/videoEdit.js';

// 行菜单「高清替换…」的高清版选择：用片库既有的文件名检索，排除长版自身；晚到的旧检索结果丢弃。
export default {
  name: 'HDReplacePickerDialog',
  components: { DialogShell },
  props: {
    longVideo: { type: Object, required: true },
    busy: { type: Boolean, default: false }
  },
  emits: ['close', 'pick'],
  data() {
    return { keyword: '', videos: [], nextCursor: null, searching: false, searched: false, error: '', generation: 0 };
  },
  computed: {
    candidates() {
      return this.videos.filter(video => video.id !== this.longVideo.id);
    }
  },
  methods: {
    facts(video) {
      const parts = [];
      if (video.width && video.height) parts.push(`${video.width}×${video.height}`);
      if (video.duration) parts.push(formatEditTime(video.duration * 1000));
      return parts.join(' · ');
    },
    async search(more = false) {
      const generation = ++this.generation;
      const keyword = this.keyword.trim();
      this.searching = true;
      this.error = '';
      try {
        const page = await SearchLibraryVideoPage({
          filter: { keyword, search_mode: 'file', sort_mode: 'balanced' },
          cursor: more ? this.nextCursor : null,
          limit: 20
        });
        if (generation !== this.generation) return;
        this.videos = more ? [...this.videos, ...(page?.videos || [])] : (page?.videos || []);
        this.nextCursor = page?.next_cursor || null;
        this.searched = true;
      } catch (err) {
        if (generation === this.generation) this.error = `查找失败：${err}`;
      } finally {
        if (generation === this.generation) this.searching = false;
      }
    }
  }
};
</script>

<style scoped>
.hd-picker__search { display: flex; gap: var(--space-2); margin: var(--space-3) 0; }
.hd-picker__search input { flex: 1; min-width: 0; }
.hd-picker__results { list-style: none; margin: 0; padding: 0; max-height: 50vh; overflow: auto; display: grid; gap: var(--space-1); }
/* 整块可点的候选：沿用 .btn 的焦点环与禁用口径，版式由这里接管。 */
.hd-picker__item { display: grid; justify-content: stretch; justify-items: start; gap: 2px; width: 100%; height: auto; padding: 6px var(--space-2); border: 1px solid var(--border-color); border-radius: var(--radius-sm); background: transparent; color: inherit; font-weight: var(--fw-regular); text-align: left; white-space: normal; }
.hd-picker__item:hover:not(:disabled) { border-color: var(--accent-color); }
.hd-picker__name { max-width: 100%; font-weight: var(--fw-semibold); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.hd-picker__more { margin-top: var(--space-2); }
.hd-picker__error { color: var(--danger-text); font-size: var(--fs-secondary); }
</style>
