import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'CreateDatabaseBackup', 'GetBackgroundTasks', 'GetIdleSchedulerStatus', 'ListCollections', 'ListPeople',
  'ListSavedLibraryViews', 'LogFrontend',
  'UpdateSettings', 'GetAITagLibrary', 'SaveAITagLibrary', 'ClearAITagLibrary', 'TriggerAITagging',
  'GetLibraryWatcherStatus',
  'StartFrameHashBackfill', 'CancelFrameHashBackfill'
].map(name => [name, vi.fn()])));

vi.mock('../../wailsjs/go/main/App', () => api);

import { appCommandsMixin, COMMAND_PALETTE_PAGES, COMMAND_PALETTE_SMART_VIEWS } from './appCommands.js';
import { commandList } from './commandRegistry.js';
import { SETTINGS_SECTIONS } from '../components/SettingsPage.vue';

// 只借 mixin：App.vue 的其余部分（页面、主题、启动错误）与命令注册无关。
const Host = {
  mixins: [appCommandsMixin],
  data() {
    return { currentPage: 'videos', startupError: '' };
  },
  methods: {
    debugLog: vi.fn()
  },
  template: '<div />'
};

function mountHost() {
  return mount(Host);
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
});

const wrappers = [];

afterEach(() => {
  while (wrappers.length) wrappers.pop().unmount();
});

function host() {
  const wrapper = mountHost();
  wrappers.push(wrapper);
  return wrapper;
}

describe('应用级命令注册', () => {
  it('注册的页面各有一条导航命令，执行即切页', () => {
    const wrapper = host();

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
  it('与 LibraryToolbar.vue 的 smartViewOptions 逐条一致', () => {
    // vitest 的根就是 frontend/，jsdom 环境下 import.meta.url 不是 file: 协议。
    const source = readFileSync(resolve(process.cwd(), 'src/components/video-list/LibraryToolbar.vue'), 'utf8');
    const block = /smartViewOptions:\s*\[([\s\S]*?)\]/.exec(source);
    expect(block, 'LibraryToolbar.vue 里应当能找到 smartViewOptions').not.toBeNull();

    const fromToolbar = [...block[1].matchAll(/\{\s*label:\s*'([^']*)',\s*value:\s*'([^']*)'\s*\}/g)]
      .map(match => ({ label: match[1], value: match[2] }));

    expect(fromToolbar.length).toBeGreaterThan(0);
    expect(COMMAND_PALETTE_SMART_VIEWS).toEqual(fromToolbar);
  });
});
