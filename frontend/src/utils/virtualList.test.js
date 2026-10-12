import { describe, expect, it, vi } from 'vitest';
import {
  LIBRARY_LAYOUTS, VirtualHeightIndex, calculateVirtualWindow, estimateVideoWallHeight, isColumnLayout,
  normalizeLibraryLayout, resolveLibraryColumns
} from './virtualList.js';

describe('logical-row height index', () => {
  it('uses the tallest card, includes each grid gap once, and keeps a partial final row', () => {
    const heights = [10, 30, 20, 40, 15, 60, 25];
    const items = heights.map((height, id) => ({ id, height }));
    const layoutIndex = new VirtualHeightIndex(items, item => item.height, { columns: 3, gap: 12 });
    expect(calculateVirtualWindow({ items, layoutIndex, scrollTop: 45, viewportHeight: 40, listTop: 0, overscan: 0 })).toEqual({ startIndex: 3, endIndex: 6, topSpacer: 42, bottomSpacer: 37, totalHeight: 139 });
    layoutIndex.setRowHeight(0, 35.5);
    expect(layoutIndex.itemTop(6)).toBe(119.5);
    expect(layoutIndex.totalHeight).toBe(144.5);
    expect(layoutIndex.indexOf(6)).toBe(6);
    expect(layoutIndex.indexOf('missing')).toBe(-1);
  });

  it('matches a linear oracle across updates, empty bounds and exact row edges', () => {
    for (const count of [0, 1, 2, 17, 128]) {
      const items = Array.from({ length: count }, (_, id) => ({ id: `row-${id}` }));
      const heights = items.map((_, i) => 10.25 + (i * 17 % 43));
      const index = new VirtualHeightIndex(items, (_, i) => heights[i]);
      for (let i = 0; i < count; i += 3) {
        heights[i] += 5.5;
        index.setRowHeight(i, heights[i]);
      }
      let prefix = 0;
      for (let i = 0; i < count; i++) {
        expect(index.itemTop(i)).toBeCloseTo(prefix, 8);
        expect(index.rowAt(prefix)).toBe(i);
        expect(index.rowAt(prefix + heights[i] - 0.01)).toBe(i);
        prefix += heights[i];
      }
      expect(index.rowAt(prefix)).toBe(count);
      expect(index.totalHeight).toBeCloseTo(prefix, 8);
      expect(index.window(0, 100, 0).startIndex).toBe(0);
      expect(index.window(prefix + 100, prefix + 200, 0).endIndex).toBe(count);
    }
  });

  it('does not revisit item payloads during repeated scrolling of 100,000 rows', () => {
    const items = Array.from({ length: 100000 }, (_, id) => ({ id }));
    const estimate = vi.fn(() => 50);
    const layoutIndex = new VirtualHeightIndex(items, estimate);
    expect(estimate).toHaveBeenCalledTimes(items.length);
    estimate.mockClear();
    for (let i = 0; i < 1000; i++) {
      const result = calculateVirtualWindow({ items, layoutIndex, scrollTop: i * 4973, viewportHeight: 600, listTop: 0, overscan: 4, getItemHeight: estimate });
      expect(result.endIndex - result.startIndex).toBeLessThanOrEqual(21);
    }
    expect(estimate).not.toHaveBeenCalled();
  });
});

// 缩略图墙（2026-10-12 用户裁决「只要一个紧凑的缩略图墙」）：与网格卡共用列式几何，只是列宽与间隙更小。
describe('片库布局几何', () => {
  it('网格沿用原来的 floor((宽 + 12) / 212) 列与 12px 间隙', () => {
    for (const width of [0, 199, 212, 423, 424, 800, 1100, 1600]) {
      expect(resolveLibraryColumns('grid', width)).toEqual({ columns: Math.max(1, Math.floor((width + 12) / 212)), gap: 12 });
    }
  });

  it('缩略图墙按 140px 最小列宽、6px 间隙排列，同宽度下列数明显多于网格', () => {
    expect(resolveLibraryColumns('wall', 1200)).toEqual({ columns: 8, gap: 6 });
    expect(resolveLibraryColumns('wall', 800)).toEqual({ columns: 5, gap: 6 });
    // 列边界：刚好放下 n 列时是 n 列，少 1px 就退一列。
    expect(resolveLibraryColumns('wall', 140 * 3 + 6 * 2).columns).toBe(3);
    expect(resolveLibraryColumns('wall', 140 * 3 + 6 * 2 - 1).columns).toBe(2);
    expect(resolveLibraryColumns('wall', 100).columns).toBe(1);
    expect(resolveLibraryColumns('wall', 0).columns).toBe(1);
    // 格子宽度落在 140–160px 一带。
    for (const width of [800, 1100, 1200, 1600]) {
      const { columns, gap } = resolveLibraryColumns('wall', width);
      const cardWidth = (width - (columns - 1) * gap) / columns;
      expect(cardWidth).toBeGreaterThanOrEqual(140);
      expect(cardWidth).toBeLessThan(170);
      expect(columns).toBeGreaterThan(resolveLibraryColumns('grid', width).columns);
    }
  });

  it('列表与未知布局恒为一列、无间隙', () => {
    expect(resolveLibraryColumns('list', 1600)).toEqual({ columns: 1, gap: 0 });
    expect(resolveLibraryColumns('mosaic', 1600)).toEqual({ columns: 1, gap: 0 });
    expect(resolveLibraryColumns('toString', 1600)).toEqual({ columns: 1, gap: 0 });
    expect(isColumnLayout('grid')).toBe(true);
    expect(isColumnLayout('wall')).toBe(true);
    expect(isColumnLayout('list')).toBe(false);
  });

  it('缩略图墙的行高预估就是带 1px 边框的 16:9 格子高度，与标签多少无关', () => {
    expect(estimateVideoWallHeight(146)).toBe(144 * 9 / 16 + 2);
    expect(estimateVideoWallHeight(162)).toBe(160 * 9 / 16 + 2);
    expect(estimateVideoWallHeight(0)).toBe(2);
  });

  it('缩略图墙接进行高索引：8 列一行，行距是 6px 间隙', () => {
    const items = Array.from({ length: 20 }, (_, id) => ({ id }));
    const { columns, gap } = resolveLibraryColumns('wall', 1200);
    const cardWidth = (1200 - (columns - 1) * gap) / columns;
    const rowHeight = estimateVideoWallHeight(cardWidth);
    const index = new VirtualHeightIndex(items, () => rowHeight, { columns, gap });
    expect(index.heights).toHaveLength(3);
    expect(index.itemTop(8)).toBeCloseTo(rowHeight + 6, 8);
    expect(index.itemTop(19)).toBeCloseTo(2 * (rowHeight + 6), 8);
    expect(index.totalHeight).toBeCloseTo(3 * rowHeight + 2 * 6, 8);
  });

  it('布局偏好只认三种值，其余（旧版本、手改、损坏）回到列表', () => {
    expect(LIBRARY_LAYOUTS).toEqual(['list', 'grid', 'wall']);
    for (const value of LIBRARY_LAYOUTS) expect(normalizeLibraryLayout(value)).toBe(value);
    for (const value of [null, undefined, '', 'Grid', 'WALL', 'tiles', 'toString']) expect(normalizeLibraryLayout(value)).toBe('list');
  });
});
