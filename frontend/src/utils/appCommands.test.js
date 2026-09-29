import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'CreateDatabaseBackup', 'GetBackgroundTasks', 'GetIdleSchedulerStatus', 'ListCollections', 'ListPeople',
  'ListSavedLibraryViews', 'LogFrontend',
  'UpdateSettings', 'GetAITagLibrary', 'SaveAITagLibrary', 'ClearAITagLibrary', 'TriggerAITagging',
  'GetLibraryWatcherStatus',
  'StartFrameHashBackfill', 'CancelFrameHashBackfill', 'SyncImageDirectories'
].map(name => [name, vi.fn()])));

vi.mock('../../wailsjs/go/main/App', () => api);

import {
  APP_NAV_GROUPS, appCommandsMixin, COMMAND_HOST_PAGES, COMMAND_PALETTE_PAGES, COMMAND_PALETTE_SMART_VIEWS
} from './appCommands.js';
import { commandList, findCommand, registerCommands, unregisterCommands } from './commandRegistry.js';
import { feedbackState, resetFeedback } from './feedback.js';
import { SETTINGS_SECTIONS } from '../components/SettingsPage.vue';

// 只借 mixin：App.vue 的其余部分（页面、主题、启动错误）与命令注册无关。
// navigateTo / openTaskCenter / openPendingWork 是 App.vue 提供给 mixin 的方法，这里给最简实现。
const Host = {
  mixins: [appCommandsMixin],
  props: { settings: { type: Object, default: () => ({}) } },
  data() {
    return { currentPage: 'videos', startupError: '', taskCenterOpen: false, pendingWorkOpen: false };
  },
  methods: {
    debugLog: vi.fn(),
    navigateTo(page) {
      this.currentPage = page;
      return true;
    },
    openTaskCenter() { this.taskCenterOpen = true; },
    openPendingWork() { this.pendingWorkOpen = true; }
  },
  template: '<div />'
};

function mountHost(props = {}) {
  return mount(Host, { props });
}

function commandByID(id) {
  return commandList().find(command => command.id === id) || null;
}

beforeEach(() => {
  vi.clearAllMocks();
  api.GetBackgroundTasks.mockResolvedValue([]);
  api.GetIdleSchedulerStatus.mockResolvedValue({ waiting: [] });
  api.ListSavedLibraryViews.mockResolvedValue([]);
  api.ListCollections.mockResolvedValue([]);
  api.ListPeople.mockResolvedValue([]);
  api.CreateDatabaseBackup.mockResolvedValue({});
  api.SyncImageDirectories.mockResolvedValue({});
  resetFeedback();
});

const wrappers = [];

afterEach(() => {
  while (wrappers.length) wrappers.pop().unmount();
});

function host(props = {}) {
  const wrapper = mountHost(props);
  wrappers.push(wrapper);
  return wrapper;
}

describe('应用级命令注册', () => {
  it('注册的页面各有一条导航命令，执行即切页', () => {
    const wrapper = host({ settings: { browser_bridge_enabled: true } });

    for (const page of COMMAND_PALETTE_PAGES) {
      const command = commandByID(`nav:page:${page.key}`);
      expect(command, page.key).not.toBeNull();
      expect(command.group).toBe('navigate');
    }

    commandByID('nav:page:photos').run();
    expect(wrapper.vm.currentPage).toBe('photos');
    commandByID('nav:page:watchlist').run();
    expect(wrapper.vm.currentPage).toBe('watchlist');
  });

  // 榜单页与已看页是 P-007 / P-008 新增的两个页面。P-007 改不到这个文件（不在它的
  // 写入清单里），所以两条一起在这里补上：少了它们，命令面板列得出其余每一个页面，
  // 唯独这两个只能靠顶栏点进去。key 必须与 App.vue 的 currentPage 逐字一致。
  // D-PC52：顶栏「已看」改名「观影记录」，命令面板同步改名；旧说法留作关键词还搜得到。
  it('榜单与观影记录两页也在导航组里，执行即切到对应页面（APP-05 改名）', () => {
    const wrapper = host();

    expect(commandByID('nav:page:movie-chart').label).toBe('打开榜单');
    commandByID('nav:page:movie-chart').run();
    expect(wrapper.vm.currentPage).toBe('movie-chart');

    const watched = commandByID('nav:page:watched-movies');
    expect(watched.label).toBe('打开观影记录');
    expect(watched.keywords).toContain('已看');
    watched.run();
    expect(wrapper.vm.currentPage).toBe('watched-movies');
  });

  // 顶栏有入口、命令面板没有，是榜单与已看两页当初漏掉的那种失配。现在顶栏直接渲染
  // APP_NAV_GROUPS，这条钉住两件事：App.vue 真的按这份清单渲染；清单里每一页在命令面板都有条目。
  it('APP-12 顶栏与命令面板读同一份页面清单，顶栏的每一页在导航组里都有条目', () => {
    const source = readFileSync(resolve(process.cwd(), 'src/App.vue'), 'utf8');
    expect(source).toMatch(/APP_NAV_GROUPS/);
    expect(source).toMatch(/v-for="page in group\.pages"/);

    const keys = COMMAND_PALETTE_PAGES.map(page => page.key);
    for (const group of APP_NAV_GROUPS) {
      for (const page of group.pages) expect(keys, page.key).toContain(page.key);
    }
    expect(APP_NAV_GROUPS.map(group => group.label)).toEqual(['片库', '片单', '工具']);
  });

  it('APP-14 「打开下载」只在开启浏览器插件桥接时注册', async () => {
    const wrapper = host();
    expect(commandByID('nav:page:downloads')).toBeNull();

    await wrapper.setProps({ settings: { browser_bridge_enabled: true } });
    wrapper.vm.registerAppCommands();
    commandByID('nav:page:downloads').run();
    expect(wrapper.vm.currentPage).toBe('downloads');
  });

  it('智能视图逐条注册为导航命令', () => {
    host();
    for (const view of COMMAND_PALETTE_SMART_VIEWS) {
      const command = commandByID(`nav:smart:${view.value || 'all'}`);
      expect(command, view.label).not.toBeNull();
      expect(command.label).toBe(`视频 · ${view.label}`);
    }
  });

  it('设置每个分区各有一条动作命令，执行即跳到设置页', async () => {
    const wrapper = host();

    for (const section of SETTINGS_SECTIONS) {
      expect(commandByID(`action:settings:${section.key}`), section.key).not.toBeNull();
    }

    await commandByID('action:settings:database').run();
    expect(wrapper.vm.currentPage).toBe('settings');
  });

  it('立即备份调用 CreateDatabaseBackup', () => {
    host();
    commandByID('action:backup-now').run();
    expect(api.CreateDatabaseBackup).toHaveBeenCalledTimes(1);
  });

  it('卸载后应用级命令与任务命令一起消失', () => {
    const wrapper = mountHost();
    expect(commandByID('nav:page:videos')).not.toBeNull();

    wrapper.unmount();
    expect(commandByID('nav:page:videos')).toBeNull();
    expect(commandList().filter(command => command.group === 'task')).toEqual([]);
  });
});

describe('全局命令（D-PC60、APP-12）', () => {
  function registerHost(id, run) {
    registerCommands('test-host', [{ id, group: 'action', label: id, hidden: true, run }]);
  }

  afterEach(() => unregisterCommands('test-host'));

  it('APP-12 任务中心与待处理各有一条全局命令', () => {
    const wrapper = host();

    commandByID('action:task-center').run();
    expect(wrapper.vm.taskCenterOpen).toBe(true);
    commandByID('action:pending-work').run();
    expect(wrapper.vm.pendingWorkOpen).toBe(true);
  });

  it.each([
    ['action:cleanup', 'library.openCleanup'],
    ['action:trash', 'library.openTrash'],
    ['action:tag-manager', 'library.openTagManager']
  ])('APP-12 %s 在任何页面都先切回片库页，再执行宿主命令 %s', async (commandID, hostID) => {
    const wrapper = host();
    const run = vi.fn();
    registerHost(hostID, run);
    wrapper.vm.currentPage = 'settings';

    await commandByID(commandID).run();

    expect(wrapper.vm.currentPage).toBe('videos');
    expect(run).toHaveBeenCalledTimes(1);
  });

  it('APP-12 扫描图片目录是全局命令，直接调用 SyncImageDirectories', async () => {
    const wrapper = host();
    wrapper.vm.currentPage = 'insights';

    await commandByID('action:scan-image-directories').run();

    expect(api.SyncImageDirectories).toHaveBeenCalledTimes(1);
    expect(wrapper.vm.currentPage).toBe('insights');
  });

  it('APP-12 宿主命令还没注册时给出中文提示并返回 false（面板据此不报「已执行」）', async () => {
    const wrapper = host();

    expect(await wrapper.vm.runHostCommand('library.openCleanup', '清理中心')).toBe(false);
    expect(feedbackState.toasts.map(toast => toast.message)).toEqual([
      '「清理中心」暂时打不开：所在页面还没有准备好，请稍后再试。'
    ]);
  });

  it('APP-12 用户在切页确认里放弃时不执行宿主命令', async () => {
    const wrapper = host();
    const run = vi.fn();
    registerHost('library.openCleanup', run);
    wrapper.vm.currentPage = 'settings';
    wrapper.vm.navigateTo = vi.fn(() => Promise.resolve(false));

    expect(await wrapper.vm.runHostCommand('library.openCleanup', '清理中心')).toBe(false);
    expect(run).not.toHaveBeenCalled();
  });

  it('APP-11 固定命令 ID 都登记了宿主页，面板执行前据此先切页', async () => {
    expect(COMMAND_HOST_PAGES).toEqual(expect.objectContaining({
      'library.openCleanup': 'videos',
      'library.openAIReview': 'videos',
      'library.openLocalMetadataUpdates': 'videos',
      'library.openCollectionSuggestions': 'videos',
      'photos.openAIReview': 'photos',
      'people.openFaceReview': 'people'
    }));
    const wrapper = host();
    wrapper.vm.currentPage = 'settings';

    expect(await wrapper.vm.prepareCommandRun({ id: 'photos.openAIReview' })).toBe(true);
    expect(wrapper.vm.currentPage).toBe('photos');
    expect(await wrapper.vm.prepareCommandRun({ id: 'action:backup-now' })).toBe(true);
    expect(wrapper.vm.currentPage).toBe('photos');
  });

  it('宿主页注册的 hidden 跳转目标不出现在面板里，但 findCommand 找得到', () => {
    host();
    registerHost('library.openCleanup', vi.fn());

    expect(commandByID('library.openCleanup')).toBeNull();
    expect(findCommand('library.openCleanup')).not.toBeNull();
  });
});

describe('打开面板时的懒加载', () => {
  it('保存视图、作品集、人物在打开面板时才拉，并注册成导航命令', async () => {
    api.ListSavedLibraryViews.mockResolvedValue([{ id: 3, name: '大文件待清理' }]);
    api.ListCollections.mockResolvedValue([{ collection: { id: 5, name: '旅行' } }]);
    api.ListPeople.mockResolvedValue([{ person: { id: 8, display_name: '张三', original_name: 'Zhang San' } }]);
    const wrapper = host();

    expect(commandByID('nav:saved:3')).toBeNull();

    wrapper.vm.openCommandPalette();
    await flushPromises();

    expect(commandByID('nav:saved:3').label).toBe('保存视图 · 大文件待清理');
    expect(commandByID('nav:collection:5').label).toBe('作品集 · 旅行');
    expect(commandByID('nav:person:8').label).toBe('人物 · 张三');
    expect(api.ListCollections).toHaveBeenCalledWith('', '', 0, 50);
    expect(api.ListPeople).toHaveBeenCalledWith('', '', 0, 50);
  });

  it('某一份清单拉失败只丢它自己，其余照常注册', async () => {
    api.ListCollections.mockRejectedValue(new Error('后端不可用'));
    api.ListPeople.mockResolvedValue([{ person: { id: 8, display_name: '张三' } }]);
    const wrapper = host();

    wrapper.vm.openCommandPalette();
    await flushPromises();

    expect(commandByID('nav:collection:5')).toBeNull();
    expect(commandByID('nav:person:8')).not.toBeNull();
    expect(commandByID('nav:page:videos')).not.toBeNull();
  });

  it('打开作品集的命令把落点交给实体页后立即清空，避免下次进页面又自动展开', async () => {
    api.ListCollections.mockResolvedValue([{ collection: { id: 5, name: '旅行' } }]);
    const wrapper = host();

    wrapper.vm.openCommandPalette();
    await flushPromises();

    const pending = commandByID('nav:collection:5').run();
    expect(wrapper.vm.currentPage).toBe('collections');
    expect(wrapper.vm.entityFocus.collection).toEqual({ id: 5, name: '旅行' });

    await pending;
    expect(wrapper.vm.entityFocus.collection).toBeNull();
  });
});

describe('任务组随事件刷新', () => {
  it('background-tasks 与 idle-scheduler-state 到达后重建任务命令', async () => {
    const handlers = {};
    window.runtime = { EventsOn: (event, handler) => { handlers[event] = handler; return () => { delete handlers[event]; }; } };
    const wrapper = host();

    expect(commandByID('task:phash').label).toBe('近重复指纹 · 启动');

    handlers['background-tasks'](['phash']);
    await flushPromises();
    expect(commandByID('task:phash').label).toBe('近重复指纹 · 运行中 · 取消');

    handlers['background-tasks']([]);
    handlers['idle-scheduler-state']({ waiting: [{ task_key: 'phash', reason: 'on_battery', since: '' }] });
    await flushPromises();
    expect(commandByID('task:phash').label).toContain('等待空闲');

    wrapper.unmount();
    wrappers.pop();
    delete window.runtime;
  });

  // 帧哈希（P-008）也是零参启动 + 可取消，必须在任务组里可用；
  // 少了绑定它就只能在"运行中"时露面，闲置时连启动入口都没有。
  it('帧哈希可从任务组启动与取消', async () => {
    const handlers = {};
    window.runtime = { EventsOn: (event, handler) => { handlers[event] = handler; return () => { delete handlers[event]; }; } };
    const wrapper = host();

    expect(commandByID('task:frame_hash').label).toBe('帧哈希 · 启动');
    await commandByID('task:frame_hash').run();
    expect(api.StartFrameHashBackfill).toHaveBeenCalledOnce();

    handlers['background-tasks'](['frame_hash']);
    await flushPromises();
    expect(commandByID('task:frame_hash').label).toBe('帧哈希 · 运行中 · 取消');
    await commandByID('task:frame_hash').run();
    expect(api.CancelFrameHashBackfill).toHaveBeenCalledOnce();

    wrapper.unmount();
    wrappers.pop();
    delete window.runtime;
  });
});

// 智能视图的真值来源是 video-list/LibraryToolbar.vue 的 smartViewOptions（本切片不改它）。
// 两份清单一旦漂移，命令面板会给出一个工具栏里不存在的视图，这条断言把它钉住。
describe('智能视图清单防漂移', () => {
  it('与 LibraryToolbar.vue 的 smartViewOptions 逐条一致（含 META-09 的「本地资料有更新」）', () => {
    // vitest 的根就是 frontend/，jsdom 环境下 import.meta.url 不是 file: 协议。
    const source = readFileSync(resolve(process.cwd(), 'src/components/video-list/LibraryToolbar.vue'), 'utf8');
    const block = /smartViewOptions:\s*\[([\s\S]*?)\]/.exec(source);
    expect(block, 'LibraryToolbar.vue 里应当能找到 smartViewOptions').not.toBeNull();

    const fromToolbar = [...block[1].matchAll(/\{\s*label:\s*'([^']*)',\s*value:\s*'([^']*)'\s*\}/g)]
      .map(match => ({ label: match[1], value: match[2] }));

    expect(fromToolbar.length).toBeGreaterThan(0);
    expect(COMMAND_PALETTE_SMART_VIEWS).toEqual(fromToolbar);
    expect(fromToolbar).toContainEqual({ label: '本地资料有更新', value: 'local_metadata_updated' });
  });
});
