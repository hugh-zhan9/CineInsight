// 播放代理分区（D-005、D-006）：占用与上限来自后端，上限按 GiB 填但存字节，
// 「清空全部」要先确认，任务状态与取消由 playback-proxy-state 事件驱动。
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

import ProxySection from './ProxySection.vue';

const GIB = 1024 * 1024 * 1024;

function usage(overrides = {}) {
  return {
    total_bytes: 3 * GIB, count: 2, limit_bytes: 50 * GIB,
    orphan_count: 0, orphan_bytes: 0, foreign_files: 0,
    ...overrides
  };
}

function proxyStatus(overrides = {}) {
  return {
    running: false,
    cancelled: false,
    completed: true,
    queued: 0,
    total: 3,
    processed: 3,
    succeeded: 2,
    skipped: 0,
    failed: 1,
    current_video_id: 0,
    current_video_name: '',
    results: [
      { video_id: 1, name: '第一部.mkv', code: 'created', strategy: 'remux', message: '' },
      { video_id: 2, name: '第二部.avi', code: 'encode_failed', strategy: 'transcode', message: 'boom' }
    ],
    ...overrides
  };
}

function settingsForm(overrides = {}) {
  return { proxy_cache_limit_bytes: 50 * GIB, ...overrides };
}

beforeEach(() => {
  vi.clearAllMocks();
  delete window.runtime;
  api.GetPlaybackProxyUsage.mockResolvedValue(usage());
  api.GetPlaybackProxyStatus.mockResolvedValue(proxyStatus());
  api.EnforcePlaybackProxyLimit.mockResolvedValue(usage({ total_bytes: GIB, count: 1 }));
  api.ClearPlaybackProxies.mockResolvedValue(usage({ total_bytes: 0, count: 0 }));
  api.CancelPlaybackProxyTask.mockResolvedValue(null);
  feedback.confirmAction.mockResolvedValue(true);
});

async function mountSection(form = settingsForm()) {
  const wrapper = mount(ProxySection, { props: { form } });
  await flushPromises();
  return wrapper;
}

describe('ProxySection', () => {
  it('显示占用、数量与上限', async () => {
    const wrapper = await mountSection();
    const text = wrapper.get('[data-test="proxy-usage-text"]').text();
    expect(text).toContain('3.0 GB');
    expect(text).toContain('2 份');
    expect(text).toContain('50.0 GB');
  });

  it('上限为 0 时显示「不限」', async () => {
    api.GetPlaybackProxyUsage.mockResolvedValue(usage({ limit_bytes: 0 }));
    const wrapper = await mountSection(settingsForm({ proxy_cache_limit_bytes: 0 }));
    expect(wrapper.get('[data-test="proxy-usage-text"]').text()).toContain('不限');
    expect(wrapper.get('[data-test="proxy-cache-limit"]').element.value).toBe('0');
  });

  it('上限输入按 GiB 填、按字节存', async () => {
    const form = settingsForm();
    const wrapper = await mountSection(form);
    await wrapper.get('[data-test="proxy-cache-limit"]').setValue('20');
    expect(form.proxy_cache_limit_bytes).toBe(20 * GIB);
    // 填 0 表示不限，不该被换算成默认值。
    await wrapper.get('[data-test="proxy-cache-limit"]').setValue('0');
    expect(form.proxy_cache_limit_bytes).toBe(0);
  });

  it('「立即整理」调后端并回显新的占用', async () => {
    const wrapper = await mountSection();
    await wrapper.get('[data-test="proxy-enforce-limit"]').trigger('click');
    await flushPromises();
    expect(api.EnforcePlaybackProxyLimit).toHaveBeenCalledTimes(1);
    expect(wrapper.get('[data-test="proxy-action-message"]').text()).toContain('1 份');
  });

  it('「清空全部」先确认再调后端', async () => {
    const wrapper = await mountSection();
    await wrapper.get('[data-test="proxy-clear-all"]').trigger('click');
    await flushPromises();
    expect(feedback.confirmAction).toHaveBeenCalledTimes(1);
    expect(api.ClearPlaybackProxies).toHaveBeenCalledTimes(1);
    expect(wrapper.get('[data-test="proxy-usage-text"]').text()).toContain('0 份');
  });

  it('取消确认后不清空', async () => {
    feedback.confirmAction.mockResolvedValue(false);
    const wrapper = await mountSection();
    await wrapper.get('[data-test="proxy-clear-all"]').trigger('click');
    await flushPromises();
    expect(api.ClearPlaybackProxies).not.toHaveBeenCalled();
  });

  it('没有代理时「清空全部」置灰', async () => {
    api.GetPlaybackProxyUsage.mockResolvedValue(usage({ total_bytes: 0, count: 0 }));
    const wrapper = await mountSection();
    expect(wrapper.get('[data-test="proxy-clear-all"]').attributes('disabled')).toBeDefined();
  });

  it('展示上一轮任务结果与失败项', async () => {
    const wrapper = await mountSection();
    expect(wrapper.get('[data-test="proxy-task-text"]').text()).toContain('成功 2');
    expect(wrapper.get('[data-test="proxy-task-text"]').text()).toContain('失败 1');
    expect(wrapper.get('[data-test="proxy-failures"]').text()).toContain('第二部.avi');
    expect(wrapper.get('[data-test="proxy-failures"]').text()).toContain('转封装失败');
    expect(wrapper.find('[data-test="proxy-task-cancel"]').exists()).toBe(false);
  });

  it('运行中显示进度并能取消', async () => {
    api.GetPlaybackProxyStatus.mockResolvedValue(proxyStatus({
      running: true, completed: false, processed: 1, total: 3, current_video_name: '第三部.mkv', results: []
    }));
    const wrapper = await mountSection();
    expect(wrapper.get('[data-test="proxy-task-text"]').text()).toContain('1/3');
    expect(wrapper.get('[data-test="proxy-task-text"]').text()).toContain('第三部.mkv');
    await wrapper.get('[data-test="proxy-task-cancel"]').trigger('click');
    await flushPromises();
    expect(api.CancelPlaybackProxyTask).toHaveBeenCalledTimes(1);
  });

  it('playback-proxy-state 事件驱动状态，跑完顺带刷新占用', async () => {
    const handlers = {};
    window.runtime = { EventsOn: (name, handler) => { handlers[name] = handler; return () => { delete handlers[name]; }; } };
    const wrapper = await mountSection();
    expect(api.GetPlaybackProxyUsage).toHaveBeenCalledTimes(1);

    handlers['playback-proxy-state'](proxyStatus({ running: true, completed: false, processed: 1, total: 2, results: [] }));
    await flushPromises();
    expect(wrapper.get('[data-test="proxy-task-text"]').text()).toContain('1/2');

    handlers['playback-proxy-state'](proxyStatus({ running: false, completed: true, processed: 2, total: 2, succeeded: 2, failed: 0, results: [] }));
    await flushPromises();
    expect(api.GetPlaybackProxyUsage).toHaveBeenCalledTimes(2);
  });

  it('孤儿产物与陌生文件分别报数', async () => {
    api.GetPlaybackProxyUsage.mockResolvedValue(usage({ orphan_count: 3, orphan_bytes: 2 * GIB, foreign_files: 1 }));
    const wrapper = await mountSection();
    expect(wrapper.get('[data-test="proxy-orphan-text"]').text()).toContain('3 份无主产物');
    expect(wrapper.get('[data-test="proxy-orphan-text"]').text()).toContain('2.0 GB');
    expect(wrapper.get('[data-test="proxy-foreign-text"]').text()).toContain('1 个不是代理的文件');
  });

  it('没有孤儿与陌生文件时不显示这两行', async () => {
    const wrapper = await mountSection();
    expect(wrapper.find('[data-test="proxy-orphan-text"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="proxy-foreign-text"]').exists()).toBe(false);
  });

  it('表里没行但有孤儿产物时「清空全部」仍可用', async () => {
    api.GetPlaybackProxyUsage.mockResolvedValue(usage({ total_bytes: 0, count: 0, orphan_count: 2, orphan_bytes: 1024 }));
    const wrapper = await mountSection();
    expect(wrapper.get('[data-test="proxy-clear-all"]').attributes('disabled')).toBeUndefined();
  });

  it('占用读不出来时说明原因而不是空白', async () => {
    api.GetPlaybackProxyUsage.mockRejectedValue(new Error('db closed'));
    const wrapper = await mountSection();
    expect(wrapper.get('[data-test="proxy-usage"]').text()).toContain('读取代理占用失败');
  });
});
