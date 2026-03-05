<template>
  <div id="app" :data-theme="settings.theme === 'system' ? systemTheme : settings.theme">
    <div class="header">
      <div class="header-left">
        <h1>析微影策</h1>
      </div>
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

    <div class="main-view">
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
      systemTheme: 'light',
      settings: {
        confirm_before_delete: true,
        delete_original_file: false,
        video_extensions: '',
        play_weight: 2.0,
        auto_scan_on_startup: false,
        theme: 'system',
        log_enabled: false
      }
    };
  },
  async mounted() {
    await this.loadSettings();
    await this.loadDirectories();
    this.loadTags();
    
    const mediaQuery = window.matchMedia('(prefers-color-scheme: dark)');
    this.systemTheme = mediaQuery.matches ? 'dark' : 'light';
    mediaQuery.addEventListener('change', e => {
      this.systemTheme = e.matches ? 'dark' : 'light';
    });

    if (this.settings.auto_scan_on_startup && this.directories.length > 0) {
      setTimeout(() => this.incrementalScanAll(), 0);
    }
  },
  methods: {
    async loadSettings() {
      try { this.settings = await GetSettings(); } catch (err) {}
    },
    async loadTags() {
      try { this.tags = await GetAllTags(); } catch (err) {}
    },
    async loadDirectories() {
      try { this.directories = await GetAllDirectories(); } catch (err) {}
    },
    handleSettingsUpdate(newSettings) {
      this.settings = { ...this.settings, ...newSettings };
    },
    handleDirectoriesChanged(newDirectories) {
      this.directories = newDirectories;
    },
    async incrementalScanAll() {
      const { ScanDirectoryWithInfo, AddVideo, DeleteVideo, RelocateVideo, GetVideosByDirectory, RefreshVideoMetadata } = await import('../wailsjs/go/main/App');
      for (const dir of this.directories) {
        try {
          const scannedFiles = await ScanDirectoryWithInfo(dir.path);
          const existingVideos = await GetVideosByDirectory(dir.path);
          const scannedMap = new Map();
          for (const f of scannedFiles) scannedMap.set(f.path, f);
          for (const video of existingVideos) {
            if (scannedMap.has(video.path) && (video.duration === 0 || !video.resolution || !video.height)) {
              RefreshVideoMetadata(video.id).catch(() => {});
            }
          }
        } catch (err) {}
      }
    }
  }
};
</script>

<style>
:root {
  --bg-color: #f8fafc;
  --panel-bg: #ffffff;
  --header-bg: #ffffff;
  --text-primary: #1e293b;
  --text-secondary: #64748b;
  --text-muted: #94a3b8;
  --border-color: #e2e8f0;
  --accent-color: #0d9488;
  --accent-hover: #0f766e;
  --danger-color: #ef4444;
  --danger-hover: #dc2626;
  --h-unit: 36px;
  --radius: 8px;
  --transition: all 0.2s ease;
}

[data-theme='dark'] {
  --bg-color: #0f172a;
  --panel-bg: #1e293b;
  --header-bg: #0f172a;
  --text-primary: #f1f5f9;
  --text-secondary: #cbd5e1;
  --text-muted: #94a3b8;
  --border-color: #334155;
  --accent-color: #2dd4bf;
  --accent-hover: #5eead4;
}

* { box-sizing: border-box; margin: 0; padding: 0; }

html, body {
  height: 100%;
  background-color: var(--bg-color) !important;
  color: var(--text-primary);
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
  -webkit-font-smoothing: antialiased;
  overflow: hidden;
}

#app { height: 100vh; display: flex; flex-direction: column; }

/* --- Header --- */
.header {
  background: var(--header-bg); height: 60px; padding: 0 24px;
  display: flex; align-items: center; justify-content: space-between;
  box-shadow: 0 1px 0 var(--border-color); z-index: 100;
  --wails-draggable: drag;
}
.header-left { padding-left: 72px; pointer-events: none; }
.header h1 { font-size: 18px; font-weight: 700; color: var(--accent-color); }
.header-actions { display: flex; gap: 4px; height: 100%; margin-left: auto; --wails-draggable: none; }

.nav-btn {
  padding: 0 20px; background: transparent; border: none; border-bottom: 2px solid transparent;
  color: var(--text-secondary); font-size: 14px; font-weight: 500; cursor: pointer; transition: var(--transition);
}
.nav-btn.active { color: var(--accent-color); border-bottom-color: var(--accent-color); }
.nav-btn:hover:not(.active) { color: var(--text-primary); background: rgba(0,0,0,0.02); }

.main-view { flex: 1; overflow-y: auto; display: flex; flex-direction: column; }

/* --- Basic Controls --- */
.search-input, .text-input, .select-input, .number-input {
  height: var(--h-unit); padding: 0 12px; border: 1px solid var(--border-color);
  border-radius: var(--radius); background: var(--panel-bg); color: var(--text-primary);
  font-size: 14px; outline: none; box-sizing: border-box; width: 100%;
}
.search-input:focus, .text-input:focus, .select-input:focus { border-color: var(--accent-color); }

.btn-primary, .btn-random, .btn-secondary, .btn-danger, .btn-action {
  height: var(--h-unit); padding: 0 16px; border-radius: var(--radius); font-size: 14px;
  font-weight: 500; cursor: pointer; border: none; display: inline-flex; align-items: center;
  justify-content: center; gap: 6px; white-space: nowrap; transition: var(--transition);
}
.btn-primary { background: var(--accent-color); color: #0f172a; }
.btn-secondary { background: transparent; border: 1px solid var(--border-color); color: var(--text-primary); }
.btn-random { background: #f59e0b; color: white; }

/* --- Video Card & List --- */
.page-content { padding: 24px; max-width: 1440px; margin: 0 auto; width: 100%; }
.toolbar { display: flex; gap: 12px; align-items: center; margin-bottom: 24px; flex-wrap: nowrap; }
.toolbar .search-group { flex: 1; }
.toolbar .filter-group, .toolbar .action-group { display: flex; gap: 8px; flex-shrink: 0; }

.video-list { display: flex; flex-direction: column; gap: 12px; }
.video-item {
  background: var(--panel-bg); padding: 16px 20px; border-radius: 10px; border: 1px solid var(--border-color);
  display: flex; align-items: center; transition: var(--transition);
}
.video-item:hover { border-color: var(--accent-color); transform: translateY(-1px); box-shadow: 0 4px 12px rgba(0,0,0,0.05); }
.video-info { flex: 1; min-width: 0; }
.video-info h3 { font-size: 15px; font-weight: 600; color: var(--text-primary); margin-bottom: 4px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.video-path { font-size: 12px; color: var(--text-secondary); margin-bottom: 6px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.video-meta { font-size: 11px; color: var(--text-muted); display: flex; gap: 12px; }
.video-tags { display: flex; gap: 8px; flex-wrap: wrap; margin-top: 12px; }

/* --- Video Actions --- */
.video-actions { display: flex; gap: 8px; margin-left: 20px; }
.video-actions .btn-action, .video-actions .btn-danger {
  height: 32px; padding: 0 12px; font-size: 12px; background: transparent;
  border: 1px solid var(--accent-color); color: var(--accent-color);
}
.video-actions .btn-danger { border-color: var(--danger-color); color: var(--danger-color); }
.video-actions .btn-action:hover { background: var(--accent-color); color: white; }
.video-actions .btn-danger:hover { background: var(--danger-color); color: white; }

/* --- Tags --- */
.tags-filter { padding: 12px 0; border-bottom: 1px solid var(--border-color); margin-bottom: 24px; }
.tags-scroll-container { display: flex; gap: 8px; overflow-x: auto; padding-bottom: 4px; }
.tag-chip {
  height: 28px; padding: 0 12px; border-radius: 14px; font-size: 12px;
  background: var(--border-color); color: var(--text-primary);
  display: inline-flex; align-items: center; gap: 6px; border: 1px solid transparent; cursor: pointer;
}
.tag-chip.active { border-color: var(--text-primary); font-weight: 600; }

.tag-badge, .btn-add-tag { height: 24px; padding: 0 10px; border-radius: 6px; font-size: 11px; display: inline-flex; align-items: center; gap: 4px; font-weight: 600; }
.tag-badge { border: 1px solid rgba(0,0,0,0.1); color: var(--text-primary); }
.tag-remove { background: transparent; border: none; cursor: pointer; opacity: 0.5; font-size: 14px; color: inherit; }
.btn-add-tag { border: 1px dashed var(--text-muted); background: transparent; color: var(--text-secondary); cursor: pointer; }

/* --- Switch --- */
.switch { display: inline-flex; align-items: center; gap: 12px; cursor: pointer; height: var(--h-unit); }
.switch input { display: none; }
.slider { width: 36px; height: 20px; background-color: var(--border-color); border-radius: 20px; position: relative; transition: .3s; flex-shrink: 0; }
.slider:before { content: ""; height: 14px; width: 14px; left: 3px; bottom: 3px; background-color: white; border-radius: 50%; position: absolute; transition: .3s; }
input:checked + .slider { background-color: var(--accent-color); }
input:checked + .slider:before { transform: translateX(16px); }

/* --- Settings & Dialogs --- */
.settings-section { background: var(--panel-bg); padding: 24px; border-radius: 10px; border: 1px solid var(--border-color); margin-bottom: 24px; }
.settings-section h3 { font-size: 16px; font-weight: 600; color: var(--text-primary); border-bottom: 1px solid var(--border-color); padding-bottom: 12px; margin-bottom: 24px; }
.setting-item { margin-bottom: 24px; }
.setting-item label { display: block; margin-bottom: 10px; font-size: 14px; font-weight: 600; }
.setting-item label.switch { display: inline-flex; margin-bottom: 0; font-weight: 400; }
.help-text { font-size: 12px; color: var(--text-secondary); margin-top: 8px; line-height: 1.5; }

.modal-overlay { position: fixed; top: 0; left: 0; right: 0; bottom: 0; background: rgba(0,0,0,0.6); display: flex; align-items: center; justify-content: center; z-index: 1000; backdrop-filter: blur(4px); }
.modal { background: var(--panel-bg); padding: 32px; border-radius: 12px; width: 100%; max-width: 500px; border: 1px solid var(--border-color); box-shadow: 0 20px 25px -5px rgba(0,0,0,0.2); color: var(--text-primary); }
.modal h2 { font-size: 18px; margin-bottom: 20px; font-weight: 700; }
.modal-actions { display: flex; gap: 12px; justify-content: flex-end; margin-top: 24px; }

.tag-edit-row { display: flex; align-items: center; gap: 10px; padding: 10px 0; border-bottom: 1px solid var(--border-color); }
.divider { height: 1px; background: var(--border-color); margin: 24px 0; }
.empty-state { text-align: center; padding: 60px 24px; color: var(--text-muted); font-size: 14px; border: 2px dashed var(--border-color); border-radius: 10px; }
</style>
