import { mount, flushPromises } from '@vue/test-utils';
import { expect, it, vi } from 'vitest';
import EpisodeAcquisitionPanel from './EpisodeAcquisitionPanel.vue';
const item = (extra = {}) => ({ origin_id:'o1', revision:2, task_id:'t1', collection_id:3, file_id:'f1', title:'file.mp4', state:'imported_unlinked', season:null, episode:null, can_associate:true, ...extra });
const stubs = { teleport:true, DialogShell:{ template:'<div><slot/><slot name="footer"/></div>' } };
const facade = items => ({ list:vi.fn().mockResolvedValue({ items, next_cursor:null }), preview:vi.fn().mockResolvedValue({ collection_title:'作品', items:[], conflicts:0, preview_token:'reviewed' }), confirm:vi.fn().mockResolvedValue({ linked_count:1 }) });
it('does not guess unmapped episodes and confirms only the frozen preview', async () => {
  const api = facade([item()]); const w = mount(EpisodeAcquisitionPanel,{ props:{ collectionId:3, api },global:{ stubs } }); await flushPromises();
  await w.find('input[type=checkbox]').setValue(true); await w.vm.prepare();
  expect(api.preview).not.toHaveBeenCalled(); expect(w.text()).toContain('明确填写');
  const fields = w.findAll('input[type=number]'); await fields[0].setValue(0); await fields[1].setValue(1);
  await w.vm.prepare();
  expect(api.preview).toHaveBeenCalledWith({ task_id:'t1',items:[{file_id:'f1',origin_id:'o1',expected_origin_revision:2,season:0,episode:1}] });
  await w.vm.confirm(); expect(api.confirm).toHaveBeenCalledWith('reviewed'); expect(w.emitted('associated')).toHaveLength(1); w.unmount();
});
it('keeps conflicting previews uncommittable and rejects mixed task selection', async () => {
  const api=facade([item({season:1,episode:1}),item({origin_id:'o2',task_id:'t2'})]);
  api.preview.mockResolvedValue({items:[],conflicts:1,preview_token:''});
  const w=mount(EpisodeAcquisitionPanel,{props:{collectionId:3,api},global:{stubs}});await flushPromises();
  const checks=w.findAll('input[type=checkbox]');await checks[0].setValue(true);await checks[1].setValue(true);
  expect(w.vm.selected).toEqual(['o1']);await w.vm.prepare();await w.vm.confirm();expect(api.confirm).not.toHaveBeenCalled();w.unmount();
});
it('ignores previews returned after the selected collection changed', async () => {
  const api=facade([item({season:1,episode:1})]); let resolve;
  api.preview.mockImplementation(()=>new Promise(done=>{resolve=done;}));
  const w=mount(EpisodeAcquisitionPanel,{props:{collectionId:3,api},global:{stubs}});await flushPromises();
  await w.find('input[type=checkbox]').setValue(true);const pending=w.vm.prepare();await w.setProps({collectionId:4});
  resolve({items:[],preview_token:'old',conflicts:0});await pending;expect(w.vm.preview).toBeNull();w.unmount();
});
