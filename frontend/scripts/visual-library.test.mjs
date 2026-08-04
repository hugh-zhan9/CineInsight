import assert from 'node:assert/strict';
import fs from 'node:fs';

const page = fs.readFileSync(new URL('../src/components/VideoListPage.vue', import.meta.url), 'utf8');
const row = fs.readFileSync(new URL('../src/components/VideoListRow.vue', import.meta.url), 'utf8');
const virtualList = fs.readFileSync(new URL('../src/components/VirtualVideoList.vue', import.meta.url), 'utf8');
const sharedCss = fs.readFileSync(new URL('../src/styles/components.css', import.meta.url), 'utf8');
const previewDrawer = fs.readFileSync(new URL('../src/components/PreviewDrawer.vue', import.meta.url), 'utf8');
const handler = fs.readFileSync(new URL('../../preview_asset_handler.go', import.meta.url), 'utf8');

assert.match(page, /cineinsight-library-layout/, 'layout choice should persist');
assert.match(page, /homeListVirtualizationEnabled && viewMode === 'list'/, 'default list virtualization must remain enabled');
assert.match(row, /\/preview\/thumbnail\/\$\{this\.video\.id\}/, 'rows should use the thumbnail asset route');
assert.match(row, /thumbnailFailed/, 'thumbnail failures need a local placeholder');
assert.match(virtualList, /virtual-video-list--\$\{layoutMode\}/, 'virtual list shell should expose layout styling');
assert.match(virtualList, /\.virtual-video-list\.virtual-video-list--grid\s*{[^}]*display:\s*grid;[^}]*gap:\s*12px;/s, 'the scoped list shell must switch from flex rows to a real grid');
assert.match(sharedCss, /\.virtual-video-list--grid\s*{[^}]*grid-template-columns:\s*repeat\(auto-fill,\s*minmax\(200px,\s*220px\)\)/s, 'grid cards should stay compact enough for multiple columns');
assert.match(sharedCss, /\.virtual-video-list--grid\s*{[^}]*justify-content:\s*start/s, 'a partially filled grid row should not stretch cards');
assert.match(previewDrawer, /\.preview-drawer__body\s*{[^}]*display:\s*grid[^}]*grid-auto-rows:\s*max-content/s, 'drawer sections must not flex-shrink the player');
assert.match(previewDrawer, /\.detail-section--player\s*{[^}]*min-height:\s*220px/s, 'player section needs a non-collapsing minimum height');
assert.match(previewDrawer, /\.preview-drawer__player-shell\s*{[^}]*aspect-ratio:\s*16\s*\/\s*9[^}]*min-height:\s*220px/s, 'player shell should preserve its 16:9 viewport');
assert.match(previewDrawer, /\.preview-drawer__video\s*{[^}]*position:\s*absolute[^}]*inset:\s*0/s, 'video should fill the preserved player shell');
assert.match(handler, /\/preview\/thumbnail\//, 'asset handler should route thumbnail requests');

console.log('visual-library tests passed');
