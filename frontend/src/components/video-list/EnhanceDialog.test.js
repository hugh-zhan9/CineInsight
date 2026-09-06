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
