import { mount, flushPromises } from '@vue/test-utils';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
const api = vi.hoisted(() => ({ ReportInlineQueuePlayback: vi.fn(), GetPlaybackQueue: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);
import Player from './QueueInlinePlayer.vue';
let wrapper;
const session = () => ({ token: 'first', video_id: 1, source_version: 'v1', locator: '/preview/queue/first', start_seconds: 0, duration: 120, view_cycle: 1, control_sequence: 0, desired_paused: false });
beforeEach(() => {
  vi.clearAllMocks(); api.ReportInlineQueuePlayback.mockResolvedValue();
  vi.spyOn(HTMLMediaElement.prototype, 'play').mockResolvedValue();
  vi.spyOn(HTMLMediaElement.prototype, 'pause').mockImplementation(() => {});
  vi.spyOn(HTMLMediaElement.prototype, 'load').mockImplementation(() => {});
});
afterEach(() => { wrapper?.unmount(); wrapper = null; vi.restoreAllMocks(); });
const events = () => api.ReportInlineQueuePlayback.mock.calls.map(([event]) => event);
async function player() {
  wrapper = mount(Player, { props: { session: session() } });
  const video = wrapper.find('video'); Object.defineProperty(video.element, 'duration', { configurable: true, value: 120 });
  await video.trigger('loadedmetadata'); await video.trigger('playing'); await flushPromises(); return video;
}
it('starts at zero, sends one natural end and a fresh cycle on native replay', async () => {
  const video = await player(); expect(video.element.currentTime).toBe(0);
  video.element.currentTime = 120; await video.trigger('ended'); await video.trigger('ended'); await flushPromises();
  expect(events().filter(e => e.kind === 'ended')).toHaveLength(1);
  video.element.currentTime = 0; await video.trigger('playing'); video.element.currentTime = 120; await video.trigger('ended'); await flushPromises();
  expect(events().filter(e => e.kind === 'ended').map(e => e.view_cycle)).toEqual([1, 2]);
  expect(events().map(e => e.seq)).toEqual(events().map((_, i) => i + 1));
});
it('seeking cannot end playback and reports keep the mounted source identity', async () => {
  const video = await player(); await video.trigger('seeking'); await video.trigger('ended'); await video.trigger('pause');
  await wrapper.setProps({ session: { ...session(), token: 'later', video_id: 9, source_version: 'v9' } });
  await video.trigger('seeked'); await video.trigger('playing'); await flushPromises();
  expect(events().some(e => e.kind === 'ended')).toBe(false);
  expect(events().every(e => e.token === 'first' && e.video_id === 1 && e.source_version === 'v1')).toBe(true);
});
it('serializes bridge reports and ignores retired-player cleanup', async () => {
  let release; api.ReportInlineQueuePlayback.mockImplementationOnce(() => new Promise(resolve => { release = resolve; }));
  wrapper = mount(Player, { props: { session: session() } });
  await wrapper.find('video').trigger('loadedmetadata'); await wrapper.find('video').trigger('playing');
  expect(api.ReportInlineQueuePlayback).toHaveBeenCalledTimes(1);
  release(); await flushPromises(); expect(events().map(e => e.kind)).toEqual(['loaded', 'playing']);
  const vm = wrapper.vm; wrapper.unmount(); wrapper = null; vm.paused(); vm.ended(); await flushPromises();
  expect(events()).toHaveLength(2);
});
it('reacts to explicit pause and resume requests', async () => {
  await player(); await wrapper.setProps({ session: { ...session(), desired_paused: true, control_sequence: 1 } });
  expect(HTMLMediaElement.prototype.pause).toHaveBeenCalled();
  await wrapper.setProps({ session: { ...session(), control_sequence: 2 } }); expect(HTMLMediaElement.prototype.play).toHaveBeenCalledTimes(2);
});

it('observes repeated external intents after native controls without requiring a boolean change', async () => {
  const video = await player(); await video.trigger('pause');
  await wrapper.setProps({ session: { ...session(), control_sequence: 1 } });
  expect(HTMLMediaElement.prototype.play).toHaveBeenCalledTimes(2);
  await wrapper.setProps({ session: { ...session(), desired_paused: true, control_sequence: 2 } });
  await video.trigger('playing');
  await wrapper.setProps({ session: { ...session(), desired_paused: true, control_sequence: 3 } });
  expect(HTMLMediaElement.prototype.pause).toHaveBeenCalledTimes(2);
});

const setPaused = (video, value) => Object.defineProperty(video.element, 'paused', { configurable: true, value });
it('reports a completed seek while paused; a scrub to the end refused by the queue keeps the cycle until a played end', async () => {
  const video = await player(); setPaused(video, false);
  let playedSinceSeek = true;
  api.ReportInlineQueuePlayback.mockImplementation(async event => {
    if (event.kind === 'seeking') playedSinceSeek = false;
    if (event.kind === 'progress' || (event.kind === 'paused' && event.position > 0 && event.position < 120)) playedSinceSeek = true;
    if (event.kind === 'ended' && !playedSinceSeek) throw new Error('queue_end_ignored: 本次结束未被确认为自然结束');
  });
  video.element.currentTime = 119.9; await video.trigger('seeking');
  setPaused(video, true); video.element.currentTime = 120; await video.trigger('seeked');
  await video.trigger('pause'); await video.trigger('ended'); await flushPromises();
  expect(events().map(e => e.kind)).toEqual(['loaded', 'playing', 'seeking', 'seeked', 'paused', 'ended']);
  expect(wrapper.emitted('error')).toBeUndefined();
  setPaused(video, false); video.element.currentTime = 0; await video.trigger('playing'); await flushPromises();
  expect(events().at(-1)).toMatchObject({ kind: 'playing', view_cycle: 1 });
  playedSinceSeek = true; video.element.currentTime = 120; await video.trigger('ended'); await flushPromises();
  await video.trigger('playing'); await flushPromises();
  expect(events().at(-1)).toMatchObject({ kind: 'playing', view_cycle: 2 });
});
it('keeps the view cycle when the backend refuses the end and does not show it as an error', async () => {
  const video = await player();
  api.ReportInlineQueuePlayback.mockImplementation(async event => { if (event.kind === 'ended') throw new Error('queue_end_ignored: 本次结束未被确认为自然结束'); });
  await video.trigger('ended'); await flushPromises();
  await video.trigger('playing'); await flushPromises();
  expect(events().at(-1)).toMatchObject({ kind: 'playing', view_cycle: 1 });
  expect(wrapper.emitted('error')).toBeUndefined(); expect(wrapper.find('[data-test="queue-inline-error"]').exists()).toBe(false);
});
it('surfaces queue_changed for its own still-current token instead of swallowing it', async () => {
  const video = await player();
  api.ReportInlineQueuePlayback.mockRejectedValue(new Error('queue_changed: 待播队列已改变，请刷新后重试'));
  api.GetPlaybackQueue.mockResolvedValue({ state: { active_token: 'first' }, session: { token: 'first' } });
  await video.trigger('pause'); await flushPromises();
  expect(wrapper.emitted('error')).toHaveLength(1); expect(wrapper.find('[data-test="queue-inline-error"]').exists()).toBe(true);
  const before = events().length; await video.trigger('playing'); await flushPromises();
  expect(events()).toHaveLength(before);
});
it('ignores queue_changed once its token is no longer current', async () => {
  const video = await player();
  api.ReportInlineQueuePlayback.mockRejectedValue(new Error('queue_changed: 待播队列已改变，请刷新后重试'));
  api.GetPlaybackQueue.mockResolvedValue({ state: { active_token: 'later' }, session: { token: 'later' } });
  await video.trigger('pause'); await flushPromises();
  expect(wrapper.emitted('error')).toBeUndefined();
});
it('treats an aborted initial play as no failure but a refused play as one', async () => {
  HTMLMediaElement.prototype.play.mockRejectedValueOnce(new DOMException('interrupted by pause', 'AbortError'));
  await player(); await flushPromises();
  expect(events().some(e => e.kind === 'error')).toBe(false); expect(wrapper.emitted('error')).toBeUndefined();
  wrapper.unmount(); wrapper = null; vi.clearAllMocks(); api.ReportInlineQueuePlayback.mockResolvedValue();
  HTMLMediaElement.prototype.play.mockRejectedValueOnce(new DOMException('not allowed', 'NotAllowedError'));
  await player(); await flushPromises();
  expect(events().filter(e => e.kind === 'error')).toHaveLength(1);
});
it('stops reporting and asks for a new play when a remount of the same token is refused as stale', async () => {
  api.ReportInlineQueuePlayback.mockRejectedValue(new Error('queue_stale_event: 播放器已重新载入，请重新播放此项'));
  const video = await player(); await flushPromises();
  expect(wrapper.find('[data-test="queue-inline-error"]').exists()).toBe(true); expect(wrapper.emitted('error')).toHaveLength(1);
  expect(HTMLMediaElement.prototype.pause).toHaveBeenCalled();
  const before = events().length; await video.trigger('pause'); await video.trigger('ended'); await flushPromises();
  expect(events()).toHaveLength(before);
});
