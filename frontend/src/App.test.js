import { flushPromises, shallowMount } from '@vue/test-utils';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'GetSettings', 'GetAllTags', 'GetAllDirectories', 'GetStartupError',
  'SyncScanDirectories', 'SyncImageDirectories', 'GetLibraryCounts', 'LogFrontend',
  'SetWindowForeground', 'GetVideosByIDs',
  'GetBackgroundTasks', 'GetIdleSchedulerStatus', 'ListSavedLibraryViews', 'ListCollections', 'ListPeople',
  'GetDatabaseBackendStatus', 'GetDatabaseSwitchStatus', 'ListDownloadTasks', 'RelaunchApp', 'GetPendingWorkSummary',
  'GetStartupBackendGuard', 'AcceptSQLiteAfterFallbackBlock'
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
  api.GetPendingWorkSummary.mockResolvedValue({ total: 0 });
  api.GetStartupBackendGuard.mockResolvedValue({
    blocked: false, reason: '', previous_location: '', config_files: [], data_dir_config: '', config_snippet: '', pg_host_empty_in_launch_env: false
  });
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
  const wrapper = shallowMount(App, { global: { stubs: { HomePage: {
    name: 'HomePage', props: ['pageActive'], emits: ['open-video', 'navigate', 'open-person'],
    methods: { clearPendingTonight() {}, replacePlayedTonight() {} }, template: '<div />'
  } } } });
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

it('启动失败页只留标题：没有页面可去，不画导航与右上角工具；正常启动时导航照常', async () => {
  api.GetStartupError.mockResolvedValue('数据库未初始化');
  const failed = shallowMount(App);
  mountedWrappers.push(failed);
  await flushPromises();
  expect(failed.find('.header h1').text()).toBe('析微影策');
  expect(failed.find('.header-nav').exists()).toBe(false);
  expect(failed.find('[data-test="open-manage"]').exists()).toBe(false);

  api.GetStartupError.mockResolvedValue('');
  const ready = shallowMount(App);
  mountedWrappers.push(ready);
  await flushPromises();
  expect(ready.find('.header-nav').exists()).toBe(true);
  expect(ready.find('[data-test="nav-home"]').exists()).toBe(true);
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
    expect(wrapper.find('[data-test="nav-pending-work"]').exists()).toBe(false);
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

  // D-UI-02：观看区的待处理数量挂在「管理」按钮上。待处理工作台改成页面（D-P3-01）之后，角标由 App 自己读
  // GetPendingWorkSummary 的总数（启动时一次、之后每分钟一次），读失败清零。
  it('META-08 观看区「管理」按钮的角标是待处理总数，超过 99 显示 99+，读失败清零，点了进管理首页', async () => {
    api.GetPendingWorkSummary.mockResolvedValue({ total: 5 });
    const wrapper = await mountApp();
    expect(api.GetPendingWorkSummary).toHaveBeenCalledTimes(1);
    expect(wrapper.get('[data-test="manage-pending-badge"]').text()).toBe('5');

    api.GetPendingWorkSummary.mockResolvedValue({ total: 120 });
    await wrapper.vm.refreshPendingWorkTotal();
    await flushPromises();
    expect(wrapper.get('[data-test="manage-pending-badge"]').text()).toBe('99+');

    api.GetPendingWorkSummary.mockRejectedValue('数据库后端已切换，请重启应用后再查看');
    await wrapper.vm.refreshPendingWorkTotal();
    await flushPromises();
    expect(wrapper.find('[data-test="manage-pending-badge"]').exists()).toBe(false);

    await wrapper.get('[data-test="open-manage"]').trigger('click');
    expect(wrapper.vm.currentPage).toBe('manage-home');
    expect(wrapper.findComponent({ name: 'ManageHomePage' }).exists()).toBe(true);
  });

  it('META-08 角标每分钟重读一次；卸载后不再读', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
    try {
      const wrapper = await mountApp();
      expect(api.GetPendingWorkSummary).toHaveBeenCalledTimes(1);
      api.GetPendingWorkSummary.mockResolvedValue({ total: 3 });
      vi.advanceTimersByTime(60000);
      await flushPromises();
      expect(api.GetPendingWorkSummary).toHaveBeenCalledTimes(2);
      expect(wrapper.get('[data-test="manage-pending-badge"]').text()).toBe('3');

      wrapper.unmount();
      vi.advanceTimersByTime(120000);
      expect(api.GetPendingWorkSummary).toHaveBeenCalledTimes(2);
    } finally {
      vi.useRealTimers();
    }
  });

  it('D-P3-01 管理导航「待处理」是页面：带同一个角标，点了切到待处理页；页面读到的总数同步角标、晚到的定时读取不覆盖它', async () => {
    api.GetPendingWorkSummary.mockResolvedValue({ total: 120 });
    const wrapper = await mountApp();
    await enterManage(wrapper);
    expect(wrapper.get('[data-test="pending-work-badge"]').text()).toBe('99+');
    expect(wrapper.find('[data-test="open-pending-work"]').exists()).toBe(false);
    expect(wrapper.findComponent({ name: 'PendingWorkPage' }).exists()).toBe(false);

    await wrapper.get('[data-test="nav-pending-work"]').trigger('click');
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('pending-work');
    expect(wrapper.vm.area).toBe('manage');
    expect(wrapper.get('[data-test="nav-pending-work"]').classes()).toContain('active');
    const page = wrapper.findComponent({ name: 'PendingWorkPage' });
    expect(page.exists()).toBe(true);

    let resolveStale;
    api.GetPendingWorkSummary.mockReturnValueOnce(new Promise(resolve => { resolveStale = resolve; }));
    const stale = wrapper.vm.refreshPendingWorkTotal();
    page.vm.$emit('badge-change', 0);
    resolveStale({ total: 120 });
    await stale;
    await flushPromises();
    expect(wrapper.find('[data-test="pending-work-badge"]').exists()).toBe(false);

    page.vm.$emit('badge-change', 4);
    await flushPromises();
    expect(wrapper.get('[data-test="pending-work-badge"]').text()).toBe('4');

    // 离开就卸载，下次进来重新读。
    await wrapper.get('[data-test="nav-manage-home"]').trigger('click');
    expect(wrapper.findComponent({ name: 'PendingWorkPage' }).exists()).toBe(false);
  });

  it('D-P3-01 命令 action:pending-work 打开待处理页（命令 ID 不变），任务中心开着时先收起', async () => {
    const wrapper = await mountApp();
    await wrapper.setData({ taskCenterOpen: true });

    await findCommand('action:pending-work').run();
    await flushPromises();

    expect(wrapper.vm.currentPage).toBe('pending-work');
    expect(wrapper.vm.taskCenterOpen).toBe(false);
    expect(wrapper.findComponent({ name: 'PendingWorkPage' }).exists()).toBe(true);
    expect(findCommand('nav:page:pending-work')).toBeNull();
  });

  it('D-P3-01 待处理页的事件接到既有入口：去管理首页、打开任务中心', async () => {
    const wrapper = await mountApp();
    await wrapper.setData({ currentPage: 'pending-work' });
    const page = wrapper.findComponent({ name: 'PendingWorkPage' });

    page.vm.$emit('open-task-center');
    await flushPromises();
    expect(wrapper.vm.taskCenterOpen).toBe(true);

    page.vm.$emit('navigate', 'manage-home');
    await flushPromises();
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

  // D-P3-04：人物观看页只看不改，人脸审阅是管理侧「人物与作品集」的「人脸审阅」标签。
  it('APP-11 按固定命令 ID 打开工具：先切到宿主页（人物与作品集按需挂载）的人脸审阅标签，再执行', async () => {
    const wrapper = await mountApp();
    const run = vi.fn();
    registerCommands('test-face-host', [{ id: 'people.openFaceReview', group: 'action', label: '人脸审阅', hidden: true, run }]);

    await wrapper.vm.runPendingCommand('people.openFaceReview', '人脸待命名');
    await flushPromises();

    expect(wrapper.vm.currentPage).toBe('entities-manage');
    expect(wrapper.vm.entitiesManageTab).toBe('faces');
    expect(wrapper.findComponent({ name: 'EntityLibraryPage' }).props('tab')).toBe('faces');
    expect(run).toHaveBeenCalledTimes(1);
    unregisterCommands('test-face-host');
  });

  it('APP-11 人物与作品集停在作品集标签上时，人脸审阅在同一页换到「人脸审阅」标签（不重挂），再执行命令', async () => {
    const wrapper = await mountApp();
    await wrapper.setData({ currentPage: 'entities-manage', entitiesManageTab: 'collection' });
    const page = wrapper.findComponent({ name: 'EntityLibraryPage' });
    expect(page.props('tab')).toBe('collection');
    const run = vi.fn();
    registerCommands('test-face-kind', [{ id: 'people.openFaceReview', group: 'action', label: '人脸审阅', hidden: true, run }]);

    await wrapper.vm.runPendingCommand('people.openFaceReview', '人脸待命名');

    unregisterCommands('test-face-kind');
    expect(wrapper.vm.currentPage).toBe('entities-manage');
    const after = wrapper.findComponent({ name: 'EntityLibraryPage' });
    expect(after.props('tab')).toBe('faces');
    expect(after.vm).toBe(page.vm);
    expect(run).toHaveBeenCalledTimes(1);
  });

  it('APP-11 待处理页「打开图片 AI 审阅」：宿主页没有注册该命令时给出中文提示，不静默失败', async () => {
    const wrapper = await mountApp();
    await wrapper.setData({ currentPage: 'pending-work' });

    wrapper.findComponent({ name: 'PendingWorkPage' }).vm.$emit('run-command', 'photos.openAIReview', 'AI 图片标签');
    await flushPromises();

    expect(wrapper.vm.currentPage).toBe('photos-manage');
    expect(feedbackState.toasts.map(toast => toast.message).join('\n')).toContain('「AI 图片标签」暂时打不开');
  });

  // 清理候选是视频清理的口径：进清理中心的视频标签（library.openCleanup，宿主页清理中心、页面参数 { tab: 'video' }），
  // 不停在上次看的标签上（独立评审 P3 #3）。
  it('D-P3-01 待处理页「清理候选」打开清理中心的视频标签，不论上次停在哪个标签', async () => {
    const wrapper = await mountApp();
    await wrapper.setData({ currentPage: 'pending-work', cleanupTab: 'image' });
    const run = vi.fn();
    registerCommands('test-pending-cleanup', [{ id: 'library.openCleanup', group: 'action', label: '清理中心 · 视频', hidden: true, run }]);

    wrapper.findComponent({ name: 'PendingWorkPage' }).vm.$emit('run-command', 'library.openCleanup', '清理候选');
    await flushPromises();
    unregisterCommands('test-pending-cleanup');

    expect(wrapper.vm.currentPage).toBe('cleanup');
    expect(wrapper.vm.cleanupTab).toBe('video');
    expect(run).toHaveBeenCalledTimes(1);
  });

  // 待处理页应用了本地资料（「应用」「全部应用」「逐项确认…」）：片库管理按当前条件重取，顶栏计数刷新（独立评审 P3 #2）。
  it('D-P3-01 待处理页 videos-changed：片库管理按当前条件重取', async () => {
    const wrapper = await mountApp();
    await wrapper.setData({ currentPage: 'pending-work' });
    const reload = vi.fn();
    wrapper.findComponent({ name: 'VideoListPage' }).vm.handleVideosChangedElsewhere = reload;
    const counts = api.GetLibraryCounts.mock.calls.length;

    wrapper.findComponent({ name: 'PendingWorkPage' }).vm.$emit('videos-changed', {});
    await flushPromises();

    expect(reload).toHaveBeenCalledWith([]);
    expect(api.GetLibraryCounts.mock.calls.length).toBe(counts + 1);
  });

  // D-P3-05：片库管理「管理」菜单发起任务的提示里点「查看」打开任务中心。
  it('D-P3-05 片库页 open-task-center 打开任务中心', async () => {
    const wrapper = await mountApp();
    wrapper.findComponent({ name: 'VideoListPage' }).vm.$emit('open-task-center');
    await flushPromises();
    expect(wrapper.vm.taskCenterOpen).toBe(true);
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

  it('待播队列里的视频已不在片库中时用队列自己的说法', async () => {
    const wrapper = await mountApp();
    api.GetVideosByIDs.mockResolvedValue([]);

    wrapper.findComponent({ name: 'PlaybackQueuePanel' }).vm.$emit('open-video', 42);
    await flushPromises();

    const messages = feedbackState.toasts.map(toast => toast.message).join('\n');
    expect(messages).toContain('这部视频已不在片库中');
    expect(messages).not.toContain('产物视频');
  });

  it('MEDIA-10 退出确认对话框常挂载；数据库连不上时顶栏不显示任务与待处理入口', async () => {
    api.GetStartupError.mockResolvedValue('数据库连接失败');
    const wrapper = await mountApp();

    expect(wrapper.findComponent({ name: 'QuitConfirmDialog' }).exists()).toBe(true);
    expect(wrapper.find('[data-test="open-task-center"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="nav-pending-work"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="manage-pending-badge"]').exists()).toBe(false);
    expect(api.GetPendingWorkSummary).not.toHaveBeenCalled();
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

// 数据库后端回落保护（D-DBG-03）：上次用的是 PostgreSQL、这次没读到它的配置时，后端在打开任何库之前停下，
// 启动出错页按 GetStartupBackendGuard().blocked 换成专门说明与两个出口；其他启动错误照旧。
describe('数据库后端回落保护的启动出错页（D-DBG-03）', () => {
  const BLOCKED_ERROR = 'backend_fallback_blocked: 上次用的是 PostgreSQL（me@db.local:5432/cine），这次没有读到 PostgreSQL 配置；为了不打开一个空的本机库，已经停下，数据还在 PostgreSQL 里';
  const CONFIG_PATHS = [
    '/Users/me/.CineInsight/.env',
    '/.env',
    '/Applications/析微影策.app/Contents/MacOS/.env',
    '/Applications/析微影策.app/Contents/Resources/.env'
  ];
  const SNIPPET = 'DB_BACKEND=postgres\nPREVIOUS_BACKEND=sqlite\nPG_HOST=db.local\nPG_PORT=5432\nPG_USER=me\nPG_PASSWORD=（在这里填密码）\nPG_DB=cine\n';
  const blockedGuard = () => ({
    blocked: true,
    reason: 'previous_postgres',
    previous_location: 'me@db.local:5432/cine',
    config_files: CONFIG_PATHS.map((path, index) => ({ path, exists: index === 3 })),
    data_dir_config: '/Users/me/.CineInsight/.env',
    data_dir_config_status: 'missing',
    config_snippet: SNIPPET,
    pg_host_empty_in_launch_env: false
  });

  // 确认框由 AppFeedback 画（shallowMount 下是桩），这里读 feedbackState、用 resolveConfirm 作答。
  // AppBadge 的文字在默认插槽里，所以渲染桩的默认插槽。
  async function mountBlocked() {
    api.GetStartupError.mockResolvedValue(BLOCKED_ERROR);
    api.GetStartupBackendGuard.mockResolvedValue(blockedGuard());
    api.GetDatabaseBackendStatus.mockResolvedValue({ backend: 'sqlite' });
    const wrapper = shallowMount(App, { global: { renderStubDefaultSlot: true } });
    mountedWrappers.push(wrapper);
    await flushPromises();
    return wrapper;
  }

  it('被拦下时换成专门说明：上次的位置、按优先级读过的配置文件（标明是否存在）、推荐的数据目录 .env；不显示原因码与通用提示', async () => {
    const wrapper = await mountBlocked();
    const page = wrapper.get('[data-test="startup-guard"]');
    expect(page.get('h2').text()).toBe('没有读到 PostgreSQL 配置，已停下');
    expect(page.get('[data-test="startup-guard-summary"]').text()).toBe(
      '上次用的是 PostgreSQL（me@db.local:5432/cine），这次没有读到 PostgreSQL 配置，为了不打开一个空的本机库，已经停下。数据还在 PostgreSQL 里。'
    );
    const files = page.findAll('[data-test="startup-guard-config-file"]');
    expect(files.map(file => file.get('.startup-guard-path').text())).toEqual(CONFIG_PATHS);
    expect(files.map(file => file.findComponent({ name: 'AppBadge' }).text())).toEqual(['不存在', '不存在', '不存在', '存在']);
    expect(files.map(file => file.findComponent({ name: 'AppBadge' }).props('tone'))).toEqual(['neutral', 'neutral', 'neutral', 'success']);
    const hint = page.get('[data-test="startup-guard-data-dir-hint"]').text();
    expect(hint).toContain('把下面这段存成 /Users/me/.CineInsight/.env，在 PG_PASSWORD 一行填上密码，然后点「重新打开」');
    expect(hint).not.toContain('替换这份文件的全部内容');
    expect(hint).not.toContain('补全空着的几项');
    // 密码提示按 godotenv v1.5.1 的规则写（见 database 的 TestDBG05QuotedPasswordRoundTripsThroughLoadEnvConfig）。
    const passwordHint = page.get('[data-test="startup-guard-password-hint"]').text();
    expect(passwordHint).toContain('密码里有空格、#、引号、\\ 或 $ 时，用英文双引号括起来');
    expect(passwordHint).toContain('PG_PASSWORD="a \\"b\\" #1"');
    // 配置块原样显示（pre 保留换行），是应用内切换写下的同一格式。
    expect(page.get('[data-test="startup-guard-snippet"]').element.textContent).toBe(SNIPPET);
    expect(page.find('[data-test="startup-guard-launch-env"]').exists()).toBe(false);
    expect(page.get('[data-test="startup-guard-use-sqlite"]').text()).toBe('这次改用本机 SQLite…');
    expect(page.get('[data-test="startup-guard-reopen"]').text()).toBe('重新打开');
    expect(wrapper.text()).not.toContain('backend_fallback_blocked');
    expect(wrapper.text()).not.toContain('应用已启动，但数据库连接失败');
    expect(wrapper.find('[data-test="startup-error-sqlite-hint"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="startup-error-postgres-hint"]').exists()).toBe(false);
    expect(wrapper.findComponent({ name: 'VideoListPage' }).exists()).toBe(false);
    expect(api.GetDatabaseSwitchStatus).not.toHaveBeenCalled();
  });

  it('按数据目录推断拦下（还没有记录）时说「看起来之前用的是 PostgreSQL」，不提上次的位置，配置块留空待补全', async () => {
    const emptySnippet = 'DB_BACKEND=postgres\nPREVIOUS_BACKEND=sqlite\nPG_HOST=\nPG_PORT=\nPG_USER=\nPG_PASSWORD=（在这里填密码）\nPG_DB=\n';
    api.GetStartupError.mockResolvedValue('backend_fallback_blocked: 数据目录里有之前使用留下的数据……');
    api.GetStartupBackendGuard.mockResolvedValue({ ...blockedGuard(), reason: 'inferred_postgres', previous_location: '', config_snippet: emptySnippet });
    const wrapper = await mountApp();
    const summary = wrapper.get('[data-test="startup-guard-summary"]').text();
    expect(summary).toContain('数据目录里有之前使用留下的数据（缩略图、备份等），但本机 SQLite 库文件不存在');
    expect(summary).toContain('看起来之前用的是 PostgreSQL');
    expect(summary).toContain('如果之前用的是放在别处的 SQLite 库，把 SQLITE_PATH 配置找回来后重新打开');
    expect(summary).not.toContain('上次用的是');
    expect(wrapper.get('[data-test="startup-guard-data-dir-hint"]').text()).toContain('补全空着的几项');
    expect(wrapper.get('[data-test="startup-guard-snippet"]').element.textContent).toBe(emptySnippet);
  });

  it('数据目录下的 .env 已经有了时说「替换这份文件的全部内容」，没有生效的才说原有内容没有生效', async () => {
    api.GetStartupError.mockResolvedValue(BLOCKED_ERROR);
    api.GetStartupBackendGuard.mockResolvedValue({ ...blockedGuard(), data_dir_config_status: 'ignored' });
    let wrapper = await mountApp();
    let hint = wrapper.get('[data-test="startup-guard-data-dir-hint"]').text();
    expect(hint).toContain('/Users/me/.CineInsight/.env 已经有了，但这份文件里原有的内容现在都没有生效。用下面这段替换这份文件的全部内容，在 PG_PASSWORD 一行填上密码');
    expect(hint).not.toContain('存成');
    wrapper.unmount();

    api.GetStartupBackendGuard.mockResolvedValue({ ...blockedGuard(), data_dir_config_status: 'loaded' });
    wrapper = await mountApp();
    hint = wrapper.get('[data-test="startup-guard-data-dir-hint"]').text();
    expect(hint).toContain('/Users/me/.CineInsight/.env 已经有了。用下面这段替换这份文件的全部内容');
    expect(hint).not.toContain('没有生效');
  });

  it('配置块里有空着的项时（记录里没有连接项）也提示补全', async () => {
    const emptySnippet = 'DB_BACKEND=postgres\nPREVIOUS_BACKEND=sqlite\nPG_HOST=\nPG_PORT=\nPG_USER=\nPG_PASSWORD=（在这里填密码）\nPG_DB=\n';
    api.GetStartupError.mockResolvedValue(BLOCKED_ERROR);
    api.GetStartupBackendGuard.mockResolvedValue({ ...blockedGuard(), config_snippet: emptySnippet });
    const wrapper = await mountApp();
    expect(wrapper.get('[data-test="startup-guard-summary"]').text()).toContain('上次用的是 PostgreSQL（me@db.local:5432/cine）');
    expect(wrapper.get('[data-test="startup-guard-data-dir-hint"]').text()).toContain('在 PG_PASSWORD 一行填上密码、补全空着的几项');
  });

  it('启动环境里有空的 PG_HOST 时说清是它挡住了配置文件', async () => {
    api.GetStartupError.mockResolvedValue(BLOCKED_ERROR);
    api.GetStartupBackendGuard.mockResolvedValue({ ...blockedGuard(), pg_host_empty_in_launch_env: true });
    const wrapper = await mountApp();
    const banner = wrapper.findComponent({ name: 'AppBanner' });
    expect(banner.attributes('data-test')).toBe('startup-guard-launch-env');
    expect(banner.props('tone')).toBe('warning');
    expect(banner.props('title')).toBe('启动环境里的 PG_HOST 是空的');
    expect(banner.props('message')).toContain('配置文件不会覆盖已有的变量');
    expect(banner.props('message')).toContain('launchctl unsetenv PG_HOST');
  });

  describe('配置块的「复制」', () => {
    const originalClipboard = Object.getOwnPropertyDescriptor(navigator, 'clipboard');
    afterEach(() => {
      if (originalClipboard) Object.defineProperty(navigator, 'clipboard', originalClipboard);
      else delete navigator.clipboard;
      window.getSelection()?.removeAllRanges();
    });
    const setClipboard = value => Object.defineProperty(navigator, 'clipboard', { value, configurable: true });

    it('写进剪贴板后按钮改成「已复制」', async () => {
      const writeText = vi.fn().mockResolvedValue(undefined);
      setClipboard({ writeText });
      const wrapper = await mountBlocked();
      await wrapper.get('[data-test="startup-guard-copy"]').trigger('click');
      await flushPromises();
      expect(writeText).toHaveBeenCalledWith(SNIPPET);
      expect(wrapper.get('[data-test="startup-guard-copy"]').text()).toBe('已复制');
      expect(wrapper.find('[data-test="startup-guard-copy-fallback"]').exists()).toBe(false);
    });

    it('剪贴板写不进去时选中整段并提示按 ⌘C', async () => {
      setClipboard({ writeText: vi.fn().mockRejectedValue(new Error('NotAllowedError')) });
      const wrapper = await mountBlocked();
      document.body.appendChild(wrapper.element);
      await wrapper.get('[data-test="startup-guard-copy"]').trigger('click');
      await flushPromises();
      expect(wrapper.get('[data-test="startup-guard-copy-fallback"]').text()).toContain('已经选中了上面这段，按 ⌘C 复制');
      expect(wrapper.get('[data-test="startup-guard-copy"]').text()).toBe('复制');
      expect(window.getSelection().toString()).toBe(SNIPPET);
    });

    it('没有剪贴板接口时同样退到选中整段', async () => {
      setClipboard(undefined);
      const wrapper = await mountBlocked();
      await wrapper.get('[data-test="startup-guard-copy"]').trigger('click');
      await flushPromises();
      expect(wrapper.find('[data-test="startup-guard-copy-fallback"]').exists()).toBe(true);
    });
  });

  it('上次的位置读不到时只说「上次用的是 PostgreSQL」，不留空括号', async () => {
    api.GetStartupError.mockResolvedValue(BLOCKED_ERROR);
    api.GetStartupBackendGuard.mockResolvedValue({ ...blockedGuard(), previous_location: '' });
    const wrapper = await mountApp();
    expect(wrapper.get('[data-test="startup-guard-summary"]').text()).toMatch(/^上次用的是 PostgreSQL，这次没有读到/);
  });

  it('「这次改用本机 SQLite…」二次确认后才调绑定；没能自动重启时把原因留在页上', async () => {
    api.AcceptSQLiteAfterFallbackBlock.mockResolvedValue({ relaunched: false, message: '当前不是从应用包运行，应用将退出，请手动重新打开' });
    const wrapper = await mountBlocked();
    await wrapper.get('[data-test="startup-guard-use-sqlite"]').trigger('click');
    expect(feedbackState.confirm).toMatchObject({ title: '这次改用本机 SQLite？', confirmText: '改用本机 SQLite', level: 'confirm' });
    expect(feedbackState.confirm.message).toContain('它可能是空的');
    expect(feedbackState.confirm.message).toContain('PostgreSQL 里的数据不受影响');
    expect(feedbackState.confirm.message).toContain('把配置放回去再重新打开应用');
    expect(api.AcceptSQLiteAfterFallbackBlock).not.toHaveBeenCalled();

    resolveConfirm(true);
    await flushPromises();
    expect(api.AcceptSQLiteAfterFallbackBlock).toHaveBeenCalledTimes(1);
    expect(api.RelaunchApp).not.toHaveBeenCalled();
    expect(wrapper.get('[data-test="startup-guard-error"]').text()).toBe('当前不是从应用包运行，应用将退出，请手动重新打开');
  });

  it('取消确认时不调绑定、页上什么都不变', async () => {
    const wrapper = await mountBlocked();
    await wrapper.get('[data-test="startup-guard-use-sqlite"]').trigger('click');
    resolveConfirm(false);
    await flushPromises();
    expect(api.AcceptSQLiteAfterFallbackBlock).not.toHaveBeenCalled();
    expect(api.RelaunchApp).not.toHaveBeenCalled();
    expect(wrapper.find('[data-test="startup-guard-error"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="startup-guard-use-sqlite"]').attributes('disabled')).toBeUndefined();
  });

  it('改用本机 SQLite 被后端拒绝时把错误留在页上，按钮恢复可点', async () => {
    api.AcceptSQLiteAfterFallbackBlock.mockRejectedValue('记下改用本机 SQLite 失败: disk full');
    const wrapper = await mountBlocked();
    await wrapper.get('[data-test="startup-guard-use-sqlite"]').trigger('click');
    resolveConfirm(true);
    await flushPromises();
    expect(wrapper.get('[data-test="startup-guard-error"]').text()).toBe('记下改用本机 SQLite 失败: disk full');
    expect(wrapper.get('[data-test="startup-guard-use-sqlite"]').attributes('disabled')).toBeUndefined();
  });

  it('「重新打开」走现有的 RelaunchApp，进行中两个按钮都禁用', async () => {
    api.RelaunchApp.mockResolvedValue({ relaunched: true, message: '应用即将重新启动' });
    const wrapper = await mountBlocked();
    await wrapper.get('[data-test="startup-guard-reopen"]').trigger('click');
    await flushPromises();
    expect(api.RelaunchApp).toHaveBeenCalledTimes(1);
    expect(api.AcceptSQLiteAfterFallbackBlock).not.toHaveBeenCalled();
    expect(wrapper.get('[data-test="startup-guard-reopen"]').text()).toBe('正在重新打开…');
    expect(wrapper.get('[data-test="startup-guard-reopen"]').attributes('disabled')).toBeDefined();
    expect(wrapper.get('[data-test="startup-guard-use-sqlite"]').attributes('disabled')).toBeDefined();
  });

  it('专门说明页同样能打开只读诊断', async () => {
    const wrapper = await mountBlocked();
    await wrapper.get('[data-test="startup-open-diagnostics"]').trigger('click');
    expect(wrapper.vm.startupDiagnosticsOpen).toBe(true);
  });

  it('其他启动错误照旧：通用卡片原样显示错误与排查提示，没有专门说明', async () => {
    api.GetStartupError.mockResolvedValue('打开数据库失败: connection refused');
    api.GetDatabaseBackendStatus.mockResolvedValue({ backend: 'postgres' });
    const wrapper = await mountApp();
    expect(api.GetStartupBackendGuard).toHaveBeenCalledTimes(1);
    expect(wrapper.find('[data-test="startup-guard"]').exists()).toBe(false);
    const card = wrapper.get('.startup-error-card');
    expect(card.get('h2').text()).toBe('应用已启动，但数据库连接失败');
    expect(card.get('.startup-error-text').text()).toBe('打开数据库失败: connection refused');
    expect(card.find('[data-test="startup-error-postgres-hint"]').exists()).toBe(true);
    expect(card.find('[data-test="startup-open-diagnostics"]').exists()).toBe(true);
    expect(card.find('[data-test="startup-guard-use-sqlite"]').exists()).toBe(false);
  });

  it('读回落保护结果失败时按没拦下处理，出错页照旧', async () => {
    api.GetStartupError.mockResolvedValue('数据库未初始化');
    api.GetStartupBackendGuard.mockRejectedValue(new Error('ipc broken'));
    const wrapper = await mountApp();
    expect(wrapper.find('[data-test="startup-guard"]').exists()).toBe(false);
    expect(wrapper.get('.startup-error-text').text()).toBe('数据库未初始化');
  });

  it('读到回落保护结果之前两张卡片都不画：被拦下时不先闪一下通用卡片', async () => {
    api.GetStartupError.mockResolvedValue(BLOCKED_ERROR);
    let resolveGuard;
    api.GetStartupBackendGuard.mockReturnValue(new Promise(done => { resolveGuard = done; }));
    const wrapper = shallowMount(App);
    mountedWrappers.push(wrapper);
    await flushPromises();
    expect(wrapper.find('.startup-error-view').exists()).toBe(true);
    expect(wrapper.find('.startup-error-card').exists()).toBe(false);
    resolveGuard(blockedGuard());
    await flushPromises();
    expect(wrapper.find('[data-test="startup-guard"]').exists()).toBe(true);
    expect(wrapper.findAll('.startup-error-card')).toHaveLength(1);
  });

  it('正常启动不读回落保护结果', async () => {
    await mountApp();
    expect(api.GetStartupBackendGuard).not.toHaveBeenCalled();
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

  // 点图片进观看侧照片流（D-P2-08），由它定位那一张；图片管理不挂载。
  await wrapper.setData({ photoBrowseMounted: true });
  const locate = vi.fn();
  wrapper.findComponent({ name: 'PhotoBrowsePage' }).vm.locateImage = locate;
  wrapper.findComponent({ name: 'RecapPage' }).vm.$emit('open-image', { id: 11, name: 'p.jpg' });
  await flushPromises();
  expect(wrapper.vm.currentPage).toBe('photos');
  expect(locate).toHaveBeenCalledWith({ id: 11, name: 'p.jpg' });
  expect(wrapper.findComponent({ name: 'PhotoLibraryPage' }).exists()).toBe(false);

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
  it('「视频」是海报墙、「片库管理」是常挂载的片库页，各自一个实例', async () => {
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
    expect(wrapper.findComponent({ name: 'PhotoLibraryPage' }).props('pageActive')).toBe(true);
    expect(wrapper.findComponent({ name: 'VideoListPage' }).props('pageActive')).toBe(false);
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
    await wrapper.setData({ currentPage: 'pending-work' });
    wrapper.findComponent({ name: 'PendingWorkPage' }).vm.$emit('run-command', 'library.openAIReview', 'AI 视频标签');
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
    home.vm.$emit('open-task-center');
    await flushPromises();
    expect(wrapper.findComponent({ name: 'TaskCenterDrawer' }).props('open')).toBe(true);
    // 待处理卡片：打开待处理页（D-P3-01），任务中心先收起。
    home.vm.$emit('open-pending-work');
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('pending-work');
    expect(wrapper.findComponent({ name: 'TaskCenterDrawer' }).props('open')).toBe(false);
    await wrapper.setData({ currentPage: 'manage-home' });
    await flushPromises();
    const homeAgain = wrapper.findComponent({ name: 'ManageHomePage' });

    // 图片清理的宿主是清理中心（D-P3-02），切页与换到图片标签同一拍。
    const run = vi.fn();
    registerCommands('test-manage-home', [{ id: 'photos.openCleanup', group: 'action', label: '图片清理', hidden: true, run }]);
    homeAgain.vm.$emit('run-command', 'photos.openCleanup', '图片清理');
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

  it('整页详情「片库管理」：切到片库管理并按带过去的文件名搜索，不打开抽屉', async () => {
    const { wrapper, openPreview } = await onPage('home');
    wrapper.findComponent({ name: 'HomePage' }).vm.$emit('open-video', 7);
    await flushPromises();
    const apply = vi.fn().mockResolvedValue(true);
    wrapper.findComponent({ name: 'VideoListPage' }).vm.applyVideoKeywordCommand = apply;

    wrapper.findComponent({ name: 'VideoDetailPage' }).vm.$emit('manage-in-library', { id: 7, keyword: ' v7.mp4 ' });
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('library-manage');
    expect(apply).toHaveBeenCalledWith('v7.mp4');
    expect(openPreview).not.toHaveBeenCalled();
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

    await wrapper.setData({ currentPage: 'people', photoBrowseMounted: true });
    const locate = vi.fn();
    wrapper.findComponent({ name: 'PhotoBrowsePage' }).vm.locateImage = locate;
    const person = wrapper.findAllComponents({ name: 'EntityBrowsePage' }).find(page => page.props('kind') === 'person');
    person.vm.$emit('open-image', { id: 11, name: 'p.jpg' });
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('photos');
    expect(locate).toHaveBeenCalledWith({ id: 11, name: 'p.jpg' });
  });

  it('「在管理中编辑」去管理侧人物与作品集，落在对应的标签上并定位到这个对象（置一次即清空）', async () => {
    const wrapper = await mountApp();
    await wrapper.setData({ currentPage: 'collections' });
    wrapper.findComponent({ name: 'EntityBrowsePage' }).vm.$emit('edit-in-manage', { kind: 'collection', id: 5, name: '旅行' });
    expect(wrapper.vm.currentPage).toBe('entities-manage');
    expect(wrapper.vm.area).toBe('manage');
    expect(wrapper.vm.entitiesManageTab).toBe('collection');
    expect(wrapper.vm.entitiesManageFocus).toEqual({ id: 5, name: '旅行' });
    await flushPromises();
    expect(wrapper.vm.entitiesManageFocus).toBeNull();
    const manage = wrapper.findComponent({ name: 'EntityLibraryPage' });
    expect(manage.props('tab')).toBe('collection');
    expect(manage.attributes('data-test')).toBe('entities-manage');
    expect(wrapper.get('[data-test="nav-entities-manage"]').classes()).toContain('active');
    expect(wrapper.vm.cinemaSurface).toBe(false);
  });

  it('空列表的「去人物与作品集管理」只切过去并挂上那一种，不定位', async () => {
    const wrapper = await mountApp();
    await wrapper.setData({ currentPage: 'people' });
    await wrapper.vm.openEntityInManage({ kind: 'person' });
    expect(wrapper.vm.currentPage).toBe('entities-manage');
    expect(wrapper.vm.entitiesManageTab).toBe('person');
    expect(wrapper.vm.entitiesManageFocus).toBeNull();
  });

  // D-P3-04：标签归 EntityLibraryPage 自己（人物 · 作品集 · 人脸审阅），App 只把它当页面参数落下：点标签 → update:tab →
  // navigateTo('entities-manage', { tab })，同一个实例只换标签（不再按种类重挂）；认不出的标签忽略。
  it('管理侧人物与作品集：标签是页面参数，换标签不重挂；App 外面不再套一层页签', async () => {
    const wrapper = await mountApp();
    await enterManage(wrapper);
    await wrapper.get('[data-test="nav-entities-manage"]').trigger('click');
    expect(wrapper.find('.entities-manage__tabs').exists()).toBe(false);
    const page = wrapper.findComponent({ name: 'EntityLibraryPage' });
    expect(page.props('tab')).toBe('person');

    page.vm.$emit('update:tab', 'collection');
    await flushPromises();
    expect(wrapper.vm.entitiesManageTab).toBe('collection');
    expect(wrapper.findComponent({ name: 'EntityLibraryPage' }).vm).toBe(page.vm);
    expect(page.props('tab')).toBe('collection');

    page.vm.$emit('update:tab', 'faces');
    await flushPromises();
    expect(page.props('tab')).toBe('faces');
    wrapper.vm.navigateTo('entities-manage', { tab: 'nope' });
    await flushPromises();
    expect(page.props('tab')).toBe('faces');

    // 从别处带着标签进来（命令面板、「在管理中编辑」）：切页与换标签同一拍。
    await wrapper.setData({ currentPage: 'insights' });
    wrapper.vm.navigateTo('entities-manage', { tab: 'person' });
    await flushPromises();
    expect(wrapper.findComponent({ name: 'EntityLibraryPage' }).props('tab')).toBe('person');
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
  it('在「视频」上按 ⌘⇧K 切到清理中心（管理区）；片库页不再有 cleanupHotkey', async () => {
    const wrapper = await mountApp();
    await wrapper.setData({ currentPage: 'videos' });
    expect(wrapper.findComponent({ name: 'VideoListPage' }).vm.$options.props.cleanupHotkey).toBeUndefined();

    const event = new KeyboardEvent('keydown', { key: 'K', code: 'KeyK', metaKey: true, shiftKey: true, bubbles: true, cancelable: true });
    window.dispatchEvent(event);
    await flushPromises();

    expect(event.defaultPrevented).toBe(true);
    expect(wrapper.vm.currentPage).toBe('cleanup');
    expect(wrapper.find('[data-test="nav-area-manage"]').exists()).toBe(true);
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
    // 同一页上换标签也拦下（独立评审 P3 #1）：另一个标签一挂上 / 卸下会打断正在进行的删除。不换标签、不带请求的照常。
    resetFeedback();
    expect(wrapper.vm.navigateTo('cleanup', { tab: 'image' })).toBe(false);
    expect(wrapper.vm.cleanupTab).toBe('video');
    expect(feedbackState.toasts.map(toast => toast.message)).toEqual(['正在移到废纸篓，完成后再离开清理中心。']);
    expect(wrapper.vm.navigateTo('cleanup', { tab: 'video' })).toBe(true);
    expect(wrapper.vm.navigateTo('cleanup')).toBe(true);
    page.vm.$emit('busy-change', false);
    await flushPromises();
    expect(wrapper.vm.navigateTo('insights')).toBe(true);
    expect(wrapper.vm.currentPage).toBe('insights');
  });

  // 独立评审 P3 #1：删除进行中，同一页上的换标签与新请求也拦下（原先只拦离开）。各入口：命令面板 / 待处理的宿主命令
  // （library.openCleanup、photos.openCleanup 带着标签）、任务中心打开集中整理任务。拦下时提示同一句、命令不执行、请求不落下。
  it('删除进行中：命令面板与待处理的「清理中心 · 视频 / 图片」不换标签、不执行，提示同一句', async () => {
    const wrapper = await mountApp();
    const page = await enterCleanup(wrapper, { tab: 'image' });
    page.vm.$emit('busy-change', true);
    await flushPromises();
    const run = vi.fn();
    registerCommands('test-busy-cleanup', [{ id: 'library.openCleanup', group: 'action', label: '清理中心 · 视频', hidden: true, run }]);
    resetFeedback();

    expect(await wrapper.vm.enterCommandHost('library.openCleanup')).toBe(false);
    expect(await wrapper.vm.prepareCommandRun({ id: 'library.openCleanup' })).toBe(false);
    expect(await wrapper.vm.runHostCommand('library.openCleanup', '清理候选')).toBe(false);
    await flushPromises();
    unregisterCommands('test-busy-cleanup');

    expect(run).not.toHaveBeenCalled();
    expect(wrapper.vm.cleanupTab).toBe('image');
    expect(page.props('tab')).toBe('image');
    expect(feedbackState.toasts.map(toast => toast.message)).toEqual(Array(3).fill('正在移到废纸篓，完成后再离开清理中心。'));

    // 删完以后照常换过去。
    page.vm.$emit('busy-change', false);
    await flushPromises();
    expect(await wrapper.vm.enterCommandHost('library.openCleanup')).toBe(true);
    expect(wrapper.vm.cleanupTab).toBe('video');
  });

  it('删除进行中：任务中心打开集中整理任务不换标签、不落请求，任务中心也不收起', async () => {
    const wrapper = await mountApp();
    const page = await enterCleanup(wrapper, { tab: 'image' });
    page.vm.$emit('busy-change', true);
    await flushPromises();
    await wrapper.setData({ taskCenterOpen: true });
    resetFeedback();

    expect(await wrapper.vm.openConsolidationTask(42)).toBe(false);
    await flushPromises();
    expect(wrapper.vm.cleanupTab).toBe('image');
    expect(wrapper.vm.cleanupRequest).toBeNull();
    expect(wrapper.vm.taskCenterOpen).toBe(true);
    expect(feedbackState.toasts.map(toast => toast.message)).toEqual(['正在移到废纸篓，完成后再离开清理中心。']);

    // 视频标签上删着视频时同样不落请求（不在删除中途打开集中整理任务）。
    page.vm.$emit('busy-change', false);
    await wrapper.setData({ cleanupTab: 'video' });
    page.vm.$emit('busy-change', true);
    await flushPromises();
    expect(await wrapper.vm.openConsolidationTask(43)).toBe(false);
    expect(wrapper.vm.cleanupRequest).toBeNull();

    page.vm.$emit('busy-change', false);
    await flushPromises();
    expect(await wrapper.vm.openConsolidationTask(44)).toBe(true);
    expect(wrapper.vm.cleanupRequest).toMatchObject({ consolidationTaskID: 44 });
    expect(wrapper.vm.taskCenterOpen).toBe(false);
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

  // 启动失败时第一屏先闪一下影院深色（2026-10-14 UI 遗留 F2）：GetStartupError 读回来之前默认页 home 已按观看区
  // 打开影院表面、画出首页。读到之前什么都不画、不开影院表面；读到正常启动才开，读到启动失败不开。
  function deferStartup() {
    let resolve;
    api.GetStartupError.mockReturnValue(new Promise(done => { resolve = done; }));
    return value => resolve(value);
  }

  it('首页上没有注入启动状态（直接由 vite 送出）：读到启动状态之前不开影院表面、不画外壳与页面；读到正常启动再打开', async () => {
    expect(document.documentElement.hasAttribute('data-startup')).toBe(false);
    const resolveStartup = deferStartup();
    const wrapper = shallowMount(App);
    mountedWrappers.push(wrapper);
    await flushPromises();
    expect(cinema()).toBe(false);
    expect(wrapper.find('.header').exists()).toBe(false);
    expect(wrapper.find('.main-view').exists()).toBe(false);
    expect(wrapper.findComponent({ name: 'HomePage' }).exists()).toBe(false);
    expect(wrapper.find('.startup-error-view').exists()).toBe(false);

    resolveStartup('');
    await flushPromises();
    expect(cinema()).toBe(true);
    expect(wrapper.find('.header').exists()).toBe(true);
    expect(wrapper.findComponent({ name: 'HomePage' }).exists()).toBe(true);
  });

  it('读到启动失败：第一次画出来的就是启动失败页，影院表面始终没开、首页没挂过', async () => {
    const resolveStartup = deferStartup();
    // body 的 class 每变一次记下变之前的值：影院表面哪怕只开过一瞬也看得出来。
    const before = [];
    const observer = new MutationObserver(records => records.forEach(record => before.push(record.oldValue || '')));
    observer.observe(document.body, { attributes: true, attributeFilter: ['class'], attributeOldValue: true });
    const wrapper = shallowMount(App);
    mountedWrappers.push(wrapper);
    await flushPromises();
    expect(wrapper.findComponent({ name: 'HomePage' }).exists()).toBe(false);
    resolveStartup('数据库未初始化');
    await flushPromises();
    observer.disconnect();
    expect(cinema()).toBe(false);
    expect(before.filter(value => value.split(/\s+/).includes('surface-cinema'))).toEqual([]);
    expect(wrapper.find('.startup-error-view').exists()).toBe(true);
    expect(wrapper.find('.header').exists()).toBe(true);
    expect(wrapper.findComponent({ name: 'HomePage' }).exists()).toBe(false);
  });

  // App 读到启动状态之前只画底色：index.html 在应用脚本之前就按系统深浅定好 data-theme，深色系统下不先闪浅色。
  it('index.html 在应用脚本之前按系统深浅设 data-theme', () => {
    const html = readFileSync(join(process.cwd(), 'index.html'), 'utf8');
    const inline = html.match(/<script>([\s\S]*?)<\/script>/);
    expect(inline).toBeTruthy();
    expect(inline.index).toBeLessThan(html.indexOf('src="./src/main.js"'));
    for (const dark of [true, false]) {
      window.matchMedia = vi.fn(query => ({ matches: query === '(prefers-color-scheme: dark)' && dark }));
      document.documentElement.removeAttribute('data-theme');
      new Function(inline[1])();
      expect(document.documentElement.getAttribute('data-theme')).toBe(dark ? 'dark' : 'light');
    }
  });

  // 第一帧的表面按 Go 送出首页时注入的启动状态定（<html data-startup>，2026-10-14 已知缺口 G2）：浅色系统下
  // 正常启动以前先画几帧浅色底、读到 GetStartupError 才翻成影院深色。外壳与页面仍等读回来，内容以它为准。
  describe('首页上注入的启动状态（data-startup）', () => {
    afterEach(() => {
      document.documentElement.removeAttribute('data-startup');
      document.body.classList.remove('surface-cinema');
    });

    // body 的 class 每变一次记下变之后的值：影院表面中途哪怕只关过一瞬也看得出来。
    function recordBodyClass() {
      const after = [];
      const observer = new MutationObserver(() => after.push(document.body.className));
      observer.observe(document.body, { attributes: true, attributeFilter: ['class'] });
      return { after, stop: () => observer.disconnect() };
    }

    it('ready：第一次渲染就打开影院表面，外壳与页面仍等 GetStartupError；读到正常启动后一直开着', async () => {
      document.documentElement.setAttribute('data-startup', 'ready');
      const resolveStartup = deferStartup();
      const record = recordBodyClass();
      const wrapper = shallowMount(App);
      mountedWrappers.push(wrapper);
      expect(cinema()).toBe(true);
      await flushPromises();
      expect(cinema()).toBe(true);
      expect(wrapper.find('.header').exists()).toBe(false);
      expect(wrapper.findComponent({ name: 'HomePage' }).exists()).toBe(false);

      resolveStartup('');
      await flushPromises();
      record.stop();
      expect(cinema()).toBe(true);
      expect(record.after.filter(value => !value.split(/\s+/).includes('surface-cinema'))).toEqual([]);
      expect(wrapper.find('.header').exists()).toBe(true);
      expect(wrapper.findComponent({ name: 'HomePage' }).exists()).toBe(true);
    });

    it('error：第一帧不开影院表面（跟随系统），读到启动失败后是启动失败页，影院表面始终没开', async () => {
      document.documentElement.setAttribute('data-startup', 'error');
      const resolveStartup = deferStartup();
      const record = recordBodyClass();
      const wrapper = shallowMount(App);
      mountedWrappers.push(wrapper);
      expect(cinema()).toBe(false);
      await flushPromises();
      resolveStartup('backend_fallback_blocked: 上次用的是 PostgreSQL');
      await flushPromises();
      record.stop();
      expect(cinema()).toBe(false);
      expect(record.after.filter(value => value.split(/\s+/).includes('surface-cinema'))).toEqual([]);
      expect(wrapper.find('.startup-error-view').exists()).toBe(true);
    });

    it('只管读到之前：与 GetStartupError 读回来的不一致时以读回来的为准', async () => {
      document.documentElement.setAttribute('data-startup', 'ready');
      api.GetStartupError.mockResolvedValue('数据库未初始化');
      const failed = await mountApp();
      expect(cinema()).toBe(false);
      expect(failed.find('.startup-error-view').exists()).toBe(true);
      failed.unmount();

      document.documentElement.setAttribute('data-startup', 'error');
      api.GetStartupError.mockResolvedValue('');
      const ready = await mountApp();
      expect(cinema()).toBe(true);
      expect(ready.findComponent({ name: 'HomePage' }).exists()).toBe(true);
    });

    // 应用脚本执行之前也可能先画一帧：index.html 在 #app 之前就按注入的状态给 body 打开影院表面。
    it('index.html 在 #app 与应用脚本之前按注入的状态打开影院表面：ready 开，error 与没有注入时不开', () => {
      const html = readFileSync(join(process.cwd(), 'index.html'), 'utf8');
      const body = html.slice(html.indexOf('<body>'));
      const inline = body.match(/<script>([\s\S]*?)<\/script>/);
      expect(inline).toBeTruthy();
      expect(inline.index).toBeLessThan(body.indexOf('<div id="app">'));
      for (const [state, on] of [['ready', true], ['error', false], [null, false]]) {
        document.body.classList.remove('surface-cinema');
        if (state) document.documentElement.setAttribute('data-startup', state);
        else document.documentElement.removeAttribute('data-startup');
        new Function(inline[1])();
        expect(cinema(), String(state)).toBe(on);
      }
    });
  });

  it('读启动状态本身失败时照旧按正常启动画出外壳（与改动前一致），错误照旧交给 Vue 的错误处理', async () => {
    api.GetStartupError.mockRejectedValue(new Error('ipc broken'));
    const errorHandler = vi.fn();
    const wrapper = shallowMount(App, { global: { config: { errorHandler } } });
    mountedWrappers.push(wrapper);
    await flushPromises();
    expect(errorHandler).toHaveBeenCalledTimes(1);
    expect(wrapper.find('.header').exists()).toBe(true);
    expect(wrapper.findComponent({ name: 'HomePage' }).exists()).toBe(true);
    expect(cinema()).toBe(true);
  });

  it('启动失败页跟随系统深浅：深色系统下在等后端状态之前就是深色，系统切换时跟着变', async () => {
    let onSchemeChange;
    window.matchMedia = vi.fn(() => ({ matches: true, addEventListener: (_event, handler) => { onSchemeChange = handler; }, removeEventListener: vi.fn() }));
    api.GetStartupError.mockResolvedValue('数据库未初始化');
    api.GetDatabaseBackendStatus.mockReturnValueOnce(new Promise(() => {}));
    document.documentElement.removeAttribute('data-theme');
    await mountApp();
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark');
    onSchemeChange({ matches: false });
    expect(document.documentElement.getAttribute('data-theme')).toBe('light');
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

// D-P2-08：「图片」是观看侧照片流（PhotoBrowsePage），「图片管理」仍是 PhotoLibraryPage；两页各一个实例、筛选各自独立，
// 各自只在自己的页面上显示。点图片、「去图片」都进照片流；「在图片管理中打开」进管理页定位；别处改了图片时照片流收到新的版本号。
describe('观看侧照片流的接线（D-P2-08）', () => {
  it('「图片」挂照片流、「图片管理」挂管理页：首次进入才挂载，切走只隐藏，互不跟着显示', async () => {
    const wrapper = await mountApp();
    expect(wrapper.findComponent({ name: 'PhotoBrowsePage' }).exists()).toBe(false);
    await wrapper.get('[data-test="nav-photos"]').trigger('click');
    const browse = wrapper.findComponent({ name: 'PhotoBrowsePage' });
    expect(browse.props('pageActive')).toBe(true);
    expect(browse.isVisible()).toBe(true);
    // 管理页不再跟着「图片」挂载（它的批量、删除、打标签、AI、目录都不出现在观看侧）。
    expect(wrapper.findComponent({ name: 'PhotoLibraryPage' }).exists()).toBe(false);

    await enterManage(wrapper);
    await wrapper.get('[data-test="nav-photos-manage"]').trigger('click');
    const manage = wrapper.findComponent({ name: 'PhotoLibraryPage' });
    expect(manage.props('pageActive')).toBe(true);
    expect(manage.isVisible()).toBe(true);
    expect(wrapper.findComponent({ name: 'PhotoBrowsePage' }).vm).toBe(browse.vm);
    expect(wrapper.findComponent({ name: 'PhotoBrowsePage' }).props('pageActive')).toBe(false);
    expect(wrapper.findComponent({ name: 'PhotoBrowsePage' }).isVisible()).toBe(false);

    await wrapper.get('[data-test="nav-back-to-viewer"]').trigger('click');
    await wrapper.get('[data-test="nav-photos"]').trigger('click');
    expect(wrapper.findComponent({ name: 'PhotoBrowsePage' }).vm).toBe(browse.vm);
    expect(wrapper.findComponent({ name: 'PhotoBrowsePage' }).props('pageActive')).toBe(true);
    expect(wrapper.findComponent({ name: 'PhotoLibraryPage' }).vm).toBe(manage.vm);
    expect(wrapper.findComponent({ name: 'PhotoLibraryPage' }).props('pageActive')).toBe(false);
    expect(wrapper.findComponent({ name: 'PhotoLibraryPage' }).isVisible()).toBe(false);
  });

  it('回顾、人物、作品集里点图片：进照片流并交给它定位那一张，图片管理不参与', async () => {
    const wrapper = await mountApp();
    await wrapper.setData({ currentPage: 'recap', photoBrowseMounted: true });
    const locate = vi.fn();
    wrapper.findComponent({ name: 'PhotoBrowsePage' }).vm.locateImage = locate;
    wrapper.findComponent({ name: 'RecapPage' }).vm.$emit('open-image', { id: 11, name: 'p.jpg' });
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('photos');
    expect(locate).toHaveBeenLastCalledWith({ id: 11, name: 'p.jpg' });

    for (const [page, kind] of [['people', 'person'], ['collections', 'collection']]) {
      await wrapper.setData({ currentPage: page });
      const entity = wrapper.findAllComponents({ name: 'EntityBrowsePage' }).find(item => item.props('kind') === kind);
      entity.vm.$emit('open-image', { id: 12, name: `${kind}.jpg` });
      await flushPromises();
      expect(wrapper.vm.currentPage).toBe('photos');
      expect(locate).toHaveBeenLastCalledWith({ id: 12, name: `${kind}.jpg` });
    }
    expect(locate).toHaveBeenCalledTimes(3);
    expect(wrapper.findComponent({ name: 'PhotoLibraryPage' }).exists()).toBe(false);
  });

  it('照片流「在图片管理中打开」：进图片管理（管理区）并交给管理页定位那一张', async () => {
    const wrapper = await mountApp();
    await wrapper.setData({ currentPage: 'photos', photosMounted: true });
    const locate = vi.fn();
    wrapper.findComponent({ name: 'PhotoLibraryPage' }).vm.locateImage = locate;
    wrapper.findComponent({ name: 'PhotoBrowsePage' }).vm.$emit('open-in-manage', { id: 7, name: 'a.jpg' });
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('photos-manage');
    expect(wrapper.vm.area).toBe('manage');
    expect(locate).toHaveBeenCalledWith({ id: 7, name: 'a.jpg' });
  });

  it('人物页「去图片」带上人物：navigateTo(\'photos\', { personId }) 作为一次性参数交给照片流；不带参数的切页不改它', async () => {
    const wrapper = await mountApp();
    await wrapper.setData({ currentPage: 'people' });
    const person = wrapper.findAllComponents({ name: 'EntityBrowsePage' }).find(page => page.props('kind') === 'person');
    person.vm.$emit('navigate', 'photos', { personId: 5, personName: '甲' });
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('photos');
    const browse = () => wrapper.findComponent({ name: 'PhotoBrowsePage' });
    expect(browse().props('navigationRequest')).toEqual({ personId: 5, personName: '甲', token: 1 });

    wrapper.vm.navigateTo('home');
    wrapper.vm.navigateTo('photos');
    await flushPromises();
    expect(browse().props('navigationRequest')).toEqual({ personId: 5, personName: '甲', token: 1 });
  });

  it('别处改了图片，照片流收到新的版本号：离开图片管理、清理中心删除或撤销、数据维护撤销图片批量编辑、标签转人物', async () => {
    const wrapper = await mountApp();
    await wrapper.setData({ currentPage: 'photos', photosMounted: true });
    const manage = wrapper.findComponent({ name: 'PhotoLibraryPage' }).vm;
    manage.handleCleanupDeleted = vi.fn();
    manage.markImagesChanged = vi.fn();
    manage.handleTagPersonConverted = vi.fn();
    const version = () => wrapper.findComponent({ name: 'PhotoBrowsePage' }).props('imagesVersion');
    const start = version();

    // 只是进去不算，离开图片管理才算（那里删除、收藏、扫描、打标签都可能改了图片）。
    await wrapper.setData({ currentPage: 'photos-manage' });
    expect(version()).toBe(start);
    await wrapper.setData({ currentPage: 'cleanup' });
    expect(version()).toBe(start + 1);

    wrapper.findComponent({ name: 'CleanupCenterPage' }).vm.$emit('images-changed');
    await flushPromises();
    expect(manage.handleCleanupDeleted).toHaveBeenCalledOnce();
    expect(version()).toBe(start + 2);

    await wrapper.setData({ currentPage: 'data-care' });
    wrapper.findComponent({ name: 'DataCarePage' }).vm.$emit('videos-changed', { removedIDs: [], kind: 'image' });
    await flushPromises();
    expect(manage.markImagesChanged).toHaveBeenCalledOnce();
    expect(version()).toBe(start + 3);
    // 视频的改动不惊动照片流。
    wrapper.findComponent({ name: 'VideoListPage' }).vm.handleVideosChangedElsewhere = vi.fn();
    wrapper.findComponent({ name: 'DataCarePage' }).vm.$emit('videos-changed', { removedIDs: [3] });
    await flushPromises();
    expect(version()).toBe(start + 3);

    wrapper.findComponent({ name: 'VideoListPage' }).vm.$emit('person-converted', { tag_id: 4, person: { id: 9 } });
    await flushPromises();
    expect(manage.handleTagPersonConverted).toHaveBeenCalledOnce();
    expect(version()).toBe(start + 4);
  });

  it('照片流里改了收藏或评分：并到图片管理已加载的那一张上；管理页还没挂载时什么也不做', async () => {
    const wrapper = await mountApp();
    await wrapper.setData({ currentPage: 'photos' });
    wrapper.findComponent({ name: 'PhotoBrowsePage' }).vm.$emit('image-changed', { id: 3, is_favorite: true });
    await flushPromises();
    expect(wrapper.findComponent({ name: 'PhotoLibraryPage' }).exists()).toBe(false);

    await wrapper.setData({ photosMounted: true });
    const patch = vi.fn();
    wrapper.findComponent({ name: 'PhotoLibraryPage' }).vm.patchImage = patch;
    wrapper.findComponent({ name: 'PhotoBrowsePage' }).vm.$emit('image-changed', { id: 3, personal_rating: 8 });
    expect(patch).toHaveBeenCalledWith({ id: 3, personal_rating: 8 });
  });

  it('照片流的 navigate（空图片库「去图片管理添加目录」）与人物芯片走 App 的切页与打开人物', async () => {
    const wrapper = await mountApp();
    await wrapper.setData({ currentPage: 'photos' });
    wrapper.findComponent({ name: 'PhotoBrowsePage' }).vm.$emit('open-person', { id: 8, name: '甲' });
    // 与命令面板打开人物同一个入口：同一拍切到人物页并把要打开的人交给它（交完即清空）。
    expect(wrapper.vm.currentPage).toBe('people');
    expect(wrapper.vm.entityFocus.person).toEqual({ id: 8, name: '甲' });
    await flushPromises();

    await wrapper.setData({ currentPage: 'photos' });
    wrapper.findComponent({ name: 'PhotoBrowsePage' }).vm.$emit('navigate', 'photos-manage');
    await flushPromises();
    expect(wrapper.vm.currentPage).toBe('photos-manage');
  });
});


it('详情播放通知交给常挂载的首页，仅替换对应今晚推荐', async () => {
  const wrapper = await mountApp();
  const home = wrapper.findComponent({ name: 'HomePage' });
  const replace = vi.fn();
  home.vm.replacePlayedTonight = replace;
  api.GetVideosByIDs.mockResolvedValue([{ id: 7, name: '7.mp4' }]);
  home.vm.$emit('open-video', 7);
  await flushPromises();
  wrapper.findComponent({ name: 'VideoDetailPage' }).vm.$emit('playback-started', 7);
  expect(replace).toHaveBeenCalledWith(7);
});


it('从其他入口打开详情会清除旧的今晚来源标记', async () => {
  const wrapper = await mountApp();
  const clear = vi.fn();
  wrapper.findComponent({ name: 'HomePage' }).vm.clearPendingTonight = clear;
  wrapper.vm.openVideo({ id: 7 }, { from: 'home' });
  expect(clear).not.toHaveBeenCalled();
  wrapper.vm.openVideo({ id: 7 }, { from: 'videos' });
  expect(clear).toHaveBeenCalledTimes(1);
});
