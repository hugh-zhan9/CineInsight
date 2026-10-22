import { describe, expect, it } from 'vitest';
import {
  canShiftBack, canShiftForward, changeText, heatLevel, heroFrameURLs, monthCalendarCells, percentText, recapRequest,
  requestKey, shiftCursor, videoArtwork, yearCalendarWeeks
} from './recap.js';

import { sourceLabel } from './recap.js';

describe('recap utils', () => {
  it('shifts months across year boundaries and years by one', () => {
    expect(shiftCursor('month', { year: 2026, month: 1 }, -1)).toEqual({ year: 2025, month: 12 });
    expect(shiftCursor('month', { year: 2025, month: 12 }, 1)).toEqual({ year: 2026, month: 1 });
    expect(shiftCursor('year', { year: 2026, month: 3 }, -1)).toEqual({ year: 2025, month: 3 });
    expect(shiftCursor('live', { year: 2026, month: 3 }, 1)).toEqual({ year: 2026, month: 3 });
  });

  it('never moves forward past the current period', () => {
    const now = new Date(2026, 9, 11);
    expect(canShiftForward('month', { year: 2026, month: 10 }, now)).toBe(false);
    expect(canShiftForward('month', { year: 2026, month: 9 }, now)).toBe(true);
    expect(canShiftForward('year', { year: 2026, month: 1 }, now)).toBe(false);
    expect(canShiftForward('year', { year: 2025, month: 1 }, now)).toBe(true);
    expect(canShiftBack('month', { year: 1, month: 1 })).toBe(false);
  });

  it('builds requests and keys per tab', () => {
    expect(recapRequest('live', { year: 2026, month: 10 })).toEqual({ period: 'live', year: 0, month: 0 });
    expect(recapRequest('year', { year: 2026, month: 10 })).toEqual({ period: 'year', year: 2026, month: 0 });
    expect(requestKey(recapRequest('month', { year: 2026, month: 10 }))).toBe('month:2026:10');
  });

  it('formats changes without inventing a percent when there is no previous data', () => {
    expect(changeText(null)).toBe('');
    expect(changeText(undefined)).toBe('');
    expect(changeText(0.2)).toBe('持平');
    expect(changeText(49.6)).toBe('↑ 50%');
    expect(changeText(-25)).toBe('↓ 25%');
    expect(percentText(0.004)).toBe('<1%');
    expect(percentText(0)).toBe('0%');
  });

  it('samples hero frames at 10–90% of the duration and none without a duration', () => {
    expect(heroFrameURLs({ id: 3, duration: 100 })).toEqual([
      '/preview/frame/3?ms=10000&w=960', '/preview/frame/3?ms=30000&w=960', '/preview/frame/3?ms=50000&w=960',
      '/preview/frame/3?ms=70000&w=960', '/preview/frame/3?ms=90000&w=960'
    ]);
    expect(heroFrameURLs({ id: 3, duration: 0 })).toEqual([]);
  });

  it('lays out a Monday-first calendar and heat levels relative to the busiest day', () => {
    const days = Array.from({ length: 31 }, (_, index) => ({ date: `2026-10-${String(index + 1).padStart(2, '0')}`, plays: 0, views: 0 }));
    const cells = monthCalendarCells(days);
    expect(cells.filter(cell => cell.blank)).toHaveLength(3); // 2026-10-01 是周四
    expect(yearCalendarWeeks(days)[0]).toHaveLength(7);
    expect(heatLevel(0, 8)).toBe(0);
    expect(heatLevel(1, 8)).toBe(1);
    expect(heatLevel(8, 8)).toBe(4);
  });
});

describe('videoArtwork', () => {
  it('builds the poster / fanart route and nothing for a missing id or unknown kind', () => {
    expect(videoArtwork(7, 'poster')).toBe('/preview/video-artwork/7?kind=poster');
    expect(videoArtwork(7, 'fanart')).toBe('/preview/video-artwork/7?kind=fanart');
    expect(videoArtwork(0, 'poster')).toBe('');
    expect(videoArtwork(7, 'banner')).toBe('');
  });
});

it('labels precise IINA viewing distinctly', () => { expect(sourceLabel('video', 'desktop_controlled_view')).toBe('IINA 精确同步观看'); });
