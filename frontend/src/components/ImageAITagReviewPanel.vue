<template>
  <BaseModal v-if="visible" class="image-ai-tag-review" data-test="image-ai-tag-review" @close="$emit('close')">
    <header class="image-ai-tag-review__header">
      <div>
        <h3>AI 标签审阅</h3>
        <p class="image-ai-tag-review__subtitle">
          AI 只会从标签库里选标签，接受后才会真正挂到图片上。
        </p>
      </div>
      <button type="button" class="btn-secondary btn-compact" data-test="image-ai-tag-review-close" @click="$emit('close')">关闭</button>
    </header>

    <!-- 人物候选与 AI 标签是两批不同的候选，但都是"确认后才写库"的审阅动作，
         放在同一个面板的两个页签下，和视频侧待审工作台同一套形状。 -->
    <nav class="image-ai-tag-review__tabs" aria-label="图片审阅类型">
      <button
        type="button"
        :class="{ active: section === 'tags' }"
        data-test="image-ai-tag-review-tab"
        @click="section = 'tags'"
      >AI 标签待审</button>
      <button
        type="button"
        :class="{ active: section === 'face' }"
        data-test="image-face-cluster-review-tab"
        @click="section = 'face'"
      >人物候选</button>
    </nav>

    <div v-if="section === 'tags'" class="image-ai-tag-review__toolbar">
      <input v-model="keyword" class="image-ai-tag-review__search" data-test="image-ai-tag-search" aria-label="搜索全部图片待审候选" placeholder="搜索文件名、路径、标签或理由" />
      <div class="image-ai-tag-review__filters" role="group" aria-label="置信度筛选">
        <button
          v-for="option in confidenceOptions"
          :key="option.value"
          type="button"
          :class="['btn-secondary', 'btn-compact', { active: confidence === option.value }]"
          :data-test="`image-ai-tag-confidence-${option.value || 'all'}`"
          @click="setConfidence(option.value)"
        >{{ option.label }}</button>
      </div>
      <button
        type="button"
        class="btn-secondary btn-compact"
        data-test="image-ai-tag-review-refresh"
        :disabled="loading || busy"
        @click="load"
      >刷新</button>
      <span class="image-ai-tag-review__count" data-test="image-ai-tag-review-count">
        待审 {{ filteredCandidates.length }} 条 · 涉及 {{ groups.length }} 张图片<template v-if="cursor">（还有更多未加载）</template>
      </span>
    </div>

    <div v-if="section === 'tags'" class="image-ai-tag-review__filters">
      <button type="button" class="btn-primary btn-compact" data-test="image-review-approve-loaded" :disabled="reviewBatchBusy || reviewRefreshCount > 0 || loadedQueryKey !== queryKey || !filteredCandidates.length" @click="$refs.reviewBatch.previewLoaded(filteredCandidates.map(item => item.id))">批准已加载结果…</button>
      <button type="button" class="btn-secondary btn-compact" data-test="image-review-approve-filtered" :disabled="reviewBatchBusy || reviewRefreshCount > 0" @click="$refs.reviewBatch.previewFiltered()">批准全部筛选结果…</button>
    </div>
    <AIReviewBatchControls ref="reviewBatch" kind="image" :visible="visible && section === 'tags'" :query="query" @busy="reviewBatchBusy = $event" @outcomes="applyBatchOutcomes" @settled="batchSettled" />

    <!-- 报错与说明留在滚动区之外：用户可能正停在列表深处，
         放进滚动区顶部等于看不见。 -->
    <p v-if="error" class="image-ai-tag-review__error" role="alert" data-test="image-ai-tag-review-error">{{ error }}<span v-if="candidates.length">；保留上次已加载的结果。</span></p>
    <p v-if="notice" class="image-ai-tag-review__notice" role="status" data-test="image-ai-tag-review-notice">{{ notice }}</p>

    <!-- 一页候选也可能几十条，加上翻页会越滚越长。滚动条必须在这一层、弹窗自身封高，
         否则内容会把 BaseModal 的面板撑出应用窗口，底部的候选点都点不到。 -->
    <div class="image-ai-tag-review__content" data-test="image-ai-tag-review-scroll-area">
      <FaceClusterReviewPanel v-if="section === 'face'" />

      <template v-else>
        <p v-if="loading" class="image-ai-tag-review__status" role="status">正在加载候选…</p>
        <!-- 还有下一页时不能说"没有待审候选"：那只是当页被处理空了，
             自动补页失败时更要留着「加载更多」而不是给一句假的空态。 -->
        <p
          v-else-if="!error && !filteredCandidates.length && !cursor"
          class="image-ai-tag-review__status"
          role="status"
          data-test="image-ai-tag-review-empty"
        >没有待审候选。AI 打标跑完后，候选会出现在这里。</p>

        <ReviewCandidateWindow :groups="groups" kind="image" :query-key="queryKey" :active="visible && section === 'tags'" scroll-owner-selector=".image-ai-tag-review__content">
          <template #header="{ group }">
          <section class="image-ai-tag-review__group glass-surface" data-test="image-ai-tag-group">
          <div class="image-ai-tag-review__group-head">
            <!-- 审阅图片标签只看文件名判断不了对错，缩略图才是证据。
                 走和图片流同一条 /preview/image-thumbnail 路由，失败就地占位。 -->
            <div :class="['image-ai-tag-review__thumb', { 'image-ai-tag-review__thumb--failed': thumbFailed(group.imageID) }]">
              <img
                v-if="!thumbFailed(group.imageID)"
                :src="thumbnailURL(group.imageID)"
                :alt="`${group.name} 缩略图`"
                loading="lazy"
                :data-test="`image-ai-tag-thumb-${group.imageID}`"
                @error="markThumbFailed(group.imageID)"
              />
              <span v-else aria-hidden="true" :data-test="`image-ai-tag-thumb-fallback-${group.imageID}`">🖼</span>
            </div>
            <strong :title="group.name">{{ group.name }}</strong>
            <span v-if="group.deleted" class="image-ai-tag-review__deleted" data-test="image-ai-tag-deleted">图片已删除</span>
            <button
              type="button"
              class="btn-secondary btn-compact"
              :disabled="busy"
              title="拒绝这张图片的全部待审候选，包括当前置信度筛选之外的"
              :data-test="`image-ai-tag-reject-all-${group.imageID}`"
              @click="rejectAll(group)"
            >全部拒绝</button>
          </div>
          </section>
          </template>
          <template #candidate="{ group, candidate }">
            <div class="image-ai-tag-review__item">
              <span class="tag-chip" :style="{ '--tag-color': candidate.matched_tag?.color }">{{ candidate.suggested_name }}</span>
              <span :class="['image-ai-tag-review__confidence', `is-${candidate.confidence}`]">{{ confidenceLabel(candidate.confidence) }}</span>
              <span class="image-ai-tag-review__reason" :title="candidate.reasoning">{{ candidate.reasoning }}</span>
              <span class="image-ai-tag-review__actions">
                <button
                  type="button"
                  class="btn-primary btn-compact"
                  :disabled="busy || group.deleted"
                  :data-test="`image-ai-tag-approve-${candidate.id}`"
                  @click="approve(candidate)"
                >接受</button>
                <button
                  type="button"
                  class="btn-secondary btn-compact"
                  :disabled="busy"
                  :data-test="`image-ai-tag-reject-${candidate.id}`"
                  @click="reject(candidate)"
                >拒绝</button>
              </span>
            </div>
          </template>
        </ReviewCandidateWindow>

        <div v-if="cursor" class="image-ai-tag-review__more">
          <button
            type="button"
            class="btn-secondary btn-compact"
            data-test="image-ai-tag-load-more"
            :disabled="loadingMore || loading"
            @click="loadMore"
          >{{ loadingMore ? '加载中…' : '加载更多候选' }}</button>
          <span>已加载 {{ candidates.length }} 条，还有更多</span>
        </div>
      </template>
    </div>
  </BaseModal>
</template>

<script>
import {
  GetAIReviewCandidates, ApproveImageAITagCandidate, ListImageAITagCandidatePage, SearchImageAITagCandidatePage,
  RejectImageAITagCandidate, RejectImageAITagCandidatesByImage
} from '../../wailsjs/go/main/App';
import BaseModal from './ui/BaseModal.vue';
import AIReviewBatchControls from './AIReviewBatchControls.vue';
import ReviewCandidateWindow from './ReviewCandidateWindow.vue';
import FaceClusterReviewPanel from './FaceClusterReviewPanel.vue';
import { applyReviewCandidateRefresh, reviewOutcomeCandidateIDs, filterCandidatesForReview, filterCandidatesByAttributes, appendCandidates, removeCandidateById, removeCandidatesAfterApproval, removeCandidatesByMedia } from '../utils/aiTagReview.js';

export default {
  name: 'ImageAITagReviewPanel',
  components: { ReviewCandidateWindow, AIReviewBatchControls, BaseModal, FaceClusterReviewPanel },
  props: {
    visible: { type: Boolean, default: false }
  },
  emits: ['close', 'changed'],
  data() {
    return {
      candidates: [],
      // 候选按 id 游标分页：cursor 非 0 表示后端还有下一页。
      cursor: 0,
      // 两次"回到第一页"竞速时，用 token 保证只有最后一次的结果落到界面上
      // （在途的翻页请求由 loadMore 自己按游标 + 筛选判定，见那里的注释）。
      loadToken: 0,
      section: 'tags',
      confidence: '',
      keyword: '',
      searchTimer: null,
      loadedQueryKey: '',
      loadedQuery: { keyword: '', confidence: '' },
      pendingMore: null,
      loading: false,
      loadingMore: false,
      busy: false,
      reviewBatchBusy: false,
      reviewRefreshQueue: Promise.resolve(),
      reviewRefreshCount: 0,
      error: '',
      // notice 与 error 分开：它是操作结果的说明，不是调用失败；而且 load() 会清空 error，
      // 混用会让这条说明在紧随其后的刷新里被冲掉。
      notice: '',
      // 缩略图失败在一次打开内是黏的：文件真的没了的时候，每次审批后的重新加载
      // 都去重试等于白发请求。重新打开面板时清空，给临时失败一次重试机会。
      failedThumbs: {},
      confidenceOptions: [
        { value: '', label: '全部' },
        { value: 'high', label: '高' },
        { value: 'medium', label: '中' }
      ]
    };
  },
  computed: {
    query() {
      return { media_id: 0, keyword: String(this.keyword || '').trim(), confidence: this.confidence, tag_id: 0, status: 'pending' };
    },
    queryKey() { return JSON.stringify({ ...this.query, keyword: this.query.keyword.toLowerCase() }); },
    filteredCandidates() {
      return filterCandidatesForReview(filterCandidatesByAttributes(this.candidates, { confidence: this.loadedQuery.confidence }), this.loadedQuery.keyword, 'image');
    },
    // 按图片分组：审阅时用户是在看"这张图该打哪些标签"，逐条平铺会让同一张图的候选散开。
    groups() {
      const byImage = new Map();
      for (const candidate of this.filteredCandidates) {
        const imageID = candidate.image_id;
        if (!byImage.has(imageID)) {
          byImage.set(imageID, {
            imageID,
            name: candidate.image?.name || `图片 #${imageID}`,
            deleted: Boolean(candidate.image_deleted),
            items: []
          });
        }
        byImage.get(imageID).items.push(candidate);
      }
      return Array.from(byImage.values());
    }
  },
  watch: {
    keyword() {
      clearTimeout(this.searchTimer);
      ++this.loadToken;
      this.pendingMore = null;
      this.loadingMore = false;
      if (this.visible) this.searchTimer = setTimeout(() => this.load(), 200);
    },
    visible(value) {
      if (value) {
        this.section = 'tags';
        this.failedThumbs = {};
        this.load();
      } else {
        this.stopRequests();
      }
    }
  },
  mounted() {
    if (this.visible) this.load();
  },
  beforeUnmount() { this.stopRequests(); },
  methods: {
    applyBatchOutcomes(outcomes) {
      const token = this.loadToken;
      this.reviewRefreshCount++;
      this.reviewRefreshQueue = this.reviewRefreshQueue.then(async () => {
        if (token !== this.loadToken) return;
        await this.refreshBatchOutcomes(outcomes);
      }).finally(() => { this.reviewRefreshCount--; });
      return this.reviewRefreshQueue;
    },
    async refreshBatchOutcomes(outcomes) {
      const ids = reviewOutcomeCandidateIDs(this.candidates, outcomes, 'image_id');
      const token = this.loadToken;
      const key = this.loadedQueryKey;
      for (let offset = 0; offset < ids.length; offset += 200) {
        const pageIDs = ids.slice(offset, offset + 200);
        const selected = new Set(pageIDs);
        const snapshot = this.candidates.filter(item => selected.has(Number(item.id)));
        try {
          const result = await GetAIReviewCandidates('image', pageIDs);
          if (token !== this.loadToken || key !== this.loadedQueryKey) return;
          if (!Array.isArray(result?.image_items)) throw new Error('候选核对结果无效');
          this.candidates = applyReviewCandidateRefresh(this.candidates, snapshot, result.image_items);
        } catch (err) { if (token === this.loadToken && key === this.loadedQueryKey) this.error = '核对候选当前状态失败，请刷新：' + err; return; }
      }
      await this.fillEmptyPage();
    },
    async batchSettled() { await this.reviewRefreshQueue; this.$emit('changed'); await this.fillEmptyPage(); },
    stopRequests() {
      clearTimeout(this.searchTimer);
      ++this.loadToken;
      this.pendingMore = null;
      this.loadingMore = false;
    },
    pageRequest(query, cursor = 0) {
      return query.keyword
        ? SearchImageAITagCandidatePage({ ...query, cursor_id: cursor, limit: 0 })
        : ListImageAITagCandidatePage(0, query.confidence, '', cursor, 0);
    },
    confidenceLabel(value) {
      return { high: '高', medium: '中', low: '低' }[value] || value;
    },
    thumbnailURL(imageID) {
      return `/preview/image-thumbnail/${imageID}`;
    },
    thumbFailed(imageID) {
      return !!this.failedThumbs[imageID];
    },
    markThumbFailed(imageID) {
      this.failedThumbs = { ...this.failedThumbs, [imageID]: true };
    },
    setConfidence(value) {
      if (this.confidence === value) return;
      this.confidence = value;
      this.load();
    },
    // load() 是"回到第一页"：打开面板与切换置信度筛选走它，已翻出来的后续页会被丢掉。
    async load() {
      clearTimeout(this.searchTimer);
      const token = ++this.loadToken;
      const query = { ...this.query };
      const key = this.queryKey;
      this.loading = true;
      this.error = '';
      try {
        const page = await this.pageRequest(query);
        if (token !== this.loadToken || key !== this.queryKey) return;
        this.candidates = Array.isArray(page?.items) ? page.items : [];
        this.cursor = Number(page?.next_id || 0);
        this.loadedQueryKey = key;
        this.loadedQuery = query;
      } catch (err) {
        if (token !== this.loadToken) return;
        this.error = `加载候选失败: ${err}`;
      } finally {
        if (token === this.loadToken) this.loading = false;
      }
    },
    // 用请求自身的身份（发出时的游标 + 筛选）判断结果还能不能用，而不是 load() 里那个
    // 自增计数器：在 load() 在途时点"加载更多"，计数器已经是新值，旧游标的结果会被当成
    // 当前结果追加进去——列表会跳过中间一整段候选，游标还越过了它们，那段再也翻不到。
    // 反过来，如果重新加载后列表末尾仍停在同一个游标、筛选也没变，这一页正好接得上，照常追加。
    async loadMore() {
      if (!this.cursor || this.loadingMore || this.loading || this.loadedQueryKey !== this.queryKey) return;
      const cursor = this.cursor;
      const key = this.queryKey;
      this.loadingMore = true;
      this.error = '';
      const request = this.pageRequest({ ...this.query }, cursor);
      this.pendingMore = request;
      try {
        const page = await request;
        if (this.cursor !== cursor || this.queryKey !== key || this.pendingMore !== request) return;
        this.candidates = appendCandidates(this.candidates, page?.items);
        this.cursor = Number(page?.next_id || 0);
      } catch (err) {
        if (this.cursor !== cursor || this.queryKey !== key || this.pendingMore !== request) return;
        this.error = `加载更多候选失败: ${err}`;
      } finally {
        if (this.pendingMore === request) { this.pendingMore = null; this.loadingMore = false; }
      }
    },
    // 局部移除后当页可能空了，但后端还有下一页：直接补一页，
    // 否则用户会看到"没有待审候选"这个假空态。
    async fillEmptyPage() {
      if (this.filteredCandidates.length || !this.cursor) return;
      await this.loadMore();
    },
    async approve(candidate) {
      if (this.busy) return;
      this.busy = true;
      this.error = '';
      this.notice = '';
      try {
        const item = await ApproveImageAITagCandidate(candidate.id);
        // 后端同时作废同图同标签的其他候选：按同一规则局部移除，不整表重拉——分页之后
        // 重拉会把已加载的页丢掉，还会把用户拉回列表顶部。D-PC28 规则 2 之后图片上已有
        // 手工标签不再让整图候选作废，这张图其他标签的候选照常留在列表里（META-06）。
        this.candidates = removeCandidatesAfterApproval(this.candidates, candidate, item, 'image_id');
        this.$emit('changed');
        await this.fillEmptyPage();
      } catch (err) {
        this.error = `接受候选失败: ${err}`;
      } finally {
        this.busy = false;
      }
    },
    async reject(candidate) {
      if (this.busy) return;
      this.busy = true;
      this.error = '';
      this.notice = '';
      try {
        await RejectImageAITagCandidate(candidate.id);
        this.candidates = removeCandidateById(this.candidates, candidate.id);
        await this.fillEmptyPage();
      } catch (err) {
        this.error = `拒绝候选失败: ${err}`;
      } finally {
        this.busy = false;
      }
    },
    async rejectAll(group) {
      if (this.busy) return;
      this.busy = true;
      this.error = '';
      this.notice = '';
      try {
        const rejected = await RejectImageAITagCandidatesByImage(group.imageID);
        const loaded = group.items.length;
        this.candidates = removeCandidatesByMedia(this.candidates, 'image_id', group.imageID);
        // 「全部拒绝」按图片拒绝全部待审候选，包括当前置信度筛选之外、尚未加载的：
        // 条数不一样时说清楚，免得用户以为只拒了眼前这几条。
        if (Number(rejected) > loaded) this.notice = `已拒绝「${group.name}」的全部 ${Number(rejected)} 条待审候选（含当前筛选之外的）。`;
        await this.fillEmptyPage();
      } catch (err) {
        this.error = `批量拒绝失败: ${err}`;
      } finally {
        this.busy = false;
      }
    }
  }
};
</script>

<style scoped>
/* 这条规则必须走 :deep：scoped 的 data-v 只挂在 BaseModal 的根节点（遮罩层），
   class 却落在内层 .modal 面板上。之前写成普通 scoped 选择器等于整条没生效——
   宽度停在全局 .modal 的 500px，而且完全不封高，候选一多就把弹窗撑出应用窗口。 */
:deep(.image-ai-tag-review) {
  display: flex;
  flex-direction: column;
  width: min(920px, calc(100vw - 40px));
  max-width: min(920px, calc(100vw - 40px));
  max-height: min(720px, calc(100vh - 48px));
  overflow: hidden;
}

.image-ai-tag-review__header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 12px;
}

.image-ai-tag-review__header h3 {
  margin: 0 0 4px;
}

.image-ai-tag-review__subtitle {
  margin: 0;
  font-size: 12px;
  color: var(--text-secondary);
}

.image-ai-tag-review__tabs {
  display: flex;
  gap: 6px;
  margin-bottom: 12px;
  border-bottom: 1px solid var(--hairline-faint);
}

.image-ai-tag-review__tabs button {
  padding: 6px 10px;
  border: 0;
  border-bottom: 2px solid transparent;
  background: transparent;
  color: var(--text-secondary);
  font-size: 13px;
  cursor: pointer;
}

.image-ai-tag-review__tabs button.active {
  border-bottom-color: var(--accent-color);
  color: var(--text-primary);
}

.image-ai-tag-review__toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
}

.image-ai-tag-review__search { flex: 1 1 240px; min-width: 160px; }

.image-ai-tag-review__filters {
  display: flex;
  gap: 6px;
}

.image-ai-tag-review__count {
  font-size: 12px;
  color: var(--text-secondary);
}

.image-ai-tag-review__content {
  flex: 1 1 auto;
  min-height: 0;
  overflow-x: hidden;
  overflow-y: auto;
  overscroll-behavior: contain;
  padding-right: 4px;
}

.image-ai-tag-review__error {
  margin: 0 0 12px;
  color: var(--danger-color);
  font-size: 13px;
}

.image-ai-tag-review__notice {
  margin: 0 0 12px;
  color: var(--text-secondary);
  font-size: 13px;
}

.image-ai-tag-review__status {
  margin: 24px 0;
  text-align: center;
  color: var(--text-secondary);
  font-size: 13px;
}

.image-ai-tag-review__more {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 10px;
  padding: 4px 0 8px;
  color: var(--text-secondary);
  font-size: 12px;
}

.image-ai-tag-review__group {
  padding: 12px;
  border-radius: 10px;
  display: flow-root;
}

.image-ai-tag-review__group-head {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 8px;
}

.image-ai-tag-review__group-head strong {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.image-ai-tag-review__thumb {
  display: grid;
  flex: 0 0 auto;
  width: 64px;
  aspect-ratio: 1;
  place-items: center;
  overflow: hidden;
  border-radius: 8px;
  background: var(--thumb-bg);
  color: var(--thumb-fg);
  font-size: 20px;
}

.image-ai-tag-review__thumb img {
  display: block;
  width: 100%;
  height: 100%;
  object-fit: cover;
}

.image-ai-tag-review__thumb--failed {
  background: var(--thumb-fallback-bg);
}

.image-ai-tag-review__deleted {
  font-size: 12px;
  color: var(--danger-color);
}

.image-ai-tag-review__item {
  display: flex;
  align-items: center;
  gap: 10px;
}

.image-ai-tag-review__confidence {
  font-size: 12px;
  color: var(--text-secondary);
}

.image-ai-tag-review__confidence.is-high {
  color: var(--success-color);
}

.image-ai-tag-review__reason {
  flex: 1;
  min-width: 0;
  font-size: 12px;
  color: var(--text-secondary);
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}

.image-ai-tag-review__actions {
  display: flex;
  gap: 6px;
}
</style>
