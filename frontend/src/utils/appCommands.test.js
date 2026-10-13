import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'CreateDatabaseBackup', 'GetBackgroundTasks', 'GetIdleSchedulerStatus', 'ListCollections', 'ListPeople',
  'ListSavedLibraryViews', 'LogFrontend',
  'UpdateSettings', 'TriggerAITagging',
  'GetLibraryWatcherStatus',
  'StartFrameHashBackfill', 'CancelFrameHashBackfill', 'SyncImageDirectories'
].map(name => [name, vi.fn()])));

vi.mock('../../wailsjs/go/main/App', () => api);

import {
  ACTION_COMMAND_PAGES, appCommandsMixin, cleanupParamsChangePage, COMMAND_HOST_PAGES, COMMAND_PALETTE_PAGES, COMMAND_PALETTE_SMART_VIEWS,
  DEFAULT_ENTITIES_MANAGE_TAB, DEFAULT_LIST_TAB, ENTITIES_MANAGE_TABS, isEntitiesManageTab, isListTab, LIBRARY_MANAGE_SMART_VIEWS,
  LIST_TABS, LISTS_PAGE, MANAGE_NAV_GROUPS, pageArea, resolvePageRoute, smartViewTarget, VIDEO_BROWSE_SMART_VIEWS, VIDEO_DETAIL_PAGE,
  VIEWER_NAV_GROUPS
} from './appCommands.js';
import { commandList, findCommand, registerCommands, unregisterCommands } from './commandRegistry.js';
import { confirmAction, feedbackState, resetFeedback, resolveConfirm } from './feedback.js';
import { SETTINGS_SECTIONS } from '../components/SettingsPage.vue';
import CommandPalette from '../components/CommandPalette.vue';
import BaseModal from '../components/ui/BaseModal.vue';

// 只借 mixin：App.vue 的其余部分（页面、主题、启动错误）与命令注册无关。
// navigateTo / openTaskCenter / openPendingWork 是 App.vue 提供给 mixin 的方法，这里给最简实现。
const Host = {
  mixins: [appCommandsMixin],
  props: { settings: { type: Object, default: () => ({}) } },
  data() {
    return { currentPage: 'videos', startupError: '', taskCenterOpen: false, pendingWorkOpen: false, navigateParams: null };
  },
  methods: {
    debugLog: vi.fn(),
    // params：App.vue 交给目标页的一次性参数（「视频」海报墙的 { watch } 等），这里记下最近一次；
    // 人物与作品集的 { tab } 照 App 的 applyPageParams 落到 entitiesManageTab 上。
    navigateTo(page, params = null) {
      this.currentPage = page;
      this.navigateParams = params;
      if (page === 'entities-manage' && params?.tab) this.entitiesManageTab = params.tab;
      return true;
    },
    openVideo() {},
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
    commandByID('nav:page:lists').run();
    expect(wrapper.vm.currentPage).toBe('lists');
    // 旧的片单页面命令把别名 key 交给 navigateTo，由 App 换成片单的对应标签（App.test.js 钉住落点）。
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

  // 顶栏有入口、命令面板没有，是榜单与已看两页当初漏掉的那种失配。现在顶栏直接渲染两个区域的
  // 导航组（D-UI-09），这条钉住两件事：App.vue 真的按这两份清单渲染；两边每一页在命令面板都有条目。
  // 管理侧带 command 的两项是动作，不是页面：走既有的同 ID 命令，不另开导航命令。由动作改成页面的（待处理，D-P3-01）
  // 在命令面板里仍由原来的动作命令代表（ACTION_COMMAND_PAGES），也不另开导航命令。
  it('APP-12 顶栏与命令面板读同一份页面清单，两个区域的每一页在导航组里都有条目', () => {
    const source = readFileSync(resolve(process.cwd(), 'src/App.vue'), 'utf8');
    expect(source).toMatch(/VIEWER_NAV_GROUPS/);
    expect(source).toMatch(/MANAGE_NAV_GROUPS/);
    expect(source).toMatch(/v-for="page in group\.pages"/);

    const keys = COMMAND_PALETTE_PAGES.map(page => page.key);
    for (const group of [...VIEWER_NAV_GROUPS, ...MANAGE_NAV_GROUPS]) {
      for (const page of group.pages) {
        if (page.command || ACTION_COMMAND_PAGES[page.key]) expect(keys, page.key).not.toContain(page.key);
        else expect(keys, page.key).toContain(page.key);
      }
    }
    expect(keys).not.toContain(VIDEO_DETAIL_PAGE);
    expect(VIEWER_NAV_GROUPS.map(group => group.label)).toEqual(['浏览', '片单', '回顾与检索']);
    expect(MANAGE_NAV_GROUPS.map(group => group.label)).toEqual(['片库', '待处理与清理', '工具', '系统']);
  });

  it('D-UI-09 每个页面 key 只属于一个区域；视频与片库管理、图片与图片管理是两个 key', () => {
    const viewer = VIEWER_NAV_GROUPS.flatMap(group => group.pages).map(page => page.key);
    const manage = MANAGE_NAV_GROUPS.flatMap(group => group.pages).filter(page => !page.command).map(page => page.key);
    expect(viewer).toEqual(['home', 'videos', 'people', 'collections', 'photos', 'lists', 'recap', 'scenes']);
    // 待处理（D-P3-01）与清理中心（D-P3-02）三期起是页面。
    expect(manage).toEqual(['manage-home', 'library-manage', 'photos-manage', 'entities-manage', 'pending-work', 'cleanup', 'downloads', 'video-workbench', 'insights', 'data-care', 'settings']);
    expect(viewer.filter(key => manage.includes(key))).toEqual([]);
    for (const key of viewer) expect(pageArea(key), key).toBe('viewer');
    for (const key of manage) expect(pageArea(key), key).toBe('manage');
    expect(pageArea(VIDEO_DETAIL_PAGE)).toBe('viewer');
    // 片单的四个别名同属观看区：拿旧 key 当来源的「打开一部片」照旧进整页详情。
    for (const tab of LIST_TABS) expect(pageArea(tab.key), tab.key).toBe('viewer');
    expect(pageArea('no-such-page')).toBeNull();
    // 管理侧只剩任务中心是动作入口；「待处理」（D-P3-01）与「清理中心」（D-P3-02）三期起是页面。
    expect(MANAGE_NAV_GROUPS.flatMap(group => group.pages).filter(page => page.command).map(page => [page.label, page.command])).toEqual([
      ['任务中心', 'action:task-center']
    ]);
    expect(pageArea('pending-work')).toBe('manage');
    expect(pageArea('cleanup')).toBe('manage');
    expect(ACTION_COMMAND_PAGES).toEqual({ 'pending-work': 'action:pending-work' });
  });

  // D-UI-09：命令面板列出两个区域的全部页面；拆分前就有的导航命令 ID 一个不少、不改名。
  it('D-UI-09 命令面板列出两侧全部页面，原有导航命令 ID 不变', () => {
    const wrapper = host({ settings: { browser_bridge_enabled: true } });
    const existing = ['videos', 'people', 'collections', 'photos', 'viewing-notes', 'recap', 'watchlist', 'movie-chart',
      'watched-movies', 'insights', 'data-care', 'scenes', 'video-workbench', 'downloads', 'settings'];
    for (const key of [...existing, 'home', 'lists', 'manage-home', 'library-manage', 'photos-manage', 'entities-manage']) {
      expect(commandByID(`nav:page:${key}`), key).not.toBeNull();
    }
    expect(commandByID('nav:page:video-detail')).toBeNull();
    // 待处理页在面板里只有原来那条动作命令（命令 ID 不变），不出现两条「打开待处理」。
    expect(commandByID('nav:page:pending-work')).toBeNull();
    expect(commandList().filter(command => command.label === '打开待处理').map(command => command.id)).toEqual(['action:pending-work']);

    commandByID('nav:page:manage-home').run();
    expect(wrapper.vm.currentPage).toBe('manage-home');
    commandByID('nav:page:home').run();
    expect(wrapper.vm.currentPage).toBe('home');
  });

  it('D-P2-04 片单合一：顶栏只有「片单」一项，四个旧页面 key 是它的标签别名，命令面板里紧跟「片单」、标签与命令名不变', () => {
    host();
    expect(VIEWER_NAV_GROUPS.find(group => group.key === 'lists')).toMatchObject({ label: '片单', pages: [{ key: LISTS_PAGE, label: '片单' }] });
    expect(VIEWER_NAV_GROUPS.some(group => 'menu' in group)).toBe(false);
    expect(LIST_TABS.map(tab => [tab.key, tab.label])).toEqual([
      ['watchlist', '想看'], ['movie-chart', '榜单'], ['watched-movies', '观影记录'], ['viewing-notes', '观看笔记']
    ]);
    expect(DEFAULT_LIST_TAB).toBe('watchlist');
    const keys = COMMAND_PALETTE_PAGES.map(page => page.key);
    expect(keys.slice(keys.indexOf('lists'), keys.indexOf('lists') + 5)).toEqual(['lists', 'watchlist', 'movie-chart', 'watched-movies', 'viewing-notes']);
    expect(commandByID('nav:page:lists').label).toBe('打开片单');
    expect(commandByID('nav:page:watchlist').label).toBe('打开想看');
    expect(commandByID('nav:page:viewing-notes').label).toBe('打开观看笔记');
  });

  it('D-P2-04 resolvePageRoute：别名换成 lists 并带上标签（保留其他参数），其余 key 与参数原样', () => {
    expect(resolvePageRoute('watchlist')).toEqual({ page: 'lists', params: { tab: 'watchlist' } });
    expect(resolvePageRoute('viewing-notes', { extra: 1 })).toEqual({ page: 'lists', params: { extra: 1, tab: 'viewing-notes' } });
    expect(resolvePageRoute('lists', { tab: 'movie-chart' })).toEqual({ page: 'lists', params: { tab: 'movie-chart' } });
    expect(resolvePageRoute('lists')).toEqual({ page: 'lists', params: null });
    expect(resolvePageRoute('recap')).toEqual({ page: 'recap', params: null });
    expect(resolvePageRoute('')).toEqual({ page: '', params: null });
    expect(isListTab('watched-movies')).toBe(true);
    expect(isListTab('lists')).toBe(false);
    expect(isListTab(undefined)).toBe(false);
  });

  // 三期（D-P3-03「命令改指向观看侧」）：片库管理只留管理向的视图。观看向的智能视图命令不论在哪一侧都去「视频」海报墙
  // （海报墙认得的带 { watch }，点赞 / 最近播放 / 最近添加不再列出、执行时去「视频」的全部）；管理向的去「片库管理」；
  // 「全部视频」跟着当前区域。保存视图一律在「片库管理」打开。
  it('D-P3-03 智能视图命令：观看向的去「视频」海报墙（不论在哪一侧），管理向的去「片库管理」，「全部」跟着区域', async () => {
    // 片库页在 App.vue 里是 ref="videoListPage"：这里用一个带同名 ref 的元素顶上，接住两个入口。
    const wrapper = mount({ ...Host, template: '<div><i ref="videoListPage" /></div>' });
    wrappers.push(wrapper);
    const applySmartView = vi.fn();
    const applySavedView = vi.fn();
    Object.assign(wrapper.vm.$refs.videoListPage, { applySmartViewCommand: applySmartView, applySavedViewCommand: applySavedView });

    for (const area of ['home', 'insights']) {
      wrapper.vm.currentPage = area;
      await commandByID('nav:smart:favorites').run();
      expect(wrapper.vm.currentPage).toBe('videos');
      expect(wrapper.vm.navigateParams).toEqual({ watch: 'favorites' });
      wrapper.vm.currentPage = area;
      await commandByID('nav:smart:continue_watching').run();
      expect(wrapper.vm.navigateParams).toEqual({ watch: 'continue_watching' });
      wrapper.vm.currentPage = area;
      await findCommand('nav:smart:liked').run();
      expect(wrapper.vm.currentPage).toBe('videos');
      expect(wrapper.vm.navigateParams).toEqual({ watch: '' });
    }
    expect(applySmartView).not.toHaveBeenCalled();

    wrapper.vm.currentPage = 'recap';
    await commandByID('nav:smart:all').run();
    expect(wrapper.vm.currentPage).toBe('videos');
    expect(wrapper.vm.navigateParams).toEqual({ watch: '' });
    wrapper.vm.currentPage = 'insights';
    await commandByID('nav:smart:all').run();
    expect(wrapper.vm.currentPage).toBe('library-manage');
    expect(applySmartView).toHaveBeenLastCalledWith('');

    wrapper.vm.currentPage = 'home';
    await commandByID('nav:smart:stale').run();
    expect(wrapper.vm.currentPage).toBe('library-manage');
    expect(applySmartView).toHaveBeenLastCalledWith('stale');

    wrapper.vm.currentPage = 'settings';
    await wrapper.vm.applySavedViewFromCommand(3);
    expect(wrapper.vm.currentPage).toBe('library-manage');
    wrapper.vm.currentPage = 'videos';
    await wrapper.vm.applySavedViewFromCommand(4);
    expect(wrapper.vm.currentPage).toBe('library-manage');
    expect(applySavedView.mock.calls).toEqual([[3], [4]]);
  });

  it('D-P3-03 smartViewTarget 逐项落点', () => {
    expect(LIBRARY_MANAGE_SMART_VIEWS).toEqual(['', 'untagged', 'no_subtitle', 'local_metadata_updated', 'stale']);
    for (const view of VIDEO_BROWSE_SMART_VIEWS.filter(Boolean)) {
      expect(smartViewTarget(view, 'manage')).toEqual({ page: 'videos', watch: view });
      expect(smartViewTarget(view, 'viewer')).toEqual({ page: 'videos', watch: view });
    }
    for (const view of ['liked', 'recently_played', 'recently_added', 'nope']) {
      expect(smartViewTarget(view, 'manage'), view).toEqual({ page: 'videos', watch: '' });
    }
    for (const view of ['untagged', 'no_subtitle', 'local_metadata_updated', 'stale']) {
      expect(smartViewTarget(view, 'viewer')).toEqual({ page: 'library-manage', view });
    }
    expect(smartViewTarget('', 'manage')).toEqual({ page: 'library-manage', view: '' });
    expect(smartViewTarget(undefined, 'viewer')).toEqual({ page: 'videos', watch: '' });
  });

  // D-P3-03：随机播放与今晚看什么只在观看侧「视频」海报墙上；命令 ID 不变，改由 App 注册，先切到「视频」（首次进入才挂载）
  // 再调海报墙的 playRandom / openTonightPicker。
  it('D-P3-03 action:random-play / action:tonight 切到「视频」海报墙再执行它的随机播放与今晚看什么', async () => {
    const playRandom = vi.fn(() => 'played');
    const openTonightPicker = vi.fn();
    // 海报墙的替身：首次切到「视频」才挂载（App 里 v-if videosMounted），方法挂在组件实例上。
    const BrowseStub = { methods: { playRandom, openTonightPicker }, template: '<i />' };
    const wrapper = mount({
      ...Host,
      components: { BrowseStub },
      data: () => ({ ...Host.data(), browseMounted: false }),
      template: '<div><BrowseStub v-if="browseMounted" ref="videoBrowsePage" /></div>'
    });
    wrappers.push(wrapper);
    const navigate = wrapper.vm.navigateTo;
    wrapper.vm.navigateTo = (page, params) => {
      const result = navigate.call(wrapper.vm, page, params);
      if (page === 'videos') wrapper.vm.browseMounted = true;
      return result;
    };
    wrapper.vm.currentPage = 'library-manage';
    const random = commandByID('action:random-play');
    expect(random.label).toBe('随机播放');
    const pending = random.run();
    expect(wrapper.vm.currentPage).toBe('videos');
    expect(playRandom).not.toHaveBeenCalled();
    expect(await pending).toBe('played');
    expect(playRandom).toHaveBeenCalledTimes(1);

    wrapper.vm.currentPage = 'home';
    await commandByID('action:tonight').run();
    expect(wrapper.vm.currentPage).toBe('videos');
    expect(openTonightPicker).toHaveBeenCalledTimes(1);

    wrapper.vm.currentPage = 'settings';
    wrapper.vm.navigateTo = vi.fn(() => Promise.resolve(false));
    expect(await commandByID('action:random-play').run()).toBe(false);
    expect(playRandom).toHaveBeenCalledTimes(1);
  });

  it('D-UI-07 命令面板打开一部片交给 App 的分流，来源标为命令面板', () => {
    const wrapper = host();
    const openVideo = vi.fn();
    wrapper.vm.openVideo = openVideo;
    wrapper.vm.openVideoFromCommand({ id: 5 });
    expect(openVideo).toHaveBeenCalledWith({ id: 5 }, { from: 'command-palette' });
  });

  it('APP-14 「打开下载」只在开启浏览器插件桥接时注册', async () => {
    const wrapper = host();
    expect(commandByID('nav:page:downloads')).toBeNull();

    await wrapper.setProps({ settings: { browser_bridge_enabled: true } });
    wrapper.vm.registerAppCommands();
    commandByID('nav:page:downloads').run();
    expect(wrapper.vm.currentPage).toBe('downloads');
  });

  // 命令 ID 不变：十二条都还在；海报墙表达不了的观看向视图（点赞、最近播放、最近添加）不在面板里列出（hidden），
  // 按 ID 照样能执行（D-P3-03）。
  it('智能视图逐条注册为导航命令（ID 不变），点赞 / 最近播放 / 最近添加不在面板里列出', () => {
    host();
    const hidden = ['liked', 'recently_played', 'recently_added'];
    for (const view of COMMAND_PALETTE_SMART_VIEWS) {
      const id = `nav:smart:${view.value || 'all'}`;
      const command = findCommand(id);
      expect(command, view.label).not.toBeNull();
      expect(command.label).toBe(`视频 · ${view.label}`);
      expect(Boolean(command.hidden), id).toBe(hidden.includes(view.value));
      expect(commandByID(id) !== null, id).toBe(!hidden.includes(view.value));
    }
    expect(COMMAND_PALETTE_SMART_VIEWS).toHaveLength(12);
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

  // D-P3-02：action:cleanup 三期起打开清理中心页（命令 ID 不变）；面板里已有页面导航「打开清理中心」，这一条只作跳转目标。
  it('D-P3-02 action:cleanup 在任何页面都切到清理中心页，不再经片库管理的宿主命令', async () => {
    const wrapper = host();
    const run = vi.fn();
    registerHost('library.openCleanup', run);
    wrapper.vm.currentPage = 'settings';

    expect(commandByID('action:cleanup')).toBeNull();
    expect(commandByID('nav:page:cleanup').label).toBe('打开清理中心');
    await findCommand('action:cleanup').run();

    expect(wrapper.vm.currentPage).toBe('cleanup');
    expect(wrapper.vm.navigateParams).toBeNull();
    expect(run).not.toHaveBeenCalled();
  });

  // D-UI-03：这些是维护动作，宿主页记管理侧的「片库管理」（与「视频」同一个常挂载片库页）。
  it.each([
    ['action:trash', 'library.openTrash'],
    ['action:tag-manager', 'library.openTagManager']
  ])('APP-12 %s 在任何页面都先切到片库管理，再执行宿主命令 %s', async (commandID, hostID) => {
    const wrapper = host();
    const run = vi.fn();
    registerHost(hostID, run);
    wrapper.vm.currentPage = 'settings';

    await commandByID(commandID).run();

    expect(wrapper.vm.currentPage).toBe('library-manage');
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
      // D-P3-02：两条清理命令的宿主是清理中心。
      'library.openCleanup': 'cleanup',
      'photos.openCleanup': 'cleanup',
      'library.openAIReview': 'library-manage',
      'library.openLocalMetadataUpdates': 'library-manage',
      'library.openCollectionSuggestions': 'library-manage',
      'photos.openAIReview': 'photos-manage',
      // D-P2-06：人物观看页只看不改，人脸审阅是管理侧「人物与作品集」的一个标签（D-P3-04）。
      'people.openFaceReview': 'entities-manage'
    }));
    const wrapper = host();
    wrapper.vm.currentPage = 'settings';

    expect(await wrapper.vm.prepareCommandRun({ id: 'photos.openAIReview' })).toBe(true);
    expect(wrapper.vm.currentPage).toBe('photos-manage');
    expect(await wrapper.vm.prepareCommandRun({ id: 'action:backup-now' })).toBe(true);
    expect(wrapper.vm.currentPage).toBe('photos-manage');
  });

  // D-P3-02：两条清理命令的宿主是清理中心，切页时一并带上标签（同一拍，不会先按上次的标签挂一遍）；
  // 已在清理中心时也把标签交过去（navigateTo 对当前页只落参数）。用户在切页确认里放弃时不执行。
  it('D-P3-02 library.openCleanup / photos.openCleanup 切到清理中心并带上各自的标签，再执行宿主命令', async () => {
    const wrapper = host();
    const runVideo = vi.fn();
    const runImage = vi.fn();
    registerCommands('test-cleanup-center', [
      { id: 'library.openCleanup', group: 'action', label: '视频', hidden: true, run: runVideo },
      { id: 'photos.openCleanup', group: 'action', label: '图片', hidden: true, run: runImage }
    ]);
    const calls = [];
    const navigate = wrapper.vm.navigateTo;
    wrapper.vm.navigateTo = (page, params) => { calls.push([page, params]); return navigate.call(wrapper.vm, page, params); };
    wrapper.vm.currentPage = 'insights';

    await wrapper.vm.runHostCommand('photos.openCleanup', '图片清理');
    expect(calls).toEqual([['cleanup', { tab: 'image' }]]);
    expect(runImage).toHaveBeenCalledTimes(1);

    // 已在清理中心（图片标签）：换到视频标签再执行。
    await wrapper.vm.runHostCommand('library.openCleanup', '清理候选');
    expect(calls[1]).toEqual(['cleanup', { tab: 'video' }]);
    expect(wrapper.vm.currentPage).toBe('cleanup');
    expect(runVideo).toHaveBeenCalledTimes(1);

    wrapper.vm.currentPage = 'settings';
    wrapper.vm.navigateTo = vi.fn(() => Promise.resolve(false));
    expect(await wrapper.vm.runHostCommand('library.openCleanup', '清理候选')).toBe(false);
    expect(runVideo).toHaveBeenCalledTimes(1);
    unregisterCommands('test-cleanup-center');
  });

  // D-P3-04：人脸审阅是「人物与作品集」的第三个标签。切页与换标签同一拍（页面参数 { tab: 'faces' }），已在这一页的别的标签上
  // 时也交一次参数（只换标签）。
  it('D-P3-04 人脸审阅的宿主是「人物与作品集」的「人脸审阅」标签：切页与换标签同一拍，已在这一页时只换标签', async () => {
    const wrapper = host();
    const run = vi.fn();
    registerHost('people.openFaceReview', run);
    wrapper.vm.entitiesManageTab = 'collection';
    wrapper.vm.currentPage = 'settings';
    const seen = [];
    const navigate = wrapper.vm.navigateTo;
    wrapper.vm.navigateTo = (page, params) => { seen.push([page, params]); return navigate.call(wrapper.vm, page, params); };

    expect(await wrapper.vm.runHostCommand('people.openFaceReview', '人脸待命名')).toBeUndefined();
    expect(wrapper.vm.currentPage).toBe('entities-manage');
    expect(wrapper.vm.entitiesManageTab).toBe('faces');
    expect(run).toHaveBeenCalledTimes(1);
    expect(seen).toEqual([['entities-manage', { tab: 'faces' }]]);

    wrapper.vm.entitiesManageTab = 'collection';
    expect(await wrapper.vm.prepareCommandRun({ id: 'people.openFaceReview' })).toBe(true);
    expect(wrapper.vm.entitiesManageTab).toBe('faces');

    // 别的宿主命令不动人物与作品集的标签。
    wrapper.vm.entitiesManageTab = 'collection';
    expect(await wrapper.vm.prepareCommandRun({ id: 'photos.openAIReview' })).toBe(true);
    expect(wrapper.vm.entitiesManageTab).toBe('collection');
  });

  it('切页确认里放弃时，人脸审阅不换标签也不执行', async () => {
    const wrapper = host();
    const run = vi.fn();
    registerHost('people.openFaceReview', run);
    wrapper.vm.entitiesManageTab = 'collection';
    wrapper.vm.currentPage = 'settings';
    wrapper.vm.navigateTo = vi.fn(() => Promise.resolve(false));

    expect(await wrapper.vm.runHostCommand('people.openFaceReview', '人脸待命名')).toBe(false);
    expect(wrapper.vm.entitiesManageTab).toBe('collection');
    expect(run).not.toHaveBeenCalled();
  });

  it('D-P3-04 人物与作品集的三个标签与校验', () => {
    expect(ENTITIES_MANAGE_TABS.map(tab => [tab.key, tab.label])).toEqual([['person', '人物'], ['collection', '作品集'], ['faces', '人脸审阅']]);
    expect(DEFAULT_ENTITIES_MANAGE_TAB).toBe('person');
    expect(isEntitiesManageTab('faces')).toBe(true);
    expect(isEntitiesManageTab('people')).toBe(false);
    expect(isEntitiesManageTab(undefined)).toBe(false);
  });

  // 清理中心删除流程中（busy）拦不拦一组落到清理中心的参数：换到另一个标签、或带一次性请求就算改动。
  it('cleanupParamsChangePage：换标签或带请求才算改动这一页', () => {
    expect(cleanupParamsChangePage(null, 'image')).toBe(false);
    expect(cleanupParamsChangePage({ tab: 'image' }, 'image')).toBe(false);
    expect(cleanupParamsChangePage({ tab: 'video' }, 'image')).toBe(true);
    expect(cleanupParamsChangePage({ tab: 'nope' }, 'image')).toBe(false);
    expect(cleanupParamsChangePage({ tab: 'video', consolidationTaskID: 42 }, 'video')).toBe(true);
    expect(cleanupParamsChangePage({ relationId: 9 }, 'video')).toBe(true);
    expect(cleanupParamsChangePage({ open: true }, 'video')).toBe(true);
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

  it('「在管理中编辑」进管理侧人物与作品集的对应标签（页面参数 { tab }）、交出要定位的对象后清空；没有 id 只切过去', async () => {
    const wrapper = host();
    const pending = wrapper.vm.openEntityInManage({ kind: 'collection', id: 5, name: '旅行' });
    expect(wrapper.vm.currentPage).toBe('entities-manage');
    expect(wrapper.vm.navigateParams).toEqual({ tab: 'collection' });
    expect(wrapper.vm.entitiesManageTab).toBe('collection');
    expect(wrapper.vm.entitiesManageFocus).toEqual({ id: 5, name: '旅行' });
    await pending;
    expect(wrapper.vm.entitiesManageFocus).toBeNull();

    wrapper.vm.currentPage = 'people';
    await wrapper.vm.openEntityInManage({ kind: 'person' });
    expect(wrapper.vm.currentPage).toBe('entities-manage');
    expect(wrapper.vm.entitiesManageTab).toBe('person');
    expect(wrapper.vm.entitiesManageFocus).toBeNull();

    wrapper.vm.currentPage = 'settings';
    wrapper.vm.navigateTo = vi.fn(() => Promise.resolve(false));
    expect(await wrapper.vm.openEntityInManage({ kind: 'collection', id: 9 })).toBe(false);
    expect(wrapper.vm.entitiesManageTab).toBe('person');
    expect(wrapper.vm.entitiesManageFocus).toBeNull();
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
// D-UD-03：⌘K / ⌘⇧P 开关命令面板。面板开着时再按关掉；面板没开、别的对话框或确认框开着时不响应
// （与 ⌘⇧K 同一条规矩）。宿主渲染真的 CommandPalette 与 BaseModal，面板自己的 role="dialog" 也在 DOM 里。
describe('命令面板快捷键与其他对话框（D-UD-03）', () => {
  const PaletteHost = {
    mixins: [appCommandsMixin],
    components: { CommandPalette, BaseModal },
    data() {
      return { currentPage: 'videos', startupError: '', otherDialog: false, drawerOpen: false };
    },
    methods: {
      debugLog: vi.fn(),
      navigateTo(page) { this.currentPage = page; return true; },
      openVideo() {},
      openTaskCenter() {},
      openPendingWork() {}
    },
    template: `<div>
      <CommandPalette :open="commandPaletteOpen" :open-video="openVideo" @close="closeCommandPalette" />
      <BaseModal v-if="otherDialog" data-test="other-dialog" @close="otherDialog = false"><p>别的对话框</p></BaseModal>
      <aside v-if="drawerOpen" role="dialog" aria-modal="false" data-test="non-modal-drawer">抽屉</aside>
    </div>`
  };

  function mountPaletteHost() {
    const wrapper = mount(PaletteHost, { attachTo: document.body });
    wrappers.push(wrapper);
    return wrapper;
  }

  function press(init) {
    const event = new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init });
    window.dispatchEvent(event);
    return event;
  }
  const cmdK = { key: 'k', code: 'KeyK', metaKey: true };
  const cmdShiftP = { key: 'P', code: 'KeyP', metaKey: true, shiftKey: true };

  it('nothing else open: ⌘K opens the palette', async () => {
    const wrapper = mountPaletteHost();
    const event = press(cmdK);
    await flushPromises();
    expect(event.defaultPrevented).toBe(true);
    expect(wrapper.vm.commandPaletteOpen).toBe(true);
    expect(wrapper.find('[data-test="command-palette"]').attributes('role')).toBe('dialog');
  });

  it('the palette itself open: ⌘K and ⌘⇧P close it (toggle)', async () => {
    const wrapper = mountPaletteHost();
    press(cmdK);
    await flushPromises();
    const closing = press(cmdK);
    await flushPromises();
    expect(closing.defaultPrevented).toBe(true);
    expect(wrapper.vm.commandPaletteOpen).toBe(false);
    expect(wrapper.find('[data-test="command-palette"]').exists()).toBe(false);

    press(cmdShiftP);
    await flushPromises();
    expect(wrapper.vm.commandPaletteOpen).toBe(true);
    press(cmdShiftP);
    await flushPromises();
    expect(wrapper.vm.commandPaletteOpen).toBe(false);
  });

  it('a BaseModal dialog open: ⌘K and ⌘⇧P do nothing and leave the key alone', async () => {
    const wrapper = mountPaletteHost();
    await wrapper.setData({ otherDialog: true });
    const k = press(cmdK);
    const p = press(cmdShiftP);
    await flushPromises();
    expect(k.defaultPrevented).toBe(false);
    expect(p.defaultPrevented).toBe(false);
    expect(wrapper.vm.commandPaletteOpen).toBe(false);
    expect(api.ListSavedLibraryViews).not.toHaveBeenCalled();

    await wrapper.setData({ otherDialog: false });
    press(cmdK);
    await flushPromises();
    expect(wrapper.vm.commandPaletteOpen).toBe(true);
  });

  it('a non-modal panel open (aria-modal="false", e.g. the library drawer or a popover): ⌘K and ⌘⇧P still work', async () => {
    const wrapper = mountPaletteHost();
    await wrapper.setData({ drawerOpen: true });
    const k = press(cmdK);
    await flushPromises();
    expect(k.defaultPrevented).toBe(true);
    expect(wrapper.vm.commandPaletteOpen).toBe(true);
    press(cmdK);
    await flushPromises();
    expect(wrapper.vm.commandPaletteOpen).toBe(false);
    press(cmdShiftP);
    await flushPromises();
    expect(wrapper.vm.commandPaletteOpen).toBe(true);
  });

  it('a confirm pending (even before AppFeedback renders it): ⌘K does nothing until it is answered', async () => {
    const wrapper = mountPaletteHost();
    const answer = confirmAction({ title: '从片库移除', message: '移除 2 部视频的记录。' });
    expect(feedbackState.confirm).not.toBe(null);
    const event = press(cmdK);
    await flushPromises();
    expect(event.defaultPrevented).toBe(false);
    expect(wrapper.vm.commandPaletteOpen).toBe(false);

    resolveConfirm(false);
    expect(await answer).toBe(false);
    press(cmdK);
    await flushPromises();
    expect(wrapper.vm.commandPaletteOpen).toBe(true);
  });
});

describe('智能视图清单防漂移', () => {
  // 三期（D-P3-03）片库管理只留管理向的视图：命令清单里不带 viewer 的几项就是工具栏的全部选项；带 viewer 的观看向视图
  // 命令 ID 照旧、改指向观看侧（smartViewTarget）。
  it('与 LibraryToolbar.vue 的 smartViewOptions 逐条一致（含 META-09 的「本地资料有更新」）', () => {
    // vitest 的根就是 frontend/，jsdom 环境下 import.meta.url 不是 file: 协议。
    const source = readFileSync(resolve(process.cwd(), 'src/components/video-list/LibraryToolbar.vue'), 'utf8');
    const block = /smartViewOptions:\s*\[([\s\S]*?)\]/.exec(source);
    expect(block, 'LibraryToolbar.vue 里应当能找到 smartViewOptions').not.toBeNull();

    const fromToolbar = [...block[1].matchAll(/\{\s*label:\s*'([^']*)',\s*value:\s*'([^']*)'\s*\}/g)]
      .map(match => ({ label: match[1], value: match[2] }));

    expect(fromToolbar.length).toBeGreaterThan(0);
    expect(COMMAND_PALETTE_SMART_VIEWS.filter(view => !view.viewer).map(({ label, value }) => ({ label, value }))).toEqual(fromToolbar);
    expect(LIBRARY_MANAGE_SMART_VIEWS).toEqual(fromToolbar.map(view => view.value));
    expect(COMMAND_PALETTE_SMART_VIEWS.filter(view => view.viewer).map(view => view.value))
      .toEqual(['continue_watching', 'favorites', 'liked', 'recently_played', 'unwatched', 'watched', 'recently_added']);
    expect(fromToolbar).toContainEqual({ label: '本地资料有更新', value: 'local_metadata_updated' });
  });
});
