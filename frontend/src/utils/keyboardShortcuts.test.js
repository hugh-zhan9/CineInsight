import { describe, expect, it } from 'vitest';
import { shortcutActionForEvent } from './keyboardShortcuts.js';

describe('keyboard shortcuts', () => {
  it('maps review keys and arrows', () => {
    expect(shortcutActionForEvent({ key: 'j' })).toBe('next');
    expect(shortcutActionForEvent({ key: 'ArrowUp' })).toBe('previous');
    expect(shortcutActionForEvent({ key: ' ' })).toBe('preview');
    expect(shortcutActionForEvent({ key: 'Enter' })).toBe('play');
  });

  it('does not hijack typing, modified, or repeated action keys', () => {
    const input = document.createElement('input');
    expect(shortcutActionForEvent({ key: 'f', target: input })).toBe('');
    expect(shortcutActionForEvent({ key: 'f', ctrlKey: true })).toBe('');
    expect(shortcutActionForEvent({ key: 'f', repeat: true })).toBe('');
  });
});
