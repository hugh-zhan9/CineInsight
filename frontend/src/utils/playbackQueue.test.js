import { beforeEach, expect, it, vi } from 'vitest';
const api = vi.hoisted(() => ({ GetPlaybackQueue: vi.fn(), EditPlaybackQueue: vi.fn(), PlayPlaybackQueue: vi.fn() }));
const confirm = vi.hoisted(() => vi.fn());
vi.mock('../../wailsjs/go/main/App', () => api);
vi.mock('./feedback.js', () => ({ confirmAction: confirm }));
import { addToPlaybackQueue } from './playbackQueue.js';
beforeEach(() => { vi.clearAllMocks(); confirm.mockResolvedValue(true); api.GetPlaybackQueue.mockResolvedValue({ state: { revision: 8 }, total: 5, items: [{ id: 101 }] }); });
it('appends explicit duplicates in order without starting playback', async () => {
  await addToPlaybackQueue({ videoIDs: [4, 4, 8] });
  expect(api.EditPlaybackQueue).toHaveBeenCalledWith({ action: 'append_videos', expected_revision: 8, video_ids: [4, 4, 8], collection_id: 0, start_video_id: 0, start_after: false });
  expect(api.PlayPlaybackQueue).not.toHaveBeenCalled(); expect(confirm).not.toHaveBeenCalled();
});
it('cancelled replacement neither edits nor plays', async () => {
  confirm.mockResolvedValue(false); expect(await addToPlaybackQueue({ collectionID: 2, startVideoID: 5, replace: true })).toBe(false);
  expect(confirm.mock.calls[0][0].message).toContain('5 项'); expect(api.EditPlaybackQueue).not.toHaveBeenCalled();
});
it('replacement starts only the revision just committed and rejects a competing edit', async () => {
  api.GetPlaybackQueue.mockResolvedValueOnce({ state: { revision: 8 }, total: 5 });
  api.GetPlaybackQueue.mockRejectedValueOnce(new Error('queue_changed'));
  await expect(addToPlaybackQueue({ collectionID: 2, startVideoID: 5, replace: true })).rejects.toThrow('queue_changed');
  expect(api.GetPlaybackQueue).toHaveBeenLastCalledWith({ revision: 9, limit: 1 }); expect(api.PlayPlaybackQueue).not.toHaveBeenCalled();
});
it('after replacement, uses the frozen first entry and its revision', async () => {
  api.GetPlaybackQueue.mockResolvedValueOnce({ state: { revision: 8 }, total: 5 });
  api.GetPlaybackQueue.mockResolvedValueOnce({ state: { revision: 9 }, items: [{ id: 101 }] });
  await addToPlaybackQueue({ collectionID: 2, startVideoID: 5, replace: true });
  expect(api.PlayPlaybackQueue).toHaveBeenCalledWith(101, 9);
});
