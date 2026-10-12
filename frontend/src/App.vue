<template>
  <div id="app" class="app-shell glass-app-shell">
    <div class="header glass-surface">
      <div class="header-left">
        <h1>析微影策</h1>
      </div>
      <!-- 顶栏两套导航（D-UI-02、D-UI-09）：区域由当前页面推出，只显示这一侧的导航；组间分隔，
           「片单」收成下拉；「下载」只在开启浏览器插件桥接时出现。页面清单与命令面板同一份
           （utils/appCommands.js 的 VIEWER_NAV_GROUPS / MANAGE_NAV_GROUPS）。管理侧带 command 的三项是动作：
           执行命令面板里同 ID 的既有命令，打开待处理、清理中心与任务中心。 -->
      <nav class="header-nav" aria-label="页面导航" :data-test="`nav-area-${area}`">
        <template v-if="area === 'manage'">
          <button type="button" class="nav-btn" data-test="nav-back-to-viewer" @click="navigateTo('home')">返回观看</button>
          <span class="header-nav__divider" aria-hidden="true"></span>
        </template>
        <template v-for="(group, index) in visibleNavGroups" :key="group.key">
          <span v-if="index > 0" class="header-nav__divider" aria-hidden="true"></span>
          <div class="header-nav__group" role="group" :aria-label="group.label" :data-test="`nav-group-${group.key}`">
            <button
              v-if="group.menu"
              type="button"
              :class="['nav-btn', { active: group.pages.some(page => page.key === currentPage) }]"
              aria-haspopup="menu"
              :aria-expanded="navMenu && navMenu.key === group.key ? 'true' : 'false'"
              :data-test="`nav-menu-${group.key}`"
              @click="toggleNavMenu(group, $event)"
            >{{ group.label }}<span class="nav-btn__caret" aria-hidden="true">▾</span></button>
            <template v-else>
              <button
                v-for="page in group.pages"
                :key="page.key"
                type="button"
                :class="['nav-btn', { active: currentPage === page.key }]"
                :data-test="page.command ? `open-${page.key}` : `nav-${page.key}`"
                @click="selectNavItem(page)"
              >
                {{ page.label }}
                <span v-if="page.key === 'pending-work' && pendingWorkTotal > 0" class="header-badge" data-test="pending-work-badge">{{ badgeText(pendingWorkTotal) }}</span>
                <span v-if="page.key === 'task-center' && taskCenterBadge.running > 0" class="header-badge" data-test="task-center-badge">{{ badgeText(taskCenterBadge.running) }}</span>
                <span v-if="page.key === 'task-center' && taskCenterBadge.failed" class="header-dot" role="img" aria-label="有任务上一轮失败" data-test="task-center-failed-dot"></span>
              </button>
            </template>
          </div>
        </template>
      </nav>
      <BaseMenu
        v-if="navMenu"
        :anchor="navMenu.anchor"
        :items="navMenuItems"
        :min-width="160"
        :label="navMenu.label"
        @select="navigateTo($event.id)"
        @close="navMenu = null"
      />
      <!-- 观看区右侧：待播队列与「管理」入口，待处理数量作为角标挂在「管理」上（D-UI-02）。 -->
      <div v-if="!startupError && area === 'viewer'" class="header-tools">
        <button type="button" class="header-tool-btn" data-test="open-playback-queue" @click="playbackQueueOpen = true">待播队列</button>
        <button type="button" class="header-tool-btn" data-test="open-manage" @click="navigateTo('manage-home')">
          管理
          <span v-if="pendingWorkTotal > 0" class="header-badge" data-test="manage-pending-badge">{{ badgeText(pendingWorkTotal) }}</span>
        </button>
      </div>
      <div v-if="libraryCountsText" class="header-counts">{{ libraryCountsText }}</div>
    </div>

    <WallpaperStatusBar v-if="!startupError" />
    <PlaybackQueuePanel v-if="!startupError" :open="playbackQueueOpen" @open="playbackQueueOpen = true" @close="playbackQueueOpen = false" @open-video="openVideoByID($event, { from: 'playback-queue' })" />

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

    <div v-else ref="mainView" class="main-view">
      <!-- 观看首页（D-UI-04）：默认落地页；首次进入才挂载，之后只隐藏，「返回」回来时滚动位置还在。 -->
      <HomePage
        v-if="homeMounted"
        v-show="currentPage === 'home'"
        :page-active="currentPage === 'home'"
        @open-video="openVideoByID($event, { from: 'home' })"
        @navigate="navigateTo"
        @open-person="openPerson"
      />
      <!-- 整页详情（D-UI-05）：每次打开或返回都是一次新挂载（key），参数来自 detailRoute。 -->
      <VideoDetailPage
        v-if="currentPage === 'video-detail'"
        :key="detailRoute.token"
        :video-id="detailRoute.videoId"
        :start-at-ms="detailRoute.startAtMs"
        :bookmark="detailRoute.bookmark"
        @ready="handleDetailReady"
        @video-state-changed="handleDetailVideoStateChange"
        @back="goBackFromDetail"
        @open-video="openVideoByID($event, { from: 'video-detail' })"
        @navigate="navigateTo"
        @edit-in-manage="editVideoInManage"
        @open-tag="openLibraryTag"
        @open-entity="openRecapEntity"
        @find-similar="openSimilarScenes"
      />
      <ManageHomePage
        v-if="currentPage === 'manage-home'"
        :page-active="currentPage === 'manage-home'"
        @navigate="navigateTo"
        @run-command="runPendingCommand"
        @open-pending-work="openPendingWork"
        @open-task-center="openTaskCenter"
      />
      <!-- 片库页常挂载（命令面板与宿主命令依赖它）：观看侧「视频」与管理侧「片库管理」共用这一个实例。 -->
      <VideoListPage
        ref="videoListPage"
        v-show="libraryPageVisible"
        :page-active="libraryPageVisible"
        :cleanup-hotkey="currentPage === 'library-manage'"
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
        @find-similar-scenes="openSimilarScenes"
        @open-video-edit="openVideoEditProject"
      />

      <ViewingNotesPage v-if="notesMounted" v-show="currentPage === 'viewing-notes'" :page-active="currentPage === 'viewing-notes'" @open-bookmark="openBookmark($event, { from: 'viewing-notes' })" @open-video="openVideo($event, { from: 'viewing-notes' })" @open-video-id="openVideoByID($event, { from: 'viewing-notes' })" />
      <!-- 观看回顾：首次进入才挂载，之后只隐藏；不可见时停掉实时刷新。 -->
      <RecapPage
        v-if="recapMounted"
        v-show="currentPage === 'recap'"
        :page-active="currentPage === 'recap'"
        @open-video-id="openVideoByID($event, { from: 'recap' })"
        @open-tag="openLibraryTag"
        @open-image="openPhotoImage"
        @open-entity="openRecapEntity"
        @navigate="navigateTo"
      />
      <DownloadsPage v-if="currentPage === 'downloads'" />
      <WatchlistPage v-if="currentPage === 'watchlist'" />
      <!-- 按需挂载：榜单页一挂载就会报一次 OpenMovieChartYear（可能起后台抓取），
           不在这个页面时不该有那个副作用，所以用 v-if 而不是 v-show。 -->
      <MovieChartPage v-if="currentPage === 'movie-chart'" />
      <!-- 已看页只读一次本地标记表，进页面才挂载就够了；切回来重新读，不用自己做失效。 -->
      <WatchedMoviesPage v-if="currentPage === 'watched-movies'" @navigate="navigateTo" @open-video="openVideo($event, { from: 'watched-movies' })" />

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

      <!-- 人物与作品集属观看区：点视频进整页详情（D-UI-07），编辑资料从详情页的「编辑资料」进片库管理。 -->
      <EntityLibraryPage v-if="currentPage === 'people'" entity-type="person" :focus-entity="entityFocus.person" @open-video="openVideo($event, { from: 'people' })" />
      <EntityLibraryPage v-if="currentPage === 'collections'" entity-type="collection" :focus-entity="entityFocus.collection" @open-video="openVideo($event, { from: 'collections' })" />
	  <InsightsPage
	    v-if="currentPage === 'insights'"
	    :directories="directories"
	    @open-video-id="openVideoByID($event, { from: 'insights' })"
	    @run-command="runPendingCommand"
	    @videos-changed="handleVideosChangedElsewhere"
	    @busy-change="insightsBusy = $event"
	  />
	  <DataCarePage
	    v-if="currentPage === 'data-care'"
	    :tab-request="dataCareTab"
	    :directories="directories"
	    @open-video-id="openVideoByID($event, { from: 'data-care' })"
	    @run-command="runPendingCommand"
	    @videos-changed="handleVideosChangedElsewhere"
	    @open-library-missing="openMissingFilter"
	    @navigate="navigateTo"
	  />
	  <!-- 场景检索：首次进入才挂载，之后只隐藏，点开命中再回来时结果还在。 -->
	  <ScenesPage v-if="scenesMounted" v-show="currentPage === 'scenes'" :page-active="currentPage === 'scenes'" :scope-request="scenesScope" @open-video-at="openVideoAt($event, { from: 'scenes' })" />
	  <!-- 视频工作台：首次进入才挂载，之后只隐藏，事件与编辑中的项目都还在。 -->
	  <VideoWorkbenchPage
	    v-if="workbenchMounted"
	    v-show="currentPage === 'video-workbench'"
	    :page-active="currentPage === 'video-workbench'"
	    :open-request="workbenchRequest"
	    @open-video="openVideoByID($event, { from: 'video-workbench' })"
	    @cleanup-sources="cleanupEditSources"
	  />
	  <!-- 首次进入才挂载，之后只隐藏不卸载：切走再切回不会丢已加载的图片和滚动位置。
	       观看侧「图片」与管理侧「图片管理」共用这一个实例。 -->
	  <PhotoLibraryPage
	    ref="photoLibrary"
	    v-if="photosMounted"
	    v-show="photoPageVisible"
	    :page-active="photoPageVisible"
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
      @open-video="openVideoByID($event, { from: 'task-center' })"
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
    <!-- 版本对比播放器：任何地方调 openVersionCompare(左, 右) 就在这里打开；每次打开是一次新会话（key）。 -->
    <VersionCompareDialog
      v-if="!startupError && versionCompare.open"
      :key="versionCompare.session"
      :left-video-id="versionCompare.leftVideoID"
      :right-video-id="versionCompare.rightVideoID"
      @close="handleVersionCompareClose"
    />

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
import DataCarePage from './components/DataCarePage.vue';
import PhotoLibraryPage from './components/PhotoLibraryPage.vue';
import DownloadsPage from './components/DownloadsPage.vue';
import WatchlistPage from './components/WatchlistPage.vue';
import MovieChartPage from './components/MovieChartPage.vue';
import WatchedMoviesPage from './components/WatchedMoviesPage.vue';
import ViewingNotesPage from './components/ViewingNotesPage.vue';
import RecapPage from './components/RecapPage.vue';
import ScenesPage from './components/ScenesPage.vue';
import VideoWorkbenchPage from './components/VideoWorkbenchPage.vue';
import HomePage from './components/viewer/HomePage.vue';
import VideoDetailPage from './components/viewer/VideoDetailPage.vue';
import ManageHomePage from './components/manage/ManageHomePage.vue';
import AppFeedback from './components/AppFeedback.vue';
import CommandPalette from './components/CommandPalette.vue';
import TaskCenterDrawer from './components/TaskCenterDrawer.vue';
import PendingWorkHub from './components/PendingWorkHub.vue';
import QuitConfirmDialog from './components/QuitConfirmDialog.vue';
import VersionCompareDialog from './components/VersionCompareDialog.vue';
import WallpaperStatusBar from './components/WallpaperStatusBar.vue';
import PlaybackQueuePanel from './components/PlaybackQueuePanel.vue';
import BaseMenu from './components/ui/BaseMenu.vue';
import BaseModal from './components/ui/BaseModal.vue';
import SystemHealthPanel from './components/SystemHealthPanel.vue';
import { runtimeEventsMixin } from './components/video-list/runtimeEvents.js';
import { logFrontend } from './utils/frontendLog.js';
import { confirmAction, notify, notifyError } from './utils/feedback.js';
import {
  appCommandsMixin, isNavPageVisible, MANAGE_NAV_GROUPS, pageArea, VIDEO_DETAIL_PAGE, VIEWER_NAV_GROUPS, whenNavigated
} from './utils/appCommands.js';
import { findCommand } from './utils/commandRegistry.js';
import { opensVideoDetail, popDetailReturn, pushDetailReturn, restoreScrollWhenReachable } from './utils/viewerNavigation.js';
import { recordDownloadDuplicate, recordScanSummary } from './utils/sessionChanges.js';
import { closeVersionCompare, versionCompareState } from './utils/versionCompare.js';

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
    VideoListPage, SettingsPage, EntityLibraryPage, InsightsPage, DataCarePage, PhotoLibraryPage, DownloadsPage, WatchlistPage,
    MovieChartPage, WatchedMoviesPage, AppFeedback, CommandPalette, TaskCenterDrawer, PendingWorkHub, QuitConfirmDialog, VersionCompareDialog, WallpaperStatusBar,
    BaseMenu, BaseModal, SystemHealthPanel, ViewingNotesPage, PlaybackQueuePanel, ScenesPage, VideoWorkbenchPage, RecapPage,
    HomePage, VideoDetailPage, ManageHomePage
  },
  data() {
    return {
      // 默认落在观看首页（D-UI-01、D-UI-09）；片库页仍常挂载，只是不再是默认可见页。
      currentPage: 'home',
      playbackQueueOpen: false,
      // 顶栏「片单」下拉：{ key, label, anchor }，关着为 null。
      navMenu: null,
      homeMounted: false,
      // 整页详情的参数（D-UI-05）：token 每次打开或返回都换，让详情页重新挂载。
      detailRoute: { videoId: 0, startAtMs: null, bookmark: null, token: 0 },
      // 整页详情的返回栈（D-UI-10）：[{ page, params, scrollTop }]，规则见 utils/viewerNavigation.js。
      detailReturnStack: [],
      // 版本对比播放器的全局开关（utils/versionCompare.js），同一个响应式对象。
      versionCompare: versionCompareState,
      // 图片页首次访问后就一直留在 DOM 里（v-show），这个开关只负责"第一次才挂载"。
      photosMounted: false,
      notesMounted: false,
      recapMounted: false,
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
      // 洞察页「存储体检」正在删除视频（InsightsPage 的 busy-change）。
      insightsBusy: false,
      // 命令面板「数据维护 · 某页签」带进数据维护页的请求（D-DC-01）。
      dataCareTab: null,
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
    // 下载完自动查重（D-DD-04）：窗口在前台时桌面通知不弹，这里给一条应用内提示。
    this.registerRuntimeEvent('download-duplicate-found', payload => {
      // 管理首页「最近的变化」只记本次会话收到的事件（D-UI-03），不持久化。
      recordDownloadDuplicate(payload);
      const what = payload?.kind === 'exact' ? '完全相同的文件' : '同一部的其他版本';
      notify(`下载的《${payload?.title || '视频'}》片库里已有${what}，可以到「下载」页处理。`);
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
        if (page === 'home') this.homeMounted = true;
        if (page === 'photos' || page === 'photos-manage') this.photosMounted = true;
        if (page === 'viewing-notes') this.notesMounted = true;
        if (page === 'recap') this.recapMounted = true;
        if (page === 'scenes') this.scenesMounted = true;
        if (page === 'video-workbench') this.workbenchMounted = true;
        this.navMenu = null;
        // 返回栈只在整页详情上用得到：落到任何别的页面（包括进管理区）就清空（D-UI-10）。
        // 留着的层再也回不去——别的页面没有「返回」，从那里再开一部片会重新压栈。
        if (page !== VIDEO_DETAIL_PAGE) this.detailReturnStack = [];
      }
    }
  },
  computed: {
    // 当前区域（D-UI-09）：由当前页面推出，不单独存。
    area() {
      return pageArea(this.currentPage) || 'viewer';
    },
    visibleNavGroups() {
      return (this.area === 'manage' ? MANAGE_NAV_GROUPS : VIEWER_NAV_GROUPS)
        .map(group => ({ ...group, pages: group.pages.filter(page => isNavPageVisible(page, this.settings)) }))
        .filter(group => group.pages.length > 0);
    },
    navMenuItems() {
      const group = this.visibleNavGroups.find(item => item.key === this.navMenu?.key);
      return (group?.pages || []).map(page => ({ id: page.key, label: page.label, checked: page.key === this.currentPage }));
    },
    // 「视频」与「片库管理」、「图片」与「图片管理」各是同一个常挂载实例的两个入口。
    libraryPageVisible() {
      return this.currentPage === 'videos' || this.currentPage === 'library-manage';
    },
    photoPageVisible() {
      return this.currentPage === 'photos' || this.currentPage === 'photos-manage';
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
    handleVersionCompareClose() {
      closeVersionCompare();
    },
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
      // 存储体检正在移到废纸篓：撤销条与「不支持废纸篓」二选一都挂在洞察页上，切走会丢掉它们，
      // 删除结果也就传不回片库（与图片清理页删除进行中不能离开同一做法）。
      if (this.currentPage === 'insights' && this.insightsBusy) {
        notify('正在移到废纸篓，完成后再离开洞察页。');
        return false;
      }
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
          this.commitPage(page);
          return true;
        });
      }
      this.commitPage(page);
      return true;
    },
    // 真正切页的唯一出口：顺带作废还在取视频的「打开一部片」（M-1），用户已经去了别处，取回来也不再把人拽回去。
    commitPage(page) {
      this.supersedeVideoOpen();
      // 上一页还没放回去的滚动位置不能落到新页面上。
      this._cancelScrollRestore?.();
      this._cancelScrollRestore = null;
      this._pendingDetailScrollTop = null;
      this.currentPage = page;
    },
    // 「打开一部片」的代次：每次新的打开、每次切页都换代，取完视频还是这一代才继续。
    supersedeVideoOpen() {
      this._videoOpenGeneration = (this._videoOpenGeneration || 0) + 1;
      return this._videoOpenGeneration;
    },
    async openConsolidationTask(taskID) {
      const generation = ++this.consolidationRouteGeneration;
      const navigated = await this.navigateTo('library-manage');
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
    // 命令面板「数据维护 · 某页签」：带上页签切过去；已在该页时由 token 变化触发切签。
    openDataCare(tab) {
      this.dataCareTab = { tab, token: Date.now() };
      return this.navigateTo('data-care');
    },
    // 完整度页签的「去处理」：切到片库管理或图片管理并带上「缺失某项」筛选（D-MC-03）。
    async openMissingFilter({ kind, field } = {}) {
      const page = kind === 'image' ? 'photos-manage' : 'library-manage';
      const navigated = await this.navigateTo(page);
      if (navigated === false) return false;
      await this.$nextTick();
      const target = kind === 'image' ? this.$refs.photoLibrary : this.$refs.videoListPage;
      return target?.applyMissingFilter?.(field);
    },
    // 洞察页「存储体检」删掉或撤销删除了视频：顶栏计数与片库按当前条件重取。
    // kind 为 image 时是数据维护里撤销了图片的批量编辑：只通知图片库。
    handleVideosChangedElsewhere({ removedIDs = [], kind = 'video' } = {}) {
      if (kind === 'image') return this.$refs.photoLibrary?.markImagesChanged();
      this.loadLibraryCounts();
      return this.$refs.videoListPage?.handleVideosChangedElsewhere(removedIDs);
    },
    // 工作台的「处理」：按固定命令 ID 跳到宿主页并打开既有面板（详细设计 §6.1）。
    runPendingCommand(commandID, label) {
      return Promise.resolve(this.runHostCommand(commandID, label))
        .catch(err => notifyError(`打开「${label || '待处理事项'}」失败：${err}`));
    },
    // 书签定位（观看笔记）：观看区进整页详情、从书签起点起播（D-UI-07）；其他来源沿片库页既有的书签入口开抽屉。
    async openBookmark(resolution, { from } = {}) {
      if (opensVideoDetail(from, this.currentPage)) {
        if (resolution?.status !== 'ready' || !resolution.video) return false;
        const mark = resolution.bookmark || {};
        return this.openVideoDetail(resolution.video.id, {
          startAtMs: mark.start_ms,
          bookmark: { start_ms: mark.start_ms, end_ms: mark.end_ms ?? null, source_token: resolution.source_token || '' }
        });
      }
      const generation = this._bookmarkNavigation = Symbol();
      const navigated = await this.navigateTo('library-manage');
      if (navigated === false || this._bookmarkNavigation !== generation) return;
      await this.$nextTick();
      return this.$refs.videoListPage?.openBookmark(resolution);
    },
    // 片库工具栏「在当前筛选中搜场景」：带上当前 LibraryFilter 进入场景检索页（D-MW-SCENES）。
    openScenesWithFilter(filter) {
      this.scenesScope = { filter: filter ? { ...filter } : null, token: Date.now() };
      return this.navigateTo('scenes');
    },
    // 详情抽屉「找相似画面」：带上这一刻进入场景检索页，全库范围按画面检索（以图搜片）。
    openSimilarScenes(similar) {
      if (!similar?.videoID) return false;
      this.scenesScope = { filter: null, similar: { ...similar }, token: Date.now() };
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
        const navigated = await this.navigateTo('library-manage');
        if (navigated === false) return;
        await this.$nextTick();
        this.$refs.videoListPage?.requestDeleteVideos(videos);
      } catch (err) {
        notifyError(`打开删除确认失败：${err}`);
      }
    },
    // 场景命中（从区间起点，已提前 2 秒）：观看区进整页详情并从该时刻起播；其他来源回片库管理开抽屉内嵌播放，
    // 沿用字幕命中的跳转机制（D-UI-07）。
    openVideoAt({ videoID, startMs } = {}, { from } = {}) {
      return this.openVideoResolved(videoID, '这个视频已不在片库中，可能不在扫描目录内或已被删除。', { from, startAtMs: startMs });
    },
    // 观看回顾里点标签：回片库，只按这一个标签筛选（VideoListPage.applyTagFilterCommand）。
    async openLibraryTag(tag) {
      const id = Number(tag?.id || tag);
      if (!Number.isInteger(id) || id <= 0) return;
      const navigated = await this.navigateTo('videos');
      if (navigated === false) return;
      await this.$nextTick();
      await this.$refs.videoListPage?.applyTagFilterCommand(id);
    },
    // 观看回顾里点图片：切到图片页并定位到这一张，打开查看器（PhotoLibraryPage.locateImage）。
    async openPhotoImage(image) {
      if (!Number(image?.id)) return;
      const navigated = await this.navigateTo('photos');
      if (navigated === false) return;
      await this.$nextTick();
      await this.$refs.photoLibrary?.locateImage(image);
    },
    // 观看回顾里点人物 / 作品集：沿用命令面板打开实体的入口。
    openRecapEntity({ type, id, name } = {}) {
      if (!Number(id) || (type !== 'person' && type !== 'collection')) return;
      return this.openEntityFromCommand(type, Number(id), name || '');
    },
    // 按 ID 打开一部片（任务中心产物、回顾、待播队列…）：取回视频后按调用方区域分流。
    openVideoByID(videoID, { from, startAtMs = null } = {}) {
      return this.openVideoResolved(videoID, '产物视频不在片库中，可能不在扫描目录内或已被删除。', { from, startAtMs });
    },
    // 取回视频再打开（M-1）：取的这段时间里又有新的打开或切了页，这一次就作废——只有最新的那次生效，
    // 也不会把已经去了别处（比如进了管理区）的用户拽回来。
    async openVideoResolved(videoID, missingMessage, options) {
      const generation = this.supersedeVideoOpen();
      let video;
      try {
        [video] = await GetVideosByIDs([Number(videoID)]) || [];
      } catch (err) {
        if (generation === this._videoOpenGeneration) notifyError(`打开视频失败：${err}`);
        return false;
      }
      if (generation !== this._videoOpenGeneration) return false;
      if (!video) {
        notifyError(missingMessage);
        return false;
      }
      try {
        return await this.openVideo(video, options);
      } catch (err) {
        notifyError(`打开视频失败：${err}`);
        return false;
      }
    },
    // 「打开一部片」的分流（D-UI-07）：from 是调用方的页面 key 或面板名（规则见 utils/viewerNavigation.js）。
    // 观看区的调用方进整页详情；管理区维持「切到片库管理 + 详情抽屉」。startAtMs 是起播位置（毫秒，可缺省）。
    openVideo(video, { from, startAtMs = null } = {}) {
      if (!video?.id) return false;
      if (opensVideoDetail(from, this.currentPage)) return this.openVideoDetail(video.id, { startAtMs });
      return this.openVideoInLibraryManage(video, startAtMs);
    },
    async openVideoInLibraryManage(video, startAtMs = null) {
      this.supersedeVideoOpen();
      const navigated = await this.navigateTo('library-manage');
      if (navigated === false) return false;
      await this.$nextTick();
      if (startAtMs == null) return this.$refs.videoListPage?.openPreview(video);
      return this.$refs.videoListPage?.openPreviewAt(video, startAtMs);
    },
    // 进整页详情：先记下来源（页面 key、参数、共用滚动容器的位置）再切页，切成了才压栈（D-UI-10）。
    // 详情本身是来源时（相关推荐）参数就是它展示的那部片。新打开的详情从顶部看起。
    // bookmark：观看笔记的书签定位带上整段区间 { start_ms, end_ms, source_token }，详情页据此只播这一段。
    openVideoDetail(videoID, { startAtMs = null, bookmark = null } = {}) {
      const id = Number(videoID);
      if (!Number.isInteger(id) || id <= 0) return false;
      this.supersedeVideoOpen();
      const start = Number(startAtMs);
      const source = {
        page: this.currentPage,
        params: this.currentPage === VIDEO_DETAIL_PAGE ? { videoId: this.detailRoute.videoId } : null,
        scrollTop: this.$refs.mainView?.scrollTop || 0
      };
      return whenNavigated(this.navigateTo(VIDEO_DETAIL_PAGE), () => {
        this.detailReturnStack = pushDetailReturn(this.detailReturnStack, source, id);
        this.showVideoDetail(id, {
          startAtMs: startAtMs != null && Number.isFinite(start) && start >= 0 ? start : null,
          bookmark
        });
        return true;
      });
    },
    // 换一层详情（详情页按 token 重新挂载）。scrollTop > 0 是「返回」到之前那一层：详情要重新取数，
    // 先记下来、等它报 ready 再放回去（I-2）；新打开的从顶部看起。
    showVideoDetail(videoId, { startAtMs = null, bookmark = null, scrollTop = 0 } = {}) {
      this.detailRoute = { videoId, startAtMs, bookmark, token: this.detailRoute.token + 1 };
      this._pendingDetailScrollTop = scrollTop > 0 ? scrollTop : null;
      if (this._pendingDetailScrollTop !== null) return Promise.resolve();
      return this.$nextTick(() => {
        if (this.$refs.mainView) this.$refs.mainView.scrollTop = 0;
      });
    },
    // 详情页主体取完并渲染好了（VideoDetailPage 的 ready）：有待放回的滚动位置就放回去。
    handleDetailReady() {
      const top = this._pendingDetailScrollTop;
      this._pendingDetailScrollTop = null;
      if (top !== null && top !== undefined && this.$refs.mainView) this.$refs.mainView.scrollTop = top;
    },
    // 整页详情的「返回」：弹回来源页并恢复它的滚动位置；栈空（从管理区或面板直接进来）时回观看首页。
    // 回到的页面可能是重新挂载、还在取数的（观影记录、想看、榜单、人物、作品集），等内容长够了再放回去。
    async goBackFromDetail() {
      this.supersedeVideoOpen();
      const { entry, stack } = popDetailReturn(this.detailReturnStack);
      this.detailReturnStack = stack;
      if (!entry) return this.navigateTo('home');
      if (entry.page === VIDEO_DETAIL_PAGE) return this.showVideoDetail(entry.params.videoId, { scrollTop: entry.scrollTop });
      if (this.navigateTo(entry.page) === false) return false;
      await this.$nextTick();
      if (this.$refs.mainView) this._cancelScrollRestore = restoreScrollWhenReachable(this.$refs.mainView, entry.scrollTop);
      return true;
    },
    // 整页详情里收藏、标记已看、评分、播放进度或失效标记变了（{ id, changes }）：交给常挂载的片库页，
    // 「视频」「片库管理」里那一行不用重载就跟上（M-5）。
    handleDetailVideoStateChange(payload) {
      return this.$refs.videoListPage?.applyExternalVideoStateChange(payload);
    },
    // 整页详情「编辑资料」：切到片库管理，在抽屉里打开这部片（详细设计「整页详情」）。
    editVideoInManage(videoID) {
      return this.openVideoByID(videoID, { from: 'library-manage' });
    },
    // 首页「本月常看的人物」：沿用命令面板打开实体的入口。payload 为 { id, name }。
    openPerson(person) {
      return this.openRecapEntity({ type: 'person', id: person?.id, name: person?.name });
    },
    toggleNavMenu(group, event) {
      if (this.navMenu?.key === group.key) {
        this.navMenu = null;
        return;
      }
      this.navMenu = { key: group.key, label: group.label, anchor: event?.currentTarget || null };
    },
    // 顶栏一项：页面就切页；动作（待处理、清理中心、任务与诊断）执行命令面板里同 ID 的既有命令。
    selectNavItem(item) {
      if (!item.command) return this.navigateTo(item.key);
      const command = findCommand(item.command);
      if (!command) return false;
      return Promise.resolve(command.run()).catch(err => notifyError(`打开「${item.label}」失败：${err}`));
    },
    badgeText(count) {
      const value = Number(count || 0);
      return value > 99 ? '99+' : String(value);
    },
    handleLibraryScanSummary(event) {
      recordScanSummary(event);
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
.header-left { flex: none; pointer-events: none; }
.header h1 { font-size: 15px; font-weight: 700; letter-spacing: 0.02em; color: var(--text-primary); white-space: nowrap; }
/* 顶栏项一律不折行：窗口变窄时先收紧间距与字号、再隐藏库规模计数，仍放不下时导航横向滚动，
   不能把「视频」挤成「视/频」两行。 */
.header-nav {
  display: flex;
  flex: 0 1 auto;
  min-width: 0;
  gap: 2px;
  align-items: center;
  overflow-x: auto;
  scrollbar-width: none;
  --wails-draggable: none;
}
.header-nav::-webkit-scrollbar { display: none; }

.nav-btn {
  position: relative;
  flex: none;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 32px;
  padding: 0 12px;
  white-space: nowrap;
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

/* 「片单」下拉按钮的箭头；管理侧动作项（待处理、任务与诊断）沿用右侧入口的角标与红点。 */
.nav-btn__caret { margin-left: -2px; font-size: 10px; opacity: 0.7; }

/* 顶栏分组（D-PC59、D-UI-02）：组内按钮紧挨，组间一条竖向发丝线。 */
.header-nav__group { display: flex; gap: 2px; align-items: center; }
.header-nav__divider {
  width: 1px;
  height: 18px;
  margin: 0 8px;
  background: var(--hairline);
}

/* 观看区右侧入口：待播队列、「管理」（角标 = 待处理数量，D-UI-02）。
   角标 = 数量，红点 = 有任务上一轮失败（D-PC18、D-PC27），管理侧导航的动作项也用这两个样式。 */
.header-tools {
  flex: none;
  margin-left: auto;
  display: flex;
  gap: 4px;
  align-items: center;
  --wails-draggable: none;
}
.header-tool-btn {
  position: relative;
  flex: none;
  white-space: nowrap;
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
@media (max-width: 1520px) {
  .header-counts { display: none; }
}
@media (max-width: 1360px) {
  .header { gap: 12px; }
  .nav-btn { padding: 0 8px; }
  .header-nav__divider { margin: 0 4px; }
}
@media (max-width: 1140px) {
  .nav-btn { padding: 0 6px; font-size: 12.5px; }
  .header-tool-btn { padding: 0 8px; }
}

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
