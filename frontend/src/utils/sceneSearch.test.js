import { describe, expect, it } from 'vitest';
import {
  createRequestGeneration,
  formatSceneRange,
  formatSceneTime,
  groupSceneHits,
  sceneCoverageParts,
  sceneErrorText,
  sceneFrameURL,
  sceneNoticeText,
  scenePlaybackStartMs,
  sceneVisualCoveragePercent
} from './sceneSearch.js';

describe('场景检索展示口径（D-MW-SCENES）', () => {
  it('时间格式：不足一小时 m:ss，超过一小时 h:mm:ss', () => {
    expect(formatSceneTime(0)).toBe('0:00');
    expect(formatSceneTime(65_400)).toBe('1:05');
    expect(formatSceneTime(7_080_000)).toBe('1:58:00');
    expect(formatSceneRange(7_080_000, 7_083_500)).toBe('1:58:00 – 1:58:03');
  });

  it('点击命中提前 2 秒起播且不小于 0', () => {
    expect(scenePlaybackStartMs(7_080_000)).toBe(7_078_000);
    expect(scenePlaybackStartMs(1500)).toBe(0);
    expect(scenePlaybackStartMs(undefined)).toBe(0);
  });

  it('缩略帧走单帧预览路由，不带路径', () => {
    expect(sceneFrameURL(12, 5000.4)).toBe('/preview/frame/12?ms=5000&w=320');
    expect(sceneFrameURL(3, -10, 160)).toBe('/preview/frame/3?ms=0&w=160');
  });

  it('按视频分组，组顺序取第一条命中出现的位置', () => {
    const groups = groupSceneHits([
      { video_id: 2, title: 'B', start_ms: 1 },
      { video_id: 1, title: 'A', start_ms: 2 },
      { video_id: 2, title: 'B', start_ms: 3 }
    ]);
    expect(groups.map(group => group.videoID)).toEqual([2, 1]);
    expect(groups[0].hits.map(hit => hit.start_ms)).toEqual([1, 3]);
    expect(groupSceneHits(null)).toEqual([]);
  });

  it('覆盖率文案说清未索引的部分，不把它说成没有命中', () => {
    const coverage = { total_videos: 10, subtitle_indexed: 4, subtitle_unindexed: 3, visual_indexed: 5 };
    expect(sceneCoverageParts(coverage).join(' · ')).toBe('范围内 10 部视频 · 字幕索引 4 部 · 另有 3 部只有其他格式或内嵌字幕（未索引） · 画面已索引 5 部');
    expect(sceneVisualCoveragePercent(coverage)).toBe(50);
    expect(sceneVisualCoveragePercent({ total_videos: 0 })).toBe(0);
  });

  it('提示与错误代码有中文说法', () => {
    expect(sceneNoticeText('scene_runtime_unavailable')).toContain('准备模型');
    expect(sceneNoticeText('visual_index_empty')).toContain('画面索引');
    expect(sceneErrorText(new Error('scene_external_not_enabled'))).toContain('不会向外部接口发送');
    expect(sceneErrorText('数据库正在恢复')).toBe('数据库正在恢复');
    expect(sceneErrorText(new Error('scene_index_cancelling'))).toContain('正在取消');
  });

  it('请求代次只认最新的一次', () => {
    const generation = createRequestGeneration();
    const first = generation.next();
    const second = generation.next();
    expect(generation.isCurrent(first)).toBe(false);
    expect(generation.isCurrent(second)).toBe(true);
  });
});
