<template>
  <div id="app" class="app-shell glass-app-shell">
    <div class="header glass-surface">
      <div class="header-left">
        <h1>析微影策</h1>
      </div>
      <!-- 顶栏按三组渲染（D-PC59）：片库、片单、工具，组间分隔；「下载」只在开启浏览器插件桥接时出现。
           页面清单与命令面板的导航组同一份（utils/appCommands.js 的 APP_NAV_GROUPS）。 -->
      <nav class="header-nav" aria-label="页面导航">
        <template v-for="(group, index) in visibleNavGroups" :key="group.key">
          <span v-if="index > 0" class="header-nav__divider" aria-hidden="true"></span>
          <div class="header-nav__group" role="group" :aria-label="group.label" :data-test="`nav-group-${group.key}`">
            <button
              v-for="page in group.pages"
              :key="page.key"
              type="button"
              :class="['nav-btn', { active: currentPage === page.key }]"
              :data-test="`nav-${page.key}`"
              @click="navigateTo(page.key)"
            >{{ page.label }}</button>
          </div>
        </template>
        <span class="header-nav__divider" aria-hidden="true"></span>
        <button
          type="button"
          :class="['nav-btn', { active: currentPage === 'settings' }]"
          data-test="nav-settings"
          @click="navigateTo('settings')"
        >设置</button>
      </nav>
      <div v-if="!startupError" class="header-tools">
        <button type="button" class="header-tool-btn" data-test="open-playback-queue" @click="playbackQueueOpen = true">待播队列</button>
        <button type="button" class="header-tool-btn" data-test="open-pending-work" @click="openPendingWork">
          待处理
          <span v-if="pendingWorkTotal > 0" class="header-badge" data-test="pending-work-badge">{{ badgeText(pendingWorkTotal) }}</span>
        </button>
        <button type="button" class="header-tool-btn" data-test="open-task-center" @click="openTaskCenter">
          任务
          <span v-if="taskCenterBadge.running > 0" class="header-badge" data-test="task-center-badge">{{ badgeText(taskCenterBadge.running) }}</span>
          <span v-if="taskCenterBadge.failed" class="header-dot" role="img" aria-label="有任务上一轮失败" data-test="task-center-failed-dot"></span>
        </button>
      </div>
      <div v-if="libraryCountsText" class="header-counts">{{ libraryCountsText }}</div>
    </div>

    <WallpaperStatusBar v-if="!startupError" />
    <PlaybackQueuePanel v-if="!startupError" :open="playbackQueueOpen" @open="playbackQueueOpen = true" @close="playbackQueueOpen = false" @open-video="openVideoByID" />

    <div v-if="startupError" class="startup-error-view">
      <div class="startup-error-card">
        <h2>应用已启动，但数据库连接失败</h2>
        <p class="startup-error-text">{{ startupError }}</p>
        <p class="startup-error-hint">
          这通常只是当前没有连上数据库，已有数据不会因此被删除。
        </p>
        <p v-if="startupBackend === 'sqlite'" class="startup-error-hint" data-test="startup-error-sqlite-hint">
          当前使用本机 SQLite 数据库：请检查数据目录所在的磁盘是否已连接、是否有剩余空间和写入权限；库文件损坏时，可以用备份目录里的备份替换后重新打开应用。
        </p>
        <p v-else class="startup-error-hint" data-test="startup-error-postgres-hint">
          当前使用 Postgres 数据库：如果你是双击启动应用，请优先检查 macOS 是否已允许“析微影策”访问本地网络，并确认 Postgres 地址和端口可达。
        </p>
        <button type="button" class="btn-secondary" data-test="startup-open-diagnostics" @click="startupDiagnosticsOpen = true">运行状态与诊断</button>
      </div>
    </div>

    <div v-else class="main-view">
      <VideoListPage
        ref="videoListPage"
        v-show="currentPage === 'videos'"
        :page-active="currentPage === 'videos'"
        :consolidation-route="consolidationRoute"
        @consolidation-opened="consolidationRoute = null"
        :tags="tags"
        :settings="settings"
        :directories="directories"
        @reload-tags="loadTags"
        @reload-directories="loadDirectories"
        @person-converted="handleTagPersonConverted"
        @update-settings="handleSettingsUpdate"
        @search-scenes="openScenesWithFilter"
        @open-video-edit="openVideoEditProject"
      />

      <ViewingNotesPage v-if="notesMounted" v-show="currentPage === 'viewing-notes'" :page-active="currentPage === 'viewing-notes'" @open-bookmark="openBookmark" @open-video="openVideoFromCommand" @open-video-id="openVideoByID" />
      <DownloadsPage v-if="currentPage === 'downloads'" />
      <WatchlistPage v-if="currentPage === 'watchlist'" />
      <!-- 按需挂载：榜单页一挂载就会报一次 OpenMovieChartYear（可能起后台抓取），
           不在这个页面时不该有那个副作用，所以用 v-if 而不是 v-show。 -->
      <MovieChartPage v-if="currentPage === 'movie-chart'" />
      <!-- 已看页只读一次本地标记表，进页面才挂载就够了；切回来重新读，不用自己做失效。 -->
      <WatchedMoviesPage v-if="currentPage === 'watched-movies'" @navigate="navigateTo" @open-video="openVideoFromCommand" />

      <SettingsPage
        ref="settingsPage"
        v-if="currentPage === 'settings'"
        :settings="settings"
        :directories="directories"
        @settings-saved="handleSettingsUpdate"
        @directories-changed="handleDirectoriesChanged"
        @update:dirty="settingsDirty = Boolean($event)"
        @relaunch-required="showRelaunchRequired($event && $event.message)"
      />

      <EntityLibraryPage v-if="currentPage === 'people'" entity-type="person" :focus-entity="entityFocus.person" />
      <EntityLibraryPage v-if="currentPage === 'collections'" entity-type="collection" :focus-entity="entityFocus.collection" />
	  <InsightsPage v-if="currentPage === 'insights'" :directories="directories" />
	  <!-- 场景检索：首次进入才挂载，之后只隐藏，点开命中再回来时结果还在。 -->
	  <ScenesPage v-if="scenesMounted" v-show="currentPage === 'scenes'" :page-active="currentPage === 'scenes'" :scope-request="scenesScope" @open-video-at="openVideoAt" />
	  <!-- 视频工作台：首次进入才挂载，之后只隐藏，事件与编辑中的项目都还在。 -->
	  <VideoWorkbenchPage
	    v-if="workbenchMounted"
	    v-show="currentPage === 'video-workbench'"
	    :page-active="currentPage === 'video-workbench'"
	    :open-request="workbenchRequest"
	    @open-video="openVideoByID"
	    @cleanup-sources="cleanupEditSources"
	  />
	  <!-- 首次进入才挂载，之后只隐藏不卸载：切走再切回不会丢已加载的图片和滚动位置。 -->
	  <PhotoLibraryPage
	    ref="photoLibrary"
	    v-if="photosMounted"
	    v-show="currentPage === 'photos'"
	    :page-active="currentPage === 'photos'"
	    :settings="settings"
	    :tags="tags"
	    @open-settings="navigateTo('settings')"
	  />
    </div>

    <!-- 命令面板（⌘⇧P / Ctrl+Shift+P）：不新增顶栏按钮，只有快捷键这一个入口 -->
    <CommandPalette
      v-if="!startupError"
      :open="commandPaletteOpen"
      :open-video="openVideoFromCommand"
      :before-run="prepareCommandRun"
      @close="closeCommandPalette"
    />

    <!-- 任务中心与待处理工作台常挂载（关着也在），顶栏角标才能跟着事件与定时刷新走。 -->
    <TaskCenterDrawer
      v-if="!startupError"
      :open="taskCenterOpen"
      @close="taskCenterOpen = false"
      @badge-change="taskCenterBadge = $event"
      @open-video="openVideoByID"
      @open-consolidation="openConsolidationTask"
      @open-video-edit="openVideoEditProject"
    />
    <PendingWorkHub
      v-if="!startupError"
      :open="pendingWorkOpen"
      @close="pendingWorkOpen = false"
      @badge-change="pendingWorkTotal = $event"
      @run-command="runPendingCommand"
      @open-task-center="openTaskCenter"
    />
    <QuitConfirmDialog />

    <!-- 「待重启」终态（D-PC55 / APP-02）：切换后端或切回之前的后端成功后，维护围栏一直保持到重启，
         任何数据库读写都会被拒绝。这层遮罩不可关闭，唯一出口是「立即重启」。 -->
    <div v-if="relaunchRequired" class="relaunch-overlay" role="alertdialog" aria-modal="true" data-test="relaunch-overlay">
      <div class="relaunch-card">
        <h2>需要重启应用</h2>
        <p class="relaunch-text">{{ relaunchMessage || '数据库后端已切换，重启应用后生效；在此之前数据库保持只读。' }}</p>
        <p v-if="relaunchDownloadsActive" class="relaunch-note" data-test="relaunch-downloads">进行中的下载会中断，重启后需回浏览器重新推送。</p>
        <p v-if="relaunchError" class="relaunch-error" data-test="relaunch-error">{{ relaunchError }}</p>
        <button type="button" class="btn btn-primary" :disabled="relaunching" data-test="relaunch-now" @click="relaunchNow">
          {{ relaunching ? '正在重启…' : '立即重启' }}
        </button>
      </div>
    </div>

    <BaseModal v-if="startupError && startupDiagnosticsOpen" class="startup-diagnostics" close-on-overlay @close="startupDiagnosticsOpen = false">
      <header class="startup-diagnostics__head"><h2>运行状态与诊断</h2><button type="button" class="btn-secondary btn-compact" data-test="startup-close-diagnostics" @click="startupDiagnosticsOpen = false">关闭</button></header>
      <SystemHealthPanel :actions-enabled="false" />
    </BaseModal>

    <!-- 全局提示宿主：webview 的 alert/confirm 是哑的，所有错误提示和危险操作确认都走这里 -->
    <AppFeedback />
  </div>
</template>

<script>
import { GetSettings, GetAllTags, GetAllDirectories, GetStartupError, SyncScanDirectories, SyncImageDirectories, GetLibraryCounts, SetWindowForeground, GetVideosByIDs, GetDatabaseBackendStatus, GetDatabaseSwitchStatus, ListDownloadTasks, RelaunchApp } from '../wailsjs/go/main/App';
import VideoListPage from './components/VideoListPage.vue';
import SettingsPage from './components/SettingsPage.vue';
import EntityLibraryPage from './components/EntityLibraryPage.vue';
import InsightsPage from './components/InsightsPage.vue';
import PhotoLibraryPage from './components/PhotoLibraryPage.vue';
import DownloadsPage from './components/DownloadsPage.vue';
import WatchlistPage from './components/WatchlistPage.vue';
import MovieChartPage from './components/MovieChartPage.vue';
import WatchedMoviesPage from './components/WatchedMoviesPage.vue';
import ViewingNotesPage from './components/ViewingNotesPage.vue';
import ScenesPage from './components/ScenesPage.vue';
import VideoWorkbenchPage from './components/VideoWorkbenchPage.vue';
import AppFeedback from './components/AppFeedback.vue';
import CommandPalette from './components/CommandPalette.vue';
import TaskCenterDrawer from './components/TaskCenterDrawer.vue';
import PendingWorkHub from './components/PendingWorkHub.vue';
import QuitConfirmDialog from './components/QuitConfirmDialog.vue';
import WallpaperStatusBar from './components/WallpaperStatusBar.vue';
import PlaybackQueuePanel from './components/PlaybackQueuePanel.vue';
import BaseModal from './components/ui/BaseModal.vue';
import SystemHealthPanel from './components/SystemHealthPanel.vue';
import { runtimeEventsMixin } from './components/video-list/runtimeEvents.js';
import { logFrontend } from './utils/frontendLog.js';
import { confirmAction, notify, notifyError } from './utils/feedback.js';
import { APP_NAV_GROUPS, appCommandsMixin, isNavPageVisible } from './utils/appCommands.js';

// 扫描根的身份只由路径集合决定：别名、时间戳变了不影响"扫描范围"。
function scanRootKey(dirs) {
  return (dirs || []).map(dir => String(dir?.path || '')).sort().join('\n');
}

// 浏览器插件的下载里还有在跑或排队的：「立即重启」会打断它们（m5）。
function hasActiveDownloads(tasks) {
  return (tasks || []).some(task => task?.state === 'running' || task?.state === 'queued');
}

export default {
  name: 'App',
  // 命令面板的全局快捷键与命令注册都在 appCommandsMixin 里（D-029）。
  mixins: [appCommandsMixin, runtimeEventsMixin],
  components: {
    VideoListPage, SettingsPage, EntityLibraryPage, InsightsPage, PhotoLibraryPage, DownloadsPage, WatchlistPage,
    MovieChartPage, WatchedMoviesPage, AppFeedback, CommandPalette, TaskCenterDrawer, PendingWorkHub, QuitConfirmDialog, WallpaperStatusBar,
    BaseModal, SystemHealthPanel, ViewingNotesPage, PlaybackQueuePanel, ScenesPage, VideoWorkbenchPage
  },
  data() {
    return {
      currentPage: 'videos',
      playbackQueueOpen: false,
      // 图片页首次访问后就一直留在 DOM 里（v-show），这个开关只负责"第一次才挂载"。
      photosMounted: false,
      notesMounted: false,
      scenesMounted: false,
      // 片库带过来的场景检索范围：{ filter, token }；为空表示全部视频。
      scenesScope: null,
      workbenchMounted: false,
      // 要视频工作台打开的项目：{ projectID, token }（片库新建、任务中心「在工作台中打开」）。
      workbenchRequest: null,
      tags: [],
      directories: [],
      startupError: '',
      startupDiagnosticsOpen: false,
      // 启动错误页按当前后端给提示（D-PC58）：sqlite / postgres，读不到时为空串（按 Postgres 口径提示）。
      startupBackend: '',
      // 「待重启」遮罩（D-PC55）。
      relaunchRequired: false,
      relaunchMessage: '',
      relaunching: false,
      relaunchError: '',
      relaunchDownloadsActive: false,
      systemTheme: 'light',
      libraryCounts: null,
      // 已上报给后端的前后台标记；null = 还没报过。相同值不重复上报。
      windowForeground: null,
      foregroundListeners: null,
      // 设置页有未保存修改（SettingsPage 的 update:dirty，D-PC57）：切走前要先确认。
      settingsDirty: false,
      taskCenterOpen: false,
      consolidationRoute: null,
      consolidationRouteGeneration: 0,
      pendingWorkOpen: false,
      // 两个顶栏角标的数据由常挂载的抽屉与工作台报上来。
      taskCenterBadge: { running: 0, failed: false },
      pendingWorkTotal: 0,
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
      // 后端状态只读配置、不需要数据库连接，连不上库时也能拿到。
      try {
        const status = await GetDatabaseBackendStatus();
        this.startupBackend = String(status?.backend || '');
      } catch {
        this.startupBackend = '';
      }
      this.applyTheme();
      return;
    }

    // 「待重启」终态的补读（I-1）：切换或切回成功之后 WebView 重载（或别的原因重新挂载）时，事件早已
    // 发过，靠 GetDatabaseSwitchStatus 把遮罩补回来。不等它：库此时被围栏挡着，下面的读取都会失败。
    this.recoverRelaunchRequired();
    await this.loadSettings();
    await this.loadDirectories();
    this.loadTags();
    this.loadLibraryCounts();
    // 扫描完成事件（D-PC09）：库里真的多了、少了或恢复了视频时，顶栏的库规模计数跟着刷新。
    // 列表本身的刷新归片库页。
    this.registerRuntimeEvent('library-scan-summary', event => this.handleLibraryScanSummary(event));
    // 迁移并切换在后台完成：用户可能已经离开设置页，遮罩由这里统一弹出（D-PC55）。
    this.registerRuntimeEvent('database-switch-state', status => {
      if (status?.completed && status?.relaunch_required) {
        this.showRelaunchRequired(status.message);
      }
    });
    // 遮罩里的「下载会中断」提示跟着下载任务的推送走（m5）。
    this.registerRuntimeEvent('browser-download-tasks', tasks => {
      if (this.relaunchRequired) this.relaunchDownloadsActive = hasActiveDownloads(tasks);
    });
    
    const mediaQuery = window.matchMedia('(prefers-color-scheme: dark)');
    this.systemTheme = mediaQuery.matches ? 'dark' : 'light';
    this.applyTheme();
    mediaQuery.addEventListener('change', e => {
      this.systemTheme = e.matches ? 'dark' : 'light';
      this.applyTheme();
    });

    if (this.settings.auto_scan_on_startup) {
      if (this.directories.length > 0) {
        this.incrementalScanAll('startup');
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
    // 「下载」页只在开启浏览器插件桥接时出现在顶栏与命令面板（D-PC59）。
    'settings.browser_bridge_enabled'() {
      this.registerAppCommands();
    },
    currentPage: {
      immediate: true,
      handler(page) {
        if (page === 'photos') this.photosMounted = true;
        if (page === 'viewing-notes') this.notesMounted = true;
        if (page === 'scenes') this.scenesMounted = true;
        if (page === 'video-workbench') this.workbenchMounted = true;
      }
    }
  },
  computed: {
    visibleNavGroups() {
      return APP_NAV_GROUPS
        .map(group => ({ ...group, pages: group.pages.filter(page => isNavPageVisible(page, this.settings)) }))
        .filter(group => group.pages.length > 0);
    },
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
    // 「待重启」终态只进不出：之后再收到事件或 emit 也只更新说明文字。
    showRelaunchRequired(message) {
      const first = !this.relaunchRequired;
      this.relaunchRequired = true;
      if (message) this.relaunchMessage = String(message);
      if (first) this.loadRelaunchDownloads();
    },
    async recoverRelaunchRequired() {
      try {
        const status = await GetDatabaseSwitchStatus();
        if (status?.completed && status?.relaunch_required) this.showRelaunchRequired(status.message);
      } catch (err) {
        // 读不到时事件仍是主通道；设置页的数据库分区挂载时也会再补读一次。
        this.debugLog('GetDatabaseSwitchStatus failed', { err: String(err) }, true);
      }
    },
    // 有在跑或排队的浏览器插件下载时，遮罩提示重启会打断它们（m5）。下载列表读内存与已缓存的历史，
    // 围栏期间照样能读；读不到就不提示。
    async loadRelaunchDownloads() {
      try {
        this.relaunchDownloadsActive = hasActiveDownloads(await ListDownloadTasks());
      } catch {
        this.relaunchDownloadsActive = false;
      }
    },
    async relaunchNow() {
      if (this.relaunching) return;
      this.relaunching = true;
      this.relaunchError = '';
      try {
        const result = await RelaunchApp();
        if (!result?.relaunched) {
          this.relaunchError = result?.message || '无法自动重启，请手动退出并重新打开应用。';
          this.relaunching = false;
        }
      } catch (error) {
        this.relaunchError = String(error?.message || error || '') || '无法自动重启，请手动退出并重新打开应用。';
        this.relaunching = false;
      }
    },
    // 所有切页都走这里（顶栏、命令面板、待处理工作台、图片页的「去设置」）。设置页有未保存修改时
    // 先确认「放弃未保存的设置修改？」（D-PC57、APP-08）：SettingsPage 是 v-if，切走即销毁表单。
    // 能直接切时同步返回 true；要确认时返回 Promise，用户选继续编辑则 resolve 为 false。
    navigateTo(page) {
      if (!page || page === this.currentPage) return true;
      if (this.currentPage === 'settings' && this.settingsDirty) {
        return confirmAction({
          title: '离开设置页',
          message: '放弃未保存的设置修改？',
          confirmText: '放弃修改',
          cancelText: '继续编辑',
          danger: true
        }).then(ok => {
          if (!ok) return false;
          this.settingsDirty = false;
          this.currentPage = page;
          return true;
        });
      }
      this.currentPage = page;
      return true;
    },
    async openConsolidationTask(taskID) {
      const generation = ++this.consolidationRouteGeneration;
      const navigated = await this.navigateTo('videos');
      if (navigated === false || generation !== this.consolidationRouteGeneration) return;
      this.consolidationRoute = { taskID: Number(taskID), generation };
      this.taskCenterOpen = false;
    },
    openTaskCenter() {
      this.pendingWorkOpen = false;
      this.taskCenterOpen = true;
    },
    openPendingWork() {
      this.taskCenterOpen = false;
      this.pendingWorkOpen = true;
    },
    // 工作台的「处理」：按固定命令 ID 跳到宿主页并打开既有面板（详细设计 §6.1）。
    runPendingCommand(commandID, label) {
      return Promise.resolve(this.runHostCommand(commandID, label))
        .catch(err => notifyError(`打开「${label || '待处理事项'}」失败：${err}`));
    },
    // 任务中心里超分产物的「在片库中打开」：按 ID 取回视频再走片库页既有的详情入口。
    async openBookmark(resolution) {
      const generation = this._bookmarkNavigation = Symbol();
      const navigated = await this.navigateTo('videos');
      if (navigated === false || this._bookmarkNavigation !== generation) return;
      await this.$nextTick();
      return this.$refs.videoListPage?.openBookmark(resolution);
    },
    // 片库工具栏「在当前筛选中搜场景」：带上当前 LibraryFilter 进入场景检索页（D-MW-SCENES）。
    openScenesWithFilter(filter) {
      this.scenesScope = { filter: filter ? { ...filter } : null, token: Date.now() };
      return this.navigateTo('scenes');
    },
    // 视频工作台：片库新建项目、任务中心「在工作台中打开」后切过去并选中该项目。
    openVideoEditProject(projectID) {
      const id = Number(projectID);
      if (!Number.isInteger(id) || id <= 0) return false;
      this.workbenchRequest = { projectID: id, token: Date.now() };
      this.taskCenterOpen = false;
      return this.navigateTo('video-workbench');
    },
    // 工作台的「清理原片…」：回片库走既有的删除确认与废纸篓流程（视频编辑合同 TC-23），工作台自己从不删除。
    async cleanupEditSources(videoIDs) {
      const ids = [...new Set((videoIDs || []).map(Number).filter(id => id > 0))];
      if (!ids.length) return;
      try {
        const videos = await GetVideosByIDs(ids) || [];
        if (!videos.length) {
          notify('原片已不在片库中，可能已经删除。');
          return;
        }
        const navigated = await this.navigateTo('videos');
        if (navigated === false) return;
        await this.$nextTick();
        this.$refs.videoListPage?.requestDeleteVideos(videos);
      } catch (err) {
        notifyError(`打开删除确认失败：${err}`);
      }
    },
    // 场景命中：回到片库打开详情抽屉，从区间起点（已提前 2 秒）内嵌播放，沿用字幕命中的跳转机制。
    async openVideoAt({ videoID, startMs } = {}) {
      try {
        const [video] = await GetVideosByIDs([Number(videoID)]) || [];
        if (!video) {
          notifyError('这个视频已不在片库中，可能不在扫描目录内或已被删除。');
          return;
        }
        const navigated = await this.navigateTo('videos');
        if (navigated === false) return;
        await this.$nextTick();
        await this.$refs.videoListPage?.openPreviewAt(video, startMs);
      } catch (err) {
        notifyError(`打开视频失败：${err}`);
      }
    },
    async openVideoByID(videoID) {
      try {
        const [video] = await GetVideosByIDs([Number(videoID)]) || [];
        if (!video) {
          notifyError('产物视频不在片库中，可能不在扫描目录内或已被删除。');
          return;
        }
        await this.openVideoFromCommand(video);
      } catch (err) {
        notifyError(`打开视频失败：${err}`);
      }
    },
    badgeText(count) {
      const value = Number(count || 0);
      return value > 99 ? '99+' : String(value);
    },
    handleLibraryScanSummary(event) {
      const result = event?.result;
      if (!result) return;
      const changed = Number(result.added || 0) + Number(result.deleted || 0) + Number(result.restored || 0) + Number(result.stale || 0);
      if (changed > 0) this.loadLibraryCounts();
    },
    handleTagPersonConverted(result) {
      this.$refs.photoLibrary?.handleTagPersonConverted(result);
    },
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
      if (rootsChanged && this.directories.length > 0) this.incrementalScanAll('manual');
    },
    // trigger：启动时那次传 startup，其余（改了扫描根之后的对账）传 manual（§1.2b，后端据此发
    // library-scan-summary）。
    async incrementalScanAll(trigger = 'manual') {
      try {
        const result = await SyncScanDirectories(trigger);
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

<style scoped>
:deep(.startup-diagnostics) { width: min(760px, calc(100vw - 48px)); max-height: calc(100vh - 64px); overflow-y: auto; }
</style>

<style>
.startup-diagnostics__head { display: flex; align-items: center; justify-content: space-between; gap: 16px; margin-bottom: 16px; }
.startup-diagnostics__head h2 { margin: 0; }
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
  padding: 0 12px;
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

/* 顶栏三组（D-PC59）：组内按钮紧挨，组间一条竖向发丝线。 */
.header-nav__group { display: flex; gap: 2px; align-items: center; }
.header-nav__divider {
  width: 1px;
  height: 18px;
  margin: 0 8px;
  background: var(--hairline);
}

/* 右侧常驻入口：待处理、任务中心。角标 = 数量，红点 = 有任务上一轮失败（D-PC18、D-PC27）。 */
.header-tools {
  margin-left: auto;
  display: flex;
  gap: 4px;
  align-items: center;
  --wails-draggable: none;
}
.header-tool-btn {
  position: relative;
  height: 30px;
  padding: 0 10px;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  border: 1px solid var(--hairline);
  border-radius: var(--radius);
  background: var(--control-bg);
  color: var(--text-secondary);
  font-size: 12.5px;
  cursor: pointer;
}
.header-tool-btn:hover { color: var(--text-primary); background: var(--control-hover-bg); }
.header-badge {
  min-width: 18px;
  padding: 0 5px;
  border-radius: 9px;
  background: var(--accent-color);
  color: var(--accent-on);
  font-family: var(--font-mono);
  font-size: 11px;
  line-height: 18px;
  text-align: center;
}
.header-dot {
  position: absolute;
  top: 3px;
  right: 3px;
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--danger-color);
}

.header-counts {
  margin-left: auto;
  color: var(--text-muted);
  font-family: var(--font-mono);
  font-size: 12px;
  white-space: nowrap;
  --wails-draggable: none;
}
.header-tools + .header-counts { margin-left: 0; }

.main-view { flex: 1 1 auto; min-height: 0; overflow-y: auto; overscroll-behavior: contain; display: flex; flex-direction: column; }
.relaunch-overlay {
  position: fixed;
  inset: 0;
  z-index: 3000;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
  background: rgba(0, 0, 0, 0.45);
}
.relaunch-card {
  max-width: 480px;
  width: 100%;
  background: var(--panel-bg);
  border: 1px solid var(--border-color);
  border-radius: var(--radius-lg);
  padding: 24px 28px;
}
.relaunch-card h2 {
  font-size: 18px;
  margin-bottom: 10px;
  color: var(--text-primary);
}
.relaunch-text {
  font-size: 14px;
  line-height: 1.7;
  color: var(--text-primary);
  margin-bottom: 16px;
}
.relaunch-error {
  font-size: 13px;
  color: var(--danger-color);
  margin-bottom: 12px;
}
.relaunch-note {
  font-size: 13px;
  line-height: 1.6;
  color: var(--warning-text);
  margin-bottom: 12px;
}
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
