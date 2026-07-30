import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const videoListSource = readFileSync(new URL('../src/components/VideoListPage.vue', import.meta.url), 'utf8');
const trashDialogSource = readFileSync(new URL('../src/components/TrashRestoreDialog.vue', import.meta.url), 'utf8');

assert.match(videoListSource, /@click="openTrashDialog"[^>]*>回收站<\/button>/, 'video toolbar should expose the trash center');
assert.match(videoListSource, /<TrashRestoreDialog\b/, 'video list should render the trash restore dialog');
assert.match(videoListSource, /ListTrashEntries/, 'video list should refresh the latest trash entry after deletion');
assert.match(videoListSource, /RestoreTrashEntry/, 'video list should support immediate undo');
assert.match(videoListSource, /undo-delete-banner/, 'successful deletion should expose an undo banner');
assert.match(videoListSource, /showDeleteUndo\(succeededIds/, 'batch deletion should report recoverable results');
assert.match(videoListSource, /BatchDeleteVideos\(selectedIDs, true\)/, 'cleanup deletion should use partial-failure-aware batch deletion');
assert.match(videoListSource, /showDeleteUndo\(succeededIDs\)/, 'cleanup deletion should report every successfully recoverable result');
assert.match(videoListSource, /await this\.waitForLoadIdle\(\)/, 'consecutive restores should serialize list refreshes');
assert.match(videoListSource, /if \(this\.undoing\) return/, 'immediate undo should reject duplicate submissions');
assert.match(trashDialogSource, /const token = \+\+this\.loadToken/, 'trash dialog should identify each list request');
assert.match(trashDialogSource, /token !== this\.loadToken \|\| !this\.visible/, 'stale trash list responses should be ignored');
assert.match(trashDialogSource, /entry\.last_error/, 'interrupted operations should expose their latest diagnostic');
assert.match(trashDialogSource, /pending_move/, 'interrupted deletes should offer an in-app recovery action');

console.log('trash-restore tests passed');
