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

import VideoWorkbenchPage from './VideoWorkbenchPage.vue';
import { feedbackState, resetFeedback, resolveConfirm } from '../utils/feedback.js';

const tracks = () => ({ audio: [], subtitle: [] });

function mergeProject(overrides = {}) {
  return {
    id: 1, kind: 'merge', title: '合并', status: 'draft', revision: 3, mode: 'precise',
    recipe: { v: 1, merge: { sources: [{ video_id: 11 }, { video_id: 12 }, { video_id: 13 }], spec_source_video_id: 0 }, tracks: tracks() },
    analysis: null, acknowledged_warnings: [], error_code: '', error_message: '', progress: 0, items: [], ...overrides
  };
}

function trimProject(overrides = {}) {
  return {
    ...mergeProject(), id: 2, kind: 'trim_intro', title: '去片头',
    recipe: { v: 1, trim_intro: { items: [
      { video_id: 21, remove_start_ms: 0, remove_end_ms: 0, origin: 'manual', detect_status: '', confidence: 0, confirmed: false },
      { video_id: 22, remove_start_ms: 0, remove_end_ms: 0, origin: 'manual', detect_status: '', confidence: 0, confirmed: false }
    ], analysis_window_ms: 600000 }, tracks: tracks() },
    ...overrides
  };
}

function preflightFor(project, overrides = {}) {
  return {
    project_id: project.id, revision: project.revision, kind: project.kind, mode: project.mode, sources: [], spec_options: [],
    outputs: [], conflicts: [], fast: { available: true, reasons: [], cut_points: [] }, warnings: [], errors: [], ready: true, ...overrides
  };
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

let handlers;
const wrappers = [];
async function mountPage(project) {
  api.GetEditProject.mockResolvedValue(project);
  api.PreflightEditProject.mockResolvedValue(preflightFor(project));
  const wrapper = mount(VideoWorkbenchPage, { props: { openRequest: { projectID: project.id, token: 1 } } });
  wrappers.push(wrapper);
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  vi.clearAllMocks();
  resetFeedback();
  handlers = {};
  window.runtime = { EventsOn: (event, handler) => { handlers[event] = handler; return () => { delete handlers[event]; }; } };
  api.ListEditProjects.mockResolvedValue([]);
  api.GetVideoEditStatus.mockResolvedValue({ analyzing_project_id: 0, analysis_progress: 0 });
  api.GetVideosByIDs.mockImplementation(async ids => ids.map(id => ({ id, name: `v${id}.mp4`, duration: 60 })));
});

afterEach(() => {
  while (wrappers.length) wrappers.pop().unmount();
  delete window.runtime;
});

function toastText() {
  return feedbackState.toasts.map(toast => toast.message).join('\n');
}

describe('视频工作台页（视频编辑合同「界面」）', () => {
  it('打开请求选中项目、读取来源名并自动预检', async () => {
    const wrapper = await mountPage(mergeProject());
    expect(api.GetEditProject).toHaveBeenCalledWith(1);
    expect(api.PreflightEditProject).toHaveBeenCalledWith(1);
    expect(api.GetVideosByIDs).toHaveBeenCalledWith([11, 12, 13]);
    expect(wrapper.get('[data-test="merge-source-11"]').text()).toContain('v11.mp4');
  });

  it('改配方带 expected_revision 保存；冲突时重读服务器版本并丢弃本地改动', async () => {
    const project = mergeProject();
    const wrapper = await mountPage(project);
    api.UpdateEditRecipe.mockRejectedValueOnce(new Error('edit_project_conflict: 项目已被修改（当前 revision 4），请刷新后重试'));
    const fresh = mergeProject({ revision: 4 });
    api.GetEditProject.mockResolvedValue(fresh);
    await wrapper.get('[data-test="merge-up-13"]').trigger('click');
    await flushPromises();
    const request = api.UpdateEditRecipe.mock.calls[0][0];
    expect(request).toMatchObject({ project_id: 1, expected_revision: 3, mode: '' });
    expect(request.recipe.merge.sources.map(source => source.video_id)).toEqual([11, 13, 12]);
    expect(api.GetEditProject).toHaveBeenLastCalledWith(1);
    expect(toastText()).toContain('已重新载入');
    const order = wrapper.findAll('[data-test^="merge-source-"]').map(node => node.attributes('data-test'));
    expect(order).toEqual(['merge-source-11', 'merge-source-12', 'merge-source-13']);
    expect(wrapper.text()).toContain('版本 4');
  });

  it('连续两次改动按顺序保存，第二次带第一次保存后的 revision', async () => {
    const wrapper = await mountPage(mergeProject());
    const first = deferred();
    api.UpdateEditRecipe.mockReturnValueOnce(first.promise).mockImplementationOnce(async request => mergeProject({ revision: 5, recipe: request.recipe }));
    await wrapper.get('[data-test="merge-up-13"]').trigger('click');
    await wrapper.get('[data-test="merge-up-12"]').trigger('click');
    expect(api.UpdateEditRecipe).toHaveBeenCalledTimes(1);
    first.resolve(mergeProject({ revision: 4, recipe: api.UpdateEditRecipe.mock.calls[0][0].recipe }));
    await flushPromises();
    expect(api.UpdateEditRecipe).toHaveBeenCalledTimes(2);
    expect(api.UpdateEditRecipe.mock.calls[1][0].expected_revision).toBe(4);
    expect(wrapper.text()).toContain('版本 5');
  });

  it('晚到的旧读取结果被丢弃（请求代次）', async () => {
    const wrapper = await mountPage(mergeProject());
    const slow = deferred();
    const fast = deferred();
    api.GetEditProject.mockReturnValueOnce(slow.promise).mockReturnValueOnce(fast.promise);
    handlers['video-edit-state']({ project_id: 1, status: 'queued' });
    handlers['video-edit-state']({ project_id: 1, status: 'running' });
    fast.resolve(mergeProject({ status: 'running', revision: 3 }));
    await flushPromises();
    slow.resolve(mergeProject({ status: 'queued', revision: 3 }));
    await flushPromises();
    expect(wrapper.vm.project.status).toBe('running');
    expect(wrapper.text()).toContain('导出中');
  });

  it('自动识别片头：带窗口开始分析，事件推进度，取消后重读', async () => {
    const project = trimProject();
    const wrapper = await mountPage(project);
    api.AnalyzeEditProject.mockResolvedValue(trimProject({ status: 'analyzing' }));
    await wrapper.get('[data-test="trim-window"]').setValue(5);
    await wrapper.get('[data-test="trim-analyze"]').trigger('click');
    await flushPromises();
    expect(api.AnalyzeEditProject).toHaveBeenCalledWith({ project_id: 2, expected_revision: 3, window_ms: 300000 });
    handlers['video-edit-state']({ project_id: 2, status: 'analyzing', progress: 0.5 });
    await flushPromises();
    expect(wrapper.get('[data-test="trim-analysis-progress"]').attributes('aria-valuenow')).toBe('50');
    expect(api.GetEditProject).toHaveBeenCalledTimes(1);
    api.GetEditProject.mockResolvedValue(trimProject({ revision: 3 }));
    await wrapper.get('[data-test="trim-cancel-analysis"]').trigger('click');
    await flushPromises();
    expect(api.CancelEditAnalysis).toHaveBeenCalledWith(2);
    expect(wrapper.find('[data-test="trim-analysis-progress"]').exists()).toBe(false);
  });
});

describe('预检与排队', () => {
  const warnings = [
    { code: 'scale', key: 'scale:12:1920x1080', message: '第二段将放大到 1920×1080', video_id: 12, seq: 1 },
    { code: 'fps', key: 'fps:12:25/1', message: '帧率将统一为 25', video_id: 12, seq: 1 }
  ];

  it('警告逐条勾选后才放行排队，排队请求带上勾选的 key', async () => {
    const project = mergeProject();
    api.GetEditProject.mockResolvedValue(project);
    api.PreflightEditProject.mockResolvedValue(preflightFor(project, { warnings }));
    const wrapper = mount(VideoWorkbenchPage, { props: { openRequest: { projectID: 1, token: 1 } } });
    wrappers.push(wrapper);
    await flushPromises();
    const queue = wrapper.get('[data-test="edit-queue"]');
    expect(queue.attributes('disabled')).toBeDefined();
    await wrapper.get('[data-test="edit-ack-scale:12:1920x1080"]').setValue(true);
    expect(queue.attributes('disabled')).toBeDefined();
    await wrapper.get('[data-test="edit-ack-fps:12:25/1"]').setValue(true);
    expect(queue.attributes('disabled')).toBeUndefined();
    api.QueueEditProject.mockResolvedValue({ queued: true, project: mergeProject({ status: 'queued' }), preflight: null, unacknowledged: [] });
    await queue.trigger('click');
    await flushPromises();
    expect(api.QueueEditProject).toHaveBeenCalledWith({
      project_id: 1, expected_revision: 3, acknowledged_warnings: ['scale:12:1920x1080', 'fps:12:25/1']
    });
    expect(wrapper.text()).toContain('排队中');
    expect(wrapper.find('[data-test="edit-queue"]').exists()).toBe(false);
  });

  it('预检有错误时不放行；配方改过后预检过期也不放行', async () => {
    const project = mergeProject();
    api.GetEditProject.mockResolvedValue(project);
    api.PreflightEditProject.mockResolvedValue(preflightFor(project, {
      ready: false, errors: [{ code: 'spec_required', key: 'spec_required:1', message: '请选择输出规格来源' }],
      spec_options: [{ video_id: 11, width: 1920, height: 1080, frame_rate: '25/1', hdr: false, selected: false },
        { video_id: 12, width: 1280, height: 720, frame_rate: '25/1', hdr: false, selected: false }]
    }));
    const wrapper = mount(VideoWorkbenchPage, { props: { openRequest: { projectID: 1, token: 1 } } });
    wrappers.push(wrapper);
    await flushPromises();
    expect(wrapper.get('[data-test="edit-errors"]').text()).toContain('请选择输出规格来源');
    expect(wrapper.get('[data-test="edit-queue"]').attributes('disabled')).toBeDefined();
    api.UpdateEditRecipe.mockImplementation(async request => mergeProject({ revision: 4, recipe: request.recipe }));
    await wrapper.get('[data-test="merge-spec-12"] input').setValue(true);
    await flushPromises();
    expect(api.UpdateEditRecipe.mock.calls[0][0].recipe.merge.spec_source_video_id).toBe(12);
    expect(wrapper.find('[data-test="edit-preflight-stale"]').exists()).toBe(true);
  });

  it('后端重新预检发现新警告时不排队，标出缺的确认', async () => {
    const project = mergeProject();
    const wrapper = await mountPage(project);
    api.QueueEditProject.mockResolvedValue({
      queued: false, project: null, unacknowledged: ['fps:12:25/1'], preflight: preflightFor(project, { warnings: [warnings[1]] })
    });
    await wrapper.get('[data-test="edit-queue"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="edit-ack-fps:12:25/1"]').element.closest('label').className).toContain('edit-check--missing');
    expect(wrapper.get('[data-test="edit-queue"]').attributes('disabled')).toBeDefined();
    expect(toastText()).toContain('还有警告没有确认');
  });

  it('切到快速模式走配方保存；快速不可用时给出原因且不能选', async () => {
    const project = mergeProject();
    api.GetEditProject.mockResolvedValue(project);
    api.PreflightEditProject.mockResolvedValue(preflightFor(project, { fast: { available: false, reasons: ['来源编码不一致'], cut_points: [] } }));
    const wrapper = mount(VideoWorkbenchPage, { props: { openRequest: { projectID: 1, token: 1 } } });
    wrappers.push(wrapper);
    await flushPromises();
    expect(wrapper.get('[data-test="edit-fast-reasons"]').text()).toContain('来源编码不一致');
    expect(wrapper.get('[data-test="edit-mode-fast"]').attributes('disabled')).toBeDefined();
  });
});

describe('事件驱动刷新与列表动作', () => {
  const runningItem = { id: 7, seq: 1, status: 'running', phase: 'encode', progress: 0.1, output_name: 'out.mkv', output_video_id: null, source_video_ids: [11, 12, 13], error_code: '', error_message: '' };

  it('同状态的进度事件就地更新，不重读；状态变化时重读项目', async () => {
    const wrapper = await mountPage(mergeProject({ status: 'running', items: [runningItem] }));
    expect(api.GetEditProject).toHaveBeenCalledTimes(1);
    handlers['video-edit-state']({ project_id: 1, item_id: 7, status: 'running', item_status: 'running', phase: 'concat', progress: 0.6 });
    await flushPromises();
    expect(api.GetEditProject).toHaveBeenCalledTimes(1);
    expect(wrapper.get('[data-test="edit-result-7"]').text()).toContain('拼接 60%');
    api.GetEditProject.mockResolvedValue(mergeProject({ status: 'completed', items: [{ ...runningItem, status: 'completed', progress: 1, output_video_id: 99 }] }));
    handlers['video-edit-state']({ project_id: 1, item_id: 7, status: 'completed', item_status: 'completed', phase: 'done', progress: 1 });
    await flushPromises();
    expect(api.GetEditProject).toHaveBeenCalledTimes(2);
    expect(wrapper.find('[data-test="edit-result-preview-7"]').exists()).toBe(true);
  });

  it('别的项目的事件只刷新列表；项目被删除时清掉选中', async () => {
    vi.useFakeTimers();
    try {
      const wrapper = await mountPage(mergeProject());
      const listCalls = api.ListEditProjects.mock.calls.length;
      handlers['video-edit-state']({ project_id: 99, status: 'running' });
      handlers['video-edit-state']({ project_id: 99, status: 'running', progress: 0.2 });
      expect(api.GetEditProject).toHaveBeenCalledTimes(1);
      await vi.advanceTimersByTimeAsync(900);
      expect(api.ListEditProjects.mock.calls.length).toBe(listCalls + 1);
      handlers['video-edit-state']({ project_id: 1, status: 'deleted' });
      await flushPromises();
      expect(wrapper.find('[data-test="workbench-placeholder"]').exists()).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });

  it('列表「继续」用已确认过的警告直接重新排队', async () => {
    const failed = mergeProject({ status: 'failed', acknowledged_warnings: ['fps:12:25/1'], items: [] });
    api.ListEditProjects.mockResolvedValue([{ id: 1, kind: 'merge', title: '合并', status: 'failed', revision: 3, item_count: 1, completed_count: 0, progress: 0 }]);
    const wrapper = await mountPage(failed);
    api.RequeueEditProject.mockResolvedValue({ queued: true, project: mergeProject({ status: 'queued' }), preflight: null, unacknowledged: [] });
    await wrapper.get('[data-test="edit-project-requeue-1"]').trigger('click');
    await flushPromises();
    expect(api.RequeueEditProject).toHaveBeenCalledWith({ project_id: 1, expected_revision: 3, acknowledged_warnings: ['fps:12:25/1'] });
    expect(api.QueueEditProject).not.toHaveBeenCalled();
    expect(wrapper.vm.project.status).toBe('queued');
  });

  it('取消导出与删除记录都先确认', async () => {
    api.ListEditProjects.mockResolvedValue([
      { id: 1, kind: 'merge', title: '合并', status: 'running', revision: 3, item_count: 1, completed_count: 0, progress: 0.3 },
      { id: 5, kind: 'merge', title: '旧项目', status: 'completed', revision: 2, item_count: 1, completed_count: 1, progress: 1 }
    ]);
    const wrapper = await mountPage(mergeProject({ status: 'running' }));
    await wrapper.get('[data-test="edit-project-cancel-1"]').trigger('click');
    expect(feedbackState.confirm.title).toBe('取消导出');
    resolveConfirm(true);
    await flushPromises();
    expect(api.CancelEditProject).toHaveBeenCalledWith(1);
    await wrapper.get('[data-test="edit-project-delete-5"]').trigger('click');
    expect(feedbackState.confirm.message).toContain('不会被删除');
    resolveConfirm(false);
    await flushPromises();
    expect(api.DeleteEditProject).not.toHaveBeenCalled();
    expect(wrapper.find('[data-test="edit-project-delete-1"]').exists()).toBe(false);
  });

  it('成品的预览与原片清理交给宿主（清理只发事件，不直接删除）', async () => {
    const item = { ...runningItem, status: 'completed', progress: 1, output_video_id: 99 };
    const wrapper = await mountPage(mergeProject({ status: 'completed', items: [item] }));
    await wrapper.get('[data-test="edit-result-preview-7"]').trigger('click');
    expect(wrapper.emitted('open-video')[0]).toEqual([99]);
    expect(wrapper.find('[data-test="edit-result-cleanup-7"]').exists()).toBe(false);
    await wrapper.get('[data-test="edit-result-confirm-7"]').setValue(true);
    await wrapper.get('[data-test="edit-result-cleanup-7"]').trigger('click');
    expect(wrapper.emitted('cleanup-sources')[0]).toEqual([[11, 12, 13]]);
    expect(api.DeleteVideosWithResult).not.toHaveBeenCalled();
    expect(wrapper.find('[data-test="edit-result-version-7"]').exists()).toBe(false);
  });
});

describe('VideoWorkbenchPage duplicate', () => {
  it('offers 复制为新草稿 only for finished/stopped projects and opens the copy', async () => {
    const failed = mergeProject({ status: 'failed', items: [] });
    const wrapper = await mountPage(failed);
    const button = wrapper.find('[data-test="workbench-duplicate"]');
    expect(button.exists()).toBe(true);
    const copy = mergeProject({ id: 9, status: 'draft', revision: 1, title: '合并（副本）' });
    api.DuplicateEditProject.mockResolvedValue(copy);
    api.GetEditProject.mockResolvedValue(copy);
    await button.trigger('click');
    await flushPromises();
    expect(api.DuplicateEditProject).toHaveBeenCalledWith(1);
    expect(api.GetEditProject).toHaveBeenLastCalledWith(9);
    expect(wrapper.find('[data-test="workbench-duplicate"]').exists()).toBe(false);
  });

  it('hides the duplicate action for drafts and active projects', async () => {
    for (const status of ['draft', 'queued', 'running', 'analyzing']) {
      const wrapper = await mountPage(mergeProject({ status }));
      expect(wrapper.find('[data-test="workbench-duplicate"]').exists()).toBe(false);
    }
  });
});
