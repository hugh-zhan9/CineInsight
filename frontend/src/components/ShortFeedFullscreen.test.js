import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import ShortFeedApp from '../short-feed/ShortFeedApp.vue';

// 全屏：有元素全屏 API 就对整页进全屏，状态跟随 fullscreenchange；iPhone 没有该 API 时
// 退到 <video> 的系统播放器全屏，且只在视频元数据到了之后才给按钮。
// 2026-10-14 手机网页重做（D-MW-01）：全屏从顶栏移进「更多」面板（teleport 到 body），选了就关面板。
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

  // 打开「更多」，返回其中的全屏一项（没有就是 null）。
  async function fullscreenRow(wrapper) {
    await wrapper.get('[data-test="short-feed-more"]').trigger('click');
    return document.body.querySelector('[data-test="short-feed-fullscreen"]');
  }

  async function closeMore() {
    document.body.querySelector('[data-test="short-feed-more-cancel"]').click();
    await flushPromises();
  }

  it('整页进全屏并跟随浏览器状态，卸载时释放监听', async () => {
    Object.defineProperty(document, 'fullscreenEnabled', { configurable: true, value: true });
    const removed = vi.spyOn(document, 'removeEventListener');
    const wrapper = await mountApp();
    const button = await fullscreenRow(wrapper);
    expect(button.textContent.trim()).toBe('全屏播放');
    button.click();
    await flushPromises();
    expect(document.documentElement.requestFullscreen).toHaveBeenCalledWith({ navigationUI: 'hide' });
    expect(document.body.querySelector('.sheet-layer')).toBeNull();
    document.dispatchEvent(new Event('fullscreenchange'));
    await flushPromises();
    const exitRow = await fullscreenRow(wrapper);
    expect(exitRow.textContent.trim()).toBe('退出全屏');
    exitRow.click();
    await flushPromises();
    expect(document.exitFullscreen).toHaveBeenCalledTimes(1);
    document.dispatchEvent(new Event('fullscreenchange'));
    await flushPromises();
    expect((await fullscreenRow(wrapper)).textContent.trim()).toBe('全屏播放');
    await closeMore();
    wrapper.unmount();
    expect(removed.mock.calls.map(([name]) => name)).toContain('fullscreenchange');
  });

  it('没有任何全屏路径时不显示按钮', async () => {
    const wrapper = await mountApp();
    expect(await fullscreenRow(wrapper)).toBeNull();
    await closeMore();
    wrapper.unmount();
  });

  it('iPhone：元数据到了才给按钮，点击走系统播放器全屏，退出事件回写状态', async () => {
    HTMLMediaElement.prototype.webkitEnterFullscreen = vi.fn();
    const wrapper = await mountApp();
    expect(await fullscreenRow(wrapper)).toBeNull();
    await closeMore();
    await wrapper.get('video').trigger('loadedmetadata');
    const button = await fullscreenRow(wrapper);
    button.click();
    await flushPromises();
    expect(HTMLMediaElement.prototype.webkitEnterFullscreen).toHaveBeenCalledTimes(1);
    expect(document.documentElement.requestFullscreen).not.toHaveBeenCalled();
    await wrapper.get('video').trigger('webkitbeginfullscreen');
    expect((await fullscreenRow(wrapper)).textContent.trim()).toBe('退出全屏');
    await closeMore();
    await wrapper.get('video').trigger('webkitendfullscreen');
    expect((await fullscreenRow(wrapper)).textContent.trim()).toBe('全屏播放');
    await closeMore();
    wrapper.unmount();
  });
});
