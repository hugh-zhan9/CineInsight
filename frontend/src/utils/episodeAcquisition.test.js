import { describe, expect, it } from 'vitest';
import { episodeNumbers, episodeNumber } from './episodeAcquisition.js';
describe('explicit episode input', () => {
  it('accepts separated ranges and preserves special season zero only when explicit', () => {
    expect(episodeNumbers('3、5-7,1')).toEqual([1,3,5,6,7]);
    expect(episodeNumber('',999)).toBeNull(); expect(episodeNumber(0,999)).toBe(0);
  });
  it.each(['', '0', '3-1', '1-201', '1,1', '1-3,3', '1.5', '1000001', 'a'])('rejects invalid range %s', text => expect(() => episodeNumbers(text)).toThrow());
  it('accepts the exact batch boundary', () => expect(episodeNumbers('1-200')).toHaveLength(200));
});
