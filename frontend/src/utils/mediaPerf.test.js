import { expect, it, vi } from 'vitest';
import { observeMediaLoad } from './mediaPerf.js';

it('measures only slow local media, limits repeats, and never forwards identifying URLs', () => {
  let listener;
  const log = vi.fn();
  const enabled = vi.fn(() => false);
  const lookup = vi.fn(() => [{ startTime: 700, responseEnd: 950 }]);
  const win = {
    document: { baseURI: 'wails://wails/index.html' },
    performance: { now: () => 1000, getEntriesByName: lookup },
    addEventListener: vi.fn((_, fn) => { listener = fn; }),
    removeEventListener: vi.fn()
  };
  const stop = observeMediaLoad(win, enabled, log);
  const emit = (src) => listener({ target: { tagName: 'IMG', currentSrc: src } });
  emit('wails://wails/preview/image-thumbnail/123?secret=path');
  expect(lookup).not.toHaveBeenCalled();
  enabled.mockReturnValue(true);
  emit('https://example.test/preview/image/123');
  expect(lookup).not.toHaveBeenCalled();
  emit('wails://wails/preview/image-thumbnail/123?secret=path');
  emit('wails://wails/preview/image-thumbnail/456');
  expect(log).toHaveBeenCalledExactlyOnceWith('perf component=webview_media stage=photo_thumbnail_load_observed duration_ms=300 status=ok');
  lookup.mockReturnValue([]);
  emit('wails://wails/preview/image/234');
  expect(log).toHaveBeenCalledTimes(1);
  stop();
  expect(win.removeEventListener).toHaveBeenCalledWith('load', listener, true);
});
