import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createThumbnailReader, thumbnailTime } from './videoThumbnail.js';
import { flushPromises } from '@vue/test-utils';
beforeEach(() => {
  Object.defineProperty(URL, 'createObjectURL', { configurable: true, value: vi.fn(() => 'blob:frame') });
  Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, value: vi.fn() });
});
afterEach(() => vi.restoreAllMocks());
const response = () => ({ ok: true, blob: async () => new Blob(['frame']) });

describe('thumbnail preview requests', () => {
  it('maps position to a bounded time, including first/last and unknown duration', () => {
    expect(thumbnailTime(120, 0)).toBe(0); expect(thumbnailTime(120, 0.5)).toBe(60000);
    expect(thumbnailTime(120, 1)).toBe(117500); expect(thumbnailTime(120, -1)).toBe(0);
    expect(thumbnailTime(0, 0.5)).toBeNull(); expect(thumbnailTime(120, NaN)).toBeNull();
  });
  it('coalesces moves to the latest frame without displaying the obsolete response', async () => {
    let first, last; const fetcher = vi.fn().mockImplementationOnce(() => new Promise(resolve => { first = resolve; })).mockImplementationOnce(() => new Promise(resolve => { last = resolve; }));
    const changed = vi.fn(), failed = vi.fn(), reader = createThumbnailReader(changed, failed, fetcher);
    reader.request(1, 1000); reader.request(1, 2000); reader.request(1, 3000);
    expect(fetcher).toHaveBeenCalledTimes(1); first(response()); await flushPromises();
    expect(changed).not.toHaveBeenCalled(); expect(fetcher).toHaveBeenCalledTimes(2);
    expect(fetcher.mock.calls[1][0]).toContain('ms=3000'); last(response()); await flushPromises();
    expect(changed).toHaveBeenCalledWith('blob:frame', 3000); expect(failed).not.toHaveBeenCalled();
    reader.cancel(); expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:frame');
  });
  it('aborts and discards a late response after leaving the card', async () => {
    let resolve; const fetcher = vi.fn(() => new Promise(done => { resolve = done; })); const changed = vi.fn();
    const reader = createThumbnailReader(changed, vi.fn(), fetcher); reader.request(1, 2000); const signal = fetcher.mock.calls[0][1].signal; reader.cancel();
    expect(signal.aborted).toBe(true); resolve(response()); await flushPromises();
    expect(changed).not.toHaveBeenCalled(); expect(URL.createObjectURL).not.toHaveBeenCalled();
  });
  it('reports read failure rather than publishing a fake frame', async () => {
    const changed = vi.fn(), failed = vi.fn(), reader = createThumbnailReader(changed, failed, async () => ({ ok: false }));
    reader.request(1, 0); await flushPromises(); expect(changed).not.toHaveBeenCalled(); expect(failed).toHaveBeenCalledTimes(1); reader.cancel();
  });
});
