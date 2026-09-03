import assert from 'node:assert/strict';
import fs from 'node:fs';

const page = fs.readFileSync(new URL('../src/components/VideoListPage.vue', import.meta.url), 'utf8');
const row = fs.readFileSync(new URL('../src/components/VideoListRow.vue', import.meta.url), 'utf8');
const virtualList = fs.readFileSync(new URL('../src/components/VirtualVideoList.vue', import.meta.url), 'utf8');
const sharedCss = fs.readFileSync(new URL('../src/styles/components.css', import.meta.url), 'utf8');
const previewDrawer = fs.readFileSync(new URL('../src/components/PreviewDrawer.vue', import.meta.url), 'utf8');
const handler = fs.readFileSync(new URL('../../preview_asset_handler.go', import.meta.url), 'utf8');
const photoPage = fs.readFileSync(new URL('../src/components/PhotoLibraryPage.vue', import.meta.url), 'utf8');
const appShell = fs.readFileSync(new URL('../src/App.vue', import.meta.url), 'utf8');

assert.match(page, /cineinsight-library-layout/, 'layout choice should persist');
assert.match(page, /homeListVirtualizationEnabled && viewMode === 'list'/, 'default list virtualization must remain enabled');
assert.match(row, /\/preview\/thumbnail\/\$\{this\.video\.id\}/, 'rows should use the thumbnail asset route');
assert.match(row, /thumbnailFailed/, 'thumbnail failures need a local placeholder');
assert.match(virtualList, /virtual-video-list--\$\{layoutMode\}/, 'virtual list shell should expose layout styling');
assert.match(virtualList, /\.virtual-video-list\.virtual-video-list--grid\s*{[^}]*display:\s*grid;[^}]*gap:\s*12px;/s, 'the scoped list shell must switch from flex rows to a real grid');
// 原型 A3 把列宽改成 minmax(200px, 1fr)，列数随窗口自增。仍必须是 auto-fill
// 而不是 auto-fit：auto-fill 保留空轨道，最后一行没填满时卡片才不会被拉宽。
assert.match(sharedCss, /\.virtual-video-list--grid\s*{[^}]*grid-template-columns:\s*repeat\(auto-fill,\s*minmax\(200px,\s*1fr\)\)/s, 'grid columns should grow with the window');
assert.doesNotMatch(sharedCss, /repeat\(auto-fit,/, 'auto-fit would stretch a partially filled row');
assert.match(sharedCss, /\.virtual-video-list--grid\s*{[^}]*justify-content:\s*start/s, 'a partially filled grid row should not stretch cards');
assert.match(previewDrawer, /\.preview-drawer__body\s*{[^}]*display:\s*grid[^}]*grid-auto-rows:\s*max-content/s, 'drawer sections must not flex-shrink the player');
assert.match(previewDrawer, /\.detail-section--player\s*{[^}]*min-height:\s*220px/s, 'player section needs a non-collapsing minimum height');
assert.match(previewDrawer, /\.preview-drawer__player-shell\s*{[^}]*aspect-ratio:\s*16\s*\/\s*9[^}]*min-height:\s*220px/s, 'player shell should preserve its 16:9 viewport');
assert.match(previewDrawer, /\.preview-drawer__video\s*{[^}]*position:\s*absolute[^}]*inset:\s*0/s, 'video should fill the preserved player shell');
// 原型 A4：抽屉固定 520px 停靠，列表让出的宽度必须与它一致，
// 窄于 1100 改为覆盖式且列表不再收窄。
assert.match(previewDrawer, /\.preview-drawer\s*{[^}]*width:\s*520px/s, 'the drawer docks at a fixed 520px');
assert.match(sharedCss, /\.page-content--with-preview\s*{[^}]*padding-right:\s*calc\(20px \+ 520px\)/s, 'the list must yield exactly the drawer width');
assert.match(sharedCss, /@media \(max-width: 1100px\)[\s\S]*?\.page-content--with-preview\s*{[^}]*padding-right:\s*20px/s, 'below 1100 the drawer overlays instead of narrowing the list');
assert.match(previewDrawer, /class="preview-drawer" role="dialog"/, 'the drawer is an opaque docked panel, not a glass floating block');

assert.match(handler, /\/preview\/thumbnail\//, 'asset handler should route thumbnail requests');

// 大图查看器：网格必须显式给一行确定高度，否则 img 的 max-height:100% 失效，竖图会被撑出视口。
assert.match(photoPage, /\.photo-viewer\s*{[^}]*grid-template-rows:\s*minmax\(0,\s*1fr\)/s, 'photo viewer should give its stage a definite row height');
assert.match(photoPage, /\.photo-viewer__stage\s*{[^}]*min-height:\s*0/s, 'photo viewer stage should not grow past the viewport');
assert.match(photoPage, /\.photo-viewer__img\s*{[^}]*max-height:\s*100%[^}]*object-fit:\s*contain/s, 'photo viewer image should letterbox instead of overflowing');
// 图片页切走只隐藏不卸载，滚动位置才能留住。
assert.match(appShell, /<PhotoLibraryPage[^>]*v-show="currentPage === 'photos'"/s, 'photo page should stay mounted across tab switches');
assert.match(photoPage, /pageActive\(active\)/, 'photo page should react to becoming active again');
assert.match(photoPage, /restoreScrollPosition\(\)\s*{/, 'photo page should restore the shared scroll position');

// 勾选之后往下滚，批量操作条必须跟着吸顶，否则要滚回顶部才能点"删除所选"。
assert.match(photoPage, /\.photo-toolbar\s*{[^}]*position:\s*sticky/s, 'photo toolbar should stick so batch actions stay reachable');
assert.match(photoPage, /\.photo-selection-tools\b/, 'batch actions live inside the sticky toolbar');

console.log('visual-library tests passed');
