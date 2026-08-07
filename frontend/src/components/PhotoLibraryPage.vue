<template>
  <main class="photo-library">
    <section class="photo-toolbar glass-surface">
      <div class="photo-toolbar__title">
        <h2>图片</h2>
        <p>独立扫描入库的本地图片库，可筛选、查看与管理。</p>
      </div>
      <div class="photo-toolbar__controls">
        <div class="photo-search-mode" role="group" aria-label="搜索模式">
          <button
            type="button"
            :class="['photo-search-mode__btn', { active: searchMode === 'name' }]"
            data-test="photo-mode-name"
            @click="setSearchMode('name')"
          >文件名</button>
          <button
            type="button"
            :class="['photo-search-mode__btn', { active: searchMode === 'semantic' }]"
            :disabled="!semanticAvailable"
            :title="semanticAvailable ? '按 AI 描述做语义检索' : semanticNotice"
            data-test="photo-mode-semantic"
            @click="setSearchMode('semantic')"
          >语义</button>
        </div>
        <input
          v-model="filters.keyword"
          class="search-input photo-toolbar__keyword"
          type="text"
          :placeholder="searchMode === 'semantic' ? '描述你想找的画面' : '按文件名搜索'"
          data-test="photo-keyword"
        />
        <select
          v-model="filters.sortMode"
          class="select-input photo-toolbar__sort"
          :disabled="searchMode === 'semantic'"
          :title="searchMode === 'semantic' ? '语义模式按相关度排序' : ''"
          data-test="photo-sort"
        >
          <option value="recent">最近添加</option>
          <option value="size">体积最大</option>
          <option value="rating">评分最高</option>
        </select>
        <label class="photo-toolbar__favorite">
          <input v-model="filters.favoriteOnly" type="checkbox" data-test="photo-favorite-only" />
          <span>仅收藏</span>
        </label>
        <div class="photo-toolbar__rating">
          <span>评分</span>
          <input v-model="filters.minRating" type="number" min="0" max="10" step="0.5" class="number-input" placeholder="最低" data-test="photo-min-rating" />
          <span>–</span>
          <input v-model="filters.maxRating" type="number" min="0" max="10" step="0.5" class="number-input" placeholder="最高" data-test="photo-max-rating" />
        </div>
        <button type="button" class="btn-secondary" :disabled="scanning" data-test="photo-scan" @click="scanNow">
          {{ scanning ? '扫描中...' : '立即扫描' }}
        </button>
        <button type="button" class="btn-secondary" data-test="photo-cleanup-open" @click="showCleanup = true">清理审阅</button>
        <button type="button" class="btn-secondary" data-test="photo-trash-open" @click="showTrash = true">回收站</button>
      </div>
      <p v-if="semanticNotice" class="photo-toolbar__semantic-notice" role="status" data-test="photo-semantic-unavailable">{{ semanticNotice }}</p>
      <div v-if="tags.length" class="photo-toolbar__tags">
        <button
          v-for="tag in tags"
          :key="tag.id"
          type="button"
          :class="['tag-chip', { active: filters.tagIDs.includes(tag.id) }]"
          :style="{ '--tag-color': tag.color }"
          @click="toggleTagFilter(tag.id)"
        >
          {{ tag.name }}
        </button>
      </div>
    </section>

    <p v-if="error" class="photo-library__error" role="alert">{{ error }}</p>

    <section v-if="images.length" class="photo-grid" data-test="photo-grid">
      <article v-for="(image, index) in images" :key="image.id" class="photo-card glass-surface">
        <button type="button" class="photo-card__media" :title="image.name" @click="openViewer(index)">
          <img
            v-if="!failedThumbs[image.id]"
            :src="`/preview/image-thumbnail/${image.id}`"
            :alt="image.name"
            loading="lazy"
            @error="markThumbFailed(image.id)"
          />
          <span v-else class="photo-card__fallback" data-test="photo-thumb-fallback">
            <strong>{{ image.name }}</strong>
            <em class="photo-format-badge">{{ formatBadge(image) }}</em>
          </span>
        </button>
        <div class="photo-card__overlay">
          <button
            type="button"
            :class="['photo-card__action', { 'photo-card__action--active': image.is_favorite }]"
            :title="image.is_favorite ? '取消收藏' : '收藏'"
            :aria-label="`${image.is_favorite ? '取消收藏' : '收藏'} ${image.name}`"
            @click.stop="toggleFavorite(image)"
          >{{ image.is_favorite ? '★' : '☆' }}</button>
          <button
            type="button"
            class="photo-card__action photo-card__action--danger"
            title="删除"
            :aria-label="`删除 ${image.name}`"
            data-test="photo-card-delete"
            @click.stop="requestDelete(image)"
          >×</button>
        </div>
        <div class="photo-card__meta">
          <span :title="image.name">{{ image.name }}</span>
          <small>
            {{ formatBytes(image.size) }}<template v-if="image.personal_rating != null"> · {{ image.personal_rating }} 分</template><template v-if="scoreLabel(image)"> · 相关度 {{ scoreLabel(image) }}</template>
          </small>
        </div>
      </article>
    </section>

    <div v-if="showEmptyState" class="photo-empty" data-test="photo-empty">
      <template v-if="searchMode === 'semantic' && !filters.keyword.trim()">
        <h3 data-test="photo-semantic-prompt">输入描述开始语义搜索</h3>
        <p>例如"海边日落的合影"。语义搜索只在图片库内进行，不会返回视频。</p>
      </template>
      <template v-else-if="searchMode === 'semantic'">
        <h3>没有语义命中</h3>
        <p v-if="semanticCoverage && Number(semanticCoverage.indexed) === 0" data-test="photo-semantic-no-index">
          图片语义索引还没有建立。请先在设置页生成 AI 描述并运行"图片语义索引"任务。
        </p>
        <p v-else>换个说法再试，或为更多图片生成 AI 描述后重跑图片语义索引。</p>
      </template>
      <template v-else-if="imageDirectories.length === 0">
        <h3>还没有配置图片扫描目录</h3>
        <p>先在设置页添加图片目录，再回来扫描入库。</p>
        <div class="photo-empty__actions">
          <button type="button" class="btn-primary" data-test="photo-empty-settings" @click="$emit('open-settings')">去设置添加图片目录</button>
          <button type="button" class="btn-secondary" :disabled="scanning" @click="scanNow">{{ scanning ? '扫描中...' : '立即扫描' }}</button>
        </div>
      </template>
      <template v-else-if="hasActiveFilters">
        <h3>没有符合筛选条件的图片</h3>
        <p>调整关键词、标签或评分区间后重试。</p>
      </template>
      <template v-else>
        <h3>图片库为空</h3>
        <p>目录已配置，点击扫描把磁盘上的图片同步进来。</p>
        <div class="photo-empty__actions">
          <button type="button" class="btn-primary" :disabled="scanning" @click="scanNow">{{ scanning ? '扫描中...' : '立即扫描' }}</button>
        </div>
      </template>
    </div>

    <div ref="loadSentinel" class="photo-library__sentinel" aria-hidden="true"></div>
    <button
      v-if="hasMore"
      type="button"
      class="btn-secondary photo-library__more"
      :disabled="loading"
      data-test="photo-load-more"
      @click="loadMore()"
    >
      {{ loading ? '加载中...' : '加载更多图片' }}
    </button>

    <div v-if="viewerImage" class="photo-viewer" role="dialog" aria-modal="true" :aria-label="`查看 ${viewerImage.name}`" data-test="photo-viewer">
      <button type="button" class="photo-viewer__close" title="关闭 (Esc)" data-test="photo-viewer-close" @click="closeViewer">×</button>
      <button type="button" class="photo-viewer__nav photo-viewer__nav--prev" :disabled="viewerIndex <= 0" title="上一张 (←)" @click="viewerPrev">‹</button>
      <div class="photo-viewer__stage" @click.self="closeViewer">
        <img
          v-if="!viewerImageError"
          :key="viewerImage.id"
          :src="`/preview/image/${viewerImage.id}`"
          :alt="viewerImage.name"
          class="photo-viewer__img"
          @error="viewerImageError = true"
        />
        <div v-else class="photo-viewer__fallback" data-test="photo-viewer-fallback">
          <strong>{{ viewerImage.name }}</strong>
          <em class="photo-format-badge">{{ formatBadge(viewerImage) }}</em>
          <p>无法加载大图。文件可能已被移动、损坏，或该格式（如 HEIC/RAW）在当前平台不支持解码。</p>
          <small>{{ viewerImage.path }}</small>
          <small>{{ formatBytes(viewerImage.size) }}<template v-if="viewerImage.width && viewerImage.height"> · {{ viewerImage.width }}×{{ viewerImage.height }}</template></small>
        </div>
      </div>
      <button type="button" class="photo-viewer__nav photo-viewer__nav--next" :disabled="viewerIndex >= images.length - 1" title="下一张 (→)" @click="viewerNext">›</button>

      <aside class="photo-viewer__sidebar glass-surface">
        <h3 :title="viewerImage.name">{{ viewerImage.name }}</h3>
        <dl class="photo-viewer__facts">
          <dt>路径</dt><dd :title="viewerImage.path">{{ viewerImage.path }}</dd>
          <dt>尺寸</dt><dd>{{ viewerImage.width && viewerImage.height ? `${viewerImage.width}×${viewerImage.height}` : '未探测' }}</dd>
          <dt>大小</dt><dd>{{ formatBytes(viewerImage.size) }}</dd>
          <dt>格式</dt><dd>{{ formatBadge(viewerImage) }}</dd>
        </dl>

        <div class="photo-viewer__block">
          <div class="photo-viewer__block-heading">
            <h4>收藏与评分</h4>
            <button
              type="button"
              :class="['photo-card__action', { 'photo-card__action--active': viewerImage.is_favorite }]"
              :title="viewerImage.is_favorite ? '取消收藏 (F)' : '收藏 (F)'"
              data-test="photo-viewer-favorite"
              @click="toggleFavorite(viewerImage)"
            >{{ viewerImage.is_favorite ? '★ 已收藏' : '☆ 收藏' }}</button>
          </div>
          <div class="photo-viewer__rating">
            <input
              v-model="ratingDraft"
              type="number"
              min="0"
              max="10"
              step="0.5"
              class="number-input"
              placeholder="0–10，步进 0.5"
              data-test="photo-rating-input"
              @change="applyRating"
            />
            <button type="button" class="btn-secondary btn-compact" data-test="photo-rating-clear" @click="clearRating">清空</button>
          </div>
        </div>

        <div class="photo-viewer__block">
          <h4>标签</h4>
          <div class="photo-viewer__tags">
            <span v-for="tag in viewerImage.tags || []" :key="tag.id" class="tag-badge" :style="{ '--tag-color': tag.color }">
              {{ tag.name }}
              <button type="button" class="tag-remove" :aria-label="`移除标签 ${tag.name}`" @click="removeTag(viewerImage, tag)">×</button>
            </span>
            <span v-if="!(viewerImage.tags || []).length" class="photo-viewer__muted">尚无标签</span>
          </div>
          <div v-if="addableTags.length" class="photo-viewer__tag-add">
            <select v-model="tagToAdd" class="select-input" data-test="photo-tag-select">
              <option :value="0" disabled>选择标签...</option>
              <option v-for="tag in addableTags" :key="tag.id" :value="tag.id">{{ tag.name }}</option>
            </select>
            <button type="button" class="btn-secondary btn-compact" :disabled="!tagToAdd" data-test="photo-tag-add" @click="addTag(viewerImage)">添加</button>
          </div>
        </div>

        <div class="photo-viewer__block">
          <div class="photo-viewer__block-heading">
            <h4>AI 描述</h4>
            <button
              type="button"
              class="btn-secondary btn-compact"
              :disabled="regeneratingDescription"
              data-test="photo-ai-regenerate"
              @click="regenerateDescription"
            >{{ regeneratingDescription ? '生成中...' : '重新生成' }}</button>
          </div>
          <p v-if="viewerDetailError" class="photo-viewer__muted">{{ viewerDetailError }}</p>
          <p v-else-if="viewerDescription" class="photo-viewer__description" data-test="photo-ai-description">{{ viewerDescription }}</p>
          <p v-else class="photo-viewer__muted" data-test="photo-ai-description-empty">尚未生成</p>
          <small v-if="descriptionGeneratedAt" class="photo-viewer__muted" data-test="photo-ai-generated-at">生成时间：{{ formatDateTime(descriptionGeneratedAt) }}</small>
          <p v-if="regenerateError" class="photo-library__error" role="alert" data-test="photo-ai-regenerate-error">{{ regenerateError }}</p>
        </div>

        <div class="photo-viewer__block">
          <button type="button" class="btn-danger" data-test="photo-viewer-delete" @click="requestDelete(viewerImage)">删除这张图片</button>
        </div>
      </aside>
    </div>

    <BaseModal v-if="deleteTarget" close-on-overlay stop-modal-clicks @close="deleteTarget = null">
      <h2>确认删除</h2>
      <p>确定要删除图片 "{{ deleteTarget.name }}" 吗？</p>
      <div class="photo-delete__options">
        <label>
          <input v-model="deleteFileChoice" type="checkbox" data-test="photo-delete-file" />
          同时将原始文件移入回收站
        </label>
      </div>
      <p class="photo-viewer__muted">不勾选时仅移除数据库记录，原文件会保留在磁盘上。</p>
      <div class="modal-actions">
        <button type="button" class="btn-danger" :disabled="deleting" data-test="photo-delete-confirm" @click="confirmDelete">确认删除</button>
        <button type="button" class="btn-secondary" :disabled="deleting" @click="deleteTarget = null">取消</button>
      </div>
    </BaseModal>

    <PhotoCleanupPanel v-if="showCleanup" @close="showCleanup = false" @deleted="reload" />

    <PhotoTrashDialog :visible="showTrash" @close="showTrash = false" @restored="handleRestored" />
  </main>
</template>

<script>
import {
  AddTagToImage, DeleteImage, GetAllImageDirectories, GetImageDetail, GetImageSemanticIndexStatus,
  RegenerateImageAIDescription, RemoveTagFromImage, SearchImagePage, SearchImagesSemantic,
  SetImageFavorite, SetImageRating, SyncImageDirectories
} from '../../wailsjs/go/main/App';
import BaseModal from './ui/BaseModal.vue';
import PhotoCleanupPanel from './PhotoCleanupPanel.vue';
import PhotoTrashDialog from './PhotoTrashDialog.vue';
import { formatBytes } from '../utils/mediaDetails.js';

const PAGE_SIZE = 60;

export default {
  name: 'PhotoLibraryPage',
  components: { BaseModal, PhotoCleanupPanel, PhotoTrashDialog },
  props: {
    settings: { type: Object, required: true },
    tags: { type: Array, default: () => [] }
  },
  emits: ['open-settings'],
  data() {
    return {
      images: [],
      nextCursor: null,
      exhausted: false,
      loading: false,
      loadedOnce: false,
      error: '',
      searchMode: 'name',
      semanticStatus: null,
      semanticNoticeOverride: '',
      semanticOffset: 0,
      semanticCoverage: null,
      semanticScores: {},
      imageDirectories: [],
      scanning: false,
      failedThumbs: {},
      filters: {
        keyword: '',
        tagIDs: [],
        favoriteOnly: false,
        minRating: '',
        maxRating: '',
        sortMode: 'recent'
      },
      viewerIndex: -1,
      viewerImageError: false,
      viewerDetail: null,
      viewerDetailError: '',
      descriptionGeneratedAt: '',
      regeneratingDescription: false,
      regenerateError: '',
      ratingDraft: '',
      tagToAdd: 0,
      showTrash: false,
      showCleanup: false,
      deleteTarget: null,
      deleteFileChoice: false,
      deleting: false
    };
  },
  computed: {
    hasMore() { return !this.exhausted && this.loadedOnce; },
    semanticAvailable() { return Boolean(this.semanticStatus?.available); },
    semanticNotice() {
      if (this.semanticNoticeOverride) return this.semanticNoticeOverride;
      if (!this.semanticStatus || this.semanticAvailable) return '';
      const reason = String(this.semanticStatus.unavailable || '').trim();
      return `语义搜索不可用：${reason || '图片语义索引能力未就绪'}`;
    },
    viewerImage() { return this.viewerIndex >= 0 ? this.images[this.viewerIndex] || null : null; },
    viewerDescription() {
      return this.viewerDetail && Number(this.viewerDetail.image?.id) === Number(this.viewerImage?.id)
        ? String(this.viewerDetail.ai_description || '').trim()
        : '';
    },
    addableTags() {
      const applied = new Set((this.viewerImage?.tags || []).map(tag => Number(tag.id)));
      return this.tags.filter(tag => !applied.has(Number(tag.id)));
    },
    hasActiveFilters() {
      return Boolean(this.filters.keyword.trim()) || this.filters.tagIDs.length > 0 || this.filters.favoriteOnly
        || this.filters.minRating !== '' || this.filters.maxRating !== '';
    },
    showEmptyState() {
      return this.loadedOnce && !this.loading && this.images.length === 0;
    }
  },
  watch: {
    'filters.keyword'() {
      clearTimeout(this._keywordTimer);
      this._keywordTimer = setTimeout(() => this.reload(), 300);
    },
    'filters.favoriteOnly'() { this.reload(); },
    'filters.sortMode'() { this.reload(); },
    'filters.minRating'() { this.reload(); },
    'filters.maxRating'() { this.reload(); }
  },
  mounted() {
    this.setupInfiniteLoading();
    window.addEventListener('keydown', this.handleKeydown);
    this.loadImageDirectories();
    this.loadSemanticStatus();
    this.reload();
  },
  beforeUnmount() {
    clearTimeout(this._keywordTimer);
    window.removeEventListener('keydown', this.handleKeydown);
    this._intersectionObserver?.disconnect();
  },
  methods: {
    formatBytes,
    formatBadge(image) {
      const format = String(image?.format || '').trim();
      if (format) return format.toUpperCase();
      const name = String(image?.name || '');
      const dot = name.lastIndexOf('.');
      return dot >= 0 ? name.slice(dot + 1).toUpperCase() : '未知格式';
    },
    setupInfiniteLoading() {
      if (typeof IntersectionObserver === 'undefined') return;
      const root = this.$el?.closest?.('.main-view') || null;
      this._intersectionObserver = new IntersectionObserver(entries => {
        if (!entries.some(entry => entry.isIntersecting)) return;
        this.loadMore();
      }, { root, rootMargin: '320px 0px' });
      this.$nextTick(() => {
        if (this.$refs.loadSentinel) this._intersectionObserver.observe(this.$refs.loadSentinel);
      });
    },
    scoreLabel(image) {
      if (this.searchMode !== 'semantic') return '';
      const score = this.semanticScores[image?.id];
      return score == null ? '' : Number(score).toFixed(2);
    },
    formatDateTime(value) {
      if (!value) return '';
      const date = new Date(value);
      return Number.isNaN(date.getTime()) ? String(value) : date.toLocaleString();
    },
    async loadSemanticStatus() {
      try {
        this.semanticStatus = await GetImageSemanticIndexStatus() || null;
      } catch (err) {
        this.semanticStatus = { available: false, unavailable: String(err?.message || err) };
      }
    },
    setSearchMode(mode) {
      if (mode === this.searchMode) return;
      if (mode === 'semantic' && !this.semanticAvailable) return;
      this.searchMode = mode;
      this.semanticNoticeOverride = '';
      this.reload();
    },
    buildFilter() {
      const parseRating = value => (value === '' || value == null ? null : Number(value));
      return {
        keyword: this.filters.keyword.trim(),
        tag_ids: [...this.filters.tagIDs],
        favorite_only: this.filters.favoriteOnly,
        min_rating: parseRating(this.filters.minRating),
        max_rating: parseRating(this.filters.maxRating),
        min_size: 0,
        max_size: 0,
        sort_mode: this.filters.sortMode
      };
    },
    buildSemanticFilter() {
      const parseRating = value => (value === '' || value == null ? null : Number(value));
      return {
        tag_ids: [...this.filters.tagIDs],
        favorite_only: this.filters.favoriteOnly,
        min_rating: parseRating(this.filters.minRating),
        max_rating: parseRating(this.filters.maxRating),
        min_size: 0,
        max_size: 0
      };
    },
    async reload() {
      const token = Symbol('photo-query');
      this._queryToken = token;
      this.images = [];
      // 文件名游标分页与语义 offset 分页是两套状态，切换时一并清空避免串档。
      this.nextCursor = null;
      this.semanticOffset = 0;
      this.semanticScores = {};
      this.semanticCoverage = null;
      this.exhausted = false;
      this.failedThumbs = {};
      this.closeViewer();
      await this.loadMore(token, true);
    },
    async loadMore(token = this._queryToken, force = false) {
      if (!token) { token = Symbol('photo-query'); this._queryToken = token; }
      if ((!force && this.loading) || (this.exhausted && !force)) return;
      if (this.searchMode === 'semantic') {
        await this.loadMoreSemantic(token);
        return;
      }
      this.loading = true;
      this.error = '';
      const request = { filter: this.buildFilter(), limit: PAGE_SIZE };
      if (this.nextCursor) request.cursor = this.nextCursor;
      try {
        const page = await SearchImagePage(request);
        if (this._queryToken !== token) return;
        const incoming = page?.images || [];
        this.images.push(...incoming);
        this.nextCursor = page?.next_cursor || null;
        this.exhausted = !this.nextCursor;
        this.loadedOnce = true;
      } catch (err) {
        if (this._queryToken === token) { this.error = `加载图片失败：${err}`; this.exhausted = true; this.loadedOnce = true; }
      } finally {
        if (this._queryToken === token) this.loading = false;
      }
    },
    async loadMoreSemantic(token) {
      const query = this.filters.keyword.trim();
      if (!query) {
        this.images = [];
        this.semanticScores = {};
        this.semanticCoverage = null;
        this.semanticOffset = 0;
        this.exhausted = true;
        this.loadedOnce = true;
        this.loading = false;
        return;
      }
      this.loading = true;
      this.error = '';
      const request = {
        query,
        filter: this.buildSemanticFilter(),
        offset: this.semanticOffset,
        limit: PAGE_SIZE
      };
      try {
        const page = await SearchImagesSemantic(request);
        if (this._queryToken !== token) return;
        const hits = page?.hits || [];
        const scores = { ...this.semanticScores };
        hits.forEach(hit => {
          if (!hit?.image) return;
          this.images.push(hit.image);
          scores[hit.image.id] = hit.score;
        });
        this.semanticScores = scores;
        this.semanticOffset += hits.length;
        this.semanticCoverage = page?.coverage || null;
        this.exhausted = !page?.has_more;
        this.loadedOnce = true;
      } catch (err) {
        if (this._queryToken !== token) return;
        this.exhausted = true;
        this.loadedOnce = true;
        this.loading = false;
        await this.handleSemanticFailure(err);
        return;
      } finally {
        if (this._queryToken === token) this.loading = false;
      }
    },
    // handleSemanticFailure 明确暴露失败原因；若能力本身不可用则退回文件名模式并禁用语义入口。
    async handleSemanticFailure(err) {
      this.semanticNoticeOverride = `语义搜索失败：${String(err?.message || err)}`;
      await this.loadSemanticStatus();
      if (this.semanticAvailable) return;
      this.searchMode = 'name';
      await this.reload();
    },
    async loadImageDirectories() {
      try {
        this.imageDirectories = await GetAllImageDirectories() || [];
      } catch (err) {
        this.imageDirectories = [];
      }
    },
    async scanNow() {
      if (this.scanning) return;
      this.scanning = true;
      this.error = '';
      try {
        await SyncImageDirectories();
        await this.loadImageDirectories();
        await this.reload();
      } catch (err) {
        this.error = `扫描图片目录失败：${err}`;
      } finally {
        this.scanning = false;
      }
    },
    toggleTagFilter(tagID) {
      const id = Number(tagID);
      this.filters.tagIDs = this.filters.tagIDs.includes(id)
        ? this.filters.tagIDs.filter(item => item !== id)
        : [...this.filters.tagIDs, id];
      this.reload();
    },
    markThumbFailed(imageID) {
      this.failedThumbs = { ...this.failedThumbs, [imageID]: true };
    },
    openViewer(index) {
      if (index < 0 || index >= this.images.length) return;
      this.viewerIndex = index;
      this.viewerImageError = false;
      this.tagToAdd = 0;
      const image = this.images[index];
      this.ratingDraft = image.personal_rating == null ? '' : image.personal_rating;
      this.loadViewerDetail(image.id);
    },
    closeViewer() {
      this.viewerIndex = -1;
      this.viewerImageError = false;
      this.viewerDetail = null;
      this.viewerDetailError = '';
      this.descriptionGeneratedAt = '';
      this.regenerateError = '';
      this.tagToAdd = 0;
    },
    viewerNext() {
      if (this.viewerIndex < this.images.length - 1) this.openViewer(this.viewerIndex + 1);
    },
    viewerPrev() {
      if (this.viewerIndex > 0) this.openViewer(this.viewerIndex - 1);
    },
    async loadViewerDetail(imageID) {
      const token = Symbol('photo-detail');
      this._detailToken = token;
      this.viewerDetail = null;
      this.viewerDetailError = '';
      this.descriptionGeneratedAt = '';
      this.regenerateError = '';
      try {
        const detail = await GetImageDetail(imageID);
        if (this._detailToken !== token) return;
        this.viewerDetail = detail;
        const image = detail?.image;
        if (image && Number(image.id) === Number(this.viewerImage?.id)) {
          this.patchImage(image);
          this.ratingDraft = image.personal_rating == null ? '' : image.personal_rating;
        }
      } catch (err) {
        if (this._detailToken === token) this.viewerDetailError = `加载详情失败：${err}`;
      }
    },
    async regenerateDescription() {
      const image = this.viewerImage;
      if (!image || this.regeneratingDescription) return;
      this.regeneratingDescription = true;
      this.regenerateError = '';
      try {
        const result = await RegenerateImageAIDescription(image.id);
        if (Number(this.viewerImage?.id) !== Number(image.id)) return;
        const description = String(result?.description || '').trim();
        this.viewerDetail = { ...(this.viewerDetail || {}), image, ai_description: description };
        this.descriptionGeneratedAt = result?.generated_at || '';
        this.viewerDetailError = '';
      } catch (err) {
        if (Number(this.viewerImage?.id) !== Number(image.id)) return;
        const message = String(err?.message || err);
        this.regenerateError = message.includes('AI 配置不可用')
          ? `重新生成描述失败：${message}。请先在设置页配置 AI 接口的 BaseURL 与模型。`
          : `重新生成描述失败：${message}`;
      } finally {
        this.regeneratingDescription = false;
      }
    },
    handleKeydown(event) {
      if (this.viewerIndex < 0) return;
      const tag = event.target?.tagName;
      if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;
      if (event.key === 'Escape') { event.preventDefault(); this.closeViewer(); }
      else if (event.key === 'ArrowRight') { event.preventDefault(); this.viewerNext(); }
      else if (event.key === 'ArrowLeft') { event.preventDefault(); this.viewerPrev(); }
      else if (event.key === 'f' || event.key === 'F') {
        event.preventDefault();
        if (this.viewerImage) this.toggleFavorite(this.viewerImage);
      }
    },
    patchImage(updated) {
      if (!updated) return;
      const index = this.images.findIndex(item => Number(item.id) === Number(updated.id));
      if (index < 0) return;
      const merged = { ...this.images[index], ...updated };
      if (!updated.tags) merged.tags = this.images[index].tags;
      this.images.splice(index, 1, merged);
    },
    async toggleFavorite(image) {
      try {
        const updated = await SetImageFavorite(image.id, !image.is_favorite);
        this.patchImage(updated);
      } catch (err) {
        this.error = `更新收藏失败：${err}`;
      }
    },
    async applyRating() {
      const image = this.viewerImage;
      if (!image) return;
      const value = this.ratingDraft === '' || this.ratingDraft == null ? null : Number(this.ratingDraft);
      try {
        const updated = await SetImageRating(image.id, value);
        this.patchImage(updated);
        this.ratingDraft = updated?.personal_rating == null ? '' : updated.personal_rating;
      } catch (err) {
        this.error = `更新评分失败：${err}`;
        this.ratingDraft = image.personal_rating == null ? '' : image.personal_rating;
      }
    },
    async clearRating() {
      this.ratingDraft = '';
      await this.applyRating();
    },
    async addTag(image) {
      const tagID = Number(this.tagToAdd);
      if (!image || !tagID) return;
      try {
        await AddTagToImage(image.id, tagID);
        const tag = this.tags.find(item => Number(item.id) === Number(tagID));
        if (tag) this.patchImage({ id: image.id, tags: [...(image.tags || []), tag] });
        this.tagToAdd = 0;
      } catch (err) {
        this.error = `添加标签失败：${err}`;
      }
    },
    async removeTag(image, tag) {
      if (!image || !tag) return;
      try {
        await RemoveTagFromImage(image.id, tag.id);
        this.patchImage({ id: image.id, tags: (image.tags || []).filter(item => Number(item.id) !== Number(tag.id)) });
      } catch (err) {
        this.error = `移除标签失败：${err}`;
      }
    },
    requestDelete(image) {
      if (!image) return;
      if (!this.settings.confirm_before_delete) {
        this.performDelete(image, !!this.settings.delete_original_file);
        return;
      }
      this.deleteFileChoice = !!this.settings.delete_original_file;
      this.deleteTarget = image;
    },
    async confirmDelete() {
      if (!this.deleteTarget || this.deleting) return;
      this.deleting = true;
      try {
        await this.performDelete(this.deleteTarget, this.deleteFileChoice);
        this.deleteTarget = null;
      } finally {
        this.deleting = false;
      }
    },
    async performDelete(image, deleteFile) {
      try {
        await DeleteImage(image.id, deleteFile);
        const index = this.images.findIndex(item => Number(item.id) === Number(image.id));
        if (index >= 0) {
          this.images.splice(index, 1);
          if (this.viewerIndex >= 0) {
            if (index === this.viewerIndex) this.closeViewer();
            else if (index < this.viewerIndex) this.viewerIndex -= 1;
          }
        }
      } catch (err) {
        this.error = `删除图片失败：${err}`;
      }
    },
    handleRestored(image) {
      if (!image) return;
      if (!this.images.some(item => Number(item.id) === Number(image.id))) {
        this.images.unshift(image);
      }
      this.loadedOnce = true;
    }
  }
};
</script>

<style scoped>
.photo-library { padding: 14px 18px 28px; display: flex; flex-direction: column; gap: 14px; }
.photo-toolbar { padding: 14px 16px; border-radius: 14px; display: flex; flex-direction: column; gap: 12px; }
.photo-toolbar__title h2 { margin: 0 0 3px; font-size: 18px; }
.photo-toolbar__title p { margin: 0; color: var(--text-muted); font-size: 12px; }
.photo-toolbar__controls { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; }
.photo-toolbar__keyword { max-width: 240px; }
.photo-toolbar__sort { width: auto; min-width: 120px; }
.photo-toolbar__favorite { display: inline-flex; align-items: center; gap: 6px; color: var(--text-primary); font-size: 13px; white-space: nowrap; cursor: pointer; }
.photo-toolbar__rating { display: inline-flex; align-items: center; gap: 6px; color: var(--text-muted); font-size: 12px; }
.photo-toolbar__rating .number-input { width: 76px; }
.photo-toolbar__tags { display: flex; flex-wrap: wrap; gap: 6px; }
.photo-search-mode { display: inline-flex; padding: 2px; border: 1px solid var(--border-color); border-radius: 999px; background: var(--control-bg); }
.photo-search-mode__btn { padding: 4px 12px; border: 0; border-radius: 999px; background: transparent; color: var(--text-secondary); font-size: 12px; cursor: pointer; }
.photo-search-mode__btn.active { background: var(--panel-bg); color: var(--text-primary); }
.photo-search-mode__btn:disabled { opacity: 0.45; cursor: not-allowed; }
.photo-toolbar__semantic-notice { margin: 0; color: var(--text-muted); font-size: 12px; }
.photo-library__error { margin: 0; color: var(--danger-color); }

.photo-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(180px, 1fr)); gap: 12px; }
.photo-card { position: relative; overflow: hidden; border-radius: 13px; }
.photo-card__media { display: block; width: 100%; aspect-ratio: 1; padding: 0; border: 0; background: var(--thumb-bg); cursor: pointer; }
.photo-card__media img { width: 100%; height: 100%; display: block; object-fit: cover; }
.photo-card__fallback { width: 100%; height: 100%; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 8px; padding: 12px; color: var(--text-muted); }
.photo-card__fallback strong { max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-primary); font-size: 12px; }
.photo-format-badge { display: inline-block; padding: 2px 8px; border: 1px solid var(--border-color); border-radius: 999px; background: var(--control-bg); color: var(--text-secondary); font-size: 11px; font-style: normal; font-weight: 700; letter-spacing: 0.4px; }
.photo-card__overlay { position: absolute; top: 8px; right: 8px; display: flex; gap: 6px; opacity: 0; transition: opacity var(--transition); }
.photo-card:hover .photo-card__overlay,
.photo-card:focus-within .photo-card__overlay { opacity: 1; }
.photo-card__action { display: inline-flex; align-items: center; justify-content: center; min-width: 26px; height: 26px; padding: 0 7px; border: 1px solid var(--border-color); border-radius: 999px; background: var(--panel-bg); color: var(--text-secondary); font-size: 13px; cursor: pointer; }
.photo-card__action--active { color: #f6b94a; opacity: 1; }
.photo-card:has(.photo-card__action--active) .photo-card__overlay { opacity: 1; }
.photo-card__action--danger:hover { color: var(--danger-color); border-color: var(--danger-border); }
.photo-card__meta { display: grid; gap: 2px; padding: 9px 11px 10px; }
.photo-card__meta span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; color: var(--text-primary); }
.photo-card__meta small { color: var(--text-muted); font-size: 11px; }

.photo-empty { padding: 48px 16px; text-align: center; color: var(--text-muted); }
.photo-empty h3 { margin: 0 0 8px; color: var(--text-primary); }
.photo-empty p { margin: 0 0 16px; font-size: 13px; }
.photo-empty__actions { display: flex; justify-content: center; gap: 10px; }
.photo-library__sentinel { height: 1px; }
.photo-library__more { align-self: center; }

.photo-viewer { position: fixed; inset: 0; z-index: 200; display: grid; grid-template-columns: minmax(0, 1fr) min(360px, 38vw); background: rgba(8, 12, 20, 0.86); }
.photo-viewer__stage { position: relative; display: flex; align-items: center; justify-content: center; min-width: 0; padding: 48px 56px; }
.photo-viewer__img { max-width: 100%; max-height: 100%; object-fit: contain; border-radius: 6px; }
.photo-viewer__fallback { max-width: 420px; display: grid; gap: 10px; justify-items: center; padding: 24px; border: 1px dashed rgba(255, 255, 255, 0.3); border-radius: 12px; color: rgba(255, 255, 255, 0.75); text-align: center; }
.photo-viewer__fallback strong { color: #fff; word-break: break-all; }
.photo-viewer__fallback p { margin: 0; font-size: 13px; }
.photo-viewer__fallback small { font-size: 11px; word-break: break-all; }
.photo-viewer__close { position: absolute; top: 14px; left: 14px; z-index: 2; width: 34px; height: 34px; border: 0; border-radius: 999px; background: rgba(255, 255, 255, 0.14); color: #fff; font-size: 18px; cursor: pointer; }
.photo-viewer__nav { position: absolute; top: 50%; z-index: 2; width: 40px; height: 40px; border: 0; border-radius: 999px; background: rgba(255, 255, 255, 0.14); color: #fff; font-size: 22px; cursor: pointer; transform: translateY(-50%); }
.photo-viewer__nav:disabled { opacity: 0.35; cursor: default; }
.photo-viewer__nav--prev { left: 14px; }
.photo-viewer__nav--next { right: calc(min(360px, 38vw) + 14px); }
.photo-viewer__sidebar { display: flex; flex-direction: column; gap: 14px; min-width: 0; padding: 18px 16px; border-radius: 0; overflow-y: auto; }
.photo-viewer__sidebar h3 { margin: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 15px; }
.photo-viewer__facts { display: grid; grid-template-columns: max-content 1fr; gap: 6px 10px; margin: 0; font-size: 12px; }
.photo-viewer__facts dt { color: var(--text-muted); }
.photo-viewer__facts dd { margin: 0; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-primary); }
.photo-viewer__block { display: grid; gap: 8px; padding-top: 12px; border-top: 1px solid var(--border-color); }
.photo-viewer__block h4 { margin: 0; font-size: 13px; color: var(--text-secondary); }
.photo-viewer__block-heading { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.photo-viewer__block-heading h4 { margin: 0; font-size: 13px; color: var(--text-secondary); }
.photo-viewer__rating { display: flex; align-items: center; gap: 8px; }
.photo-viewer__rating .number-input { width: 120px; }
.photo-viewer__tags { display: flex; flex-wrap: wrap; gap: 6px; }
.photo-viewer__tag-add { display: flex; gap: 8px; }
.photo-viewer__tag-add .select-input { flex: 1; min-width: 0; }
.photo-viewer__muted { margin: 0; color: var(--text-muted); font-size: 12px; }
.photo-viewer__description { margin: 0; color: var(--text-primary); font-size: 13px; line-height: 1.7; white-space: pre-wrap; }
.photo-delete__options { display: grid; gap: 8px; margin-top: 14px; }
.photo-delete__options label { display: inline-flex; align-items: center; gap: 7px; font-size: 13px; }

@media (max-width: 900px) {
  .photo-viewer { grid-template-columns: 1fr; grid-template-rows: minmax(0, 1fr) auto; }
  .photo-viewer__nav--next { right: 14px; }
  .photo-viewer__sidebar { max-height: 42vh; }
}
</style>
