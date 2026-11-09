import { mount, flushPromises } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
const bridge = vi.hoisted(() => ({ GetPreviewSession: vi.fn(), OpenMediaExploration: vi.fn(), GetMediaExplorationItems: vi.fn(), UpdateMediaExploration: vi.fn() }));
vi.mock('../../../wailsjs/go/main/App', () => bridge);
vi.mock('../../utils/feedback.js', () => ({ confirmAction: vi.fn(async () => true) }));
import ExplorePage from './ExplorePage.vue';

const Wall = { name: 'MediaWall', props: ['entries', 'view'], emits: ['visible', 'view', 'select', 'exposure', 'open'], template: '<div data-test="wall-stub" />', methods: { focus() {} } };
const Gallery = { name: 'RandomGallery', props: ['entries'], emits: ['exposure', 'open'], template: '<div data-test="gallery-stub" />' };
const Player = { name: 'InlineVideoPlayer', props: ['usage', 'session', 'bookmarkRange'], template: '<div data-test="player-stub" />', methods: { pause() {} } };
const mounted = [];
function fixture(count = 40) {
  let state = { database_epoch: 'db', scope: 'all', scene_id: 'scene', round_id: 'round', revision: 1, status: count ? 'ready' : 'empty', available_count: count, remaining_count: count, presented_count: 0, view_state: { mode: 'wall', scale: .35, x: 0, y: 0, selected_id: 0, gallery_id: 0, gallery_entry_ids: [] }, entries: Array.from({ length: count }, (_, i) => ({ entry_id: i + 1, media_id: i + 1, kind: 'video', slot: i, presented: false, samples: [] })) };
  const api = { open: vi.fn(async () => JSON.parse(JSON.stringify(state))), items: vi.fn(async request => ({ items: request.entry_ids.map(id => ({ entry_id: id, media_id: id, kind: 'video', title: `视频 ${id}`, duration_ms: 100000, thumbnail_url: `/preview/thumbnail/${id}?exploration_scope=all` })) })), update: vi.fn(async request => {
    state.revision++; state.last_receipt = { request_id: request.request_id };
    if (request.action === 'present') { for (const e of state.entries) if (request.entry_ids.includes(e.entry_id)) e.presented = true; state.presented_count = state.entries.filter(e => e.presented).length; }
    if (request.action === 'replace' || request.action === 'replenish') {
      const ids = request.entry_ids || [state.entries[0]?.entry_id];
      state.entries = state.entries.map(entry => ids.includes(entry.entry_id) ? { ...entry, entry_id: entry.entry_id + 1000, media_id: entry.media_id + 1000, presented: false, samples: [] } : entry);
    }
    if (request.action === 'save_view') state.view_state = request.view;
    if (request.action === 'sample') state.entries.find(e => e.entry_id === request.entry_id).samples = [{ start_ms: 12000, end_ms: 20000, source_version: 'source' }];
    return JSON.parse(JSON.stringify(state));
  }) };
  return api;
}
async function page(api = fixture()) {
  const wrapper = mount(ExplorePage, { props: { api }, global: { renderStubDefaultSlot: true, stubs: { MediaWall: Wall, RandomGallery: Gallery, InlineVideoPlayer: Player, PhotoLightbox: true, Teleport: true, DialogShell: { template: '<div><slot/><slot name="footer"/></div>' } } } });
  mounted.push(wrapper); await flushPromises(); return wrapper;
}
beforeEach(() => { vi.spyOn(document, 'hasFocus').mockReturnValue(true); bridge.GetPreviewSession.mockResolvedValue({ video_id: 1, mode: 'inline', source_version: 'source', inline_source: { locator_value: '/preview/media/1', mime: 'video/mp4' } }); });
afterEach(() => { for (const wrapper of mounted.splice(0)) wrapper.unmount(); vi.restoreAllMocks(); vi.clearAllMocks(); });

describe('exploration page', () => {
  it('opens without AI or manual annotations and switches views without reopening the scope', async () => {
    const api = fixture(), wrapper = await page(api);
    expect(wrapper.find('[data-test="wall-stub"]').exists()).toBe(true);
    await wrapper.get('[data-test="explore-gallery"]').trigger('click');
    expect(wrapper.find('[data-test="gallery-stub"]').exists()).toBe(true);
    expect(wrapper.findComponent(Gallery).props('entries')).toHaveLength(24);
    expect(api.open).toHaveBeenCalledTimes(1);
  });
  it('only acknowledges continuous visible frames and excludes hidden-page dwell', async () => {
    const api = fixture(), wrapper = await page(api); let now = 0;
    vi.spyOn(performance, 'now').mockImplementation(() => now);
    const cells = [{ id: 1, loaded: true, fraction: .5 }, { id: 2, loaded: false, fraction: 1 }];
    wrapper.vm.exposure(cells); now = 300; wrapper.vm.exposure(cells); await wrapper.vm.flush();
    expect(api.update.mock.calls.filter(([r]) => r.action === 'present').map(([r]) => r.entry_ids)).toEqual([[1]]);
    await wrapper.setProps({ pageActive: false }); now = 1000; wrapper.vm.exposure([{ id: 3, loaded: true, fraction: 1 }]); now = 1400; wrapper.vm.exposure([{ id: 3, loaded: true, fraction: 1 }]); await wrapper.vm.flush();
    expect(api.update.mock.calls.filter(([r]) => r.action === 'present')).toHaveLength(1);
  });
  it('uses a source-bound single short preview and preserves the wall on close', async () => {
    const wrapper = await page();
    wrapper.vm.cameraChanged({ scale: 1.2, x: -300, y: -400 });
    await wrapper.vm.openPreview(wrapper.vm.snapshot.entries[0]); await flushPromises();
    expect(wrapper.vm.previewError).toBe('');
    expect(wrapper.vm.previewRange).toMatchObject({ startMS: 12000, endMS: 20000 });
    expect(wrapper.find('[data-test="player-stub"]').exists(), wrapper.html()).toBe(true);
    const player = wrapper.findComponent({ name: 'InlineVideoPlayer' });
    expect(player.props('usage')).toBe('browse-preview');
    expect(player.props('bookmarkRange')).toMatchObject({ startMS: 12000, endMS: 20000, sourceToken: 'source' });
    expect(player.props('session').inline_source.locator_value).toContain('exploration_source=source');
    wrapper.vm.closePreview(); await flushPromises();
    expect(wrapper.vm.view).toMatchObject({ scale: 1.2, x: -300, y: -400 });
    expect(wrapper.vm.snapshot.scene_id).toBe('scene');
  });
  it('discards a preview response arriving after page deactivation', async () => {
    let resolve; bridge.GetPreviewSession.mockImplementationOnce(() => new Promise(r => { resolve = r; }));
    const wrapper = await page(); const opened = wrapper.vm.openPreview(wrapper.vm.snapshot.entries[0]); await flushPromises();
    await wrapper.setProps({ pageActive: false }); resolve({ video_id: 1, mode: 'inline', source_version: 'source' }); await opened; await flushPromises();
    expect(wrapper.vm.preview).toBeNull(); expect(wrapper.find('[data-test="player-stub"]').exists()).toBe(false);
  });

  it('shows sampled frames for a video needing a proxy without mounting a player', async () => {
    bridge.GetPreviewSession.mockResolvedValueOnce({ video_id: 1, mode: 'proxy-preparing', source_version: 'source' });
    const wrapper = await page(); await wrapper.vm.openPreview(wrapper.vm.snapshot.entries[0]); await flushPromises();
    expect(wrapper.find('.explore-still').exists()).toBe(true);
    expect(wrapper.findComponent(Player).exists()).toBe(false);
    expect(wrapper.get('.explore-still').attributes('src')).toContain('exploration_source=source');
  });

  it('restarts dwell after explicitly discarding an unacknowledged same-scene presentation', async () => {
    const api = fixture(), wrapper = await page(api); let now = 0;
    vi.spyOn(performance, 'now').mockImplementation(() => now);
    const item = { id: 1, loaded: true, fraction: 1 };
    wrapper.vm.exposure([item]); now = 300; wrapper.vm.exposure([item]);
    api.update.mockRejectedValueOnce(Object.assign(new Error('save failed'), { code: 'dependency_unavailable' }));
    await expect(wrapper.vm.flush()).rejects.toThrow('save failed');
    await wrapper.vm.reload(); now = 1000; wrapper.vm.exposure([item]); now = 1300; wrapper.vm.exposure([item]);
    await wrapper.vm.flush();
    expect(wrapper.vm.snapshot.presented_count).toBe(1);
  });
  it('distinguishes an empty library from a request failure', async () => {
    const empty = await page(fixture(0)); expect(empty.text()).toContain('还没有可探索的内容');
    const api = fixture(); api.open.mockRejectedValue(new Error('查询失败')); const failed = await page(api);
    expect(failed.text()).toContain('查询失败'); expect(failed.text()).not.toContain('还没有可探索的内容');
  });

  it('lets an initially empty scope discover newly imported media without resetting another scope', async () => {
    const api = fixture(0), wrapper = await page(api);
    expect(wrapper.get('[data-test="explore-next"]').attributes('disabled')).toBeUndefined();
    api.update.mockResolvedValueOnce({ ...wrapper.vm.snapshot, revision: 2, scene_id: 'new-scene', status: 'ready', available_count: 1, remaining_count: 1, entries: [{ entry_id: 1, media_id: 1, kind: 'image', slot: 0, presented: false, samples: [] }] });
    await wrapper.get('[data-test="explore-next"]').trigger('click'); await flushPromises();
    expect(api.update).toHaveBeenLastCalledWith(expect.objectContaining({ action: 'next' }));
    expect(wrapper.vm.snapshot.status).toBe('ready');
  });

  it('explains unknown duration and keeps the existing thumbnail without random sampling', async () => {
    const api = fixture(), original = api.items.getMockImplementation();
    api.items.mockImplementation(async request => { const result = await original(request); for (const item of result.items) item.duration_ms = null; return result; });
    const wrapper = await page(api); await wrapper.vm.openPreview(wrapper.vm.snapshot.entries[0]); await flushPromises();
    expect(wrapper.text()).toContain('片长未知'); expect(wrapper.find('.explore-still').exists()).toBe(true);
    expect(wrapper.findComponent(Player).exists()).toBe(false);
    expect(api.update.mock.calls.some(([request]) => request.action === 'sample')).toBe(false);
  });
});


it('replaces all opened items on close while retaining other cards and camera', async () => {
  const api = fixture(), wrapper = await page(api);
  await wrapper.vm.loadCards([1, 2, 3]);
  const third = wrapper.vm.cards[3];
  wrapper.vm.cameraChanged({ scale: 1.2, x: -300, y: -400 });
  await wrapper.vm.openPreview(wrapper.vm.snapshot.entries[0]);
  await wrapper.vm.stepPreview(1);
  expect(api.update.mock.calls.some(([r]) => r.action === 'replace')).toBe(false);
  wrapper.vm.closePreview(); await wrapper.vm.refill(); await flushPromises();
  expect(api.update.mock.calls.find(([r]) => r.action === 'replace')[0].entry_ids).toEqual([1, 2]);
  expect(wrapper.vm.snapshot.entries.slice(0, 3).map(e => e.entry_id)).toEqual([1001, 1002, 3]);
  expect(wrapper.vm.cards[3]).toBe(third);
  expect(wrapper.vm.view).toMatchObject({ scale: 1.2, x: -300, y: -400 });
});

it('waits for detail return before replacing the opened item', async () => {
  const api = fixture(), wrapper = await page(api);
  await wrapper.vm.openPreview(wrapper.vm.snapshot.entries[0]);
  await wrapper.vm.openDetail(); await wrapper.setProps({ pageActive: false });
  await wrapper.vm.refill();
  expect(api.update.mock.calls.some(([r]) => r.action === 'replace')).toBe(false);
  await wrapper.setProps({ pageActive: true }); await wrapper.vm.refill();
  expect(wrapper.vm.snapshot.entries[0].entry_id).toBe(1001);
});

it('coalesces library events and refills after returning without refreshing the whole wall', async () => {
  const handlers = {};
  window.runtime = { EventsOn: (name, fn) => { handlers[name] = fn; return () => {}; } };
  try {
    const api = fixture(), wrapper = await page(api);
    await wrapper.setProps({ pageActive: false });
    handlers['image-deleted'](); handlers['image-library-changed']();
    await wrapper.vm.refill();
    expect(api.update.mock.calls.some(([r]) => r.action === 'replenish')).toBe(false);
    await wrapper.setProps({ pageActive: true }); await wrapper.vm.refill();
    expect(api.update.mock.calls.filter(([r]) => r.action === 'replenish')).toHaveLength(1);
    expect(wrapper.vm.snapshot.entries[0].entry_id).toBe(1001);
    expect(wrapper.vm.snapshot.entries[1].entry_id).toBe(2);
  } finally { delete window.runtime; }
});

it('acknowledges reused entries again after an automatic round change', async () => {
  const api=fixture(), wrapper=await page(api); let now=0;
  vi.spyOn(performance,'now').mockImplementation(()=>now);
  const cells=[{id:1,loaded:true,fraction:1}];
  wrapper.vm.exposure(cells);now=300;wrapper.vm.exposure(cells);await wrapper.vm.flush();
  api.update.mockResolvedValueOnce({...wrapper.vm.snapshot,round_id:'next-round',revision:wrapper.vm.snapshot.revision+1,entries:wrapper.vm.snapshot.entries.map(e=>({...e,presented:false}))});
  await wrapper.vm._controller.change('replace',{entry_ids:[2]});
  now=1000;wrapper.vm.exposure(cells);now=1300;wrapper.vm.exposure(cells);await wrapper.vm.flush();
  expect(api.update.mock.calls.filter(([r])=>r.action==='present')).toHaveLength(2);
});

it('discards a stale metadata failure after local replacement and loads the new slot', async () => {
  const api=fixture(), wrapper=await page(api);
  let reject;api.items.mockImplementationOnce(()=>new Promise((_resolve,r)=>{reject=r;}));
  const old=wrapper.vm.loadCards([1]);
  await wrapper.vm._controller.change('replace',{entry_ids:[1]});
  const fresh=wrapper.vm.loadCards([1001]);
  reject(Object.assign(new Error('retired entry'),{code:'invalid_request'}));
  await old;await fresh;await flushPromises();
  expect(wrapper.vm.error).toBe('');
  expect(wrapper.vm.cards[1001]?.title).toBe('视频 1001');
});


it('ignores a delayed viewport notification containing retired entries', async () => {
  const api=fixture(),wrapper=await page(api);
  await wrapper.vm._controller.change('replace',{entry_ids:[1]});
  api.items.mockClear();
  await wrapper.vm.loadCards([1,1001]);
  expect(api.items).toHaveBeenCalledWith(expect.objectContaining({entry_ids:[1001]}));
});
