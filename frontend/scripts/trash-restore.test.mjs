import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const videoListSource = readFileSync(new URL('../src/components/VideoListPage.vue', import.meta.url), 'utf8');
const trashDialogSource = readFileSync(new URL('../src/components/TrashRestoreDialog.vue', import.meta.url), 'utf8');
// P-002 把撤销提示条与回收站入口抽成了 video-list/TrashUndoBanner.vue，相关断言跟着搬。
const trashUndoSource = readFileSync(new URL('../src/components/video-list/TrashUndoBanner.vue', import.meta.url), 'utf8');

// 回收站入口 2026-09-01 起在「管理」菜单的「维护」组里。
assert.match(videoListSource, /case 'trash': this\.openTrashDialog\(\)/, 'manage menu should expose the trash center');
assert.match(trashUndoSource, /<TrashRestoreDialog\b/, 'video list should render the trash restore dialog');
assert.match(trashUndoSource, /ListTrashEntries/, 'video list should refresh the latest trash entry after deletion');
assert.match(trashUndoSource, /RestoreTrashEntry/, 'video list should support immediate undo');
assert.match(trashUndoSource, /undo-delete-banner/, 'successful deletion should expose an undo banner');
assert.match(videoListSource, /showDeleteUndo\(succeededIds/, 'batch deletion should report recoverable results');
assert.match(videoListSource, /BatchDeleteVideos\(selectedIDs, true\)/, 'cleanup deletion should use partial-failure-aware batch deletion');
assert.match(videoListSource, /showDeleteUndo\(succeededIDs\)/, 'cleanup deletion should report every successfully recoverable result');
assert.match(videoListSource, /await this\.waitForLoadIdle\(\)/, 'consecutive restores should serialize list refreshes');
assert.match(trashUndoSource, /if \(this\.undoing\) return/, 'immediate undo should reject duplicate submissions');
assert.match(trashDialogSource, /const token = \+\+this\.loadToken/, 'trash dialog should identify each list request');
assert.match(trashDialogSource, /token !== this\.loadToken \|\| !this\.visible/, 'stale trash list responses should be ignored');
assert.match(trashDialogSource, /entry\.last_error/, 'interrupted operations should expose their latest diagnostic');
assert.match(trashDialogSource, /pending_move/, 'interrupted deletes should offer an in-app recovery action');

console.log('trash-restore tests passed');
