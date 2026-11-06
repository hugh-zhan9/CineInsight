import { describe, it, expect, vi } from 'vitest';
import { createDiscoverySession, discoveryExclusions, recordDiscoveryExposure, fetchDiscoveryBatch, resetDiscoverySession } from './discoverySession.js';
const rows = ids => ids.map(id => ({ video: { id }, reasons: [] }));
describe('discovery rounds', () => {
  it('reserves room for hard IDs even after 100 exposures', () => {
    const s = createDiscoverySession(); for (let id = 1; id <= 120; id++) recordDiscoveryExposure(s, id);
    expect(s.recent).toHaveLength(100);
    const excluded = discoveryExclusions(s, [1, 2, 3]);
    expect(excluded).toHaveLength(100); expect(excluded.slice(-3)).toEqual([1, 2, 3]);
    expect(discoveryExclusions(s, Array.from({ length: 100 }, (_, i) => i + 1))).toHaveLength(100);
  });
  it.each([0, 1, 2, 3, 9, 12, 30, 100])('browses a %i item library for multiple rounds without relaxing hard conditions', async count => {
    const s = createDiscoverySession(); let current = [];
    const fetch = vi.fn(async (request, limit) => {
      expect(request.watch_state).toBe('unwatched');
      return rows(Array.from({ length: count }, (_, i) => i + 1).filter(id => !request.exclude_ids.includes(id)).slice(0, limit));
    });
    for (let round = 0; round < 25; round++) {
      const before = fetch.mock.calls.length;
      const result = await fetchDiscoveryBatch(fetch, s, { watch_state: 'unwatched' }, 9, current);
      expect(fetch.mock.calls.length - before).toBeLessThanOrEqual(2);
      const next = result.items.map(row => row.video.id);
      expect(next.some(id => current.includes(id))).toBe(false);
      if (count > 9) expect(next.length).toBeGreaterThan(0);
      if (next.length) current = next;
      current.forEach(id => recordDiscoveryExposure(s, id));
    }
    if (count > 9 && count <= 100) expect(s.round).toBeGreaterThan(0);
  });
  it('only excludes actual exposures from the new round on the next request', async () => {
    const s = createDiscoverySession(); [1,2].forEach(id => recordDiscoveryExposure(s,id));
    const first = vi.fn().mockResolvedValueOnce([]).mockResolvedValueOnce(rows([1]));
    await fetchDiscoveryBatch(first,s,{},1,[3]); recordDiscoveryExposure(s,1);
    const next = vi.fn(async request => rows([2].filter(id => !request.exclude_ids.includes(id))));
    const result = await fetchDiscoveryBatch(next,s,{},1,[3]);
    expect(result.items).toEqual(rows([2])); expect(next).toHaveBeenCalledTimes(1);
    expect(s.round).toBe(1);
  });
  it('does not retry failures or mutate a superseded session', async () => {
    const s = createDiscoverySession(); recordDiscoveryExposure(s, 1);
    const fail = vi.fn().mockRejectedValue(new Error('offline'));
    await expect(fetchDiscoveryBatch(fail, s, {}, 9)).rejects.toThrow('offline'); expect(fail).toHaveBeenCalledTimes(1);
    let finish; const fetch = vi.fn(() => new Promise(resolve => { finish = resolve; }));
    const pending = fetchDiscoveryBatch(fetch, s, {}, 9); resetDiscoverySession(s); finish([]);
    expect(await pending).toBeNull(); expect(fetch).toHaveBeenCalledTimes(1); expect(s.round).toBe(0);
  });
});
