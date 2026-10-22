import { mount, flushPromises } from '@vue/test-utils';
import { beforeEach, expect, it, vi } from 'vitest';
const api=vi.hoisted(()=>({SetRecommendationFeedback:vi.fn(),ListRecommendationFeedback:vi.fn(),RevokeRecommendationFeedback:vi.fn()}));
vi.mock('../../wailsjs/go/main/App',()=>api);
import RecommendationFeedback from './RecommendationFeedback.vue';
beforeEach(()=>{vi.clearAllMocks();api.SetRecommendationFeedback.mockResolvedValue({});api.ListRecommendationFeedback.mockResolvedValue([]);api.RevokeRecommendationFeedback.mockResolvedValue();});
it('only downweights the explicitly selected tag and renews the reviewed row/revision',async()=>{
 const wrapper=mount(RecommendationFeedback,{props:{video:{id:7,tags:[{id:2,name:'A'},{id:3,name:'B'}]}}});
 await wrapper.get('select').setValue('3');await wrapper.findAll('button').find(button=>button.text()==='减少推荐').trigger('click');await flushPromises();
 expect(api.SetRecommendationFeedback).toHaveBeenCalledTimes(1);expect(api.SetRecommendationFeedback).toHaveBeenCalledWith({target_kind:'tag',target_id:3,id:0,expected_revision:0});
 await wrapper.vm.renew({id:8,target_kind:'work_group',target_id:9,revision:4});
 expect(api.SetRecommendationFeedback).toHaveBeenLastCalledWith({target_kind:'work_group',target_id:9,id:8,expected_revision:4});wrapper.unmount();
});
