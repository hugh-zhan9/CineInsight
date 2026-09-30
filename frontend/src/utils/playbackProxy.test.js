import { describe, expect, it } from 'vitest';
import {
  PLAYBACK_PROXY_CODE_LABELS,
  playbackProxyCodeOutcome,
  playbackProxyItemState,
  playbackProxyItemStateText
} from './playbackProxy.js';

describe('PLAY-04 代理结果码归类', () => {
  it('PLAY-04 「进行中」映射为「生成中」，不当失败', () => {
    expect(PLAYBACK_PROXY_CODE_LABELS.in_progress).toBe('生成中');
    expect(playbackProxyCodeOutcome('in_progress')).toBe('pending');
  });

  it('PLAY-04 已生成与已有可用代理都算就绪，取消与其余码分开', () => {
    expect(playbackProxyCodeOutcome('created')).toBe('ready');
    expect(playbackProxyCodeOutcome('already_exists')).toBe('ready');
    expect(playbackProxyCodeOutcome('cancelled')).toBe('cancelled');
    expect(playbackProxyCodeOutcome('disk_full')).toBe('failed');
    expect(playbackProxyCodeOutcome('')).toBe('');
  });
});

describe('PLAY-04 从代理状态快照读出某一项的排位与进度', () => {
  const status = {
    running: true,
    queued: 3,
    queued_video_ids: [5, 7, 9],
    current_video_id: 3,
    results: [
      { video_id: 1, code: 'created' },
      { video_id: 7, code: 'in_progress' }
    ]
  };

  it('PLAY-04 正在处理的一项显示「生成中」', () => {
    const state = playbackProxyItemState(status, 3);
    expect(state.phase).toBe('processing');
    expect(playbackProxyItemStateText(state)).toBe('生成中');
  });

  it('PLAY-04 排队的一项显示「排队中（第 N 个）」，in_progress 结果不算完成', () => {
    const state = playbackProxyItemState(status, 7);
    expect(state).toEqual({ phase: 'queued', position: 2 });
    expect(playbackProxyItemStateText(state)).toBe('排队中（第 2 个）');
  });

  it('PLAY-04 已处理完的一项取最后一条结果', () => {
    const state = playbackProxyItemState({ ...status, results: [{ video_id: 1, code: 'disk_full' }, { video_id: 1, code: 'created' }] }, 1);
    expect(state.phase).toBe('done');
    expect(state.outcome).toBe('ready');
    expect(state.result.code).toBe('created');
  });

  it('PLAY-04 排位只带前若干项：已知入队但不在排位表里时显示「排队中」', () => {
    const long = { running: true, queued: 250, queued_video_ids: [11, 12], current_video_id: 10, results: [] };
    expect(playbackProxyItemState(long, 99, { tracked: true })).toEqual({ phase: 'queued', position: 0 });
    expect(playbackProxyItemStateText({ phase: 'queued', position: 0 })).toBe('排队中');
    expect(playbackProxyItemState(long, 99).phase).toBe('idle');
  });

  it('PLAY-04 快照里没有这一项、或快照为空时是 idle', () => {
    expect(playbackProxyItemState(status, 42).phase).toBe('idle');
    expect(playbackProxyItemState(null, 1).phase).toBe('idle');
    expect(playbackProxyItemState(status, 0).phase).toBe('idle');
    expect(playbackProxyItemStateText({ phase: 'idle' })).toBe('');
  });

  it('PLAY-04 本轮结束后 current_video_id 残留不算正在处理', () => {
    expect(playbackProxyItemState({ running: false, current_video_id: 3, queued_video_ids: [], results: [] }, 3).phase).toBe('idle');
  });
});
