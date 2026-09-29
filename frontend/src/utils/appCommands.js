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
// 以及 App.vue 的 debugLog()。

import {
  CreateDatabaseBackup, GetBackgroundTasks, GetIdleSchedulerStatus, ListCollections, ListPeople, ListSavedLibraryViews,
  SyncImageDirectories
} from '../../wailsjs/go/main/App';
import { SETTINGS_SECTIONS } from '../components/SettingsPage.vue';
import { findCommand, isCommandEnabled, registerCommands, unregisterCommands } from './commandRegistry.js';
import { notifyError } from '../utils/feedback.js';
import { buildTaskCommands } from './taskCommands.js';

// 顶栏三组（D-PC59）：片库、片单、工具；「设置」单独放在组外。key 与 App.vue 的 currentPage 一致，
// 顶栏按钮与命令面板的导航组都读这一份，两边不会再各自漏页（APP-12、APP-14）。
// requiresBrowserBridge：只在设置里开启了浏览器插件桥接时显示（「下载」页）。
export const APP_NAV_GROUPS = [
  {
    key: 'library',
    label: '片库',
    pages: [
      { key: 'videos', label: '视频', keywords: ['video', 'library', 'shipin'] },
      { key: 'people', label: '人物', keywords: ['person', 'people', 'renwu'] },
      { key: 'collections', label: '作品集', keywords: ['collection', 'zuopinji'] },
      { key: 'photos', label: '图片', keywords: ['photo', 'image', 'tupian'] }
    ]
  },
  {
    key: 'lists',
    label: '片单',
    pages: [
      { key: 'watchlist', label: '想看', keywords: ['watchlist', 'xiangkan', '片单', '待下载'] },
      { key: 'movie-chart', label: '榜单', keywords: ['chart', 'movie chart', 'bangdan', '年度', '豆瓣'] },
      // 顶栏「已看」改名「观影记录」（D-PC52），与片库的「已看」智能视图区分开；旧说法留作关键词。
      { key: 'watched-movies', label: '观影记录', keywords: ['watched', 'guanying', 'yikan', 'kanguo', '看过', '已看', '榜单已看'] }
    ]
  },
  {
    key: 'tools',
    label: '工具',
    pages: [
      { key: 'insights', label: '洞察', keywords: ['insights', 'stats', 'dongcha'] },
      { key: 'downloads', label: '下载', keywords: ['download', 'xiazai', '插件', '浏览器'], requiresBrowserBridge: true }
    ]
  }
];

export const SETTINGS_NAV_PAGE = { key: 'settings', label: '设置', keywords: ['settings', 'shezhi'] };

// 页面导航的全集（含设置）。
const COMMAND_PAGES = [...APP_NAV_GROUPS.flatMap(group => group.pages), SETTINGS_NAV_PAGE];

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
export const COMMAND_HOST_PAGES = {
  'library.openCleanup': 'videos',
  'library.openAIReview': 'videos',
  'library.openLocalMetadataUpdates': 'videos',
  'library.openCollectionSuggestions': 'videos',
  'library.openTrash': 'videos',
  'library.openTagManager': 'videos',
  'photos.openAIReview': 'photos',
  'people.openFaceReview': 'people'
};

// 作品集与人物按需懒加载，只取首页：面板是快速跳转，不是完整列表页。
const COMMAND_ENTITY_LIMIT = 50;

// App.vue 的 navigateTo 可以直接切页（同步返回 true）、被拦下（false），或要先等用户确认
// （返回 Promise<boolean>）。直接切页时 next 同步执行：命令面板里「打开作品集」之类的落点
// 要在同一拍里交给实体页。
function whenNavigated(result, next) {
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
    // 也就是 library.openCleanup）。任何页面都能按：先切回片库页再打开。
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
    applySmartViewFromCommand(value) {
      return whenNavigated(this.navigateTo('videos'), async () => {
        await this.$nextTick();
        await this.$refs.videoListPage?.applySmartViewCommand(value);
      });
    },
    applySavedViewFromCommand(viewID) {
      return whenNavigated(this.navigateTo('videos'), async () => {
        await this.$nextTick();
        await this.$refs.videoListPage?.applySavedViewCommand(viewID);
      });
    },
    openVideoFromCommand(video) {
      return whenNavigated(this.navigateTo('videos'), async () => {
        await this.$nextTick();
        await this.$refs.videoListPage?.openPreview(video);
      });
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
