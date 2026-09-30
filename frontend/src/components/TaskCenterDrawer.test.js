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

import TaskCenterDrawer from './TaskCenterDrawer.vue';
import { BACKGROUND_TASK_LABELS } from '../utils/idleScheduling.js';
import { feedbackState, resetFeedback, resolveConfirm } from '../utils/feedback.js';

const KEYS = Object.keys(BACKGROUND_TASK_LABELS);

// 21 个 key 的快照：默认空闲，按用例覆盖其中几项。
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
  vi.useRealTimers();
});

describe('任务中心快照展示（APP-03）', () => {
  it('APP-03 21 个 key 全部列出，按运行中 / 等待空闲 / 空闲分组，名字一律是中文标签', async () => {
    api.GetTaskCenterSnapshot.mockResolvedValue(snapshot({
      overrides: {
        phash: { state: 'running', progress: { done: 3, total: 10 }, actions: ['cancel'] },
        exif: { state: 'waiting_idle', gate_reason: 'user_active', actions: ['run_now'] }
      }
    }));
    const wrapper = mountDrawer();
    await flushPromises();

    expect(wrapper.findAll('[data-test^="task-item-state-"]')).toHaveLength(21);
    expect(wrapper.get('[data-test="task-center-section-running"]').text()).toContain('近重复指纹');
    expect(wrapper.get('[data-test="task-center-section-waiting_idle"]').text()).toContain('图片 EXIF');
    expect(wrapper.get('[data-test="task-center-section-idle"]').findAll('li.task-item')).toHaveLength(19);
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
