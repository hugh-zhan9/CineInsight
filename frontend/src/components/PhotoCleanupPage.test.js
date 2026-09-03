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

import PhotoCleanupPage from './PhotoCleanupPage.vue';
import { photoCleanupStore } from '../utils/photoCleanupStore.js';

function makeMember(id, name) {
  return { id, name, path: `/p/${name}`, directory: '/p', width: 1000, height: 1000, file_size: 500000 };
}

const analysisStatus = () => ({
  running: false,
  completed: true,
  stale: false,
  started_at: '2026-09-02T00:00:00Z',
  analysis: {
    duplicate_groups: [],
    near_duplicate_groups: [{
      original: makeMember(1, '1 (102).jpg'),
      candidates: [makeMember(2, 'guochan2048.com-1 (123).jpg')],
      reason: 'dHash 汉明距离 3'
    }]
  }
});

beforeEach(() => {
  vi.clearAllMocks();
  photoCleanupStore.status = analysisStatus();
  api.GetImageCleanupStatus.mockResolvedValue(analysisStatus());
});

describe('图片清理审阅', () => {
  it('近似重复也默认按"保留推荐那份、其余勾删"预置', async () => {
    const wrapper = mount(PhotoCleanupPage);
    await flushPromises();

    // 推荐保留项（后端排序后的 original）不勾，其余勾上
    expect(wrapper.vm.selection).toEqual([2]);
    expect(wrapper.find('[data-test="cleanup-group-outcome"]').text()).toBe('本组将删除 1 张');
  });

  it('「按建议勾选 / 取消」一键翻转本组', async () => {
    const wrapper = mount(PhotoCleanupPage);
    await flushPromises();

    const toggle = () => wrapper.find('[data-test="cleanup-suggest-group"]');
    expect(toggle().text()).toBe('取消本组勾选');

    await toggle().trigger('click');
    await flushPromises();
    expect(wrapper.vm.selection).toEqual([]);
    expect(wrapper.find('[data-test="cleanup-group-outcome"]').text()).toBe('本组暂不删除任何图片');
    expect(toggle().text()).toBe('按建议勾选（保留推荐项）');

    await toggle().trigger('click');
    await flushPromises();
    expect(wrapper.vm.selection).toEqual([2]);
  });

  it('改了保留项之后，建议勾选跟着换成新的其余项', async () => {
    const wrapper = mount(PhotoCleanupPage);
    await flushPromises();

    const entry = wrapper.vm.entries[0];
    wrapper.vm.setKeep(entry, entry.members[1]);
    await flushPromises();

    expect(wrapper.vm.selection).toEqual([1]);
  });
});

describe('进入清理页的自动分析', () => {
  it('没有可用结果时自动发起一次分析，不用再点"开始分析"', async () => {
    api.GetImageCleanupStatus.mockResolvedValue({ running: false, completed: false, analysis: null });
    api.StartImageCleanupAnalysis.mockResolvedValue({ running: true, completed: false, analysis: null });
    photoCleanupStore.status = { running: false, completed: false, analysis: null };

    const wrapper = mount(PhotoCleanupPage);
    await flushPromises();

    expect(api.StartImageCleanupAnalysis).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });

  it('后端已有结果就直接用，不覆盖正在审阅的那份', async () => {
    const wrapper = mount(PhotoCleanupPage);
    await flushPromises();

    expect(api.StartImageCleanupAnalysis).not.toHaveBeenCalled();
    expect(wrapper.findAll('[data-test="cleanup-group-card"]').length).toBeGreaterThan(0);
    wrapper.unmount();
  });

  it('分析正在后台跑时不再重复发起', async () => {
    api.GetImageCleanupStatus.mockResolvedValue({ running: true, completed: false, analysis: null });
    photoCleanupStore.status = { running: true, completed: false, analysis: null };

    const wrapper = mount(PhotoCleanupPage);
    await flushPromises();

    expect(api.StartImageCleanupAnalysis).not.toHaveBeenCalled();
    wrapper.unmount();
  });
});
