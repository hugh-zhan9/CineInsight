import {mount,enableAutoUnmount} from '@vue/test-utils';
import {afterEach,expect,it,vi} from 'vitest';
import Editor from './ChapterEditorDialog.vue';
enableAutoUnmount(afterEach);
const draft={videoID:1,revision:4,sourceVersion:'old',currentSource:'new',sourceStatus:'changed',chapters:[{id:8,title:'一章',summary:'note',start_ms:1000,end_ms:2000}]};
it('keeps an inactive draft and requires explicit acceptance of changed media before saving',async()=>{
 const api={save:vi.fn().mockResolvedValue({})};const wrapper=mount(Editor,{props:{draft,api}});
 await wrapper.vm.save();expect(api.save).not.toHaveBeenCalled();await wrapper.setProps({pageActive:false});expect(wrapper.vm.rows[0].summary).toBe('note');expect(wrapper.findComponent({name:'DialogShell'}).exists()).toBe(false);
 await wrapper.setProps({pageActive:true});await wrapper.setData({acceptSource:true});await wrapper.vm.save();expect(api.save).toHaveBeenCalledWith({video_id:1,expected_revision:4,source_version:'new',chapters:draft.chapters,clear:false});
});
it('requires explicit empty clearing and rejects sub-millisecond cuts',async()=>{
 const api={save:vi.fn().mockResolvedValue({})};const wrapper=mount(Editor,{props:{draft:{...draft,sourceStatus:'ready'},api}});
 wrapper.vm.rows[0].startSeconds='1.0001';await wrapper.vm.save();expect(api.save).not.toHaveBeenCalled();expect(wrapper.vm.error).toContain('毫秒');wrapper.vm.remove(0);await wrapper.vm.save();expect(api.save).not.toHaveBeenCalled();await wrapper.setData({clear:true});await wrapper.vm.save();expect(api.save.mock.calls[0][0]).toMatchObject({chapters:[],clear:true});
});
