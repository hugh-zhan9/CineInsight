import { mount, flushPromises } from '@vue/test-utils';
import { afterEach, expect, it, vi } from 'vitest';
import LibraryQueryDialog from './LibraryQueryDialog.vue';
const draft = () => ({ draft_id:'draft', conditions:[{condition_id:'c1',kind:'watch',value:'unwatched',label:'未看',source_text:'未看',entity_id:0}], unresolved:[],unsupported:[],notices:[],status:'ready' });
const api = () => ({cancel:vi.fn().mockResolvedValue({status:'cancelled'}),status:vi.fn().mockResolvedValue({query_enabled:false,available:true,provider:'https://model.example',model:'model',provider_revision:'revision'}),parse:vi.fn().mockResolvedValue(draft()),search:vi.fn().mockResolvedValue({filter:{watch_state:'unwatched',person_ids:[],tag_ids:[]},page:{videos:[{id:1,name:'match.mp4'}]},next_cursor:null})});
const wrappers=[];
async function setup(service=api()) {const applyFilter=vi.fn().mockResolvedValue(true);const w=mount(LibraryQueryDialog,{props:{api:service,applyFilter},global:{stubs:{teleport:true,SaveViewDialog:true}}});wrappers.push(w);await flushPromises();return{w,service,applyFilter};}
afterEach(()=>wrappers.splice(0).forEach(w=>w.unmount()));
it('defaults to local and needs explicit parsing and review before querying or applying', async()=>{
 const{w,service,applyFilter}=await setup();expect(service.parse).not.toHaveBeenCalled();expect(service.search).not.toHaveBeenCalled();
 expect(w.find('input[value=external]').element.disabled).toBe(true);
 await w.get('[data-test="library-query-text"]').setValue('未看');await w.get('[data-test="library-query-parse"]').trigger('click');await flushPromises();
 expect(service.parse).toHaveBeenCalledWith({request_id:expect.any(String),text:'未看',mode:'local',provider_revision:''});expect(service.search).not.toHaveBeenCalled();
 await w.get('[data-test="library-query-search"]').trigger('click');await flushPromises();expect(w.text()).toContain('match.mp4');
 await w.get('[data-test="library-query-apply"]').trigger('click');await flushPromises();expect(applyFilter).toHaveBeenCalledWith(expect.objectContaining({watch_state:'unwatched'}),expect.any(Function));expect(w.emitted('close')).toHaveLength(1);
});
it('discloses exact provider and binds the explicit external request to its revision',async()=>{
 const service=api();service.status.mockResolvedValue({query_enabled:true,available:true,provider:'https://model.example',model:'model',provider_revision:'r7'});
 const{w}=await setup(service);await w.find('input[value=external]').setValue(true);await w.get('[data-test="library-query-text"]').setValue('未看');
 expect(w.get('[data-test="library-query-disclosure"]').text()).toContain('https://model.example');expect(service.parse).not.toHaveBeenCalled();
 await w.get('[data-test="library-query-parse"]').trigger('click');await flushPromises();expect(service.parse).toHaveBeenCalledWith({request_id:expect.any(String),text:'未看',mode:'external',provider_revision:'r7'});
});
it('requires removing unsupported text, selecting a duplicate name and accepting role conversion',async()=>{
 const service=api();service.parse.mockResolvedValue({...draft(),conditions:[{condition_id:'p',kind:'person',value:'同名',label:'人物关联：同名',source_text:'导演 同名',requires_relation_acceptance:true}],unresolved:[{id:'p',reason:'同名',candidates:[{id:1,label:'同名',disambiguation:'1'},{id:2,label:'同名',disambiguation:'2'}]}],unsupported:[{id:'u',source_text:'或',reason:'不支持'}]});
 const{w}=await setup(service);await w.get('[data-test="library-query-text"]').setValue('导演 同名 或');await w.vm.parse();await flushPromises();expect(w.vm.ready).toBe(false);
 await w.find('select').setValue('2');expect(w.vm.ready).toBe(false);
 const labels=w.findAll('label');await labels.find(l=>l.text().includes('接受按人物关联')).find('input').setValue(true);expect(w.vm.ready).toBe(false);
 await labels.find(l=>l.text().includes('明确移除')).find('input').setValue(true);expect(w.vm.ready).toBe(true);
 await w.vm.search(false);expect(service.search).toHaveBeenCalledWith(expect.objectContaining({resolved_conditions:[{condition_id:'p',entity_id:2,accept_person_relation:true}],acknowledged_removals:['u']}));
});
it('keeps status arriving during typing and drops a parse after the input changes',async()=>{
 const service=api();let statusDone,parseDone;
 service.status.mockImplementation(()=>new Promise(done=>{statusDone=done}));service.parse.mockImplementation(()=>new Promise(done=>{parseDone=done}));
 const{w}=await setup(service);await w.get('[data-test="library-query-text"]').setValue('未看');statusDone({query_enabled:true,available:true,provider_revision:'r'});await flushPromises();expect(w.vm.status.query_enabled).toBe(true);
 const pending=w.vm.parse();w.vm.text='收藏';await w.vm.$nextTick();parseDone(draft());await pending;expect(w.vm.draft).toBeNull();
});

it('cancels the exact request on close and an old cancellation cannot target the next request', async()=>{
 const service=api();const completions=[];service.parse.mockImplementation(()=>new Promise(done=>completions.push(done)));
 const{w}=await setup(service);await w.get('[data-test="library-query-text"]').setValue('未看');
 const first=w.vm.parse();const oldID=w.vm.activeRequestID;await w.vm.cancelParsing();expect(service.cancel).toHaveBeenCalledWith(oldID);
 const second=w.vm.parse();const newID=w.vm.activeRequestID;expect(newID).not.toBe(oldID);
 completions[0](draft());await first;expect(w.vm.activeRequestID).toBe(newID);expect(w.vm.draft).toBeNull();
 w.vm.close();expect(service.cancel).toHaveBeenLastCalledWith(newID);completions[1](draft());await second;expect(w.vm.draft).toBeNull();
});

it('cancels an in-flight request when its host unmounts', async()=>{
 const service=api();let finish;service.parse.mockImplementation(()=>new Promise(done=>{finish=done}));
 const{w}=await setup(service);await w.get('[data-test="library-query-text"]').setValue('未看');const pending=w.vm.parse();const id=w.vm.activeRequestID;
 w.unmount();expect(service.cancel).toHaveBeenCalledWith(id);finish(draft());await pending;expect(w.emitted('close')).toBeUndefined();
});
