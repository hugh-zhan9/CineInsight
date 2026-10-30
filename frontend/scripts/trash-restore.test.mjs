import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const videoListSource = readFileSync(new URL('../src/components/VideoListPage.vue', import.meta.url), 'utf8') + '\n' + readFileSync(new URL('../src/components/video-list/libraryQuery.js', import.meta.url), 'utf8');
const photoLibrarySource = readFileSync(new URL('../src/components/PhotoLibraryPage.vue', import.meta.url), 'utf8');
// P-030 用 TrashCenterDialog 取代了 TrashRestoreDialog / PhotoTrashDialog 两个旧弹窗；
// 撤销提示条（video-list/TrashUndoBanner.vue）改为按批次撤销，片库页与图片页共用。
const trashDialogSource = readFileSync(new URL('../src/components/TrashCenterDialog.vue', import.meta.url), 'utf8');
const trashUndoSource = readFileSync(new URL('../src/components/video-list/TrashUndoBanner.vue', import.meta.url), 'utf8');
// 视频清理的删除三期起由清理中心页执行（D-P3-02，原来在片库页里）。
const cleanupCenterSource = readFileSync(new URL('../src/components/manage/CleanupCenterPage.vue', import.meta.url), 'utf8');

// 回收站入口 2026-09-01 起在「管理」菜单的「维护」组里。
assert.match(videoListSource, /case 'trash': this\.openTrashDialog\(\)/, 'manage menu should expose the trash center');
assert.match(photoLibrarySource, /case 'trash': this\.openTrashDialog\(\)/, 'photo manage menu should expose the trash center');
assert.match(trashUndoSource, /<TrashCenterDialog\b/, 'the undo banner should host the trash center dialog');
assert.match(trashUndoSource, /<TrashUnsupportedDialog\b/, 'deletions on volumes without a trash should ask the user (D-PC02)');
assert.match(trashUndoSource, /RestoreTrashBatch\(/, 'undo should restore the whole delete batch (LIB-12)');
assert.match(trashUndoSource, /CancelBatchDelete\(/, 'batch deletion should be cancellable (IMG-12)');
// 统一设计 D-UD-07/09：删除成功后的「撤销」是全局提示，同时记进任务中心「最近操作」（同一条记录，只执行一次）。
assert.match(trashUndoSource, /notifyUndoable\(text, \(\) => this\.undoFromToast\(notice\)\)/, 'successful deletion should offer undo through the global toast and recent operations');
assert.doesNotMatch(trashUndoSource, /undo-delete-banner/, 'the in-page undo strip is gone');
assert.match(trashUndoSource, /throw new Error\(trashErrorText\(err\)\)/, 'a failed undo should surface the reason and keep the record for retry');
assert.match(videoListSource, /runDelete\(\{ ids, deleteFile/, 'library deletion should go through the result-coded delete flow');
assert.match(cleanupCenterSource, /runDelete\(\{ ids: selectedIDs, deleteFile: true/, 'cleanup deletion should use the result-coded delete flow');
assert.match(cleanupCenterSource, /showDeleteNotice\(outcome, \{ reportFailures: false \}\)/, 'cleanup deletion should report every successfully recoverable result');
assert.match(cleanupCenterSource, /<TrashUndoBanner ref="trashUndo" kind="video"/, 'the cleanup center hosts the video delete flow (D-P3-02)');
assert.match(photoLibrarySource, /<TrashUndoBanner\b/, 'photo deletion should show the same undo banner (IMG-09)');
assert.match(videoListSource, /await this\.waitForLoadIdle\(\)/, 'consecutive restores should serialize list refreshes');
assert.match(trashDialogSource, /const token = \+\+state\.token/, 'trash dialog should identify each list request');
assert.match(trashDialogSource, /token !== state\.token \|\| !this\.visible/, 'stale trash list responses should be ignored');
assert.match(trashDialogSource, /entry\.last_error/, 'interrupted operations should expose their latest diagnostic');
assert.match(trashDialogSource, /pending_move/, 'interrupted deletes should offer an in-app recovery action');

// 旧的「只列全部 / 单条恢复」绑定已由 P-040 删除，分页列表随后改回设计名 ListTrashEntries(filter)。
assert.match(trashDialogSource, /ListTrashEntries\(\{/, 'the trash center should list entries through the paged ListTrashEntries(filter)');
for (const [name, source] of [['VideoListPage', videoListSource], ['PhotoLibraryPage', photoLibrarySource], ['TrashCenterDialog', trashDialogSource], ['TrashUndoBanner', trashUndoSource]]) {
  assert.doesNotMatch(source, /\b(ListTrashEntriesPage|RestoreTrashEntry|ListImageTrashEntries|RestoreImageTrashEntry)\b/, `${name} must not call the legacy trash bindings`);
  assert.doesNotMatch(source, /\bBatchDelete(Videos|Images|ImagesInDirectory)\(/, `${name} must use the result-coded delete bindings`);
}

console.log('trash-restore tests passed');
