<template>
  <div id="app">
    <div class="header">
      <h1>Video Master</h1>
      <div class="header-actions">
        <button 
          @click="currentPage = 'videos'" 
          :class="['nav-btn', { active: currentPage === 'videos' }]"
        >
          视频列表
        </button>
        <button 
          @click="currentPage = 'settings'" 
          :class="['nav-btn', { active: currentPage === 'settings' }]"
        >
          设置
        </button>
      </div>
    </div>

    <!-- 视频列表页面 -->
    <div v-if="currentPage === 'videos'" class="page-content">
      <div class="toolbar">
        <input 
          v-model="searchKeyword" 
          @input="handleSearch"
          type="text" 
          placeholder="搜索视频..." 
          class="search-input"
        />
        <button @click="playRandom" class="btn-random">随机播放</button>
        <button @click="openScanDialog" class="btn-primary">扫描目录</button>
        <button @click="openTagManager" class="btn-secondary">管理标签</button>
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
                v-for="tag in video.tags" 
                :key="tag.id"
                class="tag-badge"
                :style="{ backgroundColor: tag.color }"
              >
                {{ tag.name }}
                <button @click="removeTag(video, tag)" class="tag-remove">×</button>
              </span>
              <button @click="showAddTagDialog(video)" class="btn-add-tag">+ 标签</button>
            </div>
          </div>
          <div class="video-actions">
            <button @click="playVideo(video.id)" class="btn-action">播放</button>
            <button @click="openDirectory(video.id)" class="btn-action">打开目录</button>
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
    </div>

    <!-- 设置页面 -->
    <div v-if="currentPage === 'settings'" class="page-content settings-page">
      <h2>设置</h2>
      
      <!-- 基本设置 -->
      <div class="settings-section">
        <h3>基本设置</h3>
        <div class="setting-item">
          <label>
            <input type="checkbox" v-model="settingsForm.confirm_before_delete" />
            删除前确认
          </label>
        </div>
        <div class="setting-item">
          <label>
            <input type="checkbox" v-model="settingsForm.delete_original_file" />
            默认删除原始文件
          </label>
        </div>
        <div class="setting-item">
          <label>
            <input type="checkbox" v-model="settingsForm.auto_scan_on_startup" />
            启动时自动增量扫描配置的目录
          </label>
        </div>
        <div class="setting-item">
          <label>
            <input type="checkbox" v-model="settingsForm.log_enabled" />
            启用日志记录（写入 ~/.video-master/app.log）
          </label>
        </div>
      </div>

      <!-- 智能随机播放设置 -->
      <div class="settings-section">
        <h3>智能随机播放</h3>
        <div class="setting-item">
          <label>播放权重（1次普通播放 = N次随机播放）</label>
          <input 
            type="number" 
            v-model.number="settingsForm.play_weight" 
            min="0.1" 
            max="10" 
            step="0.1"
            class="number-input"
          />
          <p class="hint-text">
            当前设置: 1次普通播放 = {{ settingsForm.play_weight }}次随机播放<br/>
            <span class="help-text">
              权重越高，普通播放对随机选择的影响越大。<br/>
              建议值: 1.0-3.0，默认2.0
            </span>
          </p>
        </div>
      </div>

      <!-- 视频格式设置 -->
      <div class="settings-section">
        <h3>支持的视频格式</h3>
        <div class="setting-item">
          <label>格式列表（用逗号分隔，如: .mp4,.avi,.mkv,.rmvb,.qt）</label>
          <textarea 
            v-model="settingsForm.video_extensions" 
            rows="3"
            placeholder=".mp4,.avi,.mkv,.mov,.wmv,.flv,.webm,.m4v,.ts,.3gp,.mpg,.mpeg,.rm,.rmvb,.vob,.divx,.f4v,.asf,.qt"
          ></textarea>
          <p class="hint-text settings-current">当前支持: {{ settingsForm.video_extensions }}</p>
        </div>
      </div>

      <!-- 扫描目录管理 -->
      <div class="settings-section">
        <h3>扫描目录管理</h3>
        <div class="directories-list">
          <div v-for="dir in directories" :key="dir.id" class="directory-item">
            <div class="directory-info">
              <strong>{{ dir.alias || '未命名' }}</strong>
              <span class="directory-path">{{ dir.path }}</span>
            </div>
            <div class="directory-actions">
              <button @click="editDirectory(dir)" class="btn-small">编辑</button>
              <button @click="deleteDirectoryItem(dir.id)" class="btn-small btn-danger">删除</button>
            </div>
          </div>
          <div v-if="directories.length === 0" class="empty-hint">
            暂无扫描目录配置
          </div>
        </div>
        <button @click="showAddDirectoryDialog = true" class="btn-primary mt-10">添加扫描目录</button>
      </div>

      <!-- 保存按钮 -->
      <div class="settings-actions">
        <button @click="saveSettings" class="btn-primary">保存设置</button>
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

    <!-- Scan Dialog -->
    <div v-if="showScanDialog" class="modal-overlay" @click="showScanDialog = false">
      <div class="modal" @click.stop>
        <h2>扫描视频目录</h2>
        <div class="form-group">
          <button @click="selectDirectory" class="btn-primary">选择目录</button>
          <p v-if="scanDirectory" class="selected-dir">{{ scanDirectory }}</p>
        </div>
        <div v-if="scanProgress.scanning" class="scan-progress">
          <p>正在扫描... 已发现 {{ scanProgress.found }} 个视频</p>
          <p>正在处理 {{ scanProgress.processed }}/{{ scanProgress.total }}</p>
          <p>新增 {{ scanProgress.imported }} 个，删除 {{ scanProgress.deleted }} 个，跳过 {{ scanProgress.skipped }} 个</p>
        </div>
        <div class="modal-actions">
          <button @click="startScan" :disabled="!scanDirectory || scanProgress.scanning" class="btn-primary">
            开始扫描
          </button>
          <button @click="showScanDialog = false" class="btn-secondary">取消</button>
        </div>
      </div>
    </div>

    <!-- Tag Manager Dialog -->
    <div v-if="showTagManagerDialog" class="modal-overlay" @click="showTagManagerDialog = false">
      <div class="modal" @click.stop>
        <h2>标签管理</h2>
        <div class="form-group">
          <input v-model="newTag.name" type="text" placeholder="标签名称" />
          <input v-model="newTag.color" type="color" />
          <button @click="createTag" class="btn-primary" :disabled="createTagLoading">添加标签</button>
          <p v-if="tagCreateError" class="error-text">{{ tagCreateError }}</p>
        </div>
        <div class="tag-list">
          <div v-for="tag in tags" :key="tag.id" class="tag-item tag-edit-item">
            <span class="tag-color" :style="{ backgroundColor: tag.color }"></span>
            <input v-model="tag.name" type="text" class="tag-name-input" />
            <input v-model="tag.color" type="color" class="tag-color-input" />
            <button @click="saveTag(tag)" class="btn-small">保存</button>
            <button @click.stop="requestDeleteTag(tag)" class="btn-danger">删除</button>
          </div>
        </div>
        <button @click="showTagManagerDialog = false" class="btn-secondary">关闭</button>
      </div>
    </div>

    <!-- Add Tag to Video Dialog -->
    <div v-if="addTagDialog.show" class="modal-overlay" @click="addTagDialog.show = false">
      <div class="modal" @click.stop>
        <h2>为视频添加标签</h2>
        
        <!-- 手动输入新标签 -->
        <div class="form-group">
          <div class="input-with-button">
            <input 
              v-model="addTagDialog.newTagName" 
              type="text" 
              placeholder="输入新标签名称..." 
              @keyup.enter="addNewTagToVideo"
            />
            <input 
              v-model="addTagDialog.newTagColor" 
              type="color"
            />
            <button @click="addNewTagToVideo" class="btn-primary">创建并添加</button>
          </div>
          <p class="hint-text">输入新标签名称，选择颜色，点击创建并添加</p>
        </div>

        <div class="divider">或选择已有标签</div>

        <!-- 已有标签列表 -->
        <div class="tag-list">
          <div 
            v-for="tag in availableTags" 
            :key="tag.id" 
            class="tag-item clickable"
            @click="addExistingTagToVideo(tag)"
          >
            <span class="tag-color" :style="{ backgroundColor: tag.color }"></span>
            <span>{{ tag.name }}</span>
          </div>
          <div v-if="availableTags.length === 0" class="empty-hint">
            所有标签都已添加
          </div>
        </div>
        <button @click="addTagDialog.show = false" class="btn-secondary">关闭</button>
      </div>
    </div>

    <!-- Delete Confirmation -->
    <div v-if="deleteDialog.show" class="modal-overlay" @click="deleteDialog.show = false">
      <div class="modal" @click.stop>
        <h2>确认删除</h2>
        <p>确定要删除视频 "{{ deleteDialog.video?.name }}" 吗？</p>
        <div class="form-group">
          <label>
            <input type="checkbox" v-model="deleteDialog.deleteFile" />
            同时删除原始文件
          </label>
          <label v-if="settings.confirm_before_delete">
            <input type="checkbox" v-model="deleteDialog.dontAskAgain" />
            不再提示
          </label>
        </div>
        <div class="modal-actions">
          <button @click="executeDelete" class="btn-danger">确认删除</button>
          <button @click="deleteDialog.show = false" class="btn-secondary">取消</button>
        </div>
      </div>
    </div>

    <!-- Add/Edit Directory Dialog -->
    <div v-if="showAddDirectoryDialog || editingDirectory" class="modal-overlay" @click="closeDirectoryDialog">
      <div class="modal" @click.stop>
        <h2>{{ editingDirectory ? '编辑' : '添加' }}扫描目录</h2>
        <div class="form-group">
          <label>目录路径</label>
          <div class="input-with-button">
            <input type="text" v-model="directoryForm.path" placeholder="选择目录" readonly />
            <button @click="selectDirectoryForConfig" class="btn-secondary">选择</button>
          </div>
        </div>
        <div class="form-group">
          <label>目录别名</label>
          <input type="text" v-model="directoryForm.alias" placeholder="给这个目录起个名字" />
        </div>
        <div class="modal-actions">
          <button @click="saveDirectoryConfig" class="btn-primary">保存</button>
          <button @click="closeDirectoryDialog" class="btn-secondary">取消</button>
        </div>
      </div>
    </div>

    <!-- Tag Delete Confirmation -->
    <div v-if="tagDeleteDialog.show" class="modal-overlay" @click="tagDeleteDialog.show = false">
      <div class="modal" @click.stop>
        <h2>确认删除标签</h2>
        <p>确定要删除标签 "{{ tagDeleteDialog.tag?.name }}" 吗？</p>
        <p class="hint-text">该标签会从所有视频中移除。</p>
        <div class="modal-actions">
          <button @click="confirmDeleteTag" class="btn-danger">确认删除</button>
          <button @click="tagDeleteDialog.show = false" class="btn-secondary">取消</button>
        </div>
      </div>
    </div>
  </div>
</template>

<script>
import { GetVideosPaginated, SearchVideosWithFilters, SelectDirectory, ScanDirectory, AddVideo, DeleteVideo, OpenDirectory, PlayVideo, PlayRandomVideo, AddTagToVideo, RemoveTagFromVideo, GetAllTags, CreateTag, UpdateTag, DeleteTag, GetSettings, UpdateSettings, GetAllDirectories, AddDirectory, UpdateDirectory, DeleteDirectory, GetVideosByDirectory } from '../wailsjs/go/main/App';

export default {
  name: 'App',
  data() {
    return {
      currentPage: 'videos',
      videos: [],
      tags: [],
      directories: [],
      searchKeyword: '',
      selectedTags: [],
      // 分页相关
      cursorScore: 0,
      cursorSize: 0,
      cursorID: 0,
      pageSize: 20,
      loading: false,
      hasMore: true,
      contextMenu: {
        show: false,
        x: 0,
        y: 0,
        video: null
      },
      showScanDialog: false,
      scanDirectory: '',
      scanProgress: {
        scanning: false,
        found: 0,
        processed: 0,
        imported: 0,
        deleted: 0,
        skipped: 0,
        total: 0
      },
      showTagManagerDialog: false,
      newTag: {
        name: '',
        color: '#3b82f6'
      },
      createTagLoading: false,
      tagCreateError: '',
      addTagDialog: {
        show: false,
        video: null,
        newTagName: '',
        newTagColor: '#3b82f6'
      },
      deleteDialog: {
        show: false,
        video: null,
        deleteFile: false,
        dontAskAgain: false
      },
      deletingIds: [],
      tagDeleteDialog: {
        show: false,
        tag: null
      },
      settings: {
        confirm_before_delete: true,
        delete_original_file: false,
        video_extensions: '',
        play_weight: 2.0,
        auto_scan_on_startup: false,
        log_enabled: false
      },
      settingsForm: {
        confirm_before_delete: true,
        delete_original_file: false,
        video_extensions: '',
        play_weight: 2.0,
        auto_scan_on_startup: false,
        log_enabled: false
      },
      showAddDirectoryDialog: false,
      editingDirectory: null,
      directoryForm: {
        path: '',
        alias: ''
      }
    };
  },
  computed: {
    availableTags() {
      if (!this.addTagDialog.video) return this.tags;
      const videoTagIds = this.addTagDialog.video.tags.map(t => t.id);
      return this.tags.filter(t => !videoTagIds.includes(t.id));
    }
  },
  async mounted() {
    await this.loadSettings();
    await this.loadDirectories();
    this.loadVideos();
    this.loadTags();
    if (this.settings.auto_scan_on_startup && this.directories.length > 0) {
      setTimeout(() => this.incrementalScanAll(), 0);
    }
    document.addEventListener('click', () => {
      this.contextMenu.show = false;
    });
  },
  methods: {
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
        }
        return;
      }
      this.resetAndLoadVideos();
    },
    getDirectoryLabel(video) {
      const match = this.directories.find(d => d.path === video.directory);
      if (match && match.alias) {
        return `${match.alias}（${video.directory}）`;
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
      const id = Number(tagID);
      return this.selectedTags.includes(id);
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
    async deleteTagFromHeader(tag) {
      if (!confirm(`确定要删除标签 "${tag.name}" 吗？`)) return;
      try {
        await DeleteTag(tag.id);
        this.selectedTags = this.selectedTags.filter(id => id !== tag.id);
        await this.loadTags();
        await this.reloadCurrentView();
      } catch (err) {
        console.error('删除标签失败:', err);
        alert('删除标签失败: ' + err);
      }
    },
    async loadTags() {
      try {
        this.tags = await GetAllTags();
      } catch (err) {
        console.error('加载标签失败:', err);
      }
    },
    async loadSettings() {
      try {
        this.settings = await GetSettings();
        this.settingsForm = { ...this.settings };
        if (!this.settingsForm.video_extensions || this.settingsForm.video_extensions.trim() === '') {
          this.settingsForm.video_extensions = '.mp4,.avi,.mkv,.mov,.wmv,.flv,.webm,.m4v,.ts,.3gp,.mpg,.mpeg,.rm,.rmvb,.vob,.divx,.f4v,.asf,.qt';
        }
      } catch (err) {
        console.error('加载设置失败:', err);
      }
    },
    async loadDirectories() {
      try {
        this.directories = await GetAllDirectories();
      } catch (err) {
        console.error('加载目录失败:', err);
      }
    },
    handleScroll(event) {
      const { scrollTop, scrollHeight, clientHeight } = event.target;
      if (scrollTop + clientHeight >= scrollHeight - 100) {
      if (this.searchKeyword || this.selectedTags.length > 0) {
        return; // 搜索和筛选模式下不自动加载
      }
        this.loadVideos();
      }
    },
    async handleSearch() {
      await this.reloadCurrentView();
    },
    async filterByTag(tag) {
      this.toggleTagFilter(tag.id);
    },
    async selectDirectory() {
      try {
        this.scanDirectory = await SelectDirectory();
      } catch (err) {
        console.error('选择目录失败:', err);
      }
    },
    async startScan() {
      if (!this.scanDirectory) {
        alert('请先选择目录');
        return;
      }
      
      await this.incrementalScanDirectory(this.scanDirectory, true);

      // 自动加入目录配置（增量扫描用）
      const exists = this.directories.some(d => d.path === this.scanDirectory);
      if (!exists) {
        const alias = this.scanDirectory.split(/[/\\]/).filter(Boolean).pop() || this.scanDirectory;
        try {
          await AddDirectory(this.scanDirectory, alias);
          await this.loadDirectories();
        } catch (err) {
          console.warn('保存扫描目录失败:', err);
        }
      }
    },
    async incrementalScanDirectory(dir, showProgress) {
      if (!dir) return;
      if (showProgress) {
        this.scanProgress.scanning = true;
        this.scanProgress.found = 0;
        this.scanProgress.processed = 0;
        this.scanProgress.imported = 0;
        this.scanProgress.deleted = 0;
        this.scanProgress.skipped = 0;
        this.scanProgress.total = 0;
      }

      try {
        const files = await ScanDirectory(dir);
        const existingVideos = await GetVideosByDirectory(dir);
        const scannedSet = new Set(files);
        const keptByPath = new Map();
        const duplicateVideos = [];

        for (const video of existingVideos) {
          if (!keptByPath.has(video.path)) {
            keptByPath.set(video.path, video);
          } else {
            duplicateVideos.push(video);
          }
        }

        const duplicateIDSet = new Set(duplicateVideos.map(video => video.id));
        const staleVideos = existingVideos.filter(video => !duplicateIDSet.has(video.id) && !scannedSet.has(video.path));
        const toDelete = [...duplicateVideos, ...staleVideos];
        const toAdd = files.filter(file => !keptByPath.has(file));

        if (showProgress) {
          this.scanProgress.found = files.length;
          this.scanProgress.total = toAdd.length + toDelete.length;
        }

        for (const file of toAdd) {
          try {
            await AddVideo(file);
            if (showProgress) this.scanProgress.imported++;
          } catch (err) {
            if (showProgress) this.scanProgress.skipped++;
          } finally {
            if (showProgress) {
              this.scanProgress.processed++;
              await this.flushScanProgress();
            }
          }
        }

        for (const video of toDelete) {
          try {
            await DeleteVideo(video.id, false);
            if (showProgress) this.scanProgress.deleted++;
          } catch (err) {
            if (showProgress) this.scanProgress.skipped++;
          } finally {
            if (showProgress) {
              this.scanProgress.processed++;
              await this.flushScanProgress();
            }
          }
        }

        await this.reloadCurrentView();
        if (showProgress) this.showScanDialog = false;
      } catch (err) {
        console.error('扫描失败:', err);
        if (showProgress) alert('扫描失败: ' + err);
      } finally {
        if (showProgress) this.scanProgress.scanning = false;
      }
    },
    async flushScanProgress() {
      await this.$nextTick();
      await new Promise(resolve => setTimeout(resolve, 0));
    },
    async incrementalScanAll() {
      for (const dir of this.directories) {
        await this.incrementalScanDirectory(dir.path, false);
      }
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
      
      this.deleteDialog = {
        show: true,
        video: video,
        deleteFile: this.settings.delete_original_file,
        dontAskAgain: false
      };
    },
    async executeDelete() {
      const { video, deleteFile, dontAskAgain } = this.deleteDialog;
      
      if (dontAskAgain) {
        await UpdateSettings(
          false,
          deleteFile,
          this.settings.video_extensions || '',
          this.settings.play_weight || 2.0,
          this.settings.auto_scan_on_startup || false,
          this.settings.log_enabled || false
        );
        this.settings.confirm_before_delete = false;
        this.settings.delete_original_file = deleteFile;
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
      this.contextMenu = {
        show: true,
        x: event.clientX,
        y: event.clientY,
        video: video
      };
    },
    openScanDialog() {
      this.showScanDialog = true;
    },
    openTagManager() {
      this.showTagManagerDialog = true;
    },
    async createTag() {
      if (this.createTagLoading) return;
      const name = this.newTag.name.trim();
      if (!name) return;
      this.tagCreateError = '';
      if (!this.tags || this.tags.length === 0) {
        await this.loadTags();
      }
      if (this.tags.some(t => String(t.name).toLowerCase() === name.toLowerCase())) {
        this.tagCreateError = '标签已存在';
        return;
      }
      this.createTagLoading = true;
      
      try {
        await CreateTag(name, this.newTag.color);
        await this.loadTags();
        this.newTag.name = '';
        this.newTag.color = '#3b82f6';
      } catch (err) {
        if (this.isDuplicateError(err)) {
          this.tagCreateError = '标签已存在';
          return;
        }
        console.error('创建标签失败:', err);
        this.tagCreateError = '创建标签失败';
      } finally {
        this.createTagLoading = false;
      }
    },
    async deleteTag(id) {
      if (!confirm('确定要删除此标签吗？')) return;
      
      try {
        await DeleteTag(id);
        this.tags = this.tags.filter(t => t.id !== id);
        await this.reloadCurrentView();
        alert('标签已删除');
      } catch (err) {
        console.error('删除标签失败:', err);
        alert('删除标签失败: ' + err);
      }
    },
    requestDeleteTag(tag) {
      this.tagDeleteDialog = {
        show: true,
        tag
      };
    },
    async confirmDeleteTag() {
      const tag = this.tagDeleteDialog.tag;
      if (!tag) {
        this.tagDeleteDialog.show = false;
        return;
      }
      try {
        await DeleteTag(tag.id);
        this.tags = this.tags.filter(t => t.id !== tag.id);
        this.selectedTags = this.selectedTags.filter(id => id !== tag.id);
        await this.reloadCurrentView();
        this.tagDeleteDialog.show = false;
        alert('标签已删除');
      } catch (err) {
        console.error('删除标签失败:', err);
        alert('删除标签失败: ' + err);
      }
    },
    async saveTag(tag) {
      const name = (tag.name || '').trim();
      if (!name) {
        alert('标签名称不能为空');
        return;
      }
      
      try {
        await UpdateTag(tag.id, name, tag.color);
        await this.loadTags();
      } catch (err) {
        console.error('更新标签失败:', err);
        alert('更新标签失败: ' + err);
      }
    },
    showAddTagDialog(video) {
      this.addTagDialog = {
        show: true,
        video: video,
        newTagName: '',
        newTagColor: '#3b82f6'
      };
    },
    async addNewTagToVideo() {
      const tagName = this.addTagDialog.newTagName.trim();
      if (!tagName) {
        alert('请输入标签名称');
        return;
      }
      
      try {
        // 先创建标签
        let newTag = null;
        try {
          newTag = await CreateTag(tagName, this.addTagDialog.newTagColor);
          await this.loadTags();
        } catch (err) {
          if (this.isDuplicateError(err)) {
            await this.loadTags();
            newTag = this.tags.find(t => t.name === tagName) || null;
          } else {
            throw err;
          }
        }

        if (!newTag) {
          alert('标签已存在，但未找到对应记录');
          return;
        }

        // 再添加到视频
        await AddTagToVideo(this.addTagDialog.video.id, newTag.id);
        await this.reloadCurrentView();
        
        // 清空输入并关闭
        this.addTagDialog.newTagName = '';
        this.addTagDialog.newTagColor = '#3b82f6';
        this.addTagDialog.show = false;
      } catch (err) {
        console.error('添加标签失败:', err);
        alert('添加标签失败: ' + err);
      }
    },
    async addExistingTagToVideo(tag) {
      try {
        await AddTagToVideo(this.addTagDialog.video.id, tag.id);
        await this.reloadCurrentView();
        this.addTagDialog.show = false;
      } catch (err) {
        console.error('添加标签失败:', err);
        alert('添加标签失败: ' + err);
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
    isDuplicateError(err) {
      const raw = err && (err.message || err.error || err.toString ? err.toString() : err);
      const msg = String(raw || '').toLowerCase();
      return msg.includes('tag_exists') || msg.includes('unique') || msg.includes('duplicate') || msg.includes('constraint');
    },
    // 设置相关
    async saveSettings() {
      try {
        await UpdateSettings(
          this.settingsForm.confirm_before_delete,
          this.settingsForm.delete_original_file,
          this.settingsForm.video_extensions,
          this.settingsForm.play_weight,
          this.settingsForm.auto_scan_on_startup,
          this.settingsForm.log_enabled
        );
        this.settings = { ...this.settingsForm };
        alert('设置保存成功！智能随机播放已更新。');
      } catch (err) {
        console.error('保存设置失败:', err);
        alert('保存设置失败: ' + err);
      }
    },
    // 目录管理
    async selectDirectoryForConfig() {
      try {
        this.directoryForm.path = await SelectDirectory();
      } catch (err) {
        console.error('选择目录失败:', err);
      }
    },
    editDirectory(dir) {
      this.editingDirectory = dir;
      this.directoryForm = {
        path: dir.path,
        alias: dir.alias
      };
    },
    async saveDirectoryConfig() {
      if (!this.directoryForm.path) {
        alert('请选择目录');
        return;
      }
      
      try {
        if (this.editingDirectory) {
          await UpdateDirectory(this.editingDirectory.id, this.directoryForm.path, this.directoryForm.alias);
        } else {
          await AddDirectory(this.directoryForm.path, this.directoryForm.alias);
        }
        await this.loadDirectories();
        this.closeDirectoryDialog();
      } catch (err) {
        console.error('保存目录失败:', err);
        alert('保存目录失败: ' + err);
      }
    },
    async deleteDirectoryItem(id) {
      if (!confirm('确定要删除此目录配置吗？')) return;
      
      try {
        await DeleteDirectory(id);
        await this.loadDirectories();
      } catch (err) {
        console.error('删除目录失败:', err);
        alert('删除目录失败: ' + err);
      }
    },
    closeDirectoryDialog() {
      this.showAddDirectoryDialog = false;
      this.editingDirectory = null;
      this.directoryForm = {
        path: '',
        alias: ''
      };
    }
  }
};
</script>

<style>
* {
  box-sizing: border-box;
  margin: 0;
  padding: 0;
}

html, body {
  height: 100%;
  background: #f5f5f5;
}

#app {
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Oxygen, Ubuntu, Cantarell, sans-serif;
  background: #f5f5f5;
  min-height: 100vh;
  height: 100%;
  display: flex;
  flex-direction: column;
}

.header {
  background: white;
  padding: 20px;
  box-shadow: 0 2px 4px rgba(0,0,0,0.1);
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.header h1 {
  font-size: 24px;
  color: #333;
}

.header-actions {
  display: flex;
  gap: 10px;
  align-items: center;
}

.nav-btn {
  padding: 8px 16px;
  background: transparent;
  color: #6b7280;
  border: none;
  border-bottom: 2px solid transparent;
  cursor: pointer;
  font-size: 14px;
  transition: all 0.2s;
}

.nav-btn:hover {
  color: #111;
}

.nav-btn.active {
  color: #3b82f6;
  border-bottom-color: #3b82f6;
}

.page-content {
  flex: 1;
  min-height: calc(100vh - 80px);
  background: #f5f5f5;
  width: 100%;
}

.toolbar {
  padding: 15px 20px;
  background: white;
  border-bottom: 1px solid #e5e7eb;
  display: flex;
  gap: 10px;
  align-items: center;
}

.search-input {
  padding: 8px 16px;
  border: 1px solid #ddd;
  border-radius: 4px;
  font-size: 14px;
  width: 300px;
}

.btn-primary {
  padding: 8px 16px;
  background: #3b82f6;
  color: white;
  border: none;
  border-radius: 4px;
  cursor: pointer;
  font-size: 14px;
}

.btn-primary:hover {
  background: #2563eb;
}

.btn-primary:disabled {
  background: #93c5fd;
  cursor: not-allowed;
}

.btn-random {
  padding: 8px 16px;
  background: #f59e0b;
  color: white;
  border: none;
  border-radius: 4px;
  cursor: pointer;
  font-size: 14px;
}

.btn-random:hover {
  background: #d97706;
}

.btn-secondary {
  padding: 8px 16px;
  background: #6b7280;
  color: white;
  border: none;
  border-radius: 4px;
  cursor: pointer;
  font-size: 14px;
}

.btn-secondary:hover {
  background: #4b5563;
}

.btn-danger {
  padding: 8px 16px;
  background: #ef4444;
  color: white;
  border: none;
  border-radius: 4px;
  cursor: pointer;
  font-size: 14px;
}

.btn-danger:hover {
  background: #dc2626;
}

.btn-action {
  padding: 6px 12px;
  background: #10b981;
  color: white;
  border: none;
  border-radius: 4px;
  cursor: pointer;
  font-size: 13px;
}

.btn-action:hover {
  background: #059669;
}

.tags-filter {
  padding: 15px 20px;
  background: white;
  border-bottom: 1px solid #e5e7eb;
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}

.tag-chip {
  padding: 6px 12px;
  background: #e5e7eb;
  border: 2px solid transparent;
  border-radius: 16px;
  cursor: pointer;
  font-size: 13px;
  color: #333;
  transition: all 0.2s;
}

.tag-chip-wrap {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.tag-chip-name {
  color: #111;
}

.tag-chip-check {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  border-radius: 50%;
  background: rgba(255,255,255,0.85);
  color: #111;
  font-size: 11px;
  font-weight: 700;
}

.tag-chip-delete {
  border: none;
  background: rgba(255,255,255,0.4);
  color: #111;
  width: 16px;
  height: 16px;
  border-radius: 50%;
  cursor: pointer;
  font-size: 12px;
  line-height: 1;
  display: flex;
  align-items: center;
  justify-content: center;
}

.tag-chip-delete:hover {
  background: rgba(255,255,255,0.7);
}

.tag-chip:hover {
  border-color: #3b82f6;
}

.tag-chip.active {
  border-color: #111;
  box-shadow: 0 0 0 2px #111 inset, 0 0 0 1px rgba(0,0,0,0.15);
}

.video-list {
  padding: 20px;
  max-width: 1400px;
  margin: 0 auto;
  max-height: calc(100vh - 250px);
  overflow-y: auto;
}

.loading-indicator,
.no-more-indicator {
  text-align: center;
  padding: 20px;
  color: #6b7280;
  font-size: 14px;
}

.empty-state {
  text-align: center;
  padding: 60px 20px;
  color: #6b7280;
}

.video-item {
  background: white;
  padding: 20px;
  border-radius: 8px;
  margin-bottom: 15px;
  display: flex;
  justify-content: space-between;
  align-items: center;
  box-shadow: 0 1px 3px rgba(0,0,0,0.1);
  transition: box-shadow 0.2s;
  text-align: left;
}

.video-item:hover {
  box-shadow: 0 4px 6px rgba(0,0,0,0.1);
}

.video-info h3 {
  font-size: 16px;
  color: #111;
  margin-bottom: 8px;
  text-align: left;
}

.video-path {
  font-size: 13px;
  color: #6b7280;
  margin-bottom: 10px;
  text-align: left;
}

.video-size {
  font-size: 12px;
  color: #9ca3af;
  margin-bottom: 10px;
  text-align: left;
}

.video-tags {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
  align-items: center;
}

.tag-badge {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 4px 8px;
  border-radius: 12px;
  font-size: 12px;
  color: #111;
}

.tag-remove {
  background: rgba(255,255,255,0.3);
  border: none;
  color: white;
  cursor: pointer;
  border-radius: 50%;
  width: 16px;
  height: 16px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 14px;
  line-height: 1;
}

.tag-remove:hover {
  background: rgba(255,255,255,0.5);
}

.btn-add-tag {
  padding: 4px 8px;
  background: #e5e7eb;
  border: 1px dashed #9ca3af;
  border-radius: 12px;
  cursor: pointer;
  font-size: 12px;
  color: #6b7280;
}

.btn-add-tag:hover {
  background: #d1d5db;
}

.video-actions {
  display: flex;
  gap: 8px;
}

.context-menu {
  position: fixed;
  background: white;
  border: 1px solid #e5e7eb;
  border-radius: 6px;
  box-shadow: 0 4px 6px rgba(0,0,0,0.1);
  z-index: 1000;
  min-width: 150px;
}

.context-menu div {
  padding: 10px 16px;
  cursor: pointer;
  transition: background 0.2s;
}

.context-menu div:hover {
  background: #f3f4f6;
}

.context-menu div.danger {
  color: #ef4444;
}

.modal-overlay {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background: rgba(0,0,0,0.5);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 999;
}

.modal {
  background: white;
  padding: 30px;
  border-radius: 12px;
  min-width: 400px;
  max-width: 600px;
  max-height: 80vh;
  overflow-y: auto;
}

.modal h2 {
  margin-bottom: 20px;
  font-size: 20px;
  color: #111;
}

.form-group {
  margin-bottom: 20px;
}

.form-group input[type="text"] {
  width: 100%;
  padding: 8px 12px;
  border: 1px solid #ddd;
  border-radius: 4px;
  font-size: 14px;
  margin-bottom: 10px;
}

.form-group input[type="color"] {
  width: 60px;
  height: 36px;
  border: 1px solid #ddd;
  border-radius: 4px;
  cursor: pointer;
  margin-right: 10px;
}

.form-group label {
  display: block;
  margin-bottom: 10px;
  font-size: 14px;
}

.selected-dir {
  margin-top: 10px;
  padding: 8px;
  background: #f3f4f6;
  border-radius: 4px;
  font-size: 13px;
  word-break: break-all;
  color: #111;
}

.scan-progress {
  margin: 20px 0;
  padding: 15px;
  background: #f0f9ff;
  border-radius: 6px;
}

.scan-progress p {
  margin-bottom: 5px;
  font-size: 14px;
  color: #0369a1;
}

.modal-actions {
  display: flex;
  gap: 10px;
  justify-content: flex-end;
  margin-top: 20px;
}

.tag-list {
  max-height: 300px;
  overflow-y: auto;
}

.tag-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px;
  border-bottom: 1px solid #e5e7eb;
}

.tag-list .tag-item span {
  color: #111;
}

.tag-edit-item {
  gap: 8px;
}

.tag-name-input {
  flex: 1;
  min-width: 120px;
  padding: 6px 8px;
  border: 1px solid #d1d5db;
  border-radius: 4px;
  font-size: 13px;
}

.tag-color-input {
  width: 40px;
  height: 28px;
  border: 1px solid #d1d5db;
  border-radius: 4px;
  padding: 0;
}

.tag-item.clickable {
  cursor: pointer;
  transition: background 0.2s;
}

.tag-item.clickable:hover {
  background: #f3f4f6;
}

.tag-color {
  width: 24px;
  height: 24px;
  border-radius: 4px;
}

.tag-item span:nth-child(2) {
  flex: 1;
  font-size: 14px;
}

.input-with-button {
  display: flex;
  gap: 10px;
  align-items: center;
}

.input-with-button input[type="text"] {
  flex: 1;
  margin-bottom: 0;
}

.input-with-button input[type="color"] {
  margin-bottom: 0;
  margin-right: 0;
}

.hint-text {
  font-size: 12px;
  color: #6b7280;
  margin-top: 8px;
}

.settings-current {
  color: #111;
}

.error-text {
  font-size: 12px;
  color: #dc2626;
  margin-top: 8px;
}

.divider {
  text-align: center;
  position: relative;
  margin: 20px 0;
  color: #6b7280;
  font-size: 13px;
}

.divider::before,
.divider::after {
  content: '';
  position: absolute;
  top: 50%;
  width: 40%;
  height: 1px;
  background: #e5e7eb;
}

.divider::before {
  left: 0;
}

.divider::after {
  right: 0;
}

.empty-hint {
  text-align: center;
  padding: 20px;
  color: #9ca3af;
  font-size: 14px;
}

/* 设置页面样式 */
.settings-page {
  padding: 30px;
  max-width: 900px;
  margin: 0 auto;
  min-height: calc(100vh - 80px);
  background: #f5f5f5;
  width: 100%;
}

.settings-page h2 {
  font-size: 24px;
  margin-bottom: 30px;
  color: #111;
}

.settings-section {
  background: white;
  padding: 20px;
  border-radius: 8px;
  margin-bottom: 20px;
  box-shadow: 0 1px 3px rgba(0,0,0,0.1);
  text-align: left;
}

.settings-section h3 {
  font-size: 18px;
  margin-bottom: 15px;
  color: #333;
  border-bottom: 2px solid #f3f4f6;
  padding-bottom: 10px;
}

.setting-item {
  margin-bottom: 15px;
}

.setting-item label {
  display: block;
  font-size: 14px;
  color: #374151;
  margin-bottom: 8px;
  text-align: left;
}

.setting-item input[type="checkbox"] {
  margin-right: 8px;
}

.setting-item textarea {
  width: 100%;
  padding: 10px;
  border: 1px solid #ddd;
  border-radius: 4px;
  font-size: 14px;
  font-family: monospace;
  resize: vertical;
}

.number-input {
  width: 150px;
  padding: 8px 12px;
  border: 1px solid #ddd;
  border-radius: 4px;
  font-size: 14px;
}

.help-text {
  color: #9ca3af;
  font-size: 12px;
  line-height: 1.5;
}

.directories-list {
  max-height: 300px;
  overflow-y: auto;
}

.directory-item {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 12px;
  border: 1px solid #e5e7eb;
  border-radius: 6px;
  margin-bottom: 10px;
  background: #f9fafb;
}

.directory-info {
  flex: 1;
}

.directory-info strong {
  display: block;
  font-size: 15px;
  color: #111;
  margin-bottom: 4px;
}

.directory-path {
  font-size: 13px;
  color: #6b7280;
  font-family: monospace;
}

.directory-actions {
  display: flex;
  gap: 8px;
}

.btn-small {
  padding: 4px 12px;
  font-size: 13px;
  background: #e5e7eb;
  border: none;
  border-radius: 4px;
  cursor: pointer;
}

.btn-small:hover {
  background: #d1d5db;
}

.btn-small.btn-danger {
  background: #fee2e2;
  color: #dc2626;
}

.btn-small.btn-danger:hover {
  background: #fecaca;
}

.settings-actions {
  margin-top: 30px;
  text-align: right;
}

.mt-10 {
  margin-top: 10px;
}
</style>
