import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import ShortFeedApp from '../short-feed/ShortFeedApp.vue';

// 全屏：有元素全屏 API 就对整页进全屏，状态跟随 fullscreenchange；iPhone 没有该 API 时
// 退到 <video> 的系统播放器全屏，且只在视频元数据到了之后才给按钮。
describe('短视频全屏', () => {
  let fullscreenElement = null;
  const item = id => ({ media_kind: 'video', id, name: `clip-${id}`, media_url: `/short-media/video/${id}`, width: 1080, height: 1920, tags: [] });

  beforeEach(() => {
    let nextID = 0;
    vi.stubGlobal('fetch', vi.fn(async url => ({
      ok: true,
      headers: { get: () => 'application/json' },
      json: async () => (String(url).startsWith('/short-api/feed/next') ? item(++nextID) : {})
    })));
    HTMLMediaElement.prototype.play = vi.fn(() => Promise.resolve());
    HTMLMediaElement.prototype.pause = vi.fn();
    fullscreenElement = null;
    Object.defineProperty(document, 'fullscreenElement', { configurable: true, get: () => fullscreenElement });
    Object.defineProperty(document, 'fullscreenEnabled', { configurable: true, value: false });
    document.documentElement.requestFullscreen = vi.fn(async () => { fullscreenElement = document.documentElement; });
    document.exitFullscreen = vi.fn(async () => { fullscreenElement = null; });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    delete HTMLMediaElement.prototype.webkitEnterFullscreen;
    document.body.innerHTML = '';
  });

  async function mountApp() {
    const host = document.createElement('div');
    document.body.append(host);
    const wrapper = mount(ShortFeedApp, { attachTo: host });
    await flushPromises();
    return wrapper;
  }

  it('整页进全屏并跟随浏览器状态，卸载时释放监听', async () => {
    Object.defineProperty(document, 'fullscreenEnabled', { configurable: true, value: true });
    const removed = vi.spyOn(document, 'removeEventListener');
    const wrapper = await mountApp();
    const button = wrapper.get('[data-test="short-feed-fullscreen"]');
    expect(button.attributes('title')).toBe('全屏');
    await button.trigger('click');
    await flushPromises();
    expect(document.documentElement.requestFullscreen).toHaveBeenCalledWith({ navigationUI: 'hide' });
    document.dispatchEvent(new Event('fullscreenchange'));
    await flushPromises();
    expect(wrapper.get('[data-test="short-feed-fullscreen"]').attributes('title')).toBe('退出全屏');
    await wrapper.get('[data-test="short-feed-fullscreen"]').trigger('click');
    await flushPromises();
    expect(document.exitFullscreen).toHaveBeenCalledTimes(1);
    document.dispatchEvent(new Event('fullscreenchange'));
    await flushPromises();
    expect(wrapper.get('[data-test="short-feed-fullscreen"]').attributes('title')).toBe('全屏');
    wrapper.unmount();
    expect(removed.mock.calls.map(([name]) => name)).toContain('fullscreenchange');
  });

  it('没有任何全屏路径时不显示按钮', async () => {
    const wrapper = await mountApp();
    expect(wrapper.find('[data-test="short-feed-fullscreen"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('iPhone：元数据到了才给按钮，点击走系统播放器全屏，退出事件回写状态', async () => {
    HTMLMediaElement.prototype.webkitEnterFullscreen = vi.fn();
    const wrapper = await mountApp();
    expect(wrapper.find('[data-test="short-feed-fullscreen"]').exists()).toBe(false);
    await wrapper.get('video').trigger('loadedmetadata');
    const button = wrapper.get('[data-test="short-feed-fullscreen"]');
    await button.trigger('click');
    await flushPromises();
    expect(HTMLMediaElement.prototype.webkitEnterFullscreen).toHaveBeenCalledTimes(1);
    expect(document.documentElement.requestFullscreen).not.toHaveBeenCalled();
    await wrapper.get('video').trigger('webkitbeginfullscreen');
    expect(wrapper.get('[data-test="short-feed-fullscreen"]').attributes('title')).toBe('退出全屏');
    await wrapper.get('video').trigger('webkitendfullscreen');
    expect(wrapper.get('[data-test="short-feed-fullscreen"]').attributes('title')).toBe('全屏');
    wrapper.unmount();
  });
});
