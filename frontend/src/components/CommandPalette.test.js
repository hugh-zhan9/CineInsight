import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'SearchLibraryVideoPage', 'RunGatedTaskNow',
  'CancelFaceAnalysis', 'CancelImageAITagging', 'CancelImageEXIFBackfill', 'CancelImageSemanticIndex', 'CancelLocalMetadataBackfill',
  'CancelPerceptualHashBackfill', 'CancelPlaybackProxyTask', 'CancelSemanticIndex', 'CancelSubtitle', 'CancelTechnicalBackfill',
  'CreateDatabaseBackup', 'StartImageAITagging', 'StartImageEXIFBackfill', 'StartImageSemanticIndex',
  'StartLocalMetadataBackfill', 'StartPerceptualHashBackfill', 'StartTechnicalBackfill', 'TriggerAITagging',
  'LogFrontend',
  'StartFrameHashBackfill', 'CancelFrameHashBackfill'
].map(name => [name, vi.fn()])));

vi.mock('../../wailsjs/go/main/App', () => api);

import CommandPalette from './CommandPalette.vue';
import { registerCommands, unregisterCommands } from '../utils/commandRegistry.js';
import { buildTaskCommands } from '../utils/taskCommands.js';

const scopesUsed = new Set();

function register(scopeKey, commands) {
  scopesUsed.add(scopeKey);
  registerCommands(scopeKey, commands);
}

function videoRow(id, name, displayTitle = '') {
  return { id, name, display_title: displayTitle };
}

function mountPalette(openVideo = vi.fn()) {
  return mount(CommandPalette, { props: { open: true, openVideo } });
}

// 输入 → 防抖窗口走完 → 搜索的 promise 落地。
async function typeQuery(wrapper, text) {
  await wrapper.get('[data-test="command-palette-input"]').setValue(text);
  vi.advanceTimersByTime(200);
  await flushPromises();
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.useFakeTimers();
  api.SearchLibraryVideoPage.mockResolvedValue({ videos: [] });
});

afterEach(() => {
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
    const wrapper = mount(CommandPalette, {
      props: { open: true, openVideo: vi.fn() },
      attachTo: document.body
    });
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

describe('命令面板视频组', () => {
  it('输入 2 字起调用 SearchLibraryVideoPage，限 8 条', async () => {
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
        sort_mode: 'balanced'
      },
      limit: 8
    });
  });

  it('后端多返回也只显示 8 条，标题优先显示显示标题', async () => {
    api.SearchLibraryVideoPage.mockResolvedValue({
      videos: Array.from({ length: 12 }, (_, index) => videoRow(index + 1, `file-${index + 1}.mp4`, index === 0 ? '海边落日' : ''))
    });
    const wrapper = mountPalette();

    await typeQuery(wrapper, '海边');

    const items = wrapper.findAll('[data-test^="command-palette-item-video:"]');
    expect(items).toHaveLength(8);
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
