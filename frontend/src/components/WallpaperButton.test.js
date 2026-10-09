import { mount, flushPromises } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';
const api = vi.hoisted(() => ({ GetWallpaperPreflight: vi.fn(), SetWallpaper: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);
import WallpaperButton from './WallpaperButton.vue';

beforeEach(() => {
  vi.resetAllMocks();
  api.GetWallpaperPreflight.mockResolvedValue({ eligible: true, width: 1920, height: 1080 });
  api.SetWallpaper.mockResolvedValue({ state: 'starting', message: '正在启动动态壁纸…' });
});

describe('WallpaperButton', () => {
  it('shows resolution rejection and never attempts to set low resolution media', async () => {
    api.GetWallpaperPreflight.mockResolvedValue({ eligible: false, reason_code: 'resolution_too_low', message: '素材分辨率 1280×720 不足' });
    const w = mount(WallpaperButton, { props: { kind: 'video', mediaId: 9 } }); await flushPromises();
    expect(w.get('[data-test="wallpaper-set"]').attributes('disabled')).toBeDefined();
    expect(w.text()).toContain('1280×720');
    await w.get('[data-test="wallpaper-set"]').trigger('click');
    expect(api.SetWallpaper).not.toHaveBeenCalled(); w.unmount();
  });

  it.each(['image', 'video'])('sets %s by media ID and refreshes global status', async kind => {
    const changed = vi.fn(); window.addEventListener('wallpaper-changed', changed);
    const w = mount(WallpaperButton, { props: { kind, mediaId: 12 } }); await flushPromises();
    expect(api.GetWallpaperPreflight).toHaveBeenCalledWith(kind, 12);
    await w.get('[data-test="wallpaper-set"]').trigger('click'); await flushPromises();
    expect(api.SetWallpaper).toHaveBeenCalledWith(kind, 12); expect(changed).toHaveBeenCalledTimes(1);
    expect(w.get('[data-test="wallpaper-notice"]').text()).toContain(kind === 'video' ? '正在启动' : '已设为桌面壁纸');
    window.removeEventListener('wallpaper-changed', changed); w.unmount();
  });

  it('shows native failure and allows another explicit attempt', async () => {
    api.SetWallpaper.mockRejectedValueOnce('系统无法解码该素材');
    const w = mount(WallpaperButton, { props: { kind: 'video', mediaId: 1 } }); await flushPromises();
    await w.get('[data-test="wallpaper-set"]').trigger('click'); await flushPromises();
    expect(w.get('[role="alert"]').text()).toContain('系统无法解码');
    expect(w.get('[data-test="wallpaper-set"]').attributes('disabled')).toBeUndefined(); w.unmount();
  });

  it('discards eligibility responses for a previously viewed item', async () => {
    let finish;
    api.GetWallpaperPreflight.mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
    const w = mount(WallpaperButton, { props: { kind: 'image', mediaId: 1 } });
    await w.setProps({ mediaId: 2 }); await flushPromises();
    finish({ eligible: false, message: 'old rejection' }); await flushPromises();
    expect(w.get('[data-test="wallpaper-set"]').attributes('disabled')).toBeUndefined();
    expect(w.text()).not.toContain('old rejection'); w.unmount();
  });

  it('rechecks unknown dimensions after metadata has been refreshed', async () => {
    api.GetWallpaperPreflight.mockResolvedValueOnce({ eligible: false, message: '素材尺寸尚未探测' });
    const w = mount(WallpaperButton, { props: { kind: 'image', mediaId: 1 } }); await flushPromises();
    await w.get('[data-test="wallpaper-recheck"]').trigger('click'); await flushPromises();
    expect(w.get('[data-test="wallpaper-set"]').attributes('disabled')).toBeUndefined(); w.unmount();
  });
});
