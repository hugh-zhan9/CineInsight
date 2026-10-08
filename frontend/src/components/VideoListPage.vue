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
      :selected-people="selectedPeople"
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
      @update:selected-people="updateSelectedPeople"
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
      @batch-subtitle="generateSubtitlesForSelected"
      @batch-delete="confirmBatchDelete"
    />
    <IncrementalScanBar
      ref="incrementalScanBar"
      :migration-running="migrationRunning"
      :directories="directories"
      :reload-view="reloadAfterScan"
      @reload-directories="$emit('reload-directories')"
      @state-change="incrementalScan = $event"
    />

    <!-- 「路径失效」按原因分组（D-PC06、LIB-10）：标题与计数来自 ListStaleReasonCounts，点一组只看这一组。 -->
    <div v-if="smartView === 'stale'" class="library-notice stale-reason-bar" role="group" aria-label="按失效原因筛选" data-test="stale-reason-bar">
      <span class="library-notice__title">按原因</span>
      <button
        type="button"
        :class="['tag-chip', { active: !staleReasonFilter }]"
        data-test="stale-reason-all"
        @click="setStaleReasonFilter('')"
      >全部 {{ staleReasonTotal }}</button>
      <button
        v-for="group in staleReasonGroups"
        :key="group.reason"
        type="button"
        :class="['tag-chip', { active: staleReasonFilter === group.reason }]"
        :data-test="`stale-reason-${group.reason}`"
        @click="setStaleReasonFilter(group.reason)"
      >{{ group.label }} {{ group.count }}</button>
      <button
        type="button"
        class="btn-secondary btn-compact library-notice__action"
        :disabled="selectedVideoIds.length === 0 || recheckingStale"
        data-test="stale-recheck-selected"
        @click="recheckSelectedStale"
      >{{ recheckingStale ? '正在重新检查…' : `重新检查所选（${selectedVideoIds.length}）` }}</button>
    </div>

    <!-- 「无字幕」视图与字幕搜索先用缓存的索引（D-PC23、MEDIA-14）：说清上次同步时间，给「立即同步」。 -->
    <div v-if="subtitleIndexNoticeVisible" class="library-notice" role="status" data-test="subtitle-index-sync-bar">
      <span data-test="subtitle-index-sync-text">{{ subtitleIndexSyncText }}</span>
      <button
        type="button"
        class="btn-secondary btn-compact library-notice__action"
        :disabled="subtitleIndexSyncBusy"
        data-test="subtitle-index-sync-now"
        @click="syncSubtitleIndexNow"
      >{{ subtitleIndexSyncBusy ? '同步中…' : '立即同步' }}</button>
    </div>

    <!-- 播放失败（D-PC11、PLAY-12）：那一行就地标成失效，这里说明原因并给下一步，不整页重载。 -->
    <div v-if="playFailureNotice" class="library-notice library-notice--warning" role="alert" data-test="play-failure-notice">
      <span>{{ playFailureNotice.text }}</span>
      <button type="button" class="btn-secondary btn-compact" :disabled="recheckingStale" data-test="play-failure-recheck" @click="recheckVideos([playFailureNotice.videoID])">重新检查</button>
      <button type="button" class="btn-secondary btn-compact" data-test="play-failure-open-stale" @click="openStaleView(playFailureNotice.staleReason)">查看路径失效</button>
      <button type="button" class="library-notice__close" aria-label="关闭提示" @click="playFailureNotice = null">×</button>
    </div>

    <!-- 保存视图引用的标签或人物已被删除（D-PC35、LIB-15）：照常应用其余条件，并说清少了几个。 -->
    <div v-if="savedViewNotice" class="library-notice library-notice--warning" role="status" data-test="saved-view-notice">
      <span>保存视图「{{ savedViewNotice.name }}」有 {{ savedViewNotice.dropped }} 个条件已失效（引用的标签或人物已删除），已忽略。</span>
      <button type="button" class="btn-secondary btn-compact" data-test="saved-view-notice-update" @click="openSaveViewDialog('update')">用当前条件更新视图</button>
      <button type="button" class="library-notice__close" aria-label="关闭提示" @click="savedViewNotice = null">×</button>
    </div>

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
      kind="video"
      :after-restore="afterTrashRestore"
    />

    <SubtitleGenerateDialog
      ref="subtitleTasks"
      @generating-change="generatingSubtitleIds = $event"
    />

    <SubtitleTranslateDialog
      ref="subtitleTranslate"
      @translated="handleSubtitleTranslated"
      @translating-change="handleTranslatingChange"
      @translate-progress="translatingSubtitlePercent = Number($event?.percent || 0)"
    />

    <RandomPickBanner
      :random-pick="randomPick"
      :random-pick-size="randomPickSize"
      :video-count="videos.length"
      :random-play="randomPlayBanner"
      @reshuffle="reshuffleRandomPick"
      @exit="exitRandomPick"
      @reroll="rerollRandomPlay"
      @dismiss-play="dismissRandomPlay"
    />

    <div class="video-list" ref="videoList">
      <!-- 三种空状态（D-PC58、APP-10）：没配目录、筛选没命中、目录里确实没有视频，各配一个能点的下一步。 -->
      <div v-if="videos.length === 0 && !loading" class="empty-state" :data-test="`library-empty-${emptyStateKind}`">
        <template v-if="emptyStateKind === 'no-directories'">
          <p>还没有扫描目录。添加一个存放视频的文件夹，就能开始整理片库。</p>
          <button type="button" class="btn-primary" data-test="library-empty-add-directory" @click="showScanDialog = true">添加扫描目录</button>
        </template>
        <template v-else-if="emptyStateKind === 'semantic-idle'">
          <p>用一句话描述想找的画面或情节，回车开始语义搜索。</p>
        </template>
        <template v-else-if="emptyStateKind === 'no-match'">
          <p>没有符合条件的视频</p>
          <button type="button" class="btn-secondary" data-test="library-empty-clear-conditions" @click="clearAllConditions">清除条件</button>
        </template>
        <template v-else>
          <p>扫描目录里还没有找到视频</p>
          <button type="button" class="btn-secondary" :disabled="migrationRunning || incrementalScan.running" data-test="library-empty-rescan" @click="runIncrementalScan">重新扫描</button>
        </template>
      </div>
      <VirtualVideoList
        v-else-if="videos.length > 0"
        ref="virtualList"
        :items="videos"
        :loading="loading || refreshingInPlace"
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
            :override-kinds="overrideKindsFor(video)"
            @preview="openPreview"
            @play="playVideo"
            @toggle-favorite="toggleVideoFavorite"
            @toggle-liked="toggleVideoLiked"
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
      ref="previewDrawer"
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
      @media-deleted="handlePersonMediaDeleted"
      @media-restored="handlePersonMediaRestored"
      @playback-attempted="applyPlaybackAttemptResult"
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
      :video-extensions="settings.video_extensions || ''"
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
      :saved-views="savedViews"
      :active-view-id="lastAppliedSavedViewID"
    />

    <!-- 迁移到扫描目录之外（D-PC10、LIB-09）：先说清迁完会从片库里消失，可顺手把目标加进扫描目录。 -->
    <BaseModal v-if="moveTargetConfirm.show" close-on-overlay stop-modal-clicks aria-label="确认迁移目标" data-test="move-target-confirm" @close="resolveMoveTargetConfirm(false)">
      <h2>目标不在扫描目录中</h2>
      <p>「{{ moveTargetConfirm.targetLabel }}」不在任何扫描目录里。迁移后，{{ moveTargetConfirm.subject }}会从片库列表中消失，记录进入「路径失效 · 不在任何扫描目录」。</p>
      <label class="checkbox-label">
        <input v-model="moveTargetConfirm.addToRoots" type="checkbox" data-test="move-target-add-root" />
        <span>同时把目标加入扫描目录</span>
      </label>
      <div class="modal-actions">
        <button type="button" class="btn-secondary" @click="resolveMoveTargetConfirm(false)">取消</button>
        <button type="button" class="btn-primary" data-test="move-target-continue" @click="resolveMoveTargetConfirm(true)">继续迁移</button>
      </div>
    </BaseModal>

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
      @person-converted="handleTagPersonConverted"
      @conversion-undone="handleTagConversionUndone"
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
      @open-cleanup="openCleanupFromReview"
    />

    <CleanupReviewPanel
      ref="cleanupPanel"
      :perceptual-hash-running="perceptualHash.running"
      :frame-hash-running="frameHash.running"
      :trash-videos="trashCleanupVideos"
      :after-trash-videos="afterTrashCleanupVideos"
      @start-perceptual-hash="startPerceptualHashBackfill"
      @start-frame-hash="startFrameHashBackfill"
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

.loading-indicator,
.no-more-indicator {
  padding: 14px 0;
  text-align: center;
  color: var(--text-muted);
  font-size: 12px;
}

.loading-indicator p,
.no-more-indicator p { margin: 0; }

.empty-state .btn-primary,
.empty-state .btn-secondary { margin-top: 10px; }

/* 片库页的就地提示条：失效分组、播放失败、保存视图失效条件。与增量扫描状态条同一视觉。 */
.library-notice {
  margin: 10px 0 0;
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px 8px;
  padding: 8px 10px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius);
  background: var(--control-bg);
  color: var(--text-secondary);
  font-size: 13px;
}
.library-notice--warning {
  border-color: var(--warning-border);
  color: var(--warning-color);
}
.library-notice__title { font-weight: 600; color: var(--text-primary); }
.library-notice__action { margin-left: auto; }
.library-notice__close {
  margin-left: auto;
  border: 0;
  background: transparent;
  color: var(--text-muted);
  cursor: pointer;
  font-size: 18px;
}
</style>

<script>
import { SearchLibraryVideoPage, CountLibraryVideos, GetSemanticIndexStatus, SearchSemanticVideos, FindSimilarVideos, ListRecentlyPlayedWithFilter, ListContinueWatchingWithFilter, GetAutomaticOverrideKinds, GetLibrarySubtitleHits, PlayVideo, PlayRandomVideoWithFilter, RerollRandom, PickRandomVideos, GetVideosByIDs, SelectVideoFile, RelocateVideo, ListSubtitleBackups, RestoreSubtitleBackup, GetSubtitleIndexSyncStatus, SyncSubtitleIndexNow, SetVideoFavorite, SetVideoLiked, SetVideoWatched, UpdateVideoWatchProgress, ListSavedLibraryViews, DeleteSavedLibraryView, FilterActiveTagIDs, FilterActivePersonIDs, GetPersonDetail, OpenDirectory, RemoveTagFromVideo, UpdateSettings, MoveVideo, BatchMoveVideos, MoveDirectory, CheckMoveTarget, SelectMigrationSourceDirectory, SelectMigrationDestinationDirectory, ListStaleReasonCounts, RecheckVideos, ReaddRemovedRoot, ValidateScanDirectory, AddDirectory, RetryAITagging, GetEnhancementCapability, GetPreviewSession, PreviewExternally, CreatePlaybackProxy, BatchCreatePlaybackProxies, BatchCreatePlaybackProxiesForFilter } from '../../wailsjs/go/main/App';
import ScanDialog from './ScanDialog.vue';
import TagManagerDialog from './TagManagerDialog.vue';
import AddTagDialog from './AddTagDialog.vue';
import DeleteConfirmDialog from './DeleteConfirmDialog.vue';
import TagDeleteDialog from './TagDeleteDialog.vue';
import PreviewDrawer from './PreviewDrawer.vue';
import SubtitleWorkbench from './SubtitleWorkbench.vue';
import VirtualVideoList from './VirtualVideoList.vue';
import VideoListRow, { STALE_REASON_LABELS } from './VideoListRow.vue';
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
import SaveViewDialog, { parseSavedViewIDs } from './video-list/SaveViewDialog.vue';
import SubtitlePreviewModal from './video-list/SubtitlePreviewModal.vue';
import CleanupReviewPanel from './video-list/CleanupReviewPanel.vue';
import SubtitleGenerateDialog from './video-list/SubtitleGenerateDialog.vue';
import SubtitleTranslateDialog from './video-list/SubtitleTranslateDialog.vue';
import EnhanceDialog from './video-list/EnhanceDialog.vue';
import { logFrontend } from '../utils/frontendLog.js';
import { defaultRangeEngine, estimateVideoRowHeight } from '../utils/virtualList.js';
import BaseMenu from './ui/BaseMenu.vue';
import BaseModal from './ui/BaseModal.vue';
import { patchVideoFromDetails } from '../utils/mediaDetails.js';
import { shortcutActionForEvent } from '../utils/keyboardShortcuts.js';
import { findCommand, registerCommands, unregisterCommands } from '../utils/commandRegistry.js';
import { confirmAction, notify, notifyError } from '../utils/feedback.js';
import { PLAYBACK_PROXY_CODE_LABELS, playbackProxyBatchSummary } from '../utils/playbackProxy.js';
import { resumable, resumePosition } from '../utils/watchState.js';
import { scrubAbsolutePaths } from '../utils/pathText.js';
import { formatLocalTime, subtitleExceptionText } from '../utils/subtitleTools.js';
import { loadRandomMode, loadRecentRandomIDs, normalizeRecentRandomIDs, RANDOM_REROLL_WINDOW_MS, rerollWindowOpen, saveRandomMode, saveRecentRandomIDs } from '../utils/randomPlayback.js';

// 「随机 N 部」一次抽取的条数。
const RANDOM_PICK_SIZE = 10;
// 智能视图「本地资料有更新」（后端 LibraryViewLocalMetadataUpdated）。
const LOCAL_METADATA_UPDATED_VIEW = 'local_metadata_updated';
// UpdateVideoWatchProgress 的起播来源（D-PC42）：从断点、从片头、从字幕命中或指定时间。
const WATCH_PROGRESS_ORIGINS = ['resume', 'start', 'jump'];
// 自动对账会改变的行状态：这几个视图里改了这些状态就可能不再属于当前视图，要重载。
const STATE_SENSITIVE_VIEWS = ['favorites', 'liked', 'continue_watching', 'unwatched', 'watched'];
// 分页游标的全部字段及其初值：整表重载、原地刷新从头翻页、装入随机批次时都清成这一份。
const EMPTY_PAGING_CURSOR = Object.freeze({
  cursorScore: 0,
  cursorSize: 0,
  cursorID: 0,
  cursorLastPlayedAt: '',
  cursorRecentPlayedID: 0,
  cursorProgressUpdatedAt: '',
  cursorContinueID: 0,
  libraryCursor: null
});
// 原地刷新每次请求的条数上限：后端分页接口超过 200 会退回默认值，语义检索封顶 100。
const IN_PLACE_REFRESH_PAGE_LIMIT = 100;

// 界面上只显示目录名，不显示绝对路径（G-3）。
function pathBaseName(path) {
  const parts = String(path || '').split(/[\\/]/).filter(Boolean);
  return parts[parts.length - 1] || String(path || '');
}
function pathDirName(path) {
  const text = String(path || '');
  const index = Math.max(text.lastIndexOf('/'), text.lastIndexOf('\\'));
  return index > 0 ? text.slice(0, index) : text;
}

// RelocateVideo 的两种常见失败带着完整路径（G-3）：换成不含路径的中文说法；其余擦掉路径后原样给出。
function relocateErrorText(err) {
  const raw = String(err?.message || err || '');
  if (raw.startsWith('目标文件不存在')) return '重新定位失败：所选文件不存在或无法读取。';
  if (raw.startsWith('目标路径已被其他记录占用')) return '重新定位失败：所选文件已被片库中的另一条记录收录。';
  return '重新定位失败：' + scrubAbsolutePaths(raw);
}

export default {
  name: 'VideoListPage',
  mixins: [wheelForwardingMixin],
  components: { ScanDialog, TagManagerDialog, AddTagDialog, DeleteConfirmDialog, TagDeleteDialog, PreviewDrawer, SubtitleWorkbench, LocalMetadataDialog, VirtualVideoList, VideoListRow, AITagReviewDialog, BackgroundTaskStatusBars, IncrementalScanBar, LibraryToolbar, RandomPickBanner, SemanticNoticeBar, TrashUndoBanner, CleanupReviewPanel, RenameDialogs, SaveViewDialog, SubtitleGenerateDialog, SubtitleTranslateDialog, SubtitlePreviewModal, EnhanceDialog, BaseMenu, BaseModal },
  props: {
    tags: { type: Array, default: () => [] },
    settings: { type: Object, required: true },
    directories: { type: Array, default: () => [] },
    pageActive: { type: Boolean, default: true }
  },
  emits: ['reload-tags', 'update-settings', 'reload-directories', 'person-converted'],
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
      // 「路径失效」视图里按原因筛（D-PC06）：'' 为全部，unknown 为原因未记录。
      staleReasonFilter: '',
      staleReasonCounts: {},
      recheckingStale: false,
      savedViews: [],
      selectedSavedViewID: 0,
      // 最近一次应用的保存视图：条件改动后 selectedSavedViewID 会清零，「用当前条件更新」仍要知道是哪一个。
      lastAppliedSavedViewID: 0,
      // 应用保存视图时被剔除的条件数（D-PC35）：{ name, dropped }。
      savedViewNotice: null,
      filteredCount: null,
      libraryTotalCount: null,
      countToken: 0,
      // 随机模式与「最近 12 次」排除表存在本机（D-PC44），重启后仍然生效。
      randomMode: loadRandomMode(),
      recentRandomVideoIDs: loadRecentRandomIDs(),
      randomPick: { active: false, ids: [], reason: '', loading: false },
      // 最近一次随机播放（D-PC43、D-PC44）：token 是 30 秒内「换一个」用的令牌，startedAt 为本机时刻。
      randomPlay: { active: false, video: null, reason: '', token: '', startedAt: 0, rerollable: false, loading: false },
      randomPickToken: 0,
      selectedTags: [],
      // 人物筛选（D-PC33）：[{ id, name }]，与工具栏的 selected-people 双向对应；筛选 DTO 只带 id。
      selectedPeople: [],
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
      // 「继续观看」键集游标（D-PC42）：上一页最后一行的 watch_progress_updated_at 原样回传，外加 id。
      cursorProgressUpdatedAt: '',
      cursorContinueID: 0,
      libraryCursor: null,
      // 行标签「手动」角标（D-PC36）：视频 ID（字符串）→ 被人工覆盖、结果为「加上」的自动标签种类。
      automaticOverrideKinds: {},
      pageSize: 20,
      loading: false,
      hasMore: true,
      // 列表当前装的是哪一组查询条件（virtualListQueryKey）。重载时条件没变就原地刷新、停在原位，
      // 条件变了才清空回顶；随机批次装进来时置空，退出批次一定回顶。
      listQueryKey: '',
      // 原地刷新期间列表照常显示，不出「加载中」；分页游标被刷新占用，触底加载要等它结束。
      refreshingInPlace: false,
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
      // 清理面板删除的结果：删除与撤销条分两步，中间留给面板收窄勾选。
      pendingCleanupDeleteOutcome: null,
      tagDeleteDialog: { show: false, tag: null },
      aiTagReviewDialog: { show: false, dirty: false },
      // 播放失败后就地标成失效的那一行（D-PC11）：{ videoID, staleReason, text }。
      playFailureNotice: null,
      // 迁移目标不在扫描目录里时的确认（D-PC10）。
      moveTargetConfirm: { show: false, targetLabel: '', subject: '', addToRoots: false },
      // 超分运行时状态（D-PC24）：明确不可用时行菜单标「未就绪」并指向设置页。
      enhanceCapability: null,
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
      // 翻译弹窗是单例，正在翻译哪个视频由它镜像过来，供行菜单禁用入口；进度也镜像过来标在菜单上（MEDIA-12）。
      translatingSubtitleVideoId: null,
      translatingSubtitlePercent: 0,
      // 字幕索引同步状态（D-PC23）：「无字幕」视图与字幕搜索上显示「上次同步时间 · 立即同步」。
      subtitleIndexSync: null,
      subtitleIndexSyncRequesting: false,
      runtimeOffHandlers: [],
      searchDebounceTimer: null,
    };
  },
  mounted() {
    this.configureHomeListVirtualization();
    this.listQueryKey = this.virtualListQueryKey;
    this.loadVideos();
    this.refreshLibraryCounts();
    this.loadSemanticStatus();
    this.loadSavedLibraryViews();
    this.loadEnhancementCapability();
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
        // 语义模式下随机会忽略搜索词、从全库抽取（PLAY-08），与工具栏按钮同样禁用。
        enabled: () => !this.randomPick.loading && this.searchMode !== 'semantic',
        run: () => this.playRandom()
      },
      // 待处理工作台按固定 ID 跳到本页的既有面板（详细设计 §6.1）。run() 只负责打开面板，
      // 切到片库页由调用方先做（本页常挂载、以 v-show 切换）。
      {
        id: 'library.openCleanup',
        group: 'action',
        label: '打开清理审阅',
        keywords: ['cleanup', '清理', '重复'],
        run: () => this.openCleanupDialog()
      },
      {
        id: 'library.openAIReview',
        group: 'action',
        label: '打开 AI 标签审阅',
        keywords: ['ai', 'tag', 'review', '审阅', '标签'],
        run: () => this.openAITagReviewDialog()
      },
      {
        id: 'library.openLocalMetadataUpdates',
        group: 'action',
        label: '查看本地资料有更新的视频',
        keywords: ['nfo', 'local metadata', '本地资料'],
        run: () => this.applySmartViewCommand(LOCAL_METADATA_UPDATED_VIEW)
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
        // restored 这一路是「删掉扫描目录后被标失效的记录又回来了」：只做恢复的那轮
        // 对账在别的计数上全是 0，漏掉它列表就不刷新，用户会以为数据没回来。
        if (!result || result.error_count > 0 || result.added > 0 || result.relocated > 0 || result.stale > 0 || result.restored > 0 || result.metadata_refreshed > 0) {
          this.reloadCurrentView();
        }
      });

      // 启动扫描、改目录触发的扫描与别处发起的手动扫描都在这里汇报（D-PC09、LIB-08）。
      this.registerRuntimeEvent('library-scan-summary', (event) => this.handleLibraryScanSummary(event));
      // 后台字幕索引同步完成（D-PC23）：更新「上次同步时间」，正停在依赖索引的视图上就刷新一次。
      this.registerRuntimeEvent('subtitle-index-synced', (status) => this.handleSubtitleIndexSynced(status));
      // 播放失败后的后台重定位找到了新位置（D-PC11）：就地恢复那一行。
      this.registerRuntimeEvent('video-relocated', (event) => this.handleVideoRelocated(event));
      this.registerRuntimeEvent('video-enhancement-capability', (capability) => {
        this.enhanceCapability = capability || null;
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
    randomMode(mode) {
      saveRandomMode(mode);
    },
    subtitleIndexNoticeVisible: {
      immediate: true,
      handler(visible) {
        if (visible) this.loadSubtitleIndexSyncStatus();
      }
    },
    // 进「路径失效」视图时取一次各原因的计数；离开时放掉按原因的筛选。
    smartView(view) {
      if (view === 'stale') {
        this.loadStaleReasonCounts();
      } else {
        this.staleReasonFilter = '';
      }
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
    clearTimeout(this._randomRerollTimer);
    unregisterCommands('video-list');
	window.removeEventListener('keydown', this.handleLibraryShortcut);
	window.removeEventListener('keydown', this.handleToolbarShortcut);
    this.detachWheelFallback();
    if (this.searchDebounceTimer) {
      clearTimeout(this.searchDebounceTimer);
    }
    this.teardownRuntimeEvents();
  },
  computed: {
    randomPickSize() {
      return RANDOM_PICK_SIZE;
    },
    subtitleIndexNoticeVisible() {
      return this.smartView === 'no_subtitle' || this.isSubtitleSearchActive();
    },
    subtitleIndexSyncBusy() {
      return this.subtitleIndexSyncRequesting || !!this.subtitleIndexSync?.running;
    },
    subtitleIndexSyncText() {
      const status = this.subtitleIndexSync;
      if (status?.running) return '正在后台同步字幕索引，完成后列表会自动刷新。';
      const parts = [];
      parts.push(status?.last_synced_at
        ? `字幕索引上次同步：${formatLocalTime(status.last_synced_at)}（每 10 分钟最多自动同步一次）。`
        : '字幕索引本次启动后还没有同步过，列表先按现有索引显示。');
      if (status?.error) parts.push(`上次同步失败：${scrubAbsolutePaths(status.error)}`);
      return parts.join(' ');
    },
    randomPlayBanner() {
      const play = this.randomPlay;
      if (!play.active || !play.video) return null;
      return {
        active: true,
        videoName: play.video.display_title || play.video.name || `视频 #${play.video.id}`,
        reason: play.reason,
        rerollable: play.rerollable,
        loading: play.loading
      };
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
      // 翻译弹窗一次只跑一个，所以翻译期间整条入口都禁用，而不是点了没反应。
      // 但「进行中」只标在真正在翻译的那一行，否则每一行都像是自己在翻译。
      const translating = this.translatingSubtitleVideoId !== null;
      const translatingThisVideo = this.translatingSubtitleVideoId === video.id;
      // 失效行的处理（D-PC06、LIB-10）：重新检查走窄对账；目录被移除或不在扫描范围的可以把目录加回来。
      const staleItems = video.is_stale ? [
        { id: 'recheck', label: '重新检查', disabled: this.recheckingStale },
        // 文件被挪到别处、自动查找没找到时，手动指给它（LIB-10）。
        { id: 'relocate', label: '重新定位文件…' },
        ...(['removed_root', 'outside_roots'].includes(video.stale_reason) ? [{ id: 'readd-root', label: '加回目录…' }] : [])
      ] : [];
      // 超分运行时明确不可用时标「未就绪」，点了去设置页的超分分区看原因（D-PC24、MEDIA-11）。
      const enhanceReady = this.enhanceCapability?.available !== false;
      return [
        { heading: '文件' },
        { id: 'directory', label: '打开目录' },
        { id: 'rename', label: '重命名' },
        { id: 'move', label: '迁移', disabled: this.migrationRunning },
        { id: 'export-nfo', label: '写出 NFO' },
        ...staleItems,
        { heading: '整理' },
        { id: 'like', label: video.is_liked ? '取消点赞' : '点赞' },
        { id: 'ai-reanalyze', label: '重新分析 AI 标签' },
        { heading: '字幕' },
        { id: 'subtitle', label: generating ? '生成字幕（进行中）' : '生成字幕', disabled: generating },
        { id: 'subtitle-translate', label: translatingThisVideo ? `翻译字幕（进行中 ${this.translatingSubtitlePercent}%）` : '翻译字幕…', disabled: generating || translating },
        { id: 'subtitle-edit', label: '编辑字幕' },
        { id: 'subtitle-preview', label: '预览字幕' },
        // 生成、翻译、工作台保存覆盖字幕前都会备份（D-PC13、MEDIA-05）；这里恢复最近一份，更早的在工作台「历史版本」里。
        { id: 'subtitle-restore', label: '恢复上一版字幕…', disabled: generating || translatingThisVideo },
        { heading: '增强' },
        { id: 'enhance', label: enhanceReady ? '视频超分…' : '视频超分（未就绪）' },
        { id: 'playback-proxy', label: '生成播放代理', disabled: this.playbackProxy.running },
        { divider: true },
        { id: 'delete', label: '删除', danger: true, disabled: this.deletingIds.includes(video.id) }
      ];
    },
    // 空状态三分（D-PC58、APP-10）；语义模式还没输入描述时单独提示，不算「筛选没命中」。
    emptyStateKind() {
      if ((this.directories || []).length === 0) return 'no-directories';
      if (this.searchMode === 'semantic' && !this.currentQueryKeyword() && !this.semanticSimilarVideoID) return 'semantic-idle';
      if (this.hasActiveConditions()) return 'no-match';
      return 'no-videos';
    },
    staleReasonGroups() {
      return Object.keys(STALE_REASON_LABELS)
        .map(reason => ({ reason, label: STALE_REASON_LABELS[reason], count: Number(this.staleReasonCounts?.[reason] || 0) }))
        .filter(group => group.count > 0 || group.reason === this.staleReasonFilter);
    },
    staleReasonTotal() {
      return Object.values(this.staleReasonCounts || {}).reduce((sum, count) => sum + Number(count || 0), 0);
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
        staleReason: this.staleReasonFilter,
        tags: [...this.selectedTags].sort((a, b) => a - b),
        people: this.selectedPeople.map(person => Number(person.id)).sort((a, b) => a - b),
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
      // 同步可能把断点判成了看完：那时位置回 0 且带 watched，行上要同时补「已看」，
      // 否则只看到进度条消失、徽标要等下次整页重载才出现。
      const byID = new Map(changes.map(item => [
        Number(item.video_id),
        { position: Number(item.watch_position_seconds) || 0, watched: !!item.watched }
      ]));
      let applied = 0;
      for (const video of this.videos) {
        const change = byID.get(Number(video.id));
        if (!change) continue;
        if (video.watch_position_seconds === change.position && (!change.watched || video.is_watched)) continue;
        video.watch_position_seconds = change.position;
        if (change.watched) video.is_watched = true;
        applied++;
      }
      const snapshotChange = this.previewVideoSnapshot ? byID.get(Number(this.previewVideoSnapshot.id)) : null;
      if (snapshotChange) {
        this.previewVideoSnapshot = {
          ...this.previewVideoSnapshot,
          watch_position_seconds: snapshotChange.position,
          is_watched: snapshotChange.watched || this.previewVideoSnapshot.is_watched
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
    // 删除经撤销条执行（进度、取消、不支持废纸篓时的二选一）；返回给面板的 result 保持
    // 面板认识的 { succeeded, failed, errors[{video_id, error}] } 形状。
    // 面板传来 { names: {[videoId]: 文件名} }（P-032 约定）：候选多半不在当前已加载的列表里，
    // 「不支持废纸篓」的二选一弹窗要靠它列出文件名；已加载的行只作补充。
    async trashCleanupVideos(selectedIDs, { names = {} } = {}) {
      this.deletingIds = [...new Set([...this.deletingIds, ...selectedIDs])];
      const outcome = await this.$refs.trashUndo.runDelete({ ids: selectedIDs, deleteFile: true, names: { ...this.videoNames(selectedIDs), ...(names || {}) } });
      const succeededIDs = outcome.removedIDs;
      const failedIDs = new Set(outcome.remainingIDs);
      this.videos = this.videos.filter(item => !succeededIDs.includes(item.id));
      this.pendingCleanupDeleteOutcome = outcome;
      return { result: this.cleanupBatchResult(selectedIDs, outcome), failedIDs, succeededIDs };
    },
    cleanupBatchResult(requestedIDs, outcome) {
      const errors = [
        ...outcome.failures.map(item => ({ video_id: item.id, error: item.text })),
        ...outcome.kept.map(id => ({ video_id: id, error: '所在磁盘不支持废纸篓，已保留' })),
        ...outcome.cancelled.map(id => ({ video_id: id, error: '已取消，未处理' }))
      ];
      return { requested: requestedIDs.length, succeeded: outcome.removedIDs.length, failed: errors.length, errors };
    },
    // 面板收窄完勾选之后才走这一步，顺序与拆分前一致。失败项由清理面板自己报告。
    async afterTrashCleanupVideos() {
      const outcome = this.pendingCleanupDeleteOutcome;
      this.pendingCleanupDeleteOutcome = null;
      if (outcome) this.$refs.trashUndo?.showDeleteNotice(outcome, { reportFailures: false });
      await this.reloadCurrentView();
    },
    videoNames(ids) {
      const wanted = new Set(ids);
      const names = {};
      for (const video of this.videos) {
        if (wanted.has(video.id)) names[video.id] = video.name;
      }
      return names;
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
    openSubtitleTranslate(video) {
      return this.$refs.subtitleTranslate?.translate(video);
    },
    async handleSubtitleTranslated() {
      // 译文覆盖了同一个 .srt，字幕索引已经重建，列表里的字幕命中要跟着刷新。
      await this.reloadCurrentView();
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
    // 批量生成字幕（D-PC23、MEDIA-14）：只有已加载的选中行才有名字；没加载到的按 ID 补上，名字由队列回报。
    generateSubtitlesForSelected() {
      const byID = new Map(this.videos.map(video => [Number(video.id), video]));
      const videos = [...new Set(this.selectedVideoIds.map(Number))]
        .filter(Boolean)
        .map(id => byID.get(id) || { id, name: `视频 #${id}` });
      return this.$refs.subtitleTasks?.generateBatch(videos);
    },
    handleTranslatingChange(videoID) {
      this.translatingSubtitleVideoId = videoID;
      this.translatingSubtitlePercent = 0;
    },
    // 行菜单「恢复上一版字幕」（D-PC13、MEDIA-05）：恢复最近一份备份；恢复前当前字幕也会被备份，所以可以再换回来。
    async restoreLatestSubtitleBackup(video) {
      if (!video?.id) return;
      let backups;
      try {
        backups = await ListSubtitleBackups(video.id) || [];
      } catch (err) {
        notifyError('读取字幕备份失败：' + subtitleExceptionText(err));
        return;
      }
      const latest = backups[0];
      if (!latest) {
        notify('这个视频还没有字幕备份。生成、翻译或在工作台保存覆盖字幕时，会自动备份最近 5 份。');
        return;
      }
      const name = video.display_title || video.name;
      const more = backups.length > 1 ? `\n共有 ${backups.length} 份备份，更早的版本可在「编辑字幕」的「历史版本」里恢复。` : '';
      const confirmed = await confirmAction({
        title: '恢复上一版字幕',
        message: `把「${name}」的字幕恢复到 ${formatLocalTime(latest.created_at)} 的版本？当前的字幕会先备份，之后仍可再换回来。${more}`,
        confirmText: '恢复'
      });
      if (!confirmed) return;
      try {
        const result = await RestoreSubtitleBackup(video.id, latest.id);
        const warnings = Array.isArray(result?.warnings) && result.warnings.length ? `\n${result.warnings.join('\n')}` : '';
        notify(`已把「${name}」的字幕恢复到 ${formatLocalTime(latest.created_at)} 的版本。${warnings}`);
        await this.reloadCurrentView();
      } catch (err) {
        notifyError('恢复字幕失败：' + subtitleExceptionText(err));
      }
    },
    // ===== 字幕索引同步（D-PC23、MEDIA-14）=====
    async loadSubtitleIndexSyncStatus() {
      try {
        const status = await GetSubtitleIndexSyncStatus();
        if (status) this.subtitleIndexSync = status;
      } catch (err) {
        this.debugLog('loadSubtitleIndexSyncStatus failed', { err: String(err) }, true);
      }
    },
    async syncSubtitleIndexNow() {
      if (this.subtitleIndexSyncBusy) return;
      this.subtitleIndexSyncRequesting = true;
      try {
        const status = await SyncSubtitleIndexNow();
        if (status) this.subtitleIndexSync = status;
      } catch (err) {
        notifyError('同步字幕索引失败：' + subtitleExceptionText(err));
      } finally {
        this.subtitleIndexSyncRequesting = false;
      }
    },
    async handleSubtitleIndexSynced(status) {
      if (status) this.subtitleIndexSync = { ...status, running: false };
      if (this.subtitleIndexNoticeVisible) await this.reloadCurrentView();
    },
    renameVideo(video) {
      return this.$refs.renameDialogs?.openRenameVideo(video);
    },
    async moveVideo(video) {
      if (!video || this.migrationRunning) return;
      const destination = await SelectMigrationDestinationDirectory();
      if (!destination) return;
      const target = await this.confirmMoveTarget(destination, '这个视频');
      if (!target.proceed) return;
      this.migrationRunning = true;
      try {
        const result = await MoveVideo(video.id, destination);
        if (target.addToRoots) await this.addScanRoot(destination);
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
      const target = await this.confirmMoveTarget(destination, `选中的 ${ids.length} 个视频`);
      if (!target.proceed) return;
      this.migrationRunning = true;
      try {
        const result = await BatchMoveVideos(ids, destination);
        if (target.addToRoots && Number(result?.succeeded || 0) > 0) await this.addScanRoot(destination);
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
      // 迁移的正是某个扫描目录本身时，扫描目录会跟着改写到新位置，不会从片库里消失。
      const sourceIsRoot = (this.directories || []).some(dir => String(dir?.path || '') === String(source));
      const target = sourceIsRoot ? { proceed: true, addToRoots: false } : await this.confirmMoveTarget(destinationParent, '这个文件夹里的视频');
      if (!target.proceed) return;
      if (!await confirmAction({ title: '迁移文件夹', message: `将文件夹\n${source}\n迁移到\n${destinationParent}\n并同步更新库内路径，是否继续？`, confirmText: '迁移' })) return;
      this.migrationRunning = true;
      try {
        const result = await MoveDirectory(source, destinationParent);
        if (target.addToRoots) await this.addScanRoot(destinationParent);
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
    // 重命名成功后把已加载的那一行改名，避免整表重载让它跳位置。只换路径的最后一段：
    // 目录名里恰好含同一个文件名时，字符串替换会改错位置。
    applyVideoRename({ video, finalName }) {
      const idx = this.videos.findIndex(v => v.id === video.id);
      if (idx !== -1) {
        const path = String(video.path || '');
        this.videos[idx].name = finalName;
        this.videos[idx].path = path.endsWith(video.name) ? path.slice(0, path.length - video.name.length) + finalName : path;
      }
    },
    // 迁移目标不在任何扫描目录里时先确认（D-PC10、LIB-09）。返回 { proceed, addToRoots }。
    async confirmMoveTarget(destination, subject) {
      let check;
      try {
        check = await CheckMoveTarget(destination);
      } catch (err) {
        notifyError('检查迁移目标失败: ' + err);
        return { proceed: false, addToRoots: false };
      }
      if (check?.in_scan_roots) return { proceed: true, addToRoots: false };
      return new Promise(resolve => {
        this._moveTargetResolve = resolve;
        this.moveTargetConfirm = { show: true, targetLabel: pathBaseName(destination), subject, addToRoots: false };
      });
    },
    resolveMoveTargetConfirm(proceed) {
      const resolve = this._moveTargetResolve;
      const addToRoots = !!this.moveTargetConfirm.addToRoots;
      this._moveTargetResolve = null;
      this.moveTargetConfirm = { show: false, targetLabel: '', subject: '', addToRoots: false };
      resolve?.({ proceed: !!proceed, addToRoots: !!proceed && addToRoots });
    },
    // 把一个目录加入扫描目录：加入后后端在后台对它做一次窄对账，结果经 library-watcher-reconciled 回来。
    async addScanRoot(path) {
      try {
        await AddDirectory(path, pathBaseName(path));
        this.$emit('reload-directories');
        notify(`已把「${pathBaseName(path)}」加入扫描目录。`);
        return true;
      } catch (err) {
        notifyError('加入扫描目录失败: ' + err);
        return false;
      }
    },
    // 扫描目录在界面上用别名称呼，没有别名时用目录名，不显示绝对路径。
    directoryLabel(path) {
      const dir = (this.directories || []).find(item => String(item?.path || '') === String(path || ''));
      return String(dir?.alias || '').trim() || pathBaseName(path);
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
      this.staleReasonFilter = '';
      this.selectedTags = [];
      this.selectedPeople = [];
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
    // 片库总数只在为空时才重新取（refreshLibraryCounts）；扫描与重新检查之后置空让它重取。
    // 随机批次里不走 refreshLibraryCounts，置空会让结果条一直没有总数，所以批次里先不动。
    invalidateLibraryTotal() {
      if (!this.randomPick.active) this.libraryTotalCount = null;
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
        person_ids: [],
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
      // 「用当前条件更新」「重命名」（D-PC35、LIB-15）：弹窗同一个，模式不同。
      if (item.id === 'update-current' || item.id === 'rename-current') {
        this.openSaveViewDialog(item.id === 'update-current' ? 'update' : 'rename');
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
        case 'recheck': this.recheckVideos([video.id]); break;
        case 'readd-root': this.readdRemovedRoot(video); break;
        case 'relocate': this.relocateVideoFile(video); break;
        case 'like': this.toggleVideoLiked(video); break;
        case 'ai-reanalyze': this.reanalyzeVideo(video); break;
        case 'subtitle': this.generateSubtitle(video); break;
        case 'subtitle-translate': this.openSubtitleTranslate(video); break;
        case 'subtitle-edit': this.openSubtitleWorkbench(video); break;
        case 'subtitle-preview': this.openSubtitlePreview(video); break;
        case 'subtitle-restore': this.restoreLatestSubtitleBackup(video); break;
        case 'enhance':
          if (this.enhanceCapability?.available === false) this.openEnhanceSettings();
          else this.openEnhanceDialog(video);
          break;
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
      return this.selectedTags.length > 0 || this.selectedPeople.length > 0 || this.selectedSizeRange !== 'all' || this.selectedResRange !== 'all' || this.minRating !== '' || this.maxRating !== '' || this.sortMode !== 'balanced';
    },
    // 当前有没有会让结果变少的条件：决定空列表显示「筛选没命中」还是「目录里没有视频」。
    hasActiveConditions() {
      return !!this.currentQueryKeyword() || !!this.smartView || !!this.semanticSimilarVideoID ||
        this.selectedTags.length > 0 || this.selectedPeople.length > 0 ||
        this.selectedSizeRange !== 'all' || this.selectedResRange !== 'all' || this.minRating !== '' || this.maxRating !== '';
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
        person_ids: this.selectedPeople.map(person => Number(person.id)).filter(Boolean),
        stale_reason: this.smartView === 'stale' ? this.staleReasonFilter : '',
        min_size: minSize,
        max_size: maxSize,
        min_height: minHeight,
        max_height: maxHeight,
        min_rating: this.minRating === '' ? null : Number(this.minRating),
        max_rating: this.maxRating === '' ? null : Number(this.maxRating),
        sort_mode: this.sortMode
      };
    },
    // 与后端 applyLibraryFilter 同口径的本地判定：就地改完一行后据此决定它还在不在当前视图里。
    matchesSmartView(video) {
      // 失效记录只属于「路径失效」视图（D-S02），其余视图一律不含。
      if (this.smartView === 'stale') {
        if (!video.is_stale) return false;
        if (!this.staleReasonFilter) return true;
        return (String(video.stale_reason || '') || 'unknown') === this.staleReasonFilter;
      }
      if (video.is_stale) return false;
      switch (this.smartView) {
        case 'favorites': return !!video.is_favorite;
        case 'liked': return !!video.is_liked;
        // 「继续观看」= 断点可续，含重看中的已看片（D-PC42、PLAY-10）。
        case 'continue_watching': return resumable(video);
        case 'unwatched': return !video.is_watched;
        case 'watched': return !!video.is_watched;
        case 'recently_played': return !!video.last_played_at;
        case 'recently_added': {
          const createdAt = new Date(video.created_at || 0).getTime();
          return createdAt > 0 && createdAt >= Date.now() - 30 * 24 * 60 * 60 * 1000;
        }
        // 自动分类标签不算「已打标签」（D-PC33、META-10）。
        case 'untagged': return !(video.tags || []).some(tag => !tag?.automatic_kind);
        case 'no_subtitle': return !this.isSubtitleSearchActive();
        // 「本地资料有更新」取决于 NFO 状态表，行上没有这个字段。状态只会被本地资料弹窗改掉，
        // 那条路径自己重载列表，所以其余就地修改都不会让行离开这个视图。
        case LOCAL_METADATA_UPDATED_VIEW: return true;
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
      if (this.loading || this.refreshingInPlace || !this.hasMore) return;
      this.loading = true;
      try {
        const keyword = this.currentQueryKeyword();
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

        const page = await this.fetchNextVideoPage(this.pageSize, this.videos.length);
        const newVideos = page.videos;

        this.debugLog('loadVideos query resolved', {
          count: newVideos.length,
          sample: newVideos.slice(0, 3).map(video => ({ id: video.id, name: video.name, path: video.path })),
          mode: this.smartView || keyword || this.hasStructuredFilters() ? 'filtered' : 'paginated'
        });

        if (!page.hasMore) {
          this.hasMore = false;
        }
        if (newVideos.length > 0) {
          this.videos.push(...newVideos);
        }
        this.debugLog('loadVideos applied to state', {
          totalVideos: this.videos.length,
          hasMore: this.hasMore
        });
      } catch (err) {
        this.reportVideoLoadFailure(err);
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
    // 从当前分页游标往后取一页并推进游标；取回的行要不要进列表、hasMore 怎么改由调用方决定。
    // offset 只有语义检索用（按条数翻页），其余接口走键集游标。
    async fetchNextVideoPage(limit, offset) {
      const keyword = this.currentQueryKeyword();
      let videos = [];
      let semanticHasMore = null;

      if (this.searchMode === 'semantic') {
        if (!keyword && !this.semanticSimilarVideoID) {
          return { videos, hasMore: false };
        }
        this.semanticSearchError = '';
        const request = {
          filter: this.currentLibraryFilter(),
          offset,
          limit
        };
        const page = this.semanticSimilarVideoID
          ? await FindSimilarVideos({ ...request, video_id: this.semanticSimilarVideoID })
          : await SearchSemanticVideos({ ...request, query: keyword });
        this.semanticCoverage = page?.coverage || null;
        semanticHasMore = !!page?.has_more;
        videos = (page?.hits || []).map(hit => ({ ...hit.video, _semanticScore: hit.score }));
        this.libraryCursor = null;
        await this.fillAutomaticOverrideKinds(videos);
      } else if (this.smartView === 'recently_played' && this.sortMode === 'balanced') {
        videos = await ListRecentlyPlayedWithFilter(
          this.currentLibraryFilter(),
          this.cursorLastPlayedAt,
          this.cursorRecentPlayedID,
          limit
        );
        await this.fillAutomaticOverrideKinds(videos);
      } else if (this.isContinueWatchingKeyset()) {
        // 「继续观看」按最近的观看进度倒序（D-PC42、PLAY-09）：键集游标原样回传上一页最后一行的
        // watch_progress_updated_at 与 id；进度时间为空的老数据排在最后，游标时间传空串。
        const page = await ListContinueWatchingWithFilter(
          this.currentLibraryFilter(),
          this.cursorProgressUpdatedAt,
          this.cursorContinueID,
          limit
        );
        videos = page?.videos || [];
        this.mergeAutomaticOverrideKinds(page?.automatic_override_kinds);
      } else {
        const request = { filter: this.currentLibraryFilter(), limit };
        if (this.libraryCursor) request.cursor = this.libraryCursor;
        const page = await SearchLibraryVideoPage(request);
        videos = page?.videos || [];
        this.libraryCursor = page?.next_cursor || null;
        this.mergeAutomaticOverrideKinds(page?.automatic_override_kinds);
      }

      videos = await this.attachSubtitleHits(videos, keyword);

      const recentKeyset = this.smartView === 'recently_played' && this.sortMode === 'balanced';
      const arrayKeyset = recentKeyset || this.isContinueWatchingKeyset();
      const hasMore = this.searchMode === 'semantic'
        ? semanticHasMore
        : (arrayKeyset ? videos.length >= limit : !!this.libraryCursor);
      if (videos.length > 0) {
        const last = videos[videos.length - 1];
        if (recentKeyset) {
          this.cursorLastPlayedAt = last.last_played_at || '';
          this.cursorRecentPlayedID = last.id;
        } else if (this.isContinueWatchingKeyset()) {
          this.cursorProgressUpdatedAt = last.watch_progress_updated_at || '';
          this.cursorContinueID = last.id;
        }
      }
      return { videos, hasMore };
    },
    reportVideoLoadFailure(err) {
      if (this.searchMode === 'semantic') this.semanticSearchError = String(err);
      this.debugLog('loadVideos failed', { err: String(err) }, true);
      console.error('加载视频失败:', err);
      notifyError('加载视频失败: ' + err);
    },
    pagingCursor() {
      return Object.fromEntries(Object.keys(EMPTY_PAGING_CURSOR).map(key => [key, this[key]]));
    },
    setPagingCursor(cursor) {
      for (const key of Object.keys(EMPTY_PAGING_CURSOR)) this[key] = cursor[key];
    },
    // 同一组条件下的重载（后台对账、扫描汇总、行内操作之后）：列表不清空、不出「加载中」，
    // 从头按当前已加载的条数重取一遍，取齐后整体替换，再把视口顶部那一行放回原位。
    // 取的途中失败或条件变了，就留着旧列表、放回原游标，触底加载照旧往后翻。
    async refreshVideosInPlace() {
      const depth = this.videos.length;
      const queryKey = this.virtualListQueryKey;
      const previousCursor = this.pagingCursor();
      const refreshed = [];
      let hasMore = true;
      this.refreshingInPlace = true;
      this.setPagingCursor(EMPTY_PAGING_CURSOR);
      try {
        while (hasMore && refreshed.length < depth) {
          const limit = Math.min(IN_PLACE_REFRESH_PAGE_LIMIT, depth - refreshed.length);
          const page = await this.fetchNextVideoPage(limit, refreshed.length);
          refreshed.push(...page.videos);
          hasMore = page.hasMore;
          if (page.videos.length === 0) break;
        }
      } catch (err) {
        this.setPagingCursor(previousCursor);
        this.reportVideoLoadFailure(err);
        return;
      } finally {
        this.refreshingInPlace = false;
      }
      if (this.virtualListQueryKey !== queryKey) {
        this.setPagingCursor(previousCursor);
        return;
      }
      const anchor = this.$refs.virtualList?.captureScrollAnchor?.();
      this.videos = refreshed;
      this.hasMore = hasMore;
      const keptIDs = new Set(refreshed.map(video => video.id));
      this.selectedVideoIds = this.selectedVideoIds.filter(id => keptIDs.has(id));
      this.debugLog('refreshVideosInPlace applied', { depth, totalVideos: refreshed.length, hasMore });
      await this.$nextTick();
      this.$refs.virtualList?.restoreScrollAnchor?.(anchor);
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
          const queryKey = this.virtualListQueryKey;
          if (this.videos.length > 0 && queryKey === this.listQueryKey) {
            await this.refreshVideosInPlace();
            continue;
          }
          this.listQueryKey = queryKey;
          this.videos = [];
          this.selectedVideoIds = [];
          this.setPagingCursor(EMPTY_PAGING_CURSOR);
          this.automaticOverrideKinds = {};
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
    // 「继续观看」只在均衡排序下走专用的键集接口；换了排序仍走共享分页（排序语义由用户选）。
    isContinueWatchingKeyset() {
      return this.searchMode !== 'semantic' && this.smartView === 'continue_watching' && this.sortMode === 'balanced';
    },
    overrideKindsFor(video) {
      return this.automaticOverrideKinds[String(video?.id)] || [];
    },
    // 列表载荷（SearchLibraryVideoPage、继续观看）自带角标数据，按页合并进来。
    mergeAutomaticOverrideKinds(kinds) {
      if (!kinds || typeof kinds !== 'object') return;
      const next = { ...this.automaticOverrideKinds };
      for (const [id, value] of Object.entries(kinds)) next[String(id)] = Array.isArray(value) ? value : [];
      this.automaticOverrideKinds = next;
    },
    // 返回数组的接口（最近播放、语义搜索、随机抽取、按 ID 取）没有角标数据，用 GetAutomaticOverrideKinds
    // 批量补齐。只查带自动标签的行：没有自动标签就不会有「手动」角标。补不上只少一个角标，不影响列表。
    async fillAutomaticOverrideKinds(videos) {
      const ids = (videos || [])
        .filter(video => (video?.tags || []).some(tag => tag?.automatic_kind))
        .map(video => Number(video.id))
        .filter(Boolean);
      if (ids.length === 0) return;
      try {
        const kinds = await GetAutomaticOverrideKinds(ids) || {};
        const filled = {};
        for (const id of ids) filled[String(id)] = kinds[String(id)] || [];
        this.mergeAutomaticOverrideKinds(filled);
      } catch (err) {
        this.debugLog('fillAutomaticOverrideKinds failed', { err: String(err) }, true);
      }
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
    // mode：create 另存为新视图；update 用当前条件覆盖；rename 只改名（D-PC35、LIB-15）。
    openSaveViewDialog(mode = 'create') {
      this.$refs.saveViewDialog?.open(mode);
    },
    // 保存成功后刷新视图列表并选中新视图，这两步仍归片库页。更新当前条件后，条件与视图重新一致，
    // 失效条件的提示也就不再成立。
    async afterSavedLibraryView(saved) {
      await this.loadSavedLibraryViews();
      this.selectedSavedViewID = saved.id;
      this.lastAppliedSavedViewID = saved.id;
      this.savedViewNotice = null;
    },
    // 条件改动之后当前条件就不再等于那个保存视图了。
    leaveSavedView() {
      this.selectedSavedViewID = 0;
      this.savedViewNotice = null;
    },
    // 保存视图里的标签与人物先经后端剔除已删除的 ID（与 Jellyfin 同口径），剔掉几个就提示几个。
    async applySelectedSavedView() {
      const view = this.savedViews.find(item => item.id === Number(this.selectedSavedViewID));
      if (!view) return;
      this.deactivateRandomPick();
      const storedTagIDs = parseSavedViewIDs(view.tag_ids_json);
      const storedPersonIDs = parseSavedViewIDs(view.person_ids_json);
      let tagResult;
      let personResult;
      try {
        [tagResult, personResult] = await Promise.all([
          storedTagIDs.length ? FilterActiveTagIDs(storedTagIDs) : { tag_ids: [], dropped: 0 },
          storedPersonIDs.length ? FilterActivePersonIDs(storedPersonIDs) : { person_ids: [], dropped: 0 }
        ]);
      } catch (err) {
        this.selectedSavedViewID = 0;
        notifyError('应用保存视图失败: ' + err);
        return;
      }
      const tagIDs = (tagResult?.tag_ids || []).map(Number).filter(Boolean);
      const people = await this.resolvePeople((personResult?.person_ids || []).map(Number).filter(Boolean));
      const dropped = Number(tagResult?.dropped || 0) + Number(personResult?.dropped || 0);
      this.searchMode = view.search_mode || 'file';
      this.searchKeyword = view.keyword || '';
      this.smartView = view.smart_view || '';
      this.selectedTags = tagIDs;
      this.selectedPeople = people;
      this.lastAppliedSavedViewID = view.id;
      this.savedViewNotice = dropped > 0 ? { name: view.name, dropped } : null;
      this.selectedSizeRange = this.findRangeOption(this.sizeOptions, view.min_size, view.max_size);
      this.selectedResRange = this.findRangeOption(this.resOptions, view.min_height, view.max_height);
      this.minRating = view.min_rating === null || view.min_rating === undefined ? '' : String(view.min_rating);
      this.maxRating = view.max_rating === null || view.max_rating === undefined ? '' : String(view.max_rating);
      this.sortMode = view.sort_mode || 'balanced';
      await this.reloadCurrentView();
    },
    // 保存视图只存人物 ID：名字先看已选过的，没有的再逐个取（一个视图里的人物通常只有几个）。
    async resolvePeople(ids) {
      const known = new Map(this.selectedPeople.map(person => [Number(person.id), person.name]));
      return Promise.all(ids.map(async id => {
        if (known.get(id)) return { id, name: known.get(id) };
        try {
          const detail = await GetPersonDetail(id, 0, 1);
          return { id, name: detail?.person?.person?.display_name || '' };
        } catch (err) {
          this.debugLog('resolvePeople failed', { id, err: String(err) }, true);
          return { id, name: '' };
        }
      }));
    },
    // 工具栏人物组合框的选择（D-PC33）：与点标签同一条路径——退出随机批次、离开保存视图、重载。
    updateSelectedPeople(people) {
      this.deactivateRandomPick();
      this.selectedPeople = (people || [])
        .map(person => ({ id: Number(person?.id || 0), name: String(person?.name || '') }))
        .filter(person => person.id > 0);
      this.leaveSavedView();
      this.reloadCurrentView();
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
      this.leaveSavedView();
      this.reloadCurrentView();
    },
    clearTagFilter() {
      this.deactivateRandomPick();
      this.selectedTags = [];
      this.leaveSavedView();
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
        const current = this.videos[index];
        // 状态切换接口若没带 tags（旧后端回 null），沿用行上已有的标签：null 盖上去标签就"没了"。
        const tags = Array.isArray(updatedVideo.tags) ? updatedVideo.tags : current.tags;
        this.videos.splice(index, 1, { ...current, ...updatedVideo, tags });
      }
      if (this.selectedPreviewVideoId === updatedVideo.id) {
        this.previewVideoSnapshot = {
          ...(this.previewVideoSnapshot || {}),
          ...updatedVideo,
          tags: Array.isArray(updatedVideo.tags) ? [...updatedVideo.tags] : (this.previewVideoSnapshot?.tags || [])
        };
      }
    },
    async handlePersonMediaDeleted(target) {
      if (target.kind !== 'video') return;
      const id = Number(target.media.id);
      this.videos = this.videos.filter(video => Number(video.id) !== id);
      if (Number(this.selectedPreviewVideoId) === id) this.closePreview();
      await this.reloadCurrentView();
    },
    // 抽屉里人物详情删掉的媒体被撤销恢复（LIB-12）：恢复的记录要回到列表，按当前条件重载一次。
    async handlePersonMediaRestored() {
      this.invalidateLibraryTotal();
      await this.reloadCurrentView();
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
    // 起播位置与后端同一套判定（utils/watchState.js，D-PC41/42）：断点可续且没落进片尾区间才续播；
    // 重看中的已看片也续播（PLAY-10）。
    resumePositionFor(video) {
      return resumePosition(video);
    },
    async applyVideoStateChange(updatedVideo) {
      if (!updatedVideo) return;
      if (STATE_SENSITIVE_VIEWS.includes(this.smartView) && !this.matchesSmartView(updatedVideo)) {
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
    // 点赞（D-PC40、PLAY-02）：唯一数据是 videos.is_liked，与手机端同一份。
    async toggleVideoLiked(video) {
      try {
        const updated = await SetVideoLiked(video.id, !video.is_liked);
        await this.applyVideoStateChange(updated);
      } catch (err) {
        notifyError('更新点赞状态失败: ' + err);
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
    // UpdateVideoWatchProgress 的 5 参契约（D-PC42）：duration 只在库内时长未知时采用，拿不到传 0；
    // origin 优先用抽屉上报的起播来源，没带时按这次打开抽屉的方式推断——带字幕命中时间打开为 jump，
    // 从断点续播为 resume，否则 start。嵌套条目的起播点抽屉自己定，按 resume（与 start 同样允许回写）。
    handlePreviewWatchProgress(progress) {
      const videoID = Number(progress?.videoID || this.selectedPreviewVideoId || 0);
      if (!videoID) return;
      const origin = this.watchProgressOrigin(videoID, progress?.origin);
      const reportedDuration = Number(progress?.durationSeconds);
      const duration = Number.isFinite(reportedDuration) && reportedDuration > 0 ? reportedDuration : 0;
      const save = async () => {
        const updated = await UpdateVideoWatchProgress(videoID, Number(progress?.positionSeconds || 0), duration, !!progress?.completed, origin);
        await this.applyVideoStateChange(updated);
      };
      this._watchProgressPromise = (this._watchProgressPromise || Promise.resolve())
        .then(save)
        .catch(err => console.error('保存观看进度失败:', err));
    },
    watchProgressOrigin(videoID, reported) {
      if (WATCH_PROGRESS_ORIGINS.includes(reported)) return reported;
      if (Number(videoID) !== Number(this.selectedPreviewVideoId)) return 'resume';
      if (this.previewStartTimeMs !== null && this.previewStartTimeMs !== undefined) return 'jump';
      return this.resumePositionFor(this.selectedPreviewVideo) > 0 ? 'resume' : 'start';
    },
    async applyPlaybackAttemptResult(result) {
      if (!result) return;

      const reconcile = result.reconcile_result;
      if (!result.dispatch_succeeded) {
        // 播放失败并已标失效（D-PC11、PLAY-12）：那一行就地改成失效态，给出原因与下一步，不整页重载。
        if (reconcile?.did_mark_stale) {
          this.markPlaybackFailureStale(result, reconcile);
          return;
        }
        notifyError(this.playbackFailureText(result));
      }

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
    // 后端的失败文案带着文件的完整路径；界面上只说文件名（G-3）。
    playbackFailureText(result) {
      const video = result?.video || {};
      const name = video.name || '视频';
      const text = String(result?.user_message || '播放失败');
      return video.path ? text.split(` (${video.path})`).join('').split(video.path).join(name) : text;
    },
    markPlaybackFailureStale(result, reconcile) {
      const videoID = Number(reconcile.video_id || result.video?.id || 0);
      const reason = String(reconcile.reason || result.reason || '');
      const staleReason = reconcile.updated_video?.stale_reason || (reason === 'offline_root' ? 'offline_root' : 'missing_file');
      const index = this.videos.findIndex(video => Number(video.id) === videoID);
      const base = index !== -1 ? this.videos[index] : (result.video || {});
      const updated = reconcile.updated_video || {};
      const merged = {
        ...base,
        ...updated,
        tags: Array.isArray(updated.tags) ? updated.tags : (base.tags || []),
        is_stale: true,
        stale_reason: staleReason
      };
      if (index !== -1) this.videos.splice(index, 1, merged);
      if (Number(this.selectedPreviewVideoId) === videoID) {
        this.previewVideoSnapshot = { ...merged, tags: [...(merged.tags || [])] };
      }
      const name = merged.display_title || merged.name || '这个视频';
      let text;
      if (reason === 'offline_root') {
        text = `「${name}」所在的磁盘未连接，已在列表中标为失效；接上磁盘后会自动恢复。`;
      } else if (reason === 'missing_file') {
        text = `「${name}」的文件不在原位置，已标为失效；正在后台查找它是否被移到了别处，找到会自动恢复。`;
      } else {
        text = `${this.playbackFailureText(result)}\n已在列表中标为失效。`;
      }
      this.playFailureNotice = { videoID, staleReason, text };
      if (this.smartView === 'stale') this.loadStaleReasonCounts();
    },
    async handleSearch(immediate = false, clearSimilar = false) {
      // 筛选条件变了，固定的随机批次就不再成立：退出批次、作废在途的抽取，按新条件正常加载。
      if (this.randomPick.active) immediate = true;
      this.deactivateRandomPick();
      this.leaveSavedView();
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
        await this.fillAutomaticOverrideKinds(picked);
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
      this.listQueryKey = '';
      this.selectedVideoIds = [];
      this.hasMore = false;
      this.setPagingCursor(EMPTY_PAGING_CURSOR);
    },
    // 随机批次是固定的一组 ID，刷新时按 ID 取最新记录，而不是重新抽一批。
    async refreshRandomPick() {
      let videos = [];
      try {
        const refreshed = await GetVideosByIDs(this.randomPick.ids) || [];
        await this.fillAutomaticOverrideKinds(refreshed);
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
      // 语义模式下随机会忽略搜索词、从全库抽取（PLAY-08）：入口已禁用，这里再挡一次快捷键与命令面板。
      if (this.searchMode === 'semantic') {
        notify('语义搜索结果不支持随机，请切回文件或字幕搜索。');
        return;
      }
      if (this.randomPlay.loading) return;
      this.randomPlay = { ...this.randomPlay, loading: true };
      try {
        const result = await PlayRandomVideoWithFilter({
          filter: this.currentLibraryFilter(),
          mode: this.randomMode,
          exclude_ids: this.recentRandomVideoIDs.slice(-12)
        });
        await this.handleRandomPlayResult(result);
      } catch (err) {
        console.error('随机播放失败:', err);
        notifyError('随机播放失败: ' + err);
      } finally {
        this.randomPlay = { ...this.randomPlay, loading: false };
      }
    },
    // 结果条上的「换一个」（D-PC44）：30 秒窗口内用令牌换，这次随机不计数、不记账；
    // 窗口已过、没有令牌，或后端答 reroll_expired（已提交）时，改为普通的再随机一次。
    async rerollRandomPlay() {
      const current = this.randomPlay;
      if (current.loading) return;
      if (!current.token || !rerollWindowOpen(current.startedAt)) {
        await this.playRandom();
        return;
      }
      this.randomPlay = { ...current, loading: true };
      let expired = false;
      try {
        const result = await RerollRandom(current.token);
        if (result?.reason_code === 'reroll_expired') {
          expired = true;
        } else {
          await this.handleRandomPlayResult(result);
        }
      } catch (err) {
        console.error('换一个失败:', err);
        notifyError('换一个失败: ' + err);
      } finally {
        this.randomPlay = { ...this.randomPlay, loading: false };
      }
      if (expired) {
        this.randomPlay = { ...this.randomPlay, token: '', rerollable: false };
        await this.playRandom();
      }
    },
    async handleRandomPlayResult(result) {
      if (result?.dispatch_succeeded && result.video) {
        this.rememberRandomVideo(result.video.id);
        this.showRandomPlayResult(result);
        // 返回的 video 是库里的现值：计数与 last_played_at 要等 30 秒后提交才变，这里不自行加一。
        await this.applyPlaybackAttemptResult(result);
        return;
      }
      // 没抽到或没播起来：结果条上那一部已经不是「正在随机播放」的了。
      this.dismissRandomPlay();
      await this.applyPlaybackAttemptResult(result);
    },
    showRandomPlayResult(result) {
      clearTimeout(this._randomRerollTimer);
      const token = String(result.reroll_token || '');
      this.randomPlay = {
        active: true,
        video: result.video,
        reason: result.selection_reason || '按当前筛选条件选择',
        token,
        startedAt: Date.now(),
        rerollable: Boolean(token),
        loading: this.randomPlay.loading
      };
      if (!token) return;
      // 窗口到点就把结果条改成「已计入」；令牌本身由后端判定，这里只管说法。
      this._randomRerollTimer = setTimeout(() => {
        if (this.randomPlay.token === token) this.randomPlay = { ...this.randomPlay, rerollable: false };
      }, RANDOM_REROLL_WINDOW_MS);
    },
    dismissRandomPlay() {
      clearTimeout(this._randomRerollTimer);
      this.randomPlay = { active: false, video: null, reason: '', token: '', startedAt: 0, rerollable: false, loading: this.randomPlay.loading };
    },
    rememberRandomVideo(videoID) {
      this.recentRandomVideoIDs = normalizeRecentRandomIDs([...this.recentRandomVideoIDs, videoID]);
      saveRecentRandomIDs(this.recentRandomVideoIDs);
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
      // 先关确认框再删：批量删除的进度与「取消」、磁盘不支持废纸篓时的选择框都在它下面。
      const videoIds = [...this.deleteDialog.videoIds];
      this.deleteDialog.show = false;
      if (videoIds.length > 0) {
        await this.deleteVideos(videoIds, deleteFile);
      } else {
        await this.deleteVideo(video, deleteFile);
      }
    },
    // 单个删除与批量删除走同一条路：带结果码的删除（D-PC01/02），成功项给整批撤销条。
    async deleteVideo(video, deleteFile) {
      if (!video) return;
      return this.deleteVideos([video.id], deleteFile);
    },
    async deleteVideos(videoIds, deleteFile) {
      const ids = [...new Set(videoIds)].filter(id => !!id);
      if (ids.length === 0) return;
      const banner = this.$refs.trashUndo;
      try {
        this.deletingIds = [...new Set([...this.deletingIds, ...ids])];
        const outcome = await banner.runDelete({ ids, deleteFile, names: this.videoNames(ids) });
        const removedIDs = outcome.removedIDs;

        if (removedIDs.includes(this.selectedPreviewVideoId)) {
          this.closePreview();
        }
        this.videos = this.videos.filter(video => !removedIDs.includes(video.id));
        this.selectedVideoIds = this.selectedVideoIds.filter(id => !removedIDs.includes(id));
        banner.showDeleteNotice(outcome);
        if (removedIDs.length > 0) await this.reloadCurrentView();
      } catch (err) {
        console.error('删除失败:', err);
        notifyError('删除失败: ' + err);
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
    // tab：video / image / hidden / staged，缺省停在视频页签。
    openTrashDialog(tab = 'video') {
      this.$refs.trashUndo?.openTrashDialog(tab);
    },
    // 从回收站恢复之后：清理面板的已删标记要跟着放掉，再重载列表；从回收站对话框
    // 恢复的（或整批撤销只回来一部分的）还要再拉一次清理分析状态。
    async afterTrashRestore(videoIDs, fromTrashDialog) {
      for (const videoID of videoIDs || []) this.forgetCleanupTrashed(videoID);
      await this.reloadCurrentView();
      if (fromTrashDialog) await this.refreshCleanupStatus();
    },
    async handleAITagCandidatesChanged() {
      this.aiTagReviewDialog.dirty = true;
      this.$emit('reload-tags');
    },
    // 审阅里确认同源后点「去清理」（D-PC27）：关掉审阅、打开清理中心，按 relationId 定位这一对
    // （P-032 约定：面板自己定位；这一对不在结果里时由面板说明）。
    async openCleanupFromReview(payload) {
      await this.closeAITagReviewDialog();
      const relationId = Number(payload?.relationId || 0);
      return this.$refs.cleanupPanel?.open(relationId ? { relationId } : undefined);
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
    handleTagPersonConverted(result) {
      this.selectedTags = this.selectedTags.filter(id => Number(id) !== Number(result.tag_id));
      this.$emit('person-converted', result);
      this.reloadCurrentView();
      this.$refs.previewDrawer?.loadCurrentEntry();
    },
    // 撤销「标签转人物」（D-PC34）：标签与打标关系回来了，这次新建的人物可能已被删掉。
    // 人物筛选里若还挂着不存在的人物会让结果恒为空，先剔掉；图片页经 person-converted 同样重载。
    async handleTagConversionUndone(result) {
      this.$emit('reload-tags');
      if (result?.person_deleted && this.selectedPeople.length > 0) {
        try {
          const active = await FilterActivePersonIDs(this.selectedPeople.map(person => Number(person.id)));
          const kept = new Set((active?.person_ids || []).map(Number));
          this.selectedPeople = this.selectedPeople.filter(person => kept.has(Number(person.id)));
        } catch (err) {
          this.debugLog('handleTagConversionUndone filter people failed', { err: String(err) }, true);
        }
      }
      this.$emit('person-converted', { ...(result || {}), tag_id: Number(result?.tag?.id || 0), undone: true });
      await this.reloadCurrentView();
      this.$refs.previewDrawer?.loadCurrentEntry();
    },
    handleTagAdded() {
      this.$emit('reload-tags');
      this.reloadCurrentView();
    },
    // ===== 路径失效：分组、重新检查、加回目录（D-PC06、LIB-01、LIB-10）=====
    async loadStaleReasonCounts() {
      try {
        this.staleReasonCounts = await ListStaleReasonCounts() || {};
      } catch (err) {
        this.debugLog('loadStaleReasonCounts failed', { err: String(err) }, true);
      }
    },
    setStaleReasonFilter(reason) {
      this.staleReasonFilter = reason || '';
      this.handleSearch(true);
    },
    openStaleView(reason = '') {
      this.playFailureNotice = null;
      // 从别的视图切过来时计数由 smartView 的 watcher 去取；本来就在失效视图里就自己取一次。
      if (this.smartView === 'stale') this.loadStaleReasonCounts();
      this.smartView = 'stale';
      this.staleReasonFilter = reason || '';
      return this.handleSearch(true);
    },
    recheckSelectedStale() {
      return this.recheckVideos([...this.selectedVideoIds]);
    },
    // 窄对账这些视频所在的目录：文件回到原处的恢复，找到新位置的改路径（RecheckVideos）。
    async recheckVideos(ids) {
      const unique = [...new Set((ids || []).map(Number).filter(Boolean))];
      if (unique.length === 0 || this.recheckingStale) return;
      this.recheckingStale = true;
      try {
        const summary = await RecheckVideos(unique);
        const restored = Number(summary?.restored || 0);
        const relocated = Number(summary?.relocated || 0);
        if (restored + relocated > 0) {
          const parts = [];
          if (restored) parts.push(`恢复 ${restored} 个`);
          if (relocated) parts.push(`找到新位置 ${relocated} 个`);
          notify(`重新检查完成：${parts.join('，')}。`);
        } else {
          notify('重新检查完成：文件仍不在原处，记录保持失效。');
        }
        if (Number(summary?.error_count || 0) > 0) notifyError(`重新检查时有 ${summary.error_count} 个目录读取失败，请确认磁盘已连接。`);
        if (this.playFailureNotice && unique.includes(Number(this.playFailureNotice.videoID))) this.playFailureNotice = null;
        this.invalidateLibraryTotal();
        await this.reloadCurrentView();
        if (this.smartView === 'stale') await this.loadStaleReasonCounts();
      } catch (err) {
        notifyError('重新检查失败: ' + err);
      } finally {
        this.recheckingStale = false;
      }
    },
    // 「加回目录」：找到这条记录原来所属、后来被删掉的扫描目录，确认后加回去；加回后后台窄对账恢复记录。
    async readdRemovedRoot(video) {
      let path;
      let validation;
      try {
        path = await ReaddRemovedRoot(video.id);
        validation = await ValidateScanDirectory(path);
      } catch (err) {
        notifyError(`无法加回目录：${err}。可以在设置页「扫描目录管理」里手动添加。`);
        return;
      }
      const label = pathBaseName(path);
      if (!validation?.exists) {
        notifyError(`原来的目录「${label}」现在找不到（磁盘可能没有连接），接上后再试。`);
        return;
      }
      if (validation.duplicate_of) {
        notify(`「${label}」已经在扫描目录里，已对这条记录重新检查。`);
        await this.recheckVideos([video.id]);
        return;
      }
      const hints = [];
      if (validation.nested_in) hints.push(`它位于扫描目录「${this.directoryLabel(validation.nested_in)}」之内。`);
      if ((validation.contains || []).length) hints.push(`它包含已有的扫描目录「${validation.contains.map(item => this.directoryLabel(item)).join('」「')}」。`);
      const confirmed = await confirmAction({
        title: '加回扫描目录',
        message: `把目录「${label}」加回扫描目录？加回后，这个目录下失效的记录会自动恢复，标签、评分与观看进度都还在。${hints.length ? '\n' + hints.join('\n') : ''}`,
        confirmText: '加回'
      });
      if (!confirmed) return;
      await this.addScanRoot(path);
    },
    // ===== 扫描摘要（D-PC09、LIB-08、LIB-14）=====
    // 增量扫描条自己发起的那次扫描由它自己汇报；其余来源（启动、改目录、别处的手动对账）在这里交给它显示。
    handleLibraryScanSummary(event) {
      const result = event?.result;
      if (!result) return;
      const bar = this.$refs.incrementalScanBar;
      if (event.trigger === 'manual' && (bar?.incrementalScan?.running || this.incrementalScan.running)) return;
      bar?.showSummary?.(event);
      const changed = ['added', 'deleted', 'restored', 'stale', 'relocated', 'metadata_refreshed']
        .reduce((sum, key) => sum + Number(result[key] || 0), 0);
      if (changed > 0) this.reloadAfterScan();
    },
    // 扫描之后列表与结果条总数一起刷新。
    async reloadAfterScan() {
      this.invalidateLibraryTotal();
      await this.reloadCurrentView();
      if (this.smartView === 'stale') await this.loadStaleReasonCounts();
    },
    // 「重新定位文件…」（LIB-10）：选一个视频文件作为这条记录的新位置，标签、评分与观看记录都保留。
    async relocateVideoFile(video) {
      if (!video?.id) return;
      let path;
      try {
        path = await SelectVideoFile();
      } catch (err) {
        notifyError('打开文件选择框失败：' + subtitleExceptionText(err));
        return;
      }
      if (!path) return;
      try {
        await RelocateVideo(video.id, path);
      } catch (err) {
        notifyError(relocateErrorText(err));
        return;
      }
      let refreshed = null;
      try {
        const rows = await GetVideosByIDs([video.id]) || [];
        await this.fillAutomaticOverrideKinds(rows);
        refreshed = rows[0] || null;
      } catch (err) {
        this.debugLog('relocate refresh failed', { err: String(err) }, true);
      }
      this.applyRelocatedRow(video.id, refreshed || { path, directory: pathDirName(path), name: pathBaseName(path) });
      notify(`已重新定位「${video.display_title || video.name}」，记录已恢复。`);
    },
    // 重新定位后的就地更新：失效视图里这一行不再属于这里，其余视图原位换成新记录。
    applyRelocatedRow(videoID, fields) {
      const id = Number(videoID);
      const patch = { ...fields, is_stale: false, stale_reason: '' };
      if (Number(this.playFailureNotice?.videoID) === id) this.playFailureNotice = null;
      const index = this.videos.findIndex(video => Number(video.id) === id);
      if (index !== -1) {
        if (this.smartView === 'stale') {
          this.videos.splice(index, 1);
          this.selectedVideoIds = this.selectedVideoIds.filter(selected => Number(selected) !== id);
          this.loadStaleReasonCounts();
        } else {
          this.videos.splice(index, 1, { ...this.videos[index], ...patch });
        }
      }
      if (Number(this.selectedPreviewVideoId) === id && this.previewVideoSnapshot) {
        this.previewVideoSnapshot = { ...this.previewVideoSnapshot, ...patch };
      }
    },
    // 后台重定位找到了新位置（D-PC11）：那一行改回正常；在「路径失效」视图里它就不属于这里了。
    handleVideoRelocated(event) {
      const videoID = Number(event?.video_id || 0);
      const newPath = String(event?.new_path || '');
      if (!videoID || !newPath) return;
      if (Number(this.playFailureNotice?.videoID) === videoID) this.playFailureNotice = null;
      const patch = { path: newPath, directory: pathDirName(newPath), name: pathBaseName(newPath), is_stale: false, stale_reason: '' };
      const index = this.videos.findIndex(video => Number(video.id) === videoID);
      const current = index !== -1 ? this.videos[index] : null;
      if (current) {
        if (this.smartView === 'stale') {
          this.videos.splice(index, 1);
          this.selectedVideoIds = this.selectedVideoIds.filter(id => Number(id) !== videoID);
          this.loadStaleReasonCounts();
        } else {
          this.videos.splice(index, 1, { ...current, ...patch });
        }
      }
      if (Number(this.selectedPreviewVideoId) === videoID && this.previewVideoSnapshot) {
        this.previewVideoSnapshot = { ...this.previewVideoSnapshot, ...patch };
      }
      notify(`已找到「${current?.display_title || current?.name || patch.name}」的新位置，记录已恢复。`);
    },
    // ===== 超分未就绪、重新分析 =====
    async loadEnhancementCapability() {
      try {
        this.enhanceCapability = await GetEnhancementCapability() || null;
      } catch (err) {
        this.enhanceCapability = null;
        this.debugLog('loadEnhancementCapability failed', { err: String(err) }, true);
      }
    },
    // 设置页的超分分区由 App 注册的全局命令打开（action:settings:enhance），那里说明未就绪的原因与下一步。
    openEnhanceSettings() {
      const command = findCommand('action:settings:enhance');
      if (command) {
        command.run();
        return;
      }
      notify('请到设置页的「视频超分」分区查看超分组件的状态。');
    },
    // 已有人工标签的视频也能手动重新分析（D-PC28 规则 5）：旧的待审候选作废，重新排进 AI 分析。
    async reanalyzeVideo(video) {
      try {
        await RetryAITagging(video.id);
        notify(`已安排重新分析「${video.display_title || video.name}」，新的候选会出现在 AI 标签审阅里。`);
      } catch (err) {
        notifyError('重新分析失败: ' + err);
      }
    }
  }
};
</script>
