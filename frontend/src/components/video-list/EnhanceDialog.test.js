// 断言原在 src/components/VideoListCleanupReview.test.js 的「超分弹窗」一节，
// P-002 把弹窗抽成独立组件后原样搬到这里，只换了挂载对象。
import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => new Proxy({}, {
  get(target, prop) {
    if (typeof prop === 'symbol' || prop === 'then') return undefined;
    if (!target[prop]) target[prop] = vi.fn(() => Promise.resolve(null));
    return target[prop];
  }
}));

vi.mock('../../../wailsjs/go/main/App', () => api);

// 「放弃保留的进度」先走应用内确认框：默认答「确定」，需要「取消」的用例单独覆盖。
const feedback = vi.hoisted(() => ({
  confirmAction: vi.fn(() => Promise.resolve(true)),
  notifyError: vi.fn(),
  notifySuccess: vi.fn()
}));
vi.mock('../../utils/feedback.js', async (importOriginal) => ({
  ...(await importOriginal()),
  ...feedback
}));

import EnhanceDialog from './EnhanceDialog.vue';

beforeEach(() => {
  vi.clearAllMocks();
  document.body.innerHTML = '';
});

describe('超分弹窗', () => {
  it('能力不可用时也给得出关闭按钮，不把用户困在弹窗里', async () => {
    const wrapper = mount(EnhanceDialog, { attachTo: document.body });
    await flushPromises();
    wrapper.vm.enhanceDialog = { show: true, video: null, profile: 'general', creating: false, error: '' };
    wrapper.vm.enhanceCapability = { available: false, message: '超分运行时未随应用打包' };
    await flushPromises();

    const close = wrapper.find('[data-test="enhance-close"]');
    expect(close.exists()).toBe(true);
    await close.trigger('click');
    expect(wrapper.vm.enhanceDialog.show).toBe(false);

    wrapper.unmount();
  });

  it('Esc 是弹窗的兜底逃生口', async () => {
    const wrapper = mount(EnhanceDialog, { attachTo: document.body });
    await flushPromises();
    wrapper.vm.enhanceDialog = { show: true, video: null, profile: 'general', creating: false, error: '' };
    wrapper.vm.enhanceCapability = { available: false, message: '超分运行时未随应用打包' };
    await flushPromises();

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await flushPromises();

    expect(wrapper.vm.enhanceDialog.show).toBe(false);
    wrapper.unmount();
  });

  it('行菜单与详情抽屉都经 open() 打开同一个弹窗', async () => {
    api.GetEnhancementCapability.mockResolvedValue({ available: true });
    api.GetEnhancementVideoPreflight.mockResolvedValue({ output_basename_general: 'a.enhanced-general-2x.mkv' });
    api.ListEnhancementTasks.mockResolvedValue([]);
    const wrapper = mount(EnhanceDialog, { attachTo: document.body });
    await flushPromises();

    await wrapper.vm.open({ id: 7, name: 'a.mkv', path: '/v/a.mkv' });
    await flushPromises();

    expect(wrapper.vm.enhanceDialog.show).toBe(true);
    expect(wrapper.vm.enhanceDialog.video.id).toBe(7);
    expect(api.GetEnhancementCapability).toHaveBeenCalled();
    wrapper.unmount();
  });
});

describe('超分弹窗源文件元信息', () => {
  it('显示源文件大小、时长与分辨率，好估这次要吃多少磁盘', async () => {
    const wrapper = mount(EnhanceDialog, { attachTo: document.body });
    await flushPromises();
    wrapper.vm.enhanceCapability = { available: true };
    await wrapper.vm.open({ id: 7, name: 'a.mkv', path: '/v/a.mkv', size: 3 * 1024 * 1024 * 1024, duration: 125, width: 1920, height: 1080 });
    await flushPromises();
    expect(wrapper.get('[data-test="enhance-source-meta"]').text()).toBe('3.0 GB · 02:05 · 1920×1080');
    wrapper.unmount();
  });
});

describe('超分任务的原因、进度与产物（D-PC24）', () => {
  const task = (id, extra = {}) => ({
    id, video_id: 7, video_name: `片 ${id}.mkv`, status: 'completed', phase: 'publish',
    error_code: '', error_summary: '', total_frames: 0, committed_frames: 0, ...extra
  });

  async function openWith(tasks) {
    api.GetEnhancementCapability.mockResolvedValue({ available: true });
    api.GetEnhancementVideoPreflight.mockResolvedValue({ output_basename_general: 'a.enhanced-general-2x.mkv', required_bytes: 0 });
    api.ListEnhancementTasks.mockResolvedValue(tasks);
    const wrapper = mount(EnhanceDialog, { attachTo: document.body });
    await flushPromises();
    await wrapper.vm.open({ id: 7, name: 'a.mkv', path: '/v/a.mkv' });
    await flushPromises();
    return wrapper;
  }

  it('MEDIA-11 失败原因显示中文与 error_summary，不再露出原始错误码', async () => {
    const wrapper = await openWith([
      task(1, { status: 'failed', error_code: 'decode_failed', error_summary: '第 3 段解码失败' }),
      task(2, { status: 'running', phase: 'enhance', total_frames: 3000, committed_frames: 120 })
    ]);
    const failed = wrapper.get('[data-test="enhance-task-1"]');
    expect(failed.get('[data-test="enhance-task-status"]').text()).toBe('失败：源文件解码失败');
    expect(failed.get('[data-test="enhance-task-detail"]').text()).toContain('第 3 段解码失败');
    expect(failed.text()).not.toContain('decode_failed');
    expect(wrapper.get('[data-test="enhance-task-2"] [data-test="enhance-task-status"]').text()).toBe('处理中（超分 120/3000 帧）');
    wrapper.unmount();
  });

  it('MEDIA-11 默认把原片的标签、人物、作品集与外挂字幕复制到产物，可以取消勾选', async () => {
    const wrapper = await openWith([]);
    const checkbox = wrapper.get('[data-test="enhance-copy-metadata"]');
    expect(checkbox.element.checked).toBe(true);
    const create = () => wrapper.findAll('.modal-actions button').find(button => button.text().includes('创建超分任务'));

    await create().trigger('click');
    await flushPromises();
    expect(api.CreateEnhancementTask).toHaveBeenLastCalledWith({ video_id: 7, profile: 'general', copy_metadata: true });

    await checkbox.setValue(false);
    await create().trigger('click');
    await flushPromises();
    expect(api.CreateEnhancementTask).toHaveBeenLastCalledWith({ video_id: 7, profile: 'general', copy_metadata: false });
    wrapper.unmount();
  });

  it('MEDIA-03 取消与空间不足的任务可以「放弃保留的进度」，其他失败没有这个按钮', async () => {
    const wrapper = await openWith([
      task(1, { status: 'failed', error_code: 'disk_insufficient', error_summary: '剩余空间不足' }),
      task(2, { status: 'cancelled', error_code: 'cancelled', error_summary: '用户取消' }),
      task(3, { status: 'failed', error_code: 'encode_failed' }),
      task(4, { status: 'failed', error_code: 'checkpoint_discarded', error_summary: '空间不足；保留的进度已放弃，重试将从头开始' })
    ]);
    expect(wrapper.find('[data-test="enhance-discard-1"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="enhance-discard-2"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="enhance-discard-3"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="enhance-discard-4"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="enhance-task-1"] [data-test="enhance-task-detail"]').text()).toContain('重试会从断点接着做');
    // 同一视频有保留进度的任务时，新建之前先说清楚会清掉那份进度。
    expect(wrapper.find('[data-test="enhance-retained-hint"]').exists()).toBe(true);

    feedback.confirmAction.mockResolvedValueOnce(false);
    await wrapper.get('[data-test="enhance-discard-1"]').trigger('click');
    await flushPromises();
    expect(api.DiscardEnhancementProgress).not.toHaveBeenCalled();

    api.DiscardEnhancementProgress.mockResolvedValue(task(1, { status: 'failed', error_code: 'checkpoint_discarded' }));
    await wrapper.get('[data-test="enhance-discard-1"]').trigger('click');
    await flushPromises();
    expect(api.DiscardEnhancementProgress).toHaveBeenCalledWith(1);
    expect(feedback.confirmAction.mock.calls.at(-1)[0].message).toContain('从头开始');
    wrapper.unmount();
  });

  it('MEDIA-11 完成的任务可以查看产物所在位置', async () => {
    const wrapper = await openWith([task(5, { status: 'completed', output_video_id: 88 })]);
    await wrapper.get('[data-test="enhance-reveal-5"]').trigger('click');
    await flushPromises();
    expect(api.OpenDirectory).toHaveBeenCalledWith(88);
    wrapper.unmount();
  });

  it('MEDIA-11 未就绪时说明原因与下一步（hint），不只丢一句技术文案', async () => {
    api.GetEnhancementCapability.mockResolvedValue({
      available: false, state: 'not_ready', reason_code: 'models_missing',
      message: 'models dir missing', hint: '超分模型还没下载，可在设置页「视频超分」中下载（约 52 MB）'
    });
    api.ListEnhancementTasks.mockResolvedValue([]);
    const wrapper = mount(EnhanceDialog, { attachTo: document.body });
    await flushPromises();
    await wrapper.vm.open({ id: 7, name: 'a.mkv', path: '/v/a.mkv' });
    await flushPromises();
    const text = wrapper.get('[data-test="enhance-unavailable"]').text();
    expect(text).toContain('设置页「视频超分」');
    expect(text).not.toContain('models dir missing');
    wrapper.unmount();
  });
});
