export function createHeightCacheKey(videoId, widthBucket, subtitleMode) {
  return `${videoId}:${widthBucket}:${subtitleMode ? 1 : 0}`;
}

// 行高是下限：标签照实换行，行随内容长高（2026-09-07 用户裁决：单行藏标签不可接受）。
// 这里只是首次渲染前的预估，实测高度由 VirtualVideoList 量出后覆盖；预估越接近真值，
// 滚动条长度与锚点跳动越小。列表间距 6px 计在内。窄变体（详情抽屉打开）不显示标签。
const ROW_HEIGHTS = { compact: 88, comfortable: 104, narrow: 68 };
const ROW_GAP = 6;
// 一行标签的高度（22px 徽标 + 5px 行距）；第一行已包含在基础行高里。
const TAG_LINE_HEIGHT = 27;
// 单个标签徽标的估算宽度（含间距）与「+ 标签」按钮宽度。名字 ≤92px 截断，平均六七十像素。
const TAG_BADGE_WIDTH = 69;
const ADD_TAG_BUTTON_WIDTH = 70;
// 信息列可用宽度 = 列表宽 − 缩略图 − 行内动作 − 间距/内边距。
const NON_TAG_WIDTH = { compact: 132 + 260 + 60, comfortable: 156 + 260 + 60 };

export function estimateTagLines(video, widthBucket, density = 'compact') {
  const tagCount = Array.isArray(video?.tags) ? video.tags.length : 0;
  if (tagCount === 0 || density === 'narrow') return 1;
  const usable = Math.max(120, (Number(widthBucket) || 1) * 80 - (NON_TAG_WIDTH[density] || NON_TAG_WIDTH.compact));
  return Math.max(1, Math.ceil((tagCount * TAG_BADGE_WIDTH + ADD_TAG_BUTTON_WIDTH) / usable));
}

export function estimateVideoRowHeight(video, widthBucket, subtitleMode, density = 'compact') {
  const base = ROW_HEIGHTS[density] || ROW_HEIGHTS.compact;
  const extraLines = estimateTagLines(video, widthBucket, density) - 1;
  return base + ROW_GAP + extraLines * TAG_LINE_HEIGHT;
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
