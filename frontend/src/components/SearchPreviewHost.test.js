import { mount, flushPromises } from '@vue/test-utils';
import { afterEach, expect, it, vi } from 'vitest';
import Host from './SearchPreviewHost.vue';
const hit = { video_id: 4, title: '原片', text: '字幕', start_ms: 4000, end_ms: 5000, preview_start_ms: 2000, preview_end_ms: 7000, source_version: 'captured' };
let wrapper; afterEach(() => wrapper?.unmount());
it('uses captured versions and contextual ranges without looking up new playback sessions', async () => {
 const api = { queue: vi.fn().mockResolvedValue({ state: { revision: 7 } }), preview: vi.fn().mockResolvedValue({ preview_token: 'p', items: [] }), discard: vi.fn().mockResolvedValue({}) };
 wrapper = mount(Host, { props: { api }, global: { stubs: { BookmarkPanel: true, ClipQueuePreviewDialog: true } } }); await wrapper.vm.prepare([hit]);
 expect(api.preview).toHaveBeenCalledWith({ queue_revision: 7, items: [{ video_id: 4, title: '原片', start_ms: 2000, end_ms: 7000, source_version: 'captured' }] });
});
it('discards a late preview on inactive kept-alive pages and keeps successful writes separate', async () => {
 let resolve; const api = { queue: vi.fn().mockResolvedValue({ state: { revision: 7 } }), preview: vi.fn().mockImplementation(() => new Promise(done => { resolve = done; })), discard: vi.fn().mockResolvedValue({}) };
 wrapper = mount(Host, { props: { api }, global: { stubs: { BookmarkPanel: true, ClipQueuePreviewDialog: true } } }); const pending = wrapper.vm.prepare([hit]); await flushPromises(); await wrapper.setProps({ pageActive: false }); resolve({ preview_token: 'late' }); await pending;
 expect(api.discard).toHaveBeenCalledWith('late'); expect(wrapper.vm.preview).toBeNull(); expect(wrapper.vm.busy).toBe(false);
});
it('retires the active queue token and refuses whole playback if another token appears', async () => {
 const api = { queue: vi.fn().mockResolvedValueOnce({ state: { active_token: 'old' } }).mockResolvedValueOnce({ state: { active_token: 'new' } }), stop: vi.fn().mockResolvedValue({}) };
 const watchWholeVideo = vi.fn(async (_id,stop) => stop());
 wrapper = mount(Host, { props: { api, watchWholeVideo }, global: { stubs: { BookmarkPanel: true, ClipQueuePreviewDialog: true } } }); await wrapper.vm.watchWhole(4);
 expect(api.stop).toHaveBeenCalledWith('old'); expect(wrapper.vm.error).toContain('队列已切换');
});
