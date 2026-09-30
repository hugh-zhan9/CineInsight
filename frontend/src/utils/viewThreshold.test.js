import { describe, expect, it } from 'vitest';
import { createPlaybackAccumulator, newViewSessionID, viewThreshold } from './viewThreshold.js';

// 与 services/p023_view_events_random_test.go 的 TestPLAY07ViewThresholdIsMinOfSixtySecondsAndHalfDuration
// 逐条对应（同值）。任何一边改了样例或公式，另一边必须一起改。
const THRESHOLD_SAMPLES = [
  { duration: 0, want: 60 },
  { duration: -5, want: 60 },
  { duration: Number.NaN, want: 60 },
  { duration: Number.POSITIVE_INFINITY, want: 60 },
  { duration: 30, want: 15 },
  { duration: 119, want: 59.5 },
  { duration: 120, want: 60 },
  { duration: 7200, want: 60 }
];

describe('PLAY-07 有效观看阈值（对齐 Go TestPLAY07ViewThresholdIsMinOfSixtySecondsAndHalfDuration）', () => {
  for (const sample of THRESHOLD_SAMPLES) {
    it(`PLAY-07 时长 ${sample.duration} 的阈值为 ${sample.want}`, () => {
      expect(viewThreshold(sample.duration)).toBe(sample.want);
    });
  }

  it('PLAY-07 拿不到时长（undefined / null / 非数字）按未知取 60', () => {
    expect(viewThreshold(undefined)).toBe(60);
    expect(viewThreshold(null)).toBe(60);
    expect(viewThreshold('abc')).toBe(60);
  });
});

describe('PLAY-07 累计播放时长', () => {
  it('PLAY-07 连续播放按媒体时间累计', () => {
    const acc = createPlaybackAccumulator();
    let at = 1000;
    for (let position = 0; position <= 10; position += 0.25) {
      acc.sample(position, at);
      at += 250;
    }
    expect(acc.seconds).toBeCloseTo(10, 6);
  });

  it('PLAY-07 往回拖、循环回片头都不算播放', () => {
    const acc = createPlaybackAccumulator();
    acc.sample(50, 0);
    acc.sample(51, 1000);
    acc.sample(10, 1250);
    acc.sample(11, 2250);
    expect(acc.seconds).toBeCloseTo(2, 6);
  });

  it('PLAY-07 一次往前的大跳只按两次采样之间的墙钟算', () => {
    const acc = createPlaybackAccumulator();
    acc.sample(0, 0);
    acc.sample(1800, 250);
    expect(acc.seconds).toBeCloseTo(0.75, 6);
  });

  it('PLAY-07 暂停后断开一段：停很久再往前拖不会被墙钟放行', () => {
    const acc = createPlaybackAccumulator();
    acc.sample(0, 0);
    acc.sample(5, 5000);
    acc.breakSegment();
    acc.sample(300, 600000);
    acc.sample(301, 601000);
    expect(acc.seconds).toBeCloseTo(6, 6);
  });

  it('PLAY-07 倍速播放按速率放宽每段上限', () => {
    const acc = createPlaybackAccumulator();
    acc.sample(0, 0);
    acc.sample(2, 1000, 2);
    expect(acc.seconds).toBeCloseTo(2, 6);
  });

  it('PLAY-07 非数字的位置被忽略', () => {
    const acc = createPlaybackAccumulator();
    acc.sample(0, 0);
    acc.sample(Number.NaN, 500);
    acc.sample(1, 1000);
    expect(acc.seconds).toBeCloseTo(1, 6);
  });
});

describe('PLAY-07 观看会话标识', () => {
  it('PLAY-07 每次生成 32 位十六进制且互不相同', () => {
    const a = newViewSessionID();
    const b = newViewSessionID();
    expect(a).toMatch(/^[0-9a-f]{32}$/);
    expect(b).toMatch(/^[0-9a-f]{32}$/);
    expect(a).not.toBe(b);
  });
});
