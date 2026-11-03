import {mount,flushPromises,enableAutoUnmount} from '@vue/test-utils';
import {afterEach,expect,it,vi} from 'vitest';
import Panel from './ChapterPanel.vue';
enableAutoUnmount(afterEach);
const sets={video_id:1,title:'电影',duration_ms:10000,source_version:'source',embedded:{source_status:'unread',chapters:[]},manual:null};
const prep={video_id:1,source_version:'source',subtitle_fingerprint:'subtitle',provider_revision:'summary-grant',expected_chapter_revision:0,scope:{whole:false,start_ms:2000,end_ms:3000},cue_count:1,character_count:3,chunk_count:1,provider:'fixture',model:'model'};
const api=()=>({sets:vi.fn().mockResolvedValue(sets),jobs:vi.fn().mockResolvedValue({items:[],next_cursor:0}),prepare:vi.fn().mockResolvedValue(prep),start:vi.fn().mockResolvedValue({job_id:7,status:'queued'}),draft:vi.fn().mockResolvedValue({job:{id:7,status:'queued'},chapters:[]})});
it('requires disclosed expanded scope and keeps request identity on an unknown start result',async()=>{
 const service=api();service.start.mockRejectedValueOnce(new Error('bridge outcome unknown'));
 const wrapper=mount(Panel,{props:{videoId:1,api:service},global:{stubs:{ChapterDraftDialog:true}}});await flushPromises();
 await wrapper.setData({whole:false,scopeStart:'2.1',scopeEnd:'2.9'});await wrapper.vm.prepare();
 expect(service.prepare).toHaveBeenCalledWith({video_id:1,scope:{whole:false,start_ms:2100,end_ms:2900}});expect(service.start).not.toHaveBeenCalled();
 await wrapper.vm.start();expect(service.start).not.toHaveBeenCalled();await wrapper.setData({confirmed:true});await wrapper.vm.start();const first=service.start.mock.calls[0][0];expect(first.scope).toEqual(prep.scope);
 await wrapper.vm.start();expect(service.start.mock.calls[1][0].request_id).toBe(first.request_id);expect(first.request_id).toMatch(/^[a-f0-9-]{36}$/);
});
it('does not open a late disclosure or draft on an inactive kept-alive page',async()=>{
 const service=api();let resolve;service.prepare.mockImplementation(()=>new Promise(done=>{resolve=done;}));
 const wrapper=mount(Panel,{props:{videoId:1,api:service}});await flushPromises();const pending=wrapper.vm.prepare();await wrapper.setProps({pageActive:false});resolve(prep);await pending;expect(wrapper.vm.preparation).toBeNull();expect(service.start).not.toHaveBeenCalled();
});
it('retains deleted-media chapters for reading without enabling edits or source jumps',async()=>{
 const service=api();service.sets.mockResolvedValue({...sets,source_version:'',duration_ms:0,manual:{set_id:'manual:1',revision:4,source_status:'unavailable',chapters:[{id:1,title:'保留章节',summary:'文字还在',start_ms:0,end_ms:1000}]}});
 const wrapper=mount(Panel,{props:{videoId:1,api:service}});await flushPromises();expect(wrapper.text()).toContain('保留章节');expect(wrapper.text()).toContain('文字还在');
 expect(wrapper.findAll('button').find(button=>button.text()==='编辑人工章节').attributes('disabled')).toBeDefined();expect(wrapper.emitted('seek')).toBeUndefined();
});
it('coalesces a job update arriving while an older list response is in flight',async()=>{
 const service=api();let finish;service.jobs.mockImplementationOnce(()=>new Promise(resolve=>{finish=resolve;})).mockResolvedValue({items:[{id:7,status:'draft_ready',scope_start_ms:0,scope_end_ms:1000}],next_cursor:0});
 const wrapper=mount(Panel,{props:{videoId:1,api:service}});await flushPromises();await wrapper.vm.loadJobs(false);finish({items:[{id:7,status:'queued',scope_start_ms:0,scope_end_ms:1000}],next_cursor:0});await flushPromises();
 expect(service.jobs).toHaveBeenCalledTimes(2);expect(wrapper.vm.jobs[0].status).toBe('draft_ready');
});
