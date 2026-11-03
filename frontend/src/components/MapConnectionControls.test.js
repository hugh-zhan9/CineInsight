import {mount,flushPromises,enableAutoUnmount} from '@vue/test-utils';
import {afterEach,expect,it,vi} from 'vitest';
const feedback=vi.hoisted(()=>({confirmAction:vi.fn()}));vi.mock('../utils/feedback.js',()=>feedback);
import Controls from './MapConnectionControls.vue';
enableAutoUnmount(afterEach);
it('never enables on mount and requires the shown provider disclosure before enabling',async()=>{
 const state={enabled:false,generation:'off',cache_bytes:0,budget_bytes:268435456};const api={tileStatus:vi.fn().mockResolvedValue(state),tilePolicy:vi.fn().mockResolvedValue({...state,enabled:true,generation:'on'})};const wrapper=mount(Controls,{props:{api}});await flushPromises();expect(api.tilePolicy).not.toHaveBeenCalled();feedback.confirmAction.mockResolvedValueOnce(false);await wrapper.vm.toggle();expect(api.tilePolicy).not.toHaveBeenCalled();feedback.confirmAction.mockResolvedValueOnce(true);await wrapper.vm.toggle();expect(api.tilePolicy).toHaveBeenCalledWith({enabled:true,confirmed:true,generation:'off'});expect(feedback.confirmAction.mock.calls[0][0].message).toContain('坐标清单留在本机');
});
it('allows disabling even with cache damage and ignores a late confirmation after leaving',async()=>{
 const state={enabled:true,generation:'on',cache_error:'broken',cache_bytes:0,budget_bytes:268435456};const api={tileStatus:vi.fn().mockResolvedValue(state),tilePolicy:vi.fn().mockResolvedValue({...state,enabled:false,generation:'off'})};const wrapper=mount(Controls,{props:{api}});await flushPromises();await wrapper.vm.toggle();expect(api.tilePolicy).toHaveBeenCalledWith({enabled:false,confirmed:false,generation:'on'});let answer;feedback.confirmAction.mockImplementationOnce(()=>new Promise(resolve=>{answer=resolve;}));const pending=wrapper.vm.toggle();await wrapper.setProps({pageActive:false});answer(true);await pending;expect(api.tilePolicy).toHaveBeenCalledTimes(1);
});
