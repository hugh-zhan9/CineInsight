import { flushPromises, shallowMount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'GetSettings', 'GetAllTags', 'GetAllDirectories', 'GetStartupError',
  'SyncScanDirectories', 'SyncImageDirectories', 'GetLibraryCounts', 'LogFrontend',
  'SetWindowForeground', 'GetVideosByIDs',
  'GetBackgroundTasks', 'GetIdleSchedulerStatus', 'ListSavedLibraryViews', 'ListCollections', 'ListPeople',
  'GetDatabaseBackendStatus', 'GetDatabaseSwitchStatus', 'ListDownloadTasks', 'RelaunchApp'
].map(name => [name, vi.fn()])));

vi.mock('../wailsjs/go/main/App', () => api);

import App from './App.vue';
import { registerCommands, unregisterCommands } from './utils/commandRegistry.js';
import { feedbackState, resetFeedback, resolveConfirm } from './utils/feedback.js';

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
  api.GetDatabaseSwitchStatus.mockResolvedValue({ running: false, completed: false, failed: false });
  api.ListDownloadTasks.mockResolvedValue([]);
  // jsdom 的 hasFocus 行为随实现变动，前后台用例自己钉住它。
  document.hasFocus = vi.fn(() => true);
  resetFeedback();
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

it('片库的转换事件同步给已挂载的图片页', async () => {
  const wrapper = await mountApp();
  await wrapper.setData({ photosMounted: true });
  const refresh = vi.fn();
  wrapper.findComponent({ name: 'PhotoLibraryPage' }).vm.handleTagPersonConverted = refresh;
  const result = { tag_id: 4, person: { id: 9 } };
  wrapper.findComponent({ name: 'VideoListPage' }).vm.$emit('person-converted', result);
  expect(refresh).toHaveBeenCalledWith(result);
});

it('启动失败时仍可打开只读诊断，不挂载片库或任务中心', async () => {
  api.GetStartupError.mockResolvedValue('数据库未初始化');
  const wrapper = shallowMount(App, { global: { renderStubDefaultSlot: true } });
  mountedWrappers.push(wrapper);
  await flushPromises();
  expect(wrapper.findComponent({ name: 'SystemHealthPanel' }).exists()).toBe(false);
  await wrapper.get('[data-test="startup-open-diagnostics"]').trigger('click');
  expect(wrapper.findComponent({ name: 'SystemHealthPanel' }).props('actionsEnabled')).toBe(false);
  expect(wrapper.findComponent({ name: 'VideoListPage' }).exists()).toBe(false);
  expect(wrapper.findComponent({ name: 'TaskCenterDrawer' }).exists()).toBe(false);
  await wrapper.get('[data-test="startup-close-diagnostics"]').trigger('click');
  expect(wrapper.findComponent({ name: 'SystemHealthPanel' }).exists()).toBe(false);
});

it('顶栏想看入口按需挂载独立片单页', async () => {
  const wrapper = await mountApp();
  expect(wrapper.findComponent({ name: 'WatchlistPage' }).exists()).toBe(false);
  await wrapper.get('[data-test="nav-watchlist"]').trigger('click');
  expect(wrapper.vm.currentPage).toBe('watchlist');
  expect(wrapper.findComponent({ name: 'WatchlistPage' }).exists()).toBe(true);
});

// 榜单页一挂载就会报一次 OpenMovieChartYear（可能起后台抓取），所以必须是
// 点进去才挂载：留在别的页面上不该有那个副作用。
it('顶栏榜单入口按需挂载年度榜单页，切走即卸载', async () => {
  const wrapper = await mountApp();
  expect(wrapper.findComponent({ name: 'MovieChartPage' }).exists()).toBe(false);

  await wrapper.get('[data-test="nav-movie-chart"]').trigger('click');
  expect(wrapper.vm.currentPage).toBe('movie-chart');
  expect(wrapper.findComponent({ name: 'MovieChartPage' }).exists()).toBe(true);

  await wrapper.get('[data-test="nav-watchlist"]').trigger('click');
  expect(wrapper.findComponent({ name: 'MovieChartPage' }).exists()).toBe(false);
});

// 已看页每次挂载都重读一次本地标记表，所以同样按需挂载：切走卸载、切回来重新读，
// 不用自己维护失效。
it('顶栏已看入口按需挂载已看页，切走即卸载', async () => {
  const wrapper = await mountApp();
  expect(wrapper.findComponent({ name: 'WatchedMoviesPage' }).exists()).toBe(false);

  await wrapper.get('[data-test="nav-watched-movies"]').trigger('click');
  expect(wrapper.vm.currentPage).toBe('watched-movies');
  expect(wrapper.findComponent({ name: 'WatchedMoviesPage' }).exists()).toBe(true);

  await wrapper.get('[data-test="nav-movie-chart"]').trigger('click');
  expect(wrapper.findComponent({ name: 'WatchedMoviesPage' }).exists()).toBe(false);
});

describe('扫描目录配置变更', () => {
  it('保存目录配置后自动对一次账', async () => {
    const wrapper = await mountApp();
    expect(api.SyncScanDirectories).not.toHaveBeenCalled();

    wrapper.vm.handleDirectoriesChanged([{ id: 1, path: '/Volumes/think plus/Ellieli-Collection', alias: '' }]);
    await flushPromises();

    expect(api.SyncScanDirectories).toHaveBeenCalledTimes(1);
    // 改了扫描根之后的对账按手动扫描上报（§1.2b：前端只传 startup / manual）。
    expect(api.SyncScanDirectories).toHaveBeenCalledWith('manual');
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

  // D-PC59：「管理」菜单去掉清理审阅后，⌘K 改为走待处理工作台的清理入口（library.openCleanup），
  // 任何页面都能按；它仍然不是命令面板的快捷键。
  it('APP-11 ⌘K 在其他页面先切回片库页，再执行 library.openCleanup', async () => {
    const wrapper = await mountApp();
    const run = vi.fn();
    registerCommands('test-cleanup-host', [{ id: 'library.openCleanup', group: 'action', label: '清理中心', hidden: true, run }]);
    await wrapper.setData({ currentPage: 'insights' });

    const event = pressKey(window, { key: 'k', code: 'KeyK', metaKey: true });
    await flushPromises();

    expect(event.defaultPrevented).toBe(true);
    expect(wrapper.vm.currentPage).toBe('videos');
    expect(run).toHaveBeenCalledTimes(1);
    expect(wrapper.vm.commandPaletteOpen).toBe(false);
    unregisterCommands('test-cleanup-host');
  });

  it('APP-11 ⌘K 已被片库页先接手（preventDefault）时让位，不重复打开', async () => {
    const wrapper = await mountApp();
    const run = vi.fn();
    registerCommands('test-cleanup-host', [{ id: 'library.openCleanup', group: 'action', label: '清理中心', hidden: true, run }]);
    const claim = event => event.preventDefault();
    window.addEventListener('keydown', claim, true);

    pressKey(window, { key: 'k', code: 'KeyK', metaKey: true });
    await flushPromises();

    window.removeEventListener('keydown', claim, true);
    unregisterCommands('test-cleanup-host');
    expect(run).not.toHaveBeenCalled();
    expect(wrapper.vm.currentPage).toBe('videos');
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

describe('顶栏信息架构（APP-14）', () => {
  function groupLabels(wrapper) {
    return wrapper.findAll('[data-test^="nav-group-"]').map(group => ({
      key: group.attributes('data-test').replace('nav-group-', ''),
      label: group.attributes('aria-label'),
      pages: group.findAll('.nav-btn').map(button => button.text())
    }));
  }

  it('APP-14 顶栏按片库、片单、工具三组渲染，组间有分隔，「已看」改名「观影记录」', async () => {
    const wrapper = await mountApp();

    expect(groupLabels(wrapper)).toEqual([
      { key: 'library', label: '片库', pages: ['视频', '人物', '作品集', '图片'] },
      { key: 'lists', label: '片单', pages: ['观看笔记', '想看', '榜单', '观影记录'] },
      { key: 'tools', label: '工具', pages: ['洞察', '场景检索', '视频工作台'] }
    ]);
    expect(wrapper.findAll('.header-nav__divider')).toHaveLength(3);
    expect(wrapper.get('[data-test="nav-settings"]').text()).toBe('设置');
    expect(wrapper.get('.header-nav').text()).not.toContain('已看');
  });

  it('APP-14 「下载」只在开启浏览器插件桥接时出现，关掉后随即隐藏', async () => {
    api.GetSettings.mockResolvedValue({ auto_scan_on_startup: false, theme: 'system', browser_bridge_enabled: true });
    const wrapper = await mountApp();

    expect(wrapper.find('[data-test="nav-downloads"]').exists()).toBe(true);
    await wrapper.get('[data-test="nav-downloads"]').trigger('click');
    expect(wrapper.vm.currentPage).toBe('downloads');

    wrapper.vm.handleSettingsUpdate({ browser_bridge_enabled: false });
    await flushPromises();
    expect(wrapper.find('[data-test="nav-downloads"]').exists()).toBe(false);
  });
});

describe('设置页离开确认（APP-08）', () => {
  async function onDirtySettings() {
    const wrapper = await mountApp();
    await wrapper.get('[data-test="nav-settings"]').trigger('click');
    wrapper.findComponent({ name: 'SettingsPage' }).vm.$emit('update:dirty', true);
    await flushPromises();
    return wrapper;
  }

  it('APP-08 有未保存修改时切页先确认，选「继续编辑」留在设置页', async () => {
    const wrapper = await onDirtySettings();

    await wrapper.get('[data-test="nav-videos"]').trigger('click');
    expect(feedbackState.confirm?.message).toBe('放弃未保存的设置修改？');
    resolveConfirm(false);
    await flushPromises();

    expect(wrapper.vm.currentPage).toBe('settings');
    expect(wrapper.findComponent({ name: 'SettingsPage' }).exists()).toBe(true);
  });

  it('APP-08 选「放弃修改」后切走，脏标记随之清掉', async () => {
    const wrapper = await onDirtySettings();

    await wrapper.get('[data-test="nav-photos"]').trigger('click');
    resolveConfirm(true);
    await flushPromises();

    expect(wrapper.vm.currentPage).toBe('photos');
    expect(wrapper.vm.settingsDirty).toBe(false);
  });

  it('APP-08 命令面板里的导航同样经过确认', async () => {
    const wrapper = await onDirtySettings();

    const pending = wrapper.vm.openVideoFromCommand({ id: 1 });
    expect(feedbackState.confirm).not.toBeNull();
    resolveConfirm(false);
    expect(await pending).toBe(false);
    expect(wrapper.vm.currentPage).toBe('settings');
  });

  it('APP-08 没有未保存修改时直接切页，不弹确认', async () => {
    const wrapper = await mountApp();
    await wrapper.get('[data-test="nav-settings"]').trigger('click');
    wrapper.findComponent({ name: 'SettingsPage' }).vm.$emit('update:dirty', false);

    await wrapper.get('[data-test="nav-videos"]').trigger('click');
    expect(feedbackState.confirm).toBeNull();
    expect(wrapper.vm.currentPage).toBe('videos');
  });
});

describe('启动扫描与扫描摘要', () => {
  let handlers;

  beforeEach(() => {
    handlers = {};
    window.runtime = { EventsOn: (event, handler) => { handlers[event] = handler; return () => { delete handlers[event]; }; } };
  });

  afterEach(() => {
    delete window.runtime;
  });

  it('LIB-08 启动时的全量扫描带 startup 触发来源', async () => {
    api.GetSettings.mockResolvedValue({ auto_scan_on_startup: true, theme: 'system' });
    api.GetAllDirectories.mockResolvedValue([{ id: 1, path: '/Volumes/media', alias: '' }]);
    await mountApp();

    expect(api.SyncScanDirectories).toHaveBeenCalledTimes(1);
    expect(api.SyncScanDirectories).toHaveBeenCalledWith('startup');
  });

  it('LIB-08 library-scan-summary 报告库里有增减或恢复时刷新顶栏计数，没有变化不刷', async () => {
    await mountApp();
    expect(api.GetLibraryCounts).toHaveBeenCalledTimes(1);

    handlers['library-scan-summary']({ trigger: 'startup', result: { added: 0, deleted: 0, restored: 0, stale: 0 } });
    await flushPromises();
    expect(api.GetLibraryCounts).toHaveBeenCalledTimes(1);

    handlers['library-scan-summary']({ trigger: 'manual', result: { added: 2, deleted: 0, restored: 0, stale: 0 } });
    await flushPromises();
    expect(api.GetLibraryCounts).toHaveBeenCalledTimes(2);
  });
});

describe('任务中心与待处理入口（APP-03、META-08、APP-11）', () => {
  it('APP-03 顶栏「任务」打开任务中心抽屉，角标显示运行中数量，有失败时标红点', async () => {
    const wrapper = await mountApp();
    const drawer = wrapper.findComponent({ name: 'TaskCenterDrawer' });
    expect(drawer.props('open')).toBe(false);
    expect(wrapper.find('[data-test="task-center-badge"]').exists()).toBe(false);

    drawer.vm.$emit('badge-change', { running: 3, failed: true });
    await flushPromises();
    expect(wrapper.get('[data-test="task-center-badge"]').text()).toBe('3');
    expect(wrapper.find('[data-test="task-center-failed-dot"]').exists()).toBe(true);

    await wrapper.get('[data-test="open-task-center"]').trigger('click');
    expect(wrapper.findComponent({ name: 'TaskCenterDrawer' }).props('open')).toBe(true);
    wrapper.findComponent({ name: 'TaskCenterDrawer' }).vm.$emit('close');
    await flushPromises();
    expect(wrapper.findComponent({ name: 'TaskCenterDrawer' }).props('open')).toBe(false);
  });

  it('META-08 顶栏「待处理」角标来自工作台的总数，超过 99 显示 99+', async () => {
    const wrapper = await mountApp();
    const hub = wrapper.findComponent({ name: 'PendingWorkHub' });

    hub.vm.$emit('badge-change', 5);
    await flushPromises();
    expect(wrapper.get('[data-test="pending-work-badge"]').text()).toBe('5');

    hub.vm.$emit('badge-change', 120);
    await flushPromises();
    expect(wrapper.get('[data-test="pending-work-badge"]').text()).toBe('99+');

    hub.vm.$emit('badge-change', 0);
    await flushPromises();
    expect(wrapper.find('[data-test="pending-work-badge"]').exists()).toBe(false);

    await wrapper.get('[data-test="open-pending-work"]').trigger('click');
    expect(wrapper.findComponent({ name: 'PendingWorkHub' }).props('open')).toBe(true);
  });

  it('APP-11 工作台「处理」先切到宿主页（人物页按需挂载），再按固定命令 ID 执行', async () => {
    const wrapper = await mountApp();
    const run = vi.fn();
    registerCommands('test-face-host', [{ id: 'people.openFaceReview', group: 'action', label: '人脸审阅', hidden: true, run }]);

    wrapper.findComponent({ name: 'PendingWorkHub' }).vm.$emit('run-command', 'people.openFaceReview', '人脸待命名');
    await flushPromises();

    expect(wrapper.vm.currentPage).toBe('people');
    expect(run).toHaveBeenCalledTimes(1);
    unregisterCommands('test-face-host');
  });

  it('APP-11 宿主页没有注册该命令时给出中文提示，不静默失败', async () => {
    const wrapper = await mountApp();

    wrapper.findComponent({ name: 'PendingWorkHub' }).vm.$emit('run-command', 'photos.openAIReview', '图片 AI 标签待审阅');
    await flushPromises();

    expect(wrapper.vm.currentPage).toBe('photos');
    expect(feedbackState.toasts.map(toast => toast.message).join('\n')).toContain('「图片 AI 标签待审阅」暂时打不开');
  });

  it('APP-03 任务中心里超分产物「在片库中打开」按 ID 取回视频后打开详情', async () => {
    const wrapper = await mountApp();
    const open = vi.fn();
    wrapper.vm.openVideoFromCommand = open;
    api.GetVideosByIDs.mockResolvedValue([{ id: 42, name: 'out.mp4' }]);

    wrapper.findComponent({ name: 'TaskCenterDrawer' }).vm.$emit('open-video', 42);
    await flushPromises();

    expect(api.GetVideosByIDs).toHaveBeenCalledWith([42]);
    expect(open).toHaveBeenCalledWith(expect.objectContaining({ id: 42 }));
  });

  it('APP-03 产物视频不在片库中时说明原因', async () => {
    const wrapper = await mountApp();
    api.GetVideosByIDs.mockResolvedValue([]);

    wrapper.findComponent({ name: 'TaskCenterDrawer' }).vm.$emit('open-video', 42);
    await flushPromises();

    expect(feedbackState.toasts.map(toast => toast.message).join('\n')).toContain('产物视频不在片库中');
  });

  it('MEDIA-10 退出确认对话框常挂载；数据库连不上时顶栏不显示任务与待处理入口', async () => {
    api.GetStartupError.mockResolvedValue('数据库连接失败');
    const wrapper = await mountApp();

    expect(wrapper.findComponent({ name: 'QuitConfirmDialog' }).exists()).toBe(true);
    expect(wrapper.find('[data-test="open-task-center"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="open-pending-work"]').exists()).toBe(false);
  });
});

describe('「待重启」遮罩与启动错误页', () => {
  let handlers;

  beforeEach(() => {
    handlers = {};
    window.runtime = { EventsOn: (event, handler) => { handlers[event] = handler; return () => { delete handlers[event]; }; } };
  });

  afterEach(() => {
    delete window.runtime;
  });

  it('APP-02 后台迁移完成且需要重启时，不在设置页也弹出不可关闭的「立即重启」遮罩', async () => {
    const wrapper = await mountApp();
    expect(wrapper.find('[data-test="relaunch-overlay"]').exists()).toBe(false);

    handlers['database-switch-state']({ completed: false, relaunch_required: false, running: true });
    await flushPromises();
    expect(wrapper.find('[data-test="relaunch-overlay"]').exists()).toBe(false);

    handlers['database-switch-state']({ completed: true, relaunch_required: true, message: '迁移完成，请立即重启应用' });
    await flushPromises();
    const overlay = wrapper.get('[data-test="relaunch-overlay"]');
    expect(overlay.text()).toContain('迁移完成，请立即重启应用');
    // 只有一个出口：没有关闭或取消按钮。
    expect(overlay.findAll('button').map(button => button.text())).toEqual(['立即重启']);

    api.RelaunchApp.mockResolvedValue({ relaunched: true, message: '' });
    await wrapper.get('[data-test="relaunch-now"]').trigger('click');
    await flushPromises();
    expect(api.RelaunchApp).toHaveBeenCalledTimes(1);
  });

  it('APP-02 设置页上报 relaunch-required 同样弹出遮罩；自动重启失败时说明原因且按钮可再次点击', async () => {
    const wrapper = await mountApp();
    await wrapper.setData({ currentPage: 'settings' });
    wrapper.findComponent({ name: 'SettingsPage' }).vm.$emit('relaunch-required', { message: '已切回之前的后端，重启后生效' });
    await flushPromises();
    expect(wrapper.get('[data-test="relaunch-overlay"]').text()).toContain('已切回之前的后端，重启后生效');

    api.RelaunchApp.mockResolvedValue({ relaunched: false, message: '当前不是以应用包运行，请手动重新打开应用' });
    await wrapper.get('[data-test="relaunch-now"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="relaunch-error"]').text()).toContain('请手动重新打开应用');
    expect(wrapper.get('[data-test="relaunch-now"]').attributes('disabled')).toBeUndefined();
  });

  it('APP-02 WebView 重载后挂载时补读到「待重启」终态，直接显示全局遮罩', async () => {
    api.GetDatabaseSwitchStatus.mockResolvedValue({
      running: false, completed: true, relaunch_required: true, target: 'sqlite',
      message: '已改为使用 sqlite，重启应用后生效。切换之后在当前库里产生的改动不会带回 sqlite。'
    });
    const wrapper = await mountApp();
    const overlay = wrapper.get('[data-test="relaunch-overlay"]');
    expect(overlay.text()).toContain('已改为使用 sqlite，重启应用后生效');
    expect(overlay.findAll('button').map(button => button.text())).toEqual(['立即重启']);
  });

  it('APP-02 补读到的不是「完成且要求重启」时不显示遮罩；启动错误时不补读', async () => {
    api.GetDatabaseSwitchStatus.mockResolvedValue({ running: false, failed: true, target: 'sqlite', message: '复制失败' });
    let wrapper = await mountApp();
    expect(api.GetDatabaseSwitchStatus).toHaveBeenCalledTimes(1);
    expect(wrapper.find('[data-test="relaunch-overlay"]').exists()).toBe(false);
    wrapper.unmount();

    api.GetDatabaseSwitchStatus.mockClear();
    api.GetStartupError.mockResolvedValue('打开数据库失败');
    api.GetDatabaseBackendStatus.mockResolvedValue({ backend: 'sqlite' });
    wrapper = await mountApp();
    expect(api.GetDatabaseSwitchStatus).not.toHaveBeenCalled();
    expect(wrapper.find('[data-test="relaunch-overlay"]').exists()).toBe(false);
  });

  it('APP-02 遮罩在有下载在跑或排队时提示「进行中的下载会中断」，下载推送更新它', async () => {
    api.ListDownloadTasks.mockResolvedValue([{ id: 'a', state: 'queued' }]);
    const wrapper = await mountApp();
    expect(wrapper.find('[data-test="relaunch-downloads"]').exists()).toBe(false);
    handlers['database-switch-state']({ completed: true, relaunch_required: true, message: '迁移完成，请立即重启应用' });
    await flushPromises();
    expect(wrapper.get('[data-test="relaunch-downloads"]').text()).toContain('进行中的下载会中断，重启后需回浏览器重新推送');

    handlers['browser-download-tasks']([{ id: 'a', state: 'done' }]);
    await flushPromises();
    expect(wrapper.find('[data-test="relaunch-downloads"]').exists()).toBe(false);
  });

  it('APP-02 没有在跑的下载时遮罩不提示下载', async () => {
    api.ListDownloadTasks.mockResolvedValue([{ id: 'a', state: 'done' }]);
    const wrapper = await mountApp();
    // 遮罩出来之前下载推送不改提示。
    handlers['browser-download-tasks']([{ id: 'b', state: 'running' }]);
    await flushPromises();
    expect(wrapper.find('[data-test="relaunch-downloads"]').exists()).toBe(false);
    handlers['database-switch-state']({ completed: true, relaunch_required: true, message: '迁移完成' });
    await flushPromises();
    expect(wrapper.find('[data-test="relaunch-overlay"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="relaunch-downloads"]').exists()).toBe(false);
  });

  it('APP-10 启动错误页按当前后端给出 SQLite 或 Postgres 的排查提示', async () => {
    api.GetStartupError.mockResolvedValue('打开数据库失败');
    api.GetDatabaseBackendStatus.mockResolvedValue({ backend: 'sqlite' });
    let wrapper = await mountApp();
    expect(wrapper.find('[data-test="startup-error-sqlite-hint"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="startup-error-postgres-hint"]').exists()).toBe(false);
    wrapper.unmount();

    api.GetDatabaseBackendStatus.mockResolvedValue({ backend: 'postgres' });
    wrapper = await mountApp();
    expect(wrapper.find('[data-test="startup-error-postgres-hint"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="startup-error-sqlite-hint"]').exists()).toBe(false);
  });
});

it('集中整理任务从其他页路由到片库，以持久ID留待片库挂载后消费，保留壁纸组件', async () => {
  const wrapper = await mountApp(); await wrapper.setData({ currentPage: 'photos' });
  wrapper.findComponent({ name: 'TaskCenterDrawer' }).vm.$emit('open-consolidation', 42); await flushPromises();
  expect(wrapper.vm.currentPage).toBe('videos');
  expect(wrapper.findComponent({ name: 'VideoListPage' }).props('consolidationRoute')).toMatchObject({ taskID: 42 });
  expect(wrapper.findComponent({ name: 'WallpaperStatusBar' }).exists()).toBe(true);
  wrapper.findComponent({ name: 'VideoListPage' }).vm.$emit('consolidation-opened'); await flushPromises(); expect(wrapper.vm.consolidationRoute).toBeNull();
});

it('集中整理路由的旧导航确认迟到不会覆盖新任务', async () => {
  const wrapper = await mountApp(); let resolveOld;
  vi.spyOn(wrapper.vm, 'navigateTo').mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve; })).mockResolvedValueOnce(true);
  const old = wrapper.vm.openConsolidationTask(41); await wrapper.vm.openConsolidationTask(42); resolveOld(true); await old;
  expect(wrapper.vm.consolidationRoute.taskID).toBe(42);
});

it('观看笔记的书签沿现有片库预览打开，不在页面挂载时启动播放', async () => {
  const wrapper = await mountApp(); const openBookmark = vi.fn(); wrapper.findComponent({ name: 'VideoListPage' }).vm.openBookmark = openBookmark;
  await wrapper.get('[data-test="nav-viewing-notes"]').trigger('click'); expect(openBookmark).not.toHaveBeenCalled();
  const resolution = { status: 'ready', video: { id: 7 }, bookmark: { id: 2, start_ms: 0, end_ms: null }, source_token: 'checked' };
  wrapper.findComponent({ name: 'ViewingNotesPage' }).vm.$emit('open-bookmark', resolution); await flushPromises();
  expect(wrapper.vm.currentPage).toBe('videos'); expect(openBookmark).toHaveBeenCalledWith(resolution);
});

it('观看笔记首次进入才挂载，去原片核对再返回保留同一编辑宿主', async () => {
  const wrapper = await mountApp(); expect(wrapper.findComponent({ name: 'ViewingNotesPage' }).exists()).toBe(false);
  await wrapper.get('[data-test="nav-viewing-notes"]').trigger('click'); const notes = wrapper.findComponent({ name: 'ViewingNotesPage' }).vm;
  await wrapper.get('[data-test="nav-videos"]').trigger('click'); expect(wrapper.findComponent({ name: 'ViewingNotesPage' }).vm).toBe(notes); expect(wrapper.findComponent({ name: 'ViewingNotesPage' }).props('pageActive')).toBe(false);
  await wrapper.get('[data-test="nav-viewing-notes"]').trigger('click'); expect(wrapper.findComponent({ name: 'ViewingNotesPage' }).vm).toBe(notes);
});

it('P-007 片库「在当前筛选中搜场景」带着筛选进入场景检索；点开命中回片库从指定时间预览', async () => {
  const wrapper = await mountApp();
  expect(wrapper.findComponent({ name: 'ScenesPage' }).exists()).toBe(false);
  const filter = { smart_view: 'unwatched', tag_ids: [3] };
  wrapper.findComponent({ name: 'VideoListPage' }).vm.$emit('search-scenes', filter);
  await flushPromises();
  expect(wrapper.vm.currentPage).toBe('scenes');
  const scenes = wrapper.findComponent({ name: 'ScenesPage' });
  expect(scenes.props('scopeRequest').filter).toEqual(filter);

  const openPreviewAt = vi.fn();
  wrapper.findComponent({ name: 'VideoListPage' }).vm.openPreviewAt = openPreviewAt;
  api.GetVideosByIDs.mockResolvedValue([{ id: 7, name: 'a.mp4' }]);
  scenes.vm.$emit('open-video-at', { videoID: 7, startMs: 7_078_000 });
  await flushPromises();
  expect(api.GetVideosByIDs).toHaveBeenCalledWith([7]);
  expect(wrapper.vm.currentPage).toBe('videos');
  expect(openPreviewAt).toHaveBeenCalledWith({ id: 7, name: 'a.mp4' }, 7_078_000);
  expect(wrapper.findComponent({ name: 'ScenesPage' }).exists()).toBe(true);
});

it('P-008 片库新建编辑项目后进入视频工作台并选中；「清理原片…」回片库打开既有的删除确认', async () => {
  const wrapper = await mountApp();
  expect(wrapper.findComponent({ name: 'VideoWorkbenchPage' }).exists()).toBe(false);
  wrapper.findComponent({ name: 'VideoListPage' }).vm.$emit('open-video-edit', 12);
  await flushPromises();
  expect(wrapper.vm.currentPage).toBe('video-workbench');
  const workbench = wrapper.findComponent({ name: 'VideoWorkbenchPage' });
  expect(workbench.props('openRequest').projectID).toBe(12);

  const requestDeleteVideos = vi.fn();
  wrapper.findComponent({ name: 'VideoListPage' }).vm.requestDeleteVideos = requestDeleteVideos;
  api.GetVideosByIDs.mockResolvedValue([{ id: 3, name: 'a.mp4' }]);
  workbench.vm.$emit('cleanup-sources', [3, 3, 0]);
  await flushPromises();
  expect(api.GetVideosByIDs).toHaveBeenCalledWith([3]);
  expect(wrapper.vm.currentPage).toBe('videos');
  expect(requestDeleteVideos).toHaveBeenCalledWith([{ id: 3, name: 'a.mp4' }]);
});

it('P-008 任务中心「在工作台中打开」切到视频工作台并关闭抽屉', async () => {
  const wrapper = await mountApp();
  await wrapper.setData({ taskCenterOpen: true });
  wrapper.findComponent({ name: 'TaskCenterDrawer' }).vm.$emit('open-video-edit', 31);
  await flushPromises();
  expect(wrapper.vm.currentPage).toBe('video-workbench');
  expect(wrapper.vm.taskCenterOpen).toBe(false);
  expect(wrapper.findComponent({ name: 'VideoWorkbenchPage' }).props('openRequest').projectID).toBe(31);
});
