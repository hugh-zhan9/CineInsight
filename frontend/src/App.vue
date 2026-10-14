<template>
  <div id="app" class="app-shell glass-app-shell">
    <!-- 读到启动状态之前（startupChecked）外壳什么都不画，正常启动的各部分跟着 appReady 出来。 -->
    <div v-if="startupChecked" class="header">
      <div class="header-left">
        <h1>析微影策</h1>
      </div>
      <!-- 顶栏两套导航（D-UI-02、D-UI-09）：区域由当前页面推出，只显示这一侧的导航；组间分隔，
           「片单」是一个页面（D-P2-04，不再是下拉）；「下载」只在开启浏览器插件桥接时出现。页面清单与命令面板同一份
           （utils/appCommands.js 的 VIEWER_NAV_GROUPS / MANAGE_NAV_GROUPS）。管理侧带 command 的一项是动作：
           执行命令面板里同 ID 的既有命令，打开任务中心；「待处理」「清理中心」三期起是页面（D-P3-01、D-P3-02）。
           导航项是 .btn .btn--ghost，尺寸与选中态由下面的 .header .nav-btn 收窄。 -->
      <nav class="header-nav" aria-label="页面导航" :data-test="`nav-area-${area}`">
        <template v-if="area === 'manage'">
          <button type="button" class="btn btn--ghost nav-btn" data-test="nav-back-to-viewer" @click="navigateTo('home')">返回观看</button>
          <span class="header-nav__divider" aria-hidden="true"></span>
        </template>
        <template v-for="(group, index) in visibleNavGroups" :key="group.key">
          <span v-if="index > 0" class="header-nav__divider" aria-hidden="true"></span>
          <div class="header-nav__group" role="group" :aria-label="group.label" :data-test="`nav-group-${group.key}`">
            <button
              v-for="page in group.pages"
              :key="page.key"
              type="button"
              :class="['btn', 'btn--ghost', 'nav-btn', { active: currentPage === page.key }]"
              :data-test="page.command ? `open-${page.key}` : `nav-${page.key}`"
              :aria-label="page.key === 'task-center' ? `${page.label}${taskIndicatorStatus}` : undefined"
              @click="selectNavItem(page)"
            >
              {{ page.label }}
              <span v-if="page.key === 'pending-work' && pendingWorkTotal > 0" class="header-badge" data-test="pending-work-badge">{{ badgeText(pendingWorkTotal) }}</span>
              <!-- 任务指示（D-UD-11）：有任务在跑时转圈 + 数量，上一轮有失败时右上角红点；点开任务中心。 -->
              <template v-if="page.key === 'task-center'">
                <AppSpinner v-if="taskCenterBadge.running > 0" class="header-task-spinner" :size="16" data-test="task-center-spinner" />
                <AppBadge v-if="taskCenterBadge.running > 0" tone="accent" :count="taskCenterBadge.running" data-test="task-center-badge" />
                <span v-if="taskCenterBadge.failed" class="header-dot" role="img" aria-label="有任务上一轮失败" data-test="task-center-failed-dot"></span>
              </template>
            </button>
          </div>
        </template>
      </nav>
      <!-- 观看区右侧：待播队列与「管理」入口，待处理数量作为角标挂在「管理」上（D-UI-02）。 -->
      <div v-if="!startupError && area === 'viewer'" class="header-tools">
        <button type="button" class="btn btn--secondary btn--sm header-tool-btn" data-test="open-playback-queue" @click="playbackQueueOpen = true">待播队列</button>
        <button type="button" class="btn btn--secondary btn--sm header-tool-btn" data-test="open-manage" @click="navigateTo('manage-home')">
          管理
          <span v-if="pendingWorkTotal > 0" class="header-badge" data-test="manage-pending-badge">{{ badgeText(pendingWorkTotal) }}</span>
        </button>
      </div>
      <div v-if="libraryCountsText" class="header-counts">{{ libraryCountsText }}</div>
    </div>

    <WallpaperStatusBar v-if="appReady" />
    <PlaybackQueuePanel v-if="appReady" :open="playbackQueueOpen" @open="playbackQueueOpen = true" @close="playbackQueueOpen = false" @open-video="openVideoByID($event, { from: 'playback-queue' })" />

    <div v-if="startupError" class="startup-error-view">
      <!-- 回落保护（D-DBG-03..05）：上次用的是 PostgreSQL（或按数据目录推断如此）、这次没读到它的配置，没有打开本机库。
           读到保护结果之前两张卡片都不画，被拦下时不先闪一下「数据库连接失败」；其他启动错误照旧是下面那张卡片。 -->
      <div v-if="startupGuard.blocked" class="startup-error-card" data-test="startup-guard">
        <h2>没有读到 PostgreSQL 配置，已停下</h2>
        <p v-if="startupGuardInferred" class="startup-error-text" data-test="startup-guard-summary">
          数据目录里有之前使用留下的数据（缩略图、备份等），但本机 SQLite 库文件不存在，这次也没有读到 PostgreSQL 配置。看起来之前用的是 PostgreSQL，为了不打开一个空的本机库，已经停下。
        </p>
        <p v-else class="startup-error-text" data-test="startup-guard-summary">
          上次用的是 PostgreSQL{{ startupGuard.previous_location ? `（${startupGuard.previous_location}）` : '' }}，这次没有读到 PostgreSQL 配置，为了不打开一个空的本机库，已经停下。数据还在 PostgreSQL 里。
        </p>
        <AppBanner
          v-if="startupGuard.pg_host_empty_in_launch_env"
          class="startup-guard-banner"
          tone="warning"
          title="启动环境里的 PG_HOST 是空的"
          message="应用启动时就带着一个空的 PG_HOST 变量（例如用 launchctl setenv 设过），配置文件不会覆盖已有的变量，下面所有配置文件里的 PG_HOST 都被它挡住了。先去掉它（例如 launchctl unsetenv PG_HOST），再重新打开。"
          data-test="startup-guard-launch-env"
        />
        <h3 class="startup-guard-heading">这次读过的配置文件（按优先级从高到低）</h3>
        <ul class="startup-guard-files" data-test="startup-guard-config-files">
          <li v-for="file in startupGuard.config_files" :key="file.path" class="startup-guard-file" data-test="startup-guard-config-file">
            <code class="startup-guard-path">{{ file.path }}</code>
            <AppBadge :tone="file.exists ? 'success' : 'neutral'">{{ file.exists ? '存在' : '不存在' }}</AppBadge>
          </li>
        </ul>
        <h3 class="startup-guard-heading">把 PostgreSQL 配置存进数据目录</h3>
        <p class="startup-error-hint" data-test="startup-guard-data-dir-hint">
          把下面这段存成 <code class="startup-guard-path">{{ startupGuard.data_dir_config }}</code>（这个文件已经有了时，用这几行替换其中同名的行），在 PG_PASSWORD 一行填上密码{{ startupGuardInferred ? '、补全空着的几项' : '' }}，然后点「重新打开」。这个位置不随应用更新被替换。
        </p>
        <div class="startup-guard-snippet">
          <pre ref="startupGuardSnippet" class="startup-guard-snippet__text" data-test="startup-guard-snippet">{{ startupGuard.config_snippet }}</pre>
          <button type="button" class="btn btn--secondary btn--sm startup-guard-snippet__copy" data-test="startup-guard-copy" @click="copyStartupGuardSnippet">
            {{ startupGuardCopy === 'copied' ? '已复制' : '复制' }}
          </button>
        </div>
        <p v-if="startupGuardCopy === 'selected'" class="startup-error-hint" role="status" data-test="startup-guard-copy-fallback">
          没能写进剪贴板，已经选中了上面这段，按 ⌘C 复制。
        </p>
        <p class="startup-error-hint">
          也可以这次改用本机 SQLite：会打开本机数据库，它可能是空的；PostgreSQL 里的数据不受影响。
        </p>
        <p v-if="relaunchError" class="relaunch-error startup-guard-error" data-test="startup-guard-error">{{ relaunchError }}</p>
        <div class="startup-guard-actions">
          <button type="button" class="btn btn--ghost startup-guard-diagnostics" data-test="startup-open-diagnostics" @click="startupDiagnosticsOpen = true">运行状态与诊断</button>
          <button type="button" class="btn btn--secondary" :disabled="relaunching" data-test="startup-guard-use-sqlite" @click="acceptSQLiteAfterFallbackBlock">这次改用本机 SQLite…</button>
          <button type="button" class="btn btn--primary" :disabled="relaunching" data-test="startup-guard-reopen" @click="relaunchNow">
            {{ relaunching ? '正在重新打开…' : '重新打开' }}
          </button>
        </div>
      </div>
      <div v-else-if="startupGuardChecked" class="startup-error-card">
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
        <button type="button" class="btn btn--secondary" data-test="startup-open-diagnostics" @click="startupDiagnosticsOpen = true">运行状态与诊断</button>
      </div>
    </div>

    <div v-else-if="appReady" ref="mainView" class="main-view">
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
      <!-- 待处理（D-P3-01）：管理侧页面，每次进来重新读；页内处理人脸、建议作品集与本地资料更新，其余按固定命令 ID
           进专门工具（runPendingCommand）。它读到新的摘要时同步顶栏角标；本地资料应用成功后片库管理重取。 -->
      <PendingWorkPage
        v-if="currentPage === 'pending-work'"
        @navigate="navigateTo"
        @run-command="runPendingCommand"
        @open-task-center="openTaskCenter"
        @badge-change="setPendingWorkTotal"
        @videos-changed="handleVideosChangedElsewhere"
      />
      <!-- 视频海报墙（D-P2-01）：观看侧「视频」，只做浏览，筛选状态与片库管理各自独立。首次进入才挂载，之后只隐藏，
           切走再回来筛选、已加载的卡片与滚动位置都在。navigateTo('videos', { keyword | watch | tagId }) 带来的参数经 pageRequests 交给它。 -->
      <VideoBrowsePage
        v-if="videosMounted"
        ref="videoBrowsePage"
        v-show="currentPage === 'videos'"
        :page-active="currentPage === 'videos'"
        :tags="tags"
        :navigation-request="pageRequests.videos || null"
        @open-video="openVideoByID($event, { from: 'videos' })"
        @navigate="navigateTo"
      />
      <!-- 片库页常挂载（命令面板与宿主命令依赖它），只在管理侧「片库管理」上显示（二期起「视频」是上面的海报墙）。 -->
      <VideoListPage
        ref="videoListPage"
        v-show="libraryPageVisible"
        :page-active="libraryPageVisible"
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
        @open-cleanup="openCleanupCenter({ ...$event, tab: 'video', open: true })"
        @trash-restored="handleLibraryTrashRestored"
        @open-task-center="openTaskCenter"
      />

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
      <!-- 片单（D-P2-04）：想看 · 榜单 · 观影记录 · 观看笔记四个标签。首次进入才挂载，之后只隐藏（观看笔记的
           编辑与翻页状态要留着）；各标签何时挂载、何时卸载由 ListsPage 按原来四页的做法管。当前标签是页面参数
           listsTab，别名 key（watchlist 等）由 navigateTo 换成它。 -->
      <ListsPage
        v-if="listsMounted"
        v-show="currentPage === 'lists'"
        :page-active="currentPage === 'lists'"
        :tab="listsTab"
        @update:tab="navigateTo('lists', { tab: $event })"
        @navigate="navigateTo"
        @open-video="openVideo($event, { from: 'lists' })"
        @open-video-id="openVideoByID($event, { from: 'lists' })"
        @open-bookmark="openBookmark($event, { from: 'lists' })"
      />

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

      <!-- 人物与作品集观看页（D-P2-06）：只看不改，点视频进整页详情（D-UI-07），「在管理中编辑」去管理侧。
           首次进入才挂载，之后只隐藏：从整页详情「返回」时还停在刚才那个人物 / 作品集上，滚动位置也在；
           从管理侧「人物与作品集」离开后换代（entityBrowseEpoch），下次进入重新取数。 -->
      <EntityBrowsePage
        v-if="peopleMounted"
        v-show="currentPage === 'people'"
        :key="`people-${entityBrowseEpoch}`"
        kind="person"
        :page-active="currentPage === 'people'"
        :focus-entity="entityFocus.person"
        :keyword-request="entityKeywordRequests.person"
        @open-video="openVideoByID($event, { from: 'people' })"
        @play-video="openVideoByID($event.id, { from: 'people', startAtMs: $event.startAtMs })"
        @open-image="openPhotoImage"
        @edit-in-manage="openEntityInManage"
        @navigate="navigateTo"
      />
      <EntityBrowsePage
        v-if="collectionsMounted"
        v-show="currentPage === 'collections'"
        :key="`collections-${entityBrowseEpoch}`"
        kind="collection"
        :page-active="currentPage === 'collections'"
        :focus-entity="entityFocus.collection"
        :keyword-request="entityKeywordRequests.collection"
        @open-video="openVideoByID($event, { from: 'collections' })"
        @play-video="openVideoByID($event.id, { from: 'collections', startAtMs: $event.startAtMs })"
        @open-image="openPhotoImage"
        @edit-in-manage="openEntityInManage"
        @navigate="navigateTo"
      />
      <!-- 管理侧「人物与作品集」（D-P3-04）：EntityLibraryPage 是管理页，三个标签 人物 · 作品集 · 人脸审阅（新建、改名、合并、
           删除、关联、人脸审阅都在这里）。当前标签是页面参数 entitiesManageTab（navigateTo('entities-manage', { tab })）；
           focus-entity 是观看页「在管理中编辑」要打开的对象。点视频按管理区分流（切到片库管理 + 详情抽屉）。 -->
      <EntityLibraryPage
        v-if="currentPage === 'entities-manage'"
        data-test="entities-manage"
        :tab="entitiesManageTab"
        :focus-entity="entitiesManageFocus"
        @update:tab="navigateTo('entities-manage', { tab: $event })"
        @open-video="openVideo($event, { from: 'entities-manage' })"
      />
      <!-- 清理中心（D-P3-02）：视频 · 图片两个标签。首次进入才挂载，之后只隐藏：视频这一侧的审阅进度（勾选、保留项、
           已移到废纸篓的标记）在页面里，删除宿主（撤销提示、「最近操作」的撤销回调）也一直在。当前标签是页面参数
           cleanupTab；request 是「打开清理审阅」、定位同源对、打开集中整理任务这类一次性请求。删除确认框开着或删除进行中
           （cleanupBusy）不能离开这一页。删完通知片库（handleVideosChangedElsewhere）与图片库（handleCleanupImagesChanged）。 -->
      <CleanupCenterPage
        v-if="cleanupMounted"
        ref="cleanupCenter"
        v-show="currentPage === 'cleanup'"
        :page-active="currentPage === 'cleanup'"
        :tab="cleanupTab"
        :request="cleanupRequest"
        @update:tab="navigateTo('cleanup', { tab: $event })"
        @busy-change="cleanupBusy = $event"
        @videos-changed="handleVideosChangedElsewhere"
        @images-changed="handleCleanupImagesChanged"
        @navigate="navigateTo"
      />
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
	  <!-- 观看侧「图片」是照片流（D-P2-08）：只做浏览，筛选状态与图片管理各自独立。首次进入才挂载，之后只隐藏，切走再回来
	       筛选、已加载的照片与滚动位置都在。navigateTo('photos', { personId }) 带来的参数经 pageRequests 交给它；别处改了图片时
	       photoImagesVersion 递增，它下次显示时原地重取。「在图片管理中打开」进管理页定位；看图里存成功的收藏 / 评分并到管理页上。 -->
	  <PhotoBrowsePage
	    v-if="photoBrowseMounted"
	    ref="photoBrowsePage"
	    v-show="currentPage === 'photos'"
	    :page-active="currentPage === 'photos'"
	    :navigation-request="pageRequests.photos || null"
	    :images-version="photoImagesVersion"
	    @navigate="navigateTo"
	    @open-person="openPerson"
	    @open-in-manage="openImageInManage"
	    @image-changed="handleViewerImageChanged"
	  />
	  <!-- 图片管理：首次进入才挂载，之后只隐藏不卸载，切走再切回不会丢已加载的图片和滚动位置。只在「图片管理」上显示。 -->
	  <PhotoLibraryPage
	    ref="photoLibrary"
	    v-if="photosMounted"
	    v-show="photoPageVisible"
	    :page-active="photoPageVisible"
	    :settings="settings"
	    :tags="tags"
	    @open-settings="navigateTo('settings')"
	    @open-cleanup="openCleanupCenter({ tab: 'image' })"
	  />
    </div>

    <!-- 命令面板（⌘K 或 ⌘⇧P，D-UD-03）：不新增顶栏按钮，只有快捷键这一个入口 -->
    <CommandPalette
      v-if="appReady"
      :open="commandPaletteOpen"
      :open-video="openVideoFromCommand"
      :navigate="navigateTo" :open-entity="openRecapEntity" :open-video-at="openVideoAt"
      :before-run="prepareCommandRun"
      @close="closeCommandPalette"
    />

    <!-- 任务中心常挂载（关着也在），顶栏任务指示才能跟着事件走。待处理角标由 App 自己定时读（refreshPendingWorkTotal）。 -->
    <TaskCenterDrawer
      v-if="appReady"
      :open="taskCenterOpen"
      @close="taskCenterOpen = false"
      @badge-change="taskCenterBadge = $event"
      @open-video="openVideoByID($event, { from: 'task-center' })"
      @open-consolidation="openConsolidationTask"
      @open-video-edit="openVideoEditProject"
    />
    <QuitConfirmDialog />
    <!-- 版本对比播放器：任何地方调 openVersionCompare(左, 右) 就在这里打开；每次打开是一次新会话（key）。 -->
    <VersionCompareDialog
      v-if="appReady && versionCompare.open"
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
        <button type="button" class="btn btn--primary" :disabled="relaunching" data-test="relaunch-now" @click="relaunchNow">
          {{ relaunching ? '正在重启…' : '立即重启' }}
        </button>
      </div>
    </div>

    <DialogShell
      v-if="startupError && startupDiagnosticsOpen"
      title="运行状态与诊断"
      size="lg"
      close-on-overlay
      class="startup-diagnostics"
      @close="startupDiagnosticsOpen = false"
    >
      <SystemHealthPanel :actions-enabled="false" />
      <template #footer>
        <button type="button" class="btn btn--secondary" data-test="startup-close-diagnostics" @click="startupDiagnosticsOpen = false">关闭</button>
      </template>
    </DialogShell>

    <!-- 全局提示宿主：webview 的 alert/confirm 是哑的，所有错误提示和危险操作确认都走这里 -->
    <AppFeedback />
  </div>
</template>

<script>
import { GetSettings, GetAllTags, GetAllDirectories, GetStartupError, SyncScanDirectories, SyncImageDirectories, GetLibraryCounts, SetWindowForeground, GetVideosByIDs, GetDatabaseBackendStatus, GetDatabaseSwitchStatus, ListDownloadTasks, RelaunchApp, GetPendingWorkSummary, GetStartupBackendGuard, AcceptSQLiteAfterFallbackBlock } from '../wailsjs/go/main/App';
import VideoListPage from './components/VideoListPage.vue';
import SettingsPage from './components/SettingsPage.vue';
import EntityLibraryPage from './components/EntityLibraryPage.vue';
import EntityBrowsePage from './components/viewer/EntityBrowsePage.vue';
import InsightsPage from './components/InsightsPage.vue';
import DataCarePage from './components/DataCarePage.vue';
import PhotoLibraryPage from './components/PhotoLibraryPage.vue';
import DownloadsPage from './components/DownloadsPage.vue';
import RecapPage from './components/RecapPage.vue';
import ScenesPage from './components/ScenesPage.vue';
import VideoWorkbenchPage from './components/VideoWorkbenchPage.vue';
import HomePage from './components/viewer/HomePage.vue';
import VideoBrowsePage from './components/viewer/VideoBrowsePage.vue';
import PhotoBrowsePage from './components/viewer/PhotoBrowsePage.vue';
import VideoDetailPage from './components/viewer/VideoDetailPage.vue';
import ListsPage from './components/viewer/ListsPage.vue';
import ManageHomePage from './components/manage/ManageHomePage.vue';
import PendingWorkPage from './components/manage/PendingWorkPage.vue';
import CleanupCenterPage from './components/manage/CleanupCenterPage.vue';
import AppFeedback from './components/AppFeedback.vue';
import CommandPalette from './components/CommandPalette.vue';
import TaskCenterDrawer from './components/TaskCenterDrawer.vue';
import QuitConfirmDialog from './components/QuitConfirmDialog.vue';
import VersionCompareDialog from './components/VersionCompareDialog.vue';
import WallpaperStatusBar from './components/WallpaperStatusBar.vue';
import PlaybackQueuePanel from './components/PlaybackQueuePanel.vue';
import DialogShell from './components/ui/DialogShell.vue';
import AppBadge from './components/ui/AppBadge.vue';
import AppBanner from './components/ui/AppBanner.vue';
import AppSpinner from './components/ui/AppSpinner.vue';
import SystemHealthPanel from './components/SystemHealthPanel.vue';
import { runtimeEventsMixin } from './components/video-list/runtimeEvents.js';
import { logFrontend } from './utils/frontendLog.js';
import { confirmAction, notify, notifyError } from './utils/feedback.js';
import {
  appCommandsMixin, CLEANUP_BUSY_NOTICE, CLEANUP_PAGE, cleanupParamsChangePage, DEFAULT_CLEANUP_TAB, DEFAULT_LIST_TAB,
  ENTITIES_MANAGE_PAGE, isCleanupTab, isEntitiesManageTab, isListTab, isNavPageVisible, LISTS_PAGE, MANAGE_NAV_GROUPS, pageArea,
  resolvePageRoute, VIDEO_DETAIL_PAGE, VIEWER_NAV_GROUPS, whenNavigated
} from './utils/appCommands.js';
import { findCommand } from './utils/commandRegistry.js';
import { opensVideoDetail, popDetailReturn, pushDetailReturn, restoreScrollWhenReachable } from './utils/viewerNavigation.js';
import { recordDownloadDuplicate, recordScanSummary } from './utils/sessionChanges.js';
import { closeVersionCompare, versionCompareState } from './utils/versionCompare.js';

// 扫描根的身份只由路径集合决定：别名、时间戳变了不影响"扫描范围"。
function scanRootKey(dirs) {
  return (dirs || []).map(dir => String(dir?.path || '')).sort().join('\n');
}

// 顶栏「管理」与管理导航「待处理」角标的刷新周期（原待处理工作台的做法）：数量来自好几个面板背后的计数查询，
// 不跟每个进度事件走；待处理页读到新的摘要时另外同步一次。
const PENDING_WORK_REFRESH_MS = 60000;

// 浏览器插件的下载里还有在跑或排队的：「立即重启」会打断它们（m5）。
function hasActiveDownloads(tasks) {
  return (tasks || []).some(task => task?.state === 'running' || task?.state === 'queued');
}

// 启动时的回落保护结果（D-DBG-03）：{ blocked, previous_location, config_files: [{ path, exists }], data_dir_config }。
// 读不到时按没拦下处理，出错页照旧。
function startupGuardFrom(guard) {
  return {
    blocked: guard?.blocked === true,
    reason: String(guard?.reason || ''),
    previous_location: String(guard?.previous_location || ''),
    config_files: Array.isArray(guard?.config_files) ? guard.config_files : [],
    data_dir_config: String(guard?.data_dir_config || ''),
    config_snippet: String(guard?.config_snippet || ''),
    pg_host_empty_in_launch_env: guard?.pg_host_empty_in_launch_env === true
  };
}

export default {
  name: 'App',
  // 命令面板的全局快捷键与命令注册都在 appCommandsMixin 里（D-029）。
  mixins: [appCommandsMixin, runtimeEventsMixin],
  components: {
    VideoListPage, SettingsPage, EntityLibraryPage, InsightsPage, DataCarePage, PhotoLibraryPage, DownloadsPage, ListsPage,
    AppFeedback, CommandPalette, TaskCenterDrawer, PendingWorkPage, QuitConfirmDialog, VersionCompareDialog, WallpaperStatusBar,
    DialogShell, AppBadge, AppBanner, AppSpinner, SystemHealthPanel, PlaybackQueuePanel, ScenesPage, VideoWorkbenchPage, RecapPage,
    HomePage, VideoBrowsePage, PhotoBrowsePage, VideoDetailPage, ManageHomePage, EntityBrowsePage, CleanupCenterPage
  },
  data() {
    return {
      // 默认落在观看首页（D-UI-01、D-UI-09）；片库页仍常挂载，只是不再是默认可见页。
      currentPage: 'home',
      playbackQueueOpen: false,
      homeMounted: false,
      // 片单（D-P2-04）：首次进入才挂载；当前标签是这一页的参数（navigateTo('lists', { tab })），默认「想看」。
      listsMounted: false,
      listsTab: DEFAULT_LIST_TAB,
      videosMounted: false,
      // 清理中心（D-P3-02）：首次进入才挂载；当前标签是页面参数（navigateTo('cleanup', { tab })），默认「视频」；
      // cleanupRequest 是落在视频标签上的一次性请求 { token, open?, relationId?, consolidationTaskID? }；
      // cleanupBusy：清理中心正在删除（或删除确认框开着），不能离开。
      cleanupMounted: false,
      cleanupTab: DEFAULT_CLEANUP_TAB,
      cleanupRequest: null,
      cleanupBusy: false,
      // 带参数切页（navigateTo(page, params)）：{ [page]: { ...params, token } }，token 每次都换，页面据此套用一次。
      // 「视频」（{ keyword } / { watch } / { tagId }）与「图片」（{ personId, personName? }）用。
      pageRequests: {},
      // 整页详情的参数（D-UI-05）：token 每次打开或返回都换，让详情页重新挂载。
      detailRoute: { videoId: 0, startAtMs: null, bookmark: null, token: 0 },
      // 整页详情的返回栈（D-UI-10）：[{ page, params, scrollTop }]，规则见 utils/viewerNavigation.js。
      detailReturnStack: [],
      // 版本对比播放器的全局开关（utils/versionCompare.js），同一个响应式对象。
      versionCompare: versionCompareState,
      // 图片管理首次访问后就一直留在 DOM 里（v-show），这个开关只负责"第一次才挂载"；观看侧照片流同理（D-P2-08）。
      photosMounted: false,
      photoBrowseMounted: false,
      // 别处改了图片（离开图片管理、清理中心删除或撤销、数据维护撤销、标签转人物）就递增，照片流据此原地重取。
      photoImagesVersion: 0,
      // 人物、作品集观看页：首次进入才挂载，之后只隐藏（D-P2-06）；离开管理侧「人物与作品集」时换代重挂。
      peopleMounted: false,
      collectionsMounted: false,
      entityBrowseEpoch: 0,
      recapMounted: false,
      scenesMounted: false,
      // 片库带过来的场景检索范围：{ filter, token }；为空表示全部视频。
      scenesScope: null,
      // 带关键词进人物 / 作品集观看页（命令面板「查看全部」）：{ keyword, token }，离开那一页时清空。
      entityKeywordRequests: { person: null, collection: null },
      workbenchMounted: false,
      // 要视频工作台打开的项目：{ projectID, token }（片库新建、任务中心「在工作台中打开」）。
      workbenchRequest: null,
      tags: [],
      directories: [],
      startupError: '',
      // GetStartupError 读回来之前为 false：这时还不知道是正常进观看首页（影院深色）还是启动失败页（跟随系统主题），
      // 先按哪一种画，另一种情况下第一屏都会闪一下，所以外壳先不画（见 appReady）。
      startupChecked: false,
      startupDiagnosticsOpen: false,
      // 启动错误页按当前后端给提示（D-PC58）：sqlite / postgres，读不到时为空串（按 Postgres 口径提示）。
      startupBackend: '',
      // 回落保护（D-DBG-03）：被拦下时出错页换成专门说明；startupGuardChecked 之前两张卡片都不画。
      startupGuard: startupGuardFrom(null),
      startupGuardChecked: false,
      // 配置块的「复制」：'' 没点过；copied 已写进剪贴板；selected 剪贴板不可用，已选中整段让用户自己复制。
      startupGuardCopy: '',
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
      // 任务指示的数据由常挂载的任务中心报上来；待处理角标 = GetPendingWorkSummary 的总数（refreshPendingWorkTotal）。
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
    try {
      this.startupError = await GetStartupError();
    } finally {
      // 读取本身失败时照旧按正常启动画出外壳（与以前一样），错误照旧抛出。
      this.startupChecked = true;
    }
    if (this.startupError) {
      // 启动失败页跟随系统主题（读不到设置，settings.theme 停在 system）：在下一次渲染之前定好，不先按浅色画一帧。
      this.followSystemTheme();
      // 回落保护的结果先读（只读内存，不需要数据库连接）：被拦下时出错页换成专门说明。
      try {
        this.startupGuard = startupGuardFrom(await GetStartupBackendGuard());
      } catch {
        this.startupGuard = startupGuardFrom(null);
      } finally {
        this.startupGuardChecked = true;
      }
      // 后端状态只读配置、不需要数据库连接，连不上库时也能拿到。
      try {
        const status = await GetDatabaseBackendStatus();
        this.startupBackend = String(status?.backend || '');
      } catch {
        this.startupBackend = '';
      }
      return;
    }

    // 「待重启」终态的补读（I-1）：切换或切回成功之后 WebView 重载（或别的原因重新挂载）时，事件早已
    // 发过，靠 GetDatabaseSwitchStatus 把遮罩补回来。不等它：库此时被围栏挡着，下面的读取都会失败。
    this.recoverRelaunchRequired();
    await this.loadSettings();
    await this.loadDirectories();
    this.loadTags();
    this.loadLibraryCounts();
    this.refreshPendingWorkTotal();
    this._pendingWorkTimer = setInterval(() => this.refreshPendingWorkTotal(), PENDING_WORK_REFRESH_MS);
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
    
    this.followSystemTheme();

    if (this.settings.auto_scan_on_startup) {
      if (this.directories.length > 0) {
        this.incrementalScanAll('startup');
      }
      this.incrementalScanImageDirectories();
    }
  },
  beforeUnmount() {
    this.detachForegroundReporting();
    clearInterval(this._pendingWorkTimer);
    document.body.classList.remove('surface-cinema');
  },
  watch: {
    'settings.theme'() {
      this.applyTheme();
    },
    // 影院表面（D-UD-02）打在 <body> 上而不是外壳节点上：顶栏、页面、App 里的浮层与抽屉在 body 之内，
    // teleport 到 body 的对话框、确认框与浮层菜单也在，和打开它们的区域用同一个表面。
    cinemaSurface: {
      immediate: true,
      handler(on) {
        document.body.classList.toggle('surface-cinema', on);
      }
    },
    // 「下载」页只在开启浏览器插件桥接时出现在顶栏与命令面板（D-PC59）。
    'settings.browser_bridge_enabled'() {
      this.registerAppCommands();
    },
    currentPage: {
      immediate: true,
      handler(page, previous) {
        const leftEntityKind = { people: 'person', collections: 'collection' }[previous];
        if (leftEntityKind && this.entityKeywordRequests[leftEntityKind]) {
          this.entityKeywordRequests = { ...this.entityKeywordRequests, [leftEntityKind]: null };
        }
        // 从管理侧「人物与作品集」离开：那里可能改过、删过、合并过人物或作品集，观看侧两页卸掉，下次进入重新取数。
        if (previous === 'entities-manage') {
          this.peopleMounted = false;
          this.collectionsMounted = false;
          this.entityBrowseEpoch += 1;
        }
        if (page === 'home') this.homeMounted = true;
        if (page === 'videos') this.videosMounted = true;
        // 图片管理里可能删了、收藏了、扫描进来或打了人物：离开时让照片流下次显示时原地重取（D-P2-08）。
        if (previous === 'photos-manage') this.markViewerImagesChanged();
        if (page === 'photos') this.photoBrowseMounted = true;
        if (page === 'photos-manage') this.photosMounted = true;
        if (page === LISTS_PAGE) this.listsMounted = true;
        if (page === CLEANUP_PAGE) this.cleanupMounted = true;
        if (page === 'people') this.peopleMounted = true;
        if (page === 'collections') this.collectionsMounted = true;
        if (page === 'recap') this.recapMounted = true;
        if (page === 'scenes') this.scenesMounted = true;
        if (page === 'video-workbench') this.workbenchMounted = true;
        // 返回栈只在整页详情上用得到：落到任何别的页面（包括进管理区）就清空（D-UI-10）。
        // 留着的层再也回不去——别的页面没有「返回」，从那里再开一部片会重新压栈。
        if (page !== VIDEO_DETAIL_PAGE) this.detailReturnStack = [];
      }
    }
  },
  computed: {
    // 回落保护按数据目录推断拦下（还没有记录，D-DBG-02 首次运行缺口）：说不出上次的位置，配置块留空。
    startupGuardInferred() {
      return this.startupGuard.reason === 'inferred_postgres';
    },
    // 任务指示的读屏文字（接在导航名后面）：按钮上只看得到转圈、数字和红点。
    taskIndicatorStatus() {
      const { running, failed } = this.taskCenterBadge;
      const parts = [];
      if (running > 0) parts.push(`${running} 个任务运行中`);
      if (failed) parts.push('有任务上一轮失败');
      return parts.map(part => `，${part}`).join('');
    },
    // 当前区域（D-UI-09）：由当前页面推出，不单独存。
    area() {
      return pageArea(this.currentPage) || 'viewer';
    },
    // 读到启动状态、且不是启动失败时才画正常的外壳与页面（startupChecked）。
    appReady() {
      return this.startupChecked && !this.startupError;
    },
    // 观看区（含「视频」「图片」与整页详情）不论系统深浅都用影院深色，管理区跟随系统主题（D-UD-02）。
    // 启动失败页不属于哪个区，跟随系统主题；读到启动状态之前也不开，那时只画底色（与系统一致，见 index.html）。
    cinemaSurface() {
      return this.appReady && this.area === 'viewer';
    },
    visibleNavGroups() {
      return (this.area === 'manage' ? MANAGE_NAV_GROUPS : VIEWER_NAV_GROUPS)
        .map(group => ({ ...group, pages: group.pages.filter(page => isNavPageVisible(page, this.settings)) }))
        .filter(group => group.pages.length > 0);
    },
    // 片库页（VideoListPage）常挂载，只在「片库管理」上显示；「视频」是海报墙（D-P2-01）。
    // 图片管理（PhotoLibraryPage）只在「图片管理」上显示；「图片」是照片流（D-P2-08）。
    libraryPageVisible() {
      return this.currentPage === 'library-manage';
    },
    photoPageVisible() {
      return this.currentPage === 'photos-manage';
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
      await this.runRelaunch(RelaunchApp);
    },
    // 回落保护出错页的「这次改用本机 SQLite…」（D-DBG-03）：二次确认后由后端把记录改成 sqlite 再重启，
    // 不写任何配置文件；取消什么都不做。
    async acceptSQLiteAfterFallbackBlock() {
      if (this.relaunching) return;
      const confirmed = await confirmAction({
        title: '这次改用本机 SQLite？',
        message: '会打开本机 SQLite 数据库，它可能是空的。PostgreSQL 里的数据不受影响；之后想回到 PostgreSQL，把配置放回去再重新打开应用即可。',
        confirmText: '改用本机 SQLite'
      });
      if (!confirmed) return;
      await this.runRelaunch(AcceptSQLiteAfterFallbackBlock);
    },
    // 回落保护出错页的配置块「复制」（D-DBG-05）：写剪贴板失败（WebView 没给权限等）时选中整段，让用户按 ⌘C。
    async copyStartupGuardSnippet() {
      this.startupGuardCopy = '';
      try {
        await navigator.clipboard.writeText(this.startupGuard.config_snippet);
        this.startupGuardCopy = 'copied';
      } catch {
        const element = this.$refs.startupGuardSnippet;
        const selection = window.getSelection?.();
        if (element && selection) {
          const range = document.createRange();
          range.selectNodeContents(element);
          selection.removeAllRanges();
          selection.addRange(range);
        }
        this.startupGuardCopy = 'selected';
      }
    },
    // 调一个以重启收尾的绑定（RelaunchApp、AcceptSQLiteAfterFallbackBlock）：没能自动重启时把原因留在 relaunchError。
    async runRelaunch(call) {
      if (this.relaunching) return;
      this.relaunching = true;
      this.relaunchError = '';
      try {
        const result = await call();
        if (!result?.relaunched) {
          this.relaunchError = result?.message || '无法自动重启，请手动退出并重新打开应用。';
          this.relaunching = false;
        }
      } catch (error) {
        this.relaunchError = String(error?.message || error || '') || '无法自动重启，请手动退出并重新打开应用。';
        this.relaunching = false;
      }
    },
    // 所有切页都走这里（顶栏、命令面板、待处理页、图片页的「去设置」）。设置页有未保存修改时
    // 先确认「放弃未保存的设置修改？」（D-PC57、APP-08）：SettingsPage 是 v-if，切走即销毁表单。
    // 能直接切时同步返回 true；要确认时返回 Promise，用户选继续编辑则 resolve 为 false。
    // params 是交给目标页的一次性参数（见 applyPageParams）；片单的四个别名 key 先换成 `lists` + 标签（D-P2-04），
    // 已在目标页时也照样交过去（片单只换标签）。
    navigateTo(target, targetParams = null) {
      const { page, params } = resolvePageRoute(target, targetParams);
      if (!page) return true;
      if (page === this.currentPage) {
        // 清理中心删除流程中换标签或接新请求（命令面板「清理中心 · 视频 / 图片」、打开集中整理任务……）与离开同样拦下：
        // 另一个标签一挂上 / 卸下会打断正在进行的删除（D-P3-02，见 CLEANUP_BUSY_NOTICE）。
        if (page === CLEANUP_PAGE && this.cleanupBusy && cleanupParamsChangePage(params, this.cleanupTab)) {
          notify(CLEANUP_BUSY_NOTICE);
          return false;
        }
        this.applyPageParams(page, params);
        return true;
      }
      // 存储体检正在移到废纸篓：撤销条与「不支持废纸篓」二选一都挂在洞察页上，切走会丢掉它们，
      // 删除结果也就传不回片库（与图片清理页删除进行中不能离开同一做法）。
      if (this.currentPage === 'insights' && this.insightsBusy) {
        notify('正在移到废纸篓，完成后再离开洞察页。');
        return false;
      }
      // 清理中心同理（D-P3-02）：删除进度、「不支持废纸篓」二选一与删除确认框都在那一页上。
      if (this.currentPage === CLEANUP_PAGE && this.cleanupBusy) {
        notify(CLEANUP_BUSY_NOTICE);
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
          this.commitPage(page, params);
          return true;
        });
      }
      this.commitPage(page, params);
      return true;
    },
    // 真正切页的唯一出口：顺带作废还在取视频的「打开一部片」（M-1），用户已经去了别处，取回来也不再把人拽回去。
    commitPage(page, params = null) {
      this.supersedeVideoOpen();
      // 上一页还没放回去的滚动位置不能落到新页面上。
      this._cancelScrollRestore?.();
      this._cancelScrollRestore = null;
      this._pendingDetailScrollTop = null;
      this.applyPageParams(page, params);
      this.currentPage = page;
    },
    // 页面参数落到页面状态上：
    // - 「视频」的 { keyword } / { watch } / { tagId }：经 pageRequests 交给海报墙，每次换 token，页面据此套用一次；
    // - 「图片」的 { personId, personName? }（人物页「去图片」）：同样经 pageRequests 交给照片流；
    // - 片单的标签（认不出的标签忽略，留在原来的标签上）；
    // - 人物 / 作品集的 { keyword }：页面回到列表并按它搜（每次交接换 token，同一个词再交一次也会重搜）；
    // - 场景检索的 { query, mode } 或 { image: { dataURL, name } }：范围是全部视频，页面直接检索（ScenesPage 的 scopeRequest）；
    // - 清理中心的 { tab }（认不出的忽略）与落在视频标签上的请求 { open | relationId | consolidationTaskID }；
    // - 人物与作品集的 { tab }（人物 · 作品集 · 人脸审阅，认不出的忽略）。
    applyPageParams(page, params) {
      if (page === 'videos' || page === 'photos') this.setPageRequest(page, params);
      if (page === LISTS_PAGE && isListTab(params?.tab)) this.listsTab = params.tab;
      if (page === CLEANUP_PAGE) this.applyCleanupParams(params);
      if (page === ENTITIES_MANAGE_PAGE && isEntitiesManageTab(params?.tab)) this.entitiesManageTab = params.tab;
      const entityKind = { people: 'person', collections: 'collection' }[page];
      if (entityKind && typeof params?.keyword === 'string') this.requestEntityKeyword(entityKind, params.keyword);
      if (page === 'scenes' && params && (typeof params.query === 'string' || params.mode || params.image?.dataURL)) {
        this._handOffSeq = (this._handOffSeq || 0) + 1;
        this.scenesScope = { filter: null, ...params, token: this._handOffSeq };
      }
    },
    // 关键词请求：页面已挂载时由它的监听接住（每次是新对象），首次挂载时在挂载那一拍读到。离开这一页时清空
    // （currentPage 的监听），免得之后重新挂载（离开管理侧之后）把旧关键词再搜一遍。
    requestEntityKeyword(kind, keyword) {
      this._handOffSeq = (this._handOffSeq || 0) + 1;
      this.entityKeywordRequests = { ...this.entityKeywordRequests, [kind]: { keyword, token: this._handOffSeq } };
    },
    // 当前页面的参数（整页详情返回栈记下来源时用）。
    currentPageParams() {
      if (this.currentPage === VIDEO_DETAIL_PAGE) return { videoId: this.detailRoute.videoId };
      if (this.currentPage === LISTS_PAGE) return { tab: this.listsTab };
      return null;
    },
    // 带参数切页的「一次性请求」（「视频」D-P2-01、「图片」D-P2-08）：{ [page]: { ...params, token } }。
    setPageRequest(page, params) {
      if (!params || typeof params !== 'object') return;
      const token = (this.pageRequests[page]?.token || 0) + 1;
      this.pageRequests = { ...this.pageRequests, [page]: { ...params, token } };
    },
    // 「打开一部片」的代次：每次新的打开、每次切页都换代，取完视频还是这一代才继续。
    supersedeVideoOpen() {
      this._videoOpenGeneration = (this._videoOpenGeneration || 0) + 1;
      return this._videoOpenGeneration;
    },
    applyCleanupParams(params) {
      if (isCleanupTab(params?.tab)) this.cleanupTab = params.tab;
      const relationId = Number(params?.relationId || 0);
      const consolidationTaskID = Number(params?.consolidationTaskID || 0);
      if (!params?.open && !relationId && !consolidationTaskID) return;
      this._handOffSeq = (this._handOffSeq || 0) + 1;
      this.cleanupRequest = { open: Boolean(params.open), relationId, consolidationTaskID, token: this._handOffSeq };
    },
    // 片库管理与图片管理里的「清理审阅」入口、AI 审阅的「去清理」：进清理中心的对应标签（D-P3-02）。
    openCleanupCenter(params = {}) {
      return this.navigateTo(CLEANUP_PAGE, params);
    },
    // 任务中心打开集中整理任务：进清理中心的视频标签打开那个任务（原先是片库页里的清理面板）。
    // 参数随切页一起落下，用户在切页确认里放弃时什么也不发生。
    openConsolidationTask(taskID) {
      return whenNavigated(this.navigateTo(CLEANUP_PAGE, { tab: 'video', consolidationTaskID: Number(taskID) }), () => {
        this.taskCenterOpen = false;
        return true;
      });
    },
    // 清理中心的图片标签删除或撤销删除了图片：顶栏计数重取，图片管理重载（原来清理页 @deleted 的那一条路），照片流下次显示时原地重取。
    handleCleanupImagesChanged() {
      this.loadLibraryCounts();
      this.markViewerImagesChanged();
      return this.$refs.photoLibrary?.handleCleanupDeleted();
    },
    // 别处改了图片：照片流（D-P2-08）下次显示时按同一条件原地重取，滚动位置不动；还没挂载过的不用管（进来就是新取的）。
    markViewerImagesChanged() {
      this.photoImagesVersion += 1;
    },
    // 照片流看图里存成功的收藏 / 评分（{ id, ...fields }）：并到图片管理已加载的那一张上，没挂载、没加载到就不管
    // （两页原来是同一个实例，改了这边那边当场就是新的）。
    handleViewerImageChanged(fields) {
      return this.$refs.photoLibrary?.patchImage(fields);
    },
    // 片库管理的回收站恢复了视频：清理中心视频审阅里的「已移到废纸篓」标记跟着放掉（原来同在片库页里）。
    handleLibraryTrashRestored({ videoIDs = [], fromTrashDialog = false } = {}) {
      return this.$refs.cleanupCenter?.handleVideosRestored(videoIDs, fromTrashDialog);
    },
    openTaskCenter() {
      this.taskCenterOpen = true;
    },
    // 命令 action:pending-work、管理首页的待处理卡片：打开待处理页（D-P3-01，原先是弹窗）。任务中心开着时先收起。
    openPendingWork() {
      this.taskCenterOpen = false;
      return this.navigateTo('pending-work');
    },
    // 顶栏待处理角标：读失败（数据库维护、待重启……）时清零，与原工作台一致。待处理页读到的摘要更新，
    // 用 setPendingWorkTotal 交过来，同时作废路上那次定时读取（晚到的旧数不覆盖新数）。
    async refreshPendingWorkTotal() {
      const seq = (this._pendingWorkSeq || 0) + 1;
      this._pendingWorkSeq = seq;
      try {
        const summary = await GetPendingWorkSummary();
        if (seq === this._pendingWorkSeq) this.pendingWorkTotal = Number(summary?.total || 0);
      } catch (err) {
        if (seq !== this._pendingWorkSeq) return;
        this.pendingWorkTotal = 0;
        this.debugLog('GetPendingWorkSummary failed', { err: String(err) }, true);
      }
    },
    setPendingWorkTotal(total) {
      this._pendingWorkSeq = (this._pendingWorkSeq || 0) + 1;
      this.pendingWorkTotal = Number(total || 0);
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
      if (kind === 'image') {
        this.markViewerImagesChanged();
        return this.$refs.photoLibrary?.markImagesChanged();
      }
      this.loadLibraryCounts();
      return this.$refs.videoListPage?.handleVideosChangedElsewhere(removedIDs);
    },
    // 待处理页、管理首页等的「进入专门工具」：按固定命令 ID 跳到宿主页并打开既有面板（详细设计 §6.1）。
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
    // 观看回顾、整页详情里点标签：去「视频」海报墙，只按这一个标签筛选（VideoBrowsePage 的 navigationRequest）。
    openLibraryTag(tag) {
      const id = Number(tag?.id || tag);
      if (!Number.isInteger(id) || id <= 0) return false;
      return this.navigateTo('videos', { tagId: id });
    },
    // 观看回顾、人物 / 作品集观看页里点图片：切到观看侧照片流并打开这一张（PhotoBrowsePage.locateImage，D-P2-08）。
    async openPhotoImage(image) {
      if (!Number(image?.id)) return;
      const navigated = await this.navigateTo('photos');
      if (navigated === false) return;
      await this.$nextTick();
      await this.$refs.photoBrowsePage?.locateImage(image);
    },
    // 照片流看图的「在图片管理中打开」：切到图片管理并定位到这一张，打开它的查看器（PhotoLibraryPage.locateImage）。
    async openImageInManage(image) {
      if (!Number(image?.id)) return;
      const navigated = await this.navigateTo('photos-manage');
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
        params: this.currentPageParams(),
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
    // 回到的页面可能是重新挂载、还在取数的（观影记录、想看、榜单），等内容长够了再放回去。
    async goBackFromDetail() {
      this.supersedeVideoOpen();
      const { entry, stack } = popDetailReturn(this.detailReturnStack);
      this.detailReturnStack = stack;
      if (!entry) return this.navigateTo('home');
      if (entry.page === VIDEO_DETAIL_PAGE) return this.showVideoDetail(entry.params.videoId, { scrollTop: entry.scrollTop });
      if (this.navigateTo(entry.page, entry.params) === false) return false;
      await this.$nextTick();
      if (this.$refs.mainView) this._cancelScrollRestore = restoreScrollWhenReachable(this.$refs.mainView, entry.scrollTop);
      return true;
    },
    // 整页详情里收藏、标记已看、评分、播放进度或失效标记变了（{ id, changes }）：交给常挂载的片库页与海报墙，
    // 「片库管理」里那一行、「视频」上那张卡不用重载就跟上（M-5）。
    handleDetailVideoStateChange(payload) {
      this.$refs.videoBrowsePage?.applyExternalVideoStateChange(payload);
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
    // 顶栏一项：页面就切页；动作（任务中心）执行命令面板里同 ID 的既有命令。
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
      this.markViewerImagesChanged();
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
    // 读一次系统深浅并套用，之后随系统切换（settings.theme 为 system 时才看得出）。正常启动与启动失败页都走这里。
    followSystemTheme() {
      const mediaQuery = window.matchMedia('(prefers-color-scheme: dark)');
      this.systemTheme = mediaQuery.matches ? 'dark' : 'light';
      this.applyTheme();
      mediaQuery.addEventListener('change', e => {
        this.systemTheme = e.matches ? 'dark' : 'light';
        this.applyTheme();
      });
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

<style>
/* --- Header ---
   原型 A1：52px 通栏、不透明面板、底部一条发丝线；导航是 32px 胶囊，
   选中态直接填主色，不再靠投影和浅底区分。（不再挂 .glass-surface：它在 components.css 里排在后面，
   会把四边描上发丝线、把底色换成 --panel-bg。） */
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
  z-index: var(--z-sticky);
  --wails-draggable: drag;
}
.header-left { flex: none; pointer-events: none; }
.header h1 { font-size: var(--fs-section); font-weight: 700; letter-spacing: 0.02em; color: var(--text-primary); white-space: nowrap; }
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
}
/* 导航项在 .btn .btn--ghost 的基础上收窄：32px 高、无边框、中等字重；悬停与选中压过 .btn--ghost 的悬停
   （components.css 在构建产物里排在组件样式后面，这里多一层 .header 才生效）。 */
.header .nav-btn { height: 32px; padding: 0 12px; border: 0; font-size: var(--fs-secondary); font-weight: var(--fw-medium); }
.header .nav-btn:hover:not(:disabled) { color: var(--text-primary); background: var(--panel-muted-bg); }
.header .nav-btn.active,
.header .nav-btn.active:hover:not(:disabled) { color: var(--accent-on); background: var(--accent-color); font-weight: var(--fw-semibold); }

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
}
.header-badge {
  min-width: 18px;
  padding: 0 5px;
  border-radius: 9px;
  background: var(--accent-color);
  color: var(--accent-on);
  font-family: var(--font-mono);
  font-size: var(--fs-caption);
  line-height: 18px;
  text-align: center;
}
/* 「待处理」三期起是页面（D-P3-01），选中时整个按钮是强调色：角标反色，免得与底色混在一起。 */
.nav-btn.active .header-badge { background: var(--accent-on); color: var(--accent-color); }
.header-task-spinner { color: var(--accent-text); }
.nav-btn.active .header-task-spinner { color: inherit; }
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
  font-size: var(--fs-caption);
  white-space: nowrap;
  --wails-draggable: none;
}
.header-tools + .header-counts { margin-left: 0; }
@media (max-width: 1520px) {
  .header-counts { display: none; }
}
@media (max-width: 1360px) {
  .header { gap: 12px; }
  .header .nav-btn { padding: 0 8px; }
  .header-nav__divider { margin: 0 4px; }
}
@media (max-width: 1140px) {
  .header .nav-btn { padding: 0 6px; font-size: var(--fs-caption); }
  .header .header-tool-btn { padding: 0 8px; }
}

.main-view { flex: 1 1 auto; min-height: 0; overflow-y: auto; overscroll-behavior: contain; display: flex; flex-direction: column; }
.relaunch-overlay {
  position: fixed;
  inset: 0;
  z-index: var(--z-critical);
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
  background: var(--overlay-bg);
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
  font-size: var(--fs-section);
  margin-bottom: 10px;
  color: var(--text-primary);
}
.relaunch-text {
  font-size: var(--fs-body);
  line-height: 1.7;
  color: var(--text-primary);
  margin-bottom: 16px;
}
.relaunch-error {
  font-size: var(--fs-secondary);
  color: var(--danger-color);
  margin-bottom: 12px;
}
.relaunch-note {
  font-size: var(--fs-secondary);
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
  font-size: var(--fs-title);
  margin-bottom: 12px;
  color: var(--danger-color);
}
.startup-error-text {
  font-size: var(--fs-body);
  line-height: 1.7;
  color: var(--text-primary);
  word-break: break-word;
}
.startup-error-hint {
  margin-top: 14px;
  font-size: var(--fs-secondary);
  line-height: 1.6;
  color: var(--text-secondary);
}
/* 回落保护的专门说明（D-DBG-03）：读过的配置文件逐行列出，路径等宽、可整段换行。 */
.startup-guard-heading {
  margin-top: var(--space-5);
  font-size: var(--fs-section);
  font-weight: var(--fw-semibold);
  color: var(--text-primary);
}
.startup-guard-files {
  list-style: none;
  margin: var(--space-2) 0 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}
.startup-guard-file {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-3);
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--border-color);
  border-radius: var(--radius-sm);
  background: var(--surface-faint);
}
.startup-guard-path {
  font-family: var(--font-mono);
  font-size: var(--fs-caption);
  color: var(--text-primary);
  word-break: break-all;
}
.startup-guard-error {
  margin-top: var(--space-3);
  margin-bottom: 0;
}
.startup-guard-actions {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: var(--space-2);
  margin-top: var(--space-5);
}
.startup-guard-diagnostics {
  margin-right: auto;
}
.startup-guard-banner {
  margin-top: var(--space-4);
}
.startup-guard-snippet {
  position: relative;
  margin-top: var(--space-3);
}
.startup-guard-snippet__text {
  margin: 0;
  padding: var(--space-3) var(--space-4);
  padding-right: 88px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius-sm);
  background: var(--surface-faint);
  font-family: var(--font-mono);
  font-size: var(--fs-caption);
  line-height: 1.7;
  color: var(--text-primary);
  white-space: pre-wrap;
  word-break: break-all;
  user-select: text;
}
.startup-guard-snippet__copy {
  position: absolute;
  top: var(--space-2);
  right: var(--space-2);
}

</style>
