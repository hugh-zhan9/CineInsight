<template>
  <section class="photo-anniversaries" aria-label="那年今天" data-test="photo-anniversaries">
    <div class="photo-anniversaries__toolbar">
      <strong>那年今天</strong>
      <input v-model="date" type="date" class="text-input" aria-label="回忆日期" />
      <span>{{ total }} 张 · 仅往年同月同日，沿用当前人物筛选</span>
    </div>
    <AppBanner v-if="error" tone="danger" title="读取回忆失败" :message="error" />
    <p v-else-if="!loading && !items.length">没有对应的照片回忆。没有拍摄日期的图片不会出现在这里。</p>
    <button v-if="!loading && !items.length" type="button" class="btn btn--secondary" @click="$emit('navigate', 'photos-manage')">到图片管理补全 EXIF</button>
    <div class="photo-anniversaries__grid">
      <button v-for="(image, index) in items" :key="image.id" type="button" class="btn-plain" :aria-label="image.name" @click="opened = index">
        <img :src="`/preview/image-thumbnail/${image.id}`" :alt="image.name" loading="lazy" />
        <span>{{ image.taken_at?.slice(0, 10) }} · {{ image.name }}</span>
      </button>
    </div>
    <button v-if="cursor" type="button" class="btn btn--secondary" :disabled="loading" @click="load(false)">加载更多</button>
    <PhotoLightbox v-if="pageActive && opened >= 0 && items[opened]" :image="items[opened]" :has-prev="opened > 0" :has-next="opened + 1 < items.length || Boolean(cursor)" :loading-next="loading" @close="opened = -1" @prev="opened--" @next="next" @patch="patch" @open-in-manage="$emit('open-in-manage', $event)" @open-person="$emit('open-person', $event)" />
  </section>
</template>
<script setup>
import { ref, watch, onBeforeUnmount } from 'vue';
import { ListImageAnniversaries } from '../../../wailsjs/go/main/App';
import PhotoLightbox from './PhotoLightbox.vue';
import AppBanner from '../ui/AppBanner.vue';
import { functionalPolishError } from '../../utils/functionalPolishErrors.js';
const props = defineProps({ personIds: { type: Array, default: () => [] }, pageActive: { type: Boolean, default: true } });
defineEmits(['navigate', 'open-in-manage', 'open-person']);
const now = new Date();
const date = ref(`${now.getFullYear()}-${String(now.getMonth()+1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')}`);
const items = ref([]), cursor = ref(null), total = ref(0), error = ref(''), loading = ref(false), opened = ref(-1);
let generation = 0;
async function load(reset = true) {
  const ticket = reset ? ++generation : generation;
  if (reset) { items.value = []; cursor.value = null; total.value = 0; opened.value = -1; }
  loading.value = true; error.value = '';
  try {
    const result = await ListImageAnniversaries({ date: date.value, person_ids: props.personIds, cursor: reset ? undefined : cursor.value, limit: 60 });
    if (ticket !== generation) return;
    items.value = reset ? result.items : [...items.value, ...result.items]; cursor.value = result.next_cursor || null; total.value = result.total;
  } catch (err) { if (ticket === generation) error.value = functionalPolishError(err); }
  finally { if (ticket === generation) loading.value = false; }
}
async function next() { const ticket = generation; if (opened.value + 1 >= items.value.length && cursor.value) await load(false); if (ticket === generation && opened.value + 1 < items.value.length) opened.value++; }
function patch(fields) { items.value = items.value.map(item => item.id === fields.id ? { ...item, ...fields } : item); }
watch([date, () => props.personIds, () => props.pageActive], () => {
  if (props.pageActive) load();
  else { generation++; opened.value = -1; loading.value = false; }
}, { immediate: true, deep: true });
onBeforeUnmount(() => { generation++; });
</script>
<style scoped>
.photo-anniversaries { padding: var(--space-4); }
.photo-anniversaries__toolbar { display: flex; align-items: center; gap: var(--space-3); flex-wrap: wrap; }
.photo-anniversaries__grid { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: var(--space-3); margin-block: var(--space-4); }
.photo-anniversaries__grid img { width: 100%; aspect-ratio: 1; object-fit: cover; }
.photo-anniversaries__grid span { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
