export const SWIPE_THRESHOLD_PX = 54;
export const WHEEL_THRESHOLD_PX = 72;
export const WHEEL_COOLDOWN_MS = 420;

export function createSwipeTracker(threshold = SWIPE_THRESHOLD_PX) {
  let startX = 0;
  let startY = 0;

  return {
    start(event) {
      const touch = event.touches?.[0] || event.changedTouches?.[0];
      if (!touch) return;
      startX = touch.clientX;
      startY = touch.clientY;
    },
    end(event) {
      const touch = event.changedTouches?.[0] || event.touches?.[0];
      if (!touch) return 0;
      const dx = touch.clientX - startX;
      const dy = touch.clientY - startY;
      if (Math.abs(dy) < threshold || Math.abs(dy) <= Math.abs(dx)) return 0;
      return dy < 0 ? 1 : -1;
    }
  };
}

export function wheelDirection(deltaY, now, state, threshold = WHEEL_THRESHOLD_PX, cooldown = WHEEL_COOLDOWN_MS) {
  if (Math.abs(deltaY) < threshold) return 0;
  if (now - state.lastWheelAt < cooldown) return 0;
  state.lastWheelAt = now;
  return deltaY > 0 ? 1 : -1;
}

export function keyboardDirection(key) {
  if (key === 'ArrowDown' || key === 'PageDown' || key === ' ') return 1;
  if (key === 'ArrowUp' || key === 'PageUp') return -1;
  return 0;
}
