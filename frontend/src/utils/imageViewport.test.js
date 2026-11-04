import { describe, expect, it } from 'vitest';
import { imageGeometry, initialImageView, naturalImageView, panImage, zoomImageAt } from './imageViewport.js';

const size = { width: 800, height: 600, imageWidth: 4000, imageHeight: 2000 };
describe('image viewport', () => {
  it('fits without stretching and displays the true original size', () => {
    expect(imageGeometry(size)).toMatchObject({ scale: 0.2, width: 800, height: 400, left: 0, top: 100 });
    expect(imageGeometry(size, naturalImageView(size))).toMatchObject({ scale: 1, width: 4000, height: 2000 });
    expect(imageGeometry({ ...size, imageWidth: 100, imageHeight: 200 })).toMatchObject({ scale: 1, width: 100, height: 200 });
  });
  it('keeps the pixel under the pointer fixed when zooming', () => {
    const point = { x: 600, y: 300 };
    const before = imageGeometry(size);
    const after = imageGeometry(size, zoomImageAt(size, initialImageView(), 3, point));
    expect((point.x - after.left) / after.width).toBeCloseTo((point.x - before.left) / before.width);
    expect((point.y - after.top) / after.height).toBeCloseTo((point.y - before.top) / before.height);
  });
  it('clamps panning at the image boundary and centers dimensions smaller than the viewport', () => {
    const right = imageGeometry(size, panImage(size, { zoom: 3, x: 0.5, y: 0.5 }, 100000, 100000));
    expect(right.left).toBe(0); expect(right.top).toBe(0);
    const fit = imageGeometry(size, panImage(size, initialImageView(), 1000, 1000));
    expect(fit.left).toBe(0); expect(fit.top).toBe(100);
  });
  it('shares a normalized focal point across different image dimensions without stretching', () => {
    const view = { zoom: 4, x: 0.7, y: 0.6 };
    const first = imageGeometry(size, view), second = imageGeometry({ ...size, imageWidth: 2000, imageHeight: 1000 }, view);
    expect(first.x).toBe(second.x); expect(first.y).toBe(second.y);
    expect(first.width / first.height).toBe(2); expect(second.width / second.height).toBe(2);
    expect(first.scale).toBe(second.scale / 2);
  });
});
