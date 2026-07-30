import assert from 'node:assert/strict';
import {
  createDetailNavigator,
  createVideoDetailsDraft,
  detailPlaybackStartMs,
  formatFrameRate,
  mergeCollectionCandidates,
  mergePersonCandidates,
  moveCollectionMember,
  patchVideoFromDetails,
  toggleEntityID,
  validateRatingDraft
} from '../src/utils/mediaDetails.js';

const navigator = createDetailNavigator({ type: 'video', id: 7 });
assert.deepEqual(navigator.current(), { type: 'video', id: 7 });
navigator.push({ type: 'person', id: 3 });
navigator.push({ type: 'collection', id: 9 });
assert.deepEqual(navigator.back(), { type: 'person', id: 3 });
assert.deepEqual(navigator.back(), { type: 'video', id: 7 });
assert.equal(navigator.canGoBack(), false);

const zeroDraft = createVideoDetailsDraft({
  video: { display_title: '', original_title: '', personal_rating: 0 },
  people: [{ person: { id: 2 } }],
  collections: [{ collection: { id: 4 } }]
});
assert.equal(zeroDraft.personalRating, '0');
assert.deepEqual(zeroDraft.personIDs, [2]);
assert.deepEqual(zeroDraft.collectionIDs, [4]);
assert.equal(validateRatingDraft(''), null);
assert.equal(validateRatingDraft('0'), 0);
assert.equal(validateRatingDraft('9.5'), 9.5);
assert.throws(() => validateRatingDraft('0.3'));

assert.deepEqual(toggleEntityID([1, 2], 2), [1]);
assert.deepEqual(toggleEntityID([1, 2], 3), [1, 2, 3]);
assert.deepEqual(toggleEntityID([1, 2, 2], 2, true), [1, 2]);

const actorA = { person: { id: 11, display_name: '演员 A' } };
const actorB = { person: { id: 12, display_name: '演员 B' } };
const afterSecondSearch = mergePersonCandidates([actorA], [actorB], [11, 12]);
assert.deepEqual(afterSecondSearch.map(item => item.person.id), [12, 11], 'a selected actor must remain visible across later searches');

const collectionA = { collection: { id: 21, name: '作品集 A' } };
const collectionB = { collection: { id: 22, name: '作品集 B' } };
assert.deepEqual(mergeCollectionCandidates([collectionA], [collectionB], [21, 22]).map(item => item.collection.id), [22, 21]);
assert.equal(formatFrameRate('24000/1001', ''), '23.976 fps');
assert.equal(formatFrameRate('0/0', '30000/1001'), '29.97 fps');
assert.equal(formatFrameRate('0/0', ''), '未知帧率');

const members = [{ video: { id: 1 } }, { video: { id: 2 } }, { video: { id: 3 } }];
const moved = moveCollectionMember(members, 0, 2);
assert.deepEqual(moved.map(item => item.video.id), [2, 3, 1]);
assert.deepEqual(members.map(item => item.video.id), [1, 2, 3], 'reorder must not mutate server snapshot');

const patched = patchVideoFromDetails(
  { id: 7, name: 'file.mkv', tags: [{ id: 1 }] },
  { video: { id: 7, name: 'file.mkv', display_title: 'Display', personal_rating: 8.5 } }
);
assert.equal(patched.display_title, 'Display');
assert.equal(patched.personal_rating, 8.5);
assert.deepEqual(patched.tags, [{ id: 1 }], 'narrow detail patch preserves loaded tags when absent from response');

assert.equal(detailPlaybackStartMs({ entryID: 7, rootVideoID: 7, explicitStartTimeMs: 4200, rootResumePositionSeconds: 30, nestedResumePositionSeconds: 90 }), 4200);
assert.equal(detailPlaybackStartMs({ entryID: 8, rootVideoID: 7, explicitStartTimeMs: 4200, rootResumePositionSeconds: 30, nestedResumePositionSeconds: 90 }), 90000);
assert.equal(detailPlaybackStartMs({ entryID: 8, rootVideoID: 0, explicitStartTimeMs: null, rootResumePositionSeconds: 0, nestedResumePositionSeconds: 12.5 }), 12500);

console.log('media details behavior tests passed');
