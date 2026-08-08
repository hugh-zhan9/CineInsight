// 播放期间保持屏幕常亮。与 DOM 无关，独立出来后父组件不必再持有锁的生命周期细节。
export function createWakeLock() {
  let lock = null;

  async function request() {
    if (lock || !navigator?.wakeLock?.request) return;
    try {
      lock = await navigator.wakeLock.request('screen');
      lock.addEventListener?.('release', () => {
        lock = null;
      });
    } catch (err) {
      // 不支持或被拒绝：常亮只是锦上添花，失败不影响播放。
    }
  }

  async function release() {
    const current = lock;
    lock = null;
    try {
      await current?.release?.();
    } catch (err) {
      // 释放失败无需处理：页面卸载后系统会自行回收。
    }
  }

  return { request, release, held: () => lock !== null };
}
