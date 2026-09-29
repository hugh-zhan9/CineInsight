import { describe, expect, it } from 'vitest';
import {
  applySuggestion, cleanupGroup, curationBadges, defaultSelection, describeSelectionKinds, isGroupFullySuggested,
  isLocked, keeperOf, lockedIDs, mergePlan, pruneSelection, selectionSummary, setKeeper, similarityCount,
  suggestedIDs, toggleSuggestion
} from './cleanupSelection.js';

const exact = cleanupGroup({ key: 'exact-1', kind: 'exact', keeperId: 1, memberIds: [1, 2, 3], switchable: true });
const near = cleanupGroup({ key: 'near-4', kind: 'near', keeperId: 4, memberIds: [4, 5], switchable: true });
const sameSource = cleanupGroup({ key: 'same-source-9', kind: 'same-source', keeperId: 6, memberIds: [6, 7] });
const clip = cleanupGroup({ key: 'clip-8-10', kind: 'clip', keeperId: 8, memberIds: [8, 10] });
const short = cleanupGroup({ key: 'dur-11', kind: 'low-duration', keeperId: null, memberIds: [11] });
const groups = [exact, near, sameSource, clip, short];

describe('utils/cleanupSelection（D-PC49 统一勾选规则）', () => {
  it('IMG-04 默认只勾精确重复的非保留项；近似、同源、截取片段、极短都不勾', () => {
    expect(defaultSelection(groups)).toEqual([2, 3]);
    expect(defaultSelection([near, sameSource, clip, short])).toEqual([]);
  });

  it('IMG-05 保留项锁定，而且跨组生效：别的组要保留的成员在本组也不建议勾', () => {
    const shared = cleanupGroup({ key: 'near-2', kind: 'near', keeperId: 2, memberIds: [2, 12], switchable: true });
    const all = [exact, shared];
    const locked = lockedIDs(all);
    expect([...locked].sort()).toEqual([1, 2]);
    expect(isLocked(2, locked)).toBe(true);
    expect(suggestedIDs(exact, { locked })).toEqual([3]);
    expect(defaultSelection(all)).toEqual([3]);
  });

  it('IMG-05「按建议勾选本组」只作用于传入的组；再点一次只放掉本组成员', () => {
    const options = { locked: lockedIDs(groups) };
    const selection = applySuggestion([11], near, options);
    expect(selection).toEqual([11, 5]);
    expect(isGroupFullySuggested(selection, near, options)).toBe(true);
    expect(toggleSuggestion(selection, near, options)).toEqual([11]);
    // 已删除（excluded）的成员不再被建议。
    expect(suggestedIDs(exact, { ...options, excluded: [2] })).toEqual([3]);
  });

  it('IMG-05 换保留项：全勾过的组按新保留项重新全勾，没全勾的只解除原保留项的锁定', () => {
    const full = setKeeper({ groups, selection: [2, 3] }, exact, 2);
    expect(full.overrides).toEqual({ 'exact-1': 2 });
    expect(full.selection).toEqual([1, 3]);
    expect(keeperOf(exact, full.overrides)).toBe(2);

    const partial = setKeeper({ groups, selection: [] }, near, 5);
    expect(partial.selection).toEqual([]);
    expect(isLocked(4, lockedIDs(groups, partial.overrides))).toBe(false);
    expect(isLocked(5, lockedIDs(groups, partial.overrides))).toBe(true);

    // 同源与截取片段的保留项固定，换不了。
    const fixed = setKeeper({ groups, selection: [7] }, sameSource, 7);
    expect(fixed.overrides).toEqual({});
    expect(fixed.selection).toEqual([7]);
  });

  it('IMG-05 filter 能表达「只勾与保留项同目录」，并拿到当前的保留项覆盖', () => {
    const directory = { 1: '/a', 2: '/a', 3: '/b' };
    const filter = (id, group, overrides) => directory[id] === directory[keeperOf(group, overrides)];
    expect(defaultSelection([exact], { filter })).toEqual([2]);
    const moved = setKeeper({ groups: [exact], selection: [2, 3], filter }, exact, 3);
    expect(moved.selection).toEqual([]);
  });

  it('结果或保留项变化后收窄勾选：去掉锁定、已删除、不再出现的 ID', () => {
    expect(pruneSelection([1, 2, 5, 99], groups, { excluded: [5] })).toEqual([2]);
  });

  it('IMG-03 合并计划：每组被勾的成员合并到该组当前保留项，同一保留项合成一次；没有保留项的类别不参与', () => {
    const extra = cleanupGroup({ key: 'near-1', kind: 'near', keeperId: 1, memberIds: [1, 13], switchable: true });
    const plan = mergePlan([exact, extra, near, short], [2, 13, 5, 11]);
    expect(plan).toEqual([
      { keeperId: 1, sourceIds: [2, 13], kinds: ['exact', 'near'] },
      { keeperId: 4, sourceIds: [5], kinds: ['near'] }
    ]);
    expect(mergePlan([short], [11])).toEqual([]);
    // 换过保留项之后按新保留项合并。
    expect(mergePlan([exact], [1], { 'exact-1': 3 })).toEqual([{ keeperId: 3, sourceIds: [1], kinds: ['exact'] }]);
  });

  it('IMG-05 删除汇总：条数、体积与各类别条数，按相似度判断的单独计数', () => {
    const summary = selectionSummary(groups, [2, 5, 7, 10, 11], id => id * 100);
    expect(summary).toEqual({ count: 5, bytes: 3500, byKind: { exact: 1, near: 1, 'same-source': 1, clip: 1, 'low-duration': 1 } });
    expect(describeSelectionKinds(summary, '个')).toBe('精确重复 1 个、近似重复 1 个、同源视频 1 个、截取片段 1 个、极短片段 1 个');
    expect(similarityCount(summary)).toBe(3);
    expect(selectionSummary(groups, [])).toEqual({ count: 0, bytes: 0, byKind: {} });
  });

  it('IMG-03 整理成果标记只列命中的项', () => {
    expect(curationBadges({ favorite: true, liked: false, subtitle: true, progress: true }).map(item => item.text))
      .toEqual(['★ 收藏', '字幕', '进度']);
    expect(curationBadges(null)).toEqual([]);
  });
});
