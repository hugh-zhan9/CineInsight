import { describe, it, expect } from 'vitest';
import { subtitleVTT } from './playbackSubtitles.js';
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
});
