import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries(['GetPlaybackQueue', 'EditPlaybackQueue', 'PlayPlaybackQueue'].map(name => [name, vi.fn()])));
vi.mock('../../wailsjs/go/main/App', () => api);

import QueueActionButton from './QueueActionButton.vue';

// 按钮尺寸跟着所在那一行：抽屉与列表里是紧凑的 sm（默认），整页详情的动作行是常规尺寸（md）。
describe('QueueActionButton size', () => {
  it('is compact by default and regular-sized with size="md"', () => {
    expect(mount(QueueActionButton).get('[data-test="queue-add"]').classes()).toEqual(['btn', 'btn--secondary', 'btn--sm']);
    expect(mount(QueueActionButton, { props: { size: 'md' } }).get('[data-test="queue-add"]').classes()).toEqual(['btn', 'btn--secondary']);
  });
});

// 单部视频的按钮是开关：已在待播队列里显示「移除待播」，点了把它在队列里的每一条都删掉；不在就「加入待播」。
describe('QueueActionButton 加入 / 移除待播（单部视频）', () => {
  let queue;
  let revision;
  let listeners;
  const page = () => ({ state: { revision }, items: queue.map(item => ({ ...item })), has_more: false, cursor_id: 0, cursor_position: 0 });
  const wrappers = [];
  function mountButton(props = {}) {
    const wrapper = mount(QueueActionButton, { props: { videoIds: [7], label: '加入待播', ...props } });
    wrappers.push(wrapper);
    return wrapper;
  }
  const text = wrapper => wrapper.get('[data-test="queue-add"]').text();

  beforeEach(() => {
    vi.clearAllMocks();
    queue = [];
    revision = 1;
    listeners = {};
    window.runtime = { EventsOn: vi.fn((name, handler) => { listeners[name] = handler; return () => { delete listeners[name]; }; }) };
    api.GetPlaybackQueue.mockImplementation(async () => page());
    api.EditPlaybackQueue.mockImplementation(async edit => {
      if (edit.expected_revision !== revision) throw new Error('queue_changed: 待播队列已改变，请刷新后重试');
      if (edit.action === 'append_videos') queue.push(...edit.video_ids.map((id, index) => ({ id: 100 + queue.length + index, video_id: id })));
      if (edit.action === 'remove') queue = queue.filter(item => item.id !== edit.entry_id);
      revision += 1;
    });
  });
  afterEach(() => {
    while (wrappers.length) wrappers.pop().unmount();
    delete window.runtime;
  });

  it('不在队列里：显示「加入待播」，点了加入后变成「移除待播」，并给「查看队列」', async () => {
    const wrapper = mountButton();
    await flushPromises();
    expect(text(wrapper)).toBe('加入待播');
    expect(wrapper.get('[data-test="queue-add"]').attributes('aria-pressed')).toBe('false');

    await wrapper.get('[data-test="queue-add"]').trigger('click');
    await flushPromises();
    expect(api.EditPlaybackQueue).toHaveBeenCalledWith(expect.objectContaining({ action: 'append_videos', video_ids: [7] }));
    expect(text(wrapper)).toBe('移除待播');
    expect(wrapper.get('[data-test="queue-add"]').attributes('aria-pressed')).toBe('true');
    expect(wrapper.find('[data-test="queue-show-added"]').text()).toBe('查看队列');
  });

  it('打开时已在队列里（加过两次）：显示「移除待播」，点了两条都删掉、revision 逐条递增，按钮回到「加入待播」', async () => {
    queue = [{ id: 11, video_id: 7 }, { id: 12, video_id: 3 }, { id: 13, video_id: 7 }];
    revision = 5;
    const wrapper = mountButton();
    await flushPromises();
    expect(text(wrapper)).toBe('移除待播');

    await wrapper.get('[data-test="queue-add"]').trigger('click');
    await flushPromises();
    expect(api.EditPlaybackQueue.mock.calls.map(([edit]) => [edit.action, edit.entry_id, edit.expected_revision])).toEqual([
      ['remove', 11, 5], ['remove', 13, 6]
    ]);
    expect(queue.map(item => item.id)).toEqual([12]);
    expect(text(wrapper)).toBe('加入待播');
    expect(wrapper.find('[data-test="queue-show-added"]').exists()).toBe(false);
  });

  it('队列在别处变了（playback-queue-state）：重新读，按钮跟着变', async () => {
    const wrapper = mountButton();
    await flushPromises();
    expect(text(wrapper)).toBe('加入待播');
    queue = [{ id: 21, video_id: 7 }];
    revision = 2;
    listeners['playback-queue-state']();
    await flushPromises();
    expect(text(wrapper)).toBe('移除待播');
  });

  it('跨页读完整个队列才判断（第二页里才有这部）', async () => {
    api.GetPlaybackQueue
      .mockResolvedValueOnce({ state: { revision: 3 }, items: [{ id: 1, video_id: 1 }], has_more: true, cursor_id: 1, cursor_position: 1 })
      .mockResolvedValueOnce({ state: { revision: 3 }, items: [{ id: 2, video_id: 7 }], has_more: false, cursor_id: 2, cursor_position: 2 });
    const wrapper = mountButton();
    await flushPromises();
    expect(api.GetPlaybackQueue).toHaveBeenNthCalledWith(2, { revision: 3, cursor_position: 1, cursor_id: 1, limit: 200 });
    expect(text(wrapper)).toBe('移除待播');
  });

  it('移除途中队列被别处改了：按最新队列重来一次，不漏删', async () => {
    queue = [{ id: 31, video_id: 7 }];
    revision = 1;
    const wrapper = mountButton();
    await flushPromises();
    let first = true;
    const realEdit = api.EditPlaybackQueue.getMockImplementation();
    api.EditPlaybackQueue.mockImplementation(async edit => {
      if (first) { first = false; queue.push({ id: 32, video_id: 7 }); revision += 1; throw new Error('queue_changed: 待播队列已改变，请刷新后重试'); }
      return realEdit(edit);
    });
    await wrapper.get('[data-test="queue-add"]').trigger('click');
    await flushPromises();
    expect(queue).toEqual([]);
    expect(text(wrapper)).toBe('加入待播');
  });

  it('换成另一部视频：先按新的一部重新读，旧的那次晚到的结果丢掉', async () => {
    let releaseFirst;
    api.GetPlaybackQueue.mockImplementationOnce(() => new Promise(resolve => { releaseFirst = () => resolve({ state: { revision: 1 }, items: [{ id: 1, video_id: 7 }], has_more: false }); }));
    const wrapper = mountButton();
    await wrapper.setProps({ videoIds: [8] });
    await flushPromises();
    releaseFirst();
    await flushPromises();
    expect(text(wrapper)).toBe('加入待播');
  });

  it('作品集、替换队列的按钮不是开关：始终只加，不读队列', async () => {
    const wrapper = mountButton({ videoIds: [], collectionId: 4, label: '全部加入待播' });
    await flushPromises();
    expect(api.GetPlaybackQueue).not.toHaveBeenCalled();
    expect(text(wrapper)).toBe('全部加入待播');
    expect(wrapper.get('[data-test="queue-add"]').attributes('aria-pressed')).toBeUndefined();
  });

  it('移除失败：报出原因，按钮保持「移除待播」', async () => {
    queue = [{ id: 41, video_id: 7 }];
    const wrapper = mountButton();
    await flushPromises();
    api.EditPlaybackQueue.mockRejectedValueOnce(new Error('数据库繁忙'));
    await wrapper.get('[data-test="queue-add"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[role="alert"]').text()).toContain('数据库繁忙');
    expect(text(wrapper)).toBe('移除待播');
  });
});
