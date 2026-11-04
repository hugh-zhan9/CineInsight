export const originalImageRecipe = () => ({ rotation: 0, straighten: 0, flipX: false, flipY: false, exposure: 0, shadows: 0, contrast: 0, temperature: 0, saturation: 0, crop: { x: 0, y: 0, width: 1, height: 1 } });
export const cloneImageRecipe = recipe => JSON.parse(JSON.stringify(recipe));
const clamp = (value, min, max) => Math.max(min, Math.min(max, value));

export function imageEditGeometry(width, height, recipe, maxEdge = 0) {
  const angle = (recipe.rotation * 90 + recipe.straighten) * Math.PI / 180;
  const rotatedWidth = Math.max(1, Math.ceil(Math.abs(width * Math.cos(angle)) + Math.abs(height * Math.sin(angle)) - 1e-7));
  const rotatedHeight = Math.max(1, Math.ceil(Math.abs(width * Math.sin(angle)) + Math.abs(height * Math.cos(angle)) - 1e-7));
  const scale = maxEdge > 0 ? Math.min(1, maxEdge / Math.max(rotatedWidth, rotatedHeight)) : 1;
  const crop = recipe.crop;
  const x = clamp(Math.round(crop.x * rotatedWidth), 0, rotatedWidth - 1);
  const y = clamp(Math.round(crop.y * rotatedHeight), 0, rotatedHeight - 1);
  const cropWidth = clamp(Math.round(crop.width * rotatedWidth), 1, rotatedWidth - x);
  const cropHeight = clamp(Math.round(crop.height * rotatedHeight), 1, rotatedHeight - y);
  return { angle, rotatedWidth, rotatedHeight, scale, x, y, cropWidth, cropHeight, width: Math.max(1, Math.round(cropWidth * scale)), height: Math.max(1, Math.round(cropHeight * scale)) };
}

// 预览与完整导出调用同一套像素运算，透明像素保留alpha。
export function adjustImagePixels(data, recipe) {
  const exposure = 2 ** recipe.exposure;
  const contrast = 2 ** (recipe.contrast / 100);
  const saturation = 1 + recipe.saturation / 100;
  const warm = recipe.temperature * 0.25;
  for (let index = 0; index < data.length; index += 4) {
    if (!data[index + 3]) continue;
    let r = data[index] * exposure, g = data[index + 1] * exposure, b = data[index + 2] * exposure;
    const luma = (r * 0.2126 + g * 0.7152 + b * 0.0722) / 255;
    const lift = recipe.shadows / 100 * 75 * (1 - clamp(luma, 0, 1)) ** 2;
    r = (r + lift - 128) * contrast + 128 + warm;
    g = (g + lift - 128) * contrast + 128;
    b = (b + lift - 128) * contrast + 128 - warm;
    const gray = r * 0.2126 + g * 0.7152 + b * 0.0722;
    data[index] = clamp(gray + (r - gray) * saturation, 0, 255);
    data[index + 1] = clamp(gray + (g - gray) * saturation, 0, 255);
    data[index + 2] = clamp(gray + (b - gray) * saturation, 0, 255);
  }
  return data;
}

export function automaticImageAdjustment(data) {
  const values = [];
  for (let index = 0; index < data.length; index += 4) if (data[index + 3] > 128) values.push(data[index] * 0.2126 + data[index + 1] * 0.7152 + data[index + 2] * 0.0722);
  if (!values.length) return { exposure: 0, shadows: 0, contrast: 0, saturation: 0 };
  values.sort((a, b) => a - b);
  const median = values[Math.floor(values.length / 2)];
  return { exposure: Math.round(clamp(Math.log2(110 / Math.max(median, 1)), -0.5, 1.5) * 10) / 10, shadows: median < 100 ? 20 : 0, contrast: 6, saturation: 5 };
}

export async function renderEditedImage(source, recipe, maxEdge = 0, current = () => true) {
  const width = source.naturalWidth || source.width, height = source.naturalHeight || source.height;
  if (!width || !height || width * height > 50_000_000) throw new Error('图片超出本次编辑上限（5000万像素）');
  const geometry = imageEditGeometry(width, height, recipe, maxEdge);
  if (geometry.rotatedWidth * geometry.rotatedHeight > 70_000_000) throw new Error('转正后的画布过大，请缩小转正角度');
  if (Math.max(geometry.rotatedWidth, geometry.rotatedHeight) > 32767) throw new Error('转正后的画布边长超出上限');
  const base = document.createElement('canvas'); base.width = Math.max(1, Math.round(geometry.rotatedWidth * geometry.scale)); base.height = Math.max(1, Math.round(geometry.rotatedHeight * geometry.scale));
  const context = base.getContext('2d', { colorSpace: 'srgb' });
  context.translate(base.width / 2, base.height / 2); context.rotate(geometry.angle);
  context.scale((recipe.flipX ? -1 : 1) * geometry.scale, (recipe.flipY ? -1 : 1) * geometry.scale);
  context.drawImage(source, -width / 2, -height / 2);
  const output = document.createElement('canvas'); output.width = geometry.width; output.height = geometry.height;
  const target = output.getContext('2d', { colorSpace: 'srgb', willReadFrequently: true });
  target.drawImage(base, geometry.x * geometry.scale, geometry.y * geometry.scale, geometry.cropWidth * geometry.scale, geometry.cropHeight * geometry.scale, 0, 0, output.width, output.height);
  base.width = base.height = 1;
  if (['exposure', 'shadows', 'contrast', 'temperature', 'saturation'].some(key => recipe[key] !== 0)) {
    // 分块让出主线程，整图导出期间仍可取消，不同时保留整张ImageData。
    for (let row = 0; row < output.height; row += 128) {
      if (!current()) throw new Error('编辑导出已取消');
      const pixels = target.getImageData(0, row, output.width, Math.min(128, output.height - row));
      adjustImagePixels(pixels.data, recipe); target.putImageData(pixels, 0, row);
      if (!maxEdge) await new Promise(resolve => setTimeout(resolve, 0));
    }
  }
  if (!current()) { output.width = output.height = 1; throw new Error('编辑导出已取消'); }
  return output;
}
