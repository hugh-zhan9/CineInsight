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

export function estimateVideoGridHeight(video, cardWidth = 200) {
  const usable = Math.max(120, cardWidth - 18);
  const tags = Array.isArray(video?.tags) ? video.tags.length : 0;
  return 230 + Math.ceil((tags * TAG_BADGE_WIDTH + ADD_TAG_BUTTON_WIDTH) / usable) * TAG_LINE_HEIGHT;
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

// A Fenwick tree owns geometry only. Rebuild on data/layout changes; scrolling never
// traverses item payloads. Grid rows include their following gap in the tree.
export class VirtualHeightIndex {
  constructor(items, getItemHeight, { columns = 1, gap = 0 } = {}) {
    this.count = items.length;
    this.columns = Math.max(1, Math.floor(columns));
    this.gap = Math.max(0, gap);
    this.ids = items.map(item => item.id);
    this.positions = new Map(this.ids.map((id, i) => [String(id), i]));
    this.heights = new Float64Array(Math.ceil(this.count / this.columns));
    items.forEach((item, i) => {
      const row = Math.floor(i / this.columns);
      this.heights[row] = Math.max(this.heights[row], Number(getItemHeight(item, i)) || 1);
    });
    this.tree = new Float64Array(this.heights.length + 1);
    for (let i = 1; i < this.tree.length; i++) {
      this.tree[i] += this.heights[i - 1] + this.gap;
      const parent = i + (i & -i);
      if (parent < this.tree.length) this.tree[parent] += this.tree[i];
    }
  }

  prefix(endRow) {
    let total = 0;
    for (let i = Math.min(endRow, this.heights.length); i > 0; i -= i & -i) total += this.tree[i];
    return total;
  }

  get totalHeight() { return Math.max(0, this.prefix(this.heights.length) - this.gap); }
  indexOf(id) { return this.positions.get(String(id)) ?? -1; }
  itemTop(index) { return this.prefix(Math.floor(index / this.columns)); }

  setRowHeight(row, height) {
    if (row < 0 || row >= this.heights.length || !Number.isFinite(height) || height <= 0) return false;
    const delta = height - this.heights[row];
    if (Math.abs(delta) < 0.01) return false;
    this.heights[row] = height;
    for (let i = row + 1; i < this.tree.length; i += i & -i) this.tree[i] += delta;
    return true;
  }

  rowAt(offset) {
    if (offset >= this.totalHeight) return this.heights.length;
    let index = 0;
    let total = 0;
    for (let bit = 2 ** Math.floor(Math.log2(Math.max(1, this.heights.length))); bit >= 1; bit /= 2) {
      const next = index + bit;
      if (next < this.tree.length && total + this.tree[next] <= offset) {
        total += this.tree[next];
        index = next;
      }
    }
    return index;
  }

  window(top, bottom, overscan = 0) {
    const rows = this.heights.length;
    const first = this.rowAt(Math.max(0, top));
    const last = Math.min(rows, Math.max(first + 1, this.rowAt(Math.max(0, bottom) - 0.000001) + 1));
    const margin = Math.max(0, Math.floor(overscan));
    const start = Math.max(0, first - margin);
    const end = Math.min(rows, last + margin);
    const totalHeight = this.totalHeight;
    return {
      startIndex: Math.min(this.count, start * this.columns),
      endIndex: Math.min(this.count, end * this.columns),
      topSpacer: Math.min(totalHeight, this.prefix(start)),
      bottomSpacer: end < rows ? totalHeight - this.prefix(end) + this.gap : 0,
      totalHeight
    };
  }
}

export function calculateVirtualWindow({ items, scrollTop, viewportHeight, listTop, overscan, getItemHeight, layoutIndex }) {
  const index = layoutIndex || new VirtualHeightIndex(items || [], getItemHeight);
  const top = Math.max(0, scrollTop - listTop);
  return index.window(top, Math.max(top, scrollTop + viewportHeight - listTop), overscan);
}

export function calculateAnchorScrollTop({ items, listTop, anchorIndex, anchorOffsetWithin, getItemHeight, layoutIndex }) {
  const index = layoutIndex || new VirtualHeightIndex(items || [], getItemHeight);
  return listTop + index.itemTop(anchorIndex) + anchorOffsetWithin;
}

export const defaultRangeEngine = {
  getWidthBucket,
  calculateVirtualWindow,
  calculateAnchorScrollTop
};

export function resolveScrollOwnerDescriptor(rootElement, currentOwner, selector = '.main-view') {
  const nextOwner = rootElement?.closest?.(selector) || null;
  return {
    nextOwner,
    sameOwner: nextOwner === currentOwner,
    missing: !nextOwner
  };
}
