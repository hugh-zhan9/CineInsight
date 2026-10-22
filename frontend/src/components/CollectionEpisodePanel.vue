<template>
  <section class="episode-panel" aria-label="剧集完整度">
    <button type="button" class="btn btn--secondary" @click="load(true)">检查剧集完整度</button>
    <AppBanner v-if="error" tone="danger" title="季集资料未更新" :message="error" />
    <template v-if="report">
      <p>总收录 {{ report.total }} 个文件（含离线），{{ report.uncertain }} 项需确认。未知总集数时仅检查中间编号，不代表整季收齐。</p>
      <p v-if="report.mixed_series">存在不同系列名称，请先逐项确认归属。</p>
      <div v-for="season in report.seasons" :key="season.season" class="episode-panel__season">
        <strong>{{ season.season === 0 ? '特别篇' : `第 ${season.season} 季` }}</strong>
        <span>已识别 {{ season.recognized }} 集 · 可播放 {{ season.playable }} · 离线 {{ season.offline }} · 同集多文件 {{ season.duplicate_episodes }} · 超出总集数 {{ season.range_conflicts }}</span>
        <span>编号空缺：{{ season.missing_ranges.map(range => range.start === range.end ? `E${range.start}` : `E${range.start}–E${range.end}`).join('、') || '无' }}</span>
        <label>手填总集数 <input v-model.number="totals[season.season]" type="number" min="1" max="1000000" class="number-input" /></label>
        <button type="button" class="btn btn--secondary btn--sm" :disabled="busy" @click="saveTotal(season)">保存总集数</button>
      </div>
      <div v-for="member in members" :key="member.video_id" class="episode-panel__member">
        <span>{{ member.name }} {{ member.offline ? '（离线）' : '' }} {{ member.issue }} {{ member.version_group_id ? '· 已确认版本组' : '' }}</span>
        <label>季 <input v-model="member.season" type="number" min="0" max="999" class="number-input" placeholder="未知" /></label>
        <label>集 <input v-model="member.episode" type="number" min="1" max="1000000" class="number-input" placeholder="未知" /></label>
        <label><input v-model="member.excluded" type="checkbox" />不参与完整度</label>
        <button type="button" class="btn btn--secondary btn--sm" :disabled="busy" @click="saveMember(member)">确认归属</button>
      </div>
      <button v-if="cursor" type="button" class="btn btn--secondary" :disabled="busy" @click="load(false)">加载更多成员</button>
    </template>
  </section>
</template>
<script setup>
import { ref, watch, onBeforeUnmount } from 'vue';
import { GetCollectionEpisodeReport, SetCollectionEpisodeAssignments, SetCollectionSeasonExpectation } from '../../wailsjs/go/main/App';
import AppBanner from './ui/AppBanner.vue';
import { functionalPolishError } from '../utils/functionalPolishErrors.js';

const props = defineProps({ collectionId: { type: Number, required: true } });
const report = ref(null), members = ref([]), totals = ref({}), cursor = ref(0);
const error = ref(''), busy = ref(false);
let generation = 0;
watch(() => props.collectionId, () => {
  generation++;
  report.value = null;
  members.value = [];
  cursor.value = 0;
  error.value = '';
  busy.value = false;
});
onBeforeUnmount(() => { generation++; });

async function load(reset) {
  const ticket = reset ? ++generation : generation;
  busy.value = true;
  error.value = '';
  try {
    const result = await GetCollectionEpisodeReport(props.collectionId, reset ? 0 : cursor.value, 50);
    if (ticket !== generation) return;
    report.value = result;
    members.value = reset ? result.members : [...members.value, ...result.members];
    cursor.value = result.next_cursor;
    totals.value = Object.fromEntries(result.seasons.map(season => [season.season, season.expected_count ?? '']));
  } catch (err) {
    if (ticket === generation) error.value = functionalPolishError(err);
  } finally {
    if (ticket === generation) busy.value = false;
  }
}
function nullableNumber(value) { return value === '' || value == null ? null : Number(value); }
async function saveMember(member) {
  const edit = {
    video_id: member.video_id, season: nullableNumber(member.season),
    episode: nullableNumber(member.episode), excluded: member.excluded, expected_revision: member.revision
  };
  await save(() => SetCollectionEpisodeAssignments(props.collectionId, [edit]));
}
async function saveTotal(season) {
  await save(() => SetCollectionSeasonExpectation(props.collectionId, season.season, Number(totals.value[season.season]), season.revision));
}
async function save(operation) {
  const ticket = generation;
  busy.value = true;
  error.value = '';
  try {
    await operation();
    if (ticket === generation) await load(true);
  } catch (err) {
    if (ticket === generation) error.value = functionalPolishError(err);
  } finally {
    if (ticket === generation) busy.value = false;
  }
}
</script>

<style scoped>
.episode-panel { margin-block: var(--space-4); }
.episode-panel__season, .episode-panel__member { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-3); padding-block: var(--space-3); border-bottom: 1px solid var(--border-color); }
.episode-panel__member > span { flex-basis: 100%; }
.number-input { width: 96px; }
</style>
