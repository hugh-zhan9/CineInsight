import { describe, expect, it } from 'vitest';

import {
  PHOTO_GROUP_MONTH,
  PHOTO_GROUP_NONE,
  PHOTO_ROW_CELLS,
  PHOTO_ROW_HEADER,
  buildPhotoLayout,
  calculatePhotoAnchorScrollTop,
  calculatePhotoWindow,
  findPhotoRowIndexForItem,
  firstVisiblePhotoItemIndex,
  formatPhotoTimelineLabel,
  photoTimelineKey,
  resolvePhotoColumns
} from './photoGrid.js';

const CELL = 200;
const HEADER = 40;
const GAP = 12;

// 无时区后缀的字面量按本机时区解析，与 photoTimelineKey 的本机时区口径一致，断言不随 TZ 漂移。
function makeItem(id, takenAt) {
  return { id, taken_at: takenAt, created_at: '2020-01-01T00:00:00' };
}

function layoutOf(items, columns, groupBy = PHOTO_GROUP_NONE) {
  return buildPhotoLayout({ items, columns, groupBy, cellHeight: CELL, headerHeight: HEADER, gap: GAP });
}

function rowSpans(layout) {
  return layout.rows.map(row => (row.kind === PHOTO_ROW_HEADER ? row.groupKey : [row.startIndex, row.endIndex]));
}

// renderedOuterHeight 是窗口内行在 DOM 里真正占的高度（含行间隙，末行不带尾隙），
// 组件正是按 outerHeight 渲染每一行，所以 topSpacer + 它 + bottomSpacer 必须恰好等于 totalHeight。
function renderedOuterHeight(layout, startRow, endRow) {
  return layout.rows.slice(startRow, endRow).reduce((sum, row) => sum + row.outerHeight, 0);
}

describe('resolvePhotoColumns', () => {
  it('mirrors auto-fill minmax with the gap and never drops below one column', () => {
    expect(resolvePhotoColumns(0, { minColumnWidth: 180, gap: 12 })).toBe(1);
    expect(resolvePhotoColumns(100, { minColumnWidth: 180, gap: 12 })).toBe(1);
    // 180 + 12 = 192 per column: 191 还差一点，192 刚好 1 列，384 刚好 2 列。
    expect(resolvePhotoColumns(191, { minColumnWidth: 180, gap: 12 })).toBe(1);
    expect(resolvePhotoColumns(372, { minColumnWidth: 180, gap: 12 })).toBe(2);
    expect(resolvePhotoColumns(1200, { minColumnWidth: 180, gap: 12 })).toBe(6);
    expect(resolvePhotoColumns(1200)).toBe(6);
  });
});

describe('buildPhotoLayout without grouping', () => {
  it('returns an empty layout for an empty list', () => {
    const layout = layoutOf([], 4);
    expect(layout.rows).toHaveLength(0);
    expect(layout.totalHeight).toBe(0);
    expect(layout.offsets).toEqual([0]);
    expect(calculatePhotoWindow({ layout, scrollTop: 0, viewportHeight: 800, listTop: 0, overscan: 2 }))
      .toEqual({ startRow: 0, endRow: 0, topSpacer: 0, bottomSpacer: 0, totalHeight: 0 });
  });

  it('puts a single item in one row with no trailing gap in the total height', () => {
    const layout = layoutOf([makeItem(1)], 4);
    expect(layout.rows).toHaveLength(1);
    expect(layout.rows[0]).toMatchObject({ kind: PHOTO_ROW_CELLS, startIndex: 0, endIndex: 1, top: 0, height: CELL });
    expect(layout.rows[0].outerHeight).toBe(CELL);
    expect(layout.totalHeight).toBe(CELL);
  });

  it('gives one row per item when there is a single column', () => {
    const layout = layoutOf([1, 2, 3].map(id => makeItem(id)), 1);
    expect(rowSpans(layout)).toEqual([[0, 1], [1, 2], [2, 3]]);
    expect(layout.rows.map(row => row.top)).toEqual([0, CELL + GAP, 2 * (CELL + GAP)]);
    expect(layout.totalHeight).toBe(3 * CELL + 2 * GAP);
  });

  it('accumulates the gap between rows and leaves a partial last row short', () => {
    const items = Array.from({ length: 7 }, (_, index) => makeItem(index + 1));
    const layout = layoutOf(items, 3);

    expect(rowSpans(layout)).toEqual([[0, 3], [3, 6], [6, 7]]);
    expect(layout.rows.map(row => row.top)).toEqual([0, CELL + GAP, 2 * (CELL + GAP)]);
    expect(layout.rows.map(row => row.outerHeight)).toEqual([CELL + GAP, CELL + GAP, CELL]);
    expect(layout.totalHeight).toBe(3 * CELL + 2 * GAP);
    expect(layout.offsets).toHaveLength(layout.rows.length + 1);
    expect(layout.offsets.at(-1)).toBe(layout.totalHeight + GAP);
  });

  it('treats a zero gap as a plain stack', () => {
    const items = Array.from({ length: 5 }, (_, index) => makeItem(index + 1));
    const layout = buildPhotoLayout({ items, columns: 2, cellHeight: CELL, gap: 0 });
    expect(layout.rows.map(row => row.top)).toEqual([0, CELL, 2 * CELL]);
    expect(layout.totalHeight).toBe(3 * CELL);
  });
});

describe('buildPhotoLayout with month grouping', () => {
  const items = [
    makeItem(1, '2026-08-21T10:00:00'),
    makeItem(2, '2026-08-03T10:00:00'),
    makeItem(3, '2026-08-02T10:00:00'),
    makeItem(4, '2026-07-09T10:00:00'),
    makeItem(5, '2025-12-31T23:00:00')
  ];

  it('gives every group its own header row and never mixes groups inside a cell row', () => {
    const layout = layoutOf(items, 2, PHOTO_GROUP_MONTH);

    expect(rowSpans(layout)).toEqual([
      '2026-08', [0, 2], [2, 3],
      '2026-07', [3, 4],
      '2025-12', [4, 5]
    ]);
    // 2026-08 有 3 张、列数 2：最后一行只装 1 张，绝不把 2026-07 的第一张补进来。
    expect(layout.rows[2]).toMatchObject({ startIndex: 2, endIndex: 3, groupKey: '2026-08' });
    expect(layout.rows.filter(row => row.kind === PHOTO_ROW_HEADER).map(row => row.itemCount)).toEqual([3, 1, 1]);
  });

  it('stacks header and cell heights with the gap between every row', () => {
    const layout = layoutOf(items, 2, PHOTO_GROUP_MONTH);
    const expectedHeights = [HEADER, CELL, CELL, HEADER, CELL, HEADER, CELL];
    expect(layout.rows.map(row => row.height)).toEqual(expectedHeights);

    let running = 0;
    layout.rows.forEach((row, index) => {
      expect(row.top).toBe(running);
      expect(layout.offsets[index]).toBe(running);
      running += row.height + GAP;
    });
    expect(layout.totalHeight).toBe(running - GAP);
  });

  it('keeps a one-item group as a header plus a single-cell row', () => {
    const layout = layoutOf([makeItem(9, '2026-03-01T08:00:00')], 5, PHOTO_GROUP_MONTH);
    expect(layout.rows).toHaveLength(2);
    expect(layout.rows[0]).toMatchObject({ kind: PHOTO_ROW_HEADER, groupKey: '2026-03', itemCount: 1, year: 2026, month: 3 });
    expect(layout.rows[1]).toMatchObject({ kind: PHOTO_ROW_CELLS, startIndex: 0, endIndex: 1 });
    expect(layout.totalHeight).toBe(HEADER + GAP + CELL);
  });

  it('falls back to created_at and buckets unparseable timestamps under one unknown group', () => {
    expect(photoTimelineKey({ created_at: '2024-05-06T00:00:00' })).toMatchObject({ key: '2024-05', year: 2024, month: 5 });
    expect(photoTimelineKey({ taken_at: 'not-a-date', created_at: '' })).toBeNull();

    const layout = layoutOf([
      { id: 1, created_at: '2024-05-06T00:00:00' },
      { id: 2, taken_at: 'not-a-date', created_at: '' },
      { id: 3, created_at: '' }
    ], 4, PHOTO_GROUP_MONTH);
    expect(rowSpans(layout)).toEqual(['2024-05', [0, 1], 'unknown', [1, 3]]);
  });

  it('formats the header label with the backend count and omits it when unknown', () => {
    const layout = layoutOf(items, 4, PHOTO_GROUP_MONTH);
    const header = layout.rows[0];
    expect(formatPhotoTimelineLabel(header, 128)).toBe('2026 年 8 月 · 128 张');
    expect(formatPhotoTimelineLabel(header, undefined)).toBe('2026 年 8 月');
    expect(formatPhotoTimelineLabel({ year: 0, month: 0 }, 3)).toBe('时间未知 · 3 张');
  });
});

describe('calculatePhotoWindow', () => {
  const items = Array.from({ length: 40 }, (_, index) => makeItem(index + 1));

  it('renders only the rows the viewport touches and keeps the spacers exact', () => {
    const layout = layoutOf(items, 4, PHOTO_GROUP_NONE);
    const window = calculatePhotoWindow({ layout, scrollTop: 0, viewportHeight: 600, listTop: 0, overscan: 0 });

    // 行距 212：视口 600 覆盖第 0/1/2 行的顶边。
    expect(window.startRow).toBe(0);
    expect(window.endRow).toBe(3);
    expect(window.topSpacer).toBe(0);
    expect(window.topSpacer + renderedOuterHeight(layout, window.startRow, window.endRow) + window.bottomSpacer)
      .toBe(layout.totalHeight);
  });

  it('moves the window with the scroll position and applies overscan on both sides', () => {
    const layout = layoutOf(items, 4, PHOTO_GROUP_NONE);
    const tight = calculatePhotoWindow({ layout, scrollTop: 1000, viewportHeight: 600, listTop: 0, overscan: 0 });
    const padded = calculatePhotoWindow({ layout, scrollTop: 1000, viewportHeight: 600, listTop: 0, overscan: 2 });

    expect(tight.startRow).toBeGreaterThan(0);
    expect(padded.startRow).toBe(Math.max(0, tight.startRow - 2));
    expect(padded.endRow).toBe(Math.min(layout.rows.length, tight.endRow + 2));
    expect(padded.topSpacer).toBe(layout.offsets[padded.startRow]);
  });

  it('subtracts listTop so the grid can sit below a toolbar in the scroll owner', () => {
    const layout = layoutOf(items, 4, PHOTO_GROUP_NONE);
    const atTop = calculatePhotoWindow({ layout, scrollTop: 0, viewportHeight: 600, listTop: 0, overscan: 0 });
    const shifted = calculatePhotoWindow({ layout, scrollTop: 300, viewportHeight: 600, listTop: 300, overscan: 0 });
    expect(shifted).toEqual(atTop);
  });

  it('zeroes the bottom spacer at the end of the list', () => {
    const layout = layoutOf(items, 4, PHOTO_GROUP_NONE);
    const window = calculatePhotoWindow({ layout, scrollTop: layout.totalHeight, viewportHeight: 600, listTop: 0, overscan: 4 });
    expect(window.endRow).toBe(layout.rows.length);
    expect(window.bottomSpacer).toBe(0);
    expect(window.topSpacer).toBe(layout.offsets[window.startRow]);
  });

  it('always keeps at least one row rendered when the viewport height is unknown', () => {
    const layout = layoutOf(items, 4, PHOTO_GROUP_NONE);
    const window = calculatePhotoWindow({ layout, scrollTop: 0, viewportHeight: 0, listTop: 0, overscan: 0 });
    expect(window.endRow - window.startRow).toBe(1);
  });

  it('includes the group header row that precedes the first visible cells', () => {
    const grouped = [
      ...Array.from({ length: 8 }, (_, index) => makeItem(index + 1, '2026-08-01T00:00:00')),
      ...Array.from({ length: 8 }, (_, index) => makeItem(index + 9, '2026-07-01T00:00:00'))
    ];
    const layout = layoutOf(grouped, 4, PHOTO_GROUP_MONTH);
    // 行序：header, cells, cells, header, cells, cells
    const window = calculatePhotoWindow({ layout, scrollTop: layout.offsets[3], viewportHeight: 100, listTop: 0, overscan: 0 });
    expect(layout.rows[window.startRow].kind).toBe(PHOTO_ROW_HEADER);
    expect(layout.rows[window.startRow].groupKey).toBe('2026-07');
  });
});

describe('column changes keep the scroll anchor', () => {
  const items = Array.from({ length: 40 }, (_, index) => makeItem(index + 1));

  it('locates the row of an item and re-anchors it after a relayout', () => {
    const wide = layoutOf(items, 5, PHOTO_GROUP_NONE);
    const narrow = layoutOf(items, 2, PHOTO_GROUP_NONE);

    expect(findPhotoRowIndexForItem(wide, 0)).toBe(0);
    expect(findPhotoRowIndexForItem(wide, 12)).toBe(2);
    expect(findPhotoRowIndexForItem(narrow, 12)).toBe(6);
    expect(findPhotoRowIndexForItem(wide, 40)).toBe(-1);
    expect(findPhotoRowIndexForItem(wide, -1)).toBe(-1);

    const listTop = 90;
    const before = calculatePhotoAnchorScrollTop({ layout: wide, listTop, itemIndex: 12 });
    const after = calculatePhotoAnchorScrollTop({ layout: narrow, listTop, itemIndex: 12 });
    expect(before).toBe(listTop + 2 * (CELL + GAP));
    expect(after).toBe(listTop + 6 * (CELL + GAP));

    // 重算后把同一张照片放回视口顶：新窗口的首个条目仍是它。
    const window = calculatePhotoWindow({ layout: narrow, scrollTop: after, viewportHeight: 600, listTop, overscan: 0 });
    expect(firstVisiblePhotoItemIndex(narrow, window.startRow)).toBe(12);
  });

  it('skips header rows when reading the first visible item', () => {
    const grouped = layoutOf(items.map((item, index) => makeItem(item.id, index < 20 ? '2026-08-01T00:00:00' : '2026-07-01T00:00:00')), 4, PHOTO_GROUP_MONTH);
    expect(grouped.rows[0].kind).toBe(PHOTO_ROW_HEADER);
    expect(firstVisiblePhotoItemIndex(grouped, 0)).toBe(0);
    expect(firstVisiblePhotoItemIndex(grouped, grouped.rows.length)).toBe(-1);
    expect(findPhotoRowIndexForItem(grouped, 20)).toBe(grouped.rows.findIndex(row => row.startIndex === 20 && row.kind === PHOTO_ROW_CELLS));
  });

  it('falls back to the list top when the anchor item is gone', () => {
    const layout = layoutOf(items, 4, PHOTO_GROUP_NONE);
    expect(calculatePhotoAnchorScrollTop({ layout, listTop: 42, itemIndex: 999 })).toBe(42);
  });
});

describe('photoGrid scales to a ten-thousand item library', () => {
  it('builds the layout once and answers window queries without touching every row', () => {
    // 每 834 张换一个月份，构造 12 个连续的年月组。
    const items = Array.from({ length: 10000 }, (_, index) => makeItem(index + 1,
      `2026-${String(Math.floor(index / 834) + 1).padStart(2, '0')}-01T00:00:00`));

    const buildStart = performance.now();
    const layout = buildPhotoLayout({
      items, columns: 6, groupBy: PHOTO_GROUP_MONTH, cellHeight: CELL, headerHeight: HEADER, gap: GAP
    });
    const buildMs = performance.now() - buildStart;

    // 12 个月分组头 + 每组 ceil(834 或 833 / 6) 行格子。
    expect(layout.rows.filter(row => row.kind === PHOTO_ROW_HEADER)).toHaveLength(12);
    expect(layout.itemCount).toBe(10000);

    const queryStart = performance.now();
    let maxRendered = 0;
    for (let step = 0; step < 500; step += 1) {
      const scrollTop = (layout.totalHeight * step) / 500;
      const window = calculatePhotoWindow({ layout, scrollTop, viewportHeight: 900, listTop: 0, overscan: 4 });
      maxRendered = Math.max(maxRendered, window.endRow - window.startRow);
      expect(window.topSpacer + renderedOuterHeight(layout, window.startRow, window.endRow) + window.bottomSpacer)
        .toBe(layout.totalHeight);
    }
    const queryMs = performance.now() - queryStart;

    // 渲染行数只由视口高度和 overscan 决定，与 1 万条数据量无关。这是本用例真正钉住的不变量；
    // 耗时只打印不断言，墙钟时间在不同机器/负载上不可复现。
    expect(maxRendered).toBeLessThan(20);
    // eslint-disable-next-line no-console
    console.log(`[photoGrid perf] 10000 items: build=${buildMs.toFixed(2)}ms, 500 windows=${queryMs.toFixed(2)}ms, maxRenderedRows=${maxRendered}`);
  });
});
