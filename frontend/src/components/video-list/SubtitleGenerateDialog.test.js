// 字幕生成弹窗：覆盖前说清会先备份、待确认的结果可以放弃、后台任务的失败要让人知道、
// 引擎准备可以取消且「后台继续」后不再弹回、上次中断的任务有出口、批量生成只汇总一次。
import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

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

const api = vi.hoisted(() => new Proxy({}, {
  get(target, prop) {
    if (typeof prop === 'symbol' || prop === 'then') return undefined;
    if (!target[prop]) target[prop] = vi.fn(() => Promise.resolve(null));
    return target[prop];
  }
}));
vi.mock('../../../wailsjs/go/main/App', () => api);

import SubtitleGenerateDialog from './SubtitleGenerateDialog.vue';
import { registerCommands, unregisterCommands } from '../../utils/commandRegistry.js';

const READY_ENGINE = { engine: 'whisperx', display_name: 'WhisperX', supported: true, available: true, needs_prepare: false, source_lang_mode: 'shared' };
const PREPARE_ENGINE = { ...READY_ENGINE, available: false, needs_prepare: true, prepare_hint: '需要先准备 WhisperX 运行时' };
const video = { id: 7, name: '黑客帝国.mkv' };

let handlers;
function emitRuntime(name, payload) {
  for (const handler of handlers[name] || []) handler(payload);
}

async function mountDialog() {
  const wrapper = mount(SubtitleGenerateDialog, { attachTo: document.body });
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.innerHTML = '';
  handlers = {};
  window.runtime = {
    EventsOn: vi.fn((name, handler) => {
      (handlers[name] ||= []).push(handler);
      return () => {};
    })
  };
  api.GetSubtitleQueueState.mockResolvedValue({ active_task: null, queued_tasks: [], total: 0 });
  api.GetInterruptedSubtitleJobs.mockResolvedValue({ count: 0, job_ids: [] });
  api.GetSubtitleEngineStatuses.mockResolvedValue([READY_ENGINE]);
  api.GetSubtitleOverwriteInfo.mockResolvedValue({ exists: false, shared_with: [] });
});

afterEach(() => {
  delete window.runtime;
});

describe('字幕生成：覆盖与待确认', () => {
  it('MEDIA-01 已有同名字幕时确认框写明会先备份，并列出共用这份字幕的其他视频', async () => {
    api.GetSubtitleOverwriteInfo.mockResolvedValue({ exists: true, shared_with: [{ id: 8, name: '黑客帝国.mp4' }] });
    const wrapper = await mountDialog();
    await wrapper.vm.generate(video);
    await flushPromises();

    expect(api.GetSubtitleOverwriteInfo).toHaveBeenCalledWith(7);
    const notice = wrapper.find('[data-test="subtitle-overwrite-notice"]');
    expect(notice.text()).toContain('将覆盖现有字幕（会先备份，可恢复）');
    expect(wrapper.find('[data-test="subtitle-overwrite-shared"]').text()).toContain('黑客帝国.mp4');
    wrapper.unmount();
  });

  it('MEDIA-01 没有字幕、也没有同名视频时不提覆盖', async () => {
    const wrapper = await mountDialog();
    await wrapper.vm.generate(video);
    await flushPromises();
    expect(wrapper.find('[data-test="subtitle-overwrite-notice"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('MEDIA-01 校验未通过时说清原字幕没动；「放弃这次结果」删除临时结果', async () => {
    api.GenerateSubtitle.mockResolvedValue({ status: 'validation_failed', force_eligible: true, message: '检测到疑似幻觉', engine: 'whisperx', video_id: 7 });
    const wrapper = await mountDialog();
    await wrapper.vm.generate(video);
    await flushPromises();
    await wrapper.find('[data-test="subtitle-confirm"]').trigger('click');
    await flushPromises();

    expect(wrapper.find('[data-test="subtitle-dialog-msg"]').text()).toContain('原字幕没有改动');
    expect(wrapper.find('[data-test="subtitle-confirm"]').text()).toBe('强制生成');
    await wrapper.find('[data-test="subtitle-pending-discard"]').trigger('click');
    await flushPromises();

    expect(api.DiscardPendingSubtitle).toHaveBeenCalledWith(7);
    expect(wrapper.find('[data-test="subtitle-generate-dialog"]').exists()).toBe(false);
    expect(feedback.notify).toHaveBeenCalledWith('已放弃这次的字幕结果，原字幕没有改动。');
    wrapper.unmount();
  });

  it('MEDIA-04 写回失败（subtitle_replace_failed）单独说明，按钮是「重试写回」，重试走强制生成复用临时结果', async () => {
    api.GenerateSubtitle.mockResolvedValue({
      status: 'validation_failed', force_eligible: true, error_code: 'subtitle_replace_failed', pending_retained: true,
      message: '写入字幕文件失败：没有权限', engine: 'whisperx', source_lang: 'auto', video_id: 7
    });
    api.ForceGenerateSubtitle.mockResolvedValue({ status: 'success', path: '/Volumes/片库/黑客帝国.srt', video_id: 7 });
    const wrapper = await mountDialog();
    await wrapper.vm.generate(video);
    await flushPromises();
    await wrapper.find('[data-test="subtitle-confirm"]').trigger('click');
    await flushPromises();

    expect(wrapper.text()).toContain('字幕写回失败');
    expect(wrapper.find('[data-test="subtitle-dialog-msg"]').text()).toContain('临时结果已保留');
    expect(wrapper.find('[data-test="subtitle-confirm"]').text()).toBe('重试写回');
    await wrapper.find('[data-test="subtitle-confirm"]').trigger('click');
    await flushPromises();

    expect(api.ForceGenerateSubtitle).toHaveBeenCalledWith({ video_id: 7, engine: 'whisperx', source_lang: 'auto' });
    // 成功文案只说文件名，不带绝对路径（G-3）。
    const message = wrapper.find('[data-test="subtitle-dialog-msg"]').text();
    expect(message).toContain('黑客帝国.srt');
    expect(message).not.toContain('/Volumes/');
    wrapper.unmount();
  });

  it('MEDIA-04 pending_retained=false 时不提「放弃」与「稍后处理」，说明强制生成会重新识别', async () => {
    api.GenerateSubtitle.mockResolvedValue({ status: 'validation_failed', force_eligible: true, pending_retained: false, message: '疑似幻觉', engine: 'whisperx', video_id: 7 });
    const wrapper = await mountDialog();
    await wrapper.vm.generate(video);
    await flushPromises();
    await wrapper.find('[data-test="subtitle-confirm"]').trigger('click');
    await flushPromises();

    expect(wrapper.find('[data-test="subtitle-dialog-msg"]').text()).toContain('这次的结果没有保留');
    expect(wrapper.find('[data-test="subtitle-pending-discard"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="subtitle-pending-later"]').exists()).toBe(false);
    await wrapper.find('[data-test="subtitle-confirm-cancel"]').trigger('click');
    expect(wrapper.find('[data-test="subtitle-generate-dialog"]').exists()).toBe(false);
    expect(api.DiscardPendingSubtitle).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it('MEDIA-01 「稍后处理」只关窗，临时结果留给任务中心', async () => {
    api.GenerateSubtitle.mockResolvedValue({ status: 'validation_failed', force_eligible: true, message: '疑似幻觉', engine: 'whisperx', video_id: 7 });
    const wrapper = await mountDialog();
    await wrapper.vm.generate(video);
    await flushPromises();
    await wrapper.find('[data-test="subtitle-confirm"]').trigger('click');
    await flushPromises();
    await wrapper.find('[data-test="subtitle-pending-later"]').trigger('click');
    await flushPromises();

    expect(api.DiscardPendingSubtitle).not.toHaveBeenCalled();
    expect(wrapper.find('[data-test="subtitle-generate-dialog"]').exists()).toBe(false);
    expect(feedback.notify.mock.calls.at(-1)[0]).toContain('任务中心');
    wrapper.unmount();
  });
});

describe('字幕生成：后台任务的结果（MEDIA-04）', () => {
  it('MEDIA-04 后台任务失败发提示并指向任务中心；写回失败按 error_code 换一套说法', async () => {
    const wrapper = await mountDialog();
    emitRuntime('subtitle-queue', { active_task: { task_id: 31, video_id: 9, video_name: '另一部.mp4' }, queued_tasks: [], total: 1 });
    await flushPromises();

    emitRuntime('subtitle-failed', { job_id: 31, video_id: 9, message: '音频提取失败' });
    expect(feedback.notifyError).toHaveBeenLastCalledWith('「另一部.mp4」的字幕生成失败：音频提取失败。可在任务中心查看。');

    emitRuntime('subtitle-failed', { job_id: 31, video_id: 9, message: '写入失败', error_code: 'subtitle_replace_failed' });
    expect(feedback.notifyError.mock.calls.at(-1)[0]).toContain('写回同名 .srt 失败，原字幕没有改动');
    expect(feedback.notifyError.mock.calls.at(-1)[0]).toContain('重试写回');
    wrapper.unmount();
  });

  it('MEDIA-04 弹窗正显示着的那个任务失败时不重复提示', async () => {
    let rejectGenerate;
    api.GenerateSubtitle.mockReturnValue(new Promise((_resolve, reject) => { rejectGenerate = reject; }));
    const wrapper = await mountDialog();
    await wrapper.vm.generate(video);
    await flushPromises();
    await wrapper.find('[data-test="subtitle-confirm"]').trigger('click');
    emitRuntime('subtitle-progress', { action: 'generate', taskID: 40, videoID: 7, phase: 'transcribing', percent: 30 });
    await flushPromises();

    emitRuntime('subtitle-failed', { job_id: 40, video_id: 7, message: '转写失败' });
    expect(feedback.notifyError).not.toHaveBeenCalled();
    rejectGenerate(new Error('转写失败'));
    await flushPromises();
    expect(wrapper.text()).toContain('生成字幕失败');
    wrapper.unmount();
  });

  it('MEDIA-04 「后台继续」之后校验未通过：弹窗不回来，但提示结果待确认', async () => {
    let resolveGenerate;
    api.GenerateSubtitle.mockReturnValue(new Promise(resolve => { resolveGenerate = resolve; }));
    const wrapper = await mountDialog();
    await wrapper.vm.generate(video);
    await flushPromises();
    await wrapper.find('[data-test="subtitle-confirm"]').trigger('click');
    emitRuntime('subtitle-progress', { action: 'generate', taskID: 41, videoID: 7, phase: 'transcribing', percent: 30 });
    await flushPromises();
    wrapper.vm.minimizeSubtitleProgress();
    await flushPromises();

    resolveGenerate({ status: 'validation_failed', force_eligible: true, message: '疑似幻觉', engine: 'whisperx', video_id: 7 });
    await flushPromises();
    expect(wrapper.find('[data-test="subtitle-generate-dialog"]').exists()).toBe(false);
    expect(feedback.notify.mock.calls.at(-1)[0]).toContain('「黑客帝国.mkv」的字幕没有通过质量校验');
    wrapper.unmount();
  });
});

describe('字幕引擎准备（MEDIA-13）', () => {
  it('MEDIA-13 准备中可取消；取消不算失败', async () => {
    api.GetSubtitleEngineStatuses.mockResolvedValue([PREPARE_ENGINE]);
    let rejectPrepare;
    api.PrepareSubtitleEngine.mockReturnValue(new Promise((_resolve, reject) => { rejectPrepare = reject; }));
    const wrapper = await mountDialog();
    await wrapper.vm.generate(video);
    await flushPromises();
    expect(wrapper.find('[data-test="subtitle-confirm"]').text()).toBe('准备组件');
    await wrapper.find('[data-test="subtitle-confirm"]').trigger('click');
    await flushPromises();

    await wrapper.find('[data-test="subtitle-prepare-cancel"]').trigger('click');
    await flushPromises();
    expect(api.CancelSubtitleEnginePreparation).toHaveBeenCalledTimes(1);
    rejectPrepare(new Error('已取消字幕引擎准备'));
    await flushPromises();

    expect(wrapper.text()).toContain('已取消准备组件');
    expect(wrapper.text()).not.toContain('组件准备失败');
    wrapper.unmount();
  });

  it('MEDIA-13 「后台继续准备」后进度事件不再弹回；之后失败用提示条告知', async () => {
    api.GetSubtitleEngineStatuses.mockResolvedValue([PREPARE_ENGINE]);
    let rejectPrepare;
    api.PrepareSubtitleEngine.mockReturnValue(new Promise((_resolve, reject) => { rejectPrepare = reject; }));
    const wrapper = await mountDialog();
    await wrapper.vm.generate(video);
    await flushPromises();
    await wrapper.find('[data-test="subtitle-confirm"]').trigger('click');
    await flushPromises();
    await wrapper.find('[data-test="subtitle-prepare-minimize"]').trigger('click');
    await flushPromises();

    emitRuntime('subtitle-progress', { action: 'prepare', phase: 'downloading-model', percent: 50, message: 'downloading' });
    await flushPromises();
    expect(wrapper.find('[data-test="subtitle-generate-dialog"]').exists()).toBe(false);

    rejectPrepare(new Error('pip 安装失败'));
    await flushPromises();
    expect(feedback.notifyError).toHaveBeenCalledWith('字幕组件准备失败：pip 安装失败');
    wrapper.unmount();
  });

  it('MEDIA-13 设置页发起的准备不在片库页弹进度窗', async () => {
    const wrapper = await mountDialog();
    emitRuntime('subtitle-progress', { action: 'prepare', phase: 'checking', percent: 0, message: '准备运行时...' });
    await flushPromises();
    expect(wrapper.find('[data-test="subtitle-generate-dialog"]').exists()).toBe(false);
    wrapper.unmount();
  });
});

describe('上次中断的字幕任务（MEDIA-10 / D-PC20）', () => {
  it('MEDIA-04 启动时有中断任务就提示；「全部重新排队」报告没能排上的条数', async () => {
    api.GetInterruptedSubtitleJobs.mockResolvedValueOnce({ count: 3, job_ids: [5, 6, 7] }).mockResolvedValueOnce({ count: 0, job_ids: [] });
    api.RequeueInterruptedSubtitleJobs.mockResolvedValue(2);
    const wrapper = await mountDialog();

    const bar = wrapper.find('[data-test="subtitle-interrupted-bar"]');
    expect(bar.text()).toContain('3 个字幕任务没有完成');
    await wrapper.find('[data-test="subtitle-interrupted-requeue"]').trigger('click');
    await flushPromises();

    expect(api.RequeueInterruptedSubtitleJobs).toHaveBeenCalledTimes(1);
    expect(feedback.notifyError).toHaveBeenCalledWith('已重新排队 2 个字幕任务；1 个没能重新排队（例如视频已删除），原因见任务中心。');
    expect(wrapper.find('[data-test="subtitle-interrupted-bar"]').exists()).toBe(false);
    // 重新排队的任务在后台跑，不抢占生成弹窗。
    emitRuntime('subtitle-progress', { action: 'generate', taskID: 5, videoID: 70, phase: 'transcribing', percent: 5 });
    await flushPromises();
    expect(wrapper.find('[data-test="subtitle-generate-dialog"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('MEDIA-04 「忽略」后提示条消失；「在任务中心查看」打开任务中心', async () => {
    api.GetInterruptedSubtitleJobs.mockResolvedValue({ count: 1, job_ids: [5] });
    const openTaskCenter = vi.fn();
    registerCommands('test-task-center', [{ id: 'action:task-center', group: 'action', label: '任务中心', run: openTaskCenter }]);
    const wrapper = await mountDialog();

    await wrapper.find('[data-test="subtitle-interrupted-task-center"]').trigger('click');
    expect(openTaskCenter).toHaveBeenCalledTimes(1);
    await wrapper.find('[data-test="subtitle-interrupted-dismiss"]').trigger('click');
    await flushPromises();
    expect(api.DismissInterruptedSubtitleJobs).toHaveBeenCalledTimes(1);
    expect(wrapper.find('[data-test="subtitle-interrupted-bar"]').exists()).toBe(false);
    unregisterCommands('test-task-center');
    wrapper.unmount();
  });
});

describe('批量生成字幕（MEDIA-14）', () => {
  it('MEDIA-14 确认一次后逐个入队；批量任务不抢弹窗、失败不逐条弹，全部结束后汇总', async () => {
    const settle = {};
    api.GenerateSubtitle.mockImplementation(request => new Promise((resolve, reject) => { settle[request.video_id] = { resolve, reject }; }));
    const wrapper = await mountDialog();
    await wrapper.vm.generateBatch([{ id: 1, name: 'a.mp4' }, { id: 2, name: 'b.mp4' }, { id: 1, name: 'a.mp4' }]);
    await flushPromises();

    expect(wrapper.find('[data-test="subtitle-overwrite-notice"]').text()).toContain('会先备份');
    expect(wrapper.find('[data-test="subtitle-confirm"]').text()).toBe('加入队列（2）');
    await wrapper.find('[data-test="subtitle-confirm"]').trigger('click');
    await flushPromises();

    expect(api.GenerateSubtitle).toHaveBeenCalledTimes(2);
    expect(api.GenerateSubtitle).toHaveBeenCalledWith({ video_id: 1, engine: 'whisperx', source_lang: 'auto' });
    expect(wrapper.emitted('generating-change').at(-1)[0]).toEqual(expect.arrayContaining([1, 2]));
    expect(wrapper.find('[data-test="subtitle-generate-dialog"]').exists()).toBe(false);

    emitRuntime('subtitle-progress', { action: 'generate', taskID: 50, videoID: 1, phase: 'transcribing', percent: 10 });
    emitRuntime('subtitle-failed', { job_id: 51, video_id: 2, message: '音频提取失败' });
    await flushPromises();
    expect(wrapper.find('[data-test="subtitle-generate-dialog"]').exists()).toBe(false);
    expect(feedback.notifyError).not.toHaveBeenCalled();

    settle[1].resolve({ status: 'success', video_id: 1 });
    settle[2].reject(new Error('音频提取失败'));
    await flushPromises();
    expect(feedback.notifyError).toHaveBeenCalledWith('批量生成字幕结束：成功 1 个，失败 1 个。待确认与失败的任务可在任务中心处理。');
    wrapper.unmount();
  });

  it('MEDIA-14 选中的都已在队列里时不重复发起', async () => {
    const wrapper = await mountDialog();
    emitRuntime('subtitle-queue', { active_task: { task_id: 3, video_id: 1, video_name: 'a.mp4' }, queued_tasks: [], total: 1 });
    await flushPromises();
    await wrapper.vm.generateBatch([{ id: 1, name: 'a.mp4' }]);
    await flushPromises();
    expect(feedback.notify).toHaveBeenCalledWith('选中的视频都已在字幕队列里。');
    expect(api.GenerateSubtitle).not.toHaveBeenCalled();
    wrapper.unmount();
  });
});
