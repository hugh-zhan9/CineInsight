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

    <VideoListPage
      v-if="currentPage === 'videos'"
      :tags="tags"
      :settings="settings"
      :directories="directories"
      @reload-tags="loadTags"
      @reload-directories="loadDirectories"
      @update-settings="handleSettingsUpdate"
    />

    <SettingsPage
      v-if="currentPage === 'settings'"
      :settings="settings"
      :directories="directories"
      @settings-saved="handleSettingsUpdate"
      @directories-changed="handleDirectoriesChanged"
    />
  </div>
</template>

<script>
import { GetSettings, GetAllTags, GetAllDirectories } from '../wailsjs/go/main/App';
import VideoListPage from './components/VideoListPage.vue';
import SettingsPage from './components/SettingsPage.vue';

export default {
  name: 'App',
  components: { VideoListPage, SettingsPage },
  data() {
    return {
      currentPage: 'videos',
      tags: [],
      directories: [],
      settings: {
        confirm_before_delete: true,
        delete_original_file: false,
        video_extensions: '',
        play_weight: 2.0,
        auto_scan_on_startup: false,
        log_enabled: false
      }
    };
  },
  async mounted() {
    await this.loadSettings();
    await this.loadDirectories();
    this.loadTags();
    if (this.settings.auto_scan_on_startup && this.directories.length > 0) {
      setTimeout(() => this.incrementalScanAll(), 0);
    }
  },
  methods: {
    async loadSettings() {
      try {
        this.settings = await GetSettings();
      } catch (err) {
        console.error('加载设置失败:', err);
        alert('加载设置失败: ' + err);
      }
    },
    async loadTags() {
      try {
        this.tags = await GetAllTags();
      } catch (err) {
        console.error('加载标签失败:', err);
        alert('加载标签失败: ' + err);
      }
    },
    async loadDirectories() {
      try {
        this.directories = await GetAllDirectories();
      } catch (err) {
        console.error('加载目录失败:', err);
        alert('加载目录失败: ' + err);
      }
    },
    handleSettingsUpdate(newSettings) {
      this.settings = { ...this.settings, ...newSettings };
    },
    handleDirectoriesChanged(newDirectories) {
      this.directories = newDirectories;
    },
    async incrementalScanAll() {
      const { ScanDirectory, AddVideo, DeleteVideo, GetVideosByDirectory } = await import('../wailsjs/go/main/App');
      for (const dir of this.directories) {
        try {
          const files = await ScanDirectory(dir.path);
          const existingVideos = await GetVideosByDirectory(dir.path);
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

          for (const file of toAdd) {
            try { await AddVideo(file); } catch (err) { /* skip */ }
          }
          for (const video of toDelete) {
            try { await DeleteVideo(video.id, false); } catch (err) { /* skip */ }
          }
        } catch (err) {
          console.error('自动扫描失败:', err);
        }
      }
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
