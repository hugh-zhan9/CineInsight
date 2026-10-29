import { describe, it, expect, vi } from 'vitest';
import { handlePlaybackKey } from './playbackKeyboard.js';

describe('playback keyboard', () => {
  it('seeks and changes volume within media limits, space toggles playback', () => {
    const video = { duration: 10, currentTime: 2, volume: 0.98, muted: true, paused: true, play: vi.fn(), pause: vi.fn() };
    const press = key => { const event = new KeyboardEvent('keydown', { key, cancelable: true }); handlePlaybackKey(event, video); expect(event.defaultPrevented).toBe(true); };
    press('ArrowLeft'); expect(video.currentTime).toBe(0);
    press('ArrowRight'); expect(video.currentTime).toBe(5);
    press('ArrowRight'); press('ArrowRight'); expect(video.currentTime).toBe(10);
    press('ArrowUp'); expect(video.volume).toBe(1); expect(video.muted).toBe(false);
    video.volume = 0.01; press('ArrowDown'); expect(video.volume).toBe(0);
    press(' '); expect(video.play).toHaveBeenCalledOnce();
    video.paused = false; press(' '); expect(video.pause).toHaveBeenCalledOnce();
  });
  it('does not hijack input controls or modified keys', () => {
    const video = { currentTime: 10, duration: 20 };
    const input = document.createElement('input');
    const event = { key: 'ArrowRight', target: input, preventDefault: vi.fn(), stopPropagation: vi.fn() };
    handlePlaybackKey(event, video);
    handlePlaybackKey({ ...event, target: null, metaKey: true }, video);
    expect(video.currentTime).toBe(10); expect(event.preventDefault).not.toHaveBeenCalled();
  });
});
