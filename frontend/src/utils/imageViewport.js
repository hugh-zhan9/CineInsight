const clamp = (value, min, max) => Math.max(min, Math.min(max, value));
const positive = value => Number.isFinite(Number(value)) && Number(value) > 0 ? Number(value) : 1;

export const initialImageView = () => ({ zoom: 1, x: 0.5, y: 0.5 });

export function imageGeometry(size, view = initialImageView()) {
  const iw = positive(size.imageWidth), ih = positive(size.imageHeight);
  const vw = positive(size.width), vh = positive(size.height);
  const fit = Math.min(1, positive(size.maxScale), vw / iw, vh / ih);
  const zoom = clamp(Number(view.zoom) || 1, 1, Math.max(16, 1 / fit));
  const scale = fit * zoom, width = iw * scale, height = ih * scale;
  const marginX = Math.min(0.5, vw / (2 * width));
  const marginY = Math.min(0.5, vh / (2 * height));
  const x = clamp(Number.isFinite(view.x) ? view.x : 0.5, marginX, 1 - marginX);
  const y = clamp(Number.isFinite(view.y) ? view.y : 0.5, marginY, 1 - marginY);
  return { fit, zoom, x, y, scale, width, height, left: vw / 2 - x * width, top: vh / 2 - y * height };
}

export function zoomImageAt(size, view, zoom, point) {
  const old = imageGeometry(size, view);
  const next = imageGeometry(size, { ...view, zoom });
  const x = (point.x - old.left) / old.width - (point.x - size.width / 2) / next.width;
  const y = (point.y - old.top) / old.height - (point.y - size.height / 2) / next.height;
  const result = imageGeometry(size, { zoom: next.zoom, x, y });
  return { zoom: result.zoom, x: result.x, y: result.y };
}

export function panImage(size, view, dx, dy) {
  const old = imageGeometry(size, view);
  const result = imageGeometry(size, { zoom: old.zoom, x: old.x - dx / old.width, y: old.y - dy / old.height });
  return { zoom: result.zoom, x: result.x, y: result.y };
}

export function naturalImageView(size, view = initialImageView()) {
  return zoomImageAt(size, view, 1 / imageGeometry(size, view).fit, { x: size.width / 2, y: size.height / 2 });
}
