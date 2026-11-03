import {mount,flushPromises,enableAutoUnmount} from '@vue/test-utils';
import {afterEach,expect,it,vi} from 'vitest';
import Dialog from './ChapterDraftDialog.vue';
enableAutoUnmount(afterEach);
const draft={job:{id:1,revision:3,status:'draft_ready',video_id:2,scope_start_ms:0,scope_end_ms:1000},expected_chapter_revision:7,chapters:[]};
it('shows boundary conflicts and offers no application token action',async()=>{
 const api={preview:vi.fn().mockResolvedValue({conflicts:[{id:4,title:'跨界章',start_ms:0,end_ms:5000}],remove_ids:[],additions:[],retained:1,apply_token:''}),apply:vi.fn()};
 const wrapper=mount(Dialog,{props:{draft,api}});await wrapper.vm.prepareApplication();expect(wrapper.text()).toContain('跨界章');await wrapper.vm.apply();expect(api.apply).not.toHaveBeenCalled();
});
it('revokes late unsubmitted previews when hidden but preserves an already submitted apply',async()=>{
 let resolve;const api={preview:vi.fn().mockImplementation(()=>new Promise(done=>{resolve=done;})),discardPreview:vi.fn().mockResolvedValue({}),apply:vi.fn()};
 const wrapper=mount(Dialog,{props:{draft,api}});const pending=wrapper.vm.prepareApplication();await wrapper.setProps({pageActive:false});resolve({apply_token:'late',conflicts:[],remove_ids:[],additions:[]});await pending;expect(api.discardPreview).toHaveBeenCalledWith('late');
 await wrapper.setProps({pageActive:true});await wrapper.setData({application:{apply_token:'submitted',expires_at:'2099-01-01T00:00:00Z',conflicts:[],remove_ids:[],additions:[]}});let applied;api.apply.mockImplementation(()=>new Promise(done=>{applied=done;}));const action=wrapper.vm.apply();await flushPromises();await wrapper.setProps({pageActive:false});expect(api.discardPreview).not.toHaveBeenCalledWith('submitted');applied({});await action;
});
