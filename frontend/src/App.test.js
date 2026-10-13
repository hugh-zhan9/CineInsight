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
import BaseModal from './components/ui/BaseModal.vue';
import { findCommand, registerCommands, unregisterCommands } from './utils/commandRegistry.js';
import { confirmAction, feedbackState, resetFeedback, resolveConfirm } from './utils/feedback.js';
import { closeVersionCompare, openVersionCompare, versionCompareState } from './utils/versionCompare.js';
import { resetSessionChanges, sessionChanges } from './utils/sessionChanges.js';

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

// 「片单」是一个页面、四个标签（D-P2-04）：点顶栏「片单」，再点其中一个标签（ListsPage 发 update:tab）。
async function openListsTab(wrapper, tab) {
  await wrapper.get('[data-test="nav-lists"]').trigger('click');
  wrapper.findComponent({ name: 'ListsPage' }).vm.$emit('update:tab', tab);
  await flushPromises();
}

// 从观看区经右上角「管理」进管理区（D-UI-01：管理收进一个入口）。
async function enterManage(wrapper) {
  await wrapper.get('[data-test="open-manage"]').trigger('click');
  await flushPromises();
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

it('洞察页存储体检删除或撤销删除了视频：顶栏计数重取，片库按当前条件重载', async () => {
  const wrapper = await mountApp();
  const reload = vi.fn();
  wrapper.findComponent({ name: 'VideoListPage' }).vm.handleVideosChangedElsewhere = reload;
  await wrapper.setData({ currentPage: 'insights' });
  const callsBefore = api.GetLibraryCounts.mock.calls.length;
  wrapper.findComponent({ name: 'InsightsPage' }).vm.$emit('videos-changed', { removedIDs: [7, 8] });
  await flushPromises();
  expect(api.GetLibraryCounts.mock.calls.length).toBe(callsBefore + 1);
  expect(reload).toHaveBeenCalledWith([7, 8]);
});

it('数据维护撤销了图片的批量编辑：只通知图片库，片库与顶栏计数不动', async () => {
  const wrapper = await mountApp();
  await wrapper.setData({ photosMounted: true, currentPage: 'data-care' });
  const markImages = vi.fn();
  const reload = vi.fn();
  wrapper.findComponent({ name: 'PhotoLibraryPage' }).vm.markImagesChanged = markImages;
  wrapper.findComponent({ name: 'VideoListPage' }).vm.handleVideosChangedElsewhere = reload;
  const callsBefore = api.GetLibraryCounts.mock.calls.length;
  wrapper.findComponent({ name: 'DataCarePage' }).vm.$emit('videos-changed', { removedIDs: [], kind: 'image' });
  await flushPromises();
  expect(markImages).toHaveBeenCalledOnce();
  expect(reload).not.toHaveBeenCalled();
  expect(api.GetLibraryCounts.mock.calls.length).toBe(callsBefore);
});

it('存储体检正在移到废纸篓时不能离开洞察页，结束后可以', async () => {
  const wrapper = await mountApp();
  await wrapper.setData({ currentPage: 'insights' });
  wrapper.findComponent({ name: 'InsightsPage' }).vm.$emit('busy-change', true);
  expect(wrapper.vm.navigateTo('videos')).toBe(false);
  expect(wrapper.vm.currentPage).toBe('insights');
  wrapper.findComponent({ name: 'InsightsPage' }).vm.$emit('busy-change', false);
  expect(wrapper.vm.navigateTo('videos')).toBe(true);
  expect(wrapper.vm.currentPage).toBe('videos');
});

it('片库详情「找相似画面」：切到场景检索页并带上这一刻', async () => {
  const wrapper = await mountApp();
  wrapper.findComponent({ name: 'VideoListPage' }).vm.$emit('find-similar-scenes', { videoID: 3, atMS: 4000, title: '片' });
  await flushPromises();
  expect(wrapper.vm.currentPage).toBe('scenes');
  expect(wrapper.vm.scenesScope).toEqual({ filter: null, similar: { videoID: 3, atMS: 4000, title: '片' }, token: expect.any(Number) });
});

it('启动失败时仍可打开只读诊断，不挂载片库或任务中心', async () => {
  api.GetStartupError.mockResolvedValue('数据库未初始化');
  // 诊断框是 DialogShell，「关闭」在 footer 插槽里：渲染真的 DialogShell，它里面的 BaseModal 等仍是桩。
  const wrapper = shallowMount(App, { global: { renderStubDefaultSlot: true, stubs: { DialogShell: false } } });
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

// 片单（D-P2-04）：一个页面 lists，四个标签。首次进入才挂载、之后只隐藏（观看笔记的编辑与翻页状态留着），
// 各标签何时挂载由 ListsPage 管（见 viewer/ListsPage.test.js）。
it('顶栏「片单」按需挂载片单页，默认「想看」标签；离开后只隐藏，回来是同一个实例', async () => {
  const wrapper = await mountApp();
  expect(wrapper.findComponent({ name: 'ListsPage' }).exists()).toBe(false);
  await wrapper.get('[data-test="nav-lists"]').trigger('click');
  expect(wrapper.vm.currentPage).toBe('lists');
  expect(wrapper.get('[data-test="nav-lists"]').classes()).toContain('active');
  const lists = wrapper.findComponent({ name: 'ListsPage' });
  expect(lists.props()).toMatchObject({ tab: 'watchlist', pageActive: true });

  await wrapper.get('[data-test="nav-recap"]').trigger('click');
  expect(wrapper.findComponent({ name: 'ListsPage' }).vm).toBe(lists.vm);
  expect(wrapper.findComponent({ name: 'ListsPage' }).props('pageActive')).toBe(false);
  expect(wrapper.findComponent({ name: 'ListsPage' }).isVisible()).toBe(false);
  await wrapper.get('[data-test="nav-lists"]').trigger('click');
  expect(wrapper.findComponent({ name: 'ListsPage' }).vm).toBe(lists.vm);
});

it('标签是页面参数：点标签、navigateTo(lists, { tab }) 切标签；不带标签或认不出的标签留在原标签', async () => {
  const wrapper = await mountApp();
  await openListsTab(wrapper, 'watched-movies');
  const lists = () => wrapper.findComponent({ name: 'ListsPage' });
  expect(wrapper.vm.currentPage).toBe('lists');
  expect(lists().props('tab')).toBe('watched-movies');

  expect(wrapper.vm.navigateTo('lists', { tab: 'movie-chart' })).toBe(true);
  await flushPromises();
  expect(lists().props('tab')).toBe('movie-chart');
  wrapper.vm.navigateTo('lists');
  wrapper.vm.navigateTo('lists', { tab: 'no-such-tab' });
  await flushPromises();
  expect(lists().props('tab')).toBe('movie-chart');

  // 离开再从顶栏回来，还是离开时的那个标签。
  await wrapper.get('[data-test="nav-home"]').trigger('click');
  await wrapper.get('[data-test="nav-lists"]').trigger('click');
  expect(lists().props('tab')).toBe('movie-chart');
});

describe('片单的四个旧页面 key 是别名（D-P2-04）', () => {
  it.each([
    ['watchlist'], ['movie-chart'], ['watched-movies'], ['viewing-notes']
  ])('navigateTo(%s) 打开片单的对应标签，currentPage 是 lists', async key => {
    const wrapper = await mountApp();
    expect(wrapper.vm.navigateTo(key)).toBe(true);
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('lists');
    expect(wrapper.vm.listsTab).toBe(key);
    expect(wrapper.findComponent({ name: 'ListsPage' }).props('tab')).toBe(key);
    expect(wrapper.vm.area).toBe('viewer');
  });

  it('命令面板里原有的页面命令（ID 不变）打开对应标签；已在片单上时只换标签', async () => {
    const wrapper = await mountApp();
    await findCommand('nav:page:movie-chart').run();
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('lists');
    expect(wrapper.vm.listsTab).toBe('movie-chart');
    await findCommand('nav:page:watched-movies').run();
    expect(wrapper.vm.listsTab).toBe('watched-movies');
    await findCommand('nav:page:lists').run();
    expect(wrapper.vm.listsTab).toBe('watched-movies');
  });

  it('各页发出的 navigate(旧 key) 落到对应标签：回顾「查看观影日记」、观影记录「去榜单」', async () => {
    const wrapper = await mountApp();
    await wrapper.setData({ currentPage: 'recap' });
    wrapper.findComponent({ name: 'RecapPage' }).vm.$emit('navigate', 'viewing-notes');
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('lists');
    expect(wrapper.vm.listsTab).toBe('viewing-notes');
    wrapper.findComponent({ name: 'ListsPage' }).vm.$emit('navigate', 'movie-chart');
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('lists');
    expect(wrapper.vm.listsTab).toBe('movie-chart');
  });

  it('设置页有未保存修改时别名同样先确认，放弃后才落到对应标签', async () => {
    const wrapper = await mountApp();
    await wrapper.setData({ currentPage: 'settings', settingsDirty: true });
    const result = wrapper.vm.navigateTo('viewing-notes');
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('settings');
    expect(wrapper.vm.listsTab).toBe('watchlist');
    resolveConfirm(true);
    expect(await result).toBe(true);
    expect(wrapper.vm.currentPage).toBe('lists');
    expect(wrapper.vm.listsTab).toBe('viewing-notes');
  });

  it('从片单进整页详情，返回栈记下当时的标签，返回时回到那个标签', async () => {
    const wrapper = await mountApp();
    api.GetVideosByIDs.mockResolvedValue([{ id: 7, name: 'a.mp4' }]);
    await openListsTab(wrapper, 'watched-movies');
    wrapper.findComponent({ name: 'ListsPage' }).vm.$emit('open-video', { id: 7 });
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('video-detail');
    expect(wrapper.vm.detailReturnStack).toEqual([expect.objectContaining({ page: 'lists', params: { tab: 'watched-movies' } })]);
    await wrapper.setData({ listsTab: 'watchlist' });
    wrapper.findComponent({ name: 'VideoDetailPage' }).vm.$emit('back');
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('lists');
    expect(wrapper.vm.listsTab).toBe('watched-movies');
  });
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

  // D-UD-03：⌘K 改为命令面板（⌘⇧P 是同一个面板的别名），清理中心改为 ⌘⇧K。
  it('D-UD-03 ⌘K 与 Ctrl+K 打开命令面板，再按一次关闭；不再打开清理中心', async () => {
    const wrapper = await mountApp();
    const run = vi.fn();
    registerCommands('test-cleanup-host', [{ id: 'library.openCleanup', group: 'action', label: '清理中心', hidden: true, run }]);
    await wrapper.setData({ currentPage: 'insights' });

    const opened = pressKey(window, { key: 'k', code: 'KeyK', metaKey: true });
    await flushPromises();
    expect(opened.defaultPrevented).toBe(true);
    expect(wrapper.vm.commandPaletteOpen).toBe(true);
    expect(run).not.toHaveBeenCalled();
    expect(wrapper.vm.currentPage).toBe('insights');

    pressKey(window, { key: 'k', code: 'KeyK', metaKey: true });
    await flushPromises();
    expect(wrapper.vm.commandPaletteOpen).toBe(false);

    pressKey(window, { key: 'k', code: 'KeyK', ctrlKey: true });
    await flushPromises();
    expect(wrapper.vm.commandPaletteOpen).toBe(true);
    unregisterCommands('test-cleanup-host');
  });

  // D-UD-03：面板没开时，别的对话框（BaseModal 等 role="dialog"）开着、或确认框还没答复，⌘K / ⌘⇧P 都不响应；
  // 面板自己开着时照样再按一次关掉（上面的用例）。
  it('D-UD-03 对话框开着或确认框未答复时 ⌘K / ⌘⇧P 不开面板，关掉后恢复', async () => {
    const wrapper = await mountApp();
    // 也用 shallowMount：测试工具的桩替换是全局的，这里换成 mount 会让还挂着的 App 下次重渲染时挂上真页面。
    const dialog = shallowMount(BaseModal, { attachTo: document.body, slots: { default: '<p>别的对话框</p>' } });
    mountedWrappers.push(dialog);
    expect(document.querySelector('[role="dialog"]')).not.toBe(null);

    const blocked = pressKey(window, { key: 'k', code: 'KeyK', metaKey: true });
    pressKey(window, palette);
    await flushPromises();
    expect(blocked.defaultPrevented).toBe(false);
    expect(wrapper.vm.commandPaletteOpen).toBe(false);
    dialog.unmount();

    const answer = confirmAction({ title: '永久删除', confirmText: '永久删除', level: 'irreversible' });
    pressKey(window, { key: 'k', code: 'KeyK', metaKey: true });
    await flushPromises();
    expect(wrapper.vm.commandPaletteOpen).toBe(false);
    resolveConfirm(false);
    expect(await answer).toBe(false);

    pressKey(window, { key: 'k', code: 'KeyK', metaKey: true });
    await flushPromises();
    expect(wrapper.vm.commandPaletteOpen).toBe(true);
  });

  // D-UD-03：快捷键从 ⌘K 改为 ⌘⇧K。D-P3-02：清理中心三期起是管理侧页面 `cleanup`，⌘⇧K 在任何页面都切过去，
  // 停在上次看的标签上（不另外执行 library.openCleanup）。
  it('D-P3-02 ⌘⇧K 在任何页面都切到清理中心（含片库管理），标签不变', async () => {
    const wrapper = await mountApp();
    const run = vi.fn();
    registerCommands('test-cleanup-host', [{ id: 'library.openCleanup', group: 'action', label: '清理中心', hidden: true, run }]);
    await wrapper.setData({ currentPage: 'insights', cleanupTab: 'image' });

    const event = pressKey(window, { key: 'K', code: 'KeyK', metaKey: true, shiftKey: true });
    await flushPromises();

    expect(event.defaultPrevented).toBe(true);
    expect(wrapper.vm.currentPage).toBe('cleanup');
    expect(wrapper.vm.cleanupTab).toBe('image');
    expect(wrapper.vm.cleanupRequest).toBeNull();
    expect(run).not.toHaveBeenCalled();
    expect(wrapper.vm.commandPaletteOpen).toBe(false);
    expect(wrapper.findComponent({ name: 'CleanupCenterPage' }).exists()).toBe(true);

    // 片库页不再自己接 ⌘⇧K：在片库管理上同样由 App 切过去。
    await wrapper.setData({ currentPage: 'library-manage' });
    pressKey(window, { key: 'K', code: 'KeyK', metaKey: true, shiftKey: true });
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('cleanup');
    unregisterCommands('test-cleanup-host');
  });

  // 对话框开着时不响应（与 ⌘K 同一条规矩）：免得在弹窗背后切走页面。
  it('D-P3-02 ⌘⇧K 在对话框开着时不响应，关掉后照常', async () => {
    const wrapper = await mountApp();
    await wrapper.setData({ currentPage: 'insights' });
    const dialog = shallowMount(BaseModal, { attachTo: document.body, slots: { default: '<p>别的对话框</p>' } });
    mountedWrappers.push(dialog);

    const blocked = pressKey(window, { key: 'K', code: 'KeyK', metaKey: true, shiftKey: true });
    await flushPromises();
    expect(blocked.defaultPrevented).toBe(false);
    expect(wrapper.vm.currentPage).toBe('insights');
    dialog.unmount();

    pressKey(window, { key: 'K', code: 'KeyK', ctrlKey: true, shiftKey: true });
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('cleanup');
  });

  it('APP-11 ⌘⇧K 已被别的处理器先接手（preventDefault）时让位，不重复打开', async () => {
    const wrapper = await mountApp();
    await wrapper.setData({ currentPage: 'videos' });
    const run = vi.fn();
    registerCommands('test-cleanup-host', [{ id: 'library.openCleanup', group: 'action', label: '清理中心', hidden: true, run }]);
    const claim = event => event.preventDefault();
    window.addEventListener('keydown', claim, true);

    pressKey(window, { key: 'K', code: 'KeyK', metaKey: true, shiftKey: true });
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

  it('D-UI-02 / D-P2-04 观看区：首页 · 视频 · 人物 · 作品集 · 图片 · 片单 · 回顾 · 场景检索，右侧待播队列与「管理」', async () => {
    const wrapper = await mountApp();

    expect(wrapper.find('[data-test="nav-area-viewer"]').exists()).toBe(true);
    expect(groupLabels(wrapper)).toEqual([
      { key: 'browse', label: '浏览', pages: ['首页', '视频', '人物', '作品集', '图片'] },
      { key: 'lists', label: '片单', pages: ['片单'] },
      { key: 'review', label: '回顾与检索', pages: ['回顾', '场景检索'] }
    ]);
    expect(wrapper.findAll('.header-nav__divider')).toHaveLength(2);
    expect(wrapper.find('[data-test="nav-back-to-viewer"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="nav-settings"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="open-pending-work"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="open-task-center"]').exists()).toBe(false);
    expect(wrapper.findAll('.header-tools .header-tool-btn').map(button => button.text())).toEqual(['待播队列', '管理']);

    // 「片单」不再是下拉（D-P2-04）：一个页面，四个标签在页里；「已看」改名「观影记录」（D-PC52）。
    const lists = wrapper.get('[data-test="nav-lists"]');
    expect(lists.attributes('aria-haspopup')).toBeUndefined();
    expect(wrapper.find('[data-test="nav-menu-lists"]').exists()).toBe(false);
    expect(wrapper.get('.header-nav').text()).not.toContain('已看');
    await lists.trigger('click');
    expect(wrapper.vm.currentPage).toBe('lists');
    expect(wrapper.findComponent({ name: 'BaseMenu' }).exists()).toBe(false);
  });

  it('D-UI-02 管理区：返回观看 · 管理首页 · 片库管理 · 图片管理 · 人物与作品集 · 待处理 · 清理中心 · 下载 · 视频工作台 · 洞察 · 数据维护 · 任务中心 · 设置', async () => {
    api.GetSettings.mockResolvedValue({ auto_scan_on_startup: false, theme: 'system', browser_bridge_enabled: true });
    const wrapper = await mountApp();
    await enterManage(wrapper);

    expect(wrapper.vm.currentPage).toBe('manage-home');
    expect(wrapper.find('[data-test="nav-area-manage"]').exists()).toBe(true);
    expect(wrapper.get('[data-test="nav-back-to-viewer"]').text()).toBe('返回观看');
    expect(groupLabels(wrapper)).toEqual([
      { key: 'manage-library', label: '片库', pages: ['管理首页', '片库管理', '图片管理', '人物与作品集'] },
      { key: 'manage-review', label: '待处理与清理', pages: ['待处理', '清理中心'] },
      { key: 'manage-tools', label: '工具', pages: ['下载', '视频工作台', '洞察', '数据维护'] },
      { key: 'manage-system', label: '系统', pages: ['任务中心', '设置'] }
    ]);
    expect(wrapper.findAll('.header-nav__divider')).toHaveLength(4);
    expect(wrapper.find('[data-test="nav-videos"]').exists()).toBe(false);
    expect(wrapper.find('.header-tools').exists()).toBe(false);
    expect(wrapper.get('[data-test="nav-manage-home"]').classes()).toContain('active');

    await wrapper.get('[data-test="nav-back-to-viewer"]').trigger('click');
    expect(wrapper.vm.currentPage).toBe('home');
    expect(wrapper.find('[data-test="nav-area-viewer"]').exists()).toBe(true);
  });

  it('APP-14 「下载」只在开启浏览器插件桥接时出现（管理区），关掉后随即隐藏', async () => {
    api.GetSettings.mockResolvedValue({ auto_scan_on_startup: false, theme: 'system', browser_bridge_enabled: true });
    const wrapper = await mountApp();
    await enterManage(wrapper);

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
    await enterManage(wrapper);
    await wrapper.get('[data-test="nav-settings"]').trigger('click');
    wrapper.findComponent({ name: 'SettingsPage' }).vm.$emit('update:dirty', true);
    await flushPromises();
    return wrapper;
  }

  it('APP-08 有未保存修改时切页先确认，选「继续编辑」留在设置页', async () => {
    const wrapper = await onDirtySettings();

    await wrapper.get('[data-test="nav-library-manage"]').trigger('click');
    expect(feedbackState.confirm?.message).toBe('放弃未保存的设置修改？');
    resolveConfirm(false);
    await flushPromises();

    expect(wrapper.vm.currentPage).toBe('settings');
    expect(wrapper.findComponent({ name: 'SettingsPage' }).exists()).toBe(true);
  });

  it('APP-08 「返回观看」同样经过确认', async () => {
    const wrapper = await onDirtySettings();

    await wrapper.get('[data-test="nav-back-to-viewer"]').trigger('click');
    expect(feedbackState.confirm?.message).toBe('放弃未保存的设置修改？');
    resolveConfirm(false);
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('settings');
  });

  it('APP-08 选「放弃修改」后切走，脏标记随之清掉', async () => {
    const wrapper = await onDirtySettings();

    await wrapper.get('[data-test="nav-photos-manage"]').trigger('click');
    resolveConfirm(true);
    await flushPromises();

    expect(wrapper.vm.currentPage).toBe('photos-manage');
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
    await enterManage(wrapper);
    await wrapper.get('[data-test="nav-settings"]').trigger('click');
    wrapper.findComponent({ name: 'SettingsPage' }).vm.$emit('update:dirty', false);

    await wrapper.get('[data-test="nav-library-manage"]').trigger('click');
    expect(feedbackState.confirm).toBeNull();
    expect(wrapper.vm.currentPage).toBe('library-manage');
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

  it('下载查出重复时给一条应用内提示，说清是相同文件还是其他版本', async () => {
    await mountApp();
    handlers['download-duplicate-found']({ task_id: 't1', title: '新片', kind: 'exact' });
    handlers['download-duplicate-found']({ task_id: 't2', title: '另一部', kind: 'version' });
    const texts = feedbackState.toasts.map(toast => toast.message).join('\n');
    expect(texts).toContain('下载的《新片》片库里已有完全相同的文件，可以到「下载」页处理。');
    expect(texts).toContain('下载的《另一部》片库里已有同一部的其他版本');
  });

  it('管理首页「最近的变化」：扫描摘要、下载查重与备份结束都记进本次会话（D-UI-03）', async () => {
    resetSessionChanges();
    await mountApp();
    handlers['download-duplicate-found']({ task_id: 't1', title: '新片', kind: 'exact' });
    handlers['library-scan-summary']({ trigger: 'manual', result: { added: 2, deleted: 0, restored: 0, stale: 0 } });
    handlers['background-tasks'](['backup']);
    handlers['background-tasks']([]);
    const kinds = sessionChanges().map(item => item.kind);
    expect(kinds).toEqual(expect.arrayContaining(['download-duplicate', 'scan', 'backup']));
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
  // D-UI-02：任务入口在管理导航里，叫「任务中心」（用词表 D-UD-14，原「任务与诊断」），走既有命令 action:task-center。
  it('APP-03 管理导航「任务中心」打开任务中心抽屉，角标显示运行中数量，有失败时标红点', async () => {
    const wrapper = await mountApp();
    await enterManage(wrapper);
    const drawer = wrapper.findComponent({ name: 'TaskCenterDrawer' });
    expect(drawer.props('open')).toBe(false);
    expect(wrapper.find('[data-test="task-center-badge"]').exists()).toBe(false);

    drawer.vm.$emit('badge-change', { running: 3, failed: true });
    await flushPromises();
    // D-UD-11：任务指示 = 转圈 + 数量（AppBadge），失败时红点；shallowMount 下读组件 props。
    expect(wrapper.getComponent('[data-test="task-center-badge"]').props('count')).toBe(3);
    expect(wrapper.find('[data-test="task-center-spinner"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="task-center-failed-dot"]').exists()).toBe(true);
    expect(wrapper.get('[data-test="open-task-center"]').attributes('aria-label')).toBe('任务中心，3 个任务运行中，有任务上一轮失败');

    await wrapper.get('[data-test="open-task-center"]').trigger('click');
    await flushPromises();
    expect(wrapper.findComponent({ name: 'TaskCenterDrawer' }).props('open')).toBe(true);
    expect(wrapper.vm.currentPage).toBe('manage-home');
    wrapper.findComponent({ name: 'TaskCenterDrawer' }).vm.$emit('close');
    await flushPromises();
    expect(wrapper.findComponent({ name: 'TaskCenterDrawer' }).props('open')).toBe(false);
  });

  // D-UI-02：观看区的待处理数量挂在「管理」按钮上，来源仍是工作台报上来的总数（GetPendingWorkSummary）。
  it('META-08 观看区「管理」按钮的角标来自工作台的总数，超过 99 显示 99+，点了进管理首页', async () => {
    const wrapper = await mountApp();
    const hub = wrapper.findComponent({ name: 'PendingWorkHub' });
    expect(wrapper.find('[data-test="manage-pending-badge"]').exists()).toBe(false);

    hub.vm.$emit('badge-change', 5);
    await flushPromises();
    expect(wrapper.get('[data-test="manage-pending-badge"]').text()).toBe('5');

    hub.vm.$emit('badge-change', 120);
    await flushPromises();
    expect(wrapper.get('[data-test="manage-pending-badge"]').text()).toBe('99+');

    hub.vm.$emit('badge-change', 0);
    await flushPromises();
    expect(wrapper.find('[data-test="manage-pending-badge"]').exists()).toBe(false);

    await wrapper.get('[data-test="open-manage"]').trigger('click');
    expect(wrapper.vm.currentPage).toBe('manage-home');
    expect(wrapper.findComponent({ name: 'ManageHomePage' }).exists()).toBe(true);
  });

  it('META-08 管理导航「待处理」带同一个角标，点了打开待处理工作台（既有命令 action:pending-work）', async () => {
    const wrapper = await mountApp();
    await enterManage(wrapper);
    const hub = wrapper.findComponent({ name: 'PendingWorkHub' });

    hub.vm.$emit('badge-change', 120);
    await flushPromises();
    expect(wrapper.get('[data-test="pending-work-badge"]').text()).toBe('99+');

    hub.vm.$emit('badge-change', 0);
    await flushPromises();
    expect(wrapper.find('[data-test="pending-work-badge"]').exists()).toBe(false);

    await wrapper.get('[data-test="open-pending-work"]').trigger('click');
    await flushPromises();
    expect(wrapper.findComponent({ name: 'PendingWorkHub' }).props('open')).toBe(true);
    expect(wrapper.vm.currentPage).toBe('manage-home');
  });

  // D-P3-02：管理导航的「清理中心」三期起是页面项（nav-cleanup），点了切到 `cleanup`；命令 action:cleanup 同样打开它。
  it('D-P3-02 管理导航「清理中心」是页面：切到清理中心，选中态跟着走；命令 action:cleanup 也打开它', async () => {
    const wrapper = await mountApp();
    await enterManage(wrapper);
    expect(wrapper.find('[data-test="open-cleanup"]').exists()).toBe(false);

    await wrapper.get('[data-test="nav-cleanup"]').trigger('click');
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('cleanup');
    expect(wrapper.get('[data-test="nav-cleanup"]').classes()).toContain('active');
    expect(wrapper.find('[data-test="nav-area-manage"]').exists()).toBe(true);
    const page = wrapper.findComponent({ name: 'CleanupCenterPage' });
    expect(page.props()).toMatchObject({ pageActive: true, tab: 'video', request: null });

    await wrapper.setData({ currentPage: 'insights' });
    await findCommand('action:cleanup').run();
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('cleanup');
  });

  // D-P2-06：人物观看页只看不改，人脸审阅在管理侧「人物与作品集」（过渡期挂 EntityLibraryPage 的人物那一种）。
  it('APP-11 工作台「处理」先切到宿主页（管理侧人物与作品集按需挂载），再按固定命令 ID 执行', async () => {
    const wrapper = await mountApp();
    const run = vi.fn();
    registerCommands('test-face-host', [{ id: 'people.openFaceReview', group: 'action', label: '人脸审阅', hidden: true, run }]);

    wrapper.findComponent({ name: 'PendingWorkHub' }).vm.$emit('run-command', 'people.openFaceReview', '人脸待命名');
    await flushPromises();

    expect(wrapper.vm.currentPage).toBe('entities-manage');
    expect(wrapper.vm.entitiesManageKind).toBe('person');
    expect(wrapper.findComponent({ name: 'EntityLibraryPage' }).props('entityType')).toBe('person');
    expect(run).toHaveBeenCalledTimes(1);
    unregisterCommands('test-face-host');
  });

  it('APP-11 人物与作品集停在作品集那一种时，人脸审阅先换回人物（同一页也要换），再执行命令', async () => {
    const wrapper = await mountApp();
    await wrapper.setData({ currentPage: 'entities-manage', entitiesManageKind: 'collection' });
    expect(wrapper.findComponent({ name: 'EntityLibraryPage' }).props('entityType')).toBe('collection');
    const run = vi.fn();
    registerCommands('test-face-kind', [{ id: 'people.openFaceReview', group: 'action', label: '人脸审阅', hidden: true, run }]);

    await wrapper.vm.runPendingCommand('people.openFaceReview', '人脸待命名');

    unregisterCommands('test-face-kind');
    expect(wrapper.vm.currentPage).toBe('entities-manage');
    expect(wrapper.findComponent({ name: 'EntityLibraryPage' }).props('entityType')).toBe('person');
    expect(run).toHaveBeenCalledTimes(1);
  });

  it('APP-11 宿主页没有注册该命令时给出中文提示，不静默失败', async () => {
    const wrapper = await mountApp();

    wrapper.findComponent({ name: 'PendingWorkHub' }).vm.$emit('run-command', 'photos.openAIReview', '图片 AI 标签待审阅');
    await flushPromises();

    expect(wrapper.vm.currentPage).toBe('photos-manage');
    expect(feedbackState.toasts.map(toast => toast.message).join('\n')).toContain('「图片 AI 标签待审阅」暂时打不开');
  });

  // D-UI-07：任务中心属于管理侧，仍是「切到片库管理 + 详情抽屉」。
  it('APP-03 任务中心里超分产物「在片库中打开」按 ID 取回视频后切到片库管理打开详情抽屉', async () => {
    const wrapper = await mountApp();
    const openPreview = vi.fn();
    wrapper.findComponent({ name: 'VideoListPage' }).vm.openPreview = openPreview;
    api.GetVideosByIDs.mockResolvedValue([{ id: 42, name: 'out.mp4' }]);

    wrapper.findComponent({ name: 'TaskCenterDrawer' }).vm.$emit('open-video', 42);
    await flushPromises();

    expect(api.GetVideosByIDs).toHaveBeenCalledWith([42]);
    expect(wrapper.vm.currentPage).toBe('library-manage');
    expect(openPreview).toHaveBeenCalledWith(expect.objectContaining({ id: 42 }));
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

// D-P3-02：集中整理任务（原先落在片库页的清理面板上）改为进清理中心的视频标签，带一次性请求打开那个任务。
it('集中整理任务从其他页进清理中心的视频标签，以请求交给清理中心，关掉任务中心，保留壁纸组件', async () => {
  const wrapper = await mountApp(); await wrapper.setData({ currentPage: 'photos', cleanupTab: 'image', taskCenterOpen: true });
  wrapper.findComponent({ name: 'TaskCenterDrawer' }).vm.$emit('open-consolidation', 42); await flushPromises();
  expect(wrapper.vm.currentPage).toBe('cleanup');
  expect(wrapper.vm.taskCenterOpen).toBe(false);
  const page = wrapper.findComponent({ name: 'CleanupCenterPage' });
  expect(page.props('tab')).toBe('video');
  expect(page.props('request')).toMatchObject({ consolidationTaskID: 42, relationId: 0, open: false, token: expect.any(Number) });
  expect(wrapper.findComponent({ name: 'WallpaperStatusBar' }).exists()).toBe(true);
  // 再打开另一个任务：换一个新请求（新 token）。
  const first = page.props('request').token;
  wrapper.findComponent({ name: 'TaskCenterDrawer' }).vm.$emit('open-consolidation', 43); await flushPromises();
  expect(page.props('request')).toMatchObject({ consolidationTaskID: 43 });
  expect(page.props('request').token).not.toBe(first);
});

it('集中整理任务：设置页有未保存修改、用户选「继续编辑」时什么也不发生', async () => {
  const wrapper = await mountApp();
  await wrapper.setData({ currentPage: 'settings', settingsDirty: true, taskCenterOpen: true });
  const pending = wrapper.vm.openConsolidationTask(41);
  await flushPromises();
  expect(feedbackState.confirm).toBeTruthy();
  resolveConfirm(false);
  expect(await pending).toBe(false);
  expect(wrapper.vm.currentPage).toBe('settings');
  expect(wrapper.vm.cleanupRequest).toBeNull();
  expect(wrapper.vm.taskCenterOpen).toBe(true);
});

// D-UI-07：观看笔记属于观看区，书签定位进整页详情并从书签起点起播；片库页的抽屉不再接手。
it('观看笔记的书签进整页详情并从书签起点起播，不在页面挂载时启动播放', async () => {
  const wrapper = await mountApp(); const openBookmark = vi.fn(); wrapper.findComponent({ name: 'VideoListPage' }).vm.openBookmark = openBookmark;
  await openListsTab(wrapper, 'viewing-notes'); expect(wrapper.findComponent({ name: 'VideoDetailPage' }).exists()).toBe(false);
  const resolution = { status: 'ready', video: { id: 7 }, bookmark: { id: 2, start_ms: 0, end_ms: null }, source_token: 'checked' };
  // 观看笔记嵌在片单页里（D-P2-04），书签事件经 ListsPage 原样转出来。
  wrapper.findComponent({ name: 'ListsPage' }).vm.$emit('open-bookmark', resolution); await flushPromises();
  expect(wrapper.vm.currentPage).toBe('video-detail'); expect(openBookmark).not.toHaveBeenCalled();
  expect(wrapper.findComponent({ name: 'VideoDetailPage' }).props()).toMatchObject({ videoId: 7, startAtMs: 0 });
  expect(wrapper.vm.detailReturnStack.map(entry => [entry.page, entry.params])).toEqual([['lists', { tab: 'viewing-notes' }]]);
});

// I-1：书签的整段区间（起点、终点、原片版本）交给详情页的 bookmark，起播位置仍是书签起点。
it('观看笔记的书签把整段区间交给整页详情；返回到上一层详情时不再带书签', async () => {
  const wrapper = await mountApp();
  api.GetVideosByIDs.mockResolvedValue([{ id: 9, name: '9.mp4' }]);
  await openListsTab(wrapper, 'viewing-notes');
  const resolution = { status: 'ready', video: { id: 7 }, bookmark: { id: 2, start_ms: 61_000, end_ms: 75_500 }, source_token: 'v3' };
  wrapper.findComponent({ name: 'ListsPage' }).vm.$emit('open-bookmark', resolution); await flushPromises();
  const detail = () => wrapper.findComponent({ name: 'VideoDetailPage' });
  const passed = () => detail().vm.$.vnode.props;
  expect(passed()['start-at-ms']).toBe(61_000);
  expect(passed().bookmark).toEqual({ start_ms: 61_000, end_ms: 75_500, source_token: 'v3' });

  detail().vm.$emit('open-video', 9); await flushPromises();
  expect(passed().bookmark).toBeNull();
  detail().vm.$emit('back'); await flushPromises();
  expect(detail().props('videoId')).toBe(7);
  expect(passed().bookmark).toBeNull();
  expect(passed()['start-at-ms']).toBeNull();
});

it('没有终点的书签区间 end_ms 为 null', async () => {
  const wrapper = await mountApp();
  await openListsTab(wrapper, 'viewing-notes');
  wrapper.findComponent({ name: 'ListsPage' }).vm.$emit('open-bookmark', { status: 'ready', video: { id: 7 }, bookmark: { id: 2, start_ms: 5000 }, source_token: '' });
  await flushPromises();
  expect(wrapper.findComponent({ name: 'VideoDetailPage' }).vm.$.vnode.props.bookmark).toEqual({ start_ms: 5000, end_ms: null, source_token: '' });
});

// 观看笔记本身「首次进入才挂载、之后只隐藏」由 ListsPage 管（viewer/ListsPage.test.js）；这里钉住它的宿主片单页同样只隐藏。
it('观看笔记所在的片单页首次进入才挂载，去原片核对再返回保留同一编辑宿主', async () => {
  const wrapper = await mountApp(); expect(wrapper.findComponent({ name: 'ListsPage' }).exists()).toBe(false);
  await openListsTab(wrapper, 'viewing-notes'); const lists = wrapper.findComponent({ name: 'ListsPage' }).vm;
  await wrapper.get('[data-test="nav-videos"]').trigger('click'); expect(wrapper.findComponent({ name: 'ListsPage' }).vm).toBe(lists); expect(wrapper.findComponent({ name: 'ListsPage' }).props('pageActive')).toBe(false);
  await openListsTab(wrapper, 'viewing-notes'); expect(wrapper.findComponent({ name: 'ListsPage' }).vm).toBe(lists); expect(wrapper.findComponent({ name: 'ListsPage' }).props('tab')).toBe('viewing-notes');
});

// D-UI-07：场景检索属于观看区，命中进整页详情并从该时刻起播（openVideoAt）。
it('P-007 片库「在当前筛选中搜场景」带着筛选进入场景检索；点开命中进整页详情从指定时间起播', async () => {
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
  expect(wrapper.vm.currentPage).toBe('video-detail');
  expect(wrapper.findComponent({ name: 'VideoDetailPage' }).props()).toMatchObject({ videoId: 7, startAtMs: 7_078_000 });
  expect(openPreviewAt).not.toHaveBeenCalled();
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
  expect(wrapper.vm.currentPage).toBe('library-manage');
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

// 二期（D-P2-01）：「视频」是海报墙，点标签把 { tagId } 作为一次性参数交给它（只按这一个标签筛选），片库管理页不动。
it('回顾页首次进入才挂载；点标签去「视频」海报墙按该标签筛选，点图片去图片页定位，点人物打开人物页', async () => {
  const wrapper = await mountApp();
  expect(wrapper.findComponent({ name: 'RecapPage' }).exists()).toBe(false);
  await wrapper.get('[data-test="nav-recap"]').trigger('click');
  const recap = wrapper.findComponent({ name: 'RecapPage' });
  expect(recap.exists()).toBe(true);
  expect(recap.props('pageActive')).toBe(true);
  const applyTag = vi.fn();
  wrapper.findComponent({ name: 'VideoListPage' }).vm.applyTagFilterCommand = applyTag;
  recap.vm.$emit('open-tag', { id: 5, name: '科幻' });
  await flushPromises();
  expect(wrapper.vm.currentPage).toBe('videos');
  const browse = wrapper.findComponent({ name: 'VideoBrowsePage' });
  expect(browse.props('navigationRequest')).toEqual({ tagId: 5, token: 1 });
  expect(browse.props('pageActive')).toBe(true);
  expect(applyTag).not.toHaveBeenCalled();
  expect(wrapper.findComponent({ name: 'RecapPage' }).props('pageActive')).toBe(false);
  // 已在「视频」上再点一个标签：参数照样交过去，token 换新的，页面据此再套一次。
  recap.vm.$emit('open-tag', 6);
  await flushPromises();
  expect(wrapper.findComponent({ name: 'VideoBrowsePage' }).props('navigationRequest')).toEqual({ tagId: 6, token: 2 });

  await wrapper.setData({ photosMounted: true });
  const locate = vi.fn();
  wrapper.findComponent({ name: 'PhotoLibraryPage' }).vm.locateImage = locate;
  wrapper.findComponent({ name: 'RecapPage' }).vm.$emit('open-image', { id: 11, name: 'p.jpg' });
  await flushPromises();
  expect(wrapper.vm.currentPage).toBe('photos');
  expect(locate).toHaveBeenCalledWith({ id: 11, name: 'p.jpg' });

  wrapper.findComponent({ name: 'RecapPage' }).vm.$emit('open-entity', { type: 'person', id: 8, name: '甲' });
  await flushPromises();
  expect(wrapper.vm.currentPage).toBe('people');
});

it('版本对比播放器按全局状态挂载：openVersionCompare 打开，换一对是新会话，关闭事件收起', async () => {
  closeVersionCompare();
  const wrapper = await mountApp();
  expect(wrapper.findComponent({ name: 'VersionCompareDialog' }).exists()).toBe(false);
  openVersionCompare(3, 8);
  await flushPromises();
  const dialog = wrapper.findComponent({ name: 'VersionCompareDialog' });
  expect(dialog.exists()).toBe(true);
  expect(dialog.props()).toMatchObject({ leftVideoId: 3, rightVideoId: 8 });
  const firstKey = dialog.vm.$.vnode.key;
  openVersionCompare(8, 3);
  await flushPromises();
  const reopened = wrapper.findComponent({ name: 'VersionCompareDialog' });
  expect(reopened.props()).toMatchObject({ leftVideoId: 8, rightVideoId: 3 });
  expect(reopened.vm.$.vnode.key).not.toBe(firstKey);
  reopened.vm.$emit('close');
  await flushPromises();
  expect(versionCompareState.open).toBe(false);
  expect(wrapper.findComponent({ name: 'VersionCompareDialog' }).exists()).toBe(false);
});

it('D-MC-03 完整度「去处理」：视频去片库套用缺失筛选；图片页首次挂载后再套用', async () => {
  const applyPhotoMissing = vi.fn(() => true);
  const wrapper = shallowMount(App, {
    global: { stubs: { PhotoLibraryPage: { name: 'PhotoLibraryPage', template: '<div />', methods: { applyMissingFilter: applyPhotoMissing } } } }
  });
  mountedWrappers.push(wrapper);
  await flushPromises();
  await wrapper.vm.navigateTo('data-care');
  await flushPromises();
  const applyVideoMissing = vi.fn();
  wrapper.findComponent({ name: 'VideoListPage' }).vm.applyMissingFilter = applyVideoMissing;
  wrapper.findComponent({ name: 'DataCarePage' }).vm.$emit('open-library-missing', { kind: 'video', field: 'year' });
  await flushPromises();
  expect(wrapper.vm.currentPage).toBe('library-manage');
  expect(applyVideoMissing).toHaveBeenCalledWith('year');

  expect(wrapper.findComponent({ name: 'PhotoLibraryPage' }).exists()).toBe(false);
  await wrapper.vm.navigateTo('data-care');
  await flushPromises();
  wrapper.findComponent({ name: 'DataCarePage' }).vm.$emit('open-library-missing', { kind: 'image', field: 'gps' });
  await flushPromises();
  expect(wrapper.vm.currentPage).toBe('photos-manage');
  expect(applyPhotoMissing).toHaveBeenCalledWith('gps');
});

describe('观看与管理分区（D-UI-01、D-UI-02、D-UI-09）', () => {
  it('启动进观看首页：片库页常挂载但不可见，首页按需挂载', async () => {
    const wrapper = await mountApp();
    expect(wrapper.vm.currentPage).toBe('home');
    expect(wrapper.vm.area).toBe('viewer');
    expect(wrapper.findComponent({ name: 'HomePage' }).exists()).toBe(true);
    expect(wrapper.findComponent({ name: 'HomePage' }).vm.$.vnode.props['page-active']).toBe(true);
    const library = wrapper.findComponent({ name: 'VideoListPage' });
    expect(library.exists()).toBe(true);
    expect(library.props('pageActive')).toBe(false);
    expect(library.isVisible()).toBe(false);
    expect(wrapper.get('[data-test="nav-home"]').classes()).toContain('active');
  });

  it.each([
    ['home', 'viewer'], ['videos', 'viewer'], ['people', 'viewer'], ['collections', 'viewer'], ['photos', 'viewer'],
    ['lists', 'viewer'],
    ['recap', 'viewer'], ['scenes', 'viewer'], ['video-detail', 'viewer'],
    ['manage-home', 'manage'], ['library-manage', 'manage'], ['photos-manage', 'manage'], ['entities-manage', 'manage'], ['downloads', 'manage'],
    ['video-workbench', 'manage'], ['insights', 'manage'], ['data-care', 'manage'], ['settings', 'manage']
  ])('区域由当前页面推出：%s 属于 %s', async (page, area) => {
    const wrapper = await mountApp();
    await wrapper.setData({ currentPage: page });
    expect(wrapper.vm.area).toBe(area);
    expect(wrapper.find(`[data-test="nav-area-${area}"]`).exists()).toBe(true);
  });

  // 二期（D-P2-01）：「视频」是独立的海报墙（首次进入才挂载、之后只隐藏）；片库页常挂载，只在「片库管理」上显示。
  it('「视频」是海报墙、「片库管理」是常挂载的片库页，各自一个实例；「图片」与「图片管理」共用同一实例', async () => {
    const wrapper = await mountApp();
    const library = wrapper.findComponent({ name: 'VideoListPage' }).vm;
    expect(wrapper.findComponent({ name: 'VideoBrowsePage' }).exists()).toBe(false);
    await wrapper.get('[data-test="nav-videos"]').trigger('click');
    const browse = wrapper.findComponent({ name: 'VideoBrowsePage' });
    expect(browse.props('pageActive')).toBe(true);
    expect(browse.isVisible()).toBe(true);
    expect(wrapper.findComponent({ name: 'VideoListPage' }).props('pageActive')).toBe(false);
    expect(wrapper.findComponent({ name: 'VideoListPage' }).isVisible()).toBe(false);

    await enterManage(wrapper);
    await wrapper.get('[data-test="nav-library-manage"]').trigger('click');
    expect(wrapper.findAllComponents({ name: 'VideoListPage' })).toHaveLength(1);
    expect(wrapper.findComponent({ name: 'VideoListPage' }).vm).toBe(library);
    expect(wrapper.findComponent({ name: 'VideoListPage' }).props('pageActive')).toBe(true);
    expect(wrapper.findComponent({ name: 'VideoListPage' }).isVisible()).toBe(true);
    // 海报墙切走后只隐藏、不卸载（筛选与滚动位置留着）。
    expect(wrapper.findComponent({ name: 'VideoBrowsePage' }).vm).toBe(browse.vm);
    expect(wrapper.findComponent({ name: 'VideoBrowsePage' }).props('pageActive')).toBe(false);
    expect(wrapper.findComponent({ name: 'VideoBrowsePage' }).isVisible()).toBe(false);

    await wrapper.get('[data-test="nav-photos-manage"]').trigger('click');
    const photos = wrapper.findComponent({ name: 'PhotoLibraryPage' }).vm;
    expect(wrapper.findComponent({ name: 'PhotoLibraryPage' }).props('pageActive')).toBe(true);
    expect(wrapper.findComponent({ name: 'VideoListPage' }).props('pageActive')).toBe(false);
    await wrapper.get('[data-test="nav-back-to-viewer"]').trigger('click');
    await wrapper.get('[data-test="nav-photos"]').trigger('click');
    expect(wrapper.findComponent({ name: 'PhotoLibraryPage' }).vm).toBe(photos);
    expect(wrapper.findComponent({ name: 'PhotoLibraryPage' }).props('pageActive')).toBe(true);
  });

  it('片库页不可见时启动扫描照常进行，命令面板与待处理跳转仍能打开片库宿主命令', async () => {
    api.GetSettings.mockResolvedValue({ auto_scan_on_startup: true, theme: 'system' });
    api.GetAllDirectories.mockResolvedValue([{ id: 1, path: '/Volumes/media', alias: '' }]);
    const wrapper = await mountApp();
    expect(wrapper.vm.currentPage).toBe('home');
    expect(api.SyncScanDirectories).toHaveBeenCalledWith('startup');
    expect(api.SyncImageDirectories).toHaveBeenCalledTimes(1);

    const run = vi.fn();
    registerCommands('test-hidden-library', [{ id: 'library.openAIReview', group: 'action', label: 'AI 审阅', hidden: true, run }]);
    wrapper.findComponent({ name: 'PendingWorkHub' }).vm.$emit('run-command', 'library.openAIReview', 'AI 标签待审阅');
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('library-manage');
    expect(run).toHaveBeenCalledTimes(1);

    await wrapper.setData({ currentPage: 'home' });
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'P', code: 'KeyP', metaKey: true, shiftKey: true, cancelable: true }));
    await flushPromises();
    expect(wrapper.vm.commandPaletteOpen).toBe(true);
    unregisterCommands('test-hidden-library');
  });

  it('管理首页的事件接到既有入口：切页、按命令 ID 处理、打开待处理与任务中心', async () => {
    const wrapper = await mountApp();
    await enterManage(wrapper);
    const home = wrapper.findComponent({ name: 'ManageHomePage' });
    // 占位组件还没声明 pageActive，首页工人的版本声明了：看 App 传下去的 vnode props，两种都成立。
    expect(home.vm.$.vnode.props['page-active']).toBe(true);
    home.vm.$emit('open-pending-work');
    await flushPromises();
    expect(wrapper.findComponent({ name: 'PendingWorkHub' }).props('open')).toBe(true);
    home.vm.$emit('open-task-center');
    await flushPromises();
    expect(wrapper.findComponent({ name: 'TaskCenterDrawer' }).props('open')).toBe(true);
    expect(wrapper.findComponent({ name: 'PendingWorkHub' }).props('open')).toBe(false);

    // 图片清理的宿主是清理中心（D-P3-02），切页与换到图片标签同一拍。
    const run = vi.fn();
    registerCommands('test-manage-home', [{ id: 'photos.openCleanup', group: 'action', label: '图片清理', hidden: true, run }]);
    home.vm.$emit('run-command', 'photos.openCleanup', '图片清理');
    await flushPromises();
    unregisterCommands('test-manage-home');
    expect(wrapper.vm.currentPage).toBe('cleanup');
    expect(wrapper.vm.cleanupTab).toBe('image');
    expect(run).toHaveBeenCalledTimes(1);

    await wrapper.setData({ currentPage: 'manage-home' });
    wrapper.findComponent({ name: 'ManageHomePage' }).vm.$emit('navigate', 'insights');
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('insights');
  });
});

describe('「打开一部片」按调用方区域分流（D-UI-07）', () => {
  // 观看区每个调用方：组件名、需要先切到的页面、发出的事件与载荷。
  const viewerCallers = [
    ['HomePage', 'home', 'open-video', 7],
    // D-P2-01：「视频」海报墙的卡片、「追到哪了」与今晚看什么的候选都发 open-video(id)。
    ['VideoBrowsePage', 'videos', 'open-video', 7],
    ['RecapPage', 'recap', 'open-video-id', 7],
    // 片单（D-P2-04）：想看「播放」、观看笔记、观影记录的事件经 ListsPage 转出来。
    ['ListsPage', 'lists', 'open-video-id', 7],
    ['ListsPage', 'lists', 'open-video', { id: 7, name: 'a.mp4' }],
    // I-3 / D-P2-06：人物与作品集观看页点视频交给 App 分流（发视频 ID）。
    ['EntityBrowsePage', 'people', 'open-video', 7],
    ['EntityBrowsePage', 'collections', 'open-video', 7],
    ['PlaybackQueuePanel', 'recap', 'open-video', 7]
  ];
  const manageCallers = [
    ['InsightsPage', 'insights', 'open-video-id', 7],
    ['DataCarePage', 'data-care', 'open-video-id', 7],
    ['VideoWorkbenchPage', 'video-workbench', 'open-video', 7],
    ['TaskCenterDrawer', 'manage-home', 'open-video', 7],
    // 管理侧「人物与作品集」（过渡期挂 EntityLibraryPage）：点视频按管理区分流。
    ['EntityLibraryPage', 'entities-manage', 'open-video', { id: 7, name: 'a.mp4' }]
  ];

  async function onPage(page) {
    const wrapper = await mountApp();
    await wrapper.setData({ currentPage: page });
    await flushPromises();
    const openPreview = vi.fn();
    wrapper.findComponent({ name: 'VideoListPage' }).vm.openPreview = openPreview;
    api.GetVideosByIDs.mockResolvedValue([{ id: 7, name: 'a.mp4' }]);
    return { wrapper, openPreview };
  }

  it.each(viewerCallers)('观看区 %s（%s 页）的 %s 进整页详情', async (name, page, event, payload) => {
    const { wrapper, openPreview } = await onPage(page);
    wrapper.findComponent({ name }).vm.$emit(event, payload);
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('video-detail');
    expect(wrapper.findComponent({ name: 'VideoDetailPage' }).props()).toMatchObject({ videoId: 7, startAtMs: null });
    expect(openPreview).not.toHaveBeenCalled();
    expect(wrapper.vm.detailReturnStack.map(entry => entry.page)).toEqual([page]);
  });

  it.each(manageCallers)('管理区 %s（%s 页）的 %s 仍切到片库管理开抽屉', async (name, page, event, payload) => {
    const { wrapper, openPreview } = await onPage(page);
    wrapper.findComponent({ name }).vm.$emit(event, payload);
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('library-manage');
    expect(openPreview).toHaveBeenCalledWith({ id: 7, name: 'a.mp4' });
    expect(wrapper.findComponent({ name: 'VideoDetailPage' }).exists()).toBe(false);
  });

  it('待播队列不看当前页：在管理区打开也进整页详情，栈里没有可回的观看页，返回回首页', async () => {
    const { wrapper, openPreview } = await onPage('insights');
    wrapper.findComponent({ name: 'PlaybackQueuePanel' }).vm.$emit('open-video', 7);
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('video-detail');
    expect(openPreview).not.toHaveBeenCalled();
    expect(wrapper.vm.detailReturnStack).toEqual([]);
    wrapper.findComponent({ name: 'VideoDetailPage' }).vm.$emit('back');
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('home');
  });

  it('命令面板按当前区域分流：观看区进整页详情，管理区开抽屉', async () => {
    const { wrapper, openPreview } = await onPage('home');
    await wrapper.vm.openVideoFromCommand({ id: 7, name: 'a.mp4' });
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('video-detail');

    await wrapper.setData({ currentPage: 'insights' });
    await wrapper.vm.openVideoFromCommand({ id: 7, name: 'a.mp4' });
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('library-manage');
    expect(openPreview).toHaveBeenCalledWith({ id: 7, name: 'a.mp4' });
  });

  it('整页详情「编辑资料」切到片库管理并在抽屉里打开这部片，返回栈随之清空', async () => {
    const { wrapper, openPreview } = await onPage('home');
    wrapper.findComponent({ name: 'HomePage' }).vm.$emit('open-video', 7);
    await flushPromises();
    expect(wrapper.vm.detailReturnStack).toHaveLength(1);
    wrapper.findComponent({ name: 'VideoDetailPage' }).vm.$emit('edit-in-manage', 7);
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('library-manage');
    expect(openPreview).toHaveBeenCalledWith({ id: 7, name: 'a.mp4' });
    expect(wrapper.vm.detailReturnStack).toEqual([]);
  });

  it('整页详情的标签、人物/作品集与「找相似画面」接到既有入口', async () => {
    const { wrapper } = await onPage('home');
    wrapper.findComponent({ name: 'HomePage' }).vm.$emit('open-video', 7);
    await flushPromises();
    const tag = vi.spyOn(wrapper.vm, 'openLibraryTag').mockResolvedValue();
    const entity = vi.spyOn(wrapper.vm, 'openRecapEntity').mockResolvedValue();
    const similar = vi.spyOn(wrapper.vm, 'openSimilarScenes').mockReturnValue(true);
    await wrapper.vm.$forceUpdate();
    const detail = wrapper.findComponent({ name: 'VideoDetailPage' });
    detail.vm.$emit('open-tag', 3);
    detail.vm.$emit('open-entity', { type: 'person', id: 9, name: '某人' });
    detail.vm.$emit('find-similar', { videoID: 7, atMS: 1000, title: 'a' });
    await flushPromises();
    expect(tag).toHaveBeenCalledWith(3);
    expect(entity).toHaveBeenCalledWith({ type: 'person', id: 9, name: '某人' });
    expect(similar).toHaveBeenCalledWith({ videoID: 7, atMS: 1000, title: 'a' });
  });

  it('视频不在片库里时说明原因，不切页', async () => {
    const { wrapper } = await onPage('recap');
    api.GetVideosByIDs.mockResolvedValue([]);
    wrapper.findComponent({ name: 'RecapPage' }).vm.$emit('open-video-id', 9);
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('recap');
    expect(feedbackState.toasts.map(toast => toast.message).join('\n')).toContain('不在片库中');
  });

  it('首页点人物打开人物页并定位到这个人', async () => {
    const { wrapper } = await onPage('home');
    wrapper.findComponent({ name: 'HomePage' }).vm.$emit('open-person', { id: 8, name: '甲' });
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('people');
  });
});

describe('页面参数交接：人物 / 作品集的关键词、场景检索的查询（命令面板「查看全部」）', () => {
  const browsePage = (wrapper, kind) => wrapper.findAllComponents({ name: 'EntityBrowsePage' }).find(page => page.props('kind') === kind);

  it('navigateTo(people|collections, { keyword })：首次进入时挂载那一拍就带着关键词；离开这一页时清空', async () => {
    const wrapper = await mountApp();
    const result = wrapper.vm.navigateTo('people', { keyword: '梁' });
    expect(result).toBe(true);
    expect(wrapper.vm.currentPage).toBe('people');
    expect(wrapper.vm.entityKeywordRequests.person).toEqual({ keyword: '梁', token: expect.any(Number) });
    await flushPromises();
    // 挂载时拿到的就是这份请求。
    expect(browsePage(wrapper, 'person').vm.$.vnode.props['keyword-request']).toEqual({ keyword: '梁', token: expect.any(Number) });

    wrapper.vm.navigateTo('collections', { keyword: '指环' });
    await flushPromises();
    expect(wrapper.vm.entityKeywordRequests.collection).toEqual({ keyword: '指环', token: expect.any(Number) });
    expect(browsePage(wrapper, 'collection').vm.$.vnode.props['keyword-request']).toMatchObject({ keyword: '指环' });
    // 离开人物页时它的请求清空：之后重新挂载（离开管理侧后换代）不会把旧关键词再搜一遍。
    expect(wrapper.vm.entityKeywordRequests.person).toBeNull();
    await wrapper.setData({ currentPage: 'entities-manage' });
    await wrapper.setData({ currentPage: 'people' });
    expect(browsePage(wrapper, 'person').vm.$.vnode.props['keyword-request']).toBeNull();
  });

  it('已在人物页上再交一次（同一个词也算）：换一份新请求交给同一个页面', async () => {
    const wrapper = await mountApp();
    wrapper.vm.navigateTo('people', { keyword: '梁' });
    await flushPromises();
    const page = browsePage(wrapper, 'person');
    const seen = [];
    const stop = page.vm.$watch(() => page.vm.$props.keywordRequest, value => { if (value) seen.push(value); });

    wrapper.vm.navigateTo('people', { keyword: '梁' });
    await flushPromises();
    wrapper.vm.navigateTo('people', { keyword: '梁' });
    await flushPromises();
    stop();
    expect(seen).toHaveLength(2);
    expect(seen[0].token).not.toBe(seen[1].token);
    expect(seen.map(item => item.keyword)).toEqual(['梁', '梁']);
    expect(browsePage(wrapper, 'person').vm).toBe(page.vm);
  });

  it('不带关键词进人物页不发请求；别的参数不认', async () => {
    const wrapper = await mountApp();
    wrapper.vm.navigateTo('people');
    wrapper.vm.navigateTo('collections', { tab: 'x' });
    expect(wrapper.vm.entityKeywordRequests).toEqual({ person: null, collection: null });
  });

  it('navigateTo(scenes, { query, mode } | { image })：场景检索按全部视频直接检索，每次交接 token 递增', async () => {
    const wrapper = await mountApp();
    wrapper.vm.navigateTo('scenes', { query: '你好', mode: 'dialogue' });
    expect(wrapper.vm.currentPage).toBe('scenes');
    const first = wrapper.vm.scenesScope;
    expect(first).toEqual({ filter: null, query: '你好', mode: 'dialogue', token: expect.any(Number) });
    await flushPromises();
    expect(wrapper.findComponent({ name: 'ScenesPage' }).props('scopeRequest')).toEqual(first);

    const image = { dataURL: 'data:image/jpeg;base64,AAAA', name: 'a.jpg' };
    wrapper.vm.navigateTo('scenes', { image });
    const second = wrapper.vm.scenesScope;
    expect(second).toEqual({ filter: null, image, token: expect.any(Number) });
    expect(second.token).toBeGreaterThan(first.token);
    await flushPromises();
    expect(wrapper.findComponent({ name: 'ScenesPage' }).props('scopeRequest')).toEqual(second);

    // 同样的查询再交一次也是新的一份（页面据此重新检索）。
    wrapper.vm.navigateTo('scenes', { query: '你好', mode: 'dialogue' });
    expect(wrapper.vm.scenesScope).not.toBe(first);
    expect(wrapper.vm.scenesScope.token).toBeGreaterThan(second.token);
  });

  it('不带查询进场景检索（顶栏、片库「在当前筛选中搜场景」）不动现有范围', async () => {
    const wrapper = await mountApp();
    const filter = { tag_ids: [3] };
    wrapper.vm.openScenesWithFilter(filter);
    const scope = wrapper.vm.scenesScope;
    await wrapper.setData({ currentPage: 'home' });
    wrapper.vm.navigateTo('scenes');
    wrapper.vm.navigateTo('scenes', { unrelated: true });
    expect(wrapper.vm.scenesScope).toBe(scope);
  });

  it('命令面板「查看全部」经 navigate 落到对应页面', async () => {
    const wrapper = await mountApp();
    const navigate = wrapper.findComponent({ name: 'CommandPalette' }).props('navigate');
    navigate('collections', { keyword: '指环' });
    expect(wrapper.vm.currentPage).toBe('collections');
    expect(wrapper.vm.entityKeywordRequests.collection.keyword).toBe('指环');
    navigate('scenes', { query: '画面', mode: 'visual' });
    expect(wrapper.vm.scenesScope).toMatchObject({ filter: null, query: '画面', mode: 'visual' });
  });
});

describe('人物与作品集：观看页只看、编辑去管理侧（D-P2-06）', () => {
  it('人物、作品集观看页首次进入才挂载，之后只隐藏：切走再回来还是同一个实例，pageActive 跟着走', async () => {
    const wrapper = await mountApp();
    expect(wrapper.findAllComponents({ name: 'EntityBrowsePage' })).toHaveLength(0);

    await wrapper.get('[data-test="nav-people"]').trigger('click');
    const people = wrapper.findComponent({ name: 'EntityBrowsePage' });
    expect(people.props()).toMatchObject({ kind: 'person', pageActive: true });
    const instance = people.vm;

    await wrapper.get('[data-test="nav-collections"]').trigger('click');
    const pages = wrapper.findAllComponents({ name: 'EntityBrowsePage' });
    expect(pages.map(page => [page.props('kind'), page.props('pageActive'), page.isVisible()])).toEqual([
      ['person', false, false], ['collection', true, true]
    ]);
    expect(wrapper.vm.cinemaSurface).toBe(true);

    await wrapper.get('[data-test="nav-people"]').trigger('click');
    const again = wrapper.findAllComponents({ name: 'EntityBrowsePage' }).find(page => page.props('kind') === 'person');
    expect(again.vm).toBe(instance);
    expect(again.props('pageActive')).toBe(true);
    // 观看侧不再挂编辑页。
    expect(wrapper.findComponent({ name: 'EntityLibraryPage' }).exists()).toBe(false);
  });

  it('离开管理侧人物与作品集后，观看侧两页卸掉，下次进入是新实例（那边可能改过数据）', async () => {
    const wrapper = await mountApp();
    await wrapper.setData({ currentPage: 'people' });
    await wrapper.setData({ currentPage: 'collections' });
    const before = wrapper.findAllComponents({ name: 'EntityBrowsePage' }).find(page => page.props('kind') === 'person').vm;
    await wrapper.setData({ currentPage: 'entities-manage' });
    expect(wrapper.findAllComponents({ name: 'EntityBrowsePage' })).toHaveLength(2);

    await wrapper.setData({ currentPage: 'people' });
    const pages = wrapper.findAllComponents({ name: 'EntityBrowsePage' });
    expect(pages.map(page => page.props('kind'))).toEqual(['person']);
    expect(pages[0].vm).not.toBe(before);
    expect(pages[0].props('pageActive')).toBe(true);

    // 不经过管理页的来回不重挂。
    const kept = pages[0].vm;
    await wrapper.setData({ currentPage: 'home' });
    await wrapper.setData({ currentPage: 'people' });
    expect(wrapper.findComponent({ name: 'EntityBrowsePage' }).vm).toBe(kept);
  });

  it('命令面板、首页打开人物 / 作品集：落到观看页并把要打开的对象交给它', async () => {
    const wrapper = await mountApp();
    const pending = wrapper.vm.openEntityFromCommand('collection', 5, '旅行');
    expect(wrapper.vm.currentPage).toBe('collections');
    expect(wrapper.vm.entityFocus.collection).toEqual({ id: 5, name: '旅行' });
    await pending;
    expect(wrapper.vm.entityFocus.collection).toBeNull();
    expect(wrapper.findComponent({ name: 'EntityBrowsePage' }).props('kind')).toBe('collection');
  });

  it('「从下一部播放」进整页详情并带上起播位置；相关图片去图片页定位', async () => {
    const wrapper = await mountApp();
    api.GetVideosByIDs.mockResolvedValue([{ id: 31, name: 'part3.mp4' }]);
    await wrapper.setData({ currentPage: 'collections' });
    wrapper.findComponent({ name: 'EntityBrowsePage' }).vm.$emit('play-video', { id: 31, startAtMs: 754000 });
    await flushPromises();
    expect(api.GetVideosByIDs).toHaveBeenCalledWith([31]);
    expect(wrapper.vm.currentPage).toBe('video-detail');
    expect(wrapper.findComponent({ name: 'VideoDetailPage' }).props()).toMatchObject({ videoId: 31, startAtMs: 754000 });
    expect(wrapper.vm.detailReturnStack.map(entry => entry.page)).toEqual(['collections']);

    await wrapper.setData({ currentPage: 'people', photosMounted: true });
    const locate = vi.fn();
    wrapper.findComponent({ name: 'PhotoLibraryPage' }).vm.locateImage = locate;
    const person = wrapper.findAllComponents({ name: 'EntityBrowsePage' }).find(page => page.props('kind') === 'person');
    person.vm.$emit('open-image', { id: 11, name: 'p.jpg' });
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('photos');
    expect(locate).toHaveBeenCalledWith({ id: 11, name: 'p.jpg' });
  });

  it('「在管理中编辑」去管理侧人物与作品集，挂上对应那一种并定位到这个对象（置一次即清空）', async () => {
    const wrapper = await mountApp();
    await wrapper.setData({ currentPage: 'collections' });
    wrapper.findComponent({ name: 'EntityBrowsePage' }).vm.$emit('edit-in-manage', { kind: 'collection', id: 5, name: '旅行' });
    expect(wrapper.vm.currentPage).toBe('entities-manage');
    expect(wrapper.vm.area).toBe('manage');
    expect(wrapper.vm.entitiesManageKind).toBe('collection');
    expect(wrapper.vm.entitiesManageFocus).toEqual({ id: 5, name: '旅行' });
    await flushPromises();
    expect(wrapper.vm.entitiesManageFocus).toBeNull();
    const manage = wrapper.findComponent({ name: 'EntityLibraryPage' });
    expect(manage.props('entityType')).toBe('collection');
    expect(wrapper.get('[data-test="nav-entities-manage"]').classes()).toContain('active');
    expect(wrapper.vm.cinemaSurface).toBe(false);
  });

  it('空列表的「去人物与作品集管理」只切过去并挂上那一种，不定位', async () => {
    const wrapper = await mountApp();
    await wrapper.setData({ currentPage: 'people' });
    await wrapper.vm.openEntityInManage({ kind: 'person' });
    expect(wrapper.vm.currentPage).toBe('entities-manage');
    expect(wrapper.vm.entitiesManageKind).toBe('person');
    expect(wrapper.vm.entitiesManageFocus).toBeNull();
  });

  it('管理侧人物与作品集：页签切换人物 / 作品集，换一种就重新挂载那一种的编辑页', async () => {
    const wrapper = await mountApp();
    await enterManage(wrapper);
    await wrapper.get('[data-test="nav-entities-manage"]').trigger('click');
    const tabs = wrapper.findComponent({ name: 'AppTabs' });
    expect(tabs.props('tabs').map(tab => [tab.key, tab.label])).toEqual([['person', '人物'], ['collection', '作品集']]);
    expect(tabs.props('modelValue')).toBe('person');
    const first = wrapper.findComponent({ name: 'EntityLibraryPage' });
    expect(first.props('entityType')).toBe('person');

    tabs.vm.$emit('update:modelValue', 'collection');
    await flushPromises();
    const second = wrapper.findComponent({ name: 'EntityLibraryPage' });
    expect(second.props('entityType')).toBe('collection');
    expect(second.vm).not.toBe(first.vm);
    expect(wrapper.findComponent({ name: 'AppTabs' }).props('modelValue')).toBe('collection');
  });
});

describe('整页详情的返回栈（D-UI-10）', () => {
  async function viewerApp() {
    const wrapper = await mountApp();
    api.GetVideosByIDs.mockImplementation(ids => Promise.resolve(ids.map(id => ({ id, name: `${id}.mp4` }))));
    return wrapper;
  }
  const mainView = wrapper => wrapper.get('.main-view').element;
  const detail = wrapper => wrapper.findComponent({ name: 'VideoDetailPage' });
  // jsdom 不排版：滚动容器的内容高度由用例指定，模拟回到的页面内容随数据加载长高（I-2）。
  function stubContentHeight(wrapper, height) {
    const el = mainView(wrapper);
    const box = { height, viewport: 600 };
    Object.defineProperty(el, 'scrollHeight', { configurable: true, get: () => box.height });
    Object.defineProperty(el, 'clientHeight', { configurable: true, get: () => box.viewport });
    return box;
  }

  it('从回顾打开详情：详情从顶部看起；「返回」回到回顾并恢复滚动位置', async () => {
    const wrapper = await viewerApp();
    stubContentHeight(wrapper, 5000);
    await wrapper.get('[data-test="nav-recap"]').trigger('click');
    mainView(wrapper).scrollTop = 1200;
    wrapper.findComponent({ name: 'RecapPage' }).vm.$emit('open-video-id', 7);
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('video-detail');
    expect(mainView(wrapper).scrollTop).toBe(0);
    expect(wrapper.vm.detailReturnStack).toEqual([{ page: 'recap', params: null, scrollTop: 1200 }]);

    detail(wrapper).vm.$emit('back');
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('recap');
    expect(mainView(wrapper).scrollTop).toBe(1200);
    expect(wrapper.vm.detailReturnStack).toEqual([]);
  });

  it('相关推荐多层压栈，返回逐层回到上一部片（恢复各自的滚动）再回来源页', async () => {
    const wrapper = await viewerApp();
    stubContentHeight(wrapper, 5000);
    mainView(wrapper).scrollTop = 300;
    wrapper.findComponent({ name: 'HomePage' }).vm.$emit('open-video', 1);
    await flushPromises();
    mainView(wrapper).scrollTop = 800;
    detail(wrapper).vm.$emit('open-video', 2);
    await flushPromises();
    expect(detail(wrapper).props('videoId')).toBe(2);
    expect(mainView(wrapper).scrollTop).toBe(0);
    mainView(wrapper).scrollTop = 50;
    detail(wrapper).vm.$emit('open-video', 3);
    await flushPromises();
    expect(wrapper.vm.detailReturnStack.map(entry => entry.params?.videoId || entry.page)).toEqual(['home', 1, 2]);

    // 回到上一层详情：它重新挂载、重新取数，报 ready 之前不动滚动位置。
    mainView(wrapper).scrollTop = 999;
    detail(wrapper).vm.$emit('back');
    await flushPromises();
    expect(detail(wrapper).props()).toMatchObject({ videoId: 2, startAtMs: null });
    expect(mainView(wrapper).scrollTop).toBe(999);
    detail(wrapper).vm.$emit('ready');
    expect(mainView(wrapper).scrollTop).toBe(50);
    detail(wrapper).vm.$emit('back');
    await flushPromises();
    expect(detail(wrapper).props('videoId')).toBe(1);
    detail(wrapper).vm.$emit('ready');
    expect(mainView(wrapper).scrollTop).toBe(800);
    detail(wrapper).vm.$emit('back');
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('home');
    expect(mainView(wrapper).scrollTop).toBe(300);
  });

  it('同一部片不重复压栈：再点回栈里已有的片，返回回到当初打开它的地方', async () => {
    const wrapper = await viewerApp();
    await wrapper.get('[data-test="nav-scenes"]').trigger('click');
    wrapper.findComponent({ name: 'ScenesPage' }).vm.$emit('open-video-at', { videoID: 1, startMs: 5000 });
    await flushPromises();
    expect(detail(wrapper).props()).toMatchObject({ videoId: 1, startAtMs: 5000 });
    detail(wrapper).vm.$emit('open-video', 2);
    await flushPromises();
    detail(wrapper).vm.$emit('open-video', 1);
    await flushPromises();
    expect(detail(wrapper).props('videoId')).toBe(1);
    expect(wrapper.vm.detailReturnStack.map(entry => entry.page)).toEqual(['scenes']);
    detail(wrapper).vm.$emit('open-video', 1);
    await flushPromises();
    expect(wrapper.vm.detailReturnStack.map(entry => entry.page)).toEqual(['scenes']);

    detail(wrapper).vm.$emit('back');
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('scenes');
  });

  it('栈最多 20 层，超出丢最早的', async () => {
    const wrapper = await viewerApp();
    wrapper.findComponent({ name: 'HomePage' }).vm.$emit('open-video', 1);
    await flushPromises();
    for (let id = 2; id <= 22; id++) {
      detail(wrapper).vm.$emit('open-video', id);
      await flushPromises();
    }
    const stack = wrapper.vm.detailReturnStack;
    expect(stack).toHaveLength(20);
    expect(stack[0].params.videoId).toBe(2);
    expect(stack[19].params.videoId).toBe(21);
  });

  it('离开观看区（进管理）时清空；回到别的观看页同样清空，下次从那里重新压栈', async () => {
    const wrapper = await viewerApp();
    wrapper.findComponent({ name: 'HomePage' }).vm.$emit('open-video', 1);
    await flushPromises();
    detail(wrapper).vm.$emit('open-video', 2);
    await flushPromises();
    expect(wrapper.vm.detailReturnStack).toHaveLength(2);

    await enterManage(wrapper);
    expect(wrapper.vm.area).toBe('manage');
    expect(wrapper.vm.detailReturnStack).toEqual([]);

    await wrapper.get('[data-test="nav-back-to-viewer"]').trigger('click');
    wrapper.findComponent({ name: 'HomePage' }).vm.$emit('open-video', 1);
    await flushPromises();
    await wrapper.get('[data-test="nav-recap"]').trigger('click');
    await flushPromises();
    expect(wrapper.vm.detailReturnStack).toEqual([]);
    wrapper.findComponent({ name: 'RecapPage' }).vm.$emit('open-video-id', 3);
    await flushPromises();
    expect(wrapper.vm.detailReturnStack.map(entry => entry.page)).toEqual(['recap']);
  });

  it('每次打开或返回详情都是一次新挂载，参数跟着换', async () => {
    const wrapper = await viewerApp();
    wrapper.findComponent({ name: 'HomePage' }).vm.$emit('open-video', 1);
    await flushPromises();
    const first = detail(wrapper).vm;
    detail(wrapper).vm.$emit('open-video', 2);
    await flushPromises();
    expect(detail(wrapper).vm).not.toBe(first);
    expect(detail(wrapper).props('videoId')).toBe(2);
  });
});

describe('返回到重新挂载的页面时等内容长够再恢复滚动（I-2）', () => {
  // 只假 rAF 与 Date：flushPromises 仍走真的 setTimeout。
  beforeEach(() => vi.useFakeTimers({ toFake: ['requestAnimationFrame', 'cancelAnimationFrame', 'Date'] }));
  afterEach(() => vi.useRealTimers());

  async function fromPage(page) {
    const wrapper = await mountApp();
    api.GetVideosByIDs.mockImplementation(ids => Promise.resolve(ids.map(id => ({ id, name: `${id}.mp4` }))));
    const el = wrapper.get('.main-view').element;
    const box = { height: 4000, viewport: 600 };
    Object.defineProperty(el, 'scrollHeight', { configurable: true, get: () => box.height });
    Object.defineProperty(el, 'clientHeight', { configurable: true, get: () => box.viewport });
    await wrapper.setData({ currentPage: page });
    el.scrollTop = 2500;
    return { wrapper, el, box };
  }

  async function openAndComeBack(wrapper, box, emitter) {
    emitter();
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('video-detail');
    // 回到的页面重新挂载，数据还没回来：内容只有一屏高。
    box.height = 600;
    wrapper.findComponent({ name: 'VideoDetailPage' }).vm.$emit('back');
    await flushPromises();
  }

  it.each([
    ['lists', 'ListsPage', 'open-video', { id: 7 }],
    ['people', 'EntityBrowsePage', 'open-video', 7],
    ['collections', 'EntityBrowsePage', 'open-video', 7]
  ])('%s：内容长到够高才放回滚动位置', async (page, name, event, payload) => {
    const { wrapper, el, box } = await fromPage(page);
    await openAndComeBack(wrapper, box, () => wrapper.findComponent({ name }).vm.$emit(event, payload));
    expect(wrapper.vm.currentPage).toBe(page);
    expect(el.scrollTop).toBe(0);

    vi.advanceTimersByTime(200);
    expect(el.scrollTop).toBe(0);
    box.height = 2600;
    vi.advanceTimersByTime(32);
    expect(el.scrollTop).toBe(0);
    box.height = 3200;
    vi.advanceTimersByTime(32);
    expect(el.scrollTop).toBe(2500);
  });

  it('最多等 3 秒，到点内容还不够就作罢，之后长高也不再滚', async () => {
    const { wrapper, el, box } = await fromPage('lists');
    await openAndComeBack(wrapper, box, () => wrapper.findComponent({ name: 'ListsPage' }).vm.$emit('open-video', { id: 7 }));
    vi.advanceTimersByTime(3100);
    box.height = 5000;
    vi.advanceTimersByTime(500);
    expect(el.scrollTop).toBe(0);
  });

  it('等待中切到别的页面就取消，不把滚动位置放到新页面上', async () => {
    const { wrapper, el, box } = await fromPage('lists');
    await openAndComeBack(wrapper, box, () => wrapper.findComponent({ name: 'ListsPage' }).vm.$emit('open-video', { id: 7 }));
    await wrapper.get('[data-test="nav-recap"]').trigger('click');
    box.height = 5000;
    vi.advanceTimersByTime(500);
    expect(el.scrollTop).toBe(0);
  });
});

describe('「打开一部片」只让最新的一次生效（M-1）', () => {
  function deferredVideos() {
    const pending = [];
    api.GetVideosByIDs.mockImplementation(ids => new Promise(resolve => pending.push({ ids, resolve })));
    return pending;
  }

  it('取视频期间用户进了管理区：取回来后不再把人拽回整页详情', async () => {
    const wrapper = await mountApp();
    const pending = deferredVideos();
    wrapper.findComponent({ name: 'HomePage' }).vm.$emit('open-video', 7);
    await flushPromises();
    await wrapper.get('[data-test="open-manage"]').trigger('click');
    pending[0].resolve([{ id: 7, name: '7.mp4' }]);
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('manage-home');
    expect(wrapper.findComponent({ name: 'VideoDetailPage' }).exists()).toBe(false);
  });

  it('管理侧的打开被切页取代时同样作废：不切回片库管理、不开抽屉', async () => {
    const wrapper = await mountApp();
    await wrapper.setData({ currentPage: 'insights' });
    const openPreview = vi.fn();
    wrapper.findComponent({ name: 'VideoListPage' }).vm.openPreview = openPreview;
    const pending = deferredVideos();
    wrapper.findComponent({ name: 'InsightsPage' }).vm.$emit('open-video-id', 7);
    await flushPromises();
    await wrapper.get('[data-test="nav-back-to-viewer"]').trigger('click');
    pending[0].resolve([{ id: 7, name: '7.mp4' }]);
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('home');
    expect(openPreview).not.toHaveBeenCalled();
  });

  it('先后两次打开：后一次先回来就是它，前一次迟到也不覆盖', async () => {
    const wrapper = await mountApp();
    const pending = deferredVideos();
    wrapper.findComponent({ name: 'HomePage' }).vm.$emit('open-video', 1);
    wrapper.findComponent({ name: 'HomePage' }).vm.$emit('open-video', 2);
    await flushPromises();
    pending[1].resolve([{ id: 2, name: '2.mp4' }]);
    await flushPromises();
    expect(wrapper.findComponent({ name: 'VideoDetailPage' }).props('videoId')).toBe(2);
    pending[0].resolve([{ id: 1, name: '1.mp4' }]);
    await flushPromises();
    expect(wrapper.findComponent({ name: 'VideoDetailPage' }).props('videoId')).toBe(2);
    expect(wrapper.vm.detailReturnStack.map(entry => entry.page)).toEqual(['home']);
  });

  it('先后两次打开：前一次先回来也作废，等后一次回来才打开它', async () => {
    const wrapper = await mountApp();
    const pending = deferredVideos();
    wrapper.findComponent({ name: 'HomePage' }).vm.$emit('open-video', 1);
    wrapper.findComponent({ name: 'HomePage' }).vm.$emit('open-video', 2);
    await flushPromises();
    pending[0].resolve([{ id: 1, name: '1.mp4' }]);
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('home');
    pending[1].resolve([{ id: 2, name: '2.mp4' }]);
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('video-detail');
    expect(wrapper.findComponent({ name: 'VideoDetailPage' }).props('videoId')).toBe(2);
  });

  it('被取代的那次取视频失败也不报错；没被取代时照常说明', async () => {
    const wrapper = await mountApp();
    let reject;
    api.GetVideosByIDs.mockImplementationOnce(() => new Promise((_, fail) => { reject = fail; }));
    wrapper.findComponent({ name: 'HomePage' }).vm.$emit('open-video', 1);
    await flushPromises();
    await wrapper.get('[data-test="nav-recap"]').trigger('click');
    reject(new Error('bridge down'));
    await flushPromises();
    expect(feedbackState.toasts).toEqual([]);

    api.GetVideosByIDs.mockRejectedValueOnce(new Error('bridge down'));
    wrapper.findComponent({ name: 'RecapPage' }).vm.$emit('open-video-id', 1);
    await flushPromises();
    expect(feedbackState.toasts.map(toast => toast.message)).toEqual(['打开视频失败：Error: bridge down']);
  });

  it('被取代的那次即使视频已不在片库也不报错', async () => {
    const wrapper = await mountApp();
    const pending = deferredVideos();
    wrapper.findComponent({ name: 'HomePage' }).vm.$emit('open-video', 1);
    await flushPromises();
    await wrapper.get('[data-test="nav-recap"]').trigger('click');
    pending[0].resolve([]);
    await flushPromises();
    expect(feedbackState.toasts).toEqual([]);
    expect(wrapper.vm.currentPage).toBe('recap');
  });
});

describe('整页详情改了状态，片库页跟上（M-5）', () => {
  it('video-state-changed 原样交给片库页的状态更新入口', async () => {
    const wrapper = await mountApp();
    const apply = vi.fn();
    wrapper.findComponent({ name: 'VideoListPage' }).vm.applyExternalVideoStateChange = apply;
    api.GetVideosByIDs.mockResolvedValue([{ id: 7, name: '7.mp4' }]);
    wrapper.findComponent({ name: 'HomePage' }).vm.$emit('open-video', 7);
    await flushPromises();
    const payload = { id: 7, changes: { is_favorite: true } };
    wrapper.findComponent({ name: 'VideoDetailPage' }).vm.$emit('video-state-changed', payload);
    expect(apply).toHaveBeenCalledWith(payload);
  });
});

describe('「视频」上按 ⌘⇧K 与其他页面一致（M-6、D-UD-03、D-P3-02）', () => {
  it('在「视频」上按 ⌘⇧K 切到清理中心（管理区）；片库页的 cleanupHotkey 只管页头标题', async () => {
    const wrapper = await mountApp();
    await wrapper.setData({ currentPage: 'videos' });
    expect(wrapper.findComponent({ name: 'VideoListPage' }).props('cleanupHotkey')).toBe(false);

    const event = new KeyboardEvent('keydown', { key: 'K', code: 'KeyK', metaKey: true, shiftKey: true, bubbles: true, cancelable: true });
    window.dispatchEvent(event);
    await flushPromises();

    expect(event.defaultPrevented).toBe(true);
    expect(wrapper.vm.currentPage).toBe('cleanup');
    expect(wrapper.find('[data-test="nav-area-manage"]').exists()).toBe(true);
    await wrapper.setData({ currentPage: 'library-manage' });
    expect(wrapper.findComponent({ name: 'VideoListPage' }).props('cleanupHotkey')).toBe(true);
  });
});

// D-P3-02：清理中心页的接线——首次进入才挂载、之后只隐藏；标签是页面参数；删除进行中不能离开；
// 删完与撤销后片库、图片库经既有的路刷新；片库与图片库的「清理审阅」入口、AI 审阅的「去清理」落在对应标签上。
describe('清理中心页的接线（D-P3-02）', () => {
  async function enterCleanup(wrapper, params = null) {
    wrapper.vm.navigateTo('cleanup', params);
    await flushPromises();
    return wrapper.findComponent({ name: 'CleanupCenterPage' });
  }

  it('首次进入才挂载，离开只隐藏（pageActive 跟着走）；标签由 update:tab 落下，认不出的标签忽略', async () => {
    const wrapper = await mountApp();
    expect(wrapper.findComponent({ name: 'CleanupCenterPage' }).exists()).toBe(false);
    const page = await enterCleanup(wrapper);
    expect(page.props()).toMatchObject({ pageActive: true, tab: 'video' });

    page.vm.$emit('update:tab', 'image');
    await flushPromises();
    expect(wrapper.vm.cleanupTab).toBe('image');
    expect(page.props('tab')).toBe('image');
    page.vm.$emit('update:tab', 'nope');
    await flushPromises();
    expect(wrapper.vm.cleanupTab).toBe('image');
    // 只换标签不算请求。
    expect(wrapper.vm.cleanupRequest).toBeNull();

    await wrapper.setData({ currentPage: 'insights' });
    expect(wrapper.findComponent({ name: 'CleanupCenterPage' }).exists()).toBe(true);
    expect(page.props('pageActive')).toBe(false);
    expect(page.attributes('style')).toContain('display: none');
  });

  it('删除进行中（busy-change true）不能离开清理中心，并提示；结束后可以', async () => {
    const wrapper = await mountApp();
    const page = await enterCleanup(wrapper);
    page.vm.$emit('busy-change', true);
    await flushPromises();
    expect(wrapper.vm.navigateTo('insights')).toBe(false);
    expect(wrapper.vm.currentPage).toBe('cleanup');
    expect(feedbackState.toasts.map(toast => toast.message)).toContain('正在移到废纸篓，完成后再离开清理中心。');
    // 同一页上只换标签不算离开：App 照样落参数（删除进行中切标签由清理中心页自己禁用）。
    expect(wrapper.vm.navigateTo('cleanup', { tab: 'image' })).toBe(true);
    page.vm.$emit('busy-change', false);
    await flushPromises();
    expect(wrapper.vm.navigateTo('insights')).toBe(true);
    expect(wrapper.vm.currentPage).toBe('insights');
  });

  it('视频删完 / 撤销后经 videos-changed 让片库重取、顶栏计数刷新；图片经 images-changed 让图片库重载', async () => {
    const wrapper = await mountApp();
    await wrapper.setData({ photosMounted: true });
    const reloadVideos = vi.fn();
    const reloadImages = vi.fn();
    wrapper.findComponent({ name: 'VideoListPage' }).vm.handleVideosChangedElsewhere = reloadVideos;
    wrapper.findComponent({ name: 'PhotoLibraryPage' }).vm.handleCleanupDeleted = reloadImages;
    const page = await enterCleanup(wrapper);
    const callsBefore = api.GetLibraryCounts.mock.calls.length;

    page.vm.$emit('videos-changed', { removedIDs: [7, 8] });
    await flushPromises();
    expect(reloadVideos).toHaveBeenCalledWith([7, 8]);
    expect(api.GetLibraryCounts.mock.calls.length).toBe(callsBefore + 1);

    page.vm.$emit('images-changed');
    await flushPromises();
    expect(reloadImages).toHaveBeenCalledOnce();
    expect(api.GetLibraryCounts.mock.calls.length).toBe(callsBefore + 2);
    expect(reloadVideos).toHaveBeenCalledTimes(1);
  });

  it('片库管理的「去清理」带同源对进视频标签；图片管理的「清理审阅」进图片标签', async () => {
    const wrapper = await mountApp();
    await wrapper.setData({ currentPage: 'library-manage', cleanupTab: 'image', photosMounted: true });
    wrapper.findComponent({ name: 'VideoListPage' }).vm.$emit('open-cleanup', { relationId: 9 });
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('cleanup');
    expect(wrapper.vm.cleanupTab).toBe('video');
    expect(wrapper.vm.cleanupRequest).toMatchObject({ open: true, relationId: 9, consolidationTaskID: 0 });

    await wrapper.setData({ currentPage: 'photos-manage' });
    const before = wrapper.vm.cleanupRequest;
    wrapper.findComponent({ name: 'PhotoLibraryPage' }).vm.$emit('open-cleanup');
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('cleanup');
    expect(wrapper.vm.cleanupTab).toBe('image');
    // 图片标签挂上时自己读状态，不发视频的请求。
    expect(wrapper.vm.cleanupRequest).toBe(before);
  });

  it('片库管理的回收站恢复了视频：交给清理中心放掉审阅里的已删标记（还没打开过清理中心时什么也不做）', async () => {
    const wrapper = await mountApp();
    const list = wrapper.findComponent({ name: 'VideoListPage' });
    list.vm.$emit('trash-restored', { videoIDs: [3], fromTrashDialog: true });
    await flushPromises();
    const page = await enterCleanup(wrapper);
    const restored = vi.fn();
    page.vm.handleVideosRestored = restored;
    list.vm.$emit('trash-restored', { videoIDs: [3, 4], fromTrashDialog: false });
    await flushPromises();
    expect(restored).toHaveBeenCalledWith([3, 4], false);
  });

  it('待处理与管理首页按命令 ID 打开：library.openCleanup 落在视频标签，宿主是清理中心', async () => {
    const wrapper = await mountApp();
    await wrapper.setData({ currentPage: 'insights', cleanupTab: 'image' });
    const run = vi.fn();
    registerCommands('test-cleanup-command', [{ id: 'library.openCleanup', group: 'action', label: '清理中心 · 视频', hidden: true, run }]);
    await wrapper.vm.runPendingCommand('library.openCleanup', '清理候选');
    await flushPromises();
    unregisterCommands('test-cleanup-command');
    expect(wrapper.vm.currentPage).toBe('cleanup');
    expect(wrapper.vm.cleanupTab).toBe('video');
    expect(run).toHaveBeenCalledTimes(1);
  });
});

describe('观看区用影院表面（D-UD-02）', () => {
  const cinema = () => document.body.classList.contains('surface-cinema');

  // 打在 <body> 上：顶栏、页面、App 里的浮层，以及 teleport 到 body 的对话框与确认框都在它下面。
  it('观看区各页（含「视频」「图片」与整页详情）打开影院表面，管理区各页跟随系统主题', async () => {
    const wrapper = await mountApp();
    expect(wrapper.vm.currentPage).toBe('home');
    expect(cinema()).toBe(true);

    const expected = {
      videos: true, photos: true, people: true, recap: true, 'video-detail': true,
      'manage-home': false, 'library-manage': false, 'photos-manage': false, settings: false, 'data-care': false
    };
    for (const [page, on] of Object.entries(expected)) {
      await wrapper.setData({ currentPage: page });
      expect(cinema(), page).toBe(on);
    }

    await wrapper.get('[data-test="nav-back-to-viewer"]').trigger('click');
    expect(wrapper.vm.currentPage).toBe('home');
    expect(cinema()).toBe(true);
  });

  it('卸载时摘掉；启动失败页不属于哪个区，跟随系统主题', async () => {
    const wrapper = await mountApp();
    expect(cinema()).toBe(true);
    wrapper.unmount();
    expect(cinema()).toBe(false);

    api.GetStartupError.mockResolvedValue('数据库未初始化');
    await mountApp();
    expect(cinema()).toBe(false);
  });
});

describe('「视频」海报墙的接线（D-P2-01）', () => {
  it('navigateTo(\'videos\', params) 把一次性参数交给海报墙；不带参数的切页不改它，已在本页时照样交过去', async () => {
    const wrapper = await mountApp();
    expect(wrapper.vm.navigateTo('videos', { keyword: '星际' })).toBe(true);
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('videos');
    const browse = () => wrapper.findComponent({ name: 'VideoBrowsePage' });
    expect(browse().props('navigationRequest')).toEqual({ keyword: '星际', token: 1 });

    wrapper.vm.navigateTo('home');
    wrapper.vm.navigateTo('videos');
    await flushPromises();
    expect(browse().props('navigationRequest')).toEqual({ keyword: '星际', token: 1 });

    expect(wrapper.vm.navigateTo('videos', { watch: 'favorites' })).toBe(true);
    await flushPromises();
    expect(browse().props('navigationRequest')).toEqual({ watch: 'favorites', token: 2 });
  });

  it('设置页有未保存修改时参数等确认后才交过去；选「继续编辑」什么也不交', async () => {
    const wrapper = await mountApp();
    await enterManage(wrapper);
    await wrapper.get('[data-test="nav-settings"]').trigger('click');
    wrapper.findComponent({ name: 'SettingsPage' }).vm.$emit('update:dirty', true);
    await flushPromises();

    const kept = wrapper.vm.navigateTo('videos', { keyword: '甲' });
    resolveConfirm(false);
    expect(await kept).toBe(false);
    expect(wrapper.vm.currentPage).toBe('settings');
    expect(wrapper.vm.pageRequests).toEqual({});

    const left = wrapper.vm.navigateTo('videos', { keyword: '乙' });
    resolveConfirm(true);
    expect(await left).toBe(true);
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('videos');
    expect(wrapper.findComponent({ name: 'VideoBrowsePage' }).props('navigationRequest')).toEqual({ keyword: '乙', token: 1 });
  });

  it('整页详情改了状态，海报墙上那张卡与片库管理那一行都跟上（M-5）', async () => {
    const wrapper = await mountApp();
    await wrapper.get('[data-test="nav-videos"]').trigger('click');
    const applyBrowse = vi.fn();
    const applyLibrary = vi.fn();
    wrapper.findComponent({ name: 'VideoBrowsePage' }).vm.applyExternalVideoStateChange = applyBrowse;
    wrapper.findComponent({ name: 'VideoListPage' }).vm.applyExternalVideoStateChange = applyLibrary;
    api.GetVideosByIDs.mockResolvedValue([{ id: 7, name: '7.mp4' }]);
    wrapper.findComponent({ name: 'VideoBrowsePage' }).vm.$emit('open-video', 7);
    await flushPromises();
    expect(wrapper.vm.detailReturnStack.map(entry => entry.page)).toEqual(['videos']);
    const payload = { id: 7, changes: { is_watched: true } };
    wrapper.findComponent({ name: 'VideoDetailPage' }).vm.$emit('video-state-changed', payload);
    expect(applyBrowse).toHaveBeenCalledWith(payload);
    expect(applyLibrary).toHaveBeenCalledWith(payload);
  });

  it('海报墙的 navigate（空片库「去添加扫描目录」）走 App 的切页', async () => {
    const wrapper = await mountApp();
    await wrapper.get('[data-test="nav-videos"]').trigger('click');
    wrapper.findComponent({ name: 'VideoBrowsePage' }).vm.$emit('navigate', 'settings');
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('settings');
  });
});
