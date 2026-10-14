import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => new Proxy({}, {
  get(target, prop) {
    if (typeof prop === 'symbol' || prop === 'then') return undefined;
    if (!target[prop]) target[prop] = vi.fn(() => Promise.resolve(null));
    return target[prop];
  }
}));
vi.mock('../../wailsjs/go/main/App', () => api);

import AppFeedback from './AppFeedback.vue';
import TaskCenterDrawer, { relativeTimeText } from './TaskCenterDrawer.vue';
import { BACKGROUND_TASK_LABELS } from '../utils/idleScheduling.js';
import { feedbackState, notifyUndoable, resetFeedback, resolveConfirm, runToastAction } from '../utils/feedback.js';
import * as recentOperations from '../utils/recentOperations.js';
import { registerCommands, unregisterCommands } from '../utils/commandRegistry.js';

const KEYS = Object.keys(BACKGROUND_TASK_LABELS);

// 全部 key（31 个）的快照：默认空闲，按用例覆盖其中几项。
function snapshot({ overrides = {}, recent = [], warnings = [] } = {}) {
  return {
    items: KEYS.map(key => ({
      key, state: 'idle', gate_reason: '', progress: null, last_run: null, actions: [], ...(overrides[key] || {})
    })),
    recent,
    warnings
  };
}

let handlers;
const wrappers = [];

function mountDrawer(props = { open: true }) {
  const wrapper = mount(TaskCenterDrawer, { props });
  wrappers.push(wrapper);
  return wrapper;
}

beforeEach(() => {
  vi.clearAllMocks();
  resetFeedback();
  handlers = {};
  window.runtime = { EventsOn: (event, handler) => { handlers[event] = handler; return () => { delete handlers[event]; }; } };
  api.GetTaskCenterSnapshot.mockResolvedValue(snapshot());
});

afterEach(() => {
  while (wrappers.length) wrappers.pop().unmount();
  delete window.runtime;
  recentOperations.clear();
  vi.useRealTimers();
});

describe('任务中心快照展示（APP-03）', () => {
  it('keeps skipped and unprocessed review counts distinct from success', async () => {
    api.GetTaskCenterSnapshot.mockResolvedValue(snapshot({ overrides: { ai_review: { last_run: { succeeded: 3, failed: 1, skipped: 2, remaining: 4, failures: [] } } } }));
    const wrapper = mountDrawer(); await flushPromises();
    expect(wrapper.get('[data-test="task-item-last-ai_review"]').text()).toContain('成功 3 · 失败 1 · 跳过 2 · 未处理 4');
  });

  it('运行状态只在选中该页签后检查，关闭后不挂载诊断页面', async () => {
    const wrapper = mountDrawer();
    await flushPromises();
    expect(api.GetSystemHealthSection).not.toHaveBeenCalled();
    await wrapper.get('[data-test="task-center-tab-health"]').trigger('click');
    await flushPromises();
    expect(api.GetSystemHealthSection).toHaveBeenCalledTimes(6);
    expect(wrapper.find('[data-test="system-health"]').exists()).toBe(true);
    await wrapper.setProps({ open: false });
    expect(wrapper.find('[data-test="system-health"]').exists()).toBe(false);
  });

  it('诊断里的任务处理入口回到任务页签', async () => {
    const wrapper = mountDrawer();
    await wrapper.get('[data-test="task-center-tab-health"]').trigger('click');
    await flushPromises();
    await wrapper.vm.runHealthCommand('action:task-center');
    expect(wrapper.vm.section).toBe('tasks');
    expect(wrapper.find('[data-test="task-center-recent"]').exists()).toBe(true);
  });

  it('APP-03 31 个 key 全部列出，按运行中 / 等待空闲 / 空闲分组，名字一律是中文标签', async () => {
    api.GetTaskCenterSnapshot.mockResolvedValue(snapshot({
      overrides: {
        phash: { state: 'running', progress: { done: 3, total: 10 }, actions: ['cancel'] },
        exif: { state: 'waiting_idle', gate_reason: 'user_active', actions: ['run_now'] }
      }
    }));
    const wrapper = mountDrawer();
    await flushPromises();

    expect(wrapper.findAll('[data-test^="task-item-state-"]')).toHaveLength(31);
    expect(wrapper.get('[data-test="task-center-section-running"]').text()).toContain('近重复指纹');
    expect(wrapper.get('[data-test="task-center-section-waiting_idle"]').text()).toContain('图片 EXIF');
    expect(wrapper.get('[data-test="task-center-section-idle"]').findAll('li.task-item')).toHaveLength(29);
    const names = wrapper.findAll('.task-item .task-item__name').map(name => name.text());
    expect(names).toEqual(expect.arrayContaining(Object.values(BACKGROUND_TASK_LABELS)));
    for (const key of ['image_cleanup', 'watchlist_enrich', 'movie_chart', 'browser_download']) {
      expect(names, key).not.toContain(key);
    }
  });

  it('APP-03 三种状态的文案、进度、等待原因与上一轮结果', async () => {
    api.GetTaskCenterSnapshot.mockResolvedValue(snapshot({
      overrides: {
        technical: { state: 'running', gate_reason: 'on_battery', progress: { done: 1, total: 4 } },
        face: { state: 'waiting_idle', gate_reason: 'outside_window' },
        backup: {
          last_run: { finished_at: '2026-09-29T08:05:00Z', succeeded: 0, failed: 1, failures: ['数据库备份：磁盘已满'] }
        }
      }
    }));
    const wrapper = mountDrawer();
    await flushPromises();

    expect(wrapper.get('[data-test="task-item-state-technical"]').text()).toBe('运行中 · 等待空闲（当前使用电池）');
    expect(wrapper.get('[data-test="task-item-progress-technical"]').text()).toContain('1/4');
    expect(wrapper.get('[data-test="task-item-state-face"]').text()).toBe('等待空闲（不在允许的时间段内）');
    expect(wrapper.get('[data-test="task-item-state-subtitle"]').text()).toBe('空闲');
    expect(wrapper.get('[data-test="task-item-last-backup"]').text()).toContain('成功 0 · 失败 1');
    expect(wrapper.get('[data-test="task-item-failures-backup"]').text()).toContain('数据库备份：磁盘已满');
    expect(wrapper.find('[data-test="task-item-last-subtitle"]').exists()).toBe(false);
  });

  // 明细后端只留 50 条，失败数是全部：多出来的照实说；AI 打标与技术信息给片库管理的入口看全部失败对象。
  it('失败明细被截断时写明只保留了前几条；AI 打标与技术信息给片库管理入口', async () => {
    const failures = Array.from({ length: 50 }, (_, index) => `f${index + 1}.mp4：解码失败`);
    api.GetTaskCenterSnapshot.mockResolvedValue(snapshot({
      overrides: {
        technical: { last_run: { finished_at: '2026-09-29T08:05:00Z', succeeded: 3, failed: 143, failures } },
        backup: { last_run: { finished_at: '2026-09-29T08:05:00Z', succeeded: 0, failed: 1, failures: ['数据库备份：磁盘已满'] } },
        ai_tagging: { last_run: { succeeded: 10, failed: 4, failures: [] } }
      }
    }));
    const run = vi.fn();
    registerCommands('test-ai-failed', [{ id: 'nav:smart:ai_tag_failed', group: 'navigate', label: 'AI 打标失败', run }]);
    try {
      const wrapper = mountDrawer();
      await flushPromises();
      expect(wrapper.get('[data-test="task-item-failures-technical"] summary').text())
        .toBe('失败明细 50 条（共 143 条失败，只保留了前 50 条明细）');
      expect(wrapper.get('[data-test="task-item-failures-backup"] summary').text()).toBe('失败明细 1 条');
      expect(wrapper.find('[data-test="task-item-failure-link-backup"]').exists()).toBe(false);

      await wrapper.get('[data-test="task-item-failure-link-technical"]').trigger('click');
      expect(wrapper.emitted('open-library-missing')).toEqual([[{ kind: 'video', field: 'technical' }]]);

      await wrapper.get('[data-test="task-item-failure-link-ai_tagging"]').trigger('click');
      await flushPromises();
      expect(run).toHaveBeenCalledOnce();
      expect(wrapper.emitted('close')).toHaveLength(2);
    } finally {
      unregisterCommands('test-ai-failed');
    }
  });

  it('APP-03 角标：运行中数量，有一项上一轮失败就标红点', async () => {
    api.GetTaskCenterSnapshot.mockResolvedValue(snapshot({
      overrides: {
        phash: { state: 'running' },
        proxy: { state: 'running' },
        cleanup: { last_run: { succeeded: 0, failed: 2, failures: [] } }
      }
    }));
    const wrapper = mountDrawer({ open: false });
    await flushPromises();

    expect(wrapper.emitted('badge-change').at(-1)).toEqual([{ running: 2, failed: true }]);
    expect(wrapper.find('[data-test="task-center"]').exists()).toBe(false);
  });

  it('APP-03 warnings 与读取失败都用中文说明，不丢整个抽屉', async () => {
    api.GetTaskCenterSnapshot.mockResolvedValue(snapshot({ warnings: ['数据库后端已切换，请重启应用后再查看'] }));
    const wrapper = mountDrawer();
    await flushPromises();
    expect(wrapper.get('[data-test="task-center-warnings"]').text()).toContain('请重启应用后再查看');

    api.GetTaskCenterSnapshot.mockRejectedValue('bridge down');
    await wrapper.vm.refresh();
    await flushPromises();
    expect(wrapper.get('[data-test="task-center-error"]').text()).toContain('读取任务状态失败');
  });
});

// D-P3-05：片库页后台任务状态条上的几项收进任务中心，文案沿用状态条。
describe('状态条收进任务中心（D-P3-05）', () => {
  const FINISHED = '2026-10-13T08:05:00Z';

  async function mountWith(overrides) {
    api.GetTaskCenterSnapshot.mockResolvedValue(snapshot({ overrides }));
    const wrapper = mountDrawer();
    await flushPromises();
    return wrapper;
  }

  it('准备中：运行中的项写「正在统计待处理项…」，没有进度条', async () => {
    const wrapper = await mountWith({
      technical: { state: 'running', preparing: true, actions: ['cancel'] },
      frame_hash: { state: 'running', preparing: false, progress: { done: 2, total: 8 } }
    });
    expect(wrapper.get('[data-test="task-item-state-technical"]').text()).toBe('运行中 · 正在统计待处理项…');
    expect(wrapper.find('[data-test="task-item-progress-technical"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="task-action-technical-cancel"]').exists()).toBe(true);
    expect(wrapper.get('[data-test="task-item-state-frame_hash"]').text()).toBe('运行中');
    expect(wrapper.get('[data-test="task-item-progress-frame_hash"]').text()).toContain('2/8');
  });

  it('跳过数：有跳过就列出，没有跳过不写「跳过 0」', async () => {
    const wrapper = await mountWith({
      technical: { last_run: { finished_at: FINISHED, succeeded: 3, skipped: 2, failed: 0, failures: [] } },
      local_metadata: { last_run: { finished_at: FINISHED, succeeded: 4, skipped: 0, failed: 0, failures: [] } }
    });
    expect(wrapper.get('[data-test="task-item-last-technical"]').text()).toMatch(/^上一轮（.+）：成功 3 · 失败 0 · 跳过 2（已完成）$/);
    expect(wrapper.get('[data-test="task-item-last-local_metadata"]').text()).not.toContain('跳过');
  });

  it('已取消与已完成分开写；有失败的一轮写「本轮结束，有失败项」', async () => {
    const wrapper = await mountWith({
      frame_hash: { last_run: { finished_at: FINISHED, succeeded: 1, skipped: 1, failed: 0, cancelled: true, failures: [] } },
      exif: { last_run: { finished_at: FINISHED, succeeded: 5, failed: 0, cancelled: false, failures: [] } },
      image_phash: { last_run: { finished_at: FINISHED, succeeded: 5, failed: 2, cancelled: false, failures: ['a.jpg：读不出'] } }
    });
    const cancelled = wrapper.get('[data-test="task-item-last-frame_hash"]').text();
    expect(cancelled).toContain('成功 1 · 失败 0 · 跳过 1（已取消）');
    expect(cancelled).not.toContain('已完成');
    expect(wrapper.get('[data-test="task-item-last-exif"]').text()).toContain('成功 5 · 失败 0（已完成）');
    expect(wrapper.get('[data-test="task-item-last-image_phash"]').text()).toContain('成功 5 · 失败 2（本轮结束，有失败项）');
  });

  it('没有结束时间的累计数（视频 AI 打标）不写完成与否', async () => {
    const wrapper = await mountWith({ ai_tagging: { last_run: { succeeded: 120, failed: 3, failures: [] } } });
    expect(wrapper.get('[data-test="task-item-last-ai_tagging"]').text()).toBe('上一轮：成功 120 · 失败 3');
  });

  it('近重复指纹：复用与移出范围照状态条的说法列出，并带指纹用途与失败含义两句说明', async () => {
    const wrapper = await mountWith({
      phash: { last_run: { finished_at: FINISHED, succeeded: 4, failed: 1, skipped: 0, reused: 3, out_of_scope: 2, failures: ['a.mp4：解码失败'] } }
    });
    expect(wrapper.get('[data-test="task-item-last-phash"]').text()).toContain('成功 4 · 失败 1 · 已有指纹复用 3 · 已移出处理范围 2（本轮结束，有失败项）');
    const notes = wrapper.findAll('[data-test="task-item-note-phash"]').map(note => note.text());
    expect(notes).toEqual([
      '指纹用于后续查找近重复视频，候选结果请在清理中心查看。',
      '处理失败表示未能生成指纹，部分视频仍可播放；具体原因见下方。'
    ]);
    expect(wrapper.get('[data-test="task-item-failures-phash"]').text()).toContain('a.mp4：解码失败');
  });

  it('近重复指纹的说明只在跑过、在跑或在等空闲时出现；没失败不说失败含义', async () => {
    let wrapper = await mountWith({});
    expect(wrapper.find('[data-test="task-item-note-phash"]').exists()).toBe(false);
    wrapper = await mountWith({ phash: { state: 'waiting_idle', gate_reason: 'user_active', actions: ['run_now'] } });
    expect(wrapper.findAll('[data-test="task-item-note-phash"]').map(note => note.text())).toEqual(['指纹用于后续查找近重复视频，候选结果请在清理中心查看。']);
    wrapper = await mountWith({ phash: { last_run: { finished_at: FINISHED, succeeded: 2, failed: 0, reused: 0, out_of_scope: 0, failures: [] } } });
    expect(wrapper.findAll('[data-test="task-item-note-phash"]')).toHaveLength(1);
    expect(wrapper.get('[data-test="task-item-last-phash"]').text()).not.toContain('复用');
  });

  it('近重复指纹在跑时照状态条的说法列本轮计数，跟着 task-center-changed 走；移出处理范围为 0 不列', async () => {
    const running = (counts, done) => ({ state: 'running', progress: { done, total: 10 }, run_counts: counts, actions: ['cancel'] });
    const wrapper = await mountWith({ phash: running({ succeeded: 0, reused: 0, failed: 0, out_of_scope: 0 }, 0) });
    expect(wrapper.get('[data-test="task-item-run-counts-phash"]').text()).toBe('本轮新生成 0 部，已有指纹复用 0 部，处理失败 0 部');

    handlers['task-center-changed'](snapshot({ overrides: { phash: running({ succeeded: 2, reused: 3, failed: 1, out_of_scope: 1 }, 7) } }));
    await flushPromises();
    expect(wrapper.get('[data-test="task-item-run-counts-phash"]').text()).toBe('本轮新生成 2 部，已有指纹复用 3 部，处理失败 1 部，已移出处理范围 1 部');
    expect(wrapper.get('[data-test="task-item-progress-phash"]').text()).toContain('7/10');
    expect(wrapper.find('[data-test="task-item-last-phash"]').exists()).toBe(false);
    // 在跑时没有失败明细，「具体原因见下方」那句仍只跟上一轮走。
    expect(wrapper.findAll('[data-test="task-item-note-phash"]')).toHaveLength(1);

    handlers['task-center-changed'](snapshot({ overrides: {
      phash: { last_run: { finished_at: FINISHED, succeeded: 2, failed: 1, reused: 4, out_of_scope: 1, failures: ['a.mp4：解码失败'] }, actions: ['start'] },
      frame_hash: { state: 'running', progress: { done: 1, total: 4 } }
    } }));
    await flushPromises();
    expect(wrapper.find('[data-test="task-item-run-counts-phash"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="task-item-last-phash"]').text()).toContain('成功 2 · 失败 1 · 已有指纹复用 4 · 已移出处理范围 1（本轮结束，有失败项）');
    expect(wrapper.find('[data-test="task-item-run-counts-frame_hash"]').exists()).toBe(false);
  });

  it('总数为 0 的一轮：补全类写「无需补全（已是最新状态）」，其余写「无需处理（已是最新状态）」，不列计数', async () => {
    const wrapper = await mountWith({
      technical: { last_run: { finished_at: FINISHED, succeeded: 0, failed: 0, nothing_to_do: true, failures: [] } },
      scene_index: { last_run: { finished_at: FINISHED, succeeded: 0, failed: 0, nothing_to_do: true, failures: [] } }
    });
    const technical = wrapper.get('[data-test="task-item-last-technical"]').text();
    expect(technical).toMatch(/^上一轮（.+）：无需补全（已是最新状态）$/);
    expect(technical).not.toContain('成功');
    expect(wrapper.get('[data-test="task-item-last-scene_index"]').text()).toMatch(/：无需处理（已是最新状态）$/);
  });

  it('NFO 写出单独一项：运行中显示进度，「取消」调用 CancelLocalMetadataExport，不动本地资料补全', async () => {
    const wrapper = await mountWith({
      local_metadata_export: { state: 'running', progress: { done: 2, total: 5 }, actions: ['cancel'] },
      local_metadata: { actions: ['start'] }
    });
    const item = wrapper.get('[data-test="task-item-local_metadata_export"]');
    expect(item.get('.task-item__name').text()).toBe('NFO 写出');
    expect(wrapper.get('[data-test="task-item-progress-local_metadata_export"]').text()).toContain('2/5');
    await wrapper.get('[data-test="task-action-local_metadata_export-cancel"]').trigger('click');
    await flushPromises();
    expect(api.CancelLocalMetadataExport).toHaveBeenCalledTimes(1);
    expect(api.CancelLocalMetadataBackfill).not.toHaveBeenCalled();
    expect(wrapper.get('[data-test="task-item-state-local_metadata"]').text()).toBe('空闲');
  });

  it('NFO 写出取消失败给中文提示', async () => {
    api.CancelLocalMetadataExport.mockRejectedValueOnce('本地资料写出任务未运行');
    const wrapper = await mountWith({ local_metadata_export: { state: 'running', actions: ['cancel'] } });
    await wrapper.get('[data-test="task-action-local_metadata_export-cancel"]').trigger('click');
    await flushPromises();
    expect(feedbackState.toasts.map(toast => toast.message).join('\n')).toContain('取消「NFO 写出」失败：本地资料写出任务未运行');
  });
});

describe('任务动作（APP-03）', () => {
  it('APP-03 启动 / 取消 / 立即运行调用对应的零参绑定，没有绑定的动作不出按钮', async () => {
    vi.useFakeTimers();
    api.GetTaskCenterSnapshot.mockResolvedValue(snapshot({
      overrides: {
        technical: { actions: ['start'] },
        proxy: { state: 'running', actions: ['cancel'] },
        exif: { state: 'waiting_idle', gate_reason: 'user_active', actions: ['run_now'] },
        phash: { actions: ['start', 'retry_failed'] }
      }
    }));
    const wrapper = mountDrawer();
    await flushPromises();

    await wrapper.get('[data-test="task-action-technical-start"]').trigger('click');
    await wrapper.get('[data-test="task-action-proxy-cancel"]').trigger('click');
    await wrapper.get('[data-test="task-action-exif-run_now"]').trigger('click');
    await flushPromises();

    expect(api.StartTechnicalBackfill).toHaveBeenCalledTimes(1);
    expect(api.CancelPlaybackProxyTask).toHaveBeenCalledTimes(1);
    expect(api.RunGatedTaskNow).toHaveBeenCalledWith('exif');
    expect(wrapper.find('[data-test="task-action-phash-retry_failed"]').exists()).toBe(false);

    // 动作之后重拉一次快照（合并成一次）。
    api.GetTaskCenterSnapshot.mockClear();
    vi.advanceTimersByTime(400);
    await flushPromises();
    expect(api.GetTaskCenterSnapshot).toHaveBeenCalledTimes(1);
  });

  it('APP-03 动作失败给出中文提示', async () => {
    api.GetTaskCenterSnapshot.mockResolvedValue(snapshot({ overrides: { technical: { actions: ['start'] } } }));
    api.StartTechnicalBackfill.mockRejectedValueOnce('已有任务在跑');
    const wrapper = mountDrawer();
    await flushPromises();

    await wrapper.get('[data-test="task-action-technical-start"]').trigger('click');
    await flushPromises();
    expect(feedbackState.toasts.map(toast => toast.message).join('\n')).toContain('启动「技术信息」失败：已有任务在跑');
  });
});

describe('事件刷新（APP-03）', () => {
  it('APP-03 task-center-changed 的载荷直接替换快照', async () => {
    const wrapper = mountDrawer();
    await flushPromises();

    handlers['task-center-changed'](snapshot({ overrides: { semantic: { state: 'running' } } }));
    await flushPromises();
    expect(wrapper.get('[data-test="task-center-section-running"]').text()).toContain('语义索引');
  });

  it('APP-03 subtitle-queue 与 cleanup-progress 只由服务层发出，收到后合并重拉快照', async () => {
    vi.useFakeTimers();
    mountDrawer();
    await flushPromises();
    api.GetTaskCenterSnapshot.mockClear();

    handlers['subtitle-queue']({ total: 1 });
    handlers['cleanup-progress']({ current: 1, total: 9 });
    handlers['cleanup-progress']({ current: 2, total: 9 });
    vi.advanceTimersByTime(400);
    await flushPromises();

    expect(api.GetTaskCenterSnapshot).toHaveBeenCalledTimes(1);
  });

  // 三个分区的面板写 role=tabpanel、id 与 aria-labelledby，和页签的 id / aria-controls 互相指向；← → 切换时跟着换。
  it('分区页签与面板互相指向，← → 切换后面板跟着换', async () => {
    const wrapper = mountDrawer();
    await flushPromises();
    for (const [key, next] of [['tasks', 'ArrowRight'], ['recent', 'ArrowRight'], ['health', 'ArrowRight'], ['tasks', null]]) {
      const panel = wrapper.get('[role="tabpanel"]');
      expect(panel.attributes('id')).toBe(`task-center-panel-${key}`);
      expect(panel.attributes('aria-labelledby')).toBe(`task-center-tab-${key}`);
      expect(wrapper.get(`#task-center-tab-${key}`).attributes('aria-controls')).toBe(`task-center-panel-${key}`);
      expect(wrapper.get(`#task-center-tab-${key}`).attributes('aria-selected')).toBe('true');
      if (next) { await wrapper.get('[role="tablist"]').trigger('keydown', { key: next }); await flushPromises(); }
    }
    expect(wrapper.get('[role="tabpanel"]').find('[data-test="task-center-recent"]').exists()).toBe(true);
  });

  it('打开抽屉时主动拉一次，Esc 关闭', async () => {
    const wrapper = mountDrawer({ open: false });
    await flushPromises();
    api.GetTaskCenterSnapshot.mockClear();

    await wrapper.setProps({ open: true });
    await flushPromises();
    expect(api.GetTaskCenterSnapshot).toHaveBeenCalledTimes(1);

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    expect(wrapper.emitted('close')).toHaveLength(1);
  });
});

describe('最近任务四类（APP-03）', () => {
  const recent = [
    { kind: 'subtitle', id: '12', video_id: 3, title: '海边.mp4', status: 'running', message: '', actions: ['cancel'] },
    { kind: 'subtitle', id: '13', video_id: 4, title: '山谷.mp4', status: 'needs_confirmation', message: '疑似幻觉', finished_at: '2026-09-29T08:00:00Z', actions: ['force', 'discard', 'retry'] },
    { kind: 'enhancement', id: '21', video_id: 5, output_video_id: 55, title: '老片.mkv', status: 'completed', message: '', actions: ['reveal_output', 'open_output_in_library'] },
    { kind: 'enhancement', id: '22', video_id: 6, title: '另一部.mkv', status: 'failed', message: '磁盘空间不足', actions: ['retry'] },
    { kind: 'download', id: 'abc123', video_id: 0, title: '预告片', status: 'failed', message: '网络中断', actions: ['retry'] },
    { kind: 'download', id: 'def456', video_id: 0, title: '花絮', status: 'done', error_code: 'not_in_scan_roots', message: '不在扫描目录', actions: ['reveal', 'add_directory_to_scan', 'reimport'] },
    { kind: 'proxy', id: '9', video_id: 9, title: '旧格式.avi', status: 'encode_failed', message: 'ffmpeg 退出', actions: ['retry'] }
  ];

  async function mountWithRecent() {
    api.GetTaskCenterSnapshot.mockResolvedValue(snapshot({ recent }));
    const wrapper = mountDrawer();
    await flushPromises();
    return wrapper;
  }

  it('APP-03 字幕、超分、下载、播放代理四类分组展示，状态码翻成中文', async () => {
    const wrapper = await mountWithRecent();

    expect(wrapper.findAll('.task-recent-group h4').map(heading => heading.text())).toEqual(['字幕', '视频超分', '插件下载', '播放代理']);
    expect(wrapper.get('[data-test="task-recent-subtitle-13"]').text()).toContain('待确认');
    expect(wrapper.get('[data-test="task-recent-subtitle-13"]').text()).toContain('疑似幻觉');
    expect(wrapper.get('[data-test="task-recent-enhancement-22"]').text()).toContain('失败');
    expect(wrapper.get('[data-test="task-recent-download-def456"]').text()).toContain('已完成');
    expect(wrapper.get('[data-test="task-recent-proxy-9"]').text()).toContain('转封装失败');
  });

  it('APP-03 字幕任务的 id 是字符串，调用数字参数的绑定前先转换', async () => {
    const wrapper = await mountWithRecent();

    await wrapper.get('[data-test="task-recent-action-subtitle-12-cancel"]').trigger('click');
    await wrapper.get('[data-test="task-recent-action-subtitle-13-retry"]').trigger('click');
    await flushPromises();

    expect(api.CancelSubtitleTask).toHaveBeenCalledWith(12);
    expect(api.ResolveSubtitleJob).toHaveBeenCalledWith(13, 'retry');
  });

  it('APP-03 放弃待确认字幕要先确认，取消确认就不调用', async () => {
    const wrapper = await mountWithRecent();

    await wrapper.get('[data-test="task-recent-action-subtitle-13-discard"]').trigger('click');
    expect(feedbackState.confirm?.title).toBe('放弃待确认的字幕');
    resolveConfirm(false);
    await flushPromises();
    expect(api.ResolveSubtitleJob).not.toHaveBeenCalled();

    await wrapper.get('[data-test="task-recent-action-subtitle-13-discard"]').trigger('click');
    resolveConfirm(true);
    await flushPromises();
    expect(api.ResolveSubtitleJob).toHaveBeenCalledWith(13, 'discard');
  });

  // Esc 只关最上面一层（D-UD-13）：抽屉常挂载，比之后弹出的确认框（BaseModal）先在 document 上登记 keydown，
  // 曾经一下 Esc 先关了抽屉、再关确认框。确认框开着时抽屉让出 Esc：第一下只关确认框，第二下才关抽屉。
  it('抽屉里弹出的确认框：第一下 Esc 只关确认框（按取消处理），第二下关抽屉', async () => {
    const feedback = mount(AppFeedback, { attachTo: document.body });
    const wrapper = await mountWithRecent();
    const pressEscape = () => document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));

    await wrapper.get('[data-test="task-recent-action-subtitle-13-discard"]').trigger('click');
    await flushPromises();
    expect(document.querySelector('[data-test="app-confirm"]')).not.toBeNull();

    pressEscape();
    await flushPromises();
    expect(document.querySelector('[data-test="app-confirm"]')).toBeNull();
    expect(api.ResolveSubtitleJob).not.toHaveBeenCalled();
    expect(wrapper.emitted('close')).toBeUndefined();

    pressEscape();
    expect(wrapper.emitted('close')).toHaveLength(1);
    feedback.unmount();
  });

  it('APP-03 超分完成的产物可在访达中显示、在片库中打开；失败的可重试', async () => {
    const wrapper = await mountWithRecent();

    await wrapper.get('[data-test="task-recent-action-enhancement-21-reveal_output"]').trigger('click');
    // 同一条任务的动作执行期间按钮置灰，等上一个动作结束再点下一个。
    await flushPromises();
    await wrapper.get('[data-test="task-recent-action-enhancement-21-open_output_in_library"]').trigger('click');
    await wrapper.get('[data-test="task-recent-action-enhancement-22-retry"]').trigger('click');
    await flushPromises();

    expect(api.OpenDirectory).toHaveBeenCalledWith(55);
    expect(wrapper.emitted('open-video')[0]).toEqual([55]);
    expect(api.RetryEnhancementTask).toHaveBeenCalledWith(22);
  });

  it('APP-03 下载任务按 task_uid 调用，结果码不是 ok 时显示后端的中文原因', async () => {
    api.RetryDownload.mockResolvedValueOnce({ code: 'retry_requires_browser', message: '应用重启后请求信息已不在，请回浏览器重新推送' });
    api.AddDownloadDirectoryToScan.mockResolvedValueOnce({ code: 'ok' });
    const wrapper = await mountWithRecent();

    await wrapper.get('[data-test="task-recent-action-download-abc123-retry"]').trigger('click');
    await wrapper.get('[data-test="task-recent-action-download-def456-add_directory_to_scan"]').trigger('click');
    await flushPromises();

    expect(api.RetryDownload).toHaveBeenCalledWith('abc123');
    expect(api.AddDownloadDirectoryToScan).toHaveBeenCalledWith('def456');
    const messages = feedbackState.toasts.map(toast => toast.message).join('\n');
    expect(messages).toContain('请回浏览器重新推送');
    expect(messages).toContain('已提交入库');
  });

  it('MEDIA-04 字幕写回失败的任务，「强制生成」显示为「重试写回」', async () => {
    api.GetTaskCenterSnapshot.mockResolvedValue(snapshot({ recent: [
      ...recent,
      { kind: 'subtitle', id: '14', video_id: 7, title: '雨夜.mp4', status: 'needs_confirmation', error_code: 'subtitle_replace_failed', message: '写回失败', actions: ['force', 'discard'] }
    ] }));
    const wrapper = mountDrawer();
    await flushPromises();

    expect(wrapper.get('[data-test="task-recent-action-subtitle-14-force"]').text()).toBe('重试写回');
    expect(wrapper.get('[data-test="task-recent-action-subtitle-13-force"]').text()).toBe('强制生成');
  });

  it('APP-03 播放代理失败项可重新生成', async () => {
    const wrapper = await mountWithRecent();

    await wrapper.get('[data-test="task-recent-action-proxy-9-retry"]').trigger('click');
    await flushPromises();

    expect(api.CreatePlaybackProxy).toHaveBeenCalledWith(9);
  });
});

describe('集中整理任务历史入口', () => {
  it('当前、完成及失败任务都按持久ID打开，不触发启动或清理', async () => {
    api.GetTaskCenterSnapshot.mockResolvedValue(snapshot({ recent: ['running', 'completed', 'failed'].map((status, index) => ({ kind: 'cleanup_consolidation', id: String(index + 10), status, title: '集中整理', actions: ['open_consolidation'] })) }));
    const wrapper = mountDrawer(); await flushPromises();
    expect(wrapper.text()).toContain('集中整理');
    await wrapper.get('[data-test="task-recent-action-cleanup_consolidation-11-open_consolidation"]').trigger('click');
    expect(wrapper.emitted('open-consolidation')).toEqual([[11]]); expect(wrapper.emitted('close')).toHaveLength(1); wrapper.unmount();
  });
});

describe('视频工作台最近任务（P-008）', () => {
  it('导出中的项目可取消，也可在工作台中打开（关闭抽屉）', async () => {
    api.GetTaskCenterSnapshot.mockResolvedValue(snapshot({ recent: [
      { kind: 'video_edit', id: '31', title: '合并 3 个视频', status: 'running', message: '', actions: ['cancel', 'open_video_edit'] },
      { kind: 'video_edit', id: '32', title: '去片头', status: 'partial', message: '1 项失败', actions: ['open_video_edit'] }
    ] }));
    const wrapper = mountDrawer();
    await flushPromises();
    expect(wrapper.get('[data-test="task-recent-video_edit"]').text()).toContain('视频工作台');
    expect(wrapper.get('[data-test="task-recent-video_edit-31"]').text()).toContain('导出中');
    expect(wrapper.get('[data-test="task-recent-video_edit-32"]').text()).toContain('部分完成');
    await wrapper.get('[data-test="task-recent-action-video_edit-31-cancel"]').trigger('click');
    await flushPromises();
    expect(api.CancelEditProject).toHaveBeenCalledWith(31);
    await wrapper.get('[data-test="task-recent-action-video_edit-32-open_video_edit"]').trigger('click');
    await flushPromises();
    expect(wrapper.emitted('open-video-edit')[0]).toEqual([32]);
    expect(wrapper.emitted('close')).toBeTruthy();
  });
});

// 统一设计 D-UD-09：任务中心「最近操作」页签——事后撤销的入口，与提示里的「撤销」是同一条记录。
describe('最近操作（D-UD-09）', () => {
  async function openRecent() {
    const wrapper = mountDrawer();
    await flushPromises();
    await wrapper.get('[data-test="task-center-tab-recent"]').trigger('click');
    return wrapper;
  }

  it('没有记录时给空状态，并写明只在内存里、重启后清空', async () => {
    const wrapper = await openRecent();
    // 分区是 AppTabs（D-UD-06）：当前页签 aria-selected；没有记录时不带条数徽标。
    expect(wrapper.get('[data-test="task-center-tab-recent"]').attributes('aria-selected')).toBe('true');
    expect(wrapper.get('[data-test="task-center-tab-tasks"]').attributes('aria-selected')).toBe('false');
    expect(wrapper.find('[data-test="task-center-tab-recent"] .ui-tabs__count').exists()).toBe(false);
    expect(wrapper.find('[data-test="task-center-operations-empty"]').exists()).toBe(true);
    expect(wrapper.get('[data-test="task-center-operations-note"]').text()).toContain('应用重启后清空');
    expect(wrapper.get('[data-test="task-center-operations-note"]').text()).toContain('最近 20 次');
    expect(wrapper.find('[data-test="task-center-recent"]').exists()).toBe(false);
  });

  it('新的在前，每条有说明、相对时间与「撤销」；页签上带条数', async () => {
    vi.useFakeTimers({ now: new Date('2026-10-13T10:00:00Z'), toFake: ['Date', 'setInterval', 'clearInterval', 'setTimeout', 'clearTimeout'] });
    const older = recentOperations.push({ label: '移到废纸篓：3 个视频', undo: vi.fn() });
    vi.setSystemTime(new Date('2026-10-13T10:05:00Z'));
    const newer = recentOperations.push({ label: '批量编辑：12 部视频', undo: vi.fn() });
    vi.setSystemTime(new Date('2026-10-13T10:05:20Z'));
    const wrapper = await openRecent();

    const rows = wrapper.findAll('[data-test^="task-operation-"]').filter(row => !row.attributes('data-test').includes('undo'));
    expect(rows.map(row => row.attributes('data-test'))).toEqual([`task-operation-${newer}`, `task-operation-${older}`]);
    expect(rows[0].text()).toContain('批量编辑：12 部视频');
    expect(rows[0].text()).toContain('刚刚');
    expect(rows[1].text()).toContain('5 分钟前');
    expect(rows[0].get(`[data-test="task-operation-undo-${newer}"]`).text()).toBe('撤销');
    expect(wrapper.get('[data-test="task-center-tab-recent"] .ui-tabs__count').text()).toBe('2');

    // 开着时相对时间会走：再过一分钟，「刚刚」变成「1 分钟前」。
    vi.advanceTimersByTime(60000);
    await flushPromises();
    expect(wrapper.get(`[data-test="task-operation-${newer}"]`).text()).toContain('1 分钟前');
  });

  it('点「撤销」执行一次既有的恢复接口，成功后这条从列表消失', async () => {
    const undo = vi.fn(() => Promise.resolve());
    const id = recentOperations.push({ label: '移到废纸篓：1 个视频', undo });
    const wrapper = await openRecent();

    await wrapper.get(`[data-test="task-operation-undo-${id}"]`).trigger('click');
    await flushPromises();
    expect(undo).toHaveBeenCalledTimes(1);
    expect(wrapper.find(`[data-test="task-operation-${id}"]`).exists()).toBe(false);
    expect(wrapper.find('[data-test="task-center-operations-empty"]').exists()).toBe(true);
    expect(feedbackState.toasts.filter(toast => toast.level === 'error')).toEqual([]);
  });

  it('撤销失败：用全局提示说原因，这条留着可以再试', async () => {
    const undo = vi.fn().mockRejectedValueOnce(new Error('回收站里的记录已被清空')).mockResolvedValueOnce();
    const id = recentOperations.push({ label: '批量编辑：2 张图片', undo });
    const wrapper = await openRecent();

    await wrapper.get(`[data-test="task-operation-undo-${id}"]`).trigger('click');
    await flushPromises();
    expect(feedbackState.toasts.map(toast => [toast.level, toast.message])).toContainEqual(['error', '撤销失败：回收站里的记录已被清空']);
    expect(wrapper.find(`[data-test="task-operation-${id}"]`).exists()).toBe(true);
    expect(wrapper.get(`[data-test="task-operation-undo-${id}"]`).element.disabled).toBe(false);

    await wrapper.get(`[data-test="task-operation-undo-${id}"]`).trigger('click');
    await flushPromises();
    expect(undo).toHaveBeenCalledTimes(2);
    expect(wrapper.find(`[data-test="task-operation-${id}"]`).exists()).toBe(false);
  });

  it('notifyUndoable 记下的操作出现在这里；从提示里撤销后这里同步消失，恢复接口只执行一次', async () => {
    const undo = vi.fn(() => Promise.resolve());
    const toastID = notifyUndoable('已移到废纸篓：2 个视频', undo);
    const wrapper = await openRecent();
    expect(wrapper.text()).toContain('已移到废纸篓：2 个视频');

    await runToastAction(toastID);
    await flushPromises();
    expect(undo).toHaveBeenCalledTimes(1);
    expect(wrapper.findAll('[data-test^="task-operation-undo-"]')).toHaveLength(0);
  });
});

describe('relativeTimeText', () => {
  const now = new Date(2026, 9, 13, 18, 30).getTime();
  it('刚刚 / 分钟 / 小时，再早写月日时分；无效时间给空串', () => {
    expect(relativeTimeText(now - 20 * 1000, now)).toBe('刚刚');
    expect(relativeTimeText(now - 59 * 60000, now)).toBe('59 分钟前');
    expect(relativeTimeText(now - 3 * 3600000, now)).toBe('3 小时前');
    expect(relativeTimeText(new Date(2026, 9, 11, 9, 5).getTime(), now)).toBe('10-11 09:05');
    expect(relativeTimeText(0, now)).toBe('');
    expect(relativeTimeText(undefined, now)).toBe('');
  });
});
