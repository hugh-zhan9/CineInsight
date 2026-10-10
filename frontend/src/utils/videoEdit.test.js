import { describe, expect, it } from 'vitest';

import {
  addManualSegment, applyUniformTrim, confirmAllTrim, editErrorCode, editErrorText, editFrameURL, fastCutActual,
  formatEditTime, frameBefore, moveMergeSource, parseEditTime, parseFrameRate, preflightFresh, removeMergeSource,
  setSegmentEdge, setSegmentHDStart, setTrackChoice, setTrimRange, shiftSegmentEdge, specOptionsDiffer, stepByFrames,
  trimItemFlag, unacknowledgedKeys, unmatchedLongRanges
} from './videoEdit.js';

const trimRecipe = () => ({ v: 1, trim_intro: { items: [
  { video_id: 1, remove_start_ms: 0, remove_end_ms: 0, origin: 'manual', detect_status: 'undetected', confidence: 0, confirmed: false },
  { video_id: 2, remove_start_ms: 0, remove_end_ms: 90_000, origin: 'detected', detect_status: 'detected', confidence: 0.9, confirmed: false }
], analysis_window_ms: 600_000 }, tracks: { audio: [], subtitle: [] } });

const hdRecipe = () => ({ v: 1, hd_replace: { long_video_id: 1, hd_video_id: 2, segments: [
  { long_start_ms: 10_000, long_end_ms: 20_000, hd_start_ms: 5_000, hd_end_ms: 15_000, audio_source: 'long', origin: 'detected', match_rate: 0.9, status: 'matched', confirmed: true }
] }, tracks: { audio: [], subtitle: [] } });

describe('视频工作台展示口径', () => {
  it('时间格式到毫秒，解析认 h:mm:ss.mmm / m:ss / 秒数', () => {
    expect(formatEditTime(3_723_456)).toBe('1:02:03.456');
    expect(formatEditTime(90_500)).toBe('1:30.500');
    expect(parseEditTime('1:30.5')).toBe(90_500);
    expect(parseEditTime('83.25')).toBe(83_250);
    expect(parseEditTime('1:02:03.456')).toBe(3_723_456);
    expect(parseEditTime('1:75')).toBeNull();
    expect(parseEditTime('abc')).toBeNull();
    expect(parseEditTime('')).toBeNull();
  });

  it('帧率读 ffprobe 分数；未知帧率不编默认值', () => {
    expect(parseFrameRate('24000/1001')).toBeCloseTo(23.976, 3);
    expect(parseFrameRate('0/0')).toBe(0);
    expect(stepByFrames(1000, 1, 25)).toBe(1040);
    expect(stepByFrames(20, -1, 25)).toBe(0);
    expect(stepByFrames(1000, 1, 0)).toBeNull();
    expect(frameBefore(1000, 25)).toBe(960);
  });

  it('错误码取前缀；固定说法优先，其余取后端中文说明', () => {
    expect(editErrorCode('Error: edit_project_conflict: 项目已被修改')).toBe('edit_project_conflict');
    expect(editErrorText(new Error('edit_analysis_busy: x'))).toBe('已有一轮分析在进行，请等它结束或取消。');
    expect(editErrorText('recipe_invalid: 顺序合并需要 2–50 个来源')).toBe('顺序合并需要 2–50 个来源');
    expect(editErrorText('boom')).toBe('boom');
  });

  it('切点帧走共享单帧路由', () => {
    expect(editFrameURL(7, 1234.4)).toBe('/preview/frame/7?ms=1234&w=240');
  });
});

describe('配方变换（不改入参）', () => {
  it('合并来源换序与移出；移出规格来源时回到第一个来源', () => {
    const recipe = { v: 1, merge: { sources: [{ video_id: 1 }, { video_id: 2 }, { video_id: 3 }], spec_source_video_id: 2 } };
    expect(moveMergeSource(recipe, 2, -1).merge.sources.map(s => s.video_id)).toEqual([1, 3, 2]);
    expect(recipe.merge.sources.map(s => s.video_id)).toEqual([1, 2, 3]);
    const removed = removeMergeSource(recipe, 1);
    expect(removed.merge.sources.map(s => s.video_id)).toEqual([1, 3]);
    expect(removed.merge.spec_source_video_id).toBe(0);
    expect(specOptionsDiffer([{ width: 1920, height: 1080, frame_rate: '25/1' }, { width: 1280, height: 720, frame_rate: '25/1' }])).toBe(true);
    expect(specOptionsDiffer([{ width: 1920, height: 1080, frame_rate: '25/1' }, { width: 1920, height: 1080, frame_rate: '25/1' }])).toBe(false);
  });

  it('统一区间写进每项并取消确认；手改记 manual 并清掉识别状态', () => {
    const uniform = applyUniformTrim(trimRecipe(), 1000, 61_000);
    expect(uniform.trim_intro.items.every(item => item.origin === 'uniform' && item.remove_end_ms === 61_000 && !item.confirmed)).toBe(true);
    const manual = setTrimRange(trimRecipe(), 0, 0, 30_000);
    expect(manual.trim_intro.items[0]).toMatchObject({ origin: 'manual', detect_status: '', remove_end_ms: 30_000, confirmed: false });
    expect(trimItemFlag(trimRecipe().trim_intro.items[0])).toContain('未识别');
    expect(trimItemFlag({ detect_status: 'ambiguous' })).toContain('候选');
  });

  it('全部确认跳过区间无效（未识别且未手填）的项', () => {
    const { recipe, skipped } = confirmAllTrim(trimRecipe());
    expect(skipped).toBe(1);
    expect(recipe.trim_intro.items.map(item => item.confirmed)).toEqual([false, true]);
  });

  it('高清段改长版端点时高清同端同步平移，两侧时长始终相等', () => {
    const start = setSegmentEdge(hdRecipe(), 0, 'start', 12_000).hd_replace.segments[0];
    expect(start).toMatchObject({ long_start_ms: 12_000, hd_start_ms: 7_000, origin: 'manual', confirmed: false });
    const end = shiftSegmentEdge(hdRecipe(), 0, 'end', -1000).hd_replace.segments[0];
    expect(end.long_end_ms - end.long_start_ms).toBe(end.hd_end_ms - end.hd_start_ms);
    const moved = setSegmentHDStart(hdRecipe(), 0, 6_000).hd_replace.segments[0];
    expect(moved).toMatchObject({ long_start_ms: 10_000, long_end_ms: 20_000, hd_start_ms: 6_000, hd_end_ms: 16_000 });
    const clamped = shiftSegmentEdge(hdRecipe(), 0, 'start', -60_000).hd_replace.segments[0];
    expect(clamped.hd_start_ms).toBe(0);
    expect(clamped.long_end_ms - clamped.long_start_ms).toBe(clamped.hd_end_ms - clamped.hd_start_ms);
  });

  it('未匹配的长版区间保留长版；手动加段落在第一个空档', () => {
    expect(unmatchedLongRanges(hdRecipe().hd_replace.segments, 30_000)).toEqual([
      { start_ms: 0, end_ms: 10_000 }, { start_ms: 20_000, end_ms: 30_000 }
    ]);
    const added = addManualSegment(hdRecipe(), 30_000).hd_replace.segments;
    expect(added[0]).toMatchObject({ long_start_ms: 0, long_end_ms: 10_000, hd_start_ms: 0, hd_end_ms: 10_000, origin: 'manual' });
  });

  it('轨道选择：同一输出轨同一来源只留一条；整轨不导出替掉该轨其他选择', () => {
    let recipe = { v: 1, merge: { sources: [] }, tracks: { audio: [], subtitle: [] } };
    recipe = setTrackChoice(recipe, 'audio', 1, 5, 'stream', 3);
    recipe = setTrackChoice(recipe, 'audio', 1, 5, 'silence');
    recipe = setTrackChoice(recipe, 'audio', 1, 6, 'stream', 2);
    expect(recipe.tracks.audio).toEqual([
      { output: 1, video_id: 5, choice: 'silence', stream_index: 0 },
      { output: 1, video_id: 6, choice: 'stream', stream_index: 2 }
    ]);
    recipe = setTrackChoice(recipe, 'audio', 1, 6, 'drop');
    expect(recipe.tracks.audio).toEqual([{ output: 1, video_id: 0, choice: 'drop', stream_index: 0 }]);
  });
});

describe('预检口径', () => {
  it('预检只对同一 revision 与模式有效', () => {
    const project = { id: 3, revision: 4, mode: 'precise' };
    expect(preflightFresh({ project_id: 3, revision: 4, mode: 'precise' }, project)).toBe(true);
    expect(preflightFresh({ project_id: 3, revision: 3, mode: 'precise' }, project)).toBe(false);
    expect(preflightFresh({ project_id: 3, revision: 4, mode: 'fast' }, project)).toBe(false);
  });

  it('快速模式切点按 seq + 来源 + 请求时间找到实际关键帧；未确认的警告列出 key', () => {
    const preflight = { fast: { cut_points: [{ seq: 2, segment: 1, video_id: 9, requested_ms: 90_000, actual_ms: 88_960 }] },
      warnings: [{ key: 'scale:1' }, { key: 'fps:1' }] };
    expect(fastCutActual(preflight, 2, 9, 90_000)).toBe(88_960);
    expect(fastCutActual(preflight, 1, 9, 90_000)).toBeNull();
    expect(unacknowledgedKeys(preflight, ['fps:1'])).toEqual(['scale:1']);
  });
});
