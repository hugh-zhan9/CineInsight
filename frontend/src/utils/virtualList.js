export function createHeightCacheKey(videoId, widthBucket, subtitleMode) {
  return `${videoId}:${widthBucket}:${subtitleMode ? 1 : 0}`;
}

// 2026-09-01 重构后行高是固定的：标签与字幕命中都被压在同一行里横向溢出隐藏，
// 不再换行撑高，所以预估值只随档位和窄变体变化。列表间距 6px 计在内。
const ROW_HEIGHTS = { compact: 88, comfortable: 104, narrow: 68 };
const ROW_GAP = 6;

export function estimateVideoRowHeight(video, widthBucket, subtitleMode, density = 'compact') {
  const base = ROW_HEIGHTS[density] || ROW_HEIGHTS.compact;
  return base + ROW_GAP;
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
