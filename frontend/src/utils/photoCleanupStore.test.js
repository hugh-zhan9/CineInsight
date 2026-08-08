import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';

vi.mock('../../wailsjs/go/main/App', () => ({
  GetImageCleanupStatus: vi.fn()
}));

import { GetImageCleanupStatus } from '../../wailsjs/go/main/App';
import {
  photoCleanupStore,
  startPhotoCleanupPolling,
  stopPhotoCleanupPolling,
  resumePhotoCleanupPolling
} from './photoCleanupStore.js';

const running = () => ({ running: true, completed: false, error: '', progress: { current: 1, total: 10 }, analysis: null });
const completed = () => ({ running: false, completed: true, error: '', progress: { stage: 'done' }, stale: false, analysis: { duplicate_groups: [], near_duplicate_groups: [] } });

beforeEach(() => {
  vi.useFakeTimers();
  vi.clearAllMocks();
  stopPhotoCleanupPolling();
  photoCleanupStore.status = null;
});

afterEach(() => {
  stopPhotoCleanupPolling();
  vi.useRealTimers();
});

describe('photoCleanupStore', () => {
  it('polls while an analysis is running', async () => {
    GetImageCleanupStatus.mockResolvedValue(running());

    startPhotoCleanupPolling();
    await vi.advanceTimersByTimeAsync(0);
    expect(GetImageCleanupStatus).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(3000);
    expect(GetImageCleanupStatus).toHaveBeenCalledTimes(4);
  });

  // 空闲时继续轮询会用新对象覆盖 status，把用户正在审阅的勾选/保留项/折叠状态重置掉。
  it('stops polling once the analysis is no longer running', async () => {
    GetImageCleanupStatus.mockResolvedValue(completed());

    startPhotoCleanupPolling();
    await vi.advanceTimersByTimeAsync(0);
    expect(GetImageCleanupStatus).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(60000);
    expect(GetImageCleanupStatus).toHaveBeenCalledTimes(1);
    expect(photoCleanupStore.status.completed).toBe(true);
  });

  it('stops polling after a running analysis finishes', async () => {
    GetImageCleanupStatus.mockResolvedValueOnce(running()).mockResolvedValue(completed());

    startPhotoCleanupPolling();
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(1000);
    expect(GetImageCleanupStatus).toHaveBeenCalledTimes(2);

    await vi.advanceTimersByTimeAsync(60000);
    expect(GetImageCleanupStatus).toHaveBeenCalledTimes(2);
  });

  it('resumes polling when a new analysis is started', async () => {
    GetImageCleanupStatus.mockResolvedValue(completed());
    startPhotoCleanupPolling();
    await vi.advanceTimersByTimeAsync(0);
    expect(GetImageCleanupStatus).toHaveBeenCalledTimes(1);

    // 面板启动新分析：直接写入 running 状态并唤醒轮询。
    GetImageCleanupStatus.mockResolvedValue(running());
    photoCleanupStore.status = running();
    resumePhotoCleanupPolling();

    await vi.advanceTimersByTimeAsync(1000);
    expect(GetImageCleanupStatus).toHaveBeenCalledTimes(2);
  });
});
