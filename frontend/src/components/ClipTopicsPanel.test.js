import { mount, flushPromises } from '@vue/test-utils';
import { afterEach, expect, it, vi } from 'vitest';
import Panel from './ClipTopicsPanel.vue';
const topic = { topic: { id: 1, revision: 3, name: '专题', note: '' }, member_count: 2 };
const members = [{ bookmark: { id: 11, title: '片段', video_title: '视频', start_ms: 0, end_ms: 1000, revision: 2 }, position: 1 }];
function api() { return { list: vi.fn().mockResolvedValue({ items: [topic], next_cursor: 0 }), members: vi.fn().mockResolvedValue({ topic, items: members, cursor_position: 1, cursor_bookmark_id: 11 }), queue: vi.fn().mockResolvedValue({ state: { revision: 7 } }), preview: vi.fn().mockResolvedValue({ topic_title: '专题', items: [], preview_token: 'preview', expires_at: '2099-01-01T00:00:00Z', conflicts: 0 }), discard: vi.fn().mockResolvedValue({}), edit: vi.fn().mockResolvedValue(topic) }; }
let wrapper; afterEach(() => { wrapper?.unmount(); wrapper = null; });
async function setup(service = api()) { wrapper = mount(Panel, { props: { api: service }, global: { stubs: { BookmarkPanel: true, ClipQueuePreviewDialog: true } } }); await flushPromises(); await wrapper.find('.clip-topics__choice').trigger('click'); await flushPromises(); return service; }
it('sends exact selected IDs and uses a separate explicit all request', async () => {
  const service = await setup(); expect(service.preview).not.toHaveBeenCalled();
  await wrapper.find('input[type=checkbox]').setValue(true); await wrapper.get('[data-test="clip-preview-selected"]').trigger('click'); await flushPromises();
  expect(service.preview).toHaveBeenCalledWith({ topic_id: 1, topic_revision: 3, bookmark_ids: [11], all: false, queue_revision: 7 });
  wrapper.vm.preview = null; await wrapper.vm.$nextTick(); await wrapper.get('[data-test="clip-preview-all"]').trigger('click'); await flushPromises();
  expect(service.preview).toHaveBeenLastCalledWith({ topic_id: 1, topic_revision: 3, bookmark_ids: [], all: true, queue_revision: 7 });
});
it('does not replace members or silently reset a stale paginated revision', async () => {
  const service = await setup(); service.members.mockRejectedValueOnce(Object.assign(new Error('专题已变化'), { code: 'conflict' }));
  await wrapper.vm.loadMembers(false); expect(wrapper.vm.members).toEqual(members); expect(wrapper.text()).toContain('专题已变化');
  expect(service.members).toHaveBeenLastCalledWith({ topic_id: 1, revision: 3, cursor_position: 1, cursor_bookmark_id: 11, limit: 50 });
});
it('discards a late unsubmitted preview after its host is gone', async () => {
  const service = await setup(); let resolve; service.preview.mockImplementation(() => new Promise(done => { resolve = done; }));
  const pending = wrapper.vm.prepare(true); await flushPromises(); wrapper.unmount(); resolve({ items: [], preview_token: 'late' }); await pending;
  expect(service.discard).toHaveBeenCalledWith('late');
});

it('rejects a late preview when its kept-alive page becomes inactive', async () => {
  const service = await setup(); let resolve; service.preview.mockImplementation(() => new Promise(done => { resolve = done; }));
  const pending = wrapper.vm.prepare(true); await flushPromises(); await wrapper.setProps({ pageActive: false });
  resolve({ items: [], preview_token: 'inactive' }); await pending;
  expect(wrapper.vm.preview).toBeNull(); expect(service.discard).toHaveBeenCalledWith('inactive');
});
it('hides an already open preview and preserves only editor data while inactive', async () => {
  await setup(); await wrapper.vm.prepare(true); await wrapper.setData({ editor: { id: 0, expected_revision: 0, name: 'draft', note: 'keep' } });
  await wrapper.setProps({ pageActive: false }); expect(wrapper.vm.preview).toBeNull(); expect(wrapper.vm.editor.name).toBe('draft');
  expect(wrapper.findComponent({ name: 'ClipQueuePreviewDialog' }).exists()).toBe(false);
  expect(wrapper.findComponent({ name: 'DialogShell' }).exists()).toBe(false);
});
