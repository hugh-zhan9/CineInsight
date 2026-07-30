import assert from 'node:assert/strict';
import fs from 'node:fs';

const page = fs.readFileSync(new URL('../src/components/VideoListPage.vue', import.meta.url), 'utf8');
const row = fs.readFileSync(new URL('../src/components/VideoListRow.vue', import.meta.url), 'utf8');
const virtualList = fs.readFileSync(new URL('../src/components/VirtualVideoList.vue', import.meta.url), 'utf8');
const handler = fs.readFileSync(new URL('../../preview_asset_handler.go', import.meta.url), 'utf8');

assert.match(page, /cineinsight-library-layout/, 'layout choice should persist');
assert.match(page, /homeListVirtualizationEnabled && viewMode === 'list'/, 'default list virtualization must remain enabled');
assert.match(row, /\/preview\/thumbnail\/\$\{this\.video\.id\}/, 'rows should use the thumbnail asset route');
assert.match(row, /thumbnailFailed/, 'thumbnail failures need a local placeholder');
assert.match(virtualList, /virtual-video-list--\$\{layoutMode\}/, 'virtual list shell should expose layout styling');
assert.match(handler, /\/preview\/thumbnail\//, 'asset handler should route thumbnail requests');

console.log('visual-library tests passed');
