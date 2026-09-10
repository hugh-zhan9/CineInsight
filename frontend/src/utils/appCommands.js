// App.vue 的命令面板接线（D-029）：全局 ⌘⇧P / Ctrl+Shift+P、面板开关、导航组与应用级动作组、
// 以及跟着事件刷新的任务组。
//
// 做成 mixin 而不是直接写在 App.vue 里：App.vue 是多个切片共同修改的文件，
// 命令面板只在那里留最小的挂载点（mixins、一个组件标签、两个 ref）。
// 因此这里依赖 App.vue 的两项状态与两个 ref：
//   this.currentPage / this.startupError
//   this.$refs.videoListPage（常挂载，v-show 切换）/ this.$refs.settingsPage（v-if）
// 以及 App.vue 的 debugLog()。

import {
  CreateDatabaseBackup, GetBackgroundTasks, GetIdleSchedulerStatus, ListCollections, ListPeople, ListSavedLibraryViews
} from '../../wailsjs/go/main/App';
import { SETTINGS_SECTIONS } from '../components/SettingsPage.vue';
import { registerCommands, unregisterCommands } from './commandRegistry.js';
import { buildTaskCommands } from './taskCommands.js';

// 页面导航，key 与 App.vue 的 currentPage 一致。
const COMMAND_PAGES = [
  { key: 'videos', label: '视频', keywords: ['video', 'library', 'shipin'] },
  { key: 'people', label: '人物', keywords: ['person', 'people', 'renwu'] },
  { key: 'collections', label: '作品集', keywords: ['collection', 'zuopinji'] },
  { key: 'watchlist', label: '想看', keywords: ['watchlist', 'xiangkan', '片单', '待下载'] },
  { key: 'insights', label: '洞察', keywords: ['insights', 'stats', 'dongcha'] },
  { key: 'photos', label: '图片', keywords: ['photo', 'image', 'tupian'] },
  { key: 'settings', label: '设置', keywords: ['settings', 'shezhi'] }
];

// 与 video-list/LibraryToolbar.vue 的 smartViewOptions 一一对应（那份是真值来源，
// 本切片不改工具栏）。appCommands.test.js 有一条防漂移断言比对两处。
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
  { value: 'stale', label: '路径失效' }
];

// 作品集与人物按需懒加载，只取首页：面板是快速跳转，不是完整列表页。
const COMMAND_ENTITY_LIMIT = 50;

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
    this._commandTasksOff?.();
    this._commandIdleOff?.();
    unregisterCommands('app');
    unregisterCommands('app:tasks');
  },
  methods: {
    // ⌘⇧P（macOS 的 metaKey）/ Ctrl+Shift+P。⌘K 归片库页的「清理审阅」所有
    // （LibraryToolbar 的管理菜单印着 ⌘K），命令面板不去抢它。
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
    openCommandPalette() {
      this.commandPaletteOpen = true;
      this.refreshCommandNavigation();
      this.refreshCommandTasks();
    },
    closeCommandPalette() {
      this.commandPaletteOpen = false;
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
        commands.push({
          id: `nav:page:${page.key}`,
          group: 'navigate',
          label: `打开${page.label}`,
          keywords: page.keywords,
          run: () => { this.currentPage = page.key; }
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
      commands.push({
        id: 'action:backup-now',
        group: 'action',
        label: '立即备份数据库',
        keywords: ['backup', '备份'],
        run: () => CreateDatabaseBackup()
      });
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
    async applySmartViewFromCommand(value) {
      this.currentPage = 'videos';
      await this.$nextTick();
      await this.$refs.videoListPage?.applySmartViewCommand(value);
    },
    async applySavedViewFromCommand(viewID) {
      this.currentPage = 'videos';
      await this.$nextTick();
      await this.$refs.videoListPage?.applySavedViewCommand(viewID);
    },
    async openVideoFromCommand(video) {
      this.currentPage = 'videos';
      await this.$nextTick();
      await this.$refs.videoListPage?.openPreview(video);
    },
    async openSettingsSectionFromCommand(key) {
      this.currentPage = 'settings';
      await this.$nextTick();
      this.$refs.settingsPage?.scrollToSection(key);
    },
    async openEntityFromCommand(type, id, name) {
      this.currentPage = type === 'person' ? 'people' : 'collections';
      this.entityFocus = { ...this.entityFocus, [type]: { id, name } };
      await this.$nextTick();
      this.entityFocus = { ...this.entityFocus, [type]: null };
    }
  }
};

export const COMMAND_PALETTE_PAGES = COMMAND_PAGES;
export const COMMAND_PALETTE_SMART_VIEWS = COMMAND_SMART_VIEWS;
