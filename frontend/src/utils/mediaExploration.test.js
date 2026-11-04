import { describe, expect, it, vi } from 'vitest';
vi.mock('../../wailsjs/go/main/App', () => ({ OpenMediaExploration: vi.fn(), GetMediaExplorationItems: vi.fn(), UpdateMediaExploration: vi.fn() }));
import { createExplorationController, createExplorationImageLoader, explorationAssetURL } from './mediaExploration';

function fixture() {
  let state = { database_epoch: 'db', scope: 'all', scene_id: 'scene', round_id: 'round', revision: 1, entries: Array.from({ length: 300 }, (_, i) => ({ entry_id: i + 1, presented: false })), view_state: { mode: 'wall', scale: .35, x: 0, y: 0 }, last_receipt: null };
  const api = { open: vi.fn(async () => structuredClone(state)), update: vi.fn(async request => {
    if (request.expected_revision !== state.revision) throw Object.assign(new Error('conflict'), { code: 'exploration_conflict' });
    state.revision++;
    state.last_receipt = { request_id: request.request_id };
    if (request.action === 'present') for (const e of state.entries) if (request.entry_ids.includes(e.entry_id)) e.presented = true;
    if (request.action === 'save_view') state.view_state = request.view;
    if (request.action === 'next') state.scene_id = 'next-scene';
    return structuredClone(state);
  }) };
  return { api, state: () => state };
}

describe('exploration mutation owner', () => {
  it('flushes presentation in bounded batches before changing the scene', async () => {
    const { api } = fixture(), c = createExplorationController(api);
    await c.open('all'); c.presented(Array.from({ length: 300 }, (_, i) => i + 1)); c.view({ mode: 'gallery', scale: 1, x: -10, y: -20 });
    await c.change('next');
    expect(api.update.mock.calls.map(([r]) => r.action)).toEqual(['present', 'present', 'save_view', 'next']);
    expect(api.update.mock.calls[0][0].entry_ids).toHaveLength(256);
    expect(api.update.mock.calls[1][0].entry_ids).toHaveLength(44);
    expect(c.pending).toBe(false);
    expect(c.state.scene_id).toBe('next-scene');
  });
  it('a lost reply blocks later writes and resolves by receipt without a second mutation', async () => {
    const { api } = fixture(), original = api.update.getMockImplementation();
    api.update.mockImplementationOnce(async r => { await original(r); throw new Error('transport disconnected'); });
    const c = createExplorationController(api); await c.open('all'); c.presented([1]);
    await expect(c.flush()).rejects.toThrow('transport disconnected');
    await expect(c.change('next')).rejects.toThrow('未保存');
    expect(c.pending).toBe(true); expect(api.update).toHaveBeenCalledTimes(1);
    await c.recover();
    expect(api.update).toHaveBeenCalledTimes(1); expect(c.pending).toBe(false); expect(c.state.entries[0].presented).toBe(true);
  });
  it('user retry reuses the original identity if the operation did not commit', async () => {
    const { api } = fixture(); api.update.mockRejectedValueOnce(new Error('connection lost'));
    const c = createExplorationController(api); await c.open('all'); c.presented([2]);
    await expect(c.flush()).rejects.toThrow(); await c.recover();
    expect(api.update.mock.calls[0][0].request_id).toBe(api.update.mock.calls[1][0].request_id);
    expect(c.pending).toBe(false);
  });
  it('retains a camera change made while an earlier save is in flight', async () => {
    const { api } = fixture(), original = api.update.getMockImplementation();
    let release;
    api.update.mockImplementationOnce(r => new Promise(resolve => { release = () => resolve(original(r)); }));
    const c = createExplorationController(api); await c.open('all'); c.view({ mode: 'wall', scale: 1, x: 0, y: 0 });
    const first = c.flush(); await Promise.resolve();
    c.view({ mode: 'wall', scale: 2, x: -400, y: -300 }); release(); await first;
    expect(c.pending).toBe(true);
    await c.flush(); expect(c.state.view_state.scale).toBe(2); expect(c.pending).toBe(false);
  });
  it('does not turn an unavailable preview into a failed whole-page owner', async () => {
    const { api } = fixture(); const c = createExplorationController(api); await c.open('all');
    api.update.mockRejectedValueOnce(Object.assign(new Error('changed source'), { code: 'source_changed' }));
    await expect(c.change('sample', { entry_id: 1, source_version: 'old' })).rejects.toThrow('changed source');
    expect(c.blocked).toBe(false);
  });
  it('binds resource URLs to scope, database and exact scene entry', () => {
    const value = explorationAssetURL('/preview/frame/8?ms=1200', { scope: 'video', scene_id: 'scene', database_epoch: 'db' }, 5, 'source');
    const url = new URL(value, 'http://local');
    expect(url.searchParams.get('ms')).toBe('1200');
    expect(url.searchParams.get('exploration_entry')).toBe('5');
    expect(url.searchParams.get('exploration_source')).toBe('source');
  });
});

it('loads at most six images and cancels both running and queued consumers', async () => {
  const fetch = vi.fn((_url, { signal }) => new Promise((_resolve, reject) => signal.addEventListener('abort', () => reject(new DOMException('cancelled', 'AbortError')))));
  vi.stubGlobal('fetch', fetch);
  try {
    const loader = createExplorationImageLoader(), jobs = Array.from({ length: 20 }, (_, i) => loader.load(`/image/${i}`));
    const settled = Promise.allSettled(jobs.map(j => j.promise));
    await Promise.resolve(); expect(fetch).toHaveBeenCalledTimes(6); expect(loader.active).toBe(6);
    loader.cancelAll(); const results = await settled;
    expect(results.every(r => r.status === 'rejected')).toBe(true);
    await Promise.resolve(); await Promise.resolve(); expect(fetch).toHaveBeenCalledTimes(6);
  } finally { vi.unstubAllGlobals(); }
});
