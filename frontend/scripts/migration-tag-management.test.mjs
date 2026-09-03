import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const currentDir = dirname(fileURLToPath(import.meta.url));
const videoListSource = readFileSync(join(currentDir, '../src/components/VideoListPage.vue'), 'utf8');
// P-002 把两个重命名弹窗抽成了独立组件，重命名相关断言跟着搬。
const renameDialogsSource = readFileSync(join(currentDir, '../src/components/video-list/RenameDialogs.vue'), 'utf8');
// 批量操作栏与吸顶工具栏也各自成组件了。
const batchBarSource = readFileSync(join(currentDir, '../src/components/video-list/BatchActionBar.vue'), 'utf8');
const toolbarSource = readFileSync(join(currentDir, '../src/components/video-list/LibraryToolbar.vue'), 'utf8');
const videoRowSource = readFileSync(join(currentDir, '../src/components/VideoListRow.vue'), 'utf8');
const tagManagerSource = readFileSync(join(currentDir, '../src/components/TagManagerDialog.vue'), 'utf8');

// 2026-09-01 重构后这两个动作收进了「管理」菜单的「整理」组，入口从按钮
// 变成菜单项，但必须仍然可达。
assert.match(videoListSource, /case 'move-folder': this\.moveFolder\(\)/, 'manage menu should expose folder migration');
assert.match(videoListSource, /case 'rename-folder': this\.renameFolder\(\)/, 'manage menu should expose folder rename');
assert.match(batchBarSource, /@click="\$emit\('batch-move'\)"/, 'selection toolbar should expose batch file migration');
assert.match(videoListSource, /@batch-move="moveSelectedVideos"/, 'the page still performs the batch migration itself');
assert.match(videoListSource, /SelectMigrationSourceDirectory/, 'folder migration should select an explicit source');
assert.match(videoListSource, /SelectMigrationDestinationDirectory/, 'migration should select an explicit destination');
assert.match(videoListSource, /await MoveDirectory\(source, destinationParent\)/, 'folder migration should call the backend operation');
assert.match(renameDialogsSource, /await RenameDirectory\(source, newName\)/, 'folder rename should call the backend operation');
assert.match(videoListSource, /await BatchMoveVideos\(ids, destination\)/, 'batch migration should call the backend operation');
assert.match(videoListSource, /\[\.\.\.failures, \.\.\.warnings\]\.join\('\\n'\)/, 'mixed batch results should show both failures and retained-source warnings');
// 单条迁移 2026-09-01 起在行内 ⋯ 菜单的「文件」组里。
assert.match(videoRowSource, /open-row-menu/, 'each video row should expose the shared action menu');
assert.match(videoListSource, /case 'move': this\.moveVideo\(video\)/, 'the row menu should expose a migration action');

assert.match(tagManagerSource, /合并同义标签/, 'tag manager should explain tag merging');
assert.match(tagManagerSource, /v-model\.number="mergeTargetId"/, 'tag merge should require a retained target');
assert.match(tagManagerSource, /v-model\.trim="mergeKeyword"/, 'tag merge should provide a searchable name filter');
assert.match(tagManagerSource, /v-for="tag in filteredMergeSourceTags"/, 'tag merge should render filtered source choices');
assert.match(tagManagerSource, /type="checkbox"[\s\S]+toggleMergeSource/, 'tag merge should support visible checkbox multi-selection');
assert.match(tagManagerSource, /selectAllVisibleMergeSources/, 'tag merge should support selecting all filtered sources');
assert.match(tagManagerSource, /await MergeTags\(sourceIds, Number\(this\.mergeTargetId\)\)/, 'tag merge should call the backend operation');
assert.match(tagManagerSource, /confirmAction\(\{ title: '合并标签'/, 'destructive tag merge should require final confirmation');
assert.match(tagManagerSource, /return this\.mergeableTags\.filter\(tag => Number\(tag\.id\) !== Number\(target\.id\)\)/, 'source choices should include both ordinary and AI-library tags');
assert.match(tagManagerSource, /mergeType === 'ai'/, 'tag merge should expose a dedicated AI tag type filter');
assert.match(tagManagerSource, /!tag\.automatic_kind/, 'automatic tags should not be manually merged');

console.log('migration and tag-management tests passed');
