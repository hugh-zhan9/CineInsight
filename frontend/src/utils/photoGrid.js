/**
 * photoGrid 是照片网格的二维虚拟化窗口计算。模型：条目按当前列数切成行，时间线分组模式下
 * 每个年月组前插入一整行分组头，行顶偏移预先算成前缀和，窗口定位靠二分。
 *
 * 为什么不复用 utils/virtualList.js（视频列表在用，本切片不修改它）：
 *   - virtualList 是一维等宽行，一行一条；这里一行装 columns 个格子，且分组头要占独立整行；
 *   - virtualList 每次窗口计算都用 sumHeights 从头 O(n) 累加行高，万级网格上每次滚动都要
 *     全量重算，这正是它不适合照片网格的原因；这里布局构建一次 O(n)，之后每次窗口定位 O(log n)。
 *
 * 行几何约定（组件按此渲染，见 PhotoLibraryPage.vue）：
 *   - rows[i].height 是行的内容高度，rows[i].outerHeight = height + gap（最后一行不带尾隙）；
 *   - offsets[i] 是第 i 行的顶边（含它前面所有行的行高与行间隙），offsets 长度 rows.length + 1；
 *   - layout.totalHeight = offsets[rows.length] - gap，即不含尾隙的内容总高。
 */

export const PHOTO_GROUP_NONE = 'none';
export const PHOTO_GROUP_MONTH = 'month';

export const PHOTO_ROW_HEADER = 'header';
export const PHOTO_ROW_CELLS = 'cells';

// 时间戳缺失或无法解析的条目归入同一个组，分组头显示「时间未知」。后端 created_at 恒有值，
// 正常数据不会命中这一支；保留它是为了让分组是全函数、不会因脏数据丢条目。
const UNKNOWN_GROUP = { key: 'unknown', year: 0, month: 0 };

/**
 * resolvePhotoColumns 复现 CSS `repeat(auto-fill, minmax(minColumnWidth, 1fr))` 在给定
 * 容器宽度与列间隙下的列数，至少 1 列。
 */
export function resolvePhotoColumns(containerWidth, { minColumnWidth = 180, gap = 12 } = {}) {
  const width = Math.max(0, Number(containerWidth) || 0);
  const minWidth = Math.max(1, Number(minColumnWidth) || 1);
  const spacing = Math.max(0, Number(gap) || 0);
  if (width <= 0) {
    return 1;
  }
  return Math.max(1, Math.floor((width + spacing) / (minWidth + spacing)));
}

/**
 * photoTimelineKey 取条目的时间线分组键，口径与后端 ListImageTimelineBuckets 一致：
 * 优先 EXIF 拍摄时间，缺失时回退入库时间，按本机时区取年月（后端也按 time.Local 归并，
 * 桌面端前后端同机同时区）。无法解析时返回 null。
 */
export function photoTimelineKey(item) {
  const raw = item?.taken_at || item?.created_at || '';
  if (!raw) {
    return null;
  }
  const date = new Date(raw);
  if (Number.isNaN(date.getTime())) {
    return null;
  }
  const year = date.getFullYear();
  const month = date.getMonth() + 1;
  return { key: `${year}-${String(month).padStart(2, '0')}`, year, month };
}

/**
 * formatPhotoTimelineLabel 生成分组头文案。count 为后端返回的该组总张数；拿不到计数时
 * 只显示年月，不用已加载条数冒充总数。
 */
export function formatPhotoTimelineLabel(row, count) {
  const title = row?.year ? `${row.year} 年 ${row.month} 月` : '时间未知';
  return Number.isFinite(count) ? `${title} · ${count} 张` : title;
}

/**
 * buildPhotoLayout 把扁平条目数组切成行并算出行顶前缀和。
 * groupBy 为 PHOTO_GROUP_MONTH 时按年月插入分组头，且组间不混行——一个组的最后一行即使
 * 不满也不会与下一组共享。
 */
export function buildPhotoLayout({
  items,
  columns,
  groupBy = PHOTO_GROUP_NONE,
  cellHeight,
  headerHeight = 0,
  gap = 0
} = {}) {
  const list = Array.isArray(items) ? items : [];
  const columnCount = Math.max(1, Math.floor(Number(columns) || 0));
  const cell = Math.max(0, Number(cellHeight) || 0);
  const header = Math.max(0, Number(headerHeight) || 0);
  const spacing = Math.max(0, Number(gap) || 0);
  const grouped = groupBy === PHOTO_GROUP_MONTH;
  // 分组键每个条目只解析一次：切行时要反复比较相邻条目是否同组。
  const keys = grouped ? list.map(item => photoTimelineKey(item) || UNKNOWN_GROUP) : null;

  const rows = [];
  const offsets = [0];
  const pushRow = (row) => {
    row.top = offsets[rows.length];
    rows.push(row);
    offsets.push(row.top + row.height + spacing);
  };

  let index = 0;
  while (index < list.length) {
    let groupEnd = list.length;
    let group = null;
    if (grouped) {
      group = keys[index];
      groupEnd = index;
      while (groupEnd < list.length && keys[groupEnd].key === group.key) {
        groupEnd += 1;
      }
      pushRow({
        kind: PHOTO_ROW_HEADER,
        height: header,
        groupKey: group.key,
        year: group.year,
        month: group.month,
        itemCount: groupEnd - index,
        // 空区间：让 rows 的 endIndex 单调不减，findPhotoRowIndexForItem 才能二分。
        startIndex: index,
        endIndex: index
      });
    }
    for (let cursor = index; cursor < groupEnd; cursor += columnCount) {
      pushRow({
        kind: PHOTO_ROW_CELLS,
        height: cell,
        groupKey: group ? group.key : '',
        startIndex: cursor,
        endIndex: Math.min(cursor + columnCount, groupEnd)
      });
    }
    index = groupEnd;
  }

  const totalHeight = rows.length > 0 ? offsets[rows.length] - spacing : 0;
  for (let cursor = 0; cursor < rows.length; cursor += 1) {
    rows[cursor].outerHeight = cursor === rows.length - 1 ? rows[cursor].height : rows[cursor].height + spacing;
  }

  return { rows, offsets, columns: columnCount, gap: spacing, totalHeight, itemCount: list.length };
}

// findFirstRowEndingAfter 返回第一个底边越过 position 的行下标（全在其上时返回末行）。
function findFirstRowEndingAfter(offsets, rowCount, position) {
  let low = 0;
  let high = rowCount - 1;
  while (low < high) {
    const mid = (low + high) >> 1;
    if (offsets[mid + 1] > position) {
      high = mid;
    } else {
      low = mid + 1;
    }
  }
  return low;
}

// findFirstRowStartingAtOrAfter 返回第一个顶边不早于 position 的行下标，可能等于 rowCount。
function findFirstRowStartingAtOrAfter(offsets, rowCount, position) {
  let low = 0;
  let high = rowCount;
  while (low < high) {
    const mid = (low + high) >> 1;
    if (offsets[mid] >= position) {
      high = mid;
    } else {
      low = mid + 1;
    }
  }
  return low;
}

/**
 * calculatePhotoWindow 定位当前视口应渲染的行区间与上下占位高度。
 * listTop 是网格容器顶边相对滚动宿主内容原点的偏移（镜像 virtualList 的同名入参）。
 */
export function calculatePhotoWindow({ layout, scrollTop, viewportHeight, listTop, overscan } = {}) {
  const rows = layout?.rows || [];
  const offsets = layout?.offsets || [0];
  const rowCount = rows.length;
  if (rowCount === 0) {
    return { startRow: 0, endRow: 0, topSpacer: 0, bottomSpacer: 0, totalHeight: 0 };
  }

  const normalizedOverscan = Math.max(0, Math.floor(Number(overscan) || 0));
  const top = Number(scrollTop) || 0;
  const listOrigin = Number(listTop) || 0;
  const relativeTop = Math.max(0, top - listOrigin);
  const relativeBottom = Math.max(relativeTop, top + (Number(viewportHeight) || 0) - listOrigin);

  const firstVisible = findFirstRowEndingAfter(offsets, rowCount, relativeTop);
  const afterVisible = Math.max(firstVisible + 1, findFirstRowStartingAtOrAfter(offsets, rowCount, relativeBottom));

  const startRow = Math.max(0, firstVisible - normalizedOverscan);
  const endRow = Math.min(rowCount, afterVisible + normalizedOverscan);
  const totalHeight = layout.totalHeight || 0;

  return {
    startRow,
    endRow,
    topSpacer: offsets[startRow],
    // endRow === rowCount 时 offsets[rowCount] 比 totalHeight 多一个尾隙，钳到 0。
    bottomSpacer: Math.max(0, totalHeight - offsets[endRow]),
    totalHeight
  };
}

/** findPhotoRowIndexForItem 二分定位条目所在的格子行；越界返回 -1。 */
export function findPhotoRowIndexForItem(layout, itemIndex) {
  const rows = layout?.rows || [];
  const target = Number(itemIndex);
  if (!rows.length || !Number.isFinite(target) || target < 0 || target >= (layout.itemCount || 0)) {
    return -1;
  }
  let low = 0;
  let high = rows.length - 1;
  while (low < high) {
    const mid = (low + high) >> 1;
    if (rows[mid].endIndex > target) {
      high = mid;
    } else {
      low = mid + 1;
    }
  }
  return low;
}

/** firstVisiblePhotoItemIndex 取窗口起始行往后第一个格子行的首个条目下标；没有则 -1。 */
export function firstVisiblePhotoItemIndex(layout, startRow) {
  const rows = layout?.rows || [];
  for (let index = Math.max(0, Number(startRow) || 0); index < rows.length; index += 1) {
    if (rows[index].kind === PHOTO_ROW_CELLS) {
      return rows[index].startIndex;
    }
  }
  return -1;
}

/**
 * calculatePhotoAnchorScrollTop 给出把某个条目所在行顶对齐到视口顶的 scrollTop。
 * 列数变化后重算布局时用它保持锚点，避免跳回顶部。
 */
export function calculatePhotoAnchorScrollTop({ layout, listTop, itemIndex } = {}) {
  const rowIndex = findPhotoRowIndexForItem(layout, itemIndex);
  if (rowIndex < 0) {
    return Number(listTop) || 0;
  }
  return (Number(listTop) || 0) + layout.offsets[rowIndex];
}
