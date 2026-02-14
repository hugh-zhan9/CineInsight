<template>
  <div class="page-content">
    <div class="toolbar">
      <input 
        v-model="searchKeyword" 
        @input="handleSearch"
        type="text" 
        placeholder="搜索视频..." 
        class="search-input"
      />
      <button @click="playRandom" class="btn-random">随机播放</button>
      <button @click="showScanDialog = true" class="btn-primary">扫描目录</button>
      <button @click="showTagManagerDialog = true" class="btn-secondary">管理标签</button>
    </div>

    <div class="tags-filter">
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
        :style="{ backgroundColor: tag.color }"
        @click="toggleTagFilter(tag.id)"
      >
        <span class="tag-chip-name">{{ tag.name }}</span>
        <span v-if="isTagSelected(tag.id)" class="tag-chip-check">✓</span>
        <button type="button" class="tag-chip-delete" @click.stop="requestDeleteTag(tag)">×</button>
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
        <p class="video-size">{{ formatSize(video.size) }}</p>
          <div class="video-tags">
            <span 
              v-for="tag in (video.tags || [])" 
              :key="tag.id"
              class="tag-badge"
              :style="{ backgroundColor: tag.color }"
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
      <div @click="confirmDelete(contextMenu.video)" class="danger">删除</div>
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
        
        <!-- 下载进度条 -->
        <template v-if="subtitleDialog.mode === 'progress'">
          <div class="progress-bar-container">
            <div class="progress-bar" :style="{ width: subtitleDialog.percent + '%' }"></div>
          </div>
          <p class="progress-text">{{ subtitleDialog.percent }}%</p>
        </template>
        
        <!-- 确认按钮 -->
        <div v-if="subtitleDialog.mode === 'confirm'" class="modal-actions">
          <button @click="subtitleDialog.show = false" class="btn-secondary">取消</button>
          <button @click="onSubtitleConfirm" class="btn-primary">确认下载</button>
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
import { GetVideosPaginated, SearchVideosWithFilters, PlayVideo, PlayRandomVideo, OpenDirectory, DeleteVideo, RemoveTagFromVideo, UpdateSettings, CheckSubtitleDependencies, DownloadSubtitleDependencies, GenerateSubtitle } from '../../wailsjs/go/main/App';
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
        if (!status.ffmpeg || !status.whisper || !status.model) {
          // 显示确认弹窗（不用 confirm()，WKWebView 不支持）
          this.pendingSubtitleVideo = video;
          this.subtitleDialog.show = true;
          this.subtitleDialog.mode = 'confirm';
          this.subtitleDialog.title = '需要下载组件';
          this.subtitleDialog.msg = '初次使用需下载字幕生成组件 (FFmpeg/Whisper/Model)，约 200MB。是否立即下载？';
          return; // 用户确认后在 onSubtitleConfirm 中继续
        }
        
        // 依赖已就绪，直接生成
        await this.doGenerateSubtitle(video);
      } catch (err) {
        console.error('[Subtitle] Error:', err);
        this.subtitleDialog.show = true;
        this.subtitleDialog.mode = 'result';
        this.subtitleDialog.title = '❌ 检查依赖失败';
        this.subtitleDialog.msg = String(err);
      }
    },
    async onSubtitleConfirm() {
      // 用户确认下载依赖
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
    },
    async doGenerateSubtitle(video) {
      this.generatingSubtitleIds.push(video.id);
      try {
        await GenerateSubtitle(video.id);
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
        this.subtitleDialog.show = true;
        this.subtitleDialog.mode = 'result';
        this.subtitleDialog.title = '❌ 生成字幕失败';
        this.subtitleDialog.msg = String(err);
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
      if (keyword || this.selectedTags.length > 0) {
        try {
          this.videos = await SearchVideosWithFilters(keyword, this.selectedTags, 0, 0, 0, 200);
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
