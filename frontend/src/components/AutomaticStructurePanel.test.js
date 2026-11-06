import { mount, flushPromises, enableAutoUnmount } from '@vue/test-utils';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import Panel from './AutomaticStructurePanel.vue';
import { GetVideoStructure, ResolveVideoStructure, RetryVideoStructure } from '../../wailsjs/go/main/App';
vi.mock('../../wailsjs/go/main/App', () => ({ GetVideoStructure: vi.fn(), ResolveVideoStructure: vi.fn(), RetryVideoStructure: vi.fn() }));
enableAutoUnmount(afterEach);
const view = { video_id: 1, revision: 2, generation: 3, source_version: 'source', source_status: 'ready', status: 'ready', segments: [{position:0,start_ms:0,end_ms:10000,representative_ms:3000,title:'片段01'}] };
beforeEach(() => { vi.resetAllMocks(); GetVideoStructure.mockResolvedValue(view); });
it('uses source-bound thumbnails and only copies to an editor draft', async () => {
 const wrapper = mount(Panel,{props:{videoId:1}}); await flushPromises();
 expect(wrapper.find('img').attributes('src')).toContain('source_version=source');
 await wrapper.findAll('button').find(b=>b.text()==='复制到人工编辑稿').trigger('click');
 expect(wrapper.emitted('copy')[0]).toEqual([view]); expect(RetryVideoStructure).not.toHaveBeenCalled();
});
it('coalesces refresh during a verified jump without discarding its result', async () => {
 let done; ResolveVideoStructure.mockImplementation(()=>new Promise(resolve=>{done=resolve;}));
 const wrapper=mount(Panel,{props:{videoId:1}}); await flushPromises();
 const pending=wrapper.vm.seek(view.segments[0]); await wrapper.vm.load();
 expect(GetVideoStructure).toHaveBeenCalledTimes(1);
 const resolution={video_id:1,source_version:'source',start_ms:0}; done(resolution); await pending; await flushPromises();
 expect(wrapper.emitted('seek')[0]).toEqual([resolution]); expect(GetVideoStructure).toHaveBeenCalledTimes(2);
});
it('discards a jump when the host changes video while resolution is pending', async () => {
 let done; ResolveVideoStructure.mockImplementation(()=>new Promise(resolve=>{done=resolve;}));
 const wrapper=mount(Panel,{props:{videoId:1}});await flushPromises();const pending=wrapper.vm.seek(view.segments[0]);
 await wrapper.setProps({videoId:2}); done({video_id:1,start_ms:0});await pending;await flushPromises();
 expect(wrapper.emitted('seek')).toBeUndefined();expect(GetVideoStructure).toHaveBeenLastCalledWith(2);
});
it('keeps obsolete results readable but disables source-dependent actions', async () => {
 GetVideoStructure.mockResolvedValue({...view,source_status:'changed'});const wrapper=mount(Panel,{props:{videoId:1}});await flushPromises();
 expect(wrapper.text()).toContain('旧分段不能定位');expect(wrapper.find('img').exists()).toBe(false);
 await wrapper.vm.seek(view.segments[0]);expect(ResolveVideoStructure).not.toHaveBeenCalled();
});
