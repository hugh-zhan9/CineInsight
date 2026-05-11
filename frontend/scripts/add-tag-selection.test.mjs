import assert from 'node:assert/strict';
import { selectedTagsFromIds, toggleSelectedTagId, uniqueTagsById } from '../src/utils/addTagSelection.js';

assert.deepEqual(toggleSelectedTagId([], 3), [3]);
assert.deepEqual(toggleSelectedTagId([3, 5], 3), [5]);
assert.deepEqual(toggleSelectedTagId([3], '5'), [3, 5]);
assert.deepEqual(toggleSelectedTagId([3], 0), [3]);

const tags = [
  { id: 1, name: 'a' },
  { id: 2, name: 'b' },
  { id: 1, name: 'a duplicated' },
  { id: 3, name: 'c' },
];

assert.deepEqual(uniqueTagsById(tags).map(tag => tag.name), ['a', 'b', 'c']);
assert.deepEqual(selectedTagsFromIds(tags, [3, 1]).map(tag => tag.name), ['a', 'c']);

console.log('add-tag-selection tests passed');
