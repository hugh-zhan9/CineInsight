<template>
  <div id="app" class="app-shell glass-app-shell">
    <div class="header glass-surface">
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

    <div v-if="startupError" class="startup-error-view">
      <div class="startup-error-card">
        <h2>应用已启动，但数据库连接失败</h2>
        <p class="startup-error-text">{{ startupError }}</p>
        <p class="startup-error-hint">
          这通常只是当前没有连上数据库，已有数据不会因此被删除。
        </p>
        <p class="startup-error-hint">
          如果你是双击启动应用，请优先检查 macOS 是否已允许“析微影策”访问本地网络，并确认 Postgres 地址和端口可达。
        </p>
      </div>
    </div>

    <div v-else class="main-view">
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
import { GetSettings, GetAllTags, GetAllDirectories, GetStartupError, SyncScanDirectories } from '../wailsjs/go/main/App';
import VideoListPage from './components/VideoListPage.vue';
import SettingsPage from './components/SettingsPage.vue';
import { logFrontend } from './utils/frontendLog.js';

export default {
  name: 'App',
  components: { VideoListPage, SettingsPage },
  data() {
    return {
      currentPage: 'videos',
      tags: [],
      directories: [],
      startupError: '',
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
    this.startupError = await GetStartupError();
    if (this.startupError) {
      this.applyTheme();
      return;
    }

    await this.loadSettings();
    await this.loadDirectories();
    this.loadTags();
    
    const mediaQuery = window.matchMedia('(prefers-color-scheme: dark)');
    this.systemTheme = mediaQuery.matches ? 'dark' : 'light';
    this.applyTheme();
    mediaQuery.addEventListener('change', e => {
      this.systemTheme = e.matches ? 'dark' : 'light';
      this.applyTheme();
    });

    if (this.settings.auto_scan_on_startup && this.directories.length > 0) {
      this.incrementalScanAll();
    }
  },
  watch: {
    'settings.theme'() {
      this.applyTheme();
    }
  },
  methods: {
    debugLog(message, payload = null, isError = false) {
      return logFrontend('App.vue', message, payload, isError);
    },
    applyTheme() {
      const theme = this.settings.theme === 'system' ? this.systemTheme : this.settings.theme;
      document.documentElement.setAttribute('data-theme', theme);
    },
    async loadSettings() {
      try {
        this.settings = await GetSettings();
        this.debugLog('loadSettings resolved', {
          id: this.settings?.id,
          theme: this.settings?.theme,
          log_enabled: this.settings?.log_enabled,
          auto_scan_on_startup: this.settings?.auto_scan_on_startup
        });
      } catch (err) {
        this.debugLog('loadSettings failed', { err: String(err) }, true);
      }
    },
    async loadTags() {
      try {
        this.tags = await GetAllTags();
        this.debugLog('loadTags resolved', {
          count: this.tags.length,
          sample: this.tags.slice(0, 5).map(tag => ({ id: tag.id, name: tag.name }))
        });
      } catch (err) {
        this.debugLog('loadTags failed', { err: String(err) }, true);
      }
    },
    async loadDirectories() {
      try {
        this.directories = await GetAllDirectories();
        this.debugLog('loadDirectories resolved', {
          count: this.directories.length,
          sample: this.directories.slice(0, 5).map(dir => ({ id: dir.id, alias: dir.alias, path: dir.path }))
        });
      } catch (err) {
        this.debugLog('loadDirectories failed', { err: String(err) }, true);
      }
    },
    handleSettingsUpdate(newSettings) {
      this.settings = { ...this.settings, ...newSettings };
    },
    handleDirectoriesChanged(newDirectories) {
      this.directories = newDirectories;
    },
    async incrementalScanAll() {
      try {
        const result = await SyncScanDirectories();
        this.debugLog('incrementalScanAll resolved', result);
        if ((result?.added || 0) > 0 || (result?.deleted || 0) > 0 || (result?.relocated || 0) > 0 || (result?.metadata_refreshed || 0) > 0) {
          await this.loadDirectories();
        }
      } catch (err) {
        this.debugLog('incrementalScanAll failed', { err: String(err) }, true);
      }
    }
  }
};
</script>

<style>
/* --- Header --- */
.header {
  height: 54px;
  margin: 8px 12px 0;
  padding: 0 14px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  border-radius: 16px;
  z-index: 100;
  --wails-draggable: drag;
}
.header-left { padding-left: 62px; pointer-events: none; }
.header h1 { font-size: 17px; font-weight: 740; color: var(--text-primary); }
.header-actions {
  display: flex;
  gap: 4px;
  height: 38px;
  align-items: center;
  margin-left: auto;
  padding: 4px;
  border: 1px solid var(--border-color);
  border-radius: 999px;
  background: rgba(255, 255, 255, 0.22);
  --wails-draggable: none;
}

.nav-btn {
  height: 28px;
  padding: 0 14px;
  background: transparent;
  border: none;
  border-radius: 999px;
  color: var(--text-secondary);
  font-size: 13px;
  font-weight: 650;
  cursor: pointer;
  transition: background var(--transition), color var(--transition), box-shadow var(--transition);
}
.nav-btn.active { color: var(--text-primary); background: var(--control-hover-bg); box-shadow: 0 1px 8px rgba(15, 23, 42, 0.08); }
.nav-btn:hover:not(.active) { color: var(--text-primary); background: var(--accent-soft); }

.main-view { flex: 1 1 auto; min-height: 0; overflow-y: auto; overscroll-behavior: contain; display: flex; flex-direction: column; }
.startup-error-view {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
}
.startup-error-card {
  max-width: 720px;
  width: 100%;
  background: var(--panel-bg);
  border: 1px solid var(--border-color);
  border-radius: 16px;
  padding: 28px 32px;
  box-shadow: 0 20px 25px -5px rgba(0,0,0,0.08);
}
.startup-error-card h2 {
  font-size: 22px;
  margin-bottom: 12px;
  color: var(--danger-color);
}
.startup-error-text {
  font-size: 14px;
  line-height: 1.7;
  color: var(--text-primary);
  word-break: break-word;
}
.startup-error-hint {
  margin-top: 14px;
  font-size: 13px;
  line-height: 1.6;
  color: var(--text-secondary);
}

.video-list { display: flex; flex-direction: column; gap: 12px; }
.video-item {
  background: var(--row-bg); padding: 12px 14px; border-radius: 10px; border: 1px solid var(--border-color);
  display: flex; align-items: center; transition: border-color var(--transition), background var(--transition), box-shadow var(--transition);
}
.video-item:hover { border-color: var(--accent-color); background: var(--row-hover-bg); box-shadow: 0 8px 22px rgba(15, 23, 42, 0.06); }
.video-item--selected { border-color: var(--accent-color); background: rgba(20, 184, 166, 0.08); }
.video-select { width: 28px; flex: 0 0 auto; display: inline-flex; align-items: center; justify-content: flex-start; }
.video-select input { width: 16px; height: 16px; accent-color: var(--accent-color); }
.video-info { flex: 1; min-width: 0; }
.video-info h3 { font-size: 14px; font-weight: 650; color: var(--text-primary); margin-bottom: 3px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.video-path { font-size: 12px; color: var(--text-secondary); margin-bottom: 6px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.video-meta { font-size: 11px; color: var(--text-muted); display: flex; gap: 12px; }
.video-tags { display: flex; gap: 6px; flex-wrap: wrap; margin-top: 9px; }

/* --- Video Actions --- */
.video-actions { display: grid; gap: 6px; margin-left: 16px; justify-items: end; }
.row-primary-actions,
.row-secondary-actions { display: flex; justify-content: flex-end; gap: 6px; }
.row-secondary-actions { flex-wrap: wrap; max-width: 360px; }
.video-actions .btn-action, .video-actions .btn-danger, .video-actions .btn-secondary {
  height: 28px; padding: 0 10px; font-size: 12px; background: transparent;
  border: 1px solid var(--accent-color); color: var(--accent-color);
}
.video-actions .btn-danger { border-color: var(--danger-color); color: var(--danger-color); }
.video-actions .btn-action:hover, .video-actions .btn-secondary:hover { background: var(--accent-color); color: white; }
.video-actions .btn-danger:hover { background: var(--danger-color); color: white; }

/* --- Tags --- */
.tags-filter { padding: 6px 0 10px; border-bottom: 1px solid var(--border-color); margin-bottom: 12px; }
.tags-scroll-container {
  display: flex;
  gap: 5px;
  flex-wrap: wrap;
  overflow: visible;
  padding-bottom: 0;
  align-items: flex-start;
}
/* --- Settings & Dialogs --- */
.settings-section { background: var(--panel-bg); padding: 24px; border-radius: 10px; border: 1px solid var(--border-color); margin-bottom: 24px; }
.settings-section h3 { font-size: 16px; font-weight: 600; color: var(--text-primary); border-bottom: 1px solid var(--border-color); padding-bottom: 12px; margin-bottom: 24px; }
.setting-item { margin-bottom: 24px; }
.setting-item label { display: block; margin-bottom: 10px; font-size: 14px; font-weight: 600; }
.setting-item label.switch { display: inline-flex; margin-bottom: 0; font-weight: 400; }
.setting-grid { display: grid; grid-template-columns: repeat(3, minmax(160px, 1fr)); gap: 16px; }
.setting-grid .setting-item { margin-bottom: 0; }

@media (max-width: 760px) {
  .setting-grid { grid-template-columns: 1fr; }
}

.tag-edit-row { display: flex; align-items: center; gap: 10px; padding: 10px 0; border-bottom: 1px solid var(--border-color); }
.divider { height: 1px; background: var(--border-color); margin: 24px 0; }
</style>
