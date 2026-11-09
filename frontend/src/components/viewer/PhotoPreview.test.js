import { mount, flushPromises } from '@vue/test-utils';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
const api = vi.hoisted(() => ({ SearchImagePage: vi.fn() }));
vi.mock('../../../wailsjs/go/main/App', async original => ({ ...await original(), ...api }));
import Preview from './PhotoPreview.vue';
import Core from './PhotoLightbox.vue';
const Stub = { props: Core.props, emits: Core.emits, template: '<div><slot name="related" /></div>' };
const photo = (id, directory = '/photos/trip') => ({ id, directory, name: `photo-${id}.jpg` });
const mounted = [];
async function preview(props = {}) {
 const wrapper = mount(Preview, { props: { image: photo(1), hasPrev: true, hasNext: true, ...props }, global: { stubs: { PhotoLightboxCore: Stub } } });
 mounted.push(wrapper); await flushPromises(); return wrapper;
}
beforeEach(() => api.SearchImagePage.mockReset().mockResolvedValue({images: [photo(1),photo(2)],next_cursor:null}));
afterEach(() => { mounted.splice(0).forEach(w=>w.unmount()); vi.restoreAllMocks(); });
it('uses exact directory filtering, bounded name-sorted pages and preserves origin navigation', async () => {
 const w=await preview();
 expect(api.SearchImagePage).toHaveBeenCalledWith({filter:{directory:'/photos/trip',sort_mode:'name'},limit:24});
 await w.get('[aria-label="查看同目录图片 photo-2.jpg"]').trigger('click');
 expect(w.getComponent(Stub).props('image').id).toBe(2);
 expect(w.getComponent(Stub).props('allowRemove')).toBe(false);
 expect(w.emitted('next')).toBeUndefined();
 await w.get('[data-test=photo-directory-return]').trigger('click');
 expect(w.getComponent(Stub).props('image').id).toBe(1);
 w.getComponent(Stub).vm.$emit('next');expect(w.emitted('next')).toHaveLength(1);
});
it('does not use the exploration source URL for a different image and restores it on return', async () => {
 const w=await preview({assetUrl:'/preview/image/1?exploration_entry=8',mixedMedia:true});
 w.vm.select(photo(2));await flushPromises();
 expect(w.getComponent(Stub).props('assetUrl')).toBe('');expect(w.getComponent(Stub).props('mixedMedia')).toBe(false);
 w.vm.selected=null;await flushPromises();
 expect(w.getComponent(Stub).props('assetUrl')).toContain('exploration_entry=8');
});
it('loads the next page on directory navigation and can return using its saved cursor', async () => {
 api.SearchImagePage.mockResolvedValueOnce({images:[photo(1),photo(2)],next_cursor:{id:2}}).mockResolvedValueOnce({images:[photo(3)],next_cursor:null});
 const w=await preview();w.vm.select(photo(2));w.vm.navigate(1);await flushPromises();
 expect(api.SearchImagePage).toHaveBeenLastCalledWith(expect.objectContaining({cursor:{id:2}}));
 expect(w.getComponent(Stub).props('image').id).toBe(3);expect(w.vm.items).toHaveLength(1);
 w.vm.navigate(-1);await flushPromises();expect(w.vm.currentImage.id).toBe(2);
 expect(api.SearchImagePage).toHaveBeenLastCalledWith({filter:{directory:'/photos/trip',sort_mode:'name'},limit:24});
});
it('ignores old directory responses and never searches the entire library for missing directory metadata', async () => {
 const w=await preview({image:{id:1,name:'no directory'}});expect(api.SearchImagePage).not.toHaveBeenCalled();
 let resolve;api.SearchImagePage.mockImplementationOnce(()=>new Promise(r=>{resolve=r;}));
 await w.setProps({image:photo(2,'/old')});await w.setProps({image:photo(3,'/new')});await flushPromises();
 resolve({images:[photo(7,'/old')]});await flushPromises();
 expect(w.vm.directory).toBe('/new');expect(w.vm.items.some(p=>p.id===7)).toBe(false);
});
it('keeps the preview on list failure and retries the same page explicitly', async () => {
 api.SearchImagePage.mockRejectedValueOnce(new Error('query failed'));
 const w=await preview();expect(w.vm.currentImage.id).toBe(1);expect(w.text()).toContain('同目录图片没有加载出来');
 await w.get('[data-test=photo-directory-retry]').trigger('click');await flushPromises();
 expect(w.vm.items).toHaveLength(2);expect(w.vm.error).toBe('');
});
it('applies sibling edits locally and never carries original metadata to a new source', async () => {
 const w=await preview();w.vm.patch({...photo(1),name:'old name'});w.vm.select(photo(2));w.vm.patch({id:2,is_favorite:true});await flushPromises();
 expect(w.vm.currentImage.is_favorite).toBe(true);expect(w.vm.original.name).toBe('old name');
 await w.setProps({image:photo(3,'/another')});w.vm.patch({id:3,view_count:1});await flushPromises();
 expect(w.vm.currentImage.name).toBe('photo-3.jpg');expect(w.vm.selected).toBeNull();
});
