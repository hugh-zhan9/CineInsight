import { describe, expect, it } from 'vitest';
import {
  applySuggestion, cleanupGroup, curationBadges, defaultSelection, describeMergeFailure, describeSelectionKinds,
  isGroupFullySuggested, isLocked, keeperOf, LockedSelectionError, lockedIDs, mergeOptionsFor, mergePlan,
  pruneSelection, selectionSummary, setKeeper, similarityCount, suggestedIDs, toggleSuggestion
} from './cleanupSelection.js';

const FULL_MERGE = { skip_playback_state: false, skip_subtitle: false };
const CLIP_MERGE = { skip_playback_state: true, skip_subtitle: true };

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

  // §9.2 主代理裁决（P-032 评审 Minor 2）：原保留项只解除锁定，不自动勾上。
  it('D-PC49 换保留项：全勾过的组按新保留项重算其余成员，原保留项解除锁定但不自动勾上；没全勾的只解除锁定', () => {
    const full = setKeeper({ groups, selection: [2, 3] }, exact, 2);
    expect(full.overrides).toEqual({ 'exact-1': 2 });
    expect(full.selection).toEqual([3]);
    expect(keeperOf(exact, full.overrides)).toBe(2);
    expect(isLocked(1, lockedIDs(groups, full.overrides))).toBe(false);
    expect(suggestedIDs(exact, { overrides: full.overrides, locked: lockedIDs(groups, full.overrides) })).toEqual([1, 3]);

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

  // P-032 评审 Minor 7：「本组不删」的成员跨组锁定，别的组也不能勾它删。
  it('D-PC49「本组不删」的组：成员跨组锁定、不被建议、删除前被裁掉', () => {
    const other = cleanupGroup({ key: 'near-2', kind: 'near', keeperId: 12, memberIds: [12, 2], switchable: true });
    const all = [exact, other];
    const skipped = ['exact-1'];
    const locked = lockedIDs(all, {}, skipped);
    expect([...locked].sort((a, b) => a - b)).toEqual([1, 2, 3, 12]);
    expect(suggestedIDs(other, { skipped, locked })).toEqual([]);
    expect(defaultSelection(all, { skipped })).toEqual([]);
    expect(pruneSelection([2, 3], all, { skipped })).toEqual([]);
  });

  it('IMG-03 合并计划：每组被勾的成员按组合并到该组当前保留项；同一保留项的几组也分开；没有保留项的类别不参与', () => {
    const extra = cleanupGroup({ key: 'near-1', kind: 'near', keeperId: 1, memberIds: [1, 13], switchable: true });
    const plan = mergePlan([exact, extra, near, short], [2, 13, 5, 11]);
    expect(plan).toEqual([
      { key: 'exact-1', kind: 'exact', keeperId: 1, sourceIds: [2], options: FULL_MERGE },
      { key: 'near-1', kind: 'near', keeperId: 1, sourceIds: [13], options: FULL_MERGE },
      { key: 'near-4', kind: 'near', keeperId: 4, sourceIds: [5], options: FULL_MERGE }
    ]);
    expect(mergePlan([short], [11])).toEqual([]);
    // 换过保留项之后按新保留项合并。
    expect(mergePlan([exact], [1], { overrides: { 'exact-1': 3 } }))
      .toEqual([{ key: 'exact-1', kind: 'exact', keeperId: 3, sourceIds: [1], options: FULL_MERGE }]);
  });

  // §9.1 主代理裁决（P-032 评审 I-2）：截取片段组不合并观看状态与字幕，不能和别的组混在一次调用里。
  it('D-PC48 截取片段组两项都跳过；与同一保留项的精确重复组分开调用', () => {
    expect(mergeOptionsFor('clip')).toEqual(CLIP_MERGE);
    for (const kind of ['exact', 'near', 'same-source']) expect(mergeOptionsFor(kind)).toEqual(FULL_MERGE);
    const sharedKeeperExact = cleanupGroup({ key: 'exact-8', kind: 'exact', keeperId: 8, memberIds: [8, 14], switchable: true });
    expect(mergePlan([sharedKeeperExact, clip], [14, 10])).toEqual([
      { key: 'exact-8', kind: 'exact', keeperId: 8, sourceIds: [14], options: FULL_MERGE },
      { key: 'clip-8-10', kind: 'clip', keeperId: 8, sourceIds: [10], options: CLIP_MERGE }
    ]);
  });

  // P-032 评审 I-1：保留项还在勾选里时报错，不再静默跳过这一组（那样会把保留项删掉）。
  it('IMG-03 合并计划遇到保留项仍在勾选里时报错', () => {
    expect(() => mergePlan([exact, near], [1, 2])).toThrow(LockedSelectionError);
    try {
      mergePlan([exact, near], [2, 4, 5]);
      throw new Error('应当报错');
    } catch (err) {
      expect(err).toBeInstanceOf(LockedSelectionError);
      expect(err.ids).toEqual([4]);
      expect(err.message).toContain('已中止');
    }
    // 换过保留项之后，原保留项不再锁定、可以合并；新保留项在勾选里则报错。
    expect(mergePlan([exact], [1], { overrides: { 'exact-1': 2 } })[0].sourceIds).toEqual([1]);
    expect(() => mergePlan([exact], [2], { overrides: { 'exact-1': 2 } })).toThrow(LockedSelectionError);
    // 「本组不删」的成员同样锁定；被跳过的组不进合并计划。
    const other = cleanupGroup({ key: 'near-2', kind: 'near', keeperId: 12, memberIds: [12, 2], switchable: true });
    expect(() => mergePlan([exact, other], [2], { skipped: ['exact-1'] })).toThrow(LockedSelectionError);
    expect(mergePlan([exact, other], [3], { skipped: ['near-2'] })).toEqual([
      { key: 'exact-1', kind: 'exact', keeperId: 1, sourceIds: [3], options: FULL_MERGE }
    ]);
  });

  it('IMG-03 多组合并中途失败的提示说明前面几组已合并、没有删除任何媒体', () => {
    expect(describeMergeFailure('数据库繁忙', 0, 1, '视频')).toBe('合并元数据失败，没有删除任何视频：数据库繁忙。合并可以重复执行，处理好之后再删除即可。');
    const partial = describeMergeFailure('数据库繁忙', 2, 3, '图片');
    expect(partial).toContain('没有删除任何图片');
    expect(partial).toContain('共 3 组，前 2 组已经合并到各自的保留项');
  });

  it('IMG-05 删除汇总：条数、体积与各类别条数，按相似度判断的单独计数', () => {
    const summary = selectionSummary(groups, [2, 5, 7, 10, 11], id => id * 100);
    expect(summary).toEqual({ count: 5, bytes: 3500, byKind: { exact: 1, near: 1, 'same-source': 1, clip: 1, 'low-duration': 1 }, similar: 3 });
    expect(describeSelectionKinds(summary, '个')).toBe('精确重复 1 个、近似重复 1 个、同源视频 1 个、截取片段 1 个、极短片段 1 个');
    expect(similarityCount(summary)).toBe(3);
    expect(selectionSummary(groups, [])).toEqual({ count: 0, bytes: 0, byKind: {}, similar: 0 });
  });

  // P-032 评审 Minor 1：同一条同时落在近似与同源两类里，只算一次。
  it('IMG-05 相似度计数按去重后的 ID 计，不按类别求和', () => {
    const nearAndSource = cleanupGroup({ key: 'near-6', kind: 'near', keeperId: 6, memberIds: [6, 7], switchable: true });
    const summary = selectionSummary([sameSource, nearAndSource, exact], [7, 2]);
    expect(summary.byKind).toEqual({ 'same-source': 1, near: 1, exact: 1 });
    expect(similarityCount(summary)).toBe(1);
  });

  it('IMG-03 整理成果标记只列命中的项', () => {
    expect(curationBadges({ favorite: true, liked: false, subtitle: true, progress: true }).map(item => item.text))
      .toEqual(['★ 收藏', '字幕', '进度']);
    expect(curationBadges(null)).toEqual([]);
  });
});
