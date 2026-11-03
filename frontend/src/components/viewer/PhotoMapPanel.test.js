import {mount,flushPromises,enableAutoUnmount} from '@vue/test-utils';
import {afterEach,expect,it,vi} from 'vitest';
import Panel from './PhotoMapPanel.vue';
enableAutoUnmount(afterEach);
const bounds={west:-180,south:-85,east:180,north:85};
const cluster={key:'old-cell',count:1,center_lat:0,center_lon:0,sample_image_id:1};
const makeAPI=()=>({query:vi.fn().mockResolvedValue({clusters:[cluster],matched_count:1}),list:vi.fn().mockResolvedValue({items:[],next_cursor:0}),cluster:vi.fn().mockResolvedValue({items:[],next_cursor:'',total:0}),timeline:vi.fn().mockResolvedValue({items:[],next_cursor:'',total:0})});
const stubs={PhotoMapCanvas:true,MapConnectionControls:true,PhotoLightbox:true};
it('cannot open old-scope markers after changed people fail to load',async()=>{
 const api=makeAPI();const wrapper=mount(Panel,{props:{api,people:[{id:1,name:'甲'}]},global:{stubs}});wrapper.vm.viewportChanged({bounds,zoom:0});await flushPromises();expect(wrapper.vm.mapResult.clusters).toHaveLength(1);
 api.query.mockRejectedValue(new Error('failed'));await wrapper.setProps({people:[{id:2,name:'乙'}]});await flushPromises();wrapper.vm.selectCluster(cluster);await flushPromises();expect(api.cluster).not.toHaveBeenCalled();expect(wrapper.vm.mapSnapshot).toBeNull();
});
it('clears old markers when a saved person condition is invalid',async()=>{
 const api=makeAPI();const wrapper=mount(Panel,{props:{api,people:[{id:1,name:'甲'}]},global:{stubs}});wrapper.vm.viewportChanged({bounds,zoom:0});await flushPromises();await wrapper.setData({activeRecap:{recap:{id:1,name:'已失效'},invalid_person_ids:[1]}});wrapper.vm.criteriaChanged();wrapper.vm.selectCluster(cluster);await flushPromises();expect(api.cluster).not.toHaveBeenCalled();expect(wrapper.vm.mapResult).toBeNull();
});
it('recap uses dates and people without inventing a viewport condition',async()=>{
 const api=makeAPI();const wrapper=mount(Panel,{props:{api,people:[{id:3,name:'同行人'}]},global:{stubs}});await wrapper.setData({dateFrom:'2026-11-01',dateTo:'2026-11-02'});wrapper.vm.setMode('recap');await flushPromises();expect(api.timeline).toHaveBeenLastCalledWith({selection:{person_ids:[3],date_from:'2026-11-01',date_to:'2026-11-02'},cursor:'',limit:50});
});
