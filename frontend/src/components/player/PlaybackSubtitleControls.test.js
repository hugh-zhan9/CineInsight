import { mount, flushPromises } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';
const api = vi.hoisted(() => ({ GetPlaybackSubtitle: vi.fn() }));
vi.mock('../../../wailsjs/go/main/App', () => api);
import PlaybackSubtitleControls from './PlaybackSubtitleControls.vue';
const ready = text => ({ status: 'ready', cues: [{ start_ms: 1000, end_ms: 2000, text }] });
beforeEach(() => { vi.clearAllMocks(); URL.createObjectURL = vi.fn(() => 'blob:subtitle'); URL.revokeObjectURL = vi.fn(); });
describe('playback subtitles', () => {
  it('rejects late session responses and releases Blob on replacement/unmount', async () => {
    let resolveOld; api.GetPlaybackSubtitle.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve; })).mockResolvedValue(ready('new'));
    const video = document.createElement('video');
    const wrapper = mount(PlaybackSubtitleControls, { props: { videoId: 1, sourceVersion: 'old', queueToken: 'old-token', videoElement: () => video } });
    await wrapper.setProps({ videoId: 2, sourceVersion: 'new', queueToken: 'new-token' }); await flushPromises();
    expect(api.GetPlaybackSubtitle).toHaveBeenLastCalledWith({video_id:2,source_version:'new',queue_token:'new-token'});
    expect(video.querySelectorAll('track')).toHaveLength(1);
    resolveOld(ready('old')); await flushPromises(); expect(wrapper.vm.cues[0].text).toBe('new');
    expect(wrapper.find('input[type=number]').exists()).toBe(false);
    await wrapper.get('[data-test=playback-subtitle-toggle]').trigger('click');
    await wrapper.get('input[type=number]').setValue('0.5'); await flushPromises();
    expect(URL.revokeObjectURL).toHaveBeenCalled(); expect(video.querySelectorAll('track')).toHaveLength(1);
    wrapper.unmount(); expect(video.querySelectorAll('track')).toHaveLength(0);
  });
  it('reports subtitle read errors without mutating media', async () => {
    api.GetPlaybackSubtitle.mockRejectedValue(new Error('bad encoding'));const video=document.createElement('video');video.src='/movie';
    const wrapper=mount(PlaybackSubtitleControls,{props:{videoId:1,sourceVersion:'v1',videoElement:()=>video}});await flushPromises();
    await wrapper.get('[data-test=playback-subtitle-toggle]').trigger('click');
    expect(wrapper.get('[role=alert]').text()).toContain('bad encoding');expect(video.getAttribute('src')).toBe('/movie');wrapper.unmount();
  });
  it('moves rendered cues above controls and returns them to the bottom while controls are hidden', async () => {
    api.GetPlaybackSubtitle.mockResolvedValue(ready('caption'));
    const surface = document.createElement('div'); const video = document.createElement('video'); const bar = document.createElement('div');
    bar.dataset.test = 'playback-controls'; surface.append(video, bar);
    video.getBoundingClientRect = () => ({height:300}); bar.getBoundingClientRect = () => ({height:76});
    Object.defineProperty(video, 'paused', {configurable:true,writable:true,value:true});
    const wrapper = mount(PlaybackSubtitleControls, {props:{videoId:1,sourceVersion:'v1',videoElement:()=>video}}); await flushPromises();
    const cue = {startTime:1,endTime:2,text:'caption'};
    Object.defineProperty(video.querySelector('track'), 'track', {configurable:true,value:{cues:[cue],mode:'showing'}});
    video.querySelector('track').dispatchEvent(new Event('load'));
    expect(cue.snapToLines).toBe(false); expect(cue.lineAlign).toBe('end');
    expect(cue.line / 100 * 300).toBeLessThanOrEqual(300 - 76 - 10);
    video.paused = false; surface.setAttribute('userinactive',''); await flushPromises();
    expect(cue.line).toBe(95);
    video.paused = true; surface.setAttribute('mediapaused',''); await flushPromises();
    expect(cue.line).toBeLessThan(80);
    expect([cue.startTime,cue.endTime,cue.text]).toEqual([1,2,'caption']);
    wrapper.unmount();
  });

});
