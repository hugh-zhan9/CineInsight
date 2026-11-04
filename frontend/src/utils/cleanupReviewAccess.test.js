import { afterEach, describe, expect, it } from 'vitest';
import { photoCleanupStore, resetPhotoCleanupReview } from './photoCleanupStore.js';
import { cleanupReviewSnapshot, readCleanupReview } from './cleanupReviewAccess.js';
import { cleanupGroup } from './cleanupSelection.js';
import { formatGoalBytes } from './storageGoal.js';
afterEach(() => { photoCleanupStore.status = null; resetPhotoCleanupReview(); });

describe('space plan review projection', () => {
  it('respects keeper overrides and group skip protections across groups', () => {
    const groups = [cleanupGroup({ key: 'a', kind: 'exact', keeperId: 1, memberIds: [1, 2], switchable: true }), cleanupGroup({ key: 'b', kind: 'near', keeperId: 3, memberIds: [2, 3], switchable: true })];
    const current = cleanupReviewSnapshot('analysis', groups, { a: 2 }, []);
    expect(current.locked_ids).toEqual([2, 3]); expect(current.candidates).toEqual([{ id: 1, kind: 'exact', keeper_id: 2 }]);
    const skipped = cleanupReviewSnapshot('analysis', groups, {}, ['a']);
    expect(skipped.candidates).toEqual([]); expect(skipped.locked_ids).toEqual([1, 2, 3]);
  });
  it('projects the persistent photo review without requiring a mounted cleanup page', () => {
    photoCleanupStore.status = { completed: true, started_at: 'batch', analysis: { duplicate_groups: [{ original: { id: 1, directory: '/a' }, candidates: [{ id: 2, directory: '/b' }, { id: 3, directory: '/a' }] }] } };
    resetPhotoCleanupReview('batch'); photoCleanupStore.review.sameDirOnly = true;
    expect(readCleanupReview('image').candidates.map(item => item.id)).toEqual([3]);
    photoCleanupStore.review.keepOverrides = { 'exact-1': 2 };
    expect(readCleanupReview('image').candidates).toEqual([]);
    photoCleanupStore.review.sameDirOnly = false;
    expect(readCleanupReview('image').candidates.map(item => item.id)).toEqual([1, 3]);
    photoCleanupStore.status = { ...photoCleanupStore.status, stale: true };
    expect(readCleanupReview('image')).toBeNull();
  });
  it('uses decimal GB consistently with the target input', () => {
    expect(formatGoalBytes(100 * 1e9)).toBe('100.00 GB'); expect(formatGoalBytes(0)).toBe('0 B');
  });
});
