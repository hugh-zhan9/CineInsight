import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
const api = vi.hoisted(() => ({ SuggestTonightVideos: vi.fn() }));
vi.mock('../../../wailsjs/go/main/App', () => api);
import DiscoverPage from './DiscoverPage.vue';
import { createDiscoverySession } from '../../utils/discoverySession.js';
enableAutoUnmount(afterEach);
const rows = ids => ids.map(id => ({ video: { id, name: `Video ${id}` }, reasons: [] }));
it('keeps shared home exposure when mounting with unchanged default conditions', async () => {
  const session = createDiscoverySession(); session.recent = [7];
  mount(DiscoverPage, { global: { provide: { discoverySession: session } } }); await flushPromises();
  expect(api.SuggestTonightVideos).toHaveBeenCalledWith(expect.objectContaining({ exclude_ids: [7] }), 12);
  expect(session.epoch).toBe(0);
});
beforeEach(() => { api.SuggestTonightVideos.mockReset().mockResolvedValue(rows([1,2,3])); });
async function page(props = {}) { const w = mount(DiscoverPage, { props, global: { provide: { discoverySession: createDiscoverySession() } } }); await flushPromises(); return w; }
describe('DiscoverPage', () => {
  it('requests twelve, keeps the batch on return, and opens details through its host', async () => {
    const w = await page(); expect(api.SuggestTonightVideos).toHaveBeenLastCalledWith(expect.any(Object), 12);
    await w.get('[data-test=discover-item]').trigger('click'); expect(w.emitted('open-video')).toEqual([[1]]);
    await w.setProps({ pageActive: false }); await w.setProps({ pageActive: true }); await flushPromises();
    expect(api.SuggestTonightVideos).toHaveBeenCalledTimes(1);
  });
  it('keeps explicit filters and drops delayed responses when a new condition is chosen', async () => {
    let finish; api.SuggestTonightVideos.mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
    const w = await page({ initialRequest: { token: 1, filter: { person_ids: [7] } } });
    await w.get('[data-test=discover-favorites]').setValue(true); await flushPromises();
    expect(api.SuggestTonightVideos).toHaveBeenLastCalledWith(expect.objectContaining({ filter: { person_ids: [7] }, favorites_only: true }), 12);
    finish(rows([99])); await flushPromises();
    expect(w.findAll('[data-test=discover-item]')).toHaveLength(3); expect(w.text()).not.toContain('Video 99');
  });
  it('retains the current batch on a failed reroll and permits retry', async () => {
    const w = await page(); api.SuggestTonightVideos.mockRejectedValueOnce(new Error('offline'));
    await w.get('[data-test=discover-reroll]').trigger('submit'); await flushPromises();
    expect(w.get('[role=alert]').text()).toContain('offline'); expect(w.findAll('[data-test=discover-item]')).toHaveLength(3);
    api.SuggestTonightVideos.mockResolvedValueOnce(rows([4]));
    await w.get('form').trigger('submit'); await flushPromises(); expect(w.text()).toContain('Video 4');
  });
});
