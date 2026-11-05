import { createApp, h, nextTick } from 'vue';
import PhotoLightbox from '../../src/components/viewer/PhotoLightbox.vue';
import ImageCompareDialog from '../../src/components/ImageCompareDialog.vue';
import ImageViewport from '../../src/components/ImageViewport.vue';
import ImageToolsDialog from '../../src/components/ImageToolsDialog.vue';
import VideoThumbnail from '../../src/components/VideoThumbnail.vue';
import VideoEnhancementPreview from '../../src/components/VideoEnhancementPreview.vue';
import { videoOverviewState } from '../../src/utils/videoThumbnail.js';
import '../../src/styles/tokens.css';
import '../../src/styles/components.css';

function synthetic(width, height) {
  const canvas = document.createElement('canvas'); canvas.width = width; canvas.height = height;
  const context = canvas.getContext('2d');
  const gradient = context.createLinearGradient(0, 0, width, height); gradient.addColorStop(0, '#16344f'); gradient.addColorStop(1, '#d4945a');
  context.fillStyle = gradient; context.fillRect(0, 0, width, height);
  context.strokeStyle = '#fff8'; context.lineWidth = 2;
  for (let x = 0; x < width; x += width / 16) { context.beginPath(); context.moveTo(x, 0); context.lineTo(x, height); context.stroke(); }
  for (let y = 0; y < height; y += height / 8) { context.beginPath(); context.moveTo(0, y); context.lineTo(width, y); context.stroke(); }
  context.fillStyle = '#fff'; context.font = `${width / 25}px system-ui`; context.fillText('合成画面 · 缩放与同步对比', width / 12, height / 2);
  return canvas.toDataURL('image/png');
}
const images = [{ id: 1, name: '合成原图.png', width: 2048, height: 1024, asset_url: synthetic(2048, 1024) }, { id: 2, name: '合成小图.png', width: 1024, height: 512, asset_url: synthetic(1024, 512) }];
const smallPair = [{ name: '原图局部', width: 100, height: 50, asset_url: synthetic(100, 50) }, { name: '2× 增强局部', width: 200, height: 100, asset_url: synthetic(200, 100) }];
let closes = 0;
let views = 0, requestNumber = 0, saved = 0, lastEdited = null;
const requests = new Map();
const originalFetch = window.fetch.bind(window);
window.fetch = (url, options) => String(url).startsWith('/preview/frame/') ? originalFetch(images[0].asset_url, options) : originalFetch(url, options);
window.go = { main: { App: new Proxy({}, { get: (_, name) => async (...args) => {
  if (name === 'GetImageDetail') return { image: images.find(item => item.id === args[0]), people: [] };
  if (name === 'RecordImageView') { views++; return { recorded: true, view_count: views }; }
  if (name === 'GetWallpaperPreflight') return { eligible: false, message: '合成素材' };
  if (name === 'GetEnhancementCapability') return { available: true };
  if (name === 'StartMediaProcessing') {
    const request = args[0], id = `synthetic-${++requestNumber}`;
    let view = { id, state: 'ready', name: request.kind === 'video' ? '合成视频' : images[0].name, width: images[0].width, height: images[0].height, source_url: images[0].asset_url, format: 'png' };
    if (request.operation === 'edit') {
      const data = atob(request.png_base64), bytes = Uint8Array.from(data, char => char.charCodeAt(0));
      const header = new DataView(bytes.buffer); view = { ...view, width: header.getUint32(16), height: header.getUint32(20), result_url: `data:image/png;base64,${request.png_base64}`, output_bytes: bytes.length }; lastEdited = view;
    }
    if (request.operation === 'sample') view = { ...view, format: 'mp4', result_url: new URL('enhanced.mp4', location.href).href, comparison_url: new URL('original.mp4', location.href).href };
    requests.set(id, view); return view;
  }
  if (name === 'GetMediaProcessing') return requests.get(args[0]);
  if (name === 'ReleaseMediaProcessing') { requests.delete(args[0]); return; }
  if (name === 'SaveMediaProcessing') { saved++; return '/synthetic/edited.png'; }
  if (name === 'SetMediaProcessingDirty') return;
  return [];
} }) } };
const root = createApp({ data: () => ({ mode: 'lightbox' }), render() {
  if (this.mode === 'thumbnail') return h('div', { style: 'width:480px;height:270px;margin:60px' }, h(VideoThumbnail, { video: { id: 1, name: '合成视频', duration: 120 }, src: images[0].asset_url }));
  if (this.mode === 'small-compare') return h(ImageCompareDialog, { ref: 'compare', left: smallPair[0], right: smallPair[1], sameDisplaySize: true });
  if (this.mode === 'sample') return h(VideoEnhancementPreview, { ref: 'sample', video: { id: 1, name: '合成视频', duration: 2 }, profile: 'anime' });
  return this.mode === 'compare' ? h(ImageCompareDialog, { ref: 'compare', left: images[0], right: images[1] }) : h(PhotoLightbox, { ref: 'lightbox', image: images[0], assetUrl: images[0].asset_url, onClose: () => { closes++; } });
} }).mount('#app');
const assert = (value, label) => { if (!value) throw new Error(label); };
const wait = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(test, label) { window.progress = label; const end = Date.now() + 8000; while (!test() && Date.now() < end) { await wait(20); await nextTick(); } assert(test(), label); }
async function wheel(viewport, fraction = 0.65) { const rect = viewport.$el.getBoundingClientRect(); viewport.$el.dispatchEvent(new WheelEvent('wheel', { bubbles: true, cancelable: true, deltaY: -180, clientX: rect.left + rect.width * fraction, clientY: rect.top + rect.height / 2 })); await nextTick(); }
function native(phase, x, y) { window.webkit.messageHandlers.nativeClick.postMessage({ phase, x, y }); }

window.fixture = { async run() {
  await until(() => document.querySelector('[data-test=photo-lightbox-img]')?.naturalWidth === 2048, 'decode synthetic full image');
  const box = root.$refs.lightbox, viewport = box.$refs.viewport;
  await until(() => viewport.width > 400, 'measured viewport');
  assert(viewport.geometry.width <= viewport.width + 1, 'fit image without overflow');
  const rect = viewport.$el.getBoundingClientRect(); const point = { x: rect.width * 0.65, y: rect.height / 2 };
  native('down', rect.left + rect.width / 2, rect.top + rect.height / 2); await wait(100);
  native('up', rect.left + rect.width / 2, rect.top + rect.height / 2); await wait(100);
  assert(closes === 0, 'native image click keeps preview open');
  assert(viewport.geometry.top > 8, 'fixture has a visible top margin');
  native('down', rect.left + rect.width / 2, rect.top + 4); await wait(100);
  native('up', rect.left + rect.width / 2, rect.top + 4);
  await until(() => closes === 1, 'native background click requests close exactly once');
  const before = viewport.geometry; const pixel = (point.x - before.left) / before.width;
  await wheel(viewport);
  assert(Math.abs((point.x - viewport.geometry.left) / viewport.geometry.width - pixel) < 0.001, 'pointer pixel stays anchored');
  const oldX = viewport.view.x;
  const pointerEvents = []; const collectPointer = event => pointerEvents.push({ type: event.type, x: event.clientX, y: event.clientY, id: event.pointerId, target: event.target.className });
  for (const name of ['pointerdown', 'pointermove', 'pointerup', 'lostpointercapture']) document.addEventListener(name, collectPointer);
  native('down', rect.left + rect.width / 2, rect.top + rect.height / 2); await wait(100);
  native('drag', rect.left + rect.width / 2 + 70, rect.top + rect.height / 2); await wait(100);
  native('up', rect.left + rect.width / 2 + 70, rect.top + rect.height / 2);
  await until(() => viewport.view.x !== oldX && !viewport.drag, 'native drag pans and releases capture ' + JSON.stringify({ oldX, x: viewport.view.x, drag: viewport.drag, events: pointerEvents }));
  for (const name of ['pointerdown', 'pointermove', 'pointerup', 'lostpointercapture']) document.removeEventListener(name, collectPointer);
  assert(closes === 1, 'native image drag does not close preview');
  document.querySelector('[data-test=photo-lightbox-natural]').click(); await nextTick();
  assert(Math.abs(viewport.geometry.scale - 1) < 0.001, 'original CSS pixel size');
  const previous = { ...viewport.view };
  document.dispatchEvent(new KeyboardEvent('keydown', { key: ' ', code: 'Space', bubbles: true })); await nextTick();
  assert(viewport.geometry.scale > 1, 'hold key magnifies');
  document.dispatchEvent(new KeyboardEvent('keyup', { key: ' ', code: 'Space', bubbles: true })); await nextTick();
  assert(JSON.stringify(viewport.view) === JSON.stringify(previous), 'key release restores viewport');
  document.querySelector('[data-test=photo-lightbox-toggle-info]').click(); await nextTick();
  assert(getComputedStyle(document.querySelector('[data-test=photo-lightbox-info]')).display === 'none', 'information panel hides');
  // The actual editor renders a reduced preview but exports from the frozen full source.
  document.querySelector('[data-test=photo-lightbox-edit]').click();
  await until(() => document.querySelector('[data-test=image-tools-preview]')?.naturalWidth > 0, 'editor source and reduced preview');
  let editor;
  function findEditor(vnode) { if (!vnode) return; if (vnode.component?.type === ImageToolsDialog) editor = vnode.component.proxy; if (vnode.component) findEditor(vnode.component.subTree); if (Array.isArray(vnode.children)) vnode.children.forEach(findEditor); }
  findEditor(box.$.subTree); assert(editor, 'actual editor component');
  const picture = document.querySelector('.image-tools__picture'), displayed = picture.querySelector('img');
  assert(Math.abs(picture.getBoundingClientRect().width - displayed.getBoundingClientRect().width) < 2, 'crop coordinates align with visible image');
  editor.params.rotation = 1; editor.params.crop = { x: 0.25, y: 0.25, width: 0.5, height: 0.5 }; editor.params.exposure = 0.5; editor.commit();
  await wait(180); await nextTick();
  document.querySelector('[data-test=image-edit-export]').click();
  await until(() => saved === 1 && !editor.dirty, 'editor native-save adapter and clean revision');
  assert(lastEdited.width === 512 && lastEdited.height === 1024, 'full source crop exported rather than reduced preview');
  await editor.close(); await nextTick();
  assert(requests.size === 0, 'editor closes temporary sessions');
  root.mode = 'compare'; await nextTick();
  await until(() => [...document.querySelectorAll('.image-compare img')].length === 2 && [...document.querySelectorAll('.image-compare img')].every(image => image.naturalWidth > 0), 'decode two comparison images');
  const compare = root.$refs.compare;
  const components = [];
  function visit(vnode) { if (!vnode) return; if (vnode.component?.type === ImageViewport) components.push(vnode.component.proxy); if (vnode.component) visit(vnode.component.subTree); if (Array.isArray(vnode.children)) vnode.children.forEach(visit); }
  visit(compare.$.subTree);
  assert(components.length === 2, 'two actual viewport components');
  await wheel(components[0]);
  assert(Math.abs(components[0].geometry.x - components[1].geometry.x) < 0.001, 'comparison synchronized focal point');
  assert(Math.abs(components[0].geometry.scale * 2 - components[1].geometry.scale) < 0.001, 'different native pixel scales preserved');
  const beforeViews = views; await wait(1600); assert(views === beforeViews, 'comparison does not record image views');
  root.mode = 'small-compare'; await nextTick();
  await until(() => root.$refs.compare?.$el && document.querySelectorAll('.image-compare img').length === 2 && [...document.querySelectorAll('.image-compare img')].every(img => img.naturalWidth > 0), 'decode small enhancement comparison');
  const smallImages = [...document.querySelectorAll('.image-compare .image-viewport > img')];
  assert(Math.abs(smallImages[0].getBoundingClientRect().width - smallImages[1].getBoundingClientRect().width) < 0.1 && Math.abs(smallImages[0].getBoundingClientRect().width - 100) < 0.1, 'small enhancement uses the same display size without stretching the original');
  root.mode = 'thumbnail'; await nextTick();
  const thumbnail = document.querySelector('.video-time-thumbnail'), thumbRect = thumbnail.getBoundingClientRect();
  thumbnail.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, pointerType: 'mouse', clientX: thumbRect.left + thumbRect.width / 2, clientY: thumbRect.top + 20 }));
  await until(() => thumbnail.querySelector('img')?.src.startsWith('blob:'), 'hover fetches a bounded frame');
  thumbnail.click(); await nextTick(); assert(videoOverviewState.previewRequest?.atMs === 60000, 'thumbnail click requests original preview at exact sampled time');
  thumbnail.dispatchEvent(new PointerEvent('pointerleave')); await nextTick(); assert(!thumbnail.querySelector('img').src.startsWith('blob:'), 'leaving releases thumbnail frame');
  root.mode = 'sample'; await nextTick(); await until(() => root.$refs.sample?.source, 'video sample source snapshot');
  document.querySelector('[data-test=enhancement-preview-start]').click();
  await until(() => document.querySelectorAll('.enhancement-preview video').length === 2 && [...document.querySelectorAll('.enhancement-preview video')].every(video => video.readyState >= 2), 'real WebView decodes both synthetic clips');
  const sample = root.$refs.sample; sample.$refs.original.currentTime = 0.5; await wait(150); sample.seek(); await wait(150);
  assert(Math.abs(sample.$refs.original.currentTime - sample.$refs.enhanced.currentTime) < 0.15, 'sample seek is synchronized');
  await sample.play(); await wait(350); sample.$refs.original.pause(); sample.pause(); assert(sample.$refs.enhanced.currentTime > 0.5, 'sample follower actually plays');
  assert(views === beforeViews, 'sample playback has no image/watch writes'); await sample.close();
  root.mode = 'compare'; await nextTick();
  window.results = { passed: true, cases: ['synthetic-image-decode', 'fit-geometry', 'pointer-anchored-wheel', 'native-pointer-pan', 'original-pixel-size', 'temporary-zoom-restore', 'information-toggle', 'editor-preview-alignment', 'full-resolution-edit-export', 'editor-session-release', 'synchronized-comparison', 'independent-native-scale', 'comparison-statistics-neutral', 'thumbnail-frame-hover', 'thumbnail-time-navigation', 'thumbnail-frame-release', 'synthetic-video-decode', 'sample-seek-sync', 'sample-real-playback', 'sample-statistics-neutral', 'small-enhancement-equal-display-size'] };
} }; window.ready = true;
