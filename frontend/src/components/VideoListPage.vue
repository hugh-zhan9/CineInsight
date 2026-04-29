<template>
  <div :class="['page-content', { 'page-content--with-preview': previewOpen }]">
    <div class="toolbar">
      <div class="search-group">
        <select v-model="searchMode" @change="handleSearch(true)" class="select-input" style="width: 120px; margin-right: 8px;">
          <option value="file">文件搜索</option>
          <option value="subtitle">字幕搜索</option>
        </select>
        <input 
          v-model="searchKeyword" 
          @input="handleSearch()"
          type="text" 
          :placeholder="searchMode === 'subtitle' ? '搜索字幕内容...' : '搜索视频文件名或路径...'" 
          class="search-input"
        />
      </div>
      
      <div class="filter-group">
        <select v-model="selectedSizeRange" @change="handleSearch(true)" class="select-input" style="width: 130px;">
          <option value="all">📁 体积 (全部)</option>
          <option v-for="opt in sizeOptions" :key="opt.label" :value="opt.value">{{ opt.label }}</option>
        </select>
        
        <select v-model="selectedResRange" @change="handleSearch(true)" class="select-input" style="width: 150px;">
          <option value="all">📺 分辨率 (全部)</option>
          <option v-for="opt in resOptions" :key="opt.label" :value="opt.value">{{ opt.label }}</option>
        </select>
      </div>

      <div class="action-group" style="margin-left: auto; display: flex; gap: 8px;">
        <button @click="playRandom" class="btn-random">🎲 随机播放</button>
        <button @click="openCleanupDialog" class="btn-secondary">🧹 清理候选</button>
        <button @click="showScanDialog = true" class="btn-primary">🔍 扫描目录</button>
        <button @click="showTagManagerDialog = true" class="btn-secondary">🏷️ 标签管理</button>
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
          <button type="button" class="tag-chip-delete" @click.stop="requestDeleteTag(tag)">×</button>
        </div>
      </div>
    </div>

    <div class="video-list" ref="videoList">
      <div v-if="videos.length === 0 && !loading" class="empty-state">
        <p>暂无视频，点击"扫描目录"开始导入视频</p>
      </div>
      <template v-else-if="isSubtitleSearchActive()">
        <VideoListRow
          v-for="video in videos"
          :key="video.id"
          :video="video"
          :directories="directories"
          :generating-subtitle-ids="generatingSubtitleIds"
          :deleting-ids="deletingIds"
          @preview="openPreview"
          @play="playVideo"
          @open-directory="openDirectory"
          @generate-subtitle="generateSubtitle"
          @subtitle-preview="openSubtitlePreview"
          @rename="renameVideo"
          @delete="confirmDelete"
          @open-add-tag="openAddTagDialog"
          @remove-tag="removeTag"
          @contextmenu="showContextMenu"
        />
      </template>
      <VirtualVideoList
        v-else-if="videos.length > 0"
        :items="videos"
        :loading="loading"
        :has-more="hasMore"
        :virtualization-enabled="homeListVirtualizationEnabled"
        :subtitle-mode="false"
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
            @preview="openPreview"
            @play="playVideo"
            @open-directory="openDirectory"
            @generate-subtitle="generateSubtitle"
            @subtitle-preview="openSubtitlePreview"
            @rename="renameVideo"
            @delete="confirmDelete"
            @open-add-tag="openAddTagDialog"
            @remove-tag="removeTag"
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
      :video="selectedPreviewVideo"
      :session="previewSession"
      @close="closePreview"
      @preview-externally="previewExternally"
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
      <div @click="confirmDelete(contextMenu.video)" class="danger">删除</div>
    </div>

    <!-- 重命名弹窗 -->
    <div v-if="renameDialog.show" class="modal-overlay">
      <div class="modal download-modal">
        <h3>重命名视频</h3>
        <input
          v-model="renameDialog.newName"
          type="text"
          class="search-input"
          style="margin: 15px 0; width: 100%;"
          placeholder="输入新文件名"
          @keyup.enter="executeRename"
          ref="renameInput"
        />
        <p style="font-size: 0.8em; color: #999;">扩展名会自动保留（{{ renameDialog.ext }}）</p>
        <div class="modal-actions">
          <button @click="renameDialog.show = false" class="btn-secondary">取消</button>
          <button @click="executeRename" class="btn-primary">确认</button>
        </div>
      </div>
    </div>

    <!-- 弹窗组件 -->
    <ScanDialog
      :visible="showScanDialog"
      :directories="directories"
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
      :tags="tags"
      @close="addTagDialog.show = false"
      @tag-added="handleTagAdded"
    />

    <DeleteConfirmDialog
      :visible="deleteDialog.show"
      :video="deleteDialog.video"
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

    <div v-if="cleanupDialog.show" class="modal-overlay">
      <div class="modal cleanup-modal">
        <h3>清理候选审阅</h3>
        <p class="cleanup-intro">当前审阅基于轻量规则：重复文件（大小 + 采样哈希）、低时长、低分辨率。选中的视频会直接移入回收站并从库中移除。</p>

        <div v-if="cleanupDialog.loading" class="cleanup-loading">
          <div>正在分析视频库...</div>
          <div class="cleanup-progress-meta">
            当前阶段：{{ cleanupStageLabel }}
            <span v-if="cleanupElapsedText"> · 已运行 {{ cleanupElapsedText }}</span>
          </div>
          <div v-if="cleanupDialog.progress.total > 0" class="cleanup-progress-meta">
            已处理 {{ cleanupDialog.progress.current }} / {{ cleanupDialog.progress.total }}
            <span v-if="cleanupProgressPercent !== null"> ({{ cleanupProgressPercent }}%)</span>
          </div>
          <div v-if="cleanupDialog.progress.message" class="cleanup-progress-hint">{{ cleanupDialog.progress.message }}</div>
          <div v-if="cleanupDialog.progress.path" class="cleanup-progress-path">当前文件：{{ cleanupDialog.progress.path }}</div>
          <div class="cleanup-progress-hint">该分析会逐个读取视频文件；外置硬盘、休眠磁盘或大库场景下耗时较长，长时间停留不代表已假死。</div>
        </div>
        <div v-else-if="cleanupDialog.error" class="cleanup-error">{{ cleanupDialog.error }}</div>
        <div v-else-if="cleanupDialog.analysis" class="cleanup-body">
          <div class="cleanup-summary">
            <span>重复组 {{ cleanupDialog.analysis.duplicate_groups?.length || 0 }}</span>
            <span>短视频 {{ cleanupDialog.analysis.low_duration?.length || 0 }}</span>
            <span>低清视频 {{ cleanupDialog.analysis.low_resolution?.length || 0 }}</span>
            <span>已选 {{ cleanupSelection.length }}</span>
          </div>

          <div v-if="cleanupCandidateCount" class="cleanup-toolbar">
            <button @click="selectAllCleanupCandidates" class="btn-secondary">全选候选</button>
            <button @click="clearCleanupSelection" class="btn-secondary" :disabled="cleanupSelection.length === 0">清空选择</button>
            <button @click="openCleanupDialog" class="btn-secondary" :disabled="cleanupDialog.loading || cleanupDialog.processing">重新分析</button>
          </div>

          <div v-if="cleanupDialog.analysis.duplicate_groups?.length" class="cleanup-section">
            <h4>重复候选</h4>
            <div
              v-for="group in cleanupDialog.analysis.duplicate_groups"
              :key="`${group.original?.id}-${group.candidates?.length}`"
              class="cleanup-card"
            >
              <div class="cleanup-keep-row">
                <strong>保留：</strong>
                <div class="cleanup-item-text">
                  <span class="cleanup-item-main">{{ group.original?.name }} · {{ group.original?.resolution || '未知分辨率' }}</span>
                  <span v-if="group.original?.path" class="cleanup-item-path" :title="group.original.path">{{ group.original.path }}</span>
                </div>
              </div>
              <p><strong>原因：</strong>{{ group.reason }}</p>
              <ul>
                <li v-for="candidate in group.candidates || []" :key="candidate.id">
                  <label class="cleanup-select-row">
                    <input
                      type="checkbox"
                      :checked="isCleanupSelected(candidate.id)"
                      @change="toggleCleanupSelection(candidate.id)"
                    />
                    <span class="cleanup-item-text">
                      <span class="cleanup-item-main">{{ candidate.name }} · {{ candidate.resolution || '未知分辨率' }}</span>
                      <span v-if="candidate.path" class="cleanup-item-path" :title="candidate.path">{{ candidate.path }}</span>
                    </span>
                  </label>
                </li>
              </ul>
            </div>
          </div>

          <div v-if="cleanupDialog.analysis.low_duration?.length" class="cleanup-section">
            <h4>短视频</h4>
            <ul>
              <li v-for="video in cleanupDialog.analysis.low_duration" :key="`dur-${video.id}`">
                <label class="cleanup-select-row">
                  <input
                    type="checkbox"
                    :checked="isCleanupSelected(video.id)"
                    @change="toggleCleanupSelection(video.id)"
                  />
                  <span class="cleanup-item-text">
                    <span class="cleanup-item-main">{{ video.name }} · {{ formatDuration(video.duration) || '00:00' }}</span>
                    <span v-if="video.path" class="cleanup-item-path" :title="video.path">{{ video.path }}</span>
                  </span>
                </label>
              </li>
            </ul>
          </div>

          <div v-if="cleanupDialog.analysis.low_resolution?.length" class="cleanup-section">
            <h4>低清视频</h4>
            <ul>
              <li v-for="video in cleanupDialog.analysis.low_resolution" :key="`res-${video.id}`">
                <label class="cleanup-select-row">
                  <input
                    type="checkbox"
                    :checked="isCleanupSelected(video.id)"
                    @change="toggleCleanupSelection(video.id)"
                  />
                  <span class="cleanup-item-text">
                    <span class="cleanup-item-main">{{ video.name }} · {{ video.resolution || '未知分辨率' }}</span>
                    <span v-if="video.path" class="cleanup-item-path" :title="video.path">{{ video.path }}</span>
                  </span>
                </label>
              </li>
            </ul>
          </div>

          <div
            v-if="!(cleanupDialog.analysis.duplicate_groups?.length || cleanupDialog.analysis.low_duration?.length || cleanupDialog.analysis.low_resolution?.length)"
            class="cleanup-empty"
          >
            当前没有命中轻量清理规则的候选项。
          </div>
        </div>

        <div class="modal-actions">
          <button
            @click="trashSelectedCleanupCandidates"
            class="btn-danger"
            :disabled="cleanupSelection.length === 0 || cleanupDialog.loading || cleanupDialog.processing"
          >
            {{ cleanupDialog.processing ? '处理中...' : `将选中项移入回收站 (${cleanupSelection.length})` }}
          </button>
          <button @click="cleanupDialog.show = false" class="btn-primary">关闭</button>
        </div>
      </div>
    </div>

    <div v-if="subtitlePreview.show" class="modal-overlay">
      <div class="modal subtitle-preview-modal">
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
      </div>
    </div>
    
    <!-- 字幕操作弹窗（确认/进度/结果） -->
    <div v-if="subtitleDialog.show" class="modal-overlay">
      <div class="modal download-modal">
        <h3>{{ subtitleDialog.title }}</h3>
        <p>{{ subtitleDialog.msg }}</p>
        
        <!-- 引擎与语言选择 (确认生成时显示) -->
        <div v-if="subtitleDialog.mode === 'confirm'" class="lang-select-box" style="margin-top: 15px;">
          <label style="display: block; font-size: 13px; margin-bottom: 8px; color: #666;">字幕引擎</label>
          <select v-model="selectedSubtitleEngine" @change="refreshSubtitleConfirmCopy" class="search-input" style="width: 100%; height: 36px; padding: 0 10px;">
            <option v-for="status in subtitleEngineStatuses" :key="status.engine" :value="status.engine" :disabled="!status.supported">
              {{ status.display_name }}{{ !status.supported ? '（当前平台不可用）' : '' }}
            </option>
          </select>
          <p v-if="selectedSubtitleEngineStatus?.reason_message" style="font-size: 11px; color: #999; margin-top: 5px;">{{ selectedSubtitleEngineStatus.reason_message }}</p>

          <template v-if="subtitleSourceLangVisible">
            <label style="display: block; font-size: 13px; margin: 12px 0 8px; color: #666;">识别源语言</label>
            <select v-model="sourceLang" class="search-input" style="width: 100%; height: 36px; padding: 0 10px;">
              <option v-for="opt in languageOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
            </select>
            <p style="font-size: 11px; color: #999; margin-top: 5px;">如果自动检测不准，请手动指定视频中的语言。</p>
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
      </div>
    </div>
  </div>
</template>

<style scoped>
.toolbar .search-group {
  flex: 1 1 360px;
  min-width: 280px;
  display: flex;
  align-items: center;
}
.toolbar .search-group .select-input {
  flex: 0 0 120px;
}
.toolbar .search-group .search-input {
  flex: 1 1 auto;
  min-width: 0;
}
@media (max-width: 1280px) {
  .toolbar {
    flex-wrap: wrap;
    align-items: stretch;
  }

  .toolbar .search-group {
    flex-basis: 100%;
  }

  .toolbar .action-group {
    margin-left: 0 !important;
    flex-wrap: wrap;
  }
}
.download-modal {
  width: 400px;
  text-align: center;
  padding: 30px;
}
.progress-bar-container {
  width: 100%;
  height: 10px;
  background-color: #f0f0f0;
  border-radius: 5px;
  margin: 20px 0;
  overflow: hidden;
}
.progress-bar {
  height: 100%;
  background-color: #4caf50;
  transition: width 0.3s ease;
}
.progress-text {
  font-size: 0.9em;
  color: #666;
  margin: 0;
}
.progress-meta {
  font-size: 13px;
  color: #374151;
  margin: 10px 0 0;
}
.progress-hint {
  font-size: 12px;
  line-height: 1.6;
  color: #666;
  margin: 8px 0 0;
}
.btn-processing {
  opacity: 0.7;
  cursor: wait;
  background-color: #aaa !important;
}
.cleanup-modal {
  width: 680px;
  max-width: calc(100vw - 32px);
  max-height: calc(100vh - 48px);
  overflow-y: auto;
  padding: 28px;
}
.cleanup-intro,
.cleanup-loading,
.cleanup-error,
.cleanup-empty {
  color: #666;
  font-size: 14px;
}
.cleanup-summary {
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
  margin: 16px 0;
  font-size: 13px;
  color: #444;
}
.cleanup-progress-meta,
.cleanup-progress-hint,
.cleanup-progress-path {
  margin-top: 8px;
  font-size: 13px;
  color: #4b5563;
}
.cleanup-progress-path {
  word-break: break-all;
}
.cleanup-toolbar {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 16px;
}
.cleanup-section {
  margin-top: 18px;
}
.cleanup-card {
  padding: 12px 14px;
  border: 1px solid #e5e7eb;
  border-radius: 10px;
  margin-top: 10px;
  background: rgba(0, 0, 0, 0.02);
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
  align-items: flex-start;
  gap: 8px;
}
.cleanup-item-text {
  display: flex;
  flex-direction: column;
  min-width: 0;
  gap: 2px;
}
.cleanup-item-main {
  color: #111827;
}
.cleanup-item-path {
  font-size: 12px;
  color: #6b7280;
  word-break: break-all;
}
.subtitle-preview-modal {
  width: 760px;
  max-width: calc(100vw - 32px);
  max-height: calc(100vh - 48px);
  overflow-y: auto;
  padding: 28px;
}
.subtitle-preview-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
  margin-top: 16px;
}
.subtitle-segment {
  border: 1px solid #e5e7eb;
  border-radius: 10px;
  padding: 12px 14px;
  background: #fff;
}
.subtitle-segment-match {
  border-color: #0f766e;
  background: rgba(15, 118, 110, 0.06);
}
.subtitle-segment-time {
  font-size: 12px;
  color: #666;
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
  background: rgba(15, 118, 110, 0.12);
  color: #0f766e;
}
.video-subtitle-hit {
  margin: 8px 0 0;
  color: #0f766e;
  font-size: 13px;
}
.video-stale {
  color: #b45309;
  font-weight: 600;
}
</style>

<script>
import { GetVideosPaginated, SearchVideosWithFilters, SearchSubtitleMatches, PlayVideo, PlayRandomVideo, OpenDirectory, DeleteVideo, RemoveTagFromVideo, UpdateSettings, GetSubtitleEngineStatuses, PrepareSubtitleEngine, GenerateSubtitle, ForceGenerateSubtitle, RenameVideo, CancelSubtitle, GetCleanupCandidates, GetSubtitleSegments, GetPreviewSession, PreviewExternally } from '../../wailsjs/go/main/App';
import ScanDialog from './ScanDialog.vue';
import TagManagerDialog from './TagManagerDialog.vue';
import AddTagDialog from './AddTagDialog.vue';
import DeleteConfirmDialog from './DeleteConfirmDialog.vue';
import TagDeleteDialog from './TagDeleteDialog.vue';
import PreviewDrawer from './PreviewDrawer.vue';
import VirtualVideoList from './VirtualVideoList.vue';
import VideoListRow from './VideoListRow.vue';
import { logFrontend } from '../utils/frontendLog.js';
import { defaultRangeEngine, estimateVideoRowHeight } from '../utils/virtualList.js';

export default {
  name: 'VideoListPage',
  components: { ScanDialog, TagManagerDialog, AddTagDialog, DeleteConfirmDialog, TagDeleteDialog, PreviewDrawer, VirtualVideoList, VideoListRow },
  props: {
    tags: { type: Array, default: () => [] },
    settings: { type: Object, required: true },
    directories: { type: Array, default: () => [] }
  },
  emits: ['reload-tags', 'update-settings', 'reload-directories'],
  data() {
    return {
      videos: [],
      searchKeyword: '',
      searchMode: 'file',
      selectedTags: [],
      selectedSizeRange: 'all',
      selectedResRange: 'all',
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
      pageSize: 20,
      loading: false,
      hasMore: true,
      contextMenu: { show: false, x: 0, y: 0, video: null },
      showScanDialog: false,
      showTagManagerDialog: false,
      addTagDialog: { show: false, video: null },
      deleteDialog: { show: false, video: null },
      deletingIds: [],
      tagDeleteDialog: { show: false, tag: null },
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
      selectedPreviewVideoId: null,
      previewVideoSnapshot: null,
      previewOpen: false,
      previewSession: null,
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
    };
  },
  mounted() {
    this.configureHomeListVirtualization();
    this.loadVideos();
    document.addEventListener('click', this.hideContextMenu);
    
    if (window.runtime?.EventsOn) {
      this.registerRuntimeEvent('subtitle-progress', (data) => {
        const nextAction = data?.action || '';
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
        this.resetSubtitleProgressTracking();
        const idx = this.generatingSubtitleIds.indexOf(data.videoID);
        if (idx !== -1) this.generatingSubtitleIds.splice(idx, 1);
        this.subtitleDialog.show = true;
        this.subtitleDialog.mode = 'result';
        this.subtitleDialog.title = '✅ 字幕生成成功';
        this.subtitleDialog.msg = '文件: ' + data.path;
      });

      this.registerRuntimeEvent('subtitle-cancelled', (data) => {
        this.resetSubtitleProgressTracking();
        const idx = this.generatingSubtitleIds.indexOf(data.videoID);
        if (idx !== -1) this.generatingSubtitleIds.splice(idx, 1);
        this.subtitleDialog.show = true;
        this.subtitleDialog.mode = 'result';
        this.subtitleDialog.title = '⏹️ 已取消字幕生成';
        this.subtitleDialog.msg = data.message || '当前字幕任务已取消。';
      });

      this.registerRuntimeEvent('cleanup-progress', (data) => {
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
      });
    }
  },
  beforeUnmount() {
    document.removeEventListener('click', this.hideContextMenu);
    if (this.searchDebounceTimer) {
      clearTimeout(this.searchDebounceTimer);
    }
    this.teardownRuntimeEvents();
    this.resetCleanupProgressTracking();
    this.resetSubtitleProgressTracking();
  },
  computed: {
    selectedPreviewVideo() {
      if (!this.selectedPreviewVideoId) return null;
      return this.videos.find(video => video.id === this.selectedPreviewVideoId) || this.previewVideoSnapshot;
    },
    virtualListQueryKey() {
      return JSON.stringify({
        mode: this.searchMode,
        keyword: this.currentQueryKeyword(),
        tags: [...this.selectedTags].sort((a, b) => a - b),
        size: this.selectedSizeRange === 'all' ? 'all' : `${this.selectedSizeRange.min}:${this.selectedSizeRange.max}`,
        res: this.selectedResRange === 'all' ? 'all' : `${this.selectedResRange.min}:${this.selectedResRange.max}`
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
      this.subtitleDialog.msg = forceMode ? '字幕文件已保存到视频同目录下（已跳过质量检测）。' : `字幕文件已保存到视频同目录下。\n${result.path || ''}`;
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
      this.selectedPreviewVideoId = null;
      this.previewVideoSnapshot = null;
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
      this.cleanupSelection = [];
      this.resetCleanupProgressTracking();
      this.cleanupDialog = {
        show: true,
        loading: true,
        processing: false,
        analysis: null,
        error: '',
        progress: { stage: 'load', message: '正在准备清理候选分析…', current: 0, total: 0, path: '' }
      };
      this.startCleanupProgressTracking();
      try {
        const analysis = await GetCleanupCandidates(5, 480, 320);
        this.cleanupDialog.analysis = analysis;
      } catch (err) {
        console.error('获取清理候选失败:', err);
        this.cleanupDialog.error = '获取清理候选失败: ' + err;
      } finally {
        this.resetCleanupProgressTracking();
        this.cleanupDialog.loading = false;
      }
    },
    getAllCleanupCandidates() {
      const analysis = this.cleanupDialog.analysis || {};
      const byID = new Map();
      for (const group of analysis.duplicate_groups || []) {
        for (const candidate of group.candidates || []) {
          byID.set(candidate.id, candidate);
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
      if (this.isCleanupSelected(videoID)) {
        this.cleanupSelection = this.cleanupSelection.filter(id => id !== videoID);
        return;
      }
      this.cleanupSelection = [...this.cleanupSelection, videoID];
    },
    selectAllCleanupCandidates() {
      this.cleanupSelection = this.getAllCleanupCandidates().map(video => video.id);
    },
    clearCleanupSelection() {
      this.cleanupSelection = [];
    },
    async trashSelectedCleanupCandidates() {
      const selectedVideos = this.getAllCleanupCandidates().filter(video => this.cleanupSelection.includes(video.id));
      if (selectedVideos.length === 0) {
        return;
      }
      const selectedIDs = selectedVideos.map(video => video.id);

      this.cleanupDialog.processing = true;
      try {
        for (const video of selectedVideos) {
          if (!this.deletingIds.includes(video.id)) {
            this.deletingIds.push(video.id);
          }
          await DeleteVideo(video.id, true);
          this.videos = this.videos.filter(item => item.id !== video.id);
        }
        this.cleanupSelection = [];
        await this.reloadCurrentView();
        await this.openCleanupDialog();
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
          this.subtitleDialog.mode = 'result';
          this.subtitleDialog.title = '❌ 强制生成失败';
          this.subtitleDialog.msg = String(err);
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
        this.subtitleDialog.show = true;
        this.subtitleDialog.mode = 'result';
        this.subtitleDialog.title = '❌ 生成字幕失败';
        this.subtitleDialog.msg = String(err);
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
    async cancelSubtitle() {
      try {
        await CancelSubtitle();
        this.resetSubtitleProgressTracking();
        this.subtitleDialog.show = false;
        // 清理生成中状态
        this.generatingSubtitleIds = [];
      } catch (err) {
        console.error('取消失败:', err);
      }
    },
    hideContextMenu() {
      this.contextMenu.show = false;
    },
    calculateScore(video) {
      const weight = this.settings.play_weight || 2.0;
      return video.play_count * weight + video.random_play_count;
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
        subtitleBucket: this.subtitleLengthBucket(video?._subtitleMatchText)
      });
    },
    estimateVideoHeight(video, widthBucket, subtitleMode) {
      return estimateVideoRowHeight(video, widthBucket, subtitleMode);
    },
    hasStructuredFilters() {
      return this.selectedTags.length > 0 || this.selectedSizeRange !== 'all' || this.selectedResRange !== 'all';
    },
    async loadVideos() {
      if (this.loading || !this.hasMore) return;
      this.loading = true;
      try {
        const keyword = this.currentQueryKeyword();
        let newVideos = [];
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

        if (this.isSubtitleSearchActive(keyword)) {
          const matches = await SearchSubtitleMatches(keyword, 200);
          const deduped = new Map();
          for (const match of matches || []) {
            const video = match.video;
            if (!video || deduped.has(video.id)) continue;
            video._subtitleMatchText = match.segment?.text || '';
            deduped.set(video.id, video);
          }
          newVideos = this.applyClientFilters(Array.from(deduped.values()));
          this.videos = newVideos;
          this.hasMore = false;
          this.debugLog('loadVideos subtitle mode resolved', {
            count: newVideos.length,
            sample: newVideos.slice(0, 3).map(video => ({ id: video.id, name: video.name }))
          });
          return;
        }

        if (keyword || this.hasStructuredFilters()) {
          const { minSize, maxSize, minHeight, maxHeight } = this.currentFilterBounds();
          newVideos = await SearchVideosWithFilters(
            keyword,
            this.selectedTags,
            minSize,
            maxSize,
            minHeight,
            maxHeight,
            this.cursorScore,
            this.cursorSize,
            this.cursorID,
            this.pageSize
          );
        } else {
          newVideos = await GetVideosPaginated(this.cursorScore, this.cursorSize, this.cursorID, this.pageSize);
        }

        this.debugLog('loadVideos query resolved', {
          count: newVideos.length,
          sample: newVideos.slice(0, 3).map(video => ({ id: video.id, name: video.name, path: video.path })),
          mode: keyword || this.hasStructuredFilters() ? 'filtered' : 'paginated'
        });

        if (newVideos.length < this.pageSize) {
          this.hasMore = false;
        }
        if (newVideos.length > 0) {
          this.videos.push(...newVideos);
          const last = newVideos[newVideos.length - 1];
          this.cursorScore = this.calculateScore(last);
          this.cursorSize = last.size;
          this.cursorID = last.id;
        }
        this.debugLog('loadVideos applied to state', {
          totalVideos: this.videos.length,
          hasMore: this.hasMore
        });
      } catch (err) {
        this.debugLog('loadVideos failed', { err: String(err) }, true);
        console.error('加载视频失败:', err);
        alert('加载视频失败: ' + err);
      } finally {
        this.loading = false;
        this.debugLog('loadVideos finished', {
          totalVideos: this.videos.length,
          hasMore: this.hasMore,
          loading: this.loading
        });
      }
    },
    resetAndLoadVideos() {
      this.videos = [];
      this.cursorScore = 0;
      this.cursorSize = 0;
      this.cursorID = 0;
      this.hasMore = true;
      this.loadVideos();
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
    async reloadCurrentView() {
      this.resetAndLoadVideos();
    },
    applyClientFilters(videos) {
      return (videos || []).filter(video => {
        const tagMatched = this.selectedTags.length === 0 ||
          this.selectedTags.every(id => (video.tags || []).some(tag => tag.id === id));

        const sizeMatched = this.selectedSizeRange === 'all' ||
          (video.size >= this.selectedSizeRange.min && (this.selectedSizeRange.max === 0 || video.size < this.selectedSizeRange.max));

        const resMatched = this.selectedResRange === 'all' ||
          (video.height >= this.selectedResRange.min && (this.selectedResRange.max === 0 || video.height <= this.selectedResRange.max));

        return tagMatched && sizeMatched && resMatched;
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
      this.reloadCurrentView();
    },
    clearTagFilter() {
      this.selectedTags = [];
      this.reloadCurrentView();
    },
    canVideoMatchCurrentView(video) {
      const keyword = this.currentQueryKeyword().toLowerCase();
      if (this.isSubtitleSearchActive(keyword)) {
        return false;
      }
      const nameOrPathMatched = !keyword || `${video.name} ${video.path}`.toLowerCase().includes(keyword);
      if (!nameOrPathMatched) return false;
      return this.applyClientFilters([video]).length > 0;
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
    async handleSearch(immediate = false) {
      if (this.searchDebounceTimer) {
        clearTimeout(this.searchDebounceTimer);
        this.searchDebounceTimer = null;
      }

      if (immediate) {
        await this.reloadCurrentView();
        return;
      }

      this.searchDebounceTimer = setTimeout(() => {
        this.searchDebounceTimer = null;
        this.reloadCurrentView();
      }, 250);
    },
    async playRandom() {
      try {
        const result = await PlayRandomVideo();
        if (result.dispatch_succeeded && result.video) {
          alert(`正在随机播放: ${result.video.name}\n播放次数: ${result.video.play_count}\n随机播放次数: ${result.video.random_play_count}`);
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
      this.deleteDialog = { show: true, video: video };
    },
    async executeDelete({ video, deleteFile, dontAskAgain }) {
      if (dontAskAgain) {
        await UpdateSettings({
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
      await this.deleteVideo(video, deleteFile);
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
        await this.reloadCurrentView();
      } catch (err) {
        console.error('删除失败:', err);
        alert('删除失败: ' + err);
      } finally {
        this.deletingIds = this.deletingIds.filter(id => id !== video.id);
      }
    },
    showContextMenu(event, video) {
      this.contextMenu = { show: true, x: event.clientX, y: event.clientY, video: video };
    },
    openAddTagDialog(video) {
      this.addTagDialog = { show: true, video: video };
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
