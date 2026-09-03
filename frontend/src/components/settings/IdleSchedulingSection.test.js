// 后台任务调度分区（D-030..D-032）：开关与阈值绑定在设置表单上，
// 状态块读空闲门总览，等待清单里的每一项都能「忽略空闲立即运行」。
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

import IdleSchedulingSection from './IdleSchedulingSection.vue';

function idleStatus(overrides = {}) {
  return {
    enabled: true,
    threshold_minutes: 5,
    require_ac_power: false,
    window_start: '',
    window_end: '',
    idle_seconds: 42,
    on_ac_power: true,
    probed: true,
    probe_error: '',
    settings_error: '',
    supports_probe: true,
    waiting: [],
    bypass_tasks: [],
    ...overrides
  };
}

function settingsForm(overrides = {}) {
  return {
    idle_scheduling_enabled: true,
    idle_threshold_minutes: 5,
    idle_require_ac_power: false,
    idle_window_start: '',
    idle_window_end: '',
    ...overrides
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  delete window.runtime;
  api.GetIdleSchedulerStatus.mockResolvedValue(idleStatus());
  api.RunGatedTaskNow.mockResolvedValue(null);
});

async function mountSection(form = settingsForm()) {
  const wrapper = mount(IdleSchedulingSection, { props: { form } });
  await flushPromises();
  return wrapper;
}

describe('后台任务调度设置分区', () => {
  it('把开关、阈值、电源与时间窗绑定到设置表单', async () => {
    const form = settingsForm();
    const wrapper = await mountSection(form);

    await wrapper.get('[data-test="idle-threshold-minutes"]').setValue(30);
    expect(form.idle_threshold_minutes).toBe(30);

    await wrapper.get('[data-test="idle-require-ac-power"]').setValue(true);
    expect(form.idle_require_ac_power).toBe(true);

    await wrapper.get('[data-test="idle-window-start"]').setValue('22:00');
    await wrapper.get('[data-test="idle-window-end"]').setValue('06:00');
    expect(form.idle_window_start).toBe('22:00');
    expect(form.idle_window_end).toBe('06:00');

    await wrapper.get('[data-test="idle-window-clear"]').trigger('click');
    expect(form.idle_window_start).toBe('');
    expect(form.idle_window_end).toBe('');

    await wrapper.get('[data-test="idle-scheduling-toggle"]').setValue(false);
    expect(form.idle_scheduling_enabled).toBe(false);
  });

  it('关掉开关后阈值与时间窗输入禁用', async () => {
    const wrapper = await mountSection(settingsForm({ idle_scheduling_enabled: false }));
    expect(wrapper.get('[data-test="idle-threshold-minutes"]').attributes('disabled')).toBeDefined();
    expect(wrapper.get('[data-test="idle-window-start"]').attributes('disabled')).toBeDefined();
  });

  it('展示当前空闲时长与供电状态', async () => {
    api.GetIdleSchedulerStatus.mockResolvedValue(idleStatus({ idle_seconds: 180, on_ac_power: false }));
    const wrapper = await mountSection();
    const text = wrapper.get('[data-test="idle-scheduler-state-text"]').text();
    expect(text).toContain('已空闲 3 分钟');
    expect(text).toContain('使用电池');
    expect(text).toContain('当前没有任务在等空闲');
  });

  it('探测失败时如实说明原因', async () => {
    api.GetIdleSchedulerStatus.mockResolvedValue(idleStatus({ probe_error: 'ioreg 探测失败' }));
    const wrapper = await mountSection();
    expect(wrapper.get('[data-test="idle-scheduler-state-text"]').text()).toContain('ioreg 探测失败');
  });

  it('还没探测过时如实说"正在探测"，不编一个 0 秒出来', async () => {
    api.GetIdleSchedulerStatus.mockResolvedValue(idleStatus({ probed: false, idle_seconds: 0 }));
    const wrapper = await mountSection();
    expect(wrapper.get('[data-test="idle-scheduler-state-text"]').text()).toContain('正在探测空闲状态');
  });

  it('设置读不出来时如实说明，并说清楚本轮按关闭处理', async () => {
    api.GetIdleSchedulerStatus.mockResolvedValue(idleStatus({ settings_error: '数据库未初始化' }));
    const wrapper = await mountSection();
    const text = wrapper.get('[data-test="idle-scheduler-state-text"]').text();
    expect(text).toContain('数据库未初始化');
    expect(text).toContain('按关闭处理');
  });

  it('开关关闭时说明自动任务会立即执行', async () => {
    api.GetIdleSchedulerStatus.mockResolvedValue(idleStatus({ enabled: false }));
    const wrapper = await mountSection(settingsForm({ idle_scheduling_enabled: false }));
    expect(wrapper.get('[data-test="idle-scheduler-state-text"]').text()).toContain('自动任务会立即执行');
  });

  it('列出等待中的任务并可以忽略空闲立即运行', async () => {
    api.GetIdleSchedulerStatus.mockResolvedValue(idleStatus({
      waiting: [{ task_key: 'phash', reason: 'user_active', since: '' }]
    }));
    const wrapper = await mountSection();

    const list = wrapper.get('[data-test="idle-waiting-list"]');
    expect(list.text()).toContain('近重复指纹');
    expect(list.text()).toContain('你正在用电脑');

    await wrapper.get('[data-test="idle-run-now-phash"]').trigger('click');
    await flushPromises();
    expect(api.RunGatedTaskNow).toHaveBeenCalledWith('phash');
  });

  it('立即运行失败时报错，不静默', async () => {
    api.GetIdleSchedulerStatus.mockResolvedValue(idleStatus({
      waiting: [{ task_key: 'technical', reason: 'on_battery', since: '' }]
    }));
    api.RunGatedTaskNow.mockRejectedValue(new Error('boom'));
    const wrapper = await mountSection();
    await wrapper.get('[data-test="idle-run-now-technical"]').trigger('click');
    await flushPromises();
    expect(feedback.notifyError).toHaveBeenCalled();
  });

  it('任务刚好已被放行时静默刷新，不弹错误', async () => {
    api.GetIdleSchedulerStatus.mockResolvedValue(idleStatus({
      waiting: [{ task_key: 'phash', reason: 'user_active', since: '' }]
    }));
    api.RunGatedTaskNow.mockRejectedValue(new Error('idle_gate_task_not_waiting: 该后台任务当前没有在等待空闲: phash'));
    const wrapper = await mountSection();

    await wrapper.get('[data-test="idle-run-now-phash"]').trigger('click');
    await flushPromises();
    expect(feedback.notifyError).not.toHaveBeenCalled();
    // 仍然回读一次状态：等待清单该跟着刷新。
    expect(api.GetIdleSchedulerStatus).toHaveBeenCalledTimes(2);
  });

  it('idle-scheduler-state 事件到达时刷新等待清单', async () => {
    const handlers = {};
    window.runtime = { EventsOn: (name, handler) => { handlers[name] = handler; return () => {}; } };
    const wrapper = await mountSection();
    expect(wrapper.find('[data-test="idle-waiting-list"]').exists()).toBe(false);

    handlers['idle-scheduler-state'](idleStatus({
      waiting: [{ task_key: 'exif', reason: 'outside_window', since: '' }]
    }));
    await wrapper.vm.$nextTick();
    expect(wrapper.get('[data-test="idle-waiting-list"]').text()).toContain('图片 EXIF');
    expect(wrapper.get('[data-test="idle-waiting-list"]').text()).toContain('不在允许的时间段内');
  });
});
