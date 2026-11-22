export const DEFAULT_DISCOVER_VIDEO_COUNT = 12;
export const MIN_DISCOVER_VIDEO_COUNT = 1;
export const MAX_DISCOVER_VIDEO_COUNT = 36;

export function normalizeDiscoverVideoCount(value) {
  const number = Number(value);
  if (!Number.isFinite(number) || number < MIN_DISCOVER_VIDEO_COUNT) return DEFAULT_DISCOVER_VIDEO_COUNT;
  return Math.min(MAX_DISCOVER_VIDEO_COUNT, Math.trunc(number));
}
