import { beforeEach, describe, expect, it, vi } from 'vitest';
const api = vi.hoisted(() => ({ PlayVideo: vi.fn(), UpdateVideoWatchProgress: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);
import { playVideoAfterProgress, saveVideoWatchProgress, waitForVideoWatchProgress } from './watchProgress.js';
const tick = () => new Promise(resolve => setTimeout(resolve, 0));
beforeEach(() => vi.resetAllMocks());

describe('跨宿主的观看进度保存', () => {
  it('新宿主发起播放时等待旧宿主的保存，同视频按上报顺序写入', async () => {
    let finish;
    api.UpdateVideoWatchProgress.mockReturnValueOnce(new Promise(resolve => { finish = resolve; })).mockResolvedValueOnce({ id: 1, watch_position_seconds: 43 });
    const first = saveVideoWatchProgress(1, 42, 600, false, 'resume');
    const second = saveVideoWatchProgress(1, 43, 600, false, 'resume');
    const playing = playVideoAfterProgress(1);
    await tick();
    expect(api.UpdateVideoWatchProgress).toHaveBeenCalledTimes(1);
    expect(api.PlayVideo).not.toHaveBeenCalled();
    finish({ id: 1, watch_position_seconds: 42 });
    await Promise.all([first, second, playing]);
    expect(api.UpdateVideoWatchProgress.mock.calls.map(call => call[1])).toEqual([42, 43]);
    expect(api.PlayVideo).toHaveBeenCalledWith(1);
  });

  it('当次保存失败停止播放，后续明确播放重试失败保存而不是锁死', async () => {
    let fail;
    api.UpdateVideoWatchProgress.mockReturnValueOnce(new Promise((_, reject) => { fail = reject; }));
    const saving = saveVideoWatchProgress(2, 42, 600, false, 'resume');
    const playing = playVideoAfterProgress(2);
    const saveError = expect(saving).rejects.toThrow('保存失败');
    const playError = expect(playing).rejects.toThrow('保存失败');
    fail(new Error('保存失败'));
    await Promise.all([saveError, playError]);
    expect(api.UpdateVideoWatchProgress).toHaveBeenCalledTimes(1);
    expect(api.PlayVideo).not.toHaveBeenCalled();
    api.UpdateVideoWatchProgress.mockResolvedValue({ id: 2, watch_position_seconds: 42 });
    await playVideoAfterProgress(2);
    expect(api.UpdateVideoWatchProgress).toHaveBeenLastCalledWith(2, 42, 600, false, 'resume');
    expect(api.UpdateVideoWatchProgress).toHaveBeenCalledTimes(2);
    expect(api.PlayVideo).toHaveBeenCalledTimes(1);
    await waitForVideoWatchProgress(2);
    expect(api.UpdateVideoWatchProgress).toHaveBeenCalledTimes(2); // 成功的请求已释放，不重写。
  });

  it('不同视频互不等待，较新上报取代先前失败的请求', async () => {
    let finish;
    api.UpdateVideoWatchProgress.mockReturnValueOnce(new Promise(resolve => { finish = resolve; }));
    const saving = saveVideoWatchProgress(3, 12, 600, false, 'resume');
    await playVideoAfterProgress(4);
    expect(api.PlayVideo).toHaveBeenCalledWith(4);
    finish({ id: 3 }); await saving;
    api.UpdateVideoWatchProgress.mockRejectedValueOnce(new Error('保存失败'));
    await expect(saveVideoWatchProgress(3, 20, 600, false, 'resume')).rejects.toThrow('保存失败');
    api.UpdateVideoWatchProgress.mockResolvedValue({ id: 3 });
    await saveVideoWatchProgress(3, 0, 600, false, 'resume');
    await waitForVideoWatchProgress(3);
    expect(api.UpdateVideoWatchProgress).toHaveBeenLastCalledWith(3, 0, 600, false, 'resume');
    expect(api.UpdateVideoWatchProgress).toHaveBeenCalledTimes(3);
  });
});
