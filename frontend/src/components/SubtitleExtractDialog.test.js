import { mount, flushPromises } from '@vue/test-utils';
import { beforeEach, expect, it, vi } from 'vitest';
const api = vi.hoisted(() => ({ListExtractableSubtitleTracks:vi.fn(),PreviewSubtitleExtraction:vi.fn(),ConfirmSubtitleExtraction:vi.fn(),CancelSubtitleExtraction:vi.fn(),GetSubtitleOverwriteInfo:vi.fn()}));
vi.mock('../../wailsjs/go/main/App',()=>api);
import SubtitleExtractDialog from './SubtitleExtractDialog.vue';
beforeEach(()=>{
 vi.clearAllMocks();
 api.ListExtractableSubtitleTracks.mockResolvedValue({source_version:'v1',tracks:[{index:2,codec:'ass',supported:true,reason:'样式将丢失'}]});
 api.CancelSubtitleExtraction.mockResolvedValue();
 api.GetSubtitleOverwriteInfo.mockResolvedValue({exists:true,shared_with:[{name:'other.mkv'}]});
 api.PreviewSubtitleExtraction.mockResolvedValue({token:'p',video_id:1,source_version:'v1',track_index:2,target_fingerprint:{sha256:'old'},entries:[{client_id:'1',text:'hello'}]});
 api.ConfirmSubtitleExtraction.mockResolvedValue({status:'saved_index_pending',message:'已保存，索引待刷新'});
});
it('requires a preview and confirms its exact source/target identity with shared overwrite notice',async()=>{
 const wrapper=mount(SubtitleExtractDialog,{props:{videoId:1}});await flushPromises();
 await wrapper.get('select').setValue('2');await wrapper.findAll('button').find(button=>button.text()==='提取并预览').trigger('click');await flushPromises();
 expect(api.PreviewSubtitleExtraction).toHaveBeenCalledWith({video_id:1,source_version:'v1',track_index:2});
 expect(wrapper.text()).toContain('other.mkv');expect(wrapper.text()).toContain('样式将丢失');
 await wrapper.findAll('button').find(button=>button.text()==='确认保存同名 SRT').trigger('click');await flushPromises();
 expect(api.ConfirmSubtitleExtraction).toHaveBeenCalledWith({token:'p',video_id:1,source_version:'v1',track_index:2,target_fingerprint:{sha256:'old'}});
 expect(wrapper.emitted('saved')[0][0].status).toBe('saved_index_pending');wrapper.unmount();
});
it('cancels extraction and never shows a late preview after close',async()=>{
 let resolve;api.PreviewSubtitleExtraction.mockImplementation(()=>new Promise(done=>{resolve=done}));
 const wrapper=mount(SubtitleExtractDialog,{props:{videoId:1}});await flushPromises();await wrapper.get('select').setValue('2');const pending=wrapper.vm.extract();wrapper.vm.close();resolve({token:'late',entries:[]});await pending;
 expect(wrapper.vm.preview).toBeNull();expect(api.CancelSubtitleExtraction).toHaveBeenCalledWith(1);wrapper.unmount();
});
