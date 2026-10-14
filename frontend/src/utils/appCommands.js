// App.vue 的命令面板接线（D-029、D-PC60、D-UD-03）：全局 ⌘K 与 ⌘⇧P（命令面板）、⌘⇧K（清理中心）、面板开关、导航组与
// 应用级动作组、以及跟着事件刷新的任务组；另外收口顶栏的信息架构（D-PC59）与「按固定命令 ID
// 跳到宿主页」（待处理页与全局命令共用，详细设计 §6.1）。
//
// 做成 mixin 而不是直接写在 App.vue 里：App.vue 是多个切片共同修改的文件，
// 命令面板只在那里留最小的挂载点（mixins、一个组件标签、两个 ref）。
// 因此这里依赖 App.vue 的状态、ref 与方法：
//   this.currentPage / this.startupError / this.settings（可缺省）
//   this.$refs.videoListPage（常挂载，v-show 切换）/ this.$refs.videoBrowsePage（首次进入「视频」才挂载）/ this.$refs.settingsPage（v-if）
//   this.navigateTo(page, params?)：切页（别名 key 由它换成真正的页面，见 resolvePageRoute）；params 是交给目标页的一次性参数
//     （「视频」认 { keyword } / { watch } / { tagId }；片单、清理中心、人物与作品集认 { tab }；人物 / 作品集认 { keyword }；
//     场景检索认 { query, mode } / { image }）。
//     能直接切时同步返回 true；设置页有未保存修改时返回 Promise，用户放弃切页时 resolve 为 false（见下面的 whenNavigated）
//   this.openTaskCenter() / this.openPendingWork()：打开任务中心抽屉与待处理页（D-P3-01 起是管理侧页面 pending-work）
//   this.openVideo(video, { from })：打开一部片，按调用方区域分流到整页详情或片库管理的抽屉（D-UI-07）
// 以及 App.vue 的 debugLog()。

import {
  CreateDatabaseBackup, GetBackgroundTasks, GetIdleSchedulerStatus, ListCollections, ListPeople, ListSavedLibraryViews,
  SyncImageDirectories
} from '../../wailsjs/go/main/App';
import { SETTINGS_SECTIONS } from '../components/SettingsPage.vue';
import { findCommand, isCommandEnabled, registerCommands, unregisterCommands } from './commandRegistry.js';
import { feedbackState, notifyError } from '../utils/feedback.js';
import { buildTaskCommands } from './taskCommands.js';
import { recordBackgroundTasks } from './sessionChanges.js';

// 观看与管理两套导航（D-UI-02、D-UI-09）：key 与 App.vue 的 currentPage 一致，每个页面 key 只属于一个区域；
// 顶栏只显示当前区域那一套，命令面板列出两边全部页面（APP-12、APP-14：两边读同一份清单，不会各自漏页）。
// 「视频」是观看侧的海报墙（VideoBrowsePage，D-P2-01），「片库管理」是常挂载的片库页（VideoListPage），两页筛选各自独立；
// 「图片」是观看侧的照片流（PhotoBrowsePage，D-P2-08），「图片管理」是图片库管理页（PhotoLibraryPage），两页筛选各自独立。
// requiresBrowserBridge：只在设置里开启了浏览器插件桥接时显示（「下载」页）。
// 「片单」是一个页面（D-P2-04，四个标签），不再是下拉；原来的四个页面 key 是它的别名，见下面的 LIST_TABS。
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
    pages: [
      { key: 'lists', label: '片单', keywords: ['lists', 'piandan', '片单', '想看', '榜单', '观影记录', '观看笔记'] }
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

// 管理侧（D-UI-02、D-UI-03）：一期沿用现有页面与面板。带 command 的项（任务中心）是动作不是页面：
// 点了执行命令面板里同 ID 的既有命令，打开现有面板。顶栏与命令面板都叫「任务中心」（用词表 D-UD-14）。
// 「待处理」三期起是页面（D-P3-01，manage/PendingWorkPage.vue）；命令面板里仍由既有命令 action:pending-work 代表（见 ACTION_COMMAND_PAGES）。
// 「清理中心」三期起是页面 `cleanup`（D-P3-02，视频 · 图片两个标签），命令 action:cleanup 改为打开这一页。
export const MANAGE_NAV_GROUPS = [
  {
    key: 'manage-library',
    label: '片库',
    pages: [
      { key: 'manage-home', label: '管理首页', keywords: ['manage', 'guanli', '管理', '统计', '最近的变化'] },
      { key: 'library-manage', label: '片库管理', keywords: ['library manage', 'pianku', '片库', '扫描', '批量'] },
      { key: 'photos-manage', label: '图片管理', keywords: ['photo manage', 'tupian guanli', '图片库', '扫描图片'] },
      // 人物与作品集的编辑（D-P2-06 起观看页只看不改）：三个标签 人物 · 作品集 · 人脸审阅（D-P3-04，见 ENTITIES_MANAGE_TABS）。
      { key: 'entities-manage', label: '人物与作品集', keywords: ['people manage', 'collection manage', 'renwu', 'zuopinji', '人物', '作品集', '人脸', '人脸审阅', '合并人物', '编辑'] }
    ]
  },
  {
    key: 'manage-review',
    label: '待处理与清理',
    pages: [
      { key: 'pending-work', label: '待处理' },
      { key: 'cleanup', label: '清理中心', keywords: ['cleanup', 'qingli', '清理', '重复', '废纸篓', '⌘⇧K'] }
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
      { key: 'task-center', label: '任务中心', command: 'action:task-center' },
      SETTINGS_NAV_PAGE
    ]
  }
];

// 整页详情（D-UI-05）：观看区的页面，但要带视频 ID，不在顶栏与命令面板里。
export const VIDEO_DETAIL_PAGE = 'video-detail';

// 片单（D-P2-04）：一个页面 `lists`，四个标签，第一个（想看）是默认标签。原来的四个页面 key 保留为别名：
// navigateTo、命令面板里原有的页面命令（命令 ID 不变）、各页发出的 navigate(key) 与记下的旧 key 都经
// resolvePageRoute 换成 `lists` + 对应标签，currentPage 永远不会是别名。顶栏只有「片单」一项。
export const LISTS_PAGE = 'lists';
export const LIST_TABS = [
  { key: 'watchlist', label: '想看', keywords: ['watchlist', 'xiangkan', '片单', '待下载'] },
  { key: 'movie-chart', label: '榜单', keywords: ['chart', 'movie chart', 'bangdan', '年度', '豆瓣'] },
  // 顶栏「已看」改名「观影记录」（D-PC52），与片库的「已看」智能视图区分开；旧说法留作关键词。
  { key: 'watched-movies', label: '观影记录', keywords: ['watched', 'guanying', 'yikan', 'kanguo', '看过', '已看', '榜单已看'] },
  { key: 'viewing-notes', label: '观看笔记', keywords: ['diary', 'bookmarks', '日记', '片段书签', '年度回顾'] }
];
export const DEFAULT_LIST_TAB = LIST_TABS[0].key;
const LIST_TAB_KEYS = new Set(LIST_TABS.map(tab => tab.key));

export function isListTab(key) {
  return LIST_TAB_KEYS.has(key);
}

// 清理中心（D-P3-02）：一个页面 `cleanup`，两个标签，视频是默认标签。当前标签是 App 的页面参数
// （navigateTo('cleanup', { tab })，与片单同一做法）；library.openCleanup / photos.openCleanup 落在对应标签上。
export const CLEANUP_PAGE = 'cleanup';
export const CLEANUP_TABS = [
  { key: 'video', label: '视频' },
  { key: 'image', label: '图片' }
];
export const DEFAULT_CLEANUP_TAB = CLEANUP_TABS[0].key;

export function isCleanupTab(key) {
  return CLEANUP_TABS.some(tab => tab.key === key);
}

// 清理中心在删除流程中（删除确认框开着或正在删，任一标签，CleanupCenterPage 的 busy）时不能离开，也不能换标签或接新的
// 请求：另一个标签一挂上 / 卸下会打断正在进行的删除（图片标签卸下时它的删除宿主跟着卸下，「不支持废纸篓」的二选一
// 再也答不了，撤销提示与「最近操作」都没了）。离开、换标签、打开集中整理任务都给同一句提示。
export const CLEANUP_BUSY_NOTICE = '正在移到废纸篓，完成后再离开清理中心。';

// 落到清理中心的这组参数会不会改动它：换到另一个标签，或带着一次性请求（打开清理审阅、定位同源对、打开集中整理任务）。
export function cleanupParamsChangePage(params, currentTab) {
  if (!params || typeof params !== 'object') return false;
  if (isCleanupTab(params.tab) && params.tab !== currentTab) return true;
  return Boolean(params.open) || Number(params.relationId || 0) > 0 || Number(params.consolidationTaskID || 0) > 0;
}

// 人物与作品集管理（D-P3-04）：一个页面 `entities-manage`，三个标签，人物是默认标签。当前标签是 App 的页面参数
// （navigateTo('entities-manage', { tab })，与片单、清理中心同一做法）；people.openFaceReview 落在人脸审阅标签上。
export const ENTITIES_MANAGE_PAGE = 'entities-manage';
export const ENTITIES_MANAGE_TABS = [
  { key: 'person', label: '人物' },
  { key: 'collection', label: '作品集' },
  { key: 'faces', label: '人脸审阅' }
];
export const DEFAULT_ENTITIES_MANAGE_TAB = ENTITIES_MANAGE_TABS[0].key;

export function isEntitiesManageTab(key) {
  return ENTITIES_MANAGE_TABS.some(tab => tab.key === key);
}

// 页面 key（可能是别名）+ 参数 → 真正的页面与参数：别名换成 `lists` 并带上 { tab }，其余原样。
export function resolvePageRoute(page, params = null) {
  if (LIST_TAB_KEYS.has(page)) return { page: LISTS_PAGE, params: { ...(params || {}), tab: page } };
  return { page, params: params || null };
}

// 顶栏一组里真正的页面（去掉动作入口）。
function navPages(groups) {
  return groups.flatMap(group => group.pages).filter(page => !page.command);
}

// 别名同属观看区：拿旧 key 当「打开一部片」来源（from）的调用方照旧进整页详情。
const PAGE_AREAS = new Map([
  ...navPages(VIEWER_NAV_GROUPS).map(page => [page.key, 'viewer']),
  ...LIST_TABS.map(tab => [tab.key, 'viewer']),
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

// 原先是动作、后来改成页面的导航项 → 命令面板里代表它的既有动作命令（命令 ID 不变）。这些页面不另生成
// nav:page:<key> 命令，免得面板里出现两条「打开待处理」；动作命令执行时打开这一页。
export const ACTION_COMMAND_PAGES = Object.freeze({ 'pending-work': 'action:pending-work' });

// 页面导航的全集：两个区域的全部页面（含设置），先观看后管理。片单的四个别名紧跟「片单」，
// 原有的 nav:page:watchlist 等命令 ID 照旧注册，执行时由 App 的 navigateTo 换成对应标签。
const COMMAND_PAGES = [
  ...navPages(VIEWER_NAV_GROUPS).flatMap(page => (page.key === LISTS_PAGE ? [page, ...LIST_TABS] : [page])),
  ...navPages(MANAGE_NAV_GROUPS).filter(page => !ACTION_COMMAND_PAGES[page.key])
];

export function isNavPageVisible(page, settings) {
  return !page?.requiresBrowserBridge || Boolean(settings?.browser_bridge_enabled);
}

// 命令面板的「视频 · 某智能视图」（命令 ID nav:smart:<value> 不变）。三期（D-P3-03）片库管理只留管理向的视图：
// 不带 viewer 的几项与 video-list/LibraryToolbar.vue 的 smartViewOptions 一一对应（那份是真值来源，
// appCommands.test.js 有一条防漂移断言比对两处）；带 viewer 的是观看向的视图，已从片库管理拿掉，命令改指向观看侧
// 「视频」海报墙（smartViewTarget）。
const COMMAND_SMART_VIEWS = [
  { value: '', label: '全部视频' },
  { value: 'continue_watching', label: '继续观看', viewer: true },
  { value: 'favorites', label: '收藏', viewer: true },
  { value: 'liked', label: '点赞', viewer: true },
  { value: 'recently_played', label: '最近播放', viewer: true },
  { value: 'unwatched', label: '未看', viewer: true },
  { value: 'watched', label: '已看', viewer: true },
  { value: 'recently_added', label: '最近添加', viewer: true },
  { value: 'untagged', label: '未打标签' },
  { value: 'no_subtitle', label: '无字幕' },
  { value: 'local_metadata_updated', label: '本地资料有更新' },
  { value: 'stale', label: '路径失效' }
];

// 「视频」海报墙（viewer/VideoBrowsePage.vue）的观看状态对应的智能视图，与它的 VIDEO_BROWSE_WATCH_OPTIONS 逐项一致
// （VideoBrowsePage.test.js 钉住）。写在这里而不从页面导入：页面经 viewerNavigation.js 已经依赖本文件，反过来会成环。
// 点赞、最近播放（按最近一次播放倒序）在三期集成时补回海报墙。
export const VIDEO_BROWSE_SMART_VIEWS = Object.freeze(['', 'unwatched', 'continue_watching', 'watched', 'favorites', 'liked', 'recently_played']);

// 片库管理认得的智能视图（D-P3-03：全部、未打标签、无字幕、本地资料有更新、路径失效）。保存视图或命令带来别的视图时，
// 片库管理退回「全部」并提示一次（VideoListPage）。
export const LIBRARY_MANAGE_SMART_VIEWS = Object.freeze(COMMAND_SMART_VIEWS.filter(view => !view.viewer).map(view => view.value));

// 智能视图命令的落点（D-P3-03「命令改指向观看侧」）：
// - 海报墙认得的观看状态（未看、继续观看、已看、收藏、点赞、最近播放）一律去「视频」并带上 { watch }，不论当前在哪一侧；
// - 海报墙不单列的「最近添加」片库管理已不再提供：命令保留但不在面板里列出（hidden），执行时去「视频」并带上
//   { watch: 'recently_added' }（海报墙显示成可去掉的「最近 30 天添加」筛选项，与首页「最近添加」的「查看全部」同一落点）；
// - 「全部视频」跟着当前区域：管理侧是片库管理的全部，观看侧是「视频」的全部；
// - 其余（管理向）去「片库管理」套上这个视图。
export function smartViewTarget(value, area) {
  const view = value || '';
  if (view === '') return area === 'manage' ? { page: 'library-manage', view } : { page: 'videos', watch: '' };
  if (VIDEO_BROWSE_SMART_VIEWS.includes(view) || view === 'recently_added') return { page: 'videos', watch: view };
  if (LIBRARY_MANAGE_SMART_VIEWS.includes(view)) return { page: 'library-manage', view };
  return { page: 'videos', watch: '' };
}

// 固定命令 ID → 宿主页（详细设计 §6.1）。命令由宿主页（或它的常挂载子组件）注册；宿主页可能
// 还没挂载（人物页、图片页按需挂载），所以跳转前先切到宿主页、等它挂载完再按 ID 执行。
// 片库与图片库的这些命令都是维护动作，宿主页记管理侧的 key（D-UI-03）：切过去即进管理区。
// 两条清理命令的宿主是清理中心（D-P3-02），由清理中心页注册。
export const COMMAND_HOST_PAGES = {
  'library.openCleanup': CLEANUP_PAGE,
  'library.openAIReview': 'library-manage',
  'library.openLocalMetadataUpdates': 'library-manage',
  'library.openCollectionSuggestions': 'library-manage',
  'library.openTrash': 'library-manage',
  'library.openTagManager': 'library-manage',
  'photos.openAIReview': 'photos-manage',
  'photos.openCleanup': CLEANUP_PAGE,
  'people.openFaceReview': ENTITIES_MANAGE_PAGE
};

// 切到宿主页时一并带上的页面参数：清理中心的两条命令各自落在自己的标签上、人脸审阅落在人物与作品集的「人脸审阅」
// 标签上，切页与换标签同一拍，不会先按上次的标签挂一遍（图片标签一挂上就会读状态、没结果时还会自动开始分析）。
const COMMAND_HOST_PARAMS = {
  'library.openCleanup': { tab: 'video' },
  'photos.openCleanup': { tab: 'image' },
  'people.openFaceReview': { tab: 'faces' }
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

// 按下 Shift 时 event.key 是大写字母，键盘布局不同还可能是别的字符；code 是物理键位，两种都认。
function isLetterKey(event, letter) {
  return event.code === `Key${letter.toUpperCase()}` || String(event.key || '').toLowerCase() === letter;
}

// 命令面板（D-UD-03）：⌘K 与 ⌘⇧P（Ctrl 同理）是同一个面板的两个快捷键。
function isCommandPaletteHotkey(event) {
  if (!(event.metaKey || event.ctrlKey) || event.altKey) return false;
  return event.shiftKey ? isLetterKey(event, 'p') : isLetterKey(event, 'k');
}

// 清理中心（D-UD-03）：⌘⇧K。原先的 ⌘K 让给了命令面板。
function isCleanupHotkey(event) {
  if (!(event.metaKey || event.ctrlKey) || !event.shiftKey || event.altKey) return false;
  return isLetterKey(event, 'k');
}

// 面板以外有模态的东西开着：对话框（role="dialog"，含 BaseModal / DialogShell、查看器、自制遮罩）、
// 任何 aria-modal="true" 的遮罩（如待重启遮罩），或应用内确认框还没答复（读 feedbackState，不依赖它渲染出 DOM）。
// 明确标了 aria-modal="false" 的非模态面板（片库详情抽屉、浮层）不拦：原先 ⌘⇧P 在它们开着时也能用。
// 调用方先排除「命令面板自己开着」的情况，面板的 BaseModal 同样带 role="dialog"。
function otherDialogOpen() {
  if (feedbackState.confirm) return true;
  return Boolean(document.querySelector('[role="dialog"]:not([aria-modal="false"]), [aria-modal="true"]'));
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
      entityFocus: { person: null, collection: null },
      // 管理侧「人物与作品集」的当前标签（ENTITIES_MANAGE_TABS：person | collection | faces，页面参数 { tab }）
      // 与要定位的对象（{ id, name }，置一次即清空）。
      entitiesManageTab: DEFAULT_ENTITIES_MANAGE_TAB,
      entitiesManageFocus: null
    };
  },
  mounted() {
    window.addEventListener('keydown', this.handleCommandPaletteHotkey);
    window.addEventListener('keydown', this.handleCleanupHotkey);
    this.registerAppCommands();
    this.registerTaskCommands();
    if (window.runtime?.EventsOn) {
      const tasksOff = window.runtime.EventsOn('background-tasks', running => {
        // 管理首页「最近的变化」：备份从在跑变成不在跑记一条（后端没有单独的备份事件）。
        recordBackgroundTasks(running);
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
    // ⌘K 或 ⌘⇧P（macOS 的 metaKey）/ Ctrl+K、Ctrl+Shift+P：开关命令面板（D-UD-03）。
    // 挂在 window 上、不判断事件源，所以焦点在搜索框之类的输入框里时同样生效。
    // 面板开着时再按一次关掉；面板没开而别的对话框或确认框开着时不响应（与 ⌘⇧K 及原先 ⌘K 开清理中心同一条规矩），
    // 免得把面板叠在弹窗上、执行的命令又在弹窗背后切走页面。
    handleCommandPaletteHotkey(event) {
      if (!event || event.isComposing || !isCommandPaletteHotkey(event)) return;
      // 已经被先注册的处理器接手（并 preventDefault）的按键不抢第二次。
      if (event.defaultPrevented) return;
      if (this.startupError) return;
      if (this.commandPaletteOpen) {
        event.preventDefault();
        this.commandPaletteOpen = false;
        return;
      }
      if (otherDialogOpen()) return;
      event.preventDefault();
      this.openCommandPalette();
    },
    // ⌘⇧K：打开清理中心（D-UD-03；原先是 ⌘K）。三期起清理中心是管理侧页面 `cleanup`（D-P3-02），
    // 任何页面都能按，停在上次看的那个标签上；片库页不再自己接 ⌘⇧K。
    // 已被别的处理器接手（preventDefault）的按键让位；有弹窗开着时不响应，免得在弹窗背后切走页面。
    handleCleanupHotkey(event) {
      if (!event || event.isComposing || !isCleanupHotkey(event)) return;
      if (event.defaultPrevented || this.startupError) return;
      if (document.querySelector('[role="dialog"]')) return;
      event.preventDefault();
      Promise.resolve(this.navigateTo(CLEANUP_PAGE))
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
      if (await this.enterCommandHost(commandID) === false) return false;
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
      return this.enterCommandHost(command?.id);
    },
    // 切到固定命令的宿主页（还要选定页面标签的一并选上），等它挂载、注册完命令。已经在那儿时什么也不做；
    // 用户在切页确认里放弃时返回 false。标签与切页同一拍改，宿主页只按目标标签挂载一次。
    // 带页面参数的（清理中心、人物与作品集的标签）已在宿主页上时也交一次参数：navigateTo 对当前页只落参数。
    async enterCommandHost(commandID) {
      const page = COMMAND_HOST_PAGES[commandID];
      const params = COMMAND_HOST_PARAMS[commandID] || null;
      if (!page || (page === this.currentPage && !params)) return true;
      if (await whenNavigated(this.navigateTo(page, params), () => true) === false) return false;
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
          // 海报墙不单列的观看向视图（最近添加）不再列出，命令 ID 照旧可执行（见 smartViewTarget）。
          ...(view.viewer && !VIDEO_BROWSE_SMART_VIEWS.includes(view.value) ? { hidden: true } : {}),
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
        // 随机播放与今晚看什么三期起只在观看侧「视频」海报墙上（D-P3-03，片库管理拿掉了它们的入口）：命令 ID 照旧，
        // 改为切到「视频」再按海报墙当前的筛选与随机条件执行（原先由片库页注册、按片库筛选执行）。
        {
          id: 'action:random-play',
          group: 'action',
          label: '随机播放',
          keywords: ['random', '随机'],
          run: () => this.runOnVideoBrowse(page => page.playRandom())
        },
        {
          id: 'action:tonight',
          group: 'action',
          label: '今晚看什么',
          keywords: ['tonight', '今晚', '选片', '随机'],
          run: () => this.runOnVideoBrowse(page => page.openTonightPicker())
        },
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
        // 清理中心三期起是页面（D-P3-02）：命令 ID 照旧，改为打开这一页。面板里已有同名的页面导航「打开清理中心」
        // （nav:page:cleanup），这一条只作跳转目标（hidden），免得重复出现。
        {
          id: 'action:cleanup',
          group: 'action',
          label: '打开清理中心',
          keywords: ['cleanup', '清理', '重复', 'qingli', '⌘⇧K'],
          hidden: true,
          run: () => this.navigateTo(CLEANUP_PAGE)
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
    // 智能视图的落点见 smartViewTarget（D-P3-03）：观看向的去「视频」海报墙，作为参数带过去（新的一次浏览）；
    // 管理向的（未打标签、无字幕、本地资料有更新、路径失效）去「片库管理」；「全部视频」跟着当前区域。
    applySmartViewFromCommand(value) {
      const target = smartViewTarget(value, pageArea(this.currentPage));
      if (target.page === 'videos') return this.navigateTo('videos', { watch: target.watch });
      return whenNavigated(this.navigateTo('library-manage'), async () => {
        await this.$nextTick();
        await this.$refs.videoListPage?.applySmartViewCommand(target.view);
      });
    },
    // 在「视频」海报墙上执行一个动作（随机播放、今晚看什么）：先切过去（首次进入才挂载），等它挂好再执行。
    runOnVideoBrowse(action) {
      return whenNavigated(this.navigateTo('videos'), async () => {
        await this.$nextTick();
        const page = this.$refs.videoBrowsePage;
        return page ? action(page) : false;
      });
    },
    // 保存视图是片库管理的整套筛选（体积、分辨率、评分、缺失项……），海报墙装不下，一律在「片库管理」打开。
    applySavedViewFromCommand(viewID) {
      return whenNavigated(this.navigateTo('library-manage'), async () => {
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
    // 命令面板、首页、整页详情、回顾打开人物 / 作品集：进观看侧的人物 / 作品集页并打开这一个（D-P2-06）。
    openEntityFromCommand(type, id, name) {
      return whenNavigated(this.navigateTo(type === 'person' ? 'people' : 'collections'), async () => {
        this.entityFocus = { ...this.entityFocus, [type]: { id, name } };
        await this.$nextTick();
        this.entityFocus = { ...this.entityFocus, [type]: null };
      });
    },
    // 观看页的「在管理中编辑」：去管理侧「人物与作品集」的人物或作品集标签，打开这一个（EntityLibraryPage 的 focusEntity，
    // 打开时连编辑抽屉一起打开）。没有 id 时只切过去（观看页空列表的下一步）。标签随切页一起落下（页面参数）。
    openEntityInManage({ kind, id, name } = {}) {
      const tab = kind === 'collection' ? 'collection' : 'person';
      const entityID = Number(id || 0);
      return whenNavigated(this.navigateTo(ENTITIES_MANAGE_PAGE, { tab }), async () => {
        if (!entityID) return true;
        this.entitiesManageFocus = { id: entityID, name: name || '' };
        await this.$nextTick();
        this.entitiesManageFocus = null;
        return true;
      });
    }
  }
};

export const COMMAND_PALETTE_PAGES = COMMAND_PAGES;
export const COMMAND_PALETTE_SMART_VIEWS = COMMAND_SMART_VIEWS;
