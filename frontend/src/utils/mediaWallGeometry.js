// Exploration wall uses immutable slots; loading and zoom never repack cells.
export const WALL_COLUMNS = 32;
export const WALL_CELL = 160;
export const WALL_STEP = 162;
export const WALL_MIN_SCALE = .2;
export const WALL_MAX_SCALE = 2;
export const WALL_SCENE_LIMIT = 1024;

const boundedCount = count => Math.min(WALL_SCENE_LIMIT, Math.max(0, Math.floor(Number(count) || 0)));
export const wallScale = value => Math.max(WALL_MIN_SCALE, Math.min(WALL_MAX_SCALE, Number.isFinite(value) ? value : .35));

export function wallBounds(count) {
  const n = boundedCount(count);
  return { width: n ? Math.min(n, WALL_COLUMNS) * WALL_STEP - 2 : 0, height: n ? Math.ceil(n / WALL_COLUMNS) * WALL_STEP - 2 : 0 };
}

export function wallCellRect(slot, view) {
  return { x: (slot % WALL_COLUMNS) * WALL_STEP * view.scale + view.x, y: Math.floor(slot / WALL_COLUMNS) * WALL_STEP * view.scale + view.y, width: WALL_CELL * view.scale, height: WALL_CELL * view.scale };
}

export function clampWallView(view, count, viewport) {
  const scale = wallScale(view.scale), bounds = wallBounds(count);
  const limit = (offset, content, size) => content <= size ? (size - content) / 2 : Math.min(16, Math.max(size - content - 16, Number.isFinite(offset) ? offset : 0));
  return { scale, x: limit(view.x, bounds.width * scale, viewport.width), y: limit(view.y, bounds.height * scale, viewport.height) };
}

export function zoomWallAt(view, requestedScale, point, count, viewport) {
  const scale = wallScale(requestedScale), previous = wallScale(view.scale);
  return clampWallView({ scale, x: point.x - (point.x - view.x) * scale / previous, y: point.y - (point.y - view.y) * scale / previous }, count, viewport);
}

export function fitWall(count, viewport) {
  const bounds = wallBounds(count);
  return clampWallView({ scale: Math.min((viewport.width - 32) / (bounds.width || 1), (viewport.height - 32) / (bounds.height || 1)), x: 0, y: 0 }, count, viewport);
}

export function wallVisibleSlots(count, view, viewport, overscan = 1) {
  const n = boundedCount(count), result = [];
  if (!n || viewport.width <= 0 || viewport.height <= 0) return result;
  const step = WALL_STEP * wallScale(view.scale), extra = Math.max(0, Math.min(1, overscan));
  const left = Math.max(0, Math.floor(-view.x / step) - extra);
  const right = Math.min(WALL_COLUMNS - 1, Math.floor((viewport.width - view.x) / step) + extra);
  const top = Math.max(0, Math.floor(-view.y / step) - extra);
  const bottom = Math.min(Math.ceil(n / WALL_COLUMNS) - 1, Math.floor((viewport.height - view.y) / step) + extra);
  for (let row = top; row <= bottom; row++) for (let col = left; col <= right; col++) {
    const slot = row * WALL_COLUMNS + col;
    if (slot < n) result.push(slot);
  }
  return result;
}

export function wallVisibleFraction(slot, view, viewport) {
  const rect = wallCellRect(slot, view);
  const width = Math.max(0, Math.min(viewport.width, rect.x + rect.width) - Math.max(0, rect.x));
  const height = Math.max(0, Math.min(viewport.height, rect.y + rect.height) - Math.max(0, rect.y));
  return width * height / (rect.width * rect.height);
}

export function stepWallSelection(slot, key, count) {
  const delta = { ArrowLeft: -1, ArrowRight: 1, ArrowUp: -WALL_COLUMNS, ArrowDown: WALL_COLUMNS }[key];
  if (delta === undefined || !count) return slot;
  return Math.max(0, Math.min(boundedCount(count) - 1, slot + delta));
}

export function revealWallSlot(slot, view, count, viewport) {
  const rect = wallCellRect(slot, view);
  let { x, y } = view;
  if (rect.x < 0) x -= rect.x;
  else if (rect.x + rect.width > viewport.width) x -= rect.x + rect.width - viewport.width;
  if (rect.y < 0) y -= rect.y;
  else if (rect.y + rect.height > viewport.height) y -= rect.y + rect.height - viewport.height;
  return clampWallView({ ...view, x, y }, count, viewport);
}
