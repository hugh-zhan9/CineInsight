import { cloneRecipe } from './videoEdit.js';

export const CROP_RATIOS = [
  { value: 'free', label: '自由比例' }, { value: '9:16', label: '9:16 竖屏' },
  { value: '16:9', label: '16:9 横屏' }, { value: '1:1', label: '1:1 方形' }
];
const even = value => Math.round(Number(value) / 2) * 2;
const bound = (value, low, high) => Math.min(high, Math.max(low, value));
export function validCrop(r, width, height) {
  return Boolean(r && [r.x, r.y, r.width, r.height].every(n => Number.isInteger(n) && n % 2 === 0) &&
    r.x >= 0 && r.y >= 0 && r.width >= 2 && r.height >= 2 && r.x + r.width <= width && r.y + r.height <= height);
}
export function cropRatio(value) {
  if (!CROP_RATIOS.some(r => r.value === value) || value === 'free') return 0;
  const [w, h] = value.split(':').map(Number);
  return w / h;
}
export function fitCropRatio(rect, value) {
  const ratio = cropRatio(value);
  if (!ratio) return { ...rect };
  const width = Math.min(rect.width, rect.height * ratio);
  const height = width / ratio;
  const w = Math.max(2, Math.floor(width / 2) * 2);
  const h = Math.max(2, Math.floor(height / 2) * 2);
  return { x: rect.x + Math.floor((rect.width - w) / 4) * 2, y: rect.y + Math.floor((rect.height - h) / 4) * 2, width: w, height: h };
}
export function cropFromMargins(rect, side, value, width, height) {
  if (!Number.isFinite(Number(value))) return rect;
  const margins = { left: rect.x, top: rect.y, right: width - rect.x - rect.width, bottom: height - rect.y - rect.height };
  const limit = side === 'left' || side === 'right' ? width - 2 - margins[side === 'left' ? 'right' : 'left'] : height - 2 - margins[side === 'top' ? 'bottom' : 'top'];
  margins[side] = bound(even(value), 0, limit);
  return { x: margins.left, y: margins.top, width: width - margins.left - margins.right, height: height - margins.top - margins.bottom };
}
export function dragCrop(rect, handle, dx, dy, width, height, ratioValue = 'free') {
  if (handle === 'move') return { ...rect, x: bound(even(rect.x + dx), 0, width - rect.width), y: bound(even(rect.y + dy), 0, height - rect.height) };
  if (handle === 'n' || handle === 's') dx = 0;
  if (handle === 'w' || handle === 'e') dy = 0;
  const west = handle.includes('w'), north = handle.includes('n');
  const anchorX = west ? rect.x + rect.width : rect.x;
  const anchorY = north ? rect.y + rect.height : rect.y;
  const maxW = west ? anchorX : width - anchorX;
  const maxH = north ? anchorY : height - anchorY;
  let w = bound(even(rect.width + (west ? -dx : dx)), 2, maxW);
  let h = bound(even(rect.height + (north ? -dy : dy)), 2, maxH);
  const ratio = cropRatio(ratioValue);
  if (ratio) {
    const changeX = west ? -dx : dx;
    const changeY = north ? -dy : dy;
    const desiredW = Math.abs(changeY) * ratio > Math.abs(changeX) ? (rect.height + changeY) * ratio : rect.width + changeX;
    w = Math.max(2, Math.floor(Math.min(Math.max(2, desiredW), maxW, maxH * ratio) / 2) * 2);
    h = Math.max(2, Math.floor(w / ratio / 2) * 2);
  }
  return { x: west ? anchorX - w : anchorX, y: north ? anchorY - h : anchorY, width: w, height: h };
}
export function setCropRect(recipe, index, rect, sourceVersion) {
  const next = cloneRecipe(recipe);
  Object.assign(next.crop.items[index], { rect: { ...rect }, source_version: sourceVersion, confirmed: false });
  return next;
}
