// 图片侧三个任务面板的空闲门交互（D-030..D-032）：与片库页状态条同一套说法与按钮。
import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => new Proxy({}, {
  get(target, prop) {
    if (typeof prop === 'symbol' || prop === 'then') return undefined;
    if (!target[prop]) target[prop] = vi.fn(() => Promise.resolve(null));
    return target[prop];
  }
}));
vi.mock('../../wailsjs/go/main/App', () => api);

import PhotoAITaskPanel from './PhotoAITaskPanel.vue';

const idleStatus = (waiting = []) => ({ enabled: true, probed: true, waiting, bypass_tasks: [] });

beforeEach(() => {
  vi.clearAllMocks();
  delete window.runtime;
  api.GetImageAITaggingStatus.mockResolvedValue({ running: false, completed: false, failures: [] });
  api.GetImageEXIFBackfillStatus.mockResolvedValue({ running: false, completed: false, failures: [] });
  api.GetImageSemanticIndexStatus.mockResolvedValue({ available: true, running: false, failures: [] });
  api.GetIdleSchedulerStatus.mockResolvedValue(idleStatus());
  api.RunGatedTaskNow.mockResolvedValue(null);
});

async function mountPanel() {
  const wrapper = mount(PhotoAITaskPanel);
  await flushPromises();
  return wrapper;
}

describe('图片任务面板的空闲门提示', () => {
  it('没有任务在等空闲时不显示等待提示与立即运行', async () => {
    const wrapper = await mountPanel();
    expect(wrapper.find('[data-test="image-exif-backfill-waiting-idle"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="image-ai-tagging-run-now"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('自动 EXIF 在门口排队时显示原因并可立即运行', async () => {
    api.GetIdleSchedulerStatus.mockResolvedValue(idleStatus([{ task_key: 'exif', reason: 'user_active', since: '' }]));
    const wrapper = await mountPanel();

    expect(wrapper.get('[data-test="image-exif-backfill-waiting-idle"]').text()).toBe('等待空闲（你正在用电脑）');
    await wrapper.get('[data-test="image-exif-backfill-run-now"]').trigger('click');
    await flushPromises();
    expect(api.RunGatedTaskNow).toHaveBeenCalledWith('exif');
    wrapper.unmount();
  });

  it('图片打标跑到一半被拦住时按任务状态里的 gate 显示原因', async () => {
    api.GetImageAITaggingStatus.mockResolvedValue({
      running: true, processed: 2, total: 9, failures: [],
      gate: { waiting_idle: true, reason: 'outside_window' }
    });
    const wrapper = await mountPanel();

    expect(wrapper.get('[data-test="image-ai-tagging-waiting-idle"]').text()).toBe('等待空闲（不在允许的时间段内）');
    await wrapper.get('[data-test="image-ai-tagging-run-now"]').trigger('click');
    await flushPromises();
    expect(api.RunGatedTaskNow).toHaveBeenCalledWith('image_ai_tagging');
    wrapper.unmount();
  });

  it('立即运行失败时把原因写进对应任务的错误位，不静默', async () => {
    api.GetIdleSchedulerStatus.mockResolvedValue(idleStatus([{ task_key: 'exif', reason: 'on_battery', since: '' }]));
    api.RunGatedTaskNow.mockRejectedValue(new Error('该后台任务当前没有在等待空闲'));
    const wrapper = await mountPanel();

    await wrapper.get('[data-test="image-exif-backfill-run-now"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="image-exif-backfill-error"]').text()).toContain('忽略空闲立即运行失败');
    wrapper.unmount();
  });

  it('任务刚好已被放行时静默处理，不在面板上留错误', async () => {
    api.GetIdleSchedulerStatus.mockResolvedValue(idleStatus([{ task_key: 'exif', reason: 'user_active', since: '' }]));
    api.RunGatedTaskNow.mockRejectedValue(new Error('idle_gate_task_not_waiting: 该后台任务当前没有在等待空闲: exif'));
    const wrapper = await mountPanel();

    await wrapper.get('[data-test="image-exif-backfill-run-now"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-test="image-exif-backfill-error"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('idle-scheduler-state 事件到达时刷新等待提示', async () => {
    const handlers = {};
    window.runtime = { EventsOn: (name, handler) => { handlers[name] = handler; return () => {}; } };
    const wrapper = await mountPanel();
    expect(wrapper.find('[data-test="image-ai-tagging-waiting-idle"]').exists()).toBe(false);

    handlers['idle-scheduler-state'](idleStatus([{ task_key: 'image_ai_tagging', reason: 'probe_failed', since: '' }]));
    await wrapper.vm.$nextTick();
    expect(wrapper.get('[data-test="image-ai-tagging-waiting-idle"]').text()).toBe('等待空闲（空闲状态探测失败）');
    wrapper.unmount();
  });
});
