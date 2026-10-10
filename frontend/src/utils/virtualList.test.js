import { describe, expect, it, vi } from 'vitest';
import { VirtualHeightIndex, calculateVirtualWindow } from './virtualList.js';

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
