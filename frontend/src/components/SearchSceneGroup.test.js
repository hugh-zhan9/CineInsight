import { mount, flushPromises } from '@vue/test-utils';
import { afterEach, expect, it, vi } from 'vitest';
import Group from './SearchSceneGroup.vue';
vi.mock('../utils/searchResults.js', async importOriginal => ({ ...(await importOriginal()), searchResultsAPI: {} }));
const hit = (id, source = 'source') => ({ video_id: 1, segment_id: id, start_ms: id * 1000, end_ms: id * 1000 + 500, source: 'dialogue', source_version: source, title: 'video' });
let wrapper; afterEach(() => wrapper?.unmount());
it('loads independent video pages and deduplicates changing hit pages without silently restarting', async () => {
 const api = { dialogue: vi.fn().mockResolvedValue({ items: [{ video_id: 1, title: 'video', hit_count: 2000, hits: [hit(1)], next_cursor: 'hit-cursor' }], next_cursor: 'video-cursor', notices: [] }), hits: vi.fn().mockResolvedValue({ items: [hit(1),hit(2)], hit_count: 2001, next_cursor: 'next-hit' }) };
 wrapper = mount(Group, { props: { api, request: { query: '台词' } } }); await flushPromises();
 await wrapper.vm.moreHits(wrapper.vm.groups[0]); expect(wrapper.vm.groups[0].hits.map(hit => hit.segment_id)).toEqual([1,2]);
 expect(api.hits).toHaveBeenCalledWith({ query: '台词', filter: undefined, video_id: 1, cursor: 'hit-cursor', limit: 30 });
 api.hits.mockRejectedValueOnce(new Error('search_cursor_invalid')); await wrapper.vm.moreHits(wrapper.vm.groups[0]);
 expect(wrapper.vm.groups[0].next_cursor).toBe('next-hit'); expect(wrapper.vm.groups[0].hits).toHaveLength(2);
 api.dialogue.mockResolvedValueOnce({ items: [{ video_id: 2, title: 'other', hits: [], hit_count: 1 }], next_cursor: '', notices: [] });
 await wrapper.vm.load(true); expect(api.dialogue).toHaveBeenLastCalledWith({ query: '台词', filter: undefined, cursor: 'video-cursor', limit: 20 }); expect(wrapper.vm.groups).toHaveLength(2);
});
it('marks ranked limits honestly and drops old-query responses', async () => {
 let resolve; const api = { visual: vi.fn().mockImplementationOnce(() => new Promise(done => { resolve = done; })).mockResolvedValue({ items: [{...hit(4),source:'visual'}], truncated: true, notices: [] }) };
 wrapper = mount(Group, { props: { api, kind: 'visual', request: { query: 'old' } } });
 await wrapper.setProps({ request: { query: 'new' } }); await flushPromises(); resolve({ items: [hit(99)], notices: [] }); await flushPromises();
 expect(wrapper.vm.groups[0].hits[0].segment_id).toBe(4); expect(wrapper.text()).toContain('最相关的前 1 段'); expect(wrapper.text()).toContain('不代表全库命中总数');
});
it('does not describe an unavailable visual search as zero matches', async () => {
 const api = { visual: vi.fn().mockResolvedValue({ items: [], notices:['scene_runtime_unavailable'], truncated:false }) };
 wrapper = mount(Group,{props:{api,kind:'visual',request:{query:'雨夜'}}}); await flushPromises();
 expect(wrapper.text()).toContain('检索未执行'); expect(wrapper.text()).not.toContain('没有命中'); expect(wrapper.text()).not.toContain('最相关的前 0');
});
