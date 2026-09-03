// 人脸识别分区（D-016..D-022）：运行时状态与准备、镜像前缀、分析启动/取消、
// 等待空闲与立即运行、自动开关、数据占用与二次确认清除、隐私披露。
import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

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

import FaceSection from './FaceSection.vue';

function runtimeStatus(overrides = {}) {
  return {
    state: 'available',
    reason: '',
    identity: 'insightface-1.0.1-buffalo_l-onnxruntime-1.23.1',
    runtime_dir: '/Users/tester/.CineInsight/face-runtime',
    model_source_url: 'https://github.com/deepinsight/insightface/releases/download/v0.7/buffalo_l.zip',
    model_sha256: '80ffe37d',
    model_archive_size: '275 MB',
    mirror_configured: false,
    preparing: false,
    stage: '',
    message: '',
    downloaded_bytes: 0,
    total_bytes: 0,
    cancelled: false,
    error: '',
    ...overrides
  };
}

function analysisStatus(overrides = {}) {
  return {
    running: false,
    preparing: false,
    cancelled: false,
    completed: true,
    interrupted: false,
    scope: 'all',
    total: 4,
    processed: 4,
    succeeded: 4,
    failed: 0,
    faces_detected: 6,
    clusters_created: 2,
    current_media: '',
    failures: [],
    last_error: '',
    gate: { waiting_idle: false, reason: '' },
    ...overrides
  };
}

function usage(overrides = {}) {
  return {
    observation_count: 12,
    cluster_count: 3,
    candidate_count: 1,
    crop_file_count: 12,
    crop_bytes: 1024 * 1024 * 2,
    ...overrides
  };
}

function settingsForm(overrides = {}) {
  return { auto_face_analysis: false, face_model_mirror_url: '', ...overrides };
}

beforeEach(() => {
  vi.clearAllMocks();
  delete window.runtime;
  feedback.confirmAction.mockResolvedValue(true);
  api.GetFaceRuntimeStatus.mockResolvedValue(runtimeStatus());
  api.GetFaceAnalysisStatus.mockResolvedValue(analysisStatus());
  api.GetFaceDataUsage.mockResolvedValue(usage());
  api.PrepareFaceRuntime.mockResolvedValue(runtimeStatus({ preparing: true, stage: 'python' }));
  api.StartFaceAnalysis.mockResolvedValue(analysisStatus({ running: true, completed: false }));
  api.ClearFaceData.mockResolvedValue(usage({ observation_count: 0, cluster_count: 0, candidate_count: 0, crop_file_count: 0, crop_bytes: 0 }));
});

async function mountSection(form = settingsForm()) {
  const wrapper = mount(FaceSection, { props: { form } });
  await flushPromises();
  return wrapper;
}

describe('人脸识别设置分区', () => {
  it('运行时就绪时显示已就绪并允许启动分析', async () => {
    const wrapper = await mountSection();
    expect(wrapper.get('[data-test="face-runtime-state"]').text()).toBe('已就绪');
    expect(wrapper.find('[data-test="face-prepare-runtime"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="face-analysis-start"]').attributes('disabled')).toBeUndefined();

    await wrapper.get('[data-test="face-analysis-start"]').trigger('click');
    await flushPromises();
    expect(api.StartFaceAnalysis).toHaveBeenCalledWith('all');
  });

  it('可以只分析图片', async () => {
    const wrapper = await mountSection();
    await wrapper.get('[data-test="face-analysis-scope"]').setValue('images');
    await wrapper.get('[data-test="face-analysis-start"]').trigger('click');
    await flushPromises();
    expect(api.StartFaceAnalysis).toHaveBeenCalledWith('images');
  });

  it('运行时缺模型时置灰分析入口并说明原因，同时给出准备按钮', async () => {
    api.GetFaceRuntimeStatus.mockResolvedValue(runtimeStatus({
      state: 'missing_model',
      reason: '人脸模型（buffalo_l，275 MB）尚未下载'
    }));
    const wrapper = await mountSection();

    expect(wrapper.get('[data-test="face-runtime-state"]').text()).toBe('未就绪');
    expect(wrapper.get('[data-test="face-analysis-start"]').attributes('disabled')).toBeDefined();
    expect(wrapper.get('[data-test="face-analysis-disabled-reason"]').text()).toContain('尚未下载');

    await wrapper.get('[data-test="face-prepare-runtime"]').trigger('click');
    await flushPromises();
    expect(api.PrepareFaceRuntime).toHaveBeenCalled();
  });

  it('平台不支持时不给准备入口', async () => {
    api.GetFaceRuntimeStatus.mockResolvedValue(runtimeStatus({
      state: 'incompatible',
      reason: '人脸识别 sidecar 仅支持 macOS Apple Silicon（当前 windows/amd64）'
    }));
    const wrapper = await mountSection();
    expect(wrapper.get('[data-test="face-runtime-state"]').text()).toBe('当前平台不支持');
    expect(wrapper.find('[data-test="face-prepare-runtime"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="face-runtime-reason"]').text()).toContain('Apple Silicon');
  });

  it('下载失败时显示失败原因与来源地址，并允许重试', async () => {
    api.GetFaceRuntimeStatus.mockResolvedValue(runtimeStatus({
      state: 'download_failed',
      reason: '模型下载或校验失败：模型包校验失败：sha256 与清单不符',
      error: '模型包校验失败：sha256 与清单不符'
    }));
    const wrapper = await mountSection();
    expect(wrapper.get('[data-test="face-runtime-state"]').text()).toBe('下载失败');
    expect(wrapper.get('[data-test="face-runtime-error"]').text()).toContain('sha256');
    expect(wrapper.get('[data-test="face-model-source"]').text()).toContain('buffalo_l.zip');
    expect(wrapper.find('[data-test="face-prepare-runtime"]').exists()).toBe(true);
  });

  it('准备中显示进度并可取消', async () => {
    api.GetFaceRuntimeStatus.mockResolvedValue(runtimeStatus({
      state: 'missing_model',
      preparing: true,
      stage: 'model',
      message: '正在下载人脸模型（275 MB）…',
      downloaded_bytes: 50,
      total_bytes: 200
    }));
    const wrapper = await mountSection();
    expect(wrapper.get('[data-test="face-runtime-state"]').text()).toBe('正在准备');
    expect(wrapper.get('[data-test="face-runtime-progress"]').text()).toContain('25%');

    await wrapper.get('[data-test="face-cancel-prepare"]').trigger('click');
    await flushPromises();
    expect(api.CancelFaceRuntimePrepare).toHaveBeenCalled();
  });

  it('镜像前缀与自动开关绑定到设置表单', async () => {
    const form = settingsForm();
    const wrapper = await mountSection(form);

    await wrapper.get('[data-test="face-mirror-url"]').setValue('https://mirror.example.com/');
    expect(form.face_model_mirror_url).toBe('https://mirror.example.com/');

    await wrapper.get('[data-test="face-auto-toggle"]').setValue(true);
    expect(form.auto_face_analysis).toBe(true);
  });

  it('分析进行中显示进度并可取消', async () => {
    api.GetFaceAnalysisStatus.mockResolvedValue(analysisStatus({
      running: true, completed: false, processed: 1, total: 3, current_media: 'movie.mp4'
    }));
    const wrapper = await mountSection();
    expect(wrapper.get('[data-test="face-analysis-progress"]').text()).toContain('1/3');
    expect(wrapper.get('[data-test="face-analysis-progress"]').text()).toContain('movie.mp4');

    await wrapper.get('[data-test="face-analysis-cancel"]').trigger('click');
    await flushPromises();
    expect(api.CancelFaceAnalysis).toHaveBeenCalled();
  });

  it('等待空闲时说明原因并可忽略空闲立即运行', async () => {
    api.GetFaceAnalysisStatus.mockResolvedValue(analysisStatus({
      running: true, completed: false, processed: 0, total: 2,
      gate: { waiting_idle: true, reason: 'user_active' }
    }));
    const wrapper = await mountSection();
    expect(wrapper.get('[data-test="face-analysis-progress"]').text()).toContain('你正在用电脑');

    await wrapper.get('[data-test="face-run-now"]').trigger('click');
    await flushPromises();
    expect(api.RunGatedTaskNow).toHaveBeenCalledWith('face');
  });

  it('立即运行恰好落在放行之后时静默刷新，不弹错误', async () => {
    api.GetFaceAnalysisStatus.mockResolvedValue(analysisStatus({
      running: true, completed: false, gate: { waiting_idle: true, reason: 'user_active' }
    }));
    api.RunGatedTaskNow.mockRejectedValue(new Error('idle_gate_task_not_waiting: 该后台任务当前没有在等待空闲: face'));
    const wrapper = await mountSection();

    await wrapper.get('[data-test="face-run-now"]').trigger('click');
    await flushPromises();
    expect(feedback.notifyError).not.toHaveBeenCalled();
  });

  it('中断的一轮如实说明可以续跑', async () => {
    api.GetFaceAnalysisStatus.mockResolvedValue(analysisStatus({
      completed: false, interrupted: true, processed: 2
    }));
    const wrapper = await mountSection();
    expect(wrapper.get('[data-test="face-analysis-progress"]').text()).toContain('中断');
    expect(wrapper.get('[data-test="face-analysis-progress"]').text()).toContain('接着跑');
  });

  it('逐项失败逐条列出', async () => {
    api.GetFaceAnalysisStatus.mockResolvedValue(analysisStatus({
      failed: 1,
      failures: [{ media_kind: 'video', media_id: 3, name: 'broken.mkv', error: '没有可分析的图像' }]
    }));
    const wrapper = await mountSection();
    expect(wrapper.get('[data-test="face-analysis-failures"]').text()).toContain('broken.mkv');
    expect(wrapper.get('[data-test="face-analysis-failures"]').text()).toContain('没有可分析的图像');
  });

  it('展示数据占用并在二次确认后清除', async () => {
    const wrapper = await mountSection();
    expect(wrapper.get('[data-test="face-usage"]').text()).toContain('12 条人脸观测');
    expect(wrapper.get('[data-test="face-usage"]').text()).toContain('2.0 MB');

    await wrapper.get('[data-test="face-clear-data"]').trigger('click');
    await flushPromises();
    expect(feedback.confirmAction).toHaveBeenCalled();
    expect(api.ClearFaceData).toHaveBeenCalled();
    expect(wrapper.get('[data-test="face-usage"]').text()).toContain('0 条人脸观测');
  });

  it('取消确认时不清除', async () => {
    feedback.confirmAction.mockResolvedValue(false);
    const wrapper = await mountSection();
    await wrapper.get('[data-test="face-clear-data"]').trigger('click');
    await flushPromises();
    expect(api.ClearFaceData).not.toHaveBeenCalled();
  });

  it('隐私披露如实说明准备运行时要联网、之后全程离线', async () => {
    const wrapper = await mountSection();
    const text = wrapper.get('[data-test="face-privacy-note"]').text();
    // 准备阶段的三条出口都要写出来，不能只说"模型下载"。
    expect(text).toContain('PyPI');
    expect(text).toContain('托管 Python');
    expect(text).toContain('模型包');
    expect(text).toContain('准备完成之后');
    expect(text).toContain('全程离线');
    expect(text).toContain('本机');
    expect(text).toContain('绝不外发');
  });

  it('face-analysis-state 事件到达时刷新状态', async () => {
    const handlers = {};
    window.runtime = { EventsOn: (name, handler) => { handlers[name] = handler; return () => {}; } };
    const wrapper = await mountSection();

    handlers['face-analysis-state'](analysisStatus({ running: true, completed: false, processed: 2, total: 5 }));
    await wrapper.vm.$nextTick();
    expect(wrapper.get('[data-test="face-analysis-progress"]').text()).toContain('2/5');

    handlers['face-runtime-state'](runtimeStatus({ state: 'missing_venv', reason: '人脸依赖尚未安装（onnxruntime、insightface 等）' }));
    await wrapper.vm.$nextTick();
    expect(wrapper.get('[data-test="face-runtime-reason"]').text()).toContain('依赖尚未安装');
  });
});
