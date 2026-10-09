import { mount, flushPromises } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
const api = vi.hoisted(() => ({ GetWallpaperStatus: vi.fn(), StopVideoWallpaper: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);
import WallpaperStatusBar from './WallpaperStatusBar.vue';
beforeEach(() => {
  vi.useFakeTimers(); vi.resetAllMocks();
  api.GetWallpaperStatus.mockResolvedValue({ state: 'playing', video_id: 8, display_name: 'sample.mp4', message: '无声循环播放中' });
  api.StopVideoWallpaper.mockResolvedValue({ state: 'idle' });
});
afterEach(() => vi.useRealTimers());

describe('WallpaperStatusBar', () => {
  it('keeps a global stop action and removes its polling when unmounted', async () => {
    const w = mount(WallpaperStatusBar); await flushPromises();
    expect(w.text()).toContain('sample.mp4');
    await w.get('[data-test="wallpaper-stop"]').trigger('click'); await flushPromises();
    expect(api.StopVideoWallpaper).toHaveBeenCalledOnce(); expect(w.find('[data-test="wallpaper-status"]').exists()).toBe(false);
    w.unmount(); const calls = api.GetWallpaperStatus.mock.calls.length;
    await vi.advanceTimersByTimeAsync(4000); window.dispatchEvent(new Event('wallpaper-changed')); await flushPromises();
    expect(api.GetWallpaperStatus).toHaveBeenCalledTimes(calls);
  });

  it('updates to asynchronous decode failure and lets the user dismiss it', async () => {
    const w = mount(WallpaperStatusBar); await flushPromises();
    api.GetWallpaperStatus.mockResolvedValue({ state: 'failed', display_name: 'sample.mp4', message: '动态壁纸播放失败' });
    await vi.advanceTimersByTimeAsync(2000); await flushPromises();
    expect(w.text()).toContain('播放失败'); expect(w.get('[data-test="wallpaper-stop"]').text()).toBe('关闭提示'); w.unmount();
  });

  it('retains the stop action when the backend refuses to stop', async () => {
    api.StopVideoWallpaper.mockRejectedValueOnce('desktop stalled');
    const w = mount(WallpaperStatusBar); await flushPromises();
    await w.get('[data-test="wallpaper-stop"]').trigger('click'); await flushPromises();
    expect(w.get('[role="alert"]').text()).toContain('停止动态壁纸失败'); expect(w.text()).toContain('sample.mp4'); w.unmount();
  });

  it('does not revive a stopped wallpaper from an older status response', async () => {
    const w = mount(WallpaperStatusBar); await flushPromises(); let finish;
    api.GetWallpaperStatus.mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
    window.dispatchEvent(new Event('wallpaper-changed')); await flushPromises();
    await w.get('[data-test="wallpaper-stop"]').trigger('click'); await flushPromises();
    finish({ state: 'playing', display_name: 'old' }); await flushPromises();
    expect(w.find('[data-test="wallpaper-status"]').exists()).toBe(false); w.unmount();
  });
});
