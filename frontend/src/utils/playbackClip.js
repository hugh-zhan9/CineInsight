// Null means no valid clip range; callers must never treat a malformed clip as a video.
export function queueClipRange(session) {
  if (session?.entry_kind !== 'clip') return null;
  const startMS = session.clip_start_ms, endMS = session.clip_end_ms;
  if (!Number.isSafeInteger(startMS) || !Number.isSafeInteger(endMS) || startMS < 0 || endMS <= startMS) return null;
  return { startMS, endMS };
}
export function clampClipSeconds(value, range) {
  return Number.isFinite(value) && range ? Math.max(range.startMS / 1000, Math.min(range.endMS / 1000, value)) : NaN;
}

export function clipTime(ms) { return `${(Number(ms) / 1000).toFixed(3)} 秒`; }
