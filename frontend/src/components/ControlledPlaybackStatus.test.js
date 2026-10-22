import { mount, flushPromises } from '@vue/test-utils';
import { expect, it, vi } from 'vitest';
const api=vi.hoisted(()=>({GetControlledPlaybackState:vi.fn(),StopControlledPlayback:vi.fn()}));
vi.mock('../../wailsjs/go/main/App',()=>api);
import ControlledPlaybackStatus from './ControlledPlaybackStatus.vue';
it('keeps event state when initial and stop refreshes return late',async()=>{
 let event,resolve;window.runtime={EventsOn:vi.fn((name,handler)=>{event=handler;return ()=>{}})};
 api.GetControlledPlaybackState.mockImplementation(()=>new Promise(done=>{resolve=done}));api.StopControlledPlayback.mockResolvedValue();
 const wrapper=mount(ControlledPlaybackStatus);event({status:'playing',video_id:2});resolve({status:'stopped',video_id:1});await flushPromises();expect(wrapper.vm.state.video_id).toBe(2);
 const pending=wrapper.vm.stop();await flushPromises();event({status:'playing',video_id:3});resolve({status:'stopped',video_id:2});await pending;expect(wrapper.vm.state.video_id).toBe(3);wrapper.unmount();delete window.runtime;
});

it('keeps explicit cleanup retry available after a failed stop', async () => {
  vi.clearAllMocks();
  api.GetControlledPlaybackState.mockResolvedValueOnce({ status: 'failed', message: '专用进程尚未清理，请重试停止播放' }).mockResolvedValue({ status: 'stopped' });
  api.StopControlledPlayback.mockResolvedValue();
  const wrapper = mount(ControlledPlaybackStatus);
  await flushPromises();
  expect(wrapper.text()).toContain('操作未完成');
  expect(wrapper.text()).not.toContain('已停止');
  const retry = wrapper.findAll('button').find(button => button.text() === '重试停止');
  expect(retry).toBeTruthy();
  await retry.trigger('click');
  await flushPromises();
  expect(api.StopControlledPlayback).toHaveBeenCalledTimes(1);
  expect(wrapper.find('[role="status"]').exists()).toBe(false);
  wrapper.unmount();
});
