import { build } from 'vite';
import vue from '@vitejs/plugin-vue';
import { writeFileSync } from 'node:fs';
import { resolve } from 'node:path';
const outDir = process.argv[2];
if (!outDir) throw new Error('A temporary output directory is required');
await build({ configFile: false, plugins: [vue()], root: import.meta.dirname,
  define: { 'process.env.NODE_ENV': JSON.stringify('production'), __VUE_OPTIONS_API__: true, __VUE_PROD_DEVTOOLS__: false, __VUE_PROD_HYDRATION_MISMATCH_DETAILS__: false },
  resolve: { alias: { vue: resolve(import.meta.dirname, '../../node_modules/vue/dist/vue.esm-bundler.js') } },
  build: { outDir, emptyOutDir: true, lib: { entry: resolve(import.meta.dirname, 'fixture.js'), name: 'Fixture', formats: ['iife'], fileName: () => 'fixture.js', cssFileName: 'fixture' }, minify: false }
});
writeFileSync(resolve(outDir, 'index.html'), '<!doctype html><html><head><meta charset="utf-8"><link rel="stylesheet" href="fixture.css"><style>body{margin:0;font-family:system-ui}.viewing-notes-page{height:760px;overflow:auto}</style></head><body><div id="app"></div><script src="fixture.js"></script></body></html>');
