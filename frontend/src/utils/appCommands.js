// App.vue 的命令面板接线（D-029、D-PC60）：全局 ⌘⇧P / Ctrl+Shift+P 与 ⌘K、面板开关、导航组与
// 应用级动作组、以及跟着事件刷新的任务组；另外收口顶栏的信息架构（D-PC59）与「按固定命令 ID
// 跳到宿主页」（待处理工作台与全局命令共用，详细设计 §6.1）。
//
// 做成 mixin 而不是直接写在 App.vue 里：App.vue 是多个切片共同修改的文件，
// 命令面板只在那里留最小的挂载点（mixins、一个组件标签、两个 ref）。
// 因此这里依赖 App.vue 的状态、ref 与方法：
//   this.currentPage / this.startupError / this.settings（可缺省）
//   this.$refs.videoListPage（常挂载，v-show 切换）/ this.$refs.settingsPage（v-if）
//   this.navigateTo(page)：切页。能直接切时同步返回 true；设置页有未保存修改时返回 Promise，
//     用户放弃切页时 resolve 为 false（见下面的 whenNavigated）
//   this.openTaskCenter() / this.openPendingWork()：打开任务中心抽屉与待处理工作台
//   this.openVideo(video, { from })：打开一部片，按调用方区域分流到整页详情或片库管理的抽屉（D-UI-07）
// 以及 App.vue 的 debugLog()。

import {
  CreateDatabaseBackup, GetBackgroundTasks, GetIdleSchedulerStatus, ListCollections, ListPeople, ListSavedLibraryViews,
  SyncImageDirectories
} from '../../wailsjs/go/main/App';
import { SETTINGS_SECTIONS } from '../components/SettingsPage.vue';
import { findCommand, isCommandEnabled, registerCommands, unregisterCommands } from './commandRegistry.js';
import { notifyError } from '../utils/feedback.js';
import { buildTaskCommands } from './taskCommands.js';

// 观看与管理两套导航（D-UI-02、D-UI-09）：key 与 App.vue 的 currentPage 一致，每个页面 key 只属于一个区域；
// 顶栏只显示当前区域那一套，命令面板列出两边全部页面（APP-12、APP-14：两边读同一份清单，不会各自漏页）。
// 「视频」与「片库管理」是同一个常挂载片库页的两个 key，「图片」与「图片管理」同理，key 只决定顶栏归属。
// requiresBrowserBridge：只在设置里开启了浏览器插件桥接时显示（「下载」页）。
// menu：这一组在顶栏收成一个下拉按钮（「片单」）。
export const VIEWER_NAV_GROUPS = [
  {
    key: 'browse',
    label: '浏览',
    pages: [
      { key: 'home', label: '首页', keywords: ['home', 'shouye', '继续观看', '今晚看什么', '最近添加'] },
      { key: 'videos', label: '视频', keywords: ['video', 'library', 'shipin'] },
      { key: 'people', label: '人物', keywords: ['person', 'people', 'renwu'] },
      { key: 'collections', label: '作品集', keywords: ['collection', 'zuopinji'] },
      { key: 'photos', label: '图片', keywords: ['photo', 'image', 'tupian'] }
    ]
  },
  {
    key: 'lists',
    label: '片单',
    menu: true,
    pages: [
      { key: 'watchlist', label: '想看', keywords: ['watchlist', 'xiangkan', '片单', '待下载'] },
      { key: 'movie-chart', label: '榜单', keywords: ['chart', 'movie chart', 'bangdan', '年度', '豆瓣'] },
      // 顶栏「已看」改名「观影记录」（D-PC52），与片库的「已看」智能视图区分开；旧说法留作关键词。
      { key: 'watched-movies', label: '观影记录', keywords: ['watched', 'guanying', 'yikan', 'kanguo', '看过', '已看', '榜单已看'] },
      { key: 'viewing-notes', label: '观看笔记', keywords: ['diary', 'bookmarks', '日记', '片段书签', '年度回顾'] }
    ]
  },
  {
    key: 'review',
    label: '回顾与检索',
    pages: [
      { key: 'recap', label: '回顾', keywords: ['recap', 'huigu', '月度回顾', '年度回顾', '实时', '最爱', '播放次数', '称号'] },
      { key: 'scenes', label: '场景检索', keywords: ['scene', 'search', 'changjing', '对白', '画面', '台词'] }
    ]
  }
];

export const SETTINGS_NAV_PAGE = { key: 'settings', label: '设置', keywords: ['settings', 'shezhi'] };

// 管理侧（D-UI-02、D-UI-03）：一期沿用现有页面与面板。带 command 的三项（待处理、清理中心、任务与诊断）
// 是动作不是页面：点了执行命令面板里同 ID 的既有命令，打开现有弹窗或面板。
export const MANAGE_NAV_GROUPS = [
  {
    key: 'manage-library',
    label: '片库',
    pages: [
      { key: 'manage-home', label: '管理首页', keywords: ['manage', 'guanli', '管理', '统计', '最近的变化'] },
      { key: 'library-manage', label: '片库管理', keywords: ['library manage', 'pianku', '片库', '扫描', '批量'] },
      { key: 'photos-manage', label: '图片管理', keywords: ['photo manage', 'tupian guanli', '图片库', '扫描图片'] }
    ]
  },
  {
    key: 'manage-review',
    label: '待处理与清理',
    pages: [
      { key: 'pending-work', label: '待处理', command: 'action:pending-work' },
      { key: 'cleanup', label: '清理中心', command: 'action:cleanup' }
    ]
  },
  {
    key: 'manage-tools',
    label: '工具',
    pages: [
      { key: 'downloads', label: '下载', keywords: ['download', 'xiazai', '插件', '浏览器'], requiresBrowserBridge: true },
      { key: 'video-workbench', label: '视频工作台', keywords: ['workbench', 'edit', 'merge', 'trim', 'gongzuotai', '合并', '去片头', '高清替换', '剪辑'] },
      { key: 'insights', label: '洞察', keywords: ['insights', 'stats', 'dongcha'] },
      // 数据维护（D-DC-01）：失联 · 完整度 · 规则 · 体检。
      { key: 'data-care', label: '数据维护', keywords: ['data care', 'shuju', 'weihu', '失联', '完整度', '规则', '体检', '批量编辑'] }
    ]
  },
  {
    key: 'manage-system',
    label: '系统',
    pages: [
      { key: 'task-center', label: '任务与诊断', command: 'action:task-center' },
      SETTINGS_NAV_PAGE
    ]
  }
];

// 整页详情（D-UI-05）：观看区的页面，但要带视频 ID，不在顶栏与命令面板里。
export const VIDEO_DETAIL_PAGE = 'video-detail';

// 顶栏一组里真正的页面（去掉动作入口）。
function navPages(groups) {
  return groups.flatMap(group => group.pages).filter(page => !page.command);
}

const PAGE_AREAS = new Map([
  ...navPages(VIEWER_NAV_GROUPS).map(page => [page.key, 'viewer']),
  [VIDEO_DETAIL_PAGE, 'viewer'],
  ...navPages(MANAGE_NAV_GROUPS).map(page => [page.key, 'manage'])
]);

// 「打开一部片」的调用方标记里代表命令面板的那一个（utils/viewerNavigation.js 据此分流）。
export const COMMAND_PALETTE_SOURCE = 'command-palette';

// 页面所在的区域：'viewer' | 'manage'；不认识的 key 返回 null。App.vue 的 area 由当前页面推出，不单独存。
export function pageArea(page) {
  return PAGE_AREAS.get(page) || null;
}

// 数据维护四个页签的直达命令（D-DC-01）。
export const DATA_CARE_COMMANDS = [
  { tab: 'lost', label: '失联', keywords: ['lost', 'missing', '失联', '找回', '清除', '孤儿文件'] },
  { tab: 'completeness', label: '完整度', keywords: ['completeness', 'metadata', '完整度', '缺失', '元数据'] },
  { tab: 'rules', label: '规则', keywords: ['rules', 'automation', '规则', '自动'] },
  { tab: 'health', label: '体检', keywords: ['health', 'check', '体检', '一致性'] }
];

// 页面导航的全集：两个区域的全部页面（含设置），先观看后管理。
const COMMAND_PAGES = [...navPages(VIEWER_NAV_GROUPS), ...navPages(MANAGE_NAV_GROUPS)];

export function isNavPageVisible(page, settings) {
  return !page?.requiresBrowserBridge || Boolean(settings?.browser_bridge_enabled);
}

// 与 video-list/LibraryToolbar.vue 的 smartViewOptions 一一对应（那份是真值来源）。
// appCommands.test.js 有一条防漂移断言比对两处。
const COMMAND_SMART_VIEWS = [
  { value: '', label: '全部视频' },
  { value: 'continue_watching', label: '继续观看' },
  { value: 'favorites', label: '收藏' },
  { value: 'liked', label: '点赞' },
  { value: 'recently_played', label: '最近播放' },
  { value: 'unwatched', label: '未看' },
  { value: 'watched', label: '已看' },
  { value: 'recently_added', label: '最近添加' },
  { value: 'untagged', label: '未打标签' },
  { value: 'no_subtitle', label: '无字幕' },
  { value: 'local_metadata_updated', label: '本地资料有更新' },
  { value: 'stale', label: '路径失效' }
];

// 固定命令 ID → 宿主页（详细设计 §6.1）。命令由宿主页（或它的常挂载子组件）注册；宿主页可能
// 还没挂载（人物页、图片页按需挂载），所以跳转前先切到宿主页、等它挂载完再按 ID 执行。
// 片库与图片库的这些命令都是维护动作，宿主页记管理侧的 key（D-UI-03）：同一个常挂载实例，切过去即进管理区。
export const COMMAND_HOST_PAGES = {
  'library.openCleanup': 'library-manage',
  'library.openAIReview': 'library-manage',
  'library.openLocalMetadataUpdates': 'library-manage',
  'library.openCollectionSuggestions': 'library-manage',
  'library.openTrash': 'library-manage',
  'library.openTagManager': 'library-manage',
  'photos.openAIReview': 'photos-manage',
  'photos.openCleanup': 'photos-manage',
  'people.openFaceReview': 'people'
};

// 作品集与人物按需懒加载，只取首页：面板是快速跳转，不是完整列表页。
const COMMAND_ENTITY_LIMIT = 50;

// App.vue 的 navigateTo 可以直接切页（同步返回 true）、被拦下（false），或要先等用户确认
// （返回 Promise<boolean>）。直接切页时 next 同步执行：命令面板里「打开作品集」之类的落点
// 要在同一拍里交给实体页。
export function whenNavigated(result, next) {
  if (result === false) return false;
  if (result && typeof result.then === 'function') {
    return result.then(ok => (ok === false ? false : next()));
  }
  return next();
}

function isCleanupHotkey(event) {
  if (!(event.metaKey || event.ctrlKey) || event.shiftKey || event.altKey) return false;
  return event.code === 'KeyK' || String(event.key || '').toLowerCase() === 'k';
}

export const appCommandsMixin = {
  data() {
    return {
      commandPaletteOpen: false,
      commandSavedViews: [],
      commandCollections: [],
      commandPeople: [],
      commandTaskState: { running: [], waiting: [] },
      // 命令面板打开某个作品集 / 人物：置一次即被子组件消费，随后清空，
      // 免得下次正常进入该页时又自动展开上一次的实体。
      entityFocus: { person: null, collection: null }
    };
  },
  mounted() {
    window.addEventListener('keydown', this.handleCommandPaletteHotkey);
    window.addEventListener('keydown', this.handleCleanupHotkey);
    this.registerAppCommands();
    this.registerTaskCommands();
    if (window.runtime?.EventsOn) {
      const tasksOff = window.runtime.EventsOn('background-tasks', running => {
        this.commandTaskState = { ...this.commandTaskState, running: running || [] };
        this.registerTaskCommands();
      });
      if (typeof tasksOff === 'function') this._commandTasksOff = tasksOff;
      const idleOff = window.runtime.EventsOn('idle-scheduler-state', status => {
        this.commandTaskState = { ...this.commandTaskState, waiting: status?.waiting || [] };
        this.registerTaskCommands();
      });
      if (typeof idleOff === 'function') this._commandIdleOff = idleOff;
    }
  },
  beforeUnmount() {
    window.removeEventListener('keydown', this.handleCommandPaletteHotkey);
    window.removeEventListener('keydown', this.handleCleanupHotkey);
    this._commandTasksOff?.();
    this._commandIdleOff?.();
    unregisterCommands('app');
    unregisterCommands('app:tasks');
  },
  methods: {
    // ⌘⇧P（macOS 的 metaKey）/ Ctrl+Shift+P。
    // 挂在 window 上、不判断事件源，所以焦点在搜索框之类的输入框里时同样生效。
    handleCommandPaletteHotkey(event) {
      if (!event || event.isComposing) return;
      if (!(event.metaKey || event.ctrlKey) || !event.shiftKey || event.altKey) return;
      // 按下 Shift 时 event.key 会是大写 'P'，键盘布局不同还可能是别的字符；
      // code 是物理键位，两种都认。
      if (event.code !== 'KeyP' && String(event.key || '').toLowerCase() !== 'p') return;
      // 已经被先注册的处理器接手（并 preventDefault）的按键不抢第二次。
      if (event.defaultPrevented) return;
      if (this.startupError) return;
      event.preventDefault();
      if (this.commandPaletteOpen) {
        this.commandPaletteOpen = false;
        return;
      }
      this.openCommandPalette();
    },
    // ⌘K：打开清理中心（D-PC59：「管理」菜单去掉清理审阅后，⌘K 改为走待处理工作台的清理入口，
    // 也就是 library.openCleanup）。任何页面都能按：先切到片库管理（管理区）再打开。
    // 片库页自己也监听 ⌘K（它先挂载、先收到并 preventDefault），在片库页上这里让位；
    // 有弹窗开着时同片库页一样不响应，免得把清理中心叠在别的弹窗上。
    handleCleanupHotkey(event) {
      if (!event || event.isComposing || !isCleanupHotkey(event)) return;
      if (event.defaultPrevented || this.startupError) return;
      if (document.querySelector('[role="dialog"]')) return;
      event.preventDefault();
      Promise.resolve(this.runHostCommand('library.openCleanup', '清理中心'))
        .catch(err => notifyError(`打开清理中心失败：${err}`));
    },
    openCommandPalette() {
      this.commandPaletteOpen = true;
      this.refreshCommandNavigation();
      this.refreshCommandTasks();
    },
    closeCommandPalette() {
      this.commandPaletteOpen = false;
    },
    // 按固定命令 ID 执行宿主页的命令：先切到宿主页（设置页有未保存修改时可能被用户拦下），
    // 等宿主页挂载、注册完命令再执行。找不到或不可用时给出中文提示并返回 false。
    async runHostCommand(commandID, label = '该入口') {
      const page = COMMAND_HOST_PAGES[commandID];
      if (page && page !== this.currentPage) {
        if (await whenNavigated(this.navigateTo(page), () => true) === false) return false;
        await this.$nextTick();
      }
      const command = findCommand(commandID);
      if (!command) {
        notifyError(`「${label}」暂时打不开：所在页面还没有准备好，请稍后再试。`);
        return false;
      }
      if (!isCommandEnabled(command)) {
        notifyError(`「${label}」当前不可用。`);
        return false;
      }
      return command.run();
    },
    // 命令面板执行一条命令之前的准备：宿主页的固定命令（其他页面注册、面板里可见的那些）先切到
    // 宿主页再执行，否则弹窗会开在隐藏的页面里。返回 false 表示放弃执行。
    async prepareCommandRun(command) {
      const page = COMMAND_HOST_PAGES[command?.id];
      if (!page || page === this.currentPage) return true;
      if (await whenNavigated(this.navigateTo(page), () => true) === false) return false;
      await this.$nextTick();
      return true;
    },
    registerAppCommands() {
      registerCommands('app', this.buildAppCommands());
    },
    registerTaskCommands() {
      registerCommands('app:tasks', buildTaskCommands({
        running: this.commandTaskState.running,
        waiting: this.commandTaskState.waiting,
        onSettled: () => this.refreshCommandTasks()
      }));
    },
    buildAppCommands() {
      const commands = [];
      for (const page of COMMAND_PAGES) {
        if (!isNavPageVisible(page, this.settings)) continue;
        commands.push({
          id: `nav:page:${page.key}`,
          group: 'navigate',
          label: `打开${page.label}`,
          keywords: page.keywords,
          run: () => this.navigateTo(page.key)
        });
      }
      for (const view of COMMAND_SMART_VIEWS) {
        commands.push({
          id: `nav:smart:${view.value || 'all'}`,
          group: 'navigate',
          label: `视频 · ${view.label}`,
          keywords: ['smart view', '智能视图', view.value].filter(Boolean),
          run: () => this.applySmartViewFromCommand(view.value)
        });
      }
      for (const view of this.commandSavedViews) {
        commands.push({
          id: `nav:saved:${view.id}`,
          group: 'navigate',
          label: `保存视图 · ${view.name}`,
          keywords: ['saved view', '保存视图'],
          run: () => this.applySavedViewFromCommand(view.id)
        });
      }
      for (const item of this.commandCollections) {
        const id = Number(item?.collection?.id || 0);
        const name = item?.collection?.name || '';
        if (!id) continue;
        commands.push({
          id: `nav:collection:${id}`,
          group: 'navigate',
          label: `作品集 · ${name}`,
          keywords: ['collection', '作品集'],
          run: () => this.openEntityFromCommand('collection', id, name)
        });
      }
      for (const item of this.commandPeople) {
        const id = Number(item?.person?.id || 0);
        const name = item?.person?.display_name || '';
        if (!id) continue;
        commands.push({
          id: `nav:person:${id}`,
          group: 'navigate',
          label: `人物 · ${name}`,
          keywords: ['person', '人物', item?.person?.original_name].filter(Boolean),
          run: () => this.openEntityFromCommand('person', id, name)
        });
      }
      // 全局动作（D-PC60）：任何页面都能执行，需要宿主页的先切过去。
      commands.push(
        { id: 'action:playback-queue', group: 'action', label: '打开待播队列', keywords: ['queue', '下一集', '连播'], run: () => { this.playbackQueueOpen = true; } },
        {
          id: 'action:task-center',
          group: 'action',
          label: '打开任务中心',
          keywords: ['task', 'tasks', '任务', '后台任务', 'renwu'],
          run: () => this.openTaskCenter()
        },
        {
          id: 'action:pending-work',
          group: 'action',
          label: '打开待处理',
          keywords: ['pending', 'inbox', '待处理', '审阅', 'daichuli'],
          run: () => this.openPendingWork()
        },
        {
          id: 'action:cleanup',
          group: 'action',
          label: '打开清理中心',
          keywords: ['cleanup', '清理', '重复', 'qingli', '⌘K'],
          run: () => this.runHostCommand('library.openCleanup', '清理中心')
        },
        {
          id: 'action:trash',
          group: 'action',
          label: '打开回收站',
          keywords: ['trash', '回收站', '已删除', 'huishouzhan'],
          run: () => this.runHostCommand('library.openTrash', '回收站')
        },
        {
          id: 'action:tag-manager',
          group: 'action',
          label: '打开标签管理',
          keywords: ['tag', 'tags', '标签', 'biaoqian'],
          run: () => this.runHostCommand('library.openTagManager', '标签管理')
        },
        {
          id: 'action:scan-image-directories',
          group: 'action',
          label: '扫描图片目录',
          keywords: ['scan', 'image', 'photo', '扫描', '图片'],
          run: () => SyncImageDirectories()
        },
        ...DATA_CARE_COMMANDS.map(item => ({
          id: `action:data-care:${item.tab}`,
          group: 'action',
          label: `数据维护 · ${item.label}`,
          keywords: ['data care', '数据维护', ...item.keywords],
          run: () => this.openDataCare(item.tab)
        })),
        {
          id: 'action:backup-now',
          group: 'action',
          label: '立即备份数据库',
          keywords: ['backup', '备份'],
          run: () => CreateDatabaseBackup()
        }
      );
      for (const section of SETTINGS_SECTIONS) {
        commands.push({
          id: `action:settings:${section.key}`,
          group: 'action',
          label: `设置 · ${section.label}`,
          keywords: ['settings', '设置', section.key],
          run: () => this.openSettingsSectionFromCommand(section.key)
        });
      }
      return commands;
    },
    // 保存视图 / 作品集 / 人物在打开面板时才拉：三份都是随时会变的用户数据，
    // 拉取失败保留上一次的清单并记一条日志，不把整个面板拖垮。
    async refreshCommandNavigation() {
      if (this._commandNavLoading) return;
      this._commandNavLoading = true;
      try {
        const [savedViews, collections, people] = await Promise.all([
          this.loadCommandData(() => ListSavedLibraryViews(), 'ListSavedLibraryViews'),
          this.loadCommandData(() => ListCollections('', '', 0, COMMAND_ENTITY_LIMIT), 'ListCollections'),
          this.loadCommandData(() => ListPeople('', '', 0, COMMAND_ENTITY_LIMIT), 'ListPeople')
        ]);
        if (savedViews) this.commandSavedViews = savedViews;
        if (collections) this.commandCollections = collections;
        if (people) this.commandPeople = people;
        this.registerAppCommands();
      } finally {
        this._commandNavLoading = false;
      }
    },
    async loadCommandData(load, label) {
      try {
        return await load() || [];
      } catch (err) {
        this.debugLog(`命令面板 ${label} 失败`, { err: String(err) }, true);
        return null;
      }
    },
    async refreshCommandTasks() {
      const [running, status] = await Promise.all([
        this.loadCommandData(() => GetBackgroundTasks(), 'GetBackgroundTasks'),
        this.loadCommandData(() => GetIdleSchedulerStatus(), 'GetIdleSchedulerStatus')
      ]);
      this.commandTaskState = {
        running: running || this.commandTaskState.running,
        waiting: status?.waiting || []
      };
      this.registerTaskCommands();
    },
    // 智能视图与保存视图落在当前区域的片库页 key 上（观看侧「视频」、管理侧「片库管理」，同一个实例），
    // 不为套一个筛选把用户换到另一侧。
    libraryPageForCurrentArea() {
      return pageArea(this.currentPage) === 'manage' ? 'library-manage' : 'videos';
    },
    applySmartViewFromCommand(value) {
      return whenNavigated(this.navigateTo(this.libraryPageForCurrentArea()), async () => {
        await this.$nextTick();
        await this.$refs.videoListPage?.applySmartViewCommand(value);
      });
    },
    applySavedViewFromCommand(viewID) {
      return whenNavigated(this.navigateTo(this.libraryPageForCurrentArea()), async () => {
        await this.$nextTick();
        await this.$refs.videoListPage?.applySavedViewCommand(viewID);
      });
    },
    // 命令面板不属于哪一侧，打开一部片按当前所在区域分流（D-UI-07，App.vue 的 openVideo）。
    openVideoFromCommand(video) {
      return this.openVideo(video, { from: COMMAND_PALETTE_SOURCE });
    },
    openSettingsSectionFromCommand(key) {
      return whenNavigated(this.navigateTo('settings'), async () => {
        await this.$nextTick();
        this.$refs.settingsPage?.scrollToSection(key);
      });
    },
    openEntityFromCommand(type, id, name) {
      return whenNavigated(this.navigateTo(type === 'person' ? 'people' : 'collections'), async () => {
        this.entityFocus = { ...this.entityFocus, [type]: { id, name } };
        await this.$nextTick();
        this.entityFocus = { ...this.entityFocus, [type]: null };
      });
    }
  }
};

export const COMMAND_PALETTE_PAGES = COMMAND_PAGES;
export const COMMAND_PALETTE_SMART_VIEWS = COMMAND_SMART_VIEWS;
