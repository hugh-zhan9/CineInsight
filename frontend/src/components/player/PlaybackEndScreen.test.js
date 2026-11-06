import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
const api=vi.hoisted(()=>({SuggestTonightVideos:vi.fn()}));
vi.mock('../../../wailsjs/go/main/App',()=>api);
import PlaybackEndScreen from './PlaybackEndScreen.vue';
enableAutoUnmount(afterEach);
const ending={videoID:1,sourceVersion:'v1',viewSessionID:'session',cycle:1};
const row=id=>({video:{id,name:`v${id}`,duration:100},reasons:[]});
beforeEach(()=>api.SuggestTonightVideos.mockReset().mockResolvedValue([row(2),row(3),row(4)]));
async function setup(open=vi.fn().mockResolvedValue(true)){const retire=vi.fn(),replay=vi.fn().mockResolvedValue();const w=mount(PlaybackEndScreen,{props:{ending,replay,retire},global:{provide:{openEndedVideo:open}}});await flushPromises();return {w,replay,retire,open};}
describe('PlaybackEndScreen',()=>{
 it('requests three excluding the current video and waits for user selection',async()=>{
  const {w,open,retire}=await setup();expect(api.SuggestTonightVideos).toHaveBeenCalledWith(expect.objectContaining({exclude_ids:[1]}),3);expect(open).not.toHaveBeenCalled();
  await w.findAll('[data-test=ending-choice]')[1].trigger('click');await flushPromises();expect(open).toHaveBeenCalledWith(3,expect.objectContaining({retire}));
 });
 it('keeps replay available for zero or two candidates',async()=>{
  api.SuggestTonightVideos.mockResolvedValueOnce([]);const {w,replay}=await setup();expect(w.findAll('[data-test=ending-choice]')).toHaveLength(0);
  await w.get('[data-test=ending-replay]').trigger('click');expect(replay).toHaveBeenCalledOnce();
  api.SuggestTonightVideos.mockResolvedValueOnce([row(1),row(2),row(2),row(3)]);await w.vm.load();expect(w.findAll('[data-test=ending-choice]')).toHaveLength(2);
 });
 it('keeps the ending screen on navigation failure and ignores a retired request',async()=>{
  const {w}=await setup(vi.fn().mockRejectedValue(new Error('source unavailable')));await w.get('[data-test=ending-choice]').trigger('click');await flushPromises();expect(w.text()).toContain('source unavailable');
  let complete;api.SuggestTonightVideos.mockReturnValueOnce(new Promise(resolve=>{complete=resolve}));const pending=w.vm.load();w.unmount();complete([row(9)]);await pending;
 });
 it('does not submit two choices while navigation is pending',async()=>{
  let finish;const open=vi.fn(()=>new Promise(resolve=>{finish=resolve}));const {w}=await setup(open);
  await w.get('[data-test=ending-choice]').trigger('click');await w.vm.choose(3);expect(open).toHaveBeenCalledOnce();finish(true);await flushPromises();
 });
});
