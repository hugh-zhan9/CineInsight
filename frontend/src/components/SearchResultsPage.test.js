import { mount, flushPromises } from '@vue/test-utils';
import { afterEach, expect, it, vi } from 'vitest';
import Page from './SearchResultsPage.vue';
let wrapper; afterEach(() => wrapper?.unmount());
it('keeps successful groups when one fails and uses original entity name cursors', async () => {
 const people = Array.from({length:20},(_,id) => ({person:{id:id+1,display_name:`人${id}`},cursor_name:`数据库${id}`}));
 const api = { video: vi.fn().mockRejectedValue(new Error('video failed')), person: vi.fn().mockResolvedValueOnce(people).mockResolvedValue([]), collection: vi.fn().mockResolvedValue([{collection:{id:1,name:'作品'}}]) };
 wrapper = mount(Page, { props: { api, initialRequest: { query: '词' } }, global: { stubs: { SearchSceneGroup: true, SearchPreviewHost: true } } }); await flushPromises();
 expect(wrapper.text()).toContain('video failed'); expect(wrapper.text()).toContain('作品'); expect(wrapper.vm.states.person.items).toHaveLength(20);
 await wrapper.vm.load('person',true); expect(api.person).toHaveBeenLastCalledWith('词',{id:20,name:'数据库19'},20);
 await wrapper.setProps({ pageActive: false }); await wrapper.setProps({ pageActive: true }); expect(api.collection).toHaveBeenCalledTimes(1); expect(wrapper.vm.query).toBe('词');
});

it('paginates collections with the public cursor_name rather than private normalized_name', async () => {
 const rows = Array.from({length:20},(_,i) => ({collection:{id:i+1,name:`作品${i}`},cursor_name:`normalized-${i}`}));
 const api = { video: vi.fn().mockResolvedValue({videos:[]}), person: vi.fn().mockResolvedValue([]), collection: vi.fn().mockResolvedValueOnce(rows).mockResolvedValue([{collection:{id:21,name:'末项'},cursor_name:'zz'}]) };
 wrapper = mount(Page, { props: { api, initialRequest: { query:'作品' } }, global: { stubs: { SearchSceneGroup: true, SearchPreviewHost: true } } }); await flushPromises();
 await wrapper.vm.load('collection',true);
 expect(api.collection).toHaveBeenLastCalledWith('作品',{id:20,name:'normalized-19'},20);
 expect(wrapper.vm.states.collection.items).toHaveLength(21); expect(wrapper.vm.states.collection.cursor).toBeNull();
});
