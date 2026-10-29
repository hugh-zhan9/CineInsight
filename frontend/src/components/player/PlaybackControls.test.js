import { mount, flushPromises } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vitest';
import Controls from './PlaybackControls.vue';
import { isTopModalLayer } from '../ui/modalSignals.js';
let wrapper, dialog, video, surface;
async function setup() {
  dialog = document.createElement('dialog'); dialog.open = true;
  surface = document.createElement('div'); surface.tabIndex=0; video = document.createElement('video');
  surface.appendChild(video); dialog.appendChild(surface); document.body.appendChild(dialog);
  Object.defineProperty(video, 'duration', { configurable: true, value: 120 });
  Object.defineProperty(video, 'paused', { configurable: true, writable: true, value: true });
  video.currentTime = 30; video.muted = true;
  video.play = vi.fn(async () => { video.paused = false; video.dispatchEvent(new Event('play')); });
  video.pause = vi.fn(() => { video.paused = true; video.dispatchEvent(new Event('pause')); });
  dialog.showModal = vi.fn(() => { expect(dialog.open).toBe(false); dialog.open = true; });
  dialog.close = vi.fn(() => { dialog.open = false; });
  wrapper = mount(Controls, { attachTo: surface, props: { videoElement: () => video, dialogElement: () => dialog, surfaceElement: () => surface }, global: { stubs: { BasePopover: { template: '<div><slot /></div>' } } } });
  await flushPromises(); return wrapper;
}
afterEach(() => { wrapper?.unmount(); wrapper = null; dialog?.remove(); vi.restoreAllMocks(); delete document.fullscreenElement; delete document.exitFullscreen; });

describe('PlaybackControls', () => {
  it('uses actual media time, seeks where requested and changes playback and sound', async () => {
    const w = await setup();
    expect(w.get('[data-test="playback-seek"]').element.value).toBe('30');
    await w.get('[data-test="playback-seek"]').setValue('60'); expect(video.currentTime).toBe(60);
    await w.get('[data-test="playback-seek"]').trigger('keydown', { key: 'ArrowRight' }); expect(video.currentTime).toBe(65);
    video.volume = 0.5; await w.get('[data-test="playback-seek"]').trigger('keydown', { key: 'ArrowUp' }); expect(video.volume).toBe(0.55);
    await w.get('[data-test="playback-toggle"]').trigger('click'); expect(video.play).toHaveBeenCalledOnce();
    await w.get('[data-test="playback-toggle"]').trigger('click'); expect(video.pause).toHaveBeenCalledOnce();
    await w.get('[data-test="playback-volume"]').setValue('0.4'); expect(video.volume).toBe(0.4); expect(video.muted).toBe(false);
  });
  it('one fullscreen entry offers both modes; app fullscreen preserves media and Esc returns inline', async () => {
    const w = await setup(); const original = video;
    expect(w.findAll('[data-test="inline-player-fullscreen"]')).toHaveLength(1);
    await w.get('[data-test="inline-player-fullscreen"]').trigger('click');
    expect(w.find('[data-test="player-system-fullscreen"]').exists()).toBe(true);
    await w.get('[data-test="player-app-fullscreen"]').trigger('click');
    expect(dialog.showModal).toHaveBeenCalledOnce(); expect(w.vm.appFullscreen).toBe(true); expect(isTopModalLayer(w.vm)).toBe(true); expect(document.activeElement).toBe(surface);
    const key = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true });
    window.dispatchEvent(key); await flushPromises();
    expect(key.defaultPrevented).toBe(true); expect(w.vm.activeFullscreen).toBe(false); expect(dialog.open).toBe(true);
    expect(video).toBe(original); expect(video.currentTime).toBe(30);
  });
  it('system fullscreen requests the inner surface, reports rejection and leaves another fullscreen owner alone', async () => {
    const w = await setup(); let current = null;
    Object.defineProperty(document, 'fullscreenElement', { configurable: true, get: () => current });
    surface.requestFullscreen = vi.fn(async () => { current = surface; document.dispatchEvent(new Event('fullscreenchange')); });
    document.exitFullscreen = vi.fn(async () => { current = null; document.dispatchEvent(new Event('fullscreenchange')); });
    await w.vm.enterSystemFullscreen(); expect(w.vm.systemFullscreen).toBe(true);
    await w.vm.leaveFullscreen(); expect(document.exitFullscreen).toHaveBeenCalledOnce();
    surface.requestFullscreen.mockRejectedValueOnce(new Error('denied')); await w.vm.enterSystemFullscreen();
    expect(w.get('[role="alert"]').text()).toContain('无法切换全屏'); expect(w.vm.activeFullscreen).toBe(false);
    current = document.body; w.unmount(); wrapper = null; expect(document.exitFullscreen).toHaveBeenCalledOnce();
  });
});
