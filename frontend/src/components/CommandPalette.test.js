import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'SearchLibraryVideoPage', 'ListPeople', 'ListCollections', 'SearchScenes', 'SearchScenesByImage', 'RunGatedTaskNow',
  'CancelFaceAnalysis', 'CancelImageAITagging', 'CancelImageEXIFBackfill', 'CancelImageSemanticIndex', 'CancelLocalMetadataBackfill',
  'CancelPerceptualHashBackfill', 'CancelPlaybackProxyTask', 'CancelSemanticIndex', 'CancelSubtitle', 'CancelTechnicalBackfill',
  'CreateDatabaseBackup', 'StartImageAITagging', 'StartImageEXIFBackfill', 'StartImageSemanticIndex',
  'StartLocalMetadataBackfill', 'StartPerceptualHashBackfill', 'StartTechnicalBackfill', 'TriggerAITagging',
  'LogFrontend',
  'StartFrameHashBackfill', 'CancelFrameHashBackfill'
].map(name => [name, vi.fn()])));

vi.mock('../../wailsjs/go/main/App', () => api);
// 以图的读图（解码、画布缩放）在 jsdom 里跑不了：替换成桩，其余照用真实实现。
vi.mock('../utils/unifiedSearch.js', async importOriginal => {
  const actual = await importOriginal();
  return { ...actual, prepareSearchImage: vi.fn(actual.prepareSearchImage) };
});

import CommandPalette from './CommandPalette.vue';
import * as unifiedSearch from '../utils/unifiedSearch.js';
import { registerCommands, unregisterCommands } from '../utils/commandRegistry.js';
import { buildTaskCommands } from '../utils/taskCommands.js';
import { feedbackState, resetFeedback } from '../utils/feedback.js';

const scopesUsed = new Set();

function register(scopeKey, commands) {
  scopesUsed.add(scopeKey);
  registerCommands(scopeKey, commands);
}

function videoRow(id, name, displayTitle = '') {
  return { id, name, display_title: displayTitle };
}

// 面板开着时在 window 上接管图片粘贴与拖放：用例结束必须卸载，否则旧实例会接走下一个用例派发的事件。
const mountedWrappers = [];
function track(wrapper) {
  mountedWrappers.push(wrapper);
  return wrapper;
}

function mountPalette(openVideo = vi.fn(), props = {}) {
  return track(mount(CommandPalette, { props: { open: true, openVideo, ...props } }));
}

// 输入 → 防抖窗口（250 毫秒）走完 → 搜索的 promise 落地。
async function typeQuery(wrapper, text) {
  await wrapper.get('[data-test="command-palette-input"]').setValue(text);
  vi.advanceTimersByTime(250);
  await flushPromises();
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.useFakeTimers();
  api.SearchLibraryVideoPage.mockResolvedValue({ videos: [] });
  api.ListPeople.mockResolvedValue([]);
  api.ListCollections.mockResolvedValue([]);
  api.SearchScenes.mockResolvedValue({ hits: [], notices: [] });
  api.SearchScenesByImage.mockResolvedValue({ hits: [], notices: [] });
});

afterEach(() => {
  while (mountedWrappers.length) {
    try {
      mountedWrappers.pop().unmount();
    } catch {
      // 用例自己卸过的再卸会抛，忽略。
    }
  }
  vi.useRealTimers();
  for (const scope of scopesUsed) unregisterCommands(scope);
  scopesUsed.clear();
});

describe('命令面板渲染与执行', () => {
  it('只注册了导航命令时只显示导航组', () => {
    register('nav-only', [
      { id: 'nav:page:videos', group: 'navigate', label: '打开视频', keywords: [], run: vi.fn() }
    ]);
    const wrapper = mountPalette();

    const text = wrapper.get('[data-test="command-palette-list"]').text();
    expect(text).toContain('导航');
    expect(text).toContain('打开视频');
    expect(text).not.toContain('动作');
    expect(text).not.toContain('任务');
    expect(text).not.toContain('视频组');
  });

  it('注册表为空时给出空态而不是空白', () => {
    const wrapper = mountPalette();
    expect(wrapper.get('[data-test="command-palette-empty"]').text()).toContain('没有匹配的命令');
  });

  it('打开后焦点落在输入框', async () => {
    register('nav-only', [
      { id: 'nav:page:videos', group: 'navigate', label: '打开视频', keywords: [], run: vi.fn() }
    ]);
    const wrapper = track(mount(CommandPalette, {
      props: { open: true, openVideo: vi.fn() },
      attachTo: document.body
    }));
    await flushPromises();

    expect(document.activeElement).toBe(wrapper.get('[data-test="command-palette-input"]').element);
    wrapper.unmount();
  });

  it('回车执行高亮项并关闭面板', async () => {
    const run = vi.fn();
    register('page', [{ id: 'action:one', group: 'action', label: '第一条', keywords: [], run }]);
    const wrapper = mountPalette();

    await wrapper.get('[data-test="command-palette-input"]').trigger('keydown', { key: 'Enter' });

    expect(run).toHaveBeenCalledTimes(1);
    expect(wrapper.emitted('close')).toHaveLength(1);
  });

  it('方向键在可执行项之间移动，跳过灰显项', async () => {
    const runnable = vi.fn();
    register('page', [
      { id: 'action:one', group: 'action', label: '第一条', keywords: [], run: vi.fn() },
      { id: 'action:two', group: 'action', label: '第二条', keywords: [], enabled: () => false, run: vi.fn() },
      { id: 'action:three', group: 'action', label: '第三条', keywords: [], run: runnable }
    ]);
    const wrapper = mountPalette();
    const input = wrapper.get('[data-test="command-palette-input"]');

    await input.trigger('keydown', { key: 'ArrowDown' });
    expect(wrapper.get('[data-test="command-palette-item-action:three"]').classes())
      .toContain('command-palette__item--active');

    await input.trigger('keydown', { key: 'Enter' });
    expect(runnable).toHaveBeenCalledTimes(1);
  });

  it('enabled() 为 false 的项灰显且点不动', async () => {
    const run = vi.fn();
    register('page', [
      { id: 'action:off', group: 'action', label: '不可用', keywords: [], enabled: () => false, run }
    ]);
    const wrapper = mountPalette();

    const item = wrapper.get('[data-test="command-palette-item-action:off"]');
    expect(item.classes()).toContain('command-palette__item--disabled');
    expect(item.attributes('disabled')).toBeDefined();

    await item.trigger('click');
    expect(run).not.toHaveBeenCalled();
    expect(wrapper.emitted('close')).toBeUndefined();
  });

  it('面板打开期间注册表变化立刻反映到列表（任务组靠这个跟事件走）', async () => {
    const wrapper = mountPalette();
    expect(wrapper.find('[data-test="command-palette-item-task:late"]').exists()).toBe(false);

    register('late', [{ id: 'task:late', group: 'task', label: '后到的任务', keywords: [], run: vi.fn() }]);
    await wrapper.vm.$nextTick();

    expect(wrapper.get('[data-test="command-palette-item-task:late"]').text()).toBe('后到的任务');
  });

  it('Esc 关闭面板', async () => {
    const wrapper = mountPalette();

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    await flushPromises();

    expect(wrapper.emitted('close')).toHaveLength(1);
  });
});

describe('执行反馈（APP-12）', () => {
  function toastMessages() {
    return feedbackState.toasts.map(toast => ({ level: toast.level, message: toast.message }));
  }

  beforeEach(() => resetFeedback());

  it('APP-12 执行成功统一提示「已执行：<label>」', async () => {
    register('page', [{ id: 'action:backup', group: 'action', label: '立即备份数据库', keywords: [], run: vi.fn(() => Promise.resolve({})) }]);
    const wrapper = mountPalette();

    await wrapper.get('[data-test="command-palette-input"]').trigger('keydown', { key: 'Enter' });
    await flushPromises();

    expect(toastMessages()).toEqual([{ level: 'success', message: '已执行：立即备份数据库' }]);
  });

  it('APP-12 run() 返回 false（用户中途放弃）时不提示已执行', async () => {
    register('page', [{ id: 'nav:page:videos', group: 'navigate', label: '打开视频', keywords: [], run: vi.fn(() => Promise.resolve(false)) }]);
    const wrapper = mountPalette();

    await wrapper.get('[data-test="command-palette-input"]').trigger('keydown', { key: 'Enter' });
    await flushPromises();

    expect(toastMessages()).toEqual([]);
  });

  it('APP-12 执行失败仍报错，不报已执行', async () => {
    register('page', [{ id: 'action:boom', group: 'action', label: '会失败的动作', keywords: [], run: vi.fn(() => Promise.reject(new Error('后端不可用'))) }]);
    const wrapper = mountPalette();

    await wrapper.get('[data-test="command-palette-input"]').trigger('keydown', { key: 'Enter' });
    await flushPromises();

    expect(toastMessages()).toEqual([{ level: 'error', message: '执行「会失败的动作」失败：Error: 后端不可用' }]);
  });

  it('APP-11 beforeRun 先做准备（App 用它切到宿主页），返回 false 就不执行', async () => {
    const run = vi.fn();
    register('page', [{ id: 'library.openAIReview', group: 'action', label: 'AI 标签审阅', keywords: [], run }]);
    const beforeRun = vi.fn(() => Promise.resolve(false));
    const wrapper = track(mount(CommandPalette, { props: { open: true, openVideo: vi.fn(), beforeRun } }));

    await wrapper.get('[data-test="command-palette-input"]').trigger('keydown', { key: 'Enter' });
    await flushPromises();
    expect(beforeRun).toHaveBeenCalledWith(expect.objectContaining({ id: 'library.openAIReview' }));
    expect(run).not.toHaveBeenCalled();

    beforeRun.mockResolvedValue(true);
    await wrapper.setProps({ open: false });
    await wrapper.setProps({ open: true });
    await wrapper.get('[data-test="command-palette-input"]').trigger('keydown', { key: 'Enter' });
    await flushPromises();
    expect(run).toHaveBeenCalledTimes(1);
  });
});

describe('命令面板视频组', () => {
  // D-P2-05 起视频组是统一搜索的一组：每组 5 条（原 8 条），版本组合并成一部，防抖 250 毫秒。
  it('输入 2 字起调用 SearchLibraryVideoPage，限 5 条、合并版本', async () => {
    const wrapper = mountPalette();

    await typeQuery(wrapper, '海');
    expect(api.SearchLibraryVideoPage).not.toHaveBeenCalled();

    await typeQuery(wrapper, '海边');
    expect(api.SearchLibraryVideoPage).toHaveBeenCalledTimes(1);
    expect(api.SearchLibraryVideoPage).toHaveBeenCalledWith({
      filter: {
        search_mode: 'file',
        keyword: '海边',
        smart_view: '',
        tag_ids: [],
        min_size: 0,
        max_size: 0,
        min_height: 0,
        max_height: 0,
        min_rating: null,
        max_rating: null,
        sort_mode: 'balanced',
        collapse_versions: true
      },
      limit: 5
    });
  });

  it('后端多返回也只显示 5 条，标题优先显示显示标题', async () => {
    api.SearchLibraryVideoPage.mockResolvedValue({
      videos: Array.from({ length: 12 }, (_, index) => videoRow(index + 1, `file-${index + 1}.mp4`, index === 0 ? '海边落日' : ''))
    });
    const wrapper = mountPalette();

    await typeQuery(wrapper, '海边');

    const items = wrapper.findAll('[data-test^="command-palette-item-video:"]');
    expect(items).toHaveLength(5);
    expect(items[0].text()).toBe('海边落日');
    expect(items[1].text()).toBe('file-2.mp4');
  });

  it('回车打开视频详情走 openVideo，并关闭面板', async () => {
    api.SearchLibraryVideoPage.mockResolvedValue({ videos: [videoRow(7, 'sea.mp4')] });
    const openVideo = vi.fn();
    const wrapper = mountPalette(openVideo);

    await typeQuery(wrapper, '海边');
    await wrapper.get('[data-test="command-palette-input"]').trigger('keydown', { key: 'Enter' });

    expect(openVideo).toHaveBeenCalledWith(expect.objectContaining({ id: 7 }));
    expect(wrapper.emitted('close')).toHaveLength(1);
  });

  it('视频搜索失败只报视频组的错，其他组照常可用', async () => {
    api.SearchLibraryVideoPage.mockRejectedValue(new Error('后端不可用'));
    register('page', [{ id: 'action:one', group: 'action', label: '海边动作', keywords: [], run: vi.fn() }]);
    const wrapper = mountPalette();

    await typeQuery(wrapper, '海边');

    expect(wrapper.get('[data-test="command-palette-video-error"]').text()).toContain('视频搜索失败');
    expect(wrapper.find('[data-test="command-palette-item-action:one"]').exists()).toBe(true);
  });

  it('查询退回 1 字后清空视频结果', async () => {
    api.SearchLibraryVideoPage.mockResolvedValue({ videos: [videoRow(7, 'sea.mp4')] });
    const wrapper = mountPalette();

    await typeQuery(wrapper, '海边');
    expect(wrapper.findAll('[data-test^="command-palette-item-video:"]')).toHaveLength(1);

    await typeQuery(wrapper, '海');
    expect(wrapper.findAll('[data-test^="command-palette-item-video:"]')).toHaveLength(0);
  });
});

describe('命令面板任务组', () => {
  function registerTasks(state) {
    register('tasks', buildTaskCommands(state));
  }

  it('运行中的任务显示运行中，执行即取消', async () => {
    registerTasks({ running: ['phash'], waiting: [] });
    const wrapper = mountPalette();

    const item = wrapper.get('[data-test="command-palette-item-task:phash"]');
    expect(item.text()).toBe('近重复指纹 · 运行中 · 取消');

    await item.trigger('click');
    expect(api.CancelPerceptualHashBackfill).toHaveBeenCalledTimes(1);
    expect(api.StartPerceptualHashBackfill).not.toHaveBeenCalled();
  });

  it('等待空闲的任务显示等待原因，执行即立即运行', async () => {
    registerTasks({ running: [], waiting: [{ task_key: 'exif', reason: 'user_active', since: '' }] });
    const wrapper = mountPalette();

    const item = wrapper.get('[data-test="command-palette-item-task:exif"]');
    expect(item.text()).toContain('等待空闲');
    expect(item.text()).toContain('你正在用电脑');
    expect(item.text()).toContain('立即运行');

    await item.trigger('click');
    expect(api.RunGatedTaskNow).toHaveBeenCalledWith('exif');
    expect(api.StartImageEXIFBackfill).not.toHaveBeenCalled();
  });

  it('既没在跑也没在等的任务显示显式启动', async () => {
    registerTasks({ running: [], waiting: [] });
    const wrapper = mountPalette();

    const item = wrapper.get('[data-test="command-palette-item-task:technical"]');
    expect(item.text()).toBe('技术信息 · 启动');

    await item.trigger('click');
    expect(api.StartTechnicalBackfill).toHaveBeenCalledTimes(1);
  });

  it('没有零参启动绑定的任务不在闲置时占位', () => {
    registerTasks({ running: [], waiting: [] });
    const wrapper = mountPalette();

    expect(wrapper.find('[data-test="command-palette-item-task:enhancement"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="command-palette-item-task:face"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="command-palette-item-task:subtitle"]').exists()).toBe(false);
    // 播放代理同理：启动一律要选对象（单个/选中/当前筛选），闲置时不出现。
    expect(wrapper.find('[data-test="command-palette-item-task:proxy"]').exists()).toBe(false);
  });

  it('播放代理跑起来之后可以在面板里取消', async () => {
    registerTasks({ running: ['proxy'], waiting: [] });
    const wrapper = mountPalette();

    const item = wrapper.get('[data-test="command-palette-item-task:proxy"]');
    expect(item.text()).toBe('播放代理 · 运行中 · 取消');
    expect(item.attributes('disabled')).toBeUndefined();

    await item.trigger('click');
    await flushPromises();
    expect(api.CancelPlaybackProxyTask).toHaveBeenCalledTimes(1);
  });

  it('帧哈希回填运行中可在面板里取消、闲置时可启动', async () => {
    registerTasks({ running: ['frame_hash'], waiting: [] });
    let wrapper = mountPalette();

    let item = wrapper.get('[data-test="command-palette-item-task:frame_hash"]');
    expect(item.text()).toBe('帧哈希 · 运行中 · 取消');
    expect(item.attributes('disabled')).toBeUndefined();
    await item.trigger('click');
    await flushPromises();
    expect(api.CancelFrameHashBackfill).toHaveBeenCalledTimes(1);

    // 闲置时它是零参启动，与近重复指纹、技术信息同处理，不像播放代理那样要选对象。
    registerTasks({ running: [], waiting: [] });
    wrapper = mountPalette();

    item = wrapper.get('[data-test="command-palette-item-task:frame_hash"]');
    expect(item.text()).toBe('帧哈希 · 启动');
    await item.trigger('click');
    await flushPromises();
    expect(api.StartFrameHashBackfill).toHaveBeenCalledTimes(1);
  });

  it('运行中但没有取消绑定的任务只显示状态、不可执行', async () => {
    registerTasks({ running: ['enhancement'], waiting: [] });
    const wrapper = mountPalette();

    const item = wrapper.get('[data-test="command-palette-item-task:enhancement"]');
    expect(item.text()).toBe('视频超分 · 运行中');
    expect(item.attributes('disabled')).toBeDefined();
  });

  it('立即运行撞上"任务已被放行"不算失败', async () => {
    api.RunGatedTaskNow.mockRejectedValue(new Error('idle_gate_task_not_waiting: 该后台任务当前没有在等待空闲: phash'));
    const onSettled = vi.fn();
    register('tasks', buildTaskCommands({
      running: [],
      waiting: [{ task_key: 'phash', reason: 'on_battery', since: '' }],
      onSettled
    }));
    const wrapper = mountPalette();

    await wrapper.get('[data-test="command-palette-item-task:phash"]').trigger('click');
    await flushPromises();

    expect(api.RunGatedTaskNow).toHaveBeenCalledWith('phash');
    expect(onSettled).toHaveBeenCalledTimes(1);
  });
});

// ===== ⌘K 统一搜索（D-P2-05，详细设计 §3.4）=====
describe('统一搜索：分组、防抖与各组独立', () => {
  function deferred() {
    let resolve;
    let reject;
    const promise = new Promise((res, rej) => { resolve = res; reject = rej; });
    return { promise, resolve, reject };
  }

  function groupKeys(wrapper) {
    return wrapper.findAll('[data-test^="command-palette-group-"]').map(group => group.attributes('data-test').replace('command-palette-group-', ''));
  }

  const PERSON = { person: { id: 3, display_name: '海边的张三' }, avatar_url: '/preview/person-avatar/3', active_video_count: 2 };
  const COLLECTION = { collection: { id: 5, name: '海边三部曲' }, cover_url: '/preview/collection-cover/5', active_video_count: 3 };
  const LINE = { video_id: 7, title: '长片', start_ms: 125_000, end_ms: 128_000, source: 'dialogue', text: '我们去海边吧' };
  const FRAME = { video_id: 9, title: '短片', start_ms: 1000, end_ms: 6000, source: 'visual', text: '' };

  function mockAllGroups() {
    api.SearchLibraryVideoPage.mockResolvedValue({ videos: [videoRow(1, 'sea.mp4', '海边落日')] });
    api.ListPeople.mockResolvedValue([PERSON]);
    api.ListCollections.mockResolvedValue([COLLECTION]);
    api.SearchScenes.mockImplementation(({ mode }) => Promise.resolve(mode === 'dialogue' ? { hits: [LINE], notices: [] } : { hits: [FRAME], notices: [] }));
  }

  it('停 250 毫秒才并行发出五组请求；不到 2 个字只过滤命令', async () => {
    const wrapper = mountPalette();
    await wrapper.get('[data-test="command-palette-input"]').setValue('海');
    vi.advanceTimersByTime(300);
    await flushPromises();
    expect(groupKeys(wrapper)).toEqual([]);
    expect(api.ListPeople).not.toHaveBeenCalled();

    await wrapper.get('[data-test="command-palette-input"]').setValue('海边');
    // 一够 2 个字就先出骨架，请求等防抖走完。
    expect(groupKeys(wrapper)).toEqual(['video', 'person', 'collection', 'dialogue', 'visual']);
    expect(wrapper.find('[data-test="command-palette-video-loading"]').exists()).toBe(true);
    vi.advanceTimersByTime(249);
    expect(api.SearchLibraryVideoPage).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(api.SearchLibraryVideoPage).toHaveBeenCalledTimes(1);
    expect(api.ListPeople).toHaveBeenCalledWith('海边', '', 0, 5);
    expect(api.ListCollections).toHaveBeenCalledWith('海边', '', 0, 5);
    expect(api.SearchScenes).toHaveBeenCalledWith({ query: '海边', mode: 'dialogue', limit: 5 });
    expect(api.SearchScenes).toHaveBeenCalledWith({ query: '海边', mode: 'visual', limit: 5 });
  });

  it('命令组在最前，其后依次是视频、人物、作品集、台词、画面；没有图时不出现以图组', async () => {
    mockAllGroups();
    register('page', [{ id: 'action:sea', group: 'action', label: '海边动作', keywords: [], run: vi.fn() }]);
    const wrapper = mountPalette();
    await typeQuery(wrapper, '海边');

    expect(groupKeys(wrapper)).toEqual(['video', 'person', 'collection', 'dialogue', 'visual']);
    const order = wrapper.findAll('[role="option"]').map(item => item.attributes('data-test'));
    expect(order).toEqual([
      'command-palette-item-action:sea',
      'command-palette-item-video:1',
      'command-palette-item-person:3',
      'command-palette-item-collection:5',
      'command-palette-item-dialogue:7:125000',
      'command-palette-item-visual:9:1000'
    ]);
    expect(wrapper.get('[data-test="command-palette-item-dialogue:7:125000"]').text()).toContain('我们去海边吧');
    expect(wrapper.get('[data-test="command-palette-item-dialogue:7:125000"]').text()).toContain('长片 · 2:05');
    expect(wrapper.find('[data-test="command-palette-group-image"]').exists()).toBe(false);
  });

  it('一组失败只在这一组显示错误与「重试」，其他组照常；重试只重发这一组', async () => {
    mockAllGroups();
    api.ListPeople.mockRejectedValueOnce(new Error('数据库不可用'));
    const wrapper = mountPalette();
    await typeQuery(wrapper, '海边');

    const banner = wrapper.get('[data-test="command-palette-person-error"]');
    expect(banner.text()).toContain('人物搜索失败');
    expect(banner.text()).toContain('数据库不可用');
    expect(wrapper.find('[data-test="command-palette-item-video:1"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="command-palette-item-collection:5"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="command-palette-item-dialogue:7:125000"]').exists()).toBe(true);

    await wrapper.get('[data-test="command-palette-retry-person"]').trigger('click');
    await flushPromises();
    expect(api.ListPeople).toHaveBeenCalledTimes(2);
    expect(api.SearchLibraryVideoPage).toHaveBeenCalledTimes(1);
    expect(wrapper.find('[data-test="command-palette-person-error"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="command-palette-item-person:3"]').exists()).toBe(true);
  });

  it('晚到的旧输入结果丢弃：先发的「海边」后回来也不盖住「海边的」', async () => {
    const slow = deferred();
    api.ListPeople.mockReturnValueOnce(slow.promise).mockResolvedValueOnce([{ ...PERSON, person: { id: 4, display_name: '新结果' } }]);
    const wrapper = mountPalette();
    await typeQuery(wrapper, '海边');
    await typeQuery(wrapper, '海边的');
    expect(wrapper.find('[data-test="command-palette-item-person:4"]').exists()).toBe(true);

    slow.resolve([{ ...PERSON, person: { id: 99, display_name: '旧结果' } }]);
    await flushPromises();
    expect(wrapper.find('[data-test="command-palette-item-person:99"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="command-palette-item-person:4"]').exists()).toBe(true);
  });

  it('只多敲了首尾空格时关键词没变，不重搜', async () => {
    const wrapper = mountPalette();
    await typeQuery(wrapper, '海边');
    await typeQuery(wrapper, '海边 ');
    await typeQuery(wrapper, ' 海边 ');
    expect(api.SearchLibraryVideoPage).toHaveBeenCalledTimes(1);
    await typeQuery(wrapper, '海');
    await typeQuery(wrapper, '海边');
    expect(api.SearchLibraryVideoPage).toHaveBeenCalledTimes(2);
  });

  it('退回 1 个字时收起各组，路上的响应回来也不再显示', async () => {
    const slow = deferred();
    api.SearchLibraryVideoPage.mockReturnValueOnce(slow.promise);
    const wrapper = mountPalette();
    await typeQuery(wrapper, '海边');
    await typeQuery(wrapper, '海');
    slow.resolve({ videos: [videoRow(1, 'sea.mp4')] });
    await flushPromises();
    expect(groupKeys(wrapper)).toEqual([]);
  });

  it('没有结果的组说「没有匹配的…」；场景索引没建时台词 / 画面组只显示一行说明，并给「查看全部」去场景检索', async () => {
    api.SearchScenes.mockImplementation(({ mode }) => Promise.resolve(mode === 'dialogue'
      ? { hits: [], notices: ['subtitle_index_empty'] }
      : { hits: [], notices: ['visual_index_empty'] }));
    const wrapper = mountPalette();
    await typeQuery(wrapper, '海边');

    expect(wrapper.get('[data-test="command-palette-person-empty"]').text()).toBe('没有匹配的人物。');
    expect(wrapper.find('[data-test="command-palette-view-all-person"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="command-palette-dialogue-notice"]').text()).toContain('字幕索引');
    expect(wrapper.find('[data-test="command-palette-dialogue-empty"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="command-palette-visual-notice"]').text()).toContain('建立画面索引');
    expect(wrapper.findAll('[data-test="command-palette-visual-notice"]')).toHaveLength(1);
    expect(wrapper.find('[data-test="command-palette-view-all-visual"]').exists()).toBe(true);
  });

  it('场景组的错误用中文说法，不露错误代码', async () => {
    api.SearchScenes.mockRejectedValue(new Error('scene_runtime_unsupported: arch=amd64'));
    const wrapper = mountPalette();
    await typeQuery(wrapper, '海边');
    const banner = wrapper.get('[data-test="command-palette-visual-error"]').text();
    expect(banner).toContain('画面搜索失败');
    expect(banner).toContain('仅支持 macOS Apple Silicon');
    expect(banner).not.toContain('scene_runtime_unsupported');
  });
});

describe('统一搜索：键盘与打开结果', () => {
  const PERSON = { person: { id: 3, display_name: '海边的张三' }, avatar_url: '', active_video_count: 2 };
  const LINE = { video_id: 7, title: '长片', start_ms: 125_000, end_ms: 128_000, source: 'dialogue', text: '我们去海边吧' };

  function mountWired(extra = {}) {
    const props = { openEntity: vi.fn(), openVideoAt: vi.fn(), navigate: vi.fn(), ...extra };
    const openVideo = vi.fn();
    return { wrapper: mountPalette(openVideo, props), props, openVideo };
  }

  it('上下键跨组移动：从命令往下进视频组、再进人物组；回车打开人物走实体入口', async () => {
    api.SearchLibraryVideoPage.mockResolvedValue({ videos: [videoRow(1, 'sea.mp4')] });
    api.ListPeople.mockResolvedValue([PERSON]);
    register('page', [{ id: 'action:sea', group: 'action', label: '海边动作', keywords: [], run: vi.fn() }]);
    const { wrapper, props } = mountWired();
    await typeQuery(wrapper, '海边');
    const input = wrapper.get('[data-test="command-palette-input"]');

    expect(wrapper.get('[data-test="command-palette-item-action:sea"]').classes()).toContain('command-palette__item--active');
    await input.trigger('keydown', { key: 'ArrowDown' });
    expect(wrapper.get('[data-test="command-palette-item-video:1"]').classes()).toContain('command-palette__item--active');
    await input.trigger('keydown', { key: 'ArrowDown' });
    expect(wrapper.get('[data-test="command-palette-item-person:3"]').attributes('aria-selected')).toBe('true');
    await input.trigger('keydown', { key: 'ArrowUp' });
    await input.trigger('keydown', { key: 'ArrowDown' });

    await input.trigger('keydown', { key: 'Enter' });
    await flushPromises();
    expect(wrapper.emitted('close')).toHaveLength(1);
    expect(props.openEntity).toHaveBeenCalledWith({ type: 'person', id: 3, name: '海边的张三' });
  });

  it('挑过的高亮项不被后到的组抢走；没挑过时高亮始终是第一条', async () => {
    let resolveVideos;
    api.SearchLibraryVideoPage.mockReturnValue(new Promise(resolve => { resolveVideos = resolve; }));
    api.ListPeople.mockResolvedValue([PERSON, { ...PERSON, person: { id: 4, display_name: '海边的李四' } }]);
    const { wrapper } = mountWired();
    await typeQuery(wrapper, '海边');
    expect(wrapper.get('[data-test="command-palette-item-person:3"]').classes()).toContain('command-palette__item--active');
    await wrapper.get('[data-test="command-palette-input"]').trigger('keydown', { key: 'ArrowDown' });
    expect(wrapper.get('[data-test="command-palette-item-person:4"]').classes()).toContain('command-palette__item--active');

    resolveVideos({ videos: [videoRow(1, 'sea.mp4')] });
    await flushPromises();
    expect(wrapper.get('[data-test="command-palette-item-person:4"]').classes()).toContain('command-palette__item--active');
    expect(wrapper.get('[data-test="command-palette-item-video:1"]').classes()).not.toContain('command-palette__item--active');
  });

  it('台词命中从命中前 2 秒打开，按面板所在区域分流（from = command-palette）', async () => {
    api.SearchScenes.mockImplementation(({ mode }) => Promise.resolve(mode === 'dialogue' ? { hits: [LINE], notices: [] } : { hits: [], notices: [] }));
    const { wrapper, props } = mountWired();
    await typeQuery(wrapper, '海边');

    await wrapper.get('[data-test="command-palette-item-dialogue:7:125000"]').trigger('click');
    await flushPromises();
    expect(props.openVideoAt).toHaveBeenCalledWith({ videoID: 7, startMs: 123_000 }, { from: 'command-palette' });
    expect(wrapper.emitted('close')).toHaveLength(1);
  });

  it('输入法组字时的回车不执行', async () => {
    const run = vi.fn();
    register('page', [{ id: 'action:one', group: 'action', label: '第一条', keywords: [], run }]);
    const wrapper = mountPalette();
    await wrapper.get('[data-test="command-palette-input"]').trigger('keydown', { key: 'Enter', isComposing: true });
    expect(run).not.toHaveBeenCalled();
    expect(wrapper.emitted('close')).toBeUndefined();
  });

  it('「查看全部」带上关键词去对应页面：视频页、人物页、场景检索（台词模式）', async () => {
    api.SearchLibraryVideoPage.mockResolvedValue({ videos: [videoRow(1, 'sea.mp4')] });
    api.ListPeople.mockResolvedValue([PERSON]);
    api.SearchScenes.mockResolvedValue({ hits: [LINE], notices: [] });
    const { wrapper, props } = mountWired();
    await typeQuery(wrapper, ' 海边 ');

    await wrapper.get('[data-test="command-palette-view-all-video"]').trigger('click');
    await flushPromises();
    expect(props.navigate).toHaveBeenLastCalledWith('videos', { keyword: '海边' });
    expect(wrapper.emitted('close')).toHaveLength(1);

    await wrapper.get('[data-test="command-palette-view-all-person"]').trigger('click');
    await flushPromises();
    expect(props.navigate).toHaveBeenLastCalledWith('people', { keyword: '海边' });

    await wrapper.get('[data-test="command-palette-view-all-dialogue"]').trigger('click');
    await flushPromises();
    expect(props.navigate).toHaveBeenLastCalledWith('scenes', { query: '海边', mode: 'dialogue' });
  });

  it('焦点在「查看全部」上按回车走它自己，不执行高亮项', async () => {
    api.SearchLibraryVideoPage.mockResolvedValue({ videos: [videoRow(1, 'sea.mp4')] });
    const { wrapper, openVideo } = mountWired();
    await typeQuery(wrapper, '海边');
    await wrapper.get('[data-test="command-palette-view-all-video"]').trigger('keydown', { key: 'Enter' });
    expect(openVideo).not.toHaveBeenCalled();
  });
});

describe('统一搜索：以图', () => {
  const IMAGE = { dataURL: 'data:image/jpeg;base64,QUJD', base64: 'QUJD', name: 'shot.png' };
  const FRAME = { video_id: 9, title: '短片', start_ms: 61_000, end_ms: 66_000, source: 'visual', text: '' };

  function pngFile(size = 4) {
    return new File([new Uint8Array(size)], 'shot.png', { type: 'image/png' });
  }

  function pasteEvent(files) {
    const event = new Event('paste', { bubbles: true, cancelable: true });
    event.clipboardData = { files };
    return event;
  }

  function dragEvent(type, files) {
    const event = new Event(type, { bubbles: true, cancelable: true });
    event.dataTransfer = { types: ['Files'], files };
    return event;
  }

  beforeEach(() => {
    unifiedSearch.prepareSearchImage.mockResolvedValue(IMAGE);
  });

  it('粘贴图片：显示缩略图与「清除」，按图找画面（以图组排在最前，命令让位）；页面自己的粘贴收不到', async () => {
    api.SearchScenesByImage.mockResolvedValue({ hits: [FRAME], notices: [] });
    register('page', [{ id: 'action:one', group: 'action', label: '第一条', keywords: [], run: vi.fn() }]);
    const pageHandler = vi.fn();
    window.addEventListener('paste', pageHandler);
    const openVideoAt = vi.fn();
    const wrapper = mountPalette(vi.fn(), { openVideoAt });

    const file = pngFile();
    const event = pasteEvent([file]);
    document.body.dispatchEvent(event);
    await flushPromises();
    window.removeEventListener('paste', pageHandler);

    expect(event.defaultPrevented).toBe(true);
    expect(pageHandler).not.toHaveBeenCalled();
    expect(unifiedSearch.prepareSearchImage).toHaveBeenCalledWith(file);
    expect(wrapper.get('[data-test="command-palette-image-thumb"]').attributes('src')).toBe(IMAGE.dataURL);
    expect(api.SearchScenesByImage).toHaveBeenCalledWith({ image_base64: 'QUJD', video_id: 0, at_ms: 0, limit: 8 });
    expect(wrapper.find('[data-test="command-palette-item-action:one"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="command-palette-item-image:9:61000"]').classes()).toContain('command-palette__item--active');

    await wrapper.get('[data-test="command-palette-input"]').trigger('keydown', { key: 'Enter' });
    await flushPromises();
    expect(openVideoAt).toHaveBeenCalledWith({ videoID: 9, startMs: 59_000 }, { from: 'command-palette' });
  });

  it('「清除」收起以图组与缩略图，命令回来；路上的以图结果回来也不再显示', async () => {
    let resolveImage;
    api.SearchScenesByImage.mockReturnValue(new Promise(resolve => { resolveImage = resolve; }));
    register('page', [{ id: 'action:one', group: 'action', label: '第一条', keywords: [], run: vi.fn() }]);
    const wrapper = mountPalette();
    document.body.dispatchEvent(pasteEvent([pngFile()]));
    await flushPromises();
    expect(wrapper.find('[data-test="command-palette-image-loading"]').exists()).toBe(true);

    await wrapper.get('[data-test="command-palette-clear-image"]').trigger('click');
    resolveImage({ hits: [FRAME], notices: [] });
    await flushPromises();
    expect(wrapper.find('[data-test="command-palette-image-thumb"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="command-palette-group-image"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="command-palette-item-action:one"]').exists()).toBe(true);
  });

  it('有图又输入文字：文字各组照常搜，以图组排在最后；「查看全部」把图带去场景检索', async () => {
    api.SearchScenesByImage.mockResolvedValue({ hits: [FRAME], notices: [] });
    api.SearchLibraryVideoPage.mockResolvedValue({ videos: [videoRow(1, 'sea.mp4')] });
    const navigate = vi.fn();
    const wrapper = mountPalette(vi.fn(), { navigate });
    document.body.dispatchEvent(pasteEvent([pngFile()]));
    await flushPromises();
    await typeQuery(wrapper, '海边');

    const groups = wrapper.findAll('[data-test^="command-palette-group-"]').map(group => group.attributes('data-test'));
    expect(groups.at(-1)).toBe('command-palette-group-image');
    expect(groups[0]).toBe('command-palette-group-video');

    await wrapper.get('[data-test="command-palette-view-all-image"]').trigger('click');
    await flushPromises();
    expect(navigate).toHaveBeenCalledWith('scenes', { image: { dataURL: IMAGE.dataURL, name: 'shot.png' } });
  });

  it('超过 20 MB 的图片就地拒绝并说明原因，不发请求', async () => {
    const actual = await vi.importActual('../utils/unifiedSearch.js');
    unifiedSearch.prepareSearchImage.mockImplementation(file => actual.prepareSearchImage(file));
    const wrapper = mountPalette();
    const big = { name: 'huge.png', type: 'image/png', size: 21 * 1024 * 1024 };
    document.body.dispatchEvent(pasteEvent([big]));
    await flushPromises();

    expect(wrapper.get('[data-test="command-palette-image-error"]').text()).toContain('超过 20 MB');
    expect(wrapper.find('[data-test="command-palette-image-thumb"]').exists()).toBe(false);
    expect(api.SearchScenesByImage).not.toHaveBeenCalled();
  });

  it('拖入图片同样按图找画面；拖入的不是图片时说明', async () => {
    api.SearchScenesByImage.mockResolvedValue({ hits: [], notices: ['visual_index_empty'] });
    const wrapper = mountPalette();
    const over = dragEvent('dragover', []);
    document.body.dispatchEvent(over);
    await wrapper.vm.$nextTick();
    expect(over.defaultPrevented).toBe(true);
    expect(wrapper.get('.command-palette').classes()).toContain('command-palette--dropping');

    const drop = dragEvent('drop', [pngFile()]);
    document.body.dispatchEvent(drop);
    await flushPromises();
    expect(drop.defaultPrevented).toBe(true);
    expect(wrapper.get('.command-palette').classes()).not.toContain('command-palette--dropping');
    expect(api.SearchScenesByImage).toHaveBeenCalledTimes(1);
    expect(wrapper.get('[data-test="command-palette-image-notice"]').text()).toContain('画面索引');

    document.body.dispatchEvent(dragEvent('drop', [new File(['x'], 'a.txt', { type: 'text/plain' })]));
    await flushPromises();
    expect(wrapper.get('[data-test="command-palette-image-error"]').text()).toContain('不是图片');
  });

  it('文字粘贴不拦；面板关着时图片粘贴也不拦', async () => {
    const wrapper = mountPalette();
    const text = pasteEvent([]);
    document.body.dispatchEvent(text);
    expect(text.defaultPrevented).toBe(false);

    await wrapper.setProps({ open: false });
    const image = pasteEvent([pngFile()]);
    document.body.dispatchEvent(image);
    await flushPromises();
    expect(image.defaultPrevented).toBe(false);
    expect(unifiedSearch.prepareSearchImage).not.toHaveBeenCalled();
  });

  it('关掉再打开面板，图与结果都清空', async () => {
    api.SearchScenesByImage.mockResolvedValue({ hits: [FRAME], notices: [] });
    const wrapper = mountPalette();
    document.body.dispatchEvent(pasteEvent([pngFile()]));
    await flushPromises();
    expect(wrapper.find('[data-test="command-palette-group-image"]').exists()).toBe(true);

    await wrapper.setProps({ open: false });
    await wrapper.setProps({ open: true });
    expect(wrapper.find('[data-test="command-palette-image-thumb"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="command-palette-group-image"]').exists()).toBe(false);
  });
});
