<template>
  <section class="page-content pending-work-page" data-test="pending-work-page">
    <PageHeader title="待处理" :subtitle="headerSubtitle" data-test="pending-work-header">
      <template #actions>
        <button type="button" class="btn btn--secondary btn--sm" data-test="pending-work-refresh" @click="refreshAll">刷新</button>
      </template>
    </PageHeader>

    <!-- 每类都处理完：整页一个空状态，下一步回管理首页。失败的 AI 打标不算待处理，照旧在下面提一句。 -->
    <template v-if="allClear">
      <EmptyState
        icon="check"
        title="暂无待处理事项"
        message="AI 审阅、人脸、建议作品集、本地资料更新与清理候选有了新的事项，会出现在这里。"
        data-test="pending-work-empty"
      >
        <template #actions>
          <button type="button" class="btn btn--secondary" data-test="pending-work-go-home" @click="$emit('navigate', 'manage-home')">回管理首页</button>
        </template>
      </EmptyState>
      <p v-if="failedRuns > 0" class="pending-section__note" data-test="pending-ai-failed">
        另有 {{ formatCount(failedRuns) }} 个视频 AI 打标失败（不计入待处理）。
        <button type="button" class="btn-link" data-test="pending-open-task-center" @click="$emit('open-task-center')">在任务中心查看</button>
      </p>
    </template>

    <div v-else class="pending-work-page__sections">
      <!-- 一类一个区块（D-P3-01）：各自加载、各自出错（横幅 + 只重试这一块）；数量为 0 的收成一行「已处理完」。 -->
      <section
        v-for="section in sectionViews"
        :key="section.key"
        :class="['pending-section', { 'pending-section--done': section.done }]"
        :aria-busy="section.loading ? 'true' : undefined"
        :data-test="`pending-row-${section.key}`"
      >
        <header class="pending-section__head">
          <h2 class="pending-section__title">{{ section.label }}</h2>
          <AppBadge v-if="section.showCount" tone="accent" :count="section.count" :data-test="`pending-count-${section.key}`" />
          <span v-if="section.done" class="pending-section__done" :data-test="`pending-done-${section.key}`">
            <AppIcon name="check" :size="16" />已处理完
          </span>
          <span v-if="section.updatedAt" class="pending-section__time" :data-test="`pending-time-${section.key}`">更新于 {{ clock(section.updatedAt) }}</span>
          <div v-if="!section.done" class="pending-section__actions">
            <template v-if="section.command">
              <kbd v-if="section.shortcut" class="pending-section__kbd">{{ section.shortcut }}</kbd>
              <button
                type="button"
                class="btn btn--secondary btn--sm"
                :data-test="`pending-go-${section.key}`"
                @click="openTool(section)"
              >{{ section.action }}</button>
            </template>
            <button
              v-else-if="section.key === 'collection_suggestions'"
              type="button"
              class="btn btn--secondary btn--sm"
              data-test="pending-suggestions-open"
              :disabled="!section.ready"
              @click="suggestionPanelOpen = true"
            >逐条处理…</button>
            <button
              v-else-if="section.key === 'local_metadata_updates'"
              type="button"
              class="btn btn--primary btn--sm"
              data-test="pending-local-apply-all"
              :disabled="!section.ready || Boolean(local.applying) || Boolean(local.busyID)"
              @click="applyAllLocal"
            >{{ applyAllLabel }}</button>
          </div>
        </header>

        <template v-if="!section.done">
          <SkeletonBlock v-if="section.loading" :lines="2" />
          <template v-else>
            <AppBanner
              v-if="section.error"
              tone="danger"
              :title="section.source === 'summary' ? '数量没读到' : `${section.label}没读到`"
              :message="section.error"
              :data-test="`pending-error-${section.key}`"
            >
              <template #actions>
                <button type="button" class="btn btn--secondary btn--sm" :data-test="`pending-retry-${section.key}`" @click="retrySection(section)">重试</button>
              </template>
            </AppBanner>

            <p v-if="section.description" class="pending-section__lead">{{ section.description }}</p>

            <!-- 人脸：嵌现有的人脸审阅面板（默认「待处理」视图 = 待确认追加 + 未命名），动作做完只重读这一块的数量。 -->
            <template v-if="section.key === 'face'">
              <p v-if="section.detail" class="pending-section__lead" data-test="pending-face-detail">{{ section.detail }}</p>
              <FaceClusterReviewPanel ref="facePanel" class="pending-section__panel" @changed="refreshSummary" />
            </template>

            <!-- 建议作品集：列出前几组，「逐条处理…」打开现有的建议作品集面板（对话框），逐组采纳或忽略。 -->
            <template v-else-if="section.key === 'collection_suggestions' && section.ready">
              <ul class="pending-list" data-test="pending-suggestion-list">
                <li v-for="item in suggestionPreview" :key="item.id" class="pending-list__row" data-test="pending-suggestion-item">
                  <span class="pending-list__title">{{ item.series_name }}</span>
                  <span class="pending-list__meta">{{ suggestionMeta(item) }}</span>
                </li>
              </ul>
              <p v-if="suggestionRest > 0" class="pending-section__lead">还有 {{ formatCount(suggestionRest) }} 组，在「逐条处理…」里看。</p>
            </template>

            <!-- 本地资料有更新：逐条看会改什么。只补空着的字段的可以直接「应用」；要覆盖现有资料或人物有重名要选的，
                 打开导入本地资料对话框逐项确认（与片库管理同一个对话框）。 -->
            <template v-else-if="section.key === 'local_metadata_updates' && section.ready">
              <ul class="pending-list">
                <li
                  v-for="row in local.rows"
                  :key="row.id"
                  class="pending-list__row"
                  :data-test="`pending-local-row-${row.id}`"
                >
                  <span class="pending-list__title">{{ row.title }}<small v-if="row.year">{{ row.year }}</small></span>
                  <span :class="['pending-list__meta', { 'pending-list__meta--warn': row.plan.kind !== 'ready' }]">{{ localRowText(row) }}</span>
                  <button
                    v-if="row.plan.kind === 'ready'"
                    type="button"
                    class="btn btn--secondary btn--sm"
                    :disabled="Boolean(local.busyID) || Boolean(local.applying)"
                    :data-test="`pending-local-apply-${row.id}`"
                    @click="applyLocalRow(row)"
                  >{{ local.busyID === row.id ? '正在应用…' : '应用' }}</button>
                  <button
                    v-else-if="row.plan.kind === 'review'"
                    type="button"
                    class="btn btn--secondary btn--sm"
                    :disabled="Boolean(local.applying)"
                    :data-test="`pending-local-review-${row.id}`"
                    @click="localReviewIDs = [row.id]"
                  >逐项确认…</button>
                </li>
              </ul>
              <button
                v-if="local.cursor"
                type="button"
                class="btn btn--secondary btn--sm pending-section__more"
                data-test="pending-local-more"
                :disabled="local.loadingMore"
                @click="loadMoreLocal"
              >{{ local.loadingMore ? '正在加载…' : '加载更多' }}</button>
            </template>
          </template>
        </template>

        <!-- 原工作台的脚注留作「AI 视频标签」区块的页脚：失败的打标不计入待处理，引到任务中心。 -->
        <p v-if="section.key === 'ai_video_candidates' && failedRuns > 0" class="pending-section__note" data-test="pending-ai-failed">
          另有 {{ formatCount(failedRuns) }} 个视频 AI 打标失败（不计入待处理）。
          <button type="button" class="btn-link" data-test="pending-open-task-center" @click="$emit('open-task-center')">在任务中心查看</button>
        </p>
      </section>
    </div>

    <CollectionSuggestionPanel v-if="suggestionPanelOpen" @close="closeSuggestionPanel" />
    <LocalMetadataDialog :visible="localReviewIDs.length > 0" :video-ids="localReviewIDs" @close="closeLocalReview" />
  </section>
</template>

<script>
import {
  ApplyLocalMetadataBatch, CountLibraryVideos, GetPendingWorkSummary, ListCollectionSuggestions, PreviewLocalMetadataBatch,
  SearchLibraryVideoPage
} from '../../../wailsjs/go/main/App';
import CollectionSuggestionPanel from '../CollectionSuggestionPanel.vue';
import FaceClusterReviewPanel from '../FaceClusterReviewPanel.vue';
import LocalMetadataDialog from '../LocalMetadataDialog.vue';
import AppBadge from '../ui/AppBadge.vue';
import AppBanner from '../ui/AppBanner.vue';
import AppIcon from '../ui/AppIcon.vue';
import EmptyState from '../ui/EmptyState.vue';
import PageHeader from '../ui/PageHeader.vue';
import SkeletonBlock from '../ui/SkeletonBlock.vue';
import { confirmAction, notify, notifyError, notifySuccess } from '../../utils/feedback.js';
import { scrubAbsolutePaths } from '../../utils/pathText.js';

// 待处理页的区块（D-P3-01，详细设计 §3.6）：GetPendingWorkSummary 的每一类一块；人脸的「待命名」与「待确认追加」
// 在同一个审阅面板里处理，合成一块。source 是这一块数量的来源：
//   summary     — GetPendingWorkSummary（与顶栏「管理」角标同一口径），fields 是要加起来的字段；
//   suggestions — ListCollectionSuggestions（摘要里的建议作品集数就是它的条数）；
//   local       — CountLibraryVideos(本地资料有更新)（摘要里同一口径）。
// 带 command 的是「进入专门工具」：交给 App 按固定命令 ID 先切到宿主页再打开（runHostCommand）。
export const PENDING_SECTIONS = Object.freeze([
  { key: 'face', label: '人脸待命名与待确认追加', source: 'summary', fields: ['face_unnamed', 'face_append_pending'] },
  { key: 'collection_suggestions', label: '建议作品集', source: 'suggestions' },
  { key: 'local_metadata_updates', label: '本地资料有更新', source: 'local' },
  {
    key: 'ai_video_candidates', label: 'AI 视频标签', source: 'summary', fields: ['ai_video_candidates'],
    command: 'library.openAIReview', action: '打开 AI 审阅',
    description: 'AI 给视频提的标签，采纳或否决之后才写进片库。'
  },
  {
    key: 'same_source_unconfirmed', label: '同源视频', source: 'summary', fields: ['same_source_unconfirmed'],
    command: 'library.openAIReview', action: '打开 AI 审阅',
    description: 'AI 认为来自同一来源的视频对，在 AI 审阅的「视频同源待审」里确认或否认。'
  },
  {
    key: 'ai_image_candidates', label: 'AI 图片标签', source: 'summary', fields: ['ai_image_candidates'],
    command: 'photos.openAIReview', action: '打开图片 AI 审阅',
    description: 'AI 给图片提的标签，在图片管理的 AI 审阅里处理。'
  },
  {
    key: 'cleanup_candidates', label: '清理候选', source: 'summary', fields: ['cleanup_candidates'],
    command: 'action:cleanup', action: '打开清理中心', shortcut: '⌘⇧K',
    description: '重复、同源片段、极短或低清的视频，在清理中心决定留哪份。'
  }
]);

// 「本地资料有更新」智能视图（D-PC39）：列表、计数与「全部应用」都按它取。
export const LOCAL_METADATA_FILTER = Object.freeze({ smart_view: 'local_metadata_updated' });
const LOCAL_PAGE_SIZE = 20;
// 后端批量预览 / 应用一次最多 500 个视频（localMetadataBatchMaxItems）；列表单页最多 200（SearchLibraryVideoPage）。
export const LOCAL_BATCH_LIMIT = 500;
const SEARCH_PAGE_LIMIT = 200;
const SUGGESTION_PREVIEW_COUNT = 5;

// 导入本地资料的字段（与 LocalMetadataDialog 同一套名字）。relation 的两项还要给每个来源名一个映射。
const LOCAL_FIELDS = [
  { name: 'title', label: '片名' },
  { name: 'original_title', label: '原始片名' },
  { name: 'description', label: '简介' },
  { name: 'year', label: '年份' },
  { name: 'people', label: '人物', resolutions: 'people_resolutions' },
  { name: 'collection', label: '作品集', resolutions: 'collection_resolutions' },
  { name: 'poster', label: '海报' },
  { name: 'fanart', label: '背景图' }
];

const LOCAL_FAILURE_TEXT = {
  metadata_conflict: '本地资料或片库资料在这期间变了，刷新后再看一眼',
  overwrite_required: '有要覆盖现有资料的项，需要逐项确认',
  video_not_found: '这部视频已不在片库中',
  batch_limit_exceeded: `一次最多 ${LOCAL_BATCH_LIMIT} 个视频`
};

// 一部视频的本地资料「直接应用」方案，口径与后端 ApplyDefaults（自动补全）一致：只选默认勾选的字段（只补空着的），
// 人物 / 作品集要每个来源名都有默认映射（唯一匹配或新建）才选。有要覆盖现有资料的字段、或有默认选不了的映射时，
// 不能一键应用——应用会把这一版本地资料记成已处理，没选的那些改动就再也不会提示了——归为 review，交给逐项确认。
// 返回 { kind: 'ready' | 'review' | 'missing', fill, overwrite, unresolved, request }，request 是 ApplyLocalMetadataBatch 的一项。
export function planLocalMetadataApply(diff) {
  if (!diff || diff.status === 'missing') return { kind: 'missing', fill: [], overwrite: [], unresolved: [], request: null };
  const request = {
    video_id: Number(diff.video_id),
    manifest_sha256: diff.manifest_sha256 || '',
    current_sha256: diff.current_sha256 || '',
    selected_fields: [],
    overwrite_fields: [],
    people_resolutions: [],
    collection_resolutions: []
  };
  const fill = [];
  const overwrite = [];
  const unresolved = [];
  for (const field of LOCAL_FIELDS) {
    const value = diff[field.name];
    if (!value) continue;
    if (value.requires_overwrite) {
      overwrite.push(field.label);
      continue;
    }
    if (!value.default_selected) continue;
    if (field.resolutions) {
      const sources = value.source || [];
      if (sources.some(candidate => !candidate?.default_mode)) {
        unresolved.push(field.label);
        continue;
      }
      request[field.resolutions] = sources.map(candidate => ({
        normalized_name: candidate.normalized_name,
        mode: candidate.default_mode,
        entity_id: Number(candidate.default_entity_id || 0)
      }));
    }
    request.selected_fields.push(field.name);
    fill.push(field.label);
  }
  const kind = overwrite.length || unresolved.length ? 'review' : 'ready';
  return { kind, fill, overwrite, unresolved, request };
}

// 预览失败一条（{ video_id, error_code, message }）的说法：认得的错误码给中文，其余用原文并擦掉路径。
export function localMetadataFailureText(failure) {
  if (!failure) return '没有返回原因';
  return LOCAL_FAILURE_TEXT[failure.error_code] || scrubAbsolutePaths(failure.message || '') || '没有返回原因';
}

// 一页视频 + 它们的批量预览 → 列表行。预览里报失败的视频记成 failed，预览里没有的按没找到本地资料算。
export function buildLocalRows(videos, preview) {
  const diffs = new Map((preview?.diffs || []).map(diff => [Number(diff.video_id), diff]));
  const failures = new Map((preview?.failures || []).filter(item => item?.video_id).map(item => [Number(item.video_id), item]));
  return (videos || []).map(video => {
    const id = Number(video.id);
    const failure = failures.get(id);
    const plan = failure
      ? { kind: 'failed', fill: [], overwrite: [], unresolved: [], request: null, reason: localMetadataFailureText(failure) }
      : planLocalMetadataApply(diffs.get(id));
    return { id, title: video.display_title || video.name || `视频 #${id}`, year: Number(video.year) > 0 ? Number(video.year) : null, plan };
  });
}

// 「全部应用」前给用户看的汇总：能直接应用的（按字段计数）、只标已处理的、要逐项确认而跳过的、读不出的。
export function summarizeLocalMetadataPlans(plans, { failed = 0, truncated = false } = {}) {
  const ready = plans.filter(plan => plan.kind === 'ready');
  const review = plans.filter(plan => plan.kind === 'review').length;
  const skipped = plans.filter(plan => plan.kind === 'missing').length + failed;
  const unchanged = ready.filter(plan => !plan.fill.length).length;
  const tally = new Map();
  for (const plan of ready) for (const label of plan.fill) tally.set(label, (tally.get(label) || 0) + 1);
  const lines = [];
  if (ready.length > unchanged) {
    lines.push(`补全 ${ready.length - unchanged} 个视频：${[...tally].map(([label, count]) => `${label} ${count}`).join('、')}。`);
  }
  if (unchanged) lines.push(`${unchanged} 个的本地资料与片库一致，只标为已处理。`);
  if (review) lines.push(`${review} 个有要覆盖现有资料或要选人物的项，这次跳过，在列表里逐项确认。`);
  if (skipped) lines.push(`${skipped} 个读不出本地资料，这次跳过。`);
  if (truncated) lines.push(`一次最多 ${LOCAL_BATCH_LIMIT} 个，余下的应用完再点一次「全部应用」。`);
  return { ready, review, skipped, lines };
}

// 「本地资料有更新」的视频 ID，最多 limit 个（按列表的顺序翻页取）。truncated：后面还有。
// 一页没带来任何新 ID 就停（游标没往前走时不空转）。
async function collectLocalMetadataIDs(limit) {
  const ids = [];
  let cursor = null;
  do {
    const request = { filter: { ...LOCAL_METADATA_FILTER }, limit: Math.min(SEARCH_PAGE_LIMIT, limit - ids.length) };
    if (cursor) request.cursor = cursor;
    const page = await SearchLibraryVideoPage(request);
    const before = ids.length;
    for (const video of page?.videos || []) {
      const id = Number(video?.id);
      if (id > 0 && !ids.includes(id)) ids.push(id);
    }
    cursor = page?.next_cursor || null;
    if (ids.length === before) break;
  } while (cursor && ids.length < limit);
  return { ids: ids.slice(0, limit), truncated: Boolean(cursor) && ids.length >= limit };
}

function errorText(err) {
  return String(err?.message || err || '') || '读取失败';
}

// 待处理页（D-P3-01）。App 只在这一页时挂载它（v-if），每次进来都重读。顶栏「管理」角标由 App 自己每分钟读一次
// GetPendingWorkSummary；这一页读到新的摘要时发 badge-change 让角标立刻跟上。
export default {
  name: 'PendingWorkPage',
  components: { AppBadge, AppBanner, AppIcon, CollectionSuggestionPanel, EmptyState, FaceClusterReviewPanel, LocalMetadataDialog, PageHeader, SkeletonBlock },
  emits: ['run-command', 'open-task-center', 'badge-change', 'navigate'],
  data() {
    return {
      // 每个来源一份状态：loading 只在还没有读到过时为真（之后的刷新不换成骨架）；updatedAt 是这一次读成功的时间。
      summary: { loading: true, error: '', value: null, updatedAt: null },
      suggestions: { loading: true, error: '', items: null, updatedAt: null },
      local: {
        loading: true, error: '', count: null, rows: [], cursor: null, loadingMore: false, updatedAt: null,
        // applying：'' | 'preview'（核对中）| 'confirm'（等用户确认）| 'apply'（应用中）；busyID：正在单独应用的那一行。
        applying: '', busyID: 0
      },
      suggestionPanelOpen: false,
      localReviewIDs: []
    };
  },
  computed: {
    sectionViews() {
      return PENDING_SECTIONS.map(section => {
        const state = this.sourceState(section.source);
        const count = this.sectionCount(section);
        const ready = !state.loading && !state.error && count !== null;
        return {
          ...section,
          loading: state.loading,
          error: state.error,
          updatedAt: state.updatedAt,
          count,
          ready,
          done: ready && count === 0,
          showCount: ready && count > 0,
          detail: section.key === 'face' && ready ? this.faceDetail : ''
        };
      });
    },
    allClear() {
      return this.sectionViews.every(section => section.done);
    },
    headerSubtitle() {
      if (this.summary.value && !this.summary.error) return `共 ${this.formatCount(this.summary.value.total)} 项`;
      if (this.summary.loading) return '正在统计…';
      return '待处理总数没读到';
    },
    failedRuns() {
      return this.summary.error ? 0 : Number(this.summary.value?.ai_video_failed_runs || 0);
    },
    faceDetail() {
      const value = this.summary.value || {};
      const parts = [];
      if (Number(value.face_unnamed) > 0) parts.push(`待命名 ${this.formatCount(value.face_unnamed)} 组`);
      if (Number(value.face_append_pending) > 0) parts.push(`待确认追加 ${this.formatCount(value.face_append_pending)} 组`);
      return parts.join(' · ');
    },
    suggestionPreview() {
      return (this.suggestions.items || []).slice(0, SUGGESTION_PREVIEW_COUNT);
    },
    suggestionRest() {
      return Math.max(0, (this.suggestions.items || []).length - SUGGESTION_PREVIEW_COUNT);
    },
    applyAllLabel() {
      return { preview: '正在核对…', confirm: '等待确认…', apply: '正在应用…' }[this.local.applying] || '全部应用';
    }
  },
  mounted() {
    this.refreshSummary();
    this.loadSuggestions();
    this.loadLocal();
  },
  methods: {
    sourceState(source) {
      if (source === 'suggestions') return this.suggestions;
      if (source === 'local') return this.local;
      return this.summary;
    },
    sectionCount(section) {
      if (section.source === 'suggestions') return this.suggestions.items ? this.suggestions.items.length : null;
      if (section.source === 'local') return this.local.count;
      const value = this.summary.value;
      if (!value) return null;
      return section.fields.reduce((sum, field) => sum + Number(value[field] || 0), 0);
    },
    // 页头「刷新」：三个来源都重读，人脸面板也让它自己重取一次。
    refreshAll() {
      this.refreshSummary();
      this.loadSuggestions();
      this.loadLocal();
      [].concat(this.$refs.facePanel || []).forEach(panel => panel?.refresh?.());
    },
    retrySection(section) {
      if (section.source === 'suggestions') return this.loadSuggestions();
      if (section.source === 'local') return this.loadLocal();
      return this.refreshSummary();
    },
    // 摘要：数量、页头总数与角标。数据库维护、待重启等终态下后端直接给中文说明（不去读库），原样显示，角标清零。
    async refreshSummary() {
      const token = Symbol('pending-summary');
      this._summaryToken = token;
      if (!this.summary.value) this.summary = { ...this.summary, loading: true, error: '' };
      try {
        const value = await GetPendingWorkSummary();
        if (this._summaryToken !== token) return;
        this.summary = { loading: false, error: '', value: value || {}, updatedAt: new Date() };
        this.$emit('badge-change', Number(value?.total || 0));
      } catch (err) {
        if (this._summaryToken !== token) return;
        this.summary = { ...this.summary, loading: false, error: errorText(err) };
        this.$emit('badge-change', 0);
      }
    },
    async loadSuggestions() {
      const token = Symbol('pending-suggestions');
      this._suggestionsToken = token;
      if (!this.suggestions.items) this.suggestions = { ...this.suggestions, loading: true, error: '' };
      try {
        const items = await ListCollectionSuggestions();
        if (this._suggestionsToken !== token) return;
        this.suggestions = { loading: false, error: '', items: Array.isArray(items) ? items : [], updatedAt: new Date() };
      } catch (err) {
        if (this._suggestionsToken !== token) return;
        this.suggestions = { ...this.suggestions, loading: false, error: errorText(err) };
      }
    },
    // 本地资料有更新：总数 + 第一页 + 这一页的批量预览（每行要说清会改什么）。三样任一失败整块出错。
    async loadLocal() {
      const token = Symbol('pending-local');
      this._localToken = token;
      if (this.local.count === null) this.local = { ...this.local, loading: true, error: '' };
      try {
        const [count, page] = await Promise.all([
          CountLibraryVideos({ ...LOCAL_METADATA_FILTER }),
          SearchLibraryVideoPage({ filter: { ...LOCAL_METADATA_FILTER }, limit: LOCAL_PAGE_SIZE })
        ]);
        const rows = await this.previewRows(page?.videos || []);
        if (this._localToken !== token) return;
        this.local = {
          ...this.local, loading: false, error: '', count: Number(count || 0), rows, cursor: page?.next_cursor || null,
          loadingMore: false, updatedAt: new Date()
        };
      } catch (err) {
        if (this._localToken !== token) return;
        this.local = { ...this.local, loading: false, error: errorText(err) };
      }
    },
    async loadMoreLocal() {
      const cursor = this.local.cursor;
      if (!cursor || this.local.loadingMore) return;
      const token = this._localToken;
      this.local = { ...this.local, loadingMore: true };
      try {
        const page = await SearchLibraryVideoPage({ filter: { ...LOCAL_METADATA_FILTER }, cursor, limit: LOCAL_PAGE_SIZE });
        const known = new Set(this.local.rows.map(row => row.id));
        const rows = await this.previewRows((page?.videos || []).filter(video => !known.has(Number(video.id))));
        if (this._localToken !== token) return;
        this.local = { ...this.local, rows: [...this.local.rows, ...rows], cursor: page?.next_cursor || null, loadingMore: false };
      } catch (err) {
        if (this._localToken !== token) return;
        this.local = { ...this.local, loadingMore: false };
        notifyError(`加载更多失败：${errorText(err)}`);
      }
    },
    async previewRows(videos) {
      if (!videos.length) return [];
      const preview = await PreviewLocalMetadataBatch(videos.map(video => Number(video.id)));
      return buildLocalRows(videos, preview);
    },
    // 页内动作做完：只重读这一块和摘要（角标）。
    refreshLocalSection() {
      return Promise.all([this.loadLocal(), this.refreshSummary()]);
    },
    refreshSuggestionSection() {
      return Promise.all([this.loadSuggestions(), this.refreshSummary()]);
    },
    async applyLocalRow(row) {
      if (row?.plan?.kind !== 'ready' || this.local.busyID || this.local.applying) return;
      this.local = { ...this.local, busyID: row.id };
      try {
        const result = await ApplyLocalMetadataBatch({ requests: [row.plan.request] });
        if (Number(result?.succeeded || 0) > 0) notifySuccess(`已应用《${row.title}》的本地资料`);
        else notifyError(`《${row.title}》没应用上：${localMetadataFailureText(result?.failures?.[0])}`);
      } catch (err) {
        notifyError(`应用本地资料失败：${errorText(err)}`);
      } finally {
        this.local = { ...this.local, busyID: 0 };
        this.refreshLocalSection();
      }
    },
    // 「全部应用」：先取全部（最多一批 500 个）做批量预览，把会补什么、跳过什么写进确认框，确认后只应用能直接应用的。
    async applyAllLocal() {
      if (this.local.applying || this.local.busyID) return;
      this.local = { ...this.local, applying: 'preview' };
      let summary;
      try {
        const { ids, truncated } = await collectLocalMetadataIDs(LOCAL_BATCH_LIMIT);
        const preview = ids.length ? await PreviewLocalMetadataBatch(ids) : { diffs: [], failures: [] };
        const failures = (preview?.failures || []).filter(item => item?.video_id);
        const failedIDs = new Set(failures.map(item => Number(item.video_id)));
        const diffs = new Map((preview?.diffs || []).map(diff => [Number(diff.video_id), diff]));
        const plans = ids.filter(id => !failedIDs.has(id)).map(id => planLocalMetadataApply(diffs.get(id)));
        summary = summarizeLocalMetadataPlans(plans, { failed: failures.length, truncated });
      } catch (err) {
        this.local = { ...this.local, applying: '' };
        notifyError(`核对本地资料失败：${errorText(err)}`);
        return;
      }
      if (!summary.ready.length) {
        this.local = { ...this.local, applying: '' };
        notify(summary.lines.length ? `没有能直接应用的。${summary.lines.join('')}` : '没有要应用的本地资料。');
        this.refreshLocalSection();
        return;
      }
      this.local = { ...this.local, applying: 'confirm' };
      const confirmed = await confirmAction({
        title: '全部应用本地资料',
        message: summary.lines.join('\n'),
        impact: '只填空着的字段，不覆盖已有资料；应用之后不能撤销。',
        confirmText: `应用 ${summary.ready.length} 个`
      });
      if (!confirmed) {
        this.local = { ...this.local, applying: '' };
        return;
      }
      this.local = { ...this.local, applying: 'apply' };
      try {
        const result = await ApplyLocalMetadataBatch({ requests: summary.ready.map(plan => plan.request) });
        const succeeded = Number(result?.succeeded || 0);
        const failed = Number(result?.failed || 0);
        if (succeeded > 0) notifySuccess(`已应用 ${this.formatCount(succeeded)} 个视频的本地资料`);
        if (failed > 0) notifyError(`${this.formatCount(failed)} 个没应用上：${localMetadataFailureText(result?.failures?.[0])}`);
      } catch (err) {
        notifyError(`应用本地资料失败：${errorText(err)}`);
      } finally {
        this.local = { ...this.local, applying: '' };
        this.refreshLocalSection();
      }
    },
    closeLocalReview() {
      this.localReviewIDs = [];
      this.refreshLocalSection();
    },
    closeSuggestionPanel() {
      this.suggestionPanelOpen = false;
      this.refreshSuggestionSection();
    },
    openTool(section) {
      this.$emit('run-command', section.command, section.label);
    },
    localRowText(row) {
      const plan = row.plan;
      if (plan.kind === 'failed') return `读不出本地资料：${plan.reason}`;
      if (plan.kind === 'missing') return '没找到本地资料文件';
      if (plan.kind === 'review') {
        const parts = [];
        if (plan.overwrite.length) parts.push(`要覆盖现有的${plan.overwrite.join('、')}`);
        if (plan.unresolved.length) parts.push(`${plan.unresolved.join('、')}有重名，要选是哪一个`);
        return `${parts.join('；')}，需逐项确认`;
      }
      return plan.fill.length ? `补全：${plan.fill.join('、')}` : '与片库一致，应用后不再提示';
    },
    suggestionMeta(item) {
      const count = `${this.formatCount(item.member_count)} 集`;
      const append = item?.kind === 'append' || Boolean(item?.target_collection_id);
      return append ? `新增 ${count} → 「${item.target_collection_name || '现有作品集'}」` : count;
    },
    clock(date) {
      return date.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', hour12: false });
    },
    formatCount(value) {
      return Number(value || 0).toLocaleString('zh-CN');
    }
  }
};
</script>

<style scoped>
.pending-work-page { display: grid; gap: var(--space-4); align-content: start; padding-bottom: var(--space-7); }
.pending-work-page > :deep(.ui-page-header) { margin-bottom: 0; }
.pending-work-page__sections { display: grid; gap: var(--space-3); }

.pending-section {
  display: grid;
  gap: var(--space-3);
  padding: var(--space-4);
  border: 1px solid var(--hairline);
  border-radius: var(--radius-lg);
  background: var(--panel-bg);
}
/* 处理完的区块收成一行：只剩标题、「已处理完」和更新时间。 */
.pending-section--done { padding: var(--space-2) var(--space-4); background: var(--surface-faint); }
.pending-section__head { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-2) var(--space-3); min-width: 0; }
.pending-section__title { margin: 0; color: var(--text-primary); font-size: var(--fs-section); font-weight: var(--fw-semibold); }
.pending-section--done .pending-section__title { color: var(--text-secondary); font-size: var(--fs-body); font-weight: var(--fw-medium); }
.pending-section__done { display: inline-flex; align-items: center; gap: var(--space-1); color: var(--success-color); font-size: var(--fs-secondary); }
.pending-section__time { color: var(--text-muted); font-size: var(--fs-caption); font-variant-numeric: tabular-nums; }
.pending-section__actions { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-2); margin-left: auto; }
.pending-section__kbd {
  padding: 1px 5px;
  border: 1px solid var(--hairline);
  border-radius: var(--radius-sm);
  color: var(--text-secondary);
  font-family: inherit;
  font-size: var(--fs-caption);
  line-height: 1.5;
}
.pending-section__lead { margin: 0; color: var(--text-secondary); font-size: var(--fs-secondary); line-height: 1.6; }
.pending-section__note { margin: 0; color: var(--warning-text); font-size: var(--fs-secondary); }
.pending-section__more { justify-self: start; }

.pending-list { display: grid; gap: var(--space-1); margin: 0; padding: 0; list-style: none; }
.pending-list__row {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  min-height: var(--h-unit);
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--hairline);
  border-radius: var(--radius);
  background: var(--control-bg);
  font-size: var(--fs-secondary);
}
.pending-list__title { flex: 0 1 auto; min-width: 0; overflow: hidden; color: var(--text-primary); font-weight: var(--fw-medium); text-overflow: ellipsis; white-space: nowrap; }
.pending-list__title small { margin-left: var(--space-2); color: var(--text-muted); font-size: var(--fs-caption); font-weight: var(--fw-regular); }
.pending-list__meta { flex: 1 1 auto; min-width: 0; color: var(--text-secondary); font-size: var(--fs-caption); }
.pending-list__meta--warn { color: var(--warning-text); }
.pending-list__row > .btn { flex: none; }
</style>
