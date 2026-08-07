<template>
  <div
    :class="['page-content', { 'page-content--with-preview': previewOpen }]"
    @wheel="forwardWheelToScrollOwner"
  >
    <div class="toolbar glass-surface">
      <div class="toolbar-primary">
        <div class="search-group">
          <select v-model="searchMode" @change="handleSearch(true, true)" class="select-input toolbar-control">
            <option value="file">文件搜索</option>
            <option value="subtitle">字幕搜索</option>
            <option value="semantic">语义搜索</option>
          </select>
          <input
            v-model="searchKeyword"
            @input="handleSearch(false, true)"
            @keyup.enter="handleSearch(true, true)"
            type="text"
            :placeholder="searchMode === 'subtitle' ? '搜索字幕内容...' : (searchMode === 'semantic' ? '用自然语言描述想找的内容，回车搜索...' : '搜索标题、文件名或路径...')"
            class="search-input toolbar-control"
          />
        </div>

        <div class="filter-group">
          <select v-model="smartView" @change="handleSearch(true)" class="select-input toolbar-control" aria-label="智能视图">
            <option v-for="view in smartViewOptions" :key="view.value" :value="view.value">{{ view.label }}</option>
          </select>
          <select v-model="selectedSizeRange" @change="handleSearch(true)" class="select-input toolbar-control">
            <option value="all">体积：全部</option>
            <option v-for="opt in sizeOptions" :key="opt.label" :value="opt.value">{{ opt.label }}</option>
          </select>

          <select v-model="selectedResRange" @change="handleSearch(true)" class="select-input toolbar-control">
            <option value="all">分辨率：全部</option>
            <option v-for="opt in resOptions" :key="opt.label" :value="opt.value">{{ opt.label }}</option>
          </select>
          <input v-model="minRating" @change="handleSearch(true)" type="text" inputmode="decimal" maxlength="4" class="text-input toolbar-control rating-filter-input" placeholder="最低评分" aria-label="最低评分，0 到 10" />
          <input v-model="maxRating" @change="handleSearch(true)" type="text" inputmode="decimal" maxlength="4" class="text-input toolbar-control rating-filter-input" placeholder="最高评分" aria-label="最高评分，0 到 10" />
          <select v-model="sortMode" @change="handleSearch(true)" class="select-input toolbar-control" aria-label="排序方式">
            <option value="balanced">均衡排序</option>
            <option value="rating_desc">评分从高到低</option>
            <option value="rating_asc">评分从低到高</option>
          </select>
        </div>
      </div>

      <div class="toolbar-secondary">
        <div class="toolbar-cluster toolbar-cluster--views">
          <select v-model="selectedSavedViewID" @change="applySelectedSavedView" class="select-input saved-view-select" aria-label="保存视图">
            <option :value="0">保存视图</option>
            <option v-for="view in savedViews" :key="view.id" :value="view.id">{{ view.name }}</option>
          </select>
          <button type="button" class="btn-secondary" @click="openSaveViewDialog">保存当前视图</button>
          <button v-if="selectedSavedViewID" type="button" class="btn-secondary" @click="deleteSelectedSavedView">删除该视图</button>
          <div class="layout-toggle" aria-label="片库布局">
            <button type="button" :class="['btn-secondary', { active: viewMode === 'list' }]" @click="setViewMode('list')">列表</button>
            <button type="button" :class="['btn-secondary', { active: viewMode === 'grid' }]" @click="setViewMode('grid')">网格</button>
          </div>
        </div>

        <div class="toolbar-cluster toolbar-cluster--playback">
          <select v-model="randomMode" class="select-input" aria-label="随机播放模式">
            <option value="balanced">均衡随机</option>
            <option value="unwatched">随机未看</option>
            <option value="favorites">随机收藏</option>
          </select>
          <button @click="playRandom" class="btn-random">按当前条件随机</button>
          <button
            @click="toggleSelectAllVisible"
            class="btn-secondary"
            :disabled="videos.length === 0"
          >
            {{ allVisibleSelected ? '取消全选' : '选择本页' }}
          </button>
        </div>
      </div>

      <div class="toolbar-management" aria-label="片库管理">
        <button @click="showScanDialog = true" class="btn-primary" :disabled="migrationRunning">扫描新目录</button>
        <button type="button" class="btn-secondary" :disabled="migrationRunning" @click="moveFolder">
          {{ migrationRunning ? '迁移中...' : '迁移文件夹' }}
        </button>
        <button type="button" class="btn-secondary" :disabled="migrationRunning" @click="renameFolder">
          {{ migrationRunning ? '处理中...' : '重命名文件夹' }}
        </button>
        <button
          type="button"
          class="btn-secondary"
          :disabled="migrationRunning || incrementalScan.running || directories.length === 0"
          :title="directories.length === 0 ? '请先添加扫描目录' : '扫描所有已配置目录中的文件变化'"
          @click="runIncrementalScan"
        >
          {{ incrementalScan.running ? '扫描中...' : '增量扫描' }}
        </button>
        <button type="button" class="btn-secondary" @click="openAITagReviewDialog()">AI 标签管理</button>
        <span v-if="aiTagSummary.same_source_unread" class="ai-review-badge" title="未读同源视频关系">{{ aiTagSummary.same_source_unread }}</span>
        <button type="button" class="btn-secondary" @click="openCleanupDialog()">清理候选</button>
        <button type="button" class="btn-secondary" @click="openTrashDialog">回收站</button>
        <button type="button" class="btn-secondary" :disabled="technicalBackfill.running" @click="startTechnicalBackfill">
          {{ technicalBackfill.running ? (technicalBackfill.preparing ? '正在统计待补全视频...' : `技术信息 ${technicalBackfill.processed}/${technicalBackfill.total}`) : '补全技术信息' }}
        </button>
        <button v-if="technicalBackfill.running" type="button" class="btn-secondary" @click="cancelTechnicalBackfill">取消补全</button>
        <button type="button" class="btn-secondary" :disabled="perceptualHash.running" @click="startPerceptualHashBackfill">
          {{ perceptualHash.running ? `近重复指纹 ${perceptualHash.processed}/${perceptualHash.total}` : '补全近重复指纹' }}
        </button>
        <button v-if="perceptualHash.running" type="button" class="btn-secondary" @click="cancelPerceptualHashBackfill">取消指纹补全</button>
        <button v-if="settings.local_metadata_enabled" type="button" class="btn-secondary" :disabled="localMetadataBackfill.running" @click="startLocalMetadataBackfill">
          {{ localMetadataBackfill.running ? `本地资料 ${localMetadataBackfill.processed}/${localMetadataBackfill.total}` : '补全本地资料' }}
        </button>
        <button v-if="settings.local_metadata_enabled && localMetadataBackfill.running" type="button" class="btn-secondary" @click="cancelLocalMetadataBackfill">取消本地资料补全</button>
		<button type="button" class="btn-secondary" :disabled="localMetadataExport.running" @click="startLocalMetadataExport">
		  {{ localMetadataExport.running ? `写出 NFO ${localMetadataExport.processed}/${localMetadataExport.total}` : '当前筛选写出 NFO' }}
		</button>
		<button v-if="localMetadataExport.running" type="button" class="btn-secondary" @click="cancelLocalMetadataExport">取消写出 NFO</button>
        <button type="button" class="btn-secondary" @click="showTagManagerDialog = true">标签管理</button>
      </div>

      <div v-if="selectedVideoIds.length > 0" class="selection-toolbar">
        <span>已选 {{ selectedVideoIds.length }} 个视频</span>
        <div class="selection-toolbar__actions">
          <button
            @click="openBatchAddTagDialog"
            class="btn-secondary"
          >
            批量标签编辑
          </button>
          <button
            @click="moveSelectedVideos"
            class="btn-secondary"
            :disabled="migrationRunning"
          >
            批量迁移
          </button>
          <button type="button" class="btn-secondary" @click="openLocalMetadataDialog(selectedVideoIds)">导入本地资料</button>
          <button
            @click="confirmBatchDelete"
            class="btn-danger"
          >
            批量删除
          </button>
        </div>
      </div>
    </div>

    <div
      v-if="incrementalScan.message"
      class="scan-sync-status"
      :class="`scan-sync-status--${incrementalScan.state}`"
      :role="incrementalScan.state === 'error' ? 'alert' : 'status'"
    >
      {{ incrementalScan.message }}
    </div>

    <div v-if="technicalBackfill.running || technicalBackfill.completed || technicalBackfill.cancelled || technicalBackfill.failed" class="scan-sync-status" :role="technicalBackfill.failed ? 'alert' : 'status'">
      <span v-if="technicalBackfill.preparing">正在统计待补全视频...</span>
      <span v-else-if="technicalBackfill.completed && technicalBackfill.total === 0 && !technicalBackfill.failed">技术信息无需补全（已是最新状态）。</span>
      <span v-else>
        技术信息：成功 {{ technicalBackfill.succeeded }}，跳过 {{ technicalBackfill.skipped }}，失败 {{ technicalBackfill.failed }}
        <span v-if="technicalBackfill.cancelled">（已取消）</span>
        <span v-else-if="technicalBackfill.completed">（已完成）</span>
      </span>
      <ul v-if="technicalBackfill.failures?.length" class="technical-backfill-failures">
        <li v-for="failure in technicalBackfill.failures" :key="`${failure.video_id}:${failure.name}`">{{ failure.name || `视频 #${failure.video_id}` }}：{{ failure.error }}</li>
      </ul>
    </div>

    <div v-if="perceptualHash.running || perceptualHash.completed" class="scan-sync-status" :role="perceptualHash.failed ? 'alert' : 'status'">
      近重复指纹：成功 {{ perceptualHash.succeeded }}，跳过 {{ perceptualHash.skipped }}，失败 {{ perceptualHash.failed }}
      <span v-if="perceptualHash.cancelled">（已取消）</span><span v-else-if="perceptualHash.completed">（已完成）</span>
      <ul v-if="perceptualHash.failures?.length" class="technical-backfill-failures">
        <li v-for="failure in perceptualHash.failures" :key="`phash-${failure.video_id}`">{{ failure.name || `视频 #${failure.video_id}` }}：{{ failure.error }}</li>
      </ul>
    </div>

    <div v-if="localMetadataBackfill.running || localMetadataBackfill.completed" class="scan-sync-status" :role="localMetadataBackfill.failed ? 'alert' : 'status'">
      本地资料：成功 {{ localMetadataBackfill.succeeded }}，跳过 {{ localMetadataBackfill.skipped }}，失败 {{ localMetadataBackfill.failed }}
      <span v-if="localMetadataBackfill.cancelled">（已取消）</span><span v-else-if="localMetadataBackfill.completed">（已完成）</span>
    </div>
	<div v-if="localMetadataExport.running || localMetadataExport.completed" class="scan-sync-status" :role="localMetadataExport.failed ? 'alert' : 'status'">
	  NFO 写出：成功 {{ localMetadataExport.succeeded }}，失败 {{ localMetadataExport.failed }}
	  <span v-if="localMetadataExport.cancelled">（已取消）</span><span v-else-if="localMetadataExport.completed">（已完成）</span>
	</div>
    <div v-if="searchMode === 'semantic'" class="scan-sync-status" :role="semanticSearchError ? 'alert' : 'status'">
      <span v-if="semanticSearchError">语义搜索失败：{{ semanticSearchErrorText }}</span>
      <span v-else-if="semanticCoverage">语义索引覆盖 {{ semanticCoverage.indexed || 0 }}/{{ semanticCoverage.total || 0 }}；未建立索引的视频不会出现在结果中。</span>
      <span v-else>用自然语言描述想找的内容，回车开始搜索；未建立索引的视频不会出现在结果中。</span>
    </div>

    <div v-if="undoNotice" class="undo-delete-banner" role="status">
      <span>已移入回收站 {{ undoNotice.count }} 个视频。</span>
      <button v-if="undoNotice.entry" type="button" class="btn-primary btn-compact" :disabled="undoing" @click="undoLastDelete">
        {{ undoing ? '撤销中...' : '撤销' }}
      </button>
      <button v-else type="button" class="btn-secondary btn-compact" @click="openTrashDialog">查看回收站</button>
      <button type="button" class="undo-delete-banner__close" aria-label="关闭提示" @click="undoNotice = null">×</button>
    </div>

    <div v-if="subtitleQueue.total > 0" class="subtitle-queue-panel glass-surface">
      <div class="subtitle-queue-heading">
        <strong>字幕任务队列（{{ subtitleQueue.total }}）</strong>
        <button type="button" class="btn-secondary" @click="refreshSubtitleQueue">刷新</button>
      </div>
      <div v-if="subtitleQueue.active_task" class="subtitle-queue-task subtitle-queue-task--active">
        <span class="subtitle-queue-status">处理中</span>
        <span class="subtitle-queue-name">{{ subtitleQueue.active_task.video_name || `视频 #${subtitleQueue.active_task.video_id}` }}</span>
        <button v-if="subtitleQueue.active_task.can_cancel" type="button" class="btn-danger btn-compact" :disabled="cancellingSubtitleTaskIds.includes(subtitleQueue.active_task.task_id)" @click="cancelSubtitleTask(subtitleQueue.active_task.task_id)">取消</button>
      </div>
      <div v-for="task in subtitleQueue.queued_tasks" :key="task.task_id" class="subtitle-queue-task">
        <span class="subtitle-queue-status">排队中 #{{ task.position }}</span>
        <span class="subtitle-queue-name">{{ task.video_name || `视频 #${task.video_id}` }}</span>
        <button v-if="task.can_cancel" type="button" class="btn-secondary btn-compact" :disabled="cancellingSubtitleTaskIds.includes(task.task_id)" @click="cancelSubtitleTask(task.task_id)">取消</button>
      </div>
    </div>

    <div class="tags-filter">
      <div class="tags-scroll-container">
        <button
          @click="clearTagFilter"
          :class="['tag-chip', { active: selectedTags.length === 0 }]"
        >
          全部
        </button>
        <div
          v-for="tag in tags"
          :key="tag.id"
          class="tag-chip tag-chip-wrap"
          :class="{ active: isTagSelected(tag.id) }"
          :style="{ backgroundColor: tagBgColor(tag.color) }"
          @click="toggleTagFilter(tag.id)"
        >
          <span class="tag-chip-name">{{ tag.name }}</span>
          <span v-if="isTagSelected(tag.id)" class="tag-chip-check">✓</span>
          <button v-if="!tag.automatic_kind" type="button" class="tag-chip-delete" @click.stop="requestDeleteTag(tag)">×</button>
        </div>
      </div>
    </div>

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
            :generating-subtitle-ids="generatingSubtitleIds"
            :deleting-ids="deletingIds"
            :selected="isVideoSelected(video.id)"
            :keyboard-focused="Number(video.id) === Number(keyboardFocusVideoID)"
            :layout-mode="viewMode"
            @preview="openPreview"
            @play="playVideo"
            @toggle-favorite="toggleVideoFavorite"
            @toggle-watched="toggleVideoWatched"
            @open-directory="openDirectory"
            @generate-subtitle="generateSubtitle"
            @subtitle-edit="openSubtitleWorkbench"
            @subtitle-preview="openSubtitlePreview"
            @rename="renameVideo"
            @move="moveVideo"
            @delete="confirmDelete"
            @open-add-tag="openAddTagDialog"
            @remove-tag="removeTag"
            @toggle-select="toggleVideoSelection"
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

    <TrashRestoreDialog
      :visible="trashDialog.show"
      @close="trashDialog.show = false"
      @restored="handleTrashRestored"
    />

    <!-- Context Menu -->
    <div
      v-if="contextMenu.show"
      :style="{ top: contextMenu.y + 'px', left: contextMenu.x + 'px' }"
      class="context-menu"
      @click="contextMenu.show = false"
    >
      <div @click="playVideo(contextMenu.video.id)">播放</div>
      <div @click="openDirectory(contextMenu.video.id)">打开目录</div>
      <div @click="renameVideo(contextMenu.video)">重命名</div>
      <div @click="moveVideo(contextMenu.video)">迁移</div>
	  <div @click="exportLocalMetadataNFO(contextMenu.video)">写出 NFO</div>
	  <div @click="openEnhanceDialog(contextMenu.video)">视频超分…</div>
      <div @click="confirmDelete(contextMenu.video)" class="danger">删除</div>
    </div>

    <!-- 视频超分 -->
    <BaseModal v-if="enhanceDialog.show" stop-modal-clicks @close="enhanceDialog.show = false">
      <h2>视频超分（2×）</h2>
      <template v-if="!enhanceCapability?.available">
        <p class="help-text">超分能力不可用：{{ enhanceCapability?.message || '运行时未打包' }}</p>
      </template>
      <template v-else-if="enhanceDialog.video">
        <p class="enhance-source-name" :title="enhanceDialog.video.path">{{ enhanceDialog.video.name }}</p>
        <div class="setting-item">
          <label>内容类型（决定模型，不会自动判断）</label>
          <div class="merge-type-switch" role="group" aria-label="超分内容类型">
            <button type="button" :class="{ active: enhanceDialog.profile === 'general' }" @click="enhanceDialog.profile = 'general'">普通真人</button>
            <button type="button" :class="{ active: enhanceDialog.profile === 'anime' }" @click="enhanceDialog.profile = 'anime'">动漫</button>
          </div>
        </div>
        <p class="help-text">输出：<code>{{ enhanceOutputPreview }}</code>（与源同目录）</p>
        <p class="help-text">固定 2× 放大；原文件不会被修改；任务可随时取消。运行需要同卷至少约 {{ enhanceDiskFloorText }} 可用空间。</p>
        <p v-if="enhanceDialog.error" class="cleanup-error">{{ enhanceDialog.error }}</p>
        <div class="modal-actions">
          <button type="button" class="btn-secondary" @click="enhanceDialog.show = false">取消</button>
          <button type="button" class="btn-primary" :disabled="enhanceDialog.creating" @click="createEnhancementTask">
            {{ enhanceDialog.creating ? '创建中...' : '创建超分任务' }}
          </button>
        </div>
      </template>
      <template v-if="enhanceTasks.length">
        <div class="divider"></div>
        <h3 class="enhance-task-heading">任务</h3>
        <div v-for="task in enhanceTasks" :key="task.id" class="enhance-task-row">
          <span class="enhance-task-main">
            {{ task.video_name }} · {{ enhanceStatusLabel(task) }}
            <template v-if="task.status === 'running' && task.total_frames">（{{ task.committed_frames }}/{{ task.total_frames }} 帧）</template>
          </span>
          <span class="enhance-task-actions">
            <button v-if="['queued','running'].includes(task.status)" type="button" class="btn-secondary btn-compact" @click="cancelEnhancementTask(task)">取消</button>
            <button v-if="['failed','cancelled'].includes(task.status)" type="button" class="btn-secondary btn-compact" @click="retryEnhancementTask(task)">重试</button>
          </span>
        </div>
      </template>
    </BaseModal>

    <!-- 重命名弹窗 -->
    <BaseModal v-if="renameDialog.show" class="download-modal">
        <h3>重命名视频</h3>
        <input
          v-model="renameDialog.newName"
          type="text"
          class="search-input rename-input"
          placeholder="输入新文件名"
          @keyup.enter="executeRename"
          ref="renameInput"
        />
        <p class="rename-hint">扩展名会自动保留（{{ renameDialog.ext }}）</p>
        <div class="modal-actions">
          <button @click="renameDialog.show = false" class="btn-secondary">取消</button>
          <button @click="executeRename" class="btn-primary">确认</button>
        </div>
    </BaseModal>

    <BaseModal v-if="folderRenameDialog.show" class="download-modal">
        <h3>重命名文件夹</h3>
        <p class="folder-rename-source" :title="folderRenameDialog.source">{{ folderRenameDialog.source }}</p>
        <input
          ref="folderRenameInput"
          v-model="folderRenameDialog.newName"
          type="text"
          maxlength="255"
          class="search-input rename-input"
          placeholder="输入新的文件夹名称"
          :disabled="migrationRunning"
          @keyup.enter="executeFolderRename"
        />
        <p class="rename-hint">只修改当前文件夹名称，内部目录结构和视频关联保持不变。</p>
        <p v-if="folderRenameDialog.error" class="cleanup-error">{{ folderRenameDialog.error }}</p>
        <div class="modal-actions">
          <button type="button" class="btn-secondary" :disabled="migrationRunning" @click="folderRenameDialog.show = false">取消</button>
          <button type="button" class="btn-primary" :disabled="migrationRunning || !folderRenameDialog.newName.trim()" @click="executeFolderRename">{{ migrationRunning ? '重命名中...' : '确认重命名' }}</button>
        </div>
    </BaseModal>

    <BaseModal v-if="saveViewDialog.show" class="download-modal">
        <h3>保存当前片库视图</h3>
        <input
          ref="saveViewNameInput"
          v-model="saveViewDialog.name"
          type="text"
          maxlength="80"
          class="search-input rename-input"
          placeholder="输入视图名称"
          @keyup.enter="saveCurrentView"
        />
        <p v-if="saveViewDialog.error" class="cleanup-error">{{ saveViewDialog.error }}</p>
        <div class="modal-actions">
          <button type="button" class="btn-secondary" @click="saveViewDialog.show = false">取消</button>
          <button type="button" class="btn-primary" :disabled="saveViewDialog.saving" @click="saveCurrentView">
            {{ saveViewDialog.saving ? '保存中...' : '保存' }}
          </button>
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

    <BaseModal v-if="cleanupDialog.show" class="cleanup-modal">
        <div class="cleanup-modal-header">
          <div>
            <h3>清理候选审阅</h3>
            <p class="cleanup-intro">当前审阅基于轻量规则：精确重复（大小 + 采样哈希）、近似重复（多帧感知哈希）、低清视频：分辨率低于 480x320、短视频：时长 < 5 秒。选中的视频会直接移入回收站并从库中移除。</p>
            <p class="cleanup-intro cleanup-intro--muted">每条候选都支持预览，优先看画面再决定是否保留更稳妥。</p>
          </div>
          <button @click="cleanupDialog.show = false" class="btn-secondary">关闭</button>
        </div>

        <div class="cleanup-modal-body">
          <div v-if="cleanupDialog.loading" class="cleanup-loading">
            <div>正在分析视频库...</div>
            <div class="cleanup-progress-meta">
              当前阶段：{{ cleanupStageLabel }}
              <span v-if="cleanupElapsedText"> · 已运行 {{ cleanupElapsedText }}</span>
            </div>
            <div v-if="cleanupProgressPercent !== null" class="cleanup-progress-meta">
              已处理 {{ cleanupDialog.progress.current }} / {{ cleanupDialog.progress.total }}
              <span> ({{ cleanupProgressPercent }}%)</span>
            </div>
            <div v-if="cleanupDialog.progress.message" class="cleanup-progress-hint">{{ cleanupDialog.progress.message }}</div>
            <div v-if="cleanupDialog.progress.path" class="cleanup-progress-path">当前文件：{{ cleanupDialog.progress.path }}</div>
            <div class="cleanup-progress-hint">该分析会逐个读取视频文件；外置硬盘、休眠磁盘或大库场景下耗时较长，长时间停留不代表已假死。</div>
          </div>
          <div v-else-if="cleanupDialog.error" class="cleanup-error">{{ cleanupDialog.error }}</div>
          <div v-else-if="cleanupDialog.analysis" class="cleanup-body">
            <div class="cleanup-summary">
              <span>重复组 {{ cleanupDialog.analysis.duplicate_groups?.length || 0 }}</span>
              <span>近似重复 {{ cleanupDialog.analysis.near_duplicate_groups?.length || 0 }}</span>
              <span>疑似同源 {{ cleanupDialog.analysis.same_source_groups?.length || 0 }}</span>
              <span>短视频 {{ cleanupDialog.analysis.low_duration?.length || 0 }}</span>
              <span>低清视频 {{ cleanupDialog.analysis.low_resolution?.length || 0 }}</span>
              <span>已选 {{ cleanupSelection.length }}</span>
            </div>

            <div v-if="cleanupDialog.analysis.same_source_groups?.length" class="cleanup-section">
              <h4 class="cleanup-section-title">疑似同源（不会默认选中）</h4>
              <div
                v-for="group in cleanupDialog.analysis.same_source_groups"
                :key="`same-source-${group.relation_id}`"
                class="cleanup-card"
              >
                <div class="cleanup-select-row cleanup-select-row--original">
                  <strong>建议保留：</strong>
                  <div class="cleanup-item-text">
                    <span class="cleanup-item-main">{{ group.preferred?.name }} · {{ group.preferred?.resolution || '未知分辨率' }} · {{ formatDuration(group.preferred?.duration) || '00:00' }}</span>
                    <span v-if="group.preferred?.path" class="cleanup-item-path" :title="group.preferred.path">{{ group.preferred.path }}</span>
                  </div>
                  <div class="cleanup-item-actions">
                    <button type="button" class="btn-secondary btn-compact" @click="previewCleanupVideo(group.preferred)">预览保留项</button>
                  </div>
                </div>
                <p><strong>判断：</strong>{{ group.reason }}<span v-if="group.confidence"> · 置信度 {{ group.confidence }}</span></p>
                <div class="cleanup-select-row">
                  <input
                    type="checkbox"
                    :checked="isCleanupSelected(group.alternative?.id)"
                    @change="toggleCleanupSelection(group.alternative?.id)"
                  />
                  <span class="cleanup-item-text">
                    <span class="cleanup-item-main">可清理版本：{{ group.alternative?.name }} · 预计释放 {{ formatFileSize(group.estimated_savings) }}</span>
                    <span v-if="group.alternative?.path" class="cleanup-item-path" :title="group.alternative.path">{{ group.alternative.path }}</span>
                  </span>
                  <span class="cleanup-item-actions">
                    <button type="button" class="btn-secondary btn-compact" @click="previewCleanupVideo(group.alternative)">预览该版本</button>
                    <button type="button" class="btn-secondary btn-compact" @click="rejectCleanupSameSource(group)">不是同源</button>
                  </span>
                </div>
              </div>
            </div>

            <div v-if="cleanupCandidateCount" class="cleanup-toolbar">
              <button @click="selectAllCleanupCandidates" class="btn-secondary">全选候选</button>
              <button @click="clearCleanupSelection" class="btn-secondary" :disabled="cleanupSelection.length === 0">清空选择</button>
              <button @click="reanalyzeCleanupCandidates" class="btn-secondary" :disabled="cleanupDialog.loading || cleanupDialog.processing">重新分析</button>
            </div>

            <div v-if="cleanupDialog.analysis.duplicate_groups?.length" class="cleanup-section">
              <h4 class="cleanup-section-title">重复候选</h4>
              <div
                v-for="group in cleanupDialog.analysis.duplicate_groups"
                :key="`${group.original?.id}-${group.candidates?.length}`"
                class="cleanup-card"
              >
                <div class="cleanup-select-row cleanup-select-row--original">
                  <input
                    type="checkbox"
                    :checked="isCleanupSelected(group.original?.id)"
                    @change="toggleCleanupSelection(group.original?.id)"
                  />
                  <strong>建议保留：</strong>
                  <div class="cleanup-item-text">
                    <span class="cleanup-item-main">{{ group.original?.name }} · {{ group.original?.resolution || '未知分辨率' }} · {{ formatDuration(group.original?.duration) || '00:00' }}</span>
                    <span v-if="group.original?.path" class="cleanup-item-path" :title="group.original.path">{{ group.original.path }}</span>
                  </div>
                  <div class="cleanup-item-actions">
                    <button type="button" class="btn-secondary btn-compact" @click="previewCleanupVideo(group.original)">预览</button>
                  </div>
                </div>
                <p><strong>原因：</strong>{{ group.reason }}</p>
                <ul>
                  <li v-for="candidate in group.candidates || []" :key="candidate.id">
                    <div class="cleanup-select-row">
                      <input
                        type="checkbox"
                        :checked="isCleanupSelected(candidate.id)"
                        @change="toggleCleanupSelection(candidate.id)"
                      />
                      <span class="cleanup-item-text">
                        <span class="cleanup-item-main">{{ candidate.name }} · {{ candidate.resolution || '未知分辨率' }} · {{ formatDuration(candidate.duration) || '00:00' }}</span>
                        <span v-if="candidate.path" class="cleanup-item-path" :title="candidate.path">{{ candidate.path }}</span>
                      </span>
                      <span class="cleanup-item-actions">
                        <button type="button" class="btn-secondary btn-compact" @click="previewCleanupVideo(candidate)">预览</button>
                      </span>
                    </div>
                  </li>
                </ul>
              </div>
            </div>

            <div v-if="cleanupDialog.analysis.stale_hash_count" class="cleanup-section cleanup-stale-hash-hint">
              <span>有 {{ cleanupDialog.analysis.stale_hash_count }} 个视频的源文件已变更，感知哈希待重算，暂未参与近似重复检测。</span>
              <button type="button" class="btn-secondary btn-compact" :disabled="perceptualHash.running" @click="startPerceptualHashBackfill">
                {{ perceptualHash.running ? '重算中...' : '重算感知哈希' }}
              </button>
            </div>

            <div v-if="cleanupDialog.analysis.near_duplicate_groups?.length" class="cleanup-section">
              <h4 class="cleanup-section-title">近似重复（不同转码，不会默认选中）</h4>
              <div
                v-for="group in cleanupDialog.analysis.near_duplicate_groups"
                :key="`near-${group.original?.id}-${group.candidates?.length}`"
                class="cleanup-card"
              >
                <div class="cleanup-select-row cleanup-select-row--original">
                  <input
                    type="checkbox"
                    :checked="isCleanupSelected(group.original?.id)"
                    @change="toggleCleanupSelection(group.original?.id)"
                  />
                  <strong>建议保留：</strong>
                  <div class="cleanup-item-text">
                    <span class="cleanup-item-main">{{ group.original?.name }} · {{ group.original?.resolution || '未知分辨率' }} · {{ formatDuration(group.original?.duration) || '00:00' }}</span>
                    <span v-if="group.original?.path" class="cleanup-item-path" :title="group.original.path">{{ group.original.path }}</span>
                  </div>
                  <div class="cleanup-item-actions">
                    <button type="button" class="btn-secondary btn-compact" @click="previewCleanupVideo(group.original)">预览</button>
                    <button type="button" class="btn-secondary btn-compact" @click="dismissNearDuplicateGroup(group)">不是同片</button>
                  </div>
                </div>
                <p><strong>原因：</strong>{{ group.reason }}</p>
                <ul>
                  <li v-for="candidate in group.candidates || []" :key="`near-${candidate.id}`">
                    <div class="cleanup-select-row">
                      <input
                        type="checkbox"
                        :checked="isCleanupSelected(candidate.id)"
                        @change="toggleCleanupSelection(candidate.id)"
                      />
                      <span class="cleanup-item-text">
                        <span class="cleanup-item-main">{{ candidate.name }} · {{ candidate.resolution || '未知分辨率' }} · {{ formatDuration(candidate.duration) || '00:00' }}</span>
                        <span v-if="candidate.path" class="cleanup-item-path" :title="candidate.path">{{ candidate.path }}</span>
                      </span>
                      <span class="cleanup-item-actions">
                        <button type="button" class="btn-secondary btn-compact" @click="previewCleanupVideo(candidate)">预览</button>
                      </span>
                    </div>
                  </li>
                </ul>
              </div>
            </div>

            <div v-if="cleanupDialog.analysis.low_resolution?.length" class="cleanup-section">
              <h4 class="cleanup-section-title">低清视频</h4>
              <ul>
                <li v-for="video in cleanupDialog.analysis.low_resolution" :key="`res-${video.id}`">
                  <div class="cleanup-select-row">
                    <input
                      type="checkbox"
                      :checked="isCleanupSelected(video.id)"
                      @change="toggleCleanupSelection(video.id)"
                    />
                    <span class="cleanup-item-text">
                      <span class="cleanup-item-main">{{ video.name }} · {{ video.resolution || '未知分辨率' }} · {{ formatDuration(video.duration) || '00:00' }}</span>
                      <span v-if="video.path" class="cleanup-item-path" :title="video.path">{{ video.path }}</span>
                    </span>
                    <span class="cleanup-item-actions">
                      <button type="button" class="btn-secondary btn-compact" @click="previewCleanupVideo(video)">预览</button>
                    </span>
                  </div>
                </li>
              </ul>
            </div>

            <div v-if="cleanupDialog.analysis.low_duration?.length" class="cleanup-section">
              <h4 class="cleanup-section-title">短视频</h4>
              <ul>
                <li v-for="video in cleanupDialog.analysis.low_duration" :key="`dur-${video.id}`">
                  <div class="cleanup-select-row">
                    <input
                      type="checkbox"
                      :checked="isCleanupSelected(video.id)"
                      @change="toggleCleanupSelection(video.id)"
                    />
                    <span class="cleanup-item-text">
                      <span class="cleanup-item-main">{{ video.name }} · {{ formatDuration(video.duration) || '00:00' }} · {{ video.resolution || '未知分辨率' }}</span>
                      <span v-if="video.path" class="cleanup-item-path" :title="video.path">{{ video.path }}</span>
                    </span>
                    <span class="cleanup-item-actions">
                      <button type="button" class="btn-secondary btn-compact" @click="previewCleanupVideo(video)">预览</button>
                    </span>
                  </div>
                </li>
              </ul>
            </div>

            <div
              v-if="!(cleanupDialog.analysis.duplicate_groups?.length || cleanupDialog.analysis.near_duplicate_groups?.length || cleanupDialog.analysis.same_source_groups?.length || cleanupDialog.analysis.low_duration?.length || cleanupDialog.analysis.low_resolution?.length)"
              class="cleanup-empty"
            >
              当前没有命中轻量清理规则的候选项。
            </div>
          </div>
        </div>

        <div class="cleanup-modal-footer">
          <button @click="reanalyzeCleanupCandidates" class="btn-secondary" :disabled="cleanupDialog.loading || cleanupDialog.processing">重新分析</button>
          <button
            @click="trashSelectedCleanupCandidates"
            class="btn-danger"
            :disabled="cleanupSelection.length === 0 || cleanupDialog.loading || cleanupDialog.processing"
          >
            {{ cleanupDialog.processing ? '处理中...' : `将选中项移入回收站 (${cleanupSelection.length})` }}
          </button>
          <button @click="cleanupDialog.show = false" class="btn-primary">关闭</button>
          <button v-if="cleanupDialog.loading" @click="cleanupDialog.show = false" class="btn-secondary">后台继续分析</button>
        </div>
    </BaseModal>

    <BaseModal v-if="subtitlePreview.show" class="subtitle-preview-modal">
        <h3>字幕预览</h3>
        <p class="cleanup-intro" v-if="subtitlePreview.video">{{ subtitlePreview.video.name }}</p>

        <div v-if="subtitlePreview.loading" class="cleanup-loading">正在读取字幕片段...</div>
        <div v-else-if="subtitlePreview.error" class="cleanup-error">{{ subtitlePreview.error }}</div>
        <div v-else-if="subtitlePreview.segments.length" class="subtitle-preview-list">
          <div
            v-for="segment in subtitlePreview.segments"
            :key="`${segment.index}-${segment.start_time_ms}`"
            :class="['subtitle-segment', { 'subtitle-segment-match': segmentMatchesKeyword(segment) }]"
          >
            <div class="subtitle-segment-time">
              {{ formatTimestamp(segment.start_time_ms) }} - {{ formatTimestamp(segment.end_time_ms) }}
              <span v-if="segmentMatchesKeyword(segment)" class="subtitle-match-badge">命中</span>
            </div>
            <div class="subtitle-segment-text">{{ segment.text }}</div>
          </div>
        </div>
        <div v-else class="cleanup-empty">当前视频还没有可预览的字幕片段。</div>

        <div class="modal-actions">
          <button @click="subtitlePreview.show = false" class="btn-primary">关闭</button>
        </div>
    </BaseModal>

    <!-- 字幕操作弹窗（确认/进度/结果） -->
    <BaseModal v-if="subtitleDialog.show" class="download-modal">
        <h3>{{ subtitleDialog.title }}</h3>
        <p>{{ subtitleDialog.msg }}</p>

        <!-- 引擎与语言选择 (确认生成时显示) -->
        <div v-if="subtitleDialog.mode === 'confirm'" class="lang-select-box">
          <label class="dialog-field-label">字幕引擎</label>
          <select v-model="selectedSubtitleEngine" @change="refreshSubtitleConfirmCopy" class="search-input dialog-select">
            <option v-for="status in subtitleEngineStatuses" :key="status.engine" :value="status.engine" :disabled="!status.supported">
              {{ status.display_name }}{{ !status.supported ? '（当前平台不可用）' : '' }}
            </option>
          </select>
          <p v-if="selectedSubtitleEngineStatus?.reason_message" class="dialog-field-hint">{{ selectedSubtitleEngineStatus.reason_message }}</p>

          <template v-if="subtitleSourceLangVisible">
            <label class="dialog-field-label dialog-field-label--spaced">识别源语言</label>
            <select v-model="sourceLang" class="search-input dialog-select">
              <option v-for="opt in languageOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
            </select>
            <p class="dialog-field-hint">如果自动检测不准，请手动指定视频中的语言。</p>
          </template>
        </div>

        <!-- 下载进度条 -->
        <template v-if="subtitleDialog.mode === 'progress'">
          <div class="progress-bar-container">
            <div class="progress-bar" :style="{ width: subtitleDialog.percent + '%' }"></div>
          </div>
          <p class="progress-text">{{ subtitleDialog.percent }}%</p>
          <p v-if="subtitleDialog.progressAction === 'generate'" class="progress-meta">
            当前阶段：{{ subtitleProgressPhaseLabel }}
            <span v-if="subtitleElapsedText"> · 已运行 {{ subtitleElapsedText }}</span>
          </p>
          <p v-if="subtitleDialog.progressAction === 'generate'" class="progress-hint">
            {{ subtitleProgressHint }}
          </p>
          <div class="modal-actions">
            <button v-if="subtitleDialog.progressAction === 'generate'" @click="minimizeSubtitleProgress" class="btn-secondary">后台继续</button>
            <button v-if="subtitleDialog.progressAction === 'generate'" @click="cancelSubtitle" class="btn-danger">取消生成</button>
            <button v-else @click="subtitleDialog.show = false" class="btn-secondary">后台继续准备</button>
          </div>
        </template>

        <!-- 确认按钮 -->
        <div v-if="subtitleDialog.mode === 'confirm'" class="modal-actions">
          <button @click="subtitleDialog.show = false; pendingForceRequest = null; pendingSubtitleVideo = null;" class="btn-secondary">取消</button>
          <button @click="onSubtitleConfirm" class="btn-primary" :disabled="subtitleConfirmDisabled">{{ subtitleConfirmActionLabel }}</button>
        </div>

        <!-- 结果关闭按钮 -->
        <div v-if="subtitleDialog.mode === 'result'" class="modal-actions">
          <button @click="subtitleDialog.show = false" class="btn-primary">确定</button>
        </div>
    </BaseModal>
  </div>
</template>

<style scoped>
.tags-filter { padding: 6px 0 10px; border-bottom: 1px solid var(--border-color); margin-bottom: 12px; }
.tags-scroll-container {
  display: flex;
  gap: 5px;
  flex-wrap: wrap;
  overflow: visible;
  padding-bottom: 0;
  align-items: flex-start;
}
.ai-review-badge {
  display: inline-flex;
  min-width: 18px;
  height: 18px;
  margin-left: 6px;
  padding: 0 5px;
  align-items: center;
  justify-content: center;
  border-radius: 999px;
  background: var(--danger-color);
  color: #fff;
  font-size: 11px;
  font-weight: 700;
}

.toolbar .search-group {
  flex: 1 1 360px;
  min-width: 280px;
  display: flex;
  align-items: center;
  gap: 8px;
}
.toolbar .search-group .select-input {
  flex: 0 0 120px;
}
.toolbar .search-group .search-input {
  flex: 1 1 auto;
  min-width: 0;
}
.toolbar {
  position: sticky;
  top: 10px;
  z-index: 90;
  display: flex;
  flex-direction: column;
  gap: 9px;
  margin-bottom: 10px;
  padding: 9px;
  border-radius: 16px;
}

.toolbar-primary {
  display: flex;
  flex-wrap: wrap;
  min-width: 0;
  align-items: center;
  gap: 10px;
}
.layout-toggle {
  display: inline-flex;
  gap: 3px;
  padding: 3px;
  border: 1px solid var(--border-color);
  border-radius: 8px;
}
.layout-toggle .btn-secondary.active {
  background: var(--accent-color);
  color: white;
}

.filter-group {
  display: flex;
  flex: 0 1 auto;
  flex-wrap: wrap;
  gap: 8px;
}

.filter-group .select-input {
  width: 132px;
}
.toolbar-control {
  height: var(--h-unit);
  border-color: var(--border-color);
  border-radius: var(--radius);
  background-color: var(--control-bg);
  color: var(--text-primary);
  font-size: 13px;
}
.rating-filter-input {
  width: 88px;
  min-width: 78px;
  padding-inline: 10px;
}

.toolbar-secondary,
.toolbar-management {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  padding-top: 9px;
  border-top: 1px solid var(--border-color);
}

.toolbar-secondary {
  justify-content: space-between;
  gap: 8px 18px;
}

.toolbar-cluster {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 8px;
}

.toolbar-cluster .select-input {
  width: 132px;
  flex: 0 0 132px;
}

.toolbar-cluster .saved-view-select {
  width: 168px;
  flex-basis: 168px;
}

.toolbar-management {
  justify-content: flex-end;
}

/* 批量操作栏内嵌在 .toolbar 中，随其一起吸顶：选中项后向下滚动时按钮
   必须保持可见，否则用户要滚回顶部才能操作（旧实现是流内元素，会被滚走）。
   放在 .toolbar 内部而非自行设 sticky top，是为了避免依赖主工具栏的高度
   ——它是 flex column 且窄屏会换行，高度不固定。 */
.selection-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin: 0;
  padding: 8px 10px;
  border-radius: 12px;
  border: 1px solid var(--accent-color);
  background: color-mix(in srgb, var(--accent-color) 12%, transparent);
  color: var(--text-primary);
  font-size: 13px;
}

.selection-toolbar__actions {
  display: flex;
  gap: 8px;
}

.scan-sync-status {
  margin: 0 0 10px;
  padding: 8px 10px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius);
  background: var(--control-bg);
  color: var(--text-secondary);
  font-size: 13px;
}

.scan-sync-status--running {
  border-color: var(--border-strong);
}

.scan-sync-status--success {
  border-color: var(--accent-border);
  color: var(--accent-color);
}

.scan-sync-status--warning {
  border-color: var(--warning-border);
  color: var(--warning-color);
}

.scan-sync-status--error {
  border-color: var(--danger-border);
  color: var(--danger-color);
}

.undo-delete-banner {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0 0 10px;
  padding: 8px 10px;
  border: 1px solid var(--accent-border);
  border-radius: var(--radius);
  background: var(--control-bg);
  color: var(--text-secondary);
  font-size: 13px;
}

.undo-delete-banner__close {
  margin-left: auto;
  border: 0;
  background: transparent;
  color: var(--text-muted);
  cursor: pointer;
  font-size: 18px;
}

.page-content--with-preview .toolbar .search-group {
  flex-basis: 100%;
}

.page-content--with-preview .toolbar-management {
  justify-content: flex-start;
}

@media (max-width: 920px) {
  .toolbar .search-group {
    flex-basis: 100%;
  }

  .filter-group,
  .toolbar-secondary,
  .toolbar-management,
  .selection-toolbar {
    flex-wrap: wrap;
  }

  .toolbar-secondary,
  .toolbar-management {
    justify-content: flex-start;
  }
}

@media (max-width: 620px) {
  .toolbar .search-group,
  .toolbar-cluster {
    flex-wrap: wrap;
  }

  .toolbar .search-group .search-input {
    flex-basis: 100%;
  }
}
:deep(.download-modal) {
  width: 400px;
  text-align: center;
  padding: 30px;
}
.rename-input {
  margin: 15px 0;
}
.rename-hint {
  color: var(--text-muted);
  font-size: 12px;
}
.folder-rename-source {
  margin: 12px 0 4px;
  color: var(--text-secondary);
  font-size: 12px;
  line-height: 1.5;
  overflow-wrap: anywhere;
}
.lang-select-box {
  margin-top: 15px;
}
.dialog-field-label {
  display: block;
  margin-bottom: 8px;
  color: var(--text-secondary);
  font-size: 13px;
}
.dialog-field-label--spaced {
  margin-top: 12px;
}
.dialog-select {
  height: 36px;
  padding: 0 10px;
}
.dialog-field-hint {
  margin-top: 5px;
  color: var(--text-muted);
  font-size: 11px;
}
.progress-bar-container {
  width: 100%;
  height: 10px;
  background-color: var(--review-progress-track-bg);
  border-radius: 5px;
  margin: 20px 0;
  overflow: hidden;
}
.progress-bar {
  height: 100%;
  background-color: var(--success-bright);
  transition: width 0.3s ease;
}
.progress-text {
  font-size: 0.9em;
  color: var(--review-text-muted);
  margin: 0;
}
.progress-meta {
  font-size: 13px;
  color: var(--review-text-meta);
  margin: 10px 0 0;
}
.progress-hint {
  font-size: 12px;
  line-height: 1.6;
  color: var(--review-text-muted);
  margin: 8px 0 0;
}
:deep(.cleanup-modal) {
  width: min(920px, calc(100vw - 32px));
  max-width: calc(100vw - 32px);
  max-height: calc(100vh - 48px);
  overflow: hidden;
  padding: 0;
  display: flex;
  flex-direction: column;
}
.cleanup-modal-header,
.cleanup-modal-footer {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  padding: 18px 22px;
  flex: 0 0 auto;
  background: var(--panel-bg);
}
.cleanup-modal-header {
  border-bottom: 1px solid var(--border-color);
}
.cleanup-modal-header h3 {
  margin: 0;
}
.cleanup-modal-footer {
  align-items: center;
  justify-content: flex-end;
  border-top: 1px solid var(--border-color);
}
.cleanup-modal-body {
  flex: 1 1 auto;
  min-height: 0;
  overflow-y: auto;
  padding: 16px 22px 20px;
}
.cleanup-intro,
.cleanup-loading,
.cleanup-error,
.cleanup-empty {
  color: var(--review-text-muted);
  font-size: 13px;
}
.cleanup-intro--muted {
  color: var(--review-text-secondary);
  margin-top: 4px;
}
.cleanup-summary {
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
  margin: 0 0 14px;
  font-size: 13px;
  color: var(--review-text-emphasis);
}
.cleanup-summary span {
  padding: 5px 9px;
  border: 1px solid var(--border-color);
  border-radius: 6px;
  background: var(--review-neutral-chip-bg);
}
.cleanup-progress-meta,
.cleanup-progress-hint,
.cleanup-progress-path {
  margin-top: 8px;
  font-size: 13px;
  color: var(--review-text-secondary);
}
.cleanup-progress-path {
  word-break: break-all;
}
.cleanup-toolbar {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 14px;
}
.cleanup-section {
  margin-top: 18px;
}
.cleanup-section-title {
  position: sticky;
  top: -16px;
  z-index: 1;
  margin: 0 0 10px;
  padding: 8px 0;
  font-size: 14px;
  font-weight: 700;
  color: var(--review-text-strong);
  background: var(--panel-bg);
  border-bottom: 1px solid var(--border-color);
}
.cleanup-card {
  padding: 12px 14px;
  border: 1px solid var(--review-border-color);
  border-radius: 8px;
  margin-top: 10px;
  background: var(--review-subtle-bg);
}
.cleanup-card p,
.cleanup-card ul,
.cleanup-section ul {
  margin: 6px 0;
}
.cleanup-keep-row {
  display: flex;
  align-items: flex-start;
  gap: 6px;
}
.cleanup-select-row {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  min-width: 0;
  padding: 8px 0;
  border-top: 1px solid var(--neutral-softer);
}
.cleanup-select-row--original {
  border-top: 0;
  padding-top: 0;
}
.cleanup-item-text {
  display: flex;
  flex-direction: column;
  flex: 1 1 auto;
  min-width: 0;
  gap: 2px;
}
.cleanup-item-main {
  color: var(--review-text-strong);
  font-size: 13px;
  font-weight: 600;
}
.cleanup-item-path {
  font-size: 11px;
  color: var(--review-text-path);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.cleanup-item-actions {
  display: flex;
  gap: 6px;
  flex: 0 0 auto;
}
.btn-compact {
  height: 28px;
  padding: 0 10px;
  font-size: 12px;
}
:deep(.subtitle-preview-modal) {
  width: 760px;
  max-width: calc(100vw - 32px);
  max-height: calc(100vh - 48px);
  overflow-y: auto;
  padding: 28px;
}
.subtitle-queue-panel {
  margin: 12px 0;
  padding: 12px 16px;
}
.subtitle-queue-heading,
.subtitle-queue-task {
  display: flex;
  align-items: center;
  gap: 10px;
}
.subtitle-queue-heading {
  justify-content: space-between;
  margin-bottom: 8px;
}
.subtitle-queue-task {
  min-height: 32px;
  border-top: 1px solid var(--neutral-soft);
  font-size: 13px;
}
.subtitle-queue-status {
  flex: 0 0 70px;
  color: var(--accent-deep);
  font-size: 12px;
}
.subtitle-queue-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.subtitle-preview-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
  margin-top: 16px;
}
.subtitle-segment {
  border: 1px solid var(--review-border-color);
  border-radius: 10px;
  padding: 12px 14px;
  background: var(--review-solid-bg);
}
.subtitle-segment-match {
  border-color: var(--accent-deep);
  background: var(--review-accent-soft);
}
.subtitle-segment-time {
  font-size: 12px;
  color: var(--review-text-muted);
  margin-bottom: 6px;
}
.subtitle-segment-text {
  white-space: pre-wrap;
  line-height: 1.5;
}
.subtitle-match-badge {
  display: inline-block;
  margin-left: 8px;
  padding: 2px 8px;
  border-radius: 999px;
  background: var(--review-accent-badge-bg);
  color: var(--accent-deep);
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

.enhance-source-name { font-weight: 650; margin-bottom: 10px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.enhance-task-heading { font-size: 14px; margin-bottom: 8px; }
.enhance-task-row { display: flex; align-items: center; justify-content: space-between; gap: 10px; padding: 6px 0; border-bottom: 1px solid var(--border-color); font-size: 12px; }
.enhance-task-main { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.enhance-task-actions { flex: 0 0 auto; display: flex; gap: 6px; }
</style>

<script>
import { SearchLibraryVideoPage, SearchSemanticVideos, FindSimilarVideos, ListRecentlyPlayedWithFilter, GetLibrarySubtitleHits, PlayVideo, PlayRandomVideoWithFilter, SetVideoFavorite, SetVideoWatched, UpdateVideoWatchProgress, ListSavedLibraryViews, SaveLibraryView, DeleteSavedLibraryView, RejectSameSourceRelation, OpenDirectory, DeleteVideo, BatchDeleteVideos, ListTrashEntries, RestoreTrashEntry, RemoveTagFromVideo, UpdateSettings, GetSettings, GetSubtitleEngineStatuses, PrepareSubtitleEngine, GenerateSubtitle, ForceGenerateSubtitle, RenameVideo, RenameDirectory, MoveVideo, BatchMoveVideos, MoveDirectory, SelectFolderToRename, SelectMigrationSourceDirectory, SelectMigrationDestinationDirectory, CancelSubtitle, CancelSubtitleTask, GetSubtitleQueueState, GetCleanupStatus, GetAITaggingStatusSummary, StartCleanupAnalysis, GetSubtitleSegments, GetPreviewSession, PreviewExternally, SyncScanDirectories, StartTechnicalBackfill, GetTechnicalBackfillStatus, CancelTechnicalBackfill, StartPerceptualHashBackfill, DismissNearDuplicateGroup, GetEnhancementCapability, GetEnhancementVideoPreflight, CreateEnhancementTask, ListEnhancementTasks, CancelEnhancementTask, RetryEnhancementTask, GetPerceptualHashBackfillStatus, CancelPerceptualHashBackfill, StartLocalMetadataBackfill, GetLocalMetadataBackfillStatus, CancelLocalMetadataBackfill, ExportLocalMetadataNFO, StartLocalMetadataExport, GetLocalMetadataExportStatus, CancelLocalMetadataExport } from '../../wailsjs/go/main/App';
import ScanDialog from './ScanDialog.vue';
import TagManagerDialog from './TagManagerDialog.vue';
import AddTagDialog from './AddTagDialog.vue';
import DeleteConfirmDialog from './DeleteConfirmDialog.vue';
import TagDeleteDialog from './TagDeleteDialog.vue';
import PreviewDrawer from './PreviewDrawer.vue';
import SubtitleWorkbench from './SubtitleWorkbench.vue';
import TrashRestoreDialog from './TrashRestoreDialog.vue';
import VirtualVideoList from './VirtualVideoList.vue';
import VideoListRow from './VideoListRow.vue';
import AITagReviewDialog from './AITagReviewDialog.vue';
import LocalMetadataDialog from './LocalMetadataDialog.vue';
import { logFrontend } from '../utils/frontendLog.js';
import { defaultRangeEngine, estimateVideoRowHeight } from '../utils/virtualList.js';
import BaseModal from './ui/BaseModal.vue';
import { patchVideoFromDetails } from '../utils/mediaDetails.js';
import { shortcutActionForEvent } from '../utils/keyboardShortcuts.js';

export default {
  name: 'VideoListPage',
  components: { ScanDialog, TagManagerDialog, AddTagDialog, DeleteConfirmDialog, TagDeleteDialog, PreviewDrawer, SubtitleWorkbench, LocalMetadataDialog, TrashRestoreDialog, VirtualVideoList, VideoListRow, AITagReviewDialog, BaseModal },
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
      searchKeyword: '',
      searchMode: 'file',
      semanticSimilarVideoID: 0,
      semanticCoverage: null,
      semanticSearchError: '',
      enhanceCapability: null,
      enhanceDialog: { show: false, video: null, profile: 'general', creating: false, error: '', preflight: null },
      enhanceTasks: [],
      smartView: '',
      smartViewOptions: [
        { label: '全部视频', value: '' },
        { label: '继续观看', value: 'continue_watching' },
        { label: '收藏', value: 'favorites' },
        { label: '最近播放', value: 'recently_played' },
        { label: '未看', value: 'unwatched' },
        { label: '已看', value: 'watched' },
        { label: '最近添加', value: 'recently_added' },
        { label: '未打标签', value: 'untagged' },
        { label: '无字幕', value: 'no_subtitle' },
        { label: '路径失效', value: 'stale' }
      ],
      savedViews: [],
      selectedSavedViewID: 0,
      saveViewDialog: { show: false, name: '', saving: false, error: '' },
      randomMode: 'balanced',
      recentRandomVideoIDs: [],
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
      contextMenu: { show: false, x: 0, y: 0, video: null },
      showScanDialog: false,
      incrementalScan: { running: false, state: 'idle', message: '' },
      migrationRunning: false,
      showTagManagerDialog: false,
      trashDialog: { show: false },
      undoNotice: null,
      undoNoticeTimer: null,
      undoing: false,
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
      technicalBackfill: { running: false, preparing: false, cancelled: false, completed: false, total: 0, processed: 0, succeeded: 0, skipped: 0, failed: 0, failures: [] },
      perceptualHash: { running: false, cancelled: false, completed: false, total: 0, processed: 0, succeeded: 0, skipped: 0, failed: 0, failures: [] },
      localMetadataBackfill: { running: false, cancelled: false, completed: false, total: 0, processed: 0, succeeded: 0, skipped: 0, failed: 0, failures: [] },
	  localMetadataExport: { running: false, cancelled: false, completed: false, total: 0, processed: 0, succeeded: 0, failed: 0, failures: [] },
      localMetadataDialog: { show: false, videoIds: [] },
      cleanupDialog: {
        show: false,
        loading: false,
        processing: false,
        analysis: null,
        error: '',
        progress: { stage: '', message: '', current: 0, total: 0, path: '' }
      },
      cleanupSelection: [],
      cleanupStartedAt: 0,
      cleanupNow: Date.now(),
      cleanupTimer: null,
      subtitlePreview: { show: false, loading: false, error: '', video: null, segments: [] },
      subtitleWorkbench: { show: false, video: null },
      selectedPreviewVideoId: null,
      previewVideoSnapshot: null,
      previewOpen: false,
      previewSession: null,
      previewStartTimeMs: null,
      wheelFallbackTarget: null,
      wheelFallbackHandler: null,
      rangeEngine: defaultRangeEngine,
      homeListVirtualizationEnabled: true,
      // Subtitle states
      generatingSubtitleIds: [],
      subtitleDialog: { show: false, mode: 'confirm', title: '', msg: '', percent: 0, progressAction: '', phase: '', requiresPrepare: false },
      subtitleEngineStatuses: [],
      selectedSubtitleEngine: 'whisperx',
      pendingSubtitleVideo: null,
      pendingForceRequest: null,
      subtitleProgressStartedAt: 0,
      subtitleProgressNow: Date.now(),
      subtitleProgressTimer: null,
      subtitleProgressTaskID: null,
      subtitleProgressVideoID: null,
      minimizedSubtitleTaskIds: [],
      subtitleQueue: { active_task: null, queued_tasks: [], total: 0 },
      cancellingSubtitleTaskIds: [],
      runtimeOffHandlers: [],
      searchDebounceTimer: null,
      sourceLang: 'auto',
      languageOptions: [
        { label: '自动检测', value: 'auto' },
        { label: '中文 (Chinese)', value: 'chinese' },
        { label: '英语 (English)', value: 'english' },
        { label: '日语 (Japanese)', value: 'japanese' },
        { label: '韩语 (Korean)', value: 'korean' },
        { label: '德语 (German)', value: 'german' },
        { label: '法语 (French)', value: 'french' },
        { label: '西班牙语 (Spanish)', value: 'spanish' }
      ],
      // 重命名弹窗
      renameDialog: { show: false, video: null, newName: '', ext: '' },
      folderRenameDialog: { show: false, source: '', currentName: '', newName: '', error: '' },
    };
  },
  mounted() {
    this.configureHomeListVirtualization();
    this.loadVideos();
    this.loadSavedLibraryViews();
    this.refreshSubtitleQueue();
    this.refreshAITagSummary();
    this.refreshTechnicalBackfillStatus();
    this.refreshPerceptualHashBackfillStatus();
    this.refreshLocalMetadataBackfillStatus();
	this.refreshLocalMetadataExportStatus();
    this.aiTagSummaryTimer = window.setInterval(this.refreshAITagSummary, 60000);
    this.attachWheelFallback();
	window.addEventListener('keydown', this.handleLibraryShortcut);
    document.addEventListener('click', this.hideContextMenu);

    if (window.runtime?.EventsOn) {
      this.registerRuntimeEvent('subtitle-progress', (data) => {
        const nextAction = data?.action || '';
        if (nextAction === 'generate' && !this.acceptSubtitleTaskEvent(data)) return;
        if (nextAction === 'generate') {
          this.startSubtitleProgressTracking();
        } else {
          this.resetSubtitleProgressTracking();
        }

        this.subtitleDialog.show = true;
        this.subtitleDialog.mode = 'progress';
        this.subtitleDialog.progressAction = nextAction;
        this.subtitleDialog.phase = data.phase || '';
        this.subtitleDialog.title = nextAction === 'generate' ? '正在生成字幕' : '正在准备组件';
        this.subtitleDialog.percent = data.percent;
        this.subtitleDialog.msg = data.message || '';
      });

      this.registerRuntimeEvent('subtitle-prepare-complete', async () => {
        await this.loadSubtitleEngineStatuses();
        if (this.subtitleDialog.show && this.subtitleDialog.progressAction === 'prepare') {
          this.subtitleDialog.mode = 'result';
          this.subtitleDialog.title = '✅ 组件准备完成';
          this.subtitleDialog.msg = '当前引擎已就绪，现在可以开始生成字幕。';
        }
      });

      this.registerRuntimeEvent('subtitle-success', (data) => {
        const idx = this.generatingSubtitleIds.indexOf(data.videoID);
        if (idx !== -1) this.generatingSubtitleIds.splice(idx, 1);
		this.refreshSubtitleQueue();
		if (!this.acceptSubtitleTaskCompletion(data)) return;
		this.resetSubtitleProgressTracking();
        this.subtitleDialog.show = true;
        this.subtitleDialog.mode = 'result';
        this.subtitleDialog.title = '✅ 字幕生成成功';
        const warnings = Array.isArray(data.warnings) && data.warnings.length > 0 ? `\n\n注意：\n${data.warnings.join('\n')}` : '';
        this.subtitleDialog.msg = '文件: ' + data.path + warnings;
      });

      this.registerRuntimeEvent('subtitle-cancelled', (data) => {
        const idx = this.generatingSubtitleIds.indexOf(data.videoID);
        if (idx !== -1) this.generatingSubtitleIds.splice(idx, 1);
		this.refreshSubtitleQueue();
		if (!this.acceptSubtitleTaskCompletion(data)) return;
		this.resetSubtitleProgressTracking();
        this.subtitleDialog.show = true;
        this.subtitleDialog.mode = 'result';
        this.subtitleDialog.title = '⏹️ 已取消字幕生成';
        this.subtitleDialog.msg = data.message || '当前字幕任务已取消。';
      });

      this.registerRuntimeEvent('subtitle-queue', (data) => {
        this.applySubtitleQueueState(data);
      });

      this.registerRuntimeEvent('cleanup-progress', async (data) => {
        if (!this.cleanupDialog.show) {
          return;
        }
        this.startCleanupProgressTracking();
        this.cleanupDialog.loading = data?.stage !== 'done';
        this.cleanupDialog.progress = {
          stage: data?.stage || '',
          message: data?.message || '',
          current: Number(data?.current || 0),
          total: Number(data?.total || 0),
          path: data?.path || ''
        };
        if (data?.stage === 'done') {
          const status = await GetCleanupStatus();
          this.applyCleanupStatus(status);
        }
      });

      this.registerRuntimeEvent('video-enhancement-state', view => this.applyEnhancementState(view));
	  this.registerRuntimeEvent('technical-backfill-state', (data) => {
        this.technicalBackfill = { ...this.technicalBackfill, ...(data || {}) };
      });

      this.registerRuntimeEvent('perceptual-hash-state', (data) => {
        this.perceptualHash = { ...this.perceptualHash, ...(data || {}) };
      });

      this.registerRuntimeEvent('local-metadata-backfill', (data) => {
        this.localMetadataBackfill = { ...this.localMetadataBackfill, ...(data || {}) };
        if (data?.completed && data?.succeeded) this.reloadCurrentView();
      });

	  this.registerRuntimeEvent('local-metadata-export', (data) => {
		this.localMetadataExport = { ...this.localMetadataExport, ...(data || {}) };
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
    directories: {
      handler() {
        if (this.settings?.auto_scan_on_startup) {
          this.reloadCurrentView();
        }
      },
      deep: true
    }
  },
  beforeUnmount() {
	window.removeEventListener('keydown', this.handleLibraryShortcut);
    document.removeEventListener('click', this.hideContextMenu);
    this.detachWheelFallback();
    if (this.searchDebounceTimer) {
      clearTimeout(this.searchDebounceTimer);
    }
    if (this.aiTagSummaryTimer) {
      clearInterval(this.aiTagSummaryTimer);
    }
    if (this.undoNoticeTimer) {
      clearTimeout(this.undoNoticeTimer);
    }
    this.teardownRuntimeEvents();
    this.resetCleanupProgressTracking();
    this.resetSubtitleProgressTracking();
  },
  computed: {
    semanticSearchErrorText() {
      const raw = String(this.semanticSearchError || '');
      if (raw.includes('semantic_index_rebuild_required') || raw.includes('需要重建')) return '语义索引需要重建（模型或配置已变更），请到设置页重建索引。';
      if (raw.includes('尚未建立语义索引')) return '当前视频尚未建立语义索引，请先在设置页运行索引补全。';
      if (raw.includes('pgvector')) return '语义检索不可用：数据库缺少 pgvector 扩展。';
      return raw;
    },
    enhanceOutputPreview() {
      const preflight = this.enhanceDialog.preflight;
      if (preflight) {
        return this.enhanceDialog.profile === 'anime' ? preflight.output_basename_anime : preflight.output_basename_general;
      }
      const name = this.enhanceDialog.video?.name || '';
      return `${name.replace(/\.[^.]+$/, '')}.enhanced-${this.enhanceDialog.profile}-2x.mkv`;
    },
    enhanceDiskFloorText() {
      const required = Number(this.enhanceDialog.preflight?.required_bytes || 0);
      if (!required) return '数 GiB';
      return `${(required / (1 << 30)).toFixed(1)} GiB`;
    },
    allVisibleSelected() {
      const ids = this.videos.map(video => video.id);
      return ids.length > 0 && ids.every(id => this.selectedVideoIds.includes(id));
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
    virtualListQueryKey() {
      return JSON.stringify({
        mode: this.searchMode,
        keyword: this.currentQueryKeyword(),
        similarVideoID: this.semanticSimilarVideoID,
        smartView: this.smartView,
        tags: [...this.selectedTags].sort((a, b) => a - b),
        size: this.selectedSizeRange === 'all' ? 'all' : `${this.selectedSizeRange.min}:${this.selectedSizeRange.max}`,
        res: this.selectedResRange === 'all' ? 'all' : `${this.selectedResRange.min}:${this.selectedResRange.max}`,
        rating: `${this.minRating}:${this.maxRating}:${this.sortMode}`
      });
    },
    cleanupCandidateCount() {
      return this.getAllCleanupCandidates().length;
    },
    selectedSubtitleEngineStatus() {
      return this.subtitleEngineStatuses.find(status => status.engine === this.selectedSubtitleEngine) || null;
    },
    subtitleSourceLangVisible() {
      return !!this.selectedSubtitleEngineStatus && this.selectedSubtitleEngineStatus.source_lang_mode !== 'ignored';
    },
    subtitleConfirmActionLabel() {
      if (this.pendingForceRequest) return '强制生成';
      if (this.selectedSubtitleEngineStatus?.needs_prepare) return '准备组件';
      return '开始生成';
    },
    subtitleConfirmDisabled() {
      const status = this.selectedSubtitleEngineStatus;
      if (!status) return true;
      if (!status.supported) return true;
      if (!status.available && !status.needs_prepare) return true;
      return false;
    },
    cleanupStageLabel() {
      const stage = this.cleanupDialog.progress.stage;
      if (stage === 'load') return '读取候选记录';
      if (stage === 'group') return '按文件大小整理候选';
      if (stage === 'hash') return '计算疑似重复文件哈希';
      if (stage === 'done') return '分析完成';
      return '准备分析';
    },
    cleanupElapsedText() {
      if (!this.cleanupStartedAt) return '';
      return this.formatElapsedDuration(this.cleanupNow - this.cleanupStartedAt);
    },
    cleanupProgressPercent() {
      const stage = this.cleanupDialog.progress.stage;
      if (stage === 'load' || stage === 'done') return null;
      const total = Number(this.cleanupDialog.progress.total || 0);
      const current = Number(this.cleanupDialog.progress.current || 0);
      if (total <= 0) return null;
      return Math.min(100, Math.max(0, Math.round((current / total) * 100)));
    },
    subtitleProgressPhaseLabel() {
      const phase = this.subtitleDialog.phase || '';
      const engineName = this.selectedSubtitleEngineStatus?.display_name || '当前引擎';
      switch (phase) {
        case 'preparing-runtime': return '准备运行时';
        case 'downloading-model': return '下载模型';
        case 'extracting-audio': return '提取音频';
        case 'transcribing': return `${engineName} 音频转写`;
        case 'normalizing': return '整理转写结果';
        case 'validating': return '字幕质量校验';
        case 'translating': return '双语翻译';
        case 'merging': return '双语字幕合并';
        case 'finalizing': return '完成收尾';
        default: return '初始化任务';
      }
    },
    subtitleElapsedText() {
      if (!this.subtitleProgressStartedAt) return '';
      return this.formatElapsedDuration(this.subtitleProgressNow - this.subtitleProgressStartedAt);
    },
    subtitleProgressHint() {
      if (this.subtitleDialog.progressAction !== 'generate') {
        return '';
      }

      const phase = this.subtitleProgressPhaseLabel;
      if (phase.includes('音频转写')) {
        return `当前正在进行 ${phase}。长视频或 CPU 模式下停留较久是正常现象，不代表任务假死。`;
      }
      if (phase === '提取音频' || phase === '初始化任务' || phase === '准备运行时' || phase === '下载模型') {
        return '字幕任务已经启动，完成音频准备后会自动进入转写阶段。';
      }
      if (phase === '字幕质量校验' || phase === '双语翻译' || phase === '双语字幕合并') {
        return '转写已经完成，当前正在做结果校验或双语处理，通常会继续向后推进。';
      }
      return '任务仍在继续处理，请等待当前阶段完成。';
    }
  },
  methods: {
	handleLibraryShortcut(event) {
	  if (!this.pageActive || this.previewOpen || this.contextMenu.show || document.querySelector('[role="dialog"]')) return;
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
    applySubtitleQueueState(snapshot) {
      const next = snapshot || {};
      this.subtitleQueue = {
        active_task: next.active_task || null,
        queued_tasks: Array.isArray(next.queued_tasks) ? next.queued_tasks : [],
        total: Number(next.total || 0)
      };
      const ids = [];
      if (this.subtitleQueue.active_task?.video_id) ids.push(this.subtitleQueue.active_task.video_id);
      for (const task of this.subtitleQueue.queued_tasks) {
        if (task.video_id) ids.push(task.video_id);
      }
      this.generatingSubtitleIds = Array.from(new Set(ids));
		if (!this.subtitleProgressTaskID && this.subtitleProgressVideoID) {
			const tasks = [this.subtitleQueue.active_task, ...this.subtitleQueue.queued_tasks].filter(Boolean);
			const task = tasks.find(item => item.video_id === this.subtitleProgressVideoID);
			if (task) this.subtitleProgressTaskID = task.task_id;
		}
    },
    acceptSubtitleTaskEvent(data) {
		const taskID = Number(data?.taskID || 0);
		const videoID = Number(data?.videoID || 0);
		if (taskID && this.minimizedSubtitleTaskIds.includes(taskID)) return false;
		if (this.subtitleProgressTaskID && taskID && this.subtitleProgressTaskID !== taskID) return false;
		if (this.subtitleProgressVideoID && videoID && this.subtitleProgressVideoID !== videoID) return false;
		if (taskID) this.subtitleProgressTaskID = taskID;
		if (videoID) this.subtitleProgressVideoID = videoID;
		return true;
	},
	acceptSubtitleTaskCompletion(data) {
		const taskID = Number(data?.taskID || 0);
		if (taskID && this.minimizedSubtitleTaskIds.includes(taskID)) {
			return false;
		}
		return this.acceptSubtitleTaskEvent(data);
	},
	consumeMinimizedSubtitleTask() {
		if (!this.subtitleProgressTaskID || !this.minimizedSubtitleTaskIds.includes(this.subtitleProgressTaskID)) return false;
		this.minimizedSubtitleTaskIds = this.minimizedSubtitleTaskIds.filter(id => id !== this.subtitleProgressTaskID);
		return true;
	},
    async refreshSubtitleQueue() {
      try {
        this.applySubtitleQueueState(await GetSubtitleQueueState());
      } catch (err) {
        this.debugLog('refresh subtitle queue failed', { error: String(err) }, true);
      }
    },
    async cancelSubtitleTask(taskID) {
      if (!taskID || this.cancellingSubtitleTaskIds.includes(taskID)) return;
      this.cancellingSubtitleTaskIds.push(taskID);
      try {
        await CancelSubtitleTask(taskID);
        await this.refreshSubtitleQueue();
      } catch (err) {
        alert('取消字幕任务失败: ' + err);
      } finally {
        this.cancellingSubtitleTaskIds = this.cancellingSubtitleTaskIds.filter(id => id !== taskID);
      }
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
    async refreshTechnicalBackfillStatus() {
      try {
        this.technicalBackfill = { ...this.technicalBackfill, ...(await GetTechnicalBackfillStatus()) };
      } catch (err) {
        this.debugLog('technical backfill status failed', { err: String(err) }, true);
      }
    },
    async startTechnicalBackfill() {
      try {
        this.technicalBackfill = { ...this.technicalBackfill, ...(await StartTechnicalBackfill()) };
      } catch (err) {
        alert('启动技术信息补全失败: ' + err);
      }
    },
    async cancelTechnicalBackfill() {
      try {
        await CancelTechnicalBackfill();
        await this.refreshTechnicalBackfillStatus();
      } catch (err) {
        alert('取消技术信息补全失败: ' + err);
      }
    },
    async refreshPerceptualHashBackfillStatus() {
      try {
        this.perceptualHash = { ...this.perceptualHash, ...(await GetPerceptualHashBackfillStatus()) };
      } catch (err) {
        this.debugLog('perceptual hash status failed', { err: String(err) }, true);
      }
    },
    async startPerceptualHashBackfill() {
      try {
        this.perceptualHash = { ...this.perceptualHash, ...(await StartPerceptualHashBackfill()) };
      } catch (err) {
        alert('启动近重复指纹补全失败: ' + err);
      }
    },
    async cancelPerceptualHashBackfill() {
      try {
        await CancelPerceptualHashBackfill();
        await this.refreshPerceptualHashBackfillStatus();
      } catch (err) {
        alert('取消近重复指纹补全失败: ' + err);
      }
    },
    async refreshLocalMetadataBackfillStatus() {
      try {
        this.localMetadataBackfill = { ...this.localMetadataBackfill, ...(await GetLocalMetadataBackfillStatus()) };
      } catch (err) {
        this.debugLog('local metadata backfill status failed', { err: String(err) }, true);
      }
    },
    async startLocalMetadataBackfill() {
      try {
        this.localMetadataBackfill = { ...this.localMetadataBackfill, ...(await StartLocalMetadataBackfill()) };
      } catch (err) {
        alert('启动本地资料补全失败: ' + err);
      }
    },
    async cancelLocalMetadataBackfill() {
      try {
        await CancelLocalMetadataBackfill();
        await this.refreshLocalMetadataBackfillStatus();
      } catch (err) {
        alert('取消本地资料补全失败: ' + err);
      }
    },
	async refreshLocalMetadataExportStatus() {
	  try {
		this.localMetadataExport = { ...this.localMetadataExport, ...(await GetLocalMetadataExportStatus()) };
	  } catch (err) {
		this.debugLog('local metadata export status failed', { err: String(err) }, true);
	  }
	},
	async exportLocalMetadataNFO(video) {
	  if (!video?.id) return;
	  try {
		const result = await ExportLocalMetadataNFO(video.id);
		const warning = Array.isArray(result?.warnings) && result.warnings.length ? `\n${result.warnings.join('\n')}` : '';
		alert(`NFO 已写出：${result?.nfo_path || video.name}${warning}`);
	  } catch (err) {
		alert('写出 NFO 失败: ' + err);
	  }
	},
	async startLocalMetadataExport() {
	  if (!window.confirm('将当前筛选结果逐个写出为同名 NFO；已有 NFO 会保留未知字段并合并应用管理字段。继续吗？')) return;
	  try {
		this.localMetadataExport = { ...this.localMetadataExport, ...(await StartLocalMetadataExport({ filter: this.currentLibraryFilter() })) };
	  } catch (err) {
		alert('启动 NFO 写出失败: ' + err);
	  }
	},
	async cancelLocalMetadataExport() {
	  try {
		await CancelLocalMetadataExport();
		await this.refreshLocalMetadataExportStatus();
	  } catch (err) {
		alert('取消 NFO 写出失败: ' + err);
	  }
	},
    startCleanupProgressTracking() {
      if (!this.cleanupStartedAt) {
        this.cleanupStartedAt = Date.now();
      }
      this.cleanupNow = Date.now();
      if (this.cleanupTimer) {
        return;
      }
      this.cleanupTimer = window.setInterval(() => {
        this.cleanupNow = Date.now();
      }, 1000);
    },
    resetCleanupProgressTracking() {
      if (this.cleanupTimer) {
        clearInterval(this.cleanupTimer);
        this.cleanupTimer = null;
      }
      this.cleanupStartedAt = 0;
      this.cleanupNow = Date.now();
      if (this.cleanupDialog?.progress) {
        this.cleanupDialog.progress = { stage: '', message: '', current: 0, total: 0, path: '' };
      }
    },
    applyCleanupStatus(status) {
      if (!status) return;
      this.cleanupDialog.loading = !!status.running;
      this.cleanupDialog.error = status.error || '';
      this.cleanupDialog.analysis = status.analysis || null;
      this.cleanupDialog.progress = status.progress || { stage: '', message: '', current: 0, total: 0, path: '' };
      if (status.running) {
        this.startCleanupProgressTracking();
      } else {
        this.resetCleanupProgressTracking();
        this.cleanupDialog.progress = status.progress || this.cleanupDialog.progress;
      }
    },
    async loadSubtitleEngineStatuses() {
      const statuses = await GetSubtitleEngineStatuses();
      this.subtitleEngineStatuses = Array.isArray(statuses) ? statuses : [];
      const current = this.subtitleEngineStatuses.find(status => status.engine === this.selectedSubtitleEngine && status.supported);
      if (current) return;
      const preferred = this.subtitleEngineStatuses.find(status => status.engine === 'whisperx' && status.supported)
        || this.subtitleEngineStatuses.find(status => status.supported)
        || this.subtitleEngineStatuses[0];
      this.selectedSubtitleEngine = preferred?.engine || 'whisperx';
    },
    refreshSubtitleConfirmCopy() {
      const status = this.selectedSubtitleEngineStatus;
      if (!status) return;
      if (!status.supported) {
        this.subtitleDialog.title = '当前引擎不可用';
        this.subtitleDialog.msg = status.reason_message || '当前平台暂不支持该字幕引擎。';
        this.subtitleDialog.requiresPrepare = false;
        return;
      }
      if (status.needs_prepare) {
        this.subtitleDialog.title = '需要准备组件';
        this.subtitleDialog.msg = status.prepare_hint || `${status.display_name} 需要先准备运行时组件。`;
        this.subtitleDialog.requiresPrepare = true;
        return;
      }
      if (!status.available) {
        this.subtitleDialog.title = '缺少前置条件';
        this.subtitleDialog.msg = status.reason_message || `${status.display_name} 当前还不可用。`;
        this.subtitleDialog.requiresPrepare = false;
        return;
      }
      this.subtitleDialog.title = '准备生成字幕';
      this.subtitleDialog.msg = `我们将使用 ${status.display_name} 为您生成本地字幕，这可能需要几分钟。`;
      this.subtitleDialog.requiresPrepare = false;
    },
    buildSubtitleRequest(video) {
      return {
        video_id: video.id,
        engine: this.selectedSubtitleEngine,
        source_lang: this.subtitleSourceLangVisible ? this.sourceLang : 'auto',
      };
    },
    async handleSubtitleGenerateResult(result, video, forceMode = false) {
      if (!result) return;
		if (this.subtitleProgressVideoID && this.subtitleProgressVideoID !== video.id) return;
		if (this.consumeMinimizedSubtitleTask()) return;
      if (result.status === 'validation_failed' && result.force_eligible) {
        this.subtitleDialog.show = true;
        this.subtitleDialog.mode = 'confirm';
        this.subtitleDialog.title = '⚠️ 字幕质量警告';
        this.subtitleDialog.msg = `${result.message}\n\n是否强制生成，保留当前结果？`;
        this.pendingForceRequest = {
          video,
          request: {
            video_id: video.id,
            engine: result.engine,
            source_lang: result.source_lang || 'auto',
          },
        };
        return;
      }
      this.pendingForceRequest = null;
      this.subtitleDialog.show = true;
      this.subtitleDialog.mode = 'result';
      if (result.status === 'cancelled') {
        this.subtitleDialog.title = '⏹️ 已取消字幕生成';
        this.subtitleDialog.msg = result.message || '当前字幕任务已取消。';
        return;
      }
      this.subtitleDialog.title = '✅ 字幕生成完成';
      const warningText = Array.isArray(result.warnings) && result.warnings.length > 0
        ? `\n\n注意：\n${result.warnings.join('\n')}`
        : '';
      this.subtitleDialog.msg = (forceMode ? '字幕文件已保存到视频同目录下（已确认保留上次校验结果）。' : `字幕文件已保存到视频同目录下。\n${result.path || ''}`) + warningText;
    },
    startSubtitleProgressTracking() {
      if (!this.subtitleProgressStartedAt) {
        this.subtitleProgressStartedAt = Date.now();
      }
      this.subtitleProgressNow = Date.now();
      if (this.subtitleProgressTimer) {
        return;
      }
      this.subtitleProgressTimer = window.setInterval(() => {
        this.subtitleProgressNow = Date.now();
      }, 1000);
    },
    resetSubtitleProgressTracking() {
      if (this.subtitleProgressTimer) {
        clearInterval(this.subtitleProgressTimer);
        this.subtitleProgressTimer = null;
      }
      this.subtitleProgressStartedAt = 0;
      this.subtitleProgressNow = Date.now();
    },
    minimizeSubtitleProgress() {
		let taskID = this.subtitleProgressTaskID;
		if (!taskID && this.subtitleProgressVideoID) {
			const tasks = [this.subtitleQueue.active_task, ...this.subtitleQueue.queued_tasks].filter(Boolean);
			taskID = tasks.find(task => task.video_id === this.subtitleProgressVideoID)?.task_id || null;
		}
		if (taskID && !this.minimizedSubtitleTaskIds.includes(taskID)) {
			this.minimizedSubtitleTaskIds.push(taskID);
		}
      this.subtitleDialog.show = false;
    },
    formatElapsedDuration(ms) {
      if (!ms || ms < 0) return '0s';
      const totalSeconds = Math.floor(ms / 1000);
      const hours = Math.floor(totalSeconds / 3600);
      const minutes = Math.floor((totalSeconds % 3600) / 60);
      const seconds = totalSeconds % 60;

      if (hours > 0) {
        return `${hours}h ${String(minutes).padStart(2, '0')}m ${String(seconds).padStart(2, '0')}s`;
      }
      if (minutes > 0) {
        return `${minutes}m ${String(seconds).padStart(2, '0')}s`;
      }
      return `${seconds}s`;
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
        alert('外部预览失败: ' + err);
      }
    },
    async openCleanupDialog() {
      this.cleanupDialog.show = true;
      try {
        const status = await GetCleanupStatus();
        if (status?.running || status?.completed) {
          this.applyCleanupStatus(status);
          return;
        }
        await this.startNewCleanupAnalysis();
      } catch (err) {
        console.error('获取清理候选失败:', err);
        this.cleanupDialog.error = '获取清理候选失败: ' + err;
        this.cleanupDialog.loading = false;
      }
    },
    async reanalyzeCleanupCandidates() {
      this.cleanupDialog.show = true;
      try {
        await this.startNewCleanupAnalysis();
      } catch (err) {
        console.error('重新分析清理候选失败:', err);
        this.cleanupDialog.error = '重新分析清理候选失败: ' + err;
        this.cleanupDialog.loading = false;
      }
    },
    async startNewCleanupAnalysis() {
      this.cleanupSelection = [];
      this.cleanupDialog.loading = true;
      this.cleanupDialog.processing = false;
      this.cleanupDialog.analysis = null;
      this.cleanupDialog.error = '';
      this.cleanupDialog.progress = { stage: 'load', message: '正在准备清理候选分析…', current: 0, total: 0, path: '' };
      this.startCleanupProgressTracking();
      const started = await StartCleanupAnalysis(5, 480, 320);
      this.applyCleanupStatus(started);
    },
    getAllCleanupCandidates() {
      const analysis = this.cleanupDialog.analysis || {};
      const byID = new Map();
      for (const group of analysis.duplicate_groups || []) {
        if (group.original?.id) {
          byID.set(group.original.id, group.original);
        }
        for (const candidate of group.candidates || []) {
          byID.set(candidate.id, candidate);
        }
      }
      for (const group of analysis.near_duplicate_groups || []) {
        if (group.original?.id) {
          byID.set(group.original.id, group.original);
        }
        for (const candidate of group.candidates || []) {
          byID.set(candidate.id, candidate);
        }
      }
      for (const group of analysis.same_source_groups || []) {
        if (group.alternative?.id) {
          byID.set(group.alternative.id, group.alternative);
        }
      }
      for (const video of analysis.low_duration || []) {
        byID.set(video.id, video);
      }
      for (const video of analysis.low_resolution || []) {
        byID.set(video.id, video);
      }
      return Array.from(byID.values());
    },
    isCleanupSelected(videoID) {
      return this.cleanupSelection.includes(videoID);
    },
    toggleCleanupSelection(videoID) {
      if (!videoID) return;
      if (this.isCleanupSelected(videoID)) {
        this.cleanupSelection = this.cleanupSelection.filter(id => id !== videoID);
        return;
      }
      this.cleanupSelection = [...this.cleanupSelection, videoID];
    },
    getSelectAllCleanupCandidates() {
      const analysis = this.cleanupDialog.analysis || {};
      const byID = new Map();
      for (const group of analysis.duplicate_groups || []) {
        if (group.original?.id) {
          byID.set(group.original.id, group.original);
        }
        for (const candidate of group.candidates || []) {
          byID.set(candidate.id, candidate);
        }
      }
      for (const group of analysis.same_source_groups || []) {
        if (group.alternative?.id) {
          byID.set(group.alternative.id, group.alternative);
        }
      }
      for (const video of analysis.low_duration || []) {
        byID.set(video.id, video);
      }
      for (const video of analysis.low_resolution || []) {
        byID.set(video.id, video);
      }
      return Array.from(byID.values());
    },
    selectAllCleanupCandidates() {
      this.cleanupSelection = this.getSelectAllCleanupCandidates().map(video => video.id);
    },
    clearCleanupSelection() {
      this.cleanupSelection = [];
    },
    async previewCleanupVideo(video) {
      if (!video) return;
      await this.openPreview(video);
    },
    async openEnhanceDialog(video) {
      this.enhanceDialog = { show: true, video, profile: 'general', creating: false, error: '' };
      try {
        this.enhanceCapability = await GetEnhancementCapability();
      } catch (err) {
        this.enhanceCapability = { available: false, message: String(err) };
      }
      try {
        this.enhanceDialog.preflight = await GetEnhancementVideoPreflight(video.id);
      } catch (err) {
        this.enhanceDialog.preflight = null;
      }
      await this.refreshEnhancementTasks();
    },
    async refreshEnhancementTasks() {
      try {
        this.enhanceTasks = await ListEnhancementTasks(10) || [];
      } catch (err) {
        this.enhanceTasks = [];
      }
    },
    applyEnhancementState(view) {
      if (!view?.id) return;
      const index = this.enhanceTasks.findIndex(task => task.id === view.id);
      if (index >= 0) this.enhanceTasks.splice(index, 1, view);
      else this.enhanceTasks.unshift(view);
    },
    enhanceStatusLabel(task) {
      const labels = { queued: '排队中', running: `处理中（${task.phase}）`, cancel_requested: '取消中', cancelled: '已取消', completed: '已完成', failed: `失败（${task.error_code}）` };
      return labels[task.status] || task.status;
    },
    async createEnhancementTask() {
      if (!this.enhanceDialog.video) return;
      this.enhanceDialog.creating = true;
      this.enhanceDialog.error = '';
      try {
        await CreateEnhancementTask({ video_id: this.enhanceDialog.video.id, profile: this.enhanceDialog.profile });
        await this.refreshEnhancementTasks();
      } catch (err) {
        this.enhanceDialog.error = String(err);
      } finally {
        this.enhanceDialog.creating = false;
      }
    },
    async cancelEnhancementTask(task) {
      try {
        await CancelEnhancementTask(task.id);
      } catch (err) {
        alert('取消超分任务失败: ' + err);
      }
      await this.refreshEnhancementTasks();
    },
    async retryEnhancementTask(task) {
      try {
        await RetryEnhancementTask(task.id);
      } catch (err) {
        alert('重试超分任务失败: ' + err);
      }
      await this.refreshEnhancementTasks();
    },
    async dismissNearDuplicateGroup(group) {
      const ids = [group.original?.id, ...(group.candidates || []).map(video => video.id)].filter(Boolean);
      if (ids.length < 2) return;
      try {
        await DismissNearDuplicateGroup(ids);
        this.cleanupDialog.analysis.near_duplicate_groups = (this.cleanupDialog.analysis.near_duplicate_groups || [])
          .filter(item => item !== group);
        this.cleanupSelection = this.cleanupSelection.filter(id => !ids.includes(id));
      } catch (err) {
        alert('忽略近似重复组失败: ' + err);
      }
    },
    async rejectCleanupSameSource(group) {
      if (!group?.relation_id) return;
      try {
        await RejectSameSourceRelation(group.relation_id);
        this.cleanupDialog.analysis.same_source_groups = (this.cleanupDialog.analysis.same_source_groups || [])
          .filter(item => item.relation_id !== group.relation_id);
        if (group.alternative?.id) {
          this.cleanupSelection = this.cleanupSelection.filter(id => id !== group.alternative.id);
        }
        await this.refreshAITagSummary();
      } catch (err) {
        alert('更新同源判断失败: ' + err);
      }
    },
    async trashSelectedCleanupCandidates() {
      const selectedVideos = this.getAllCleanupCandidates().filter(video => this.cleanupSelection.includes(video.id));
      if (selectedVideos.length === 0) {
        return;
      }
      const selectedIDs = selectedVideos.map(video => video.id);

      this.cleanupDialog.processing = true;
      try {
        this.deletingIds = [...new Set([...this.deletingIds, ...selectedIDs])];
        const result = await BatchDeleteVideos(selectedIDs, true);
        const failedIDs = new Set((result?.errors || []).map(item => item.video_id));
        const succeededIDs = selectedIDs.filter(id => !failedIDs.has(id));
        this.videos = this.videos.filter(item => !succeededIDs.includes(item.id));
        this.cleanupSelection = selectedIDs.filter(id => failedIDs.has(id));
        await this.showDeleteUndo(succeededIDs);
        await this.reloadCurrentView();
        await this.reanalyzeCleanupCandidates();
        if (result?.failed > 0) {
          const firstError = result.errors?.[0];
          alert(`批量清理完成：成功 ${result.succeeded} 个，失败 ${result.failed} 个。${firstError ? `\n首个失败：视频 ${firstError.video_id}，${firstError.error}` : ''}`);
        }
      } catch (err) {
        console.error('批量清理失败:', err);
        alert('批量清理失败: ' + err);
      } finally {
        this.cleanupDialog.processing = false;
        this.deletingIds = this.deletingIds.filter(id => !selectedIDs.includes(id));
      }
    },
    async openSubtitlePreview(video) {
      this.subtitlePreview = { show: true, loading: true, error: '', video, segments: [] };
      try {
        const segments = await GetSubtitleSegments(video.id);
        this.subtitlePreview.segments = segments || [];
      } catch (err) {
        console.error('读取字幕片段失败:', err);
        this.subtitlePreview.error = '读取字幕片段失败: ' + err;
      } finally {
        this.subtitlePreview.loading = false;
      }
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
    segmentMatchesKeyword(segment) {
      const keyword = this.searchKeyword.trim().toLowerCase();
      if (!keyword || this.searchMode !== 'subtitle') {
        return false;
      }
      return (segment?.text || '').toLowerCase().includes(keyword);
    },
    formatTimestamp(ms) {
      if (ms === null || ms === undefined) return '00:00:00';
      const totalSeconds = Math.floor(ms / 1000);
      const hours = Math.floor(totalSeconds / 3600);
      const minutes = Math.floor((totalSeconds % 3600) / 60);
      const seconds = totalSeconds % 60;
      return [hours, minutes, seconds].map(value => String(value).padStart(2, '0')).join(':');
    },
    async generateSubtitle(video) {
      console.log('[Subtitle] generateSubtitle called for video:', video.id);
      if (this.generatingSubtitleIds.includes(video.id)) return;

      try {
        await this.loadSubtitleEngineStatuses();
        this.pendingSubtitleVideo = video;
        this.pendingForceRequest = null;
        this.subtitleDialog.show = true;
		this.subtitleProgressTaskID = null;
		this.subtitleProgressVideoID = video.id;
        this.subtitleDialog.mode = 'confirm';
        this.refreshSubtitleConfirmCopy();
      } catch (err) {
        console.error('[Subtitle] Error:', err);
        this.subtitleDialog.show = true;
        this.subtitleDialog.mode = 'result';
        this.subtitleDialog.title = '❌ 检查依赖失败';
        this.subtitleDialog.msg = String(err);
      }
    },
    async onSubtitleConfirm() {
      // 场景一：用户确认强制生成字幕（跳过幻觉检测）
      if (this.pendingForceRequest) {
        const { video, request } = this.pendingForceRequest;
        this.pendingForceRequest = null;
		this.subtitleProgressTaskID = null;
		this.subtitleProgressVideoID = video.id;
        this.subtitleDialog.mode = 'progress';
        this.subtitleDialog.progressAction = 'generate';
        this.subtitleDialog.phase = 'validating';
        this.subtitleDialog.title = '正在强制生成字幕';
        this.subtitleDialog.percent = 0;
        this.subtitleDialog.msg = '跳过质量检测，重新生成...';
        this.startSubtitleProgressTracking();
        this.generatingSubtitleIds.push(video.id);
        try {
          const result = await ForceGenerateSubtitle(request);
          this.resetSubtitleProgressTracking();
          const idx = this.generatingSubtitleIds.indexOf(video.id);
          if (idx !== -1) this.generatingSubtitleIds.splice(idx, 1);
          await this.handleSubtitleGenerateResult(result, video, true);
        } catch (err) {
          this.resetSubtitleProgressTracking();
          const idx = this.generatingSubtitleIds.indexOf(video.id);
          if (idx !== -1) this.generatingSubtitleIds.splice(idx, 1);
			if (!this.consumeMinimizedSubtitleTask() && (!this.subtitleProgressVideoID || this.subtitleProgressVideoID === video.id)) {
				this.subtitleDialog.mode = 'result';
				this.subtitleDialog.title = '❌ 强制生成失败';
				this.subtitleDialog.msg = String(err);
			}
        }
        return;
      }

      // 场景二：用户确认准备依赖
      if (this.subtitleDialog.requiresPrepare) {
        this.resetSubtitleProgressTracking();
        this.subtitleDialog.mode = 'progress';
        this.subtitleDialog.progressAction = 'prepare';
        this.subtitleDialog.phase = 'preparing-runtime';
        this.subtitleDialog.title = '正在准备组件';
        this.subtitleDialog.percent = 0;
        this.subtitleDialog.msg = '准备中... 可关闭此窗口，后台会继续。';
        try {
          await PrepareSubtitleEngine(this.selectedSubtitleEngine);
          await this.loadSubtitleEngineStatuses();
          if (this.subtitleDialog.show) {
            this.subtitleDialog.mode = 'result';
            this.subtitleDialog.title = '✅ 组件准备完成';
            this.subtitleDialog.msg = '现在可以点击字幕按钮生成字幕了。';
          }
        } catch (err) {
          this.subtitleDialog.mode = 'result';
          this.subtitleDialog.title = '❌ 组件准备失败';
          this.subtitleDialog.msg = String(err);
        }
        this.pendingSubtitleVideo = null;
        return;
      }

      // 场景三：依赖已就绪，开始生成
      if (this.pendingSubtitleVideo) {
        const video = this.pendingSubtitleVideo;
        this.pendingSubtitleVideo = null;
		this.subtitleProgressTaskID = null;
		this.subtitleProgressVideoID = video.id;
        this.subtitleDialog.show = true;
        this.subtitleDialog.mode = 'progress';
        this.subtitleDialog.progressAction = 'generate';
        this.subtitleDialog.phase = 'checking';
        this.subtitleDialog.title = '正在生成字幕';
        this.subtitleDialog.percent = 0;
        this.subtitleDialog.msg = `任务已启动，正在准备 ${this.selectedSubtitleEngineStatus?.display_name || '当前引擎'}...`;
        this.startSubtitleProgressTracking();
        await this.doGenerateSubtitle(video);
      }
    },
    async doGenerateSubtitle(video) {
      this.generatingSubtitleIds.push(video.id);
      try {
        this.subtitleDialog.progressAction = 'generate';
        const result = await GenerateSubtitle(this.buildSubtitleRequest(video));
        this.resetSubtitleProgressTracking();
        // 成功后移除 ID（event 也会移除，双重保障）
        const idx = this.generatingSubtitleIds.indexOf(video.id);
        if (idx !== -1) this.generatingSubtitleIds.splice(idx, 1);
        await this.handleSubtitleGenerateResult(result, video, false);
      } catch (err) {
        console.error('[Subtitle] Generate error:', err);
        this.resetSubtitleProgressTracking();
        const idx = this.generatingSubtitleIds.indexOf(video.id);
        if (idx !== -1) this.generatingSubtitleIds.splice(idx, 1);
		if (!this.consumeMinimizedSubtitleTask() && (!this.subtitleProgressVideoID || this.subtitleProgressVideoID === video.id)) {
			this.subtitleDialog.show = true;
			this.subtitleDialog.mode = 'result';
			this.subtitleDialog.title = '❌ 生成字幕失败';
			this.subtitleDialog.msg = String(err);
		}
      }
    },
    async renameVideo(video) {
      const ext = video.name.lastIndexOf('.') > 0 ? video.name.substring(video.name.lastIndexOf('.')) : '';
      const baseName = ext ? video.name.slice(0, -ext.length) : video.name;
      this.renameDialog = { show: true, video, newName: baseName, ext: ext || '(无)' };
      this.$nextTick(() => {
        if (this.$refs.renameInput) this.$refs.renameInput.focus();
      });
    },
    async executeRename() {
      const { video, newName, ext } = this.renameDialog;
      if (!newName.trim()) return;
      try {
        await RenameVideo(video.id, newName.trim());
        const idx = this.videos.findIndex(v => v.id === video.id);
        if (idx !== -1) {
          const finalName = newName.trim() + (ext !== '(无)' ? ext : '');
          this.videos[idx].name = finalName;
          this.videos[idx].path = video.path.replace(video.name, finalName);
        }
        this.renameDialog.show = false;
      } catch (err) {
        console.error('重命名失败:', err);
        alert('重命名失败: ' + err);
      }
    },
    async moveVideo(video) {
      if (!video || this.migrationRunning) return;
      const destination = await SelectMigrationDestinationDirectory();
      if (!destination) return;
      this.migrationRunning = true;
      try {
        const result = await MoveVideo(video.id, destination);
        await this.reloadCurrentView();
        if (result?.warning) alert(`视频迁移完成。\n警告：${result.warning}`);
      } catch (err) {
        console.error('迁移视频失败:', err);
        alert('迁移视频失败: ' + err);
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
          alert(`迁移完成：成功 ${result.succeeded || 0}，失败 ${result.failed || 0}\n${[...failures, ...warnings].join('\n')}`);
        }
      } catch (err) {
        console.error('批量迁移失败:', err);
        alert('批量迁移失败: ' + err);
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
      if (!window.confirm(`将文件夹\n${source}\n迁移到\n${destinationParent}\n并同步更新库内路径，是否继续？`)) return;
      this.migrationRunning = true;
      try {
        const result = await MoveDirectory(source, destinationParent);
        this.$emit('reload-directories');
        await this.reloadCurrentView();
        const warning = result?.warning ? `\n警告：${result.warning}` : '';
        alert(`文件夹迁移完成：更新 ${result?.videos_updated || 0} 个视频、${result?.directories_updated || 0} 个扫描目录。${warning}`);
      } catch (err) {
        console.error('迁移文件夹失败:', err);
        alert('迁移文件夹失败: ' + err);
      } finally {
        this.migrationRunning = false;
      }
    },
    async renameFolder() {
      if (this.migrationRunning) return;
      try {
        const source = await SelectFolderToRename();
        if (!source) return;
        const currentName = String(source).replace(/[\\/]+$/, '').split(/[\\/]/).pop() || '';
        this.folderRenameDialog = { show: true, source, currentName, newName: currentName, error: '' };
        this.$nextTick(() => this.$refs.folderRenameInput?.focus());
      } catch (err) {
        alert('选择文件夹失败: ' + err);
      }
    },
    async executeFolderRename() {
      const source = this.folderRenameDialog.source;
      const newName = this.folderRenameDialog.newName.trim();
      if (!source || !newName || this.migrationRunning) return;
      if (newName === this.folderRenameDialog.currentName) {
        this.folderRenameDialog.error = '请输入不同于当前名称的新名称。';
        return;
      }
      this.migrationRunning = true;
      this.folderRenameDialog.error = '';
      try {
        const result = await RenameDirectory(source, newName);
        this.folderRenameDialog.show = false;
        this.$emit('reload-directories');
        try {
          const refreshedSettings = await GetSettings();
          this.$emit('update-settings', refreshedSettings);
        } catch (settingsErr) {
          console.warn('文件夹重命名后刷新设置失败:', settingsErr);
        }
        await this.reloadCurrentView();
        alert(`文件夹重命名完成：更新 ${result?.videos_updated || 0} 个视频、${result?.directories_updated || 0} 个扫描目录。`);
      } catch (err) {
        console.error('重命名文件夹失败:', err);
        this.folderRenameDialog.error = '重命名失败：' + err;
      } finally {
        this.migrationRunning = false;
      }
    },
    async cancelSubtitle() {
      try {
		if (this.subtitleProgressTaskID) {
			await CancelSubtitleTask(this.subtitleProgressTaskID);
		} else {
			await CancelSubtitle();
		}
        this.resetSubtitleProgressTracking();
        this.subtitleDialog.show = false;
		await this.refreshSubtitleQueue();
      } catch (err) {
        console.error('取消失败:', err);
      }
    },
    hideContextMenu() {
      this.contextMenu.show = false;
    },
    attachWheelFallback() {
      this.$nextTick(() => {
        const scrollOwner = this.$el?.closest?.('.main-view');
        if (!scrollOwner || this.wheelFallbackTarget === scrollOwner) {
          return;
        }
        this.detachWheelFallback();
        this.wheelFallbackTarget = scrollOwner;
        this.wheelFallbackHandler = (event) => {
          if (!this.$el?.contains(event.target)) {
            return;
          }
          this.forwardWheelToScrollOwner(event);
        };
        scrollOwner.addEventListener('wheel', this.wheelFallbackHandler, { capture: true, passive: false });
      });
    },
    detachWheelFallback() {
      if (this.wheelFallbackTarget && this.wheelFallbackHandler) {
        this.wheelFallbackTarget.removeEventListener('wheel', this.wheelFallbackHandler, { capture: true });
      }
      this.wheelFallbackTarget = null;
      this.wheelFallbackHandler = null;
    },
    forwardWheelToScrollOwner(event) {
      if (!event || event.defaultPrevented) return;
      if (this.findScrollableWheelTarget(event.target, event.deltaY)) return;
      const scrollOwner = this.$el?.closest?.('.main-view');
      if (!scrollOwner) return;
      const before = scrollOwner.scrollTop;
      scrollOwner.scrollTop += event.deltaY;
      if (scrollOwner.scrollTop !== before) {
        event.preventDefault();
      }
    },
    findScrollableWheelTarget(target, deltaY) {
      let node = target;
      while (node && node !== this.$el) {
        if (node instanceof HTMLElement) {
          const style = window.getComputedStyle(node);
          const canScrollY = /(auto|scroll)/.test(style.overflowY);
          if (canScrollY && node.scrollHeight > node.clientHeight) {
            if (deltaY > 0 && node.scrollTop < node.scrollHeight - node.clientHeight) return node;
            if (deltaY < 0 && node.scrollTop > 0) return node;
          }
        }
        node = node.parentNode;
      }
      return null;
    },
    calculateScore(video) {
      const weight = this.settings.play_weight || 2.0;
      return video.play_count * weight + video.random_play_count;
    },
    setViewMode(mode) {
      this.viewMode = mode === 'grid' ? 'grid' : 'list';
      window.localStorage?.setItem('cineinsight-library-layout', this.viewMode);
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
      return estimateVideoRowHeight(video, widthBucket, subtitleMode);
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

        if (this.isSubtitleSearchActive(keyword) && newVideos.length > 0) {
          const hits = await GetLibrarySubtitleHits(keyword, newVideos.map(video => video.id));
          const hitsByVideoID = new Map((hits || []).map(hit => [hit.video_id, hit.segment]));
          newVideos = newVideos.map(video => {
            const segment = hitsByVideoID.get(video.id);
            if (!segment) return video;
            return {
              ...video,
              _subtitleMatchText: segment.text || '',
              _subtitleMatchStartMs: segment.start_time_ms,
              _subtitleMatchEndMs: segment.end_time_ms
            };
          });
        }

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
        alert('加载视频失败: ' + err);
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
    openSaveViewDialog() {
      this.saveViewDialog = { show: true, name: '', saving: false, error: '' };
      this.$nextTick(() => this.$refs.saveViewNameInput?.focus());
    },
    async saveCurrentView() {
      const name = this.saveViewDialog.name.trim();
      if (!name || this.saveViewDialog.saving) return;
      this.saveViewDialog.saving = true;
      this.saveViewDialog.error = '';
      try {
        const saved = await SaveLibraryView({ name, ...this.currentLibraryFilter() });
        await this.loadSavedLibraryViews();
        this.selectedSavedViewID = saved.id;
        this.saveViewDialog.show = false;
      } catch (err) {
        this.saveViewDialog.error = String(err);
      } finally {
        this.saveViewDialog.saving = false;
      }
    },
    async applySelectedSavedView() {
      const view = this.savedViews.find(item => item.id === Number(this.selectedSavedViewID));
      if (!view) return;
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
      if (!view || !window.confirm(`确定删除保存视图「${view.name}」吗？`)) return;
      try {
        await DeleteSavedLibraryView(view.id);
        this.selectedSavedViewID = 0;
        await this.loadSavedLibraryViews();
      } catch (err) {
        alert('删除保存视图失败: ' + err);
      }
    },
    async reloadCurrentView() {
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
    formatDuration(seconds) {
      if (!seconds) return '';
      const h = Math.floor(seconds / 3600);
      const m = Math.floor((seconds % 3600) / 60);
      const s = Math.floor(seconds % 60);
      const parts = [];
      if (h > 0) parts.push(h.toString().padStart(2, '0'));
      parts.push(m.toString().padStart(2, '0'));
      parts.push(s.toString().padStart(2, '0'));
      return parts.join(':');
    },
    formatFileSize(bytes) {
      const value = Number(bytes || 0);
      if (value <= 0) return '0 B';
      const units = ['B', 'KB', 'MB', 'GB', 'TB'];
      const index = Math.min(units.length - 1, Math.floor(Math.log(value) / Math.log(1024)));
      const scaled = value / Math.pow(1024, index);
      return `${scaled.toFixed(scaled >= 10 || index === 0 ? 0 : 1)} ${units[index]}`;
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
      const stateSensitiveViews = ['favorites', 'continue_watching', 'unwatched', 'watched'];
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
        alert('更新收藏状态失败: ' + err);
      }
    },
    async toggleVideoWatched(video) {
      try {
        const updated = await SetVideoWatched(video.id, !video.is_watched);
        await this.applyVideoStateChange(updated);
      } catch (err) {
        alert('更新观看状态失败: ' + err);
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
        alert(result.user_message || '播放失败');
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
          alert(`正在随机播放: ${result.video.name}\n${result.selection_reason || '按当前筛选条件选择'}`);
          return;
        }
        await this.applyPlaybackAttemptResult(result);
      } catch (err) {
        console.error('随机播放失败:', err);
        alert('随机播放失败: ' + err);
      }
    },
    async playVideo(id) {
      try {
        const result = await PlayVideo(id);
        await this.applyPlaybackAttemptResult(result);
      } catch (err) {
        console.error('播放失败:', err);
        alert('播放失败: ' + err);
      }
    },
    async openDirectory(id) {
      try {
        await OpenDirectory(id);
      } catch (err) {
        console.error('打开目录失败:', err);
        alert('打开目录失败: ' + err);
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
        alert('删除失败: ' + err);
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
          alert(`批量删除完成：成功 ${result.succeeded} 个，失败 ${result.failed} 个。${firstError ? `\n首个失败：视频 ${firstError.video_id}，${firstError.error}` : ''}`);
        }
      } catch (err) {
        console.error('批量删除失败:', err);
        alert('批量删除失败: ' + err);
      } finally {
        this.deletingIds = this.deletingIds.filter(id => !ids.includes(id));
      }
    },
    showContextMenu(event, video) {
      this.contextMenu = { show: true, x: event.clientX, y: event.clientY, video: video };
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
      this.trashDialog.show = true;
    },
    async showDeleteUndo(videoIDs, preferredVideoID = null) {
      const ids = [...new Set((videoIDs || []).filter(Boolean))];
      if (ids.length === 0) return;
      try {
        const entries = await ListTrashEntries() || [];
        const entry = preferredVideoID
          ? entries.find(item => item.video_id === preferredVideoID) || null
          : ids.length === 1
            ? entries.find(item => item.video_id === ids[0]) || null
            : null;
        this.undoNotice = { count: ids.length, entry };
        if (this.undoNoticeTimer) clearTimeout(this.undoNoticeTimer);
        this.undoNoticeTimer = window.setTimeout(() => {
          this.undoNotice = null;
          this.undoNoticeTimer = null;
        }, 12000);
      } catch (err) {
        console.error('读取回收站失败:', err);
        this.undoNotice = { count: ids.length, entry: null };
      }
    },
    async undoLastDelete() {
      const entry = this.undoNotice?.entry;
      if (!entry) {
        this.openTrashDialog();
        return;
      }
      if (this.undoing) return;
      this.undoing = true;
      try {
        await RestoreTrashEntry(entry.id);
        this.undoNotice = null;
        if (this.undoNoticeTimer) clearTimeout(this.undoNoticeTimer);
        this.undoNoticeTimer = null;
        await this.reloadCurrentView();
      } catch (err) {
        console.error('撤销删除失败:', err);
        alert('撤销删除失败: ' + err);
      } finally {
        this.undoing = false;
      }
    },
    async handleTrashRestored() {
      this.undoNotice = null;
      if (this.undoNoticeTimer) clearTimeout(this.undoNoticeTimer);
      this.undoNoticeTimer = null;
      await this.reloadCurrentView();
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
    async runIncrementalScan() {
      if (this.migrationRunning || this.incrementalScan.running || this.directories.length === 0) {
        return;
      }

      this.incrementalScan = { running: true, state: 'running', message: '正在扫描已配置目录...' };
      try {
        const result = await SyncScanDirectories();
        const errors = Array.isArray(result?.errors) ? result.errors : [];
        const summary = [
          `扫描 ${Number(result?.scanned || 0)} 个文件`,
          `新增 ${Number(result?.added || 0)}`,
          `迁移 ${Number(result?.relocated || 0)}`,
          `移除记录 ${Number(result?.deleted || 0)}`,
          `补全元数据 ${Number(result?.metadata_refreshed || 0)}`,
          `跳过 ${Number(result?.skipped || 0)}`
        ];
        if (errors.length > 0) {
          summary.push(`失败 ${errors.length}`);
        }
        this.incrementalScan = {
          running: false,
          state: errors.length > 0 ? 'warning' : 'success',
          message: `增量扫描完成：${summary.join('，')}`
        };
        this.$emit('reload-directories');
        await this.reloadCurrentView();
      } catch (err) {
        console.error('增量扫描失败:', err);
        this.incrementalScan = {
          running: false,
          state: 'error',
          message: `增量扫描失败：${String(err)}`
        };
      }
    },
    async removeTag(video, tag) {
      try {
        await RemoveTagFromVideo(video.id, tag.id);
        await this.reloadCurrentView();
      } catch (err) {
        console.error('移除标签失败:', err);
        alert('移除标签失败: ' + err);
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
        alert('标签已删除');
      } catch (err) {
        console.error('删除标签失败:', err);
        alert('删除标签失败: ' + err);
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
