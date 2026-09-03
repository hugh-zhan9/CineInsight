import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
  ListBrowserDownloadTasks: vi.fn(),
  CancelBrowserDownloadTask: vi.fn()
}));
vi.mock('../../wailsjs/go/main/App', () => api);

import DownloadsPage from './DownloadsPage.vue';

async function mountPage(tasks = []) {
  api.ListBrowserDownloadTasks.mockResolvedValue(tasks);
  const wrapper = mount(DownloadsPage);
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  vi.clearAllMocks();
  delete window.runtime;
});

describe('下载任务页', () => {
  it('没有任务时给出去哪里产生任务的说明', async () => {
    const wrapper = await mountPage([]);
    expect(wrapper.get('[data-test="downloads-empty"]').text()).toContain('发送到 CineInsight');
    wrapper.unmount();
  });

  it('区分下载失败与入库失败', async () => {
    const wrapper = await mountPage([
      { id: 'a', filename: '甲.mp4', state: 'failed', error: 'ffmpeg 失败：取不到分片' },
      { id: 'b', filename: '乙.mp4', state: 'done', import_error: '下载目录不在片库扫描目录里' }
    ]);
    const rows = wrapper.findAll('[data-test="download-item"]');
    expect(rows).toHaveLength(2);
    expect(rows[0].text()).toContain('取不到分片');
    // 文件已经在盘上了，不能说成下载失败
    expect(rows[1].text()).toContain('文件已保存，但没有入库');
    wrapper.unmount();
  });

  it('运行中的任务可以取消，已完成与入库中的不给取消按钮', async () => {
    api.CancelBrowserDownloadTask.mockResolvedValue(undefined);
    const wrapper = await mountPage([
      { id: 'running-1', filename: '丙.mp4', state: 'running' },
      { id: 'done-1', filename: '丁.mp4', state: 'done' },
      // 入库那一步是扫描，不接受取消
      { id: 'importing-1', filename: '戊.mp4', state: 'importing' }
    ]);

    const buttons = wrapper.findAll('[data-test="download-item"] button');
    expect(buttons).toHaveLength(1);
    await buttons[0].trigger('click');
    await flushPromises();
    expect(api.CancelBrowserDownloadTask).toHaveBeenCalledWith('running-1');
    wrapper.unmount();
  });

  it('转封装是独立状态，不冒充下载中，也不给取消按钮', async () => {
    const wrapper = await mountPage([
      { id: 'r', filename: '甲.mp4', state: 'remuxing', bytes_written: 1024 }
    ]);
    const row = wrapper.get('[data-test="download-item"]');
    expect(row.text()).toContain('转封装');
    expect(row.text()).not.toContain('下载中');
    expect(row.text()).toContain('不重新编码');
    // 转封装已经在写最终文件了，中途取消只会留下半个文件
    expect(row.findAll('button')).toHaveLength(0);
    wrapper.unmount();
  });

  it('概览按状态分别计数，入库失败单独点出来', async () => {
    const wrapper = await mountPage([
      { id: 'a', filename: '甲.mp4', state: 'running' },
      { id: 'b', filename: '乙.mp4', state: 'queued' },
      { id: 'c', filename: '丙.mp4', state: 'done' },
      { id: 'd', filename: '丁.mp4', state: 'done', import_error: '目录不在扫描范围' },
      { id: 'e', filename: '戊.mp4', state: 'failed', error: 'ffmpeg 失败' }
    ]);
    const summary = wrapper.get('[data-test="downloads-summary"]').text();
    expect(summary).toContain('进行中');
    expect(summary).toContain('1 个没入库');
    wrapper.unmount();
  });

  // 这条是这次改动的要点：进度靠后端推事件，不该再要用户手动刷新。
  it('后端推来的任务快照直接刷新界面，不需要手动刷新', async () => {
    const handlers = {};
    window.runtime = { EventsOn: (name, fn) => { handlers[name] = fn; return () => {}; } };
    const wrapper = await mountPage([{ id: 'a', filename: '甲.mp4', state: 'queued' }]);

    expect(wrapper.text()).toContain('排队中');
    handlers['browser-download-tasks']([
      { id: 'a', filename: '甲.mp4', state: 'running', processed_seconds: 65, bytes_written: 2048 }
    ]);
    await flushPromises();

    const row = wrapper.get('[data-test="download-item"]');
    expect(row.text()).toContain('下载中');
    expect(row.text()).toContain('1:05');
    // 界面没有再去查一次后端——刷新完全靠推送
    expect(api.ListBrowserDownloadTasks).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });
});
