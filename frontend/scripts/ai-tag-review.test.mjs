import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import {
  appendCandidates,
  applyBatchApprovalResult,
  approvableCandidateIDs,
  batchApprovalSummaryText,
  candidateTagOptions,
  confidenceMeta,
  createRejectVideoConfirm,
  filterCandidatesByAttributes,
  filterCandidatesForReview,
  groupCandidatesByVideo,
  removeCandidateById,
  removeCandidatesAfterApproval,
  removeCandidatesByMedia,
  removeCandidatesForManualTags
} from '../src/utils/aiTagReview.js';

assert.equal(confidenceMeta('high').label, '高置信');
assert.equal(confidenceMeta('high').className, 'ai-confidence--high');
assert.equal(confidenceMeta('medium').label, '中置信');
assert.equal(confidenceMeta('medium').className, 'ai-confidence--medium');
assert.notEqual(confidenceMeta('high').className, confidenceMeta('medium').className);

const groups = groupCandidatesByVideo([
  { id: 1, video_id: 10, confidence: 'medium', video: { id: 10, name: 'a.mp4', path: '/a.mp4', tags: [{ id: 3, name: '已有标签' }] } },
  { id: 2, video_id: 10, confidence: 'high', video_deleted: true, video: { id: 10, name: 'a.mp4', path: '/a.mp4', tags: [{ id: 3, name: '已有标签' }] } },
]);
assert.equal(groups.length, 1);
assert.equal(groups[0].candidates[0].confidence, 'high');
assert.equal(groups[0].videoName, 'a.mp4');
assert.equal(groups[0].videoDeleted, true);
assert.equal(groups[0].video.id, 10);
assert.deepEqual(groups[0].videoTags.map(tag => tag.name), ['已有标签']);

const remaining = removeCandidateById([{ id: 1 }, { id: 2 }], 1);
assert.deepEqual(remaining, [{ id: 2 }]);

const reviewCandidates = [
  { id: 1, suggested_name: '动作', reasoning: 'fast cuts', video: { name: 'fight.mp4', path: '/library/fight.mp4', tags: [{ id: 9, name: '功夫' }] } },
  { id: 2, suggested_name: '舞蹈', reasoning: 'stage', video: { name: 'dance.mp4', path: '/library/dance.mp4' } },
];
assert.deepEqual(filterCandidatesForReview(reviewCandidates, ''), reviewCandidates);
assert.deepEqual(filterCandidatesForReview(reviewCandidates, 'fight').map(candidate => candidate.id), [1]);
assert.deepEqual(filterCandidatesForReview(reviewCandidates, '舞蹈').map(candidate => candidate.id), [2]);
assert.deepEqual(filterCandidatesForReview(reviewCandidates, '/library/dance').map(candidate => candidate.id), [2]);
assert.deepEqual(filterCandidatesForReview(reviewCandidates, '功夫').map(candidate => candidate.id), [1]);
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

// 游标翻页的三个纯函数：追加去重、整媒体移除、接受后按后端同一规则局部移除。
const firstPage = [{ id: 9, video_id: 10 }, { id: 8, video_id: 10 }];
const secondPage = [{ id: 8, video_id: 10 }, { id: 7, video_id: 11 }];
assert.deepEqual(
  appendCandidates(firstPage, secondPage).map(candidate => candidate.id),
  [9, 8, 7],
  'appending a page must dedupe by id and keep the id-desc order'
);
assert.deepEqual(appendCandidates(null, null), []);
assert.deepEqual(appendCandidates(firstPage, []).map(candidate => candidate.id), [9, 8]);

assert.deepEqual(
  removeCandidatesByMedia(
    [{ id: 1, video_id: 10 }, { id: 2, video_id: 10 }, { id: 3, video_id: 11 }],
    'video_id',
    10
  ).map(candidate => candidate.id),
  [3],
  'rejecting a whole video should drop exactly its candidates'
);

// 接受一条候选：后端连同"同媒体同名"的另一条一起置 superseded，别的媒体不动。
const approvalPool = [
  { id: 1, image_id: 10, normalized_name: '海边', matched_tag_id: 10 },
  { id: 2, image_id: 10, normalized_name: '海边', matched_tag_id: 10 },
  { id: 3, image_id: 10, normalized_name: '日落', matched_tag_id: 11 },
  { id: 4, image_id: 11, normalized_name: '海边', matched_tag_id: 10 },
];
assert.deepEqual(
  removeCandidatesAfterApproval(approvalPool, approvalPool[0], { status: 'approved' }, 'image_id')
    .map(candidate => candidate.id),
  [3, 4],
  'approving should drop the approved row and its same-media same-name siblings only'
);
// META-06：D-PC28 规则 2 之后手工标签不再让整媒体的候选作废，前端也不能整媒体移除——
// 同媒体其他标签的候选必须留在列表里（此前这里断言的是相反的旧行为）。
assert.deepEqual(
  removeCandidatesAfterApproval(approvalPool, approvalPool[0], { status: 'superseded' }, 'image_id')
    .map(candidate => candidate.id),
  [3, 4],
  'META-06 approval never voids the other-tag candidates of the same media'
);
// 同一标签 ID 的候选一起移除，与后端 SQL 一致。
assert.deepEqual(
  removeCandidatesAfterApproval(
    [
      { id: 1, video_id: 5, normalized_name: '动作', matched_tag_id: 20, suggested_name: '动作' },
      { id: 2, video_id: 5, normalized_name: '动作', matched_tag_id: 20, suggested_name: '打斗' },
      { id: 3, video_id: 5, normalized_name: '动作片', matched_tag_id: 21, suggested_name: '动作片' },
    ],
    { id: 1, video_id: 5, normalized_name: '动作', matched_tag_id: 20, suggested_name: '动作' },
    { status: 'approved' },
    'video_id'
  ).map(candidate => candidate.id),
  [3],
  'the sibling rule must key on matched_tag_id, like the backend SQL does'
);

const componentSource = readFileSync(new URL('../src/components/AITagReviewDialog.vue', import.meta.url), 'utf8');
assert.match(componentSource, /data-confidence/);
assert.match(componentSource, /ai-confidence--high/);
assert.match(componentSource, /ai-confidence--medium/);
assert.match(componentSource, /ai-confirm-overlay/);
assert.match(componentSource, /RejectAITagCandidatesByVideo/);
assert.match(componentSource, /reviewSearch/);
assert.match(componentSource, /RenameVideo/);
assert.match(componentSource, /ai-video-deleted-badge/);
assert.match(componentSource, /candidate\.video_deleted/);
assert.match(componentSource, /renameConfirm/);
assert.match(componentSource, /ListSameSourceRelations/);
assert.match(componentSource, /MarkSameSourceRelationRead/);
assert.match(componentSource, /RejectSameSourceRelation/);
assert.match(componentSource, /data-test="ai-candidate-review-tab"/, 'AI tag candidates should have a dedicated workbench tab');
assert.match(componentSource, /data-test="same-source-review-tab"/, 'same-source candidates should have a dedicated workbench tab');
assert.match(componentSource, /relation\.video_a\?\.path/, 'same-source review should show the original A path');
assert.match(componentSource, /relation\.video_b\?\.path/, 'same-source review should show the original B path');
assert.match(componentSource, /thumbnailURL\(relation\.video_a_id\)/, 'same-source review should show the A thumbnail');
assert.match(componentSource, /thumbnailURL\(relation\.video_b_id\)/, 'same-source review should show the B thumbnail');
assert.match(componentSource, /same-source-thumbnail--failed/, 'same-source thumbnails need a local failure placeholder');
// P-034：同源删除改走带结果码的删除与整批撤销条（D-PC04），仍只删记录、保留原文件。
assert.match(componentSource, /runDelete\(\{ ids: \[videoId\], deleteFile: false/, 'same-source deletion should preserve the original file');
assert.doesNotMatch(componentSource, /\bDeleteVideo\(/, 'same-source deletion must not call the legacy single delete binding');
assert.match(componentSource, /删除 A/);
assert.match(componentSource, /删除 B/);
assert.match(componentSource, /不是同源/);
assert.match(componentSource, /relation\.video_a_deleted/);
assert.match(componentSource, /relation\.video_b_deleted/);
assert.match(componentSource, /手动添加标签/);
assert.match(componentSource, /group\.videoTags/);
assert.match(componentSource, /<AddTagDialog/);
assert.match(componentSource, /handleManualTagAdded/);
assert.match(componentSource, /class="search-input ai-tag-review-search"/, 'AI review search should reuse shared search input styling');
assert.match(componentSource, /class="text-input ai-tag-rename-input"/, 'AI review rename dialog should reuse shared text input styling');
assert.match(componentSource, /class="ai-confirm-dialog glass-surface"/, 'AI review nested confirmations should use shared glass surfaces');
assert.match(componentSource, /class="btn-secondary btn-compact" @click="previewVideo/, 'AI review preview action should use the shared compact button size');
assert.match(componentSource, /class="btn-secondary btn-compact" @click="openRenameDialog/, 'AI review rename action should use the shared compact button size');
assert.match(componentSource, /class="btn-secondary btn-compact" @click="retryVideo/, 'AI review retry action should use the shared compact button size');
// 候选没有上限：审阅工作台必须走分页接口，并把"还有下一页"做成显式入口。
assert.match(componentSource, /ListAITagCandidatePage\(0, '', 'pending', 0, 0\)/, 'the workbench should load the first candidate page, not every candidate');
assert.match(componentSource, /ListAITagCandidatePage\(0, '', 'pending', cursor, limit\)/, 'load-more should continue from the page cursor');
assert.match(componentSource, /data-test="ai-candidate-load-more"/, 'the workbench needs an explicit load-more entry');
assert.match(componentSource, /loadAllCandidatesForSearch/, 'keyword search must still cover every pending candidate, not only loaded pages');
assert.doesNotMatch(componentSource, /ListAITagCandidates\(/, 'the workbench should no longer pull the unbounded candidate list');
assert.match(componentSource, /thumbnailURL\(group\.videoId\)/, 'AI tag candidate groups should show the video thumbnail');
assert.match(componentSource, /ai-video-thumbnail--failed/, 'AI tag candidate thumbnails need a local failure placeholder');
assert.match(componentSource, /:deep\(\.ai-tag-review-modal\)\s*{[^}]*overflow-x:\s*hidden;/s, 'AI review modal should suppress horizontal overflow');
assert.match(componentSource, /\.ai-review-workbench-content\s*{[^}]*min-height:\s*0;[^}]*overflow-x:\s*hidden;[^}]*overflow-y:\s*auto;/s, 'AI review workbench should own a vertically scrollable content area');
assert.match(componentSource, /\.ai-video-actions\s*{[^}]*flex-wrap:\s*wrap;/s, 'AI review video actions should wrap inside the modal');
assert.match(componentSource, /\.ai-candidate-row\s*{[^}]*grid-template-columns:\s*minmax\(0,\s*1fr\)\s*auto;/s, 'AI review candidate rows should constrain content before action buttons');
assert.match(componentSource, /@media \(max-width: 640px\)/, 'AI review dialog should adapt candidate rows on narrow screens');
assert.doesNotMatch(componentSource, /\.btn-small\b/, 'AI review dialog should not keep a local compact button class');
assert.doesNotMatch(componentSource, /var\(--input-bg\)/, 'AI review dialog should not depend on legacy input background tokens');

console.log('ai-tag-review tests passed');

const caseDistinct = [
  { id: 101, video_id: 5, matched_tag_id: 30, normalized_name: 'action' },
  { id: 102, video_id: 5, matched_tag_id: 31, normalized_name: 'action' },
];
assert.deepEqual(removeCandidatesAfterApproval(caseDistinct, caseDistinct[0], {status:'approved'}, 'video_id').map(c=>c.id), [102]);

// META-06：手动加标签后按 (video_id, tag_id) 局部移除，同视频其他标签、别的视频同标签都保留。
const manualPool = [
  { id: 1, video_id: 5, matched_tag_id: 20 },
  { id: 2, video_id: 5, matched_tag_id: 21 },
  { id: 3, video_id: 6, matched_tag_id: 20 },
  { id: 4, video_id: 5, matched_tag_id: 20 },
];
assert.deepEqual(removeCandidatesForManualTags(manualPool, 5, [20]).map(c => c.id), [2, 3], 'META-06 manual tag removes only (video, tag) matches');
assert.deepEqual(removeCandidatesForManualTags(manualPool, 5, []).map(c => c.id), [1, 2, 3, 4], 'META-06 no tag ids means no removal');
assert.deepEqual(removeCandidatesForManualTags(manualPool, 0, [20]).map(c => c.id), [1, 2, 3, 4]);

// META-11：批量批准只交出可批准的 ID；结果按逐项局部移除，superseded 带原因归并。
const batchPool = [
  { id: 1, video_id: 5, matched_tag_id: 20, confidence: 'high' },
  { id: 2, video_id: 5, matched_tag_id: 20, confidence: 'medium' },
  { id: 3, video_id: 5, matched_tag_id: 21, confidence: 'low' },
  { id: 4, video_id: 6, matched_tag_id: 20, confidence: 'high', video_deleted: true },
  { id: 5, video_id: 7, matched_tag_id: 22, confidence: 'high', matched_tag: { id: 22, name: '舞蹈' } },
];
assert.deepEqual(approvableCandidateIDs(batchPool), [1, 2, 5], 'META-11 low confidence and deleted videos are never submitted');
assert.deepEqual(filterCandidatesByAttributes(batchPool, { confidence: 'high' }).map(c => c.id), [1, 4, 5]);
assert.deepEqual(filterCandidatesByAttributes(batchPool, { tagId: 20 }).map(c => c.id), [1, 2, 4]);
assert.deepEqual(filterCandidatesByAttributes(batchPool, { confidence: 'high', tagId: 20 }).map(c => c.id), [1, 4]);
assert.deepEqual(candidateTagOptions([{ matched_tag_id: 22, matched_tag: { name: '舞蹈' } }, { matched_tag_id: 20, suggested_name: '动作' }, { matched_tag_id: 22 }]).map(t => t.id), [20, 22]);

const applied = applyBatchApprovalResult(batchPool, {
  requested: 3, succeeded: 1, superseded: 1, failed: 1,
  results: [
    { id: 1, ok: true, item: { id: 1, video_id: 5, matched_tag_id: 20 } },
    { id: 2, superseded: true, message: '同标签的其他候选已批准' },
    { id: 5, message: '候选标签已不在 AI 词表中' },
  ],
});
assert.deepEqual(applied.candidates.map(c => c.id), [3, 4, 5], 'META-11 ok and superseded rows leave the list, failures stay for retry');
assert.deepEqual(applied.summary.supersededReasons, [{ message: '同标签的其他候选已批准', count: 1 }]);
const summaryText = batchApprovalSummaryText(applied.summary);
assert.match(summaryText, /成功 1 条/);
assert.match(summaryText, /1 条已失效（同标签的其他候选已批准 1 条）/, 'META-11 superseded reasons are spelled out');
assert.match(summaryText, /1 条失败（候选标签已不在 AI 词表中 1 条）/);
// 结果里有未加载的 ID（按标签整批批准时常见）也不影响已加载列表。
assert.deepEqual(applyBatchApprovalResult(batchPool, { results: [{ id: 99, ok: true, item: { id: 99, video_id: 7, matched_tag_id: 22 } }] }).candidates.map(c => c.id), [1, 2, 3, 4]);

console.log('ai-tag-review batch helpers passed');
