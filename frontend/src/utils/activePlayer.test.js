import { afterEach, describe, expect, it, vi } from 'vitest';
import { claimActivePlayer, currentActivePlayer, releaseActivePlayer } from './activePlayer.js';

const player = () => ({ pause: vi.fn() });

describe('activePlayer（D-UI-11 同一时间只有一个内嵌播放器在播）', () => {
  afterEach(() => releaseActivePlayer(currentActivePlayer()));

  it('第一个开始播放的登记为当前播放者，不暂停任何人', () => {
    const a = player();
    claimActivePlayer(a);
    expect(currentActivePlayer()).toBe(a);
    expect(a.pause).not.toHaveBeenCalled();
  });

  it('另一个开始播放时暂停上一个，并换成新的', () => {
    const a = player(); const b = player();
    claimActivePlayer(a);
    claimActivePlayer(b);
    expect(a.pause).toHaveBeenCalledTimes(1);
    expect(b.pause).not.toHaveBeenCalled();
    expect(currentActivePlayer()).toBe(b);
  });

  it('同一个再次开始播放不会暂停自己', () => {
    const a = player();
    claimActivePlayer(a);
    claimActivePlayer(a);
    expect(a.pause).not.toHaveBeenCalled();
  });

  it('注销后下一个开始播放不再暂停已注销的；注销别人不影响当前播放者', () => {
    const a = player(); const b = player(); const c = player();
    claimActivePlayer(a);
    releaseActivePlayer(b);
    expect(currentActivePlayer()).toBe(a);
    releaseActivePlayer(a);
    expect(currentActivePlayer()).toBeNull();
    claimActivePlayer(c);
    expect(a.pause).not.toHaveBeenCalled();
    expect(currentActivePlayer()).toBe(c);
  });

  it('空句柄不登记', () => {
    const a = player();
    claimActivePlayer(a);
    claimActivePlayer(null);
    expect(currentActivePlayer()).toBe(a);
    expect(a.pause).not.toHaveBeenCalled();
  });
});
