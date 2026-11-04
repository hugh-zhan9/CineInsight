// The page owns the clock. Missing/hidden/unloaded cells lose continuous dwell;
// the tracker emits an ID once, while its caller owns durable acknowledgement.
export function createMediaExposureTracker(delay = 300) {
  const started = new Map(), emitted = new Set();
  return {
    update(items, { active = true, now = performance.now() } = {}) {
      if (!active) { started.clear(); return []; }
      const eligible = new Set(), result = [];
      for (const item of items) {
        if (!item.loaded || item.fraction < .5 || item.presented || emitted.has(item.id)) continue;
        eligible.add(item.id);
        if (!started.has(item.id)) started.set(item.id, now);
        if (now - started.get(item.id) >= delay) { result.push(item.id); emitted.add(item.id); started.delete(item.id); }
      }
      for (const id of started.keys()) if (!eligible.has(id)) started.delete(id);
      return result;
    },
    reset() { started.clear(); emitted.clear(); },
    forget(ids) { for (const id of ids) { emitted.delete(id); started.delete(id); } }
  };
}

// Intersect the screen and every clipping ancestor. A scrollable wall/gallery
// can itself be outside App's scroll viewport even when its own cells intersect.
export function mediaVisibleClip(element) {
  let left = 0, top = 0, right = window.innerWidth, bottom = window.innerHeight;
  for (let node = element; node; node = node.parentElement) {
    const style = getComputedStyle(node), rect = node.getBoundingClientRect();
    if (style.display === 'none' || style.visibility === 'hidden' || style.opacity === '0') return { left: 0, top: 0, right: 0, bottom: 0 };
    const clips = value => /^(auto|scroll|hidden|clip)$/.test(value);
    if (node === element || clips(style.overflowX || style.overflow)) { left = Math.max(left, rect.left); right = Math.min(right, rect.right); }
    if (node === element || clips(style.overflowY || style.overflow)) { top = Math.max(top, rect.top); bottom = Math.min(bottom, rect.bottom); }
  }
  return { left, top, right: Math.max(left, right), bottom: Math.max(top, bottom) };
}

export function mediaVisibleFraction(rect, clip) {
  const area = rect.width * rect.height;
  if (area <= 0) return 0;
  return Math.max(0, Math.min(rect.right, clip.right) - Math.max(rect.left, clip.left)) * Math.max(0, Math.min(rect.bottom, clip.bottom) - Math.max(rect.top, clip.top)) / area;
}

export function mediaUnobscured(element, clip) {
  if (!element || typeof document.elementFromPoint !== 'function') return false;
  const rect = element.getBoundingClientRect();
  const left = Math.max(rect.left, clip.left), right = Math.min(rect.right, clip.right), top = Math.max(rect.top, clip.top), bottom = Math.min(rect.bottom, clip.bottom);
  if (right <= left || bottom <= top) return false;
  const hit = document.elementFromPoint((left + right) / 2, (top + bottom) / 2);
  return !!hit && (hit === element || element.contains(hit));
}
