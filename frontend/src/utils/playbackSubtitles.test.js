import { describe, it, expect } from 'vitest';
import { subtitleVTT, normalizeSubtitlePreferences, projectTranscript, activeTranscriptRows, subtitleSeekPosition } from './playbackSubtitles.js';
describe('playback subtitle projection', () => {
  it('escapes text, preserves bilingual lines and clips negative intervals', () => {
    const result = subtitleVTT([{ start_ms: 0, end_ms: 500, text: 'discard' }, { start_ms: 500, end_ms: 2000, text: '<b>A&B</b>\n你好' }], -1);
    expect(result).not.toContain('discard');
    expect(result).toContain('00:00:00.000 --> 00:00:01.000');
    expect(result).toContain('&lt;b&gt;A&amp;B&lt;/b&gt;\n你好');
  });
  it('positive offset delays and clamps to one minute', () => {
    expect(subtitleVTT([{ start_ms: 0, end_ms: 1000, text: 'hello' }], 100)).toContain('00:01:00.000 --> 00:01:01.000');
  });
  it('validates each preference independently', () => {
    expect(normalizeSubtitlePreferences({ size: 'xlarge', background: 'script', position: 'top' }))
      .toEqual({ size: 'xlarge', background: 'translucent', position: 'top' });
    expect(normalizeSubtitlePreferences(null)).toEqual({ size: 'medium', background: 'translucent', position: 'bottom' });
  });
  it('projects ordered overlapping dialogue without altering source cues', () => {
    const cues = [{ start_ms: 2000, end_ms: 4000, text: 'later' }, { start_ms: 0, end_ms: 5000, text: 'long\nline' }, { start_ms: 500, end_ms: 1000, text: 'past' }];
    const rows = projectTranscript(cues, -1);
    expect(rows.map(row => row.id)).toEqual([1, 0]);
    expect(activeTranscriptRows(rows, 1500)).toEqual([1, 0]);
    expect(activeTranscriptRows(rows, 3000)).toEqual([1]);
    expect(activeTranscriptRows(rows, 4000)).toEqual([]);
    expect(cues[1]).toEqual({ start_ms: 0, end_ms: 5000, text: 'long\nline' });
    expect(rows[0].startMS).toBe(0);
  });
  it('keeps unknown and out-of-range dialogue from expanding a clip', () => {
    const rows = projectTranscript([{ start_ms: 1000, end_ms: 2000, text: '<b>plain</b>' }], 0.5);
    expect(subtitleSeekPosition(rows[0], { startMS: 1600, endMS: 1900 })).toBeNull();
    expect(subtitleSeekPosition(rows[0], { startMS: 0, endMS: 1500 })).toBeNull();
    expect(subtitleSeekPosition(rows[0], { startMS: 1000, endMS: 1800 })).toBe(1.5);
    expect(subtitleSeekPosition(rows[0])).toBe(1.5);
  });
});
