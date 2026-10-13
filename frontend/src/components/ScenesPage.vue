<template>
  <section
    :class="['page-content', 'scenes-page', { 'scenes-page--dropping': dropping }]"
    data-test="scenes-page"
    @dragover.prevent="onDragOver"
    @dragleave="onDragLeave"
    @drop.prevent="onDrop"
  >
    <div class="scenes-heading">
      <div>
        <h2>场景检索</h2>
        <p>在字幕对白与画面内容里找具体的时间区间。没建索引的内容不会被当成"没有命中"。也可以拖进、粘贴一张截图或剧照，找出它是哪部片的哪一段。</p>
      </div>
    </div>

    <form class="scenes-search" data-test="scenes-search-form" @submit.prevent="search">
      <input
        v-model="query"
        data-test="scenes-query"
        class="text-input scenes-search__input"
        type="search"
        :maxlength="maxQueryLength"
        placeholder="例如：雨夜里的告白、海边日落、两个人在厨房吵架"
      />
      <div class="scenes-modes" role="radiogroup" aria-label="检索模式">
        <button
          v-for="option in modes"
          :key="option.value"
          type="button"
          role="radio"
          :aria-checked="mode === option.value"
          :data-test="`scenes-mode-${option.value}`"
          :class="['scenes-mode', { active: mode === option.value }]"
          @click="setMode(option.value)"
        >{{ option.label }}</button>
      </div>
      <button type="submit" class="btn-primary" data-test="scenes-submit" :disabled="!canSearch">检索</button>
      <button type="button" class="btn-secondary" data-test="scenes-pick-image" title="选一张图片，也可以直接拖进本页或粘贴" @click="$refs.imageInput.click()">以图搜片</button>
      <input ref="imageInput" type="file" accept="image/png,image/jpeg,image/webp,image/gif,image/bmp" hidden data-test="scenes-image-input" @change="onImagePicked" />
    </form>

    <!-- 以图搜片 / 相似画面的查询来源：一张上传的图，或某部片的某一刻。 -->
    <div v-if="imageQuery || similarSource" class="scenes-image-query" data-test="scenes-image-query">
      <img v-if="imageQuery" :src="imageQuery.dataURL" alt="查询图片" class="scenes-image-query__thumb" />
      <img v-else :src="frameURL({ video_id: similarSource.videoID, start_ms: similarSource.atMS })" alt="" class="scenes-image-query__thumb" />
      <span class="scenes-image-query__text">{{ imageQueryText }}</span>
      <button type="button" class="btn-secondary btn-compact" data-test="scenes-clear-image" @click="clearImageQuery">清除</button>
    </div>

    <div class="scenes-scope" data-test="scenes-scope">
      <span>范围：{{ scopeFilter ? '片库当前筛选' : '全部视频' }}</span>
      <button v-if="scopeFilter" type="button" class="btn-secondary btn-compact" data-test="scenes-clear-scope" @click="clearScope">改为全部视频</button>
    </div>

    <div class="scenes-coverage" data-test="scenes-coverage" role="status">
      <div class="scenes-coverage__bar"><i :style="{ width: `${visualPercent}%` }"></i></div>
      <span>{{ coverageText }}</span>
      <span class="scenes-coverage__provider" data-test="scenes-provider">画面来源：{{ providerText }}</span>
    </div>

    <div class="scenes-actions" data-test="scenes-actions">
      <span v-if="!imageSearch && !isExternal" class="scenes-runtime" data-test="scenes-runtime-state">{{ runtimeText }}</span>
      <button v-if="!imageSearch && !isExternal && runtimePreparing" type="button" class="btn-secondary" data-test="scenes-cancel-prepare" @click="cancelPrepare">取消准备</button>
      <button v-else-if="!imageSearch && !isExternal && runtimeCanPrepare" type="button" class="btn-primary" data-test="scenes-prepare" @click="prepareRuntime">准备模型</button>
      <button v-if="indexRunning" type="button" class="btn-secondary" data-test="scenes-cancel-index" @click="cancelIndex">取消建立索引</button>
      <button
        v-else
        type="button"
        class="btn-secondary"
        data-test="scenes-start-index"
        :disabled="!canStartIndex"
        @click="startIndex"
      >{{ scopeFilter ? '为当前范围建立画面索引' : '为全部建立画面索引' }}</button>
      <button type="button" class="btn-secondary" data-test="scenes-clear-stale" :disabled="indexRunning" @click="clearStale">清理旧索引</button>
    </div>
    <p v-if="imageSearch" class="help-text" data-test="scenes-apple-note">以图检索使用苹果系统能力，无需准备中文文字模型；第一次使用需要建立苹果画面索引。</p>
    <p v-if="indexText" class="scenes-index-status" data-test="scenes-index-status">{{ indexText }}</p>

    <p v-if="errorMessage" class="scenes-error" role="alert" data-test="scenes-error">{{ errorMessage }}</p>
    <ul v-if="notices.length" class="scenes-notices" data-test="scenes-notices">
      <li v-for="code in notices" :key="code" :data-test="`scenes-notice-${code}`">{{ noticeText(code) }}</li>
    </ul>

    <div v-if="searching" class="scenes-empty" data-test="scenes-loading">正在检索…</div>
    <div v-else-if="searched && groups.length === 0" class="scenes-empty" data-test="scenes-empty">
      <h3>{{ emptyTitle }}</h3>
      <p>{{ emptyHint }}</p>
    </div>
    <div v-else-if="!searched" class="scenes-empty" data-test="scenes-idle">
      <p>输入一句描述或台词，选择在对白、画面或两者里检索；或者把一张截图拖进来、粘贴进来。</p>
    </div>

    <div v-else class="scenes-results" data-test="scenes-results">
      <section v-for="group in groups" :key="group.videoID" class="scenes-group" :data-test="`scenes-group-${group.videoID}`">
        <h3 class="scenes-group__title">{{ group.title }} <small>{{ group.hits.length }} 处</small></h3>
        <div v-for="(hit, index) in group.hits" :key="`${hit.source}-${hit.start_ms}-${index}`" class="scenes-hit-row">
        <button
          type="button"
          class="scenes-hit"
          :data-test="`scenes-hit-${group.videoID}-${index}`"
          @click="openHit(hit)"
        >
          <img class="scenes-hit__frame" :src="frameURL(hit)" alt="" loading="lazy" />
          <span class="scenes-hit__body">
            <span class="scenes-hit__meta">
              <span :class="['scenes-hit__source', `scenes-hit__source--${hit.source}`]">{{ sourceLabel(hit.source) }}</span>
              <span class="scenes-hit__range">{{ rangeText(hit) }}</span>
            </span>
            <span v-if="hit.context_before" class="scenes-hit__context">{{ hit.context_before }}</span>
            <span class="scenes-hit__text">{{ hitText(hit) }}</span>
            <span v-if="hit.context_after" class="scenes-hit__context">{{ hit.context_after }}</span>
          </span>
        </button>
        <div class="scenes-hit__actions">
          <button
            type="button"
            class="btn-secondary btn-compact"
            title="用这一刻的画面在全部视频里找相似的段"
            :data-test="`scenes-similar-${group.videoID}-${index}`"
            @click="searchSimilar({ videoID: hit.video_id, atMS: hit.start_ms, title: group.title })"
          >相似画面</button>
          <!-- 台词卡（2026-10-12）：只有带台词文字的对白命中才有。 -->
          <button
            v-if="hit.source === 'dialogue' && hit.text"
            type="button"
            class="btn-secondary btn-compact"
            title="把这句台词和这一刻的画面做成一张图"
            :data-test="`scenes-quote-${group.videoID}-${index}`"
            @click="openQuoteCard(hit, group.title)"
          >台词卡</button>
        </div>
        </div>
      </section>
    </div>
    <QuoteCardDialog v-if="quoteCard" v-bind="quoteCard" @close="quoteCard = null" />
  </section>
</template>

<script>
import {
  CancelSceneIndex,
  CancelSceneRuntimePrepare,
  ClearStaleSceneIndex,
  GetSceneCoverage,
  GetSceneIndexStatus,
  GetSceneRuntimeStatus,
  PrepareSceneRuntime,
  SearchScenes,
  SearchScenesByImage,
  StartSceneIndex
} from '../../wailsjs/go/main/App';
import { confirmAction, notifyError, notifySuccess } from '../utils/feedback.js';
import {
  SCENE_MODES,
  SCENE_QUERY_MAX_LENGTH,
  SCENE_SOURCE_LABELS,
  createRequestGeneration,
  formatSceneRange,
  formatSceneTime,
  readSceneQueryImage,
  groupSceneHits,
  sceneCoverageParts,
  sceneErrorText,
  sceneFrameURL,
  sceneNoticeText,
  scenePlaybackStartMs,
  sceneVisualSkipped,
  sceneVisualCoveragePercent
} from '../utils/sceneSearch.js';
import { quoteCardMomentMS } from '../utils/quoteCard.js';
import QuoteCardDialog from './QuoteCardDialog.vue';

const PREPARABLE_STATES = ['missing_python', 'missing_venv', 'missing_model', 'download_failed'];
const RUNTIME_STATE_TEXT = {
  available: '本地画面模型已就绪',
  missing_python: '本地画面模型未就绪',
  missing_venv: '本地画面模型未就绪',
  missing_model: '本地画面模型未下载',
  download_failed: '模型下载失败',
  incompatible: '当前平台不支持本地画面模型'
};

// 场景检索页（D-MW-SCENES）：查询、模式切换、覆盖率与索引动作、按视频分组的命中。
// 每次检索取一个请求代次，晚到的旧结果直接丢弃；点击命中交给宿主打开详情抽屉并内嵌播放。
export default {
  name: 'ScenesPage',
  components: { QuoteCardDialog },
  props: {
    pageActive: { type: Boolean, default: true },
    // 片库工具栏「在当前筛选中搜场景」带来的范围：{ filter, token }；为空表示全部视频。
    // 详情抽屉「找相似画面」另带 similar：{ videoID, atMS, title }，进来直接按这一刻的画面检索。
    scopeRequest: { type: Object, default: null }
  },
  emits: ['open-video-at'],
  data() {
    return {
      query: '',
      mode: 'all',
      modes: SCENE_MODES,
      maxQueryLength: SCENE_QUERY_MAX_LENGTH,
      scopeFilter: null,
      coverage: null,
      runtimeStatus: null,
      indexStatus: null,
      result: null,
      searching: false,
      searched: false,
      errorMessage: '',
      // 以图搜片：上传的图 { dataURL, name }；相似画面：{ videoID, atMS, title }。两者互斥，检索结果都是画面命中。
      imageQuery: null,
      similarSource: null,
      dropping: false,
      // 正在做的台词卡：{ videoID, atMS, quote, title }；为空表示弹窗关着。
      quoteCard: null
    };
  },
  computed: {
    canSearch() {
      const query = this.query.trim();
      return query.length > 0 && query.length <= SCENE_QUERY_MAX_LENGTH;
    },
    groups() {
      return groupSceneHits(this.result?.hits || []);
    },
    notices() {
      return this.result?.notices || [];
    },
    isExternal() {
      return this.coverage?.visual_provider === 'external';
    },
    providerText() {
      return this.imageSearch ? '苹果系统图像特征' : this.isExternal ? '外部描述（AI 标签接口）' : '本地中文文字模型';
    },
    activeCoverage() {
      if (!this.coverage) return null;
      return this.imageSearch ? { ...this.coverage, visual_indexed: this.coverage.image_indexed || 0 } : this.coverage;
    },
    coverageText() {
      return this.activeCoverage ? sceneCoverageParts(this.activeCoverage).join(' · ') : '正在读取覆盖率…';
    },
    visualPercent() {
      return sceneVisualCoveragePercent(this.activeCoverage);
    },
    runtimePreparing() {
      return Boolean(this.runtimeStatus?.preparing);
    },
    runtimeCanPrepare() {
      return PREPARABLE_STATES.includes(this.runtimeStatus?.state);
    },
    runtimeText() {
      const status = this.runtimeStatus;
      if (!status) return '正在检查本地画面模型…';
      if (status.preparing) {
        const total = Number(status.total_bytes || 0);
        const percent = total > 0 ? Math.min(100, Math.round((Number(status.downloaded_bytes || 0) / total) * 100)) : 0;
        return `正在准备：${status.message || ''}${total > 0 ? ` ${percent}%` : ''}`;
      }
      return RUNTIME_STATE_TEXT[status.state] || '本地画面模型未就绪';
    },
    indexRunning() {
      return Boolean(this.indexStatus?.running);
    },
    canStartIndex() {
      return this.imageSearch ? Boolean(this.coverage?.image_available) : this.isExternal || this.runtimeStatus?.state === 'available';
    },
    indexText() {
      const status = this.indexStatus;
      if (!status) return '';
      if (status.running) {
        if (status.preparing) return '正在整理需要建立画面索引的视频…';
        const current = status.current_video_name ? `，当前 ${status.current_video_name}` : '';
        return `正在建立画面索引 ${status.processed}/${status.total}${current}`;
      }
      if (status.cancelled) return `已取消，本轮处理了 ${status.processed} 部`;
      if (status.completed) return `上一轮：成功 ${status.succeeded} 部，跳过 ${status.skipped} 部，失败 ${status.failed} 部`;
      return '';
    },
    imageSearch() {
      return Boolean(this.imageQuery || this.similarSource);
    },
    imageQueryText() {
      if (this.imageQuery) return `按这张图找画面${this.imageQuery.name ? `：${this.imageQuery.name}` : ''}`;
      if (!this.similarSource) return '';
      return `与《${this.similarSource.title || '这部片'}》${formatSceneTime(this.similarSource.atMS)} 相似的画面（不含这一刻前后 10 秒）`;
    },
    // 画面部分没有执行时不能说"没有找到"：只有真正检索过的部分才谈得上没有命中（M-5）。
    visualSkipped() {
      return (this.imageSearch || this.mode !== 'dialogue') && sceneVisualSkipped(this.notices);
    },
    emptyTitle() {
      if (!this.visualSkipped) return '在已索引的内容里没有找到';
      return this.imageSearch || this.mode === 'visual' ? '画面检索没有执行' : '对白里没有找到，画面检索没有执行';
    },
    emptyHint() {
      if (this.visualSkipped) return '原因见上方提示；处理之后再检索一次。';
      const coverage = this.activeCoverage || {};
      const missing = Number(coverage.total_videos || 0) - Number(coverage.visual_indexed || 0);
      const hints = [];
      if (this.mode !== 'dialogue' && missing > 0) hints.push(`还有 ${missing} 部视频没有画面索引`);
      if (this.mode !== 'visual' && Number(coverage.subtitle_unindexed || 0) > 0) {
        hints.push(`${coverage.subtitle_unindexed} 部视频只有其他格式或内嵌字幕，没有进入对白索引`);
      }
      return hints.length ? `${hints.join('；')}，这些内容不在本次检索范围内。` : '换个说法或改用其他模式再试试。';
    }
  },
  watch: {
    scopeRequest: {
      immediate: true,
      handler(request) {
        this.scopeFilter = request?.filter ? { ...request.filter } : null;
        this.resetResults();
        this.loadCoverage();
        if (request?.similar?.videoID) this.searchSimilar(request.similar);
      }
    },
    pageActive(active) {
      if (!active) return;
      this.loadCoverage();
      this.loadRuntime();
    }
  },
  // 代次计数器放在 beforeCreate：scopeRequest 的 immediate 监听在 created 之前就会触发。
  beforeCreate() {
    this.searchGeneration = createRequestGeneration();
    this.coverageGeneration = createRequestGeneration();
  },
  mounted() {
    window.addEventListener('paste', this.onPaste);
    this.loadRuntime();
    this.loadIndexStatus();
    if (window.runtime?.EventsOn) {
      const offRuntime = window.runtime.EventsOn('scene-runtime-state', status => {
        if (status) this.runtimeStatus = status;
      });
      const offIndex = window.runtime.EventsOn('scene-index-state', status => this.applyIndexStatus(status));
      this.eventOffs = [offRuntime, offIndex].filter(off => typeof off === 'function');
    }
  },
  beforeUnmount() {
    window.removeEventListener('paste', this.onPaste);
    (this.eventOffs || []).forEach(off => off());
  },
  methods: {
    noticeText: sceneNoticeText,
    sourceLabel(source) {
      return SCENE_SOURCE_LABELS[source] || source;
    },
    rangeText(hit) {
      return formatSceneRange(hit.start_ms, hit.end_ms);
    },
    frameURL(hit) {
      return sceneFrameURL(hit.video_id, hit.start_ms);
    },
    hitText(hit) {
      if (hit.text) return hit.text;
      return hit.source === 'visual' ? '画面与检索描述相近' : '';
    },
    resetResults() {
      this.searchGeneration?.next();
      this.result = null;
      this.searched = false;
      this.searching = false;
      this.errorMessage = '';
    },
    clearScope() {
      this.scopeFilter = null;
      this.resetResults();
      this.loadCoverage();
    },
    setMode(mode) {
      if (mode === this.mode) return;
      this.mode = mode;
      if (this.searched && this.canSearch) this.search();
    },
    // 文字检索：清掉以图搜片的查询来源。
    search() {
      if (!this.canSearch) return;
      this.imageQuery = null;
      this.similarSource = null;
      return this.runSearch(() => SearchScenes({ query: this.query.trim(), mode: this.mode, filter: this.scopeFilter, limit: 50 }));
    },
    searchImage(image) {
      this.imageQuery = image;
      this.similarSource = null;
      const encoded = image.dataURL.slice(image.dataURL.indexOf(',') + 1);
      return this.runSearch(() => SearchScenesByImage({ image_base64: encoded, video_id: 0, at_ms: 0, filter: this.scopeFilter, limit: 50 }));
    },
    searchSimilar(source) {
      this.imageQuery = null;
      this.similarSource = { videoID: Number(source.videoID), atMS: Math.max(0, Math.round(Number(source.atMS) || 0)), title: source.title || '' };
      const { videoID, atMS } = this.similarSource;
      return this.runSearch(() => SearchScenesByImage({ image_base64: '', video_id: videoID, at_ms: atMS, filter: this.scopeFilter, limit: 50 }));
    },
    clearImageQuery() {
      this.imageQuery = null;
      this.similarSource = null;
      this.resetResults();
      this.loadCoverage();
    },
    async useImageFile(file) {
      try {
        const image = await readSceneQueryImage(file);
        await this.searchImage(image);
      } catch (err) {
        this.errorMessage = sceneErrorText(err);
      }
    },
    onImagePicked(event) {
      const file = event.target.files?.[0];
      event.target.value = '';
      if (file) this.useImageFile(file);
    },
    onDragOver(event) {
      if ([...(event.dataTransfer?.types || [])].includes('Files')) this.dropping = true;
    },
    onDragLeave(event) {
      if (!event.currentTarget.contains(event.relatedTarget)) this.dropping = false;
    },
    onDrop(event) {
      this.dropping = false;
      const file = [...(event.dataTransfer?.files || [])].find(item => item.type.startsWith('image/'));
      return file ? this.useImageFile(file) : undefined;
    },
    // 只在本页可见、且焦点不在输入框里时接管粘贴：输入框里粘贴文字照常。
    onPaste(event) {
      // 台词卡弹窗开着时粘贴归弹窗里的输入框，不当成以图搜片。
      if (!this.pageActive || this.quoteCard) return;
      const file = [...(event.clipboardData?.files || [])].find(item => item.type.startsWith('image/'));
      if (!file) return;
      event.preventDefault();
      return this.useImageFile(file);
    },
    async runSearch(request) {
      const generation = this.searchGeneration.next();
      this.searching = true;
      this.errorMessage = '';
      try {
        const result = await request();
        if (!this.searchGeneration.isCurrent(generation)) return;
        this.result = result || { hits: [], notices: [] };
        if (result?.coverage) this.coverage = result.coverage;
      } catch (err) {
        if (!this.searchGeneration.isCurrent(generation)) return;
        this.result = null;
        this.errorMessage = sceneErrorText(err);
      } finally {
        if (this.searchGeneration.isCurrent(generation)) {
          this.searching = false;
          this.searched = true;
        }
      }
    },
    openHit(hit) {
      this.$emit('open-video-at', { videoID: hit.video_id, startMs: scenePlaybackStartMs(hit.start_ms) });
    },
    openQuoteCard(hit, title) {
      this.quoteCard = { videoID: Number(hit.video_id), atMS: quoteCardMomentMS(hit.start_ms, hit.end_ms), quote: hit.text || '', title: title || '' };
    },
    async loadCoverage() {
      const generation = this.coverageGeneration.next();
      try {
        const coverage = await GetSceneCoverage(this.scopeFilter || {});
        if (this.coverageGeneration.isCurrent(generation) && coverage) this.coverage = coverage;
      } catch (err) {
        if (this.coverageGeneration.isCurrent(generation)) this.errorMessage = sceneErrorText(err);
      }
    },
    async loadRuntime() {
      try {
        this.runtimeStatus = await GetSceneRuntimeStatus();
      } catch (err) {
        this.runtimeStatus = { state: 'incompatible', reason: String(err) };
      }
    },
    async loadIndexStatus() {
      try {
        this.applyIndexStatus(await GetSceneIndexStatus());
      } catch (_err) {
        this.indexStatus = null;
      }
    },
    applyIndexStatus(status) {
      if (!status) return;
      const finished = this.indexStatus?.running && !status.running;
      this.indexStatus = status;
      if (finished) this.loadCoverage();
    },
    async prepareRuntime() {
      try {
        this.runtimeStatus = await PrepareSceneRuntime();
      } catch (err) {
        notifyError(`准备模型失败：${sceneErrorText(err)}`);
      }
    },
    async cancelPrepare() {
      try {
        await CancelSceneRuntimePrepare();
      } catch (err) {
        notifyError(`取消准备失败：${sceneErrorText(err)}`);
      }
      await this.loadRuntime();
    },
    async startIndex() {
      try {
        this.applyIndexStatus(await StartSceneIndex({ video_ids: [], filter: this.scopeFilter, provider: this.imageSearch ? 'apple' : '' }));
      } catch (err) {
        notifyError(`建立画面索引失败：${sceneErrorText(err)}`);
      }
    },
    async cancelIndex() {
      try {
        await CancelSceneIndex();
      } catch (err) {
        notifyError(`取消失败：${sceneErrorText(err)}`);
      }
      await this.loadIndexStatus();
    },
    async clearStale() {
      const confirmed = await confirmAction({
        title: '清理旧索引',
        message: '删除不属于当前画面模型（或当前外部描述模型）的全部画面索引。当前模型的索引保留。确认继续吗？',
        confirmText: '清理',
        danger: true
      });
      if (!confirmed) return;
      try {
        const result = await ClearStaleSceneIndex();
        notifySuccess(`已清理 ${Number(result?.deleted_segments || 0)} 个旧画面段`);
        await this.loadCoverage();
      } catch (err) {
        notifyError(`清理旧索引失败：${sceneErrorText(err)}`);
      }
    }
  }
};
</script>

<style scoped>
.scenes-page { padding: 20px 24px 40px; max-width: 1040px; margin: 0 auto; }
.scenes-heading { margin-bottom: 14px; padding: 12px 16px; border: 1px solid var(--hairline); border-radius: var(--radius-md); background: var(--panel-bg); }
.scenes-heading h2 { margin: 0 0 3px; font-size: 18px; }
.scenes-heading p { margin: 0; color: var(--text-muted); font-size: 12px; }
.scenes-search { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin-bottom: 10px; }
.scenes-search__input { flex: 1 1 320px; min-width: 0; }
.scenes-modes { display: inline-flex; border: 1px solid var(--border-color); border-radius: var(--radius-sm); overflow: hidden; }
.scenes-mode { border: 0; background: var(--control-bg); color: var(--text-secondary); padding: 6px 12px; cursor: pointer; font-size: 13px; }
.scenes-mode.active { background: var(--accent-color); color: var(--accent-on); }
.scenes-scope { display: flex; align-items: center; gap: 8px; color: var(--text-secondary); font-size: 13px; margin-bottom: 10px; }
.scenes-coverage { display: grid; gap: 6px; padding: 10px 12px; border: 1px solid var(--border-color); border-radius: var(--radius); background: var(--control-bg); font-size: 13px; color: var(--text-secondary); }
.scenes-coverage__bar { height: 6px; border-radius: 3px; background: var(--border-color); overflow: hidden; }
.scenes-coverage__bar i { display: block; height: 100%; background: var(--accent-color); transition: width var(--transition); }
.scenes-coverage__provider { font-size: 12px; }
.scenes-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin-top: 10px; }
.scenes-runtime { font-size: 13px; color: var(--text-secondary); margin-right: 4px; }
.scenes-index-status { margin: 8px 0 0; font-size: 13px; color: var(--text-secondary); }
.scenes-error { color: var(--danger-color); margin: 12px 0 0; }
.scenes-notices { margin: 12px 0 0; padding: 10px 12px 10px 28px; border-radius: var(--radius); background: var(--control-bg); color: var(--text-secondary); font-size: 13px; }
.scenes-empty { margin-top: 24px; text-align: center; color: var(--text-secondary); }
.scenes-empty h3 { margin: 0 0 6px; color: var(--text-primary); }
.scenes-empty p { margin: 0; }
.scenes-results { display: grid; gap: 18px; margin-top: 18px; }
.scenes-group__title { margin: 0 0 8px; font-size: 15px; }
.scenes-group__title small { color: var(--text-secondary); font-weight: normal; margin-left: 6px; }
.scenes-page--dropping { outline: 2px dashed var(--accent-color); outline-offset: -6px; }
.scenes-image-query { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; padding: 8px 10px; border: 1px solid var(--accent-color); border-radius: var(--radius); background: var(--accent-soft); font-size: 13px; }
.scenes-image-query__thumb { width: 96px; height: 54px; border-radius: 4px; object-fit: cover; background: var(--control-bg); }
.scenes-image-query__text { flex: 1; min-width: 0; color: var(--text-primary); overflow-wrap: anywhere; }
.scenes-hit-row { display: flex; align-items: flex-start; gap: 8px; margin-bottom: 6px; }
.scenes-hit-row .scenes-hit { margin-bottom: 0; }
.scenes-hit__actions { display: flex; flex: none; flex-direction: column; gap: 6px; margin-top: 8px; }
.scenes-hit { display: flex; gap: 12px; width: 100%; padding: 8px; margin-bottom: 6px; border: 1px solid var(--border-color); border-radius: var(--radius); background: transparent; color: inherit; text-align: left; cursor: pointer; }
.scenes-hit:hover { border-color: var(--accent-color); }
.scenes-hit__frame { width: 160px; height: 90px; flex: 0 0 160px; object-fit: cover; border-radius: 4px; background: var(--control-bg); }
.scenes-hit__body { display: flex; flex-direction: column; gap: 3px; min-width: 0; font-size: 13px; }
.scenes-hit__meta { display: flex; align-items: center; gap: 8px; }
.scenes-hit__source { padding: 1px 6px; border-radius: 3px; font-size: 12px; background: var(--control-bg); }
.scenes-hit__source--dialogue { color: var(--accent-color); }
.scenes-hit__range { color: var(--text-secondary); font-variant-numeric: tabular-nums; }
.scenes-hit__context { color: var(--text-secondary); font-size: 12px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.scenes-hit__text { color: var(--text-primary); }
.btn-compact { height: 28px; padding: 0 10px; font-size: 12px; }
@media (max-width: 640px) {
  .scenes-page { padding: 16px 16px 32px; }
  .scenes-hit__frame { width: 112px; height: 63px; flex-basis: 112px; }
}
</style>
