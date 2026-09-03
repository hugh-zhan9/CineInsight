import { flushPromises, shallowMount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'GetSettings', 'GetAllTags', 'GetAllDirectories', 'GetStartupError',
  'SyncScanDirectories', 'SyncImageDirectories', 'GetLibraryCounts', 'LogFrontend',
  'SetWindowForeground',
  'GetBackgroundTasks', 'GetIdleSchedulerStatus', 'ListSavedLibraryViews', 'ListCollections', 'ListPeople'
].map(name => [name, vi.fn()])));

vi.mock('../wailsjs/go/main/App', () => api);

import App from './App.vue';

beforeEach(() => {
  vi.clearAllMocks();
  // jsdom 不实现 matchMedia，App.mounted 里跟随系统主题的那段需要它。
  window.matchMedia = vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }));
  api.GetStartupError.mockResolvedValue('');
  api.GetSettings.mockResolvedValue({ auto_scan_on_startup: false, theme: 'system' });
  api.GetAllTags.mockResolvedValue([]);
  api.GetAllDirectories.mockResolvedValue([]);
  api.GetLibraryCounts.mockResolvedValue({});
  api.SyncScanDirectories.mockResolvedValue({ added: 0, deleted: 0, relocated: 0, metadata_refreshed: 0 });
  api.SyncImageDirectories.mockResolvedValue({});
  api.SetWindowForeground.mockResolvedValue(undefined);
  api.GetBackgroundTasks.mockResolvedValue([]);
  api.GetIdleSchedulerStatus.mockResolvedValue({ waiting: [] });
  api.ListSavedLibraryViews.mockResolvedValue([]);
  api.ListCollections.mockResolvedValue([]);
  api.ListPeople.mockResolvedValue([]);
  // jsdom 的 hasFocus 行为随实现变动，前后台用例自己钉住它。
  document.hasFocus = vi.fn(() => true);
});

// 用例结束后必须卸载：App 在 window / document 上挂了前后台监听，
// 留着不卸的旧实例会一起响应下一个用例派发的事件。
const mountedWrappers = [];

afterEach(() => {
  while (mountedWrappers.length) {
    const wrapper = mountedWrappers.pop();
    try {
      wrapper.unmount();
    } catch {
      // 用例自己卸过一次的 wrapper 再卸会抛，忽略即可。
    }
  }
});

async function mountApp() {
  const wrapper = shallowMount(App);
  mountedWrappers.push(wrapper);
  await flushPromises();
  return wrapper;
}

describe('扫描目录配置变更', () => {
  it('保存目录配置后自动对一次账', async () => {
    const wrapper = await mountApp();
    expect(api.SyncScanDirectories).not.toHaveBeenCalled();

    wrapper.vm.handleDirectoriesChanged([{ id: 1, path: '/Volumes/think plus/Ellieli-Collection', alias: '' }]);
    await flushPromises();

    expect(api.SyncScanDirectories).toHaveBeenCalledTimes(1);
    expect(wrapper.vm.directories).toHaveLength(1);
  });

  it('只改别名不触发全盘扫描', async () => {
    api.GetAllDirectories.mockResolvedValue([{ id: 1, path: '/Volumes/think plus', alias: '旧名' }]);
    const wrapper = await mountApp();

    wrapper.vm.handleDirectoriesChanged([{ id: 1, path: '/Volumes/think plus', alias: '新名' }]);
    await flushPromises();

    expect(api.SyncScanDirectories).not.toHaveBeenCalled();
    expect(wrapper.vm.directories[0].alias).toBe('新名');
  });

  it('目录被删光时没有可扫的根，不再空跑一次对账', async () => {
    const wrapper = await mountApp();

    wrapper.vm.handleDirectoriesChanged([]);
    await flushPromises();

    expect(api.SyncScanDirectories).not.toHaveBeenCalled();
  });
});

describe('窗口前后台上报', () => {
  it('挂载后立刻把当前状态报给后端', async () => {
    await mountApp();

    expect(api.SetWindowForeground).toHaveBeenCalledTimes(1);
    expect(api.SetWindowForeground).toHaveBeenCalledWith(true);
  });

  it('blur 报后台、focus 报前台，重复事件不重复上报', async () => {
    await mountApp();
    api.SetWindowForeground.mockClear();

    window.dispatchEvent(new Event('blur'));
    window.dispatchEvent(new Event('blur'));
    await flushPromises();
    expect(api.SetWindowForeground.mock.calls).toEqual([[false]]);

    window.dispatchEvent(new Event('focus'));
    window.dispatchEvent(new Event('focus'));
    await flushPromises();
    expect(api.SetWindowForeground.mock.calls).toEqual([[false], [true]]);
  });

  it('visibilitychange 转为隐藏时报后台', async () => {
    await mountApp();
    api.SetWindowForeground.mockClear();

    const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden');
    document.dispatchEvent(new Event('visibilitychange'));
    await flushPromises();
    expect(api.SetWindowForeground.mock.calls).toEqual([[false]]);

    visibility.mockReturnValue('visible');
    document.dispatchEvent(new Event('visibilitychange'));
    await flushPromises();
    expect(api.SetWindowForeground.mock.calls).toEqual([[false], [true]]);
    visibility.mockRestore();
  });

  it('后端上报失败不抛到界面', async () => {
    api.SetWindowForeground.mockRejectedValue(new Error('bridge down'));
    const wrapper = await mountApp();

    expect(wrapper.vm.windowForeground).toBe(true);
  });

  it('卸载后不再上报', async () => {
    const wrapper = await mountApp();
    api.SetWindowForeground.mockClear();
    wrapper.unmount();

    window.dispatchEvent(new Event('blur'));
    await flushPromises();
    expect(api.SetWindowForeground).not.toHaveBeenCalled();
  });
});

describe('命令面板快捷键', () => {
  function pressKey(target, init) {
    const event = new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init });
    target.dispatchEvent(event);
    return event;
  }

  // Shift 按下时 event.key 是大写 'P'，浏览器实际派发的就是这个形状。
  const palette = { key: 'P', code: 'KeyP', metaKey: true, shiftKey: true };

  it('⌘⇧P 打开面板，再按一次关闭', async () => {
    const wrapper = await mountApp();
    expect(wrapper.vm.commandPaletteOpen).toBe(false);

    const opened = pressKey(window, palette);
    await flushPromises();
    expect(wrapper.vm.commandPaletteOpen).toBe(true);
    expect(opened.defaultPrevented).toBe(true);

    pressKey(window, palette);
    await flushPromises();
    expect(wrapper.vm.commandPaletteOpen).toBe(false);
  });

  it('Ctrl+Shift+P 同样打开面板', async () => {
    const wrapper = await mountApp();

    pressKey(window, { key: 'P', code: 'KeyP', ctrlKey: true, shiftKey: true });
    await flushPromises();

    expect(wrapper.vm.commandPaletteOpen).toBe(true);
  });

  it('只报小写 p 的键盘布局也认', async () => {
    const wrapper = await mountApp();

    pressKey(window, { key: 'p', metaKey: true, shiftKey: true });
    await flushPromises();

    expect(wrapper.vm.commandPaletteOpen).toBe(true);
  });

  it('焦点在输入框里 ⌘⇧P 仍然生效', async () => {
    const wrapper = await mountApp();
    const input = document.createElement('input');
    document.body.appendChild(input);
    input.focus();

    pressKey(input, palette);
    await flushPromises();

    expect(wrapper.vm.commandPaletteOpen).toBe(true);
    input.remove();
  });

  it('打开面板时才拉保存视图与任务状态，关闭状态下不打后端', async () => {
    const wrapper = await mountApp();
    expect(api.ListSavedLibraryViews).not.toHaveBeenCalled();

    pressKey(window, palette);
    await flushPromises();

    expect(api.ListSavedLibraryViews).toHaveBeenCalledTimes(1);
    expect(api.ListCollections).toHaveBeenCalledTimes(1);
    expect(api.ListPeople).toHaveBeenCalledTimes(1);
    expect(api.GetBackgroundTasks).toHaveBeenCalled();
    expect(api.GetIdleSchedulerStatus).toHaveBeenCalled();
    expect(wrapper.vm.commandPaletteOpen).toBe(true);
  });

  it('⌘K 不是命令面板的快捷键：它仍归片库页的清理审阅', async () => {
    const wrapper = await mountApp();

    const event = pressKey(window, { key: 'k', code: 'KeyK', metaKey: true });
    await flushPromises();

    expect(wrapper.vm.commandPaletteOpen).toBe(false);
    expect(event.defaultPrevented).toBe(false);
  });

  it('面板关闭时不拦截 J / K / 空格等既有审阅快捷键', async () => {
    const wrapper = await mountApp();

    for (const key of ['j', 'k', ' ', 'f', 'w', 't', 'p', 'Enter', 'ArrowDown']) {
      const event = pressKey(window, { key });
      expect(event.defaultPrevented).toBe(false);
    }
    await flushPromises();

    expect(wrapper.vm.commandPaletteOpen).toBe(false);
  });

  it('⌘⇧P 已被先注册的处理器接手时让位', async () => {
    const wrapper = await mountApp();
    // 捕获阶段先跑，等价于某个先注册的 window 监听已经 preventDefault。
    const claim = event => event.preventDefault();
    window.addEventListener('keydown', claim, true);

    pressKey(window, palette);
    await flushPromises();

    window.removeEventListener('keydown', claim, true);
    expect(wrapper.vm.commandPaletteOpen).toBe(false);
  });

  it('⌘P 与 ⌥⌘⇧P 不是命令面板的快捷键', async () => {
    const wrapper = await mountApp();

    pressKey(window, { key: 'p', code: 'KeyP', metaKey: true });
    pressKey(window, { key: 'P', code: 'KeyP', metaKey: true, shiftKey: true, altKey: true });
    await flushPromises();

    expect(wrapper.vm.commandPaletteOpen).toBe(false);
  });

  it('数据库连不上时不开面板：那个状态下没有页面可跳', async () => {
    api.GetStartupError.mockResolvedValue('数据库连接失败');
    const wrapper = await mountApp();

    pressKey(window, palette);
    await flushPromises();

    expect(wrapper.vm.commandPaletteOpen).toBe(false);
  });
});
