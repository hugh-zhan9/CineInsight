import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import {
  confidenceMeta,
  createRejectVideoConfirm,
  filterCandidatesForReview,
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

const reviewCandidates = [
  { id: 1, suggested_name: '动作', reasoning: 'fast cuts', video: { name: 'fight.mp4', path: '/library/fight.mp4' } },
  { id: 2, suggested_name: '舞蹈', reasoning: 'stage', video: { name: 'dance.mp4', path: '/library/dance.mp4' } },
];
assert.deepEqual(filterCandidatesForReview(reviewCandidates, ''), reviewCandidates);
assert.deepEqual(filterCandidatesForReview(reviewCandidates, 'fight').map(candidate => candidate.id), [1]);
assert.deepEqual(filterCandidatesForReview(reviewCandidates, '舞蹈').map(candidate => candidate.id), [2]);
assert.deepEqual(filterCandidatesForReview(reviewCandidates, '/library/dance').map(candidate => candidate.id), [2]);
assert.deepEqual(filterCandidatesForReview(reviewCandidates, 'missing'), []);

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
assert.match(componentSource, /reviewSearch/);
assert.match(componentSource, /RenameVideo/);
assert.match(componentSource, /renameConfirm/);
assert.match(componentSource, /class="search-input ai-tag-review-search"/, 'AI review search should reuse shared search input styling');
assert.match(componentSource, /class="text-input ai-tag-rename-input"/, 'AI review rename dialog should reuse shared text input styling');
assert.match(componentSource, /class="ai-confirm-dialog glass-surface"/, 'AI review nested confirmations should use shared glass surfaces');
assert.match(componentSource, /class="btn-action btn-compact" @click="previewVideo/, 'AI review preview action should use the shared compact button size');
assert.match(componentSource, /class="btn-secondary btn-compact" @click="openRenameDialog/, 'AI review rename action should use the shared compact button size');
assert.match(componentSource, /class="btn-action btn-compact" @click="retryVideo/, 'AI review retry action should use the shared compact button size');
assert.match(componentSource, /\.ai-tag-review-modal\s*{[^}]*overflow-x:\s*hidden;/s, 'AI review modal should suppress horizontal overflow');
assert.match(componentSource, /\.ai-tag-review-list\s*{[^}]*overflow-x:\s*hidden;/s, 'AI review list should not expose a horizontal scrollbar');
assert.match(componentSource, /\.ai-video-actions\s*{[^}]*flex-wrap:\s*wrap;/s, 'AI review video actions should wrap inside the modal');
assert.match(componentSource, /\.ai-candidate-row\s*{[^}]*grid-template-columns:\s*minmax\(0,\s*1fr\)\s*auto;/s, 'AI review candidate rows should constrain content before action buttons');
assert.match(componentSource, /@media \(max-width: 640px\)/, 'AI review dialog should adapt candidate rows on narrow screens');
assert.doesNotMatch(componentSource, /\.btn-small\b/, 'AI review dialog should not keep a local compact button class');
assert.doesNotMatch(componentSource, /var\(--input-bg\)/, 'AI review dialog should not depend on legacy input background tokens');

console.log('ai-tag-review tests passed');
