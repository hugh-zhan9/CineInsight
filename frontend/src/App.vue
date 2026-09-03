<template>
  <div id="app" class="app-shell glass-app-shell">
    <div class="header glass-surface">
      <div class="header-left">
        <h1>析微影策</h1>
      </div>
      <div class="header-nav">
        <button 
          @click="currentPage = 'videos'" 
          :class="['nav-btn', { active: currentPage === 'videos' }]"
        >
          视频
        </button>
        <button @click="currentPage = 'people'" :class="['nav-btn', { active: currentPage === 'people' }]">
          人物
        </button>
        <button @click="currentPage = 'collections'" :class="['nav-btn', { active: currentPage === 'collections' }]">
          作品集
        </button>
		<button @click="currentPage = 'insights'" :class="['nav-btn', { active: currentPage === 'insights' }]">
		  洞察
		</button>
		<button @click="currentPage = 'photos'" :class="['nav-btn', { active: currentPage === 'photos' }]">
		  图片
		</button>
        <button 
          @click="currentPage = 'settings'" 
          :class="['nav-btn', { active: currentPage === 'settings' }]"
        >
          设置
        </button>
      </div>
      <div v-if="libraryCountsText" class="header-counts">{{ libraryCountsText }}</div>
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
        ref="videoListPage"
        v-show="currentPage === 'videos'"
        :page-active="currentPage === 'videos'"
        :tags="tags"
        :settings="settings"
        :directories="directories"
        @reload-tags="loadTags"
        @reload-directories="loadDirectories"
        @update-settings="handleSettingsUpdate"
      />

      <SettingsPage
        ref="settingsPage"
        v-if="currentPage === 'settings'"
        :settings="settings"
        :directories="directories"
        @settings-saved="handleSettingsUpdate"
        @directories-changed="handleDirectoriesChanged"
        @tags-changed="loadTags"
      />

      <EntityLibraryPage v-if="currentPage === 'people'" entity-type="person" :focus-entity="entityFocus.person" />
      <EntityLibraryPage v-if="currentPage === 'collections'" entity-type="collection" :focus-entity="entityFocus.collection" />
	  <InsightsPage v-if="currentPage === 'insights'" :directories="directories" />
	  <!-- 首次进入才挂载，之后只隐藏不卸载：切走再切回不会丢已加载的图片和滚动位置。 -->
	  <PhotoLibraryPage
	    v-if="photosMounted"
	    v-show="currentPage === 'photos'"
	    :page-active="currentPage === 'photos'"
	    :settings="settings"
	    :tags="tags"
	    @open-settings="currentPage = 'settings'"
	  />
    </div>

    <!-- 命令面板（⌘⇧P / Ctrl+Shift+P）：不新增顶栏按钮，只有快捷键这一个入口 -->
    <CommandPalette
      v-if="!startupError"
      :open="commandPaletteOpen"
      :open-video="openVideoFromCommand"
      @close="closeCommandPalette"
    />

    <!-- 全局提示宿主：webview 的 alert/confirm 是哑的，所有错误提示和危险操作确认都走这里 -->
    <AppFeedback />
  </div>
</template>

<script>
import { GetSettings, GetAllTags, GetAllDirectories, GetStartupError, SyncScanDirectories, SyncImageDirectories, GetLibraryCounts, SetWindowForeground } from '../wailsjs/go/main/App';
import VideoListPage from './components/VideoListPage.vue';
import SettingsPage from './components/SettingsPage.vue';
import EntityLibraryPage from './components/EntityLibraryPage.vue';
import InsightsPage from './components/InsightsPage.vue';
import PhotoLibraryPage from './components/PhotoLibraryPage.vue';
import AppFeedback from './components/AppFeedback.vue';
import CommandPalette from './components/CommandPalette.vue';
import { logFrontend } from './utils/frontendLog.js';
import { appCommandsMixin } from './utils/appCommands.js';

// 扫描根的身份只由路径集合决定：别名、时间戳变了不影响"扫描范围"。
function scanRootKey(dirs) {
  return (dirs || []).map(dir => String(dir?.path || '')).sort().join('\n');
}

export default {
  name: 'App',
  // 命令面板的全局快捷键与命令注册都在 appCommandsMixin 里（D-029）。
  mixins: [appCommandsMixin],
  components: { VideoListPage, SettingsPage, EntityLibraryPage, InsightsPage, PhotoLibraryPage, AppFeedback, CommandPalette },
  data() {
    return {
      currentPage: 'videos',
      // 图片页首次访问后就一直留在 DOM 里（v-show），这个开关只负责"第一次才挂载"。
      photosMounted: false,
      tags: [],
      directories: [],
      startupError: '',
      systemTheme: 'light',
      libraryCounts: null,
      // 已上报给后端的前后台标记；null = 还没报过。相同值不重复上报。
      windowForeground: null,
      foregroundListeners: null,
      settings: {
        confirm_before_delete: true,
        delete_original_file: false,
        video_extensions: '',
        image_extensions: '',
        scan_exclude_paths: '',
        play_weight: 2.0,
        auto_scan_on_startup: false,
        theme: 'system',
        log_enabled: false
      }
    };
  },
  async mounted() {
    // 前后台上报（D-013）：后端据此决定长任务终态要不要发系统通知。
    // 放在最前面，数据库连不上时也要报——通知开关与库无关。
    this.attachForegroundReporting();
    this.startupError = await GetStartupError();
    if (this.startupError) {
      this.applyTheme();
      return;
    }

    await this.loadSettings();
    await this.loadDirectories();
    this.loadTags();
    this.loadLibraryCounts();
    
    const mediaQuery = window.matchMedia('(prefers-color-scheme: dark)');
    this.systemTheme = mediaQuery.matches ? 'dark' : 'light';
    this.applyTheme();
    mediaQuery.addEventListener('change', e => {
      this.systemTheme = e.matches ? 'dark' : 'light';
      this.applyTheme();
    });

    if (this.settings.auto_scan_on_startup) {
      if (this.directories.length > 0) {
        this.incrementalScanAll();
      }
      this.incrementalScanImageDirectories();
    }
  },
  beforeUnmount() {
    this.detachForegroundReporting();
  },
  watch: {
    'settings.theme'() {
      this.applyTheme();
    },
    currentPage: {
      immediate: true,
      handler(page) {
        if (page === 'photos') this.photosMounted = true;
      }
    }
  },
  computed: {
    // 计数任一失败就整段不显示，不给占位数字——头部的数字是用来一眼确认库规模的，
    // 显示一个假的比不显示更糟。
    libraryCountsText() {
      const counts = this.libraryCounts;
      if (!counts) return '';
      const format = value => Number(value || 0).toLocaleString('zh-CN');
      return `库 ${format(counts.video_count)} 视频 · ${format(counts.image_count)} 图片`;
    }
  },
  methods: {
    // 三个事件报的是同一件事：窗口现在是不是用户正在看的那个。
    // 后端未收到上报时默认按后台处理，所以挂上监听后立刻同步一次当前状态。
    attachForegroundReporting() {
      if (this.foregroundListeners) return;
      const onFocus = () => this.reportWindowForeground(true);
      const onBlur = () => this.reportWindowForeground(false);
      const onVisibility = () => this.reportWindowForeground(this.documentIsVisibleAndFocused());
      window.addEventListener('focus', onFocus);
      window.addEventListener('blur', onBlur);
      document.addEventListener('visibilitychange', onVisibility);
      this.foregroundListeners = { onFocus, onBlur, onVisibility };
      this.reportWindowForeground(this.documentIsVisibleAndFocused());
    },
    detachForegroundReporting() {
      const listeners = this.foregroundListeners;
      if (!listeners) return;
      window.removeEventListener('focus', listeners.onFocus);
      window.removeEventListener('blur', listeners.onBlur);
      document.removeEventListener('visibilitychange', listeners.onVisibility);
      this.foregroundListeners = null;
    },
    documentIsVisibleAndFocused() {
      const visible = document.visibilityState !== 'hidden';
      const focused = typeof document.hasFocus === 'function' ? document.hasFocus() : true;
      return visible && focused;
    },
    reportWindowForeground(foreground) {
      const next = Boolean(foreground);
      if (this.windowForeground === next) return;
      this.windowForeground = next;
      Promise.resolve(SetWindowForeground(next)).catch(err => {
        this.debugLog('SetWindowForeground failed', { err: String(err) }, true);
      });
    },
    async loadLibraryCounts() {
      try {
        this.libraryCounts = await GetLibraryCounts();
      } catch (err) {
        this.libraryCounts = null;
        this.debugLog('loadLibraryCounts failed', { err: String(err) }, true);
      }
    },
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
      const rootsChanged = scanRootKey(this.directories) !== scanRootKey(newDirectories);
      this.directories = newDirectories;
      // 扫描根真的变了才对账：新根下的文件立刻入库，范围外的旧记录靠查询侧的
      // 扫描根裁剪自动从列表里消失。只改别名不该触发一次全盘扫描。
      if (rootsChanged && this.directories.length > 0) this.incrementalScanAll();
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
    },
    async incrementalScanImageDirectories() {
      try {
        const result = await SyncImageDirectories();
        this.debugLog('incrementalScanImageDirectories resolved', result);
      } catch (err) {
        this.debugLog('incrementalScanImageDirectories failed', { err: String(err) }, true);
      }
    }
  }
};
</script>

<style>
/* --- Header ---
   原型 A1：52px 通栏、不透明面板、底部一条发丝线；导航是 32px 胶囊，
   选中态直接填主色，不再靠投影和浅底区分。 */
.header {
  height: 52px;
  flex: none;
  padding: 0 20px 0 82px;
  display: flex;
  align-items: center;
  gap: 20px;
  border: 0;
  border-bottom: 1px solid var(--hairline);
  border-radius: 0;
  background: var(--header-bg);
  z-index: 100;
  --wails-draggable: drag;
}
.header-left { pointer-events: none; }
.header h1 { font-size: 15px; font-weight: 700; letter-spacing: 0.02em; color: var(--text-primary); }
.header-nav {
  display: flex;
  gap: 2px;
  align-items: center;
  --wails-draggable: none;
}

.nav-btn {
  height: 32px;
  padding: 0 14px;
  background: transparent;
  border: none;
  border-radius: var(--radius);
  color: var(--text-secondary);
  font-size: 13.5px;
  font-weight: 500;
  cursor: pointer;
  transition: background var(--transition), color var(--transition);
}
.nav-btn.active { color: var(--accent-on); background: var(--accent-color); font-weight: 600; }
.nav-btn:hover:not(.active) { color: var(--text-primary); background: var(--panel-muted-bg); }

.header-counts {
  margin-left: auto;
  color: var(--text-muted);
  font-family: var(--font-mono);
  font-size: 12px;
  white-space: nowrap;
  --wails-draggable: none;
}

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
  border-radius: var(--radius-lg);
  padding: 28px 32px;
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

</style>
