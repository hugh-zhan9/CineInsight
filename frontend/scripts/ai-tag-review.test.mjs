import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import {
  confidenceMeta,
  createRejectVideoConfirm,
  groupCandidatesByVideo,
  removeCandidateById
} from '../src/utils/aiTagReview.js';

assert.equal(confidenceMeta('high').label, '高置信');
assert.equal(confidenceMeta('high').className, 'ai-confidence--high');
assert.equal(confidenceMeta('medium').label, '中置信');
assert.equal(confidenceMeta('medium').className, 'ai-confidence--medium');
assert.notEqual(confidenceMeta('high').className, confidenceMeta('medium').className);

const groups = groupCandidatesByVideo([
  { id: 1, video_id: 10, confidence: 'medium', video: { id: 10, name: 'a.mp4', path: '/a.mp4' } },
  { id: 2, video_id: 10, confidence: 'high', video: { id: 10, name: 'a.mp4', path: '/a.mp4' } },
]);
assert.equal(groups.length, 1);
assert.equal(groups[0].candidates[0].confidence, 'high');

const remaining = removeCandidateById([{ id: 1 }, { id: 2 }], 1);
assert.deepEqual(remaining, [{ id: 2 }]);

const confirmState = createRejectVideoConfirm({
  videoId: 42,
  videoName: 'sample-video.mp4',
  candidates: [{ id: 7 }, { id: '8' }],
});

assert.deepEqual(confirmState, {
  show: true,
  videoId: 42,
  videoName: 'sample-video.mp4',
  count: 2,
  candidateIds: [7, 8],
});

assert.equal(createRejectVideoConfirm({ videoId: 42, candidates: [] }), null);
assert.equal(createRejectVideoConfirm(null), null);

const componentSource = readFileSync(new URL('../src/components/AITagReviewDialog.vue', import.meta.url), 'utf8');
assert.match(componentSource, /data-confidence/);
assert.match(componentSource, /ai-confidence--high/);
assert.match(componentSource, /ai-confidence--medium/);
assert.match(componentSource, /ai-confirm-overlay/);
assert.match(componentSource, /RejectAITagCandidatesByVideo/);

console.log('ai-tag-review tests passed');
