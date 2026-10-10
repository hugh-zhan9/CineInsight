import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ ConfirmQuit: vi.fn(), LogFrontend: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);

import QuitConfirmDialog from './QuitConfirmDialog.vue';
import { feedbackState, resetFeedback } from '../utils/feedback.js';

let handlers;
const wrappers = [];

function mountDialog() {
  const wrapper = mount(QuitConfirmDialog);
  wrappers.push(wrapper);
  return wrapper;
}

beforeEach(() => {
  vi.clearAllMocks();
  resetFeedback();
  handlers = {};
  window.runtime = { EventsOn: (event, handler) => { handlers[event] = handler; return () => { delete handlers[event]; }; } };
  api.ConfirmQuit.mockResolvedValue(undefined);
});

afterEach(() => {
  while (wrappers.length) wrappers.pop().unmount();
  delete window.runtime;
});

const REQUEST = {
  tasks: [
    { key: 'subtitle', running: 1, queued: 2, names: ['海边.mp4', '山谷.mp4', '城市.mp4'] },
    { key: 'enhancement', running: 1, queued: 0, names: [] },
    { key: 'browser_download', running: 2, queued: 5, names: ['预告片'] }
  ]
};

describe('退出确认（MEDIA-10）', () => {
  it('MEDIA-10 收到 quit-confirm-required 才出现，列出被中断的任务、个数与名字', async () => {
    const wrapper = mountDialog();
    expect(wrapper.find('[data-test="quit-confirm"]').exists()).toBe(false);

    handlers['quit-confirm-required'](REQUEST);
    await flushPromises();

    expect(wrapper.get('[data-test="quit-task-subtitle"]').text()).toContain('字幕生成');
    expect(wrapper.get('[data-test="quit-task-subtitle"]').text()).toContain('进行中 1 · 排队 2');
    expect(wrapper.get('[data-test="quit-task-subtitle"]').text()).toContain('海边.mp4、山谷.mp4、城市.mp4');
    expect(wrapper.get('[data-test="quit-task-enhancement"]').text()).toContain('视频超分');
    expect(wrapper.get('[data-test="quit-task-browser_download"]').text()).toContain('预告片 等 7 个');
    const text = wrapper.text();
    expect(text).toContain('上次中断');
    expect(text).toContain('需要回到浏览器重新推送');
  });

  it('MEDIA-10 只说与本次任务有关的后果', async () => {
    const wrapper = mountDialog();
    handlers['quit-confirm-required']({ tasks: [{ key: 'proxy', running: 1, queued: 3, names: ['旧格式.avi'] }] });
    await flushPromises();

    expect(wrapper.get('[data-test="quit-task-proxy"]').text()).toContain('播放代理');
    expect(wrapper.text()).not.toContain('上次中断');
    expect(wrapper.text()).not.toContain('重新推送');
  });

  it('P-008 视频工作台的导出有中文名，并提示下次可继续未完成项', async () => {
    const wrapper = mountDialog();
    handlers['quit-confirm-required']({ tasks: [{ key: 'video_edit', running: 1, queued: 2, names: [] }] });
    await flushPromises();

    expect(wrapper.get('[data-test="quit-task-video_edit"]').text()).toContain('视频工作台');
    expect(wrapper.get('[data-test="quit-task-video_edit"]').text()).toContain('进行中 1 · 排队 2');
    expect(wrapper.text()).toContain('「继续」未完成项');
    expect(wrapper.text()).not.toContain('重新推送');
  });

  it('MEDIA-10 「仍然退出」调用 ConfirmQuit，退出过程中按钮置灰', async () => {
    let finish;
    api.ConfirmQuit.mockReturnValue(new Promise(resolve => { finish = resolve; }));
    const wrapper = mountDialog();
    handlers['quit-confirm-required'](REQUEST);
    await flushPromises();

    await wrapper.get('[data-test="quit-confirm-button"]').trigger('click');
    expect(api.ConfirmQuit).toHaveBeenCalledTimes(1);
    expect(wrapper.get('[data-test="quit-confirm-button"]').text()).toBe('正在退出…');
    expect(wrapper.get('[data-test="quit-cancel"]').attributes('disabled')).toBeDefined();
    finish();
  });

  it('MEDIA-10 「继续运行」关闭确认框、不退出', async () => {
    const wrapper = mountDialog();
    handlers['quit-confirm-required'](REQUEST);
    await flushPromises();

    await wrapper.get('[data-test="quit-cancel"]').trigger('click');
    expect(api.ConfirmQuit).not.toHaveBeenCalled();
    expect(wrapper.find('[data-test="quit-confirm"]').exists()).toBe(false);
  });

  it('MEDIA-10 ConfirmQuit 失败时提示原因并恢复按钮', async () => {
    api.ConfirmQuit.mockRejectedValue('应用尚未就绪，无法退出');
    const wrapper = mountDialog();
    handlers['quit-confirm-required'](REQUEST);
    await flushPromises();

    await wrapper.get('[data-test="quit-confirm-button"]').trigger('click');
    await flushPromises();
    expect(feedbackState.toasts.map(toast => toast.message)).toContain('退出失败：应用尚未就绪，无法退出');
    expect(wrapper.get('[data-test="quit-confirm-button"]').attributes('disabled')).toBeUndefined();
  });
});
