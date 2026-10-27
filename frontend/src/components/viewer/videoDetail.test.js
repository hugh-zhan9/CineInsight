import { describe, expect, it } from 'vitest';
import {
  RELATED_LIMIT, collectionProgress, detailBackdrop, detailFacts, detailTitle, mergeRelatedVideos, playbackStaleChanges, ratingOf, sharedTagReason,
  stateChanges, viewingRecord
} from './videoDetail.js';

const video = (id, extra = {}) => ({ id, name: `v${id}.mp4`, display_title: '', duration: 1000, ...extra });

describe('detailBackdrop', () => {
  it('uses only the fanart when the details say it exists', () => {
    expect(detailBackdrop({ video: video(7), artwork: { has_fanart: true } })).toEqual({
      frames: ['/preview/video-artwork/7?kind=fanart'], fallback: '/preview/thumbnail/7'
    });
  });

  it('falls back to the five hero frames without fanart, and to the thumbnail when the duration is unknown', () => {
    const frames = detailBackdrop({ video: video(7), artwork: { has_fanart: false, has_poster: true } }).frames;
    expect(frames).toHaveLength(5);
    expect(frames[0]).toBe('/preview/frame/7?ms=100000&w=960');
    expect(detailBackdrop({ video: video(7, { duration: 0 }), artwork: {} })).toEqual({ frames: [], fallback: '/preview/thumbnail/7' });
    expect(detailBackdrop(null)).toEqual({ frames: [], fallback: '' });
  });
});

describe('detail facts and title', () => {
  it('lists year, duration, resolution, version count and rating, skipping what is missing', () => {
    const facts = detailFacts(video(1, { year: 1999, duration: 5400, width: 1920, height: 1080, personal_rating: 8.5 }), 3);
    expect(facts.map(fact => fact.text)).toEqual(['1999 年', '01:30:00', '1920×1080', '3 个版本', '评分 8.5']);
    expect(detailFacts(video(1, { duration: 0, resolution: '' }), 1)).toEqual([]);
    expect(detailFacts(video(1, { duration: 0, resolution: '720p' }))[0]).toEqual({ key: 'resolution', text: '720p' });
  });

  it('prefers the effective title and reads ratings as numbers or null', () => {
    expect(detailTitle({ effective_title: '显示名', video: video(1) })).toBe('显示名');
    expect(detailTitle({ video: video(1) })).toBe('v1.mp4');
    expect(ratingOf({ personal_rating: 0 })).toBe(0);
    expect(ratingOf({ personal_rating: null })).toBeNull();
  });
});

describe('collectionProgress', () => {
  const detail = { videos: [{ position: 3, video: video(30) }, { position: 1, video: video(10) }, { position: 2, video: video(20) }] };

  it('finds the position by member order and the next member', () => {
    expect(collectionProgress(detail, 10)).toEqual({ total: 3, position: 1, next: video(20) });
    expect(collectionProgress(detail, 30)).toEqual({ total: 3, position: 3, next: null });
  });

  it('gives no next item when the video is no longer a member', () => {
    expect(collectionProgress(detail, 99)).toEqual({ total: 3, position: 0, next: null });
    expect(collectionProgress(null, 1)).toEqual({ total: 0, position: 0, next: null });
  });
});

describe('mergeRelatedVideos', () => {
  it('merges people in order, drops the current video and duplicates, and keeps the first reason', () => {
    const items = mergeRelatedVideos([
      { name: '甲', videos: [video(1), video(2), video(3)] },
      { name: '乙', videos: [video(3), video(4)] }
    ], 1);
    expect(items.map(item => item.video.id)).toEqual([2, 3, 4]);
    expect(items.map(item => item.reason)).toEqual(['同为 甲 的作品', '同为 甲 的作品', '同为 乙 的作品']);
  });

  it('stops at the limit', () => {
    const many = Array.from({ length: RELATED_LIMIT + 5 }, (_, index) => video(index + 2));
    expect(mergeRelatedVideos([{ name: '甲', videos: many }], 1)).toHaveLength(RELATED_LIMIT);
    expect(mergeRelatedVideos([], 1)).toEqual([]);
  });
});

describe('sharedTagReason', () => {
  it('lists up to three shared tags and summarizes the rest', () => {
    expect(sharedTagReason(['科幻'])).toBe('科幻');
    expect(sharedTagReason(['科幻', '动作', '悬疑'])).toBe('科幻 · 动作 · 悬疑');
    expect(sharedTagReason(['科幻', '动作', '悬疑', '剧情', '犯罪'])).toBe('科幻 · 动作 · 悬疑 等 5 个标签');
    expect(sharedTagReason([])).toBe('');
    expect(sharedTagReason(null)).toBe('');
  });
});

describe('viewingRecord', () => {
  it('is empty for a video that was never played, watched or resumed', () => {
    expect(viewingRecord(video(1, { play_count: 0, is_watched: false, watch_position_seconds: 0 }))).toEqual([]);
    expect(viewingRecord(null)).toEqual([]);
  });

  it('shows plays, last played, watched date and inline progress', () => {
    const tiles = viewingRecord(video(1, {
      play_count: 4, last_played_at: '2026-10-01T20:00:00+08:00', is_watched: true, watched_at: '2026-10-02T21:00:00+08:00',
      watch_position_seconds: 75, duration: 600
    }));
    expect(tiles.map(tile => [tile.key, tile.value])).toEqual([
      ['plays', 4], ['last-played', '2026年10月1日'], ['watched', '2026年10月2日'], ['progress', '01:15 / 10:00']
    ]);
  });
});

describe('state changes for the host', () => {
  it('picks only the fields each action changes, and only those present', () => {
    const video = { id: 1, is_favorite: true, favorited_at: 't1', is_watched: true, watched_at: 't2', watch_position_seconds: 30, watch_progress_updated_at: 't3', personal_rating: 6, tags: null };
    expect(stateChanges('favorite', video)).toEqual({ is_favorite: true, favorited_at: 't1' });
    expect(stateChanges('progress', video)).toEqual({ watch_position_seconds: 30, watch_progress_updated_at: 't3', is_watched: true, watched_at: 't2' });
    expect(stateChanges('watched', { is_watched: false })).toEqual({ is_watched: false });
    expect(stateChanges('rating', null)).toEqual({});
  });

  it('marks the row stale only when playback failed and the backend marked it, with the library reason rules', () => {
    expect(playbackStaleChanges({ dispatch_succeeded: true, reconcile_result: { did_mark_stale: true } })).toBeNull();
    expect(playbackStaleChanges({ dispatch_succeeded: false, reconcile_result: { did_mark_stale: false } })).toBeNull();
    expect(playbackStaleChanges({ dispatch_succeeded: false })).toBeNull();
    expect(playbackStaleChanges({ dispatch_succeeded: false, reconcile_result: { did_mark_stale: true, reason: 'offline_root' } }))
      .toEqual({ is_stale: true, stale_reason: 'offline_root' });
    expect(playbackStaleChanges({ dispatch_succeeded: false, reason: 'error', reconcile_result: { did_mark_stale: true } }))
      .toEqual({ is_stale: true, stale_reason: 'missing_file' });
    expect(playbackStaleChanges({ dispatch_succeeded: false, reconcile_result: { did_mark_stale: true, reason: 'offline_root', updated_video: { stale_reason: 'moved' } } }))
      .toEqual({ is_stale: true, stale_reason: 'moved' });
  });
});
