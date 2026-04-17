export function createHeightCacheKey(videoId, widthBucket, subtitleMode) {
  return `${videoId}:${widthBucket}:${subtitleMode ? 1 : 0}`;
}

export function estimateVideoRowHeight(video, widthBucket, subtitleMode) {
  const baseHeight = 150;
  const tags = Array.isArray(video?.tags) ? video.tags.length : 0;
  const tagsPerRow = widthBucket >= 14 ? 5 : widthBucket >= 10 ? 4 : 3;
  const extraTagRows = Math.max(0, Math.ceil(Math.max(tags, 1) / tagsPerRow) - 1);
  const tagExtra = extraTagRows * 28;
  const subtitleExtra = subtitleMode && video?._subtitleMatchText ? 42 : 0;
  const staleExtra = video?.is_stale ? 18 : 0;
  return baseHeight + tagExtra + subtitleExtra + staleExtra;
}

export function getWidthBucket(width) {
  const safeWidth = Number(width) || 0;
  return Math.max(1, Math.round(safeWidth / 80));
}

export function sumHeights(items, endExclusive, getItemHeight) {
  let total = 0;
  for (let index = 0; index < endExclusive; index += 1) {
    total += getItemHeight(items[index], index);
  }
  return total;
}

export function calculateVirtualWindow({
  items,
  scrollTop,
  viewportHeight,
  listTop,
  overscan,
  getItemHeight
}) {
  const count = Array.isArray(items) ? items.length : 0;
  if (count === 0) {
    return {
      startIndex: 0,
      endIndex: 0,
      topSpacer: 0,
      bottomSpacer: 0,
      totalHeight: 0
    };
  }

  const normalizedOverscan = Math.max(0, overscan || 0);
  const relativeTop = Math.max(0, scrollTop - listTop);
  const relativeBottom = Math.max(relativeTop, scrollTop + viewportHeight - listTop);

  let runningTop = 0;
  let visibleStart = 0;
  while (visibleStart < count) {
    const height = getItemHeight(items[visibleStart], visibleStart);
    if (runningTop + height > relativeTop) {
      break;
    }
    runningTop += height;
    visibleStart += 1;
  }

  let visibleEnd = visibleStart;
  let runningBottom = runningTop;
  while (visibleEnd < count) {
    runningBottom += getItemHeight(items[visibleEnd], visibleEnd);
    visibleEnd += 1;
    if (runningBottom >= relativeBottom) {
      break;
    }
  }

  const startIndex = Math.max(0, visibleStart - normalizedOverscan);
  const endIndex = Math.min(count, visibleEnd + normalizedOverscan);
  const topSpacer = sumHeights(items, startIndex, getItemHeight);
  const visibleHeight = sumHeights(items.slice(startIndex, endIndex), items.slice(startIndex, endIndex).length, (item, index) =>
    getItemHeight(item, startIndex + index)
  );
  const totalHeight = sumHeights(items, count, getItemHeight);

  return {
    startIndex,
    endIndex,
    topSpacer,
    bottomSpacer: Math.max(0, totalHeight - topSpacer - visibleHeight),
    totalHeight
  };
}

export function calculateAnchorScrollTop({
  items,
  listTop,
  anchorIndex,
  anchorOffsetWithin,
  getItemHeight
}) {
  return listTop + sumHeights(items, anchorIndex, getItemHeight) + anchorOffsetWithin;
}

export const defaultRangeEngine = {
  getWidthBucket,
  calculateVirtualWindow,
  calculateAnchorScrollTop
};

export function resolveScrollOwnerDescriptor(rootElement, currentOwner) {
  const nextOwner = rootElement?.closest?.('.main-view') || null;
  return {
    nextOwner,
    sameOwner: nextOwner === currentOwner,
    missing: !nextOwner
  };
}
