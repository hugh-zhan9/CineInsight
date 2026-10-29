import { mount, flushPromises } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vitest';
import Controls from './PlaybackControls.vue';
import { isTopModalLayer } from '../ui/modalSignals.js';
let wrapper, dialog, video, surface;
async function setup(realPopover = false) {
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
  wrapper = mount(Controls, { attachTo: surface, props: { videoElement: () => video, dialogElement: () => dialog, surfaceElement: () => surface }, global: { stubs: realPopover ? {} : { BasePopover: { template: '<div><slot /></div>' } } } });
  await flushPromises(); return wrapper;
}
afterEach(() => { wrapper?.unmount(); wrapper = null; dialog?.remove(); vi.restoreAllMocks(); delete document.fullscreenElement; delete document.exitFullscreen; });

describe('PlaybackControls', () => {
  it('has one library timeline and keeps volume collapsed until requested', async () => {
    const w = await setup();
    expect(w.findAll('media-time-range')).toHaveLength(1);
    expect(w.find('input[type=range]').exists()).toBe(false);
    expect(w.find('[data-test="playback-volume"]').exists()).toBe(false);
    surface.setAttribute('userinactive','');
    await w.get('[data-test="playback-volume-toggle"]').trigger('click'); await flushPromises();
    expect(surface.hasAttribute('userinactive')).toBe(false);
    expect(w.get('[data-test="playback-volume"]').element.tagName).toBe('MEDIA-VOLUME-RANGE');
    expect(surface.getAttribute('autohide')).toBe('-1');
    await w.get('[data-test="playback-mute"]').trigger('click'); expect(video.muted).toBe(false);
    await w.vm.togglePlayback(); expect(video.play).toHaveBeenCalledOnce();
    await w.vm.togglePlayback(); expect(video.pause).toHaveBeenCalledOnce();
    await w.get('[data-test="playback-volume-toggle"]').trigger('click');
    expect(surface.getAttribute('autohide')).toBe('2');
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
  it('closes a settings popover with Escape before leaving application fullscreen', async () => {
    const w = await setup(true); w.vm.enterAppFullscreen(); await flushPromises();
    await w.get('[data-test="playback-volume-toggle"]').trigger('click'); await flushPromises();
    await w.get('.base-popover').trigger('keydown', {key:'Escape'}); await flushPromises();
    expect(w.vm.volumeOpen).toBe(false); expect(w.vm.appFullscreen).toBe(true);
    expect(surface.getAttribute('autohide')).toBe('2');
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
