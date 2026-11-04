import { describe, expect, it } from 'vitest';
import { clampWallView, fitWall, revealWallSlot, stepWallSelection, wallBounds, wallCellRect, wallVisibleFraction, wallVisibleSlots, zoomWallAt } from './mediaWallGeometry';

describe('fixed media wall', () => {
  it('keeps slot geometry across zoom, with no repacking at the last row', () => {
    expect(wallBounds(0)).toEqual({ width: 0, height: 0 });
    expect(wallBounds(1)).toEqual({ width: 160, height: 160 });
    expect(wallBounds(33)).toEqual({ width: 5182, height: 322 });
    expect(wallCellRect(32, { scale: .5, x: 10, y: 20 })).toEqual({ x: 10, y: 101, width: 80, height: 80 });
  });
  it('holds the point under the cursor while zooming inside the wall', () => {
    const view = { scale: 1, x: -700, y: -600 }, viewport = { width: 700, height: 500 }, point = { x: 323, y: 214 };
    const next = zoomWallAt(view, 1.5, point, 1024, viewport);
    expect((point.x - next.x) / next.scale).toBeCloseTo(1023, 8);
    expect((point.y - next.y) / next.scale).toBeCloseTo(814, 8);
    expect(zoomWallAt(view, 99, point, 1024, viewport).scale).toBe(2);
    expect(zoomWallAt(view, .001, point, 1024, viewport).scale).toBe(.2);
  });
  it('clips the two-dimensional viewport and never fabricates items', () => {
    const slots = wallVisibleSlots(1024, { scale: 1, x: 0, y: 0 }, { width: 160, height: 160 }, 0);
    expect(slots).toEqual([0]);
    expect(wallVisibleSlots(1, { scale: 1, x: 0, y: 0 }, { width: 1000, height: 1000 })).toEqual([0]);
    expect(wallVisibleSlots(0, { scale: 1, x: 0, y: 0 }, { width: 1000, height: 1000 })).toEqual([]);
    expect(wallVisibleFraction(0, { scale: 1, x: -80, y: 0 }, { width: 160, height: 160 })).toBe(.5);
  });
  it('centres small scenes and reveals offscreen keyboard selections', () => {
    const viewport = { width: 640, height: 480 };
    expect(clampWallView({ scale: 1, x: -9999, y: -9999 }, 1, viewport)).toEqual({ scale: 1, x: 240, y: 160 });
    expect(fitWall(1, viewport).scale).toBe(2);
    expect(stepWallSelection(31, 'ArrowDown', 33)).toBe(32);
    const next = revealWallSlot(900, { scale: .5, x: 0, y: 0 }, 1024, viewport);
    expect(wallVisibleFraction(900, next, viewport)).toBeCloseTo(1);
  });
});
