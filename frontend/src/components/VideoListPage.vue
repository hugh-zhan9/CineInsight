<template>
  <div
    :class="['page-content', { 'page-content--with-preview': previewOpen }]"
    @wheel="forwardWheelToScrollOwner"
  >
    <LibraryToolbar
      ref="libraryToolbar"
      :tags="tags"
      :videos="videos"
      :selected-video-ids="selectedVideoIds"
      :search-keyword="searchKeyword"
      :search-mode="searchMode"
      :smart-view="smartView"
      :sort-mode="sortMode"
      :view-mode="viewMode"
      :row-density="rowDensity"
      :selected-tags="selectedTags"
      :selected-size-range="selectedSizeRange"
      :selected-res-range="selectedResRange"
      :min-rating="minRating"
      :max-rating="maxRating"
      :size-options="sizeOptions"
      :res-options="resOptions"
      :saved-views="savedViews"
      :selected-saved-view-i-d="selectedSavedViewID"
      :random-mode="randomMode"
      :random-pick-loading="randomPick.loading"
      :random-pick-size="randomPickSize"
      :semantic-available="semanticAvailable"
      :semantic-unavailable-notice="semanticUnavailableNotice"
      :filtered-count="filteredCount"
      :library-total-count="libraryTotalCount"
      :has-more="hasMore"
      :migration-running="migrationRunning"
      :incremental-scan="incrementalScan"
      :directories="directories"
      :settings="settings"
      :ai-tag-summary="aiTagSummary"
      :cleanup-badge-count="cleanupBadgeCount"
      :cleanup-analyzing="cleanupAnalyzing"
      :technical-backfill="technicalBackfill"
      :perceptual-hash="perceptualHash"
      :local-metadata-backfill="localMetadataBackfill"
      :local-metadata-export="localMetadataExport"
      :playback-proxy="playbackProxy"
      :tag-bg-color="tagBgColor"
      :library-filter-from="libraryFilterFrom"
      @update:search-keyword="searchKeyword = $event"
      @update:smart-view="smartView = $event"
      @update:sort-mode="sortMode = $event"
      @update:view-mode="viewMode = $event"
      @update:row-density="rowDensity = $event"
      @search="handleSearch"
      @set-search-mode="setSearchMode"
      @play-random="playRandom"
      @toggle-tag="toggleTagFilter"
      @clear-tags="clearTagFilter"
      @delete-tag="requestDeleteTag"
      @open-tag-manager="showTagManagerDialog = true"
      @toggle-select-all="toggleSelectAllVisible"
      @clear-selection="clearSelection"
      @clear-conditions="clearAllConditions"
      @apply-filter="applyFilterConditions"
      @open-save-view="openSaveViewDialog"
      @manage-select="onManageSelect"
      @view-select="onViewSelect"
      @random-select="onRandomSelect"
      @batch-add-tag="openBatchAddTagDialog"
      @batch-move="moveSelectedVideos"
      @batch-local-metadata="openLocalMetadataDialog(selectedVideoIds)"
      @batch-playback-proxy="createProxiesForSelected"
      @batch-delete="confirmBatchDelete"
    />
    <IncrementalScanBar
      ref="incrementalScanBar"
      :migration-running="migrationRunning"
      :directories="directories"
      :reload-view="reloadCurrentView"
      @reload-directories="$emit('reload-directories')"
      @state-change="incrementalScan = $event"
    />

    <BackgroundTaskStatusBars
      ref="taskBars"
      @state-change="applyBackgroundTaskState"
      @library-changed="reloadCurrentView"
    />
    <SemanticNoticeBar
      :semantic-available="semanticAvailable"
      :semantic-unavailable-notice="semanticUnavailableNotice"
      :search-mode="searchMode"
      :semantic-search-error="semanticSearchError"
      :semantic-coverage="semanticCoverage"
    />

    <TrashUndoBanner
      ref="trashUndo"
      :after-restore="afterTrashRestore"
    />

    <SubtitleGenerateDialog
      ref="subtitleTasks"
      @generating-change="generatingSubtitleIds = $event"
    />

    <RandomPickBanner
      :random-pick="randomPick"
      :random-pick-size="randomPickSize"
      :video-count="videos.length"
      @reshuffle="reshuffleRandomPick"
      @exit="exitRandomPick"
    />

    <div class="video-list" ref="videoList">
      <div v-if="videos.length === 0 && !loading" class="empty-state">
        <p>暂无视频，点击"扫描新目录"开始导入视频</p>
      </div>
      <VirtualVideoList
        v-else-if="videos.length > 0"
        ref="virtualList"
        :items="videos"
        :loading="loading"
        :has-more="hasMore"
        :virtualization-enabled="homeListVirtualizationEnabled && viewMode === 'list'"
        :layout-mode="viewMode"
        :subtitle-mode="isSubtitleSearchActive()"
        :preview-open="previewOpen"
        :query-key="virtualListQueryKey"
        :estimate-height="estimateVideoHeight"
        :item-version="videoVisualVersion"
        :range-engine="rangeEngine"
        @load-more="loadVideos"
      >
        <template #default="{ item: video }">
          <VideoListRow
            :video="video"
            :directories="directories"
            :selected="isVideoSelected(video.id)"
            :keyboard-focused="Number(video.id) === Number(keyboardFocusVideoID)"
            :layout-mode="viewMode"
            :density="rowDensity"
            :narrow="previewOpen && viewMode === 'list'"
            :actions-suspended="selectedVideoIds.length > 0"
            @preview="openPreview"
            @play="playVideo"
            @toggle-favorite="toggleVideoFavorite"
            @toggle-watched="toggleVideoWatched"
            @open-add-tag="openAddTagDialog"
            @remove-tag="removeTag"
            @toggle-select="toggleVideoSelection"
            @open-row-menu="openRowMenu"
            @contextmenu="showContextMenu"
          />
        </template>
      </VirtualVideoList>

      <!-- 加载更多指示器 -->
      <div v-if="loading" class="loading-indicator">
        <p>加载中...</p>
      </div>
      <div v-if="!hasMore && videos.length > 0" class="no-more-indicator">
        <p>没有更多视频了</p>
      </div>
    </div>

    <PreviewDrawer
      v-if="previewOpen && selectedPreviewVideo"
      :page-active="pageActive"
      :video="selectedPreviewVideo"
      :session="previewSession"
      :start-time-ms="previewStartTimeMs"
      :resume-position-seconds="resumePositionFor(selectedPreviewVideo)"
      @close="closePreview"
      @preview-externally="previewExternally"
      @watch-progress="handlePreviewWatchProgress"
      @details-updated="handleVideoDetailsUpdated"
      @open-local-metadata="openLocalMetadataDialog([$event.id])"
	  @export-local-metadata="exportLocalMetadataNFO"
	  @enhance="openEnhanceDialog"
	  @find-similar="findSimilarVideos"
	  @shortcut="handlePreviewShortcut"
	  @preview-session-stale="refreshPreviewSession"
    />

    <SubtitleWorkbench
      v-if="subtitleWorkbench.show && subtitleWorkbench.video"
      :video="subtitleWorkbench.video"
      @close="closeSubtitleWorkbench"
      @saved="handleSubtitleWorkbenchSaved"
    />

    <LocalMetadataDialog
      :visible="localMetadataDialog.show"
      :video-ids="localMetadataDialog.videoIds"
      @close="localMetadataDialog = { show: false, videoIds: [] }"
      @applied="handleLocalMetadataApplied"
    />

    <!-- 行内 ⋯ 与右键菜单共用同一份菜单项定义，只是两个入口 -->
    <BaseMenu
      v-if="rowMenu.video"
      :anchor="rowMenu.anchor"
      :position="rowMenu.position"
      :items="rowMenuItems"
      :min-width="200"
      align="end"
      label="视频操作"
      @select="onRowMenuSelect"
      @close="closeRowMenu"
    />

    <!-- 视频超分 -->
    <EnhanceDialog ref="enhanceDialog" />

    <RenameDialogs
      ref="renameDialogs"
      :migration-running="migrationRunning"
      :reload-view="reloadCurrentView"
      @update:migration-running="migrationRunning = $event"
      @video-renamed="applyVideoRename"
      @reload-directories="$emit('reload-directories')"
      @update-settings="$emit('update-settings', $event)"
    />

    <SaveViewDialog
      ref="saveViewDialog"
      :current-library-filter="currentLibraryFilter"
      :after-saved="afterSavedLibraryView"
    />

    <!-- 弹窗组件 -->
    <ScanDialog
      :visible="showScanDialog"
      :directories="directories"
      :settings="settings"
      @close="showScanDialog = false"
      @scan-complete="handleScanComplete"
    />

    <TagManagerDialog
      :visible="showTagManagerDialog"
      :tags="tags"
      @close="showTagManagerDialog = false"
      @tags-changed="handleTagsChanged"
      @request-delete-tag="requestDeleteTag"
    />

    <AddTagDialog
      :visible="addTagDialog.show"
      :video="addTagDialog.video"
      :video-ids="addTagDialog.videoIds"
      :selected-videos="selectedBatchVideos"
      :mode="addTagDialog.mode"
      :tags="tags"
      @close="addTagDialog.show = false"
      @tag-added="handleTagAdded"
    />

    <DeleteConfirmDialog
      :visible="deleteDialog.show"
      :video="deleteDialog.video"
      :video-count="deleteDialog.videoIds.length"
      :settings="settings"
      @close="deleteDialog.show = false"
      @confirm-delete="executeDelete"
    />

    <TagDeleteDialog
      :visible="tagDeleteDialog.show"
      :tag="tagDeleteDialog.tag"
      @close="tagDeleteDialog.show = false"
      @confirm-delete="confirmDeleteTag"
    />

    <AITagReviewDialog
      :visible="aiTagReviewDialog.show"
      :tags="tags"
      :quality-enabled="settings.ai_quality_enabled"
      @close="closeAITagReviewDialog"
      @changed="handleAITagCandidatesChanged"
    />

    <CleanupReviewPanel
      ref="cleanupPanel"
      :perceptual-hash-running="perceptualHash.running"
      :frame-hash-running="frameHash.running"
      :trash-videos="trashCleanupVideos"
      :after-trash-videos="afterTrashCleanupVideos"
      @badge-change="cleanupBadgeCount = $event"
      @analyzing-change="cleanupAnalyzing = $event"
      @start-perceptual-hash="startPerceptualHashBackfill"
      @start-frame-hash="startFrameHashBackfill"
      @same-source-rejected="refreshAITagSummary"
      @trash-settled="handleCleanupTrashSettled"
    />

    <SubtitlePreviewModal
      ref="subtitlePreview"
      :search-keyword="searchKeyword"
      :search-mode="searchMode"
    />

  </div>
</template>

<style scoped>

.btn-compact {
  height: 28px;
  padding: 0 10px;
  font-size: 12px;
}
.video-subtitle-hit {
  display: block;
  width: 100%;
  margin: 8px 0 0;
  padding: 0;
  border: 0;
  background: transparent;
  color: var(--accent-deep);
  cursor: pointer;
  font-size: 13px;
  text-align: left;
}
.video-stale {
  color: var(--warning-strong);
  font-weight: 600;
}

</style>

<script>
import { SearchLibraryVideoPage, CountLibraryVideos, GetSemanticIndexStatus, SearchSemanticVideos, FindSimilarVideos, ListRecentlyPlayedWithFilter, GetLibrarySubtitleHits, PlayVideo, PlayRandomVideoWithFilter, PickRandomVideos, GetVideosByIDs, SetVideoFavorite, SetVideoWatched, UpdateVideoWatchProgress, ListSavedLibraryViews, DeleteSavedLibraryView, OpenDirectory, DeleteVideo, BatchDeleteVideos, RemoveTagFromVideo, UpdateSettings, MoveVideo, BatchMoveVideos, MoveDirectory, SelectMigrationSourceDirectory, SelectMigrationDestinationDirectory, GetAITaggingStatusSummary, GetPreviewSession, PreviewExternally, CreatePlaybackProxy, BatchCreatePlaybackProxies, BatchCreatePlaybackProxiesForFilter } from '../../wailsjs/go/main/App';
import ScanDialog from './ScanDialog.vue';
import TagManagerDialog from './TagManagerDialog.vue';
import AddTagDialog from './AddTagDialog.vue';
import DeleteConfirmDialog from './DeleteConfirmDialog.vue';
import TagDeleteDialog from './TagDeleteDialog.vue';
import PreviewDrawer from './PreviewDrawer.vue';
import SubtitleWorkbench from './SubtitleWorkbench.vue';
import VirtualVideoList from './VirtualVideoList.vue';
import VideoListRow from './VideoListRow.vue';
import AITagReviewDialog from './AITagReviewDialog.vue';
import LocalMetadataDialog from './LocalMetadataDialog.vue';
import BackgroundTaskStatusBars from './video-list/BackgroundTaskStatusBars.vue';
import LibraryToolbar from './video-list/LibraryToolbar.vue';
import IncrementalScanBar from './video-list/IncrementalScanBar.vue';
import RandomPickBanner from './video-list/RandomPickBanner.vue';
import RenameDialogs from './video-list/RenameDialogs.vue';
import SemanticNoticeBar from './video-list/SemanticNoticeBar.vue';
import TrashUndoBanner from './video-list/TrashUndoBanner.vue';
import { wheelForwardingMixin } from './video-list/wheelForwarding.js';
import SaveViewDialog from './video-list/SaveViewDialog.vue';
import SubtitlePreviewModal from './video-list/SubtitlePreviewModal.vue';
import CleanupReviewPanel from './video-list/CleanupReviewPanel.vue';
import SubtitleGenerateDialog from './video-list/SubtitleGenerateDialog.vue';
import EnhanceDialog from './video-list/EnhanceDialog.vue';
import { logFrontend } from '../utils/frontendLog.js';
import { defaultRangeEngine, estimateVideoRowHeight } from '../utils/virtualList.js';
import BaseMenu from './ui/BaseMenu.vue';
import { patchVideoFromDetails } from '../utils/mediaDetails.js';
import { shortcutActionForEvent } from '../utils/keyboardShortcuts.js';
import { registerCommands, unregisterCommands } from '../utils/commandRegistry.js';
import { confirmAction, notify, notifyError } from '../utils/feedback.js';
import { PLAYBACK_PROXY_CODE_LABELS, playbackProxyBatchSummary } from '../utils/playbackProxy.js';

// 「随机 N 部」一次抽取的条数。
const RANDOM_PICK_SIZE = 10;

export default {
  name: 'VideoListPage',
  mixins: [wheelForwardingMixin],
  components: { ScanDialog, TagManagerDialog, AddTagDialog, DeleteConfirmDialog, TagDeleteDialog, PreviewDrawer, SubtitleWorkbench, LocalMetadataDialog, VirtualVideoList, VideoListRow, AITagReviewDialog, BackgroundTaskStatusBars, IncrementalScanBar, LibraryToolbar, RandomPickBanner, SemanticNoticeBar, TrashUndoBanner, CleanupReviewPanel, RenameDialogs, SaveViewDialog, SubtitleGenerateDialog, SubtitlePreviewModal, EnhanceDialog, BaseMenu },
  props: {
    tags: { type: Array, default: () => [] },
    settings: { type: Object, required: true },
    directories: { type: Array, default: () => [] },
    pageActive: { type: Boolean, default: true }
  },
  emits: ['reload-tags', 'update-settings', 'reload-directories'],
  data() {
    return {
      videos: [],
      viewMode: window.localStorage?.getItem('cineinsight-library-layout') === 'grid' ? 'grid' : 'list',
      rowDensity: window.localStorage?.getItem('cineinsight-library-density') === 'comfortable' ? 'comfortable' : 'compact',
      searchKeyword: '',
      searchMode: 'file',
      semanticSimilarVideoID: 0,
      semanticCoverage: null,
      semanticSearchError: '',
      semanticStatus: null,
      smartView: '',
      savedViews: [],
      selectedSavedViewID: 0,
      filteredCount: null,
      libraryTotalCount: null,
      countToken: 0,
      randomMode: 'balanced',
      recentRandomVideoIDs: [],
      randomPick: { active: false, ids: [], reason: '', loading: false },
      randomPickToken: 0,
      selectedTags: [],
      selectedSizeRange: 'all',
      selectedResRange: 'all',
      minRating: '',
      maxRating: '',
      sortMode: 'balanced',
      sizeOptions: [
        { label: '0-10M', value: { min: 0, max: 10 * 1024 * 1024 } },
        { label: '10M-100M', value: { min: 10 * 1024 * 1024, max: 100 * 1024 * 1024 } },
        { label: '100M-1G', value: { min: 100 * 1024 * 1024, max: 1024 * 1024 * 1024 } },
        { label: '1G-2G', value: { min: 1024 * 1024 * 1024, max: 2 * 1024 * 1024 * 1024 } },
        { label: '2G-4G', value: { min: 2 * 1024 * 1024 * 1024, max: 4 * 1024 * 1024 * 1024 } },
        { label: '4G-10G', value: { min: 4 * 1024 * 1024 * 1024, max: 10 * 1024 * 1024 * 1024 } },
        { label: '>=10G', value: { min: 10 * 1024 * 1024 * 1024, max: 0 } }
      ],
      resOptions: [
        { label: '480P以下', value: { min: 0, max: 479 } },
        { label: '480P-720P', value: { min: 480, max: 719 } },
        { label: '720P-1080P', value: { min: 720, max: 1079 } },
        { label: '1080P-2k', value: { min: 1080, max: 1439 } },
        { label: '2k-4k', value: { min: 1440, max: 2159 } },
        { label: '4k以上', value: { min: 2160, max: 0 } }
      ],
      cursorScore: 0,
      cursorSize: 0,
      cursorID: 0,
      cursorLastPlayedAt: '',
      cursorRecentPlayedID: 0,
      libraryCursor: null,
      pageSize: 20,
      loading: false,
      hasMore: true,
      rowMenu: { video: null, anchor: null, position: null },
      showScanDialog: false,
      // 增量扫描状态条自己持有真值；这里的镜像给「管理」菜单文案与 ⌘R 用。
      incrementalScan: { running: false, state: 'idle', message: '' },
      migrationRunning: false,
      showTagManagerDialog: false,
      reloadRequested: false,
      reloadPromise: null,
      loadIdleResolvers: [],
      addTagDialog: { show: false, video: null, videoIds: [], mode: 'single' },
      selectedVideoIds: [],
	  keyboardFocusVideoID: 0,
      deleteDialog: { show: false, video: null, videoIds: [] },
      deletingIds: [],
      tagDeleteDialog: { show: false, tag: null },
      aiTagReviewDialog: { show: false, dirty: false },
      aiTagSummary: { same_source_unread: 0 },
      aiTagSummaryTimer: null,
      // 四个后台任务的状态条已抽成 BackgroundTaskStatusBars，这里保留一份镜像给「管理」菜单文案。
      technicalBackfill: { running: false, preparing: false, cancelled: false, completed: false, total: 0, processed: 0, succeeded: 0, skipped: 0, failed: 0, failures: [] },
      perceptualHash: { running: false, cancelled: false, completed: false, total: 0, processed: 0, succeeded: 0, skipped: 0, failed: 0, failures: [] },
      frameHash: { running: false, preparing: false, cancelled: false, completed: false, total: 0, processed: 0, succeeded: 0, skipped: 0, failed: 0, failures: [] },
      localMetadataBackfill: { running: false, cancelled: false, completed: false, total: 0, processed: 0, succeeded: 0, skipped: 0, failed: 0, failures: [] },
	  localMetadataExport: { running: false, cancelled: false, completed: false, total: 0, processed: 0, succeeded: 0, failed: 0, failures: [] },
      // 播放代理任务状态（D-006）：管理菜单项要显示进度，跑完再提示一次结果。
      playbackProxy: { running: false, cancelled: false, completed: false, total: 0, processed: 0, succeeded: 0, skipped: 0, failed: 0, results: [] },
      localMetadataDialog: { show: false, videoIds: [] },
      // 切走前记下共用滚动容器的位置，切回来照原样恢复（图片页也在用同一个容器）。
      inactiveScrollTop: 0,
      // 清理审阅面板自己持有分析结果；这两项是管理菜单徽标与文案要用的镜像。
      cleanupBadgeCount: 0,
      cleanupAnalyzing: false,
      subtitleWorkbench: { show: false, video: null },
      selectedPreviewVideoId: null,
      previewVideoSnapshot: null,
      previewOpen: false,
      previewSession: null,
      previewStartTimeMs: null,
      rangeEngine: defaultRangeEngine,
      homeListVirtualizationEnabled: true,
      // 行菜单要知道哪些视频正在生成字幕；值由字幕任务组件镜像过来。
      generatingSubtitleIds: [],
      runtimeOffHandlers: [],
      searchDebounceTimer: null,
    };
  },
  mounted() {
    this.configureHomeListVirtualization();
    this.loadVideos();
    this.refreshLibraryCounts();
    this.loadSemanticStatus();
    this.loadSavedLibraryViews();
    this.refreshAITagSummary();
    this.aiTagSummaryTimer = window.setInterval(this.refreshAITagSummary, 60000);
    this.attachWheelFallback();
	window.addEventListener('keydown', this.handleLibraryShortcut);
	window.addEventListener('keydown', this.handleToolbarShortcut);
    // 命令面板的本页动作（D-029）。智能视图与保存视图的导航项由 App 注册，
    // 执行时回调下面两个方法，走的还是工具栏那条路径。
    registerCommands('video-list', [
      {
        id: 'action:scan-new',
        group: 'action',
        label: '扫描新目录',
        keywords: ['scan', '扫描'],
        enabled: () => !this.migrationRunning,
        run: () => { this.showScanDialog = true; }
      },
      {
        id: 'action:random-play',
        group: 'action',
        label: '随机播放',
        keywords: ['random', '随机'],
        enabled: () => !this.randomPick.loading,
        run: () => this.playRandom()
      }
    ]);

    if (window.runtime?.EventsOn) {
      // IINA 退出播放时写断点，后端同步完发这个事件。这里只就地改受影响的那几行，
      // 不整表重载：重载会按当前排序重新打分，刚看完的视频会跳到别的位置，
      // 用户反而找不到自己刚才在看哪个。进度条是行内状态，改完当场就更新。
      this.registerRuntimeEvent('iina-progress-synced', (result) => {
        this.applyWatchProgressUpdates(result?.changes);
      });

      this.registerRuntimeEvent('playback-proxy-state', (status) => {
        if (!status) return;
        const wasRunning = this.playbackProxy.running;
        this.playbackProxy = { ...this.playbackProxy, ...status };
        // 只在"刚刚从运行变成不运行"这一刻提示一次，别每条结果都弹。
        if (wasRunning && !status.running && !status.cancelled) {
          notify(playbackProxyBatchSummary(status));
        }
      });

      this.registerRuntimeEvent('library-watcher-reconciled', (event) => {
        const result = event?.result;
        if (!result || result.error_count > 0 || result.added > 0 || result.relocated > 0 || result.stale > 0 || result.metadata_refreshed > 0) {
          this.reloadCurrentView();
        }
      });

    }
  },
  watch: {
    // .main-view 是各页共用的滚动容器：切走时别的页面会改掉 scrollTop，
    // 所以离开前记下位置，切回来再放回去。
    pageActive(active) {
      const owner = this.$el?.closest?.('.main-view');
      if (!owner) return;
      if (!active) {
        this.inactiveScrollTop = owner.scrollTop || 0;
        return;
      }
      const target = this.inactiveScrollTop;
      if (target <= 0) return;
      this.$nextTick(() => {
        const el = this.$el?.closest?.('.main-view');
        if (!el) return;
        el.scrollTop = target;
      });
    },
    directories: {
      // 扫描范围变了就重载：列表只展示扫描根之内的视频，范围一变当前这页就过期了。
      // 只比路径集合——别名变化不影响范围，父组件首次赋值也不该触发一次白重载。
      handler(next, previous) {
        const key = dirs => (dirs || []).map(dir => String(dir?.path || '')).sort().join('\n');
        if (key(next) === key(previous)) return;
        this.reloadCurrentView();
      },
      deep: true
    }
  },
  beforeUnmount() {
    unregisterCommands('video-list');
	window.removeEventListener('keydown', this.handleLibraryShortcut);
	window.removeEventListener('keydown', this.handleToolbarShortcut);
    this.detachWheelFallback();
    if (this.searchDebounceTimer) {
      clearTimeout(this.searchDebounceTimer);
    }
    if (this.aiTagSummaryTimer) {
      clearInterval(this.aiTagSummaryTimer);
    }
    this.teardownRuntimeEvents();
  },
  computed: {
    randomPickSize() {
      return RANDOM_PICK_SIZE;
    },
    // 能力不可用时把「语义」入口置灰并说明原因，而不是让用户搜完看到一个空结果——
    // 空结果会被读成"库里没有匹配内容"，而不是"这个能力现在用不了"。
    // SQLite 后端下语义检索依赖的 pgvector 不存在，这是最常见的触发。
    semanticAvailable() {
      // 状态尚未取回时不预判：默认可用，避免启动瞬间入口闪一下灰。
      if (!this.semanticStatus) return true;
      return Boolean(this.semanticStatus.available);
    },
    semanticUnavailableNotice() {
      if (!this.semanticStatus || this.semanticStatus.available) return '';
      const reason = String(this.semanticStatus.unavailable || '').trim();
      return `语义搜索不可用：${reason || '语义索引能力未就绪'}`;
    },
    // 行内 ⋯ 与右键菜单共用这一份。原型只画了七项，但「写出 NFO」和「视频超分」
    // 此前只有右键菜单这一个入口，合并后不能把它们弄丢。
    rowMenuItems() {
      const video = this.rowMenu.video;
      if (!video) return [];
      const generating = this.generatingSubtitleIds.includes(video.id);
      return [
        { heading: '文件' },
        { id: 'directory', label: '打开目录' },
        { id: 'rename', label: '重命名' },
        { id: 'move', label: '迁移', disabled: this.migrationRunning },
        { id: 'export-nfo', label: '写出 NFO' },
        { heading: '字幕' },
        { id: 'subtitle', label: generating ? '生成字幕（进行中）' : '生成字幕', disabled: generating },
        { id: 'subtitle-edit', label: '编辑字幕' },
        { id: 'subtitle-preview', label: '预览字幕' },
        { heading: '增强' },
        { id: 'enhance', label: '视频超分…' },
        { id: 'playback-proxy', label: '生成播放代理', disabled: this.playbackProxy.running },
        { divider: true },
        { id: 'delete', label: '删除', danger: true, disabled: this.deletingIds.includes(video.id) }
      ];
    },
    selectedBatchVideos() {
      if (!this.addTagDialog.show || this.addTagDialog.mode !== 'batch') return [];
      const ids = new Set(this.addTagDialog.videoIds);
      return this.videos.filter(video => ids.has(video.id));
    },
    selectedPreviewVideo() {
      if (!this.selectedPreviewVideoId) return null;
      return this.videos.find(video => video.id === this.selectedPreviewVideoId) || this.previewVideoSnapshot;
    },
    allVisibleSelected() {
      const ids = this.videos.map(video => video.id);
      return ids.length > 0 && ids.every(id => this.selectedVideoIds.includes(id));
    },
    virtualListQueryKey() {
      return JSON.stringify({
        randomPick: this.randomPick.active ? this.randomPick.ids.join(',') : '',
        mode: this.searchMode,
        keyword: this.currentQueryKeyword(),
        similarVideoID: this.semanticSimilarVideoID,
        smartView: this.smartView,
        tags: [...this.selectedTags].sort((a, b) => a - b),
        size: this.selectedSizeRange === 'all' ? 'all' : `${this.selectedSizeRange.min}:${this.selectedSizeRange.max}`,
        res: this.selectedResRange === 'all' ? 'all' : `${this.selectedResRange.min}:${this.selectedResRange.max}`,
        rating: `${this.minRating}:${this.maxRating}:${this.sortMode}`
      });
    }
  },
  methods: {
	// 管理菜单里印了 ⇧⌘N / ⌘R / ⌘T / ⌘K，搜索框上印了 ⌘F —— 印了就得真的能按，
	// 否则那些提示是在骗人。这些走独立处理器，不受列表焦点限制。
	handleToolbarShortcut(event) {
	  if (!this.pageActive || !(event.metaKey || event.ctrlKey)) return;
	  const key = event.key.toLowerCase();
	  if (key === 'f') {
	    event.preventDefault();
	    this.$refs.libraryToolbar?.focusSearch();
	    return;
	  }
	  if (document.querySelector('[role="dialog"]')) return;
	  if (key === 'n' && event.shiftKey) {
	    event.preventDefault();
	    if (!this.migrationRunning) this.showScanDialog = true;
	  } else if (key === 'r' && !event.shiftKey) {
	    event.preventDefault();
	    if (!this.migrationRunning && !this.incrementalScan.running && this.directories.length > 0) this.runIncrementalScan();
	  } else if (key === 't' && !event.shiftKey) {
	    event.preventDefault();
	    this.openAITagReviewDialog();
	  } else if (key === 'k' && !event.shiftKey) {
	    event.preventDefault();
	    this.openCleanupDialog();
	  }
	},
	handleLibraryShortcut(event) {
	  if (!this.pageActive || this.previewOpen || this.rowMenu.video || document.querySelector('[role="dialog"]')) return;
	  if (event.key === 'Escape' && this.selectedVideoIds.length > 0) {
	    event.preventDefault();
	    this.clearSelection();
	    return;
	  }
	  const action = shortcutActionForEvent(event);
	  if (!action) return;
	  event.preventDefault();
	  if (action === 'next' || action === 'previous') {
		this.moveKeyboardFocus(action === 'next' ? 1 : -1, false);
		return;
	  }
	  const video = this.keyboardFocusedVideo();
	  if (video) this.applyReviewShortcut(action, video);
	},
	keyboardFocusedVideo(allowFirstRowFallback = false) {
	  if (!this.videos.length) return null;
	  const selectedID = Number(this.keyboardFocusVideoID || this.selectedVideoIds.at(-1) || 0);
	  const found = this.videos.find(video => Number(video.id) === selectedID) || null;
	  // 动作键（收藏/已看/播放等）不允许在没有可见焦点时落到第一行；
	  // 只有 J/K 导航可以从第一行开始建立焦点。
	  return found || (allowFirstRowFallback ? this.videos[0] : null);
	},
	moveKeyboardFocus(delta, openPreview) {
	  if (!this.videos.length) return;
	  const current = this.keyboardFocusedVideo(true);
	  const hadFocus = !!(this.keyboardFocusVideoID || this.selectedVideoIds.length);
	  const currentIndex = Math.max(0, this.videos.findIndex(video => video.id === current?.id));
	  const nextIndex = hadFocus
		? Math.max(0, Math.min(this.videos.length - 1, currentIndex + delta))
		: currentIndex;
	  const next = this.videos[nextIndex];
	  this.keyboardFocusVideoID = next.id;
	  // 保留用户已建立的多选（批量操作目标）；单选/无选时选中跟随焦点。
	  if (this.selectedVideoIds.length <= 1) this.selectedVideoIds = [next.id];
	  this.scrollKeyboardFocusIntoView(next.id);
	  if (openPreview) this.openPreview(next);
	},
	scrollKeyboardFocusIntoView(videoId) {
	  this.$nextTick(() => this.$refs.virtualList?.scrollToItem?.(videoId));
	},
	async applyReviewShortcut(action, video) {
	  switch (action) {
		case 'preview':
		  await this.openPreview(video);
		  break;
		case 'favorite':
		  await this.toggleVideoFavorite(video);
		  break;
		case 'watched':
		  await this.toggleVideoWatched(video);
		  break;
		case 'tag':
		  this.openAddTagDialog(video);
		  break;
		case 'play':
		  await this.playVideo(video.id);
		  break;
	  }
	},
	handlePreviewShortcut({ action, video }) {
	  if (action === 'preview') {
		this.closePreview();
		return;
	  }
	  if (action === 'next' || action === 'previous') {
		this.keyboardFocusVideoID = video?.id || this.selectedPreviewVideoId;
		this.moveKeyboardFocus(action === 'next' ? 1 : -1, true);
		return;
	  }
	  if (video) this.applyReviewShortcut(action, video);
	},
    registerRuntimeEvent(eventName, handler) {
      if (!window.runtime?.EventsOn) {
        return;
      }
      const off = window.runtime.EventsOn(eventName, handler);
      if (typeof off === 'function') {
        this.runtimeOffHandlers.push(off);
      }
    },
    teardownRuntimeEvents() {
      while (this.runtimeOffHandlers.length > 0) {
        const off = this.runtimeOffHandlers.pop();
        try {
          off?.();
        } catch (_err) {}
      }
    },
    configureHomeListVirtualization() {
      const userAgent = window.navigator?.userAgent || '';
      const platform = window.navigator?.userAgentData?.platform || window.navigator?.platform || '';
      const hasWailsRuntime = !!window.runtime;
      const isMac = /mac/i.test(platform) || /Macintosh|Mac OS X/i.test(userAgent);
      const isAppleWebKit = /AppleWebKit/i.test(userAgent);
      const isChromium = /Chrome|Chromium|Edg\//i.test(userAgent);
      const shouldDisable = hasWailsRuntime && isMac && isAppleWebKit && !isChromium;

      this.homeListVirtualizationEnabled = !shouldDisable;
      this.debugLog('configureHomeListVirtualization resolved', {
        enabled: this.homeListVirtualizationEnabled,
        hasWailsRuntime,
        platform,
        userAgent
      });
    },
    debugLog(message, payload = null, isError = false) {
      return logFrontend('VideoListPage', message, payload, isError);
    },
    // 四个后台任务的启动入口留在菜单里，实际执行与状态条都在 BackgroundTaskStatusBars。
    startTechnicalBackfill() {
      return this.$refs.taskBars?.startTechnicalBackfill();
    },
    startPerceptualHashBackfill() {
      return this.$refs.taskBars?.startPerceptualHashBackfill();
    },
    startFrameHashBackfill() {
      return this.$refs.taskBars?.startFrameHashBackfill();
    },
    startLocalMetadataBackfill() {
      return this.$refs.taskBars?.startLocalMetadataBackfill();
    },
    // 代理增删之后根条目的预览会话要重取：mode 会在外部预览与内嵌之间切换。
    async refreshPreviewSession(videoID) {
      if (Number(videoID) !== Number(this.selectedPreviewVideoId)) return;
      try {
        this.previewSession = await GetPreviewSession(videoID);
      } catch (err) {
        notifyError('刷新预览会话失败: ' + err);
      }
    },
    // ===== 播放代理入口（D-006）=====
    // 三个入口都走同一条后端队列：单个、选中、当前筛选。结果由
    // playback-proxy-state 事件汇总提示，这里只报"入队"这一步的失败。
    async createProxyForVideo(video) {
      try {
        const status = await CreatePlaybackProxy(video.id);
        const item = (status?.results || []).find(result => Number(result.video_id) === Number(video.id));
        if (item && item.code !== 'created' && item.code !== 'already_exists') {
          notify(`${video.display_title || video.name}：${PLAYBACK_PROXY_CODE_LABELS[item.code] || item.code}`);
        }
      } catch (err) {
        notifyError('生成播放代理失败: ' + err);
      }
    },
    async createProxiesForSelected() {
      if (this.selectedVideoIds.length === 0) return;
      try {
        await BatchCreatePlaybackProxies([...this.selectedVideoIds]);
      } catch (err) {
        notifyError('批量生成播放代理失败: ' + err);
      }
    },
    async createProxiesForCurrentFilter() {
      try {
        const status = await BatchCreatePlaybackProxiesForFilter(this.currentLibraryFilter());
        if (!status?.total) notify('当前筛选没有命中任何视频，未生成播放代理。');
      } catch (err) {
        notifyError('为当前筛选生成播放代理失败: ' + err);
      }
    },
    startLocalMetadataExport() {
      return this.$refs.taskBars?.startLocalMetadataExport(this.currentLibraryFilter());
    },
    exportLocalMetadataNFO(video) {
      return this.$refs.taskBars?.exportLocalMetadataNFO(video);
    },
    async openPreview(video) {
      const requestToken = Symbol('preview');
      this._previewRequestToken = requestToken;
      const requestedStartMs = Number(video?._subtitleMatchStartMs);
      this.previewStartTimeMs = this.isSubtitleSearchActive() && Number.isFinite(requestedStartMs) && requestedStartMs >= 0
        ? requestedStartMs
        : null;
      this.selectedPreviewVideoId = video.id;
      this.previewVideoSnapshot = {
        ...video,
        tags: Array.isArray(video.tags) ? [...video.tags] : []
      };
      this.previewOpen = true;
      this.previewSession = null;

      try {
        const session = await GetPreviewSession(video.id);
        if (this._previewRequestToken !== requestToken) return;
        this.previewSession = session;
      } catch (err) {
        if (this._previewRequestToken !== requestToken) return;
        this.previewSession = {
          video_id: video.id,
          mode: 'unsupported',
          display_name: video.name,
          reason_code: 'preview_failed',
          reason_message: '准备预览失败：' + err
        };
      }
    },
    closePreview() {
      this._previewRequestToken = null;
      this.previewOpen = false;
      this.previewSession = null;
      this.previewStartTimeMs = null;
      this.selectedPreviewVideoId = null;
      this.previewVideoSnapshot = null;
    },
    async findSimilarVideos(video) {
      if (!video?.id) return;
      this.deactivateRandomPick();
      this.semanticSimilarVideoID = video.id;
      this.searchMode = 'semantic';
      this.searchKeyword = `与「${video.display_title || video.name}」相似`;
      this.semanticSearchError = '';
      this.closePreview();
      await this.resetAndLoadVideos();
    },
    async previewExternally(video) {
      if (!video) return;
      try {
        await PreviewExternally(video.id);
      } catch (err) {
        console.error('外部预览失败:', err);
        notifyError('外部预览失败: ' + err);
      }
    },
    // 就地把同步回来的观看进度写到已加载的行上；不在当前列表里的忽略。
    applyWatchProgressUpdates(changes) {
      if (!Array.isArray(changes) || changes.length === 0) return 0;
      const byID = new Map(changes.map(item => [Number(item.video_id), Number(item.watch_position_seconds) || 0]));
      let applied = 0;
      for (const video of this.videos) {
        const position = byID.get(Number(video.id));
        if (position === undefined || video.watch_position_seconds === position) continue;
        video.watch_position_seconds = position;
        applied++;
      }
      if (this.previewVideoSnapshot && byID.has(Number(this.previewVideoSnapshot.id))) {
        this.previewVideoSnapshot = {
          ...this.previewVideoSnapshot,
          watch_position_seconds: byID.get(Number(this.previewVideoSnapshot.id))
        };
      }
      return applied;
    },
    openEnhanceDialog(video) {
      this.$refs.enhanceDialog?.open(video);
    },
    openCleanupDialog() {
      return this.$refs.cleanupPanel?.open();
    },
    refreshCleanupStatus() {
      return this.$refs.cleanupPanel?.refreshStatus();
    },
    forgetCleanupTrashed(videoID) {
      this.$refs.cleanupPanel?.forgetTrashed(videoID);
    },
    // 清理面板勾中的候选仍由片库页删除：删除、撤销提示条与列表重载的先后顺序不变。
    async trashCleanupVideos(selectedIDs) {
      this.deletingIds = [...new Set([...this.deletingIds, ...selectedIDs])];
      const result = await BatchDeleteVideos(selectedIDs, true);
      const failedIDs = new Set((result?.errors || []).map(item => item.video_id));
      const succeededIDs = selectedIDs.filter(id => !failedIDs.has(id));
      this.videos = this.videos.filter(item => !succeededIDs.includes(item.id));
      return { result, failedIDs, succeededIDs };
    },
    // 面板收窄完勾选之后才走这一步，顺序与拆分前一致。
    async afterTrashCleanupVideos(succeededIDs) {
      await this.showDeleteUndo(succeededIDs);
      await this.reloadCurrentView();
    },
    // 状态条组件把四份任务状态镜像过来，「管理」菜单的进度文案与清理面板的重算按钮才有数据。
    applyBackgroundTaskState(state) {
      this.technicalBackfill = state.technicalBackfill;
      this.perceptualHash = state.perceptualHash;
      this.frameHash = state.frameHash;
      this.localMetadataBackfill = state.localMetadataBackfill;
      this.localMetadataExport = state.localMetadataExport;
    },
    handleCleanupTrashSettled(selectedIDs) {
      this.deletingIds = this.deletingIds.filter(id => !selectedIDs.includes(id));
    },
    openSubtitlePreview(video) {
      return this.$refs.subtitlePreview?.open(video);
    },
    openSubtitleWorkbench(video) {
      this.subtitleWorkbench = { show: true, video };
    },
    closeSubtitleWorkbench() {
      this.subtitleWorkbench = { show: false, video: null };
    },
    async handleSubtitleWorkbenchSaved() {
      await this.reloadCurrentView();
    },
    openLocalMetadataDialog(videoIDs) {
      this.localMetadataDialog = { show: true, videoIds: [...new Set((videoIDs || []).map(Number).filter(Boolean))] };
    },
    async handleLocalMetadataApplied() {
      this.selectedVideoIds = [];
      await this.reloadCurrentView();
    },
    generateSubtitle(video) {
      return this.$refs.subtitleTasks?.generate(video);
    },
    renameVideo(video) {
      return this.$refs.renameDialogs?.openRenameVideo(video);
    },
    async moveVideo(video) {
      if (!video || this.migrationRunning) return;
      const destination = await SelectMigrationDestinationDirectory();
      if (!destination) return;
      this.migrationRunning = true;
      try {
        const result = await MoveVideo(video.id, destination);
        await this.reloadCurrentView();
        if (result?.warning) notify(`视频迁移完成。\n警告：${result.warning}`);
      } catch (err) {
        console.error('迁移视频失败:', err);
        notifyError('迁移视频失败: ' + err);
      } finally {
        this.migrationRunning = false;
      }
    },
    async moveSelectedVideos() {
      if (this.selectedVideoIds.length === 0 || this.migrationRunning) return;
      const destination = await SelectMigrationDestinationDirectory();
      if (!destination) return;
      const ids = [...this.selectedVideoIds];
      this.migrationRunning = true;
      try {
        const result = await BatchMoveVideos(ids, destination);
        this.selectedVideoIds = [];
        await this.reloadCurrentView();
        const failures = (result?.errors || []).map(item => `失败 #${item.video_id}: ${item.error}`);
        const warnings = (result?.warnings || []).map(item => `警告 #${item.video_id}: ${item.warning}`);
        if (failures.length > 0 || warnings.length > 0) {
          notifyError(`迁移完成：成功 ${result.succeeded || 0}，失败 ${result.failed || 0}\n${[...failures, ...warnings].join('\n')}`);
        }
      } catch (err) {
        console.error('批量迁移失败:', err);
        notifyError('批量迁移失败: ' + err);
      } finally {
        this.migrationRunning = false;
      }
    },
    async moveFolder() {
      if (this.migrationRunning) return;
      const source = await SelectMigrationSourceDirectory();
      if (!source) return;
      const destinationParent = await SelectMigrationDestinationDirectory();
      if (!destinationParent) return;
      if (!await confirmAction({ title: '迁移文件夹', message: `将文件夹\n${source}\n迁移到\n${destinationParent}\n并同步更新库内路径，是否继续？`, confirmText: '迁移' })) return;
      this.migrationRunning = true;
      try {
        const result = await MoveDirectory(source, destinationParent);
        this.$emit('reload-directories');
        await this.reloadCurrentView();
        const warning = result?.warning ? `\n警告：${result.warning}` : '';
        notify(`文件夹迁移完成：更新 ${result?.videos_updated || 0} 个视频、${result?.directories_updated || 0} 个扫描目录。${warning}`);
      } catch (err) {
        console.error('迁移文件夹失败:', err);
        notifyError('迁移文件夹失败: ' + err);
      } finally {
        this.migrationRunning = false;
      }
    },
    renameFolder() {
      return this.$refs.renameDialogs?.openRenameFolder();
    },
    // 重命名成功后把已加载的那一行改名，避免整表重载让它跳位置。
    applyVideoRename({ video, finalName }) {
      const idx = this.videos.findIndex(v => v.id === video.id);
      if (idx !== -1) {
        this.videos[idx].name = finalName;
        this.videos[idx].path = video.path.replace(video.name, finalName);
      }
    },
    calculateScore(video) {
      const weight = this.settings.play_weight || 2.0;
      return video.play_count * weight + video.random_play_count;
    },
    setSearchMode(mode) {
      if (mode === 'semantic' && !this.semanticAvailable) return;
      if (this.searchMode === mode) return;
      this.searchMode = mode;
      this.handleSearch(true, true);
    },
    async loadSemanticStatus() {
      try {
        this.semanticStatus = await GetSemanticIndexStatus();
        // 能力掉了而当前正停在语义模式，退回文件搜索，别把用户卡在一个用不了的模式里。
        if (!this.semanticAvailable && this.searchMode === 'semantic') {
          this.searchMode = 'file';
          this.handleSearch(true, true);
        }
      } catch (err) {
        this.semanticStatus = null;
        this.debugLog('loadSemanticStatus failed', { err: String(err) }, true);
      }
    },
    // 浮层里的区间草稿归工具栏组件；「应用」时把结果收回来提交为生效条件。
    applyFilterConditions(draft) {
      this.selectedSizeRange = draft.sizeRange;
      this.selectedResRange = draft.resRange;
      this.minRating = draft.minRating;
      this.maxRating = draft.maxRating;
      this.handleSearch(true);
    },
    clearAllConditions() {
      this.smartView = '';
      this.selectedTags = [];
      this.selectedSizeRange = 'all';
      this.selectedResRange = 'all';
      this.minRating = '';
      this.maxRating = '';
      this.searchKeyword = '';
      this.handleSearch(true, true);
    },
    // 结果条与浮层预览共用一份筛选 DTO 构造：给定一份区间条件覆盖，
    // 其余维度沿用当前生效值。
    libraryFilterFrom(overrides) {
      const base = this.currentLibraryFilter();
      const bounds = source => {
        const range = source === 'all' ? null : source;
        return range ? { min: range.min, max: range.max } : { min: 0, max: 0 };
      };
      const size = bounds(overrides.sizeRange);
      const res = bounds(overrides.resRange);
      return {
        ...base,
        min_size: size.min,
        max_size: size.max,
        min_height: res.min,
        max_height: res.max,
        min_rating: overrides.minRating === '' ? null : Number(overrides.minRating),
        max_rating: overrides.maxRating === '' ? null : Number(overrides.maxRating)
      };
    },
    // 结果条的命中数：筛选变化后请求一次，翻页不再重复请求。
    async refreshLibraryCounts() {
      if (this.searchMode === 'semantic') {
        this.filteredCount = null;
        return;
      }
      const token = ++this.countToken;
      try {
        const [filtered, total] = await Promise.all([
          CountLibraryVideos(this.currentLibraryFilter()),
          this.libraryTotalCount === null ? CountLibraryVideos(this.emptyLibraryFilter()) : Promise.resolve(this.libraryTotalCount)
        ]);
        if (token !== this.countToken) return;
        this.filteredCount = filtered;
        this.libraryTotalCount = total;
      } catch (err) {
        if (token !== this.countToken) return;
        this.filteredCount = null;
        this.debugLog('refreshLibraryCounts failed', { err: String(err) }, true);
      }
    },
    emptyLibraryFilter() {
      return {
        search_mode: 'file',
        keyword: '',
        smart_view: '',
        tag_ids: [],
        min_size: 0,
        max_size: 0,
        min_height: 0,
        max_height: 0,
        min_rating: null,
        max_rating: null,
        sort_mode: 'balanced'
      };
    },
    onManageSelect(item) {
      switch (item.id) {
        case 'scan-new': this.showScanDialog = true; break;
        case 'scan-incremental': this.runIncrementalScan(); break;
        case 'move-folder': this.moveFolder(); break;
        case 'rename-folder': this.renameFolder(); break;
        case 'export-nfo': this.startLocalMetadataExport(); break;
        case 'backfill-technical': this.startTechnicalBackfill(); break;
        case 'backfill-phash': this.startPerceptualHashBackfill(); break;
        case 'backfill-playback-proxy': this.createProxiesForCurrentFilter(); break;
        case 'backfill-local-metadata': this.startLocalMetadataBackfill(); break;
        case 'ai-tags': this.openAITagReviewDialog(); break;
        case 'tag-manager': this.showTagManagerDialog = true; break;
        case 'cleanup': this.openCleanupDialog(); break;
        case 'trash': this.openTrashDialog(); break;
        default: break;
      }
    },
    onViewSelect(item) {
      if (item.id === 'save-current') {
        this.openSaveViewDialog();
        return;
      }
      if (item.id === 'delete-current') {
        this.deleteSelectedSavedView();
        return;
      }
      if (item.id.startsWith('saved:')) {
        this.selectedSavedViewID = Number(item.id.slice(6));
        this.applySelectedSavedView();
      }
    },
    clearSelection() {
      this.selectedVideoIds = [];
    },
    openRowMenu(video, anchor) {
      this.rowMenu = { video, anchor, position: null };
    },
    closeRowMenu() {
      this.rowMenu = { video: null, anchor: null, position: null };
    },
    onRowMenuSelect(item) {
      const video = this.rowMenu.video;
      if (!video) return;
      switch (item.id) {
        case 'directory': this.openDirectory(video.id); break;
        case 'rename': this.renameVideo(video); break;
        case 'move': this.moveVideo(video); break;
        case 'export-nfo': this.exportLocalMetadataNFO(video); break;
        case 'subtitle': this.generateSubtitle(video); break;
        case 'subtitle-edit': this.openSubtitleWorkbench(video); break;
        case 'subtitle-preview': this.openSubtitlePreview(video); break;
        case 'enhance': this.openEnhanceDialog(video); break;
        case 'playback-proxy': this.createProxyForVideo(video); break;
        case 'delete': this.confirmDelete(video); break;
        default: break;
      }
    },
    onRandomSelect(item) {
      if (item.id === 'pick-ten') {
        this.enterRandomPick();
        return;
      }
      if (item.id.startsWith('mode:')) this.randomMode = item.id.slice(5);
    },
    currentQueryKeyword() {
      return this.searchKeyword.trim();
    },
    subtitleLengthBucket(text) {
      const length = text ? text.length : 0;
      if (length === 0) return 0;
      if (length <= 40) return 1;
      if (length <= 100) return 2;
      return 3;
    },
    videoVisualVersion(video) {
      return JSON.stringify({
        tagCount: Array.isArray(video?.tags) ? video.tags.length : 0,
        isStale: !!video?.is_stale,
        isFavorite: !!video?.is_favorite,
        isWatched: !!video?.is_watched,
        watchPosition: Math.floor(Number(video?.watch_position_seconds || 0)),
        subtitleBucket: this.subtitleLengthBucket(video?._subtitleMatchText)
      });
    },
    estimateVideoHeight(video, widthBucket, subtitleMode) {
      const density = this.previewOpen && this.viewMode === 'list' ? 'narrow' : this.rowDensity;
      return estimateVideoRowHeight(video, widthBucket, subtitleMode, density);
    },
    hasStructuredFilters() {
      return this.selectedTags.length > 0 || this.selectedSizeRange !== 'all' || this.selectedResRange !== 'all' || this.minRating !== '' || this.maxRating !== '' || this.sortMode !== 'balanced';
    },
    currentLibraryFilter() {
      const { minSize, maxSize, minHeight, maxHeight } = this.currentFilterBounds();
      // 语义模式不进入共享筛选 DTO：后端筛选器不认识 semantic 模式，语义
      // 查询只通过 SearchSemanticVideos/FindSimilarVideos 的专用参数传递；
      // 随机播放、保存视图、批量导出等消费方拿到的是纯结构化筛选。
      const semantic = this.searchMode === 'semantic';
      return {
        search_mode: semantic ? 'file' : this.searchMode,
        keyword: semantic ? '' : this.currentQueryKeyword(),
        smart_view: this.smartView,
        tag_ids: [...this.selectedTags],
        min_size: minSize,
        max_size: maxSize,
        min_height: minHeight,
        max_height: maxHeight,
        min_rating: this.minRating === '' ? null : Number(this.minRating),
        max_rating: this.maxRating === '' ? null : Number(this.maxRating),
        sort_mode: this.sortMode
      };
    },
    matchesSmartView(video) {
      switch (this.smartView) {
        case 'favorites': return !!video.is_favorite;
        case 'liked': return !!video.is_liked;
        case 'continue_watching': return !video.is_watched && Number(video.watch_position_seconds || 0) > 0;
        case 'unwatched': return !video.is_watched;
        case 'watched': return !!video.is_watched;
        case 'recently_played': return !!video.last_played_at;
        case 'recently_added': {
          const createdAt = new Date(video.created_at || 0).getTime();
          return createdAt > 0 && createdAt >= Date.now() - 30 * 24 * 60 * 60 * 1000;
        }
        case 'untagged': return !Array.isArray(video.tags) || video.tags.length === 0;
        case 'no_subtitle': return !this.isSubtitleSearchActive();
        case 'stale': return !!video.is_stale;
        default: return true;
      }
    },
    // 字幕搜索模式下给每条结果补上首个命中片段，行内的"字幕命中"按钮才有内容可跳转。
    async attachSubtitleHits(videos, keyword) {
      if (!this.isSubtitleSearchActive(keyword) || videos.length === 0) return videos;
      const hits = await GetLibrarySubtitleHits(keyword, videos.map(video => video.id));
      const hitsByVideoID = new Map((hits || []).map(hit => [hit.video_id, hit.segment]));
      return videos.map(video => {
        const segment = hitsByVideoID.get(video.id);
        if (!segment) return video;
        return {
          ...video,
          _subtitleMatchText: segment.text || '',
          _subtitleMatchStartMs: segment.start_time_ms,
          _subtitleMatchEndMs: segment.end_time_ms
        };
      });
    },
    async loadVideos() {
      if (this.loading || !this.hasMore) return;
      this.loading = true;
      try {
        const keyword = this.currentQueryKeyword();
        let newVideos = [];
        let semanticHasMore = null;
        this.debugLog('loadVideos begin', {
          keyword,
          searchMode: this.searchMode,
          hasStructuredFilters: this.hasStructuredFilters(),
          cursorScore: this.cursorScore,
          cursorSize: this.cursorSize,
          cursorID: this.cursorID,
          pageSize: this.pageSize,
          existingVideos: this.videos.length
        });

        if (this.searchMode === 'semantic') {
          if (!keyword && !this.semanticSimilarVideoID) {
            this.hasMore = false;
            return;
          }
          this.semanticSearchError = '';
          const request = {
            filter: this.currentLibraryFilter(),
            offset: this.videos.length,
            limit: this.pageSize
          };
          const page = this.semanticSimilarVideoID
            ? await FindSimilarVideos({ ...request, video_id: this.semanticSimilarVideoID })
            : await SearchSemanticVideos({ ...request, query: keyword });
          this.semanticCoverage = page?.coverage || null;
          semanticHasMore = !!page?.has_more;
          newVideos = (page?.hits || []).map(hit => ({ ...hit.video, _semanticScore: hit.score }));
          this.libraryCursor = null;
        } else if (this.smartView === 'recently_played' && this.sortMode === 'balanced') {
          newVideos = await ListRecentlyPlayedWithFilter(
            this.currentLibraryFilter(),
            this.cursorLastPlayedAt,
            this.cursorRecentPlayedID,
            this.pageSize
          );
        } else {
          const request = { filter: this.currentLibraryFilter(), limit: this.pageSize };
          if (this.libraryCursor) request.cursor = this.libraryCursor;
          const page = await SearchLibraryVideoPage(request);
          newVideos = page?.videos || [];
          this.libraryCursor = page?.next_cursor || null;
        }

        newVideos = await this.attachSubtitleHits(newVideos, keyword);

        this.debugLog('loadVideos query resolved', {
          count: newVideos.length,
          sample: newVideos.slice(0, 3).map(video => ({ id: video.id, name: video.name, path: video.path })),
          mode: this.smartView || keyword || this.hasStructuredFilters() ? 'filtered' : 'paginated'
        });

        if (this.searchMode === 'semantic' ? !semanticHasMore : (this.smartView === 'recently_played' && this.sortMode === 'balanced' ? newVideos.length < this.pageSize : !this.libraryCursor)) {
          this.hasMore = false;
        }
        if (newVideos.length > 0) {
          this.videos.push(...newVideos);
          const last = newVideos[newVideos.length - 1];
          if (this.smartView === 'recently_played' && this.sortMode === 'balanced') {
            this.cursorLastPlayedAt = last.last_played_at || '';
            this.cursorRecentPlayedID = last.id;
          }
        }
        this.debugLog('loadVideos applied to state', {
          totalVideos: this.videos.length,
          hasMore: this.hasMore
        });
      } catch (err) {
		if (this.searchMode === 'semantic') this.semanticSearchError = String(err);
        this.debugLog('loadVideos failed', { err: String(err) }, true);
        console.error('加载视频失败:', err);
        notifyError('加载视频失败: ' + err);
      } finally {
        this.loading = false;
        const idleResolvers = this.loadIdleResolvers.splice(0);
        idleResolvers.forEach(resolve => resolve());
        this.debugLog('loadVideos finished', {
          totalVideos: this.videos.length,
          hasMore: this.hasMore,
          loading: this.loading
        });
      }
    },
    waitForLoadIdle() {
      if (!this.loading) return Promise.resolve();
      return new Promise(resolve => this.loadIdleResolvers.push(resolve));
    },
    async resetAndLoadVideos() {
      this.reloadRequested = true;
      if (this.reloadPromise) {
        await this.reloadPromise;
        if (this.reloadRequested) return this.resetAndLoadVideos();
        return;
      }
      this.refreshLibraryCounts();
      const activeReload = (async () => {
        while (this.reloadRequested) {
          this.reloadRequested = false;
          await this.waitForLoadIdle();
          this.videos = [];
          this.selectedVideoIds = [];
          this.cursorScore = 0;
          this.cursorSize = 0;
          this.cursorID = 0;
          this.cursorLastPlayedAt = '';
          this.cursorRecentPlayedID = 0;
          this.libraryCursor = null;
          if (this.searchMode !== 'semantic') {
            this.semanticCoverage = null;
            this.semanticSearchError = '';
          }
          this.hasMore = true;
          await this.loadVideos();
        }
      })();
      this.reloadPromise = activeReload;
      await activeReload;
      if (this.reloadPromise === activeReload) {
        this.reloadPromise = null;
      }
      if (this.reloadRequested) return this.resetAndLoadVideos();
    },
    isSubtitleSearchActive(keyword = this.searchKeyword.trim()) {
      return this.searchMode === 'subtitle' && !!keyword;
    },
    currentFilterBounds() {
      let minSize = 0, maxSize = 0;
      let minHeight = 0, maxHeight = 0;
      if (this.selectedSizeRange !== 'all') {
        minSize = this.selectedSizeRange.min;
        maxSize = this.selectedSizeRange.max;
      }
      if (this.selectedResRange !== 'all') {
        minHeight = this.selectedResRange.min;
        maxHeight = this.selectedResRange.max;
      }
      return { minSize, maxSize, minHeight, maxHeight };
    },
    findRangeOption(options, min, max) {
      return options.find(option => option.value.min === Number(min || 0) && option.value.max === Number(max || 0))?.value || 'all';
    },
    async loadSavedLibraryViews() {
      try {
        this.savedViews = await ListSavedLibraryViews() || [];
      } catch (err) {
        console.error('加载保存视图失败:', err);
      }
    },
    // 命令面板的导航入口（D-029）：切智能视图与应用保存视图都复用工具栏那条路径，
    // 不另建一套加载逻辑。
    applySmartViewCommand(value) {
      this.smartView = value || '';
      return this.handleSearch(true);
    },
    applySavedViewCommand(viewID) {
      this.selectedSavedViewID = Number(viewID);
      return this.applySelectedSavedView();
    },
    openSaveViewDialog() {
      this.$refs.saveViewDialog?.open();
    },
    // 保存成功后刷新视图列表并选中新视图，这两步仍归片库页。
    async afterSavedLibraryView(saved) {
      await this.loadSavedLibraryViews();
      this.selectedSavedViewID = saved.id;
    },
    async applySelectedSavedView() {
      const view = this.savedViews.find(item => item.id === Number(this.selectedSavedViewID));
      if (!view) return;
      this.deactivateRandomPick();
      let tagIDs = [];
      try {
        const parsed = JSON.parse(view.tag_ids_json || '[]');
        const activeTagIDs = new Set((this.tags || []).map(tag => Number(tag.id)));
        tagIDs = Array.isArray(parsed)
          ? parsed.map(Number).filter(id => Number.isFinite(id) && activeTagIDs.has(id))
          : [];
      } catch (err) {
        console.error('保存视图标签条件无效:', err);
      }
      this.searchMode = view.search_mode || 'file';
      this.searchKeyword = view.keyword || '';
      this.smartView = view.smart_view || '';
      this.selectedTags = tagIDs;
      this.selectedSizeRange = this.findRangeOption(this.sizeOptions, view.min_size, view.max_size);
      this.selectedResRange = this.findRangeOption(this.resOptions, view.min_height, view.max_height);
      this.minRating = view.min_rating === null || view.min_rating === undefined ? '' : String(view.min_rating);
      this.maxRating = view.max_rating === null || view.max_rating === undefined ? '' : String(view.max_rating);
      this.sortMode = view.sort_mode || 'balanced';
      await this.reloadCurrentView();
    },
    async deleteSelectedSavedView() {
      const view = this.savedViews.find(item => item.id === Number(this.selectedSavedViewID));
      if (!view || !await confirmAction({ title: '删除保存视图', message: `确定删除保存视图「${view.name}」吗？`, confirmText: '删除', danger: true })) return;
      try {
        await DeleteSavedLibraryView(view.id);
        this.selectedSavedViewID = 0;
        await this.loadSavedLibraryViews();
      } catch (err) {
        notifyError('删除保存视图失败: ' + err);
      }
    },
    async reloadCurrentView() {
      if (this.randomPick.active) return this.refreshRandomPick();
      return this.resetAndLoadVideos();
    },
    applyClientFilters(videos) {
      return (videos || []).filter(video => {
        const tagMatched = this.selectedTags.length === 0 ||
          this.selectedTags.every(id => (video.tags || []).some(tag => tag.id === id));

        const sizeMatched = this.selectedSizeRange === 'all' ||
          (video.size >= this.selectedSizeRange.min && (this.selectedSizeRange.max === 0 || video.size < this.selectedSizeRange.max));

        const resMatched = this.selectedResRange === 'all' ||
          (video.height >= this.selectedResRange.min && (this.selectedResRange.max === 0 || video.height <= this.selectedResRange.max));

        const rating = video.personal_rating;
        const ratingMatched = (this.minRating === '' && this.maxRating === '') ||
          (rating !== null && rating !== undefined &&
            (this.minRating === '' || Number(rating) >= Number(this.minRating)) &&
            (this.maxRating === '' || Number(rating) <= Number(this.maxRating)));

        return tagMatched && sizeMatched && resMatched && ratingMatched;
      });
    },
    tagBgColor(hex) {
      if (!hex || !hex.startsWith('#')) return hex;
      const r = parseInt(hex.slice(1, 3), 16);
      const g = parseInt(hex.slice(3, 5), 16);
      const b = parseInt(hex.slice(5, 7), 16);
      return `rgba(${r},${g},${b},0.35)`;
    },
    isTagSelected(tagID) {
      return this.selectedTags.includes(Number(tagID));
    },
    toggleTagFilter(tagID) {
      this.deactivateRandomPick();
      const id = Number(tagID);
      if (this.isTagSelected(id)) {
        this.selectedTags = this.selectedTags.filter(item => item !== id);
      } else {
        this.selectedTags = [...this.selectedTags, id];
      }
      this.selectedSavedViewID = 0;
      this.reloadCurrentView();
    },
    clearTagFilter() {
      this.deactivateRandomPick();
      this.selectedTags = [];
      this.selectedSavedViewID = 0;
      this.reloadCurrentView();
    },
    canVideoMatchCurrentView(video) {
      const keyword = this.currentQueryKeyword().toLowerCase();
      if (this.searchMode === 'semantic') {
        return this.applyClientFilters([video]).length > 0 && this.matchesSmartView(video);
      }
      if (this.isSubtitleSearchActive(keyword)) {
        return false;
      }
      const nameOrPathMatched = !keyword || `${video.display_title || ''} ${video.original_title || ''} ${video.name} ${video.path}`.toLowerCase().includes(keyword);
      if (!nameOrPathMatched) return false;
      return this.applyClientFilters([video]).length > 0 && this.matchesSmartView(video);
    },
    mergeVideoState(updatedVideo) {
      if (!updatedVideo) return;
      const index = this.videos.findIndex(video => video.id === updatedVideo.id);
      if (index !== -1) {
        this.videos.splice(index, 1, { ...this.videos[index], ...updatedVideo });
      }
      if (this.selectedPreviewVideoId === updatedVideo.id) {
        this.previewVideoSnapshot = {
          ...(this.previewVideoSnapshot || {}),
          ...updatedVideo,
          tags: Array.isArray(updatedVideo.tags) ? [...updatedVideo.tags] : (this.previewVideoSnapshot?.tags || [])
        };
      }
    },
    async handleVideoDetailsUpdated(details) {
      const updatedVideoID = Number(details?.video?.id || 0);
      if (this.selectedPreviewVideoId === updatedVideoID) {
        this.previewVideoSnapshot = patchVideoFromDetails(this.previewVideoSnapshot || details.video, details);
      }
      const index = this.videos.findIndex(video => video.id === updatedVideoID);
      if (index < 0) return;
      const patched = patchVideoFromDetails(this.videos[index], details);
      if (!this.canVideoMatchCurrentView(patched) || this.sortMode !== 'balanced') {
        await this.reloadCurrentView();
        return;
      }
      this.videos.splice(index, 1, patched);
    },
    resumePositionFor(video) {
      if (!video || video.is_watched) return 0;
      const position = Number(video.watch_position_seconds || 0);
      const duration = Number(video.duration || 0);
      if (!Number.isFinite(position) || position <= 0) return 0;
      if (duration > 0 && position >= Math.max(duration - 5, duration * 0.98)) return 0;
      return position;
    },
    async applyVideoStateChange(updatedVideo) {
      if (!updatedVideo) return;
      const stateSensitiveViews = ['favorites', 'liked', 'continue_watching', 'unwatched', 'watched'];
      if (stateSensitiveViews.includes(this.smartView) && !this.matchesSmartView(updatedVideo)) {
        await this.reloadCurrentView();
        return;
      }
      this.mergeVideoState(updatedVideo);
    },
    async toggleVideoFavorite(video) {
      try {
        const updated = await SetVideoFavorite(video.id, !video.is_favorite);
        await this.applyVideoStateChange(updated);
      } catch (err) {
        notifyError('更新收藏状态失败: ' + err);
      }
    },
    async toggleVideoWatched(video) {
      try {
        const updated = await SetVideoWatched(video.id, !video.is_watched);
        await this.applyVideoStateChange(updated);
      } catch (err) {
        notifyError('更新观看状态失败: ' + err);
      }
    },
    handlePreviewWatchProgress(progress) {
      const videoID = Number(progress?.videoID || this.selectedPreviewVideoId || 0);
      if (!videoID) return;
      const save = async () => {
        const updated = await UpdateVideoWatchProgress(videoID, Number(progress?.positionSeconds || 0), !!progress?.completed);
        await this.applyVideoStateChange(updated);
      };
      this._watchProgressPromise = (this._watchProgressPromise || Promise.resolve())
        .then(save)
        .catch(err => console.error('保存观看进度失败:', err));
    },
    async applyPlaybackAttemptResult(result) {
      if (!result) return;

      if (!result.dispatch_succeeded) {
        notifyError(result.user_message || '播放失败');
      }

      const reconcile = result.reconcile_result;
      if (!reconcile) {
        return;
      }

      if (reconcile.needs_reload || !reconcile.updated_video) {
        await this.reloadCurrentView();
        return;
      }

      if (this.smartView === 'recently_played') {
        await this.reloadCurrentView();
        return;
      }

      if (!this.canVideoMatchCurrentView(reconcile.updated_video)) {
        await this.reloadCurrentView();
        return;
      }

      const index = this.videos.findIndex(video => video.id === reconcile.video_id);
      if (index === -1) {
        return;
      }

      const merged = {
        ...this.videos[index],
        ...reconcile.updated_video
      };
      this.videos.splice(index, 1, merged);
      if (this.selectedPreviewVideoId === reconcile.video_id) {
        this.previewVideoSnapshot = {
          ...merged,
          tags: Array.isArray(merged.tags) ? [...merged.tags] : []
        };
      }
    },
    async handleSearch(immediate = false, clearSimilar = false) {
      // 筛选条件变了，固定的随机批次就不再成立：退出批次、作废在途的抽取，按新条件正常加载。
      if (this.randomPick.active) immediate = true;
      this.deactivateRandomPick();
      this.selectedSavedViewID = 0;
      if (clearSimilar) this.semanticSimilarVideoID = 0;
      if (this.searchDebounceTimer) {
        clearTimeout(this.searchDebounceTimer);
        this.searchDebounceTimer = null;
      }

      if (immediate) {
        await this.reloadCurrentView();
        return;
      }

      // 语义模式下每次检索都要调用一次 embedding 接口，输入过程不自动触发，
      // 由回车（immediate）显式发起。
      if (this.searchMode === 'semantic') return;

      this.searchDebounceTimer = setTimeout(() => {
        this.searchDebounceTimer = null;
        this.reloadCurrentView();
      }, 250);
    },
    // 「随机 N 部」直接接管主列表：抽出来的条目走的是同一个列表组件，
    // 预览/播放/标签/删除等操作与平时完全一致，不需要另做一套。
    async enterRandomPick() {
      await this.loadRandomPickBatch([]);
    },
    async reshuffleRandomPick() {
      await this.loadRandomPickBatch(this.randomPick.ids);
    },
    async loadRandomPickBatch(extraExcludeIDs) {
      if (this.randomPick.loading) return;
      const token = ++this.randomPickToken;
      this.randomPick.loading = true;
      try {
        const excludeIDs = [...new Set([
          ...this.recentRandomVideoIDs.slice(-12),
          ...(extraExcludeIDs || [])
        ])];
        const result = await PickRandomVideos({
          filter: this.currentLibraryFilter(),
          mode: this.randomMode,
          exclude_ids: excludeIDs
        }, RANDOM_PICK_SIZE);
        const picked = result?.videos || [];
        if (picked.length === 0) {
          notifyError(result?.user_message || '当前筛选范围没有可随机的视频。');
          return;
        }
        const videos = await this.attachSubtitleHits(picked, this.currentQueryKeyword());
        // 抽样结果要盖掉列表，先等在途的加载收尾，避免被后到的分页结果覆盖。
        if (this.reloadPromise) await this.reloadPromise;
        await this.waitForLoadIdle();
        // 等待期间用户可能改了筛选或退出了随机，这一批已经作废，不能再装回列表。
        if (token !== this.randomPickToken) return;
        this.randomPick.active = true;
        this.randomPick.ids = videos.map(video => video.id);
        this.randomPick.reason = result?.selection_reason || '';
        this.applyRandomPickVideos(videos);
      } catch (err) {
        console.error('随机抽取失败:', err);
        notifyError('随机抽取失败: ' + err);
      } finally {
        this.randomPick.loading = false;
      }
    },
    applyRandomPickVideos(videos) {
      this.videos = videos;
      this.selectedVideoIds = [];
      this.hasMore = false;
      this.libraryCursor = null;
      this.cursorScore = 0;
      this.cursorSize = 0;
      this.cursorID = 0;
      this.cursorLastPlayedAt = '';
      this.cursorRecentPlayedID = 0;
    },
    // 随机批次是固定的一组 ID，刷新时按 ID 取最新记录，而不是重新抽一批。
    async refreshRandomPick() {
      let videos = [];
      try {
        const refreshed = await GetVideosByIDs(this.randomPick.ids) || [];
        videos = await this.attachSubtitleHits(refreshed, this.currentQueryKeyword());
      } catch (err) {
        // 与普通列表加载失败保持一致：报错并留住当前批次，不把列表清空。
        console.error('刷新随机批次失败:', err);
        notifyError('刷新随机批次失败: ' + err);
        return;
      }
      if (videos.length === 0) {
        this.deactivateRandomPick();
        await this.resetAndLoadVideos();
        return;
      }
      this.randomPick.ids = videos.map(video => video.id);
      this.applyRandomPickVideos(videos);
    },
    deactivateRandomPick() {
      // 顺带作废在途的抽取请求；loading 仍由 loadRandomPickBatch 自己收尾。
      this.randomPickToken += 1;
      this.randomPick.active = false;
      this.randomPick.ids = [];
      this.randomPick.reason = '';
    },
    async exitRandomPick() {
      if (this.randomPick.loading) return;
      this.deactivateRandomPick();
      await this.resetAndLoadVideos();
    },
    async playRandom() {
      try {
        const result = await PlayRandomVideoWithFilter({
          filter: this.currentLibraryFilter(),
          mode: this.randomMode,
          exclude_ids: this.recentRandomVideoIDs.slice(-12)
        });
        if (result.dispatch_succeeded && result.video) {
          this.recentRandomVideoIDs = [...this.recentRandomVideoIDs, result.video.id].slice(-24);
          await this.applyPlaybackAttemptResult(result);
          notify(`正在随机播放: ${result.video.name}\n${result.selection_reason || '按当前筛选条件选择'}`);
          return;
        }
        await this.applyPlaybackAttemptResult(result);
      } catch (err) {
        console.error('随机播放失败:', err);
        notifyError('随机播放失败: ' + err);
      }
    },
    async playVideo(id) {
      try {
        const result = await PlayVideo(id);
        await this.applyPlaybackAttemptResult(result);
      } catch (err) {
        console.error('播放失败:', err);
        notifyError('播放失败: ' + err);
      }
    },
    async openDirectory(id) {
      try {
        await OpenDirectory(id);
      } catch (err) {
        console.error('打开目录失败:', err);
        notifyError('打开目录失败: ' + err);
      }
    },
    confirmDelete(video) {
      if (!this.settings.confirm_before_delete) {
        this.deleteVideo(video, this.settings.delete_original_file);
        return;
      }
      this.deleteDialog = { show: true, video: video, videoIds: [] };
    },
    confirmBatchDelete() {
      const videoIds = [...new Set(this.selectedVideoIds)];
      if (videoIds.length === 0) return;
      if (!this.settings.confirm_before_delete) {
        this.deleteVideos(videoIds, this.settings.delete_original_file);
        return;
      }
      this.deleteDialog = { show: true, video: null, videoIds };
    },
    async executeDelete({ video, deleteFile, dontAskAgain }) {
      if (dontAskAgain) {
        await UpdateSettings({
          ...this.settings,
          confirm_before_delete: false,
          delete_original_file: deleteFile,
          video_extensions: this.settings.video_extensions || '',
          play_weight: this.settings.play_weight || 2.0,
          auto_scan_on_startup: this.settings.auto_scan_on_startup || false,
          log_enabled: this.settings.log_enabled || false
        });
        this.$emit('update-settings', {
          ...this.settings,
          confirm_before_delete: false,
          delete_original_file: deleteFile
        });
      }
      if (this.deleteDialog.videoIds.length > 0) {
        await this.deleteVideos(this.deleteDialog.videoIds, deleteFile);
      } else {
        await this.deleteVideo(video, deleteFile);
      }
      this.deleteDialog.show = false;
    },
    async deleteVideo(video, deleteFile) {
      try {
        if (!this.deletingIds.includes(video.id)) {
          this.deletingIds.push(video.id);
        }
        await DeleteVideo(video.id, deleteFile);
      if (this.selectedPreviewVideoId === video.id) {
        this.closePreview();
      }
        this.videos = this.videos.filter(v => v.id !== video.id);
        await this.showDeleteUndo([video.id], video.id);
        await this.reloadCurrentView();
      } catch (err) {
        console.error('删除失败:', err);
        notifyError('删除失败: ' + err);
      } finally {
        this.deletingIds = this.deletingIds.filter(id => id !== video.id);
      }
    },
    async deleteVideos(videoIds, deleteFile) {
      const ids = [...new Set(videoIds)].filter(id => !!id);
      if (ids.length === 0) return;
      try {
        this.deletingIds = [...new Set([...this.deletingIds, ...ids])];
        const result = await BatchDeleteVideos(ids, deleteFile);
        const failedIds = new Set((result?.errors || []).map(item => item.video_id));
        const succeededIds = ids.filter(id => !failedIds.has(id));

        if (succeededIds.includes(this.selectedPreviewVideoId)) {
          this.closePreview();
        }
        this.videos = this.videos.filter(video => !succeededIds.includes(video.id));
        this.selectedVideoIds = this.selectedVideoIds.filter(id => failedIds.has(id));
        await this.showDeleteUndo(succeededIds);
        await this.reloadCurrentView();

        if (result?.failed > 0) {
          const firstError = result.errors?.[0];
          notifyError(`批量删除完成：成功 ${result.succeeded} 个，失败 ${result.failed} 个。${firstError ? `\n首个失败：视频 ${firstError.video_id}，${firstError.error}` : ''}`);
        }
      } catch (err) {
        console.error('批量删除失败:', err);
        notifyError('批量删除失败: ' + err);
      } finally {
        this.deletingIds = this.deletingIds.filter(id => !ids.includes(id));
      }
    },
    showContextMenu(event, video) {
      this.rowMenu = { video, anchor: null, position: { x: event.clientX, y: event.clientY } };
    },
    isVideoSelected(videoID) {
      return this.selectedVideoIds.includes(videoID);
    },
    toggleVideoSelection(video, selected) {
      if (!video) return;
      if (selected) {
        if (!this.selectedVideoIds.includes(video.id)) {
          this.selectedVideoIds = [...this.selectedVideoIds, video.id];
        }
      } else {
        this.selectedVideoIds = this.selectedVideoIds.filter(id => id !== video.id);
      }
    },
    toggleSelectAllVisible() {
      const visibleIds = this.videos.map(video => video.id);
      if (this.allVisibleSelected) {
        this.selectedVideoIds = this.selectedVideoIds.filter(id => !visibleIds.includes(id));
      } else {
        this.selectedVideoIds = [...new Set([...this.selectedVideoIds, ...visibleIds])];
      }
    },
    openBatchAddTagDialog() {
      if (this.selectedVideoIds.length === 0) return;
      this.addTagDialog = { show: true, video: null, videoIds: [...this.selectedVideoIds], mode: 'batch' };
    },
    openAddTagDialog(video) {
      this.addTagDialog = { show: true, video: video, videoIds: [], mode: 'single' };
    },
    openAITagReviewDialog() {
      this.aiTagReviewDialog = { show: true, dirty: false };
    },
    async closeAITagReviewDialog() {
      const dirty = this.aiTagReviewDialog.dirty;
      this.aiTagReviewDialog = { show: false, dirty: false };
      if (dirty) await this.reloadCurrentView();
    },
    openTrashDialog() {
      this.$refs.trashUndo?.openTrashDialog();
    },
    showDeleteUndo(videoIDs, preferredVideoID = null) {
      return this.$refs.trashUndo?.showDeleteUndo(videoIDs, preferredVideoID);
    },
    // 从回收站恢复之后：清理面板的已删标记要跟着放掉，再重载列表；从回收站对话框
    // 恢复的还要再拉一次清理分析状态。
    async afterTrashRestore(videoID, fromTrashDialog) {
      this.forgetCleanupTrashed(videoID);
      await this.reloadCurrentView();
      if (fromTrashDialog) await this.refreshCleanupStatus();
    },
    async refreshAITagSummary() {
      try {
        this.aiTagSummary = await GetAITaggingStatusSummary() || { same_source_unread: 0 };
      } catch (err) {
        this.aiTagSummary = { ...this.aiTagSummary, same_source_unread: 0 };
      }
    },
    async handleAITagCandidatesChanged() {
      this.aiTagReviewDialog.dirty = true;
      this.$emit('reload-tags');
      await this.refreshAITagSummary();
    },
    runIncrementalScan() {
      return this.$refs.incrementalScanBar?.runIncrementalScan();
    },
    async removeTag(video, tag) {
      try {
        await RemoveTagFromVideo(video.id, tag.id);
        await this.reloadCurrentView();
      } catch (err) {
        console.error('移除标签失败:', err);
        notifyError('移除标签失败: ' + err);
      }
    },
    requestDeleteTag(tag) {
      this.tagDeleteDialog = { show: true, tag };
    },
    async confirmDeleteTag(tag) {
      if (!tag) {
        this.tagDeleteDialog.show = false;
        return;
      }
      try {
        const { DeleteTag } = await import('../../wailsjs/go/main/App');
        await DeleteTag(tag.id);
        this.selectedTags = this.selectedTags.filter(id => id !== tag.id);
        this.$emit('reload-tags');
        await this.reloadCurrentView();
        this.tagDeleteDialog.show = false;
        notify('标签已删除');
      } catch (err) {
        console.error('删除标签失败:', err);
        notifyError('删除标签失败: ' + err);
      }
    },
    handleScanComplete() {
      this.$emit('reload-tags');
      this.$emit('reload-directories');
      this.reloadCurrentView();
    },
    handleTagsChanged() {
      this.$emit('reload-tags');
    },
    handleTagAdded() {
      this.$emit('reload-tags');
      this.reloadCurrentView();
    }
  }
};
</script>
