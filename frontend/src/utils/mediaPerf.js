// The path/URL is used only to classify and match a local WebView timing entry.
// Only fixed labels and elapsed time are forwarded to the local app logger.
const assets = [
  ['/preview/image-thumbnail/', 'photo_thumbnail'],
  ['/preview/image/', 'photo_view'],
  ['/preview/thumbnail/', 'poster'],
  ['/preview/video-artwork/', 'artwork']
];

export function observeMediaLoad(win, enabled, log) {
  const last = new Map();
  function onLoad(event) {
    if (!enabled() || event.target?.tagName !== 'IMG') return;
    const img = event.target;
    if (!img.currentSrc && !img.src) return;
    let url, origin;
    try {
      origin = new URL(win.document.baseURI);
      url = new URL(img.currentSrc || img.src, origin);
    } catch { return; }
    if (url.protocol !== origin.protocol || url.host !== origin.host) return;
    const kind = assets.find(([prefix]) => url.pathname.startsWith(prefix))?.[1];
    if (!kind) return;
    const clock = win.performance;
    const now = clock?.now?.();
    if (!Number.isFinite(now) || now - (last.get(kind) ?? -Infinity) < 5000) return;
    const entry = clock?.getEntriesByName?.(url.href, 'resource')?.at(-1);
    // WKWebView may not expose resource timing on its custom scheme. Skip
    // missing or stale samples instead of recording a fake zero.
    if (!entry || !Number.isFinite(entry.startTime) || !Number.isFinite(entry.responseEnd) ||
        entry.startTime < 0 || entry.responseEnd <= entry.startTime ||
        now < entry.responseEnd || now - entry.responseEnd > 1000) return;
    const elapsed = Math.round(now - entry.startTime);
    if (elapsed < 150) return;
    last.set(kind, now);
    log(`perf component=webview_media stage=${kind}_load_observed duration_ms=${elapsed} status=ok`);
  }
  win.addEventListener('load', onLoad, true);
  return () => win.removeEventListener('load', onLoad, true);
}
