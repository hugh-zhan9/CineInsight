import { flushPromises } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ RequestPreviewProxy: vi.fn(), CancelPreviewProxy: vi.fn(), CreatePlaybackProxy: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);

import { createPreviewProxyFollower } from './previewProxy.js';

const queued = id => ({ running: true, queued: 1, queued_video_ids: [id], current_video_id: 99, results: [] });

beforeEach(() => {
  vi.clearAllMocks();
  window.runtime = { EventsOn: vi.fn(() => () => {}) };
  api.CancelPreviewProxy.mockResolvedValue(true);
});
afterEach(() => { delete window.runtime; });

describe('createPreviewProxyFollower（2026-10-13 第三轮评审）', () => {
  it('撤销后马上再跟同一部：迟到的撤销只撤第一次的令牌', async () => {
    let resolveFirst;
    api.RequestPreviewProxy
      .mockImplementationOnce(id => new Promise(resolve => { resolveFirst = () => resolve(queued(id)); }))
      .mockImplementation(id => Promise.resolve(queued(id)));
    const follower = createPreviewProxyFollower({ kind: 'test', onState: () => {}, onReady: () => {} });
    const session = { video_id: 4, mode: 'proxy-preparing' };
    follower.follow(session);
    follower.stop();
    follower.follow(session);
    await flushPromises();
    resolveFirst();
    await flushPromises();
    const [first, second] = api.RequestPreviewProxy.mock.calls.map(([, owner]) => owner);
    expect(first).not.toBe(second);
    expect(api.CancelPreviewProxy.mock.calls).toEqual([[4, first]]);
    follower.stop();
    await flushPromises();
    expect(api.CancelPreviewProxy.mock.calls.at(-1)).toEqual([4, second]);
  });

  it('只在 proxy-preparing 时自动请求；已在跟同一部时不重复', async () => {
    api.RequestPreviewProxy.mockImplementation(id => Promise.resolve(queued(id)));
    const follower = createPreviewProxyFollower({ kind: 'test', onState: () => {}, onReady: () => {} });
    follower.follow({ video_id: 4, mode: 'proxy-manual' });
    follower.follow({ video_id: 4, mode: 'inline' });
    expect(api.RequestPreviewProxy).not.toHaveBeenCalled();
    follower.follow({ video_id: 4, mode: 'proxy-preparing' });
    follower.follow({ video_id: 4, mode: 'proxy-preparing' });
    await flushPromises();
    expect(api.RequestPreviewProxy).toHaveBeenCalledTimes(1);
    follower.stop();
  });
});
