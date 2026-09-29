import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const videoListSource = readFileSync(new URL('../src/components/VideoListPage.vue', import.meta.url), 'utf8');
const photoLibrarySource = readFileSync(new URL('../src/components/PhotoLibraryPage.vue', import.meta.url), 'utf8');
// P-030 用 TrashCenterDialog 取代了 TrashRestoreDialog / PhotoTrashDialog 两个旧弹窗；
// 撤销提示条（video-list/TrashUndoBanner.vue）改为按批次撤销，片库页与图片页共用。
const trashDialogSource = readFileSync(new URL('../src/components/TrashCenterDialog.vue', import.meta.url), 'utf8');
const trashUndoSource = readFileSync(new URL('../src/components/video-list/TrashUndoBanner.vue', import.meta.url), 'utf8');

// 回收站入口 2026-09-01 起在「管理」菜单的「维护」组里。
assert.match(videoListSource, /case 'trash': this\.openTrashDialog\(\)/, 'manage menu should expose the trash center');
assert.match(photoLibrarySource, /case 'trash': this\.openTrashDialog\(\)/, 'photo manage menu should expose the trash center');
assert.match(trashUndoSource, /<TrashCenterDialog\b/, 'the undo banner should host the trash center dialog');
assert.match(trashUndoSource, /<TrashUnsupportedDialog\b/, 'deletions on volumes without a trash should ask the user (D-PC02)');
assert.match(trashUndoSource, /RestoreTrashBatch\(/, 'undo should restore the whole delete batch (LIB-12)');
assert.match(trashUndoSource, /CancelBatchDelete\(/, 'batch deletion should be cancellable (IMG-12)');
assert.match(trashUndoSource, /undo-delete-banner/, 'successful deletion should expose an undo banner');
assert.match(trashUndoSource, /if \(!notice \|\| this\.undoing\) return/, 'immediate undo should reject duplicate submissions');
assert.match(videoListSource, /runDelete\(\{ ids, deleteFile/, 'library deletion should go through the result-coded delete flow');
assert.match(videoListSource, /runDelete\(\{ ids: selectedIDs, deleteFile: true/, 'cleanup deletion should use the result-coded delete flow');
assert.match(videoListSource, /showDeleteNotice\(outcome, \{ reportFailures: false \}\)/, 'cleanup deletion should report every successfully recoverable result');
assert.match(photoLibrarySource, /<TrashUndoBanner\b/, 'photo deletion should show the same undo banner (IMG-09)');
assert.match(videoListSource, /await this\.waitForLoadIdle\(\)/, 'consecutive restores should serialize list refreshes');
assert.match(trashDialogSource, /const token = \+\+state\.token/, 'trash dialog should identify each list request');
assert.match(trashDialogSource, /token !== state\.token \|\| !this\.visible/, 'stale trash list responses should be ignored');
assert.match(trashDialogSource, /entry\.last_error/, 'interrupted operations should expose their latest diagnostic');
assert.match(trashDialogSource, /pending_move/, 'interrupted deletes should offer an in-app recovery action');

// 旧的「只列全部 / 单条恢复」绑定由 P-040 删除：前端不能再调用它们。
for (const [name, source] of [['VideoListPage', videoListSource], ['PhotoLibraryPage', photoLibrarySource], ['TrashCenterDialog', trashDialogSource], ['TrashUndoBanner', trashUndoSource]]) {
  assert.doesNotMatch(source, /\b(ListTrashEntries|RestoreTrashEntry|ListImageTrashEntries|RestoreImageTrashEntry)\b(?!Page|s\b)/, `${name} must not call the legacy trash bindings`);
  assert.doesNotMatch(source, /\bBatchDelete(Videos|Images|ImagesInDirectory)\(/, `${name} must use the result-coded delete bindings`);
}

console.log('trash-restore tests passed');
