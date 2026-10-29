import { createApp, h, nextTick } from 'vue';
import CropEditor from '../../src/components/video-workbench/CropEditor.vue';
import '../../src/styles/tokens.css';
import '../../src/styles/components.css';
import '../../src/components/video-workbench/workbench.css';
const Editor = { ...CropEditor, computed: { ...CropEditor.computed, frameURL() { return new URL('./preview.png', location.href).href; } } };
const root = createApp({
 data: () => ({ recipe: { v: 1, crop: { items: [{ video_id: 1, rect: { x: 112, y: 10, width: 96, height: 160 }, source_version: 'fixture', confirmed: false }] }, tracks: { audio: [], subtitle: [] } } }),
 render() { return h(Editor, { recipe: this.recipe, editable: true,
  preflight: { sources: [{ video_id: 1, display_width: 320, display_height: 180, source_version: 'fixture', duration_ms: 2000 }] },
  videos: { 1: { name: '带左右黑边的竖屏视频.mp4' } },
  onChange: recipe => { this.recipe = recipe; }
 }); }
}).mount('#app');
const assert = (value, message) => { if (!value) throw new Error(message); };
const tick = () => new Promise(resolve => setTimeout(resolve, 30));
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
 const result = document.querySelector('.crop-result').getBoundingClientRect();
 assert(Math.abs(result.width / result.height - .6) < .01, 'portrait result ratio');
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
 window.results = { passed: true, checks: ['image load', 'overlay pixel geometry', 'portrait preview', 'no overflow', 'pixel edit', 'fixed ratio', 'confirmation', 'reset'], stage: { width: bounds.width, height: bounds.height } };
} };
window.ready = true;
