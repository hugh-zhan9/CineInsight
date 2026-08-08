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
          :disabled="searchMode === 'semantic' || timelineMode"
          :title="sortDisabledReason"
          data-test="photo-sort"
        >
          <option value="recent">最近添加</option>
          <option value="size">体积最大</option>
          <option value="rating">评分最高</option>
          <option value="taken">拍摄时间</option>
        </select>
        <label
          class="photo-toolbar__timeline"
          :title="searchMode === 'semantic' ? '语义模式按相关度排序，不做时间线分组' : '按拍摄时间倒序，并插入年月分组头'"
        >
          <input
            v-model="timelineMode"
            type="checkbox"
            :disabled="searchMode === 'semantic'"
            data-test="photo-timeline-toggle"
          />
          <span>时间线</span>
        </label>
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
        <div class="photo-toolbar__taken" :title="searchMode === 'semantic' ? '语义模式暂不支持拍摄日期筛选' : ''">
          <span>拍摄日期</span>
          <input
            v-model="filters.takenAfter"
            type="date"
            class="text-input photo-toolbar__date"
            aria-label="拍摄日期起"
            :disabled="searchMode === 'semantic'"
            data-test="photo-taken-after"
          />
          <span>–</span>
          <input
            v-model="filters.takenBefore"
            type="date"
            class="text-input photo-toolbar__date"
            aria-label="拍摄日期止"
            :disabled="searchMode === 'semantic'"
            data-test="photo-taken-before"
          />
        </div>
        <button type="button" class="btn-secondary" :disabled="scanning" data-test="photo-scan" @click="scanNow">
          {{ scanning ? '扫描中...' : '立即扫描' }}
        </button>
        <button type="button" class="btn-secondary photo-cleanup-open-btn" data-test="photo-cleanup-open" @click="showCleanup = true">
          清理审阅
          <span v-if="cleanupRunning" class="photo-cleanup-badge" data-test="photo-cleanup-badge" :title="cleanupBadgeTitle">分析中 {{ cleanupProgressText }}</span>
          <span v-else-if="cleanupDone" class="photo-cleanup-badge photo-cleanup-badge--done" data-test="photo-cleanup-done" title="清理分析已完成，点击查看候选">待审阅 {{ cleanupGroupCount }} 组</span>
        </button>
        <button type="button" class="btn-secondary" data-test="photo-trash-open" @click="showTrash = true">回收站</button>
      </div>
      <p v-if="semanticNotice" class="photo-toolbar__semantic-notice" role="status" data-test="photo-semantic-unavailable">{{ semanticNotice }}</p>
      <div v-if="imageTags.length" class="photo-toolbar__tags">
        <button
          v-for="tag in imageTags"
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

    <!-- 切走再切回不重新加载列表（滚动位置要留着），所以用提示条告诉用户库里有变化。 -->
    <div v-if="libraryChanged" class="photo-library__refresh" role="status" data-test="photo-library-refresh">
      <span>图片库有更新。刷新会回到列表顶部。</span>
      <button type="button" class="btn-secondary btn-compact" data-test="photo-library-refresh-apply" @click="applyLibraryRefresh">刷新</button>
      <button type="button" class="photo-library__refresh-dismiss" aria-label="忽略此提示" data-test="photo-library-refresh-dismiss" @click="libraryChanged = false">×</button>
    </div>

    <p v-if="error" class="photo-library__error" role="alert">{{ error }}</p>

    <section
      v-if="images.length"
      ref="gridShell"
      class="photo-grid"
      :style="gridStyle"
      :data-scroll-owner-fallback="virtualized ? null : 'true'"
      data-test="photo-grid"
    >
      <div v-if="virtualized && windowState.topSpacer > 0" :style="{ height: `${windowState.topSpacer}px` }" aria-hidden="true"></div>

      <template v-for="row in renderedRows" :key="row.key">
        <div
          v-if="row.isHeader"
          class="photo-timeline-header"
          :style="rowStyle(row)"
          data-test="photo-timeline-header"
        >
          <h3>{{ timelineLabel(row) }}</h3>
        </div>
        <div v-else class="photo-grid-row" :style="rowStyle(row)">
          <article v-for="(image, offset) in rowImages(row)" :key="image.id" class="photo-card glass-surface">
            <button type="button" class="photo-card__media" :title="image.name" @click="openViewer(row.startIndex + offset)">
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
              <p v-if="cardDescription(image)" class="photo-card__description" :title="cardDescription(image)" data-test="photo-card-description">{{ cardDescription(image) }}</p>
            </div>
          </article>
        </div>
      </template>

      <div v-if="virtualized && windowState.bottomSpacer > 0" :style="{ height: `${windowState.bottomSpacer}px` }" aria-hidden="true"></div>
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

        <div v-if="exifFacts.length" class="photo-viewer__block" data-test="photo-viewer-exif">
          <h4>拍摄信息</h4>
          <dl class="photo-viewer__facts">
            <template v-for="fact in exifFacts" :key="fact.label">
              <dt>{{ fact.label }}</dt><dd :title="fact.value">{{ fact.value }}</dd>
            </template>
          </dl>
        </div>

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
  AddTagToImage, DeleteImage, GetAllImageDirectories, GetImageDetail, GetImageSemanticIndexStatus, GetImageTags,
  ListImageTimelineBuckets, RegenerateImageAIDescription, RemoveTagFromImage, SearchImagePage,
  SearchImagesSemantic, SetImageFavorite, SetImageRating, SyncImageDirectories
} from '../../wailsjs/go/main/App';
import BaseModal from './ui/BaseModal.vue';
import PhotoCleanupPanel from './PhotoCleanupPanel.vue';
import PhotoTrashDialog from './PhotoTrashDialog.vue';
import { formatBytes } from '../utils/mediaDetails.js';
import { photoCleanupStore, startPhotoCleanupPolling, stopPhotoCleanupPolling, refreshPhotoCleanupStatus } from '../utils/photoCleanupStore.js';
import {
  PHOTO_GROUP_MONTH, PHOTO_GROUP_NONE, PHOTO_ROW_HEADER,
  buildPhotoLayout, calculatePhotoAnchorScrollTop, calculatePhotoWindow,
  firstVisiblePhotoItemIndex, formatPhotoTimelineLabel, photoTimelineKey, resolvePhotoColumns
} from '../utils/photoGrid.js';
// 只读复用视频列表的滚动宿主解析（.main-view），不修改 virtualList.js。
import { resolveScrollOwnerDescriptor } from '../utils/virtualList.js';

const PAGE_SIZE = 60;
// 网格几何常量。卡片高度固定 = 方形缩略图 + 定高信息条，布局计算才能不依赖实测回写：
// 缩略图区高度由列宽算出（aspect-ratio 1 的等价物），信息条用 CSS 钉死成 CARD_META_HEIGHT。
const GRID_GAP = 12;
const MIN_COLUMN_WIDTH = 180;
const CARD_META_HEIGHT = 86;
// glass-surface 给卡片加了 1px 边框，卡片是 border-box：上下各 1px 要算进行高，
// 缩略图的正方形边长也要相应减去，否则内容比卡片内容盒高 2px 被裁掉。
const CARD_BORDER = 2;
const TIMELINE_HEADER_HEIGHT = 40;
const OVERSCAN_ROWS = 3;
// 视口底边距列表底部小于该值时预取下一页；虚拟化后哨兵元素不再稳定出现在 DOM 里。
const LOAD_MORE_THRESHOLD = 400;

export default {
  name: 'PhotoLibraryPage',
  components: { BaseModal, PhotoCleanupPanel, PhotoTrashDialog },
  props: {
    settings: { type: Object, required: true },
    tags: { type: Array, default: () => [] },
    // 切到别的标签页时本页只是隐藏、不卸载：已加载的图片和滚动位置都要留着。
    pageActive: { type: Boolean, default: true }
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
      imageTags: [],
      scanning: false,
      failedThumbs: {},
      filters: {
        keyword: '',
        tagIDs: [],
        favoriteOnly: false,
        minRating: '',
        maxRating: '',
        takenAfter: '',
        takenBefore: '',
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
      deleting: false,
      timelineMode: false,
      timelineBuckets: {},
      columns: 1,
      mediaHeight: 0,
      scrollOwnerEl: null,
      scrollOwnerMissing: false,
      // 切走前记下的滚动位置，切回来照原样恢复。
      inactiveScrollTop: 0,
      // 切回本页时探到列表首页与已加载的不一致，就提示可刷新（不自动刷，免得丢滚动位置）。
      libraryChanged: false,
      loadMoreQueued: false,
      windowState: { startRow: 0, endRow: 0, topSpacer: 0, bottomSpacer: 0, totalHeight: 0 }
    };
  },
  computed: {
    hasMore() { return !this.exhausted && this.loadedOnce; },
    // 时间线分组只在文件名模式下生效：语义结果按相关度排序，插年月分组头没有意义。
    timelineActive() { return this.timelineMode && this.searchMode !== 'semantic'; },
    // 时间线开启时强制按拍摄时间排序，但不改写用户在下拉里的选择，关掉即恢复。
    effectiveSortMode() { return this.timelineActive ? 'taken' : this.filters.sortMode; },
    sortDisabledReason() {
      if (this.searchMode === 'semantic') return '语义模式按相关度排序';
      if (this.timelineMode) return '时间线分组固定按拍摄时间倒序';
      return '';
    },
    virtualized() { return Boolean(this.scrollOwnerEl) && !this.scrollOwnerMissing; },
    layout() {
      return buildPhotoLayout({
        items: this.images,
        columns: this.columns,
        groupBy: this.timelineActive ? PHOTO_GROUP_MONTH : PHOTO_GROUP_NONE,
        cellHeight: this.mediaHeight + CARD_META_HEIGHT + CARD_BORDER,
        headerHeight: TIMELINE_HEADER_HEIGHT,
        gap: GRID_GAP
      });
    },
    renderedRows() {
      const rows = this.virtualized
        ? this.layout.rows.slice(this.windowState.startRow, this.windowState.endRow)
        : this.layout.rows;
      return rows.map(row => ({
        ...row,
        isHeader: row.kind === PHOTO_ROW_HEADER,
        // startIndex 在同一份布局里逐行递增，分组头带上它才不会在同一个年月出现两次时撞 key
        // （回收站恢复会把照片插到队首，可能造出不连续的同名分组）。
        key: row.kind === PHOTO_ROW_HEADER ? `h:${row.startIndex}:${row.groupKey}` : `c:${row.startIndex}`
      }));
    },
    gridStyle() {
      const style = {
        '--photo-columns': String(this.columns),
        // 行间隙与信息条高度由 JS 常量下发，保证 CSS 与 photoGrid 的布局计算同一份来源。
        '--photo-grid-gap': `${GRID_GAP}px`,
        '--photo-card-meta': `${CARD_META_HEIGHT}px`
      };
      // 尚未测量出宽度时不下发高度变量，让卡片退回 aspect-ratio: 1，避免首帧塌成 0 高。
      if (this.mediaHeight > 0) style['--photo-cell-media'] = `${this.mediaHeight}px`;
      return style;
    },
    semanticAvailable() { return Boolean(this.semanticStatus?.available); },
    semanticNotice() {
      if (this.semanticNoticeOverride) return this.semanticNoticeOverride;
      if (!this.semanticStatus || this.semanticAvailable) return '';
      const reason = String(this.semanticStatus.unavailable || '').trim();
      return `语义搜索不可用：${reason || '图片语义索引能力未就绪'}`;
    },
    viewerImage() { return this.viewerIndex >= 0 ? this.images[this.viewerIndex] || null : null; },
    cleanupRunning() { return !!photoCleanupStore.status?.running; },
    cleanupDone() {
      const s = photoCleanupStore.status;
      return Boolean(!s?.running && s?.completed && s?.analysis);
    },
    // 后台跑完不自动重来，徽标直接报出待审阅组数，提醒去处理。
    cleanupGroupCount() {
      const analysis = photoCleanupStore.status?.analysis;
      return (analysis?.duplicate_groups?.length || 0) + (analysis?.near_duplicate_groups?.length || 0);
    },
    cleanupProgressText() {
      const p = photoCleanupStore.status?.progress || {};
      return p.total > 0 ? `${p.current}/${p.total}` : '…';
    },
    cleanupBadgeTitle() {
      const p = photoCleanupStore.status?.progress || {};
      return `清理分析进行中${p.message ? '：' + p.message : ''}`;
    },
    // 卡片描述：优先取已完成的状态行；仅 Preload 回填（SearchImagePage）的图有值。
    cardDescription() {
      return (image) => {
        const rows = image?.ai_descriptions || [];
        const done = rows.find(r => r.status === 'completed' && String(r.description || '').trim());
        return done ? String(done.description).trim() : '';
      };
    },
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
        || this.filters.minRating !== '' || this.filters.maxRating !== ''
        || this.filters.takenAfter !== '' || this.filters.takenBefore !== '';
    },
    // exifFacts 只列出真正有值的项；全空时整个「拍摄信息」区不渲染。
    exifFacts() {
      const image = this.viewerImage;
      if (!image) return [];
      const camera = [image.camera_make, image.camera_model].map(part => String(part || '').trim()).filter(Boolean).join(' ');
      const facts = [
        { label: '拍摄时间', value: image.taken_at ? this.formatDateTime(image.taken_at) : '' },
        { label: '相机', value: camera },
        { label: '镜头', value: String(image.lens_model || '').trim() },
        { label: 'ISO', value: image.iso ? String(image.iso) : '' },
        { label: '光圈', value: image.f_number ? `f/${Number(image.f_number).toFixed(1)}` : '' },
        { label: '快门', value: image.exposure_time ? `${image.exposure_time} 秒` : '' },
        { label: '焦距', value: image.focal_length ? `${Number(image.focal_length).toFixed(0)} mm` : '' }
      ];
      if (image.gps_latitude != null && image.gps_longitude != null) {
        facts.push({ label: 'GPS', value: `${Number(image.gps_latitude).toFixed(6)}, ${Number(image.gps_longitude).toFixed(6)}` });
      }
      return facts.filter(fact => fact.value);
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
    'filters.maxRating'() { this.reload(); },
    'filters.takenAfter'() { this.reload(); },
    'filters.takenBefore'() { this.reload(); },
    timelineMode() { this.reload(); },
    // 必须侦听长度而不是 images 本身：分页是 push/splice/unshift 原地改数组，
    // Vue 3 的非深度侦听只在整个引用被替换时触发，watch images 会让翻页后的窗口永远不刷新。
    'images.length'() {
      this.$nextTick(() => this.syncWindow());
    },
    // 滚动容器（.main-view）是各页共用的：切走时别的页面会把 scrollTop 改掉，
    // 所以离开前记下位置，切回来再放回去，不用从头往下滑。
    pageActive(active) {
      if (!active) {
        this.inactiveScrollTop = this.scrollOwnerEl?.scrollTop || 0;
        return;
      }
      this.$nextTick(() => this.restoreScrollPosition());
      this.checkLibraryFreshness();
    }
  },
  mounted() {
    window.addEventListener('keydown', this.handleKeydown);
    this.loadImageDirectories();
    this.loadImageTags();
    this.loadSemanticStatus();
    this.reload();
    // 清理审阅状态由本页持续轮询：面板关闭后分析仍在后台跑，徽标可见进度。
    startPhotoCleanupPolling();
    this.$nextTick(() => {
      this.attachResizeObserver();
      this.syncWindow(true);
    });
  },
  beforeUnmount() {
    clearTimeout(this._keywordTimer);
    window.removeEventListener('keydown', this.handleKeydown);
    this.detachScrollOwner();
    this.detachResizeObserver();
    stopPhotoCleanupPolling();
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
    rowImages(row) {
      return this.images.slice(row.startIndex, row.endIndex);
    },
    rowStyle(row) {
      // outerHeight 把行间隙做成行自身的 padding-bottom（border-box），既不引入外边距合并，
      // 又让 topSpacer/bottomSpacer 与前缀和精确对齐。
      return { height: `${row.outerHeight}px`, paddingBottom: `${row.outerHeight - row.height}px` };
    },
    timelineLabel(row) {
      return formatPhotoTimelineLabel(row, this.timelineBuckets[row.groupKey]);
    },
    // ===== 网格虚拟化（AC-15）=====
    resolveScrollOwner() {
      const { nextOwner, sameOwner, missing } = resolveScrollOwnerDescriptor(this.$el, this.scrollOwnerEl);
      this.scrollOwnerMissing = missing;
      if (sameOwner) return;
      this.detachScrollOwner();
      this.scrollOwnerEl = nextOwner;
      // 找不到 .main-view 时退回整列表渲染，并把状态挂到 data-scroll-owner-fallback 上
      // （与 VirtualVideoList 同款可观测标记）。
      if (this.scrollOwnerEl) {
        this.scrollOwnerEl.addEventListener('scroll', this.handleOwnerScroll, { passive: true });
      }
    },
    detachScrollOwner() {
      if (!this.scrollOwnerEl) return;
      this.scrollOwnerEl.removeEventListener('scroll', this.handleOwnerScroll);
      this.scrollOwnerEl = null;
    },
    attachResizeObserver() {
      if (typeof ResizeObserver === 'undefined' || this._resizeObserver) return;
      this._resizeObserver = new ResizeObserver(() => this.handleGridResize());
      this._resizeObserver.observe(this.$el);
    },
    detachResizeObserver() {
      this._resizeObserver?.disconnect();
      this._resizeObserver = null;
    },
    getListTop() {
      const shell = this.$refs.gridShell;
      if (!this.scrollOwnerEl || !shell) return 0;
      const ownerRect = this.scrollOwnerEl.getBoundingClientRect();
      const shellRect = shell.getBoundingClientRect();
      return this.scrollOwnerEl.scrollTop + (shellRect.top - ownerRect.top);
    },
    // measureGrid 只读容器宽度就能定出列数与卡片高度：卡片是定高的，不需要实测回写。
    // 网格未挂载（如 reload 清空列表期间）或还没布局出宽度时保留上一次的测量值，
    // 否则行高会被清成 0，卡片撑出行外互相压盖。
    measureGrid() {
      const width = this.$refs.gridShell?.getBoundingClientRect().width || 0;
      if (width <= 0) return false;
      const columns = resolvePhotoColumns(width, { minColumnWidth: MIN_COLUMN_WIDTH, gap: GRID_GAP });
      const cellWidth = (width - GRID_GAP * (columns - 1)) / columns;
      const mediaHeight = Math.max(0, Math.round(cellWidth) - CARD_BORDER);
      const changed = columns !== this.columns || mediaHeight !== this.mediaHeight;
      this.columns = columns;
      this.mediaHeight = mediaHeight;
      return changed;
    },
    // handleGridResize 在列数/卡片高度变化后重算布局，并把重算前视口顶部那张照片放回视口顶，
    // 避免宽度变化时滚动位置跳回列表开头。
    handleGridResize() {
      // 隐藏时 ResizeObserver 会报 0 宽，按它重算会把布局清空。
      if (!this.pageActive) return;
      const anchorIndex = this.virtualized
        ? firstVisiblePhotoItemIndex(this.layout, this.windowState.startRow)
        : -1;
      const changed = this.measureGrid();
      const willRestoreAnchor = changed && anchorIndex >= 0 && Boolean(this.scrollOwnerEl);
      // 锚点回写前 scrollTop 还停在旧布局的位置，拿它去跟新的（加宽后会变短的）总高比，会误判
      // 成"已经滚到底"而多预取一页；预取判断交给回写之后的那次同步。
      this.syncWindow(false, !willRestoreAnchor);
      if (!willRestoreAnchor) return;
      // 等新布局的占位块渲染出来再改 scrollTop，否则浏览器会按旧的 scrollHeight 把它夹回去。
      this.$nextTick(() => {
        if (!this.scrollOwnerEl) return;
        this.scrollOwnerEl.scrollTop = calculatePhotoAnchorScrollTop({
          layout: this.layout,
          listTop: this.getListTop(),
          itemIndex: anchorIndex
        });
        this.syncWindow();
      });
    },
    handleOwnerScroll() {
      // 本页隐藏时滚动的是别的页面，别拿那个位置去算本页的可视窗口。
      if (!this.pageActive) return;
      this.syncWindow();
    },
    // 切回本页：先按记下的位置回写，再让虚拟化窗口跟上。占位块高度要等窗口重算后才
    // 回到原尺寸，浏览器可能先把 scrollTop 夹小，所以下一帧再校正一次。
    restoreScrollPosition() {
      this.resolveScrollOwner();
      const target = this.inactiveScrollTop;
      if (!this.scrollOwnerEl) return;
      if (target > 0) this.scrollOwnerEl.scrollTop = target;
      this.syncWindow(true);
      if (target <= 0) return;
      const settle = () => {
        if (!this.scrollOwnerEl || !this.pageActive) return;
        if (this.scrollOwnerEl.scrollTop === target) return;
        this.scrollOwnerEl.scrollTop = target;
        this.syncWindow();
      };
      if (typeof requestAnimationFrame === 'function') requestAnimationFrame(settle);
      else settle();
    },
    syncWindow(remeasure = false, allowLoadMore = true) {
      this.resolveScrollOwner();
      if (remeasure || this.mediaHeight === 0) this.measureGrid();
      if (!this.virtualized) {
        this.windowState = { startRow: 0, endRow: this.layout.rows.length, topSpacer: 0, bottomSpacer: 0, totalHeight: this.layout.totalHeight };
        return;
      }
      const listTop = this.getListTop();
      const next = calculatePhotoWindow({
        layout: this.layout,
        scrollTop: this.scrollOwnerEl.scrollTop,
        viewportHeight: this.scrollOwnerEl.clientHeight,
        listTop,
        overscan: OVERSCAN_ROWS
      });
      // 绝大多数滚动帧窗口没有变化，原样赋值会白白触发一次重渲染。
      if (this.windowChanged(next)) this.windowState = next;
      if (allowLoadMore) this.maybeLoadMore(listTop, next.totalHeight);
    },
    windowChanged(next) {
      const current = this.windowState;
      return next.startRow !== current.startRow || next.endRow !== current.endRow
        || next.topSpacer !== current.topSpacer || next.bottomSpacer !== current.bottomSpacer
        || next.totalHeight !== current.totalHeight;
    },
    // maybeLoadMore 取代原来的 IntersectionObserver 哨兵：虚拟化后列表末尾的哨兵元素不再
    // 稳定存在于 DOM 中，改为按滚动位置与列表底部的距离判断。
    maybeLoadMore(listTop, totalHeight) {
      if (!this.hasMore || this.loading || this.loadMoreQueued || !this.scrollOwnerEl) return;
      const viewportBottom = this.scrollOwnerEl.scrollTop + this.scrollOwnerEl.clientHeight;
      if (viewportBottom < listTop + totalHeight - LOAD_MORE_THRESHOLD) return;
      this.loadMoreQueued = true;
      this.loadMore();
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
    // takenBoundary 把 date 输入转成 RFC3339；后端按闭区间比较，所以起点取当天 0 点、
    // 终点取当天最后一毫秒，让"某一天"能整天命中。
    takenBoundary(value, endOfDay) {
      const raw = String(value || '').trim();
      if (!raw) return null;
      const date = new Date(`${raw}T${endOfDay ? '23:59:59.999' : '00:00:00.000'}`);
      return Number.isNaN(date.getTime()) ? null : date.toISOString();
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
        taken_after: this.takenBoundary(this.filters.takenAfter, false),
        taken_before: this.takenBoundary(this.filters.takenBefore, true),
        sort_mode: this.effectiveSortMode
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
    // checkLibraryFreshness 只比对列表首页：拉一页当前筛选下的结果，和已加载的前一页比 id。
    // 不动任何列表状态，只决定要不要亮提示条——自动重新加载会把滚动位置冲掉。
    async checkLibraryFreshness() {
      if (this.libraryChanged || this.loading || !this.loadedOnce) return;
      // 语义搜索是一次性查询结果，没有"库里多了几张"这个概念，跳过。
      if (this.searchMode === 'semantic') return;
      const token = this._queryToken;
      try {
        const page = await SearchImagePage({ filter: this.buildFilter(), limit: PAGE_SIZE });
        if (this._queryToken !== token) return;
        const incoming = (page?.images || []).map(image => Number(image.id));
        const loaded = this.images.slice(0, incoming.length).map(image => Number(image.id));
        if (incoming.length !== loaded.length || incoming.some((id, index) => id !== loaded[index])) {
          this.libraryChanged = true;
        }
      } catch (err) {
        // 探测失败不打扰用户：下次切回来再试。
      }
    },
    async applyLibraryRefresh() {
      this.libraryChanged = false;
      await this.reload();
      if (this.scrollOwnerEl) this.scrollOwnerEl.scrollTop = 0;
      this.inactiveScrollTop = 0;
      this.syncWindow(true);
    },
    async reload() {
      const token = Symbol('photo-query');
      this._queryToken = token;
      this.libraryChanged = false;
      this.images = [];
      // 文件名游标分页与语义 offset 分页是两套状态，切换时一并清空避免串档。
      this.nextCursor = null;
      this.semanticOffset = 0;
      this.semanticScores = {};
      this.semanticCoverage = null;
      this.exhausted = false;
      this.failedThumbs = {};
      this.loadMoreQueued = false;
      this.closeViewer();
      const buckets = this.loadTimelineBuckets(token);
      await this.loadMore(token, true);
      await buckets;
    },
    // loadTimelineBuckets 拉后端的年月计数摘要：分组头要显示整组张数，而前端只加载了当前页，
    // 不能靠已加载条目数冒充总数，也不能为了算分组头去拉全量图片。
    async loadTimelineBuckets(token) {
      if (!this.timelineActive) {
        this.timelineBuckets = {};
        return;
      }
      try {
        const buckets = await ListImageTimelineBuckets(this.buildFilter()) || [];
        if (this._queryToken !== token) return;
        const counts = {};
        buckets.forEach(bucket => {
          counts[`${bucket.year}-${String(bucket.month).padStart(2, '0')}`] = bucket.count;
        });
        this.timelineBuckets = counts;
      } catch (err) {
        if (this._queryToken !== token) return;
        this.timelineBuckets = {};
        this.error = `加载时间线分组失败：${err}`;
      }
    },
    // adjustTimelineBucket 在删除/恢复单张照片后就地增减它所属的年月桶。
    // 不重新调 ListImageTimelineBuckets：那个接口会把全部匹配行读进后端，
    // 为一张照片重扫一遍全表在大图库上代价随库增长。分组键用与后端同一个 photoTimelineKey 定义。
    adjustTimelineBucket(image, delta) {
      if (!this.timelineActive) return;
      const group = photoTimelineKey(image);
      if (!group) return;
      const counts = { ...this.timelineBuckets };
      const next = (counts[group.key] || 0) + delta;
      if (next > 0) counts[group.key] = next;
      else delete counts[group.key];
      this.timelineBuckets = counts;
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
        if (this._queryToken === token) { this.loading = false; this.loadMoreQueued = false; }
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
        this.loadMoreQueued = false;
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
        if (this._queryToken === token) { this.loading = false; this.loadMoreQueued = false; }
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
    async loadImageTags() {
      try {
        this.imageTags = await GetImageTags() || [];
      } catch (err) {
        this.imageTags = [];
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
      if (!this.pageActive) return;
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
    // patchImage 是等长替换，不会触发 'images.length' 侦听，因此不会重算虚拟化窗口。
    // 前提是它合并进来的字段不影响布局：现有接口都不会改写 taken_at/created_at，
    // 所以行数与分组不变。若将来有接口能改拍摄时间，这里要显式补一次 syncWindow。
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
        this.loadImageTags();
      } catch (err) {
        this.error = `添加标签失败：${err}`;
      }
    },
    async removeTag(image, tag) {
      if (!image || !tag) return;
      try {
        await RemoveTagFromImage(image.id, tag.id);
        this.patchImage({ id: image.id, tags: (image.tags || []).filter(item => Number(item.id) !== Number(tag.id)) });
        this.loadImageTags();
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
        // 分组头显示的是后端整组总数，删掉一张后要同步减一，否则计数长期偏大。
        this.adjustTimelineBucket(image, -1);
        // 后端已把清理分析标记为过期，空闲时不轮询，得主动同步一次。
        refreshPhotoCleanupStatus();
      } catch (err) {
        this.error = `删除图片失败：${err}`;
      }
    },
    handleRestored(image) {
      if (!image) return;
      if (!this.images.some(item => Number(item.id) === Number(image.id))) {
        this.images.unshift(image);
        this.adjustTimelineBucket(image, 1);
      }
      this.loadedOnce = true;
      // 恢复回来的图片不该再在清理审阅里显示为"已删除"。
      const restoredID = Number(image.id);
      const review = photoCleanupStore.review;
      if (review.deletedIDs.includes(restoredID)) {
        review.deletedIDs = review.deletedIDs.filter(id => id !== restoredID);
      }
      refreshPhotoCleanupStatus();
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
.photo-cleanup-open-btn { display: inline-flex; align-items: center; gap: 6px; }
.photo-cleanup-badge { padding: 1px 7px; border-radius: 999px; background: var(--control-bg); color: var(--text-secondary); font-size: 10px; white-space: nowrap; }
.photo-cleanup-badge--done { background: var(--primary-color, #0d9488); color: #fff; }
.photo-toolbar__keyword { max-width: 240px; }
.photo-toolbar__sort { width: auto; min-width: 120px; }
.photo-toolbar__favorite { display: inline-flex; align-items: center; gap: 6px; color: var(--text-primary); font-size: 13px; white-space: nowrap; cursor: pointer; }
.photo-toolbar__rating { display: inline-flex; align-items: center; gap: 6px; color: var(--text-muted); font-size: 12px; }
.photo-toolbar__rating .number-input { width: 76px; }
.photo-toolbar__taken { display: inline-flex; align-items: center; gap: 6px; color: var(--text-muted); font-size: 12px; }
.photo-toolbar__timeline { display: inline-flex; align-items: center; gap: 6px; color: var(--text-primary); font-size: 13px; white-space: nowrap; cursor: pointer; }
.photo-toolbar__timeline input:disabled { cursor: not-allowed; }
.photo-toolbar__date { width: 148px; }
.photo-toolbar__date:disabled { opacity: 0.45; cursor: not-allowed; }
.photo-toolbar__tags { display: flex; flex-wrap: wrap; gap: 6px; }
.photo-search-mode { display: inline-flex; padding: 2px; border: 1px solid var(--border-color); border-radius: 999px; background: var(--control-bg); }
.photo-search-mode__btn { padding: 4px 12px; border: 0; border-radius: 999px; background: transparent; color: var(--text-secondary); font-size: 12px; cursor: pointer; }
.photo-search-mode__btn.active { background: var(--panel-bg); color: var(--text-primary); }
.photo-search-mode__btn:disabled { opacity: 0.45; cursor: not-allowed; }
.photo-toolbar__semantic-notice { margin: 0; color: var(--text-muted); font-size: 12px; }
.photo-library__refresh { display: flex; align-items: center; gap: 10px; margin: 0 0 10px; padding: 8px 12px; border: 1px solid var(--primary-color, #0d9488); border-radius: 10px; background: var(--control-bg); color: var(--text-primary); font-size: 12px; }
.photo-library__refresh span { flex: 1; min-width: 0; }
.photo-library__refresh-dismiss { flex: none; width: 22px; height: 22px; padding: 0; border: 0; border-radius: 999px; background: transparent; color: var(--text-muted); font-size: 15px; line-height: 1; cursor: pointer; }
.photo-library__refresh-dismiss:hover { background: var(--border-color); color: var(--text-primary); }
.photo-library__error { margin: 0; color: var(--danger-color); }

/* 虚拟化网格：外层只负责堆叠"行"，列数由 --photo-columns 显式给出（而不是 auto-fill），
   保证 DOM 布局与 photoGrid.js 的窗口计算用的是同一个列数。 */
.photo-grid { display: block; }
.photo-grid-row { display: grid; grid-template-columns: repeat(var(--photo-columns, 1), minmax(0, 1fr)); gap: var(--photo-grid-gap, 12px); box-sizing: border-box; }
.photo-timeline-header { display: flex; align-items: flex-end; overflow: hidden; box-sizing: border-box; }
.photo-timeline-header h3 { margin: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-secondary); font-size: 13px; font-weight: 600; letter-spacing: 0.2px; }
.photo-card { position: relative; overflow: hidden; border-radius: 13px; }
/* 定高卡片：缩略图区高度由列宽算出（等价于 aspect-ratio 1），信息条固定 52px，
   两者相加即 photoGrid 的 cellHeight，布局无需实测回写。 */
.photo-card__media { display: block; width: 100%; height: var(--photo-cell-media, auto); aspect-ratio: 1; padding: 0; border: 0; background: var(--thumb-bg); cursor: pointer; }
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
.photo-card__meta { display: grid; align-content: start; gap: 2px; height: var(--photo-card-meta, 86px); box-sizing: border-box; overflow: hidden; padding: 9px 11px 10px; }
.photo-card__meta span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; color: var(--text-primary); }
.photo-card__meta small { color: var(--text-muted); font-size: 11px; }
.photo-card__description {
  margin: 2px 0 0;
  color: var(--text-secondary);
  font-size: 11px;
  line-height: 1.4;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

.photo-empty { padding: 48px 16px; text-align: center; color: var(--text-muted); }
.photo-empty h3 { margin: 0 0 8px; color: var(--text-primary); }
.photo-empty p { margin: 0 0 16px; font-size: 13px; }
.photo-empty__actions { display: flex; justify-content: center; gap: 10px; }
.photo-library__more { align-self: center; }

/* grid-template-rows 必须显式给一行确定高度：隐式行是 auto，img 的 max-height:100%
   会因为父级高度不确定而失效，竖图就按原始尺寸撑出视口显示不全。 */
.photo-viewer { position: fixed; inset: 0; z-index: 200; display: grid; grid-template-columns: minmax(0, 1fr) min(360px, 38vw); grid-template-rows: minmax(0, 1fr); background: rgba(8, 12, 20, 0.86); }
.photo-viewer__stage { position: relative; display: flex; align-items: center; justify-content: center; min-width: 0; min-height: 0; overflow: hidden; padding: 48px 56px; }
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
