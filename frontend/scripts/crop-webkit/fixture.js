import { createApp, h, nextTick } from 'vue';
import CropEditor from '../../src/components/video-workbench/CropEditor.vue';
import CropTimeline from '../../src/components/video-workbench/CropTimeline.vue';
import '../../src/styles/tokens.css';
import '../../src/styles/components.css';
import '../../src/components/video-workbench/workbench.css';
const Timeline = { ...CropTimeline, methods: { ...CropTimeline.methods, frame() { return new URL('./preview.png', location.href).href; } } };
const Editor = { ...CropEditor, components: { ...CropEditor.components, CropTimeline: Timeline }, computed: { ...CropEditor.computed, frameURL() { return new URL('./preview.png', location.href).href; } } };
const root = createApp({
 data: () => ({ recipe: { v: 1, crop: { items: [{ video_id: 1, rect: { x: 112, y: 10, width: 96, height: 160 }, source_version: 'fixture', confirmed: false }] }, tracks: { audio: [], subtitle: [] } } }),
 render() { return h(Editor, { recipe: this.recipe, editable: true, navigateFrame: async ({ at_ms, direction }) => ({ at_ms: Math.min(1999, Math.max(0, at_ms + direction * 40)) }),
  preflight: { sources: [{ video_id: 1, display_width: 320, display_height: 180, source_version: 'fixture', duration_ms: 2000 }] },
  videos: { 1: { name: '带左右黑边的竖屏视频.mp4' } },
  onChange: recipe => { this.recipe = recipe; }
 }); }
}).mount('#app');
const assert = (value, message) => { if (!value) throw new Error(message); };
const tick = () => new Promise(resolve => setTimeout(resolve, 30));
async function dragHandle(node, dx, dy) {
 node.scrollIntoView({ block: 'center' }); await tick();
 const r = node.getBoundingClientRect(), x = r.left + r.width / 2, y = r.top + r.height / 2;
 window.webkit.messageHandlers.nativeClick.postMessage({ x, y, phase: 'down' }); await tick();
 for (let step = 1; step <= 5; step++) { window.webkit.messageHandlers.nativeClick.postMessage({ x: x + dx * step / 5, y: y + dy * step / 5, phase: 'drag' }); await tick(); }
 window.webkit.messageHandlers.nativeClick.postMessage({ x: x + dx, y: y + dy, phase: 'up' }); await tick();
}
window.fixture = { async run() {
 await nextTick();
 const img = document.querySelector('.crop-stage > img');
 for (let i = 0; i < 100 && !img.complete; i++) await tick();
 assert(img.naturalWidth === 320, 'synthetic frame loaded');
 const stage = document.querySelector('.crop-stage');
 const box = document.querySelector('.crop-box');
 const bounds = stage.getBoundingClientRect();
 const selected = box.getBoundingClientRect();
 assert(Math.abs(selected.width / bounds.width - .3) < .005, 'crop overlay width corresponds to source pixels');
 [...document.querySelectorAll('button')].find(button => button.textContent === '结果预览').click(); await nextTick();
 const result = document.querySelector('.crop-result').getBoundingClientRect();
 assert(Math.abs(result.width / result.height - .6) < .01, 'portrait result ratio');
 [...document.querySelectorAll('button')].find(button => button.textContent === '裁剪').click(); await nextTick();
 const inspector = document.querySelector('.crop-inspector').getBoundingClientRect();
 assert(inspector.left >= bounds.right, 'settings beside the monitor');
 assert(document.querySelector('.crop-timeline').getBoundingClientRect().top >= bounds.bottom, 'timeline below the monitor');
 assert(document.documentElement.scrollWidth <= innerWidth, 'no horizontal overflow');
 const left = document.querySelector('[data-test="crop-margin-left"]');
 left.value = '110'; left.dispatchEvent(new Event('change', { bubbles: true })); await nextTick();
 assert(root.recipe.crop.items[0].rect.width === 98, 'pixel change saved');
 const ratio = document.querySelector('[data-test="crop-ratio"]');
 ratio.value = '1:1'; ratio.dispatchEvent(new Event('change', { bubbles: true })); await nextTick();
 const r = root.recipe.crop.items[0].rect;
 assert(r.width === r.height, 'square ratio');
 const confirm = document.querySelector('[data-test="crop-confirm"]');
 confirm.click(); await nextTick();
 assert(root.recipe.crop.items[0].confirmed, 'confirm saved');
 document.querySelector('[data-test="crop-reset"]').click(); await nextTick();
 assert(!root.recipe.crop.items[0].confirmed && root.recipe.crop.items[0].rect.width === 320, 'reset clears confirmation');
 root.recipe.crop.items[0].rect = { x: 112, y: 10, width: 96, height: 160 }; await nextTick();
 const first = root.recipe.crop.items[0];
 root.recipe.crop.items = Array.from({ length: 200 }, (_, i) => ({ ...first, video_id: i + 1 })); await nextTick();
 const sources = document.querySelector('.crop-sources');
 assert(sources.scrollHeight > sources.clientHeight, '200 sources scroll independently');
 assert(document.querySelector('.crop-timeline').getBoundingClientRect().bottom < 850, 'batch timeline remains reachable');
 root.recipe.crop.items = [first]; await nextTick();
 const startHandle = document.querySelector('[data-test="crop-time-start"]');
 startHandle.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true })); await nextTick();
 assert(root.recipe.crop.items[0].range.start_ms === 1 && !root.recipe.crop.items[0].confirmed, 'trim handle keyboard adjustment saves range');
 document.querySelector('[data-test="crop-range-reset"]').click(); await nextTick();
 assert(root.recipe.crop.items[0].range === null, 'restore whole source');
 const zoom = document.querySelector('[data-test="crop-zoom"]');
 zoom.value = '4'; zoom.dispatchEvent(new Event('change', { bubbles: true })); await nextTick();
 const monitor = document.querySelector('.crop-monitor');
 assert(monitor.scrollWidth > monitor.clientWidth, 'zoomed canvas can scroll without widening page');
 assert(document.documentElement.scrollWidth <= innerWidth, 'zoom has no page overflow');
 zoom.value = '1'; zoom.dispatchEvent(new Event('change', { bubbles: true })); await nextTick();
 assert(document.querySelectorAll('.crop-filmstrip-images img').length === 7, 'filmstrip available');
 let trusted = false;
 document.querySelector('[data-test="crop-time-start"]').addEventListener('pointerdown', e => { trusted = e.isTrusted; }, { once: true });
 await dragHandle(document.querySelector('[data-test="crop-time-start"]'), 80, 0);
 assert(trusted && root.recipe.crop.items[0].range?.start_ms > 0, 'trusted native trim drag saves selection');
 document.querySelector('[data-test="crop-range-reset"]').click(); await nextTick();
 const before = root.recipe.crop.items[0].rect.width;
 await dragHandle(document.querySelector('[data-test="crop-handle-e"]'), -20, 0);
 assert(root.recipe.crop.items[0].rect.width < before, 'trusted native edge drag resizes crop');
 const iframe = document.createElement('iframe');
 iframe.style.cssText = 'position:absolute;left:-10000px;width:600px;height:1000px;border:0';
 const loaded = new Promise(resolve => { iframe.onload = resolve; });
 iframe.srcdoc = `<html><head><link rel="stylesheet" href="${new URL('./fixture.css', location.href).href}"><style>body{margin:0;padding:12px;font-family:system-ui}</style></head><body><div id="narrow"></div></body></html>`;
 document.body.appendChild(iframe); await loaded;
 const narrow = createApp({ render() { return h(Editor, { recipe: root.recipe, editable: true, preflight: { sources: [{ video_id: 1, display_width: 320, display_height: 180, source_version: 'fixture', duration_ms: 2000 }] } }); } });
 narrow.mount(iframe.contentDocument.getElementById('narrow')); await nextTick(); await tick();
 const doc = iframe.contentDocument;
 assert(doc.documentElement.scrollWidth <= 600, '600px editor has no horizontal overflow');
 assert(doc.querySelector('.crop-inspector').getBoundingClientRect().top >= doc.querySelector('.crop-workspace').getBoundingClientRect().bottom, 'narrow settings stack below monitor and timeline');
 narrow.unmount(); iframe.remove();
 document.documentElement.scrollTop = 0;
 window.results = { passed: true, checks: ['image load', 'overlay pixel geometry', 'portrait preview', 'no overflow', 'pixel edit', 'fixed ratio', 'confirmation', 'reset'], stage: { width: bounds.width, height: bounds.height } };
} };
window.ready = true;
