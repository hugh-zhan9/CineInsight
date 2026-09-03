// 断言原在 src/components/VideoListPage.test.js：四条后台任务状态条抽成独立组件后
// 原样搬来，只把挂载对象换成 BackgroundTaskStatusBars。
import { flushPromises, mount } from '@vue/test-utils';

const feedback = vi.hoisted(() => ({
  confirmAction: vi.fn(() => Promise.resolve(true)),
  notify: vi.fn(),
  notifyError: vi.fn(),
  notifySuccess: vi.fn()
}));
vi.mock('../../utils/feedback.js', async (importOriginal) => ({
  ...(await importOriginal()),
  ...feedback
}));

import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => new Proxy({}, {
  get(target, prop) {
    if (typeof prop === 'symbol' || prop === 'then') return undefined;
    if (!target[prop]) target[prop] = vi.fn(() => Promise.resolve(null));
    return target[prop];
  }
}));

vi.mock('../../../wailsjs/go/main/App', () => api);

import BackgroundTaskStatusBars from './BackgroundTaskStatusBars.vue';

beforeEach(() => {
  vi.clearAllMocks();
  delete window.runtime;
  feedback.confirmAction.mockResolvedValue(true);
  for (const name of ['GetTechnicalBackfillStatus', 'GetPerceptualHashBackfillStatus', 'GetFrameHashBackfillStatus', 'GetLocalMetadataBackfillStatus', 'GetLocalMetadataExportStatus']) {
    api[name].mockResolvedValue({ running: false, preparing: false, completed: false, cancelled: false, failed: 0, failures: [] });
  }
  api.GetIdleSchedulerStatus.mockResolvedValue({ enabled: true, waiting: [], bypass_tasks: [] });
});

async function mountBars() {
  const wrapper = mount(BackgroundTaskStatusBars);
  await flushPromises();
  return wrapper;
}

describe('后台任务状态条', () => {
  it('renders preparing, empty, failure, and failure-summary backfill states', async () => {
    const wrapper = await mountBars();

    wrapper.vm.technicalBackfill = { ...wrapper.vm.technicalBackfill, running: true, preparing: true };
    await wrapper.vm.$nextTick();
    expect(wrapper.text()).toContain('正在统计待补全视频');

    wrapper.vm.technicalBackfill = { ...wrapper.vm.technicalBackfill, running: false, preparing: false, completed: true, total: 0, failed: 0 };
    await wrapper.vm.$nextTick();
    expect(wrapper.text()).toContain('技术信息无需补全');

    wrapper.vm.technicalBackfill = {
      ...wrapper.vm.technicalBackfill,
      completed: true,
      total: 1,
      processed: 1,
      succeeded: 0,
      skipped: 0,
      failed: 1,
      failures: [{ video_id: 4, name: 'broken.mkv', error: 'ffprobe failed' }]
    };
    await wrapper.vm.$nextTick();
    expect(wrapper.text()).toContain('失败 1');
    expect(wrapper.text()).toContain('broken.mkv：ffprobe failed');
    wrapper.unmount();
  });

  it('starts backfill from the mounted page and applies the returned state', async () => {
    const wrapper = await mountBars();
    api.StartTechnicalBackfill.mockResolvedValueOnce({ running: true, preparing: true, total: 0 });

    await wrapper.vm.startTechnicalBackfill();

    expect(api.StartTechnicalBackfill).toHaveBeenCalledOnce();
    expect(wrapper.vm.technicalBackfill.running).toBe(true);
    expect(wrapper.vm.technicalBackfill.preparing).toBe(true);
    wrapper.unmount();
  });

  it('starts NFO export for the complete current filter', async () => {
    const wrapper = await mountBars();
    feedback.confirmAction.mockResolvedValueOnce(true);
    api.StartLocalMetadataExport.mockResolvedValueOnce({ running: true, total: 3, processed: 0 });

    // 筛选由片库页在调用时算好传进来，组件只负责确认与发起。
    await wrapper.vm.startLocalMetadataExport({ keyword: '导演剪辑版', tag_ids: [7] });

    expect(api.StartLocalMetadataExport).toHaveBeenCalledWith({
      filter: expect.objectContaining({ keyword: '导演剪辑版', tag_ids: [7] })
    });
    expect(wrapper.vm.localMetadataExport).toEqual(expect.objectContaining({ running: true, total: 3 }));
    wrapper.unmount();
  });

  it('applies runtime backfill events and refreshes state after cancellation', async () => {
    const handlers = new Map();
    window.runtime = {
      EventsOn: vi.fn((name, handler) => {
        handlers.set(name, handler);
        return () => handlers.delete(name);
      })
    };
    const wrapper = await mountBars();
    handlers.get('technical-backfill-state')({ running: true, preparing: false, total: 3, processed: 1, succeeded: 1 });
    await wrapper.vm.$nextTick();
    expect(wrapper.vm.technicalBackfill).toEqual(expect.objectContaining({ running: true, total: 3, processed: 1, succeeded: 1 }));
    expect(wrapper.text()).toContain('技术信息 1/3');

    api.CancelTechnicalBackfill.mockResolvedValueOnce();
    api.GetTechnicalBackfillStatus.mockResolvedValueOnce({ running: false, cancelled: true, completed: false, total: 3, processed: 1, succeeded: 1, failed: 0, failures: [] });
    await wrapper.vm.cancelTechnicalBackfill();

    expect(api.CancelTechnicalBackfill).toHaveBeenCalledOnce();
    expect(wrapper.vm.technicalBackfill.cancelled).toBe(true);
    wrapper.unmount();
    expect(handlers.has('technical-backfill-state')).toBe(false);
  });

  it('各份状态镜像回片库页，管理菜单的进度文案才有数据', async () => {
    const wrapper = await mountBars();
    wrapper.vm.perceptualHash = { ...wrapper.vm.perceptualHash, running: true, processed: 2, total: 9 };
    await wrapper.vm.$nextTick();
    const state = wrapper.emitted('state-change').at(-1)[0];
    expect(state.perceptualHash).toEqual(expect.objectContaining({ running: true, processed: 2, total: 9 }));
    // 帧哈希（P-008）也要镜像出去：清理面板的「补全帧哈希」按钮读的就是它。
    expect(Object.keys(state).sort()).toEqual(['frameHash', 'localMetadataBackfill', 'localMetadataExport', 'perceptualHash', 'technicalBackfill']);
    wrapper.unmount();
  });

  // 帧哈希状态条（P-008）：进度、失败清单、取消，与另外两条同一套路。
  it('帧哈希状态条给出进度、失败清单与取消入口', async () => {
    const handlers = new Map();
    window.runtime = { EventsOn: vi.fn((name, handler) => { handlers.set(name, handler); return () => handlers.delete(name); }) };
    const wrapper = await mountBars();

    handlers.get('frame-hash-backfill-state')({ running: true, preparing: true });
    await wrapper.vm.$nextTick();
    expect(wrapper.text()).toContain('正在统计待补全帧哈希的视频');

    handlers.get('frame-hash-backfill-state')({ running: true, preparing: false, total: 7, processed: 3 });
    await wrapper.vm.$nextTick();
    expect(wrapper.text()).toContain('帧哈希 3/7');

    api.CancelFrameHashBackfill.mockResolvedValueOnce();
    api.GetFrameHashBackfillStatus.mockResolvedValueOnce({
      running: false, cancelled: true, completed: false, total: 7, processed: 3,
      succeeded: 3, skipped: 0, failed: 1,
      failures: [{ video_id: 9, name: 'broken.mkv', error: 'ffmpeg 抽帧失败' }]
    });
    await wrapper.findAll('.status-cancel').at(-1).trigger('click');
    await flushPromises();
    expect(api.CancelFrameHashBackfill).toHaveBeenCalledOnce();
    expect(wrapper.vm.frameHash.cancelled).toBe(true);
    expect(wrapper.text()).toContain('broken.mkv：ffmpeg 抽帧失败');
    wrapper.unmount();
    expect(handlers.has('frame-hash-backfill-state')).toBe(false);
  });

  it('帧哈希的自动那一轮在门口排队时显示等待空闲与立即运行', async () => {
    api.GetIdleSchedulerStatus.mockResolvedValue({
      enabled: true,
      waiting: [{ task_key: 'frame_hash', reason: 'user_active', since: '' }],
      bypass_tasks: []
    });
    const wrapper = await mountBars();

    expect(wrapper.get('[data-test="frame-hash-waiting-idle"]').text()).toBe('等待空闲（你正在用电脑）');
    expect(wrapper.find('.status-cancel').exists()).toBe(false);

    await wrapper.get('[data-test="frame-hash-run-now"]').trigger('click');
    await flushPromises();
    expect(api.RunGatedTaskNow).toHaveBeenCalledWith('frame_hash');
    wrapper.unmount();
  });

  it('显式启动的帧哈希回填不显示等待空闲', async () => {
    const wrapper = await mountBars();
    api.StartFrameHashBackfill.mockResolvedValueOnce({ running: true, preparing: true, total: 0 });

    await wrapper.vm.startFrameHashBackfill();
    await wrapper.vm.$nextTick();

    expect(api.StartFrameHashBackfill).toHaveBeenCalledOnce();
    expect(wrapper.vm.frameHash.running).toBe(true);
    expect(wrapper.find('[data-test="frame-hash-waiting-idle"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="frame-hash-run-now"]').exists()).toBe(false);
    wrapper.unmount();
  });

  // 空闲门（AC-20）：自动任务还没开始就在门口排队时，状态条要说明原因并给出路。
  it('自动任务在门口排队时显示等待空闲与立即运行', async () => {
    api.GetIdleSchedulerStatus.mockResolvedValue({
      enabled: true,
      waiting: [{ task_key: 'phash', reason: 'user_active', since: '' }],
      bypass_tasks: []
    });
    const wrapper = await mountBars();

    expect(wrapper.get('[data-test="phash-waiting-idle"]').text()).toBe('等待空闲（你正在用电脑）');
    // 还没开始跑：不该冒出"取消"按钮。
    expect(wrapper.find('.status-cancel').exists()).toBe(false);

    await wrapper.get('[data-test="phash-run-now"]').trigger('click');
    await flushPromises();
    expect(api.RunGatedTaskNow).toHaveBeenCalledWith('phash');
    wrapper.unmount();
  });

  // 「立即运行」恰好落在任务刚被放行之后：那不是失败，不该弹红条。
  it('任务刚好已被放行时静默刷新，不弹错误', async () => {
    api.GetIdleSchedulerStatus.mockResolvedValue({
      enabled: true,
      waiting: [{ task_key: 'phash', reason: 'user_active', since: '' }],
      bypass_tasks: []
    });
    api.RunGatedTaskNow.mockRejectedValue(new Error('idle_gate_task_not_waiting: 该后台任务当前没有在等待空闲: phash'));
    const wrapper = await mountBars();

    await wrapper.get('[data-test="phash-run-now"]').trigger('click');
    await flushPromises();
    expect(feedback.notifyError).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  // 其他失败照旧要说出来。
  it('立即运行真的失败时仍然报错', async () => {
    api.GetIdleSchedulerStatus.mockResolvedValue({
      enabled: true,
      waiting: [{ task_key: 'phash', reason: 'user_active', since: '' }],
      bypass_tasks: []
    });
    api.RunGatedTaskNow.mockRejectedValue(new Error('未知的后台任务标识: phash'));
    const wrapper = await mountBars();

    await wrapper.get('[data-test="phash-run-now"]').trigger('click');
    await flushPromises();
    expect(feedback.notifyError).toHaveBeenCalled();
    wrapper.unmount();
  });

  // AC-21：跑到一半用户回来了，状态条从任务自己的 gate 字段读等待原因。
  it('跑到一半被拦住时按任务状态里的 gate 显示原因', async () => {
    const handlers = new Map();
    window.runtime = { EventsOn: vi.fn((name, handler) => { handlers.set(name, handler); return () => handlers.delete(name); }) };
    const wrapper = await mountBars();

    handlers.get('technical-backfill-state')({
      running: true, preparing: false, total: 5, processed: 2,
      gate: { waiting_idle: true, reason: 'on_battery' }
    });
    await wrapper.vm.$nextTick();
    expect(wrapper.get('[data-test="technical-waiting-idle"]').text()).toBe('等待空闲（当前使用电池）');

    api.RunGatedTaskNow.mockResolvedValueOnce(null);
    await wrapper.get('[data-test="technical-run-now"]').trigger('click');
    await flushPromises();
    expect(api.RunGatedTaskNow).toHaveBeenCalledWith('technical');

    // 放行之后等待提示消失。
    handlers.get('technical-backfill-state')({
      running: true, preparing: false, total: 5, processed: 3,
      gate: { waiting_idle: false, reason: '' }
    });
    await wrapper.vm.$nextTick();
    expect(wrapper.find('[data-test="technical-waiting-idle"]').exists()).toBe(false);
    wrapper.unmount();
  });

  // 用户显式启动的那一轮没有 gate.waiting_idle，也不在门的等待清单里：
  // 状态条上不该出现任何"等待空闲"的字样。
  it('显式启动的任务不显示等待空闲', async () => {
    const wrapper = await mountBars();
    wrapper.vm.perceptualHash = { ...wrapper.vm.perceptualHash, running: true, processed: 1, total: 4, gate: { waiting_idle: false, reason: '' } };
    await wrapper.vm.$nextTick();
    expect(wrapper.find('[data-test="phash-waiting-idle"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="phash-run-now"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('本地资料补全跑完有成功项时请片库页重载列表', async () => {
    const handlers = new Map();
    window.runtime = { EventsOn: vi.fn((name, handler) => { handlers.set(name, handler); return () => handlers.delete(name); }) };
    const wrapper = await mountBars();

    handlers.get('local-metadata-backfill')({ completed: true, succeeded: 0 });
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted('library-changed')).toBeUndefined();

    handlers.get('local-metadata-backfill')({ completed: true, succeeded: 2 });
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted('library-changed')).toHaveLength(1);
    wrapper.unmount();
  });
});
