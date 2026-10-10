import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, afterEach, expect, it, vi } from 'vitest';
const api = vi.hoisted(() => Object.fromEntries(['GetPlaybackQueue', 'EditPlaybackQueue', 'PlayPlaybackQueue', 'NextPlaybackQueue', 'StopPlaybackQueue', 'ControlPlaybackQueue'].map(name => [name, vi.fn()])));
const confirm = vi.hoisted(() => vi.fn());
vi.mock('../../wailsjs/go/main/App', () => api);
vi.mock('../utils/feedback.js', () => ({ confirmAction: confirm }));
import Panel from './PlaybackQueuePanel.vue';
let wrapper;
function snapshot(overrides = {}) { return { state: { revision: 1, player: 'inline', autoplay: false, status: 'paused', active_token: 'token' }, items: [{ id: 1, title: '第一集', video_id: 1, source_available: true }], total: 51, current: { id: 1, title: '第一集', video_id: 1 }, has_more: true, cursor_id: 1, cursor_position: 1, sequence: 1, capabilities: [{ player: 'inline', available: true }, { player: 'system', available: true }], session: { token: 'token', video_id: 1 }, ...overrides }; }
beforeEach(() => { vi.clearAllMocks(); api.GetPlaybackQueue.mockResolvedValue(snapshot()); confirm.mockResolvedValue(true); });
afterEach(() => { wrapper?.unmount(); wrapper = null; delete window.runtime; });
async function panel() { wrapper = mount(Panel, { props: { open: true }, global: { stubs: { QueueInlinePlayer: true } } }); await flushPromises(); }
it('failed pagination keeps the committed list and cursor', async () => {
  await panel(); api.GetPlaybackQueue.mockRejectedValueOnce(new Error('read failed'));
  await wrapper.find('[data-test="queue-next-page"]').trigger('click'); await flushPromises();
  expect(wrapper.find('[data-test="queue-item-1"]').exists()).toBe(true); expect(wrapper.vm.history).toEqual([]); expect(wrapper.vm.cursor.cursor_id).toBe(0);
  api.GetPlaybackQueue.mockResolvedValueOnce(snapshot({ items: [{ id: 2, title: '下一页', video_id: 2 }], has_more: false }));
  await wrapper.find('[data-test="queue-next-page"]').trigger('click'); await flushPromises();
  expect(api.GetPlaybackQueue).toHaveBeenLastCalledWith({ revision: 1, cursor_position: 1, cursor_id: 1, limit: 50 }); expect(wrapper.vm.history).toHaveLength(1);
});
it('clear keeps the count and revision approved before a background change', async () => {
  await panel(); let approve; confirm.mockImplementationOnce(() => new Promise(resolve => { approve = resolve; }));
  const clearing = wrapper.vm.clear(); await wrapper.setData({ snapshot: snapshot({ state: { revision: 8 }, total: 99 }) }); approve(true); await clearing;
  expect(confirm.mock.calls[0][0].message).toContain('51 项'); expect(api.EditPlaybackQueue).toHaveBeenCalledWith({ action: 'clear', expected_revision: 1 });
});
it('switching player keeps the confirmed revision and disables system autoplay', async () => {
  await panel(); let approve; confirm.mockImplementationOnce(() => new Promise(resolve => { approve = resolve; }));
  const switching = wrapper.vm.configure('system', true); await wrapper.setData({ snapshot: snapshot({ state: { revision: 9 } }) }); approve(true); await switching;
  expect(api.EditPlaybackQueue).toHaveBeenCalledWith({ action: 'configure', expected_revision: 1, player: 'system', autoplay: false });
});
it('closing the list retains the player and late old pages cannot replace new data', async () => {
  await panel(); await wrapper.setProps({ open: false }); expect(wrapper.findComponent({ name: 'QueueInlinePlayer' }).exists()).toBe(true);
  let old; api.GetPlaybackQueue.mockImplementationOnce(() => new Promise(resolve => { old = resolve; }));
  const pending = wrapper.vm.nextPage(); api.GetPlaybackQueue.mockResolvedValueOnce(snapshot({ items: [{ id: 9, title: '权威', video_id: 9 }] }));
  await wrapper.vm.load(); old(snapshot({ items: [{ id: 3, title: '迟到', video_id: 3 }] })); await pending;
  expect(wrapper.vm.snapshot.items[0].id).toBe(9); expect(wrapper.vm.history).toEqual([]);
});
it('refreshes page one after a revision conflict and keeps errors visible', async () => {
  await panel(); api.GetPlaybackQueue.mockRejectedValueOnce(new Error('queue_changed')); api.GetPlaybackQueue.mockResolvedValueOnce(snapshot({ items: [], total: 0 }));
  await wrapper.vm.nextPage(); expect(api.GetPlaybackQueue).toHaveBeenLastCalledWith({ revision: 0, cursor_position: 0, cursor_id: 0, limit: 50 });
  api.GetPlaybackQueue.mockRejectedValueOnce(new Error('database unavailable')); await wrapper.vm.load(); expect(wrapper.find('[data-test="queue-error"]').text()).toContain('database unavailable');
});

it('allows adjacent moves across the page 50/51 boundary with the captured revision', async () => {
  const items = Array.from({ length: 50 }, (_, i) => ({ id: i + 1, video_id: i + 1, title: `item ${i + 1}`, position: i + 1 }));
  api.GetPlaybackQueue.mockResolvedValue(snapshot({ items, cursor_id: 50, cursor_position: 50 })); await panel();
  let down = wrapper.find('[data-test="queue-item-50"]').findAll('button').find(button => button.text() === '下移');
  expect(down.element.disabled).toBe(false);
  api.GetPlaybackQueue.mockResolvedValueOnce(snapshot({ items: [{ id: 51, position: 51 }] }));
  await down.trigger('click'); await flushPromises();
  expect(api.EditPlaybackQueue).toHaveBeenLastCalledWith({ action: 'move', entry_id: 51, before_entry_id: 50, expected_revision: 1 });
  await wrapper.setData({ snapshot: snapshot({ items: [{ id: 51, position: 51 }], has_more: false }), history: [{ revision: 0, cursor_id: 0, cursor_position: 0 }], cursor: { revision: 1, cursor_id: 50, cursor_position: 50 } });
  const up = wrapper.find('[data-test="queue-item-51"]').findAll('button').find(button => button.text() === '上移');
  expect(up.element.disabled).toBe(false);
  api.GetPlaybackQueue.mockResolvedValueOnce(snapshot({ items }));
  await up.trigger('click'); await flushPromises();
  expect(api.EditPlaybackQueue).toHaveBeenLastCalledWith({ action: 'move', entry_id: 51, before_entry_id: 50, expected_revision: 1 });
});

it('unmounts the actual media component on a maintenance RPC code', async () => {
  const pause = vi.spyOn(HTMLMediaElement.prototype, 'pause').mockImplementation(() => {});
  const load = vi.spyOn(HTMLMediaElement.prototype, 'load').mockImplementation(() => {});
  try {
    wrapper = mount(Panel, { props: { open: true } }); await flushPromises();
    expect(wrapper.find('video').exists()).toBe(true);
    api.GetPlaybackQueue.mockRejectedValueOnce(new Error('queue_unavailable: 数据库正在恢复或切换，暂时无法读取'));
    await wrapper.vm.load(); await flushPromises();
    expect(wrapper.find('video').exists()).toBe(false); expect(pause).toHaveBeenCalled();
    expect(wrapper.find('[data-test="queue-item-1"]').exists()).toBe(true);
  } finally { pause.mockRestore(); load.mockRestore(); }
});
