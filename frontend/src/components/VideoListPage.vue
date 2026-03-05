<template>
  <div class="page-content">
    <div class="toolbar">
      <div class="search-group">
        <input 
          v-model="searchKeyword" 
          @input="handleSearch"
          type="text" 
          placeholder="搜索视频文件名或路径..." 
          class="search-input"
        />
      </div>
      
      <div class="filter-group">
        <select v-model="selectedSizeRange" @change="handleSearch" class="select-input" style="width: 130px;">
          <option value="all">📁 体积 (全部)</option>
          <option v-for="opt in sizeOptions" :key="opt.label" :value="opt.value">{{ opt.label }}</option>
        </select>
        
        <select v-model="selectedResRange" @change="handleSearch" class="select-input" style="width: 150px;">
          <option value="all">📺 分辨率 (全部)</option>
          <option v-for="opt in resOptions" :key="opt.label" :value="opt.value">{{ opt.label }}</option>
        </select>
      </div>

      <div class="action-group" style="margin-left: auto; display: flex; gap: 8px;">
        <button @click="playRandom" class="btn-random">🎲 随机播放</button>
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

    <div class="video-list" @scroll="handleScroll" ref="videoList">
      <div v-if="videos.length === 0" class="empty-state">
        <p>暂无视频，点击"扫描目录"开始导入视频</p>
      </div>
      <div 
        v-for="video in videos" 
        :key="video.id"
        class="video-item"
        @contextmenu.prevent="showContextMenu($event, video)"
      >
        <div class="video-info">
          <h3>{{ video.name }}</h3>
        <p class="video-path">{{ getDirectoryLabel(video) }}</p>
        <div class="video-meta">
          <span class="video-size">{{ formatSize(video.size) }}</span>
          <span v-if="video.duration" class="meta-divider">|</span>
          <span v-if="video.duration" class="video-duration">{{ formatDuration(video.duration) }}</span>
          <span v-if="video.resolution" class="meta-divider">|</span>
          <span v-if="video.resolution" class="video-resolution">{{ video.resolution }}</span>
        </div>
          <div class="video-tags">
            <span 
              v-for="tag in (video.tags || [])" 
              :key="tag.id"
              class="tag-badge"
              :style="{ backgroundColor: tagBgColor(tag.color) }"
            >
              {{ tag.name }}
              <button @click="removeTag(video, tag)" class="tag-remove">×</button>
            </span>
            <button @click="openAddTagDialog(video)" class="btn-add-tag">+ 标签</button>
          </div>
        </div>
        <div class="video-actions">
          <button @click="playVideo(video.id)" class="btn-action">播放</button>
          <button @click="openDirectory(video.id)" class="btn-action">打开目录</button>
          <button 
            @click="generateSubtitle(video)" 
            class="btn-action" 
            :class="{ 'btn-processing': generatingSubtitleIds.includes(video.id) }"
            :disabled="generatingSubtitleIds.includes(video.id)"
          >
            {{ generatingSubtitleIds.includes(video.id) ? '生成中...' : '字幕' }}
          </button>
          <button @click="renameVideo(video)" class="btn-action">重命名</button>
        <button @click="confirmDelete(video)" class="btn-danger" :disabled="deletingIds.includes(video.id)">删除</button>
        </div>
      </div>
      
      <!-- 加载更多指示器 -->
      <div v-if="loading" class="loading-indicator">
        <p>加载中...</p>
      </div>
      <div v-if="!hasMore && videos.length > 0" class="no-more-indicator">
        <p>没有更多视频了</p>
      </div>
    </div>

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
    
    <!-- 字幕操作弹窗（确认/进度/结果） -->
    <div v-if="subtitleDialog.show" class="modal-overlay">
      <div class="modal download-modal">
        <h3>{{ subtitleDialog.title }}</h3>
        <p>{{ subtitleDialog.msg }}</p>
        
        <!-- 语言选择 (确认生成时显示) -->
        <div v-if="subtitleDialog.mode === 'confirm'" class="lang-select-box" style="margin-top: 15px;">
          <label style="display: block; font-size: 13px; margin-bottom: 8px; color: #666;">识别源语言 (Whisper):</label>
          <select v-model="sourceLang" class="search-input" style="width: 100%; height: 36px; padding: 0 10px;">
            <option v-for="opt in languageOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
          </select>
          <p style="font-size: 11px; color: #999; margin-top: 5px;">如果自动检测不准，请手动指定视频中的语言。</p>
        </div>
        
        <!-- 下载进度条 -->
        <template v-if="subtitleDialog.mode === 'progress'">
          <div class="progress-bar-container">
            <div class="progress-bar" :style="{ width: subtitleDialog.percent + '%' }"></div>
          </div>
          <p class="progress-text">{{ subtitleDialog.percent }}%</p>
          <div class="modal-actions">
            <button @click="cancelSubtitle" class="btn-danger">取消生成</button>
          </div>
        </template>
        
        <!-- 确认按钮 -->
        <div v-if="subtitleDialog.mode === 'confirm'" class="modal-actions">
          <button @click="subtitleDialog.show = false; pendingForceVideo = null;" class="btn-secondary">取消</button>
          <button @click="onSubtitleConfirm" class="btn-primary">{{ pendingForceVideo ? '强制生成' : '确认下载' }}</button>
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
.btn-processing {
  opacity: 0.7;
  cursor: wait;
  background-color: #aaa !important;
}
</style>

<script>
import { GetVideosPaginated, SearchVideosWithFilters, PlayVideo, PlayRandomVideo, OpenDirectory, DeleteVideo, RemoveTagFromVideo, UpdateSettings, CheckSubtitleDependencies, DownloadSubtitleDependencies, GenerateSubtitle, ForceGenerateSubtitle, RenameVideo, CancelSubtitle } from '../../wailsjs/go/main/App';
import ScanDialog from './ScanDialog.vue';
import TagManagerDialog from './TagManagerDialog.vue';
import AddTagDialog from './AddTagDialog.vue';
import DeleteConfirmDialog from './DeleteConfirmDialog.vue';
import TagDeleteDialog from './TagDeleteDialog.vue';

export default {
  name: 'VideoListPage',
  components: { ScanDialog, TagManagerDialog, AddTagDialog, DeleteConfirmDialog, TagDeleteDialog },
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
      // Subtitle states
      generatingSubtitleIds: [],
      subtitleDialog: { show: false, mode: 'confirm', title: '', msg: '', percent: 0 },
      pendingSubtitleVideo: null,
      pendingForceVideo: null,
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
    this.loadVideos();
    document.addEventListener('click', this.hideContextMenu);
    
    // 使用 window.runtime 直接注册事件（避免 import 问题）
    if (window.runtime) {
      window.runtime.EventsOn('download-progress', (data) => {
        this.subtitleDialog.show = true;
        this.subtitleDialog.mode = 'progress';
        this.subtitleDialog.title = '正在下载组件';
        this.subtitleDialog.percent = data.percent;
        this.subtitleDialog.msg = data.msg;
      });
      
      window.runtime.EventsOn('subtitle-success', (data) => {
        const idx = this.generatingSubtitleIds.indexOf(data.videoID);
        if (idx !== -1) this.generatingSubtitleIds.splice(idx, 1);
        this.subtitleDialog.show = true;
        this.subtitleDialog.mode = 'result';
        this.subtitleDialog.title = '✅ 字幕生成成功';
        this.subtitleDialog.msg = '文件: ' + data.path;
      });
    }
  },
  beforeUnmount() {
    document.removeEventListener('click', this.hideContextMenu);
  },
  methods: {
    async generateSubtitle(video) {
      console.log('[Subtitle] generateSubtitle called for video:', video.id);
      if (this.generatingSubtitleIds.includes(video.id)) return;

      try {
        const status = await CheckSubtitleDependencies();
        console.log('[Subtitle] Dependencies status:', status);
        
        this.pendingSubtitleVideo = video;
        this.subtitleDialog.show = true;
        this.subtitleDialog.mode = 'confirm';
        
        if (!status.ffmpeg || !status.whisper || !status.model) {
          this.subtitleDialog.title = '需要下载组件';
          this.subtitleDialog.msg = '初次使用需下载字幕生成组件 (FFmpeg/Whisper/Model)，约 1.7GB。是否立即下载？';
          this.subtitleDialog.requiresDownload = true;
        } else {
          this.subtitleDialog.title = '准备生成字幕';
          this.subtitleDialog.msg = '我们将使用 AI 为您生成本地字幕，这可能需要几分钟。';
          this.subtitleDialog.requiresDownload = false;
        }
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
      if (this.pendingForceVideo) {
        const video = this.pendingForceVideo;
        this.pendingForceVideo = null;
        this.subtitleDialog.mode = 'progress';
        this.subtitleDialog.title = '正在强制生成字幕';
        this.subtitleDialog.percent = 0;
        this.subtitleDialog.msg = '跳过质量检测，重新生成...';
        this.generatingSubtitleIds.push(video.id);
        try {
          await ForceGenerateSubtitle(video.id, this.sourceLang);
          const idx = this.generatingSubtitleIds.indexOf(video.id);
          if (idx !== -1) this.generatingSubtitleIds.splice(idx, 1);
          this.subtitleDialog.mode = 'result';
          this.subtitleDialog.title = '✅ 字幕生成完成';
          this.subtitleDialog.msg = '字幕文件已保存到视频同目录下（已跳过质量检测）。';
        } catch (err) {
          const idx = this.generatingSubtitleIds.indexOf(video.id);
          if (idx !== -1) this.generatingSubtitleIds.splice(idx, 1);
          this.subtitleDialog.mode = 'result';
          this.subtitleDialog.title = '❌ 强制生成失败';
          this.subtitleDialog.msg = String(err);
        }
        return;
      }

      // 场景二：用户确认下载依赖
      if (this.subtitleDialog.requiresDownload) {
        this.subtitleDialog.mode = 'progress';
        this.subtitleDialog.title = '正在下载组件';
        this.subtitleDialog.percent = 0;
        this.subtitleDialog.msg = '准备下载...';
        try {
          await DownloadSubtitleDependencies();
          this.subtitleDialog.mode = 'result';
          this.subtitleDialog.title = '✅ 组件下载完成';
          this.subtitleDialog.msg = '现在可以点击字幕按钮生成字幕了。';
        } catch (err) {
          this.subtitleDialog.mode = 'result';
          this.subtitleDialog.title = '❌ 下载失败';
          this.subtitleDialog.msg = String(err);
        }
        this.pendingSubtitleVideo = null;
        return;
      }

      // 场景三：依赖已就绪，开始生成
      if (this.pendingSubtitleVideo) {
        const video = this.pendingSubtitleVideo;
        this.pendingSubtitleVideo = null;
        this.subtitleDialog.show = false; // 先关掉弹窗，按钮会显示“生成中...”
        await this.doGenerateSubtitle(video);
      }
    },
    async doGenerateSubtitle(video) {
      this.generatingSubtitleIds.push(video.id);
      try {
        await GenerateSubtitle(video.id, this.sourceLang);
        // 成功后移除 ID（event 也会移除，双重保障）
        const idx = this.generatingSubtitleIds.indexOf(video.id);
        if (idx !== -1) this.generatingSubtitleIds.splice(idx, 1);
        this.subtitleDialog.show = true;
        this.subtitleDialog.mode = 'result';
        this.subtitleDialog.title = '✅ 字幕生成完成';
        this.subtitleDialog.msg = '字幕文件已保存到视频同目录下。';
      } catch (err) {
        console.error('[Subtitle] Generate error:', err);
        const idx = this.generatingSubtitleIds.indexOf(video.id);
        if (idx !== -1) this.generatingSubtitleIds.splice(idx, 1);
        
        // 检测幻觉警告：弹窗询问是否强制生成
        const errMsg = String(err);
        if (errMsg.includes('HALLUCINATION_DETECTED')) {
          this.subtitleDialog.show = true;
          this.subtitleDialog.mode = 'confirm';
          this.subtitleDialog.title = '⚠️ 字幕质量警告';
          this.subtitleDialog.msg = errMsg.replace('HALLUCINATION_DETECTED: ', '') + '\n\n是否强制生成，保留当前结果？';
          this.pendingForceVideo = video;
          return;
        }

        this.subtitleDialog.show = true;
        this.subtitleDialog.mode = 'result';
        this.subtitleDialog.title = '❌ 生成字幕失败';
        this.subtitleDialog.msg = errMsg;
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
    async loadVideos() {
      if (this.loading || !this.hasMore) return;
      this.loading = true;
      try {
        const newVideos = await GetVideosPaginated(this.cursorScore, this.cursorSize, this.cursorID, this.pageSize);
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
      } catch (err) {
        console.error('加载视频失败:', err);
        alert('加载视频失败: ' + err);
      } finally {
        this.loading = false;
      }
    },
    tagBgColor(hex) {
      if (!hex || !hex.startsWith('#')) return hex;
      const r = parseInt(hex.slice(1,3), 16);
      const g = parseInt(hex.slice(3,5), 16);
      const b = parseInt(hex.slice(5,7), 16);
      return `rgba(${r},${g},${b},0.35)`;
    },
    resetAndLoadVideos() {
      this.videos = [];
      this.cursorScore = 0;
      this.cursorSize = 0;
      this.cursorID = 0;
      this.hasMore = true;
      this.loadVideos();
    },
    async reloadCurrentView() {
      const keyword = this.searchKeyword.trim();
      const hasSizeFilter = this.selectedSizeRange !== 'all';
      const hasResFilter = this.selectedResRange !== 'all';

      if (keyword || this.selectedTags.length > 0 || hasSizeFilter || hasResFilter) {
        try {
          let minSize = 0, maxSize = 0;
          if (hasSizeFilter) {
            minSize = this.selectedSizeRange.min;
            maxSize = this.selectedSizeRange.max;
          }

          let minHeight = 0, maxHeight = 0;
          if (hasResFilter) {
            minHeight = this.selectedResRange.min;
            maxHeight = this.selectedResRange.max;
          }

          this.videos = await SearchVideosWithFilters(
            keyword, 
            this.selectedTags, 
            minSize, 
            maxSize, 
            minHeight, 
            maxHeight, 
            0, 0, 0, 200
          );
          this.hasMore = false;
        } catch (err) {
          console.error('组合搜索失败:', err);
          alert('组合搜索失败: ' + err);
        }
        return;
      }
      this.resetAndLoadVideos();
    },
    getDirectoryLabel(video) {
      if (!this.directories || this.directories.length === 0) return video.directory;

      // 按路径长度降序排序，优先匹配最长（最深）的目录
      const sortedDirs = [...this.directories]
        .filter(d => d.alias)
        .sort((a, b) => b.path.length - a.path.length);

      for (const dir of sortedDirs) {
        // 1. 精确匹配
        if (dir.path === video.directory) {
          return dir.alias;
        }

        // 2. 子目录匹配 (确保路径分隔符正确，避免 /data 匹配 /database)
        // 检测系统分隔符（Windows用\, 其他用/）
        const isWindows = video.directory.includes('\\');
        const sep = isWindows ? '\\' : '/';
        const prefix = dir.path.endsWith(sep) ? dir.path : dir.path + sep;

        if (video.directory.startsWith(prefix)) {
          const suffix = video.directory.substring(prefix.length);
          return `${dir.alias}${sep}${suffix}`;
        }
      }
      return video.directory;
    },
    formatSize(bytes) {
      if (bytes === 0 || bytes === null || bytes === undefined) return '0 B';
      const k = 1024;
      const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
      const i = Math.floor(Math.log(bytes) / Math.log(k));
      const value = bytes / Math.pow(k, i);
      return `${value.toFixed(value >= 10 || i === 0 ? 0 : 1)} ${sizes[i]}`;
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
    handleScroll(event) {
      const { scrollTop, scrollHeight, clientHeight } = event.target;
      if (scrollTop + clientHeight >= scrollHeight - 100) {
        if (this.searchKeyword || this.selectedTags.length > 0) return;
        this.loadVideos();
      }
    },
    async handleSearch() {
      await this.reloadCurrentView();
    },
    async playRandom() {
      try {
        const video = await PlayRandomVideo();
        alert(`正在随机播放: ${video.name}\n播放次数: ${video.play_count}\n随机播放次数: ${video.random_play_count}`);
      } catch (err) {
        console.error('随机播放失败:', err);
        alert('随机播放失败: ' + err);
      }
    },
    async playVideo(id) {
      try {
        await PlayVideo(id);
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
