import { h } from 'vue';
import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
const api = vi.hoisted(() => ({ StartMediaProcessing: vi.fn(), GetMediaProcessing: vi.fn(), ReleaseMediaProcessing: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);
import VideoEnhancementPreview from './VideoEnhancementPreview.vue';
let wrapper;
beforeEach(() => {
  vi.clearAllMocks();
  api.StartMediaProcessing.mockResolvedValue({ id: 'source', state: 'ready' });
  api.ReleaseMediaProcessing.mockResolvedValue(undefined);
  vi.spyOn(HTMLMediaElement.prototype, 'load').mockImplementation(() => {});
});
afterEach(() => { wrapper?.unmount(); vi.restoreAllMocks(); });
async function sample() {
  wrapper = mount(VideoEnhancementPreview, { props: { video: { id: 1, name: 'synthetic', duration: 10 }, profile: 'anime' }, global: { stubs: { Teleport: { setup(_, { slots }) { return () => h('div', slots.default?.()); } }, DialogShell: { template: '<div><slot /></div>' } } } });
  await flushPromises();
  await wrapper.setData({ result: { id: 'trial', state: 'ready', result_url: '/result.mp4', comparison_url: '/original.mp4' } });
  expect(wrapper.findAll('video'), wrapper.html()).toHaveLength(2);
  for (const video of [wrapper.vm.$refs.original, wrapper.vm.$refs.enhanced]) {
    let paused = true;
    Object.defineProperties(video, { paused: { configurable: true, get: () => paused }, readyState: { configurable: true, value: 4 }, duration: { configurable: true, value: 5 } });
    video.play = vi.fn(async () => { paused = false; }); video.pause = vi.fn(() => { paused = true; }); video.currentTime = 1;
  }
  return wrapper.vm;
}
describe('private enhancement sample synchronization', () => {
  it('pauses both sides while one buffers, avoids seeking through missing frames and resumes together', async () => {
    const vm = await sample(); await vm.play();
    const { original, enhanced } = vm.$refs;
    await wrapper.findAll('video')[1].trigger('waiting');
    expect(original.paused).toBe(true); expect(enhanced.paused).toBe(true);
    original.currentTime = 2; await wrapper.findAll('video')[0].trigger('timeupdate'); expect(enhanced.currentTime).toBe(1);
    original.currentTime = 1; await wrapper.findAll('video')[1].trigger('canplay'); await flushPromises();
    expect(original.paused).toBe(false); expect(enhanced.paused).toBe(false);
  });
  it('waits for both seek completions before resuming the same sample time', async () => {
    const vm = await sample(); await vm.play(); const { original, enhanced } = vm.$refs;
    original.currentTime = 2; await wrapper.findAll('video')[0].trigger('seeking');
    expect(enhanced.currentTime).toBe(2); expect(original.paused).toBe(true); expect(enhanced.paused).toBe(true);
    await wrapper.findAll('video')[0].trigger('seeked'); expect(original.paused).toBe(true);
    await wrapper.findAll('video')[1].trigger('seeked'); await flushPromises();
    expect(original.paused).toBe(false); expect(enhanced.paused).toBe(false);
  });
  it('stops both panes after one errors and does not resume on a late canplay', async () => {
    const vm = await sample(); await vm.play(); const { original, enhanced } = vm.$refs;
    await wrapper.findAll('video')[1].trigger('error');
    expect(original.paused).toBe(true); expect(enhanced.paused).toBe(true); expect(vm.error).toContain('增强样片');
    await wrapper.findAll('video')[1].trigger('canplay'); expect(original.paused).toBe(true);
  });
  it('rechecks the frozen source before entering the existing full enhancement flow', async () => {
    const vm = await sample(); api.GetMediaProcessing.mockRejectedValueOnce(new Error('来源已变化'));
    await vm.createFull(); expect(wrapper.emitted('create-full')).toBeUndefined(); expect(vm.error).toContain('来源已变化');
  });
});
