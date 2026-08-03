import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const currentDir = dirname(fileURLToPath(import.meta.url));
const videoListSource = readFileSync(join(currentDir, '../src/components/VideoListPage.vue'), 'utf8');
const videoRowSource = readFileSync(join(currentDir, '../src/components/VideoListRow.vue'), 'utf8');
const tagManagerSource = readFileSync(join(currentDir, '../src/components/TagManagerDialog.vue'), 'utf8');

assert.match(videoListSource, /@click="moveFolder"/, 'toolbar should expose folder migration');
assert.match(videoListSource, /@click="renameFolder"/, 'toolbar should expose folder rename');
assert.match(videoListSource, /@click="moveSelectedVideos"/, 'selection toolbar should expose batch file migration');
assert.match(videoListSource, /SelectMigrationSourceDirectory/, 'folder migration should select an explicit source');
assert.match(videoListSource, /SelectMigrationDestinationDirectory/, 'migration should select an explicit destination');
assert.match(videoListSource, /await MoveDirectory\(source, destinationParent\)/, 'folder migration should call the backend operation');
assert.match(videoListSource, /await RenameDirectory\(source, newName\)/, 'folder rename should call the backend operation');
assert.match(videoListSource, /await BatchMoveVideos\(ids, destination\)/, 'batch migration should call the backend operation');
assert.match(videoListSource, /\[\.\.\.failures, \.\.\.warnings\]\.join\('\\n'\)/, 'mixed batch results should show both failures and retained-source warnings');
assert.match(videoRowSource, /\$emit\('move', video\)/, 'each video row should expose a migration action');

assert.match(tagManagerSource, /合并同义标签/, 'tag manager should explain tag merging');
assert.match(tagManagerSource, /v-model\.number="mergeTargetId"/, 'tag merge should require a retained target');
assert.match(tagManagerSource, /v-model\.trim="mergeKeyword"/, 'tag merge should provide a searchable name filter');
assert.match(tagManagerSource, /v-for="tag in filteredMergeSourceTags"/, 'tag merge should render filtered source choices');
assert.match(tagManagerSource, /type="checkbox"[\s\S]+toggleMergeSource/, 'tag merge should support visible checkbox multi-selection');
assert.match(tagManagerSource, /selectAllVisibleMergeSources/, 'tag merge should support selecting all filtered sources');
assert.match(tagManagerSource, /await MergeTags\(sourceIds, Number\(this\.mergeTargetId\)\)/, 'tag merge should call the backend operation');
assert.match(tagManagerSource, /window\.confirm\(`确定将/, 'destructive tag merge should require final confirmation');
assert.match(tagManagerSource, /Boolean\(tag\.is_system\) === Boolean\(target\.is_system\)/, 'manual and AI-library tags should merge within their own type');
assert.match(tagManagerSource, /mergeType === 'ai'/, 'tag merge should expose a dedicated AI tag type filter');
assert.match(tagManagerSource, /!tag\.automatic_kind/, 'automatic tags should not be manually merged');

console.log('migration and tag-management tests passed');
