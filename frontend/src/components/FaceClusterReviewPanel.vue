<template>
  <section class="face-review" data-test="face-cluster-review">
    <div class="face-review__toolbar">
      <p class="help-text">
        人脸只产出候选：只有你在这里命名、关联或确认追加，才会真正建立人物关系。
      </p>
      <button
        type="button"
        class="btn-secondary btn-compact"
        data-test="face-cluster-refresh"
        :disabled="!!busy"
        @click="refresh()"
      >刷新</button>
    </div>

    <!-- 三个视图（D-PC30）：待处理 = 追加候选 + 未命名；已命名可解除关联、改派；
         已忽略可恢复。未命名与已忽略按服务端游标分页（D-PC29），不再一次拉全部。 -->
    <nav class="face-review__views" aria-label="人脸组视图">
      <button type="button" :class="{ active: view === 'pending' }" data-test="face-cluster-view-pending" @click="switchView('pending')">待处理 <span>{{ pendingCountText }}</span></button>
      <button type="button" :class="{ active: view === 'named' }" data-test="face-cluster-view-named" @click="switchView('named')">已命名 <span>{{ namedClusters.length }}</span></button>
      <button type="button" :class="{ active: view === 'ignored' }" data-test="face-cluster-view-ignored" @click="switchView('ignored')">已忽略<span v-if="ignored.loaded">{{ ignored.items.length }}{{ ignored.cursor ? '+' : '' }}</span></button>
    </nav>

    <p v-if="error" class="face-review__error" role="alert" data-test="face-cluster-review-error">{{ error }}</p>
    <p v-if="notice" class="face-review__notice" role="status" data-test="face-cluster-review-notice">{{ notice }}</p>

    <template v-if="view !== 'ignored'">
      <p v-if="initialLoading" class="face-review__status" role="status">正在加载人物候选…</p>
      <p
        v-else-if="!visibleCards.length && view === 'pending'"
        class="face-review__status"
        role="status"
        data-test="face-cluster-review-empty"
      >暂无人物候选。人脸分析跑完后，认得出的面孔会在这里等你命名。</p>
      <p
        v-else-if="!visibleCards.length"
        class="face-review__status"
        role="status"
        data-test="face-cluster-named-empty"
      >还没有已命名的人脸组。</p>

      <article
        v-for="card in visibleCards"
        :key="card.id"
        class="face-review__card glass-surface"
        :data-test="`face-cluster-card-${card.id}`"
      >
        <div class="face-review__card-main">
          <div class="face-review__crop">
            <img
              v-if="card.representative_observation_id && !cropFailed[card.id]"
              :src="cropURL(card)"
              :alt="`${cardTitle(card)} 的人脸截图`"
              loading="lazy"
              @error="markCropFailed(card.id)"
            />
            <span v-else aria-hidden="true">?</span>
          </div>
          <div class="face-review__summary">
            <strong>{{ cardTitle(card) }}</strong>
            <p class="face-review__meta">{{ mediaSummary(card) }}</p>
            <p v-if="card.candidates.length" class="face-review__candidates">
              <span
                v-for="candidate in card.candidates"
                :key="candidate.person_id"
                class="face-review__candidate"
                :data-test="`face-cluster-candidate-${card.id}-${candidate.person_id}`"
              >可能是 {{ candidate.display_name }}（相似度 {{ similarityText(candidate.similarity) }}）</span>
            </p>
            <p
              v-if="card.status === 'named' && card.append_pending_count"
              class="face-review__meta"
              :data-test="`face-cluster-append-summary-${card.id}`"
            >
              新出现 {{ card.append_pending_count }} 处：{{ appendMediaText(card) }}
            </p>
          </div>
        </div>

        <button type="button" class="btn-secondary btn-compact" :data-test="`face-cluster-detail-${card.id}`" @click="detailCluster = card">查看来源详情</button>
        <div v-if="card.status === 'unnamed'" class="face-review__actions">
          <button
            type="button"
            class="btn-primary btn-compact"
            :disabled="!!busy"
            :data-test="`face-cluster-name-open-${card.id}`"
            @click="openNameForm(card)"
          >命名为新人物</button>
          <button
            type="button"
            class="btn-secondary btn-compact"
            :disabled="!!busy"
            :data-test="`face-cluster-link-open-${card.id}`"
            @click="openLinkForm(card)"
          >关联到现有人物</button>
          <button
            type="button"
            class="btn-secondary btn-compact"
            :disabled="!!busy"
            :data-test="`face-cluster-ignore-${card.id}`"
            @click="ignore(card)"
          >忽略</button>
        </div>
        <template v-else>
          <!-- 追加可逐条勾选（D-PC30）：按媒体勾，确认时只把所勾媒体上的待确认人脸交给后端。 -->
          <div v-if="card.append_pending_count" class="face-review__append" :data-test="`face-cluster-append-list-${card.id}`">
            <label
              v-for="media in card.append_pending_media"
              :key="mediaKey(media)"
              class="checkbox-label face-review__append-item"
            >
              <input
                type="checkbox"
                :checked="isAppendSelected(card, media)"
                :data-test="`face-cluster-append-item-${card.id}-${media.media_kind}-${media.media_id}`"
                @change="toggleAppendMedia(card, media, $event.target.checked)"
              />
              <span>{{ mediaLabel(media) }}</span>
            </label>
          </div>
          <div class="face-review__actions">
            <template v-if="card.append_pending_count">
              <button
                type="button"
                class="btn-primary btn-compact"
                :disabled="!!busy || !selectedAppendMedia(card).length"
                :data-test="`face-cluster-append-confirm-${card.id}`"
                @click="confirmAppend(card)"
              >{{ selectedAppendMedia(card).length === card.append_pending_media.length ? '确认追加' : `确认所选（${selectedAppendMedia(card).length}）` }}</button>
              <button
                type="button"
                class="btn-secondary btn-compact"
                :disabled="!!busy"
                :data-test="`face-cluster-append-dismiss-${card.id}`"
                @click="dismissAppend(card)"
              >忽略追加</button>
            </template>
            <button
              type="button"
              class="btn-secondary btn-compact"
              :disabled="!!busy"
              :data-test="`face-cluster-unlink-open-${card.id}`"
              @click="openRelink(card, 'unlink')"
            >解除关联</button>
            <button
              type="button"
              class="btn-secondary btn-compact"
              :disabled="!!busy"
              :data-test="`face-cluster-reassign-open-${card.id}`"
              @click="openRelink(card, 'reassign')"
            >改派给其他人物</button>
          </div>
        </template>

        <div v-if="nameForm.clusterID === card.id" class="face-review__form" data-test="face-cluster-name-form">
          <input
            v-model="nameForm.displayName"
            type="text"
            class="text-input"
            placeholder="显示名，例如 周迅"
            data-test="face-cluster-name-display"
            @keyup.enter="submitName(card)"
          />
          <input
            v-model="nameForm.originalName"
            type="text"
            class="text-input"
            placeholder="原始名（可留空），例如 Zhou Xun"
            data-test="face-cluster-name-original"
            @keyup.enter="submitName(card)"
          />
          <button
            type="button"
            class="btn-primary btn-compact"
            :disabled="!!busy"
            data-test="face-cluster-name-submit"
            @click="submitName(card)"
          >确认命名</button>
          <button
            type="button"
            class="btn-secondary btn-compact"
            data-test="face-cluster-name-cancel"
            @click="closeForms"
          >取消</button>
        </div>

        <div v-if="linkForm.clusterID === card.id" class="face-review__form" data-test="face-cluster-link-form">
          <div class="face-review__combobox">
            <input
              v-model="linkForm.keyword"
              type="search"
              class="search-input"
              placeholder="搜索人物，回车关联"
              role="combobox"
              autocomplete="off"
              aria-controls="face-cluster-link-options"
              :aria-expanded="linkForm.menuOpen"
              data-test="face-cluster-link-search"
              @focus="linkForm.menuOpen = true"
              @input="handleLinkInput"
              @keydown="handleLinkKeydown"
            />
            <div
              v-if="linkForm.menuOpen"
              id="face-cluster-link-options"
              class="face-review__options"
              role="listbox"
              data-test="face-cluster-link-options"
            >
              <button
                v-for="(option, index) in linkOptions"
                :key="option.personID"
                type="button"
                role="option"
                :aria-selected="index === linkForm.activeIndex"
                :class="['face-review__option', { active: index === linkForm.activeIndex }]"
                :data-test="`face-cluster-link-option-${option.personID}`"
                @mouseenter="linkForm.activeIndex = index"
                @mousedown.prevent="submitLink(card, option)"
              >
                {{ option.name }}
                <span v-if="option.similarity" class="face-review__option-hint">候选 · {{ similarityText(option.similarity) }}</span>
              </button>
              <span v-if="!linkOptions.length" class="face-review__options-empty">没有匹配人物</span>
            </div>
          </div>
          <button
            type="button"
            class="btn-secondary btn-compact"
            data-test="face-cluster-link-cancel"
            @click="closeForms"
          >取消</button>
        </div>

        <!-- 解除关联 / 改派的预览确认（META-04）：先列出这组人脸涉及的媒体，逐条说明关系的来源
             （这组人脸写入 / 来源不明）与去留，用户看清之后再点确认。来源不明的关系一律保留。 -->
        <div v-if="relinkForm.clusterID === card.id" class="face-review__form face-review__relink" data-test="face-cluster-relink-form">
          <p class="face-review__relink-title">
            <strong>{{ relinkForm.mode === 'unlink' ? '解除关联' : '改派给其他人物' }}</strong>
            <span>{{ relinkForm.mode === 'unlink' ? `这组人脸会回到待命名，不再算作「${cardTitle(card)}」。` : `这组人脸会改为指向所选人物，不再算作「${cardTitle(card)}」。` }}</span>
          </p>
          <div v-if="relinkForm.mode === 'reassign'" class="face-review__combobox">
            <input
              v-model="relinkForm.keyword"
              type="search"
              class="search-input"
              placeholder="搜索要改派到的人物"
              autocomplete="off"
              data-test="face-cluster-reassign-search"
              @input="searchPeople(relinkForm.keyword)"
            />
            <div class="face-review__reassign-options" role="listbox">
              <button
                v-for="option in reassignOptions"
                :key="option.personID"
                type="button"
                role="option"
                :aria-selected="relinkForm.target?.personID === option.personID"
                :class="['face-review__option', { active: relinkForm.target?.personID === option.personID }]"
                :data-test="`face-cluster-reassign-option-${option.personID}`"
                @click="relinkForm.target = option"
              >{{ option.name }}</button>
              <span v-if="!reassignOptions.length" class="face-review__options-empty">没有匹配人物</span>
            </div>
          </div>
          <p v-if="relinkForm.loading" class="face-review__meta" role="status">正在读取这组人脸涉及的媒体…</p>
          <p v-else-if="relinkForm.error" class="face-review__error" role="alert">{{ relinkForm.error }}</p>
          <template v-else>
            <ul v-if="relinkForm.items.length" class="face-review__relink-list" data-test="face-cluster-relink-items">
              <li
                v-for="item in relinkForm.items"
                :key="mediaKey(item)"
                :data-test="`face-cluster-relink-item-${item.media_kind}-${item.media_id}`"
              >
                <span class="face-review__relink-name">{{ mediaLabel(item) }}</span>
                <span :class="['face-review__relink-source', { 'is-face': item.source === 'face' && item.has_relation }]">{{ relinkSourceText(item) }}</span>
                <span class="face-review__relink-outcome">{{ relinkOutcomeText(item) }}</span>
              </li>
            </ul>
            <p v-else class="face-review__meta">这组人脸没有已确认的媒体，不涉及任何人物关系。</p>
            <label class="checkbox-label face-review__relink-apply">
              <input
                v-model="relinkForm.applyRelations"
                type="checkbox"
                data-test="face-cluster-relink-apply"
              />
              {{ relinkApplyLabel }}
            </label>
          </template>
          <div class="face-review__actions">
            <button
              type="button"
              class="btn-danger btn-compact"
              :disabled="!!busy || relinkForm.loading || !!relinkForm.error || (relinkForm.mode === 'reassign' && !relinkForm.target)"
              data-test="face-cluster-relink-submit"
              @click="submitRelink(card)"
            >{{ relinkForm.mode === 'unlink' ? '确认解除关联' : (relinkForm.target ? `确认改派给「${relinkForm.target.name}」` : '先选择人物') }}</button>
            <button type="button" class="btn-secondary btn-compact" data-test="face-cluster-relink-cancel" @click="closeForms">取消</button>
          </div>
        </div>
      </article>

      <button
        v-if="view === 'pending' && unnamed.hasMore"
        type="button"
        class="btn-secondary face-review__more"
        data-test="face-cluster-load-more"
        :disabled="unnamed.loadingMore || !!busy"
        @click="loadMoreUnnamed"
      >{{ unnamed.loadingMore ? '加载中…' : '加载更多未命名的面孔' }}</button>
      <!-- 已命名簇一次取全（它们是用户逐个命名出来的，数量有限），但渲染仍分批放出。 -->
      <button
        v-if="view === 'named' && hiddenNamedCount > 0"
        type="button"
        class="btn-secondary face-review__more"
        data-test="face-cluster-show-more"
        @click="namedLimit += cardPageSize"
      >显示更多（还有 {{ hiddenNamedCount }} 组）</button>
    </template>

    <template v-else>
      <p class="help-text">忽略过的人脸组仍会吸收同一个人的新人脸，只是不再提示。恢复后回到「待处理」等你命名。</p>
      <p v-if="ignored.loading && !ignored.items.length" class="face-review__status" role="status">正在加载已忽略的人脸组…</p>
      <p v-else-if="!ignored.items.length" class="face-review__status" role="status" data-test="face-cluster-ignored-empty">没有已忽略的人脸组。</p>
      <article
        v-for="row in ignored.items"
        :key="row.cluster.id"
        class="face-review__card glass-surface"
        :data-test="`face-cluster-ignored-${row.cluster.id}`"
      >
        <div class="face-review__card-main">
          <div class="face-review__crop">
            <img
              v-if="row.cluster.representative_observation_id && !cropFailed[row.cluster.id]"
              :src="cropURL(row.cluster)"
              alt="已忽略的人脸截图"
              loading="lazy"
              @error="markCropFailed(row.cluster.id)"
            />
            <span v-else aria-hidden="true">?</span>
          </div>
          <div class="face-review__summary">
            <strong>已忽略的面孔</strong>
            <p class="face-review__meta">{{ mediaSummary(row.cluster) }}</p>
            <p class="face-review__meta" :data-test="`face-cluster-absorbed-${row.cluster.id}`">忽略后又并入 {{ Number(row.absorbed_since_ignored || 0) }} 张新人脸</p>
          </div>
        </div>
        <div class="face-review__actions">
          <button
            type="button"
            class="btn-primary btn-compact"
            :disabled="!!busy"
            :data-test="`face-cluster-restore-${row.cluster.id}`"
            @click="restore(row)"
          >恢复</button>
        </div>
      </article>
      <button
        v-if="ignored.cursor"
        type="button"
        class="btn-secondary face-review__more"
        data-test="face-cluster-ignored-more"
        :disabled="ignored.loading"
        @click="loadIgnored({ reset: false })"
      >{{ ignored.loading ? '加载中…' : '加载更多' }}</button>
    </template>

    <FaceClusterDetailDialog v-if="detailCluster" :cluster="detailCluster" @close="detailCluster = null" @removed="refresh()" @name="card => { detailCluster = null; openNameForm(card); }" @link="card => { detailCluster = null; openLinkForm(card); }" />
  </section>
</template>

<script>
import FaceClusterDetailDialog from './FaceClusterDetailDialog.vue';
import {
  ConfirmFaceClusterAppend, ConfirmFaceClusterAppendObservations, DismissFaceClusterAppend, GetFaceClusterObservations,
  IgnoreFaceCluster, LinkFaceCluster, ListFaceClusterPage, ListIgnoredFaceClusters, ListPeople, NameFaceCluster,
  PreviewFaceClusterUnlink, ReassignFaceCluster, RestoreFaceCluster, UnlinkFaceCluster
} from '../../wailsjs/go/main/App';
import { confirmAction } from '../utils/feedback.js';

// 后端错误码到人话的映射。STALE_CODES 说明这一簇已经被别处处理过了，除了报错还要顺手
// 刷新，否则用户对着过期的卡片再点一次还是同一个错。按长度倒序匹配，避免一个码是另一个码的子串。
const ERROR_TEXT = {
  cluster_not_unnamed: '这一簇刚刚已经被命名或忽略过了，列表已刷新。',
  cluster_not_ignored: '这一簇已经不在「已忽略」里了，列表已刷新。',
  cluster_not_found: '这一簇已经不在了，列表已刷新。',
  cluster_not_named: '这一簇当前不是已命名状态（可能刚被解除关联），列表已刷新。',
  cluster_conflict: '这一簇刚刚被改派给了其他人物，列表已刷新。',
  cluster_ignored: '这一簇已被忽略，请先在「已忽略」中恢复。',
  person_name_invalid: '显示名不能为空，且不超过 200 个字。',
  person_not_found: '选中的人物已经不存在了。'
};
const ERROR_CODES = Object.keys(ERROR_TEXT).sort((a, b) => b.length - a.length);
const STALE_CODES = ['cluster_not_unnamed', 'cluster_not_ignored', 'cluster_not_found', 'cluster_not_named', 'cluster_conflict'];

// 一页卡片数：未命名按这个大小向服务端翻页；已命名一次取全后按这个大小分批渲染。
const CARD_PAGE_SIZE = 20;
// 服务端单页上限（normalizeEntityPageLimit）：刷新时按已加载的范围重取，但不超过它。
const MAX_PAGE_LIMIT = 200;
// 取全已命名簇、逐条确认追加时翻页的上限，防止异常数据让循环停不下来。
const MAX_PAGES = 50;

const FIRST_CURSOR = () => ({ count: 0, id: 0 });
const EMPTY_RELINK_FORM = () => ({ clusterID: 0, mode: 'unlink', loading: false, error: '', items: [], applyRelations: true, target: null, keyword: '', personID: 0 });

// 人物候选（D-019 / D-PC29 / D-PC30）：视频侧待审工作台、图片侧审阅面板与人物页共用这一个组件——
// 三处看到的是同一批簇，一个簇可能同时含视频与图片观测，因此这里不按媒体类型筛。
export default {
  name: 'FaceClusterReviewPanel',
  components: { FaceClusterDetailDialog },
  // loaded 载荷 { unnamed, appendPending, unnamedHasMore }：unnamed 是已加载的未命名组数，
  // 还有下一页时 unnamedHasMore 为真（宿主据此写「N+ 组」）。
  emits: ['changed', 'loaded'],
  data() {
    return {
      view: 'pending',
      detailCluster: null,
      unnamed: { items: [], next: FIRST_CURSOR(), hasMore: false, loadingMore: false },
      namedClusters: [],
      namedLimit: CARD_PAGE_SIZE,
      cardPageSize: CARD_PAGE_SIZE,
      ignored: { items: [], cursor: 0, loaded: false, loading: false },
      // 逐条确认追加：按簇记下被取消勾选的媒体（默认全选）。
      appendDeselected: {},
      people: [],
      cropFailed: {},
      // initialLoading 只在第一次加载时为真：事件驱动的刷新不该把用户正在看的卡片换成"加载中"。
      initialLoading: true,
      busy: '',
      error: '',
      notice: '',
      analysisRunning: false,
      nameForm: { clusterID: 0, displayName: '', originalName: '' },
      linkForm: { clusterID: 0, keyword: '', menuOpen: false, activeIndex: 0 },
      relinkForm: EMPTY_RELINK_FORM(),
      reviewOff: null,
      analysisOff: null
    };
  },
  computed: {
    // 追加候选在前：它们是已命名人物的少量待确认项；未命名的面孔按页往下翻，放在前面会把追加项埋没。
    appendCards() {
      return this.namedClusters.filter(card => card.append_pending_count > 0);
    },
    allCards() {
      return [...this.unnamed.items, ...this.namedClusters];
    },
    visibleCards() {
      if (this.view === 'named') return this.namedClusters.slice(0, this.namedLimit);
      return [...this.appendCards, ...this.unnamed.items];
    },
    hiddenNamedCount() {
      return Math.max(0, this.namedClusters.length - this.namedLimit);
    },
    pendingCountText() {
      return `${this.appendCards.length + this.unnamed.items.length}${this.unnamed.hasMore ? '+' : ''}`;
    },
    // 候选人物置顶：算出来的"可能是谁"排在搜索结果之前，用户不用自己打字。
    linkOptions() {
      const card = this.allCards.find(item => item.id === this.linkForm.clusterID);
      if (!card) return [];
      const options = [];
      const seen = new Set();
      for (const candidate of card.candidates || []) {
        options.push({ personID: candidate.person_id, name: candidate.display_name || `人物 #${candidate.person_id}`, similarity: candidate.similarity });
        seen.add(candidate.person_id);
      }
      for (const item of this.people) {
        const id = Number(item?.person?.id);
        if (!id || seen.has(id)) continue;
        options.push({ personID: id, name: item.person.display_name || `人物 #${id}`, similarity: 0 });
      }
      const keyword = this.linkForm.keyword.trim().toLowerCase();
      if (!keyword) return options;
      return options.filter(option => option.name.toLowerCase().includes(keyword));
    },
    // 改派目标：排除簇当前指向的人物本身。
    reassignOptions() {
      return this.people
        .map(item => ({ personID: Number(item?.person?.id), name: item?.person?.display_name || `人物 #${item?.person?.id}` }))
        .filter(option => option.personID && option.personID !== Number(this.relinkForm.personID));
    },
    relinkRemovableCount() {
      return this.relinkForm.items.filter(item => this.isRemovableRelation(item)).length;
    },
    relinkApplyLabel() {
      if (this.relinkForm.mode === 'unlink') {
        return `同时删除由这组人脸写入的 ${this.relinkRemovableCount} 条人物关系（来源不明的关系保留）`;
      }
      return `把这些媒体一并关联到新人物，并移除其中 ${this.relinkRemovableCount} 条由这组人脸写入的原关系`;
    }
  },
  mounted() {
    this.refresh({ initial: true });
    if (window.runtime?.EventsOn) {
      const reviewOff = window.runtime.EventsOn('face-review-changed', () => this.refresh());
      if (typeof reviewOff === 'function') this.reviewOff = reviewOff;
      const analysisOff = window.runtime.EventsOn('face-analysis-state', (status) => {
        if (!status) return;
        if (status.running) {
          this.analysisRunning = true;
          return;
        }
        // 进度事件每件媒体来一次，只有"跑完"才值得重新拉簇。
        const finished = this.analysisRunning || status.completed;
        this.analysisRunning = false;
        if (finished) this.refresh();
      });
      if (typeof analysisOff === 'function') this.analysisOff = analysisOff;
    }
  },
  beforeUnmount() {
    this.reviewOff?.();
    this.analysisOff?.();
  },
  methods: {
    cropURL(card) {
      return `/preview/face-crop/${card.representative_observation_id}`;
    },
    markCropFailed(clusterID) {
      this.cropFailed = { ...this.cropFailed, [clusterID]: true };
    },
    cardTitle(card) {
      if (card.status === 'named') return card.person_name || `人物 #${card.person_id}`;
      return '未命名的面孔';
    },
    similarityText(similarity) {
      return `${Math.round(Number(similarity || 0) * 100)}%`;
    },
    mediaSummary(card) {
      const parts = [`${card.observation_count} 次出现`];
      if (card.video_count) parts.push(`${card.video_count} 个视频`);
      if (card.image_count) parts.push(`${card.image_count} 张图片`);
      return parts.join(' · ');
    },
    mediaKey(media) {
      return `${media.media_kind}:${media.media_id}`;
    },
    mediaLabel(media) {
      return media.name || `${media.media_kind === 'video' ? '视频' : '图片'} #${media.media_id}`;
    },
    appendMediaText(card) {
      return (card.append_pending_media || []).map(item => this.mediaLabel(item)).join('、');
    },
    normalize(list) {
      return (list || []).map(card => ({
        ...card,
        candidates: card.candidates || [],
        append_pending_media: card.append_pending_media || [],
        append_pending_count: Number(card.append_pending_count || 0)
      }));
    },
    emitLoaded() {
      // 宿主页（人物页）据此写摘要、决定默认收起还是展开。
      this.$emit('loaded', { unnamed: this.unnamed.items.length, appendPending: this.appendCards.length, unnamedHasMore: this.unnamed.hasMore });
    },
    // 已命名簇一次取全：它们是用户一个个命名出来的，数量有限；待确认追加也要在全集里挑。
    async fetchAllNamed() {
      const clusters = [];
      let cursor = FIRST_CURSOR();
      for (let page = 0; page < MAX_PAGES; page++) {
        const result = await ListFaceClusterPage({ status: 'named', media_kind: '' }, cursor, MAX_PAGE_LIMIT);
        clusters.push(...(result?.clusters || []));
        if (!result?.has_more) break;
        cursor = result.next;
      }
      return this.normalize(clusters);
    },
    // 刷新 = 按已经加载的范围重取第一页（最多一页上限），用户翻出来的卡片不会被收回去。
    async refresh({ initial = false } = {}) {
      if (initial) this.initialLoading = true;
      const token = Symbol('face-refresh');
      this._refreshToken = token;
      const unnamedLimit = Math.min(MAX_PAGE_LIMIT, Math.max(CARD_PAGE_SIZE, this.unnamed.items.length));
      try {
        const [unnamedPage, named] = await Promise.all([
          ListFaceClusterPage({ status: 'unnamed', media_kind: '' }, FIRST_CURSOR(), unnamedLimit),
          this.fetchAllNamed()
        ]);
        if (this._refreshToken !== token) return;
        this.unnamed = { ...this.unnamed, items: this.normalize(unnamedPage?.clusters), next: unnamedPage?.next || FIRST_CURSOR(), hasMore: Boolean(unnamedPage?.has_more) };
        this.namedClusters = named;
        this.pruneForms();
        this.emitLoaded();
        if (this.ignored.loaded) await this.loadIgnored({ reset: true });
      } catch (err) {
        // 刷新失败时保留已有卡片：把面板清空只会让用户以为候选没了。
        if (this._refreshToken === token) this.error = `加载人物候选失败：${err}`;
      } finally {
        if (this._refreshToken === token) this.initialLoading = false;
      }
    },
    async reloadNamed() {
      this.namedClusters = await this.fetchAllNamed();
      this.pruneForms();
    },
    pruneForms() {
      const ids = new Set(this.allCards.map(card => card.id));
      if (this.detailCluster) this.detailCluster = this.allCards.find(card => card.id === this.detailCluster.id) || null;
      if (!ids.has(this.nameForm.clusterID)) this.nameForm.clusterID = 0;
      if (!ids.has(this.linkForm.clusterID)) this.linkForm.clusterID = 0;
      if (!ids.has(this.relinkForm.clusterID)) this.relinkForm = EMPTY_RELINK_FORM();
    },
    // 翻下一页未命名：用请求自身的游标判断结果还能不能用，刷新之后回来的旧页直接丢掉。
    async loadMoreUnnamed() {
      if (!this.unnamed.hasMore || this.unnamed.loadingMore) return;
      const cursor = { ...this.unnamed.next };
      this.unnamed.loadingMore = true;
      this.error = '';
      try {
        const page = await ListFaceClusterPage({ status: 'unnamed', media_kind: '' }, cursor, CARD_PAGE_SIZE);
        if (this.unnamed.next.id !== cursor.id || this.unnamed.next.count !== cursor.count) return;
        const known = new Set(this.unnamed.items.map(card => card.id));
        this.unnamed.items = [...this.unnamed.items, ...this.normalize(page?.clusters).filter(card => !known.has(card.id))];
        this.unnamed.next = page?.next || FIRST_CURSOR();
        this.unnamed.hasMore = Boolean(page?.has_more);
        this.emitLoaded();
      } catch (err) {
        this.error = `加载更多人物候选失败：${err}`;
      } finally {
        this.unnamed.loadingMore = false;
      }
    },
    async loadIgnored({ reset = true } = {}) {
      if (this.ignored.loading && !reset) return;
      const cursor = reset ? 0 : this.ignored.cursor;
      const limit = reset ? Math.min(MAX_PAGE_LIMIT, Math.max(CARD_PAGE_SIZE, this.ignored.items.length)) : CARD_PAGE_SIZE;
      const token = Symbol('face-ignored');
      this._ignoredToken = token;
      this.ignored.loading = true;
      try {
        const page = await ListIgnoredFaceClusters(cursor, limit);
        if (this._ignoredToken !== token) return;
        const rows = (page?.clusters || []).map(row => ({ ...row, cluster: this.normalize([row.cluster])[0] }));
        const known = new Set(reset ? [] : this.ignored.items.map(item => item.cluster.id));
        this.ignored.items = reset ? rows : [...this.ignored.items, ...rows.filter(row => !known.has(row.cluster.id))];
        this.ignored.cursor = Number(page?.next_cursor || 0);
        this.ignored.loaded = true;
      } catch (err) {
        if (this._ignoredToken === token) this.error = `加载已忽略的人脸组失败：${err}`;
      } finally {
        if (this._ignoredToken === token) this.ignored.loading = false;
      }
    },
    switchView(view) {
      this.view = view;
      this.closeForms();
      if (view === 'ignored' && !this.ignored.loaded) this.loadIgnored({ reset: true });
    },
    closeForms() {
      this.nameForm = { clusterID: 0, displayName: '', originalName: '' };
      this.linkForm = { clusterID: 0, keyword: '', menuOpen: false, activeIndex: 0 };
      this.relinkForm = EMPTY_RELINK_FORM();
    },
    openNameForm(card) {
      this.closeForms();
      this.error = '';
      this.notice = '';
      this.nameForm.clusterID = card.id;
    },
    openLinkForm(card) {
      this.closeForms();
      this.error = '';
      this.notice = '';
      this.linkForm.clusterID = card.id;
      this.linkForm.menuOpen = true;
      this.searchPeople(this.linkForm.keyword);
    },
    handleLinkInput() {
      this.linkForm.menuOpen = true;
      this.linkForm.activeIndex = 0;
      this.searchPeople(this.linkForm.keyword);
    },
    // 人物候选来自库里，输入即查；用 token 丢弃过期响应，避免慢的那一次覆盖新的。
    async searchPeople(keyword = '') {
      const token = Symbol('face-person-search');
      this._peopleToken = token;
      try {
        const results = await ListPeople(String(keyword || '').trim(), '', 0, 20);
        if (this._peopleToken !== token) return;
        this.people = results || [];
      } catch (err) {
        if (this._peopleToken === token) this.error = `搜索人物失败：${err}`;
      }
    },
    handleLinkKeydown(event) {
      if (event.key === 'Escape') {
        event.preventDefault();
        this.linkForm.menuOpen = false;
        return;
      }
      const options = this.linkOptions;
      if (!options.length) return;
      if (event.key === 'ArrowDown') {
        event.preventDefault();
        this.linkForm.menuOpen = true;
        this.linkForm.activeIndex = (this.linkForm.activeIndex + 1) % options.length;
      } else if (event.key === 'ArrowUp') {
        event.preventDefault();
        this.linkForm.menuOpen = true;
        this.linkForm.activeIndex = (this.linkForm.activeIndex - 1 + options.length) % options.length;
      } else if (event.key === 'Enter') {
        event.preventDefault();
        const card = this.allCards.find(item => item.id === this.linkForm.clusterID);
        if (card) this.submitLink(card, options[this.linkForm.activeIndex] || options[0]);
      }
    },
    errorMessage(err) {
      const raw = String(err?.message || err || '');
      const code = ERROR_CODES.find(item => raw.includes(item));
      return { code, text: code ? ERROR_TEXT[code] : `操作失败：${raw}` };
    },
    // 动作成功后只做局部更新（D-PC29）：apply 用返回值改本地列表，不整表重拉——
    // 否则翻出来的页会被收回去。过期类错误（别处已经处理过）才刷新。
    async runAction(key, action, apply) {
      if (this.busy) return;
      this.busy = key;
      this.error = '';
      this.notice = '';
      try {
        const result = await action();
        this.closeForms();
        const notice = await apply(result);
        if (notice) this.notice = notice;
        this.$emit('changed');
        this.emitLoaded();
      } catch (err) {
        const { code, text } = this.errorMessage(err);
        this.error = text;
        if (code && STALE_CODES.includes(code)) await this.refresh();
      } finally {
        this.busy = '';
      }
    },
    removeUnnamed(id) {
      this.unnamed.items = this.unnamed.items.filter(card => card.id !== id);
    },
    upsertNamed(view) {
      const card = this.normalize([view])[0];
      const index = this.namedClusters.findIndex(item => item.id === card.id);
      if (index >= 0) this.namedClusters.splice(index, 1, card);
      else this.namedClusters = [card, ...this.namedClusters];
    },
    submitName(card) {
      const displayName = this.nameForm.displayName;
      const originalName = this.nameForm.originalName;
      return this.runAction(`name-${card.id}`, () => NameFaceCluster(card.id, displayName, originalName), view => {
        this.removeUnnamed(card.id);
        if (view?.id) this.upsertNamed(view);
        return `已建立人物「${displayName.trim()}」并关联了这一簇里的媒体。`;
      });
    },
    submitLink(card, option) {
      if (!option) return Promise.resolve();
      return this.runAction(`link-${card.id}`, () => LinkFaceCluster(card.id, option.personID), view => {
        this.removeUnnamed(card.id);
        if (view?.id) this.upsertNamed(view);
        return `已把这一簇关联到「${option.name}」。`;
      });
    },
    // 忽略前二次确认（D-PC30）：被忽略的簇还会吸收同一个人的新人脸，只是不再提示。
    async ignore(card) {
      if (this.busy) return;
      const confirmed = await confirmAction({
        title: '忽略这组人脸',
        message: '忽略后同一个人的新人脸会归入此组且不再提示，可在「已忽略」中恢复。',
        confirmText: '忽略'
      });
      if (!confirmed) return;
      await this.runAction(`ignore-${card.id}`, () => IgnoreFaceCluster(card.id), () => {
        this.removeUnnamed(card.id);
        if (this.ignored.loaded) {
          this.ignored.items = [{ cluster: { ...card, status: 'ignored' }, ignored_at: new Date().toISOString(), absorbed_since_ignored: 0 }, ...this.ignored.items];
        }
        return '已忽略这一组；同一个人的新人脸会归入此组且不再提示，可在「已忽略」中恢复。';
      });
    },
    restore(row) {
      const cluster = row.cluster;
      return this.runAction(`restore-${cluster.id}`, () => RestoreFaceCluster(cluster.id), () => {
        this.ignored.items = this.ignored.items.filter(item => item.cluster.id !== cluster.id);
        if (!this.unnamed.items.some(card => card.id === cluster.id)) {
          this.unnamed.items = [{ ...cluster, status: 'unnamed', person_id: 0, person_name: '' }, ...this.unnamed.items];
        }
        return '已恢复这组人脸，它回到了「待处理」等你命名。';
      });
    },
    isAppendSelected(card, media) {
      return !(this.appendDeselected[card.id] || []).includes(this.mediaKey(media));
    },
    toggleAppendMedia(card, media, selected) {
      const key = this.mediaKey(media);
      const current = (this.appendDeselected[card.id] || []).filter(item => item !== key);
      this.appendDeselected = { ...this.appendDeselected, [card.id]: selected ? current : [...current, key] };
    },
    selectedAppendMedia(card) {
      return (card.append_pending_media || []).filter(media => this.isAppendSelected(card, media));
    },
    // 所勾媒体上属于这一簇的全部观测：已确认或已忽略的那几条后端会跳过，只确认 pending 的。
    async appendObservationIDs(clusterID, mediaList) {
      const wanted = new Set(mediaList.map(media => this.mediaKey(media)));
      const ids = [];
      let cursor = 0;
      for (let page = 0; page < MAX_PAGES; page++) {
        const result = await GetFaceClusterObservations(clusterID, cursor, MAX_PAGE_LIMIT);
        for (const observation of result?.observations || []) {
          if (wanted.has(this.mediaKey(observation))) ids.push(Number(observation.observation_id));
        }
        cursor = Number(result?.next_id || 0);
        if (!cursor) break;
      }
      return ids;
    },
    confirmAppend(card) {
      const selected = this.selectedAppendMedia(card);
      if (!selected.length) return Promise.resolve();
      const all = selected.length === card.append_pending_media.length;
      const name = this.cardTitle(card);
      return this.runAction(`append-confirm-${card.id}`, async () => {
        if (all) return ConfirmFaceClusterAppend(card.id);
        const ids = await this.appendObservationIDs(card.id, selected);
        if (!ids.length) throw new Error('没有找到所选媒体上待确认的人脸，请刷新后重试。');
        return ConfirmFaceClusterAppendObservations(card.id, ids);
      }, async () => {
        const { [card.id]: _dropped, ...rest } = this.appendDeselected;
        this.appendDeselected = rest;
        if (all) {
          this.upsertNamed({ ...card, append_pending_count: 0, append_pending_media: [] });
          return `已把新出现的媒体关联到「${name}」。`;
        }
        // 部分确认之后剩余的待确认数只有后端知道：已命名簇是有限集合，重取一次拿准确数字。
        await this.reloadNamed();
        return `已把所选的 ${selected.length} 个媒体关联到「${name}」，其余继续等待确认。`;
      });
    },
    dismissAppend(card) {
      return this.runAction(`append-dismiss-${card.id}`, () => DismissFaceClusterAppend(card.id), () => {
        this.upsertNamed({ ...card, append_pending_count: 0, append_pending_media: [] });
        return '已忽略这批追加候选，同一处不会再提示。';
      });
    },
    // 与后端 faceUnlinkItem.removable 同一口径：关系在、是这组人脸写的、且没有被其他已命名组覆盖。
    isRemovableRelation(item) {
      return Boolean(item?.has_relation) && item?.source === 'face' && !item?.covered_by_other;
    },
    relinkSourceText(item) {
      if (!item.has_relation) return '关系已不存在';
      const source = item.source === 'face' ? '由这组人脸写入' : '来源不明（手动、NFO 或标签转换）';
      return item.covered_by_other ? `${source} · 该人物的其他人脸组仍覆盖` : source;
    },
    relinkOutcomeText(item) {
      const apply = this.relinkForm.applyRelations;
      if (this.relinkForm.mode === 'unlink') {
        if (!item.has_relation) return '无需处理';
        return apply && this.isRemovableRelation(item) ? '将删除' : '保留';
      }
      if (!apply) return '关系不变';
      if (this.isRemovableRelation(item)) return '迁到新人物';
      return item.has_relation ? '新人物也会关联，原关系保留' : '新人物会关联';
    },
    async openRelink(card, mode) {
      this.closeForms();
      this.error = '';
      this.notice = '';
      this.relinkForm = { ...EMPTY_RELINK_FORM(), clusterID: card.id, mode, loading: true, personID: Number(card.person_id || 0) };
      if (mode === 'reassign') this.searchPeople('');
      const token = Symbol('face-relink-preview');
      this._relinkToken = token;
      const current = () => this._relinkToken === token && this.relinkForm.clusterID === card.id;
      try {
        const items = await PreviewFaceClusterUnlink(card.id);
        if (!current()) return;
        this.relinkForm.items = items || [];
      } catch (err) {
        if (!current()) return;
        const { code, text } = this.errorMessage(err);
        this.relinkForm.error = code ? text : `读取这组人脸涉及的媒体失败：${err}`;
        if (code && STALE_CODES.includes(code)) await this.refresh();
      } finally {
        if (current()) this.relinkForm.loading = false;
      }
    },
    submitRelink(card) {
      const form = { ...this.relinkForm };
      const apply = Boolean(form.applyRelations);
      const removable = this.relinkRemovableCount;
      const name = this.cardTitle(card);
      if (form.mode === 'unlink') {
        return this.runAction(`unlink-${card.id}`, () => UnlinkFaceCluster(card.id, apply), view => {
          this.namedClusters = this.namedClusters.filter(item => item.id !== card.id);
          const fallback = { ...card, status: 'unnamed', person_id: 0, person_name: '', append_pending_count: 0, append_pending_media: [] };
          const unnamedCard = this.normalize([view?.id ? view : fallback])[0];
          if (!this.unnamed.items.some(item => item.id === unnamedCard.id)) this.unnamed.items = [unnamedCard, ...this.unnamed.items];
          return apply && removable
            ? `已解除「${name}」与这组人脸的关联，并删除了 ${removable} 条由这组人脸写入的关系。`
            : `已解除「${name}」与这组人脸的关联，人物关系保留。`;
        });
      }
      const target = form.target;
      if (!target) return Promise.resolve();
      return this.runAction(`reassign-${card.id}`, () => ReassignFaceCluster(card.id, target.personID, apply), view => {
        this.upsertNamed(view?.id ? view : { ...card, person_id: target.personID, person_name: target.name });
        if (!apply) return `已把这组人脸改派给「${target.name}」，人物关系未改动。`;
        const moved = removable ? `，并从「${name}」移除了 ${removable} 条由这组人脸写入的关系` : '';
        return `已把这组人脸改派给「${target.name}」，涉及的媒体已关联到新人物${moved}。`;
      });
    }
  }
};
</script>

<style scoped>
.face-review {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.face-review__toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.face-review__toolbar .help-text {
  margin: 0;
}

.face-review__error {
  margin: 0;
  color: var(--danger-text);
  font-size: 13px;
}

.face-review__notice {
  margin: 0;
  color: var(--text-secondary);
  font-size: 13px;
}

.face-review__status {
  margin: 24px 0;
  text-align: center;
  color: var(--text-secondary);
  font-size: 13px;
}

.face-review__more {
  align-self: center;
}

.face-review__card {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 12px;
  border-radius: var(--radius-md);
}

.face-review__card-main {
  display: flex;
  align-items: flex-start;
  gap: 12px;
}

.face-review__crop {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 72px;
  height: 72px;
  flex: 0 0 auto;
  overflow: hidden;
  border: 1px solid var(--hairline);
  border-radius: var(--radius);
  background: var(--panel-muted-bg);
  color: var(--text-muted);
}

.face-review__crop img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}

.face-review__summary {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}

.face-review__meta {
  margin: 0;
  color: var(--text-secondary);
  font-size: 12px;
}

.face-review__candidates {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin: 0;
}

.face-review__candidate {
  padding: 2px 8px;
  border: 1px solid var(--accent-border);
  border-radius: 999px;
  background: var(--accent-soft);
  color: var(--accent-text);
  font-size: 12px;
}

.face-review__actions {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.face-review__form {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  padding-top: 8px;
  border-top: 1px solid var(--hairline-faint);
}

.face-review__form .text-input {
  min-width: 160px;
}

.face-review__combobox {
  position: relative;
  min-width: 220px;
}

.face-review__combobox > .search-input {
  width: 100%;
}

.face-review__options {
  position: absolute;
  top: calc(100% + 4px);
  right: 0;
  left: 0;
  z-index: 20;
  max-height: 220px;
  overflow-y: auto;
  padding: 4px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius);
  background: var(--panel-bg);
  box-shadow: var(--shadow-popover);
}

.face-review__option {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  width: 100%;
  padding: 7px 8px;
  border: 0;
  border-radius: 4px;
  background: transparent;
  color: var(--text-primary);
  text-align: left;
  cursor: pointer;
}

.face-review__option:hover,
.face-review__option.active {
  background: var(--control-hover-bg);
  color: var(--accent-color);
}

.face-review__option-hint {
  color: var(--text-muted);
  font-size: 11px;
}

.face-review__options-empty {
  display: block;
  padding: 8px;
  color: var(--text-muted);
  font-size: 12px;
}
.face-review__views {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.face-review__views button {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-height: 30px;
  padding: 0 10px;
  border: 1px solid var(--border-color);
  border-radius: 8px;
  background: var(--control-bg);
  color: var(--text-secondary);
  cursor: pointer;
  font-size: 12px;
}

.face-review__views button.active {
  border-color: var(--accent-border);
  background: var(--accent-soft);
  color: var(--accent-color);
  font-weight: 700;
}

.face-review__views span {
  min-width: 18px;
  padding: 0 5px;
  border-radius: 999px;
  background: color-mix(in srgb, currentColor 12%, transparent);
  font-size: 11px;
  text-align: center;
}

.face-review__append {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 12px;
}

.face-review__append-item {
  margin: 0;
  font-size: 12px;
  overflow-wrap: anywhere;
}

.face-review__relink {
  flex-direction: column;
  align-items: stretch;
}

.face-review__relink-title {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin: 0;
  font-size: 13px;
}

.face-review__relink-title span {
  color: var(--text-secondary);
}

.face-review__relink-list {
  display: grid;
  gap: 4px;
  max-height: 220px;
  margin: 0;
  padding: 0;
  overflow-y: auto;
  list-style: none;
}

.face-review__relink-list li {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto auto;
  align-items: center;
  gap: 8px;
  padding: 4px 6px;
  border-radius: 6px;
  background: var(--panel-muted-bg);
  font-size: 12px;
}

.face-review__relink-name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.face-review__relink-source {
  color: var(--text-muted);
}

.face-review__relink-source.is-face {
  color: var(--accent-text);
}

.face-review__relink-outcome {
  color: var(--text-primary);
  font-weight: 600;
  white-space: nowrap;
}

.face-review__relink-apply {
  margin: 0;
  font-size: 12px;
}

.face-review__reassign-options {
  display: grid;
  gap: 2px;
  max-height: 160px;
  margin-top: 4px;
  overflow-y: auto;
}
</style>
