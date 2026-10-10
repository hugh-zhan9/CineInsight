import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  COLLAPSE_VERSIONS_STORAGE_KEY, isVersionGroupConflict, isVersionGroupNotFound, loadCollapseVersions, moveVersionMember, parseVersionMemberConflict,
  primaryFirstOrder, saveCollapseVersions, versionGroupErrorText, versionGroupSummaryText, versionGroupTitle, versionMemberFacts
} from './versionGroups.js';

function stubStorage(store) {
  Object.defineProperty(window, 'localStorage', { configurable: true, value: store });
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('合并版本开关的本机记忆', () => {
  it('默认开，存过 0 才关，存储不可用时照常默认开', () => {
    stubStorage({ getItem: vi.fn(() => null), setItem: vi.fn() });
    expect(loadCollapseVersions()).toBe(true);
    stubStorage({ getItem: vi.fn(() => '0'), setItem: vi.fn() });
    expect(loadCollapseVersions()).toBe(false);
    stubStorage({ getItem: vi.fn(() => { throw new Error('denied'); }), setItem: vi.fn(() => { throw new Error('denied'); }) });
    expect(loadCollapseVersions()).toBe(true);
    expect(() => saveCollapseVersions(false)).not.toThrow();
  });

  it('保存为 1 / 0', () => {
    const setItem = vi.fn();
    stubStorage({ getItem: vi.fn(), setItem });
    saveCollapseVersions(false);
    saveCollapseVersions(true);
    expect(setItem.mock.calls).toEqual([[COLLAPSE_VERSIONS_STORAGE_KEY, '0'], [COLLAPSE_VERSIONS_STORAGE_KEY, '1']]);
  });
});

describe('错误码解析', () => {
  it('从成员冲突文案里取出视频与组', () => {
    const err = 'version_member_conflict: 2 个视频已在版本组中 {"video_ids":[3,5],"group_ids":[9]}';
    expect(parseVersionMemberConflict(err)).toEqual({ videoIDs: [3, 5], groupIDs: [9] });
    expect(parseVersionMemberConflict(new Error(err))).toEqual({ videoIDs: [3, 5], groupIDs: [9] });
    expect(parseVersionMemberConflict('version_member_conflict: 坏的 {')).toEqual({ videoIDs: [], groupIDs: [] });
    expect(parseVersionMemberConflict('别的错误')).toBeNull();
    expect(versionGroupErrorText(err)).toBe('有 2 个视频已在其他版本组中');
  });

  it('revision 冲突与其他错误码去掉前缀', () => {
    const conflict = 'version_group_conflict: 版本组已被修改或解散，请刷新后重试';
    expect(isVersionGroupConflict(conflict)).toBe(true);
    expect(isVersionGroupNotFound(conflict)).toBe(false);
    expect(isVersionGroupNotFound(new Error('version_group_not_found: 版本组不存在或已解散'))).toBe(true);
    expect(versionGroupErrorText(conflict)).toBe('版本组已被修改或解散，请刷新后重试');
    expect(versionGroupErrorText('version_group_invalid: 版本组需要 2–20 个视频')).toBe('版本组需要 2–20 个视频');
    expect(versionGroupErrorText('数据库正忙')).toBe('数据库正忙');
  });
});

describe('汇总文案', () => {
  it('没有评分不报 0，单一评分不写区间', () => {
    expect(versionGroupSummaryText({ watched_count: 1, member_count: 3, min_rating: null, max_rating: null })).toBe('已看 1/3');
    expect(versionGroupSummaryText({ watched_count: 0, member_count: 2, min_rating: 8, max_rating: 8 })).toBe('已看 0/2 · 评分 8');
    expect(versionGroupSummaryText({ watched_count: 2, member_count: 3, min_rating: 7, max_rating: 8.5 })).toBe('已看 2/3 · 评分 7–8.5');
  });

  it('组标题为空时用主版本标题', () => {
    const members = [{ video_id: 1, display_title: '', name: 'a.mp4' }, { video_id: 2, display_title: '乙', name: 'b.mp4' }];
    expect(versionGroupTitle({ title: '', members })).toBe('a.mp4');
    expect(versionGroupTitle({ title: '  正片 ', members })).toBe('正片');
  });

  it('版本事实缺哪项省哪项；已看只标已看', () => {
    expect(versionMemberFacts({ resolution: '1920x1080', size: 2048, duration: 3661, is_watched: false, watch_position_seconds: 60, personal_rating: 7.5 }))
      .toBe('1920x1080 · 2.0 KB · 01:01:01 · 看到 01:00 · 评分 7.5');
    expect(versionMemberFacts({ width: 640, height: 360, size: 0, duration: 0, is_watched: true, watch_position_seconds: 30, personal_rating: null }))
      .toBe('640x360 · 已看');
  });
});

describe('排列', () => {
  it('设为主版本把目标挪到第一位，其余保持顺序', () => {
    const members = [{ video_id: 1 }, { video_id: 2 }, { video_id: 3 }];
    expect(primaryFirstOrder(members, 3)).toEqual([3, 1, 2]);
    expect(primaryFirstOrder(members, 9)).toEqual([1, 2, 3]);
  });

  it('上移下移，越界不动', () => {
    expect(moveVersionMember([1, 2, 3], 1, -1)).toEqual([2, 1, 3]);
    expect(moveVersionMember([1, 2, 3], 2, 1)).toEqual([1, 2, 3]);
    expect(moveVersionMember([1, 2, 3], 0, -1)).toEqual([1, 2, 3]);
  });
});
