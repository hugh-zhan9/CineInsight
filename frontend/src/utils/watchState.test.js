import { describe, expect, it } from 'vitest';
import { isWatchCompleted, resumable, resumePosition, watchCompletionTail } from './watchState.js';

// 与 services/p020_watch_state_test.go 的 TestWatchCompletionSamplesPLAY11 逐条对应（同名、同值）。
// 任何一边改了样例或公式，另一边必须一起改。
const COMPLETION_SAMPLES = [
  { name: '两小时片停在 1:57:30', position: 7050, duration: 7200, tail: 180, want: true },
  { name: '两小时片片尾区间边界', position: 7020, duration: 7200, tail: 180, want: true },
  { name: '两小时片差 181 秒', position: 7019, duration: 7200, tail: 180, want: false },
  { name: '一小时片区间正好 180 秒', position: 3420, duration: 3600, tail: 180, want: true },
  { name: '二十八秒片 5%', position: 26.6, duration: 28, tail: 1.4, want: true },
  { name: '二十八秒片出区间', position: 26.5, duration: 28, tail: 1.4, want: false },
  { name: '亚秒片位置 0 不算', position: 0, duration: 0.8, tail: 0.04, want: false },
  { name: '时长未知不算', position: 9999, duration: 0, tail: 0, want: false },
  { name: '位置为 0 不算', position: 0, duration: 7200, tail: 180, want: false }
];

describe('PLAY-11 看完判定（对齐 Go TestWatchCompletionSamplesPLAY11）', () => {
  for (const sample of COMPLETION_SAMPLES) {
    it(`PLAY-11 ${sample.name}`, () => {
      if (sample.duration > 0) {
        expect(watchCompletionTail(sample.duration)).toBeCloseTo(sample.tail, 9);
      }
      expect(isWatchCompleted(sample.position, sample.duration)).toBe(sample.want);
    });
  }
});

// 与 TestResumableMatchesResumableSQLPLAY10 逐条对应：base 为标已看时刻，before / after 各差一小时。
const base = '2026-09-30T12:00:00+08:00';
const before = '2026-09-30T11:00:00+08:00';
const after = '2026-09-30T13:00:00+08:00';
const RESUMABLE_SAMPLES = [
  { name: '没有断点', video: {}, want: false },
  { name: '没看完有断点', video: { watch_position_seconds: 10 }, want: true },
  { name: '已看但不知道何时标的', video: { watch_position_seconds: 10, is_watched: true }, want: true },
  { name: '标已看之前留下的断点', video: { watch_position_seconds: 10, is_watched: true, watched_at: base, watch_progress_updated_at: before }, want: false },
  { name: '标已看之后重看', video: { watch_position_seconds: 10, is_watched: true, watched_at: base, watch_progress_updated_at: after }, want: true },
  { name: '已看且没有进度时间', video: { watch_position_seconds: 10, is_watched: true, watched_at: base }, want: false },
  { name: '已看且断点为 0', video: { is_watched: true, watched_at: base, watch_progress_updated_at: after }, want: false },
  { name: '同一时刻写入不算重看', video: { watch_position_seconds: 10, is_watched: true, watched_at: base, watch_progress_updated_at: base }, want: false }
];

describe('PLAY-10 断点可续判定（对齐 Go TestResumableMatchesResumableSQLPLAY10）', () => {
  for (const sample of RESUMABLE_SAMPLES) {
    it(`PLAY-10 ${sample.name}`, () => {
      expect(resumable({ watch_position_seconds: 0, is_watched: false, ...sample.video })).toBe(sample.want);
    });
  }

  it('PLAY-10 空值与无法解析的时间按「没有」处理', () => {
    expect(resumable(null)).toBe(false);
    expect(resumable({ watch_position_seconds: 10, is_watched: true, watched_at: '' })).toBe(true);
    expect(resumable({ watch_position_seconds: 10, is_watched: true, watched_at: base, watch_progress_updated_at: 'not-a-time' })).toBe(false);
  });

  it('PLAY-10 同一毫秒内的先后照样分得出（Go 比到纳秒），与时区写法无关', () => {
    const watchedAt = '2026-09-30T12:00:00.123400+08:00';
    expect(resumable({ watch_position_seconds: 10, is_watched: true, watched_at: watchedAt, watch_progress_updated_at: '2026-09-30T12:00:00.1235+08:00' })).toBe(true);
    expect(resumable({ watch_position_seconds: 10, is_watched: true, watched_at: watchedAt, watch_progress_updated_at: '2026-09-30T12:00:00.1234+08:00' })).toBe(false);
    expect(resumable({ watch_position_seconds: 10, is_watched: true, watched_at: watchedAt, watch_progress_updated_at: '2026-09-30T04:00:00.123401Z' })).toBe(true);
  });
});

describe('续播位置（resumePositionFor 与行进度条共用）', () => {
  it('PLAY-11 落进片尾区间就从头播：两小时片停在 1:57:30 不再续播', () => {
    expect(resumePosition({ duration: 7200, watch_position_seconds: 7050 })).toBe(0);
    expect(resumePosition({ duration: 7200, watch_position_seconds: 7019 })).toBe(7019);
  });

  it('PLAY-10 重看中的已看片按断点续播，标已看之前的旧断点不续播', () => {
    expect(resumePosition({ duration: 7200, watch_position_seconds: 600, is_watched: true, watched_at: base, watch_progress_updated_at: after })).toBe(600);
    expect(resumePosition({ duration: 7200, watch_position_seconds: 600, is_watched: true, watched_at: base, watch_progress_updated_at: before })).toBe(0);
  });

  it('时长未知时有断点就续播', () => {
    expect(resumePosition({ duration: 0, watch_position_seconds: 42 })).toBe(42);
  });
});
