import { describe, expect, it } from 'vitest';
import { cleanupAnalysisState, reclaimSummary } from './storageReclaim.js';

const video = (id, size) => ({ id, name: `v${id}.mp4`, size });
const image = (id, size, fileSize) => ({ id, name: `i${id}.jpg`, size, file_size: fileSize });
const ready = (analysis, extra = {}) => ({ running: false, completed: true, stale: false, started_at: '2026-10-12T00:00:00Z', analysis, ...extra });

describe('存储体检的可回收空间', () => {
  it('分清还没分析、分析中、已取消、结果过期与已分析', () => {
    expect(cleanupAnalysisState(null)).toBe('missing');
    expect(cleanupAnalysisState({ running: true })).toBe('running');
    expect(cleanupAnalysisState({ cancelled: true })).toBe('cancelled');
    expect(cleanupAnalysisState(ready({}, { stale: true }))).toBe('stale');
    expect(cleanupAnalysisState(ready({}))).toBe('ready');
  });

  it('每组保留推荐项、其余算可回收；单条候选整条算；精确重复是「确定」，其余「待确认」', () => {
    const summary = reclaimSummary(ready({
      duplicate_groups: [{ original: video(1, 100), candidates: [video(2, 100), video(3, 100)] }],
      near_duplicate_groups: [{ original: video(4, 500), candidates: [video(5, 200)] }],
      same_source_groups: [{ preferred: video(6, 900), alternative: video(7, 300) }],
      clip_groups: [{ full: video(8, 1000), clip: video(9, 50) }],
      low_duration: [video(10, 5)],
      low_resolution: [video(11, 7)]
    }), null);
    expect(summary.video.rows.map(row => [row.label, row.certain, row.count, row.bytes])).toEqual([
      ['精确重复', true, 2, 200],
      ['近似重复', false, 1, 200],
      ['同源视频', false, 1, 300],
      ['截取片段', false, 1, 50],
      ['极短片段', false, 1, 5],
      ['极低分辨率', false, 1, 7]
    ]);
    expect(summary.totalBytes).toBe(762);
    expect(summary.certainBytes).toBe(200);
    expect(summary.reviewBytes).toBe(562);
    expect(summary.image.state).toBe('missing');
    expect(summary.image.rows).toEqual([]);
    expect(summary.analyzed).toBe(true);
  });

  it('同一个文件落在几类里：各类各算一次，总数只算一次，算进「确定」就不再算「待确认」', () => {
    const summary = reclaimSummary(ready({
      duplicate_groups: [{ original: video(1, 100), candidates: [video(2, 100)] }],
      near_duplicate_groups: [{ original: video(3, 100), candidates: [video(2, 100), video(4, 40)] }],
      low_duration: [video(4, 40)]
    }), null);
    expect(summary.video.rows.map(row => row.bytes)).toEqual([100, 140, 40]);
    expect(summary.totalCount).toBe(2);
    expect(summary.totalBytes).toBe(140);
    expect(summary.certainBytes).toBe(100);
    expect(summary.reviewBytes).toBe(40);
  });

  it('图片按分析时实测的 file_size 算，近似重复叫「疑似重复」；结果过期也照算', () => {
    const summary = reclaimSummary(null, ready({
      duplicate_groups: [{ original: image(1, 10, 11), candidates: [image(2, 10, 12)] }],
      near_duplicate_groups: [{ original: image(3, 10), candidates: [image(4, 30)] }]
    }, { stale: true }));
    expect(summary.image.state).toBe('stale');
    expect(summary.image.rows.map(row => [row.label, row.bytes])).toEqual([['精确重复', 12], ['疑似重复', 30]]);
    expect(summary.video.state).toBe('missing');
    expect(summary.totalBytes).toBe(42);
  });

  it('都没分析过时没有数字可报', () => {
    const summary = reclaimSummary({ running: true }, { cancelled: true });
    expect(summary.analyzed).toBe(false);
    expect(summary.totalBytes).toBe(0);
    expect(summary.video.rows).toEqual([]);
    expect(summary.image.rows).toEqual([]);
  });

  it('刚移到废纸篓、清理分析还没重跑的视频不再算可回收', () => {
    const status = ready({
      duplicate_groups: [{ original: video(1, 100), candidates: [video(2, 100), video(3, 50)] }],
      low_duration: [video(2, 100)]
    });
    const summary = reclaimSummary(status, null, { excludedVideoIDs: [2] });
    expect(summary.video.rows.map(row => [row.label, row.count, row.bytes])).toEqual([['精确重复', 1, 50]]);
    expect(summary.totalBytes).toBe(50);
  });

  it('删掉的是某组保留的那份：这一组整组先不算，等重新分析', () => {
    const status = ready({
      duplicate_groups: [
        { original: video(1, 100), candidates: [video(2, 100)] },
        { original: video(3, 70), candidates: [video(4, 70)] }
      ],
      clip_groups: [{ full: video(1, 100), clip: video(5, 20) }]
    });
    const summary = reclaimSummary(status, null, { excludedVideoIDs: [1] });
    expect(summary.video.rows.map(row => [row.label, row.count, row.bytes])).toEqual([['精确重复', 1, 70]]);
    expect(summary.certainBytes).toBe(70);
  });
});
