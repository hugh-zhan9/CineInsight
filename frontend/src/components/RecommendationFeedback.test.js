import { mount, flushPromises } from '@vue/test-utils';
import { beforeEach, expect, it, vi } from 'vitest';
const api=vi.hoisted(()=>({SetRecommendationTagPreference:vi.fn(),SetRecommendationFeedback:vi.fn(),ListRecommendationFeedback:vi.fn(),RevokeRecommendationFeedback:vi.fn()}));
vi.mock('../../wailsjs/go/main/App',()=>api);
import RecommendationFeedback from './RecommendationFeedback.vue';
beforeEach(()=>{vi.clearAllMocks();api.SetRecommendationTagPreference.mockResolvedValue({status:'ok',data:{preference:null}});api.SetRecommendationFeedback.mockResolvedValue({});api.ListRecommendationFeedback.mockResolvedValue([]);api.RevokeRecommendationFeedback.mockResolvedValue();});
it('only downweights the explicitly selected tag and renews the reviewed row/revision',async()=>{
 const wrapper=mount(RecommendationFeedback,{props:{video:{id:7,tags:[{id:2,name:'A'},{id:3,name:'B'}]}}});
 await wrapper.get('select').setValue('3');await flushPromises();await wrapper.findAll('button').find(button=>button.text()==='减少推荐').trigger('click');await flushPromises();
 expect(api.SetRecommendationTagPreference).toHaveBeenCalledTimes(1);expect(api.SetRecommendationTagPreference).toHaveBeenCalledWith({tag_id:3,effect:'downweight',id:0,expected_revision:0});
 await wrapper.vm.renew({id:8,target_kind:'work_group',target_id:9,revision:4});
 expect(api.SetRecommendationFeedback).toHaveBeenLastCalledWith({target_kind:'work_group',target_id:9,id:8,expected_revision:4});wrapper.unmount();
});
it('switches the explicitly reviewed direction using its revision',async()=>{
 api.ListRecommendationFeedback.mockResolvedValue([{id:8,target_kind:'tag',target_id:2,effect:'downweight',revision:4}]);
 const wrapper=mount(RecommendationFeedback,{props:{video:{id:7,tags:[{id:2,name:'A'}]}}});
 await wrapper.get('select').setValue('2');await flushPromises();
 expect(wrapper.text()).toContain('当前：减少推荐');
 await wrapper.findAll('button').find(button=>button.text()==='增加推荐').trigger('click');await flushPromises();
 expect(api.SetRecommendationTagPreference).toHaveBeenCalledWith({tag_id:2,effect:'boost',id:8,expected_revision:4});wrapper.unmount();
});
