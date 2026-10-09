import { describe, it, expect } from 'vitest';
import { cleanupGroup } from './cleanupSelection.js';
import { consolidationRequest, consolidationReviewSignature } from './cleanupConsolidation.js';
const group = (key, kind, keeperId, memberIds) => cleanupGroup({ key, kind, keeperId, memberIds, switchable: kind === 'exact' || kind === 'near' });
describe('集中整理范围与保护', () => {
  it('A/B C/D保留A/D，移动范围独立删除勾选，低清只作为保护；保留覆盖显式固定', () => {
    const groups = [group('a', 'near', 1, [1, 2]), group('b', 'near', 3, [3, 4]), group('low', 'low-duration', null, [5])];
    const req = consolidationRequest(groups, { scope: ['a', 'b', 'low'], overrides: { b: 4 }, selection: [2] });
    expect(req.groups.map(g => g.keeper_id)).toEqual([1, 4]); expect(req.groups[1].selected_ids).toEqual([]); expect(req.groups[1].keeper_pinned).toBe(true);
    expect(req.protections).toHaveLength(3); expect(req.protections[2].keeper_id).toBe(0);
  });
  it('不自动授权任何组；本组不删跨组保护并从整理范围移除，保留同keeper的两个关系', () => {
    const groups = [group('a', 'exact', 1, [1, 2]), group('b', 'near', 1, [1, 3]), group('c', 'clip', 4, [4, 2])];
    expect(consolidationRequest(groups).groups).toEqual([]);
    const req = consolidationRequest(groups, { scope: ['a', 'b', 'c'], skipped: ['c'], selection: [2, 3] });
    expect(req.groups).toHaveLength(2); expect(req.groups[0].selected_ids).toEqual([]); expect(req.groups[1].selected_ids).toEqual([3]);
    expect(req.protections[2].skipped).toBe(true);
  });
  it('回读签名检测保护/成员/路径变化，不受返回顺序影响', () => {
    const review = { task_id: 1, groups: [{ kind: 'near', member_ids: [1, 2], keeper_id: 1 }], locked_ids: [3, 4], analysis: { near_duplicate_groups: [{ original: { id: 1, path: '/a' }, candidates: [{ id: 2, path: '/b' }] }] } };
    expect(consolidationReviewSignature(review)).toBe(consolidationReviewSignature({ ...review, locked_ids: [4, 3] }));
    expect(consolidationReviewSignature(review)).not.toBe(consolidationReviewSignature({ ...review, locked_ids: [2, 3, 4] }));
    expect(consolidationReviewSignature(review)).not.toBe(consolidationReviewSignature({ ...review, analysis: { near_duplicate_groups: [{ original: { id: 1, path: '/changed' } }] } }));
  });
});
