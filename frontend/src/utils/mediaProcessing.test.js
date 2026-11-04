import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
const api = vi.hoisted(() => ({ StartMediaProcessing: vi.fn(), GetMediaProcessing: vi.fn(), ReleaseMediaProcessing: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);
import { createMediaProcessingController } from './mediaProcessing.js';
beforeEach(() => { vi.useFakeTimers(); api.StartMediaProcessing.mockReset(); api.GetMediaProcessing.mockReset(); api.ReleaseMediaProcessing.mockReset().mockResolvedValue(undefined); });
afterEach(() => vi.useRealTimers());

describe('temporary media controller', () => {
  it('waits for an in-flight start and its actual release before finishing close', async () => {
    let resolve, released; api.StartMediaProcessing.mockImplementationOnce(() => new Promise(done => { resolve = done; }));
    api.ReleaseMediaProcessing.mockImplementationOnce(() => new Promise(done => { released = done; }));
    const changed = vi.fn(), controller = createMediaProcessingController(changed);
    const start = controller.start({ operation: 'inspect' }); await Promise.resolve();
    const stopped = vi.fn(), closing = controller.close().then(stopped);
    await Promise.resolve(); expect(stopped).not.toHaveBeenCalled();
    resolve({ id: 'late', state: 'ready' });
    expect(await start).toBeNull(); await Promise.resolve(); await Promise.resolve(); expect(changed).not.toHaveBeenCalled(); expect(api.ReleaseMediaProcessing).toHaveBeenCalledWith('late');
    expect(stopped).not.toHaveBeenCalled(); released(); await closing; expect(stopped).toHaveBeenCalledTimes(1);
    expect(await controller.start({})).toBeNull(); expect(api.StartMediaProcessing).toHaveBeenCalledTimes(1);
  });
  it('keeps a late-start release failure visible and retains the id for close retry', async () => {
    let resolve; api.StartMediaProcessing.mockImplementationOnce(() => new Promise(done => { resolve = done; }));
    api.ReleaseMediaProcessing.mockRejectedValueOnce(new Error('late stop failed'));
    const controller = createMediaProcessingController(vi.fn()); const start = controller.start({}); await Promise.resolve();
    const closing = controller.close(); const rejected = expect(closing).rejects.toThrow('late stop failed');
    resolve({ id: 'late-failure', state: 'running' }); await start; await rejected;
    await controller.close(); expect(api.ReleaseMediaProcessing).toHaveBeenCalledTimes(2);
  });
  it('polls a single job to completion without starting duplicate work', async () => {
    api.StartMediaProcessing.mockResolvedValue({ id: 'one', state: 'queued' });
    api.GetMediaProcessing.mockResolvedValueOnce({ id: 'one', state: 'running' }).mockResolvedValueOnce({ id: 'one', state: 'ready' });
    const changed = vi.fn(), controller = createMediaProcessingController(changed); await controller.start({});
    await vi.advanceTimersByTimeAsync(750);
    expect(api.StartMediaProcessing).toHaveBeenCalledTimes(1); expect(api.GetMediaProcessing).toHaveBeenCalledTimes(2);
    expect(changed.mock.calls.map(([view]) => view.state)).toEqual(['queued', 'running', 'ready']);
    await controller.close(); expect(api.ReleaseMediaProcessing).toHaveBeenCalledWith('one');
  });
  it('retains failed releases for an explicit stop retry', async () => {
    api.StartMediaProcessing.mockResolvedValue({ id: 'one', state: 'ready' }); const controller = createMediaProcessingController(() => {}); await controller.start({});
    api.ReleaseMediaProcessing.mockRejectedValueOnce(new Error('still stopping'));
    await expect(controller.close()).rejects.toThrow('still stopping'); await controller.close();
    expect(api.ReleaseMediaProcessing).toHaveBeenCalledTimes(2);
  });
});
