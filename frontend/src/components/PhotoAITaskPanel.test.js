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

describe('IMG-06 图片语义索引的说明', () => {
  it('如实写索引文本只含文件名与标签，不再提已移除的 AI 描述', async () => {
    const wrapper = await mountPanel();
    const help = wrapper.get('.image-semantic-index-controls .help-text').text();
    expect(help).toContain('文件名 + 标签');
    expect(help).not.toContain('AI 描述');
    wrapper.unmount();
  });
});

describe('任务进度用统一的进度条（D-UD-11）', () => {
  it('运行中按已处理 / 总数显示比例；没在跑也没跑过时不显示', async () => {
    api.GetImageAITaggingStatus.mockResolvedValue({ running: true, processed: 3, total: 12, failures: [] });
    api.GetImageEXIFBackfillStatus.mockResolvedValue({ running: false, completed: false, processed: 0, total: 40, failures: [] });
    const wrapper = await mountPanel();
    const bar = wrapper.get('[data-test="image-ai-tagging-status"] [role="progressbar"]');
    expect(bar.attributes('aria-valuenow')).toBe('25');
    expect(wrapper.find('[data-test="image-exif-backfill-status"] [role="progressbar"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="image-ai-tagging-start"]').classes()).toEqual(expect.arrayContaining(['btn', 'btn--primary']));
    wrapper.unmount();
  });

  it('读取状态前的提示用省略号「…」', async () => {
    api.GetImageAITaggingStatus.mockReturnValue(new Promise(() => {}));
    const wrapper = mount(PhotoAITaskPanel);
    expect(wrapper.get('[data-test="image-ai-tagging-status"]').text()).toContain('正在读取图片打标任务状态…');
    wrapper.unmount();
  });
});

// 失败明细改用共用的 TaskFailureList：不再只露 5 条、能展开；明细比失败数少时照实说。
describe('图片任务的失败明细', () => {
  it('可以展开全部带来的明细，截断时写明只保留了前几条', async () => {
    const failures = Array.from({ length: 12 }, (_, index) => ({ image_id: index + 1, name: `p${index + 1}.jpg`, code: 'ai_timeout' }));
    api.GetImageAITaggingStatus.mockResolvedValue({ running: false, completed: true, failed: 80, failures });
    api.GetImageEXIFBackfillStatus.mockResolvedValue({ running: false, completed: true, failed: 1, failures: [{ image_id: 9, name: '', error: 'bad exif' }] });
    const wrapper = await mountPanel();

    const tagging = wrapper.get('[data-test="image-ai-tagging-failures"]');
    expect(tagging.findAll('li')).toHaveLength(3);
    expect(tagging.text()).toContain('p1.jpg：ai_timeout');
    expect(tagging.get('[data-test="task-failure-capped"]').text()).toBe('共 80 条失败，只保留了前 12 条明细。');
    await tagging.get('[data-test="task-failure-toggle"]').trigger('click');
    expect(wrapper.get('[data-test="image-ai-tagging-failures"]').findAll('li')).toHaveLength(12);

    const exif = wrapper.get('[data-test="image-exif-backfill-failures"]');
    expect(exif.text()).toContain('图片 #9：bad exif');
    expect(exif.find('[data-test="task-failure-capped"]').exists()).toBe(false);
    wrapper.unmount();
  });
});
