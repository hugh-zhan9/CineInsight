import { describe, expect, it } from 'vitest';
import { adjustImagePixels, automaticImageAdjustment, imageEditGeometry, originalImageRecipe } from './imageEditing.js';

describe('image editing', () => {
  it('uses the same source crop geometry for preview and full resolution after rotation', () => {
    const recipe = { ...originalImageRecipe(), rotation: 1, crop: { x: 0.25, y: 0.25, width: 0.5, height: 0.5 } };
    const full = imageEditGeometry(4000, 3000, recipe), preview = imageEditGeometry(4000, 3000, recipe, 1000);
    expect(full).toMatchObject({ rotatedWidth: 3000, rotatedHeight: 4000, x: 750, y: 1000, width: 1500, height: 2000 });
    expect(preview).toMatchObject({ x: 750, y: 1000, cropWidth: 1500, cropHeight: 2000, width: 375, height: 500 });
    expect(imageEditGeometry(4000, 3000, originalImageRecipe())).toMatchObject({ width: 4000, height: 3000 });
  });
  it('keeps crop coordinates inside the transformed source', () => {
    const recipe = { ...originalImageRecipe(), crop: { x: 0.99, y: 0.99, width: 1, height: 1 } };
    expect(imageEditGeometry(4000, 3000, recipe)).toMatchObject({ x: 3960, y: 2970, width: 40, height: 30 });
  });
  it('leaves neutral pixels and transparency unchanged and increases exposure by stops', () => {
    const pixels = new Uint8ClampedArray([10, 20, 30, 100, 40, 50, 60, 0]);
    expect([...adjustImagePixels(pixels.slice(), originalImageRecipe())]).toEqual([...pixels]);
    expect([...adjustImagePixels(pixels.slice(), { ...originalImageRecipe(), exposure: 1 })]).toEqual([20, 40, 60, 100, 40, 50, 60, 0]);
  });
  it('changes saturation and shadows without changing alpha or clipping white shadows', () => {
    const gray = adjustImagePixels(new Uint8ClampedArray([200, 100, 50, 128]), { ...originalImageRecipe(), saturation: -100 });
    expect(gray[0]).toBe(gray[1]); expect(gray[1]).toBe(gray[2]); expect(gray[3]).toBe(128);
    const lifted = adjustImagePixels(new Uint8ClampedArray([30, 30, 30, 255, 255, 255, 255, 255]), { ...originalImageRecipe(), shadows: 100 });
    expect(lifted[0]).toBeGreaterThan(30); expect([...lifted.slice(4)]).toEqual([255, 255, 255, 255]);
  });
  it('improves dark exposure with bounded adjustments and ignores transparent samples', () => {
    expect(automaticImageAdjustment(new Uint8ClampedArray([20, 20, 20, 255]))).toMatchObject({ exposure: 1.5, shadows: 20 });
    expect(automaticImageAdjustment(new Uint8ClampedArray([0, 0, 0, 0]))).toEqual({ exposure: 0, shadows: 0, contrast: 0, saturation: 0 });
  });
});
